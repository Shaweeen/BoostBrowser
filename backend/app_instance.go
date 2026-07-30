package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/logger"
	"boost-browser/backend/internal/proxy"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ============================================================================
// 浏览器实例管理 API
// ============================================================================

func fingerprintArgValue(args []string, prefix string) string {
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(prefix)) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return ""
}

func buildChromeUAFromFingerprintArgs(chromeVersion string, fpArgs []string) string {
	platform := strings.ToLower(fingerprintArgValue(fpArgs, "--fingerprint-platform="))
	switch platform {
	case "mac", "macos", "darwin":
		platformVersion := strings.TrimSpace(fingerprintArgValue(fpArgs, "--fingerprint-platform-version="))
		macVersion := "10_15_7"
		if platformVersion != "" {
			parts := strings.Split(platformVersion, ".")
			if len(parts) >= 2 {
				macVersion = parts[0] + "_" + parts[1] + "_0"
			} else if len(parts) == 1 {
				macVersion = parts[0] + "_0_0"
			}
		}
		return fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X %s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36", macVersion, chromeVersion)
	case "linux":
		return fmt.Sprintf("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36", chromeVersion)
	default:
		return fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36", chromeVersion)
	}
}

// extractBadgeNumberFromName 从 ProfileName 里抽出 badge 显示的数字。
// 规则：取名字里**最后一段**连续数字。
//   - "1"        → 1
//   - "11"       → 11
//   - "实例-11"  → 11
//   - "Profile 5"→ 5
//   - "abc"      → 0（无数字，调用方应回退到顺序号）
//
// 取最后一段而不是第一段：避免类似 "2024年-3号" 被错误识别成 2024。
// 数字最大保留 4 位（badge 图标渲染上限），超过的截尾。
func extractBadgeNumberFromName(name string) int {
	end := -1
	for i := len(name) - 1; i >= 0; i-- {
		c := name[i]
		if c >= '0' && c <= '9' {
			end = i
			break
		}
	}
	if end < 0 {
		return 0
	}
	start := end
	for start-1 >= 0 && name[start-1] >= '0' && name[start-1] <= '9' {
		start--
	}
	// 跳过前导零
	for start < end && name[start] == '0' {
		start++
	}
	digits := name[start : end+1]
	if len(digits) > 4 {
		digits = digits[len(digits)-4:]
	}
	n := 0
	for _, c := range []byte(digits) {
		n = n*10 + int(c-'0')
	}
	return n
}

// resolveBadgeDisplayNumber 优先使用名称中的显式数字；没有数字时按 ProfileId
// 固定排序生成序号。Go map 遍历顺序不稳定，不能用于用户可见的任务栏编号。
func resolveBadgeDisplayNumber(profileId, profileName string, profiles map[string]*browser.Profile) int {
	if number := extractBadgeNumberFromName(profileName); number > 0 {
		return number
	}
	ids := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		if profile != nil && strings.TrimSpace(profile.ProfileId) != "" {
			ids = append(ids, profile.ProfileId)
		}
	}
	sort.Strings(ids)
	for index, id := range ids {
		if id == profileId {
			return index + 1
		}
	}
	return 0
}

func (a *App) BrowserInstanceStart(profileId string) (*BrowserProfile, error) {
	return a.browserInstanceStartInternal(profileId, nil, nil, false, false, false)
}

// BrowserInstanceStartWithParams 通过额外参数启动实例（仅本次启动生效，不落库）
func (a *App) BrowserInstanceStartWithParams(profileId string, extraLaunchArgs []string, startURLs []string, skipDefaultStartURLs bool) (*BrowserProfile, error) {
	return a.browserInstanceStartInternal(profileId, extraLaunchArgs, startURLs, skipDefaultStartURLs, true, false)
}

