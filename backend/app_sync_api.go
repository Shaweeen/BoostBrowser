//go:build windows

package backend

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	"boost-browser/backend/internal/logger"

	"golang.org/x/sys/windows"
)

// ============================================================================
// 窗口同步 API（供前端调用）
// ============================================================================

// syncState 全局同步状态
var syncState struct {
	mu          sync.Mutex
	syncer      *InputSyncer
	masterHwnd  windows.HWND
	followerIds []string
	masterId    string
	active      bool
}

var syncSessionMu sync.Mutex

var syncRuntimeDiscovery struct {
	sync.Mutex
	lastAttempt time.Time
	running     bool
}

// SyncProfileInfo 同步页面的实例信息
type SyncProfileInfo struct {
	ProfileId   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	Pid         int    `json:"pid"`
	DebugPort   int    `json:"debugPort"`
	Hwnd        int64  `json:"hwnd"`
	Running     bool   `json:"running"`
	Status      string `json:"status"` // "running" | "no_window" | "stopped"
	BadgeNumber int    `json:"badgeNumber"`
}

// GetSyncProfiles 获取所有可用于同步的实例列表
func (a *App) GetSyncProfiles() []SyncProfileInfo {
	// 同步面板直接读取共享状态并接管存活窗口。它不再依赖主客户端
	// Bridge，因此主客户端崩溃或 watchdog 重启不会让同步面板失联退出。
	return a.getSyncProfilesLocal()
}

func (a *App) getSyncProfilesLocal() []SyncProfileInfo {
	// Prefer the main client's shared runtime snapshot. A full Windows CIM scan
	// is only a throttled fallback, not a two-second polling dependency.
	liveSnapshots, totalSnapshots := a.applyBrowserRuntimeSnapshot()
	if liveSnapshots == 0 || liveSnapshots < totalSnapshots {
		a.reconcileSyncRuntimeStateAsync()
	}
	// NOTE: 不要在这里加 browserMgr.Mutex 锁！List() 内部会自行加锁，
	// 如果外层再锁一次会导致死锁（Go sync.Mutex 不可重入）。
	profiles := a.browserMgr.List()
	candidates := make([]BrowserProfile, 0, len(profiles))

	for _, p := range profiles {
		// Return any profile that has a live runtime handle. Older builds could leave
		// Running=false after app restart while the browser window was still open;
		// sync panel must still surface those windows after reconciliation.
		if !p.Running && p.Pid <= 0 && p.DebugPort <= 0 {
			continue
		}

		candidates = append(candidates, p)
	}

	rootPIDs := make([]int, 0, len(candidates))
	for _, profile := range candidates {
		if profile.Pid > 0 {
			rootPIDs = append(rootPIDs, profile.Pid)
		}
	}
	resolvedWindows := findProcessTreeWindows(rootPIDs)
	result := make([]SyncProfileInfo, len(candidates))
	resolvedCount := 0
	for i, p := range candidates {
		info := SyncProfileInfo{ProfileId: p.ProfileId, ProfileName: p.ProfileName, Pid: p.Pid, DebugPort: p.DebugPort, Running: p.Running, BadgeNumber: extractBadgeNumberFromName(p.ProfileName)}
		if hwnd := resolvedWindows[p.Pid]; hwnd != 0 {
			info.Hwnd = int64(hwnd)
			info.Status = "running"
			resolvedCount++
		} else {
			info.Status = "no_window"
		}
		result[i] = info
	}
	if resolvedCount < len(candidates) {
		a.reconcileSyncRuntimeStateAsync()
	}
	return result
}

