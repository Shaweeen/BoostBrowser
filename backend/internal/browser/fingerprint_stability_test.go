package browser

import (
	"strings"
	"testing"

	"boost-browser/backend/internal/config"
)

func fingerprintSeedValue(args []string) string {
	for _, a := range args {
		trimmed := strings.TrimSpace(a)
		if strings.HasPrefix(strings.ToLower(trimmed), "--fingerprint=") {
			return strings.TrimSpace(trimmed[len("--fingerprint="):])
		}
	}
	return ""
}

func TestDeriveStableFingerprintSeedIsDeterministic(t *testing.T) {
	t.Parallel()
	a1 := DeriveStableFingerprintSeed("profile-1")
	a2 := DeriveStableFingerprintSeed("profile-1")
	if a1 == "" || a1 != a2 {
		t.Fatalf("seed must be stable per identity: %q vs %q", a1, a2)
	}
	if other := DeriveStableFingerprintSeed("profile-2"); other == a1 {
		t.Fatal("different identities must derive different seeds")
	}
}

// ApplyDefaults 缺失种子时不得重新随机整套身份（那会让站点判定为全新浏览器、
// 登录状态每次关闭即失效），而是用稳定派生种子补齐，且幂等。
func TestApplyDefaultsUsesStableSeedWhenMissing(t *testing.T) {
	cfg := config.DefaultConfig()
	mgr := NewManager(cfg, t.TempDir())
	profile := &Profile{
		ProfileId:       "profile-a",
		FingerprintArgs: []string{"--fingerprint-brand=Chrome", "--fingerprint-platform=windows"},
	}
	if !mgr.ApplyDefaults(profile) {
		t.Fatal("first apply must add the missing seed")
	}
	first := fingerprintSeedValue(profile.FingerprintArgs)
	if first == "" {
		t.Fatal("expected a derived seed to be present")
	}
	if first != DeriveStableFingerprintSeed("profile-a") {
		t.Fatalf("seed must be derived from profile ID, got %q", first)
	}

	changed := mgr.ApplyDefaults(profile)
	if changed {
		t.Fatal("second apply must not change the fingerprint")
	}
	if second := fingerprintSeedValue(profile.FingerprintArgs); second != first {
		t.Fatalf("seed changed between applies: %q -> %q", first, second)
	}
	// 已有身份字段必须原样保留（不是被 Strip 后重建）。
	found := false
	for _, a := range profile.FingerprintArgs {
		if a == "--fingerprint-brand=Chrome" {
			found = true
		}
	}
	if !found {
		t.Fatal("existing identity field must be preserved, not regenerated")
	}
}

// 已有种子的环境绝不被 ApplyDefaults 改动种子（完整身份时完全不改动）。
func TestApplyDefaultsKeepsExistingSeedUntouched(t *testing.T) {
	cfg := config.DefaultConfig()
	mgr := NewManager(cfg, t.TempDir())
	profile := &Profile{
		ProfileId: "profile-b",
		FingerprintArgs: []string{
			"--fingerprint=12345",
			"--fingerprint-brand=Chrome",
			"--fingerprint-brand-version=146.0.7680.177",
			"--fingerprint-platform=windows",
			"--fingerprint-platform-version=10.0.0",
		},
	}
	if mgr.ApplyDefaults(profile) {
		t.Fatal("apply must not change a fully-seeded profile")
	}
	if got := fingerprintSeedValue(profile.FingerprintArgs); got != "12345" {
		t.Fatalf("existing seed must be kept, got %q", got)
	}
}

// 编辑保存时前端/旧客户端丢失种子 → Update 必须把旧种子带回来。
func TestUpdatePreservesFingerprintSeedWhenInputLosesIt(t *testing.T) {
	cfg := config.DefaultConfig()
	mgr := NewManager(cfg, t.TempDir())
	created, err := mgr.Create(ProfileInput{
		ProfileName:     "环境A",
		UserDataDir:     "dir-a",
		FingerprintArgs: []string{"--fingerprint-brand=Chrome", "--fingerprint-platform=windows"},
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	originalSeed := fingerprintSeedValue(created.FingerprintArgs)
	if originalSeed == "" {
		t.Fatal("create must assign a seed")
	}

	// 模拟编辑页丢失种子的输入
	updated, err := mgr.Update(created.ProfileId, ProfileInput{
		ProfileName:     "环境A-改",
		FingerprintArgs: []string{"--fingerprint-brand=Chrome", "--lang=en-US"},
	})
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if got := fingerprintSeedValue(updated.FingerprintArgs); got != originalSeed {
		t.Fatalf("update must preserve the identity seed: got %q want %q", got, originalSeed)
	}
}

// 面板进程定期重读 SQLite：新环境出现、删除环境消失、运行时状态保留。
func TestReloadProfilesFromDAOMergesNewAndRemovesDeleted(t *testing.T) {
	_, dao := newProfileDAOTestDB(t)
	mgr := NewManager(config.DefaultConfig(), t.TempDir())
	mgr.ProfileDAO = dao
	if err := dao.UpsertMany([]*Profile{
		{ProfileId: "p1", ProfileName: "one", UserDataDir: "d1"},
		{ProfileId: "p2", ProfileName: "two", UserDataDir: "d2"},
	}); err != nil {
		t.Fatalf("seed profiles failed: %v", err)
	}
	mgr.InitData()

	mgr.Mutex.Lock()
	mgr.Profiles["p1"].Running = true
	mgr.Profiles["p1"].Pid = 4242
	mgr.Mutex.Unlock()

	// 主客户端新增 p3、删除 p2、改名 p1。
	if err := dao.Upsert(&Profile{ProfileId: "p3", ProfileName: "three", UserDataDir: "d3"}); err != nil {
		t.Fatalf("add p3 failed: %v", err)
	}
	if err := dao.Upsert(&Profile{ProfileId: "p1", ProfileName: "one-renamed", UserDataDir: "d1"}); err != nil {
		t.Fatalf("rename p1 failed: %v", err)
	}
	if err := dao.Delete("p2"); err != nil {
		t.Fatalf("delete p2 failed: %v", err)
	}

	mgr.ReloadProfilesFromDAO()

	mgr.Mutex.Lock()
	defer mgr.Mutex.Unlock()
	if _, ok := mgr.Profiles["p3"]; !ok {
		t.Fatal("new profile created in the main client must appear in the panel")
	}
	if _, ok := mgr.Profiles["p2"]; ok {
		t.Fatal("profile deleted in the main client must be dropped from the panel")
	}
	if mgr.Profiles["p1"].ProfileName != "one-renamed" {
		t.Fatalf("profile name must refresh, got %q", mgr.Profiles["p1"].ProfileName)
	}
	if !mgr.Profiles["p1"].Running || mgr.Profiles["p1"].Pid != 4242 {
		t.Fatal("live runtime state must be preserved across the reload")
	}
}
