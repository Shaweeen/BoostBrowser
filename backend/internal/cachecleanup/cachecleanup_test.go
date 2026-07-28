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

func TestCleanProfileRootRemovesOnlyRegenerableCache(t *testing.T) {
	profileRoot := t.TempDir()
	defaultRoot := filepath.Join(profileRoot, "Default")
	mustDelete := []string{
		filepath.Join(defaultRoot, "Cache", "Cache_Data", "img.cache"),
		filepath.Join(defaultRoot, "Network", "Cache", "Cache_Data", "net.cache"),
		filepath.Join(defaultRoot, "Code Cache", "js", "app.cache"),
		filepath.Join(defaultRoot, "GPUCache", "gpu.cache"),
		filepath.Join(defaultRoot, "Media Cache", "video.cache"),
		filepath.Join(defaultRoot, "Favicons"),
		filepath.Join(profileRoot, "chrome_debug.log"),
	}
	for _, path := range mustDelete {
		writeTestFile(t, path)
	}
	mustKeep := []string{
		filepath.Join(defaultRoot, "Bookmarks"),
		filepath.Join(defaultRoot, "Preferences"),
		filepath.Join(defaultRoot, "History"),
		filepath.Join(defaultRoot, "Cookies"),
		filepath.Join(defaultRoot, "Network", "Cookies"),
		filepath.Join(defaultRoot, "Service Worker", "CacheStorage", "worker.cache"),
		filepath.Join(defaultRoot, "IndexedDB", "chrome-extension_wallet.indexeddb.leveldb", "000003.log"),
		filepath.Join(defaultRoot, "Local Storage", "leveldb", "000004.log"),
		filepath.Join(defaultRoot, "Session Storage", "000005.log"),
		filepath.Join(defaultRoot, "WebStorage", "QuotaManager"),
		filepath.Join(defaultRoot, "Storage", "ext", "wallet.bin"),
		filepath.Join(defaultRoot, "Local Extension Settings", "wallet-id", "000006.log"),
		filepath.Join(defaultRoot, "Local Extension Settings", "wallet-id", "Cache", "state.bin"),
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

func TestIsDisposableRelativePathForBackupScope(t *testing.T) {
	cases := map[string]bool{
		"profile-1/Default/Cache/data/file":                         true,
		"profile-1/Default/Network/Cache/data/file":                 true,
		"profile-1/Default/Media Cache/video":                       true,
		"profile-1/Default/Favicons":                                true,
		"profile-1/Default/Cookies":                                 false,
		"profile-1/Default/IndexedDB/wallet/000003.log":             false,
		"profile-1/Default/Local Extension Settings/id/000003.log":  false,
		"profile-1/Default/Local Extension Settings/id/Cache/state": false,
		"profile-1/GPUCache/data":                                   true,
		"profile-1/chrome_debug.log":                                true,
	}
	for path, want := range cases {
		if got := IsDisposableRelativePath(path); got != want {
			t.Fatalf("IsDisposableRelativePath(%q)=%v want %v", path, got, want)
		}
	}
}
