package browser

import (
	"boost-browser/backend/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteAlwaysRemovesProfileOwnedUserData(t *testing.T) {
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

	// The legacy false flag must no longer leave an orphaned data directory.
	if err := manager.DeleteWithCache(profile.ProfileId, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(userDataDir); !os.IsNotExist(err) {
		t.Fatalf("profile-owned browser data survived deletion: %v", err)
	}
	if _, exists := manager.Profiles[profile.ProfileId]; exists {
		t.Fatal("profile record survived deletion")
	}
}
