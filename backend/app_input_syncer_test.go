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
	if got := syncPopupBoundsIntervalForFollowers(20); got != 450*time.Millisecond {
		t.Fatalf("20 follower popup interval=%v", got)
	}
	if got := syncPopupBoundsIntervalForFollowers(2); got != 280*time.Millisecond {
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

func TestPopupGeometryUpdateSuspendsDispatch(t *testing.T) {
	s := NewInputSyncer()
	atomic.StoreInt32(&s.active, 1)
	atomic.StoreInt32(&s.popupUpdating, 1)
	if s.canDispatch() {
		t.Fatal("input dispatch must pause during popup geometry updates")
	}
	atomic.StoreInt32(&s.popupUpdating, 0)
	if !s.canDispatch() {
		t.Fatal("input dispatch must resume after popup geometry updates")
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
	master := syncInputSurfaceCandidate{left: 700, top: 80, width: 320, height: 680}
	exact := syncInputSurfaceCandidate{left: 1700, top: 80, width: 320, height: 680}
	wrongSize := syncInputSurfaceCandidate{left: 1700, top: 80, width: 180, height: 60}

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

func TestConstrainSyncPopupRectKeepsNestedMenuInsideTile(t *testing.T) {
	owner := winRect{Left: 0, Top: 0, Right: 500, Bottom: 700}
	popup := winRect{Left: 430, Top: 120, Right: 680, Bottom: 520}
	x, y, width, height, changed := constrainSyncPopupRect(popup, owner, 2)
	if !changed || x != 248 || y != 120 || width != 250 || height != 400 {
		t.Fatalf("nested menu clamp mismatch: x=%d y=%d width=%d height=%d changed=%v", x, y, width, height, changed)
	}
}

func TestConstrainSyncPopupRectShrinksOversizedPopupToTile(t *testing.T) {
	owner := winRect{Left: 100, Top: 50, Right: 400, Bottom: 350}
	popup := winRect{Left: 20, Top: 10, Right: 700, Bottom: 800}
	x, y, width, height, changed := constrainSyncPopupRect(popup, owner, 2)
	// Natural 680x790 into 296x296 cell uses uniform scale min(296/680, 296/790)
	// → ~254x296, not a square independent clamp.
	if !changed || x != 102 || y != 52 || width != 254 || height != 296 {
		t.Fatalf("oversized popup proportional shrink mismatch: x=%d y=%d width=%d height=%d changed=%v", x, y, width, height, changed)
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

func TestPanelSnapshotReplacesProcessLocalRuntimeWithMainClientState(t *testing.T) {
	currentPID := os.Getpid()
	root := t.TempDir()
	app := NewApp(root, true)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	app.browserMgr.Profiles["profile-1"] = &browser.Profile{
		ProfileId: "profile-1",
		Running:   true,
		Pid:       currentPID + 100000,
		DebugPort: 32124,
	}
	snapshot := browserRuntimeSnapshot{Entries: []browserRuntimeSnapshotEntry{{
		ProfileID: "profile-1",
		PID:       currentPID,
		DebugPort: 32123,
	}}}
	live, total := app.applyBrowserRuntimeSnapshotData(snapshot)
	if live != 1 || total != 1 {
		t.Fatalf("unexpected snapshot counts: live=%d total=%d", live, total)
	}
	profile := app.browserMgr.Profiles["profile-1"]
	if profile.Pid != currentPID || profile.DebugPort != 32123 || !profile.Running {
		t.Fatalf("panel did not inherit main-client runtime: %+v", profile)
	}

	app.applyBrowserRuntimeSnapshotData(browserRuntimeSnapshot{})
	if profile.Running || profile.Pid != 0 || profile.DebugPort != 0 {
		t.Fatalf("an empty main-client snapshot must clear panel runtime state: %+v", profile)
	}
}