func (a *App) reconcileSyncRuntimeStateAsync() {
	syncRuntimeDiscovery.Lock()
	if syncRuntimeDiscovery.running || time.Since(syncRuntimeDiscovery.lastAttempt) < 2*time.Second {
		syncRuntimeDiscovery.Unlock()
		return
	}
	syncRuntimeDiscovery.lastAttempt = time.Now()
	syncRuntimeDiscovery.running = true
	syncRuntimeDiscovery.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.lifecycleLog("sync-runtime-discovery-panic", fmt.Sprintf("value=%v", r))
			}
			syncRuntimeDiscovery.Lock()
			syncRuntimeDiscovery.running = false
			syncRuntimeDiscovery.Unlock()
		}()
		a.reconcileBrowserRuntimeStateOnce()
	}()
}

// StartInputSync 启动输入同步
// masterProfileId: 主控实例 ID
// followerProfileIds: 跟随实例 ID 列表
func (a *App) StartInputSync(masterProfileId string, followerProfileIds []string) error {
	if !a.panelMode {
		a.lifecycleLog("sync-start-rejected", "reason=main-process-isolation")
		return fmt.Errorf("输入同步只能在独立同步工具中启动")
	}
	return a.startInputSyncLocal(masterProfileId, followerProfileIds)
}

func (a *App) startInputSyncLocal(masterProfileId string, followerProfileIds []string) error {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()
	log := logger.New("SyncAPI")
	liveSnapshots, totalSnapshots := a.applyBrowserRuntimeSnapshot()
	if liveSnapshots == 0 || liveSnapshots < totalSnapshots {
		a.reconcileSyncRuntimeStateAsync()
	}

	// 查找主控实例
	a.browserMgr.Mutex.Lock()
	masterProfile, ok := a.browserMgr.Profiles[masterProfileId]
	if !ok {
		a.browserMgr.Mutex.Unlock()
		return fmt.Errorf("未找到主控实例：%s", masterProfileId)
	}
	if !masterProfile.Running || masterProfile.Pid <= 0 {
		a.browserMgr.Mutex.Unlock()
		return fmt.Errorf("主控实例未在运行：%s", masterProfileId)
	}
	masterSnapshot := *masterProfile

	// 收集跟随窗口
	type followerCandidate struct {
		id      string
		profile BrowserProfile
	}
	followers := make([]followerCandidate, 0, len(followerProfileIds))
	for _, fid := range followerProfileIds {
		if fid == masterProfileId {
			continue // 主控不能同时是跟随
		}
		fp, ok := a.browserMgr.Profiles[fid]
		if !ok || !fp.Running || fp.Pid <= 0 {
			continue
		}
		followers = append(followers, followerCandidate{id: fid, profile: *fp})
	}
	a.browserMgr.Mutex.Unlock()

	type followerWindow struct {
		hwnd      windows.HWND
		debugPort int
	}
	rootPIDs := []int{masterSnapshot.Pid}
	for _, candidate := range followers {
		rootPIDs = append(rootPIDs, candidate.profile.Pid)
	}
	resolvedWindows := findProcessTreeWindows(rootPIDs)
	masterHwnd := resolvedWindows[masterSnapshot.Pid]
	if masterHwnd == 0 {
		return fmt.Errorf("未找到主控实例窗口")
	}
	resolved := make([]followerWindow, len(followers))
	for i, candidate := range followers {
		resolved[i] = followerWindow{hwnd: resolvedWindows[candidate.profile.Pid], debugPort: candidate.profile.DebugPort}
	}

	var followerHwnds []windows.HWND
	var followerDebugPorts []int
	validFollowerIds := make([]string, 0, len(followers))
	for i, item := range resolved {
		if item.hwnd == 0 {
			continue
		}
		followerHwnds = append(followerHwnds, item.hwnd)
		followerDebugPorts = append(followerDebugPorts, item.debugPort)
		validFollowerIds = append(validFollowerIds, followers[i].id)
	}

	if len(followerHwnds) == 0 {
		return fmt.Errorf("没有可用的跟随实例")
	}

	syncState.mu.Lock()
	oldSyncer := syncState.syncer
	syncState.syncer = nil
	syncState.active = false
	syncState.mu.Unlock()
	if oldSyncer != nil {
		oldSyncer.Stop()
	}

	// 创建并启动同步器（带 CDP URL 同步，URL 同步默认由崩溃隔离开关关闭）
	syncer := NewInputSyncerWithLogger(func(event string, fields ...string) {
		a.lifecycleLog(event, fields...)
	})
	masterDebugPort := masterSnapshot.DebugPort
	if err := syncer.StartWithURLSync(masterHwnd, followerHwnds, masterSnapshot.Pid, masterDebugPort, followerDebugPorts); err != nil {
		return fmt.Errorf("启动同步失败：%v", err)
	}

	syncState.mu.Lock()
	syncState.syncer = syncer
	syncState.masterHwnd = masterHwnd
	syncState.masterId = masterProfileId
	syncState.followerIds = validFollowerIds
	syncState.active = true
	syncState.mu.Unlock()

	log.Info("输入同步已启动",
		logger.F("master", masterProfileId),
		logger.F("followers", fmt.Sprintf("%v", validFollowerIds)),
	)

	return nil
}

