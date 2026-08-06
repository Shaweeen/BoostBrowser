//go:build windows

package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"

	"golang.org/x/sys/windows"
)

func TestInputSyncerURLSyncDefaultOnUnlessExplicitlyDisabled(t *testing.T) {
	t.Setenv("BOOST_BROWSER_ENABLE_SYNC_URL_SYNC", "")
	if !syncURLSyncEnabled() {
		t.Fatalf("URL sync must be enabled by default in the isolated sync process")
	}

	t.Setenv("BOOST_BROWSER_ENABLE_SYNC_URL_SYNC", "0")
	if syncURLSyncEnabled() {
		t.Fatalf("URL sync should be disabled when BOOST_BROWSER_ENABLE_SYNC_URL_SYNC=0")
	}
}

func TestNewInputSyncerWithLoggerStoresLifecycleLogger(t *testing.T) {
	called := false
	s := NewInputSyncerWithLogger(func(event string, fields ...string) {
		called = event == "sync-test" && len(fields) == 1 && fields[0] == "ok=true"
	})

	s.lifecycle("sync-test", "ok=true")
	if !called {
		t.Fatalf("expected lifecycle logger to be called")
	}
}

func TestGetFollowerSnapshotReturnsCopy(t *testing.T) {
	s := NewInputSyncer()
	s.followerMu.Lock()
	s.followerSnapshot = []windows.HWND{1, 2}
	s.followerMu.Unlock()

	snap := s.getFollowerSnapshot()
	snap[0] = 99

	s.followerMu.RLock()
	defer s.followerMu.RUnlock()
	if s.followerSnapshot[0] != 1 {
		t.Fatalf("snapshot mutation leaked into syncer state: got %v", s.followerSnapshot[0])
	}
}

func TestSyncFollowerTargetsAlignedKeepsPortsWithSurvivors(t *testing.T) {
	// A closed follower (dead handle) must be dropped together with its debug
	// port so CDP URL/key dispatch never shifts onto the next environment.
	followers, ports := syncFollowerTargetsAligned(
		windows.HWND(1),
		[]windows.HWND{10, 0, 30, 40, 10},
		[]int{9221, 9222, 9223, 9224, 9225},
		func(windows.HWND) bool { return true },
	)
	want := []windows.HWND{10, 30, 40}
	if len(followers) != len(want) {
		t.Fatalf("followers=%v want %v", followers, want)
	}
	for i := range want {
		if followers[i] != want[i] {
			t.Fatalf("followers=%v want %v", followers, want)
		}
	}
	wantPorts := []int{9221, 9223, 9224}
	if len(ports) != len(wantPorts) {
		t.Fatalf("ports=%v want %v", ports, wantPorts)
	}
	for i := range wantPorts {
		if ports[i] != wantPorts[i] {
			t.Fatalf("ports must stay index-aligned with survivors: got %v want %v", ports, wantPorts)
		}
	}

	// Master handle itself is never a follower target.
	followers2, ports2 := syncFollowerTargetsAligned(
		windows.HWND(7),
		[]windows.HWND{7, 8},
		[]int{9001, 9002},
		nil,
	)
	if len(followers2) != 1 || followers2[0] != windows.HWND(8) || len(ports2) != 1 || ports2[0] != 9002 {
		t.Fatalf("master must be excluded with its port: %v %v", followers2, ports2)
	}
}

func TestPlanSyncSessionTargetsRepairsClosedReopenedFollower(t *testing.T) {
	// Follower closed and reopened: the fresh scan carries a new pid/hwnd/port,
	// and the session must adopt them so sync continues without a restart.
	byID := map[string]syncProfileCandidate{
		"master":   {profileID: "master", pid: 1, debugPort: 9001, hintHWND: windows.HWND(101)},
		"follower": {profileID: "follower", pid: 2, debugPort: 9002, hintHWND: windows.HWND(202)},
	}
	resolved := map[int]windows.HWND{1: 101, 2: 202}
	master, followers, ports, next, ok := planSyncSessionTargets("master", []string{"follower"}, nil, byID, resolved)
	if !ok || master != windows.HWND(101) {
		t.Fatalf("ok=%v master=%v", ok, master)
	}
	if len(followers) != 1 || followers[0] != windows.HWND(202) || len(ports) != 1 || ports[0] != 9002 {
		t.Fatalf("followers=%v ports=%v", followers, ports)
	}
	if len(next) != 1 || next[0] != windows.HWND(202) {
		t.Fatalf("next=%v", next)
	}
}

