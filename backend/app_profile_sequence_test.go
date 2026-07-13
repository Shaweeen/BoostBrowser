package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"testing"
)

func TestReserveProfileNameRangeSkipsExistingAndHistoricalNumbers(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	app.browserMgr.Profiles["existing"] = &BrowserProfile{
		ProfileId:   "existing",
		ProfileName: "实例-3",
	}

	start, err := app.reserveProfileNameRange("实例", 1, 2)
	if err != nil {
		t.Fatalf("first reservation failed: %v", err)
	}
	if start != 4 {
		t.Fatalf("expected first reservation to start at 4, got %d", start)
	}

	// Even if the old environment is later deleted, the persisted high-water
	// mark prevents a future batch from reusing its historical number.
	app.browserMgr.Profiles = make(map[string]*BrowserProfile)
	start, err = app.reserveProfileNameRange("实例", 1, 2)
	if err != nil {
		t.Fatalf("second reservation failed: %v", err)
	}
	if start != 6 {
		t.Fatalf("expected historical reservation to continue at 6, got %d", start)
	}
}

func TestReserveProfileNameRangeKeepsPrefixesIndependent(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)

	first, err := app.reserveProfileNameRange("实例", 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.reserveProfileNameRange("工作", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 1 {
		t.Fatalf("expected independent prefix sequences, got 实例=%d 工作=%d", first, second)
	}
}
