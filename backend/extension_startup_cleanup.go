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
	"sync"
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

// pendingTabHandoffProfiles holds environments that still need the one-shot
// user-handoff collapse (sole about:blank). Keyed by profile ID so starting a
// *new* environment never re-arms collapse for already-working environments.
var (
	pendingTabHandoffMu       sync.Mutex
	pendingTabHandoffProfiles = map[string]struct{}{}
)

// armEnvironmentTabsUserHandoffForProfile marks only this profile for the next
// user-handoff click. Other running environments keep their open work tabs.
func armEnvironmentTabsUserHandoffForProfile(profileId string) {
	profileId = strings.TrimSpace(profileId)
	if profileId == "" {
		return
	}
	pendingTabHandoffMu.Lock()
	pendingTabHandoffProfiles[profileId] = struct{}{}
	pendingTabHandoffMu.Unlock()
}

// clearEnvironmentTabsUserHandoffForProfile drops handoff state when an
// environment stops (next start re-arms that profile only).
func clearEnvironmentTabsUserHandoffForProfile(profileId string) {
	profileId = strings.TrimSpace(profileId)
	if profileId == "" {
		return
	}
	pendingTabHandoffMu.Lock()
	delete(pendingTabHandoffProfiles, profileId)
	pendingTabHandoffMu.Unlock()
}

func takePendingTabHandoffProfiles() []string {
	pendingTabHandoffMu.Lock()
	defer pendingTabHandoffMu.Unlock()
	if len(pendingTabHandoffProfiles) == 0 {
		return nil
	}
	ids := make([]string, 0, len(pendingTabHandoffProfiles))
	for id := range pendingTabHandoffProfiles {
		ids = append(ids, id)
	}
	pendingTabHandoffProfiles = make(map[string]struct{})
	return ids
}

func pendingTabHandoffCount() int {
	pendingTabHandoffMu.Lock()
	defer pendingTabHandoffMu.Unlock()
	return len(pendingTabHandoffProfiles)
}

func profilePendingTabHandoff(profileId string) bool {
	profileId = strings.TrimSpace(profileId)
	if profileId == "" {
		return false
	}
	pendingTabHandoffMu.Lock()
	defer pendingTabHandoffMu.Unlock()
	_, ok := pendingTabHandoffProfiles[profileId]
	return ok
}

// finalizeBrowserStartupTabs is phase-1 of tab management for every environment
// start (including stop → open again). Runs ONLY on this profile's debug port:
//  1) close extension auto-pages (never touch service workers / storage)
//  2) collapse to a single about:blank so the user takes over a clean shell
// Already-running environments are never re-armed here (per-profile handoff).
// Phase-2 (user click / open sync) only re-collapses profiles still pending;
// work tabs opened after handoff are never managed again until that env restarts.
func finalizeBrowserStartupTabs(debugPort int, profileId string) {
	if debugPort <= 0 {
		return
	}
	// Only this newly started profile waits for user handoff. Do not re-arm a
	// global latch that would later collapse every already-open work session.
	armEnvironmentTabsUserHandoffForProfile(profileId)
	closedExt := runStartupTabCleanupOnce(debugPort)
	// Leave exactly one blank page ready for takeover. Extension SW / storage /
	// dapp providers stay alive (we only close page targets, not backgrounds).
	closedTabs := collapseEnvironmentTabsToSoleAboutBlank(debugPort)
	if closedExt > 0 || closedTabs > 0 {
		logger.New("Browser").Info("启动标签检查：扩展自动页已清理并保留唯一空白页（等待用户接管）",
			logger.F("profile_id", profileId),
			logger.F("closed_extension_pages", closedExt),
			logger.F("closed_extra_tabs", closedTabs),
		)
	}
}

