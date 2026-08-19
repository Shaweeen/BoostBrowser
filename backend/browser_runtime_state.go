package backend

import (
	"fmt"
	"os"
	"os/exec"
	stdruntime "runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"boost-browser/backend/internal/logger"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Debounced profile DB write so multi-open start does not rewrite the entire
// profile catalog under the manager lock on every single environment launch.
var (
	profilePersistMu      sync.Mutex
	profilePersistTimer   *time.Timer
	profilePersistPending atomic.Bool
)

func (a *App) scheduleProfilePersist() {
	if a == nil || a.browserMgr == nil {
		return
	}
	profilePersistMu.Lock()
	defer profilePersistMu.Unlock()
	if profilePersistTimer != nil {
		profilePersistTimer.Stop()
	}
	profilePersistPending.Store(true)
	profilePersistTimer = time.AfterFunc(500*time.Millisecond, func() {
		profilePersistPending.Store(false)
		if a == nil || a.browserMgr == nil {
			return
		}
		a.browserMgr.Mutex.Lock()
		err := a.browserMgr.SaveProfiles()
		a.browserMgr.Mutex.Unlock()
		if err != nil {
			logger.New("Browser").Warn("防抖持久化环境配置失败", logger.F("error", err.Error()))
		}
	})
}

const (
	browserAsyncDebugAttachTimeout   = 45 * time.Second
	browserLauncherDetachGraceWindow = 15 * time.Second
)

func copyBrowserProfileSnapshot(profile *BrowserProfile) *BrowserProfile {
	if profile == nil {
		return nil
	}
	snapshot := *profile
	return &snapshot
}

func browserDebugPendingWarning(timeout time.Duration) string {
	return fmt.Sprintf("浏览器窗口已启动，但调试接口在 %s 内仍未就绪；系统会继续在后台连接。连接完成前，Cookie、自动化和统一 CDP 入口暂不可用。", formatBrowserWaitWindow(timeout))
}

func browserDebugPendingStartNotice(timeout time.Duration) string {
	return fmt.Sprintf("浏览器窗口已启动，但在 %s 内尚未完成接管；系统会继续在后台连接，请稍后查看实例状态。连接完成前，Cookie、自动化和统一 CDP 入口暂不可用。", formatBrowserWaitWindow(timeout))
}

func formatBrowserWaitWindow(timeout time.Duration) string {
	if timeout <= 0 {
		return "当前等待窗口"
	}

	rounded := timeout.Round(100 * time.Millisecond)
	if rounded%time.Second == 0 {
		return fmt.Sprintf("%d 秒", rounded/time.Second)
	}
	if rounded%time.Millisecond == 0 {
		return fmt.Sprintf("%d 毫秒", rounded/time.Millisecond)
	}
	return rounded.String()
}

func browserInstanceEventPayload(profile *BrowserProfile, reused bool) map[string]interface{} {
	if profile == nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{
		"profileId":      profile.ProfileId,
		"profileName":    profile.ProfileName,
		"debugPort":      profile.DebugPort,
		"debugReady":     profile.DebugReady,
		"pid":            profile.Pid,
		"reused":         reused,
		"running":        profile.Running,
		"runtimeWarning": profile.RuntimeWarning,
	}
}

func (a *App) emitBrowserInstanceStarted(profile *BrowserProfile, reused bool) {
	if a == nil || a.ctx == nil || profile == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "browser:instance:started", browserInstanceEventPayload(profile, reused))
}

func (a *App) emitBrowserInstanceUpdated(profile *BrowserProfile) {
	if a == nil || a.ctx == nil || profile == nil {
		return
	}
	runtime.EventsEmit(a.ctx, "browser:instance:updated", browserInstanceEventPayload(profile, false))
}

func (a *App) markProfileRunningLocked(profileId string, profile *BrowserProfile, cmd *exec.Cmd, pid int, debugPort int, debugReady bool, runtimeWarning string) {
	if profile == nil {
		return
	}
	profile.Running = true
	profile.DebugPort = debugPort
	profile.DebugReady = debugReady
	profile.Pid = pid
	profile.LastStartAt = time.Now().Format(time.RFC3339)
	profile.RuntimeWarning = runtimeWarning
	profile.LastError = ""
	if cmd != nil {
		a.browserMgr.BrowserProcesses[profileId] = cmd
	}
	if debugReady && a.launchServer != nil {
		a.launchServer.SetActiveProfile(profile)
	}
	a.persistBrowserRuntimeSnapshotLocked()
	// Debounced persist: multi-open used to call SaveProfiles on every start
	// under the manager lock, serializing N full SQLite rewrites and stretching
	// batch start wall time dramatically.
	a.scheduleProfilePersist()
}

