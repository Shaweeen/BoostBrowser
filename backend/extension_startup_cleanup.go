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
	extensionStartupCleanupWindow = 2 * time.Second
	extensionStartupProbeDelay    = 50 * time.Millisecond
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

type startupPageCloseKind uint8

const (
	startupPageCloseExtension startupPageCloseKind = iota + 1
	startupPageCloseExtraBlank
)

type startupPageCloseAction struct {
	targetID string
	kind     startupPageCloseKind
}

func finalizeBrowserStartupTabs(debugPort int, pid int, profileId string) {
	if debugPort <= 0 {
		return
	}
	// BrowserStudio never creates a default tab. During this bounded startup
	// window it preserves the browser core's first natural blank page and closes
	// extension-created top-level pages plus extra blank pages as soon as they
	// appear. Extension workers/background pages remain untouched. The CDP
	// connection is released before this function returns; no worker, listener,
	// timer or target registry survives startup.
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return
	}
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return
	}
	defer browserClient.close()
	closedExtensions, closedBlanks := closeUnwantedStartupPages(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		func(targetID string) error {
			_, closeErr := browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, 1500*time.Millisecond)
			return closeErr
		},
		extensionStartupCleanupWindow,
		extensionStartupProbeDelay,
	)
	if closedExtensions > 0 || closedBlanks > 0 {
		logger.New("Browser").Info("启动页面已收敛为唯一空白页",
			logger.F("profile_id", profileId),
			logger.F("closed_extension_pages", closedExtensions),
			logger.F("closed_extra_blank_pages", closedBlanks),
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

func closeUnwantedStartupPages(
	fetch func() ([]cdpTarget, error),
	closeTarget func(string) error,
	timeout time.Duration,
	probeDelay time.Duration,
) (int, int) {
	if fetch == nil || closeTarget == nil || timeout <= 0 {
		return 0, 0
	}
	if probeDelay <= 0 {
		probeDelay = 25 * time.Millisecond
	}

	deadline := time.Now().Add(timeout)
	keeperBlankID := ""
	closedTargetIDs := map[string]bool{}
	closedExtensions := 0
	closedBlanks := 0
	for {
		if targets, err := fetch(); err == nil {
			var actions []startupPageCloseAction
			keeperBlankID, actions = planStartupPageCleanup(targets, keeperBlankID, closedTargetIDs)
			for _, action := range actions {
				if closeTarget(action.targetID) != nil {
					continue
				}
				closedTargetIDs[action.targetID] = true
				switch action.kind {
				case startupPageCloseExtension:
					closedExtensions++
				case startupPageCloseExtraBlank:
					closedBlanks++
				}
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return closedExtensions, closedBlanks
		}
		if remaining < probeDelay {
			time.Sleep(remaining)
		} else {
			time.Sleep(probeDelay)
		}
	}
}

func planStartupPageCleanup(targets []cdpTarget, keeperBlankID string, alreadyClosed map[string]bool) (string, []startupPageCloseAction) {
	blankStillPresent := false
	for _, target := range targets {
		if target.ID == keeperBlankID && isNaturalBlankPageTarget(target) && !alreadyClosed[target.ID] {
			blankStillPresent = true
			break
		}
	}
	if !blankStillPresent {
		keeperBlankID = ""
	}
	if keeperBlankID == "" {
		for _, target := range targets {
			if target.ID != "" && !alreadyClosed[target.ID] && isNaturalBlankPageTarget(target) {
				keeperBlankID = target.ID
				break
			}
		}
	}

	actions := make([]startupPageCloseAction, 0)
	for _, target := range targets {
		if target.ID == "" || alreadyClosed[target.ID] {
			continue
		}
		if shouldCloseAutomaticExtensionStartupTarget(target) {
			actions = append(actions, startupPageCloseAction{targetID: target.ID, kind: startupPageCloseExtension})
			continue
		}
		if isNaturalBlankPageTarget(target) && target.ID != keeperBlankID {
			actions = append(actions, startupPageCloseAction{targetID: target.ID, kind: startupPageCloseExtraBlank})
		}
	}
	return keeperBlankID, actions
}

func isNaturalBlankPageTarget(target cdpTarget) bool {
	if !strings.EqualFold(strings.TrimSpace(target.Type), "page") {
		return false
	}
	url := strings.ToLower(strings.TrimSpace(target.URL))
	return url == "" || url == "about:blank" || url == "about:blank#" ||
		url == "chrome://newtab/" || url == "chrome://newtab"
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