func TestPlanSyncSessionTargetsDropsClosedKeepsUnresolvedAlive(t *testing.T) {
	// A closed follower must be dropped; an alive follower whose frame is
	// temporarily unresolved keeps its previous target instead of flapping out.
	byID := map[string]syncProfileCandidate{
		"master": {profileID: "master", pid: 1, debugPort: 9001, hintHWND: windows.HWND(101)},
		"gone":   {profileID: "gone", pid: 0},                    // closed
		"flappy": {profileID: "flappy", pid: 3, debugPort: 9003}, // alive, window unresolved
	}
	resolved := map[int]windows.HWND{1: 101}
	prev := []windows.HWND{windows.HWND(101), 0, windows.HWND(303)}
	master, followers, ports, next, ok := planSyncSessionTargets("master", []string{"master", "gone", "flappy"}, prev, byID, resolved)
	if !ok {
		t.Fatal("session must stay repairable")
	}
	if master != windows.HWND(101) {
		t.Fatalf("master=%v", master)
	}
	if len(followers) != 1 || followers[0] != windows.HWND(303) {
		t.Fatalf("followers=%v want [303]", followers)
	}
	if len(ports) != 1 || ports[0] != 9003 {
		t.Fatalf("ports=%v want [9003]", ports)
	}
	if next[1] != 0 || next[2] != windows.HWND(303) {
		t.Fatalf("next=%v", next)
	}
}

func TestPlanSyncSessionTargetsMasterGoneReturnsNotOK(t *testing.T) {
	byID := map[string]syncProfileCandidate{
		"master": {profileID: "master", pid: 0},
	}
	_, _, _, _, ok := planSyncSessionTargets("master", nil, nil, byID, map[int]windows.HWND{})
	if ok {
		t.Fatal("master gone must not repair the session")
	}
}

func TestEscapePauseKeepsSessionAndTogglesImmediately(t *testing.T) {
	s := NewInputSyncer()
	atomic.StoreInt32(&s.active, 1)
	changed := make(chan bool, 2)
	s.SetPauseChangedHandler(func(paused bool) { changed <- paused })

	if paused := s.togglePausedFromEscape(); !paused || !s.IsActive() || !s.IsPaused() || s.canDispatch() {
		t.Fatalf("first Esc must pause without stopping session: active=%v paused=%v", s.IsActive(), s.IsPaused())
	}
	if paused := <-changed; !paused {
		t.Fatal("pause callback did not report paused state")
	}
	if paused := s.togglePausedFromEscape(); paused || !s.IsActive() || s.IsPaused() || !s.canDispatch() {
		t.Fatalf("second Esc must resume same session: active=%v paused=%v", s.IsActive(), s.IsPaused())
	}
	if paused := <-changed; paused {
		t.Fatal("resume callback did not report active state")
	}
}

func TestUrlsMatchForSyncSkipsEquivalentLocations(t *testing.T) {
	if !urlsMatchForSync("https://a.test/path", "https://a.test/path") {
		t.Fatal("identical URLs must match")
	}
	if !urlsMatchForSync("https://a.test/path/", "https://a.test/path") {
		t.Fatal("trailing slash must not force navigate")
	}
	if !urlsMatchForSync("https://a.test/path#", "https://a.test/path") {
		t.Fatal("bare hash must not force navigate")
	}
	if urlsMatchForSync("https://a.test/one", "https://a.test/two") {
		t.Fatal("different paths must not match")
	}
	if urlsMatchForSync("", "https://a.test/") {
		t.Fatal("empty URL must not match")
	}
}

