package backend

import (
	"path/filepath"
	"strings"
	"time"

	"boost-browser/backend/internal/logger"
)

// 扩展启动自动页清理（Extension startup auto-page cleanup）
//
// 背景：环境通过 --load-extension 注入 unpacked 扩展时，Chrome 会把该扩展
// 视为“新安装”，触发 chrome.runtime.onInstalled(install)，钱包类扩展的
// background 脚本随即 chrome.tabs.create 打开欢迎页/解锁页/通知页。若 CLI
// 注入在每次启动重复发生（注册缺失、ID 解析失败、首次适配未完成），用户
// 每次打开环境都会看到扩展主页标签，即使钱包数据已经导入。
//
// 两层修复（都只作用于“已分配”扩展，绝不触碰 http(s) 工作标签）：
//  1. 启动前补写 Scheme A（Preferences unpacked 注册）：Chrome 从 profile
//     加载扩展，不再把它当作 CLI 新装 → 不触发 onInstalled(install) 弹页。
//  2. 启动后短窗口 CDP 清扫：兜底关闭已分配扩展自动打开的
//     chrome-extension:// 页面，保证环境保持单一空白初始页。
//
// 钱包批量导入启动（allowRabbyImport）需要扩展页面完成导入，两层都跳过。

// registerAssignedExtensionsIntoProfile 对仍需要 CLI 注入的包，先尝试写入
// Preferences unpacked 注册（Scheme A）。注册成功且 Chrome 从 profile 加载后，
// 不再每次触发 onInstalled(install)，从根上消除扩展欢迎页。返回新注册数量。
// 仅在 profile 未运行（冷启动）时由启动路径调用；manifest 无效或非 Web Store
// ID 的包跳过（无法预置注册，仍走 CLI 首次适配 + 启动后清扫兜底）。
func (a *App) registerAssignedExtensionsIntoProfile(userDataDir string, needingInject []string) int {
	if a == nil || strings.TrimSpace(userDataDir) == "" || len(needingInject) == 0 {
		return 0
	}
	log := logger.New("Extension")
	registered := 0
	for _, packageDir := range needingInject {
		packageDir = strings.TrimSpace(packageDir)
		if packageDir == "" || validateUnpackedExtensionManifest(packageDir) != nil {
			continue
		}
		// 已注册的包跳过：幂等（Profile 注册已存在时不能重复计数/重复写入）。
		if canSkipLoadExtensionCLI(userDataDir, packageDir) {
			continue
		}
		if !isWebStoreExtensionID(resolveExtensionPackageID(packageDir)) {
			// 目录名/公钥推导不出稳定 Web Store ID 的包无法预置注册。
			continue
		}
		if err := installUnpackedExtensionIntoProfile(userDataDir, packageDir); err != nil {
			log.Warn("启动前补写扩展注册失败，仍走 CLI 首次适配",
				logger.F("package", packageDir),
				logger.F("error", err.Error()),
			)
			continue
		}
		registered++
	}
	if registered > 0 {
		log.Info("启动前已补写扩展 Profile 注册（方案 A），本次不再 CLI 注入，避免每次启动弹扩展主页",
			logger.F("registered", registered),
		)
	}
	return registered
}

// assignedExtensionIDSet 收集当前分配清单里所有扩展的稳定 ID。
// 优先用 manifest 公钥推导的 Web Store ID；无 key 时退回目录名（与
// resolveExtensionPackageID 一致），保证 chrome-extension:// 前缀能匹配。
func assignedExtensionIDSet(launchArgs []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, dir := range activeLoadExtensionDirs(launchArgs) {
		id := resolveExtensionPackageID(dir)
		if id == "" {
			id = strings.ToLower(filepath.Base(strings.TrimSpace(dir)))
		}
		if id != "" {
			out[id] = struct{}{}
		}
	}
	return out
}