func (a *App) browserInstanceStartInternal(profileId string, extraLaunchArgs []string, startURLs []string, skipDefaultStartURLs bool, preferVisibleWindow bool, allowRabbyImport bool) (*BrowserProfile, error) {
	log := logger.New("Browser")
	if !allowRabbyImport {
		a.rabbyImportMu.Lock()
		blocked := a.rabbyImportActive[profileId]
		a.rabbyImportMu.Unlock()
		if blocked {
			return nil, fmt.Errorf("该环境正在执行钱包批量导入，请等待完成")
		}
	}
	if err := a.browserMgr.ValidateUserDataDirOwnership(profileId); err != nil {
		startErr := fmt.Errorf("实例启动失败：%w", err)
		log.Error("环境数据目录所有权冲突", logger.F("profile_id", profileId), logger.F("reason", startErr.Error()))
		return nil, startErr
	}
	a.browserMgr.Mutex.Lock()
	managerLocked := true
	unlockManager := func() {
		if managerLocked {
			a.browserMgr.Mutex.Unlock()
			managerLocked = false
		}
	}
	defer unlockManager()

	normalizedExtraLaunchArgs := normalizeNonEmptyStrings(extraLaunchArgs)
	normalizedStartURLs := normalizeNonEmptyStrings(startURLs)
	if preferVisibleWindow {
		normalizedExtraLaunchArgs = ensureNewWindowLaunchArg(normalizedExtraLaunchArgs)
	}

	profile, exists := a.browserMgr.Profiles[profileId]
	if !exists {
		err := fmt.Errorf("实例启动失败：未找到实例配置（ID=%s）。请刷新列表后重试。", profileId)
		log.Error("实例不存在", logger.F("profile_id", profileId), logger.F("reason", err.Error()))
		return nil, err
	}
	if profile.Running {
		if !isBrowserProfileLive(profile, a.browserMgr.BrowserProcesses[profileId]) {
			log.Info("检测到实例运行状态已失效，准备重新启动",
				logger.F("profile_id", profileId),
				logger.F("pid", profile.Pid),
				logger.F("debug_port", profile.DebugPort),
			)
			a.markProfileStoppedLocked(profileId, profile)
		} else {
			if preferVisibleWindow {
				if err := a.openBrowserWindowForRunningProfile(profile, normalizedExtraLaunchArgs, normalizedStartURLs); err != nil {
					startErr := fmt.Errorf("实例已在运行，但窗口唤起失败：%w", err)
					log.Error("运行中实例窗口唤起失败",
						logger.F("profile_id", profileId),
						logger.F("debug_port", profile.DebugPort),
						logger.F("error", err.Error()),
						logger.F("reason", startErr.Error()),
					)
					profile.LastError = startErr.Error()
					return profile, startErr
				}
			}
			if a.launchServer != nil && profile.DebugReady {
				a.launchServer.SetActiveProfile(profile)
			}
			a.emitBrowserInstanceStarted(profile, true)
			return profile, nil
		}
	}
	sanitizedProfileLaunchArgs, managedProfileArgs := sanitizeManagedLaunchArgs(profile.LaunchArgs)
	sanitizedProfileLaunchArgs, managedWindowPlacementArgs := sanitizeManagedWindowPlacementArgs(sanitizedProfileLaunchArgs)
	sanitizedExtraLaunchArgs, managedExtraArgs := sanitizeManagedLaunchArgs(normalizedExtraLaunchArgs)
	logManagedLaunchArgOverrides(log, profileId, "profile.launchArgs", managedProfileArgs)
	logManagedLaunchArgOverrides(log, profileId, "profile.launchArgs.windowPlacement", managedWindowPlacementArgs)
	logManagedLaunchArgOverrides(log, profileId, "start.extraLaunchArgs", managedExtraArgs)

	profileBeforeDefaults := copyBrowserProfileSnapshot(profile)
	profileChanged := a.browserMgr.ApplyDefaults(profile)
	if profileChanged {
		if err := a.browserMgr.SaveProfiles(); err != nil {
			*profile = *profileBeforeDefaults
			startErr := fmt.Errorf("实例启动失败：保存环境身份与代理默认值失败，已恢复原配置：%w", err)
			profile.LastError = startErr.Error()
			return profile, startErr
		}
	}

	chromeBinaryPath, err := a.browserMgr.ResolveChromeBinary(profile)
	if err != nil {
		startErr := fmt.Errorf("实例启动失败：%w", err)
		log.Error("内核路径解析失败", logger.F("profile_id", profileId), logger.F("error", err.Error()), logger.F("reason", startErr.Error()))
		profile.LastError = startErr.Error()
		return profile, startErr
	}
	selectedCore := browser.Core{}
	selectedCoreFound := false
	coreId := strings.TrimSpace(profile.CoreId)
	if coreId != "" {
		selectedCore, selectedCoreFound = a.browserMgr.GetCore(coreId)
	}
	if !selectedCoreFound {
		selectedCore, selectedCoreFound = a.browserMgr.GetDefaultCore()
	}
	isCloakSelectedCore := selectedCoreFound && isCloakCore(selectedCore, chromeBinaryPath)
	effectiveFingerprintArgs := buildEffectiveFingerprintArgs(profile, selectedCore, chromeBinaryPath)

	userDataDir := a.browserMgr.ResolveUserDataDir(profile)
	if err := os.MkdirAll(userDataDir, 0755); err != nil {
		startErr := fmt.Errorf("实例启动失败：无法创建用户数据目录 %s。原因：%w。请检查目录权限或路径配置。", userDataDir, err)
		log.Error("用户数据目录创建失败", logger.F("profile_id", profileId), logger.F("dir", userDataDir), logger.F("error", err.Error()), logger.F("reason", startErr.Error()))
		profile.LastError = startErr.Error()
		return profile, startErr
	}
	// 启动前关闭 Chrome 的“恢复上次会话”，避免上次遗留的扩展 welcome/options 页面
	// 在重开实例时再次弹出。
	sanitizeChromeStartupPreferences(userDataDir)
	if err := ensureBrowserUserDataDirReadyForFreshLaunch(chromeBinaryPath, userDataDir); err != nil {
		log.Error("浏览器用户目录启动前检查失败", logger.F("profile_id", profileId), logger.F("chrome", chromeBinaryPath), logger.F("dir", userDataDir), logger.F("error", err.Error()))
		profile.LastError = err.Error()
		return profile, err
	}
	// 搜索引擎修复分两条路径：
	//   - cloak 内核：启动时禁止再做静态 Web Data/Preferences seed，避免留下
	//     dead guid / partial Google state；只允许在 debug port 就绪后走 runtime
	//     CDP settings UI 路径，这是 packaged 目标里唯一稳定不会回退成 No Search
	//     的方案。
	//   - 非 cloak：仍保留启动前静态 seed 作为兜底。
	if !isCloakSelectedCore {
		seedDefaultSearchEngine(userDataDir)
	}

	// 每次启动时合并默认书签（已存在的 URL 不重复添加）
	if err := browser.EnsureDefaultBookmarks(userDataDir, a.BookmarkList()); err != nil {
		log.Error("默认书签写入失败", logger.F("error", err.Error()))
	}

	proxies := a.getLatestProxies()
	acquiredXrayBridgeKey := ""
	releaseXrayBridge := false
	acquiredStandardRelay := false
	defer func() {
		if releaseXrayBridge && acquiredXrayBridgeKey != "" && a.xrayMgr != nil {
			a.xrayMgr.ReleaseBridge(acquiredXrayBridgeKey)
		}
		if acquiredStandardRelay && a.standardRelayMgr != nil {
			a.standardRelayMgr.Release(profileId)
		}
	}()

	// 解析实际代理配置（可能来自 proxyId 引用）
	resolvedProxyConfig := strings.TrimSpace(profile.ProxyConfig)
	if profile.ProxyId != "" {
		for _, item := range proxies {
			if strings.EqualFold(item.ProxyId, profile.ProxyId) {
				resolvedProxyConfig = strings.TrimSpace(item.ProxyConfig)
				break
			}
		}
	}
	if proxy.LooksLikeStandardProxyConfig(resolvedProxyConfig) {
		normalizedProxy, normalizeErr := proxy.NormalizeStandardProxyConfig(resolvedProxyConfig, "http")
		if normalizeErr != nil {
			startErr := fmt.Errorf("实例启动失败：代理格式无效。原因：%v", normalizeErr)
			profile.LastError = startErr.Error()
			return profile, startErr
		}
		resolvedProxyConfig = normalizedProxy
	}
	effectiveProxy := resolvedProxyConfig
	log.Info("代理配置检查",
		logger.F("profile_id", profileId),
		logger.F("proxy_id", profile.ProxyId),
		logger.F("config_present", resolvedProxyConfig != ""),
		logger.F("standard_proxy", proxy.IsStandardProxyURL(resolvedProxyConfig)),
	)
	if supported, errorMsg := proxy.ValidateProxyConfig(resolvedProxyConfig, proxies, profile.ProxyId); !supported {
		startErr := fmt.Errorf("实例启动失败：%s", errorMsg)
		profile.LastError = startErr.Error()
		log.Error("代理配置无效", logger.F("profile_id", profileId), logger.F("proxy_id", profile.ProxyId), logger.F("error", errorMsg), logger.F("reason", startErr.Error()))
		return profile, startErr
	}

	if proxy.IsSingBoxProtocol(resolvedProxyConfig) {
		// hysteria2 / tuic → sing-box 桥接
		socksURL, bridgeErr := a.singboxMgr.EnsureBridge(resolvedProxyConfig, proxies, profile.ProxyId)
		if bridgeErr != nil {
			startErr := fmt.Errorf("实例启动失败：代理桥接启动失败（sing-box）。原因：%v。请检查代理节点配置、sing-box 可执行文件是否存在，以及本地端口是否被占用。", bridgeErr)
			log.Error("代理桥接失败(sing-box)", logger.F("error", bridgeErr.Error()), logger.F("reason", startErr.Error()))
			profile.LastError = startErr.Error()
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "proxy:bridge:failed", map[string]interface{}{
					"profileId":   profileId,
					"profileName": profile.ProfileName,
					"error":       startErr.Error(),
				})
			}
			return profile, startErr
		}
		effectiveProxy = socksURL
		log.Info("sing-box 桥接成功", logger.F("socks_url", socksURL))
	} else if proxy.RequiresBridge(resolvedProxyConfig, proxies, profile.ProxyId) {
		// vmess / vless / trojan / ss → xray 桥接
		socksURL, bridgeKey, bridgeErr := a.xrayMgr.AcquireBridge(resolvedProxyConfig, proxies, profile.ProxyId)
		if bridgeErr != nil {
			startErr := fmt.Errorf("实例启动失败：代理桥接启动失败（xray）。原因：%v。请检查代理节点配置、xray 可执行文件是否存在，以及本地端口是否被占用。", bridgeErr)
			log.Error("代理桥接失败(xray)", logger.F("error", bridgeErr.Error()), logger.F("reason", startErr.Error()))
			profile.LastError = startErr.Error()
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "proxy:bridge:failed", map[string]interface{}{
					"profileId":   profileId,
					"profileName": profile.ProfileName,
					"error":       startErr.Error(),
				})
			}
			return profile, startErr
		}
		acquiredXrayBridgeKey = bridgeKey
		releaseXrayBridge = bridgeKey != ""
		effectiveProxy = socksURL
		log.Info("xray 桥接成功", logger.F("socks_url", socksURL))
	} else if proxy.IsStandardProxyURL(resolvedProxyConfig) && a.standardRelayMgr != nil {
		localProxy, relayKey, relayErr := a.standardRelayMgr.Acquire(profileId, resolvedProxyConfig, proxy.StandardProxyRouteOptions{
			Mode:            a.config.Browser.ProxyNetworkMode,
			LocalGatewayURL: a.config.Browser.LocalVPNProxy,
		})
		if relayErr != nil {
			startErr := fmt.Errorf("实例启动失败：标准代理本地转发启动失败。原因：%v。请检查代理协议、账号密码和节点可用性。", relayErr)
			log.Error("标准代理本地转发失败", logger.F("profile_id", profileId), logger.F("proxy_id", profile.ProxyId), logger.F("error", relayErr.Error()), logger.F("reason", startErr.Error()))
			profile.LastError = startErr.Error()
			return profile, startErr
		}
		if relayKey != "" {
			acquiredStandardRelay = true
			effectiveProxy = localProxy
			a.persistDetectedStandardProxy(profile.ProxyId, resolvedProxyConfig, relayKey)
			log.Info("标准代理已切换为本地转发", logger.F("profile_id", profileId), logger.F("local_proxy", localProxy))
		}
	}

	startReadyTimeout, startStableWindow := a.browserStartTimingSettings()
	maxStartAttempts := browserStartAttemptCount()
	totalReadyTimeout := time.Duration(maxStartAttempts) * startReadyTimeout
	var lastStartErr error
	assignedDebugPort, err := nextAvailablePort()
	if err != nil {
		startErr := fmt.Errorf("实例启动失败：本地调试端口分配失败。原因：%v。请关闭占用端口的程序后重试。", err)
		log.Error("调试端口分配失败", logger.F("profile_id", profileId), logger.F("error", err.Error()), logger.F("reason", startErr.Error()))
		profile.LastError = startErr.Error()
		return profile, startErr
	}

	args := []string{
		fmt.Sprintf("--user-data-dir=%s", userDataDir),
		fmt.Sprintf("--remote-debugging-port=%d", assignedDebugPort),
		"--disable-session-crashed-bubble",
		"--no-first-run",
		"--no-default-browser-check",
	}
	// 非 Cloak 内核仍保留 --search-provider-* 作为启动期兜底。
	// Cloak 路径下禁止再注入这组命令行参数：实际 packaged 目标里它会留下
	// default_search_provider 与 runtime row 不一致的 mixed state，最终仍可能
	// 回退成 No Search。Cloak 统一只走 runtime CDP seed，避免 dead guid / drift。
	args = appendDefaultSearchProviderLaunchArgs(args, !isCloakSelectedCore)

	hasFingerprint := false
	for _, arg := range effectiveFingerprintArgs {
		if strings.HasPrefix(arg, "--fingerprint=") {
			hasFingerprint = true
			break
		}
	}
	if !hasFingerprint {
		seed := 0
		for _, char := range profile.ProfileId {
			seed = (seed << 5) - seed + int(char)
		}
		if seed < 0 {
			seed = -seed
		}
		args = append(args, fmt.Sprintf("--fingerprint=%d", seed))
	}

	if effectiveProxy == "direct://" {
		// 强制直连，覆盖系统全局代理
		args = append(args, "--proxy-server=direct://")
	} else if effectiveProxy != "" {
		args = append(args, fmt.Sprintf("--proxy-server=%s", effectiveProxy))
	}
	// 从内核/指纹参数提取版本号，构造 --user-agent 参数。
	// Cloak 的 --fingerprint-brand-version 会影响 UA-CH，但 navigator.userAgent
	// 仍会保留内核默认版本；这里必须显式补 --user-agent，旧实例重启后 UA 才会不同。
	if selectedCoreFound && selectedCore.CorePath != "" {
		chromeVersion := ""
		if isCloakSelectedCore {
			chromeVersion = fingerprintArgValue(effectiveFingerprintArgs, "--fingerprint-brand-version=")
		}
		if chromeVersion == "" {
			chromeVersion = a.browserMgr.GetChromeVersion(selectedCore.CorePath)
		}
		if chromeVersion != "" {
			chromeUA := buildChromeUAFromFingerprintArgs(chromeVersion, effectiveFingerprintArgs)
			args = append(args, fmt.Sprintf("--user-agent=%s", chromeUA))
			log.Info("已设置 Chrome UA 启动参数",
				logger.F("profile_id", profileId),
				logger.F("version", chromeVersion),
				logger.F("cloak", isCloakSelectedCore),
			)

			// 跟随上游 Ant-Browser：不再强制注入内置 Header Fix 扩展。
			// 该 DNR 扩展会在 chrome://extensions/工具栏里显示成一个折叠/异常的内置扩展，
			// 且与当前内置 Google Chrome 内核自身的 UA-CH 能力重复。
		}
	}

	args = append(args, effectiveFingerprintArgs...)
	args = append(args, sanitizedProfileLaunchArgs...)
	args = append(args, sanitizedExtraLaunchArgs...)
	args = appendChromeTestingInfobarSuppressArg(args, isCloakSelectedCore)

	// cloak 路径下额外剥掉几个会暴露 chromium 身份的 launch arg：
	//   - --extension-mime-request-handling   (Chromium-only debug switch)
	//   - --disable-sync                       (经常被 anti-bot 当作 chromium 信号)
	// 它们多半来自 config.yaml 的 default_launch_args，删掉对正常使用无影响。
	if isCloakSelectedCore {
		stripPrefixes := []string{
			"--extension-mime-request-handling",
			"--disable-sync",
		}
		filtered := args[:0]
		for _, arg := range args {
			drop := false
			low := strings.ToLower(strings.TrimSpace(arg))
			for _, p := range stripPrefixes {
				if strings.EqualFold(low, p) || strings.HasPrefix(low, p+"=") {
					drop = true
					break
				}
			}
			if !drop {
				filtered = append(filtered, arg)
			}
		}
		args = filtered
	}

	if isCloakSelectedCore {
		// 默认开启 chrome://flags / extension-mime-request-handling = "Always prompt for install"。
		// 不开这个 flag，cloak 内核里从 chromewebstore.google.com 下载 .crx 不会自动弹
		// "添加扩展程序？"对话框（用户得手动拖到 chrome://extensions），开了就和普通 Chrome
		// 一样下载完直接弹安装。
		if err := ensureCloakLocalStateFlags(userDataDir); err != nil {
			logger.New("CloakFlags").Warn("写入 cloak 默认 flags 失败（不阻塞启动）",
				logger.F("profile_id", profileId),
				logger.F("user_data_dir", userDataDir),
				logger.F("error", err.Error()),
			)
		}

		// Do not inject the bundled chromium-web-store helper extension in self-use
		// builds. User requested a clean browser with no default/search helper
		// extensions visible in chrome://extensions or toolbar.
	}

	args = normalizeLoadExtensionArgs(args)
	// Wallet/content-script providers key off chrome-extension://<id>. Repair
	// missing CRX public keys in --load-extension packages before Chrome starts
	// so path-derived IDs cannot hide Local Extension Settings vault data.
	a.repairLoadExtensionStableIDs(args)
	// Final authoritative placement pass: fingerprint/profile/API arguments are
	// already appended, so stale sizes and maximised/fullscreen flags cannot win.
	args, removedWindowArgs := sanitizeManagedWindowPlacementArgs(args)
	if len(removedWindowArgs) > 0 {
		logManagedLaunchArgOverrides(log, profileId, "final.windowPlacement", removedWindowArgs)
	}
	// 浏览器内核负责自然创建唯一初始空白页。BrowserStudio 只移除旧版本遗留
	// 的位置型启动 URL，不再通过命令行创建任何默认标签。
	args = preparePrimaryEnvironmentLaunchArgs(args)
	// 等 CDP 就绪后先注入 stealth + UA override（确保 Sec-CH-UA 和 navigator.userAgentData
	// 在目标页面首次请求前就正确），然后再通过 CDP Page.navigate 导航到目标 URL。
	// 这解决了 Chrome Web Store 首次请求时 Sec-CH-UA 仍为 "Chromium" 导致
	// 显示「切换到 Chrome」横幅的问题。
	targetURLs := buildTargetURLs(profile, normalizedStartURLs, skipDefaultStartURLs)
	displayNumber := resolveBadgeDisplayNumber(profileId, profile.ProfileName, a.browserMgr.Profiles)

	cmd := exec.Command(chromeBinaryPath, args...)
	cmd.Dir = filepath.Dir(chromeBinaryPath)
	monitor, err := newBrowserProcessMonitor(cmd)
	if err != nil {
		startErr := fmt.Errorf("实例启动失败：无法建立浏览器错误输出捕获。可执行文件：%s。原因：%v。", chromeBinaryPath, err)
		log.Error("浏览器错误输出捕获初始化失败", logger.F("profile_id", profileId), logger.F("chrome", chromeBinaryPath), logger.F("error", err.Error()), logger.F("reason", startErr.Error()))
		profile.LastError = startErr.Error()
		return profile, startErr
	}
	if err := cmd.Start(); err != nil {
		startErr := fmt.Errorf("%s", describeChromeProcessStartError(chromeBinaryPath, err))
		log.Error("浏览器进程启动失败", logger.F("profile_id", profileId), logger.F("chrome", chromeBinaryPath), logger.F("error", err.Error()), logger.F("reason", startErr.Error()))
		profile.LastError = startErr.Error()
		return profile, startErr
	}
	monitor.Start()

	// Register the live process before waiting for DevTools. This is the key
	// boundary for multi-instance startup: profile preparation and process
	// creation remain serialized, while the slow debug-port readiness window and
	// independent browser readiness waits can overlap across environments.
	a.markProfileRunningLocked(profileId, profile, cmd, cmd.Process.Pid, assignedDebugPort, false, "浏览器正在启动并等待调试接口")
	if acquiredXrayBridgeKey != "" {
		a.bindProfileXrayBridge(profileId, acquiredXrayBridgeKey)
		releaseXrayBridge = false
	}
	acquiredStandardRelay = false
	profile = copyBrowserProfileSnapshot(profile)
	unlockManager()

	for attempt := 1; attempt <= maxStartAttempts; attempt++ {
		stableDebugPort, readyErr := waitBrowserDebugPortStable(assignedDebugPort, userDataDir, startReadyTimeout, startStableWindow, monitor)
		if readyErr == nil {
			readyProfile, _ := a.setProfileDebugReady(profileId, stableDebugPort)
			if readyProfile == nil {
				startErr := fmt.Errorf("实例启动已取消：环境在浏览器就绪前被关闭或删除")
				if !tryCloseBrowserViaCDP(stableDebugPort, 5*time.Second) ||
					!waitEnvironmentDataFlush(stableDebugPort, cmd.Process.Pid, userDataDir, 10*time.Second) {
					log.Error("取消启动的浏览器未通过写盘关闭确认，未执行强制终止",
						logger.F("profile_id", profileId),
						logger.F("pid", cmd.Process.Pid),
						logger.F("debug_port", stableDebugPort),
					)
				}
				return profile, startErr
			}
			profile = readyProfile

			log.Info("实例启动",
				logger.F("profile_id", profileId),
				logger.F("debug_port", stableDebugPort),
				logger.F("pid", profile.Pid),
				logger.F("proxy", effectiveProxy),
				logger.F("attempt", attempt),
				logger.F("max_attempts", maxStartAttempts),
				logger.F("args", strings.Join(args, " ")),
			)
			// 快速单次收敛启动页（v1.7.48 模型）：不阻塞多秒。延迟出现的钱包
			// 启动页由 finalize 内后台短重试处理，避免多开时串行等待。
			finalizeBrowserStartupTabs(stableDebugPort, profileId)

			// 任务栏 badge 数字直接来自实例名字里的数字段：
			//   名字 "1"        → badge 1
			//   名字 "11"       → badge 11
			//   名字 "实例-11"  → badge 11
			// 这样改名后 badge 会跟着变，不再依赖排序位置。
			// 名字里完全没数字时按 ProfileId 固定排序，确保重启前后编号一致。
			// 同步注入反检测隐身脚本 + UA 覆写（必须在导航到目标 URL 前完成，
			// 否则 Chrome Web Store 的首次请求仍会携带错误的 Sec-CH-UA）
			//
			// CloakBrowser 内核时完全跳过 wrapper 级 CDP 注入：
			//   - cloak 内核已在 C++ 源码层面修复了所有 navigator/chrome.* 字段
			//   - wrapper 再用 Page.addScriptToEvaluateOnNewDocument 重叠注入会
			//     被 Fingerprint Pro 等深度检测识别成 "Browser Tampering" 与
			//     "Bot: nodriver" 双红灯（CDP 注入痕迹是 nodriver 检测的核心信号）
			//   - 同理 Turnstile 自动点击也跳过：cloak 已经能让 CF 验证以人类
			//     身份直接通过，无需再走 CDP Input 路径制造可被识别的 trusted=false
			//     mouse event 序列
			if isCloakSelectedCore {
				log.Info("CloakBrowser 内核启用，已跳过 wrapper 级 stealth/UA/Turnstile 注入",
					logger.F("profile_id", profileId),
					logger.F("debug_port", stableDebugPort),
				)
				// Cloak 内核下只能走 runtime CDP seed。它在后台打开临时 settings tab，
				// 通过与用户手动“添加 → 设为默认”同一条 UI 路径写入 TemplateURLService。
				// settings/private API 在 brand-new profile 上常晚于 debug port 就绪，因此
				// 这里必须异步重试；成功后会写 .boost_search_seeded marker，后续启动跳过。
				go seedDefaultSearchEngineViaCDPWithRetry(userDataDir, stableDebugPort, 8, 1500*time.Millisecond)
			} else {
				if stealthErr := injectStealthToAllPagesWithUA(stableDebugPort, true); stealthErr != nil {
					log.Warn("反检测脚本注入失败（非致命）",
						logger.F("profile_id", profileId),
						logger.F("debug_port", stableDebugPort),
						logger.F("error", stealthErr.Error()),
					)
				} else {
					log.Info("反检测脚本注入成功",
						logger.F("profile_id", profileId),
						logger.F("debug_port", stableDebugPort),
					)
				}
			}

			// stealth 注入完成后，通过 CDP 导航到用户明确配置的目标 URL。
			// 默认启动页完全由浏览器内核创建，BrowserStudio 不传入默认 URL。
			//
			// CloakBrowser 内核分支只用 Target.createTarget(url) 直接打开标签页，
			// 完全跳过 Page.addScriptToEvaluateOnNewDocument + Emulation.setUserAgentOverride，
			// 否则 nodriver / Browser Tampering 检测会捕获到 CDP 注入痕迹。
			if len(targetURLs) > 0 {
				navigateToTargetURLs(stableDebugPort, targetURLs, profileId, isCloakSelectedCore)
			}

			enforceBrowserWindowBounds(profile.Pid, 1400, 600)

			// crashprobe: 临时停用实例启动后的 Turnstile 自动点击监控，继续收缩每实例后台
			// CDP 监控/注入链路，验证是否仍会出现 watchdog exit_code=2。
			// if !isCloakSelectedCore {
			// 	go startTurnstileMonitor(stableDebugPort, profileId)
			// }

			// 恢复实例数字 badge，但只走一次性异步设置；底层 setBadgeForInstance 已改成
			// 启动阶段有限重试，不再持有长期 watchdog。
			go func() {
				defer func() {
					if r := recover(); r != nil {
						logger.New("Browser").Error("badge icon goroutine panic recovered",
							logger.F("profile_id", profileId),
							logger.F("error", r),
						)
					}
				}()
				if displayNumber > 0 && profile.Pid > 0 {
					if badgeErr := setBadgeForInstance(profile.Pid, displayNumber); badgeErr != nil {
						log.Warn("任务栏 badge 图标设置失败（非致命）",
							logger.F("profile_id", profileId),
							logger.F("pid", profile.Pid),
							logger.F("display_number", displayNumber),
							logger.F("error", badgeErr.Error()),
						)
					}
				}
			}()

			a.emitBrowserInstanceStarted(profile, false)

			go func() {
				defer func() {
					if r := recover(); r != nil {
						logger.New("Browser").Error("waitBrowserProcess goroutine panic recovered",
							logger.F("profile_id", profileId),
							logger.F("error", r),
						)
					}
				}()
				a.waitBrowserProcess(profileId, monitor)
			}()
			return profile, nil
		}

		startErr := fmt.Errorf("%s", describeBrowserReadyFailure(chromeBinaryPath, assignedDebugPort, totalReadyTimeout, readyErr))
		lastStartErr = startErr
		log.Error("浏览器启动未就绪",
			logger.F("profile_id", profileId),
			logger.F("chrome", chromeBinaryPath),
			logger.F("debug_port", assignedDebugPort),
			logger.F("attempt", attempt),
			logger.F("max_attempts", maxStartAttempts),
			logger.F("error", readyErr.Error()),
			logger.F("reason", startErr.Error()),
		)

		if attempt < maxStartAttempts && shouldRetryBrowserReadyFailure(readyErr) {
			log.Warn("浏览器启动未就绪，继续检测",
				logger.F("profile_id", profileId),
				logger.F("debug_port", assignedDebugPort),
				logger.F("attempt", attempt),
				logger.F("next_attempt", attempt+1),
				logger.F("max_attempts", maxStartAttempts),
				logger.F("timeout_ms", startReadyTimeout.Milliseconds()),
			)
			continue
		}

		break
	}

	pendingStartNotice := ""
	if shouldKeepBrowserRunningPendingDebugReady(assignedDebugPort, monitor) {
		runtimeWarning := browserDebugPendingWarning(totalReadyTimeout)
		pendingStartNotice = browserDebugPendingStartNotice(totalReadyTimeout)
		a.browserMgr.Mutex.Lock()
		if current, exists := a.browserMgr.Profiles[profileId]; exists && current != nil && current.Running && current.Pid == cmd.Process.Pid {
			current.RuntimeWarning = runtimeWarning
			current.LastError = pendingStartNotice
			a.persistBrowserRuntimeSnapshotLocked()
			profile = copyBrowserProfileSnapshot(current)
		}
		a.browserMgr.Mutex.Unlock()

		log.Warn("浏览器窗口已启动，但调试接口在等待窗口内未就绪，转入后台附着",
			logger.F("profile_id", profileId),
			logger.F("debug_port", assignedDebugPort),
			logger.F("pid", profile.Pid),
			logger.F("max_attempts", maxStartAttempts),
			logger.F("warning", runtimeWarning),
		)
		a.emitBrowserInstanceStarted(profile, false)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.New("Browser").Error("waitBrowserProcess goroutine panic recovered",
						logger.F("profile_id", profileId),
						logger.F("error", r),
					)
				}
			}()
			a.waitBrowserProcess(profileId, monitor)
		}()
		go func() {
			defer func() {
				if r := recover(); r != nil {
					logger.New("Browser").Error("waitBrowserDebugReadyAsync goroutine panic recovered",
						logger.F("profile_id", profileId),
						logger.F("error", r),
					)
				}
			}()
			a.waitBrowserDebugReadyAsync(profileId, assignedDebugPort, browserAsyncDebugAttachTimeout)
		}()
	}

	if pendingStartNotice != "" {
		return profile, fmt.Errorf("%s", pendingStartNotice)
	}

	a.browserMgr.Mutex.Lock()
	if current, exists := a.browserMgr.Profiles[profileId]; exists && current != nil && current.Pid == cmd.Process.Pid {
		a.markProfileStoppedLocked(profileId, current)
		if lastStartErr != nil {
			current.LastError = lastStartErr.Error()
		}
		profile = copyBrowserProfileSnapshot(current)
	}
	a.browserMgr.Mutex.Unlock()

	if lastStartErr != nil {
		return profile, lastStartErr
	}
	return profile, fmt.Errorf("实例启动失败：浏览器在等待窗口内仍未就绪")
}