func TestEscapeResumeReseedsURLBaselineWithoutStaleNavigate(t *testing.T) {
	// Pause/resume must only freeze and restore *input* sync. URL/editable
	// mirrors are re-baselined so resume cannot Page.navigate followers back to
	// a pre-pause master URL or overwrite their form values.
	s := NewInputSyncer()
	atomic.StoreInt32(&s.active, 1)
	s.lastSyncURL = "https://master.example/before-pause"
	s.lastFocusedEditableState = `{"kind":"input","value":"secret"}`

	if !s.togglePausedFromEscape() {
		t.Fatal("expected pause")
	}
	if s.lastSyncURL != "" || s.lastFocusedEditableState != "" {
		t.Fatalf("pause must drop URL/editable baseline: url=%q editable=%q", s.lastSyncURL, s.lastFocusedEditableState)
	}
	if atomic.LoadInt32(&s.urlSyncReseed) != 0 {
		t.Fatal("pause must not schedule a reseed push")
	}
	if s.canDispatch() {
		t.Fatal("paused session must not dispatch input")
	}

	if s.togglePausedFromEscape() {
		t.Fatal("expected resume")
	}
	if atomic.LoadInt32(&s.urlSyncReseed) != 1 {
		t.Fatal("resume must reseed baseline (record master URL only, no navigate)")
	}
	if s.lastSyncURL != "" || s.lastFocusedEditableState != "" {
		t.Fatalf("resume must not restore the pre-pause baseline: url=%q editable=%q", s.lastSyncURL, s.lastFocusedEditableState)
	}
	if !s.canDispatch() {
		t.Fatal("resumed session must dispatch input again")
	}
}

func TestLargeFollowerSchedulingUsesStableCadence(t *testing.T) {
	if got := syncMouseMoveThrottle(2); got != 6*time.Millisecond {
		t.Fatalf("small follower throttle=%v", got)
	}
	if got := syncMouseMoveThrottle(20); got != 28*time.Millisecond {
		t.Fatalf("20 follower throttle=%v", got)
	}
	if got := syncMouseDragThrottle(2); got != 4*time.Millisecond {
		t.Fatalf("drag throttle should be denser than idle hover: %v", got)
	}
	if got := syncMouseDragThrottle(20); got >= syncMouseMoveThrottle(20) {
		t.Fatalf("drag throttle must stay denser under large follower counts")
	}
	if got := syncPopupBoundsIntervalForFollowers(20); got != 900*time.Millisecond {
		t.Fatalf("20 follower popup interval=%v", got)
	}
	if got := syncPopupBoundsIntervalForFollowers(2); got != 450*time.Millisecond {
		t.Fatalf("small multi-open popup interval=%v", got)
	}
}

func TestPageWheelDeltaMappingForZoomAndHorizontal(t *testing.T) {
	// Ctrl+wheel keeps vertical delta for browser zoom; Shift+vertical becomes horizontal.
	deltaX, deltaY := pageWheelDeltas(WM_MOUSEWHEEL, 120, MK_CONTROL)
	if deltaX != 0 || deltaY != -120 {
		t.Fatalf("ctrl zoom deltas: dx=%v dy=%v", deltaX, deltaY)
	}
	deltaX, deltaY = pageWheelDeltas(WM_MOUSEWHEEL, 120, MK_SHIFT)
	if deltaX != -120 || deltaY != 0 {
		t.Fatalf("shift horizontal deltas: dx=%v dy=%v", deltaX, deltaY)
	}
	deltaX, deltaY = pageWheelDeltas(WM_MOUSEHWHEEL, 80, 0)
	if deltaX != 80 || deltaY != 0 {
		t.Fatalf("native horizontal wheel: dx=%v dy=%v", deltaX, deltaY)
	}
}

func TestWaitForFollowerCDPMatchOnlyForPopupLikeTargets(t *testing.T) {
	if waitForFollowerCDPMatch(cdpTarget{Type: "page", URL: "https://example.com/"}) {
		t.Fatal("normal web pages must not enable popup wait retries")
	}
	if !waitForFollowerCDPMatch(cdpTarget{
		Type:     "page",
		URL:      "chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/notification.html",
		OpenerID: "parent",
	}) {
		t.Fatal("wallet notification surfaces must keep popup wait")
	}
}

func TestFocusedMasterCDPTargetCacheIsShortLived(t *testing.T) {
	s := NewInputSyncer()
	s.mu.Lock()
	s.cachedMasterPort = 9222
	s.cachedMasterTarget = cdpTarget{ID: "page-1", Type: "page", WebSocketDebuggerUrl: "ws://127.0.0.1:9222/devtools/page/1"}
	s.cachedMasterTargetExp = time.Now().Add(focusedMasterCDPTargetCacheTTL)
	s.mu.Unlock()

	got, ok := s.focusedMasterCDPTarget(9222)
	if !ok || got.ID != "page-1" {
		t.Fatalf("expected cached master target, got ok=%v id=%q", ok, got.ID)
	}
	s.invalidateMasterCDPTargetCache()
	s.mu.Lock()
	if s.cachedMasterPort != 0 || s.cachedMasterTarget.ID != "" {
		t.Fatalf("cache was not cleared: port=%d id=%q", s.cachedMasterPort, s.cachedMasterTarget.ID)
	}
	s.mu.Unlock()
}

