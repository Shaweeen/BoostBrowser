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
	// LES vault alone must NOT cancel CLI (stale package path after upgrade).
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(les, "000003.log"), []byte("vault"), 0600); err != nil {
		t.Fatal(err)
	}
	if !extensionAlreadyPresentInProfileReadOnly(userData, pkg) {
		t.Fatal("LES vault must still count as durable data to protect")
	}
	next2, present2, cli2 := applyProfileNativeExtensionLaunchArgs(args, userData)
	if present2 != 0 || cli2 != 1 {
		t.Fatalf("LES alone must keep CLI until Preferences path is loadable: present=%d cli=%d next=%#v", present2, cli2, next2)
	}
	// After healing Preferences path (never wipes LES), CLI can be cancelled.
	if err := installUnpackedExtensionIntoProfile(userData, pkg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(les, "000003.log")); err != nil {
		t.Fatalf("heal must not touch LES vault: %v", err)
	}
	next3, present3, cli3 := applyProfileNativeExtensionLaunchArgs(args, userData)
	if present3 != 1 || cli3 != 0 {
		t.Fatalf("after loadable prefs, CLI must cancel: present=%d cli=%d next=%#v", present3, cli3, next3)
	}
	for _, a := range next3 {
		if strings.Contains(strings.ToLower(a), "--load-extension=") {
			t.Fatalf("CLI must be stripped when Preferences path is loadable: %#v", next3)
		}
	}
}

func TestHealAssignedExtensionPackagePathsRepairsStalePathKeepsLES(t *testing.T) {
	root := t.TempDir()
	extID := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	// New package location after upgrade.
	pkg := filepath.Join(root, "new-install", "extensions", "imported", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"1","manifest_version":3,"key":"MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAu"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Real packages need valid key for id — use folder name id fallback path.
	// Force id via folder name by skipping key derivation issues: use plain id folder.
	pkg = filepath.Join(root, "new-install", extID)
	if err := os.MkdirAll(pkg, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "manifest.json"), []byte(`{"name":"W","version":"1","manifest_version":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	// resolveExtensionPackageID may not equal extID without key — use base name.
	extID = strings.ToLower(filepath.Base(pkg))
	userData := filepath.Join(root, "user")
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0700); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(les, "000003.log")
	if err := os.WriteFile(vault, []byte("wallet-vault-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	// Stale Preferences path from previous install root.
	stale := filepath.Join(root, "old-install", "gone", extID)
	prefDir := filepath.Join(userData, "Default")
	if err := os.MkdirAll(prefDir, 0755); err != nil {
		t.Fatal(err)
	}
	prefs := map[string]any{
		"extensions": map[string]any{
			"settings": map[string]any{
				extID: map[string]any{
					"state": float64(1),
					"path":  stale,
				},
			},
		},
	}
	raw, _ := json.Marshal(prefs)
	if err := os.WriteFile(filepath.Join(prefDir, "Preferences"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if canSkipLoadExtensionCLI(userData, pkg) {
		t.Fatal("stale path must not skip CLI before heal")
	}
	healed := healAssignedExtensionPackagePaths(userData, []string{"--load-extension=" + pkg})
	if healed != 1 {
		t.Fatalf("healed=%d", healed)
	}
	if data, err := os.ReadFile(vault); err != nil || string(data) != "wallet-vault-bytes" {
		t.Fatalf("LES vault must be untouched: %v %q", err, data)
	}
	if !canSkipLoadExtensionCLI(userData, pkg) {
		t.Fatal("after heal, CLI must be skippable")
	}
	if !isExtensionInstalledInProfile(userData, pkg) {
		t.Fatal("prefs path must point at new package")
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
	// Prefs alone still needs CLI for first adapt; detect must not write Preferences.
	_, need := selectLoadExtensionCLIReadOnly(userData, []string{"--load-extension=" + pkg})
	if len(need) != 1 {
		t.Fatalf("prefs-only must keep CLI until durable LES: need=%#v", need)
	}
	after, _ := os.ReadFile(prefPath)
	if string(before) != string(after) {
		t.Fatal("read-only detect must not rewrite Preferences")
	}
	// After Chrome durable runtime exists, CLI can cancel — still read-only.
	les := filepath.Join(userData, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(les, "000003.log"), []byte("vault"), 0600); err != nil {
		t.Fatal(err)
	}
	before2, _ := os.ReadFile(prefPath)
	_, need2 := selectLoadExtensionCLIReadOnly(userData, []string{"--load-extension=" + pkg})
	if len(need2) != 0 {
		t.Fatalf("prefs+LES must cancel CLI: %#v", need2)
	}
	after2, _ := os.ReadFile(prefPath)
	if string(before2) != string(after2) {
		t.Fatal("read-only detect must not rewrite Preferences after LES")
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
