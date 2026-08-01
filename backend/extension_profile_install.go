package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"boost-browser/backend/internal/fsutil"
	"boost-browser/backend/internal/logger"
)

// Scheme A (AdsPower / MoreLogin style) — reliable Chromium form:
//
// Do NOT invent incomplete INTERNAL (location=1) rows under Default/Extensions
// with empty permissions: Chromium ignores or disables them → "no extensions",
// and rewriting Preferences every start can stall multi-open.
//
// Reliable unpacked persistence (same as chrome://extensions "Load unpacked"):
//   - extensions.ui.developer_mode = true
//   - extensions.settings[id] with location = 4 (UNPACKED/LOAD)
//   - path = absolute path to the shared program package (no per-profile copy)
//   - active_permissions / granted_permissions filled from manifest
//   - state = 1 (ENABLED)
// Then strip --load-extension when registration is already valid.
//
// Policy:
//   - never touch Local Extension Settings / Cookies / IndexedDB (wallets);
//   - never wipe an existing Preferences.settings[id] vault-bearing entry;
//   - no multi-MB package copy on the start critical path;
//   - CLI --load-extension only as fallback when prefs registration fails.

// Chromium Extension::Location
const (
	chromeExtLocationInternal = 1
	chromeExtLocationUnpacked = 4 // LOAD / Load unpacked
)

// installUnpackedExtensionIntoProfile registers packageDir as a persistent
// unpacked extension in Preferences (no file copy).
func installUnpackedExtensionIntoProfile(userDataDir, packageDir string) error {
	userDataDir = strings.TrimSpace(userDataDir)
	packageDir = strings.TrimSpace(packageDir)
	if userDataDir == "" || packageDir == "" {
		return fmt.Errorf("profile install: empty path")
	}
	absPkg, err := filepath.Abs(packageDir)
	if err != nil {
		return fmt.Errorf("profile install: abs path: %w", err)
	}
	if err := validateUnpackedExtensionManifest(absPkg); err != nil {
		return fmt.Errorf("profile install: invalid package: %w", err)
	}
	extID := resolveExtensionPackageID(absPkg)
	if extID == "" {
		return fmt.Errorf("profile install: cannot resolve extension id for %s", absPkg)
	}
	version := readManifestVersionFromDir(absPkg)
	if version == "" {
		version = "0.0.0.0"
	}
	return ensurePreferencesUnpackedExtension(userDataDir, extID, absPkg, version)
}

// isExtensionInstalledInProfile reports a Preferences registration that Chromium
// can load without --load-extension (unpacked path still present + enabled).
func isExtensionInstalledInProfile(userDataDir, packageDir string) bool {
	userDataDir = strings.TrimSpace(userDataDir)
	packageDir = strings.TrimSpace(packageDir)
	if userDataDir == "" || packageDir == "" {
		return false
	}
	absPkg, err := filepath.Abs(packageDir)
	if err != nil {
		return false
	}
	if validateUnpackedExtensionManifest(absPkg) != nil {
		return false
	}
	extID := resolveExtensionPackageID(absPkg)
	if extID == "" {
		return false
	}
	entry, ok := readPreferencesExtensionEntry(userDataDir, extID)
	if !ok || entry == nil {
		// Path match under settings (folder name ≠ chrome id).
		entry, ok = findPreferencesExtensionByPackagePath(userDataDir, absPkg)
		if !ok || entry == nil {
			return false
		}
	}
	state, _ := entry["state"].(float64)
	if state != 1 {
		return false
	}
	path, _ := entry["path"].(string)
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	// Accept either shared package path or a legacy profile Extensions copy.
	if normalizeExtensionPath(path) == normalizeExtensionPath(absPkg) {
		return validateUnpackedExtensionManifest(path) == nil
	}
	if strings.Contains(strings.ToLower(path), string(os.PathSeparator)+"extensions"+string(os.PathSeparator)+extID) {
		return validateUnpackedExtensionManifest(path) == nil
	}
	// Path differs but package still valid at assigned dir — treat as not settled
	// so we heal Preferences path.
	return false
}

func readPreferencesExtensionEntry(userDataDir, extID string) (map[string]any, bool) {
	extID = strings.ToLower(strings.TrimSpace(extID))
	prefPath := filepath.Join(userDataDir, "Default", "Preferences")
	data, err := os.ReadFile(prefPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil, false
	}
	var prefs map[string]any
	if json.Unmarshal(data, &prefs) != nil {
		return nil, false
	}
	extRoot, _ := prefs["extensions"].(map[string]any)
	if extRoot == nil {
		return nil, false
	}
	settings, _ := extRoot["settings"].(map[string]any)
	if settings == nil {
		return nil, false
	}
	entry, _ := settings[extID].(map[string]any)
	if entry == nil {
		return nil, false
	}
	return entry, true
}

