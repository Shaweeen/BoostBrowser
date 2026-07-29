package browser

import (
	"boost-browser/backend/internal/config"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateRejectsUserDataDirectoryOwnedByAnotherEnvironment(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "profiles")
	manager := NewManager(cfg, root)

	first, err := manager.Create(ProfileInput{ProfileName: "环境-1", UserDataDir: "wallet-owner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(ProfileInput{ProfileName: "环境-2", UserDataDir: filepath.Join(root, "profiles", "wallet-owner")}); err == nil {
		t.Fatal("absolute and relative references to the same user-data directory must conflict")
	}
	if got := manager.Profiles[first.ProfileId].UserDataDir; got != "wallet-owner" {
		t.Fatalf("first environment storage identity changed: %q", got)
	}
}

func TestUpdatePreservesEnvironmentStorageIdentity(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "profiles")
	manager := NewManager(cfg, root)

	profile, err := manager.Create(ProfileInput{ProfileName: "环境-1", UserDataDir: "wallet-owner"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := manager.Update(profile.ProfileId, ProfileInput{
		ProfileName: "环境-1-已编辑",
		UserDataDir: "",
		LaunchArgs:  []string{"--no-first-run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.UserDataDir != "wallet-owner" {
		t.Fatalf("empty update remapped wallet storage: %q", updated.UserDataDir)
	}
	if _, err := manager.Update(profile.ProfileId, ProfileInput{
		ProfileName: "环境-1-已编辑",
		UserDataDir: "another-wallet-folder",
	}); err == nil || !strings.Contains(err.Error(), "永久数据身份") {
		t.Fatalf("storage remap should be rejected, got %v", err)
	}
	if got := manager.Profiles[profile.ProfileId].UserDataDir; got != "wallet-owner" {
		t.Fatalf("rejected update mutated in-memory storage identity: %q", got)
	}
}

func TestTwoHundredEnvironmentStorageKeysRemainUnique(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "profiles")
	manager := NewManager(cfg, root)
	seen := map[string]string{}

	for index := 1; index <= 200; index++ {
		profile := &Profile{
			ProfileId:   fmt.Sprintf("profile-%03d", index),
			ProfileName: fmt.Sprintf("环境-%d", index),
			UserDataDir: fmt.Sprintf("storage-%03d", index),
		}
		key := manager.userDataDirKey(profile)
		if previous := seen[key]; previous != "" {
			t.Fatalf("storage key collision: %s and %s", previous, profile.ProfileId)
		}
		seen[key] = profile.ProfileId
		manager.Profiles[profile.ProfileId] = profile
	}

	for profileID, profile := range manager.Profiles {
		if err := manager.validateUserDataDirOwnerLocked(profile, profileID); err != nil {
			t.Fatalf("%s ownership validation failed: %v", profileID, err)
		}
	}
}
