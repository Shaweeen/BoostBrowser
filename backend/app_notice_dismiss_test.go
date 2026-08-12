package backend

import (
	"os"
	"path/filepath"
	"testing"

	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
)

// TestNoticeDismissalStatePersists verifies the notice-dismiss state file is a
// single-source-of-truth record that survives across App instances (i.e. across
// client restarts and upgrades), and that both notice families round-trip.
func TestNoticeDismissalStatePersists(t *testing.T) {
	app := &App{appRoot: t.TempDir()}

	if err := app.BrowserExtensionIntegrityDismissNotice([]string{"p1", "p2", " p2 "}); err != nil {
		t.Fatalf("dismiss notice failed: %v", err)
	}
	dismissed := app.dismissedExtensionIncompleteSet()
	if len(dismissed) != 2 || !dismissed["p1"] || !dismissed["p2"] {
		t.Fatalf("extension incomplete set mismatch: %+v", dismissed)
	}

	// A brand-new App instance (fresh start / upgraded client) must still see
	// the record — this is the “记录留档” guarantee.
	reopened := &App{appRoot: app.appRoot}
	dismissedAfterRestart := reopened.dismissedExtensionIncompleteSet()
	if !dismissedAfterRestart["p1"] || !dismissedAfterRestart["p2"] {
		t.Fatalf("dismissal did not persist across restart: %+v", dismissedAfterRestart)
	}

	// Clearing re-enables the notice.
	if err := reopened.BrowserExtensionIntegrityClearDismissed(); err != nil {
		t.Fatalf("clear dismissed failed: %v", err)
	}
	if len(reopened.dismissedExtensionIncompleteSet()) != 0 {
		t.Fatal("clear dismissed did not take effect")
	}
}

// TestLegacyFolderDismissalDeletesAndPersists verifies ignore permanently
// deletes orphan folders under the data root and records them so they never
// reappear. Scan without pending returns empty (no startup scan).
func testAppWithDataRoot(t *testing.T) (*App, string) {
	t.Helper()
	root := t.TempDir()
	app := NewApp(root)
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = "data"
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	return app, app.backupResolveUserDataRoot(cfg)
}

func TestLegacyFolderDismissalDeletesAndPersists(t *testing.T) {
	app, activeRoot := testAppWithDataRoot(t)
	orphan1 := filepath.Join(activeRoot, "orphan-a")
	orphan2 := filepath.Join(activeRoot, "orphan-b")
	for _, d := range []string{orphan1, orphan2} {
		if err := os.MkdirAll(filepath.Join(d, "Default"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "Default", "Preferences"), []byte(`{}`), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Without pending, scan returns no folders.
	preview, err := app.BrowserLegacyDataAutoScan(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Folders) != 0 {
		t.Fatalf("startup/no-pending scan must be empty: %#v", preview)
	}

	// force scan sees orphans
	force, err := app.BrowserLegacyDataAutoScan(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(force.Folders) < 2 {
		t.Fatalf("force scan should see orphans: %#v", force)
	}

	keys := []string{"orphan-a", "orphan-b"}
	if err := app.BrowserLegacyDataDismissFolders(keys); err != nil {
		t.Fatalf("dismiss legacy folders failed: %v", err)
	}
	if _, err := os.Stat(orphan1); !os.IsNotExist(err) {
		t.Fatalf("orphan-a should be deleted, err=%v", err)
	}
	if _, err := os.Stat(orphan2); !os.IsNotExist(err) {
		t.Fatalf("orphan-b should be deleted, err=%v", err)
	}
	dismissed := app.dismissedLegacyFolderSet()
	if !dismissed["orphan-a"] || !dismissed["orphan-b"] {
		t.Fatalf("legacy folder set mismatch: %+v", dismissed)
	}
}

func TestLegacyScanOnlyWhenPending(t *testing.T) {
	app, activeRoot := testAppWithDataRoot(t)
	orphan := filepath.Join(activeRoot, "leftover-1")
	if err := os.MkdirAll(filepath.Join(orphan, "Default"), 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(orphan, "Default", "Preferences"), []byte(`{}`), 0644)

	p, err := app.BrowserLegacyDataAutoScan(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Folders) != 0 {
		t.Fatal("without pending, scan must not surface leftovers")
	}
	app.armLegacyScanAfterProfileDelete()
	p2, err := app.BrowserLegacyDataAutoScan(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Folders) == 0 {
		t.Fatal("after delete arm, scan must surface leftovers")
	}
}

// TestIntegrityScanCountsDismissedSeparately verifies the scan message format
// treats acknowledged environments as “已忽略” instead of re-warning.
func TestIntegrityScanCountsDismissedSeparately(t *testing.T) {
	r := &ExtensionIntegrityScanResult{}
	r.appendIncomplete("p1", nil)
	r.appendIncomplete("p2", map[string]bool{"p2": true})
	r.appendIncomplete("p3", map[string]bool{"p2": true, "p3": true})

	if r.Incomplete != 3 || r.DismissedIncomplete != 2 || len(r.IncompleteIDs) != 1 || r.IncompleteIDs[0] != "p1" {
		t.Fatalf("incomplete accounting mismatch: %+v", r)
	}
	msg := formatIntegrityScanMessage(r)
	if msg == "部分环境扩展尚未完成首次适配；打开对应环境一次即可，完整后不再巡检" {
		t.Fatalf("dismissed environments must not produce the default warning: %s", msg)
	}

	// All dismissed → informational only, no warning wording.
	r2 := &ExtensionIntegrityScanResult{}
	r2.appendIncomplete("p1", map[string]bool{"p1": true})
	msg2 := formatIntegrityScanMessage(r2)
	if msg2 == "部分环境扩展尚未完成首次适配；打开对应环境一次即可，完整后不再巡检" {
		t.Fatalf("all-dismissed scan must not warn: %s", msg2)
	}
}

// TestNoticeDismissStateFilePathSanity guards the storage location so future
// refactors cannot silently move/delete the record.
func TestNoticeDismissStateFilePathSanity(t *testing.T) {
	app := &App{appRoot: t.TempDir()}
	path := app.noticeDismissPath()
	if filepath.Base(path) != noticeDismissStateFile {
		t.Fatalf("unexpected dismiss state file name: %s", path)
	}
	if filepath.Dir(path) == "" {
		t.Fatal("dismiss state must live under the data root")
	}
}
