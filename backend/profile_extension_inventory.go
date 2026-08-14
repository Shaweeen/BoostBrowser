package backend

import (
	"boost-browser/backend/internal/fsutil"
	"boost-browser/backend/internal/logger"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const profileExtensionInventoryFilename = "profile-extension-inventory.json"

// profileExtensionInventory records extension identity only. It deliberately
// never reads or stores extension values, wallet vaults, Cookies or IndexedDB
// contents. The record survives an EXE/installer replacement under data/.
type profileExtensionInventory struct {
	Version   int                 `json:"version"`
	UpdatedAt string              `json:"updatedAt"`
	Profiles  map[string][]string `json:"profiles"`
}

func (a *App) profileExtensionInventoryPath() string {
	return a.resolveAppPath(filepath.Join("data", profileExtensionInventoryFilename))
}

func (a *App) loadProfileExtensionInventory() (profileExtensionInventory, error) {
	result := profileExtensionInventory{Version: 1, Profiles: map[string][]string{}}
	data, err := os.ReadFile(a.profileExtensionInventoryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("读取扩展身份清单失败: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("解析扩展身份清单失败: %w", err)
	}
	if result.Profiles == nil {
		result.Profiles = map[string][]string{}
	}
	result.Version = 1
	for profileID, ids := range result.Profiles {
		result.Profiles[profileID] = normalizeExtensionIDs(ids)
	}
	return result, nil
}

func (a *App) saveProfileExtensionInventory(inventory profileExtensionInventory) error {
	if inventory.Profiles == nil {
		inventory.Profiles = map[string][]string{}
	}
	inventory.Version = 1
	inventory.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	normalizedProfiles := make(map[string][]string, len(inventory.Profiles))
	for rawProfileID, ids := range inventory.Profiles {
		profileID := strings.TrimSpace(rawProfileID)
		if profileID == "" {
			continue
		}
		merged, _ := mergeExtensionIDs(normalizedProfiles[profileID], ids)
		normalizedProfiles[profileID] = merged
	}
	inventory.Profiles = normalizedProfiles
	data, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化扩展身份清单失败: %w", err)
	}
	if err := fsutil.WriteFileAtomic(a.profileExtensionInventoryPath(), data, 0o600); err != nil {
		return fmt.Errorf("保存扩展身份清单失败: %w", err)
	}
	return nil
}

func normalizeExtensionIDs(ids []string) []string {
	seen := map[string]struct{}{}
	for _, raw := range ids {
		id := strings.ToLower(strings.TrimSpace(raw))
		if !isWebStoreExtensionID(id) || isChromiumWebStoreHelperExtension(id, "") {
			continue
		}
		seen[id] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func mergeExtensionIDs(current, observed []string) ([]string, bool) {
	before := normalizeExtensionIDs(current)
	after := normalizeExtensionIDs(append(append([]string{}, before...), observed...))
	if len(before) != len(after) {
		return after, true
	}
	for i := range before {
		if before[i] != after[i] {
			return after, true
		}
	}
	return before, false
}

// observedProfileExtensionIDs reads identity-bearing names and Preferences
// keys only. It does not read any extension storage value.
func observedProfileExtensionIDs(userDataDir string, launchArgs []string) []string {
	userDataDir = strings.TrimSpace(userDataDir)
	if userDataDir == "" {
		return nil
	}
	profileName := chromeLaunchProfileDirectory(userDataDir, launchArgs)
	profileDir := filepath.Join(userDataDir, profileName)
	ids := make([]string, 0)
	for id := range chromePreferenceExtensionStatesInProfile(profileDir) {
		ids = append(ids, id)
	}
	for _, relative := range []string{"Extensions", "Local Extension Settings"} {
		entries, err := os.ReadDir(filepath.Join(profileDir, relative))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				ids = append(ids, entry.Name())
			}
		}
	}
	if entries, err := os.ReadDir(filepath.Join(profileDir, "IndexedDB")); err == nil {
		const prefix = "chrome-extension_"
		for _, entry := range entries {
			name := strings.ToLower(entry.Name())
			if !strings.HasPrefix(name, prefix) || len(name) < len(prefix)+32 {
				continue
			}
			ids = append(ids, name[len(prefix):len(prefix)+32])
		}
	}
	for _, packageDir := range activeLoadExtensionDirs(launchArgs) {
		if id := resolveExtensionPackageID(packageDir); id != "" {
			ids = append(ids, id)
		}
	}
	return normalizeExtensionIDs(ids)
}

func profileHasDurableExtensionData(userDataDir string, launchArgs []string, extensionID string) bool {
	extensionID = strings.ToLower(strings.TrimSpace(extensionID))
	if !isWebStoreExtensionID(extensionID) {
		return false
	}
	profileDir := filepath.Join(userDataDir, chromeLaunchProfileDirectory(userDataDir, launchArgs))
	if directoryHasAnyFile(filepath.Join(profileDir, "Local Extension Settings", extensionID)) {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(profileDir, "IndexedDB"))
	if err != nil {
		return false
	}
	needle := "chrome-extension_" + extensionID
	for _, entry := range entries {
		if strings.HasPrefix(strings.ToLower(entry.Name()), needle) {
			return true
		}
	}
	return false
}

func orphanedProfileExtensionIDs(userDataDir string, launchArgs []string, recorded []string) []string {
	profileDir := filepath.Join(userDataDir, chromeLaunchProfileDirectory(userDataDir, launchArgs))
	registered := chromePreferenceExtensionStatesInProfile(profileDir)
	candidates, _ := mergeExtensionIDs(recorded, observedProfileExtensionIDs(userDataDir, launchArgs))
	orphaned := make([]string, 0)
	for _, id := range candidates {
		if _, ok := registered[id]; ok {
			continue
		}
		if profileHasDurableExtensionData(userDataDir, launchArgs, id) {
			orphaned = append(orphaned, id)
		}
	}
	return orphaned
}

// captureProfileExtensionInventory is the single writer for the persistent
// extension identity record. Missing observations never erase an older ID;
// recovery still requires durable per-ID data, so a normal uninstall that
// removed its storage is not resurrected.
func (a *App) captureProfileExtensionInventory() error {
	if a == nil || a.browserMgr == nil {
		return nil
	}
	a.extensionRecoveryMu.Lock()
	defer a.extensionRecoveryMu.Unlock()
	inventory, err := a.loadProfileExtensionInventory()
	if err != nil {
		return err
	}
	changed := false
	for _, profile := range a.browserMgr.List() {
		observed := observedProfileExtensionIDs(a.browserMgr.ResolveUserDataDir(&profile), profile.LaunchArgs)
		merged, mergedChanged := mergeExtensionIDs(inventory.Profiles[profile.ProfileId], observed)
		if mergedChanged {
			inventory.Profiles[profile.ProfileId] = merged
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return a.saveProfileExtensionInventory(inventory)
}

// prepareProfileExtensionRecovery downloads only exact Chrome Web Store IDs
// that have durable data but lost Chromium registration. CRX public-key
// verification is mandatory before a package can be returned for launch.
func (a *App) prepareProfileExtensionRecovery(profileID, userDataDir string, launchArgs []string) ([]string, []string, error) {
	if a == nil || strings.TrimSpace(profileID) == "" || strings.TrimSpace(userDataDir) == "" {
		return nil, nil, nil
	}
	a.extensionRecoveryMu.Lock()
	defer a.extensionRecoveryMu.Unlock()
	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()

	inventory, err := a.loadProfileExtensionInventory()
	if err != nil {
		return nil, nil, err
	}
	observed := observedProfileExtensionIDs(userDataDir, launchArgs)
	recorded, changed := mergeExtensionIDs(inventory.Profiles[profileID], observed)
	if changed {
		inventory.Profiles[profileID] = recorded
		if err := a.saveProfileExtensionInventory(inventory); err != nil {
			return nil, nil, err
		}
	}
	orphaned := orphanedProfileExtensionIDs(userDataDir, launchArgs, recorded)
	if len(orphaned) == 0 {
		return nil, nil, nil
	}

	packageDirs := make([]string, 0, len(orphaned))
	errorsFound := make([]string, 0)
	for _, id := range orphaned {
		extDir := a.globalExtensionDir(id)
		if !extensionManifestHasStableKey(extDir, id) {
			downloadedDir, downloadErr := a.downloadAndInstallVerifiedWebStoreExtension(id)
			if downloadErr != nil {
				errorsFound = append(errorsFound, id+": "+downloadErr.Error())
				continue
			}
			if !extensionManifestHasStableKey(downloadedDir, id) {
				errorsFound = append(errorsFound, id+": 下载包公钥与原扩展 ID 不一致")
				continue
			}
			extDir = downloadedDir
		}
		packageDirs = append(packageDirs, extDir)
	}
	sort.Strings(packageDirs)
	if len(errorsFound) > 0 {
		return packageDirs, orphaned, fmt.Errorf("部分扩展恢复包准备失败: %s", strings.Join(errorsFound, "; "))
	}
	logger.New("Extension").Info("已按原 ID 准备扩展恢复包",
		logger.F("profile_id", profileID),
		logger.F("extensions", strings.Join(orphaned, ",")),
	)
	return packageDirs, orphaned, nil
}

func appendPreparedExtensionRecoveryArgs(args []string, packageDirs []string) []string {
	if len(packageDirs) == 0 {
		return args
	}
	args = removeExtensionBlockingLaunchArgs(args)
	return normalizeLoadExtensionArgs(append(args, "--load-extension="+strings.Join(packageDirs, ",")))
}
