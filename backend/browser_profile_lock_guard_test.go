package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowserSingletonArtifactsPresentUsesFastCleanProfilePath(t *testing.T) {
	t.Parallel()

	userDataDir := t.TempDir()
	if browserSingletonArtifactsPresent(userDataDir) {
		t.Fatal("clean user data directory must not require a Windows process scan")
	}

	if err := os.WriteFile(filepath.Join(userDataDir, "SingletonLock"), []byte("stale"), 0o600); err != nil {
		t.Fatalf("create singleton artifact: %v", err)
	}
	if !browserSingletonArtifactsPresent(userDataDir) {
		t.Fatal("singleton artifact must require the guarded ownership path")
	}
}
