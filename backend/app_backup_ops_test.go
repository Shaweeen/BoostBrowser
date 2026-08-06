package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"boost-browser/backend/internal/database"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRunningProfilesBlocksMutableDataMaintenance(t *testing.T) {
	app := &App{
		browserMgr: &browser.Manager{
			Profiles: map[string]*browser.Profile{
				"stopped": {ProfileId: "stopped", Running: false},
				"running": {ProfileId: "running", Running: true},
			},
		},
	}
	running := app.backupRunningProfiles()
	if len(running) != 1 || running[0] != "running" {
		t.Fatalf("运行环境识别错误: %+v", running)
	}
}

func TestBackupEnsureZipSuffix(t *testing.T) {
	if got := backupEnsureZipSuffix("c:/tmp/a.zip"); got != "c:/tmp/a.zip" {
		t.Fatalf("zip 后缀重复追加: %s", got)
	}
	if got := backupEnsureZipSuffix("c:/tmp/a"); got != "c:/tmp/a.zip" {
		t.Fatalf("zip 后缀追加失败: %s", got)
	}
}

func TestBackupMergeConfigDedup(t *testing.T) {
	current := config.DefaultConfig()
	current.App.MaxProfileLimit = 12
	current.App.UsedCDKeys = []string{"A1", "B2"}
	current.Browser.DefaultBookmarks = []config.BrowserBookmark{
		{Name: "Google", URL: "https://www.google.com/"},
	}
	current.Browser.Proxies = []config.BrowserProxy{
		{ProxyId: "p1", ProxyName: "P1", ProxyConfig: "http://127.0.0.1:7890"},
	}
	current.Browser.Cores = []config.BrowserCore{
		{CoreId: "c1", CoreName: "C1", CorePath: "chrome/c1"},
	}
	current.Browser.Profiles = []config.BrowserProfileConfig{
		{ProfileId: "u1", ProfileName: "U1", UserDataDir: "u1"},
	}

	incoming := config.DefaultConfig()
	incoming.App.UsedCDKeys = []string{"b2", "C3"}
	incoming.Browser.DefaultBookmarks = []config.BrowserBookmark{
		{Name: "Google Dup", URL: "https://www.google.com/"},
		{Name: "ChatGPT", URL: "https://chatgpt.com/"},
	}
	incoming.Browser.Proxies = []config.BrowserProxy{
		{ProxyId: "p1", ProxyName: "P1 Dup", ProxyConfig: "http://127.0.0.1:7890"},
		{ProxyId: "p2", ProxyName: "P2", ProxyConfig: "socks5://127.0.0.1:1080"},
	}
	incoming.Browser.Cores = []config.BrowserCore{
		{CoreId: "c1", CoreName: "C1 Dup", CorePath: "chrome/c1"},
		{CoreId: "c2", CoreName: "C2", CorePath: "chrome/c2"},
	}
	incoming.Browser.Profiles = []config.BrowserProfileConfig{
		{ProfileId: "u1", ProfileName: "U1 Dup", UserDataDir: "u1"},
		{ProfileId: "u2", ProfileName: "U2", UserDataDir: "u2"},
	}

	merged := backupMergeConfig(current, incoming)
	if merged == nil {
		t.Fatalf("merged 为空")
	}

	if merged.App.MaxProfileLimit != 12 {
		t.Fatalf("license limit 不应被导入配置改写: got=%d", merged.App.MaxProfileLimit)
	}
	if len(merged.App.UsedCDKeys) != 2 {
		t.Fatalf("used cd keys 不应被导入配置改写: %+v", merged.App.UsedCDKeys)
	}
	if len(merged.Browser.DefaultBookmarks) != 2 {
		t.Fatalf("bookmarks 判重失败: %+v", merged.Browser.DefaultBookmarks)
	}
	if len(merged.Browser.Proxies) != 2 {
		t.Fatalf("proxies 判重失败: %+v", merged.Browser.Proxies)
	}
	if len(merged.Browser.Cores) != 2 {
		t.Fatalf("cores 判重失败: %+v", merged.Browser.Cores)
	}
	if len(merged.Browser.Profiles) != 2 {
		t.Fatalf("profiles 判重失败: %+v", merged.Browser.Profiles)
	}
}