// extensionIDFromPageURL 从 chrome-extension://<id>/... 页面 URL 提取扩展 ID。
func extensionIDFromPageURL(rawURL string) string {
	rawURL = strings.ToLower(strings.TrimSpace(rawURL))
	const prefix = "chrome-extension://"
	if !strings.HasPrefix(rawURL, prefix) {
		return ""
	}
	rest := rawURL[len(prefix):]
	if idx := strings.IndexByte(rest, '/'); idx >= 0 {
		rest = rest[:idx]
	}
	if idx := strings.IndexByte(rest, '?'); idx >= 0 {
		rest = rest[:idx]
	}
	if idx := strings.IndexByte(rest, '#'); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

// closeAssignedExtensionAutoPagesAfterStart 在环境启动后的短窗口内，关闭已
// 分配扩展自动打开的 chrome-extension:// 页面（onInstalled/onStartup 触发）。
// 只关闭属于本次分配清单的扩展页面，绝不关闭 http(s) 工作标签或其他扩展。
// 有界重试：慢启动/多开冷启动时扩展页面可能晚几秒才弹出，单次清扫会漏，
// 因此做至多 3 次递增延时清扫（总计约 8s），到点即终止，绝不成为常驻后台
// worker。用户稍后手动打开的扩展页面不在窗口内，不受影响。
func closeAssignedExtensionAutoPagesAfterStart(debugPort int, launchArgs []string) {
	defer func() { _ = recover() }()
	if debugPort <= 0 {
		return
	}
	assignedIDs := assignedExtensionIDSet(launchArgs)
	if len(assignedIDs) == 0 {
		return
	}
	// 给扩展加载与自动弹页留出时间；窗口太短会导致清扫时页面还没创建。
	delays := []time.Duration{
		2500 * time.Millisecond,
		2500 * time.Millisecond,
		3000 * time.Millisecond,
	}
	for i, delay := range delays {
		time.Sleep(delay)
		closed := sweepAssignedExtensionAutoPages(debugPort, assignedIDs)
		if closed > 0 {
			logger.New("Extension").Info("已关闭扩展启动自动打开的页面，保持环境空白初始页",
				logger.F("debug_port", debugPort),
				logger.F("closed", closed),
				logger.F("sweep", i+1),
			)
		}
	}
}

// sweepAssignedExtensionAutoPages 连接一次调试端口，关闭所有已分配扩展自动
// 打开的 chrome-extension:// 页面，返回关闭数量。失败静默返回 0（下一轮
// 清扫会重试）。
func sweepAssignedExtensionAutoPages(debugPort int, assignedIDs map[string]struct{}) int {
	if debugPort <= 0 || len(assignedIDs) == 0 {
		return 0
	}
	browserWS, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return 0
	}
	client, err := newRabbyCDPClient(browserWS)
	if err != nil {
		return 0
	}
	defer client.close()

	targets, err := listCDPTargets(debugPort)
	if err != nil {
		return 0
	}
	closed := 0
	for _, target := range assignedExtensionPageTargetsToClose(targets, assignedIDs) {
		if _, err := client.call("Target.closeTarget", map[string]any{"targetId": target.ID}, 3*time.Second); err == nil {
			closed++
		}
	}
	return closed
}

// assignedExtensionPageTargetsToClose 纯过滤函数：只保留“属于已分配扩展的
// chrome-extension:// page 目标”。http(s) 工作标签、DevTools、Service
// Worker/background 页、其他扩展的页面一律排除——这是“绝不关闭用户工作
// 标签”的核心保证，独立成函数以便单测守卫。
func assignedExtensionPageTargetsToClose(targets []cdpTarget, assignedIDs map[string]struct{}) []cdpTarget {
	var out []cdpTarget
	for _, target := range targets {
		if !strings.EqualFold(strings.TrimSpace(target.Type), "page") {
			continue
		}
		id := extensionIDFromPageURL(target.URL)
		if id == "" {
			continue
		}
		if _, ok := assignedIDs[id]; !ok {
			continue // 非本次分配的扩展，绝不关闭
		}
		out = append(out, target)
	}
	return out
}
