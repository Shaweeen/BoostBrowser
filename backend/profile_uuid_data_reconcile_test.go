package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"boost-browser/backend/internal/database"
	"os"
	"path/filepath"
	"testing"
)

func newUUIDReconcileTestApp(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := database.NewDB(filepath.Join(root, "data", "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	app := NewApp(root)
	app.config = cfg
	app.db = db
	app.browserMgr = browser.NewManager(cfg, root)
	app.browserMgr.ProfileDAO = browser.NewSQLiteProfileDAO(db.GetConn())
	app.browserMgr.Profiles = map[string]*browser.Profile{}
	return app, root
}

func TestReconcileProfileUUIDDataAttachesMissingEnvironmentInPlace(t *testing.T) {
	app, root := newUUIDReconcileTestApp(t)
	id := "0ee496e2-ed0c-4f16-91c3-e8271a5540b1"
	preferences := filepath.Join(root, "data", id, "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(preferences), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"extensions":{"settings":{"nkbihfbeogaeaoehlefnkodbefgpgknn":{"state":1}}}}`)
	if err := os.WriteFile(preferences, original, 0o600); err != nil {
		t.Fatal(err)
	}
	stateFiles := map[string][]byte{
		filepath.Join(root, "data", id, "Local State"):                   []byte(`{"profile":{"last_used":"Default"}}`),
		filepath.Join(root, "data", id, "Default", "Network", "Cookies"): []byte("opaque-cookie-database"),
	}
	for path, content := range stateFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	count, err := app.reconcileProfileUUIDData()
	if err != nil || count != 1 {
		t.Fatalf("reconcile count=%d err=%v", count, err)
	}
	profile := app.browserMgr.Profiles[id]
	if profile == nil || profile.UserDataDir != id {
		t.Fatalf("UUID environment was not attached to matching data directory: %+v", profile)
	}
	after, err := os.ReadFile(preferences)
	if err != nil || string(after) != string(original) {
		t.Fatalf("browser application/extension state was modified: %q err=%v", after, err)
	}
	for path, want := range stateFiles {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != string(want) {
			t.Fatalf("Cookie/application state was modified at %s: %q err=%v", path, got, readErr)
		}
	}
}

func TestReconcileProfileUUIDDataRealignsOnlyMissingStalePath(t *testing.T) {
	app, root := newUUIDReconcileTestApp(t)
	id := "0ee496e2-ed0c-4f16-91c3-e8271a5540b1"
	profile := &browser.Profile{ProfileId: id, ProfileName: "环境", UserDataDir: "stale-folder"}
	if err := app.browserMgr.ProfileDAO.Upsert(profile); err != nil {
		t.Fatal(err)
	}
	app.browserMgr.Profiles[id] = profile
	if err := os.MkdirAll(filepath.Join(root, "data", id, "Default"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", id, "Local State"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	count, err := app.reconcileProfileUUIDData()
	if err != nil || count != 1 || app.browserMgr.Profiles[id].UserDataDir != id {
		t.Fatalf("stale path was not realigned: count=%d profile=%+v err=%v", count, app.browserMgr.Profiles[id], err)
	}
}

func TestReconcileProfileUUIDDataRejectsTwoLiveDirectories(t *testing.T) {
	app, root := newUUIDReconcileTestApp(t)
	id := "0ee496e2-ed0c-4f16-91c3-e8271a5540b1"
	profile := &browser.Profile{ProfileId: id, ProfileName: "环境", UserDataDir: "other-live-data"}
	if err := app.browserMgr.ProfileDAO.Upsert(profile); err != nil {
		t.Fatal(err)
	}
	app.browserMgr.Profiles[id] = profile
	for _, dir := range []string{filepath.Join(root, "data", id), filepath.Join(root, "data", "other-live-data")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.reconcileProfileUUIDData(); err == nil {
		t.Fatal("ambiguous live data directories must stop update reconciliation")
	}
}
