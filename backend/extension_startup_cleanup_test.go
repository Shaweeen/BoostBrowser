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
		t.Fatalf("restore_on_startup=%v, want 4", got)
	}
	startupURLs, ok := session["startup_urls"].([]any)
	if !ok || len(startupURLs) != 1 || startupURLs[0] != "about:blank" {
		t.Fatalf("startup_urls=%#v, want [about:blank]", session["startup_urls"])
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

func TestIsExtensionStartupURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"chrome-extension://nkbihfbeogaeaoehlefnkodbefgpgknn/home.html", true},
		{"chrome-extension://ekaiemolceheaedaknpealhgfjljmica/home.html#/unlock", true},
		{"chrome://extensions/", true},
		{"https://example.com/chrome-extension://not-a-scheme", false},
		{"about:blank", false},
	}
	for _, tc := range cases {
		if got := isExtensionStartupURL(tc.url); got != tc.want {
			t.Fatalf("isExtensionStartupURL(%q)=%v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestAutomaticExtensionStartupCleanupKeepsBlankAndWebPages(t *testing.T) {
	for _, target := range []cdpTarget{
		{Type: "page", URL: "chrome-extension://wallet/onboarding.html"},
		{Type: "page", URL: "chrome-extension://wallet/unlock.html"},
		{Type: "page", URL: "chrome://extensions/"},
	} {
		if !shouldCloseAutomaticExtensionStartupTarget(target) {
			t.Fatalf("automatic extension startup page must close: %#v", target)
		}
	}
	for _, target := range []cdpTarget{
		{Type: "page", URL: "about:blank"},
		{Type: "page", URL: "chrome://newtab/"},
		{Type: "page", URL: "https://example.com/"},
		{Type: "service_worker", URL: "chrome-extension://wallet/background.js"},
	} {
		if shouldCloseAutomaticExtensionStartupTarget(target) {
			t.Fatalf("blank, web and extension background targets must remain: %#v", target)
		}
	}
}
