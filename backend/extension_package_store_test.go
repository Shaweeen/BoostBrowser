package backend

import (
	"os"
	"path/filepath"
	"testing"

	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
)

func TestLegacyExtensionPackageMigratesToDataWithoutChangingProfileOrSource(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	extensionID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	legacy := app.legacyGlobalExtensionDir(extensionID)
	if err := os.MkdirAll(legacy, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "manifest.json"), []byte(`{"name":"MetaMask","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	profile, err := app.browserMgr.Create(BrowserProfileInput{
		ProfileName: "legacy-extension",
		LaunchArgs:  []string{"--load-extension=" + legacy},
	})
	if err != nil {
		t.Fatal(err)
	}

	app.migrateLegacyExtensionPackageStore()
	persistent := app.globalExtensionDir(extensionID)
	if err := validateUnpackedExtensionManifest(persistent); err != nil {
		t.Fatalf("persistent package missing after migration: %v", err)
	}
	if err := validateUnpackedExtensionManifest(legacy); err != nil {
		t.Fatalf("legacy package must remain untouched: %v", err)
	}
	if got := app.browserMgr.Profiles[profile.ProfileId].LaunchArgs; !hasExtensionDirInLaunchArgs(got, legacy) {
		t.Fatalf("migration must not rewrite saved user launch args: %#v", got)
	}
	runtimeArgs := app.resolveLegacyManagedExtensionLaunchArgs([]string{"--no-first-run", "--load-extension=" + legacy})
	if !hasExtensionDirInLaunchArgs(runtimeArgs, persistent) {
		t.Fatalf("fresh launch must use persistent package: %#v", runtimeArgs)
	}
}

func TestLegacyExtensionPathResolverLeavesExternalPathsUntouched(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	external := filepath.Join(root, "external", "wallet")
	args := []string{"--load-extension=" + external}
	got := app.resolveLegacyManagedExtensionLaunchArgs(args)
	if !hasExtensionDirInLaunchArgs(got, external) {
		t.Fatalf("external extension path must not be rewritten: %#v", got)
	}
}

func TestLegacyExtensionMigrationRecoversProgramCodeFromProfileWithoutReadingWalletState(t *testing.T) {
	root := t.TempDir()
	app := NewApp(root)
	app.browserMgr = browser.NewManager(config.DefaultConfig(), root)
	extensionID := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	profile, err := app.browserMgr.Create(BrowserProfileInput{
		ProfileName: "profile-copy",
		LaunchArgs:  []string{"--load-extension=" + app.legacyGlobalExtensionDir(extensionID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	packageDir := filepath.Join(app.browserMgr.ResolveUserDataDir(profile), "Default", "Extensions", extensionID, "2.0")
	if err := os.MkdirAll(packageDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "manifest.json"), []byte(`{"name":"MetaMask","version":"2.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	walletState := filepath.Join(app.browserMgr.ResolveUserDataDir(profile), "Default", "Local Extension Settings", extensionID, "000003.log")
	if err := os.MkdirAll(filepath.Dir(walletState), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walletState, []byte("wallet-state"), 0600); err != nil {
		t.Fatal(err)
	}

	app.migrateLegacyExtensionPackageStore()
	if err := validateUnpackedExtensionManifest(app.globalExtensionDir(extensionID)); err != nil {
		t.Fatalf("profile package was not recovered: %v", err)
	}
	gotWalletState, err := os.ReadFile(walletState)
	if err != nil || string(gotWalletState) != "wallet-state" {
		t.Fatalf("migration must not read or change wallet state: data=%q err=%v", gotWalletState, err)
	}
}
