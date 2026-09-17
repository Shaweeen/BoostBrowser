//go:build windows

package backend

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"boost-browser/backend/internal/logger"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
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

	// followerTargets mirrors the fixed collection used by this session. It is
	// changed only by an explicit user add/remove action, never by scanning.
	followerTargets []windows.HWND
	followerPorts   []int
}

var syncSessionMu sync.Mutex

func (a *App) GetSyncSnapshot() SyncSnapshot {
	if a.panelMode {
		return a.requestMainSyncCollection(false)
	}
	return a.collectSyncSnapshotForPanel(false)
}

func (a *App) collectSyncSnapshotForPanel(_ bool) SyncSnapshot {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()
	profiles := a.getSyncProfilesLocal()
	generation := a.syncCollection.replace(profiles)
	return SyncSnapshot{
		Profiles:   profiles,
		Status:     a.getSyncStatusLocal(),
		Generation: generation,
	}
}

// GetSyncProfiles 获取所有可用于同步的实例列表
func (a *App) GetSyncProfiles() []SyncProfileInfo {
	// 同步面板只读取主客户端发布的环境运行状态。
	// Serialize collection with Start/Stop so a fast refresh can never rebuild
	// window data while the previous session is still releasing hooks/workers.
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()
	return a.getSyncProfilesLocal()
}

// RefreshSyncSnapshot asks the main client for one new complete collection.
// It never changes an already-running sync session's target handles.
func (a *App) RefreshSyncSnapshot() SyncSnapshot {
	if a.panelMode {
		return a.requestMainSyncCollection(true)
	}
	return a.collectSyncSnapshotForPanel(true)
}

// resolveLiveMainEnvironmentFrame accepts a candidate from the current batch
// scan only when it is a real environment frame. A wallet/OAuth popup must
// never become a sync target merely because it is the largest visible Chrome
// surface at that instant.
func resolveLiveMainEnvironmentFrame(rootPID int, candidate windows.HWND) windows.HWND {
	if candidate != 0 && isWindow(candidate) && isMainEnvironmentBrowserFrame(candidate, getWindowTitle(candidate)) {
		return candidate
	}
	return findMainEnvironmentBrowserWindow(rootPID)
}

func (a *App) getSyncProfilesLocal() []SyncProfileInfo {
	if a.panelMode {
		_, profiles := a.syncCollection.snapshot()
		return profiles
	}
	return a.collectMainClientSyncProfiles()

}

// collectMainClientSyncProfiles takes exactly one main-client-owned snapshot.
// It trusts the runtime state maintained by BrowserManager and resolves HWNDs
// in one bounded batch. It does not enumerate unrelated Chromium processes,
// reload SQLite, or run on a timer.
func (a *App) collectMainClientSyncProfiles() []SyncProfileInfo {
	if a == nil || a.browserMgr == nil {
		return nil
	}
	profiles := a.browserMgr.List()
	running := make([]BrowserProfile, 0, len(profiles))
	pids := make([]int, 0, len(profiles))
	seenPIDs := make(map[int]struct{}, len(profiles))
	for _, profile := range profiles {
		if !profile.Running || profile.Pid <= 0 || !isProcessAlive(profile.Pid) {
			continue
		}
		running = append(running, profile)
		if _, exists := seenPIDs[profile.Pid]; !exists {
			seenPIDs[profile.Pid] = struct{}{}
			pids = append(pids, profile.Pid)
		}
	}
	resolved := findProcessTreeWindows(pids)
	for _, pid := range pids {
		resolved[pid] = resolveLiveMainEnvironmentFrame(pid, resolved[pid])
	}
	result := make([]SyncProfileInfo, 0, len(running))
	for _, profile := range running {
		info := SyncProfileInfo{
			ProfileId: profile.ProfileId, ProfileName: profile.ProfileName,
			Pid: profile.Pid, DebugPort: profile.DebugPort, Running: true,
			BadgeNumber: extractBadgeNumberFromName(profile.ProfileName), Status: "no_window",
		}
		if hwnd := resolved[profile.Pid]; hwnd != 0 && isWindow(hwnd) {
			info.Hwnd = int64(hwnd)
			info.Status = "running"
		}
		result = append(result, info)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return naturalProfileNameLess(result[i].ProfileName, result[i].ProfileId, result[j].ProfileName, result[j].ProfileId)
	})
	return result
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
	return a.startInputSyncFromCollection(masterProfileId, followerProfileIds)

}