func (a *App) BrowserInstanceStop(profileId string) (*BrowserProfile, error) {
	log := logger.New("Browser")
	a.browserCloseMu.Lock()
	defer a.browserCloseMu.Unlock()

	a.rabbyImportMu.Lock()
	blocked := a.rabbyImportActive[profileId]
	a.rabbyImportMu.Unlock()
	if blocked {
		return nil, fmt.Errorf("该环境正在执行钱包批量导入，请等待完成后再关闭")
	}
	a.browserMgr.Mutex.Lock()
	profile, exists := a.browserMgr.Profiles[profileId]
	if !exists {
		a.browserMgr.Mutex.Unlock()
		return nil, fmt.Errorf("profile not found")
	}
	cmd := a.browserMgr.BrowserProcesses[profileId]
	debugPort := profile.DebugPort
	pid := profile.Pid
	if cmd != nil && cmd.Process != nil && cmd.Process.Pid > 0 {
		pid = cmd.Process.Pid
	}
	userDataDir := a.browserMgr.ResolveUserDataDir(profile)
	profileSnapshot := copyBrowserProfileSnapshot(profile)
	wasRunning := profile.Running || debugPort > 0 || pid > 0 || cmd != nil
	if !wasRunning {
		snapshot := copyBrowserProfileSnapshot(profile)
		a.browserMgr.Mutex.Unlock()
		return snapshot, nil
	}
	if debugPort <= 0 && pid <= 0 && cmd == nil && !browserSingletonArtifactsPresent(userDataDir) {
		a.markProfileStoppedLocked(profileId, profile)
		snapshot := copyBrowserProfileSnapshot(profile)
		a.browserMgr.Mutex.Unlock()
		return snapshot, nil
	}
	a.browserMgr.Mutex.Unlock()

	closeRequestedAt := time.Now()
	if err := a.browserMgr.WriteProfileDataPointer(profileSnapshot, "closing", pid, closeRequestedAt); err != nil {
		err = fmt.Errorf("环境关闭前无法保存数据指向，已取消关闭以保护 Cookie、扩展和钱包数据: %w", err)
		a.browserMgr.Mutex.Lock()
		if current := a.browserMgr.Profiles[profileId]; current != nil {
			current.LastError = err.Error()
		}
		a.browserMgr.Mutex.Unlock()
		return nil, err
	}

	method := "cdp"
	gracefulRequested := tryCloseBrowserViaCDP(debugPort, 5*time.Second)
	if !gracefulRequested {
		method = "os-soft-close"
		gracefulRequested = requestSoftProcessStopPID(pid) == nil
	}
	if !gracefulRequested || !waitEnvironmentDataFlush(debugPort, pid, userDataDir, 15*time.Second) {
		err := fmt.Errorf("环境关闭未完成写盘确认；为保护 Cookie、扩展和钱包数据，未强制终止浏览器，请稍后重试")
		a.browserMgr.Mutex.Lock()
		if current := a.browserMgr.Profiles[profileId]; current != nil {
			current.LastError = err.Error()
		}
		a.browserMgr.Mutex.Unlock()
		log.Error("实例停止失败",
			logger.F("profile_id", profileId),
			logger.F("pid", pid),
			logger.F("debug_port", debugPort),
			logger.F("reason", err.Error()),
		)
		return nil, err
	}

	closedAt := time.Now()
	pointerErr := a.browserMgr.WriteProfileDataPointer(profileSnapshot, "closed", pid, closedAt)
	a.browserMgr.Mutex.Lock()
	current, exists := a.browserMgr.Profiles[profileId]
	if !exists || current == nil {
		a.browserMgr.Mutex.Unlock()
		return nil, fmt.Errorf("profile not found")
	}
	if current.Running || current.DebugPort > 0 || current.Pid > 0 || a.browserMgr.BrowserProcesses[profileId] != nil {
		a.markProfileStoppedLocked(profileId, current)
	}
	current.LastStopAt = closedAt.Format(time.RFC3339)
	if pointerErr != nil {
		current.LastError = fmt.Sprintf("环境数据已由浏览器正常写盘，但数据指向索引保存失败: %v", pointerErr)
	}
	snapshot := copyBrowserProfileSnapshot(current)
	a.browserMgr.Mutex.Unlock()
	if pointerErr != nil {
		log.Error("环境数据已写盘但关闭索引保存失败",
			logger.F("profile_id", profileId),
			logger.F("pid", pid),
			logger.F("error", pointerErr.Error()),
		)
		return snapshot, pointerErr
	}
	log.Info("实例停止并完成数据写盘确认",
		logger.F("profile_id", profileId),
		logger.F("pid", pid),
		logger.F("method", method),
	)
	return snapshot, nil
}

