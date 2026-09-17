//go:build windows

package backend

import (
	"fmt"
	"path/filepath"
	"slices"
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

	// followerTargets mirrors the syncer's dispatch list, aligned 1:1 with
	// followerIds (0 = env currently not dispatched). Maintained by the sync
	// start/stop, tile and live-repair paths so a follower whose process is
	// alive but whose frame is temporarily unresolved keeps its previous target
	// instead of flapping out of the session for one poll.
	followerTargets []windows.HWND
}

var syncSessionMu sync.Mutex
var syncSnapshotGeneration uint64

// maybeReloadSyncProfilesFromDB throttles the panel's periodic SQLite
// profile-table reload. The assistant is a separate process whose in-memory
// profile map is only loaded once; without this, environments created in the
// main client after the assistant started never appear in the sync list. The
// main client's map is already authoritative and must not be re-read here.
func (a *App) maybeReloadSyncProfilesFromDB() {
	if a == nil || !a.panelMode || a.browserMgr == nil || a.browserMgr.ProfileDAO == nil {
		return
	}
	if time.Since(a.syncProfileReloadAt) < 5*time.Second {
		return
	}
	a.syncProfileReloadAt = time.Now()
	a.browserMgr.ReloadProfilesFromDAO()
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

// RefreshSyncSnapshot is the explicit-refresh entry for the assistant UI. It
// bypasses the short-lived process-scan cache so a just-started environment is
// visible immediately instead of returning the stale list from the previous
// 2-second window. The cache is invalidated once; getSyncProfilesLocal then
// performs exactly one fresh scan.
func (a *App) RefreshSyncSnapshot() SyncSnapshot {
	invalidateBrowserProcessDiscoveryCache()
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()
	return SyncSnapshot{
		Profiles:   a.getSyncProfilesLocal(),
		Status:     a.getSyncStatusLocal(),
		Generation: atomic.LoadUint64(&syncSnapshotGeneration),
	}
}

// syncProfileCandidate is one live-discovered environment in the sync panel's
// refresh scan. It contains only current process/runtime data.
type syncProfileCandidate struct {
	profileID   string
	profileName string
	pid         int
	debugPort   int
	badge       int
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
	// The panel is a separate process: re-read the profile table from SQLite so
	// environments created in the main client become visible (throttled).
	a.maybeReloadSyncProfilesFromDB()
	// Discover from already-started client environments through the current
	// process tree and their live debug-port files. The selected, currently
	// open environment set is the sole source for the sync panel.
	byID := make(map[string]syncProfileCandidate, 64)
	rootPIDs := make([]int, 0, 64)

	// NOTE: do not hold browserMgr.Mutex here — List() locks internally.
	profiles := a.browserMgr.List()
	profileByDataDir := make(map[string]BrowserProfile, len(profiles))
	profileByFolder := make(map[string]BrowserProfile, len(profiles))
	for _, p := range profiles {
		ud := a.browserMgr.ResolveUserDataDir(&p)
		key := normalizeRuntimePathKey(ud)
		if key != "" {
			profileByDataDir[key] = p
			folder := strings.ToLower(filepath.Base(key))
			if folder != "" && folder != "." {
				// Last write wins if folders collide; full path match preferred first.
				if _, exists := profileByFolder[folder]; !exists {
					profileByFolder[folder] = p
				}
			}
		}
	}

	matchProfile := func(userDataDir string) (BrowserProfile, bool) {
		key := normalizeRuntimePathKey(userDataDir)
		if key == "" {
			return BrowserProfile{}, false
		}
		if p, ok := profileByDataDir[key]; ok {
			return p, true
		}
		// Fallback: folder name match (path prefix/suffix drift across roots).
		if p, ok := profileByFolder[strings.ToLower(filepath.Base(key))]; ok {
			return p, true
		}
		return BrowserProfile{}, false
	}

	// 1) Primary: live Chromium process scan (user-data-dir + debug port).
	// The sync panel is a separate process whose only runtime source is this
	// scan. The main client already owns authoritative runtime state (set by
	// its start/stop paths); running the PowerShell CIM query on every
	// sync-page poll inside the main Wails host is the same background-work
	// class implicated in the exit_code=2 watchdog history, so it is skipped
	// outside panel mode (the main client's own map + DevTools port files cover
	// the same environments).
	var liveProcesses []browserRuntimeProcess
	if a.panelMode {
		liveProcesses, _ = discoverBoostBrowserProcessesCached(a.appRoot)
	}
	byDataDir := make(map[string][]browserRuntimeProcess)
	portToPID := make(map[int]int, len(liveProcesses))
	for _, proc := range liveProcesses {
		key := normalizeRuntimePathKey(proc.UserDataDir)
		if key != "" {
			byDataDir[key] = append(byDataDir[key], proc)
		}
		if proc.DebugPort > 0 && proc.PID > 0 {
			if _, ok := portToPID[proc.DebugPort]; !ok {
				portToPID[proc.DebugPort] = proc.PID
			}
		}
	}
	for key, list := range byDataDir {
		p, ok := matchProfile(key)
		if !ok {
			// Try raw key as path for matchProfile
			p, ok = matchProfile(list[0].UserDataDir)
		}
		if !ok {
			continue
		}
		proc := list[0]
		for _, item := range list[1:] {
			if item.PID > 0 && item.DebugPort > 0 && (proc.PID <= 0 || item.PID < proc.PID) {
				proc = item
			}
		}
		if proc.PID <= 0 {
			continue
		}
		byID[p.ProfileId] = syncProfileCandidate{
			profileID:   p.ProfileId,
			profileName: p.ProfileName,
			pid:         proc.PID,
			debugPort:   proc.DebugPort,
			badge:       extractBadgeNumberFromName(p.ProfileName),
		}
		rootPIDs = append(rootPIDs, proc.PID)
	}

	// 2) Secondary: DevToolsActivePort for every configured profile dir.
	for _, p := range profiles {
		if _, exists := byID[p.ProfileId]; exists {
			continue
		}
		userDataDir := a.browserMgr.ResolveUserDataDir(&p)
		port, err := readBrowserDebugPortFile(userDataDir)
		if err != nil || port <= 0 {
			continue
		}
		if !canConnectDebugPort(port, 180*time.Millisecond) {
			continue
		}
		pid := portToPID[port]
		if pid <= 0 && p.Pid > 0 && isProcessAlive(p.Pid) {
			pid = p.Pid
		}
		byID[p.ProfileId] = syncProfileCandidate{
			profileID:   p.ProfileId,
			profileName: p.ProfileName,
			pid:         pid,
			debugPort:   port,
			badge:       extractBadgeNumberFromName(p.ProfileName),
		}
		if pid > 0 {
			rootPIDs = append(rootPIDs, pid)
		}
	}

	// The assistant must not use a persisted Running/PID flag as a third
	// discovery source: a reused PID can otherwise describe a different window.
	// Non-panel callers retain their own in-process runtime fallback for
	// diagnostics, but the standalone sync panel is live-discovery-only.
	if !a.panelMode {
		for _, p := range profiles {
			if !p.Running || p.Pid <= 0 {
				continue
			}
			if _, exists := byID[p.ProfileId]; exists {
				continue
			}
			if !isProcessAlive(p.Pid) {
				continue
			}
			byID[p.ProfileId] = syncProfileCandidate{
				profileID:   p.ProfileId,
				profileName: p.ProfileName,
				pid:         p.Pid,
				debugPort:   p.DebugPort,
				badge:       extractBadgeNumberFromName(p.ProfileName),
			}
			rootPIDs = append(rootPIDs, p.Pid)
		}
	}

	pidSeen := map[int]struct{}{}
	uniquePIDs := make([]int, 0, len(rootPIDs))
	for _, pid := range rootPIDs {
		if pid <= 0 {
			continue
		}
		if _, ok := pidSeen[pid]; ok {
			continue
		}
		pidSeen[pid] = struct{}{}
		uniquePIDs = append(uniquePIDs, pid)
	}

	resolvedWindows := findProcessTreeWindows(uniquePIDs)
	for _, pid := range uniquePIDs {
		resolvedWindows[pid] = resolveLiveMainEnvironmentFrame(pid, resolvedWindows[pid])
	}
	missing := 0
	for _, c := range byID {
		if c.pid > 0 && resolvedWindows[c.pid] == 0 {
			missing++
		}
	}
	if missing > 0 && (missing >= 2 || missing*3 >= len(byID)+1) {
		time.Sleep(220 * time.Millisecond)
		resolvedWindows = findProcessTreeWindows(uniquePIDs)
		for _, pid := range uniquePIDs {
			resolvedWindows[pid] = resolveLiveMainEnvironmentFrame(pid, resolvedWindows[pid])
		}
	}

	// Authoritative write-back: only live-discovered envs are Running. The sync
	// panel is a separate process whose in-memory map is otherwise stale, so the
	// live scan is its sole runtime owner. The main client keeps the runtime
	// state its own start/stop paths set — a transient scan miss there must
	// never flip a live environment to stopped.
	if a.browserMgr != nil && a.panelMode {
		a.browserMgr.Mutex.Lock()
		for id, p := range a.browserMgr.Profiles {
			if p == nil {
				continue
			}
			if _, ok := byID[id]; !ok {
				// A process-discovery miss is not proof that an environment
				// stopped. Preserve the last known runtime while Chromium is
				// still alive so an empty/slow CIM scan cannot poison sync.
				if p.Running && p.Pid > 0 && isProcessAlive(p.Pid) {
					continue
				}
				p.Running = false
				p.Pid = 0
				p.DebugPort = 0
				p.DebugReady = false
			}
		}
		for id, c := range byID {
			p := a.browserMgr.Profiles[id]
			if p == nil {
				p = &BrowserProfile{ProfileId: id, ProfileName: c.profileName}
				a.browserMgr.Profiles[id] = p
			}
			p.Running = true
			if c.pid > 0 {
				p.Pid = c.pid
			}
			if c.debugPort > 0 {
				p.DebugPort = c.debugPort
				p.DebugReady = true
			}
			if strings.TrimSpace(p.ProfileName) == "" {
				p.ProfileName = c.profileName
			}
		}
		a.browserMgr.Mutex.Unlock()
	}

	result := make([]SyncProfileInfo, 0, len(byID))
	for _, c := range byID {
		info := SyncProfileInfo{
			ProfileId:   c.profileID,
			ProfileName: c.profileName,
			Pid:         c.pid,
			DebugPort:   c.debugPort,
			Running:     true,
			BadgeNumber: c.badge,
		}
		hwnd := windows.HWND(0)
		if c.pid > 0 {
			hwnd = resolvedWindows[c.pid]
		}
		if hwnd != 0 && isWindow(hwnd) {
			info.Hwnd = int64(hwnd)
			info.Status = "running"
		} else {
			info.Status = "no_window"
		}
		result = append(result, info)
	}

	// Keep an active sync session pointed at the live environment set: if a
	// follower was closed and reopened (new PID/HWND/debug port), repair the
	// session targets now so sync continues without a restart or re-tile.
	a.repairActiveSyncSessionTargets(byID, resolvedWindows)

	return result
}

// planSyncSessionTargets computes the next master/follower dispatch targets for
// an active session from a fresh live scan. Followers whose environment is gone
// are dropped; followers whose process is alive but whose frame is temporarily
// unresolved keep their previous target (prevTargets, aligned with followerIds)
// so a single transient scan miss never flaps them out. Returns ok=false when
// the master is gone or unresolved — the session must be restarted by the user.
func planSyncSessionTargets(
	masterID string,
	followerIDs []string,
	prevTargets []windows.HWND,
	byID map[string]syncProfileCandidate,
	resolvedWindows map[int]windows.HWND,
) (master windows.HWND, followers []windows.HWND, ports []int, nextTargets []windows.HWND, ok bool) {
	resolveOne := func(id string, prev windows.HWND) (windows.HWND, int) {
		c, found := byID[id]
		if !found || c.pid <= 0 {
			return 0, 0 // environment closed
		}
		hwnd := resolvedWindows[c.pid]
		if hwnd == 0 {
			// A single scan can miss a frame while Chromium is moving between
			// processes. Keep only the target already owned by this active session;
			// never substitute a persisted HWND from another run.
			hwnd = prev
		}
		return hwnd, c.debugPort
	}
	master, _ = resolveOne(masterID, 0)
	if master == 0 {
		return 0, nil, nil, nil, false
	}
	nextTargets = make([]windows.HWND, len(followerIDs))
	followers = make([]windows.HWND, 0, len(followerIDs))
	ports = make([]int, 0, len(followerIDs))
	for i, id := range followerIDs {
		if strings.TrimSpace(id) == masterID {
			continue
		}
		prev := windows.HWND(0)
		if i < len(prevTargets) {
			prev = prevTargets[i]
		}
		hwnd, port := resolveOne(id, prev)
		nextTargets[i] = hwnd
		if hwnd == 0 {
			continue
		}
		followers = append(followers, hwnd)
		ports = append(ports, port)
	}
	return master, followers, ports, nextTargets, true
}

// repairActiveSyncSessionTargets re-resolves the active session's master and
// follower windows from the freshest live scan. A closed-and-reopened follower
// gets its new HWND and debug port so input + CDP URL sync keep working
// without stopping and restarting the session. The session member list
// (masterId/followerIds) is never changed here; the same isWindow filter the
// syncer applies is run before the change comparison so a kept-stale target is
// dropped consistently and never churns on every poll.
func (a *App) repairActiveSyncSessionTargets(byID map[string]syncProfileCandidate, resolvedWindows map[int]windows.HWND) {
	if a == nil || !a.panelMode {
		return
	}
	syncState.mu.Lock()
	syncer := syncState.syncer
	active := syncState.active
	masterID := strings.TrimSpace(syncState.masterId)
	followerIDs := append([]string(nil), syncState.followerIds...)
	prevTargets := append([]windows.HWND(nil), syncState.followerTargets...)
	syncState.mu.Unlock()
	if !active || syncer == nil || masterID == "" {
		return
	}
	master, followers, ports, nextTargets, ok := planSyncSessionTargets(masterID, followerIDs, prevTargets, byID, resolvedWindows)
	if !ok {
		// Master closed or unresolved: keep the old session; the user restarts.
		return
	}
	// Run the same dead-window filter ReplaceWindowTargets applies so the change
	// comparison below is accurate and the session never churns on a stale keep.
	filtered, filteredPorts := syncFollowerTargetsAligned(master, followers, ports, isWindow)
	curMaster, curFollowers, curPorts := syncer.CurrentTargets()
	if master != curMaster || !slices.Equal(filtered, curFollowers) || !slices.Equal(filteredPorts, curPorts) {
		syncer.ReplaceWindowTargets(master, filtered, filteredPorts)
	}
	// Persist a profile-ID-aligned hint list. The compact dispatch list omits a
	// closed follower; writing it back by position would shift follower #2 into
	// follower #1's slot and could route a later repair to the wrong window.
	kept := alignSessionFollowerTargets(master, nextTargets, isWindow)
	syncState.mu.Lock()
	if isWindow(master) {
		syncState.masterHwnd = master
	}
	syncState.followerTargets = kept
	syncState.mu.Unlock()
	a.lifecycleLog("sync-session-repair", fmt.Sprintf("followers=%d", len(filtered)))
}

func alignSessionFollowerTargets(master windows.HWND, candidates []windows.HWND, alive func(windows.HWND) bool) []windows.HWND {
	aligned := make([]windows.HWND, len(candidates))
	seen := map[windows.HWND]struct{}{master: {}}
	for i, hwnd := range candidates {
		if hwnd == 0 || hwnd == master || (alive != nil && !alive(hwnd)) {
			continue
		}
		if _, duplicate := seen[hwnd]; duplicate {
			continue
		}
		seen[hwnd] = struct{}{}
		aligned[i] = hwnd
	}
	return aligned
}

// removeSessionFollowerTarget removes one exact HWND while retaining the port
// paired with every surviving active target. A zero target means the profile
// was already absent from the dispatch set, so nothing else may be removed.
func removeSessionFollowerTarget(followers []windows.HWND, ports []int, removed windows.HWND) ([]windows.HWND, []int) {
	if removed == 0 {
		return append([]windows.HWND(nil), followers...), append([]int(nil), ports...)
	}
	keptFollowers := make([]windows.HWND, 0, len(followers))
	keptPorts := make([]int, 0, len(followers))
	for i, hwnd := range followers {
		if hwnd == removed {
			continue
		}
		keptFollowers = append(keptFollowers, hwnd)
		port := 0
		if i < len(ports) {
			port = ports[i]
		}
		keptPorts = append(keptPorts, port)
	}
	return keptFollowers, keptPorts
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
	// Live scan from already-started client envs (same as GetSyncProfiles).
	// The selected profile IDs are the session source of truth. Resolve their
	// current main frames from the live process tree below; do not reuse a
	// persisted HWND here because a closed/reopened environment can leave that
	// handle alive long enough for the first toolbar click to hit the wrong
	// window. Sync is input-only — never closes user work tabs.
	_ = a.getSyncProfilesLocal()

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
		return fmt.Errorf("主控实例未在运行：%s（请确认该环境已在主客户端启动）", masterProfileId)
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
	invalidFollowerIDs := make([]string, 0)
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
			invalidFollowerIDs = append(invalidFollowerIDs, fid)
			continue
		}
		if _, duplicateProcess := seenFollowerPIDs[fp.Pid]; duplicateProcess {
			invalidFollowerIDs = append(invalidFollowerIDs, fid)
			continue
		}
		seenFollowerPIDs[fp.Pid] = struct{}{}
		followers = append(followers, followerCandidate{id: fid, profile: *fp})
	}
	a.browserMgr.Mutex.Unlock()
	if len(invalidFollowerIDs) > 0 {
		return fmt.Errorf("所选跟随环境已不在运行清单或窗口重复：%s；请刷新后重新选择", strings.Join(invalidFollowerIDs, "、"))
	}

	type followerWindow struct {
		hwnd      windows.HWND
		debugPort int
	}
	rootPIDs := []int{masterSnapshot.Pid}
	for _, candidate := range followers {
		rootPIDs = append(rootPIDs, candidate.profile.Pid)
	}
	resolvedWindows := findProcessTreeWindows(rootPIDs)
	masterHwnd := resolveLiveMainEnvironmentFrame(masterSnapshot.Pid, resolvedWindows[masterSnapshot.Pid])
	if masterHwnd == 0 {
		return fmt.Errorf("未找到主控实例窗口")
	}
	resolved := make([]followerWindow, len(followers))
	for i, candidate := range followers {
		hwnd := resolveLiveMainEnvironmentFrame(candidate.profile.Pid, resolvedWindows[candidate.profile.Pid])
		if hwnd == 0 {
			return fmt.Errorf("所选跟随环境没有可用主窗口：%s；请刷新后重新选择", candidate.id)
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
			return fmt.Errorf("所选跟随环境窗口重复：%s；请刷新后重新选择", followers[i].id)
		}
		seenFollowerWindows[item.hwnd] = struct{}{}
		followerHwnds = append(followerHwnds, item.hwnd)
		followerDebugPorts = append(followerDebugPorts, item.debugPort)
		validFollowerIds = append(validFollowerIds, followers[i].id)
	}

	if len(followerHwnds) == 0 {
		return fmt.Errorf("没有可用的跟随实例")
	}

	// Input sync only — never close/navigate user tabs or extension pages.
	// (AdsPower/MoreLogin-style: profile owns session; sync does not wipe work.)
	masterDebugPort := masterSnapshot.DebugPort

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
	syncState.followerTargets = append([]windows.HWND(nil), followerHwnds...)
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
	syncState.followerTargets = nil
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
// layoutMode: grid | horizontal | vertical | custom:<columns>x<rows>
func (a *App) SyncTileWindows(profileIds []string, masterProfileId string, layoutMode string) (*TileWindowsResult, error) {
	return a.syncTileWindowsLocal(profileIds, masterProfileId, layoutMode)
}

