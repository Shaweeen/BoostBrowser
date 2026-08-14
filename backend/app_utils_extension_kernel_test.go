package backend

import (
	"boost-browser/backend/internal/browser"
	"os"
	"path/filepath"
	"testing"
)

func TestChromeForTestingKernelRequiresCompatibilityMarker(t *testing.T) {
	dir := t.TempDir()
	if isChromeForTestingKernelDir(dir) {
		t.Fatal("unmarked branded Chrome directory must not be treated as extension-compatible")
	}
	marker := filepath.Join(dir, "chrome-for-testing.marker")
	if err := os.WriteFile(marker, []byte("Chrome for Testing\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !isChromeForTestingKernelDir(dir) {
		t.Fatal("verified Chrome for Testing marker should enable the fallback kernel")
	}
}

func TestBundledGoogleCorePrefersChrome148OverNewerMajor(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"149.0.8000.1", "148.0.7778.167"} {
		dir := filepath.Join(root, "chrome", "google-"+version)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		exe := filepath.Join(dir, filepath.FromSlash(browser.CoreExecutableCandidates()[0]))
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, []byte("test"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "chrome-for-testing.marker"), []byte("Chrome for Testing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	app := NewApp(root)
	path, version := app.findBundledGoogleChromeCore()
	if version != "148.0.7778.167" || filepath.Base(path) != "google-148.0.7778.167" {
		t.Fatalf("selected path=%q version=%q", path, version)
	}
}