func (a *App) BrowserInstanceRestart(profileId string) (*BrowserProfile, error) {
	if _, err := a.BrowserInstanceStop(profileId); err != nil {
		return nil, err
	}
	return a.BrowserInstanceStart(profileId)
}

// BrowserProfileBatchSetTags 批量为实例设置标签（追加模式：将 tags 加入已有标签；replace 模式：直接替换）
func (a *App) BrowserProfileBatchSetTags(profileIds []string, tags []string, replace bool) error {
	log := logger.New("Browser")
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()

	for _, profileId := range profileIds {
		profile, exists := a.browserMgr.Profiles[profileId]
		if !exists {
			continue
		}
		if replace {
			profile.Tags = tags
		} else {
			// 追加去重
			existing := make(map[string]struct{})
			for _, t := range profile.Tags {
				existing[t] = struct{}{}
			}
			for _, t := range tags {
				if _, ok := existing[t]; !ok {
					profile.Tags = append(profile.Tags, t)
					existing[t] = struct{}{}
				}
			}
		}
		profile.UpdatedAt = time.Now().Format(time.RFC3339)
		if a.browserMgr.ProfileDAO != nil {
			if err := a.browserMgr.ProfileDAO.Upsert(profile); err != nil {
				log.Error("批量设置标签失败", logger.F("profile_id", profileId), logger.F("error", err))
				return err
			}
		}
	}
	return nil
}