func TestEnqueuePageInputDropsMovesBeforeCriticalEvents(t *testing.T) {
	s := NewInputSyncer()
	atomic.StoreInt32(&s.active, 1)
	s.pageInputQueue = make(chan pageInputEvent, 1)
	// Fill the queue so the next enqueue hits the overflow path.
	s.pageInputQueue <- pageInputEvent{generation: 1, kind: pageInputMove, action: func() {}}

	s.enqueuePageInput(pageInputMove, func() {})
	if atomic.LoadInt32(&s.pageInputDrops) != 1 {
		t.Fatalf("move overflow should drop immediately, drops=%d", atomic.LoadInt32(&s.pageInputDrops))
	}

	// Critical path may wait briefly; with a full queue and no consumer it should
	// still count as a drop after the short wait, without panicking.
	s.enqueuePageInput(pageInputCritical, func() {})
	if atomic.LoadInt32(&s.pageInputDrops) < 1 {
		t.Fatal("critical overflow accounting missing")
	}
}

func TestPauseGenerationDropsDelayedEventAfterResume(t *testing.T) {
	s := NewInputSyncer()
	atomic.StoreInt32(&s.active, 1)
	s.SetRandomDelay(true, 40, 40)
	fired := make(chan struct{}, 1)
	s.dispatchWithRandomDelay(windows.HWND(1), func() { fired <- struct{}{} })
	s.togglePausedFromEscape()
	s.togglePausedFromEscape()
	select {
	case <-fired:
		t.Fatal("event queued before Esc pause must not fire after resume")
	case <-time.After(90 * time.Millisecond):
	}
}

func TestLayoutBoundarySuspendsDispatchAndDropsOldGeometryEvents(t *testing.T) {
	s := NewInputSyncer()
	atomic.StoreInt32(&s.active, 1)
	s.SetRandomDelay(true, 40, 40)
	fired := make(chan struct{}, 1)
	s.dispatchWithRandomDelay(windows.HWND(1), func() { fired <- struct{}{} })

	s.BeginLayoutUpdate()
	if s.canDispatch() {
		t.Fatal("input dispatch must be suspended while windows are moving")
	}
	s.EndLayoutUpdate()
	if !s.canDispatch() {
		t.Fatal("input dispatch must resume after the layout boundary")
	}
	select {
	case <-fired:
		t.Fatal("event mapped against the old window geometry must not fire")
	case <-time.After(90 * time.Millisecond):
	}
}

func TestPopupGeometryUpdateDoesNotBlockInputDispatch(t *testing.T) {
	// Geometry work must never freeze key/mouse (was thrashing wallets + input).
	s := NewInputSyncer()
	atomic.StoreInt32(&s.active, 1)
	atomic.StoreInt32(&s.popupUpdating, 1)
	if !s.canDispatch() {
		t.Fatal("popupUpdating must not block input dispatch")
	}
	atomic.StoreInt32(&s.layoutUpdating, 1)
	if s.canDispatch() {
		t.Fatal("layoutUpdating must still suspend input dispatch")
	}
}

func TestClearRuntimeStateReleasesCollectedSessionData(t *testing.T) {
	s := NewInputSyncer()
	s.masterHwnd = windows.HWND(11)
	s.followerHwnds = []windows.HWND{12, 13}
	s.masterPid = 101
	s.masterDebug = 9222
	s.followerDebug = []int{9223, 9224}
	s.lastSyncURL = "https://example.test"
	s.lastFocusedEditableState = "stale"
	s.followerSnapshot = []windows.HWND{12, 13}
	s.randomDelayNext[windows.HWND(12)] = time.Now()
	atomic.StoreInt32(&s.paused, 1)
	atomic.StoreInt32(&s.pointerInsideMaster, 1)
	atomic.StoreInt32(&s.mouseEnabled, 1)
	atomic.StoreInt32(&s.keyEnabled, 1)
	atomic.StoreInt32(&s.layoutUpdating, 1)
	atomic.StoreInt32(&s.popupUpdating, 1)

	s.clearRuntimeState()

	if s.masterHwnd != 0 || s.masterPid != 0 || s.masterDebug != 0 ||
		len(s.followerHwnds) != 0 || len(s.followerDebug) != 0 ||
		len(s.followerSnapshot) != 0 || len(s.randomDelayNext) != 0 ||
		s.cdpKeyQueue != nil || s.pageInputQueue != nil ||
		s.lastSyncURL != "" || s.lastFocusedEditableState != "" {
		t.Fatalf("runtime session data was not fully released: %+v", s.GetStats())
	}
	if s.IsPaused() || s.PointerInsideMaster() ||
		atomic.LoadInt32(&s.mouseEnabled) != 0 || atomic.LoadInt32(&s.keyEnabled) != 0 ||
		atomic.LoadInt32(&s.layoutUpdating) != 0 || atomic.LoadInt32(&s.popupUpdating) != 0 {
		t.Fatal("runtime atomic state was not reset")
	}
}

