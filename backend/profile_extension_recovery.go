package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// appendProfileExtensionRecoveryLaunchArgs performs one bounded, read-only
// recovery check before Chromium starts. It is intentionally not a watcher,
// worker, or Preferences repair: a profile package is used for this launch only
// when Chromium has lost its registration. A manifest public key, when present,
// must prove the exact same extension ID as the profile folder. Chrome Web
// Store profile packages normally omit that key, so their Chrome-created
// <extension-id>/<version> folder is treated as the identity source.
//
// This is the safe recovery path for old environments affected by Chromium's
// "settings changed outside Chrome" reset. Cookies, Local Extension Settings,
// IndexedDB, wallet data and every Chromium-owned JSON file are left untouched.
func appendProfileExtensionRecoveryLaunchArgs(args []string, userDataDir string) ([]string, int) {
	dirs := recoverableProfileExtensionDirs(userDataDir)
	if len(dirs) == 0 {
		return args, 0
	}
	return normalizeLoadExtensionArgs(append(args, "--load-extension="+strings.Join(dirs, ","))), len(dirs)
}

func recoverableProfileExtensionDirs(userDataDir string) []string {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return nil
	}
	registered := chromePreferenceExtensionStates(userDataDir)
	root := filepath.Join(userDataDir, "Default", "Extensions")
	ids, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	result := make([]string, 0)
	for _, idEntry := range ids {
		if !idEntry.IsDir() {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(idEntry.Name()))
		if !isWebStoreExtensionID(id) {
			continue
		}
		// Presence in Preferences, including an explicitly disabled extension,
		// belongs to the user/Chromium and must not be overridden.
		if _, found := registered[id]; found {
			continue
		}
		versions, readErr := os.ReadDir(filepath.Join(root, idEntry.Name()))
		if readErr != nil {
			continue
		}
		sort.Slice(versions, func(i, j int) bool { return versions[i].Name() > versions[j].Name() })
		for _, version := range versions {
			if !version.IsDir() {
				continue
			}
			candidate := filepath.Join(root, idEntry.Name(), version.Name())
			if validateUnpackedExtensionManifest(candidate) != nil {
				continue
			}
			// A key proves identity mathematically. Most Chrome Web Store packages
			// do not retain the key in manifest.json after installation; in that
			// normal case the browser-created ID directory is the only stable
			// identity record. Never accept a package with a present but mismatched
			// key, because that would create a second extension and sever wallet
			// storage from its original extension ID.
			if key := readManifestPublicKey(candidate); len(key) > 0 && !strings.EqualFold(extensionIDFromPublicKey(key), id) {
				continue
			}
			result = append(result, candidate)
			break
		}
	}
	sort.Strings(result)
	return result
}

// chromePreferenceExtensionStates returns only extension IDs that Chromium has
// already recorded. State values are deliberately not interpreted here: both an
// enabled and a user-disabled record must prevent BrowserStudio from loading a
// second copy through the command line.
func chromePreferenceExtensionStates(userDataDir string) map[string]struct{} {
	states := make(map[string]struct{})
	for _, prefPath := range chromeProfilePreferencePaths(userDataDir) {
		data, err := os.ReadFile(prefPath)
		if err != nil || len(strings.TrimSpace(string(data))) == 0 {
			continue
		}
		var prefs map[string]any
		if json.Unmarshal(data, &prefs) != nil {
			continue
		}
		extensions, _ := prefs["extensions"].(map[string]any)
		settings, _ := extensions["settings"].(map[string]any)
		for rawID := range settings {
			id := strings.ToLower(strings.TrimSpace(rawID))
			if isWebStoreExtensionID(id) {
				states[id] = struct{}{}
			}
		}
	}
	return states
}

// stripBrowserStudioManagedExtensionLaunchArgs strips only manager-owned
// package roots from the runtime command line. It never changes the saved
// profile configuration and never strips a user-supplied unpacked extension
// outside BrowserStudio's own package stores.
func stripBrowserStudioManagedExtensionLaunchArgs(args []string, managedRoots ...string) ([]string, int) {
	roots := make([]string, 0, len(managedRoots))
	for _, root := range managedRoots {
		if root = normalizeExtensionPath(root); root != "" {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		return args, 0
	}
	out := make([]string, 0, len(args))
	suppressed := 0
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if !strings.HasPrefix(strings.ToLower(trimmed), "--load-extension=") {
			out = append(out, arg)
			continue
		}
		kept := make([]string, 0)
		for _, dir := range strings.Split(trimmed[len("--load-extension="):], ",") {
			dir = strings.TrimSpace(strings.Trim(dir, `"`))
			normalized := normalizeExtensionPath(dir)
			managed := false
			for _, root := range roots {
				if normalized == root || strings.HasPrefix(normalized, root+string(os.PathSeparator)) {
					managed = true
					break
				}
			}
			if managed {
				suppressed++
				continue
			}
			if dir != "" {
				kept = append(kept, dir)
			}
		}
		if len(kept) > 0 {
			out = append(out, "--load-extension="+strings.Join(kept, ","))
		}
	}
	return normalizeLoadExtensionArgs(out), suppressed
}