// BrowserProfileBatchRemoveTags 批量从实例移除指定标签
func (a *App) BrowserProfileBatchRemoveTags(profileIds []string, tags []string) error {
	log := logger.New("Browser")
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()

	removeSet := make(map[string]struct{})
	for _, t := range tags {
		removeSet[t] = struct{}{}
	}

	for _, profileId := range profileIds {
		profile, exists := a.browserMgr.Profiles[profileId]
		if !exists {
			continue
		}
		filtered := profile.Tags[:0]
		for _, t := range profile.Tags {
			if _, ok := removeSet[t]; !ok {
				filtered = append(filtered, t)
			}
		}
		profile.Tags = filtered
		profile.UpdatedAt = time.Now().Format(time.RFC3339)
		if a.browserMgr.ProfileDAO != nil {
			if err := a.browserMgr.ProfileDAO.Upsert(profile); err != nil {
				log.Error("批量移除标签失败", logger.F("profile_id", profileId), logger.F("error", err))
				return err
			}
		}
	}
	return nil
}

// BrowserRenameTag 重命名所有实例中的指定标签
func (a *App) BrowserRenameTag(oldName string, newName string) error {
	log := logger.New("Browser")
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" || newName == "" {
		return fmt.Errorf("标签名称不能为空")
	}

	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()

	changedCount := 0
	for profileId, profile := range a.browserMgr.Profiles {
		tagChanged := false
		var newTags []string
		for _, t := range profile.Tags {
			if strings.EqualFold(t, oldName) {
				newTags = append(newTags, newName)
				tagChanged = true
			} else {
				newTags = append(newTags, t)
			}
		}

		if tagChanged {
			// 去重
			uniqueTags := make([]string, 0)
			seen := make(map[string]struct{})
			for _, t := range newTags {
				if _, ok := seen[t]; !ok {
					uniqueTags = append(uniqueTags, t)
					seen[t] = struct{}{}
				}
			}

			profile.Tags = uniqueTags
			profile.UpdatedAt = time.Now().Format(time.RFC3339)
			if a.browserMgr.ProfileDAO != nil {
				if err := a.browserMgr.ProfileDAO.Upsert(profile); err != nil {
					log.Error("重命名标签保存失败", logger.F("profile_id", profileId), logger.F("error", err))
					return err
				}
			}
			changedCount++
		}
	}

	if changedCount > 0 && a.browserMgr.ProfileDAO == nil {
		if err := a.browserMgr.SaveProfiles(); err != nil {
			return err
		}
	}

	if changedCount > 0 {
		log.Info("重命名标签成功", logger.F("old", oldName), logger.F("new", newName), logger.F("changed_profiles", changedCount))
	}
	return nil
}

