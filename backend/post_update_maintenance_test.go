package backend

import (
	"boost-browser/backend/internal/browser"
	"os"
	"path/filepath"
	"testing"
)

func TestPostUpdateMaintenanceAlignsChrome148AndPreservesBrowserOwnedState(t *testing.T) {
	app, root := newUUIDReconcileTestApp(t)
	app.browserMgr.CoreDAO = browser.NewSQLiteCoreDAO(app.db.GetConn())
	coreDir := filepath.Join(root, filepath.FromSlash(bundledChrome148RelativeDir))
	if err := os.MkdirAll(coreDir, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(coreDir, filepath.FromSlash(browser.CoreExecutableCandidates()[0]))
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("chrome-for-testing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coreDir, "chrome-for-testing.marker"), []byte(bundledChrome148Version), 0o644); err != nil {
		t.Fatal(err)
	}

	profileID := "0ee496e2-ed0c-4f16-91c3-e8271a5540b1"
	profile := &browser.Profile{ProfileId: profileID, ProfileName: "环境", UserDataDir: profileID, CoreId: "old-core"}
	if err := app.browserMgr.ProfileDAO.Upsert(profile); err != nil {
		t.Fatal(err)
	}
	app.browserMgr.Profiles[profileID] = profile
	ownedFiles := map[string][]byte{
		filepath.Join(root, "data", profileID, "Default", "Preferences"):        []byte(`{"extensions":{"settings":{"wallet":{"state":1}}}}`),
		filepath.Join(root, "data", profileID, "Default", "Network", "Cookies"): []byte("opaque-cookie-db"),
		filepath.Join(root, "data", profileID, "Default", "Local State"):        []byte("opaque-application-state"),
	}
	for path, content := range ownedFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := app.RunPostUpdateMaintenance(); err != nil {
		t.Fatal(err)
	}
	defaultCore, ok := app.browserMgr.GetDefaultCore()
	if !ok || defaultCore.CoreId != bundledChrome148CoreID {
		t.Fatalf("Chrome 148 was not made global default: %+v", defaultCore)
	}
	if filepath.Clean(defaultCore.CorePath) != filepath.Clean(filepath.FromSlash(bundledChrome148RelativeDir)) {
		t.Fatalf("unexpected Chrome 148 path: %+v", defaultCore)
	}
	profiles, err := app.browserMgr.ProfileDAO.List()
	if err != nil || len(profiles) != 1 || profiles[0].CoreId != bundledChrome148CoreID {
		t.Fatalf("environment core was not aligned: profiles=%+v err=%v", profiles, err)
	}
	for path, want := range ownedFiles {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != string(want) {
			t.Fatalf("browser-owned state changed at %s: got=%q err=%v", path, got, readErr)
		}
	}
}