func TestSyncDebugLogEnabledByEnv(t *testing.T) {
	old := os.Getenv("BOOST_BROWSER_SYNC_DEBUG_LOG")
	defer os.Setenv("BOOST_BROWSER_SYNC_DEBUG_LOG", old)

	os.Unsetenv("BOOST_BROWSER_SYNC_DEBUG_LOG")
	if syncDebugLogEnabled() {
		t.Fatalf("sync debug log should be off by default")
	}
	os.Setenv("BOOST_BROWSER_SYNC_DEBUG_LOG", "true")
	if !syncDebugLogEnabled() {
		t.Fatalf("sync debug log should be enabled by env")
	}
}

func TestMainProcessCannotOwnInputSyncHooks(t *testing.T) {
	app := NewApp(t.TempDir(), false)
	if err := app.StartInputSync("master", []string{"follower"}); err == nil {
		t.Fatal("main process must reject input sync before creating native hooks")
	}
}

func TestWindowEnumerationCallbacksAreProcessReusable(t *testing.T) {
	if processWindowEnumCallback == 0 || chromeRenderChildEnumCallback == 0 || syncInputSurfaceEnumCallback == 0 || syncPopupBoundsEnumCallback == 0 || processMouseHookCallback == 0 || processKeyHookCallback == 0 {
		t.Fatal("expected reusable Win32 enumeration callbacks")
	}
	processCallback := processWindowEnumCallback
	renderCallback := chromeRenderChildEnumCallback
	surfaceCallback := syncInputSurfaceEnumCallback
	popupBoundsCallback := syncPopupBoundsEnumCallback
	mouseCallback := processMouseHookCallback
	keyCallback := processKeyHookCallback
	for i := 0; i < 10000; i++ {
		if processWindowEnumCallback != processCallback || chromeRenderChildEnumCallback != renderCallback || syncInputSurfaceEnumCallback != surfaceCallback || syncPopupBoundsEnumCallback != popupBoundsCallback || processMouseHookCallback != mouseCallback || processKeyHookCallback != keyCallback {
			t.Fatal("Win32 callback address changed; repeated lookup would exhaust the callback table")
		}
	}
}

func TestPopupSurfaceMatchScorePrefersSameSizedSameOffsetMenu(t *testing.T) {
	master := syncInputSurfaceCandidate{left: 700, top: 80, width: 320, height: 680, title: "MetaMask"}
	exact := syncInputSurfaceCandidate{left: 1700, top: 80, width: 320, height: 680, title: "MetaMask"}
	wrongSize := syncInputSurfaceCandidate{left: 1700, top: 80, width: 180, height: 60, title: "Other"}

	exactScore := popupSurfaceMatchScore(master, exact, 1700, 80)
	wrongScore := popupSurfaceMatchScore(master, wrongSize, 1700, 80)
	if exactScore >= wrongScore {
		t.Fatalf("same menu geometry should win: exact=%d wrong=%d", exactScore, wrongScore)
	}
}

func TestExpectedPopupSurfaceLeftPreservesNearestWindowEdge(t *testing.T) {
	if got := expectedPopupSurfaceLeft(700, 990, 0, 1000, 1000, 1800); got != 1500 {
		t.Fatalf("right-anchored menu mismatch: got=%d want=1500", got)
	}
	if got := expectedPopupSurfaceLeft(20, 320, 0, 1000, 1000, 1800); got != 1020 {
		t.Fatalf("left-anchored menu mismatch: got=%d want=1020", got)
	}
}

