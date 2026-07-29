package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotRestoreCorruptArchivePreservesWalletData(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "data", "profiles")
	app := NewApp(root)
	app.browserMgr = browser.NewManager(cfg, root)

	profile := &browser.Profile{
		ProfileId:   "profile-wallet",
		ProfileName: "钱包环境",
		UserDataDir: "profile-wallet",
	}
	app.browserMgr.Profiles[profile.ProfileId] = profile
	userDataDir := app.browserMgr.ResolveUserDataDir(profile)
	walletState := filepath.Join(userDataDir, "Default", "Local Extension Settings", "wallet-id", "000003.log")
	if err := os.MkdirAll(filepath.Dir(walletState), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walletState, []byte("encrypted-wallet-state"), 0600); err != nil {
		t.Fatal(err)
	}

	snapshotDir, err := app.snapshotDir(profile.ProfileId)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := "corrupt-snapshot"
	if err := os.WriteFile(filepath.Join(snapshotDir, snapshotID+"_test.meta.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, snapshotID+"_test.zip"), []byte("not-a-zip"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := app.BrowserSnapshotRestore(profile.ProfileId, snapshotID); err == nil {
		t.Fatal("corrupt snapshot restore should fail")
	}
	data, err := os.ReadFile(walletState)
	if err != nil || string(data) != "encrypted-wallet-state" {
		t.Fatalf("live wallet data changed after failed restore: data=%q err=%v", data, err)
	}
}
