package cachecleanup

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestCleanProfileRootRemovesOnlyCacheStorageAndCookies(t *testing.T) {
	profileRoot := t.TempDir()
	defaultRoot := filepath.Join(profileRoot, "Default")
	mustDelete := []string{
		filepath.Join(defaultRoot, "Cache", "Cache_Data", "img.cache"),
		filepath.Join(defaultRoot, "Code Cache", "js", "app.cache"),
		filepath.Join(defaultRoot, "GPUCache", "gpu.cache"),
		filepath.Join(defaultRoot, "Service Worker", "CacheStorage", "worker.cache"),
		filepath.Join(defaultRoot, "IndexedDB", "https_app_0.indexeddb.leveldb", "000003.log"),
		filepath.Join(defaultRoot, "Local Storage", "leveldb", "000004.log"),
		filepath.Join(defaultRoot, "Session Storage", "000005.log"),
		filepath.Join(defaultRoot, "WebStorage", "QuotaManager"),
		filepath.Join(defaultRoot, "Storage", "ext", "cache.bin"),
		filepath.Join(defaultRoot, "Cookies"),
		filepath.Join(defaultRoot, "Cookies-journal"),
	}
	for _, path := range mustDelete {
		writeTestFile(t, path)
	}
	mustKeep := []string{
		filepath.Join(defaultRoot, "Bookmarks"),
		filepath.Join(defaultRoot, "Preferences"),
		filepath.Join(defaultRoot, "History"),
	}
	for _, path := range mustKeep {
		writeTestFile(t, path)
	}

	res, err := CleanProfileRoot(profileRoot)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesRemoved == 0 || res.BytesRemoved == 0 {
		t.Fatalf("expected files removed, got %+v", res)
	}
	for _, path := range mustDelete {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected removed: %s err=%v", path, err)
		}
	}
	for _, path := range mustKeep {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected kept: %s err=%v", path, err)
		}
	}
}
