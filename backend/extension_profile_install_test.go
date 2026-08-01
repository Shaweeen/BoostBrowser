package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallUnpackedExtensionIntoProfileRegistersPrefsNoCopy(t *testing.T) {
	root := t.TempDir()
	pkg := filepath.Join(root, "pkg", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"Wallet","version":"1.2.3","manifest_version":3,"permissions":["storage"],"host_permissions":["<all_urls>"]}`
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
	// Must NOT copy into Default/Extensions (slow + wrong for shared packages).
	copied := filepath.Join(userData, "Default", "Extensions", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if _, err := os.Stat(copied); err == nil {
		t.Fatal("must not copy package into profile Extensions tree")
	}
	if !isExtensionInstalledInProfile(userData, pkg) {
		t.Fatal("expected prefs registration")
	}
	prefIDs := preferenceExtensionIDs(userData)
	if _, ok := prefIDs["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"]; !ok {
		t.Fatalf("Preferences missing extension id: %#v", prefIDs)
	}
	entry, _ := readPreferencesExtensionEntry(userData, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if entry["location"] != float64(chromeExtLocationUnpacked) {
		t.Fatalf("location must be UNPACKED(4): %#v", entry["location"])
	}
	path, _ := entry["path"].(string)
	abs, _ := filepath.Abs(pkg)
	if normalizeExtensionPath(path) != normalizeExtensionPath(abs) {
		t.Fatalf("path must point at shared package: %q vs %q", path, abs)
	}
	active, _ := entry["active_permissions"].(map[string]any)
	if active == nil {
		t.Fatal("active_permissions required")
	}
}

func TestInstallIntoProfileDoesNotTouchLocalExtensionSettings(t *testing.T) {
	root := t.TempDir()
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

func TestApplyProfileNativeKeepsCLIUntilChromeDataExists(t *testing.T) {
	root := t.TempDir()
	extID := "cccccccccccccccccccccccccccccccc"
	pkg := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"2.0","manifest_version":3,"permissions":["storage"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	userData := filepath.Join(root, "user")
	args := []string{"--no-first-run", "--load-extension=" + pkg}
	// First open: no profile evidence → must keep --load-extension.
	next, present, cli := applyProfileNativeExtensionLaunchArgs(args, userData)
	if present != 0 || cli != 1 {
		t.Fatalf("first open: present=%d cli=%d next=%#v", present, cli, next)
	}
	if !hasExtensionDirInLaunchArgs(next, pkg) {
		t.Fatalf("first open must inject CLI: %#v", next)
	}
	// Simulate Chrome already has extension: LES vault (read-only detect).
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(les, "000003.log"), []byte("vault"), 0600); err != nil {
		t.Fatal(err)
	}
	next2, present2, cli2 := applyProfileNativeExtensionLaunchArgs(args, userData)
	if present2 != 1 || cli2 != 0 {
		t.Fatalf("after LES, CLI must be cancelled: present=%d cli=%d next=%#v", present2, cli2, next2)
	}
	for _, a := range next2 {
		if strings.Contains(strings.ToLower(a), "--load-extension=") {
			t.Fatalf("CLI must be stripped when extension already present: %#v", next2)
		}
	}
}

func TestReadOnlyDetectUsesEnabledPreferencesWithoutWriting(t *testing.T) {
	root := t.TempDir()
	extID := "dddddddddddddddddddddddddddddddd"
	pkg := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"1","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(pkg)
	userData := filepath.Join(root, "user")
	prefDir := filepath.Join(userData, "Default")
	if err := os.MkdirAll(prefDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Chrome-style enabled entry pointing at package (simulate after prior load).
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{
					"state": float64(1),
					"path":  abs,
				},
			},
		},
	}
	raw, _ := json.Marshal(prefs)
	prefPath := filepath.Join(prefDir, "Preferences")
	if err := os.WriteFile(prefPath, raw, 0644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(prefPath)
	if !extensionAlreadyPresentInProfileReadOnly(userData, pkg) {
		t.Fatal("enabled prefs+path must count as present")
	}
	// Read-only path must not mutate Preferences.
	_, need := selectLoadExtensionCLIReadOnly(userData, []string{"--load-extension=" + pkg})
	if len(need) != 0 {
		t.Fatalf("CLI must be cancelled: %#v", need)
	}
	after, _ := os.ReadFile(prefPath)
	if string(before) != string(after) {
		t.Fatal("read-only detect must not rewrite Preferences")
	}
}

func TestPermissionsFromManifest(t *testing.T) {
	m := map[string]any{
		"permissions":      []any{"storage", "tabs", "https://*/*"},
		"host_permissions": []any{"<all_urls>"},
	}
	api, hosts := permissionsFromManifest(m)
	if len(api) != 2 {
		t.Fatalf("api=%#v", api)
	}
	if len(hosts) < 2 {
		t.Fatalf("hosts=%#v", hosts)
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
	pkg := filepath.Join(root, "pkg", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"1.0","manifest_version":3,"permissions":["storage"]}`), 0644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(pkg)
	if err := ensurePreferencesUnpackedExtension(userData, extID, abs, "1.0"); err != nil {
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
	if entry["state"] != float64(1) {
		t.Fatalf("re-assign must re-enable extension: %#v", entry["state"])
	}
	if normalizeExtensionPath(entry["path"].(string)) != normalizeExtensionPath(abs) {
		t.Fatalf("path must update to shared package: %#v", entry["path"])
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