// StopInputSync 停止输入同步
func (a *App) StopInputSync() error {
	return a.stopInputSyncLocal()
}

func (a *App) stopInputSyncLocal() error {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()
	log := logger.New("SyncAPI")

	syncState.mu.Lock()
	syncer := syncState.syncer
	syncState.syncer = nil
	syncState.active = false
	syncState.masterHwnd = 0
	syncState.masterId = ""
	syncState.followerIds = nil
	syncState.mu.Unlock()

	if syncer != nil {
		syncer.Stop()
	}

	log.Info("输入同步已停止")
	return nil
}

// GetSyncStatus 获取当前同步状态
func (a *App) GetSyncStatus() map[string]interface{} {
	return a.getSyncStatusLocal()
}

func (a *App) getSyncStatusLocal() map[string]interface{} {
	runningProfileCount := 0
	a.browserMgr.Mutex.Lock()
	for _, profile := range a.browserMgr.Profiles {
		if profile != nil && profile.Running {
			runningProfileCount++
		}
	}
	a.browserMgr.Mutex.Unlock()
	syncState.mu.Lock()
	defer syncState.mu.Unlock()

	config := SyncConfig{MouseEnabled: true, KeyEnabled: true}
	if syncState.syncer != nil {
		config = syncState.syncer.GetConfig()
	}

	return map[string]interface{}{
		"active":              syncState.active,
		"masterId":            syncState.masterId,
		"followerIds":         append([]string(nil), syncState.followerIds...),
		"mouseEnabled":        config.MouseEnabled,
		"keyEnabled":          config.KeyEnabled,
		"randomDelayEnabled":  config.RandomDelayEnabled,
		"randomDelayMinMs":    config.RandomDelayMinMs,
		"randomDelayMaxMs":    config.RandomDelayMaxMs,
		"runningProfileCount": runningProfileCount,
	}
}

func (a *App) UpdateSyncRandomDelay(enabled bool, minMs, maxMs int) error {
	return a.updateSyncRandomDelayLocal(enabled, minMs, maxMs)
}

func (a *App) updateSyncRandomDelayLocal(enabled bool, minMs, maxMs int) error {
	syncState.mu.Lock()
	defer syncState.mu.Unlock()
	if syncState.syncer == nil {
		return fmt.Errorf("同步未启动")
	}
	syncState.syncer.SetRandomDelay(enabled, minMs, maxMs)
	return nil
}

// UpdateSyncConfig updates the optional mouse switch. Keyboard synchronisation
// is an invariant of an active session and cannot be accidentally disabled.
func (a *App) UpdateSyncConfig(mouseEnabled, keyEnabled bool) error {
	return a.updateSyncConfigLocal(mouseEnabled)
}

func (a *App) updateSyncConfigLocal(mouseEnabled bool) error {
	syncState.mu.Lock()
	defer syncState.mu.Unlock()

	if syncState.syncer == nil {
		return fmt.Errorf("同步未启动")
	}
	syncState.syncer.SetConfig(mouseEnabled, true)
	return nil
}

