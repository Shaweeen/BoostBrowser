package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
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
	if got := session["restore_on_startup"]; got != float64(5) {
		t.Fatalf("restore_on_startup=%v, want 5", got)
	}
	if _, exists := session["startup_urls"]; exists {
		t.Fatalf("startup_urls must be removed so Chrome remains the only initial-page owner: %#v", session["startup_urls"])
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
	if !shouldCloseAutomaticExtensionStartupTarget(cdpTarget{
		Type:     "page",
		URL:      "chrome-extension://wallet/onboarding.html",
		OpenerID: "extension-background",
	}) {
		t.Fatal("an extension startup page must close regardless of extension-reported opener metadata")
	}
}

func TestCloseAutomaticExtensionStartupPagesLeavesBlanksUntouched(t *testing.T) {
	fetches := 0
	closed := map[string]bool{}
	n := closeAutomaticExtensionStartupPagesOnce(
		func() ([]cdpTarget, error) {
			fetches++
			return []cdpTarget{
				{ID: "blank-1", Type: "page", URL: "about:blank"},
				{ID: "blank-2", Type: "page", URL: "about:blank"},
				{ID: "metamask", Type: "page", URL: "chrome-extension://jjinamgbgbggacldmehfllejmllfecgim/home.html#/onboarding/welcome"},
				{ID: "worker", Type: "service_worker", URL: "chrome-extension://jjinamgbgbggacldmehfllejmllfecgim/background.js"},
				{ID: "web", Type: "page", URL: "https://example.com/"},
			}, nil
		},
		func(targetID string) error {
			closed[targetID] = true
			return nil
		},
	)
	if fetches != 1 {
		t.Fatalf("must use a single snapshot: fetches=%d", fetches)
	}
	if n != 1 || !closed["metamask"] {
		t.Fatalf("only extension auto page must close: n=%d closed=%#v", n, closed)
	}
	if closed["blank-1"] || closed["blank-2"] || closed["worker"] || closed["web"] {
		t.Fatalf("blanks, workers and web pages must never be closed: %#v", closed)
	}
}

func TestIsExtensionStartupURLDoesNotMatchBlank(t *testing.T) {
	if isExtensionStartupURL("about:blank") || isExtensionStartupURL("chrome://new-tab-page/") {
		t.Fatal("blank/new-tab must not be treated as extension startup pages")
	}
	if !isExtensionStartupURL("chrome://welcome") {
		t.Fatal("chrome welcome page should still be closed at startup")
	}
	// MetaMask onboarding from the user screenshot must be closed.
	mm := "chrome-extension://jjinamgbgbggacldmehfllejmllfecgim/home.html#/onboarding/welcome"
	if !isExtensionStartupURL(mm) {
		t.Fatal("MetaMask onboarding URL must be closed at startup")
	}
}

func TestNormalizeBrowserTabsToSingleBlankClosesExtensionKeepsOneBlank(t *testing.T) {
	// Unit-level policy check via classifiers used by normalizeBrowserTabsToSingleBlank.
	if !isExtensionStartupURL("chrome-extension://x/home.html#/onboarding/welcome") {
		t.Fatal("onboarding must be closable")
	}
	if isNaturalBlankPageTarget(cdpTarget{Type: "page", URL: "about:blank"}) != true {
		t.Fatal("about:blank is the sole kept shell")
	}
	if isNaturalBlankPageTarget(cdpTarget{Type: "page", URL: "https://example.com"}) {
		t.Fatal("https pages must not count as blank shell")
	}
}

func TestStartupExtensionAutoTabCloseScheduleIsDiscreteNotContinuous(t *testing.T) {
	if len(startupExtensionAutoTabCloseSchedule) < 2 {
		t.Fatal("need discrete follow-up passes for delayed wallet onboarding")
	}
	// Total span stays short (seconds, not session-long).
	last := startupExtensionAutoTabCloseSchedule[len(startupExtensionAutoTabCloseSchedule)-1]
	if last > 5*time.Second {
		t.Fatalf("schedule too long for a non-watcher design: %v", last)
	}
	// Delays are absolute from ready and strictly increasing after the first.
	for i := 1; i < len(startupExtensionAutoTabCloseSchedule); i++ {
		if startupExtensionAutoTabCloseSchedule[i] <= startupExtensionAutoTabCloseSchedule[i-1] {
			t.Fatalf("schedule must be strictly increasing: %v", startupExtensionAutoTabCloseSchedule)
		}
	}
}
