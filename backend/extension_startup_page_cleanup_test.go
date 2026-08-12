package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssignedExtensionIDSet(t *testing.T) {
	args := []string{
		"--user-data-dir=/tmp/x",
		"--load-extension=/app/extensions/imported/nkbihfbeogaeaoehlefnkodbefgpgknn,/app/extensions/imported/acmacodkjbdgmoleebolmdjonilkdbch",
	}
	ids := assignedExtensionIDSet(args)
	if len(ids) != 2 {
		t.Fatalf("expected 2 assigned extension ids, got %d: %v", len(ids), ids)
	}
	for _, want := range []string{"nkbihfbeogaeaoehlefnkodbefgpgknn", "acmacodkjbdgmoleebolmdjonilkdbch"} {
		if _, ok := ids[want]; !ok {
			t.Fatalf("missing assigned extension id %q in %v", want, ids)
		}
	}
}

func TestAssignedExtensionIDSetEmpty(t *testing.T) {
	if ids := assignedExtensionIDSet(nil); len(ids) != 0 {
		t.Fatalf("expected empty set for nil args, got %v", ids)
	}
	if ids := assignedExtensionIDSet([]string{"--no-first-run"}); len(ids) != 0 {
		t.Fatalf("expected empty set for no-extension args, got %v", ids)
	}
}

func TestExtensionIDFromPageURL(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/home.html#onboarding/welcome", "nkbihfbeogaeaoehlefnkodbefgpgknn"},
		{"chrome-extension://acmacodkjbdgmoleebolmdjonilkdbch/index.html#/unlock", "acmacodkjbdgmoleebolmdjonilkdbch"},
		{"chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/notification.html", "nkbihfbeogaeaoehlefnkodbefgpgknn"},
		{"https://example.com/", ""},
		{"about:blank", ""},
		{"", ""},
		{"chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn", "nkbihfbeogaeaoehlefnkodbefgpgknn"},
	}
	for _, tc := range cases {
		if got := extensionIDFromPageURL(tc.url); got != tc.want {
			t.Errorf("extensionIDFromPageURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestRegisterAssignedExtensionsIntoProfile: Scheme A Preferences registration
// is written before launch. CLI is kept until Chrome durable runtime exists
// (prefs alone is not enough). After LES exists, CLI can be skipped.
func TestRegisterAssignedExtensionsIntoProfile(t *testing.T) {
	root := t.TempDir()
	userDataDir := filepath.Join(root, "profile")
	if err := os.MkdirAll(userDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	extID := "abcdefghijklmnopabcdefghijklmnop" // 32 chars, a-p
	pkgDir := filepath.Join(root, "extensions", "imported", extID)
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"manifest_version": 3, "name": "Test Wallet", "version": "1.0.0"}`
	if err := os.WriteFile(filepath.Join(pkgDir, "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	if canSkipLoadExtensionCLI(userDataDir, pkgDir) {
		t.Fatal("precondition: package must NOT be skippable before registration")
	}

	app := &App{}
	registered := app.registerAssignedExtensionsIntoProfile(userDataDir, []string{pkgDir})
	if registered != 1 {
		t.Fatalf("expected 1 package registered, got %d", registered)
	}
	if canSkipLoadExtensionCLI(userDataDir, pkgDir) {
		t.Fatal("prefs-only registration must keep CLI for first Chrome adapt")
	}
	// Idempotent: a second pass must register nothing new.
	if again := app.registerAssignedExtensionsIntoProfile(userDataDir, []string{pkgDir}); again != 0 {
		t.Fatalf("expected idempotent re-run to register 0, got %d", again)
	}
	les := filepath.Join(userDataDir, "Default", "Local Extension Settings", extID)
	if err := os.MkdirAll(les, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(les, "000003.log"), []byte("vault"), 0600); err != nil {
		t.Fatal(err)
	}
	if !canSkipLoadExtensionCLI(userDataDir, pkgDir) {
		t.Fatal("prefs+LES must skip CLI injection")
	}
}

func TestRegisterAssignedExtensionsSkipsInvalidPackage(t *testing.T) {
	root := t.TempDir()
	userDataDir := filepath.Join(root, "profile")
	if err := os.MkdirAll(userDataDir, 0755); err != nil {
		t.Fatal(err)
	}
	// No manifest.json → registration must be skipped, not panic.
	pkgDir := filepath.Join(root, "broken")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if registered := app.registerAssignedExtensionsIntoProfile(userDataDir, []string{pkgDir}); registered != 0 {
		t.Fatalf("expected 0 registered for invalid package, got %d", registered)
	}
	if canSkipLoadExtensionCLI(userDataDir, pkgDir) {
		t.Fatal("invalid package must still require CLI injection")
	}
}
