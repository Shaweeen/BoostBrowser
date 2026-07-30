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
	// Discrete one-shot closes only (not a continuous poller / event watcher).
	// MetaMask and similar wallets often open onboarding AFTER first debug-ready,
	// so a single immediate close is not enough; a short fixed schedule is.
	startupPageCloseTargetTimeout = 800 * time.Millisecond
)

// Fixed delays after debug-ready. Each tick is an independent list+close, then
// CDP disconnects. No timer loop that stays attached for the session lifetime.
var startupExtensionAutoTabCloseSchedule = []time.Duration{
	0,
	350 * time.Millisecond,
	900 * time.Millisecond,
	1800 * time.Millisecond,
	3000 * time.Millisecond,
}

// finalizeBrowserStartupTabs manages automatic extension tabs at environment
// start. Root cause of MetaMask "onboarding/welcome" tabs is the extension's
// own service worker opening a page when it loads via --load-extension — not
// BrowserStudio re-scanning the extension list every launch.
//
// Policy:
//   - never create/close about:blank (browser owns the single default blank);
//   - only close automatic extension/product pages (onboarding, welcome, …);
//   - use a few fixed one-shot passes (not realtime scanning) so late opens
//     are still caught without long-lived listeners that could close user clicks.
func finalizeBrowserStartupTabs(debugPort int, profileId string) {
	if debugPort <= 0 {
		return
	}
	// Immediate pass on the start path (first opportunity).
	if n := runStartupTabCleanupOnce(debugPort); n > 0 {
		logger.New("Browser").Info("启动时已关闭扩展自动页",
			logger.F("profile_id", profileId),
			logger.F("closed_extension_pages", n),
			logger.F("pass", "immediate"),
		)
	}
	// Follow-up discrete passes for delayed wallet onboarding tabs.
	go scheduleStartupExtensionAutoTabCloses(debugPort, profileId)
}

func scheduleStartupExtensionAutoTabCloses(debugPort int, profileId string) {
	defer func() { _ = recover() }()
	// Skip the 0 delay entry — already done synchronously above.
	for i, delay := range startupExtensionAutoTabCloseSchedule {
		if delay <= 0 {
			continue
		}
		time.Sleep(delay - previousScheduleDelay(i))
		n := runStartupTabCleanupOnce(debugPort)
		if n > 0 {
			logger.New("Browser").Info("启动后续一拍关闭扩展自动页",
				logger.F("profile_id", profileId),
				logger.F("closed_extension_pages", n),
				logger.F("delay_ms", delay.Milliseconds()),
			)
		}
	}
}

func previousScheduleDelay(index int) time.Duration {
	if index <= 0 {
		return 0
	}
	return startupExtensionAutoTabCloseSchedule[index-1]
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
	// Blank/new-tab pages are never closed here. Only automatic extension/product pages.
	return strings.EqualFold(strings.TrimSpace(target.Type), "page") &&
		isExtensionStartupURL(target.URL)
}

// normalizeBrowserTabsToSingleBlank closes every top-level page that is not a
// natural blank, then ensures exactly one about:blank remains. Used as a
// one-shot user-triggered pass (sync tool open / first interaction), not a
// background watcher.
func normalizeBrowserTabsToSingleBlank(debugPort int) (closed int, ensuredBlank bool) {
	if debugPort <= 0 {
		return 0, false
	}
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return 0, false
	}
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return 0, false
	}
	defer browserClient.close()

	closeTarget := func(targetID string) error {
		_, closeErr := browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, startupPageCloseTargetTimeout)
		return closeErr
	}
	createBlank := func() (string, error) {
		result, createErr := browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
		if createErr != nil {
			return "", createErr
		}
		if result == nil {
			return "", fmt.Errorf("Target.createTarget 返回空 result")
		}
		id, _ := result["targetId"].(string)
		return strings.TrimSpace(id), nil
	}

	targets, err := listCDPTargets(debugPort)
	if err != nil {
		return 0, false
	}
	blankIDs := make([]string, 0, 2)
	for _, target := range targets {
		if target.ID == "" || !strings.EqualFold(strings.TrimSpace(target.Type), "page") {
			continue
		}
		if isNaturalBlankPageTarget(target) {
			blankIDs = append(blankIDs, target.ID)
			continue
		}
		// Close extension pages, product pages, and any other content tabs so
		// the environment returns to a single blank shell for sync/work.
		if closeTarget(target.ID) == nil {
			closed++
		}
	}
	// Keep one blank if present; drop extras.
	for i, id := range blankIDs {
		if i == 0 {
			continue
		}
		if closeTarget(id) == nil {
			closed++
		}
	}
	if len(blankIDs) == 0 {
		if id, createErr := createBlank(); createErr == nil && id != "" {
			ensuredBlank = true
		}
	}
	return closed, ensuredBlank
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