func (a *App) markProfileDebugReadyLocked(profile *BrowserProfile, debugPort int) {
	if profile == nil {
		return
	}
	profile.DebugPort = debugPort
	profile.DebugReady = true
	profile.RuntimeWarning = ""
	profile.LastError = ""
}

func (a *App) setProfileDebugReady(profileId string, debugPort int) (*BrowserProfile, bool) {
	if a == nil || a.browserMgr == nil {
		return nil, false
	}

	a.browserMgr.Mutex.Lock()
	profile, exists := a.browserMgr.Profiles[profileId]
	if !exists || profile == nil || !profile.Running || profile.DebugPort != debugPort {
		a.browserMgr.Mutex.Unlock()
		return nil, false
	}

	changed := !profile.DebugReady || profile.RuntimeWarning != ""
	if changed {
		a.markProfileDebugReadyLocked(profile, debugPort)
		a.persistBrowserRuntimeSnapshotLocked()
	}
	snapshot := copyBrowserProfileSnapshot(profile)
	a.browserMgr.Mutex.Unlock()

	if snapshot != nil && snapshot.DebugReady && a.launchServer != nil {
		a.launchServer.SetActiveProfile(snapshot)
	}
	return snapshot, changed
}

func (a *App) waitForBrowserDebugReady(profileId string, debugPort int, timeout time.Duration) (*BrowserProfile, bool) {
	if a == nil || a.browserMgr == nil || debugPort <= 0 || timeout <= 0 {
		return nil, false
	}

	deadline := time.Now().Add(timeout)
	for {
		a.browserMgr.Mutex.Lock()
		profile, exists := a.browserMgr.Profiles[profileId]
		if !exists || profile == nil || !profile.Running || profile.DebugPort != debugPort {
			a.browserMgr.Mutex.Unlock()
			return nil, false
		}
		if profile.DebugReady {
			snapshot := copyBrowserProfileSnapshot(profile)
			a.browserMgr.Mutex.Unlock()
			return snapshot, false
		}
		a.browserMgr.Mutex.Unlock()

		if err := probeBrowserDebugPort(debugPort, browserDebugProbeTimeout); err == nil {
			return a.setProfileDebugReady(profileId, debugPort)
		}
		if time.Now().After(deadline) {
			return nil, false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (a *App) waitBrowserDebugReadyAsync(profileId string, debugPort int, timeout time.Duration) {
	snapshot, changed := a.waitForBrowserDebugReady(profileId, debugPort, timeout)
	if snapshot == nil || !changed {
		return
	}

	logger.New("Browser").Info("实例调试接口已就绪",
		logger.F("profile_id", profileId),
		logger.F("debug_port", debugPort),
	)
	// Post-start tab cleanup retired (root: selective --load-extension).
	// 延迟就绪后也注入反检测脚本 + 启动 Turnstile 自动点击
	// CloakBrowser 内核完全跳过：内核层已处理身份/反检测，
	// wrapper 再走 CDP 注入会触发 nodriver / Browser Tampering 检测。
	if a.isProfileUsingCloakCore(profileId) {
		logger.New("Browser").Info("延迟就绪：CloakBrowser 内核，跳过 wrapper 级 stealth/Turnstile 注入",
			logger.F("profile_id", profileId),
			logger.F("debug_port", debugPort),
		)
	}

	// Delayed-ready is still the same user start; size only the main frame once.
	if snapshot.Pid > 0 {
		enforceMainEnvironmentWindowOnStart(snapshot.Pid)
	}

	a.emitBrowserInstanceUpdated(snapshot)
}

func shouldKeepBrowserRunningPendingDebugReady(debugPort int, monitor *browserProcessMonitor) bool {
	return debugPort > 0 && monitor != nil && !monitor.HasExited()
}

func isBrowserProfileLive(profile *BrowserProfile, trackedCmd *exec.Cmd) bool {
	if profile == nil || !profile.Running {
		return false
	}
	if profile.DebugPort > 0 && canConnectDebugPort(profile.DebugPort, 250*time.Millisecond) {
		return true
	}
	if profile.Pid > 0 && isProcessAlive(profile.Pid) {
		return true
	}
	if trackedCmd != nil && trackedCmd.Process != nil && trackedCmd.Process.Pid > 0 {
		return isProcessAlive(trackedCmd.Process.Pid)
	}
	return false
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if stdruntime.GOOS == "windows" {
		alive, err := isProcessAliveWindows(pid)
		return err == nil && alive
	}

	process, err := os.FindProcess(pid)
	if err != nil || process == nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
