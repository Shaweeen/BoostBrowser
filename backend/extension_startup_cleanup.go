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

type startupPageCloseKind uint8

const (
	startupPageCloseExtension startupPageCloseKind = iota + 1
	startupPageCloseExtraBlank
)

type startupPageCloseAction struct {
	targetID string
	kind     startupPageCloseKind
}

const (
	// Observation stays bounded so multi-window batch starts remain responsive,
	// while quiet-pass early exit releases machines that never open extension UI.
	startupPageCleanupObservationWindow = 3500 * time.Millisecond
	startupPageCleanupPollInterval      = 100 * time.Millisecond
	// Quiet passes apply whenever no automatic extension page is visible, not
	// only after the first close. Delayed MetaMask tabs still reset the counter.
	startupPageCleanupQuietPasses = 8
)

func finalizeBrowserStartupTabs(debugPort int, profileId string) {
	if debugPort <= 0 {
		return
	}
	// Run inside this browser process's startup transaction, before
	// BrowserStudio navigates an explicit URL and before the minimized window is
	// handed to the user. Wallet extensions can create onboarding pages after
	// the debug endpoint first becomes ready, especially during a large batch
	// launch, so observe only this short bounded startup window. Keep the core's
	// first natural blank and extension workers, and close only automatic
	// extension pages plus extra natural blanks. No DOM, form or page content is
	// read, and no cleanup owner survives this function.
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return
	}
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return
	}
	defer browserClient.close()
	maxPasses := int(startupPageCleanupObservationWindow/startupPageCleanupPollInterval) + 1
	closedExtensions, closedBlanks := closeUnwantedStartupPagesDuringLaunch(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		func(targetID string) error {
			_, closeErr := browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, 1500*time.Millisecond)
			return closeErr
		},
		time.Sleep,
		maxPasses,
		startupPageCleanupQuietPasses,
	)
	blankEnsured := ensureSingleNaturalBlankStartupPage(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		func() (string, error) {
			result, createErr := browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 2*time.Second)
			if createErr != nil {
				return "", createErr
			}
			if result == nil {
				return "", fmt.Errorf("Target.createTarget 返回空 result")
			}
			targetID, _ := result["targetId"].(string)
			return strings.TrimSpace(targetID), nil
		},
		func(targetID string) error {
			_, closeErr := browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, 1500*time.Millisecond)
			return closeErr
		},
	)
	if closedExtensions > 0 || closedBlanks > 0 || blankEnsured {
		logger.New("Browser").Info("启动页面已收敛为唯一空白页",
			logger.F("profile_id", profileId),
			logger.F("closed_extension_pages", closedExtensions),
			logger.F("closed_extra_blank_pages", closedBlanks),
			logger.F("blank_ensured", blankEnsured),
		)
	}
	// Browser windows are launched at their real onscreen position now.  Do not
	// run the legacy restore pass here: it walks the whole Chromium process tree
	// and calls ShowWindow/SetForegroundWindow for every titled top-level HWND.
	// Recent Chrome/Cloak builds create renderer, IME and extension-host windows
	// in child processes; surfacing one of those produces a large, undecorated
	// white window over the page.  The restore pass was only needed when startup
	// deliberately used an offscreen --window-position, which is no longer done.
}