func TestHttpishAndExtensionCDPTargetHelpers(t *testing.T) {
	if !httpishCDPTarget(cdpTarget{URL: "https://beta.auralabs.pro/"}) {
		t.Fatal("https dapp must be httpish")
	}
	if httpishCDPTarget(cdpTarget{URL: "chrome-extension://abc/popup.html"}) {
		t.Fatal("extension must not be httpish")
	}
	if !extensionLikeCDPTarget(cdpTarget{URL: "chrome-extension://abc/notification.html"}) {
		t.Fatal("extension notification")
	}
	if !popupLikeCDPTarget(cdpTarget{URL: "chrome-extension://abc/notification.html"}) {
		t.Fatal("notification is popup-like")
	}
}

func TestPidBelongsToSyncSurfaceTree(t *testing.T) {
	if !pidBelongsToSyncSurface(10, 10, nil) {
		t.Fatal("same pid")
	}
	if pidBelongsToSyncSurface(11, 10, nil) {
		t.Fatal("child without tree must fail")
	}
	tree := map[int]int{11: 10, 10: 10, 12: 10}
	if !pidBelongsToSyncSurface(11, 10, tree) {
		t.Fatal("child in tree must match")
	}
	if pidBelongsToSyncSurface(99, 10, tree) {
		t.Fatal("foreign pid")
	}
}

func TestFocusedCDPTargetPreferOrdersPageBeforeExtension(t *testing.T) {
	// Document pure ranking used when focus probes fail: page mode must not
	// pick extension first (Connect Wallet regression). Implemented via
	// httpishCDPTarget / extensionLikeCDPTarget predicates exercised above;
	// full focusedCDPTargetPrefer needs a live CDP port so we assert ordering
	// helpers only here.
	pages := []cdpTarget{
		{URL: "chrome-extension://metamask/home.html", Type: "page"},
		{URL: "https://beta.auralabs.pro/stake", Type: "page"},
	}
	var chosen cdpTarget
	for _, target := range pages {
		if httpishCDPTarget(target) {
			chosen = target
			break
		}
	}
	if !httpishCDPTarget(chosen) {
		t.Fatal("page-prefer path must select https dapp over extension")
	}
}

func TestConstrainSyncPopupRectKeepsNestedMenuInsideTile(t *testing.T) {
	owner := winRect{Left: 0, Top: 0, Right: 500, Bottom: 700}
	popup := winRect{Left: 430, Top: 120, Right: 680, Bottom: 520}
	x, y, width, height, changed := constrainSyncPopupRect(popup, owner, 2)
	// Fits in cell: keep natural 250×400, slide left so fully inside.
	if !changed || x != 248 || y != 120 || width != 250 || height != 400 {
		t.Fatalf("nested menu clamp mismatch: x=%d y=%d width=%d height=%d changed=%v", x, y, width, height, changed)
	}
}

func TestConstrainSyncPopupRectForceFitsOversizedWalletIntoCell(t *testing.T) {
	owner := winRect{Left: 100, Top: 50, Right: 400, Bottom: 350} // available 296×296 after inset 2
	popup := winRect{Left: 20, Top: 10, Right: 700, Bottom: 800}  // natural 680×790
	x, y, width, height, changed := constrainSyncPopupRect(popup, owner, 2)
	// Force scale into owner so popup tracks environment tile (CM-style fit).
	if !changed || x != 102 || y != 52 || width != 296 || height != 296 {
		t.Fatalf("oversized wallet must force-fit cell: x=%d y=%d width=%d height=%d changed=%v", x, y, width, height, changed)
	}
}

func TestConstrainSyncPopupRectLeavesContainedPopupUnchanged(t *testing.T) {
	owner := winRect{Left: 0, Top: 0, Right: 800, Bottom: 700}
	popup := winRect{Left: 300, Top: 100, Right: 600, Bottom: 500}
	x, y, width, height, changed := constrainSyncPopupRect(popup, owner, 2)
	if changed || x != 300 || y != 100 || width != 300 || height != 400 {
		t.Fatalf("contained popup changed unexpectedly: x=%d y=%d width=%d height=%d changed=%v", x, y, width, height, changed)
	}
}

