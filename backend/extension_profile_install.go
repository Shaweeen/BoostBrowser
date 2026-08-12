package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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

// materializeExtensionPackageForProfile copies the shared program package into
// the environment's Default/Extensions/<id>/<version>/ so Chromium can always
// load it via --load-extension even when:
//   - branded Chrome 137+ policy is in play (with DisableLoadExtension flag off)
//   - the install directory later moves (shared path would go stale)
// If a valid materialization already exists, it is reused (no re-copy).
func materializeExtensionPackageForProfile(userDataDir, packageDir string) (localPath, extID, version string, err error) {
	userDataDir = strings.TrimSpace(userDataDir)
	packageDir = strings.TrimSpace(packageDir)
	if userDataDir == "" || packageDir == "" {
		return "", "", "", fmt.Errorf("profile materialize: empty path")
	}
	absPkg, err := filepath.Abs(packageDir)
	if err != nil {
		return "", "", "", fmt.Errorf("profile materialize: abs path: %w", err)
	}
	if err := validateUnpackedExtensionManifest(absPkg); err != nil {
		return "", "", "", fmt.Errorf("profile materialize: invalid package: %w", err)
	}
	extID = resolveExtensionPackageID(absPkg)
	if extID == "" {
		return "", "", "", fmt.Errorf("profile materialize: cannot resolve extension id for %s", absPkg)
	}
	version = readManifestVersionFromDir(absPkg)
	if version == "" {
		version = "0.0.0.0"
	}
	// Chrome version folder names: keep safe characters only.
	verDir := sanitizeExtensionVersionDir(version)
	dest := filepath.Join(userDataDir, "Default", "Extensions", extID, verDir)
	if validateUnpackedExtensionManifest(dest) == nil {
		return dest, extID, version, nil
	}
	if err := os.RemoveAll(dest); err != nil && !os.IsNotExist(err) {
		return "", "", "", fmt.Errorf("profile materialize: clear dest: %w", err)
	}
	if err := copyExtensionPackageTree(absPkg, dest); err != nil {
		_ = os.RemoveAll(dest)
		return "", "", "", fmt.Errorf("profile materialize: copy: %w", err)
	}
	if err := validateUnpackedExtensionManifest(dest); err != nil {
		_ = os.RemoveAll(dest)
		return "", "", "", fmt.Errorf("profile materialize: dest invalid after copy: %w", err)
	}
	return dest, extID, version, nil
}

func sanitizeExtensionVersionDir(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "0.0.0.0"
	}
	var b strings.Builder
	for _, r := range version {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "0.0.0.0"
	}
	return out
}