func (a *App) BrowserInstanceStatus(profileId string) (*BrowserProfile, error) {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	profile, exists := a.browserMgr.Profiles[profileId]
	if !exists {
		return nil, fmt.Errorf("profile not found")
	}
	return copyBrowserProfileSnapshot(profile), nil
}

func (a *App) BrowserInstanceOpenUrl(profileId string, targetUrl string) bool {
	a.browserMgr.Mutex.Lock()
	profile, exists := a.browserMgr.Profiles[profileId]
	a.browserMgr.Mutex.Unlock()
	if !exists || !profile.Running {
		return false
	}
	return true
}

func (a *App) BrowserInstanceGetTabs(profileId string) []BrowserTab {
	return []BrowserTab{
		{TabId: "tab-1", Title: "新标签页", Url: "about:blank", Active: true},
		{TabId: "tab-2", Title: "示例站点", Url: "https://example.com", Active: false},
	}
}

func (a *App) waitBrowserProcess(profileId string, monitor *browserProcessMonitor) {
	err := monitor.Wait()

	log := logger.New("Browser")
	debugPort := 0
	profileName := profileId
	shouldMonitorDetached := false

	a.browserMgr.Mutex.Lock()
	profile, exists := a.browserMgr.Profiles[profileId]
	wasRunning := exists && profile.Running
	if exists {
		profileName = profile.ProfileName
		debugPort = profile.DebugPort
	}
	a.browserMgr.Mutex.Unlock()

	if wasRunning && debugPort > 0 {
		snapshot, changed := a.waitForBrowserDebugReady(profileId, debugPort, browserLauncherDetachGraceWindow)
		if snapshot != nil {
			if changed {
				log.Info("浏览器启动器进程退出后，调试接口延迟就绪",
					logger.F("profile_id", profileId),
					logger.F("debug_port", debugPort),
				)
				a.emitBrowserInstanceUpdated(snapshot)
			}
		}

		a.browserMgr.Mutex.Lock()
		profile, exists = a.browserMgr.Profiles[profileId]
		if exists && profile.Running && profile.DebugPort == debugPort && profile.DebugReady && canConnectDebugPort(debugPort, 250*time.Millisecond) {
			delete(a.browserMgr.BrowserProcesses, profileId)
			profile.Pid = 0
			shouldMonitorDetached = true
		}
		a.browserMgr.Mutex.Unlock()
		if shouldMonitorDetached {
			log.Info("浏览器启动器进程已退出，切换为调试端口存活监控",
				logger.F("profile_id", profileId),
				logger.F("profile_name", profileName),
				logger.F("debug_port", debugPort),
			)
			a.waitDetachedBrowser(profileId, debugPort)
			return
		}
	}

	a.browserMgr.Mutex.Lock()
	profile, exists = a.browserMgr.Profiles[profileId]
	wasRunning = exists && profile.Running
	var closeSnapshot *BrowserProfile
	closePID := 0
	if exists {
		profileName = profile.ProfileName
		closeSnapshot = copyBrowserProfileSnapshot(profile)
		closePID = profile.Pid
	}
	a.browserMgr.Mutex.Unlock()

	// A user closing the Chromium frame directly bypasses BrowserInstanceStop.
	// The process monitor therefore records the same clean-close pointer once
	// the process and profile lock are gone. It does not read browser content.
	if wasRunning && err == nil && closeSnapshot != nil {
		a.browserCloseMu.Lock()
		a.browserMgr.Mutex.Lock()
		current := a.browserMgr.Profiles[profileId]
		stillNeedsCloseRecord := current != nil && current.Running
		a.browserMgr.Mutex.Unlock()
		dataDir := a.browserMgr.ResolveUserDataDir(closeSnapshot)
		if stillNeedsCloseRecord && waitEnvironmentDataFlush(closeSnapshot.DebugPort, closePID, dataDir, 5*time.Second) {
			closedAt := time.Now()
			if pointerErr := a.browserMgr.WriteProfileDataPointer(closeSnapshot, "closed", closePID, closedAt); pointerErr != nil {
				log.Error("用户关闭环境后数据指向保存失败",
					logger.F("profile_id", profileId),
					logger.F("error", pointerErr.Error()),
				)
			}
		}
		a.browserCloseMu.Unlock()
	}

	a.browserMgr.Mutex.Lock()
	profile, exists = a.browserMgr.Profiles[profileId]
	wasRunning = exists && profile.Running
	if exists {
		profileName = profile.ProfileName
		a.markProfileStoppedLocked(profileId, profile)
	}
	a.browserMgr.Mutex.Unlock()

	if a.ctx == nil {
		return
	}

	// 进程是正常退出（用户手动关闭）还是异常崩溃
	if wasRunning && err != nil {
		// 异常退出，推送崩溃通知
		if exists && profile != nil {
			profile.LastError = fmt.Sprintf("实例运行异常退出：%s", err.Error())
		}
		log.Error("浏览器进程异常退出", logger.F("profile_id", profileId), logger.F("profile_name", profileName), logger.F("error", err))
		runtime.EventsEmit(a.ctx, "browser:instance:crashed", map[string]interface{}{
			"profileId":   profileId,
			"profileName": profileName,
			"error":       err.Error(),
		})
	} else {
		runtime.EventsEmit(a.ctx, "browser:instance:stopped", profileId)
	}
}

