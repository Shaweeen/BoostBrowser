package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"boost-browser/backend/internal/logger"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// =============================================================================
// 方案 A：纯 GitHub Releases 自动升级
//
// 发版流程（你只需做这件事）：
//   1. 升 wails.json 里 productVersion，比如 1.1.0 → 1.1.1
//   2. wails build -clean -platform windows/amd64
//   3. 计算 sha256: certutil -hashfile boost-browser.exe SHA256
//   4. 在 GitHub 上 Create new release，tag 填 v1.1.1
//   5. 上传两个文件作为 release assets：
//        - boost-browser.exe              (主程序)
//        - boost-browser.exe.sha256       (纯文本，里面就一行 sha256 hash)
//   6. Publish release，结束
//
// 客户端流程（自动）：
//   启动 5s 后 GET https://api.github.com/repos/{owner}/{repo}/releases/latest
//     → 比较版本号
//     → 有新版 → 弹 Dialog
//     → 用户确认 → 流式下载 + sha256 校验
//     → 启动 updater.exe → 主程序退出 → updater 替换 exe → 启动新版
// =============================================================================

const (
	// GitHub 仓库：https://github.com/Shaweeen/BoostBrowser
	githubOwner = "Shaweeen"
	githubRepo  = "BoostBrowser"

	updateDirName  = "updates"
	updaterExeName = "updater.exe"
	successMarker  = ".update_success"
)

func normalizeUpdateSHA256(value string) (string, error) {
	hash := strings.ToLower(strings.TrimSpace(value))
	if len(hash) != sha256.Size*2 {
		return "", fmt.Errorf("SHA256 格式无效")
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", fmt.Errorf("SHA256 格式无效")
	}
	return hash, nil
}

// validateUpdateAssetURL confines executable downloads to the stable public
// release channel. The source repository is private, while existing clients
// intentionally keep using Shaweeen/BoostBrowser for backward compatibility.
func validateUpdateAssetURL(rawURL, assetName string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return nil, fmt.Errorf("更新地址不是受信任的 GitHub Release HTTPS 地址")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("更新地址包含不允许的认证或查询参数")
	}
	prefix := fmt.Sprintf("/%s/%s/releases/download/", githubOwner, githubRepo)
	path := parsed.EscapedPath()
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/"+assetName) {
		return nil, fmt.Errorf("更新地址不属于 BrowserStudio 稳定发行通道")
	}
	tag := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/"+assetName)
	if tag == "" || strings.Contains(tag, "/") || tag == "." || tag == ".." {
		return nil, fmt.Errorf("更新地址中的版本标签无效")
	}
	return parsed, nil
}

func isWindowsPEFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	header := []byte{0, 0}
	_, err = io.ReadFull(f, header)
	return err == nil && header[0] == 'M' && header[1] == 'Z'
}

// githubReleaseAPI 实时查询 GitHub Releases
func githubReleaseAPI() string {
	return fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubOwner, githubRepo)
}

// githubLatestReleasePage 不走 GitHub API；/releases/latest 会 302 到最新 tag。
func githubLatestReleasePage() string {
	return fmt.Sprintf("https://github.com/%s/%s/releases/latest", githubOwner, githubRepo)
}

func githubReleaseAssetURL(tag, assetName string) string {
	return fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s", githubOwner, githubRepo, tag, assetName)
}

