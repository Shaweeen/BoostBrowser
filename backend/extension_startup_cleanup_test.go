package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPatchChromePreferencesFileDisablesSessionRestore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Preferences")
	initial := map[string]any{
		"session": map[string]any{
			"restore_on_startup": float64(1),
			"startup_urls":       []any{"chrome-extension://abcdef/options.html"},
		},
		"profile": map[string]any{
			"exited_cleanly": false,
			"exit_type":      "Crashed",
		},
	}
	data, _ := json.Marshal(initial)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := patchChromePreferencesFile(path); err != nil {
		t.Fatal(err)
	}
	outData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(outData, &out); err != nil {
		t.Fatal(err)
	}
	session := out["session"].(map[string]any)
	if got := session["restore_on_startup"]; got != float64(4) {
		t.Fatalf("restore_on_startup=%v, want 4 (open startup_urls)", got)
	}
	urls, ok := session["startup_urls"].([]any)
	if !ok || len(urls) != 1 || urls[0] != "about:blank" {
		t.Fatalf("startup_urls must be exactly [about:blank], got %#v", session["startup_urls"])
	}
	browser := out["browser"].(map[string]any)
	if browser["homepage"] != "about:blank" || browser["homepage_is_newtabpage"] != false {
		t.Fatalf("homepage must be about:blank and not NTP: %#v", browser)
	}
	profile := out["profile"].(map[string]any)
	if profile["exited_cleanly"] != true || profile["exit_type"] != "Normal" {
		t.Fatalf("profile clean exit flags not patched: %#v", profile)
	}
	signin := out["signin"].(map[string]any)
	if signin["allowed"] != false || signin["allowed_on_next_startup"] != false || signin["signin_interception_enabled"] != false {
		t.Fatalf("browser sign-in preferences not disabled: %#v", signin)
	}
	syncPrefs := out["sync"].(map[string]any)
	if syncPrefs["requested"] != false || syncPrefs["suppress_start"] != true {
		t.Fatalf("browser sync startup preferences not suppressed: %#v", syncPrefs)
	}
	// 默认搜索引擎现在由 seedDefaultSearchEngine 接管（需要 Web Data 文件），
	// patchChromePreferencesFile 不再负责 search provider 字段。
}

func TestSanitizeChromeStartupPreferencesDoesNotCreateSearchProvider(t *testing.T) {
	dir := t.TempDir()
	sanitizeChromeStartupPreferences(dir)
	prefsPath := filepath.Join(dir, "Default", "Preferences")
	outData, err := os.ReadFile(prefsPath)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(outData, &out); err != nil {
		t.Fatal(err)
	}
	// sanitize 路径不再写默认搜索引擎；那由 seedDefaultSearchEngine 在
	// chrome.exe 启动前以 Web Data SQLite 为权威源的方式处理。
	if _, ok := out["default_search_provider"]; ok {
		t.Fatalf("sanitize should not create default_search_provider; got %#v", out["default_search_provider"])
	}
}

func TestDiscardChromeRestorableTabSessionsRemovesExtensionTabSnapshotsOnly(t *testing.T) {
	root := t.TempDir()
	def := filepath.Join(root, "Default")
	if err := os.MkdirAll(filepath.Join(def, "Sessions"), 0755); err != nil {
		t.Fatal(err)
	}
	// Tab session files that would restore chrome-extension:// pages.
	mustWrite := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(def, "Current Session"), "session-snss")
	mustWrite(filepath.Join(def, "Last Tabs"), "tabs-snss")
	mustWrite(filepath.Join(def, "Sessions", "Tabs_123"), "tabs")
	// Wallet / extension durable data must survive.
	les := filepath.Join(def, "Local Extension Settings", "nkbihfbeogaeaoehlefnkodbefgpgknn")
	if err := os.MkdirAll(les, 0755); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(les, "000003.log")
	mustWrite(vault, "wallet-vault")
	cookie := filepath.Join(def, "Cookies")
	mustWrite(cookie, "cookie-db")

	discardChromeRestorableTabSessions(root)

	if _, err := os.Stat(filepath.Join(def, "Current Session")); !os.IsNotExist(err) {
		t.Fatal("Current Session snapshot must be removed")
	}
	if _, err := os.Stat(filepath.Join(def, "Last Tabs")); !os.IsNotExist(err) {
		t.Fatal("Last Tabs snapshot must be removed")
	}
	if _, err := os.Stat(filepath.Join(def, "Sessions", "Tabs_123")); !os.IsNotExist(err) {
		t.Fatal("Sessions/* tab payload must be removed")
	}
	if _, err := os.Stat(vault); err != nil {
		t.Fatalf("wallet LES must be kept: %v", err)
	}
	if _, err := os.Stat(cookie); err != nil {
		t.Fatalf("Cookies must be kept: %v", err)
	}
}

func TestPatchChromePreferencesFileKeepsExistingSearchProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Preferences")
	initial := map[string]any{
		"default_search_provider": map[string]any{
			"enabled":    true,
			"name":       "Custom",
			"keyword":    "custom.local",
			"search_url": "https://custom.local/search?q={searchTerms}",
		},
	}
	data, _ := json.Marshal(initial)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := patchChromePreferencesFile(path); err != nil {
		t.Fatal(err)
	}
	outData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(outData, &out); err != nil {
		t.Fatal(err)
	}
	provider := out["default_search_provider"].(map[string]any)
	if provider["name"] != "Custom" || provider["search_url"] != "https://custom.local/search?q={searchTerms}" {
		t.Fatalf("existing search provider should be preserved: %#v", provider)
	}
}

func TestSessionRestoreIsAboutBlank(t *testing.T) {
	if sessionRestoreIsAboutBlank(map[string]any{
		"restore_on_startup": float64(5),
	}) {
		t.Fatal("NTP mode must not count as about:blank foundation")
	}
	if !sessionRestoreIsAboutBlank(map[string]any{
		"restore_on_startup": float64(4),
		"startup_urls":       []any{"about:blank"},
	}) {
		t.Fatal("restore=4 + about:blank must be recognized")
	}
}