func (a *App) startInputSyncFromCollection(masterProfileID string, followerProfileIDs []string) error {
	log := logger.New("SyncAPI")
	generation, profiles := a.syncCollection.snapshot()
	if generation == 0 {
		return fmt.Errorf("尚未从主客户端获取窗口数据，请点击刷新")
	}
	byID := make(map[string]SyncProfileInfo, len(profiles))
	for _, profile := range profiles {
		byID[profile.ProfileId] = profile
	}
	validate := func(id, role string) (SyncProfileInfo, windows.HWND, error) {
		profile, ok := byID[id]
		if !ok || !profile.Running || profile.Pid <= 0 || profile.Hwnd == 0 {
			return SyncProfileInfo{}, 0, fmt.Errorf("%s环境窗口数据无效：%s，请点击刷新", role, id)
		}
		hwnd := windows.HWND(profile.Hwnd)
		if !isWindow(hwnd) || !isMainEnvironmentBrowserFrame(hwnd, getWindowTitle(hwnd)) {
			return SyncProfileInfo{}, 0, fmt.Errorf("%s环境窗口已失效：%s，请点击刷新", role, id)
		}
		return profile, hwnd, nil
	}

	masterProfileID = strings.TrimSpace(masterProfileID)
	if masterProfileID == "" {
		return fmt.Errorf("必须且只能指定一个主控实例")
	}
	master, masterHWND, err := validate(masterProfileID, "主控")
	if err != nil {
		return err
	}
	seenIDs := map[string]struct{}{masterProfileID: {}}
	seenHWNDs := map[windows.HWND]struct{}{masterHWND: {}}
	followerIDs := make([]string, 0, len(followerProfileIDs))
	followerHWNDs := make([]windows.HWND, 0, len(followerProfileIDs))
	followerPorts := make([]int, 0, len(followerProfileIDs))
	for _, rawID := range followerProfileIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, duplicate := seenIDs[id]; duplicate {
			if id == masterProfileID {
				return fmt.Errorf("主控实例不能同时出现在跟随列表：%s", id)
			}
			continue
		}
		seenIDs[id] = struct{}{}
		profile, hwnd, validateErr := validate(id, "跟随")
		if validateErr != nil {
			return validateErr
		}
		if _, duplicate := seenHWNDs[hwnd]; duplicate {
			return fmt.Errorf("窗口数据冲突：环境 %s 与其他环境指向同一窗口，请点击刷新", id)
		}
		seenHWNDs[hwnd] = struct{}{}
		followerIDs = append(followerIDs, id)
		followerHWNDs = append(followerHWNDs, hwnd)
		followerPorts = append(followerPorts, profile.DebugPort)
	}
	if len(followerHWNDs) == 0 {
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
	syncer := NewInputSyncerWithLogger(func(event string, fields ...string) {
		a.lifecycleLog(event, fields...)
	})
	syncer.SetPauseChangedHandler(func(paused bool) {
		syncState.mu.Lock()
		current := syncState.active && syncState.syncer == syncer
		syncState.mu.Unlock()
		if current && a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "window-sync:pause-changed", map[string]interface{}{"paused": paused})
		}
	})
	if err := syncer.StartWithURLSync(masterHWND, followerHWNDs, master.Pid, master.DebugPort, followerPorts); err != nil {
		return fmt.Errorf("启动同步失败：%v", err)
	}
	syncer.SetRandomDelay(false, 0, 0)
	syncState.mu.Lock()
	syncState.syncer = syncer
	syncState.masterHwnd = masterHWND
	syncState.masterId = masterProfileID
	syncState.followerIds = followerIDs
	syncState.followerTargets = append([]windows.HWND(nil), followerHWNDs...)
	syncState.followerPorts = append([]int(nil), followerPorts...)
	syncState.active = true
	syncState.mu.Unlock()
	log.Info("输入同步已从固定窗口批次启动", logger.F("master", masterProfileID), logger.F("followers", fmt.Sprintf("%v", followerIDs)))
	minimizeMainClientWindow()
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
	syncState.followerTargets = nil
	syncState.followerPorts = nil
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
	pointerInsideMaster := false
	paused := false
	if syncState.syncer != nil {
		config = syncState.syncer.GetConfig()
		pointerInsideMaster = syncState.syncer.PointerInsideMaster()
		paused = syncState.syncer.IsPaused()
	}

	return map[string]interface{}{
		"active":              syncState.active,
		"paused":              paused,
		"masterId":            syncState.masterId,
		"followerIds":         append([]string(nil), syncState.followerIds...),
		"mouseEnabled":        config.MouseEnabled,
		"keyEnabled":          config.KeyEnabled,
		"pointerInsideMaster": pointerInsideMaster,
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
// layoutMode: grid | horizontal | vertical | custom:<columns>x<rows>
func (a *App) SyncTileWindows(profileIds []string, masterProfileId string, layoutMode string) (*TileWindowsResult, error) {
	return a.syncTileWindowsLocal(profileIds, masterProfileId, layoutMode)
}

func (a *App) syncTileWindowsLocal(profileIds []string, masterProfileId string, layoutMode string) (*TileWindowsResult, error) {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()

	// Arrangement consumes the last explicit open/refresh collection. It must
	// not rescan or silently replace targets while sync is active.
	_, collected := a.syncCollection.snapshot()
	collectedByID := make(map[string]SyncProfileInfo, len(collected))
	for _, profile := range collected {
		collectedByID[profile.ProfileId] = profile
	}

	// The main management client is a separate process from the sync assistant.
	// Minimise it before arranging browser windows so it cannot cover the grid.
	minimizeMainClientWindow()

	// Hold popup confinement for the whole arrange + DWM settle. Short holds
	// under 10-window sync let the confiner race half-applied SetWindowPos and
	// leave frames "displaced".
	if a != nil {
		setLayoutHoldRoot(a.appRoot)
	}
	holdPopupConfinementForLayout(true)
	layoutHoldMs := 80
	if len(profileIds) >= 8 {
		layoutHoldMs = 220
	}
	defer func() {
		time.Sleep(time.Duration(layoutHoldMs) * time.Millisecond)
		holdPopupConfinementForLayout(false)
	}()

	// Window movement and coordinate replay must never overlap.
	syncState.mu.Lock()
	layoutSyncer := syncState.syncer
	layoutActive := syncState.active && layoutSyncer != nil
	syncState.mu.Unlock()
	if layoutActive {
		layoutSyncer.BeginLayoutUpdate()
		defer func() {
			time.Sleep(50 * time.Millisecond)
			layoutSyncer.EndLayoutUpdate()
		}()
	}

	activeMasterID := ""
	activeWindows := make(map[string]windows.HWND)
	syncState.mu.Lock()
	if syncState.active && syncState.masterHwnd != 0 {
		activeMasterID = syncState.masterId
		activeWindows[syncState.masterId] = syncState.masterHwnd
		for i, id := range syncState.followerIds {
			if i < len(syncState.followerTargets) {
				activeWindows[id] = syncState.followerTargets[i]
			}
		}
	}
	syncState.mu.Unlock()

	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()

	type winInfo struct {
		hwnd        windows.HWND
		profileId   string
		profileName string
	}
	var wins []winInfo
	seenProfileIDs := make(map[string]struct{}, len(profileIds))
	seenWindows := make(map[windows.HWND]struct{}, len(profileIds))

	resolveMainHWND := func(profileID string) windows.HWND {
		if hwnd := activeWindows[profileID]; hwnd != 0 {
			if isWindow(hwnd) && isMainEnvironmentBrowserFrame(hwnd, getWindowTitle(hwnd)) {
				return hwnd
			}
			return 0
		}
		info, ok := collectedByID[profileID]
		if !ok || !info.Running || info.Hwnd == 0 {
			return 0
		}
		hwnd := windows.HWND(info.Hwnd)
		if isWindow(hwnd) && isMainEnvironmentBrowserFrame(hwnd, getWindowTitle(hwnd)) {
			return hwnd
		}
		return 0
	}

	for _, rawProfileID := range profileIds {
		pid := strings.TrimSpace(rawProfileID)
		if pid == "" {
			continue
		}
		if _, duplicate := seenProfileIDs[pid]; duplicate {
			continue
		}
		seenProfileIDs[pid] = struct{}{}
		profile, profileExists := a.browserMgr.Profiles[pid]
		if !profileExists || profile == nil || !profile.Running {
			return nil, fmt.Errorf("环境 %s 不在当前窗口批次中，请点击刷新", pid)
		}
		hwnd := resolveMainHWND(pid)
		if hwnd == 0 {
			return nil, fmt.Errorf("环境 %s 的窗口已失效，请点击刷新", pid)
		}
		if _, duplicate := seenWindows[hwnd]; duplicate {
			// Two profiles resolved to the same frame — skip second (prevents
			// one physical window receiving two tile cells / "swap" chaos).
			continue
		}
		seenWindows[hwnd] = struct{}{}
		wins = append(wins, winInfo{
			hwnd: hwnd, profileId: pid, profileName: profile.ProfileName,
		})
	}

	if len(wins) == 0 {
		return nil, fmt.Errorf("没有可用的运行实例窗口")
	}

	// 确定主控ID：优先用参数传入的，否则从同步状态取
	effectiveMaster := strings.TrimSpace(masterProfileId)
	if activeMasterID != "" {
		if effectiveMaster != "" && effectiveMaster != activeMasterID {
			return nil, fmt.Errorf("同步期间主控唯一且不可由排列操作改写：当前主控=%s", activeMasterID)
		}
		effectiveMaster = activeMasterID
	} else if effectiveMaster == "" {
		syncState.mu.Lock()
		effectiveMaster = syncState.masterId
		syncState.mu.Unlock()
	}

	// Stable natural-number order for all non-master cells (1,2,3…), then pin
	// master at index 0 for sync UX. Avoids random profileIds order under sync.
	sort.SliceStable(wins, func(i, j int) bool {
		return naturalProfileNameLess(wins[i].profileName, wins[i].profileId, wins[j].profileName, wins[j].profileId)
	})
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
	type RECT struct {
		Left, Top, Right, Bottom int32
	}
	var workArea RECT
	procSystemParametersInfoW := user32dll.NewProc("SystemParametersInfoW")
	procSystemParametersInfoW.Call(0x0030, 0, uintptr(unsafe.Pointer(&workArea)), 0) // SPI_GETWORKAREA
	screenW := int(workArea.Right - workArea.Left)
	screenH := int(workArea.Bottom - workArea.Top)
	originX := int(workArea.Left)
	originY := int(workArea.Top)

	if screenW <= 0 || screenH <= 0 {
		smCXScreen, _, _ := procGetSystemMetrics.Call(0)
		smCYScreen, _, _ := procGetSystemMetrics.Call(1)
		screenW = int(smCXScreen)
		screenH = int(smCYScreen)
		originX = 0
		originY = 0
	}

	n := len(wins)
	windowAspects := make([]float64, 0, n)
	for _, win := range wins {
		if width, height, ok := getClientSize(win.hwnd); ok && width > 0 && height > 0 {
			windowAspects = append(windowAspects, float64(width)/float64(height))
		}
	}
	preferredWindowAspect := 1.0
	if len(windowAspects) > 0 {
		sort.Float64s(windowAspects)
		middle := len(windowAspects) / 2
		preferredWindowAspect = windowAspects[middle]
		if len(windowAspects)%2 == 0 {
			preferredWindowAspect = (windowAspects[middle-1] + preferredWindowAspect) / 2
		}
	}
	requestedLayout := strings.TrimSpace(strings.ToLower(layoutMode))
	requestedCols, requestedRows, customLayout := parseCustomTileLayout(requestedLayout)
	if strings.HasPrefix(requestedLayout, "custom:") && !customLayout {
		return nil, fmt.Errorf("自定义排列格式无效，应为 custom:<列数>x<行数>")
	}
	if customLayout && !tileLayoutFitsCount(n, requestedCols, requestedRows) {
		return nil, fmt.Errorf("自定义排列 %d×%d 容纳不足：已选择 %d 个环境", requestedCols, requestedRows, n)
	}
	resolvedLayout := layoutMode
	if customLayout {
		// Result stays "grid" for the existing UI status type; the exact
		// user-selected dimensions have already been consumed below.
		resolvedLayout = "grid"
	} else {
		switch resolvedLayout {
		case "horizontal", "vertical", "grid":
		default:
			resolvedLayout = "grid"
		}
	}

	var cols, rows int
	if customLayout {
		cols, rows = requestedCols, requestedRows
	} else {
		switch resolvedLayout {
		case "horizontal":
			cols = n
			rows = 1
		case "vertical":
			cols = 1
			rows = n
		default:
			cols, rows = tileGridDimensions(n, screenW, screenH, preferredWindowAspect)
		}
	}

	// Fixed 1px gap + identical cell size for every window (including last row).
	// Large DWM overlap made seams look uneven and broke scroll/click ratios.
	rects := computeUniformTileRects(n, cols, rows, originX, originY, screenW, screenH, defaultTileGapPx)

	procShowWindow := user32dll.NewProc("ShowWindow")
	procBeginDeferWindowPos := user32dll.NewProc("BeginDeferWindowPos")
	procDeferWindowPos := user32dll.NewProc("DeferWindowPos")
	procEndDeferWindowPos := user32dll.NewProc("EndDeferWindowPos")

	// Restore (un-minimize / un-maximize) first so DeferWindowPos sizes apply.
	for _, w := range wins {
		procShowWindow.Call(uintptr(w.hwnd), 9) // SW_RESTORE
	}

	// Atomic multi-window placement: one DWM transaction avoids intermediate
	// layouts that confiner/input saw as "wrong positions" under 10-way sync.
	hdwp, _, _ := procBeginDeferWindowPos.Call(uintptr(n))
	if hdwp != 0 {
		for i, w := range wins {
			if i >= len(rects) {
				break
			}
			r := rects[i]
			hdwp, _, _ = procDeferWindowPos.Call(
				hdwp,
				uintptr(w.hwnd),
				0, // HWND_TOP, with SWP_NOZORDER ignored
				uintptr(r.X),
				uintptr(r.Y),
				uintptr(r.W),
				uintptr(r.H),
				uintptr(SWP_NOZORDER|SWP_NOACTIVATE),
			)
			if hdwp == 0 {
				break
			}
		}
		if hdwp != 0 {
			procEndDeferWindowPos.Call(hdwp)
		}
	}
	// Fallback if DeferWindowPos failed mid-way.
	if hdwp == 0 {
		for i, w := range wins {
			if i >= len(rects) {
				break
			}
			r := rects[i]
			procSetWindowPos.Call(
				uintptr(w.hwnd),
				0,
				uintptr(r.X),
				uintptr(r.Y),
				uintptr(r.W),
				uintptr(r.H),
				uintptr(SWP_NOZORDER|SWP_NOACTIVATE|SWP_SHOWWINDOW),
			)
		}
	}

	tiledIds := make([]string, 0, n)
	var masterHandle windows.HWND
	for i, w := range wins {
		if i >= len(rects) {
			break
		}
		tiledIds = append(tiledIds, w.profileId)
		if w.profileId == effectiveMaster {
			masterHandle = w.hwnd
		}
	}
	if masterHandle == 0 && len(wins) > 0 {
		masterHandle = wins[0].hwnd
	}

	if effectiveMaster != "" && masterHandle != 0 {
		procSetForegroundWindow := user32dll.NewProc("SetForegroundWindow")
		procSetForegroundWindow.Call(uintptr(masterHandle))
	}

	return &TileWindowsResult{
		Count:    len(tiledIds),
		TiledIds: tiledIds,
		Layout:   resolvedLayout,
	}, nil
}

// SyncCloseAll 关闭所有已选中实例。并发关闭以加速批量操作，semaphore 限制并发数
// 避免同时关闭过多浏览器导致系统资源争抢。
func (a *App) SyncCloseAll(profileIds []string) []string {
	if len(profileIds) == 0 {
		return nil
	}
	// 并发关闭：限制最大 5 个并发，避免同时打开过多 CDP 连接和文件句柄。
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var mu sync.Mutex
	closed := make([]string, 0, len(profileIds))
	var wg sync.WaitGroup
	for _, profileID := range profileIds {
		wg.Add(1)
		sem <- struct{}{} // 获取信号量
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }() // 释放信号量
			if _, err := a.BrowserInstanceStop(id); err == nil {
				mu.Lock()
				closed = append(closed, id)
				mu.Unlock()
			}
		}(profileID)
	}
	wg.Wait()
	return closed
}

