package backend

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRecoveredProfileExtension(t *testing.T, userDataDir string) (string, string) {
	t.Helper()
	pub, id := testRSAPublicKeyAndID(t)
	dir := filepath.Join(userDataDir, "Default", "Extensions", id, "1.2.3")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"User extension","version":"1.2.3","manifest_version":3,"key":"` + base64.StdEncoding.EncodeToString(pub) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return id, dir
}

func TestProfileExtensionRecoveryUsesOnlyUnregisteredStablePackages(t *testing.T) {
	userData := t.TempDir()
	id, dir := writeRecoveredProfileExtension(t, userData)
	got := recoverableProfileExtensionDirs(userData)
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("recoverable dirs = %v, want %q", got, dir)
	}

	prefs := `{"extensions":{"settings":{"` + id + `":{"state":0}}}}`
	prefPath := filepath.Join(userData, "Default", "Preferences")
	if err := os.WriteFile(prefPath, []byte(prefs), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := recoverableProfileExtensionDirs(userData); len(got) != 0 {
		t.Fatalf("user-disabled Chromium entry must not be overridden, got %v", got)
	}
}

func TestProfileExtensionRecoveryUsesChromeStorePackageWithoutManifestKey(t *testing.T) {
	userData := t.TempDir()
	id := "nkbihfbeogaeaoehlefnkodbefgpgknn"
	dir := filepath.Join(userData, "Default", "Extensions", id, "12.3.4")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"name":"MetaMask","version":"12.3.4","manifest_version":3}`)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := recoverableProfileExtensionDirs(userData)
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("Chrome Store package without manifest key must recover, got %v", got)
	}
	after, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("recovery must not rewrite the package: err=%v", err)
	}
}

func TestProfileExtensionRecoveryRejectsIdentityMismatchAndDoesNotWrite(t *testing.T) {
	userData := t.TempDir()
	_, dir := writeRecoveredProfileExtension(t, userData)
	wrong := filepath.Join(userData, "Default", "Extensions", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "1.0")
	if err := os.MkdirAll(wrong, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wrong, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(wrong, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := recoverableProfileExtensionDirs(userData)
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("identity mismatch must be ignored, got %v", got)
	}
	after, err := os.ReadFile(filepath.Join(wrong, "manifest.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("recovery wrote profile package: err=%v before=%q after=%q", err, before, after)
	}
}

func TestStripBrowserStudioManagedExtensionLaunchArgsKeepsUserPackage(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "data", "extensions", "imported")
	user := filepath.Join(root, "user-extension")
	args, suppressed := stripBrowserStudioManagedExtensionLaunchArgs([]string{
		"--load-extension=" + filepath.Join(managed, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") + "," + user,
		"--proxy-server=direct://",
	}, managed)
	if suppressed != 1 {
		t.Fatalf("suppressed = %d, want 1", suppressed)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, user) || strings.Contains(joined, managed) {
		t.Fatalf("unexpected args after strip: %v", args)
	}
}
