package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallUnpackedExtensionIntoProfileMaterializesFilesAndPrefs(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "pkg", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"Wallet","version":"1.2.3","manifest_version":3}`
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "background.js"), []byte("console.log(1)"), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	if err := installUnpackedExtensionIntoProfile(userData, pkg); err != nil {
		t.Fatalf("install: %v", err)
	}
	dest := filepath.Join(userData, "Default", "Extensions", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "1.2.3", "manifest.json")
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("package files missing: %v", err)
	}
	if !isExtensionInstalledInProfile(userData, pkg) {
		t.Fatal("expected profile install to report installed")
	}
	// Preferences must list the id.
	prefIDs := preferenceExtensionIDs(userData)
	if _, ok := prefIDs["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]; !ok {
		t.Fatalf("Preferences missing extension id: %#v", prefIDs)
	}
	// Second install is idempotent and must not wipe prefs.
	if err := installUnpackedExtensionIntoProfile(userData, pkg); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
}

func TestInstallIntoProfileDoesNotTouchLocalExtensionSettings(t *testing.T) {
	root := t.TempDir()
	// Use a custom folder name; ID falls back to basename (not 32-char).
	// For vault protection we use a web-store style folder id.
	extID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	pkg := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0700); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(les, "000003.log")
	if err := os.WriteFile(vault, []byte("secret-wallet"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installUnpackedExtensionIntoProfile(userData, pkg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(vault)
	if err != nil || string(got) != "secret-wallet" {
		t.Fatalf("wallet LES must be untouched: %v %q", err, got)
	}
}

func TestApplyProfileNativeExtensionLaunchArgsStripsCLIWhenInstalled(t *testing.T) {
	root := t.TempDir()
	extID := "cccccccccccccccccccccccccccccccc"
	pkg := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"2.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	args := []string{"--no-first-run", "--load-extension=" + pkg}
	next, installed, cli := applyProfileNativeExtensionLaunchArgs(args, userData)
	if installed != 1 || cli != 0 {
		t.Fatalf("installed=%d cli=%d next=%#v", installed, cli, next)
	}
	for _, a := range next {
		if strings.Contains(strings.ToLower(a), "--load-extension=") {
			t.Fatalf("CLI inject must be stripped after profile install: %#v", next)
		}
	}
	// Assignment bookkeeping path is gone from argv but package is in profile.
	if !isExtensionInstalledInProfile(userData, pkg) {
		t.Fatal("profile must hold the package")
	}
}

func TestEnsurePreferencesExtensionInstalledPreservesExistingEntry(t *testing.T) {
	root := t.TempDir()
	extID := "dddddddddddddddddddddddddddddddd"
	userData := filepath.Join(root, "user")
	prefDir := filepath.Join(userData, "Default")
	if err := os.MkdirAll(prefDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Pre-existing wallet-bearing prefs entry (disabled after unbind).
	initial := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{
					"state":           float64(0),
					"disable_reasons": float64(1),
					"path":            "/old/path",
					"account":         "keep-me",
				},
			},
		},
	}
	raw, _ := json.Marshal(initial)
	if err := os.WriteFile(filepath.Join(prefDir, "Preferences"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	installPath := filepath.Join(prefDir, "Extensions", extID, "1.0")
	if err := os.MkdirAll(installPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installPath, "manifest.json"), []byte(`{"name":"W","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ensurePreferencesExtensionInstalled(userData, extID, installPath, "1.0"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(prefDir, "Preferences"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	settings := out["extensions"].(map[string]any)["settings"].(map[string]any)
	entry := settings[extID].(map[string]any)
	if entry["account"] != "keep-me" {
		t.Fatalf("existing prefs fields must be preserved: %#v", entry)
	}
	// Re-assign must re-enable and point at the current package path.
	if entry["state"] != float64(1) {
		t.Fatalf("re-assign must re-enable extension: %#v", entry["state"])
	}
	if entry["path"] != installPath {
		t.Fatalf("path must update to current install: %#v", entry["path"])
	}
}

func TestDisableExtensionInProfileKeepsWalletLES(t *testing.T) {
	root := t.TempDir()
	extID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	pkg := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"1.0","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	if err := installUnpackedExtensionIntoProfile(userData, pkg); err != nil {
		t.Fatal(err)
	}
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0700); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(les, "vault")
	if err := os.WriteFile(vault, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := disableExtensionInProfile(userData, pkg); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(vault)
	if string(got) != "secret" {
		t.Fatal("disable must keep LES vault")
	}
	prefIDs := preferenceExtensionIDs(userData)
	if _, ok := prefIDs[extID]; !ok {
		t.Fatal("settings row must remain (disabled, not deleted)")
	}
	data, _ := os.ReadFile(filepath.Join(userData, "Default", "Preferences"))
	var prefs map[string]any
	_ = json.Unmarshal(data, &prefs)
	entry := prefs["extensions"].(map[string]any)["settings"].(map[string]any)[extID].(map[string]any)
	if entry["state"] != float64(0) {
		t.Fatalf("expected disabled state, got %#v", entry["state"])
	}
}