func TestSyncPopupCandidateRejectsMainWindowAndDevTools(t *testing.T) {
	owner := winRect{Left: 0, Top: 0, Right: 500, Bottom: 700}
	popup := winRect{Left: 200, Top: 100, Right: 480, Bottom: 600}
	if isSyncPopupSurfaceCandidate("Example - Google Chrome", popup, owner, false, false) {
		t.Fatal("unowned browser frame must not be constrained as a popup")
	}
	if isSyncPopupSurfaceCandidate("DevTools - chrome-extension://example", popup, owner, true, false) {
		t.Fatal("DevTools must not be constrained as a popup")
	}
	if !isSyncPopupSurfaceCandidate("", popup, owner, false, true) {
		t.Fatal("empty-title Chrome menu surface should be constrained when process-linked")
	}
	if isSyncPopupSurfaceCandidate("", owner, owner, false, true) {
		t.Fatal("full-size empty Chrome frame must not be adopted as a popup")
	}
	if !isSyncPopupSurfaceCandidate("MetaMask Notification", popup, owner, false, true) {
		t.Fatal("titled wallet notification must be confined to its environment cell")
	}
}

func TestSyncCDPTargetMatchPrefersCorrespondingPopup(t *testing.T) {
	master := cdpTarget{Type: "page", URL: "chrome-extension://wallet/popup.html#/sign", Title: "Wallet"}
	matching := cdpTarget{Type: "page", URL: "chrome-extension://wallet/popup.html#/sign", Title: "Wallet"}
	mainTab := cdpTarget{Type: "page", URL: "https://example.com/", Title: "Example"}
	if syncCDPTargetMatchScore(master, matching) <= syncCDPTargetMatchScore(master, mainTab) {
		t.Fatal("corresponding popup target must win over the profile main tab")
	}
}

func TestSyncCDPTargetMatchAllowsSamePopupRouteBase(t *testing.T) {
	master := cdpTarget{Type: "page", URL: "chrome-extension://wallet/popup.html#/confirm", Title: "Wallet"}
	follower := cdpTarget{Type: "page", URL: "chrome-extension://wallet/popup.html#/pending", Title: "Wallet"}
	if score := syncCDPTargetMatchScore(master, follower); score < 6000 {
		t.Fatalf("same popup document with a transient hash route should remain matchable: score=%d", score)
	}
}

func TestSyncCDPTargetMatchIgnoresTransientPopupQuery(t *testing.T) {
	master := cdpTarget{Type: "page", URL: "https://wallet.example/approve?request=master", Title: "Approve"}
	follower := cdpTarget{Type: "page", URL: "https://wallet.example/approve?request=follower", Title: "Approve"}
	if score := syncCDPTargetMatchScore(master, follower); score < 4000 {
		t.Fatalf("same popup document with per-profile query data should remain matchable: score=%d", score)
	}
}

func TestPopupLikeCDPTargetDoesNotConfuseMainPage(t *testing.T) {
	popup := cdpTarget{Type: "page", URL: "chrome-extension://wallet/popup.html#/confirm"}
	main := cdpTarget{Type: "page", URL: "https://example.com/"}
	if !popupLikeCDPTarget(popup) {
		t.Fatal("extension popup target should require a corresponding follower target")
	}
	if popupLikeCDPTarget(main) {
		t.Fatal("ordinary main page must not be classified as a popup")
	}
}

func TestPopupLikeCDPTargetCoversWalletUnlockAndHome(t *testing.T) {
	cases := []string{
		"chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/home.html#unlock",
		"chrome-extension://acmacodkjbdgmoleebolmdjonilkdbch/index.html#/unlock",
		"chrome-extension://wallet/notification.html",
		"chrome-extension://wallet/home.html#/onboarding/welcome",
		"chrome-extension://wallet/unknown-surface.html",
	}
	for _, url := range cases {
		if !popupLikeCDPTarget(cdpTarget{Type: "page", URL: url}) {
			t.Fatalf("wallet surface must be popup-like for match wait: %s", url)
		}
		if !extensionLikeCDPTarget(cdpTarget{Type: "page", URL: url}) {
			t.Fatalf("wallet surface must be extension-like: %s", url)
		}
	}
	if extensionLikeCDPTarget(cdpTarget{URL: "https://app.uniswap.org/"}) {
		t.Fatal("normal https page must not be extension-like")
	}
}

