//go:build windows

package backend

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
}

var syncSessionMu sync.Mutex
var syncSnapshotGeneration uint64

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

// SyncSnapshot returns profiles and session state from one serialized boundary.
// The UI must not combine a newly scanned window list with status from an older
// Start/Stop generation.
type SyncSnapshot struct {
	Profiles   []SyncProfileInfo      `json:"profiles"`
	Status     map[string]interface{} `json:"status"`
	Generation uint64                 `json:"generation"`
}

func (a *App) GetSyncSnapshot() SyncSnapshot {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()
	return SyncSnapshot{
		Profiles:   a.getSyncProfilesLocal(),
		Status:     a.getSyncStatusLocal(),
		Generation: atomic.LoadUint64(&syncSnapshotGeneration),
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

func (a *App) getSyncProfilesLocal() []SyncProfileInfo {
	// The main client's environment list and root PIDs are authoritative. Reuse
	// every valid HWND it published, then resolve all still-missing root PIDs in
	// one bounded batch. The panel never reconciles or persists runtime state.
	runtimeSnapshot, snapshotOK := a.readBrowserRuntimeSnapshot()
	if !snapshotOK {
		runtimeSnapshot = browserRuntimeSnapshot{}
	}
	a.applyBrowserRuntimeSnapshotData(runtimeSnapshot)
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

	cachedWindows := make(map[string]windows.HWND, len(runtimeSnapshot.Entries))
	rootPIDs := make([]int, 0, len(candidates))
	for _, entry := range runtimeSnapshot.Entries {
		cachedWindows[entry.ProfileID] = validRuntimeSnapshotWindow(entry)
	}
	for _, profile := range candidates {
		if profile.Pid > 0 && cachedWindows[profile.ProfileId] == 0 {
			rootPIDs = append(rootPIDs, profile.Pid)
		}
	}
	resolvedWindows := findProcessTreeWindows(rootPIDs)
	missing := 0
	for _, p := range candidates {
		if cachedWindows[p.ProfileId] == 0 && resolvedWindows[p.Pid] == 0 && p.Pid > 0 {
			missing++
		}
	}
	// Multi-open often leaves top-level frames unregistered for a short moment.
	// One cheap retry avoids an empty/unusable assistant list right after batch start.
	if missing > 0 && missing >= (len(candidates)+1)/2 {
		time.Sleep(150 * time.Millisecond)
		resolvedWindows = findProcessTreeWindows(rootPIDs)
	}
	result := make([]SyncProfileInfo, len(candidates))
	for i, p := range candidates {
		info := SyncProfileInfo{ProfileId: p.ProfileId, ProfileName: p.ProfileName, Pid: p.Pid, DebugPort: p.DebugPort, Running: p.Running, BadgeNumber: extractBadgeNumberFromName(p.ProfileName)}
		hwnd := cachedWindows[p.ProfileId]
		if hwnd == 0 {
			hwnd = resolvedWindows[p.Pid]
		}
		if hwnd != 0 {
			info.Hwnd = int64(hwnd)
			info.Status = "running"
		} else {
			info.Status = "no_window"
		}
		result[i] = info
	}
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
	log := logger.New("SyncAPI")
	// Start is a transaction boundary: discard assumptions from earlier UI
	// scans and obtain the current process/window ownership before validating
	// the requested master and followers.
	runtimeSnapshot, snapshotOK := a.readBrowserRuntimeSnapshot()
	if !snapshotOK {
		return fmt.Errorf("无法读取主客户端的运行环境状态，请返回主客户端后重新打开同步助手")
	}
	a.applyBrowserRuntimeSnapshotData(runtimeSnapshot)
	snapshotEntries := make(map[string]browserRuntimeSnapshotEntry, len(runtimeSnapshot.Entries))
	for _, entry := range runtimeSnapshot.Entries {
		snapshotEntries[entry.ProfileID] = entry
	}

	masterProfileId = strings.TrimSpace(masterProfileId)
	if masterProfileId == "" {
		return fmt.Errorf("必须且只能指定一个主控实例")
	}

	// 查找唯一主控实例
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
	seenFollowerIDs := make(map[string]struct{}, len(followerProfileIds))
	seenFollowerPIDs := map[int]struct{}{masterSnapshot.Pid: {}}
	for _, rawFollowerID := range followerProfileIds {
		fid := strings.TrimSpace(rawFollowerID)
		if fid == "" {
			continue
		}
		if fid == masterProfileId {
			a.browserMgr.Mutex.Unlock()
			return fmt.Errorf("主控实例不能同时出现在跟随列表：%s", fid)
		}
		if _, duplicate := seenFollowerIDs[fid]; duplicate {
			continue
		}
		seenFollowerIDs[fid] = struct{}{}
		fp, ok := a.browserMgr.Profiles[fid]
		if !ok || !fp.Running || fp.Pid <= 0 {
			continue
		}
		if _, duplicateProcess := seenFollowerPIDs[fp.Pid]; duplicateProcess {
			continue
		}
		seenFollowerPIDs[fp.Pid] = struct{}{}
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
	masterHwnd := validRuntimeSnapshotWindow(snapshotEntries[masterProfileId])
	if masterHwnd == 0 {
		masterHwnd = resolvedWindows[masterSnapshot.Pid]
	}
	if masterHwnd == 0 {
		return fmt.Errorf("未找到主控实例窗口")
	}
	resolved := make([]followerWindow, len(followers))
	for i, candidate := range followers {
		hwnd := validRuntimeSnapshotWindow(snapshotEntries[candidate.id])
		if hwnd == 0 {
			hwnd = resolvedWindows[candidate.profile.Pid]
		}
		resolved[i] = followerWindow{hwnd: hwnd, debugPort: candidate.profile.DebugPort}
	}

	var followerHwnds []windows.HWND
	var followerDebugPorts []int
	validFollowerIds := make([]string, 0, len(followers))
	seenFollowerWindows := map[windows.HWND]struct{}{masterHwnd: {}}
	for i, item := range resolved {
		if item.hwnd == 0 {
			continue
		}
		if _, duplicateWindow := seenFollowerWindows[item.hwnd]; duplicateWindow {
			continue
		}
		seenFollowerWindows[item.hwnd] = struct{}{}
		followerHwnds = append(followerHwnds, item.hwnd)
		followerDebugPorts = append(followerDebugPorts, item.debugPort)
		validFollowerIds = append(validFollowerIds, followers[i].id)
	}

	if len(followerHwnds) == 0 {
		return fmt.Errorf("没有可用的跟随实例")
	}

	// Per-env one-shot: brand-new processes only → one about:blank then stop.
	// Already-handed-off envs keep user web tabs + extension pages untouched.
	masterDebugPort := masterSnapshot.DebugPort
	handoffTargets := make([]syncHandoffTarget, 0, 1+len(validFollowerIds))
	handoffTargets = append(handoffTargets, syncHandoffTarget{
		profileID: masterProfileId,
		pid:       masterSnapshot.Pid,
		debugPort: masterDebugPort,
	})
	// validFollowerIds[i] aligns with followerDebugPorts[i]; resolve pid by id.
	pidByID := make(map[string]int, len(followers))
	for _, c := range followers {
		pidByID[c.id] = c.profile.Pid
	}
	for i, fid := range validFollowerIds {
		port := 0
		if i < len(followerDebugPorts) {
			port = followerDebugPorts[i]
		}
		handoffTargets = append(handoffTargets, syncHandoffTarget{
			profileID: fid,
			pid:       pidByID[fid],
			debugPort: port,
		})
	}
	applied, skippedHandoff, handoffClosed := prepareEnvironmentsForSyncHandoff(handoffTargets)
	if applied > 0 || skippedHandoff > 0 {
		log.Info("同步前标签接管",
			logger.F("applied_new", applied),
			logger.F("skipped_already", skippedHandoff),
			logger.F("closed_tabs", handoffClosed),
		)
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
	syncer.SetPauseChangedHandler(func(paused bool) {
		syncState.mu.Lock()
		current := syncState.active && syncState.syncer == syncer
		syncState.mu.Unlock()
		if current && a.ctx != nil {
			wailsruntime.EventsEmit(a.ctx, "window-sync:pause-changed", map[string]interface{}{"paused": paused})
		}
	})
	if err := syncer.StartWithURLSync(masterHwnd, followerHwnds, masterSnapshot.Pid, masterDebugPort, followerDebugPorts); err != nil {
		return fmt.Errorf("启动同步失败：%v", err)
	}
	// Hard guarantee: starting sync is always immediate. Random delay is only
	// applied after the user explicitly enables it in the assistant UI.
	syncer.SetRandomDelay(false, 0, 0)

	syncState.mu.Lock()
	syncState.syncer = syncer
	syncState.masterHwnd = masterHwnd
	syncState.masterId = masterProfileId
	syncState.followerIds = validFollowerIds
	syncState.active = true
	syncState.mu.Unlock()
	atomic.AddUint64(&syncSnapshotGeneration, 1)

	log.Info("输入同步已启动",
		logger.F("master", masterProfileId),
		logger.F("followers", fmt.Sprintf("%v", validFollowerIds)),
	)
	// Keep the management client out of the tiled workspace as soon as sync is
	// enabled, even when the user has not pressed a separate layout button yet.
	// The dedicated sync assistant remains visible because it uses another title.
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
	syncState.mu.Unlock()

	if syncer != nil {
		syncer.Stop()
	}
	atomic.AddUint64(&syncSnapshotGeneration, 1)

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
// layoutMode: grid | horizontal | vertical
func (a *App) SyncTileWindows(profileIds []string, masterProfileId string, layoutMode string) (*TileWindowsResult, error) {
	return a.syncTileWindowsLocal(profileIds, masterProfileId, layoutMode)
}

func (a *App) syncTileWindowsLocal(profileIds []string, masterProfileId string, layoutMode string) (*TileWindowsResult, error) {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()

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

	// Hint HWNDs from the active sync session, but re-validate as *main* frames.
	// Under multi-open, a stored handle can be a wallet popup after focus shift.
	activeWindows := make(map[string]windows.HWND)
	activeMasterID := ""
	syncState.mu.Lock()
	if syncState.active && syncState.masterHwnd != 0 {
		activeMasterID = syncState.masterId
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

	resolveMainHWND := func(profileID string, profile *BrowserProfile, hinted windows.HWND) windows.HWND {
		// Prefer live main-frame resolution so we do not tile a wallet popup.
		// Always keep hard fallbacks: over-strict filters must not make tile
		// return "没有可用的运行实例窗口" when Chrome is clearly open.
		if profile != nil && profile.Pid > 0 {
			if hwnd := findMainEnvironmentBrowserWindow(profile.Pid); hwnd != 0 {
				return hwnd
			}
			if hwnd, err := findProcessTreeWindow(profile.Pid); err == nil && hwnd != 0 {
				return hwnd
			}
			if hwnd, err := findProcessWindow(profile.Pid); err == nil && hwnd != 0 {
				return hwnd
			}
		}
		if hinted != 0 && isWindow(hinted) {
			title := getWindowTitle(hinted)
			if isMainEnvironmentBrowserFrame(hinted, title) {
				return hinted
			}
			// Sync-session handle still better than giving up entirely.
			return hinted
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
			continue
		}
		hwnd := resolveMainHWND(pid, profile, activeWindows[pid])
		if hwnd == 0 {
			continue
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
	resolvedLayout := layoutMode
	switch resolvedLayout {
	case "horizontal", "vertical", "grid":
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
		cols, rows = tileGridDimensions(n)
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
	idToHwnd := make(map[string]windows.HWND, n)
	var masterHandle windows.HWND
	for i, w := range wins {
		if i >= len(rects) {
			break
		}
		tiledIds = append(tiledIds, w.profileId)
		idToHwnd[w.profileId] = w.hwnd
		if w.profileId == effectiveMaster {
			masterHandle = w.hwnd
		}
	}
	if masterHandle == 0 && len(wins) > 0 {
		masterHandle = wins[0].hwnd
	}

	// Keep sync engine HWND snapshot aligned with the frames we just moved.
	// Rebuild followers in syncState.followerIds order so CDP debug ports stay
	// index-aligned (tile natural-sort must not reorder input/URL targets).
	if layoutActive && layoutSyncer != nil {
		syncState.mu.Lock()
		fids := append([]string(nil), syncState.followerIds...)
		syncState.mu.Unlock()
		followerHandles := make([]windows.HWND, 0, len(fids))
		seenFollower := make(map[windows.HWND]struct{}, len(fids))
		for _, id := range fids {
			h := idToHwnd[id]
			if h == 0 || h == masterHandle {
				continue
			}
			if _, dup := seenFollower[h]; dup {
				continue
			}
			seenFollower[h] = struct{}{}
			followerHandles = append(followerHandles, h)
		}
		// Tiled non-master not listed in followerIds (rare) — append last.
		for _, w := range wins {
			if w.hwnd == 0 || w.hwnd == masterHandle {
				continue
			}
			if _, ok := seenFollower[w.hwnd]; ok {
				continue
			}
			seenFollower[w.hwnd] = struct{}{}
			followerHandles = append(followerHandles, w.hwnd)
		}
		layoutSyncer.ReplaceWindowHandles(masterHandle, followerHandles)
	}
	syncState.mu.Lock()
	if syncState.active {
		if masterHandle != 0 {
			syncState.masterHwnd = masterHandle
		}
	}
	syncState.mu.Unlock()

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

// SyncCloseAll 关闭所有已选中实例
func (a *App) SyncCloseAll(profileIds []string) []string {
	closed := make([]string, 0)
	for _, profileID := range profileIds {
		if _, err := a.BrowserInstanceStop(profileID); err == nil {
			closed = append(closed, profileID)
		}
	}
	return closed
}