// NormalizeAllRunningEnvironmentTabsToBlank is a one-shot user action: after
// environments are open, when the user opens the sync tool or first interacts,
// collapse every running browser to a single about:blank tab. Not a watcher.
func (a *App) NormalizeAllRunningEnvironmentTabsToBlank() map[string]interface{} {
	result := map[string]interface{}{
		"profiles":     0,
		"closedTabs":   0,
		"ensuredBlank": 0,
	}
	if a == nil || a.browserMgr == nil {
		return result
	}
	type target struct {
		id   string
		port int
	}
	a.browserMgr.Mutex.Lock()
	targets := make([]target, 0)
	for id, p := range a.browserMgr.Profiles {
		if p == nil || !p.Running || p.DebugPort <= 0 {
			continue
		}
		targets = append(targets, target{id: id, port: p.DebugPort})
	}
	a.browserMgr.Mutex.Unlock()

	closedTotal := 0
	ensured := 0
	for _, t := range targets {
		c, blank := normalizeBrowserTabsToSingleBlank(t.port)
		closedTotal += c
		if blank {
			ensured++
		}
		// newtab-style blanks that survived as "natural blank" should become
		// real about:blank when it is the sole remaining page.
		if c >= 0 {
			_ = ensureSoleBlankIsAboutBlank(t.port)
		}
	}
	result["profiles"] = len(targets)
	result["closedTabs"] = closedTotal
	result["ensuredBlank"] = ensured
	logger.New("Browser").Info("用户触发：所有运行环境标签已收敛为唯一空白页",
		logger.F("profiles", len(targets)),
		logger.F("closed_tabs", closedTotal),
		logger.F("ensured_blank", ensured),
	)
	return result
}

// ensureSoleBlankIsAboutBlank navigates the remaining sole blank/new-tab page
// to about:blank when needed (one CDP call, no loop).
func ensureSoleBlankIsAboutBlank(debugPort int) error {
	targets, err := listCDPTargets(debugPort)
	if err != nil {
		return err
	}
	var sole *cdpTarget
	pageCount := 0
	for i := range targets {
		t := &targets[i]
		if !strings.EqualFold(strings.TrimSpace(t.Type), "page") || t.ID == "" {
			continue
		}
		pageCount++
		if isNaturalBlankPageTarget(*t) {
			sole = t
		}
	}
	if pageCount != 1 || sole == nil {
		return nil
	}
	u := strings.ToLower(strings.TrimSpace(sole.URL))
	if u == "about:blank" || u == "about:blank#" {
		return nil
	}
	// Remaining page is chrome://newtab style — replace with about:blank.
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return err
	}
	// Prefer close+create over Page.navigate (simpler, works without attaching to page).
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return err
	}
	defer browserClient.close()
	_, _ = browserClient.call("Target.closeTarget", map[string]any{"targetId": sole.ID}, startupPageCloseTargetTimeout)
	_, err = browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
	return err
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
	// Never treat the browser's natural blank/new-tab as an extension page.
	if u == "about:blank" || u == "about:blank#" ||
		u == "chrome://newtab" || u == "chrome://newtab/" ||
		u == "chrome://new-tab-page" || u == "chrome://new-tab-page/" ||
		strings.HasPrefix(u, "chrome://new-tab-page/") {
		return false
	}
	// Any top-level chrome-extension page opened at startup is treated as
	// automatic (MetaMask onboarding/welcome, Rabby unlock, etc.). User toolbar
	// clicks after the short schedule ends are not closed.
	if strings.HasPrefix(u, "chrome-extension://") {
		return true
	}
	if strings.HasPrefix(u, "chrome://extensions") {
		return true
	}
	// Chromium first-run / product pages that compete with the blank shell.
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