func findPreferencesExtensionByPackagePath(userDataDir, absPkg string) (map[string]any, bool) {
	prefPath := filepath.Join(userDataDir, "Default", "Preferences")
	data, err := os.ReadFile(prefPath)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	var prefs map[string]any
	if json.Unmarshal(data, &prefs) != nil {
		return nil, false
	}
	extRoot, _ := prefs["extensions"].(map[string]any)
	settings, _ := extRoot["settings"].(map[string]any)
	if settings == nil {
		return nil, false
	}
	target := normalizeExtensionPath(absPkg)
	for _, raw := range settings {
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		p, _ := entry["path"].(string)
		if normalizeExtensionPath(p) == target {
			return entry, true
		}
	}
	return nil, false
}

// ensurePreferencesUnpackedExtension writes/heals a LOAD(unpacked) settings row.
// Does not copy package files. Existing non-path fields (Chrome-written grants)
// are preserved when already present.
func ensurePreferencesUnpackedExtension(userDataDir, extID, absPackageDir, version string) error {
	extID = strings.ToLower(strings.TrimSpace(extID))
	absPackageDir = filepath.Clean(strings.TrimSpace(absPackageDir))
	if userDataDir == "" || extID == "" || absPackageDir == "" {
		return fmt.Errorf("profile install: preferences args incomplete")
	}
	prefPath := filepath.Join(userDataDir, "Default", "Preferences")
	if err := ensureChromePreferencesFile(prefPath); err != nil {
		return err
	}
	data, err := os.ReadFile(prefPath)
	if err != nil {
		return err
	}
	var prefs map[string]any
	if len(strings.TrimSpace(string(data))) == 0 {
		prefs = map[string]any{}
	} else if err := json.Unmarshal(data, &prefs); err != nil {
		return fmt.Errorf("profile install: parse Preferences: %w", err)
	}
	if prefs == nil {
		prefs = map[string]any{}
	}

	extRoot := ensureJSONMap(prefs, "extensions")
	settings := ensureJSONMap(extRoot, "settings")
	ui := ensureJSONMap(extRoot, "ui")
	if ui["developer_mode"] != true {
		ui["developer_mode"] = true
	}

	manifestObj, err := readManifestObject(absPackageDir)
	if err != nil {
		return err
	}
	apiPerms, hostPerms := permissionsFromManifest(manifestObj)
	now := chromeExtensionInstallTime()

	existing, _ := settings[extID].(map[string]any)
	if existing == nil {
		settings[extID] = map[string]any{
			"active_permissions": map[string]any{
				"api":                 apiPerms,
				"explicit_host":       hostPerms,
				"manifest_permissions": apiPerms,
				"scriptable_host":     hostPerms,
			},
			"granted_permissions": map[string]any{
				"api":                 apiPerms,
				"explicit_host":       hostPerms,
				"manifest_permissions": apiPerms,
				"scriptable_host":     hostPerms,
			},
			"commands":                     map[string]any{},
			"content_settings":             []any{},
			"creation_flags":               float64(1),
			"from_webstore":                false,
			"incognito_content_settings":   []any{},
			"incognito_preferences":        map[string]any{},
			"install_time":                 now,
			"first_install_time":           now,
			"last_update_time":             now,
			"location":                     float64(chromeExtLocationUnpacked),
			"manifest":                     manifestObj,
			"path":                         absPackageDir,
			"preferences":                  map[string]any{},
			"regular_only_preferences":     map[string]any{},
			"state":                        float64(1),
			"was_installed_by_default":     false,
			"was_installed_by_oem":         false,
			"withholding_permissions":      false,
		}
	} else {
		// Heal registration without wiping Chrome-written wallet metadata.
		existing["state"] = float64(1)
		delete(existing, "disable_reasons")
		existing["path"] = absPackageDir
		existing["location"] = float64(chromeExtLocationUnpacked)
		existing["manifest"] = manifestObj
		existing["last_update_time"] = now
		if _, ok := existing["first_install_time"]; !ok {
			if _, ok2 := existing["install_time"]; !ok2 {
				existing["first_install_time"] = now
				existing["install_time"] = now
			}
		}
		// Only fill empty permission maps — never shrink Chrome-granted sets.
		if needsPermissionHeal(existing) {
			existing["active_permissions"] = map[string]any{
				"api":                  apiPerms,
				"explicit_host":        hostPerms,
				"manifest_permissions": apiPerms,
				"scriptable_host":      hostPerms,
			}
			existing["granted_permissions"] = map[string]any{
				"api":                  apiPerms,
				"explicit_host":        hostPerms,
				"manifest_permissions": apiPerms,
				"scriptable_host":      hostPerms,
			}
		}
		settings[extID] = existing
	}
	_ = version

	out, err := json.MarshalIndent(prefs, "", "   ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(prefPath, out, 0644)
}

func needsPermissionHeal(entry map[string]any) bool {
	if entry == nil {
		return true
	}
	for _, key := range []string{"active_permissions", "granted_permissions"} {
		m, _ := entry[key].(map[string]any)
		if m == nil {
			return true
		}
		api, _ := m["api"].([]any)
		if len(api) == 0 {
			// Host-only extensions may have empty api — check hosts.
			hosts, _ := m["explicit_host"].([]any)
			if len(hosts) == 0 {
				return true
			}
		}
	}
	return false
}

// permissionsFromManifest extracts Chromium-style permission lists from a
// parsed manifest (MV2 permissions + MV3 host_permissions).
func permissionsFromManifest(manifest map[string]any) (api []any, hosts []any) {
	api = []any{}
	hosts = []any{}
	seenAPI := map[string]struct{}{}
	seenHost := map[string]struct{}{}
	addAPI := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		// Host patterns in permissions array (MV2).
		if strings.Contains(s, "://") || strings.HasPrefix(s, "<") || strings.HasPrefix(s, "*") {
			if _, ok := seenHost[s]; !ok {
				seenHost[s] = struct{}{}
				hosts = append(hosts, s)
			}
			return
		}
		if _, ok := seenAPI[s]; !ok {
			seenAPI[s] = struct{}{}
			api = append(api, s)
		}
	}
	addHost := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seenHost[s]; !ok {
			seenHost[s] = struct{}{}
			hosts = append(hosts, s)
		}
	}
	if raw, ok := manifest["permissions"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				addAPI(s)
			}
		}
	}
	if raw, ok := manifest["optional_permissions"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				// Optional: grant so first open is not permission-blocked.
				addAPI(s)
			}
		}
	}
	if raw, ok := manifest["host_permissions"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				addHost(s)
			}
		}
	}
	if raw, ok := manifest["optional_host_permissions"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				addHost(s)
			}
		}
	}
	return api, hosts
}

