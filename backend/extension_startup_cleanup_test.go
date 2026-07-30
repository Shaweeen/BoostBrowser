package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestPlanStartupPageCleanupKeepsOneNaturalBlank(t *testing.T) {
	targets := []cdpTarget{
		{ID: "blank-1", Type: "page", URL: "about:blank"},
		{ID: "blank-2", Type: "page", URL: "about:blank"},
		{ID: "blank-3", Type: "page", URL: "about:blank"},
		{ID: "metamask", Type: "page", URL: "chrome-extension://jjinamgbgbggacldmehfllejmllfecgim/home.html#/onboarding/welcome"},
		{ID: "worker", Type: "service_worker", URL: "chrome-extension://jjinamgbgbggacldmehfllejmllfecgim/background.js"},
		{ID: "web", Type: "page", URL: "https://example.com/"},
	}
	actions := planStartupPageCleanup(targets)
	got := map[string]startupPageCloseKind{}
	for _, action := range actions {
		got[action.targetID] = action.kind
	}
	want := map[string]startupPageCloseKind{
		"blank-2":  startupPageCloseExtraBlank,
		"blank-3":  startupPageCloseExtraBlank,
		"metamask": startupPageCloseExtension,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("startup cleanup actions mismatch: got=%#v want=%#v", got, want)
	}
	if _, exists := got["worker"]; exists {
		t.Fatal("extension service worker must remain available")
	}
	if _, exists := got["web"]; exists {
		t.Fatal("normal web page must remain available")
	}
}

func TestCloseUnwantedStartupPagesDuringLaunchClosesDelayedTargetsAndReleases(t *testing.T) {
	calls := 0
	fetch := func() ([]cdpTarget, error) {
		calls++
		targets := []cdpTarget{
			{ID: "blank-1", Type: "page", URL: "about:blank"},
			{ID: "blank-2", Type: "page", URL: "about:blank"},
		}
		// Appear after the first quiet sample so delayed wallet pages are still
		// covered by the bounded observation window.
		if calls == 2 {
			targets = append(targets, cdpTarget{
				ID: "metamask-auto", Type: "page",
				URL: "chrome-extension://jjinamgbgbggacldmehfllejmllfecgim/home.html#/onboarding/welcome",
			})
		}
		return targets, nil
	}
	closed := map[string]bool{}
	pauses := 0
	closedExtensions, closedBlanks := closeUnwantedStartupPagesDuringLaunch(
		fetch,
		func(targetID string) error {
			closed[targetID] = true
			return nil
		},
		func(time.Duration) { pauses++ },
		8,
		2,
	)
	// pass1 quiet blanks, pass2 close extension+extra blank, pass3+pass4 quiet release
	if calls != 4 {
		t.Fatalf("startup cleanup must include the delayed target and release after quiet passes: calls=%d", calls)
	}
	if closedExtensions != 1 || closedBlanks != 1 {
		t.Fatalf("unexpected close counts: extension=%d blanks=%d", closedExtensions, closedBlanks)
	}
	for _, id := range []string{"blank-2", "metamask-auto"} {
		if !closed[id] {
			t.Fatalf("startup target %q was not closed: %#v", id, closed)
		}
	}
	if closed["blank-1"] {
		t.Fatal("the browser core's first natural blank page must remain")
	}
	if pauses != calls-1 {
		t.Fatalf("startup cleanup pause count mismatch: pauses=%d calls=%d", pauses, calls)
	}
}

func TestCloseUnwantedStartupPagesDuringLaunchReleasesWhenNoExtensionAppears(t *testing.T) {
	fetches := 0
	pauses := 0
	closedExtensions, closedBlanks := closeUnwantedStartupPagesDuringLaunch(
		func() ([]cdpTarget, error) {
			fetches++
			return []cdpTarget{{ID: "blank-1", Type: "page", URL: "about:blank"}}, nil
		},
		func(string) error {
			t.Fatal("the only natural blank must never be closed")
			return nil
		},
		func(time.Duration) { pauses++ },
		10,
		2,
	)
	if closedExtensions != 0 || closedBlanks != 0 {
		t.Fatalf("unexpected close counts: extension=%d blanks=%d", closedExtensions, closedBlanks)
	}
	// Quiet-pass early exit: no extension UI means release after quietPasses only.
	if fetches != 2 || pauses != 1 {
		t.Fatalf("quiet early-exit mismatch: fetches=%d pauses=%d", fetches, pauses)
	}
}

func TestEnsureSingleNaturalBlankStartupPageCreatesWhenEmpty(t *testing.T) {
	created := false
	changed := ensureSingleNaturalBlankStartupPage(
		func() ([]cdpTarget, error) {
			return []cdpTarget{{ID: "ext", Type: "page", URL: "chrome-extension://wallet/home.html"}}, nil
		},
		func() (string, error) {
			created = true
			return "blank-new", nil
		},
		func(string) error { return nil },
	)
	if !changed || !created {
		t.Fatalf("expected blank creation when no natural blank remains: changed=%v created=%v", changed, created)
	}
}

func TestEnsureSingleNaturalBlankStartupPageTrimsExtras(t *testing.T) {
	closed := map[string]bool{}
	changed := ensureSingleNaturalBlankStartupPage(
		func() ([]cdpTarget, error) {
			return []cdpTarget{
				{ID: "blank-1", Type: "page", URL: "about:blank"},
				{ID: "blank-2", Type: "page", URL: "chrome://new-tab-page/"},
			}, nil
		},
		func() (string, error) {
			t.Fatal("must not create blank when one already exists")
			return "", nil
		},
		func(id string) error {
			closed[id] = true
			return nil
		},
	)
	if !changed || !closed["blank-2"] || closed["blank-1"] {
		t.Fatalf("expected only extra blank closed: changed=%v closed=%#v", changed, closed)
	}
}

func TestIsNaturalBlankRecognizesNewTabPage(t *testing.T) {
	if !isNaturalBlankPageTarget(cdpTarget{Type: "page", URL: "chrome://new-tab-page/"}) {
		t.Fatal("chrome new-tab-page must count as the natural blank shell")
	}
	if isExtensionStartupURL("chrome://welcome") != true {
		t.Fatal("chrome welcome page should be closed at startup")
	}
}