func copyExtensionPackageTree(src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil // never follow/copy symlinks into profile
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// installUnpackedExtensionIntoProfile materializes the package into the profile
// Extensions tree and registers it in Preferences (path = profile-local copy).
func installUnpackedExtensionIntoProfile(userDataDir, packageDir string) error {
	userDataDir = strings.TrimSpace(userDataDir)
	packageDir = strings.TrimSpace(packageDir)
	if userDataDir == "" || packageDir == "" {
		return fmt.Errorf("profile install: empty path")
	}
	localPath, extID, version, err := materializeExtensionPackageForProfile(userDataDir, packageDir)
	if err != nil {
		// Fallback: shared path only (older behaviour) if copy fails.
		absPkg, absErr := filepath.Abs(packageDir)
		if absErr != nil {
			return err
		}
		if validateUnpackedExtensionManifest(absPkg) != nil {
			return err
		}
		id := resolveExtensionPackageID(absPkg)
		ver := readManifestVersionFromDir(absPkg)
		if ver == "" {
			ver = "0.0.0.0"
		}
		if id == "" {
			return err
		}
		logger.New("Extension").Warn("扩展未能物化进环境目录，回退共享包路径",
			logger.F("error", err.Error()),
			logger.F("package", absPkg),
		)
		return ensurePreferencesUnpackedExtension(userDataDir, id, absPkg, ver)
	}
	return ensurePreferencesUnpackedExtension(userDataDir, extID, localPath, version)
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

// extensionAlreadyPresentInProfileReadOnly is a READ-ONLY check used for
// "does Chrome have durable extension/wallet data we must protect?".
// Never creates/updates/deletes Preferences, LES, Cookies, or package files.
//
// True when any of:
//   - Local Extension Settings / Extension State / extension IndexedDB has files
//   - Preferences.settings[id] is ENABLED and path still has a valid manifest
//
// NOTE: LES-only true means vault data exists, NOT that Chrome can load the
// package. CLI-skip decisions must use canSkipLoadExtensionCLI instead.
func extensionAlreadyPresentInProfileReadOnly(userDataDir, packageDir string) bool {
	userDataDir = strings.TrimSpace(userDataDir)
	packageDir = strings.TrimSpace(packageDir)
	if userDataDir == "" || packageDir == "" {
		return false
	}
	absPkg, err := filepath.Abs(packageDir)
	if err != nil {
		absPkg = packageDir
	}
	extID := resolveExtensionPackageID(packageDir)
	if extID == "" {
		extID = strings.ToLower(filepath.Base(packageDir))
	}
	if extID != "" && extensionHasDurableRuntimeFiles(userDataDir, extID) {
		return true
	}
	return extensionPreferencesPathLoadable(userDataDir, extID, absPkg)
}

// extensionPreferencesPathLoadable reports Preferences ENABLED + path whose
// manifest.json still exists (Chrome can load without --load-extension).
func extensionPreferencesPathLoadable(userDataDir, extID, absPkg string) bool {
	entry, ok := readPreferencesExtensionEntry(userDataDir, extID)
	if !ok || entry == nil {
		entry, ok = findPreferencesExtensionByPackagePath(userDataDir, absPkg)
	}
	if !ok || entry == nil {
		return false
	}
	state, _ := entry["state"].(float64)
	if state != 1 {
		return false
	}
	path, _ := entry["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	if validateUnpackedExtensionManifest(path) == nil {
		return true
	}
	if absPkg != "" && validateUnpackedExtensionManifest(absPkg) == nil &&
		normalizeExtensionPath(path) == normalizeExtensionPath(absPkg) {
		return true
	}
	return false
}

// canSkipLoadExtensionCLI is the start-path gate for cancelling --load-extension.
//
// Both required:
//  1. Preferences ENABLED + package path still loadable on disk
//     (LES alone is NOT enough — upgrade can leave a stale absolute path while
//     the wallet vault remains → strip CLI → empty toolbar).
//  2. Chrome has already written durable runtime data under this extension id
//     (LES / Extension State / extension IndexedDB). Preferences-only rows
//     written at assign time do not prove Chromium loaded the package; keeping
//     CLI until first real adapt fixes "分配成功但 chrome://extensions 为空".
//
// Never writes disk.
func canSkipLoadExtensionCLI(userDataDir, packageDir string) bool {
	if !isExtensionInstalledInProfile(userDataDir, packageDir) {
		return false
	}
	extID := resolveExtensionPackageID(packageDir)
	if extID == "" {
		extID = strings.ToLower(filepath.Base(packageDir))
	}
	if extID == "" {
		return false
	}
	return extensionHasDurableRuntimeFiles(userDataDir, extID)
}

// healAssignedExtensionPackagePaths rewrites only Preferences path/state for
// assigned packages so Chrome can load them after an upgrade moved the program
// package directory. Never deletes/truncates LES, Cookies, IndexedDB, or vaults.
// Returns how many packages were healed.
func healAssignedExtensionPackagePaths(userDataDir string, launchArgs []string) int {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return 0
	}
	healed := 0
	log := logger.New("Extension")
	for _, packageDir := range activeLoadExtensionDirs(launchArgs) {
		packageDir = strings.TrimSpace(packageDir)
		if packageDir == "" {
			continue
		}
		if validateUnpackedExtensionManifest(packageDir) != nil {
			// Shared package missing entirely — keep CLI path / wait for re-import.
			continue
		}
		if isExtensionInstalledInProfile(userDataDir, packageDir) {
			continue
		}
		// Broken prefs path or LES-only: re-register path. installUnpacked
		// heals path/state only and never wipes existing LES.
		if err := installUnpackedExtensionIntoProfile(userDataDir, packageDir); err != nil {
			log.Warn("扩展包路径修复失败（不触碰钱包 LES）",
				logger.F("package", packageDir),
				logger.F("error", err.Error()),
			)
			continue
		}
		healed++
		log.Info("已修复扩展 Preferences 加载路径（钱包/LES 未改动）",
			logger.F("package", packageDir),
			logger.F("extension_id", resolveExtensionPackageID(packageDir)),
		)
	}
	return healed
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

// selectLoadExtensionCLIReadOnly decides which assigned packages still need
// --load-extension. READ-ONLY: never writes Preferences / LES / files.
// Returns (alreadyPresentCount, needCLIPaths).
func selectLoadExtensionCLIReadOnly(userDataDir string, launchArgs []string) (present int, needCLI []string) {
	dirs := activeLoadExtensionDirs(launchArgs)
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
		// Skip CLI only when Preferences package path is loadable — not LES alone.
		if canSkipLoadExtensionCLI(userDataDir, packageDir) {
			present++
			log.Info("只读检测：扩展 Preferences 路径可加载，取消该包 CLI load",
				logger.F("package", packageDir),
				logger.F("extension_id", resolveExtensionPackageID(packageDir)),
			)
			continue
		}
		needCLI = append(needCLI, packageDir)
	}
	return present, needCLI
}

// ensureAssignedExtensionsInProfile is for explicit user assign actions only
// (writes prefs registration). Start path must use selectLoadExtensionCLIReadOnly.
func ensureAssignedExtensionsInProfile(userDataDir string, launchArgs []string) (registered int, needCLI []string) {
	// Start-critical path must NOT call this. Kept for assign-time optional use.
	return selectLoadExtensionCLIReadOnly(userDataDir, launchArgs)
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

// applyProfileNativeExtensionLaunchArgs is the START-PATH policy (read-only):
//   - if the environment already has the extension → cancel CLI for that package
//   - otherwise keep --load-extension for first adapt only
// Never writes Preferences, LES, Cookies, or package files.
func applyProfileNativeExtensionLaunchArgs(args []string, userDataDir string) (next []string, present, needCLI int) {
	args = normalizeLoadExtensionArgs(args)
	all := activeLoadExtensionDirs(args)
	if len(all) == 0 {
		return args, 0, 0
	}
	base := stripLoadExtensionArgs(args)
	presentN, need := selectLoadExtensionCLIReadOnly(userDataDir, args)
	if len(need) == 0 {
		return base, presentN, 0
	}
	return normalizeLoadExtensionArgs(append(base, "--load-extension="+strings.Join(need, ","))), presentN, len(need)
}

// ensureLoadExtensionCommandLineSwitchEnabled makes --load-extension work on
// Chrome 137+ branded builds that disable the switch by default.
// See: DisableLoadExtensionCommandLineSwitch.
func ensureLoadExtensionCommandLineSwitchEnabled(args []string) []string {
	hasLoad := false
	for _, arg := range args {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(arg)), "--load-extension=") {
			hasLoad = true
			break
		}
	}
	if !hasLoad {
		return args
	}
	const feature = "DisableLoadExtensionCommandLineSwitch"
	out := make([]string, 0, len(args)+1)
	merged := false
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		low := strings.ToLower(trimmed)
		if strings.HasPrefix(low, "--disable-features=") {
			val := strings.TrimSpace(trimmed[len("--disable-features="):])
			parts := strings.Split(val, ",")
			found := false
			for _, p := range parts {
				if strings.EqualFold(strings.TrimSpace(p), feature) {
					found = true
					break
				}
			}
			if !found {
				if val == "" {
					trimmed = "--disable-features=" + feature
				} else {
					trimmed = "--disable-features=" + val + "," + feature
				}
			}
			merged = true
			out = append(out, trimmed)
			continue
		}
		out = append(out, arg)
	}
	if !merged {
		out = append(out, "--disable-features="+feature)
	}
	return out
}