// disableExtensionInProfile keeps package files + LES/wallet vaults, but sets
// Preferences state to DISABLED so the extension no longer loads after unbind.
func disableExtensionInProfile(userDataDir, packageDir string) error {
	userDataDir = strings.TrimSpace(userDataDir)
	packageDir = strings.TrimSpace(packageDir)
	if userDataDir == "" {
		return fmt.Errorf("disable extension: empty user data")
	}
	extID := resolveExtensionPackageID(packageDir)
	if extID == "" {
		extID = strings.ToLower(filepath.Base(packageDir))
	}
	if extID == "" {
		return fmt.Errorf("disable extension: cannot resolve id")
	}
	prefPath := filepath.Join(userDataDir, "Default", "Preferences")
	data, err := os.ReadFile(prefPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var prefs map[string]any
	if err := json.Unmarshal(data, &prefs); err != nil {
		return err
	}
	extRoot, _ := prefs["extensions"].(map[string]any)
	if extRoot == nil {
		return nil
	}
	settings, _ := extRoot["settings"].(map[string]any)
	if settings == nil {
		return nil
	}
	existing, _ := settings[extID].(map[string]any)
	if existing == nil {
		absPkg, _ := filepath.Abs(packageDir)
		if found, ok := findPreferencesExtensionByPackagePath(userDataDir, absPkg); ok {
			// find returns entry only — need id. Scan again for id.
			for id, raw := range settings {
				entry, _ := raw.(map[string]any)
				if entry == nil {
					continue
				}
				p, _ := entry["path"].(string)
				if normalizeExtensionPath(p) == normalizeExtensionPath(absPkg) {
					existing = entry
					extID = id
					break
				}
			}
			_ = found
		}
	}
	if existing == nil {
		return nil
	}
	existing["state"] = float64(0)
	existing["disable_reasons"] = float64(1)
	settings[extID] = existing
	out, err := json.MarshalIndent(prefs, "", "   ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(prefPath, out, 0644)
}

// disableAssignedExtensionOnProfiles disables the package in each profile's
// Preferences when the environment is not running.
func disableAssignedExtensionOnProfiles(a *App, profileIDs []string, extDir string) {
	if a == nil || a.browserMgr == nil || len(profileIDs) == 0 {
		return
	}
	log := logger.New("Extension")
	for _, id := range profileIDs {
		a.browserMgr.Mutex.Lock()
		profile := a.browserMgr.Profiles[id]
		running := profile != nil && profile.Running
		var userDataDir string
		if profile != nil {
			userDataDir = a.browserMgr.ResolveUserDataDir(profile)
		}
		a.browserMgr.Mutex.Unlock()
		if profile == nil || running || userDataDir == "" {
			continue
		}
		if err := disableExtensionInProfile(userDataDir, extDir); err != nil {
			log.Warn("解绑时禁用 Profile 扩展失败（下次启动可能仍显示）",
				logger.F("profile_id", id),
				logger.F("error", err.Error()),
			)
			continue
		}
		log.Info("解绑：已在 Profile 中禁用扩展（保留钱包 data）",
			logger.F("profile_id", id),
			logger.F("extension_id", resolveExtensionPackageID(extDir)),
		)
	}
}

func readManifestObject(extDir string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(extDir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("profile install: read manifest: %w", err)
	}
	if len(data) > 2*1024*1024 {
		return nil, fmt.Errorf("profile install: manifest too large")
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("profile install: manifest json: %w", err)
	}
	return manifest, nil
}

func chromeExtensionInstallTime() string {
	const windowsToUnixSeconds int64 = 11644473600
	us := time.Now().UnixMicro() + windowsToUnixSeconds*1_000_000
	return strconv.FormatInt(us, 10)
}

// canSkipLoadExtensionCLI is true only when Chrome has already left durable
// per-extension runtime files (LES LevelDB / IndexedDB under the extension id).
// Hand-written Preferences alone is NOT enough — Chromium often ignores those
// rows, which left users with zero toolbar extensions after we stripped CLI.
func canSkipLoadExtensionCLI(userDataDir, packageDir string) bool {
	extID := resolveExtensionPackageID(packageDir)
	if extID == "" {
		extID = strings.ToLower(filepath.Base(packageDir))
	}
	if extID == "" {
		return false
	}
	// Must have real on-disk chrome state, not just our Preferences registration.
	if !extensionHasDurableRuntimeFiles(userDataDir, extID) {
		return false
	}
	// And package path should still resolve / be registered when possible.
	if isExtensionInstalledInProfile(userDataDir, packageDir) {
		return true
	}
	// LES exists from a prior --load-extension session even if path drifted.
	return true
}

// extensionHasDurableRuntimeFiles reports Chrome-written storage under LES or
// the profile Extensions tree for this id (not Preferences-only).
func extensionHasDurableRuntimeFiles(userDataDir, extID string) bool {
	extID = strings.ToLower(strings.TrimSpace(extID))
	if userDataDir == "" || extID == "" {
		return false
	}
	candidates := []string{
		filepath.Join(userDataDir, "Default", "Local Extension Settings", extID),
		filepath.Join(userDataDir, "Local Extension Settings", extID),
		filepath.Join(userDataDir, "Default", "Extension State", extID),
		filepath.Join(userDataDir, "Default", "IndexedDB"),
	}
	for _, dir := range candidates[:3] {
		if directoryHasAnyFile(dir) {
			return true
		}
	}
	// IndexedDB folders are named chrome-extension_<id>_0.indexeddb.*
	idbRoot := candidates[3]
	entries, err := os.ReadDir(idbRoot)
	if err != nil {
		return false
	}
	needle := "chrome-extension_" + extID
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Name()), needle) {
			return true
		}
	}
	return false
}