// TileWindowsResult 平铺结果
type TileWindowsResult struct {
	Count    int      `json:"count"`
	TiledIds []string `json:"tiledIds"`
	Layout   string   `json:"layout"` // "grid" | "horizontal" | "vertical"
}

// SyncTileWindows 平铺所有已选中实例的窗口
// masterProfileId: 主控实例ID，主控窗口始终放在最左边（index 0）
// layoutMode: grid | horizontal | vertical
func (a *App) SyncTileWindows(profileIds []string, masterProfileId string, layoutMode string) (*TileWindowsResult, error) {
	return a.syncTileWindowsLocal(profileIds, masterProfileId, layoutMode)
}

func (a *App) syncTileWindowsLocal(profileIds []string, masterProfileId string, layoutMode string) (*TileWindowsResult, error) {
	// The main management client is a separate process from the sync assistant.
	// Minimise it before arranging browser windows so it cannot cover the grid.
	minimizeMainClientWindow()

	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()

	// Reuse the exact HWNDs already validated by the active sync engine. Chrome
	// can transfer its top-level frame to a sibling process, so resolving again
	// from a stored PID is less reliable than the running session snapshot.
	activeWindows := make(map[string]windows.HWND)
	syncState.mu.Lock()
	if syncState.active && syncState.masterHwnd != 0 {
		activeWindows[syncState.masterId] = syncState.masterHwnd
		if syncState.syncer != nil {
			followerWindows := syncState.syncer.getFollowerSnapshot()
			for i, id := range syncState.followerIds {
				if i < len(followerWindows) {
					activeWindows[id] = followerWindows[i]
				}
			}
		}
	}
	syncState.mu.Unlock()

	// 收集运行中的实例窗口
	type winInfo struct {
		hwnd      windows.HWND
		profileId string
	}
	var wins []winInfo
	for _, pid := range profileIds {
		if hwnd := activeWindows[pid]; hwnd != 0 && isWindow(hwnd) {
			wins = append(wins, winInfo{hwnd: hwnd, profileId: pid})
			continue
		}
		profile, ok := a.browserMgr.Profiles[pid]
		if !ok || !profile.Running || profile.Pid <= 0 {
			continue
		}
		hwnd, err := findProcessTreeWindow(profile.Pid)
		if err != nil {
			continue
		}
		wins = append(wins, winInfo{hwnd: hwnd, profileId: pid})
	}

	if len(wins) == 0 {
		return nil, fmt.Errorf("没有可用的运行实例窗口")
	}

	// 确定主控ID：优先用参数传入的，否则从同步状态取
	effectiveMaster := masterProfileId
	if effectiveMaster == "" {
		syncState.mu.Lock()
		effectiveMaster = syncState.masterId
		syncState.mu.Unlock()
	}

	// 确保主控排在最左边（index 0 = 左侧）
	if effectiveMaster != "" {
		masterIdx := -1
		for i, w := range wins {
			if w.profileId == effectiveMaster {
				masterIdx = i
				break
			}
		}
		if masterIdx > 0 {
			masterWin := wins[masterIdx]
			wins = append(wins[:masterIdx], wins[masterIdx+1:]...)
			wins = append([]winInfo{masterWin}, wins...)
		}
	}

	// 获取屏幕可用工作区（排除任务栏）
	// 使用 SystemParametersInfo 获取 SPI_GETWORKAREA
	type RECT struct {
		Left, Top, Right, Bottom int32
	}
	var workArea RECT
	procSystemParametersInfoW := user32dll.NewProc("SystemParametersInfoW")
	procSystemParametersInfoW.Call(0x0030, 0, uintptr(unsafe.Pointer(&workArea)), 0) // SPI_GETWORKAREA = 0x0030
	screenW := int(workArea.Right - workArea.Left)
	screenH := int(workArea.Bottom - workArea.Top)
	originX := int(workArea.Left)
	originY := int(workArea.Top)

	if screenW <= 0 || screenH <= 0 {
		// 回退：使用全屏尺寸
		smCXScreen, _, _ := procGetSystemMetrics.Call(0)
		smCYScreen, _, _ := procGetSystemMetrics.Call(1)
		screenW = int(smCXScreen)
		screenH = int(smCYScreen)
		originX = 0
		originY = 0
	}

	n := len(wins)
	resolvedLayout := layoutMode
	switch resolvedLayout {
	case "horizontal", "vertical", "grid":
		// ok
	default:
		resolvedLayout = "grid"
	}
	if resolvedLayout == "grid" && n <= 2 {
		resolvedLayout = "horizontal"
	}

	var cols, rows int
	switch resolvedLayout {
	case "horizontal":
		cols = n
		rows = 1
	case "vertical":
		cols = 1
		rows = n
	default:
		if n <= 2 {
			cols = n
			rows = 1
		} else if n <= 4 {
			cols = 2
			rows = (n + 1) / 2
		} else if n <= 6 {
			cols = 3
			rows = 2
		} else {
			cols = 4
			rows = (n + 3) / 4
		}
	}

	cellW := screenW / cols
	cellH := screenH / rows

	// Chrome/Windows 在顶层窗口外缘会画 1~数 px 的深色 resize frame/DWM 阴影。
	// 只把最外侧窗口边缘向工作区外轻推，遮掉黑边；内部相邻窗口不再互相重叠，
	// 避免上下/左右内容被压盖。
	const tileWindowBleedPx = 8

	// SW_RESTORE = 9
	procShowWindow := user32dll.NewProc("ShowWindow")

	tiledIds := make([]string, 0, n)
	for i, w := range wins {
		col := i % cols
		row := i / cols
		x := originX + col*cellW
		y := originY + row*cellH
		winW := cellW
		winH := cellH

		if col == 0 {
			x -= tileWindowBleedPx
			winW += tileWindowBleedPx
		}
		if col == cols-1 {
			winW += tileWindowBleedPx
		}
		if row == 0 {
			y -= tileWindowBleedPx
			winH += tileWindowBleedPx
		}
		if row == rows-1 {
			winH += tileWindowBleedPx
		}

		// 先恢复窗口（如果被最小化）
		procShowWindow.Call(uintptr(w.hwnd), 9) // SW_RESTORE

		procSetWindowPos.Call(
			uintptr(w.hwnd),
			0, // HWND_TOP
			uintptr(x),
			uintptr(y),
			uintptr(winW),
			uintptr(winH),
			uintptr(SWP_NOZORDER|SWP_SHOWWINDOW),
		)
		tiledIds = append(tiledIds, w.profileId)
	}

	// 平铺后将主控窗口设为前台焦点（与 Python 版本一致）
	if effectiveMaster != "" && len(wins) > 0 && wins[0].profileId == effectiveMaster {
		procSetForegroundWindow := user32dll.NewProc("SetForegroundWindow")
		procSetForegroundWindow.Call(uintptr(wins[0].hwnd))
	}

	return &TileWindowsResult{
		Count:    len(tiledIds),
		TiledIds: tiledIds,
		Layout:   resolvedLayout,
	}, nil
}

// SyncCloseAll 关闭所有已选中实例
func (a *App) SyncCloseAll(profileIds []string) []string {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()

	closed := make([]string, 0)
	for _, pid := range profileIds {
		profile, ok := a.browserMgr.Profiles[pid]
		if !ok || !profile.Running {
			continue
		}
		if a.browserMgr.BrowserProcesses[pid] != nil && a.browserMgr.BrowserProcesses[pid].Process != nil {
			a.browserMgr.BrowserProcesses[pid].Process.Kill()
		}
		profile.Running = false
		profile.Pid = 0
		profile.DebugPort = 0
		profile.DebugReady = false
		closed = append(closed, pid)
	}
	return closed
}
