package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"boost-browser/backend/internal/database"
	"os"
	"path/filepath"
	"testing"
)

func TestStartupCompatibilityAttachesRawProfileWithoutRewritingBrowserData(t *testing.T) {
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	profileRoot := filepath.Join(dataRoot, "environment-42")
	preferences := filepath.Join(profileRoot, "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(preferences), 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"extensions":{"settings":{"wallet":{"state":1}}}}`)
	if err := os.WriteFile(preferences, original, 0644); err != nil {
		t.Fatal(err)
	}

	db, err := database.NewDB(filepath.Join(dataRoot, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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

	app.initializeActiveDataCompatibility(dataRoot, true)
	status := app.GetStartupDataCompatibilityStatus()
	if status.AutoRecovered != 1 {
		t.Fatalf("expected one recovered profile: %+v", status)
	}
	profiles, err := app.browserMgr.ProfileDAO.List()
	if err != nil || len(profiles) != 1 || profiles[0].UserDataDir != "environment-42" {
		t.Fatalf("raw profile was not attached: profiles=%+v err=%v", profiles, err)
	}
	after, err := os.ReadFile(preferences)
	if err != nil || string(after) != string(original) {
		t.Fatalf("browser/wallet data was rewritten: %q err=%v", after, err)
	}
}
