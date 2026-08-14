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
	dirs := recoverableProfileExtensionDirsForLaunch(userDataDir, args)
	if len(dirs) == 0 {
		return args, 0
	}
	// A legacy launch configuration can still contain an old
	// --disable-extensions(-except) flag.  It must not silently defeat this
	// one-time recovery when the active Chromium profile has extension package
	// data but no registration.  This changes launch arguments only; it never
	// changes Preferences or the user's extension state on disk.
	args = removeExtensionBlockingLaunchArgs(args)
	return normalizeLoadExtensionArgs(append(args, "--load-extension="+strings.Join(dirs, ","))), len(dirs)
}

func recoverableProfileExtensionDirs(userDataDir string) []string {
	return recoverableProfileExtensionDirsForLaunch(userDataDir, nil)
}

// recoverableProfileExtensionDirsForLaunch inspects exactly the Chromium
// profile selected by this launch.  Reading every profile under user-data-dir
// here is incorrect: an extension registered in "Profile 1" must not suppress
// recovery of the same extension package in the active "Default" profile.
func recoverableProfileExtensionDirsForLaunch(userDataDir string, launchArgs []string) []string {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return nil
	}
	profileDir := chromeLaunchProfileDirectory(userDataDir, launchArgs)
	registered := chromePreferenceExtensionStatesInProfile(filepath.Join(userDataDir, profileDir))
	root := filepath.Join(userDataDir, profileDir, "Extensions")
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

// chromeLaunchProfileDirectory matches Chromium's profile-selection order for
// this read-only recovery check: an explicit --profile-directory wins; when it
// is absent, Chromium reopens the profile recorded in Local State. Falling back
// to Default is only for fresh/legacy user-data folders without that record.
// This prevents a Default-only scan from missing the user's actual Profile 1,
// Profile 2, etc. extension packages.
func chromeLaunchProfileDirectory(userDataDir string, args []string) string {
	const prefix = "--profile-directory="
	for i, raw := range args {
		arg := strings.TrimSpace(raw)
		lower := strings.ToLower(arg)
		value := ""
		switch {
		case strings.HasPrefix(lower, prefix):
			value = strings.TrimSpace(arg[len(prefix):])
		case strings.EqualFold(arg, "--profile-directory") && i+1 < len(args):
			value = strings.TrimSpace(args[i+1])
		}
		value = strings.TrimSpace(strings.Trim(value, `"`))
		if safeChromeProfileDirectoryName(value) {
			return value
		}
	}
	if profileDir := chromeLastUsedProfileDirectory(userDataDir); profileDir != "" {
		return profileDir
	}
	return "Default"
}

func chromeLastUsedProfileDirectory(userDataDir string) string {
	data, err := os.ReadFile(filepath.Join(userDataDir, "Local State"))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return ""
	}
	var localState struct {
		Profile struct {
			LastUsed           string   `json:"last_used"`
			LastActiveProfiles []string `json:"last_active_profiles"`
		} `json:"profile"`
	}
	if json.Unmarshal(data, &localState) != nil {
		return ""
	}
	if safeChromeProfileDirectoryName(localState.Profile.LastUsed) {
		return strings.TrimSpace(strings.Trim(localState.Profile.LastUsed, `"`))
	}
	for _, candidate := range localState.Profile.LastActiveProfiles {
		if safeChromeProfileDirectoryName(candidate) {
			return strings.TrimSpace(strings.Trim(candidate, `"`))
		}
	}
	return ""
}

func safeChromeProfileDirectoryName(name string) bool {
	name = strings.TrimSpace(strings.Trim(name, `"`))
	if name == "" || name == "." || name == ".." || len(name) > 128 {
		return false
	}
	return !strings.ContainsAny(name, `\\/`)
}

// chromePreferenceExtensionStates returns only extension IDs that Chromium has
// already recorded. State values are deliberately not interpreted here: both an
// enabled and a user-disabled record must prevent BrowserStudio from loading a
// second copy through the command line.
func chromePreferenceExtensionStatesInProfile(profileDir string) map[string]struct{} {
	states := make(map[string]struct{})
	data, err := os.ReadFile(filepath.Join(profileDir, "Preferences"))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return states
	}
	var prefs map[string]any
	if json.Unmarshal(data, &prefs) != nil {
		return states
	}
	extensions, _ := prefs["extensions"].(map[string]any)
	settings, _ := extensions["settings"].(map[string]any)
	for rawID := range settings {
		id := strings.ToLower(strings.TrimSpace(rawID))
		if isWebStoreExtensionID(id) {
			states[id] = struct{}{}
		}
	}
	return states
}