// ============================================================================
// 动态增减跟随环境（运行中同步会话热修改）
// ============================================================================

// AddFollowerToSync 向运行中的同步会话添加一个跟随环境。
// 返回 error 表示添加失败（环境未运行、已是主控等）。
func (a *App) AddFollowerToSync(profileId string) error {
	if !a.panelMode {
		return fmt.Errorf("动态增减只能在同步面板中操作")
	}
	return a.addFollowerToSyncLocal(profileId)
}

func (a *App) addFollowerToSyncLocal(profileId string) error {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()
	return a.addFollowerFromCollection(profileId)

}

func (a *App) addFollowerFromCollection(profileID string) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return fmt.Errorf("环境 ID 不能为空")
	}
	_, profiles := a.syncCollection.snapshot()
	byID := make(map[string]SyncProfileInfo, len(profiles))
	for _, profile := range profiles {
		byID[profile.ProfileId] = profile
	}
	info, ok := byID[profileID]
	if !ok || !info.Running || info.Hwnd == 0 {
		return fmt.Errorf("环境 %s 不在当前窗口批次中，请点击刷新", profileID)
	}
	hwnd := windows.HWND(info.Hwnd)
	if !isWindow(hwnd) || !isMainEnvironmentBrowserFrame(hwnd, getWindowTitle(hwnd)) {
		return fmt.Errorf("环境 %s 的窗口已失效，请点击刷新", profileID)
	}

	syncState.mu.Lock()
	defer syncState.mu.Unlock()
	if !syncState.active || syncState.syncer == nil {
		return fmt.Errorf("同步未启动")
	}
	if profileID == syncState.masterId {
		return fmt.Errorf("主控环境不能同时作为跟随者")
	}
	for _, id := range syncState.followerIds {
		if id == profileID {
			return fmt.Errorf("环境 %s 已在跟随列表中", profileID)
		}
	}
	for _, target := range syncState.followerTargets {
		if target == hwnd {
			return fmt.Errorf("环境 %s 与现有跟随窗口冲突，请点击刷新", profileID)
		}
	}
	syncState.followerIds = append(syncState.followerIds, profileID)
	syncState.followerTargets = append(syncState.followerTargets, hwnd)
	syncState.followerPorts = append(syncState.followerPorts, info.DebugPort)
	syncState.syncer.ReplaceWindowTargets(syncState.masterHwnd, append([]windows.HWND(nil), syncState.followerTargets...), append([]int(nil), syncState.followerPorts...))
	return nil
}

