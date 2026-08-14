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
	cfg.Browser.CacheAutoCleanIntervalDays = 3
	cfg.Browser.CacheLastCleanAt = time.Now().Add(-4 * 24 * time.Hour).Format(time.RFC3339)
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

func TestCacheAutoCleanWaitsForCustomInterval(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = "data"
	cfg.Browser.CacheAutoCleanEnabled = true
	cfg.Browser.CacheAutoCleanIntervalDays = 30
	cfg.Browser.CacheLastCleanAt = time.Now().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
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
	if res.Ran {
		t.Fatalf("custom interval ran too early: %+v", res)
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("cache was touched before custom interval: %v", err)
	}
}

func TestCacheCleanSettingsUseCustomIntervalAndValidateRange(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	app := NewApp(root)
	app.config = cfg

	settings, err := app.BrowserSaveCacheCleanSettings(true, 21)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.AutoCleanEnabled || settings.IntervalDays != 21 {
		t.Fatalf("custom interval not saved: %+v", settings)
	}
	if _, err := app.BrowserSaveCacheCleanSettings(true, 0); err == nil {
		t.Fatal("expected zero-day interval to be rejected")
	}
	if _, err := app.BrowserSaveCacheCleanSettings(true, 366); err == nil {
		t.Fatal("expected interval above maximum to be rejected")
	}
}

func TestSyncPanelCannotOwnCacheCleanup(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	app := NewApp(root, true)
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	app.browserMgr.Profiles = map[string]*browser.Profile{}

	if _, err := app.BrowserCleanCache(false); err == nil {
		t.Fatal("sync panel must not execute cache cleanup")
	}
	res, err := app.BrowserRunDueCacheAutoClean()
	if err != nil || res.Ran || res.Reason == "" {
		t.Fatalf("sync panel scheduler guard failed: result=%+v err=%v", res, err)
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

func TestBrowserCleanCacheSkipsLiveUserDataDirEvenWhenRuntimeStateIsStale(t *testing.T) {
	root := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Browser.UserDataRoot = "data"
	app := NewApp(root)
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	app.browserMgr.Profiles = map[string]*browser.Profile{
		"stale": {ProfileId: "stale", ProfileName: "运行态过期", UserDataDir: "stale"},
	}
	profileRoot := filepath.Join(root, "data", "stale")
	cacheFile := filepath.Join(profileRoot, "Default", "Cache", "Cache_Data", "img.cache")
	writeCacheCleanupTestFile(t, cacheFile)

	res, err := app.browserCleanCacheWithLiveRoots(map[string]struct{}{
		normalizeCacheProfileRoot(profileRoot): {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedRunning != 1 || res.SkippedLiveProcess != 1 || res.FilesRemoved != 0 {
		t.Fatalf("live process guard failed: %+v", res)
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("live profile cache was touched: %v", err)
	}
}
