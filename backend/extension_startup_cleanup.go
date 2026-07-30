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
	"sync/atomic"
	"time"
)

// sanitizeChromeStartupPreferences sets the profile foundation so Chromium
// itself opens a single about:blank tab on start. No post-start CDP rewrite of
// newtab → blank is required when these prefs stick.
//
// Chrome session.restore_on_startup:
//
//	4 = open the URLs in session.startup_urls
//	5 = open the New Tab Page (chrome://new-tab-page) — NOT what we want
//
// We pin restore_on_startup=4 and startup_urls=["about:blank"].
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
	// Foundation: single startup document is about:blank (not New Tab Page).
	// restore_on_startup=4 + startup_urls=["about:blank"] is the Chromium
	// setting for "open these URLs"; value 5 is New Tab Page and causes
	// chrome://new-tab-page which we previously tried to "fix" after launch.
	if !sessionRestoreIsAboutBlank(sessionPrefs) {
		sessionPrefs["restore_on_startup"] = float64(4)
		sessionPrefs["startup_urls"] = []any{"about:blank"}
		changed = true
	}
	// Homepage also points at blank so "home" does not reopen NTP.
	if browserPrefs["homepage"] != "about:blank" {
		browserPrefs["homepage"] = "about:blank"
		changed = true
	}
	if browserPrefs["homepage_is_newtabpage"] != false {
		browserPrefs["homepage_is_newtabpage"] = false
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

// sessionRestoreIsAboutBlank reports whether session prefs already pin a single
// about:blank startup URL (Chromium restore_on_startup=4).
func sessionRestoreIsAboutBlank(sessionPrefs map[string]any) bool {
	if sessionPrefs == nil {
		return false
	}
	switch v := sessionPrefs["restore_on_startup"].(type) {
	case float64:
		if int(v) != 4 {
			return false
		}
	case int:
		if v != 4 {
			return false
		}
	case json.Number:
		n, err := v.Int64()
		if err != nil || n != 4 {
			return false
		}
	default:
		return false
	}
	urls, ok := sessionPrefs["startup_urls"].([]any)
	if !ok || len(urls) != 1 {
		return false
	}
	s, _ := urls[0].(string)
	return strings.TrimSpace(strings.ToLower(s)) == "about:blank"
}

const (
	// Startup pass closes extension auto-pages only. Final handoff (user click)
	// collapses to a single about:blank; after that flag is set, never again.
	startupPageCloseTargetTimeout = 800 * time.Millisecond
)

// environmentTabsUserHandoffDone is set after the user-triggered final tab
// check. While true, no further tab management runs until new environments start.
var environmentTabsUserHandoffDone atomic.Bool

// armEnvironmentTabsUserHandoff re-enables the one final user-triggered tab
// check after new environments are started.
func armEnvironmentTabsUserHandoff() {
	environmentTabsUserHandoffDone.Store(false)
}

// finalizeBrowserStartupTabs is phase-1 of tab management for every environment
// start (including stop → open again). Foundation prefs pin about:blank; this
// closes extension auto-pages present at debug-ready. Phase-2 (sole about:blank
// + permanent user control) is FinalizeEnvironmentTabsForUserHandoff on user click.
func finalizeBrowserStartupTabs(debugPort int, profileId string) {
	if debugPort <= 0 {
		return
	}
	// Every start re-arms handoff so stop→restart runs the full cycle again.
	armEnvironmentTabsUserHandoff()
	if n := runStartupTabCleanupOnce(debugPort); n > 0 {
		logger.New("Browser").Info("启动时已关闭扩展自动页（等待用户点击完成最终接管）",
			logger.F("profile_id", profileId),
			logger.F("closed_extension_pages", n),
		)
	}
}

// FinalizeEnvironmentTabsForUserHandoff is the LAST tab-management action of the
// current start cycle: user clicks sync tool / main client / list. Collapse every
// running environment to exactly one about:blank, then stop all tab management
// until the next environment start or stop re-arms the cycle.
func (a *App) FinalizeEnvironmentTabsForUserHandoff() map[string]interface{} {
	result := map[string]interface{}{
		"skipped":    false,
		"profiles":   0,
		"closedTabs": 0,
	}
	if !environmentTabsUserHandoffDone.CompareAndSwap(false, true) {
		result["skipped"] = true
		result["reason"] = "already_handed_off"
		return result
	}
	if a == nil || a.browserMgr == nil {
		return result
	}
	type item struct {
		id   string
		port int
	}
	a.browserMgr.Mutex.Lock()
	items := make([]item, 0)
	for id, p := range a.browserMgr.Profiles {
		if p == nil || !p.Running || p.DebugPort <= 0 {
			continue
		}
		items = append(items, item{id: id, port: p.DebugPort})
	}
	a.browserMgr.Mutex.Unlock()

	closedTotal := 0
	for _, it := range items {
		closedTotal += collapseEnvironmentTabsToSoleAboutBlank(it.port)
	}
	result["profiles"] = len(items)
	result["closedTabs"] = closedTotal
	logger.New("Browser").Info("用户触发最终标签检查：仅保留 about:blank，此后完全由用户接管",
		logger.F("profiles", len(items)),
		logger.F("closed_tabs", closedTotal),
	)
	return result
}

// collapseEnvironmentTabsToSoleAboutBlank closes every page that is not
// about:blank, keeps one blank (navigating chrome://newtab → about:blank in
// place), or creates about:blank if none remain. One-shot, no watcher.
func collapseEnvironmentTabsToSoleAboutBlank(debugPort int) int {
	if debugPort <= 0 {
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

	closeTarget := func(id string) error {
		_, e := browserClient.call("Target.closeTarget", map[string]any{"targetId": id}, startupPageCloseTargetTimeout)
		return e
	}
	targets, err := listCDPTargets(debugPort)
	if err != nil {
		return 0
	}

	closed := 0
	var keepBlank *cdpTarget
	for i := range targets {
		t := &targets[i]
		if t.ID == "" || !strings.EqualFold(strings.TrimSpace(t.Type), "page") {
			continue
		}
		u := strings.ToLower(strings.TrimSpace(t.URL))
		isBlank := u == "about:blank" || u == "about:blank#" ||
			u == "chrome://newtab" || u == "chrome://newtab/" ||
			u == "chrome://new-tab-page" || u == "chrome://new-tab-page/" ||
			strings.HasPrefix(u, "chrome://new-tab-page/")
		if isBlank {
			if keepBlank == nil {
				keepBlank = t
			} else if closeTarget(t.ID) == nil {
				closed++
			}
			continue
		}
		if closeTarget(t.ID) == nil {
			closed++
		}
	}
	if keepBlank == nil {
		_, _ = browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
		return closed
	}
	// Foundation wants about:blank; rewrite NTP shell in place on this same tab.
	u := strings.ToLower(strings.TrimSpace(keepBlank.URL))
	if u != "about:blank" && u != "about:blank#" {
		if strings.TrimSpace(keepBlank.WebSocketDebuggerUrl) == "" {
			if all, e := listCDPTargets(debugPort); e == nil {
				for _, t := range all {
					if t.ID == keepBlank.ID && strings.TrimSpace(t.WebSocketDebuggerUrl) != "" {
						*keepBlank = t
						break
					}
				}
			}
		}
		if strings.TrimSpace(keepBlank.WebSocketDebuggerUrl) != "" {
			_, _ = cdpCallTarget(*keepBlank, "Page.enable", map[string]any{})
			_, _ = cdpCallTarget(*keepBlank, "Page.navigate", map[string]any{"url": "about:blank"})
		}
	}
	return closed
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
