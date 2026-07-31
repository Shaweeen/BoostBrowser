//go:build windows

package backend

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedLayoutHoldFlagCrossProcess(t *testing.T) {
	root := t.TempDir()
	setLayoutHoldRoot(root)
	defer setLayoutHoldRoot("")

	if sharedLayoutHoldActive() {
		t.Fatal("hold must be inactive before set")
	}
	setSharedLayoutHold(true)
	if !sharedLayoutHoldActive() {
		t.Fatal("hold must be active after set")
	}
	path := layoutHoldFlagPath()
	if path == "" || filepath.Dir(path) != filepath.Join(root, "data") {
		t.Fatalf("unexpected hold path: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("hold flag missing: %v", err)
	}
	setSharedLayoutHold(false)
	if sharedLayoutHoldActive() {
		t.Fatal("hold must clear after release")
	}
}

func TestSharedLayoutHoldAutoExpires(t *testing.T) {
	root := t.TempDir()
	setLayoutHoldRoot(root)
	defer setLayoutHoldRoot("")
	path := layoutHoldFlagPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	// Stale timestamp > 3s ago.
	old := time.Now().UTC().Add(-10 * time.Second).Format(time.RFC3339Nano)
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if sharedLayoutHoldActive() {
		t.Fatal("stale hold flag must auto-expire")
	}
}
