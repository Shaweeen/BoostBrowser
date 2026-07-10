package backend

import (
	"boost-browser/backend/internal/browser"
	appconfig "boost-browser/backend/internal/config"
	"boost-browser/backend/internal/launchcode"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const cloakMarkerFilename = "cloak.marker"

// StartInstance implements launchcode.BrowserStarter for the local LaunchServer.
func (a *App) StartInstance(profileId string) (*browser.Profile, error) {
	return a.BrowserInstanceStart(profileId)
}

// StartInstanceWithParams implements launchcode.BrowserStarterWithParams.
func (a *App) StartInstanceWithParams(profileId string, params launchcode.LaunchRequestParams) (*browser.Profile, error) {
	return a.BrowserInstanceStartWithParams(profileId, params.LaunchArgs, params.StartURLs, params.SkipDefaultStartURLs)
}

func launchArgKey(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ""
	}
	if i := strings.Index(arg, "="); i >= 0 {
		arg = arg[:i]
	}
	return strings.ToLower(strings.TrimSpace(arg))
}

func isCloakCore(core browser.Core, chromeBinaryPath string) bool {
	joined := strings.ToLower(strings.Join([]string{core.CoreId, core.CoreName, core.CorePath, chromeBinaryPath}, " "))
	return strings.Contains(joined, "cloak") || strings.Contains(joined, "cloakbrowser")
}

func buildEffectiveFingerprintArgs(profile *browser.Profile, selectedCore browser.Core, chromeBinaryPath string) []string {
	if profile == nil {
		return nil
	}
	args := append([]string{}, profile.FingerprintArgs...)
	if !isCloakCore(selectedCore, chromeBinaryPath) {
		return args
	}
	if !hasArgKey(args, "--fingerprint") {
		args = append(args, "--fingerprint="+stableFingerprintSeed(profile.ProfileId))
	}
	if !hasArgKey(args, "--fingerprint-platform") {
		args = append(args, "--fingerprint-platform=windows")
	}
	return args
}

func hasArgKey(args []string, key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, arg := range args {
		if launchArgKey(arg) == key {
			return true
		}
	}
	return false
}

func stableFingerprintSeed(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	sum := sha1.Sum([]byte(value))
	seed := binary.BigEndian.Uint32(sum[:4])
	if seed == 0 {
		seed = 1
	}
	return fmt.Sprintf("%d", seed)
}

func resolveCloakGeoArgs(proxyConfig string) []string {
	proxyConfig = strings.TrimSpace(proxyConfig)
	if proxyConfig == "" {
		return nil
	}
	return nil
}

func languageFromCountry(country string) string {
	switch strings.ToUpper(strings.TrimSpace(country)) {
	case "CN":
		return "zh-CN"
	case "TW":
		return "zh-TW"
	case "HK":
		return "zh-HK"
	case "JP":
		return "ja-JP"
	case "KR":
		return "ko-KR"
	case "GB", "UK":
		return "en-GB"
	case "AU":
		return "en-AU"
	case "CA":
		return "en-CA"
	case "DE":
		return "de-DE"
	case "FR":
		return "fr-FR"
	case "IT":
		return "it-IT"
	case "ES":
		return "es-ES"
	case "BR":
		return "pt-BR"
	case "RU":
		return "ru-RU"
	case "US":
		fallthrough
	default:
		return "en-US"
	}
}

func buildAcceptLanguageHeader(primary, raw string) string {
	primary = strings.TrimSpace(primary)
	if primary == "" {
		return "en-US,en;q=0.9"
	}
	seen := map[string]struct{}{}
	items := []string{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[strings.ToLower(v)]; ok {
			return
		}
		seen[strings.ToLower(v)] = struct{}{}
		items = append(items, v)
	}
	add(primary)
	if dash := strings.Index(primary, "-"); dash > 0 {
		add(primary[:dash])
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(strings.Split(part, ";")[0])
		add(part)
	}
	add("en-US")
	add("en")
	if len(items) == 0 {
		return "en-US,en;q=0.9"
	}
	out := []string{items[0]}
	q := 0.9
	for _, item := range items[1:] {
		out = append(out, fmt.Sprintf("%s;q=%.1f", item, q))
		if q > 0.2 {
			q -= 0.1
		}
	}
	return strings.Join(out, ",")
}

