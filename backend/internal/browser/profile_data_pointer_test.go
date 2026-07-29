package browser

import (
	"boost-browser/backend/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProfileDataPointerTracksOneEnvironmentCleanClose(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "data")
	manager := NewManager(cfg, root)
	profile := &Profile{
		ProfileId:       "profile-17",
		ProfileName:     "环境-17",
		UserDataDir:     "profile-17",
		CoreId:          "core-148",
		FingerprintArgs: []string{"--fingerprint-platform=windows"},
		ProxyId:         "proxy-17",
		CreatedAt:       "2026-07-29T08:00:00Z",
	}
	dataDir := manager.ResolveUserDataDir(profile)
	if err := os.MkdirAll(filepath.Join(dataDir, "Default", "Local Extension Settings"), 0700); err != nil {
		t.Fatal(err)
	}
	walletMarker := filepath.Join(dataDir, "Default", "Local Extension Settings", "wallet-state")
	if err := os.WriteFile(walletMarker, []byte("opaque-encrypted-wallet-bytes"), 0600); err != nil {
		t.Fatal(err)
	}

	requestedAt := time.Date(2026, 7, 29, 8, 10, 0, 0, time.UTC)
	if err := manager.WriteProfileDataPointer(profile, "closing", 8817, requestedAt); err != nil {
		t.Fatalf("write closing pointer: %v", err)
	}
	closedAt := requestedAt.Add(2 * time.Second)
	if err := manager.WriteProfileDataPointer(profile, "closed", 8817, closedAt); err != nil {
		t.Fatalf("write closed pointer: %v", err)
	}

	pointer, err := ReadProfileDataPointer(dataDir)
	if err != nil {
		t.Fatalf("read pointer: %v", err)
	}
	if pointer.ProfileID != profile.ProfileId || pointer.UserDataDir != profile.UserDataDir || pointer.CloseState != "closed" {
		t.Fatalf("unexpected data pointer: %+v", pointer)
	}
	if pointer.LastCleanCloseAt != closedAt.Format(time.RFC3339Nano) {
		t.Fatalf("clean-close timestamp mismatch: %q", pointer.LastCleanCloseAt)
	}
	got, err := os.ReadFile(walletMarker)
	if err != nil {
		t.Fatalf("wallet marker was modified or removed: %v", err)
	}
	if string(got) != "opaque-encrypted-wallet-bytes" {
		t.Fatal("pointer writer must not inspect or modify wallet data")
	}
}