// ensureAssignedExtensionsInProfile registers packages in Preferences (fast) and
// decides which still need --load-extension. First opens always keep CLI until
// Chrome has written durable extension data.
func ensureAssignedExtensionsInProfile(userDataDir string, launchArgs []string) (registered int, needCLI []string) {
	dirs := activeLoadExtensionDirs(launchArgs)
	disableUnassignedProfileExtensions(userDataDir, dirs)

	if len(dirs) == 0 {
		return 0, nil
	}
	paths := make([]string, 0, len(dirs))
	for _, p := range dirs {
		paths = append(paths, p)
	}
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			if paths[j] < paths[i] {
				paths[i], paths[j] = paths[j], paths[i]
			}
		}
	}
	log := logger.New("Extension")
	for _, packageDir := range paths {
		// Best-effort prefs registration (unpacked path + permissions). Fast.
		if err := installUnpackedExtensionIntoProfile(userDataDir, packageDir); err != nil {
			log.Warn("扩展 Preferences 注册失败",
				logger.F("package", packageDir),
				logger.F("error", err.Error()),
			)
		} else {
			registered++
		}
		if canSkipLoadExtensionCLI(userDataDir, packageDir) {
			log.Info("扩展已有环境 data，跳过 --load-extension",
				logger.F("package", packageDir),
				logger.F("extension_id", resolveExtensionPackageID(packageDir)),
			)
			continue
		}
		needCLI = append(needCLI, packageDir)
	}
	return registered, needCLI
}

