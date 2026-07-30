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
	// Background-only recheck for wallet pages that open after debug-ready.
	// Must not run on the start critical path (see finalizeBrowserStartupTabs).
	startupPageCleanupObservationWindow = 1200 * time.Millisecond
	startupPageCleanupPollInterval      = 100 * time.Millisecond
	startupPageCleanupQuietPasses       = 2
	startupPageCloseTargetTimeout       = 800 * time.Millisecond
)

func finalizeBrowserStartupTabs(debugPort int, profileId string) {
	if debugPort <= 0 {
		return
	}
	// Fast path restored to the v1.7.48 model: one CDP snapshot, close automatic
	// extension startup pages + extra blanks, ensure a single natural blank, then
	// return so multi-open is not blocked for seconds. Delayed wallet onboarding
	// tabs are handled by a short non-blocking background recheck only.
	closedExtensions, closedBlanks, blankEnsured := runStartupTabCleanupOnce(debugPort)
	if closedExtensions > 0 || closedBlanks > 0 || blankEnsured {
		logger.New("Browser").Info("启动页面已收敛为唯一空白页",
			logger.F("profile_id", profileId),
			logger.F("closed_extension_pages", closedExtensions),
			logger.F("closed_extra_blank_pages", closedBlanks),
			logger.F("blank_ensured", blankEnsured),
		)
	}
	go func() {
		defer func() { _ = recover() }()
		// Wallet extensions may open onboarding after first debug-ready; recheck
		// briefly without holding the environment start transaction.
		ext, blanks := runStartupTabCleanupDelayed(debugPort)
		if ext > 0 || blanks > 0 {
			logger.New("Browser").Info("延迟扩展启动页已关闭",
				logger.F("profile_id", profileId),
				logger.F("closed_extension_pages", ext),
				logger.F("closed_extra_blank_pages", blanks),
			)
		}
	}()
}

func runStartupTabCleanupOnce(debugPort int) (closedExtensions, closedBlanks int, blankEnsured bool) {
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return 0, 0, false
	}
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return 0, 0, false
	}
	defer browserClient.close()

	closeTarget := func(targetID string) error {
		_, closeErr := browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, startupPageCloseTargetTimeout)
		return closeErr
	}
	closedExtensions, closedBlanks = closeUnwantedStartupPagesOnce(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		closeTarget,
	)
	blankEnsured = ensureSingleNaturalBlankStartupPage(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		func() (string, error) {
			result, createErr := browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
			if createErr != nil {
				return "", createErr
			}
			if result == nil {
				return "", fmt.Errorf("Target.createTarget 返回空 result")
			}
			targetID, _ := result["targetId"].(string)
			return strings.TrimSpace(targetID), nil
		},
		closeTarget,
	)
	return closedExtensions, closedBlanks, blankEnsured
}

func runStartupTabCleanupDelayed(debugPort int) (int, int) {
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return 0, 0
	}
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return 0, 0
	}
	defer browserClient.close()
	maxPasses := int(startupPageCleanupObservationWindow/startupPageCleanupPollInterval) + 1
	return closeUnwantedStartupPagesDuringLaunch(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		func(targetID string) error {
			_, closeErr := browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, startupPageCloseTargetTimeout)
			return closeErr
		},
		time.Sleep,
		maxPasses,
		startupPageCleanupQuietPasses,
	)
}

// closeUnwantedStartupPagesOnce is the fast v1.7.48-style single snapshot pass.
func closeUnwantedStartupPagesOnce(
	fetch func() ([]cdpTarget, error),
	closeTarget func(string) error,
) (int, int) {
	if fetch == nil || closeTarget == nil {
		return 0, 0
	}
	targets, err := fetch()
	if err != nil {
		return 0, 0
	}
	closedExtensions := 0
	closedBlanks := 0
	for _, action := range planStartupPageCleanup(targets) {
		if closeTarget(action.targetID) != nil {
			continue
		}
		switch action.kind {
		case startupPageCloseExtension:
			closedExtensions++
		case startupPageCloseExtraBlank:
			closedBlanks++
		}
	}
	return closedExtensions, closedBlanks
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