func closeUnwantedStartupPagesDuringLaunch(
	fetch func() ([]cdpTarget, error),
	closeTarget func(string) error,
	pause func(time.Duration),
	maxPasses int,
	quietPassesRequired int,
) (int, int) {
	if fetch == nil || closeTarget == nil || maxPasses <= 0 {
		return 0, 0
	}
	if quietPassesRequired <= 0 {
		quietPassesRequired = 1
	}

	closedTargetIDs := make(map[string]struct{})
	closedExtensions := 0
	closedBlanks := 0
	quietPasses := 0

	for pass := 0; pass < maxPasses; pass++ {
		targets, err := fetch()
		closedExtensionThisPass := false
		liveExtensionPages := false
		if err == nil {
			for _, target := range targets {
				if shouldCloseAutomaticExtensionStartupTarget(target) {
					liveExtensionPages = true
					break
				}
			}
			for _, action := range planStartupPageCleanup(targets) {
				if _, alreadyClosed := closedTargetIDs[action.targetID]; alreadyClosed {
					continue
				}
				if closeTarget(action.targetID) != nil {
					continue
				}
				closedTargetIDs[action.targetID] = struct{}{}
				switch action.kind {
				case startupPageCloseExtension:
					closedExtensions++
					closedExtensionThisPass = true
				case startupPageCloseExtraBlank:
					closedBlanks++
				}
			}
		}

		// Exit once the page set is quiet: no live automatic extension page and
		// no close action this pass. This covers both "no extension UI ever" and
		// "extension UI already closed", while a late MetaMask tab resets quiet.
		if err != nil || closedExtensionThisPass || liveExtensionPages {
			quietPasses = 0
		} else {
			quietPasses++
			if quietPasses >= quietPassesRequired {
				break
			}
		}
		if pass+1 < maxPasses && pause != nil {
			pause(startupPageCleanupPollInterval)
		}
	}
	return closedExtensions, closedBlanks
}

func planStartupPageCleanup(targets []cdpTarget) []startupPageCloseAction {
	keeperBlankID := ""
	for _, target := range targets {
		if target.ID != "" && isNaturalBlankPageTarget(target) {
			keeperBlankID = target.ID
			break
		}
	}

	actions := make([]startupPageCloseAction, 0)
	for _, target := range targets {
		if target.ID == "" {
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
	return actions
}

// ensureSingleNaturalBlankStartupPage guarantees the user-facing startup set is
// exactly one natural blank page. Closing every auto-opened wallet tab can leave
// Chrome with zero pages; creating about:blank restores the expected shell.
// Returns true when a blank was created or an extra blank was closed.
func ensureSingleNaturalBlankStartupPage(
	fetch func() ([]cdpTarget, error),
	createBlank func() (string, error),
	closeTarget func(string) error,
) bool {
	if fetch == nil {
		return false
	}
	targets, err := fetch()
	if err != nil {
		return false
	}
	blankIDs := make([]string, 0, 2)
	for _, target := range targets {
		if target.ID == "" {
			continue
		}
		if isNaturalBlankPageTarget(target) {
			blankIDs = append(blankIDs, target.ID)
		}
	}
	changed := false
	if len(blankIDs) == 0 {
		if createBlank == nil {
			return false
		}
		id, createErr := createBlank()
		if createErr != nil || strings.TrimSpace(id) == "" {
			return false
		}
		return true
	}
	if closeTarget == nil {
		return false
	}
	for _, id := range blankIDs[1:] {
		if closeTarget(id) == nil {
			changed = true
		}
	}
	return changed
}

func isNaturalBlankPageTarget(target cdpTarget) bool {
	if !strings.EqualFold(strings.TrimSpace(target.Type), "page") {
		return false
	}
	url := strings.ToLower(strings.TrimSpace(target.URL))
	switch {
	case url == "" || url == "about:blank" || url == "about:blank#":
		return true
	case url == "chrome://newtab/" || url == "chrome://newtab":
		return true
	case url == "chrome://new-tab-page/" || url == "chrome://new-tab-page":
		return true
	case strings.HasPrefix(url, "chrome://new-tab-page/"):
		return true
	default:
		return false
	}
}

func shouldCloseAutomaticExtensionStartupTarget(target cdpTarget) bool {
	// This classifier is called only inside the pre-handoff startup snapshot.
	// At that point BrowserStudio has not navigated an application URL and the
	// minimized window is not available for user interaction, so every top-level
	// extension page in the snapshot belongs to extension startup. Opener
	// metadata is intentionally irrelevant and no content is inspected.
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