// FinalizeEnvironmentTabsForUserHandoff collapses ONLY environments that were
// started since the last handoff and are still pending. Already-open working
// environments are never touched when the user starts additional ones.
func (a *App) FinalizeEnvironmentTabsForUserHandoff() map[string]interface{} {
	result := map[string]interface{}{
		"skipped":    false,
		"profiles":   0,
		"closedTabs": 0,
	}
	// Sync assistant is a separate process; main already owns handoff. Panel
	// must not race Target.closeTarget on shared environments.
	if a != nil && a.panelMode {
		result["skipped"] = true
		result["reason"] = "panel_never_owns_tab_handoff"
		return result
	}
	pendingIDs := takePendingTabHandoffProfiles()
	if len(pendingIDs) == 0 {
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
	want := make(map[string]struct{}, len(pendingIDs))
	for _, id := range pendingIDs {
		want[id] = struct{}{}
	}
	a.browserMgr.Mutex.Lock()
	items := make([]item, 0, len(pendingIDs))
	for id := range want {
		p := a.browserMgr.Profiles[id]
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
	logger.New("Browser").Info("用户触发最终标签检查：仅收拢待接管的新启动环境，不触碰已办公环境",
		logger.F("profiles", len(items)),
		logger.F("pending_requested", len(pendingIDs)),
		logger.F("closed_tabs", closedTotal),
	)
	return result
}

func isBlankOrNewTabURL(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	return u == "about:blank" || u == "about:blank#" ||
		u == "chrome://newtab" || u == "chrome://newtab/" ||
		u == "chrome://new-tab-page" || u == "chrome://new-tab-page/" ||
		strings.HasPrefix(u, "chrome://new-tab-page/")
}

func isExactAboutBlankURL(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	return u == "about:blank" || u == "about:blank#"
}

// collapseTabPlan describes a last-tab-safe collapse: always keep one page
// alive, navigate it to about:blank before closing anything else. Closing the
// final Chromium page destroys the whole browser window — that is the multi-
// environment "从属突然关闭" failure mode when handoff races or closes first.
type collapseTabPlan struct {
	keepID             string
	navigateKeepBlank  bool
	closeIDs           []string
	createBlankIfEmpty bool
}

func planCollapseToSoleAboutBlank(targets []cdpTarget) collapseTabPlan {
	pages := make([]cdpTarget, 0, len(targets))
	for _, t := range targets {
		if t.ID == "" || !strings.EqualFold(strings.TrimSpace(t.Type), "page") {
			continue
		}
		pages = append(pages, t)
	}
	if len(pages) == 0 {
		return collapseTabPlan{createBlankIfEmpty: true}
	}

	var keep cdpTarget
	var closeIDs []string
	foundBlankLike := false
	for _, t := range pages {
		if isBlankOrNewTabURL(t.URL) {
			if !foundBlankLike {
				keep = t
				foundBlankLike = true
				continue
			}
			closeIDs = append(closeIDs, t.ID)
			continue
		}
		closeIDs = append(closeIDs, t.ID)
	}
	if !foundBlankLike {
		// No blank tab yet: promote the first page and navigate in place.
		// Never close it — that would be the last tab and kill the window.
		keep = pages[0]
		closeIDs = closeIDs[:0]
		for i := 1; i < len(pages); i++ {
			closeIDs = append(closeIDs, pages[i].ID)
		}
		return collapseTabPlan{
			keepID:            keep.ID,
			navigateKeepBlank: true,
			closeIDs:          closeIDs,
		}
	}
	return collapseTabPlan{
		keepID:            keep.ID,
		navigateKeepBlank: !isExactAboutBlankURL(keep.URL),
		closeIDs:          closeIDs,
	}
}

// collapseEnvironmentTabsToSoleAboutBlank closes every page that is not
// about:blank, keeps one blank (navigating chrome://newtab → about:blank in
// place), or creates about:blank if none remain. One-shot, no watcher.
//
// Safety: never Target.closeTarget the last remaining page before a blank
// survivor exists. Chromium exits the whole environment window when the last
// tab closes — that looked like "从属强制退出" under multi-open + sync handoff.
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
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("empty target id")
		}
		_, e := browserClient.call("Target.closeTarget", map[string]any{"targetId": id}, startupPageCloseTargetTimeout)
		return e
	}
	targets, err := listCDPTargets(debugPort)
	if err != nil {
		return 0
	}
	plan := planCollapseToSoleAboutBlank(targets)
	if plan.createBlankIfEmpty {
		_, _ = browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
		return 0
	}

	// 1) Ensure the survivor is about:blank BEFORE closing siblings.
	if plan.navigateKeepBlank && plan.keepID != "" {
		var keepTarget cdpTarget
		for _, t := range targets {
			if t.ID == plan.keepID {
				keepTarget = t
				break
			}
		}
		if strings.TrimSpace(keepTarget.WebSocketDebuggerUrl) == "" {
			if all, e := listCDPTargets(debugPort); e == nil {
				for _, t := range all {
					if t.ID == plan.keepID && strings.TrimSpace(t.WebSocketDebuggerUrl) != "" {
						keepTarget = t
						break
					}
				}
			}
		}
		if strings.TrimSpace(keepTarget.WebSocketDebuggerUrl) != "" {
			_, _ = cdpCallTarget(keepTarget, "Page.enable", map[string]any{})
			_, _ = cdpCallTarget(keepTarget, "Page.navigate", map[string]any{"url": "about:blank"})
		} else {
			// No page WS: open a blank first so closing siblings cannot kill the window.
			_, _ = browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
		}
	}

	// 2) Close only non-survivor pages. Never close keepID.
	closed := 0
	for _, id := range plan.closeIDs {
		if id == plan.keepID {
			continue
		}
		if closeTarget(id) == nil {
			closed++
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

	ensureBlank := func() {
		_, _ = browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
	}
	return closeAutomaticExtensionStartupPagesOnce(
		func() ([]cdpTarget, error) { return listCDPTargets(debugPort) },
		func(targetID string) error {
			_, closeErr := browserClient.call("Target.closeTarget", map[string]any{"targetId": targetID}, startupPageCloseTargetTimeout)
			return closeErr
		},
		ensureBlank,
	)
}

// closeAutomaticExtensionStartupPagesOnce closes only automatic extension
// startup pages. It does not open blanks, close blanks, or manage new-tab pages.
// If closing those pages would remove the last page, ensureBlank is called first
// so Chromium does not destroy the environment window.
func closeAutomaticExtensionStartupPagesOnce(
	fetch func() ([]cdpTarget, error),
	closeTarget func(string) error,
	ensureBlank func(),
) int {
	if fetch == nil || closeTarget == nil {
		return 0
	}
	targets, err := fetch()
	if err != nil {
		return 0
	}
	pageCount := 0
	closeIDs := make([]string, 0)
	hasSurvivor := false
	for _, target := range targets {
		if target.ID == "" || !strings.EqualFold(strings.TrimSpace(target.Type), "page") {
			continue
		}
		pageCount++
		if shouldCloseAutomaticExtensionStartupTarget(target) {
			closeIDs = append(closeIDs, target.ID)
			continue
		}
		hasSurvivor = true
	}
	if len(closeIDs) == 0 {
		return 0
	}
	// Closing every page (only extension auto-pages present) would exit Chrome.
	if !hasSurvivor && ensureBlank != nil {
		ensureBlank()
		// Re-check after blank is created; still proceed to close extensions.
	} else if !hasSurvivor && ensureBlank == nil {
		// Without a blank factory, leave at least one page alive.
		closeIDs = closeIDs[:len(closeIDs)-1]
		if len(closeIDs) == 0 {
			return 0
		}
	}
	closed := 0
	for _, id := range closeIDs {
		if closeTarget(id) == nil {
			closed++
		}
	}
	_ = pageCount
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
