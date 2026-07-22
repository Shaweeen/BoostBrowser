package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"boost-browser/backend/internal/database"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyPreparePreservesNumbersAndSkipsExistingDestination(t *testing.T) {
	appRoot := filepath.Join(t.TempDir(), "new")
	if err := os.MkdirAll(filepath.Join(appRoot, "data", "occupied"), 0755); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	app := NewApp(appRoot)
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, appRoot)
	app.browserMgr.Profiles = map[string]*browser.Profile{}

	oldRoot := filepath.Join(t.TempDir(), "old", "data")
	if err := os.MkdirAll(oldRoot, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := database.NewDB(filepath.Join(oldRoot, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	dao := browser.NewSQLiteProfileDAO(db.GetConn())
	for _, profile := range []*browser.Profile{
		{ProfileId: "profile-ready", ProfileName: "实例-12", UserDataDir: "ready"},
		{ProfileId: "profile-conflict", ProfileName: "实例-13", UserDataDir: "occupied"},
	} {
		if err := dao.Upsert(profile); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(oldRoot, profile.UserDataDir, "Default"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	preview, err := app.legacyDataRecoveryPreparePath(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer app.clearLegacyDataRecovery()
	if preview.Total != 2 || preview.Restorable != 1 || preview.Conflicts != 1 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if preview.Rows[0].EnvironmentNumber != 12 || preview.Rows[0].Status != "ready" {
		t.Fatalf("number/status not preserved: %+v", preview.Rows[0])
	}
}

func TestLegacyResolveDataRootAcceptsDataAndInstallParent(t *testing.T) {
	installRoot := t.TempDir()
	dataRoot := filepath.Join(installRoot, "data")
	if err := os.MkdirAll(dataRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataRoot, "app.db"), []byte("db"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{dataRoot, installRoot} {
		gotRoot, gotDB, err := legacyResolveDataRoot(selected)
		if err != nil {
			t.Fatalf("selected=%s: %v", selected, err)
		}
		if !backupSamePath(gotRoot, dataRoot) || !backupSamePath(gotDB, filepath.Join(dataRoot, "app.db")) {
			t.Fatalf("unexpected paths root=%s db=%s", gotRoot, gotDB)
		}
	}
}

func TestLegacyResolveDataRootRequiresDatabase(t *testing.T) {
	if _, _, err := legacyResolveDataRoot(t.TempDir()); err == nil {
		t.Fatal("expected missing app.db to fail exact recovery")
	}
}

func TestLegacyProfilePathsStayUnderActiveRoot(t *testing.T) {
	root := t.TempDir()
	profile := &browser.Profile{ProfileId: "profile-7", UserDataDir: filepath.Join(root, "old", "profile-7")}
	registered, destination := legacyResolveProfileDestination(root, profile)
	if registered != "profile-profile-7" || destination != filepath.Join(root, registered) {
		t.Fatalf("absolute legacy destination escaped root: %q %q", registered, destination)
	}

	profile.UserDataDir = "profile-8"
	registered, destination = legacyResolveProfileDestination(root, profile)
	if registered != "profile-8" || destination != filepath.Join(root, "profile-8") {
		t.Fatalf("relative destination changed unexpectedly: %q %q", registered, destination)
	}

	profile.UserDataDir = ".."
	registered, destination = legacyResolveProfileDestination(root, profile)
	if registered != "profile-profile-7" || destination != filepath.Join(root, registered) {
		t.Fatalf("parent traversal escaped root: %q %q", registered, destination)
	}
}

func TestLegacySkipRuntimeLockFile(t *testing.T) {
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket", "DevToolsActivePort"} {
		if !legacySkipRuntimeLockFile(filepath.Join("Default", name)) {
			t.Fatalf("runtime lock file not skipped: %s", name)
		}
	}
	if legacySkipRuntimeLockFile(filepath.Join("Default", "Cookies")) {
		t.Fatal("Cookies must be preserved")
	}
}
