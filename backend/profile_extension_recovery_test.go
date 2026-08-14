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

func TestProfileExtensionRecoveryRejectsChromeStorePackageWithoutManifestKey(t *testing.T) {
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
	if len(got) != 0 {
		t.Fatalf("keyless package would derive a different ID from its path, got %v", got)
	}
	after, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("recovery must not rewrite the package: err=%v", err)
	}
}

func TestProfileExtensionRecoveryUsesOnlyProfileSelectedForThisLaunch(t *testing.T) {
	userData := t.TempDir()
	id, dir := writeRecoveredProfileExtension(t, userData)
	profileOne := filepath.Join(userData, "Profile 1")
	if err := os.MkdirAll(profileOne, 0o755); err != nil {
		t.Fatal(err)
	}
	// The same extension being registered in a different Chrome profile must
	// not make the active Default profile look healthy.
	prefs := `{"extensions":{"settings":{"` + id + `":{"state":1}}}}`
	if err := os.WriteFile(filepath.Join(profileOne, "Preferences"), []byte(prefs), 0o644); err != nil {
		t.Fatal(err)
	}
	got := recoverableProfileExtensionDirsForLaunch(userData, nil)
	if len(got) != 1 || got[0] != dir {
		t.Fatalf("Default recovery was incorrectly suppressed by Profile 1: %v", got)
	}

	if err := os.MkdirAll(filepath.Join(profileOne, "Extensions", id, "1.2.3"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileOne, "Extensions", id, "1.2.3", "manifest.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	got = recoverableProfileExtensionDirsForLaunch(userData, []string{`--profile-directory="Profile 1"`})
	if len(got) != 0 {
		t.Fatalf("registered selected profile must not be overridden, got %v", got)
	}

	// Chrome reopens Local State's last-used profile when the launch command
	// does not explicitly select one. The recovery probe must follow that same
	// choice instead of always inspecting Default.
	localState := `{"profile":{"last_used":"Profile 1"}}`
	if err := os.WriteFile(filepath.Join(userData, "Local State"), []byte(localState), 0o644); err != nil {
		t.Fatal(err)
	}
	got = recoverableProfileExtensionDirsForLaunch(userData, nil)
	if len(got) != 0 {
		t.Fatalf("Local State selected profile must not be overridden, got %v", got)
	}
}

func TestProfileExtensionRecoveryRestoresLastUsedNonDefaultProfile(t *testing.T) {
	userData := t.TempDir()
	pub, id := testRSAPublicKeyAndID(t)
	profileOneDir := filepath.Join(userData, "Profile 1", "Extensions", id, "1.2.3")
	if err := os.MkdirAll(profileOneDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"Legacy wallet","version":"1.2.3","manifest_version":3,"key":"` + base64.StdEncoding.EncodeToString(pub) + `"}`
	if err := os.WriteFile(filepath.Join(profileOneDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userData, "Local State"), []byte(`{"profile":{"last_used":"Profile 1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := recoverableProfileExtensionDirsForLaunch(userData, nil)
	if len(got) != 1 || got[0] != profileOneDir {
		t.Fatalf("last-used Profile 1 package was not recovered: %v", got)
	}
	// An explicit launch profile must override Local State.
	if got := recoverableProfileExtensionDirsForLaunch(userData, []string{"--profile-directory=Default"}); len(got) != 0 {
		t.Fatalf("explicit Default must not recover Profile 1 package: %v", got)
	}
}

func TestProfileExtensionRecoveryUnblocksOnlyARecoveryLaunch(t *testing.T) {
	userData := t.TempDir()
	_, dir := writeRecoveredProfileExtension(t, userData)
	args, recovered := appendProfileExtensionRecoveryLaunchArgs([]string{
		"--disable-extensions",
		"--disable-extensions-except=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"--proxy-server=direct://",
	}, userData)
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(strings.ToLower(joined), "--disable-extensions") {
		t.Fatalf("recovery launch must not retain extension blocking flags: %v", args)
	}
	if !strings.Contains(joined, dir) || !strings.Contains(joined, "--proxy-server=direct://") {
		t.Fatalf("recovery must preserve unrelated launch arguments: %v", args)
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

func TestProfileExtensionRecoveryKeepsSavedManagedPackageAndEnablesCLI(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "data", "extensions", "imported")
	user := filepath.Join(root, "user-extension")
	args := []string{
		"--load-extension=" + filepath.Join(managed, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") + "," + user,
		"--proxy-server=direct://",
	}
	args, recovered := appendProfileExtensionRecoveryLaunchArgs(args, filepath.Join(root, "profile"))
	if recovered != 0 {
		t.Fatalf("unexpected recovery count = %d", recovered)
	}
	args = ensureLoadExtensionCommandLineSwitchEnabled(normalizeLoadExtensionArgs(args))
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, user) || !strings.Contains(joined, managed) {
		t.Fatalf("saved extension paths must survive startup: %v", args)
	}
	if !strings.Contains(joined, "DisableLoadExtensionCommandLineSwitch") {
		t.Fatalf("Chrome 137+ CLI compatibility switch missing: %v", args)
	}
}
