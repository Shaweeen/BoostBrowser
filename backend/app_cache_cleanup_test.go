package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCacheCleanupTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserCleanCachePreservesLoginAndWalletData(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = "data"
	app := NewApp(root)
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	profileID := "profile-1"
	app.browserMgr.Profiles = map[string]*browser.Profile{
		profileID: {
			ProfileId:   profileID,
			ProfileName: "测试实例",
			UserDataDir: "profile-1",
		},
	}

	profileRoot := filepath.Join(root, "data", "profile-1")
	defaultRoot := filepath.Join(profileRoot, "Default")
	mustDelete := []string{
		filepath.Join(defaultRoot, "Cache", "Cache_Data", "img.cache"),
		filepath.Join(defaultRoot, "Code Cache", "js", "app.cache"),
		filepath.Join(defaultRoot, "GPUCache", "gpu.cache"),
		filepath.Join(defaultRoot, "Media Cache", "video.cache"),
		filepath.Join(defaultRoot, "Favicons"),
	}
	for _, path := range mustDelete {
		writeCacheCleanupTestFile(t, path)
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
	}
	for _, path := range mustKeep {
		writeCacheCleanupTestFile(t, path)
	}

	res, err := app.BrowserCleanCache(false)
	if err != nil {
		t.Fatal(err)
	}
	if res.ProfilesScanned != 1 || res.ProfilesCleaned != 1 || res.FilesRemoved == 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	for _, path := range mustDelete {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected cache path removed: %s err=%v", path, err)
		}
	}
	for _, path := range mustKeep {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected profile data preserved: %s err=%v", path, err)
		}
	}
}

func TestCacheAutoCleanRunsOnlyWhenEnabledAndDue(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = "data"
	cfg.Browser.CacheAutoCleanEnabled = true
	cfg.Browser.CacheAutoCleanIntervalDays = 30
	cfg.Browser.CacheLastCleanAt = time.Now().Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	app := NewApp(root)
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	app.browserMgr.Profiles = map[string]*browser.Profile{
		"profile-1": {ProfileId: "profile-1", ProfileName: "测试实例", UserDataDir: "profile-1"},
	}
	cacheFile := filepath.Join(root, "data", "profile-1", "Default", "Cache", "Cache_Data", "img.cache")
	writeCacheCleanupTestFile(t, cacheFile)

	res, err := app.BrowserRunDueCacheAutoClean()
	if err != nil {
		t.Fatal(err)
	}
	if !res.Ran || res.Result == nil || res.Result.FilesRemoved == 0 {
		t.Fatalf("expected auto clean to run, got %+v", res)
	}
	if cfg.Browser.CacheLastCleanAt == "" {
		t.Fatal("expected last clean timestamp to be saved")
	}
}

func TestSanitizeCacheAutoCleanIntervalDaysClampsToSafeRange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want int
	}{
		{0, 30}, {1, 30}, {-5, 30}, {7, 30}, {29, 30}, {30, 30}, {60, 60}, {90, 90}, {365, 90},
	}
	for _, c := range cases {
		if got := sanitizeCacheAutoCleanIntervalDays(c.in); got != c.want {
			t.Fatalf("sanitizeCacheAutoCleanIntervalDays(%d)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestBrowserSaveCacheCleanSettingsPersistsMonthlyInterval(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	app := NewApp(root)
	app.config = cfg

	got, err := app.BrowserSaveCacheCleanSettings(true, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoCleanEnabled || got.IntervalDays != 30 {
		t.Fatalf("unexpected settings: %+v", got)
	}
	if !app.config.Browser.CacheAutoCleanEnabled || app.config.Browser.CacheAutoCleanIntervalDays != 30 {
		t.Fatalf("config not persisted in memory: %+v", app.config.Browser)
	}
	if _, err := os.Stat(filepath.Join(root, "config.yaml")); err != nil {
		t.Fatalf("config.yaml not written: %v", err)
	}
}

func TestCacheAutoCleanDueUsesConfiguredMonthlyInterval(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	app := NewApp(root)
	app.config = cfg
	cfg.Browser.CacheAutoCleanIntervalDays = 30
	cfg.Browser.CacheLastCleanAt = time.Now().Add(-20 * 24 * time.Hour).Format(time.RFC3339)
	if app.cacheAutoCleanDue(time.Now()) {
		t.Fatal("20 days must not be due for a 30-day interval")
	}
	cfg.Browser.CacheLastCleanAt = time.Now().Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	if !app.cacheAutoCleanDue(time.Now()) {
		t.Fatal("31 days must be due for a 30-day interval")
	}
}

func TestBrowserCleanCacheNeverTouchesRunningProfile(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = "data"
	app := NewApp(root)
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	app.browserMgr.Profiles = map[string]*browser.Profile{
		"running": {
			ProfileId:   "running",
			ProfileName: "运行中",
			UserDataDir: "running",
			Running:     true,
			Pid:         1234,
		},
	}
	cacheFile := filepath.Join(root, "data", "running", "Default", "Cache", "Cache_Data", "img.cache")
	writeCacheCleanupTestFile(t, cacheFile)

	// The legacy includeRunning argument remains for API compatibility but is
	// intentionally ignored by the safe cleaner.
	res, err := app.BrowserCleanCache(true)
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedRunning != 1 || res.FilesRemoved != 0 {
		t.Fatalf("running profile was not protected: %+v", res)
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("running profile cache was touched: %v", err)
	}
}
