package backend

import (
	"boost-browser/backend/internal/fsutil"
	"boost-browser/backend/internal/logger"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sanitizeChromeStartupPreferences disables explicit URL/session restoration
// without deleting session files or extension data. Chrome owns its natural
// initial page; BrowserStudio does not configure or pass a replacement page.
func sanitizeChromeStartupPreferences(userDataDir string) {
	if strings.TrimSpace(userDataDir) == "" {
		return
	}
	defaultPrefsPath := filepath.Join(userDataDir, "Default", "Preferences")
	_ = ensureChromePreferencesFile(defaultPrefsPath)
	for _, rel := range []string{
		filepath.Join("Default", "Preferences"),
		"Preferences",
	} {
		path := filepath.Join(userDataDir, rel)
		_ = patchChromePreferencesFile(path)
	}
}

func ensureChromePreferencesFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, []byte("{}"), 0644)
}

func patchChromePreferencesFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return err
	}
	var prefs map[string]any
	if err := json.Unmarshal(data, &prefs); err != nil {
		return err
	}

	changed := false
	browserPrefs := ensureJSONMap(prefs, "browser")
	if browserPrefs["check_default_browser"] != false {
		browserPrefs["check_default_browser"] = false
		changed = true
	}

	sessionPrefs := ensureJSONMap(prefs, "session")
	// Let Chrome create its natural initial page. Configured startup URLs,
	// restored sessions and positional launch URLs are competing tab owners and
	// must not participate in a managed environment launch.
	if sessionPrefs["restore_on_startup"] != float64(5) {
		sessionPrefs["restore_on_startup"] = 5
		changed = true
	}
	if _, exists := sessionPrefs["startup_urls"]; exists {
		delete(sessionPrefs, "startup_urls")
		changed = true
	}

	profilePrefs := ensureJSONMap(prefs, "profile")
	if profilePrefs["exited_cleanly"] != true {
		profilePrefs["exited_cleanly"] = true
		changed = true
	}
	if profilePrefs["exit_type"] != "Normal" {
		profilePrefs["exit_type"] = "Normal"
		changed = true
	}

	// Keep every isolated environment local-only by default. Windows enterprise
	// policy is authoritative for Chrome, while these preferences cover Chromium
	// variants that do not implement every Google policy hook.
	signinPrefs := ensureJSONMap(prefs, "signin")
	if signinPrefs["allowed"] != false {
		signinPrefs["allowed"] = false
		changed = true
	}
	if signinPrefs["allowed_on_next_startup"] != false {
		signinPrefs["allowed_on_next_startup"] = false
		changed = true
	}
	if signinPrefs["signin_interception_enabled"] != false {
		signinPrefs["signin_interception_enabled"] = false
		changed = true
	}
	syncPrefs := ensureJSONMap(prefs, "sync")
	if syncPrefs["requested"] != false {
		syncPrefs["requested"] = false
		changed = true
	}
	if syncPrefs["suppress_start"] != true {
		syncPrefs["suppress_start"] = true
		changed = true
	}

	// 默认搜索引擎由 seedDefaultSearchEngine（chrome_search_engine_seed.go）处理，
	// 那条路径会同时写 Web Data + Preferences 的 mirrored_template_url_data，
	// 与 cloak 内核 UI 操作产生的字段名一致。这里不再重复写。

	if !changed {
		return nil
	}
	out, err := json.MarshalIndent(prefs, "", "   ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, out, 0644)
}

func ensureJSONMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

const (
	// Single-shot close of automatic *extension* pages only at debug-ready.
	// The browser owns its natural blank startup tab — we never create, keep,
	// or close about:blank / new-tab pages (no mutually exclusive blank logic).
	startupPageCloseTargetTimeout = 800 * time.Millisecond
)

// finalizeBrowserStartupTabs runs exactly once at environment debug-ready:
// close chrome-extension:// (and similar) auto-opened pages only.
// Blank/new-tab pages are left entirely to Chromium.
func finalizeBrowserStartupTabs(debugPort int, profileId string) {
	if debugPort <= 0 {
		return
	}
	closedExtensions := runStartupTabCleanupOnce(debugPort)
	if closedExtensions > 0 {
		logger.New("Browser").Info("启动时已单次关闭扩展自动页（不触碰空白标签）",
			logger.F("profile_id", profileId),
			logger.F("closed_extension_pages", closedExtensions),
		)
	}
}

func runStartupTabCleanupOnce(debugPort int) int {
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return 0
	}
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return 0
	}
	defer browserClient.close()

	return closeAutomaticExtensionStartupPagesOnce(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		func(targetID string) error {
			_, closeErr := browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, startupPageCloseTargetTimeout)
			return closeErr
		},
	)
}

// closeAutomaticExtensionStartupPagesOnce closes only automatic extension
// startup pages. It does not open blanks, close blanks, or manage new-tab pages.
func closeAutomaticExtensionStartupPagesOnce(
	fetch func() ([]cdpTarget, error),
	closeTarget func(string) error,
) int {
	if fetch == nil || closeTarget == nil {
		return 0
	}
	targets, err := fetch()
	if err != nil {
		return 0
	}
	closed := 0
	for _, target := range targets {
		if target.ID == "" || !shouldCloseAutomaticExtensionStartupTarget(target) {
			continue
		}
		if closeTarget(target.ID) == nil {
			closed++
		}
	}
	return closed
}

func shouldCloseAutomaticExtensionStartupTarget(target cdpTarget) bool {
	// Only the single startup snapshot may call this. Blank/new-tab pages are
	// never closed. After handoff there is no watcher.
	return strings.EqualFold(strings.TrimSpace(target.Type), "page") &&
		isExtensionStartupURL(target.URL)
}

func listCDPTargets(debugPort int) ([]cdpTarget, error) {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", debugPort))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var targets []cdpTarget
	if err := json.Unmarshal(body, &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func isExtensionStartupURL(rawURL string) bool {
	u := strings.TrimSpace(strings.ToLower(rawURL))
	if u == "" {
		return false
	}
	if strings.HasPrefix(u, "chrome-extension://") || strings.HasPrefix(u, "chrome://extensions") {
		return true
	}
	// Chromium first-run / product pages that compete with the single blank shell.
	for _, prefix := range []string{
		"chrome://welcome",
		"chrome://whats-new",
		"chrome://settings/help",
	} {
		if u == prefix || strings.HasPrefix(u, prefix+"/") || strings.HasPrefix(u, prefix+"#") {
			return true
		}
	}
	return false
}