// RemoveFollowerFromSync 从运行中的同步会话移除一个跟随环境。
func (a *App) RemoveFollowerFromSync(profileId string) error {
	if !a.panelMode {
		return fmt.Errorf("动态增减只能在同步面板中操作")
	}
	return a.removeFollowerFromSyncLocal(profileId)
}

func (a *App) removeFollowerFromSyncLocal(profileId string) error {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()
	return a.removeFollowerFromCollection(profileId)

}

func (a *App) removeFollowerFromCollection(profileID string) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return fmt.Errorf("环境 ID 不能为空")
	}
	syncState.mu.Lock()
	defer syncState.mu.Unlock()
	if !syncState.active || syncState.syncer == nil {
		return fmt.Errorf("同步未启动")
	}
	ids := make([]string, 0, len(syncState.followerIds))
	targets := make([]windows.HWND, 0, len(syncState.followerTargets))
	ports := make([]int, 0, len(syncState.followerPorts))
	found := false
	for i, id := range syncState.followerIds {
		if id == profileID {
			found = true
			continue
		}
		ids = append(ids, id)
		if i < len(syncState.followerTargets) {
			targets = append(targets, syncState.followerTargets[i])
		}
		if i < len(syncState.followerPorts) {
			ports = append(ports, syncState.followerPorts[i])
		}
	}
	if !found {
		return fmt.Errorf("环境 %s 不在跟随列表中", profileID)
	}
	syncState.followerIds = ids
	syncState.followerTargets = targets
	syncState.followerPorts = ports
	syncState.syncer.ReplaceWindowTargets(syncState.masterHwnd, append([]windows.HWND(nil), targets...), ports)
	return nil
}

// GetSyncFollowerIds 获取当前同步会话中的跟随环境 ID 列表
func (a *App) GetSyncFollowerIds() []string {
	syncState.mu.Lock()
	defer syncState.mu.Unlock()
	if !syncState.active {
		return nil
	}
	return append([]string(nil), syncState.followerIds...)
}