// TestBackupReloadAfterMutationDoesNotResurrectClearedProfiles guards the
// reset/import ordering: backupReloadAfterMutation must drop the in-memory
// profile map BEFORE ReloadConfig runs. Otherwise ReloadConfig's internal
// proxy-binding reconcile iterates the stale (pre-import) profiles and, when
// a binding would change, calls SaveProfiles — writing old environments back
// into the database that was just cleared for a reset-import.
func TestBackupReloadAfterMutationDoesNotResurrectClearedProfiles(t *testing.T) {
	root := t.TempDir()

	// Prepare a real SQLite database (empty profile table, like right after a
	// reset/initialize).
	db, err := database.NewDB(filepath.Join(root, "app.db"))
	if err != nil {
		t.Fatalf("创建测试数据库失败: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("数据库迁移失败: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Browser.Proxies = []config.BrowserProxy{
		{ProxyId: "p1", ProxyName: "P1", ProxyConfig: "http://127.0.0.1:7890"},
	}

	app := NewApp(root)
	app.db = db
	app.config = cfg
	app.browserMgr = browser.NewManager(cfg, root)
	app.browserMgr.ProfileDAO = browser.NewSQLiteProfileDAO(db.GetConn())
	app.browserMgr.ProxyDAO = browser.NewSQLiteProxyDAO(db.GetConn())
	app.browserMgr.CoreDAO = browser.NewSQLiteCoreDAO(db.GetConn())
	app.browserMgr.BookmarkDAO = browser.NewSQLiteBookmarkDAO(db.GetConn())
	app.browserMgr.GroupDAO = browser.NewSQLiteGroupDAO(db.GetConn())

	// The backup package carries proxies in its own database/config, so the
	// proxy pool is non-empty even though the profile table was just cleared.
	// Without this, ReloadConfig would load a default (proxy-less) config and
	// the stale binding could never be re-resolved — masking the regression.
	if err := app.browserMgr.ProxyDAO.Upsert(browser.Proxy{
		ProxyId:     "p1",
		ProxyName:   "P1",
		ProxyConfig: "http://127.0.0.1:7890",
	}); err != nil {
		t.Fatalf("预置代理失败: %v", err)
	}
	// ReloadConfig 会重新读盘；写一份含 p1 的 config.yaml，保持内存/磁盘一致。
	if err := cfg.Save(filepath.Join(root, "config.yaml")); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	// Seed a stale pre-import environment in memory: proxy binding to p1 with
	// an empty config, so reconcile would change it and persist the row back.
	app.browserMgr.Mutex.Lock()
	app.browserMgr.Profiles["stale-env"] = &browser.Profile{
		ProfileId:   "stale-env",
		ProfileName: "旧环境",
		UserDataDir: "stale-env",
		ProxyId:     "p1",
		ProxyConfig: "",
	}
	app.browserMgr.Mutex.Unlock()

	if err := app.backupReloadAfterMutation(); err != nil {
		t.Fatalf("backupReloadAfterMutation 失败: %v", err)
	}

	rows, err := app.browserMgr.ProfileDAO.List()
	if err != nil {
		t.Fatalf("读取数据库实例失败: %v", err)
	}
	for _, p := range rows {
		if p.ProfileId == "stale-env" {
			t.Fatalf("重置/导入后旧环境被重新写回数据库: %+v", p)
		}
	}
	// 注意：migrateToSQLite 在实例表为空时自动创建「默认实例」是既有行为；
	// 本测试只验证导入前的旧内存环境不会被 reconcile 写回。
	if _, exists := app.browserMgr.Profiles["stale-env"]; exists {
		t.Fatal("重置/导入后旧环境仍残留内存")
	}
}

func TestBackupSyncDirConflictAndOverwrite(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	dst := filepath.Join(t.TempDir(), "dst")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}

	srcFile := filepath.Join(src, "a.txt")
	dstFile := filepath.Join(dst, "a.txt")
	if err := os.WriteFile(srcFile, []byte("new-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstFile, []byte("old-content"), 0644); err != nil {
		t.Fatal(err)
	}

	stats := &backupMergeStats{}
	if err := backupSyncDir(src, dst, false, stats, nil); err != nil {
		t.Fatal(err)
	}
	if stats.Conflicts != 1 || stats.Imported != 0 {
		t.Fatalf("非覆盖模式统计异常: %+v", stats)
	}
	got, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old-content" {
		t.Fatalf("非覆盖模式不应改写目标文件: %s", string(got))
	}

	stats2 := &backupMergeStats{}
	if err := backupSyncDir(src, dst, true, stats2, nil); err != nil {
		t.Fatal(err)
	}
	if stats2.Imported != 1 {
		t.Fatalf("覆盖模式导入统计异常: %+v", stats2)
	}
	got2, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != "new-content" {
		t.Fatalf("覆盖模式应改写目标文件: %s", string(got2))
	}
}
