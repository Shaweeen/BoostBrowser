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

// sanitizeChromeStartupPreferences sets the profile foundation so Chromium
// itself opens a single about:blank tab on start (fast path: prefs only, no CDP).
//
// Chrome session.restore_on_startup:
//
//	4 = open the URLs in session.startup_urls
//	5 = open the New Tab Page (chrome://new-tab-page) — NOT what we want
//
// Also discards restorable Session/Tabs files so last-run extension pages do
// not restore on reopen (wallet vaults / Cookies / LES are never touched).
//
// Tab policy after launch:
//   - Start path: NO CDP close sweeps (user speed + respect open actions).
//   - Sync start only: one-shot sole about:blank (see sync_tab_handoff.go).
//   - After that handoff: never manage tabs again until the next StartInputSync.
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
	// Drop previous open-tab snapshots so restored extension pages cannot stack
	// with a new --load-extension activation (all extensions, not only MetaMask).
	discardChromeRestorableTabSessions(userDataDir)
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


