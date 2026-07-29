package browser

import (
	"boost-browser/backend/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteArchivesProfileOwnedUserDataForRecovery(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "data")
	manager := NewManager(cfg, root)
	profile := &Profile{ProfileId: "profile-delete", ProfileName: "delete", UserDataDir: "profile-delete"}
	manager.Profiles[profile.ProfileId] = profile
	userDataDir := manager.ResolveUserDataDir(profile)
	if err := os.MkdirAll(filepath.Join(userDataDir, "Default", "Extensions"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDataDir, "Default", "Cookies"), []byte("session"), 0644); err != nil {
		t.Fatal(err)
	}

	// The legacy false flag must still remove the active record without
	// destroying the user-owned browser directory.
	if err := manager.DeleteWithCache(profile.ProfileId, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userDataDir); !os.IsNotExist(err) {
		t.Fatalf("active profile path survived archival: %v", err)
	}
	if _, exists := manager.Profiles[profile.ProfileId]; exists {
		t.Fatal("profile record survived deletion")
	}
	archives, err := ListProfileDataArchives(manager.ProfileRecoveryArchiveRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != 1 || archives[0].ProfileID != profile.ProfileId || !archives[0].DataAvailable {
		t.Fatalf("recovery linkage missing: %+v", archives)
	}
	archivedData := filepath.Join(manager.ProfileRecoveryArchiveRoot(), archives[0].ArchivedDataDir)
	if data, err := os.ReadFile(filepath.Join(archivedData, "Default", "Cookies")); err != nil || string(data) != "session" {
		t.Fatalf("archived Cookie data changed or missing: data=%q err=%v", data, err)
	}
}

func TestProfileArchiveRollbackRestoresOwnedData(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "data")
	manager := NewManager(cfg, root)
	profile := &Profile{ProfileId: "profile-rollback", ProfileName: "rollback", UserDataDir: "profile-rollback"}
	manager.Profiles[profile.ProfileId] = profile
	userDataDir := manager.ResolveUserDataDir(profile)
	walletState := filepath.Join(userDataDir, "Default", "Local Extension Settings", "wallet", "state")
	if err := os.MkdirAll(filepath.Dir(walletState), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walletState, []byte("encrypted"), 0600); err != nil {
		t.Fatal(err)
	}

	move, err := manager.ArchiveProfileDataForReplacement(profile.ProfileId, "rollback-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := move.Rollback(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(walletState)
	if err != nil || string(data) != "encrypted" {
		t.Fatalf("rollback changed wallet state: data=%q err=%v", data, err)
	}
}

func TestProfileArchiveRollbackKeepsRecoveryIndexWhenDestinationIsOccupied(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "data")
	manager := NewManager(cfg, root)
	profile := &Profile{ProfileId: "profile-occupied", ProfileName: "occupied", UserDataDir: "profile-occupied"}
	manager.Profiles[profile.ProfileId] = profile
	userDataDir := manager.ResolveUserDataDir(profile)
	walletState := filepath.Join(userDataDir, "Default", "Local Extension Settings", "wallet", "state")
	if err := os.MkdirAll(filepath.Dir(walletState), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walletState, []byte("encrypted"), 0600); err != nil {
		t.Fatal(err)
	}

	move, err := manager.ArchiveProfileDataForReplacement(profile.ProfileId, "occupied-rollback-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(userDataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := move.Rollback(); err == nil {
		t.Fatal("occupied original path should block rollback")
	}
	if _, err := os.Stat(move.manifest); err != nil {
		t.Fatalf("recovery index disappeared while archived data remained: %v", err)
	}
	archives, err := ListProfileDataArchives(manager.ProfileRecoveryArchiveRoot())
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != 1 || !archives[0].DataAvailable {
		t.Fatalf("archived wallet data became undiscoverable: %+v", archives)
	}
	if data, err := os.ReadFile(filepath.Join(move.archiveDir, "Default", "Local Extension Settings", "wallet", "state")); err != nil || string(data) != "encrypted" {
		t.Fatalf("archived wallet data changed: data=%q err=%v", data, err)
	}
}