func (a *App) waitDetachedBrowser(profileId string, debugPort int) {
	const (
		pollInterval = 500 * time.Millisecond
		maxMisses    = 3
	)

	log := logger.New("Browser")
	misses := 0
	for {
		if canConnectDebugPort(debugPort, 250*time.Millisecond) {
			misses = 0
			time.Sleep(pollInterval)
			continue
		}

		misses++
		if misses < maxMisses {
			time.Sleep(pollInterval)
			continue
		}

		profileName := profileId
		a.browserMgr.Mutex.Lock()
		profile, exists := a.browserMgr.Profiles[profileId]
		if !exists || !profile.Running || profile.DebugPort != debugPort {
			a.browserMgr.Mutex.Unlock()
			return
		}
		profileName = profile.ProfileName
		closeSnapshot := copyBrowserProfileSnapshot(profile)
		a.browserMgr.Mutex.Unlock()

		// A detached launcher has no process handle to wait on. The debug
		// endpoint and Chromium Singleton lock are therefore the authoritative
		// close barrier before publishing the stopped state and clean pointer.
		a.browserCloseMu.Lock()
		a.browserMgr.Mutex.Lock()
		current := a.browserMgr.Profiles[profileId]
		stillClosing := current != nil && current.Running && current.DebugPort == debugPort
		a.browserMgr.Mutex.Unlock()
		var closeErr error
		if stillClosing {
			dataDir := a.browserMgr.ResolveUserDataDir(closeSnapshot)
			if waitEnvironmentDataFlush(debugPort, 0, dataDir, 5*time.Second) {
				closeErr = a.browserMgr.WriteProfileDataPointer(closeSnapshot, "closed", 0, time.Now())
			} else {
				closeErr = fmt.Errorf("浏览器已退出，但环境数据锁未在期限内释放，未标记为完整写盘")
			}
		}

		a.browserMgr.Mutex.Lock()
		profile, exists = a.browserMgr.Profiles[profileId]
		if !exists || !profile.Running || profile.DebugPort != debugPort {
			a.browserMgr.Mutex.Unlock()
			a.browserCloseMu.Unlock()
			return
		}
		if closeErr != nil {
			profile.LastError = closeErr.Error()
		}
		a.markProfileStoppedLocked(profileId, profile)
		a.browserMgr.Mutex.Unlock()
		a.browserCloseMu.Unlock()

		log.Info("检测到浏览器调试端口关闭，实例已停止",
			logger.F("profile_id", profileId),
			logger.F("profile_name", profileName),
			logger.F("debug_port", debugPort),
			logger.F("data_flush_confirmed", closeErr == nil),
		)
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "browser:instance:stopped", profileId)
		}
		return
	}
}

func tryCloseBrowserViaCDP(debugPort int, timeout time.Duration) bool {
	if debugPort <= 0 || !canConnectDebugPort(debugPort, 200*time.Millisecond) {
		return false
	}

	_ = cdpBrowserCall(debugPort, "Browser.close", nil)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !canConnectDebugPort(debugPort, 150*time.Millisecond) {
			return true
		}
		time.Sleep(80 * time.Millisecond)
	}
	return false
}

func waitEnvironmentDataFlush(debugPort, pid int, userDataDir string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	// Require two consecutive ready samples so a transient lock disappearance
	// does not report success before Chromium finishes LevelDB flushes.
	// Poll faster than the previous fixed 150ms+150ms path once the process is
	// already exiting, without shortening the safety barrier itself.
	const readySamplesNeeded = 2
	readySamples := 0
	for time.Now().Before(deadline) {
		debugClosed := debugPort <= 0 || !canConnectDebugPort(debugPort, 120*time.Millisecond)
		processClosed := pid <= 0
		if pid > 0 {
			alive, err := isProcessAlivePID(pid)
			processClosed = err == nil && !alive
		}
		profileUnlocked := !browserSingletonArtifactsPresent(userDataDir)
		if debugClosed && processClosed && profileUnlocked {
			readySamples++
			if readySamples >= readySamplesNeeded {
				return true
			}
			time.Sleep(40 * time.Millisecond)
			continue
		}
		readySamples = 0
		// While any barrier is still open, poll a little slower to avoid hot loops.
		time.Sleep(80 * time.Millisecond)
	}
	return false
}

func normalizeNonEmptyStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		v := strings.TrimSpace(item)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func ensureNewWindowLaunchArg(args []string) []string {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), "--new-window") {
			return args
		}
	}
	return append(args, "--new-window")
}

// buildTargetURLs returns only URLs explicitly requested for this launch.
// We intentionally do not restore last tabs or open verification/ad pages by
// default; new instances should start from a clean blank page.
func buildTargetURLs(profile *BrowserProfile, startURLs []string, skipDefaultStartURLs bool) []string {
	if len(startURLs) > 0 {
		return startURLs
	}
	return nil
}

func (a *App) markProfileStoppedLocked(profileId string, profile *BrowserProfile) {
	if profile == nil {
		return
	}

	profile.Running = false
	profile.DebugReady = false
	profile.Pid = 0
	profile.DebugPort = 0
	profile.RuntimeWarning = ""
	profile.LastStopAt = time.Now().Format(time.RFC3339)
	delete(a.browserMgr.BrowserProcesses, profileId)
	a.releaseProfileXrayBridge(profileId)
	a.releaseProfileStandardRelay(profileId)
	if a.launchServer != nil {
		a.launchServer.ClearActiveProfile(profileId)
	}
	a.persistBrowserRuntimeSnapshotLocked()
	// Async: this helper may already hold browserMgr.Mutex.
	a.scheduleEnvironmentPopupConfinementRefresh()
}

func (a *App) openBrowserWindowForRunningProfile(profile *BrowserProfile, extraLaunchArgs []string, startURLs []string) error {
	chromeBinaryPath, err := a.browserMgr.ResolveChromeBinary(profile)
	if err != nil {
		return err
	}

	userDataDir := a.browserMgr.ResolveUserDataDir(profile)
	if err := os.MkdirAll(userDataDir, 0755); err != nil {
		return fmt.Errorf("无法创建用户数据目录 %s：%w", userDataDir, err)
	}

	args := []string{
		fmt.Sprintf("--user-data-dir=%s", userDataDir),
	}

	// 跟随上游 Ant-Browser：已运行实例打开新窗口时也不再注入 Header Fix 扩展。
	coreId := strings.TrimSpace(profile.CoreId)
	var core browser.Core
	var coreFound bool
	if coreId != "" {
		core, coreFound = a.browserMgr.GetCore(coreId)
	}
	if !coreFound {
		core, coreFound = a.browserMgr.GetDefaultCore()
	}
	isCloakCoreForOpen := coreFound && isCloakCore(core, chromeBinaryPath)
	if coreFound && core.CorePath != "" && !isCloakCoreForOpen {
		// CloakBrowser 内核自身按 --fingerprint seed 生成 UA/UA-CH，避免 wrapper 强制覆写引发 Browser Tampering。
		chromeVersion := a.browserMgr.GetChromeVersion(core.CorePath)
		if chromeVersion != "" {
			chromeUA := fmt.Sprintf(
				"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36",
				chromeVersion,
			)
			args = append(args, fmt.Sprintf("--user-agent=%s", chromeUA))
		}
	}

	sanitizedExtraLaunchArgs, managedExtraArgs := sanitizeManagedLaunchArgs(extraLaunchArgs)
	logManagedLaunchArgOverrides(logger.New("Browser"), profile.ProfileId, "running-window.extraLaunchArgs", managedExtraArgs)
	args = append(args, sanitizedExtraLaunchArgs...)
	args = appendChromeTestingInfobarSuppressArg(args, isCloakCoreForOpen)
	args = normalizeLoadExtensionArgs(args)
	if len(startURLs) > 0 {
		args = append(args, startURLs...)
	}

	cmd := exec.Command(chromeBinaryPath, args...)
	cmd.Dir = filepath.Dir(chromeBinaryPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s", describeChromeProcessStartError(chromeBinaryPath, err))
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.New("Browser").Error("browser cmd.Wait goroutine panic recovered",
					logger.F("error", r),
				)
			}
		}()
		_ = cmd.Wait()
	}()
	return nil
}

