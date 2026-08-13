package browser

import (
	"boost-browser/backend/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newArchiveLifecycleManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = filepath.Join(root, "data")
	return NewManager(cfg, root)
}

func createDeletedArchive(t *testing.T, manager *Manager, profileName string) ProfileDataArchiveManifest {
	t.Helper()
	profile := &Profile{
		ProfileId:   "deleted-" + profileName,
		ProfileName: profileName,
		UserDataDir: "deleted-" + profileName,
	}
	manager.Profiles[profile.ProfileId] = profile
	dataDir := manager.ResolveUserDataDir(profile)
	if err := os.MkdirAll(filepath.Join(dataDir, "Default", "Local Extension Settings", "wallet"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "Default", "Cookies"), []byte("cookie-session"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "Default", "Local Extension Settings", "wallet", "vault"), []byte("encrypted-wallet-state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteWithCache(profile.ProfileId, false); err != nil {
		t.Fatal(err)
	}
	archives, err := ListProfileDataArchives(manager.ProfileRecoveryArchiveRoot())
	if err != nil || len(archives) != 1 {
		t.Fatalf("expected one deleted archive, archives=%+v err=%v", archives, err)
	}
	return archives[0]
}

func TestDeletedEnvironmentDataRestoresOnlyIntoFreshExactMatch(t *testing.T) {
	manager := newArchiveLifecycleManager(t)
	archive := createDeletedArchive(t, manager, "环境-12")

	target, err := manager.Create(ProfileInput{ProfileName: "环境-12"})
	if err != nil {
		t.Fatal(err)
	}
	offers, err := manager.FindDeletedEnvironmentDataOffers([]string{target.ProfileId})
	if err != nil || len(offers) != 1 || offers[0].ArchiveKey != archive.ArchivedDataDir {
		t.Fatalf("expected exact deleted-data offer, offers=%+v err=%v", offers, err)
	}
	if err := manager.RestoreDeletedEnvironmentDataOffer(target.ProfileId, offers[0].ArchiveKey); err != nil {
		t.Fatal(err)
	}

	restoredDir := manager.ResolveUserDataDir(target)
	if data, err := os.ReadFile(filepath.Join(restoredDir, "Default", "Cookies")); err != nil || string(data) != "cookie-session" {
		t.Fatalf("Cookies were not preserved exactly: data=%q err=%v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(restoredDir, "Default", "Local Extension Settings", "wallet", "vault")); err != nil || string(data) != "encrypted-wallet-state" {
		t.Fatalf("wallet extension state was not preserved exactly: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(manager.ProfileRecoveryArchiveRoot(), archive.ArchivedDataDir)); !os.IsNotExist(err) {
		t.Fatalf("restored archive should no longer remain in recovery root: %v", err)
	}
	if pointer, err := ReadProfileDataPointer(restoredDir); err != nil || pointer.ProfileID != target.ProfileId {
		t.Fatalf("new profile identity pointer was not written: pointer=%+v err=%v", pointer, err)
	}
}

func TestDeletedEnvironmentDataNeverOverwritesUsedNewEnvironment(t *testing.T) {
	manager := newArchiveLifecycleManager(t)
	archive := createDeletedArchive(t, manager, "环境-24")
	target, err := manager.Create(ProfileInput{ProfileName: "环境-24"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(manager.ResolveUserDataDir(target), "Default"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.ResolveUserDataDir(target), "Default", "Cookies"), []byte("new-session"), 0600); err != nil {
		t.Fatal(err)
	}

	err = manager.RestoreDeletedEnvironmentDataOffer(target.ProfileId, archive.ArchivedDataDir)
	if err == nil {
		t.Fatal("used new environment must reject recovery overwrite")
	}
	if data, readErr := os.ReadFile(filepath.Join(manager.ResolveUserDataDir(target), "Default", "Cookies")); readErr != nil || string(data) != "new-session" {
		t.Fatalf("existing target data changed: data=%q err=%v", data, readErr)
	}
	if data, readErr := os.ReadFile(filepath.Join(manager.ProfileRecoveryArchiveRoot(), archive.ArchivedDataDir, "Default", "Cookies")); readErr != nil || string(data) != "cookie-session" {
		t.Fatalf("archive data changed after rejected restore: data=%q err=%v", data, readErr)
	}
}

func TestIgnoredDeletedEnvironmentDataIsNotPromptedAndExpiresAfterSixMonths(t *testing.T) {
	manager := newArchiveLifecycleManager(t)
	archive := createDeletedArchive(t, manager, "环境-36")
	target, err := manager.Create(ProfileInput{ProfileName: "环境-36"})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.IgnoreDeletedEnvironmentDataOffer(archive.ArchivedDataDir); err != nil {
		t.Fatal(err)
	}
	offers, err := manager.FindDeletedEnvironmentDataOffers([]string{target.ProfileId})
	if err != nil || len(offers) != 0 {
		t.Fatalf("ignored archive must never prompt again: offers=%+v err=%v", offers, err)
	}

	beforeExpiry, err := manager.CleanupExpiredDeletedEnvironmentData(time.Now().AddDate(0, ProfileDataArchiveRetentionMonths, 0).Add(-time.Second))
	if err != nil || beforeExpiry.Removed != 0 {
		t.Fatalf("archive deleted before six calendar months: result=%+v err=%v", beforeExpiry, err)
	}
	afterExpiry, err := manager.CleanupExpiredDeletedEnvironmentData(time.Now().AddDate(0, ProfileDataArchiveRetentionMonths, 0).Add(time.Second))
	if err != nil || afterExpiry.Removed != 1 {
		t.Fatalf("archive should expire after six calendar months: result=%+v err=%v", afterExpiry, err)
	}
	if _, err := os.Stat(filepath.Join(manager.ProfileRecoveryArchiveRoot(), archive.ArchivedDataDir)); !os.IsNotExist(err) {
		t.Fatalf("expired archive still exists: %v", err)
	}
}
