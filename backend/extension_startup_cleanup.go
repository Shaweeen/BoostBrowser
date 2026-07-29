package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"boost-browser/backend/internal/logger"
)

// sanitizeChromeStartupPreferences selects one fixed blank startup page without
// deleting session files or extension data. A separate one-shot startup cleanup
// closes extension-created tabs once the environment debug endpoint is ready.
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
	return os.WriteFile(path, []byte("{}"), 0644)
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
	// 4 = open configured URLs. Use about:blank explicitly instead of Chrome's
	// New Tab Page (5), because Google Chrome/Cloak can turn NTP/session restore
	// into google.com/sorry or other large white startup pages that cover the
	// environment list/user workspace.
	if sessionPrefs["restore_on_startup"] != float64(4) {
		sessionPrefs["restore_on_startup"] = 4
		changed = true
	}
	startupURLs, ok := sessionPrefs["startup_urls"].([]any)
	if !ok || len(startupURLs) != 1 || startupURLs[0] != "about:blank" {
		sessionPrefs["startup_urls"] = []any{"about:blank"}
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
	return os.WriteFile(path, out, 0644)
}

func ensureJSONMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

func finalizeBrowserStartupTabs(debugPort int, pid int, profileId string) {
	if debugPort <= 0 {
		return
	}
	// Extensions remain installed and enabled, but their onboarding/unlock
	// pages must not take over every environment at process startup. The
	// explicit about:blank bootstrap tab remains as the only default page.
	// This is deliberately a single startup sweep: the CDP connection is closed
	// before this function returns, so normal browsing and later user extension
	// clicks have no background observer or controller.
	if closed := closeAutomaticExtensionStartupPages(debugPort); closed > 0 {
		logger.New("Browser").Info("已关闭扩展自动启动页面",
			logger.F("profile_id", profileId),
			logger.F("count", closed),
		)
	}
	// Browser windows are launched at their real onscreen position now.  Do not
	// run the legacy restore pass here: it walks the whole Chromium process tree
	// and calls ShowWindow/SetForegroundWindow for every titled top-level HWND.
	// Recent Chrome/Cloak builds create renderer, IME and extension-host windows
	// in child processes; surfacing one of those produces a large, undecorated
	// white window over the page.  The restore pass was only needed when startup
	// deliberately used an offscreen --window-position, which is no longer done.
	_ = pid
}

func closeAutomaticExtensionStartupPages(debugPort int) int {
	targets, err := listCDPTargets(debugPort)
	if err != nil {
		return 0
	}

	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return 0
	}
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return 0
	}
	defer browserClient.close()

	closed := 0
	for _, target := range targets {
		if target.ID == "" || !shouldCloseAutomaticExtensionStartupTarget(target) {
			continue
		}
		if _, err := browserClient.call("Target.closeTarget", map[string]any{"targetId": target.ID}, 1500*time.Millisecond); err == nil {
			closed++
		}
	}
	return closed
}

func shouldCloseAutomaticExtensionStartupTarget(target cdpTarget) bool {
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
	return strings.HasPrefix(u, "chrome-extension://") || strings.HasPrefix(u, "chrome://extensions")
}
