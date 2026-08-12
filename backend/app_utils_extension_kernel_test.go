package backend

import (
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