func injectStealthToAllPagesWithUA(debugPort int, includeUA bool) error {
	if debugPort <= 0 {
		return nil
	}
	client := http.Client{Timeout: 750 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", debugPort))
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func seedDefaultSearchEngineWithRetry(userDataDir string, attempts int, delay time.Duration) {
	if attempts <= 0 {
		attempts = 1
	}
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	for i := 0; i < attempts; i++ {
		if seedDefaultSearchEngine(userDataDir) {
			return
		}
		time.Sleep(delay)
	}
}

func seedDefaultSearchEngineViaCDPWithRetry(userDataDir string, debugPort int, attempts int, delay time.Duration) {
	seedDefaultSearchEngineWithRetry(userDataDir, attempts, delay)
}

func seedDefaultSearchEngine(userDataDir string) bool {
	defaultDir := filepath.Join(userDataDir, "Default")
	webDataPath := filepath.Join(defaultDir, "Web Data")
	prefsPath := filepath.Join(defaultDir, "Preferences")
	if _, err := os.Stat(webDataPath); err != nil {
		return false
	}
	if err := normalizeSearchPreferences(prefsPath); err != nil {
		return false
	}
	if err := normalizeSearchKeywords(webDataPath); err != nil {
		return false
	}
	return true
}

func normalizeSearchKeywords(webDataPath string) error {
	db, err := sql.Open("sqlite", webDataPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM keywords WHERE NOT (keyword LIKE '@%' OR short_name = 'Google' OR keyword = '9oo91e.qjz9zk')`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE keywords SET is_active = 0 WHERE keyword != '9oo91e.qjz9zk'`); err != nil {
		return err
	}
	var existingID int64
	err = db.QueryRow(`SELECT id FROM keywords WHERE short_name = 'Google' OR keyword = '9oo91e.qjz9zk' ORDER BY id LIMIT 1`).Scan(&existingID)
	if err == nil {
		_, err = db.Exec(`UPDATE keywords SET short_name = 'Google', keyword = '9oo91e.qjz9zk', url = 'https://www.google.com/search?q={searchTerms}', suggest_url = 'https://www.google.com/complete/search?client=chrome&q={searchTerms}', input_encodings = 'UTF-8', is_active = 1, sync_guid = CASE WHEN sync_guid IS NULL OR sync_guid = '' THEN 'boost-google' ELSE sync_guid END WHERE id = ?`, existingID)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = db.Exec(`INSERT INTO keywords (short_name, keyword, favicon_url, url, safe_for_autoreplace, originating_url, date_created, usage_count, input_encodings, suggest_url, prepopulate_id, created_by_policy, last_modified, sync_guid, alternate_urls, image_url, search_url_post_params, suggest_url_post_params, image_url_post_params, new_tab_url, last_visited, created_from_play_api, is_active, starter_pack_id, enforced_by_policy, featured_by_policy) VALUES ('Google', '9oo91e.qjz9zk', '', 'https://www.google.com/search?q={searchTerms}', 1, '', 0, 0, 'UTF-8', 'https://www.google.com/complete/search?client=chrome&q={searchTerms}', 1, 0, 0, 'boost-google', '', '', '', '', '', '', 0, 0, 1, 0, 0, 0)`)
	return err
}

func normalizeSearchPreferences(prefsPath string) error {
	if err := os.MkdirAll(filepath.Dir(prefsPath), 0o755); err != nil {
		return err
	}
	root := map[string]any{}
	if data, err := os.ReadFile(prefsPath); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &root)
	}
	provider := map[string]any{
		"name":        "Google",
		"keyword":     "9oo91e.qjz9zk",
		"search_url":  "https://www.google.com/search?q={searchTerms}",
		"suggest_url": "https://www.google.com/complete/search?client=chrome&q={searchTerms}",
		"encodings":   "UTF-8",
	}
	root["default_search_provider"] = provider
	root["default_search_provider_data"] = map[string]any{
		"template_url_data":          provider,
		"mirrored_template_url_data": provider,
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(prefsPath, out, 0o644)
}

func (a *App) isProfileUsingCloakCore(profileId string) bool {
	if a == nil || a.browserMgr == nil {
		return false
	}
	a.browserMgr.Mutex.Lock()
	profile := a.browserMgr.Profiles[profileId]
	a.browserMgr.Mutex.Unlock()
	if profile == nil {
		return false
	}
	chromeBinaryPath, err := a.browserMgr.ResolveChromeBinary(profile)
	if err != nil {
		return false
	}
	core := browser.Core{}
	found := false
	if strings.TrimSpace(profile.CoreId) != "" {
		core, found = a.browserMgr.GetCore(profile.CoreId)
	}
	if !found {
		core, found = a.browserMgr.GetDefaultCore()
	}
	return found && isCloakCore(core, chromeBinaryPath)
}

func mergeUniqueLaunchArgs(primary []string, secondary []string) []string {
	result := make([]string, 0, len(primary)+len(secondary))
	seenKeys := map[string]int{}
	appendOrReplace := func(arg string) {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			return
		}
		key := launchArgKey(arg)
		if key != "" {
			if idx, ok := seenKeys[key]; ok {
				result[idx] = arg
				return
			}
			seenKeys[key] = len(result)
		}
		result = append(result, arg)
	}
	for _, arg := range primary {
		appendOrReplace(arg)
	}
	for _, arg := range secondary {
		appendOrReplace(arg)
	}
	return result
}

type localLicenseState struct {
	MaxProfileLimit int      `json:"maxProfileLimit"`
	UsedCDKeys      []string `json:"usedCDKeys"`
}

func localLicenseStatePath(configPath string) string {
	return configPath + ".local-license.json"
}

func saveLocalLicenseState(configPath string, state *localLicenseState) error {
	if state == nil {
		state = &localLicenseState{}
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(localLicenseStatePath(configPath), out, 0o600)
}

func loadLocalLicenseState(configPath string) (*localLicenseState, error) {
	data, err := os.ReadFile(localLicenseStatePath(configPath))
	if err != nil {
		return nil, err
	}
	var state localLicenseState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func reconcileConfigWithLocalLicense(path string, cfg *appconfig.Config) (bool, string, error) {
	if cfg == nil {
		return false, "", nil
	}
	state, err := loadLocalLicenseState(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", err
	}
	changed := false
	if state.MaxProfileLimit > 0 && cfg.App.MaxProfileLimit != state.MaxProfileLimit {
		cfg.App.MaxProfileLimit = state.MaxProfileLimit
		changed = true
	}
	if len(state.UsedCDKeys) > 0 {
		cfg.App.UsedCDKeys = append([]string{}, state.UsedCDKeys...)
		changed = true
	}
	return changed, localLicenseStatePath(path), nil
}

func getBrowserWebSocketURL(debugPort int) (string, error) {
	if debugPort <= 0 {
		return "", fmt.Errorf("invalid debug port: %d", debugPort)
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", debugPort))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", err
	}
	if strings.TrimSpace(info.WebSocketDebuggerURL) == "" {
		return "", fmt.Errorf("DevTools /json/version missing webSocketDebuggerUrl")
	}
	return info.WebSocketDebuggerURL, nil
}

func getUserAgentOverride(debugPort int) (string, map[string]any, error) {
	if debugPort <= 0 {
		return "", nil, fmt.Errorf("invalid debug port: %d", debugPort)
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", debugPort))
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	var info struct {
		UserAgent string `json:"User-Agent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", nil, err
	}
	ua := strings.TrimSpace(info.UserAgent)
	if ua == "" {
		ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.7680.177 Safari/537.36"
	}
	ua = strings.ReplaceAll(ua, "Chromium/", "Chrome/")
	metadata := map[string]any{
		"brands": []map[string]string{
			{"brand": "Google Chrome", "version": "146"},
			{"brand": "Chromium", "version": "146"},
			{"brand": "Not A(Brand", "version": "24"},
		},
		"fullVersionList": []map[string]string{
			{"brand": "Google Chrome", "version": "146.0.7680.177"},
			{"brand": "Chromium", "version": "146.0.7680.177"},
			{"brand": "Not A(Brand", "version": "24.0.0.0"},
		},
		"platform":        "Windows",
		"platformVersion": "10.0.0",
		"architecture":    "x86",
		"model":           "",
		"mobile":          false,
	}
	return ua, metadata, nil
}

const stealthJS = `Object.defineProperty(navigator, 'webdriver', {get: () => undefined});`

func proxyURLHost(proxyConfig string) string {
	u, err := url.Parse(proxyConfig)
	if err == nil && u.Host != "" {
		return u.Host
	}
	return proxyConfig
}