func (a *App) syncTileWindowsLocal(profileIds []string, masterProfileId string, layoutMode string) (*TileWindowsResult, error) {
	syncSessionMu.Lock()
	defer syncSessionMu.Unlock()

	// Refresh live runtime state first: the panel map otherwise lags one poll,
	// so environments opened (or closed/reopened) right before this arrange call
	// would be missing from the tile. Same authoritative boundary as sync start.
	_ = a.getSyncProfilesLocal()

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

	// The active session may already own current target handles. They are only
	// short-lived in-session hints and are always re-validated below; no
	// persisted HWND or old layout data participates in arranging windows.
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

	resolveMainHWND := func(profile *BrowserProfile, hinted windows.HWND) windows.HWND {
		// Resolve the main frame from this environment's current process tree so
		// a wallet/OAuth popup can never become a tile target. This is fail-closed:
		// an unresolved environment is omitted until the user refreshes it.
		if profile != nil && profile.Pid > 0 {
			if hwnd := findMainEnvironmentBrowserWindow(profile.Pid); hwnd != 0 {
				return hwnd
			}
		}
		if hinted != 0 && isWindow(hinted) {
			title := getWindowTitle(hinted)
			if isMainEnvironmentBrowserFrame(hinted, title) {
				return hinted
			}
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
		hwnd := resolveMainHWND(profile, activeWindows[pid])
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
		followerPorts := make([]int, 0, len(fids))
		seenFollower := make(map[windows.HWND]struct{}, len(fids))
		// Debug ports must stay index-aligned with the HWND list so CDP URL/key
		// dispatch never targets the wrong environment after a closed follower
		// is dropped or a reopened one replaces a stale handle.
		portForID := func(id string) int {
			if p, ok := a.browserMgr.Profiles[id]; ok && p != nil {
				return p.DebugPort
			}
			return 0
		}
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
			followerPorts = append(followerPorts, portForID(id))
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
			followerPorts = append(followerPorts, portForID(w.profileId))
		}
		layoutSyncer.ReplaceWindowTargets(masterHandle, followerHandles, followerPorts)
	}
	syncState.mu.Lock()
	if syncState.active {
		if masterHandle != 0 {
			syncState.masterHwnd = masterHandle
		}
		// Keep the followerId-aligned target list in sync so a live repair can
		// fall back to the frames this tile pass just moved.
		targets := make([]windows.HWND, len(syncState.followerIds))
		for i, id := range syncState.followerIds {
			targets[i] = idToHwnd[id]
		}
		syncState.followerTargets = targets
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
	log := logger.New("SyncAPI")

	profileId = strings.TrimSpace(profileId)
	if profileId == "" {
		return fmt.Errorf("环境 ID 不能为空")
	}

	// 获取当前同步状态（一次性读取，避免竞态）
	syncState.mu.Lock()
	if !syncState.active || syncState.syncer == nil {
		syncState.mu.Unlock()
		return fmt.Errorf("同步未启动")
	}
	masterId := strings.TrimSpace(syncState.masterId)
	followerIds := append([]string(nil), syncState.followerIds...)
	syncState.mu.Unlock()

	if profileId == masterId {
		return fmt.Errorf("主控环境不能同时作为跟随者")
	}

	// 检查是否已在跟随列表
	for _, fid := range followerIds {
		if fid == profileId {
			return fmt.Errorf("环境 %s 已在跟随列表中", profileId)
		}
	}

	// 查找环境并验证状态
	a.browserMgr.Mutex.Lock()
	profile, ok := a.browserMgr.Profiles[profileId]
	if !ok || profile == nil {
		a.browserMgr.Mutex.Unlock()
		return fmt.Errorf("未找到环境：%s", profileId)
	}
	if !profile.Running || profile.Pid <= 0 {
		a.browserMgr.Mutex.Unlock()
		return fmt.Errorf("环境 %s 未在运行", profileId)
	}
	profileSnapshot := *profile
	a.browserMgr.Mutex.Unlock()

	// 解析窗口句柄
	resolvedWindows := findProcessTreeWindows([]int{profileSnapshot.Pid})
	hwnd := resolveLiveMainEnvironmentFrame(profileSnapshot.Pid, resolvedWindows[profileSnapshot.Pid])
	if hwnd == 0 {
		return fmt.Errorf("未找到环境 %s 的窗口", profileId)
	}

	// 原子更新同步状态：一次性获取锁，完成所有更新
	syncState.mu.Lock()
	// 再次检查状态（可能在等待锁期间被其他操作修改）
	if !syncState.active || syncState.syncer == nil {
		syncState.mu.Unlock()
		return fmt.Errorf("同步已停止")
	}
	// 再次检查是否已存在（避免竞态）
	for _, fid := range syncState.followerIds {
		if fid == profileId {
			syncState.mu.Unlock()
			return fmt.Errorf("环境 %s 已在跟随列表中", profileId)
		}
	}
	syncer := syncState.syncer
	syncState.mu.Unlock()

	// A membership edit must not re-discover every existing follower: a brief
	// process-enumeration miss would otherwise silently drop an already synced
	// window. Keep the active dispatch set and append only the user-selected,
	// freshly resolved environment.
	curMaster, curFollowers, curPorts := syncer.CurrentTargets()
	for _, current := range curFollowers {
		if current == hwnd {
			return fmt.Errorf("环境 %s 的窗口已在同步列表中", profileId)
		}
	}
	curFollowers = append(curFollowers, hwnd)
	curPorts = append(curPorts, profileSnapshot.DebugPort)

	syncState.mu.Lock()
	if !syncState.active || syncState.syncer != syncer {
		syncState.mu.Unlock()
		return fmt.Errorf("同步已停止")
	}
	syncState.followerIds = append(syncState.followerIds, profileId)
	syncState.followerTargets = append(syncState.followerTargets, hwnd)
	newFollowerIds := append([]string(nil), syncState.followerIds...)
	syncState.mu.Unlock()
	syncer.ReplaceWindowTargets(curMaster, curFollowers, curPorts)

	atomic.AddUint64(&syncSnapshotGeneration, 1)
	log.Info("已添加跟随环境",
		logger.F("profile_id", profileId),
		logger.F("total_followers", fmt.Sprintf("%d", len(newFollowerIds)-1)),
	)
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
	log := logger.New("SyncAPI")

	profileId = strings.TrimSpace(profileId)
	if profileId == "" {
		return fmt.Errorf("环境 ID 不能为空")
	}

	// 原子更新同步状态：一次性获取锁，完成查找和更新
	syncState.mu.Lock()
	if !syncState.active || syncState.syncer == nil {
		syncState.mu.Unlock()
		return fmt.Errorf("同步未启动")
	}
	oldFollowerIds := syncState.followerIds
	oldFollowerTargets := syncState.followerTargets

	// 查找并移除
	found := false
	removedTarget := windows.HWND(0)
	newFollowerIds := make([]string, 0, len(oldFollowerIds))
	newTargets := make([]windows.HWND, 0, len(oldFollowerIds))
	for i, fid := range oldFollowerIds {
		if fid == profileId {
			found = true
			if i < len(oldFollowerTargets) {
				removedTarget = oldFollowerTargets[i]
			}
			continue
		}
		newFollowerIds = append(newFollowerIds, fid)
		if i < len(oldFollowerTargets) {
			newTargets = append(newTargets, oldFollowerTargets[i])
		}
	}
	if !found {
		syncState.mu.Unlock()
		return fmt.Errorf("环境 %s 不在跟随列表中", profileId)
	}
	syncState.followerIds = newFollowerIds
	syncState.followerTargets = newTargets
	syncState.mu.Unlock()

	// Removing a follower is a pure session-state operation. Do not rescan the
	// remaining windows: their active HWND/port pairs stay untouched.
	curMaster, curFollowers, curPorts := syncState.syncer.CurrentTargets()
	followerHwnds, followerPorts := removeSessionFollowerTarget(curFollowers, curPorts, removedTarget)
	syncState.syncer.ReplaceWindowTargets(curMaster, followerHwnds, followerPorts)

	atomic.AddUint64(&syncSnapshotGeneration, 1)
	log.Info("已移除跟随环境",
		logger.F("profile_id", profileId),
		logger.F("total_followers", fmt.Sprintf("%d", len(newFollowerIds))),
	)
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
