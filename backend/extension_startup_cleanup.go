package backend

import (
	"boost-browser/backend/internal/fsutil"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// sanitizeChromeStartupPreferences pins Profile-level session prefs (AdsPower /
// MoreLogin style foundation). Fast path: prefs only, no CDP tab sweeps.
//
// Chrome session.restore_on_startup:
//
//	4 = open the URLs in session.startup_urls
//	5 = open the New Tab Page (chrome://new-tab-page) — NOT what we want
//
// Tab policy (aligned with commercial multi-account browsers):
//   - Extensions live in the Profile (Scheme A); daily start must not re-CLI
//     inject and must not wipe user work sessions after first adapt.
//   - Session tab files are discarded ONLY on first-adapt starts (caller passes
//     wipeRestorableSessions=true). Hot restarts keep user tabs.
//   - Sync never closes tabs — input sync only.
func sanitizeChromeStartupPreferences(userDataDir string) {
	sanitizeChromeStartupPreferencesOpts(userDataDir, false)
}

// sanitizeChromeStartupPreferencesOpts allows a one-time restorable-session wipe
// for first extension adapt (prevents double extension tabs with --load-extension).
func sanitizeChromeStartupPreferencesOpts(userDataDir string, wipeRestorableSessions bool) {
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
	if wipeRestorableSessions {
		// First-adapt only: drop Session/Tabs so CLI load does not stack with
		// restored chrome-extension unlock pages. Never touch LES/wallets.
		// One-shot: once wiped, never wipe again — a profile that never completes
		// extension adapt must not lose its user session on every start.
		if !sessionWipeDone(userDataDir) {
			discardChromeRestorableTabSessions(userDataDir)
			markSessionWipeDone(userDataDir)
		}
	}
}

// One-time restorable-session wipe marker. Written after the first-adapt wipe
// so repeated starts never discard user work tabs again, even when the
// extension adapt never fully completes.
const sessionWipeDoneMarkerName = ".boost_session_wipe_done"

func sessionWipeDone(userDataDir string) bool {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return true
	}
	_, err := os.Stat(filepath.Join(userDataDir, sessionWipeDoneMarkerName))
	return err == nil
}

func markSessionWipeDone(userDataDir string) {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return
	}
	_ = os.MkdirAll(userDataDir, 0755)
	_ = os.WriteFile(filepath.Join(userDataDir, sessionWipeDoneMarkerName), []byte("1\n"), 0600)
}

// discardChromeRestorableTabSessions removes Chromium session tab files that
// re-open whatever tabs the user left open last time (including
// chrome-extension:// unlock / notification full pages).
//
// Safe scope: only Session / Tabs / Sessions* under profile roots.
// Does NOT delete: Local Extension Settings, Cookies, IndexedDB, History,
// Preferences extension settings, or the shared extension package dirs.
func discardChromeRestorableTabSessions(userDataDir string) {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return
	}
	// Profile roots that may hold tab snapshots.
	roots := []string{
		userDataDir,
		filepath.Join(userDataDir, "Default"),
	}
	if entries, err := os.ReadDir(userDataDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if name == "Default" || strings.HasPrefix(name, "Profile ") || name == "Guest Profile" {
				roots = append(roots, filepath.Join(userDataDir, name))
			}
		}
	}
	// File names Chrome uses for restorable browsing sessions (versioned SNSS).
	fileNames := []string{
		"Current Session",
		"Last Session",
		"Current Tabs",
		"Last Tabs",
	}
	seen := map[string]struct{}{}
	for _, root := range roots {
		root = filepath.Clean(root)
		if _, dup := seen[root]; dup {
			continue
		}
		seen[root] = struct{}{}
		for _, name := range fileNames {
			_ = os.Remove(filepath.Join(root, name))
		}
		// Newer Chromium also keeps numbered files under Sessions/.
		sessionsDir := filepath.Join(root, "Sessions")
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			// Keep directory structure; only drop session payloads.
			_ = os.Remove(filepath.Join(sessionsDir, entry.Name()))
		}
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

	// Website login durability (Gmail / X / OAuth such as Privy):
	//   - allow third-party cookies so authorize redirects keep state;
	//   - default content setting for cookies = Allow (1);
	//   - never arm "clear cookies on exit" for multi-account environments.
	// Chromium cookie_controls_mode: 0 = allow all third-party cookies.
	if !jsonNumberEquals(profilePrefs["cookie_controls_mode"], 0) {
		profilePrefs["cookie_controls_mode"] = float64(0)
		changed = true
	}
	if profilePrefs["block_third_party_cookies"] != false {
		profilePrefs["block_third_party_cookies"] = false
		changed = true
	}
	defaultContent := ensureJSONMap(profilePrefs, "default_content_setting_values")
	if !jsonNumberEquals(defaultContent["cookies"], 1) {
		defaultContent["cookies"] = float64(1)
		changed = true
	}
	// Some Chromium builds store clear-on-exit under privacy.clear_on_exit.*;
	// force cookies off so Gmail/X sessions survive close + upgrade restart.
	privacyPrefs := ensureJSONMap(prefs, "privacy")
	clearOnExit := ensureJSONMap(privacyPrefs, "clear_on_exit")
	for _, key := range []string{"cookies", "hosted_app_data", "site_settings"} {
		if clearOnExit[key] != false {
			clearOnExit[key] = false
			changed = true
		}
	}

	// Keep every isolated environment local-only by default. Windows enterprise
	// policy is authoritative for Chrome, while these preferences cover Chromium
	// variants that do not implement every Google policy hook.
	// Note: signin.allowed=false only blocks Chrome browser-account sync, not
	// website logins (mail.google.com / x.com cookies still persist).
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

	// Developer mode for unpacked --load-extension packages. Does not open tabs
	// by itself; avoids Chrome re-prompting "disable developer mode extensions".
	extRoot := ensureJSONMap(prefs, "extensions")
	extUI := ensureJSONMap(extRoot, "ui")
	if extUI["developer_mode"] != true {
		extUI["developer_mode"] = true
		changed = true
	}
	// Do not invent extensions.settings[*] rows here — that is Chrome's job after
	// first load. Inventing rows can fight real wallet vault state.

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

// jsonNumberEquals compares JSON numbers that may arrive as float64, int, or
// json.Number after round-trips.
func jsonNumberEquals(value any, want int) bool {
	switch v := value.(type) {
	case float64:
		return int(v) == want
	case int:
		return v == want
	case int64:
		return int(v) == want
	case json.Number:
		n, err := v.Int64()
		return err == nil && int(n) == want
	default:
		return false
	}
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

// listCDPTargets is shared by wallet import and input sync (not tab cleanup).
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