// navigateToTargetURLs 通过 CDP 将浏览器导航到目标 URL。
//
// 非 Cloak 内核（ungoogled-chromium 等）：
//
//	先创建空白标签页 → 注入 UA override + stealth JS（确保 Sec-CH-UA 在首次请求前就正确）
//	→ 再用 Page.navigate 导航到真实 URL。
//	这解决了 Chrome Web Store 检测 Sec-CH-UA 为 "Chromium" 而非 "Google Chrome"
//	导致显示「切换到 Chrome」横幅的问题。
//
// Cloak 内核（cloakOnly=true）：
//
//	只用 Target.createTarget(url) 直接打开目标 URL 的新标签页。完全不走
//	Page.addScriptToEvaluateOnNewDocument / Emulation.setUserAgentOverride，
//	因为：
//	1. cloak 已在 C++ 源码层面处理 UA / Sec-CH-UA / navigator.* 字段；
//	2. wrapper 端再次 CDP 注入会被 Fingerprint Pro 等检测识别成
//	   Browser Tampering / Bot: nodriver 双红灯。
func navigateToTargetURLs(debugPort int, urls []string, profileId string, cloakOnly bool) {
	log := logger.New("Browser")
	if len(urls) == 0 {
		return
	}

	// 获取 browser target 的 WebSocket URL（用于 Target.createTarget）
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		log.Warn("CDP 导航：获取浏览器 WebSocket 失败",
			logger.F("profile_id", profileId),
			logger.F("error", err.Error()),
		)
		return
	}

	browserConn, _, err := websocket.DefaultDialer.Dial(browserWsURL, nil)
	if err != nil {
		log.Warn("CDP 导航：浏览器 WebSocket 连接失败",
			logger.F("profile_id", profileId),
			logger.F("error", err.Error()),
		)
		return
	}
	browserConn.SetReadDeadline(time.Now().Add(15 * time.Second))

	// Cloak 内核：直接 Target.createTarget(url)，不再注入任何 CDP 脚本
	if cloakOnly {
		for i, url := range urls {
			createMsg := cdpMessage{
				Id:     i + 200,
				Method: "Target.createTarget",
				Params: map[string]any{"url": url},
			}
			if err := browserConn.WriteJSON(createMsg); err != nil {
				log.Warn("CDP 导航(cloak)：Target.createTarget 写入失败",
					logger.F("profile_id", profileId),
					logger.F("url", url),
					logger.F("error", err.Error()),
				)
				continue
			}
			browserConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var resp cdpResponse
			_ = browserConn.ReadJSON(&resp)
			log.Info("CDP 导航(cloak)：目标页面已打开（无 stealth/UA 注入）",
				logger.F("profile_id", profileId),
				logger.F("url", url),
			)
		}
		browserConn.Close()
		return
	}

	// 获取 UA 覆写参数（将 Chromium 替换为 Chrome）—— 仅非 cloak 路径需要
	fixedUA, uaMetadata, uaErr := getUserAgentOverride(debugPort)
	if uaErr != nil {
		log.Warn("CDP 导航：获取 UA 覆写参数失败，将直接导航（可能导致 Chrome Web Store 检测异常）",
			logger.F("profile_id", profileId),
			logger.F("error", uaErr.Error()),
		)
	}

	// 逐个创建标签页：先 about:blank → 注入 → 再导航
	for i, url := range urls {
		targetId, createErr := createBlankTab(browserConn, i+1)
		if createErr != nil {
			log.Warn("CDP 导航：创建空白标签页失败，回退到直接导航",
				logger.F("profile_id", profileId),
				logger.F("url", url),
				logger.F("error", createErr.Error()),
			)
			// 回退：直接用 Target.createTarget(url) 创建
			fallbackMsg := cdpMessage{
				Id:     i + 100,
				Method: "Target.createTarget",
				Params: map[string]any{"url": url},
			}
			_ = browserConn.WriteJSON(fallbackMsg)
			var fallbackResp cdpResponse
			_ = browserConn.ReadJSON(&fallbackResp)
			continue
		}

		// 获取新标签页的 WebSocket URL
		targetWsURL := fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/%s", debugPort, targetId)

		// 连接到新标签页并注入 UA override + stealth JS
		pageConn, _, dialErr := websocket.DefaultDialer.Dial(targetWsURL, nil)
		if dialErr != nil {
			log.Warn("CDP 导航：连接新标签页失败，回退到直接导航",
				logger.F("profile_id", profileId),
				logger.F("url", url),
				logger.F("targetId", targetId),
				logger.F("error", dialErr.Error()),
			)
			continue
		}

		injectSuccess := false

		// 注入 stealth JS（Page.addScriptToEvaluateOnNewDocument）
		stealthMsg := cdpMessage{
			Id:     1,
			Method: "Page.addScriptToEvaluateOnNewDocument",
			Params: map[string]any{
				"source": stealthJS,
			},
		}
		if writeErr := pageConn.WriteJSON(stealthMsg); writeErr == nil {
			pageConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var stealthResp cdpResponse
			_ = pageConn.ReadJSON(&stealthResp)
		}

		// 注入 UA override（Emulation.setUserAgentOverride）
		if fixedUA != "" && uaMetadata != nil {
			uaMsg := cdpMessage{
				Id:     2,
				Method: "Emulation.setUserAgentOverride",
				Params: map[string]any{
					"userAgent":         fixedUA,
					"platform":          "Win32",
					"userAgentMetadata": uaMetadata,
				},
			}
			if writeErr := pageConn.WriteJSON(uaMsg); writeErr == nil {
				pageConn.SetReadDeadline(time.Now().Add(5 * time.Second))
				var uaResp cdpResponse
				_ = pageConn.ReadJSON(&uaResp)
				injectSuccess = true
				log.Info("CDP 导航：UA override 注入成功",
					logger.F("profile_id", profileId),
					logger.F("targetId", targetId),
				)
			}
		} else {
			injectSuccess = true // 没有 UA override 也继续导航
		}

		if !injectSuccess {
			log.Warn("CDP 导航：UA override 注入失败，继续导航（可能触发 Chrome Web Store 横幅检测）",
				logger.F("profile_id", profileId),
				logger.F("targetId", targetId),
			)
		}

		// 导航到目标 URL
		navMsg := cdpMessage{
			Id:     3,
			Method: "Page.navigate",
			Params: map[string]any{
				"url": url,
			},
		}
		if navErr := pageConn.WriteJSON(navMsg); navErr != nil {
			log.Warn("CDP 导航：Page.navigate 失败",
				logger.F("profile_id", profileId),
				logger.F("url", url),
				logger.F("error", navErr.Error()),
			)
		} else {
			pageConn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var navResp cdpResponse
			_ = pageConn.ReadJSON(&navResp)
			log.Info("CDP 导航：目标页面已导航",
				logger.F("profile_id", profileId),
				logger.F("url", url),
				logger.F("targetId", targetId),
			)
		}
		pageConn.Close()
	}
	browserConn.Close()
}

// createBlankTab 通过 CDP Target.createTarget 创建一个 about:blank 空白标签页，
// 并返回新标签页的 targetId 用于后续注入和导航。
func createBlankTab(browserConn *websocket.Conn, msgId int) (string, error) {
	createMsg := cdpMessage{
		Id:     msgId,
		Method: "Target.createTarget",
		Params: map[string]any{
			"url": "about:blank",
		},
	}
	if err := browserConn.WriteJSON(createMsg); err != nil {
		return "", fmt.Errorf("发送 Target.createTarget 失败: %w", err)
	}

	var resp cdpResponse
	if err := browserConn.ReadJSON(&resp); err != nil {
		return "", fmt.Errorf("读取 Target.createTarget 响应失败: %w", err)
	}
	if resp.Error != nil {
		return "", fmt.Errorf("Target.createTarget 错误: %s", resp.Error.Message)
	}

	// 从 result 中提取 targetId
	result := resp.Result
	if result == nil {
		return "", fmt.Errorf("Target.createTarget 返回空 result")
	}
	targetId, ok := result["targetId"].(string)
	if !ok || targetId == "" {
		return "", fmt.Errorf("Target.createTarget 未返回有效的 targetId")
	}
	return targetId, nil
}
