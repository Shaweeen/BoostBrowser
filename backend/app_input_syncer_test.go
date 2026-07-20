//go:build windows

package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
	if processWindowEnumCallback == 0 || chromeRenderChildEnumCallback == 0 || syncInputSurfaceEnumCallback == 0 || browserWindowRestoreEnumCallback == 0 || processMouseHookCallback == 0 || processKeyHookCallback == 0 {
		t.Fatal("expected reusable Win32 enumeration callbacks")
	}
	processCallback := processWindowEnumCallback
	renderCallback := chromeRenderChildEnumCallback
	surfaceCallback := syncInputSurfaceEnumCallback
	restoreCallback := browserWindowRestoreEnumCallback
	mouseCallback := processMouseHookCallback
	keyCallback := processKeyHookCallback
	for i := 0; i < 10000; i++ {
		if processWindowEnumCallback != processCallback || chromeRenderChildEnumCallback != renderCallback || syncInputSurfaceEnumCallback != surfaceCallback || browserWindowRestoreEnumCallback != restoreCallback || processMouseHookCallback != mouseCallback || processKeyHookCallback != keyCallback {
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