// UpdateCheckResult 给前端的检查结果
type UpdateCheckResult struct {
	HasUpdate    bool   `json:"hasUpdate"`
	Current      string `json:"current"`
	Latest       string `json:"latest"`
	Force        bool   `json:"force"`
	ReleaseNotes string `json:"releaseNotes"`
	URL          string `json:"url"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
}

type ghAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Name       string    `json:"name"`
	Body       string    `json:"body"`
	Prerelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
	Assets     []ghAsset `json:"assets"`
}

type updateNetworkRoute struct {
	name     string
	proxyURL string
}

func (a *App) updateNetworkRoutes() []updateNetworkRoute {
	raw := []updateNetworkRoute{}
	if a != nil && a.config != nil {
		raw = append(raw, updateNetworkRoute{name: "客户端代理", proxyURL: strings.TrimSpace(a.config.Browser.LocalVPNProxy)})
	}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			raw = append(raw, updateNetworkRoute{name: "环境变量 " + key, proxyURL: value})
		}
	}
	if value := strings.TrimSpace(readWindowsSystemProxy()); value != "" {
		raw = append(raw, updateNetworkRoute{name: "Windows 系统代理", proxyURL: value})
	}
	raw = append(raw, updateNetworkRoute{name: "直连"})

	seen := map[string]bool{}
	routes := make([]updateNetworkRoute, 0, len(raw))
	for _, route := range raw {
		key := strings.ToLower(strings.TrimSpace(route.proxyURL))
		if key == "" {
			key = "<direct>"
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		routes = append(routes, route)
	}
	return routes
}

func (a *App) updateDownloadClients(timeout time.Duration) []struct {
	name   string
	client *http.Client
} {
	routes := a.updateNetworkRoutes()
	clients := make([]struct {
		name   string
		client *http.Client
	}, 0, len(routes))
	for _, route := range routes {
		clients = append(clients, struct {
			name   string
			client *http.Client
		}{
			name:   route.name,
			client: newPublicRemoteHTTPClientWithProxyPolicy(timeout, false, route.proxyURL, false),
		})
	}
	return clients
}

// updateHTTPClient uses the same public-remote policy as extension downloads:
// honour HTTP(S)_PROXY / ALL_PROXY and optional LocalVPNProxy so checks work
// behind Clash/Nym. Destinations stay restricted to public hosts.
func (a *App) updateHTTPClient(timeout time.Duration) *http.Client {
	proxyURL := ""
	if a != nil && a.config != nil {
		proxyURL = strings.TrimSpace(a.config.Browser.LocalVPNProxy)
	}
	return newPublicRemoteHTTPClientWithProxy(timeout, false, proxyURL)
}

// CheckUpdate Wails binding：用户点检查更新 / 启动后自动调用
func (a *App) CheckUpdate() (*UpdateCheckResult, error) {
	log := logger.New("Updater")
	current := a.appVersion()

	// 45s: GitHub from China is slow even via local proxy; 15s caused false timeouts.
	client := a.updateHTTPClient(45 * time.Second)
	rel, err := fetchLatestReleaseWithFallback(client, githubReleaseAPI(), githubLatestReleasePage())
	if err != nil {
		log.Info("更新信息获取失败", logger.F("error", err.Error()))
		return nil, fmt.Errorf("%w（请开启系统代理/本机转发网关，或到 GitHub Releases 手动下载）", err)
	}
	if rel.Draft || rel.Prerelease {
		log.Info("最新 release 是草稿或预发布，跳过", logger.F("tag", rel.TagName))
		return &UpdateCheckResult{HasUpdate: false, Current: current, Latest: current}, nil
	}

	latest := strings.TrimPrefix(rel.TagName, "v")
	var exeURL, sha256URL string
	var exeSize int64
	for _, asset := range rel.Assets {
		switch asset.Name {
		case "boost-browser.exe":
			exeURL = asset.BrowserDownloadURL
			exeSize = asset.Size
		case "boost-browser.exe.sha256":
			sha256URL = asset.BrowserDownloadURL
		}
	}
	if exeURL == "" {
		return nil, fmt.Errorf("release %s 缺少 boost-browser.exe asset", rel.TagName)
	}
	if sha256URL == "" {
		return nil, fmt.Errorf("release %s 缺少 boost-browser.exe.sha256 asset", rel.TagName)
	}
	if _, err := validateUpdateAssetURL(exeURL, "boost-browser.exe"); err != nil {
		return nil, err
	}

	// 拉 sha256 文件内容（同一代理客户端）
	sha256Hex, err := fetchSHA256AssetWithClient(client, sha256URL)
	if err != nil {
		return nil, fmt.Errorf("sha256 文件下载失败：%w", err)
	}

	// release notes 里可以写 [force] 触发强制升级
	force := strings.Contains(strings.ToLower(rel.Body), "[force]")

	hasUpdate := compareVersion(latest, current) > 0
	log.Info("更新检查完成",
		logger.F("current", current),
		logger.F("latest", latest),
		logger.F("has_update", hasUpdate),
		logger.F("force", force),
	)

	return &UpdateCheckResult{
		HasUpdate:    hasUpdate,
		Current:      current,
		Latest:       latest,
		Force:        force,
		ReleaseNotes: rel.Body,
		URL:          exeURL,
		SHA256:       sha256Hex,
		Size:         exeSize,
	}, nil
}

func fetchLatestReleaseWithFallback(client *http.Client, apiURL, latestPageURL string) (*ghRelease, error) {
	// The public /releases/latest redirect does not consume GitHub API quota.
	// Prefer it for installed clients, which can otherwise exhaust the shared
	// unauthenticated API allowance when many environments start together.
	rel, err := fetchLatestReleaseFromRedirect(client, latestPageURL)
	if err == nil {
		return rel, nil
	}
	fallback, fallbackErr := fetchLatestReleaseFromAPI(client, apiURL)
	if fallbackErr != nil {
		return nil, fmt.Errorf("GitHub Release 页面和 API 均不可用（请检查系统代理/本机转发网关）：页面=%v；API=%w", err, fallbackErr)
	}
	return fallback, nil
}

func fetchLatestReleaseFromAPI(client *http.Client, apiURL string) (*ghRelease, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "BoostBrowser-Updater/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法连接更新服务器：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, fmt.Errorf("尚未发布任何 release")
	}
	if resp.StatusCode == 403 {
		// 403 may be rate-limit OR network middlebox/block; wording covers both.
		return nil, fmt.Errorf("GitHub API 限流或访问被拒绝(HTTP 403)")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API 返回 HTTP %d", resp.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("release 数据解析失败：%w", err)
	}
	return &rel, nil
}

func fetchLatestReleaseFromRedirect(client *http.Client, latestPageURL string) (*ghRelease, error) {
	req, err := http.NewRequest("GET", latestPageURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "BoostBrowser-Updater/1.0")

	redirectClient := *client
	redirectClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := redirectClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法访问最新版本页面：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusPermanentRedirect {
		return nil, fmt.Errorf("最新版本页面返回 HTTP %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return nil, fmt.Errorf("最新版本页面未返回跳转地址")
	}
	locURL, err := resp.Request.URL.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("最新版本跳转地址异常：%w", err)
	}
	parts := strings.Split(strings.Trim(locURL.Path, "/"), "/")
	var tag string
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "tag" {
			tag = parts[i+1]
			break
		}
	}
	if tag == "" {
		return nil, fmt.Errorf("最新版本跳转地址未包含 tag：%s", locURL.String())
	}
	return &ghRelease{
		TagName: tag,
		Assets: []ghAsset{
			{Name: "boost-browser.exe", BrowserDownloadURL: githubReleaseAssetURL(tag, "boost-browser.exe")},
			{Name: "boost-browser.exe.sha256", BrowserDownloadURL: githubReleaseAssetURL(tag, "boost-browser.exe.sha256")},
		},
	}, nil
}

func fetchSHA256Asset(url string) (string, error) {
	return fetchSHA256AssetWithClient(nil, url)
}

func fetchSHA256AssetWithClient(client *http.Client, url string) (string, error) {
	trustedURL, err := validateUpdateAssetURL(url, "boost-browser.exe.sha256")
	if err != nil {
		return "", err
	}
	if client == nil {
		client = newPublicRemoteHTTPClientWithProxy(30*time.Second, false, "")
	}
	resp, err := client.Get(trustedURL.String())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	// 容忍 "hash filename" / "hash" / "hash\n" 等格式
	first := strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0])
	parts := strings.Fields(first)
	if len(parts) == 0 {
		return "", fmt.Errorf("sha256 文件为空")
	}
	return normalizeUpdateSHA256(parts[0])
}

// DownloadUpdate Wails binding：流式下载，进度通过 update:progress 事件推送给前端
// 返回值是临时文件路径，传给 ApplyUpdate
func (a *App) DownloadUpdate(url, expectedSHA256 string) (string, error) {
	log := logger.New("Updater")
	trustedURL, err := validateUpdateAssetURL(url, "boost-browser.exe")
	if err != nil {
		return "", err
	}
	expectedSHA256, err = normalizeUpdateSHA256(expectedSHA256)
	if err != nil {
		return "", err
	}
	a.updateMu.Lock()
	a.verifiedUpdatePath = ""
	a.updateMu.Unlock()

	updateDir := a.resolveAppPath(filepath.Join("data", updateDirName))
	if err := os.MkdirAll(updateDir, 0755); err != nil {
		return "", fmt.Errorf("创建下载目录失败：%w", err)
	}
	dst := filepath.Join(updateDir, "boost-browser.new.exe")
	_ = os.Remove(dst)

	var downloaded int64
	var total int64
	lastEmit := time.Now()
	emitProgress := func(force bool) {
		if !force && time.Since(lastEmit) < 200*time.Millisecond {
			return
		}
		lastEmit = time.Now()
		percent := 0
		if total > 0 {
			percent = int(float64(downloaded) * 100 / float64(total))
		}
		runtime.EventsEmit(a.ctx, "update:progress", map[string]any{
			"downloaded": downloaded,
			"total":      total,
			"percent":    percent,
		})
	}

	var gotHash string
	var routeErrors []string
	for _, candidate := range a.updateDownloadClients(30 * time.Minute) {
		downloaded, total = 0, 0
		_ = os.Remove(dst)
		req, reqErr := http.NewRequest("GET", trustedURL.String(), nil)
		if reqErr != nil {
			return "", reqErr
		}
		req.Header.Set("User-Agent", "BoostBrowser-Updater/1.0")
		resp, requestErr := candidate.client.Do(req)
		if requestErr != nil {
			routeErrors = append(routeErrors, candidate.name+": "+requestErr.Error())
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			routeErrors = append(routeErrors, fmt.Sprintf("%s: HTTP %d", candidate.name, resp.StatusCode))
			continue
		}
		total = resp.ContentLength
		out, createErr := os.Create(dst)
		if createErr != nil {
			_ = resp.Body.Close()
			return "", createErr
		}
		hasher := sha256.New()
		buf := make([]byte, 64*1024)
		copyErr := error(nil)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, writeErr := out.Write(buf[:n]); writeErr != nil {
					copyErr = writeErr
					break
				}
				_, _ = hasher.Write(buf[:n])
				downloaded += int64(n)
				emitProgress(false)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				copyErr = readErr
				break
			}
		}
		_ = resp.Body.Close()
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			if copyErr == nil {
				copyErr = closeErr
			}
			routeErrors = append(routeErrors, candidate.name+": 下载中断: "+copyErr.Error())
			continue
		}
		gotHash = hex.EncodeToString(hasher.Sum(nil))
		if strings.EqualFold(gotHash, expectedSHA256) {
			log.Info("更新下载链路成功", logger.F("route", candidate.name))
			break
		}
		routeErrors = append(routeErrors, candidate.name+": SHA256 不匹配")
	}
	if gotHash == "" || !strings.EqualFold(gotHash, expectedSHA256) {
		_ = os.Remove(dst)
		return "", fmt.Errorf("所有更新下载链路均失败：%s", strings.Join(routeErrors, "；"))
	}
	if !isWindowsPEFile(dst) {
		_ = os.Remove(dst)
		return "", fmt.Errorf("更新文件不是有效的 Windows PE 可执行文件")
	}

	a.updateMu.Lock()
	a.verifiedUpdatePath = filepath.Clean(dst)
	a.updateMu.Unlock()

	emitProgress(true)
	log.Info("更新包下载完成",
		logger.F("path", dst),
		logger.F("size", downloaded),
		logger.F("sha256", gotHash),
	)
	return dst, nil
}

// applyUpdateDebugLog 同步写应急日志，绕过 async logger（os.Exit 时丢消息的问题）
func applyUpdateDebugLog(currentExe, msg string) {
	debugLogPath := filepath.Join(filepath.Dir(currentExe), "data", "logs", "apply_update.debug.log")
	_ = os.MkdirAll(filepath.Dir(debugLogPath), 0755)
	f, err := os.OpenFile(debugLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		fmt.Fprintf(f, "[%s] %s\n", time.Now().Format("2006-01-02 15:04:05.000"), msg)
		_ = f.Sync()
		_ = f.Close()
	}
}

// ApplyUpdate Wails binding：启动 updater.exe，主程序退出
func (a *App) ApplyUpdate(newExePath string) error {
	log := logger.New("Updater")
	expectedPath := filepath.Clean(a.resolveAppPath(filepath.Join("data", updateDirName, "boost-browser.new.exe")))
	requestedPath := filepath.Clean(strings.TrimSpace(newExePath))
	a.updateMu.Lock()
	verifiedPath := filepath.Clean(a.verifiedUpdatePath)
	if verifiedPath != "" && strings.EqualFold(verifiedPath, requestedPath) {
		a.verifiedUpdatePath = ""
	}
	a.updateMu.Unlock()
	if requestedPath == "." || !strings.EqualFold(requestedPath, expectedPath) || !strings.EqualFold(verifiedPath, requestedPath) {
		return fmt.Errorf("更新文件未通过本次下载会话的完整性验证")
	}
	newExePath = requestedPath
	if !isWindowsPEFile(newExePath) {
		return fmt.Errorf("更新文件不是有效的 Windows PE 可执行文件")
	}

	currentExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取当前 exe 路径失败：%w", err)
	}
	currentExe, _ = filepath.EvalSymlinks(currentExe)

	applyUpdateDebugLog(currentExe, fmt.Sprintf("ApplyUpdate 被调用 newExePath=%s", newExePath))

	updaterPath := filepath.Join(filepath.Dir(currentExe), updaterExeName)
	if _, err := os.Stat(updaterPath); err != nil {
		applyUpdateDebugLog(currentExe, fmt.Sprintf("ERROR updater.exe 不存在 path=%s err=%v", updaterPath, err))
		return fmt.Errorf("updater.exe 不存在：%s（请重装应用）", updaterPath)
	}
	if _, err := os.Stat(newExePath); err != nil {
		applyUpdateDebugLog(currentExe, fmt.Sprintf("ERROR 新版本文件不存在 path=%s err=%v", newExePath, err))
		return fmt.Errorf("新版本文件不存在：%s", newExePath)
	}

	// 清理可能残留的成功标记
	_ = os.Remove(a.resolveAppPath(filepath.Join("data", successMarker)))
	// Reuse the normal UUID data identity chain before replacing the EXE. This
	// only attaches/aligns an existing data/<UUID> directory; it never moves or
	// rewrites Cookies, extension storage, sessions or application state.
	if _, err := a.reconcileProfileUUIDData(); err != nil {
		return fmt.Errorf("更新前环境 UUID 数据完整性校验失败，已取消更新: %w", err)
	}
	// Capture the extension identity record immediately before replacement.
	// This stores IDs only and never reads wallet/Cookie/extension values.
	if err := a.captureProfileExtensionInventory(); err != nil {
		return fmt.Errorf("更新前保存扩展身份清单失败，已取消更新以保护用户扩展: %w", err)
	}
	a.prepareApplyUpdateQuit()

	pid := os.Getpid()
	cmd := exec.Command(updaterPath, strconv.Itoa(pid), currentExe, newExePath)
	cmd.SysProcAttr = detachedProcessAttrs()
	if err := cmd.Start(); err != nil {
		applyUpdateDebugLog(currentExe, fmt.Sprintf("ERROR cmd.Start 失败 err=%v", err))
		return fmt.Errorf("启动 updater 失败：%w", err)
	}

	updaterPID := cmd.Process.Pid
	applyUpdateDebugLog(currentExe, fmt.Sprintf("updater.exe 已启动 pid=%d", updaterPID))

	// 关键修复：Release 让 Go runtime 释放对子进程 handle，
	// 否则父进程 os.Exit 时 Windows 会把 Go runtime 跟踪的子进程一起带走（即使有 DETACHED_PROCESS）。
	if rerr := cmd.Process.Release(); rerr != nil {
		applyUpdateDebugLog(currentExe, fmt.Sprintf("WARN cmd.Process.Release 失败 err=%v", rerr))
	} else {
		applyUpdateDebugLog(currentExe, "cmd.Process.Release 成功，updater 与主进程已脱钩")
	}

	log.Info("updater 已启动，主程序即将退出",
		logger.F("updater_pid", updaterPID),
		logger.F("self_pid", pid),
		logger.F("current_exe", currentExe),
		logger.F("new_exe", newExePath),
	)

	// 关键修复：强制 flush async logger（默认 1s flush 间隔），避免 os.Exit 时丢消息
	_ = logger.Close()

	go func() {
		time.Sleep(800 * time.Millisecond)
		applyUpdateDebugLog(currentExe, "调用 runtime.Quit")
		runtime.Quit(a.ctx)
		time.Sleep(2 * time.Second)
		applyUpdateDebugLog(currentExe, "调用 os.Exit(0)")
		os.Exit(0)
	}()
	return nil
}

// prepareApplyUpdateQuit 把本次退出标记为交给 updater 的受控退出：
//  1. intentional-exit 标记阻止 watchdog 在 updater 替换窗口期把旧进程重新拉起；
//  2. forceQuit + quitModeFull 放行 Windows OnBeforeClose 拦截——否则 runtime.Quit
//     会被关闭确认框挡住，主程序不退出，updater 只能等 30 秒后强杀（升级卡住、
//     且浏览器数据无法被正常关闭）。
func (a *App) prepareApplyUpdateQuit() {
	a.markIntentionalExit("apply-update")
	a.setQuitMode(quitModeFull)
}

// WriteUpdateSuccessMarker 升级后第一次启动时调用，告诉 updater 升级成功
// 由 main.go / startup 在检测到 --post-update 参数时调用
func (a *App) WriteUpdateSuccessMarker() {
	marker := a.resolveAppPath(filepath.Join("data", successMarker))
	_ = os.MkdirAll(filepath.Dir(marker), 0755)
	_ = os.WriteFile(marker, []byte(time.Now().Format(time.RFC3339)+"\nversion="+a.appVersion()+"\n"), 0644)
	logger.New("Updater").Info("升级后首次启动，已写入成功标记", logger.F("marker", marker))
}

// compareVersion 简单 semver 比较：a > b 返回 1，相等返回 0，小于返回 -1
// 不支持 pre-release / build metadata，只比 major.minor.patch
func compareVersion(a, b string) int {
	a = strings.TrimPrefix(strings.TrimSpace(a), "v")
	b = strings.TrimPrefix(strings.TrimSpace(b), "v")
	pa := splitVersion(a)
	pb := splitVersion(b)
	for i := 0; i < 3; i++ {
		var na, nb int
		if i < len(pa) {
			na = pa[i]
		}
		if i < len(pb) {
			nb = pb[i]
		}
		if na > nb {
			return 1
		}
		if na < nb {
			return -1
		}
	}
	return 0
}

func splitVersion(v string) []int {
	parts := strings.Split(v, ".")
	out := make([]int, 0, 3)
	for _, p := range parts {
		// 截断 1.1.1-beta 这种后缀
		if idx := strings.IndexAny(p, "-+"); idx >= 0 {
			p = p[:idx]
		}
		n, _ := strconv.Atoi(strings.TrimSpace(p))
		out = append(out, n)
	}
	return out
}
