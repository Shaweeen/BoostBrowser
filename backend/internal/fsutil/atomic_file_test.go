package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomicReplacesCompleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Preferences")
	if err := WriteFileAtomic(path, []byte(`{"version":"old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte(`{"version":"new","complete":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"version":"new","complete":true}` {
		t.Fatalf("atomic replacement produced partial content: %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file survived atomic replacement: %s", entry.Name())
		}
	}
}