func TestSyncCDPTargetMatchPrefersSameExtensionID(t *testing.T) {
	master := cdpTarget{Type: "page", URL: "chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/home.html#unlock", Title: "MetaMask"}
	sameExt := cdpTarget{Type: "page", URL: "chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/popup.html", Title: "MetaMask"}
	otherSite := cdpTarget{Type: "page", URL: "https://example.com/", Title: "Example"}
	if syncCDPTargetMatchScore(master, sameExt) <= syncCDPTargetMatchScore(master, otherSite) {
		t.Fatal("same extension ID unlock/popup pair must outrank an unrelated main tab")
	}
	if chromeExtensionID(master.URL) != "nkbihfbeogaeaoehlefnkodbefgpgknn" {
		t.Fatalf("extension id parse failed: %q", chromeExtensionID(master.URL))
	}
}

func TestCDPKeyNameCoversDigits(t *testing.T) {
	if cdpKeyName(0x35) != "5" {
		t.Fatalf("top-row 5: got %q", cdpKeyName(0x35))
	}
	if cdpKeyName(0x65) != "5" {
		t.Fatalf("numpad 5: got %q", cdpKeyName(0x65))
	}
	if cdpKeyName(0x20) != " " {
		t.Fatalf("space key name: got %q", cdpKeyName(0x20))
	}
}

func TestBrowserWindowCloseChordsAreDetected(t *testing.T) {
	if !isBrowserWindowCloseChord(0x57, true, false, false) { // Ctrl+W
		t.Fatal("Ctrl+W must be blocked from follower replay")
	}
	if !isBrowserWindowCloseChord(0x57, true, false, true) { // Ctrl+Shift+W
		t.Fatal("Ctrl+Shift+W must be blocked from follower replay")
	}
	if !isBrowserWindowCloseChord(0x73, true, false, false) { // Ctrl+F4
		t.Fatal("Ctrl+F4 must be blocked from follower replay")
	}
	if !isBrowserWindowCloseChord(0x73, false, true, false) { // Alt+F4
		t.Fatal("Alt+F4 must be blocked from follower replay")
	}
	if isBrowserWindowCloseChord(0x41, true, false, false) { // Ctrl+A
		t.Fatal("Ctrl+A must still sync")
	}
}

func TestBrowserZoomVirtualKeys(t *testing.T) {
	for _, vk := range []uint32{0x30, 0x6B, 0x6D, 0xBB, 0xBD} {
		if !isBrowserZoomVirtualKey(vk) {
			t.Fatalf("expected zoom virtual key %#x", vk)
		}
	}
	if isBrowserZoomVirtualKey(0x41) {
		t.Fatal("ordinary keys must not be treated as browser zoom shortcuts")
	}
}

func TestCDPRuntimeValueSupportsProtocolAndLegacyShapes(t *testing.T) {
	value, ok := cdpRuntimeValue(map[string]any{"result": map[string]any{"type": "string", "value": "chrome://extensions"}})
	if !ok || value != "chrome://extensions" {
		t.Fatalf("nested CDP value mismatch: value=%v ok=%v", value, ok)
	}
	value, ok = cdpRuntimeValue(map[string]any{"value": "legacy"})
	if !ok || value != "legacy" {
		t.Fatalf("flat CDP value mismatch: value=%v ok=%v", value, ok)
	}
}

func TestPageMouseButtonUpMessagePreservesButton(t *testing.T) {
	tests := []struct {
		down uint32
		up   uint32
	}{
		{WM_LBUTTONDOWN, WM_LBUTTONUP},
		{WM_RBUTTONDOWN, WM_RBUTTONUP},
		{WM_MBUTTONDOWN, WM_MBUTTONUP},
	}
	for _, test := range tests {
		if got := pageMouseButtonUpMessage(test.down); got != test.up {
			t.Fatalf("button release mismatch: down=%#x got=%#x want=%#x", test.down, got, test.up)
		}
	}
}

func TestMainProcessPersistsBrowserRuntimeSnapshot(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root, false)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	app.browserMgr.Profiles["profile-1"] = &browser.Profile{
		ProfileId: "profile-1",
		Running:   true,
		Pid:       4321,
		DebugPort: 32123,
	}

	app.browserMgr.Mutex.Lock()
	app.persistBrowserRuntimeSnapshotLocked()
	app.browserMgr.Mutex.Unlock()

	data, err := os.ReadFile(filepath.Join(root, "data", "browser-runtime.json"))
	if err != nil {
		t.Fatalf("read runtime snapshot: %v", err)
	}
	var snapshot browserRuntimeSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode runtime snapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].ProfileID != "profile-1" || snapshot.Entries[0].PID != 4321 {
		t.Fatalf("unexpected runtime snapshot: %+v", snapshot)
	}
}
