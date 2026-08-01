package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"boost-browser/backend/internal/fsutil"
	"boost-browser/backend/internal/logger"
)

// Scheme A (AdsPower / MoreLogin style): install assigned packages into the
// Chrome profile (Default/Extensions/<id>/<version> + Preferences) so daily
// starts do NOT need --load-extension. That command-line reload is what opens
// chrome-extension:// unlock/home tabs into the user's tab strip.
//
// Policy:
//   - never touch Local Extension Settings / Cookies / IndexedDB (wallets);
//   - never wipe an existing Preferences.settings[id] vault-bearing entry;
//   - only materialize files + ensure a minimal enabled settings row;
//   - if install fails, caller may fall back to one-time --load-extension.

// installUnpackedExtensionIntoProfile copies packageDir into the profile's
// Extensions tree and registers it in Preferences when missing.
func installUnpackedExtensionIntoProfile(userDataDir, packageDir string) error {
	userDataDir = strings.TrimSpace(userDataDir)
	packageDir = strings.TrimSpace(packageDir)
	if userDataDir == "" || packageDir == "" {
		return fmt.Errorf("profile install: empty path")
	}
	if err := validateUnpackedExtensionManifest(packageDir); err != nil {
		return fmt.Errorf("profile install: invalid package: %w", err)
	}
	extID := resolveExtensionPackageID(packageDir)
	if extID == "" {
		return fmt.Errorf("profile install: cannot resolve extension id for %s", packageDir)
	}
	version := readManifestVersionFromDir(packageDir)
	if version == "" {
		version = "0.0.0.0"
	}
	// Chrome expects Default/Extensions/<id>/<version>/manifest.json
	dest := filepath.Join(userDataDir, "Default", "Extensions", extID, version)
	if err := materializeExtensionPackageFiles(packageDir, dest); err != nil {
		return err
	}
	if err := ensurePreferencesExtensionInstalled(userDataDir, extID, dest, version); err != nil {
		return err
	}
	return nil
}

// isExtensionInstalledInProfile reports durable profile install (files + prefs).
func isExtensionInstalledInProfile(userDataDir, packageDir string) bool {
	userDataDir = strings.TrimSpace(userDataDir)
	packageDir = strings.TrimSpace(packageDir)
	if userDataDir == "" || packageDir == "" {
		return false
	}
	extID := resolveExtensionPackageID(packageDir)
	if extID == "" {
		return false
	}
	if !profileExtensionPackageFilesPresent(userDataDir, extID) {
		return false
	}
	prefIDs := preferenceExtensionIDs(userDataDir)
	_, ok := prefIDs[extID]
	return ok
}

func profileExtensionPackageFilesPresent(userDataDir, extID string) bool {
	extID = strings.ToLower(strings.TrimSpace(extID))
	if userDataDir == "" || extID == "" {
		return false
	}
	root := filepath.Join(userDataDir, "Default", "Extensions", extID)
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if validateUnpackedExtensionManifest(filepath.Join(root, entry.Name())) == nil {
			return true
		}
	}
	return false
}

func materializeExtensionPackageFiles(packageDir, dest string) error {
	if validateUnpackedExtensionManifest(dest) == nil {
		// Already present with a valid manifest — do not overwrite (may share
		// disk with a previous successful install).
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("profile install: create extensions parent: %w", err)
	}
	// Stage then rename so a partial copy never becomes the live version dir.
	stage := dest + ".installing-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	_ = os.RemoveAll(stage)
	if err := copyExtensionPackageDir(packageDir, stage); err != nil {
		_ = os.RemoveAll(stage)
		return fmt.Errorf("profile install: copy package: %w", err)
	}
	if err := validateUnpackedExtensionManifest(stage); err != nil {
		_ = os.RemoveAll(stage)
		return fmt.Errorf("profile install: staged package invalid: %w", err)
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(stage, dest); err != nil {
		// Cross-device fallback.
		if copyErr := copyExtensionPackageDir(stage, dest); copyErr != nil {
			_ = os.RemoveAll(stage)
			_ = os.RemoveAll(dest)
			return fmt.Errorf("profile install: place package: %w", err)
		}
		_ = os.RemoveAll(stage)
	}
	return nil
}

func copyExtensionPackageDir(src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		// Skip junk that wallets never need and can be huge.
		base := strings.ToLower(filepath.Base(path))
		if base == ".git" || base == "node_modules" || base == ".ds_store" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return copyFileContents(path, target, info.Mode())
	})
}