// disableUnassignedProfileExtensions turns off extensions no longer assigned.
func disableUnassignedProfileExtensions(userDataDir string, assignedDirs map[string]string) {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return
	}
	assigned := map[string]struct{}{}
	assignedPaths := map[string]struct{}{}
	for _, dir := range assignedDirs {
		id := resolveExtensionPackageID(dir)
		if id == "" {
			id = strings.ToLower(filepath.Base(dir))
		}
		if id != "" {
			assigned[id] = struct{}{}
		}
		if abs, err := filepath.Abs(dir); err == nil {
			assignedPaths[normalizeExtensionPath(abs)] = struct{}{}
		}
	}
	prefPath := filepath.Join(userDataDir, "Default", "Preferences")
	data, err := os.ReadFile(prefPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return
	}
	var prefs map[string]any
	if json.Unmarshal(data, &prefs) != nil {
		return
	}
	extRoot, _ := prefs["extensions"].(map[string]any)
	if extRoot == nil {
		return
	}
	settings, _ := extRoot["settings"].(map[string]any)
	if settings == nil {
		return
	}
	// Only disable entries we manage: path under app extensions/imported or
	// profile Default/Extensions, or path equals a previously assigned package.
	changed := false
	for id, raw := range settings {
		idLower := strings.ToLower(strings.TrimSpace(id))
		if _, keep := assigned[idLower]; keep {
			continue
		}
		entry, _ := raw.(map[string]any)
		if entry == nil {
			continue
		}
		p, _ := entry["path"].(string)
		pl := normalizeExtensionPath(p)
		if pl == "" {
			continue
		}
		if _, keepPath := assignedPaths[pl]; keepPath {
			continue
		}
		// Only touch browserstudio-managed paths (imported packages or our copies).
		plLower := strings.ToLower(pl)
		managed := strings.Contains(plLower, string(os.PathSeparator)+"extensions"+string(os.PathSeparator)+"imported"+string(os.PathSeparator)) ||
			strings.Contains(plLower, string(os.PathSeparator)+"default"+string(os.PathSeparator)+"extensions"+string(os.PathSeparator))
		if !managed {
			continue
		}
		state, _ := entry["state"].(float64)
		if state == 0 {
			continue
		}
		entry["state"] = float64(0)
		entry["disable_reasons"] = float64(1)
		settings[id] = entry
		changed = true
	}
	if !changed {
		return
	}
	out, err := json.MarshalIndent(prefs, "", "   ")
	if err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(prefPath, out, 0644)
}

// applyProfileNativeExtensionLaunchArgs:
//  1. Registers assigned packages in Preferences (unpacked path, no copy);
//  2. Injects --load-extension for packages Chrome has not adapted yet (has no
//     LES/vault). This guarantees the toolbar is never empty after upgrade;
//  3. Skips CLI only when durable profile data proves Chrome already owns the
//     extension — avoiding daily reinject tab spam once wallets exist.
func applyProfileNativeExtensionLaunchArgs(args []string, userDataDir string) (next []string, profileInstalled, cliFallback int) {
	args = normalizeLoadExtensionArgs(args)
	all := activeLoadExtensionDirs(args)
	if len(all) == 0 {
		return args, 0, 0
	}
	base := stripLoadExtensionArgs(args)
	registered, needCLI := ensureAssignedExtensionsInProfile(userDataDir, args)
	if len(needCLI) == 0 {
		return base, registered, 0
	}
	return normalizeLoadExtensionArgs(append(base, "--load-extension="+strings.Join(needCLI, ","))), registered, len(needCLI)
}
