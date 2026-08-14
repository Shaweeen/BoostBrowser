package backend

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"boost-browser/backend/internal/logger"
)

// extensionPackageRoot is mutable application state, not a program asset.
// Keeping imported package code under data/ means an EXE/installer replacement
// cannot leave a valid environment, Cookies or wallet vault pointing at a
// missing extension package. Wallet secrets remain only in the profile; this
// directory contains extension program files and manifest metadata.
func (a *App) extensionPackageRoot() string {
	return a.resolveAppPath(filepath.Join("data", "extensions", "imported"))
}

func (a *App) legacyGlobalExtensionDir(extensionID string) string {
	return filepath.Join(a.appRoot, "extensions", "imported", safePathName(strings.TrimSpace(extensionID)))
}

// migrateLegacyExtensionPackageStore is a bounded startup migration, not a
// watcher. It copies only a valid extension program package when the new
// persistent store has no package. It never deletes or rewrites a legacy
// package, Chrome profile, Cookies, Local Extension Settings or wallet data.
func (a *App) migrateLegacyExtensionPackageStore() {
	if a == nil || a.browserMgr == nil {
		return
	}
	ids := a.managedExtensionPackageIDs()
	if len(ids) == 0 {
		return
	}
	migrated := 0
	for _, extensionID := range ids {
		destination := a.globalExtensionDir(extensionID)
		if validateUnpackedExtensionManifest(destination) == nil {
			continue
		}
		source := a.legacyGlobalExtensionDir(extensionID)
		if validateUnpackedExtensionManifest(source) != nil {
			source = a.findProfileExtensionPackage(extensionID)
		}
		if source == "" || validateUnpackedExtensionManifest(source) != nil {
			continue
		}
		if err := copyMissingExtensionPackage(source, destination); err != nil {
			logger.New("Extension").Warn("迁移旧版扩展包失败（未改动环境数据）",
				logger.F("extension_id", extensionID),
				logger.F("error", err.Error()),
			)
			continue
		}
		migrated++
	}
	if migrated > 0 {
		logger.New("Extension").Info("已迁移旧版扩展程序包到持久数据目录",
			logger.F("count", migrated),
			logger.F("root", a.extensionPackageRoot()),
		)
	}
}

func (a *App) managedExtensionPackageIDs() []string {
	seen := map[string]struct{}{}
	add := func(value string) {
		value = safePathName(value)
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	if registry, err := a.loadGlobalExtensionRegistry(); err == nil {
		for _, entry := range registry.Extensions {
			add(entry.ExtensionID)
		}
	}
	if registry, err := a.loadProfileExtensionRegistry(); err == nil {
		for _, entry := range registry.Extensions {
			add(entry.ExtensionID)
		}
	}
	for _, profile := range a.browserMgr.List() {
		for _, packageDir := range activeLoadExtensionDirs(profile.LaunchArgs) {
			if id := legacyManagedExtensionID(packageDir, a.appRoot); id != "" {
				add(id)
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (a *App) findProfileExtensionPackage(extensionID string) string {
	for _, profile := range a.browserMgr.List() {
		userDataDir := a.browserMgr.ResolveUserDataDir(&profile)
		for _, profileDir := range chromeUserDataProfileDirectories(userDataDir) {
			base := filepath.Join(userDataDir, profileDir, "Extensions", extensionID)
			entries, err := os.ReadDir(base)
			if err != nil {
				continue
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				candidate := filepath.Join(base, entry.Name())
				if validateUnpackedExtensionManifest(candidate) == nil {
					return candidate
				}
			}
		}
	}
	return ""
}

// chromeUserDataProfileDirectories returns only the top-level Chromium profile
// directories that can own Extensions data. It is used by a one-time package
// preservation migration, never to rewrite profile registration or wallet
// files. Older BrowserStudio environments may have used Profile 1 rather than
// Default, so Default alone is insufficient for upgrade compatibility.
func chromeUserDataProfileDirectories(userDataDir string) []string {
	result := []string{"Default"}
	entries, err := os.ReadDir(userDataDir)
	if err != nil {
		return result
	}
	extra := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() || !safeChromeProfileDirectoryName(entry.Name()) {
			continue
		}
		name := entry.Name()
		if name == "Default" || (!strings.HasPrefix(name, "Profile ") && name != "Guest Profile") {
			continue
		}
		extra = append(extra, name)
	}
	sort.Strings(extra)
	return append(result, extra...)
}

func copyMissingExtensionPackage(source, destination string) error {
	if validateUnpackedExtensionManifest(destination) == nil {
		return nil
	}
	if _, err := os.Stat(destination); err == nil {
		// An invalid destination might be user/Chrome-owned. Preserve it.
		return os.ErrExist
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(destination), ".extension-migration-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := copyExtensionPackageTree(source, staging); err != nil {
		return err
	}
	if err := validateUnpackedExtensionManifest(staging); err != nil {
		return err
	}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	return nil
}

// resolveLegacyManagedExtensionLaunchArgs rewrites only the ephemeral command
// line passed to a newly opened browser. Saved user configuration remains
// untouched. It replaces a legacy BrowserStudio-owned package path only after
// a valid persistent copy exists; external/user-supplied paths are never
// changed.
func (a *App) resolveLegacyManagedExtensionLaunchArgs(args []string) []string {
	result := make([]string, 0, len(args))
	const prefix = "--load-extension="
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			result = append(result, arg)
			continue
		}
		parts := strings.Split(strings.TrimSpace(trimmed[len(prefix):]), ",")
		for index, part := range parts {
			clean := strings.TrimSpace(strings.Trim(part, `"`))
			if id := legacyManagedExtensionID(clean, a.appRoot); id != "" {
				persistent := a.globalExtensionDir(id)
				if validateUnpackedExtensionManifest(persistent) == nil {
					parts[index] = persistent
				}
			}
		}
		result = append(result, prefix+strings.Join(parts, ","))
	}
	return normalizeLoadExtensionArgs(result)
}

func legacyManagedExtensionID(packageDir, appRoot string) string {
	packageDir = strings.TrimSpace(strings.Trim(packageDir, `"`))
	if packageDir == "" {
		return ""
	}
	if abs, err := filepath.Abs(packageDir); err == nil {
		packageDir = abs
	}
	legacyRoot := filepath.Join(appRoot, "extensions", "imported")
	if normalizeExtensionPath(filepath.Dir(packageDir)) != normalizeExtensionPath(legacyRoot) {
		return ""
	}
	id := safePathName(filepath.Base(packageDir))
	if id == "" || id != strings.ToLower(filepath.Base(packageDir)) {
		return ""
	}
	return id
}
