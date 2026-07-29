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

const (
	extensionStartupLoadTimeout      = 2 * time.Second
	extensionStartupCompletionWindow = 500 * time.Millisecond
	extensionStartupProbeDelay       = 75 * time.Millisecond
)

// sanitizeChromeStartupPreferences selects one fixed blank startup page without
// deleting session files or extension data. A bounded startup barrier closes
// extension-created tabs after assigned extension targets finish loading.
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
	// The command line is the only owner of the single about:blank target.
	// Keeping about:blank here as a configured startup URL makes Chrome create
	// its initial target plus a second configured target.
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

func finalizeBrowserStartupTabs(debugPort int, pid int, profileId string, launchArgs []string) {
	if debugPort <= 0 {
		return
	}
	// Extensions remain installed and enabled, but their onboarding/unlock
	// pages must not take over every environment at process startup. The bounded
	// startup barrier ends after the assigned extensions have exposed their CDP
	// targets (or the deadline expires), closes their top-level pages once, and
	// releases every HTTP/CDP connection before returning. No worker, listener,
	// timer or target registry remains after the environment is shown.
	if closed := closeAutomaticExtensionStartupPages(debugPort, managedExtensionIDsFromLaunchArgs(launchArgs)); closed > 0 {
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

func managedExtensionIDsFromLaunchArgs(args []string) map[string]bool {
	ids := map[string]bool{}
	for _, dir := range activeLoadExtensionDirs(args) {
		if id := extractExtensionID(dir); id != "" {
			ids[id] = true
		}
	}
	return ids
}

func closeAutomaticExtensionStartupPages(debugPort int, expectedExtensionIDs map[string]bool) int {
	targets := awaitAssignedExtensionStartupTargets(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		expectedExtensionIDs,
		extensionStartupLoadTimeout,
		extensionStartupCompletionWindow,
		extensionStartupProbeDelay,
	)
	if len(targets) == 0 {
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

func awaitAssignedExtensionStartupTargets(
	fetch func() ([]cdpTarget, error),
	expectedExtensionIDs map[string]bool,
	timeout time.Duration,
	completionWindow time.Duration,
	probeDelay time.Duration,
) []cdpTarget {
	if fetch == nil {
		return nil
	}
	if timeout <= 0 || len(expectedExtensionIDs) == 0 {
		targets, _ := fetch()
		return targets
	}
	if completionWindow < 0 {
		completionWindow = 0
	}
	if probeDelay <= 0 {
		probeDelay = 25 * time.Millisecond
	}

	deadline := time.Now().Add(timeout)
	var latest []cdpTarget

	for {
		if targets, err := fetch(); err == nil {
			latest = targets
			if allAssignedExtensionsObserved(targets, expectedExtensionIDs) {
				remaining := time.Until(deadline)
				if remaining > 0 {
					if remaining > completionWindow {
						remaining = completionWindow
					}
					time.Sleep(remaining)
				}
				if finalTargets, err := fetch(); err == nil {
					latest = finalTargets
				}
				return latest
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return latest
		}
		if remaining < probeDelay {
			time.Sleep(remaining)
		} else {
			time.Sleep(probeDelay)
		}
	}
}

func allAssignedExtensionsObserved(targets []cdpTarget, expectedExtensionIDs map[string]bool) bool {
	if len(expectedExtensionIDs) == 0 {
		return true
	}
	observed := map[string]bool{}
	for _, target := range targets {
		if id := extensionIDFromTargetURL(target.URL); id != "" {
			observed[id] = true
		}
	}
	for id := range expectedExtensionIDs {
		if !observed[strings.ToLower(strings.TrimSpace(id))] {
			return false
		}
	}
	return true
}

func extensionIDFromTargetURL(rawURL string) string {
	const prefix = "chrome-extension://"
	value := strings.ToLower(strings.TrimSpace(rawURL))
	if !strings.HasPrefix(value, prefix) {
		return ""
	}
	value = strings.TrimPrefix(value, prefix)
	if slash := strings.IndexByte(value, '/'); slash >= 0 {
		value = value[:slash]
	}
	if chromeWebStoreIDPattern.MatchString(value) && len(value) == 32 {
		return value
	}
	return ""
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