func copyFileContents(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// ensurePreferencesExtensionInstalled merges a minimal enabled settings row for
// extID. Existing rows are left intact (wallet metadata, permissions Chrome
// already granted). Only missing path/state/location are filled.
func ensurePreferencesExtensionInstalled(userDataDir, extID, installPath, version string) error {
	extID = strings.ToLower(strings.TrimSpace(extID))
	installPath = filepath.Clean(strings.TrimSpace(installPath))
	if userDataDir == "" || extID == "" || installPath == "" {
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
	// Unpacked/profile installs still benefit from developer mode UI flag.
	if ui["developer_mode"] != true {
		ui["developer_mode"] = true
	}

	existing, _ := settings[extID].(map[string]any)
	if existing == nil {
		manifestObj, manifestErr := readManifestObject(installPath)
		if manifestErr != nil {
			return manifestErr
		}
		settings[extID] = map[string]any{
			"active_permissions":   map[string]any{},
			"commands":             map[string]any{},
			"content_settings":     []any{},
			"creation_flags":       float64(1),
			"from_webstore":        false,
			"granted_permissions":  map[string]any{},
			"incognito_content_settings": []any{},
			"incognito_preferences":      map[string]any{},
			"install_time":         chromeExtensionInstallTime(),
			// 1 = INTERNAL — persists without --load-extension.
			"location":             float64(1),
			"manifest":             manifestObj,
			"path":                 installPath,
			"preferences":          map[string]any{},
			"regular_only_preferences": map[string]any{},
			"state":                float64(1), // ENABLED
			"was_installed_by_default": false,
			"was_installed_by_oem":     false,
			"withholding_permissions":  false,
		}
	} else {
		// Never replace the whole map — only heal path/state/location if empty.
		if path, _ := existing["path"].(string); strings.TrimSpace(path) == "" {
			existing["path"] = installPath
		}
		if _, ok := existing["state"]; !ok {
			existing["state"] = float64(1)
		}
		if _, ok := existing["location"]; !ok {
			existing["location"] = float64(1)
		}
		if _, ok := existing["manifest"]; !ok {
			if manifestObj, err := readManifestObject(installPath); err == nil {
				existing["manifest"] = manifestObj
			}
		}
		settings[extID] = existing
	}
	_ = version // version is encoded in installPath; kept for call-site clarity

	out, err := json.MarshalIndent(prefs, "", "   ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(prefPath, out, 0644)
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
	// Chrome stores install_time as Windows FILETIME-style microseconds.
	const windowsToUnixSeconds int64 = 11644473600
	us := time.Now().UnixMicro() + windowsToUnixSeconds*1_000_000
	return strconv.FormatInt(us, 10)
}

// ensureAssignedExtensionsInProfile materializes every --load-extension package
// into the profile. Returns how many installed cleanly and which still need CLI
// fallback (should be rare).
func ensureAssignedExtensionsInProfile(userDataDir string, launchArgs []string) (installed int, needCLI []string) {
	dirs := activeLoadExtensionDirs(launchArgs)
	if len(dirs) == 0 {
		return 0, nil
	}
	// Stable order for logs/tests.
	paths := make([]string, 0, len(dirs))
	for _, p := range dirs {
		paths = append(paths, p)
	}
	// map iteration order is random — sort by path.
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			if paths[j] < paths[i] {
				paths[i], paths[j] = paths[j], paths[i]
			}
		}
	}
	log := logger.New("Extension")
	for _, packageDir := range paths {
		if isExtensionInstalledInProfile(userDataDir, packageDir) {
			installed++
			continue
		}
		if err := installUnpackedExtensionIntoProfile(userDataDir, packageDir); err != nil {
			log.Warn("扩展 Profile 安装失败，将回退单次 --load-extension",
				logger.F("package", packageDir),
				logger.F("error", err.Error()),
			)
			needCLI = append(needCLI, packageDir)
			continue
		}
		installed++
		log.Info("扩展已写入环境 Profile（方案 A，无需日常 --load-extension）",
			logger.F("package", packageDir),
			logger.F("extension_id", resolveExtensionPackageID(packageDir)),
		)
	}
	return installed, needCLI
}

// applyProfileNativeExtensionLaunchArgs is Scheme A launch policy:
// install into profile, strip --load-extension for success; keep CLI only for
// packages that failed materialization (fallback).
// Returns (args, profileInstalled, cliFallback).
func applyProfileNativeExtensionLaunchArgs(args []string, userDataDir string) (next []string, profileInstalled, cliFallback int) {
	args = normalizeLoadExtensionArgs(args)
	all := activeLoadExtensionDirs(args)
	if len(all) == 0 {
		return args, 0, 0
	}
	base := stripLoadExtensionArgs(args)
	installed, needCLI := ensureAssignedExtensionsInProfile(userDataDir, args)
	if len(needCLI) == 0 {
		return base, installed, 0
	}
	return normalizeLoadExtensionArgs(append(base, "--load-extension="+strings.Join(needCLI, ","))), installed, len(needCLI)
}
