package backend

import (
	"path/filepath"
	"testing"
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

// TestLegacyFolderDismissalPersists verifies that “确认不导入” 的旧数据文件夹被
// 记录后不再重复出现，且清空后可恢复提醒。
func TestLegacyFolderDismissalPersists(t *testing.T) {
	app := &App{appRoot: t.TempDir()}

	if err := app.BrowserLegacyDataDismissFolders([]string{"old/profile-1", "old/profile-2"}); err != nil {
		t.Fatalf("dismiss legacy folders failed: %v", err)
	}
	dismissed := app.dismissedLegacyFolderSet()
	if len(dismissed) != 2 || !dismissed["old/profile-1"] || !dismissed["old/profile-2"] {
		t.Fatalf("legacy folder set mismatch: %+v", dismissed)
	}

	reopened := &App{appRoot: app.appRoot}
	if !reopened.dismissedLegacyFolderSet()["old/profile-1"] {
		t.Fatal("legacy folder dismissal did not persist across restart")
	}

	// Re-enable: the startup scan must surface the folders again.
	if err := reopened.BrowserLegacyDataClearDismissed(); err != nil {
		t.Fatalf("clear legacy dismissed failed: %v", err)
	}
	if len(reopened.dismissedLegacyFolderSet()) != 0 {
		t.Fatal("clear legacy dismissed did not take effect")
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
