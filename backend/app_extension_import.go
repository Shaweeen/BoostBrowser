package backend

import (
	"archive/zip"
	"boost-browser/backend/internal/fsutil"
	"boost-browser/backend/internal/logger"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type ExtensionImportResult struct {
	ExtensionDir     string   `json:"extensionDir"`
	ExtensionID      string   `json:"extensionId"`
	ExtensionVersion string   `json:"extensionVersion"`
	PreviousVersion  string   `json:"previousVersion"`
	UpdatedProfiles  []string `json:"updatedProfiles"`
	// SkippedProfiles: already had the same loadable extension (no overwrite).
	SkippedCount int `json:"skippedCount,omitempty"`
	// PrefsInstalledCount: stopped envs where Preferences registration is loadable after assign.
	PrefsInstalledCount int `json:"prefsInstalledCount,omitempty"`
	// DeferredRunningCount: envs were running; LaunchArgs bound, Preferences on next cold start.
	DeferredRunningCount int `json:"deferredRunningCount,omitempty"`
	Message              string `json:"message"`
}

// extensionBindResult is the outcome of binding one shared package to profiles.
// Policy: never wipe LES/Cookies/IndexedDB; skip is decided before bind by
// filterProfilesMissingEquivalentExtension.
type extensionBindResult struct {
	UpdatedProfiles []string
	PrefsInstalled  int
	DeferredRunning int
	FailedPrefs     []string
}

// GlobalManagedExtension is the backend-authoritative global extension policy.
// Extension directories are resolved from ExtensionID at runtime so an installed
// application can be moved without leaving stale absolute paths in the registry.
type GlobalManagedExtension struct {
	DownloadAddress string   `json:"downloadAddress"`
	ExtensionID     string   `json:"extensionId"`
	ExtensionDir    string   `json:"extensionDir"`
	Installed       bool     `json:"installed"`
	ProfileIDs      []string `json:"profileIds"`
}

type globalExtensionRegistry struct {
	Extensions []globalExtensionRegistryEntry `json:"extensions"`
}

type globalExtensionRegistryEntry struct {
	DownloadAddress string   `json:"downloadAddress"`
	ExtensionID     string   `json:"extensionId"`
	ProfileIDs      []string `json:"profileIds,omitempty"`
}

// profileExtensionRegistry records explicit manual assignments for later edit
// and removal actions. Browser startup never reads it or performs extension
// maintenance; persisted profile launch arguments remain the launch authority.
type profileExtensionRegistry struct {
	Extensions []profileExtensionRegistryEntry `json:"extensions"`
}

type profileExtensionRegistryEntry struct {
	DownloadAddress string   `json:"downloadAddress"`
	ExtensionID     string   `json:"extensionId"`
	ProfileIDs      []string `json:"profileIds"`
}

const (
	globalExtensionRegistryFilename  = "global-extensions.json"
	profileExtensionRegistryFilename = "profile-extensions.json"
	managedExtensionChromeVersion    = "148.0.7778.167"
)

var chromeWebStoreIDPattern = regexp.MustCompile(`[a-p]{32}`)

// BrowserProfileRemoveExtension 解除扩展与实例启动参数的绑定，并清理 profile 内残留的扩展记录。
func (a *App) BrowserProfileRemoveExtension(profileIds []string, downloadAddress string) (*ExtensionImportResult, error) {
	profileIds = normalizeProfileIDs(profileIds)
	if len(profileIds) == 0 {
		return nil, fmt.Errorf("请选择要移除扩展的实例")
	}
	downloadAddress = strings.TrimSpace(downloadAddress)
	if downloadAddress == "" {
		return nil, fmt.Errorf("缺少扩展标识")
	}
	if a == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("浏览器管理器未初始化")
	}

	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()

	extID := extractExtensionID(downloadAddress)
	if extID == "" {
		return nil, fmt.Errorf("无法识别扩展 ID")
	}
	extDir := filepath.Join(a.appRoot, "extensions", "imported", safePathName(extID))

	assignments, err := a.loadProfileExtensionRegistry()
	if err != nil {
		return nil, err
	}
	assignments.Extensions, _ = removeProfileExtensionAssignments(assignments.Extensions, extID, profileIds)
	if err := a.saveProfileExtensionRegistry(assignments); err != nil {
		return nil, err
	}

	a.browserMgr.Mutex.Lock()
	type previousProfileState struct {
		launchArgs []string
		updatedAt  string
	}
	previous := make(map[string]previousProfileState, len(profileIds))
	updated := make([]string, 0, len(profileIds))
	for _, id := range profileIds {
		profile, ok := a.browserMgr.Profiles[id]
		if !ok || profile == nil {
			continue
		}
		nextArgs, changed := removeExtensionDirFromLaunchArgs(profile.LaunchArgs, extDir)
		if changed {
			previous[id] = previousProfileState{
				launchArgs: append([]string{}, profile.LaunchArgs...),
				updatedAt:  profile.UpdatedAt,
			}
			profile.LaunchArgs = nextArgs
			profile.UpdatedAt = time.Now().Format(time.RFC3339)
		}
		updated = append(updated, id)
	}
	if len(updated) == 0 {
		a.browserMgr.Mutex.Unlock()
		return nil, fmt.Errorf("选中的实例未绑定该扩展")
	}
	stillReferenced := profileExtensionAssigned(assignments.Extensions, extID)
	for _, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}
		if hasExtensionDirInLaunchArgs(profile.LaunchArgs, extDir) {
			stillReferenced = true
			break
		}
	}
	if err := a.browserMgr.SaveProfiles(); err != nil {
		for id, state := range previous {
			if profile := a.browserMgr.Profiles[id]; profile != nil {
				profile.LaunchArgs = state.launchArgs
				profile.UpdatedAt = state.updatedAt
			}
		}
		a.browserMgr.Mutex.Unlock()
		return nil, fmt.Errorf("保存实例扩展配置失败：%w", err)
	}
	a.browserMgr.Mutex.Unlock()
	a.clearExtensionLaunchReadyForProfiles(updated)

	// Scheme A: LaunchArgs no longer drive daily load, so unbind must also
	// disable the extension inside each stopped profile's Preferences.
	// Wallet LES / Cookies are preserved for re-assign.
	disableAssignedExtensionOnProfiles(a, updated, extDir)

	if !stillReferenced && !a.globalExtensionRegistered(extID) {
		_ = os.RemoveAll(extDir)
		_ = os.RemoveAll(extDir + ".previous")
	}

	return &ExtensionImportResult{
		ExtensionDir:    extDir,
		ExtensionID:     extID,
		UpdatedProfiles: updated,
		Message:         fmt.Sprintf("扩展已从 %d 个实例解绑并在 Profile 中禁用（钱包数据保留），重启后不再加载", len(updated)),
	}, nil
}

// BrowserProfileImportExtension 下载扩展并绑定到选中的实例。
// 支持 Chrome Web Store 详情页、32 位扩展 ID、直接 .crx/.zip 下载地址。
func (a *App) BrowserProfileImportExtension(profileIds []string, downloadAddress string) (*ExtensionImportResult, error) {
	profileIds = normalizeProfileIDs(profileIds)
	if len(profileIds) == 0 {
		return nil, fmt.Errorf("请选择要导入扩展的实例")
	}
	downloadAddress = strings.TrimSpace(downloadAddress)
	if downloadAddress == "" {
		return nil, fmt.Errorf("请输入扩展程序下载地址")
	}
	if a == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("浏览器管理器未初始化")
	}

	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()

	candidateID := a.registeredExtensionIDForAddress(downloadAddress)
	if candidateID == "" {
		candidateID = extractExtensionID(downloadAddress)
	}
	requestedN := len(profileIds)
	if candidateID != "" {
		missing := a.filterProfilesMissingEquivalentExtension(profileIds, candidateID, "")
		if len(missing) == 0 {
			return &ExtensionImportResult{
				ExtensionID:     candidateID,
				UpdatedProfiles: []string{},
				SkippedCount:    requestedN,
				Message:         formatExtensionAssignMessage(candidateID, "", requestedN, requestedN, 0, 0, 0, 0),
			}, nil
		}
		profileIds = missing
	}

	extID, extDir, previousVersion, extensionVersion, err := a.downloadAndInstallExtension(downloadAddress)
	if err != nil {
		return nil, fmt.Errorf("扩展分配失败：%w", err)
	}
	manifestName := readManifestNameFromDir(extDir)
	profileIds = a.filterProfilesMissingEquivalentExtension(profileIds, extID, manifestName)
	skipped := requestedN - len(profileIds)
	if len(profileIds) == 0 {
		return &ExtensionImportResult{
			ExtensionDir:     extDir,
			ExtensionID:      extID,
			ExtensionVersion: extensionVersion,
			PreviousVersion:  previousVersion,
			UpdatedProfiles:  []string{},
			SkippedCount:     skipped,
			Message:          formatExtensionAssignMessage(extID, extensionVersion, requestedN, skipped, 0, 0, 0, 0),
		}, nil
	}
	for _, id := range profileIds {
		if err := a.enableExtensionDeveloperModeForProfile(id); err != nil {
			return nil, err
		}
	}

	bind, err := a.bindExtensionDirToProfiles(profileIds, extDir)
	if err != nil {
		return nil, err
	}
	assignments, err := a.loadProfileExtensionRegistry()
	if err != nil {
		return nil, err
	}
	assignments.Extensions = upsertProfileExtensionAssignments(assignments.Extensions, profileExtensionRegistryEntry{
		DownloadAddress: downloadAddress,
		ExtensionID:     extID,
		ProfileIDs:      bind.UpdatedProfiles,
	})
	if err := a.saveProfileExtensionRegistry(assignments); err != nil {
		return nil, err
	}
	return &ExtensionImportResult{
		ExtensionDir:         extDir,
		ExtensionID:          extID,
		ExtensionVersion:     extensionVersion,
		PreviousVersion:      previousVersion,
		UpdatedProfiles:      bind.UpdatedProfiles,
		SkippedCount:         skipped,
		PrefsInstalledCount:  bind.PrefsInstalled,
		DeferredRunningCount: bind.DeferredRunning,
		Message: formatExtensionAssignMessage(
			extID, extensionVersion, requestedN, skipped,
			len(bind.UpdatedProfiles), bind.PrefsInstalled, bind.DeferredRunning, len(bind.FailedPrefs),
		),
	}, nil
}

// BrowserGlobalExtensionImport runs only for the user's explicit global
// distribution action. It checks the current profile set once, binds only
// missing extensions and records the completed set. Profile creation and
// browser startup never call this workflow.
func (a *App) BrowserGlobalExtensionImport(downloadAddress string) (*ExtensionImportResult, error) {
	downloadAddress = strings.TrimSpace(downloadAddress)
	if downloadAddress == "" {
		return nil, fmt.Errorf("请输入扩展程序下载地址")
	}
	if a == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("浏览器管理器未初始化")
	}

	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()

	registry, err := a.loadGlobalExtensionRegistry()
	if err != nil {
		return nil, err
	}
	profiles := a.browserMgr.List()
	targetIDs := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		targetIDs = append(targetIDs, profile.ProfileId)
	}
	candidateID := a.registeredExtensionIDForAddress(downloadAddress)
	if candidateID == "" {
		candidateID = extractExtensionID(downloadAddress)
	}
	completed := globalExtensionCompletedProfiles(registry.Extensions, candidateID, downloadAddress)
	unchecked := make([]string, 0, len(targetIDs))
	for _, profileID := range targetIDs {
		if !completed[profileID] {
			unchecked = append(unchecked, profileID)
		}
	}
	if len(unchecked) == 0 && candidateID != "" && extensionManifestExists(a.globalExtensionDir(candidateID)) {
		return &ExtensionImportResult{
			ExtensionDir:    a.globalExtensionDir(candidateID),
			ExtensionID:     candidateID,
			UpdatedProfiles: []string{},
			SkippedCount:    len(targetIDs),
			Message:         formatExtensionAssignMessage(candidateID, "", len(targetIDs), len(targetIDs), 0, 0, 0, 0),
		}, nil
	}

	extID, extDir, previousVersion, extensionVersion, err := a.downloadAndInstallExtension(downloadAddress)
	if err != nil {
		return nil, fmt.Errorf("扩展分配失败：%w", err)
	}
	missing := a.filterProfilesMissingEquivalentExtension(unchecked, extID, readManifestNameFromDir(extDir))
	// Already-complete registry entries + loadable equivalents are skips.
	skipped := len(targetIDs) - len(missing)
	bind := &extensionBindResult{}
	if len(missing) > 0 {
		for _, profileID := range missing {
			if err := a.enableExtensionDeveloperModeForProfile(profileID); err != nil {
				return nil, err
			}
		}
		bind, err = a.bindExtensionDirToProfiles(missing, extDir)
		if err != nil {
			return nil, err
		}
	}
	registry.Extensions = upsertGlobalExtensionRegistryEntry(registry.Extensions, globalExtensionRegistryEntry{
		DownloadAddress: downloadAddress,
		ExtensionID:     extID,
		ProfileIDs:      targetIDs,
	})
	if err := a.saveGlobalExtensionRegistry(registry); err != nil {
		return nil, err
	}
	return &ExtensionImportResult{
		ExtensionDir:         extDir,
		ExtensionID:          extID,
		ExtensionVersion:     extensionVersion,
		PreviousVersion:      previousVersion,
		UpdatedProfiles:      bind.UpdatedProfiles,
		SkippedCount:         skipped,
		PrefsInstalledCount:  bind.PrefsInstalled,
		DeferredRunningCount: bind.DeferredRunning,
		Message: formatExtensionAssignMessage(
			extID, extensionVersion, len(targetIDs), skipped,
			len(bind.UpdatedProfiles), bind.PrefsInstalled, bind.DeferredRunning, len(bind.FailedPrefs),
		),
	}, nil
}

// BrowserGlobalExtensionRemove removes a global policy and unbinds the
// extension from every existing profile.
func (a *App) BrowserGlobalExtensionRemove(downloadAddress string) (*ExtensionImportResult, error) {
	downloadAddress = strings.TrimSpace(downloadAddress)
	if downloadAddress == "" {
		return nil, fmt.Errorf("缺少扩展标识")
	}
	if a == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("浏览器管理器未初始化")
	}

	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()

	registry, err := a.loadGlobalExtensionRegistry()
	if err != nil {
		return nil, err
	}
	wantedID := extractExtensionID(downloadAddress)
	removedIDs := make([]string, 0, 1)
	kept := make([]globalExtensionRegistryEntry, 0, len(registry.Extensions))
	for _, entry := range registry.Extensions {
		addressMatches := strings.EqualFold(strings.TrimSpace(entry.DownloadAddress), downloadAddress)
		idMatches := wantedID != "" && strings.EqualFold(strings.TrimSpace(entry.ExtensionID), wantedID)
		if addressMatches || idMatches {
			removedIDs = appendUniqueString(removedIDs, strings.TrimSpace(entry.ExtensionID))
			continue
		}
		kept = append(kept, entry)
	}
	if len(removedIDs) == 0 && wantedID != "" {
		removedIDs = append(removedIDs, wantedID)
	}
	registry.Extensions = kept
	if err := a.saveGlobalExtensionRegistry(registry); err != nil {
		return nil, err
	}

	updatedSet := map[string]bool{}
	lastDir := ""
	assignments, assignmentErr := a.loadProfileExtensionRegistry()
	if assignmentErr != nil {
		return nil, assignmentErr
	}
	for _, extID := range removedIDs {
		if extID == "" {
			continue
		}
		extDir := a.globalExtensionDir(extID)
		lastDir = extDir
		keepProfiles := assignedProfilesForExtension(assignments.Extensions, extID)
		updated, removeErr := a.removeExtensionDirFromProfilesExcept(extDir, keepProfiles)
		if removeErr != nil {
			return nil, removeErr
		}
		for _, id := range updated {
			updatedSet[id] = true
		}
		if len(keepProfiles) == 0 {
			_ = os.RemoveAll(extDir)
			_ = os.RemoveAll(extDir + ".previous")
		}
	}
	updated := make([]string, 0, len(updatedSet))
	for id := range updatedSet {
		updated = append(updated, id)
	}
	return &ExtensionImportResult{
		ExtensionDir:    lastDir,
		ExtensionID:     wantedID,
		UpdatedProfiles: updated,
		Message:         fmt.Sprintf("全局扩展已移除并从 %d 个实例解绑，运行中的实例重启后生效", len(updated)),
	}, nil
}

// BrowserGlobalExtensionList reports the persisted backend policies. The
// frontend uses this to distinguish an actually installed global extension
// from an item that merely exists in localStorage.
func (a *App) BrowserGlobalExtensionList() ([]GlobalManagedExtension, error) {
	if a == nil {
		return nil, fmt.Errorf("应用未初始化")
	}
	registry, err := a.loadGlobalExtensionRegistry()
	if err != nil {
		return nil, err
	}
	out := make([]GlobalManagedExtension, 0, len(registry.Extensions))
	existingProfiles := map[string]bool{}
	for _, profile := range a.browserMgr.List() {
		existingProfiles[profile.ProfileId] = true
	}
	for _, entry := range registry.Extensions {
		extDir := a.globalExtensionDir(entry.ExtensionID)
		profileIDs := make([]string, 0, len(entry.ProfileIDs))
		for _, profileID := range entry.ProfileIDs {
			if existingProfiles[profileID] {
				profileIDs = append(profileIDs, profileID)
			}
		}
		out = append(out, GlobalManagedExtension{
			DownloadAddress: entry.DownloadAddress,
			ExtensionID:     entry.ExtensionID,
			ExtensionDir:    extDir,
			Installed:       extensionManifestExists(extDir),
			ProfileIDs:      profileIDs,
		})
	}
	return out, nil
}

// chromiumWebStoreHelperExtensionID is the public-key-derived ID of the
// embedded Cloak "chromium-web-store" helper (not a user wallet/business ext).
const chromiumWebStoreHelperExtensionID = "lfoeajgcchlidpicbabpmckkejpckcfb"

func isChromiumWebStoreHelperExtension(extID, extDir string) bool {
	if strings.EqualFold(strings.TrimSpace(extID), chromiumWebStoreHelperExtensionID) {
		return true
	}
	low := strings.ToLower(filepath.ToSlash(extDir))
	return strings.Contains(low, "/chromium-web-store") || strings.HasSuffix(low, "/chromium-web-store")
}

func (a *App) downloadAndInstallExtension(downloadAddress string) (string, string, string, string, error) {
	extID := extractExtensionID(downloadAddress)
	if extID == "" {
		extID = a.registeredExtensionIDForAddress(downloadAddress)
	}
	if isChromiumWebStoreHelperExtension(extID, "") {
		return "", "", "", "", fmt.Errorf("不能分配内置 Web Store 助手扩展（%s）。请上传 MetaMask/Rabby 等业务扩展的商店地址、扩展 ID 或 .crx/.zip", chromiumWebStoreHelperExtensionID)
	}
	if extID != "" {
		extDir := a.globalExtensionDir(extID)
		if validateUnpackedExtensionManifest(extDir) == nil {
			if isChromiumWebStoreHelperExtension(extID, extDir) {
				return "", "", "", "", fmt.Errorf("不能分配内置 Web Store 助手扩展。请重新从 Chrome 网上应用店或 .crx 导入业务扩展（需代理）")
			}
			// Reuse the program package without a network round-trip. Missing
			// manifest keys are repaired at environment start (and after a real
			// CRX re-download) so offline distribution and unit tests stay fast.
			version := readManifestVersionFromDir(extDir)
			return extID, extDir, version, version, nil
		}
	}
	downloadURL := resolveExtensionDownloadURL(downloadAddress, extID)
	if err := validateExtensionDownloadURL(downloadURL); err != nil {
		return "", "", "", "", err
	}
	payload, err := a.downloadExtensionPayload(downloadURL)
	if err != nil {
		return "", "", "", "", err
	}
	zipPayload, publicKey, err := extractZipAndPublicKeyFromCRX(payload)
	if err != nil {
		return "", "", "", "", err
	}
	if derivedID := extensionIDFromPublicKey(publicKey); derivedID != "" {
		// The CRX signature is the cryptographic authority for the extension ID.
		// Prefer it over a path-guessed external ID so wallet storage stays under
		// the same chrome-extension:// ID that dApps and content scripts expect.
		if extID == "" || !isWebStoreExtensionID(extID) || !strings.EqualFold(extID, derivedID) {
			if isWebStoreExtensionID(derivedID) {
				extID = derivedID
			}
		}
	}
	if extID == "" {
		sum := sha256.Sum256(payload)
		extID = "external-" + hex.EncodeToString(sum[:])[:16]
	}
	extDir := a.globalExtensionDir(extID)
	if validateUnpackedExtensionManifest(extDir) == nil {
		if len(publicKey) > 0 {
			_ = ensureManifestPublicKey(extDir, publicKey)
		}
		version := readManifestVersionFromDir(extDir)
		return extID, extDir, version, version, nil
	}
	extDir, previousVersion, extensionVersion, err := installUnpackedExtension(a.appRoot, extID, zipPayload, publicKey)
	if err != nil {
		return "", "", "", "", err
	}
	return extID, extDir, previousVersion, extensionVersion, nil
}

func (a *App) registeredExtensionIDForAddress(downloadAddress string) string {
	key := extensionSourceKey(downloadAddress)
	if key == "" {
		return ""
	}
	if registry, err := a.loadGlobalExtensionRegistry(); err == nil {
		for _, entry := range registry.Extensions {
			if extensionSourceKey(entry.DownloadAddress) == key {
				return strings.TrimSpace(entry.ExtensionID)
			}
		}
	}
	if registry, err := a.loadProfileExtensionRegistry(); err == nil {
		for _, entry := range registry.Extensions {
			if extensionSourceKey(entry.DownloadAddress) == key {
				return strings.TrimSpace(entry.ExtensionID)
			}
		}
	}
	return ""
}

func extensionSourceKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func extensionInstallMessage(previousVersion, extensionVersion string, count int) string {
	return formatExtensionAssignMessage("", extensionVersion, count, 0, count, count, 0, 0)
}

// formatExtensionAssignMessage is the user-facing assign summary.
// Policy: skip same loadable extension; Preferences inject for missing; no LES wipe;
// hot starts do not re-CLI (no extension homepage tabs).
func formatExtensionAssignMessage(extID, version string, total, skipped, bound, prefsOK, deferred, failed int) string {
	name := strings.TrimSpace(extID)
	if ver := strings.TrimSpace(version); ver != "" {
		if name != "" {
			name = name + "@" + ver
		} else {
			name = ver
		}
	}
	if name == "" {
		name = "扩展"
	}
	if total <= 0 {
		total = skipped + bound
	}
	if bound == 0 && skipped > 0 {
		return fmt.Sprintf("%s：所选 %d 个环境均已存在同一可加载扩展，已跳过（未覆盖钱包/扩展数据）", name, skipped)
	}
	parts := make([]string, 0, 6)
	parts = append(parts, fmt.Sprintf("%s 分配完成", name))
	if bound > 0 {
		parts = append(parts, fmt.Sprintf("新绑定 %d", bound))
	}
	if prefsOK > 0 {
		parts = append(parts, fmt.Sprintf("已写入 Profile %d（请关闭后重新打开环境一次以加载扩展）", prefsOK))
	}
	if deferred > 0 {
		parts = append(parts, fmt.Sprintf("运行中延后 %d（必须先关闭环境再分配才能写入）", deferred))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("跳过已有 %d", skipped))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("写入失败 %d", failed))
	}
	parts = append(parts, "未改动已有钱包/LES")
	return strings.Join(parts, "；")
}

func (a *App) filterProfilesMissingEquivalentExtension(profileIDs []string, extensionID string, manifestName string) []string {
	profiles := map[string]BrowserProfile{}
	for _, profile := range a.browserMgr.List() {
		profiles[profile.ProfileId] = profile
	}
	extDir := ""
	if extensionID != "" {
		extDir = a.globalExtensionDir(extensionID)
	}
	missing := make([]string, 0, len(profileIDs))
	for _, profileID := range normalizeProfileIDs(profileIDs) {
		profile, ok := profiles[profileID]
		if !ok {
			continue
		}
		userDataDir := a.browserMgr.ResolveUserDataDir(&profile)
		// Assigned in launch args AND still loadable → skip re-bind.
		// If path is stale after upgrade, re-bind to heal Preferences (LES kept).
		if extDir != "" && hasExtensionDirInLaunchArgs(profile.LaunchArgs, extDir) {
			if isExtensionInstalledInProfile(userDataDir, extDir) {
				continue
			}
			missing = append(missing, profileID)
			continue
		}
		// Only skip when Chrome can still load an equivalent extension package.
		// Stale prefs/LES-only must not block re-import/heal ("已存在未覆盖").
		if profileHasLoadableEquivalentExtension(userDataDir, extensionID, manifestName, extDir) {
			continue
		}
		missing = append(missing, profileID)
	}
	return missing
}

// profileHasEquivalentExtension reports any prefs/Extensions folder residue for
// the id/name. Prefer profileHasLoadableEquivalentExtension for import/assign
// skip decisions so broken package paths can be healed.
func profileHasEquivalentExtension(userDataDir string, extensionID string, manifestName string) bool {
	return profileHasLoadableEquivalentExtension(userDataDir, extensionID, manifestName, "")
}

// profileHasLoadableEquivalentExtension is true only when an equivalent
// extension is ENABLED and its package path still has a valid manifest.
// packageDir (optional) is the shared import package to match against.
// Match by extension id and/or manifest name. Broken paths return false so
// re-import can heal Preferences without claiming "已存在未覆盖".
func profileHasLoadableEquivalentExtension(userDataDir string, extensionID string, manifestName string, packageDir string) bool {
	extensionID = strings.ToLower(strings.TrimSpace(extensionID))
	manifestName = strings.ToLower(strings.TrimSpace(manifestName))
	packageDir = strings.TrimSpace(packageDir)
	if packageDir != "" && isExtensionInstalledInProfile(userDataDir, packageDir) {
		return true
	}
	if extensionID == "" && manifestName == "" {
		return false
	}
	for _, prefPath := range chromeProfilePreferencePaths(userDataDir) {
		data, err := os.ReadFile(prefPath)
		if err != nil {
			continue
		}
		var prefs map[string]any
		if json.Unmarshal(data, &prefs) != nil {
			continue
		}
		extensions, _ := prefs["extensions"].(map[string]any)
		settings, _ := extensions["settings"].(map[string]any)
		if settings == nil {
			continue
		}
		for id, raw := range settings {
			setting, _ := raw.(map[string]any)
			if setting == nil || !extensionSettingIsLoadable(setting) {
				continue
			}
			if extensionID != "" && strings.EqualFold(strings.TrimSpace(id), extensionID) {
				return true
			}
			if manifestName != "" && extensionSettingManifestName(setting) == manifestName {
				return true
			}
		}
	}
	return false
}

func extensionSettingIsLoadable(entry map[string]any) bool {
	if entry == nil {
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
	return validateUnpackedExtensionManifest(path) == nil
}

func chromeProfileDirs(userDataDir string) []string {
	dirs := []string{filepath.Join(userDataDir, "Default"), userDataDir}
	if entries, err := os.ReadDir(userDataDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), "Profile ") {
				dirs = append(dirs, filepath.Join(userDataDir, entry.Name()))
			}
		}
	}
	return dirs
}

func normalizeProfileIDs(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func extractExtensionID(input string) string {
	lower := strings.ToLower(strings.TrimSpace(input))
	if m := chromeWebStoreIDPattern.FindString(lower); m != "" {
		return m
	}
	return ""
}

func resolveExtensionDownloadURL(input string, extID string) string {
	input = strings.TrimSpace(input)
	if extID != "" && (strings.Contains(strings.ToLower(input), "chromewebstore.google.com") || input == extID) {
		return "https://clients2.google.com/service/update2/crx?response=redirect&prodversion=" + managedExtensionChromeVersion + "&acceptformat=crx2,crx3&x=" + url.QueryEscape("id="+extID+"&installsource=ondemand&uc")
	}
	return input
}

func validateExtensionDownloadURL(rawURL string) error {
	if _, err := validatePublicRemoteURL(rawURL, false); err != nil {
		return fmt.Errorf("扩展下载地址无效: %w", err)
	}
	return nil
}

func downloadExtensionPayload(downloadURL string) ([]byte, error) {
	return downloadExtensionPayloadWithTimeoutAndProxy(downloadURL, 90*time.Second, "")
}

func (a *App) downloadExtensionPayload(downloadURL string) ([]byte, error) {
	proxyURL := ""
	if a != nil && a.config != nil {
		// Prefer explicit local gateway from Browser settings (Clash/Nym local port).
		proxyURL = strings.TrimSpace(a.config.Browser.LocalVPNProxy)
	}
	return downloadExtensionPayloadWithTimeoutAndProxy(downloadURL, 90*time.Second, proxyURL)
}

func downloadExtensionPayloadWithTimeout(downloadURL string, timeout time.Duration) ([]byte, error) {
	return downloadExtensionPayloadWithTimeoutAndProxy(downloadURL, timeout, "")
}

func downloadExtensionPayloadWithTimeoutAndProxy(downloadURL string, timeout time.Duration, optionalProxyURL string) ([]byte, error) {
	const maxExtensionDownloadBytes = 128 * 1024 * 1024
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	client := newPublicRemoteHTTPClientWithProxy(timeout, false, optionalProxyURL)
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("扩展下载地址无效：%w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/"+managedExtensionChromeVersion+" Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("扩展下载失败：%w。若在国内网络无法直连 Google，请开启本地代理并设置 HTTPS_PROXY=http://127.0.0.1:端口（或在客户端「本机转发网关」填写该地址），也可改用 .crx/.zip 直链导入", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("扩展下载失败：HTTP %d。若无法访问 Chrome 网上应用店，请检查本机代理/HTTPS_PROXY 或使用 .crx/.zip 直链", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxExtensionDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取扩展数据失败：%w", err)
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("扩展下载失败：文件为空或格式错误")
	}
	if len(data) > maxExtensionDownloadBytes {
		return nil, fmt.Errorf("扩展下载失败：文件超过 128MB 限制")
	}
	return data, nil
}

func extractZipPayloadFromCRX(data []byte) ([]byte, error) {
	zipPayload, _, err := extractZipAndPublicKeyFromCRX(data)
	return zipPayload, err
}

// extractZipAndPublicKeyFromCRX unpacks the ZIP body of a CRX/ZIP payload and,
// for CRX2/CRX3, returns the signing public key used by Chrome to derive the
// stable extension ID. Pure ZIP inputs have no embedded key.
func extractZipAndPublicKeyFromCRX(data []byte) ([]byte, []byte, error) {
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("PK\x03\x04")) {
		return data, nil, nil
	}
	if len(data) < 16 || !bytes.Equal(data[:4], []byte("Cr24")) {
		return nil, nil, fmt.Errorf("扩展格式不支持：请提供 Chrome Web Store 地址、.crx 或 .zip 下载地址")
	}
	version := binary.LittleEndian.Uint32(data[4:8])
	var offset uint32
	var publicKey []byte
	switch version {
	case 2:
		pubLen := binary.LittleEndian.Uint32(data[8:12])
		sigLen := binary.LittleEndian.Uint32(data[12:16])
		if pubLen == 0 || int(16+pubLen) > len(data) {
			return nil, nil, fmt.Errorf("CRX2 解包失败：公钥长度无效")
		}
		publicKey = append([]byte(nil), data[16:16+pubLen]...)
		offset = 16 + pubLen + sigLen
	case 3:
		headerLen := binary.LittleEndian.Uint32(data[8:12])
		if headerLen == 0 || int(12+headerLen) > len(data) {
			return nil, nil, fmt.Errorf("CRX3 解包失败：header 长度无效")
		}
		header := data[12 : 12+headerLen]
		publicKey = extractCRX3PublicKey(header)
		offset = 12 + headerLen
	default:
		return nil, nil, fmt.Errorf("扩展格式不支持：CRX version %d", version)
	}
	if int(offset)+4 > len(data) || !bytes.Equal(data[offset:offset+4], []byte("PK\x03\x04")) {
		return nil, nil, fmt.Errorf("CRX 解包失败：未找到 ZIP 数据")
	}
	return data[offset:], publicKey, nil
}

// extractCRX3PublicKey reads the first RSA (field 2) or ECDSA (field 3)
// AsymmetricKeyProof.public_key (field 1) from a CrxFileHeader protobuf.
func extractCRX3PublicKey(header []byte) []byte {
	var rsaKey, ecdsaKey []byte
	for _, field := range parseProtobufBytesFields(header) {
		switch field.number {
		case 2: // sha256_with_rsa
			if key := protobufMessageBytesField(field.value, 1); len(key) > 0 && len(rsaKey) == 0 {
				rsaKey = key
			}
		case 3: // sha256_with_ecdsa
			if key := protobufMessageBytesField(field.value, 1); len(key) > 0 && len(ecdsaKey) == 0 {
				ecdsaKey = key
			}
		}
	}
	if len(rsaKey) > 0 {
		return rsaKey
	}
	return ecdsaKey
}

type protobufBytesField struct {
	number int
	value  []byte
}

func parseProtobufBytesFields(data []byte) []protobufBytesField {
	out := make([]protobufBytesField, 0, 4)
	i := 0
	for i < len(data) {
		tag, n := readProtobufVarint(data[i:])
		if n <= 0 {
			break
		}
		i += n
		fieldNumber := int(tag >> 3)
		wireType := int(tag & 0x7)
		switch wireType {
		case 0: // varint
			_, n = readProtobufVarint(data[i:])
			if n <= 0 {
				return out
			}
			i += n
		case 1: // 64-bit
			if i+8 > len(data) {
				return out
			}
			i += 8
		case 2: // length-delimited
			length, n := readProtobufVarint(data[i:])
			if n <= 0 {
				return out
			}
			i += n
			if length < 0 || i+int(length) > len(data) {
				return out
			}
			out = append(out, protobufBytesField{
				number: fieldNumber,
				value:  append([]byte(nil), data[i:i+int(length)]...),
			})
			i += int(length)
		case 5: // 32-bit
			if i+4 > len(data) {
				return out
			}
			i += 4
		default:
			return out
		}
	}
	return out
}

func protobufMessageBytesField(message []byte, fieldNumber int) []byte {
	for _, field := range parseProtobufBytesFields(message) {
		if field.number == fieldNumber {
			return field.value
		}
	}
	return nil
}

func readProtobufVarint(data []byte) (uint64, int) {
	var value uint64
	for i := 0; i < len(data) && i < 10; i++ {
		b := data[i]
		value |= uint64(b&0x7f) << (uint(i) * 7)
		if b < 0x80 {
			return value, i + 1
		}
	}
	return 0, 0
}

// extensionIDFromPublicKey derives the Chrome Web Store style extension ID from
// a CRX public key (SHA-256, first 16 bytes, hex digits remapped onto a-p).
func extensionIDFromPublicKey(publicKey []byte) string {
	if len(publicKey) == 0 {
		return ""
	}
	sum := sha256.Sum256(publicKey)
	const alphabet = "abcdefghijklmnop"
	var b strings.Builder
	b.Grow(32)
	for _, byt := range sum[:16] {
		b.WriteByte(alphabet[byt>>4])
		b.WriteByte(alphabet[byt&0x0f])
	}
	return b.String()
}

func isWebStoreExtensionID(id string) bool {
	id = strings.ToLower(strings.TrimSpace(id))
	return len(id) == 32 && chromeWebStoreIDPattern.MatchString(id)
}

func readManifestPublicKey(extDir string) []byte {
	data, err := os.ReadFile(filepath.Join(extDir, "manifest.json"))
	if err != nil || len(data) > 2*1024*1024 {
		return nil
	}
	var manifest map[string]any
	if json.Unmarshal(data, &manifest) != nil {
		return nil
	}
	raw, _ := manifest["key"].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Accept both raw base64 and PEM-wrapped public keys.
	raw = strings.ReplaceAll(raw, "-----BEGIN PUBLIC KEY-----", "")
	raw = strings.ReplaceAll(raw, "-----END PUBLIC KEY-----", "")
	raw = strings.ReplaceAll(raw, "\n", "")
	raw = strings.ReplaceAll(raw, "\r", "")
	raw = strings.ReplaceAll(raw, " ", "")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil
	}
	return decoded
}

func extensionManifestHasStableKey(extDir, expectedID string) bool {
	expectedID = strings.ToLower(strings.TrimSpace(expectedID))
	if !isWebStoreExtensionID(expectedID) {
		return true
	}
	publicKey := readManifestPublicKey(extDir)
	if len(publicKey) == 0 {
		return false
	}
	return strings.EqualFold(extensionIDFromPublicKey(publicKey), expectedID)
}

// ensureManifestPublicKey writes the CRX public key into manifest.json so
// --load-extension keeps the official chrome-extension:// ID. Wallet vaults
// live under Local Extension Settings/<id>/; a path-derived ID makes the page
// provider unable to read that vault even though the files still exist.
func ensureManifestPublicKey(extDir string, publicKey []byte) error {
	if strings.TrimSpace(extDir) == "" || len(publicKey) == 0 {
		return nil
	}
	manifestPath := filepath.Join(extDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("读取扩展 manifest.json 失败：%w", err)
	}
	if len(data) > 2*1024*1024 {
		return fmt.Errorf("扩展 manifest.json 体积异常")
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("扩展 manifest.json 格式无效：%w", err)
	}
	keyB64 := base64.StdEncoding.EncodeToString(publicKey)
	if existing, _ := manifest["key"].(string); strings.TrimSpace(existing) == keyB64 {
		return nil
	}
	// If an existing key already yields the same extension ID, leave it alone.
	if existingKey := readManifestPublicKey(extDir); len(existingKey) > 0 {
		if strings.EqualFold(extensionIDFromPublicKey(existingKey), extensionIDFromPublicKey(publicKey)) {
			return nil
		}
	}
	manifest["key"] = keyB64
	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("生成扩展 manifest.json 失败：%w", err)
	}
	out = append(out, '\n')
	if err := fsutil.WriteFileAtomic(manifestPath, out, 0644); err != nil {
		return fmt.Errorf("写入扩展 manifest.json 失败：%w", err)
	}
	return nil
}

func (a *App) tryRepairExtensionManifestKey(extDir, extID, downloadAddress string) bool {
	extID = strings.ToLower(strings.TrimSpace(extID))
	if a == nil || !isWebStoreExtensionID(extID) || validateUnpackedExtensionManifest(extDir) != nil {
		return false
	}
	if extensionManifestHasStableKey(extDir, extID) {
		return true
	}
	downloadURL := resolveExtensionDownloadURL(downloadAddress, extID)
	if validateExtensionDownloadURL(downloadURL) != nil {
		return false
	}
	// Key repair is best-effort metadata recovery. Keep it short so offline
	// machines and unit tests never block environment start or distribution.
	payload, err := downloadExtensionPayloadWithTimeout(downloadURL, 6*time.Second)
	if err != nil {
		return false
	}
	_, publicKey, err := extractZipAndPublicKeyFromCRX(payload)
	if err != nil || len(publicKey) == 0 {
		return false
	}
	if !strings.EqualFold(extensionIDFromPublicKey(publicKey), extID) {
		return false
	}
	if err := ensureManifestPublicKey(extDir, publicKey); err != nil {
		return false
	}
	return extensionManifestHasStableKey(extDir, extID)
}

// repairLoadExtensionStableIDs ensures every --load-extension package that is
// stored under a Web Store ID folder has a matching manifest key before Chrome
// starts. This is a one-shot package metadata repair: it never reads profile
// Cookies, Local Extension Settings, or wallet vaults.
func (a *App) repairLoadExtensionStableIDs(args []string) {
	if a == nil {
		return
	}
	log := logger.New("Extension")
	for _, dir := range activeLoadExtensionDirs(args) {
		extDir := strings.TrimSpace(dir)
		if extDir == "" || validateUnpackedExtensionManifest(extDir) != nil {
			continue
		}
		expectedID := strings.ToLower(filepath.Base(extDir))
		if !isWebStoreExtensionID(expectedID) {
			continue
		}
		if extensionManifestHasStableKey(extDir, expectedID) {
			continue
		}
		if a.tryRepairExtensionManifestKey(extDir, expectedID, expectedID) {
			log.Info("已修复扩展稳定 ID（写入 manifest key）",
				logger.F("extension_id", expectedID),
				logger.F("extension_dir", extDir),
			)
			continue
		}
		log.Warn("扩展缺少稳定 ID 公钥，网页可能无法读取钱包数据",
			logger.F("extension_id", expectedID),
			logger.F("extension_dir", extDir),
		)
	}
}

func unzipBytes(data []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("扩展 ZIP 解析失败：%w", err)
	}
	const maxExtensionFiles = 20000
	const maxUnpackedExtensionBytes = 1024 * 1024 * 1024
	if len(zr.File) > maxExtensionFiles {
		return fmt.Errorf("扩展 ZIP 文件数量过多")
	}
	var unpackedBytes uint64
	for _, f := range zr.File {
		unpackedBytes += f.UncompressedSize64
		if unpackedBytes > maxUnpackedExtensionBytes {
			return fmt.Errorf("扩展 ZIP 解压后体积过大")
		}
	}
	cleanDest, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cleanDest, 0755); err != nil {
		return err
	}
	for _, f := range zr.File {
		name := filepath.Clean(f.Name)
		if name == "." || strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			continue
		}
		target := filepath.Join(cleanDest, name)
		absTarget, err := filepath.Abs(target)
		if err != nil || !strings.HasPrefix(absTarget, cleanDest+string(os.PathSeparator)) && absTarget != cleanDest {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(absTarget, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(absTarget), 0755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(absTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, copyErr := io.Copy(out, rc)
		closeErr := out.Close()
		rc.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func installUnpackedExtension(appRoot string, extID string, zipPayload []byte, publicKey []byte) (string, string, string, error) {
	parent := filepath.Join(appRoot, "extensions", "imported")
	if err := os.MkdirAll(parent, 0755); err != nil {
		return "", "", "", fmt.Errorf("创建扩展目录失败：%w", err)
	}
	extDir := filepath.Join(parent, safePathName(extID))
	stageDir, err := os.MkdirTemp(parent, ".installing-")
	if err != nil {
		return "", "", "", fmt.Errorf("创建扩展暂存目录失败：%w", err)
	}
	defer os.RemoveAll(stageDir)
	if err := unzipBytes(zipPayload, stageDir); err != nil {
		return "", "", "", err
	}
	if err := validateUnpackedExtensionManifest(stageDir); err != nil {
		return "", "", "", err
	}
	// Inject the CRX public key before the package becomes live so the first
	// Chrome launch already uses the official extension ID.
	if len(publicKey) > 0 {
		if err := ensureManifestPublicKey(stageDir, publicKey); err != nil {
			return "", "", "", err
		}
		if expected := strings.ToLower(strings.TrimSpace(extID)); isWebStoreExtensionID(expected) {
			if !extensionManifestHasStableKey(stageDir, expected) {
				return "", "", "", fmt.Errorf("扩展公钥与声明的 ID 不一致：expected=%s derived=%s", expected, extensionIDFromPublicKey(publicKey))
			}
		}
	}
	newVersion := readManifestVersionFromDir(stageDir)
	previousVersion := readManifestVersionFromDir(extDir)

	backupDir := extDir + ".previous"
	displacedBackupDir := ""
	if _, statErr := os.Stat(backupDir); statErr == nil {
		displacedBackupDir = filepath.Join(parent, fmt.Sprintf(".previous-%s-%d", safePathName(extID), time.Now().UnixNano()))
		if err := os.Rename(backupDir, displacedBackupDir); err != nil {
			return "", "", "", fmt.Errorf("暂存旧扩展回滚包失败：%w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return "", "", "", fmt.Errorf("检查扩展回滚包失败：%w", statErr)
	}
	restoreDisplacedBackup := func() error {
		if displacedBackupDir == "" {
			return nil
		}
		if _, err := os.Stat(backupDir); err == nil {
			return fmt.Errorf("无法恢复旧扩展回滚包：目标已存在 %s", backupDir)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(displacedBackupDir, backupDir); err != nil {
			return fmt.Errorf("恢复旧扩展回滚包失败：%w", err)
		}
		displacedBackupDir = ""
		return nil
	}
	movedCurrent := false
	if _, statErr := os.Stat(extDir); statErr == nil {
		if err := os.Rename(extDir, backupDir); err != nil {
			return "", "", "", errors.Join(
				fmt.Errorf("替换旧扩展目录失败：%w", err),
				restoreDisplacedBackup(),
			)
		}
		movedCurrent = true
	} else if !os.IsNotExist(statErr) {
		return "", "", "", errors.Join(
			fmt.Errorf("检查旧扩展目录失败：%w", statErr),
			restoreDisplacedBackup(),
		)
	}
	if err := os.Rename(stageDir, extDir); err != nil {
		var rollbackErr error
		if movedCurrent {
			if restoreErr := os.Rename(backupDir, extDir); restoreErr != nil {
				rollbackErr = fmt.Errorf("恢复原扩展目录失败，原包仍位于 %s：%w", backupDir, restoreErr)
			}
		}
		if rollbackErr == nil {
			rollbackErr = restoreDisplacedBackup()
		}
		return "", "", "", errors.Join(
			fmt.Errorf("安装扩展目录失败：%w", err),
			rollbackErr,
		)
	}
	if displacedBackupDir != "" {
		if err := os.RemoveAll(displacedBackupDir); err != nil {
			return "", "", "", fmt.Errorf("扩展已安装，但清理更早的程序包备份失败：%w", err)
		}
	}
	// Keep one program-package rollback. Wallet secrets/state are never copied
	// here: they remain inside each profile's Chrome user-data directory.
	return extDir, previousVersion, newVersion, nil
}

func readManifestVersionFromDir(extDir string) string {
	data, err := os.ReadFile(filepath.Join(extDir, "manifest.json"))
	if err != nil || len(data) > 2*1024*1024 {
		return ""
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return strings.TrimSpace(manifest.Version)
}

func validateUnpackedExtensionManifest(extDir string) error {
	manifestFile, err := os.Open(filepath.Join(extDir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("扩展解包失败：未找到 manifest.json")
	}
	defer manifestFile.Close()
	data, err := io.ReadAll(io.LimitReader(manifestFile, 2*1024*1024+1))
	if err != nil {
		return fmt.Errorf("读取扩展 manifest.json 失败：%w", err)
	}
	if len(data) > 2*1024*1024 {
		return fmt.Errorf("扩展 manifest.json 体积异常")
	}
	var manifest struct {
		ManifestVersion int    `json:"manifest_version"`
		Name            string `json:"name"`
		Version         string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("扩展 manifest.json 格式无效：%w", err)
	}
	if manifest.ManifestVersion != 2 && manifest.ManifestVersion != 3 {
		return fmt.Errorf("扩展 manifest_version 不受支持：%d", manifest.ManifestVersion)
	}
	if strings.TrimSpace(manifest.Name) == "" || strings.TrimSpace(manifest.Version) == "" {
		return fmt.Errorf("扩展 manifest.json 缺少 name 或 version")
	}
	return nil
}

func (a *App) bindExtensionDirToProfiles(profileIds []string, extDir string) (*extensionBindResult, error) {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	type previousProfileState struct {
		launchArgs []string
		updatedAt  string
	}
	previous := make(map[string]previousProfileState, len(profileIds))
	result := &extensionBindResult{
		UpdatedProfiles: make([]string, 0, len(profileIds)),
	}
	// Materialize package into each stopped profile (Default/Extensions/…) and
	// bind LaunchArgs to that profile-local path so --load-extension always
	// points at a path Chromium can open. Shared program package remains the
	// source of truth for copies; running profiles only get shared LaunchArgs
	// until next cold start.
	type pendingInstall struct {
		profileID   string
		userDataDir string
	}
	pending := make([]pendingInstall, 0, len(profileIds))
	for _, id := range profileIds {
		profile, ok := a.browserMgr.Profiles[id]
		if !ok || profile == nil {
			continue
		}
		previous[id] = previousProfileState{
			launchArgs: append([]string{}, profile.LaunchArgs...),
			updatedAt:  profile.UpdatedAt,
		}
		userDataDir := a.browserMgr.ResolveUserDataDir(profile)
		bindPath := extDir
		if !profile.Running {
			if local, _, _, mErr := materializeExtensionPackageForProfile(userDataDir, extDir); mErr == nil && local != "" {
				bindPath = local
			}
		}
		// LaunchArgs record which package path this environment must load.
		profile.LaunchArgs = addExtensionDirToLaunchArgs(profile.LaunchArgs, bindPath)
		// Always keep shared path too so heal can re-copy after upgrade moves appRoot.
		if bindPath != extDir {
			profile.LaunchArgs = addExtensionDirToLaunchArgs(profile.LaunchArgs, extDir)
		}
		profile.UpdatedAt = time.Now().Format(time.RFC3339)
		result.UpdatedProfiles = append(result.UpdatedProfiles, id)
		if profile.Running {
			result.DeferredRunning++
			continue
		}
		pending = append(pending, pendingInstall{
			profileID:   id,
			userDataDir: userDataDir,
		})
	}
	if len(result.UpdatedProfiles) == 0 {
		return nil, fmt.Errorf("未找到可更新的实例")
	}
	if err := a.browserMgr.SaveProfiles(); err != nil {
		for id, state := range previous {
			if profile := a.browserMgr.Profiles[id]; profile != nil {
				profile.LaunchArgs = state.launchArgs
				profile.UpdatedAt = state.updatedAt
			}
		}
		return nil, fmt.Errorf("保存实例扩展配置失败：%w", err)
	}
	// Force next start to re-verify extension packages ↔ profile data.
	a.clearExtensionLaunchReadyForProfilesLocked(result.UpdatedProfiles)
	a.clearExtensionIntegrityForProfilesLocked(result.UpdatedProfiles)
	// Materialize while mutex held but Chrome is stopped for these profiles.
	log := logger.New("Extension")
	for _, item := range pending {
		if err := os.MkdirAll(item.userDataDir, 0755); err != nil {
			log.Warn("扩展 Profile 安装失败：无法创建用户目录",
				logger.F("profile_id", item.profileID),
				logger.F("error", err.Error()),
			)
			result.FailedPrefs = append(result.FailedPrefs, item.profileID)
			continue
		}
		localPath, _, _, matErr := materializeExtensionPackageForProfile(item.userDataDir, extDir)
		if matErr != nil {
			log.Warn("扩展物化进环境目录失败，尝试仅写 Preferences",
				logger.F("profile_id", item.profileID),
				logger.F("error", matErr.Error()),
			)
			localPath = extDir
		}
		if err := installUnpackedExtensionIntoProfile(item.userDataDir, extDir); err != nil {
			log.Warn("分配时 Profile 安装失败，启动时将 heal/CLI 兜底（不触碰 LES）",
				logger.F("profile_id", item.profileID),
				logger.F("error", err.Error()),
			)
			result.FailedPrefs = append(result.FailedPrefs, item.profileID)
			continue
		}
		// Success = Preferences path is loadable for the materialised (or shared) package.
		checkPath := localPath
		if checkPath == "" {
			checkPath = extDir
		}
		if !isExtensionInstalledInProfile(item.userDataDir, checkPath) && !isExtensionInstalledInProfile(item.userDataDir, extDir) {
			log.Warn("分配后 Preferences 仍不可加载，启动时将 heal",
				logger.F("profile_id", item.profileID),
			)
			result.FailedPrefs = append(result.FailedPrefs, item.profileID)
			continue
		}
		result.PrefsInstalled++
		log.Info("分配时已将扩展物化并写入环境 Profile（首次打开强制 CLI 加载）",
			logger.F("profile_id", item.profileID),
			logger.F("extension_id", resolveExtensionPackageID(extDir)),
			logger.F("local_path", checkPath),
		)
	}
	// Success policy: at least one stopped environment must have loadable Preferences.
	// All-running → user must stop browsers first. Prefs all failed → hard error.
	if len(pending) == 0 && result.DeferredRunning > 0 && result.PrefsInstalled == 0 {
		return result, fmt.Errorf("所选环境均在运行中，无法写入扩展 Profile。请先关闭这些环境后再点「分配」（否则浏览器内看不到扩展）")
	}
	if len(pending) > 0 && result.PrefsInstalled == 0 {
		return result, fmt.Errorf("扩展未能写入任何环境的 Preferences（失败 %d 个）。请检查扩展包是否完整、环境目录是否可写后重试", len(result.FailedPrefs))
	}
	if len(result.FailedPrefs) > 0 && result.PrefsInstalled == 0 {
		return result, fmt.Errorf("扩展 Preferences 写入全部失败（%d 个环境）", len(result.FailedPrefs))
	}
	return result, nil
}

func (a *App) removeExtensionDirFromProfilesExcept(extDir string, keepProfiles map[string]bool) ([]string, error) {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	type previousProfileState struct {
		launchArgs []string
		updatedAt  string
	}
	previous := make(map[string]previousProfileState)
	updated := make([]string, 0, len(a.browserMgr.Profiles))
	for id, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}
		if keepProfiles[id] {
			continue
		}
		nextArgs, changed := removeExtensionDirFromLaunchArgs(profile.LaunchArgs, extDir)
		if !changed {
			continue
		}
		previous[id] = previousProfileState{
			launchArgs: append([]string{}, profile.LaunchArgs...),
			updatedAt:  profile.UpdatedAt,
		}
		profile.LaunchArgs = nextArgs
		profile.UpdatedAt = time.Now().Format(time.RFC3339)
		updated = append(updated, id)
	}
	if len(updated) == 0 {
		return updated, nil
	}
	if err := a.browserMgr.SaveProfiles(); err != nil {
		for id, state := range previous {
			if profile := a.browserMgr.Profiles[id]; profile != nil {
				profile.LaunchArgs = state.launchArgs
				profile.UpdatedAt = state.updatedAt
			}
		}
		return nil, fmt.Errorf("保存全局扩展配置失败：%w", err)
	}
	a.clearExtensionLaunchReadyForProfilesLocked(updated)
	toDisable := make([]string, 0, len(updated))
	for _, id := range updated {
		if p := a.browserMgr.Profiles[id]; p != nil && !p.Running {
			toDisable = append(toDisable, id)
		}
	}
	// Must disable after releasing Mutex (helper takes the lock).
	extDirCopy := extDir
	idsCopy := append([]string{}, toDisable...)
	// Caller holds lock; schedule disable after return via defer-like pattern:
	// we unlock in defer of this function — call disable without holding lock
	// by unlocking early is unsafe. Collect paths while locked instead.
	type disableJob struct {
		userDataDir string
	}
	jobs := make([]disableJob, 0, len(idsCopy))
	for _, id := range idsCopy {
		if p := a.browserMgr.Profiles[id]; p != nil {
			jobs = append(jobs, disableJob{userDataDir: a.browserMgr.ResolveUserDataDir(p)})
		}
	}
	// Unlock happens when function returns; run disable after unlock using
	// a deferred call that runs while... still locked. So disable inline
	// using pre-resolved paths (no lock needed).
	for _, job := range jobs {
		if job.userDataDir == "" {
			continue
		}
		_ = disableExtensionInProfile(job.userDataDir, extDirCopy)
	}
	return updated, nil
}

func (a *App) globalExtensionRegistryPath() string {
	return a.resolveAppPath(filepath.Join("data", globalExtensionRegistryFilename))
}

func (a *App) profileExtensionRegistryPath() string {
	return a.resolveAppPath(filepath.Join("data", profileExtensionRegistryFilename))
}

func (a *App) globalExtensionDir(extensionID string) string {
	return filepath.Join(a.appRoot, "extensions", "imported", safePathName(strings.TrimSpace(extensionID)))
}

func (a *App) loadGlobalExtensionRegistry() (globalExtensionRegistry, error) {
	registry := globalExtensionRegistry{Extensions: []globalExtensionRegistryEntry{}}
	data, err := os.ReadFile(a.globalExtensionRegistryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return registry, nil
		}
		return registry, fmt.Errorf("读取全局扩展配置失败：%w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return registry, nil
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return registry, fmt.Errorf("解析全局扩展配置失败：%w", err)
	}
	registry.Extensions = normalizeGlobalExtensionRegistryEntries(registry.Extensions)
	return registry, nil
}

func (a *App) saveGlobalExtensionRegistry(registry globalExtensionRegistry) error {
	registry.Extensions = normalizeGlobalExtensionRegistryEntries(registry.Extensions)
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化全局扩展配置失败：%w", err)
	}
	path := a.globalExtensionRegistryPath()
	if err := fsutil.WriteFileAtomic(path, data, 0600); err != nil {
		return fmt.Errorf("保存全局扩展配置失败：%w", err)
	}
	return nil
}

func (a *App) loadProfileExtensionRegistry() (profileExtensionRegistry, error) {
	registry := profileExtensionRegistry{Extensions: []profileExtensionRegistryEntry{}}
	data, err := os.ReadFile(a.profileExtensionRegistryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return registry, nil
		}
		return registry, fmt.Errorf("读取环境扩展分配配置失败：%w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return registry, nil
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return registry, fmt.Errorf("解析环境扩展分配配置失败：%w", err)
	}
	registry.Extensions = normalizeProfileExtensionRegistryEntries(registry.Extensions)
	return registry, nil
}

func (a *App) saveProfileExtensionRegistry(registry profileExtensionRegistry) error {
	registry.Extensions = normalizeProfileExtensionRegistryEntries(registry.Extensions)
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化环境扩展分配配置失败：%w", err)
	}
	path := a.profileExtensionRegistryPath()
	if err := fsutil.WriteFileAtomic(path, data, 0600); err != nil {
		return fmt.Errorf("保存环境扩展分配配置失败：%w", err)
	}
	return nil
}

func normalizeProfileExtensionRegistryEntries(entries []profileExtensionRegistryEntry) []profileExtensionRegistryEntry {
	out := make([]profileExtensionRegistryEntry, 0, len(entries))
	indexByID := map[string]int{}
	for _, entry := range entries {
		entry.DownloadAddress = strings.TrimSpace(entry.DownloadAddress)
		entry.ExtensionID = strings.TrimSpace(entry.ExtensionID)
		entry.ProfileIDs = normalizeProfileIDs(entry.ProfileIDs)
		if entry.ExtensionID == "" || len(entry.ProfileIDs) == 0 {
			continue
		}
		key := strings.ToLower(entry.ExtensionID)
		if index, ok := indexByID[key]; ok {
			merged := append(out[index].ProfileIDs, entry.ProfileIDs...)
			out[index].ProfileIDs = normalizeProfileIDs(merged)
			if entry.DownloadAddress != "" {
				out[index].DownloadAddress = entry.DownloadAddress
			}
			continue
		}
		indexByID[key] = len(out)
		out = append(out, entry)
	}
	return out
}

func upsertProfileExtensionAssignments(entries []profileExtensionRegistryEntry, want profileExtensionRegistryEntry) []profileExtensionRegistryEntry {
	want.DownloadAddress = strings.TrimSpace(want.DownloadAddress)
	want.ExtensionID = strings.TrimSpace(want.ExtensionID)
	want.ProfileIDs = normalizeProfileIDs(want.ProfileIDs)
	if want.ExtensionID == "" || len(want.ProfileIDs) == 0 {
		return normalizeProfileExtensionRegistryEntries(entries)
	}
	merged := append([]profileExtensionRegistryEntry{}, entries...)
	merged = append(merged, want)
	return normalizeProfileExtensionRegistryEntries(merged)
}

func removeProfileExtensionAssignments(entries []profileExtensionRegistryEntry, extensionID string, profileIDs []string) ([]profileExtensionRegistryEntry, bool) {
	removeIDs := map[string]bool{}
	for _, id := range normalizeProfileIDs(profileIDs) {
		removeIDs[id] = true
	}
	changed := false
	out := make([]profileExtensionRegistryEntry, 0, len(entries))
	for _, entry := range normalizeProfileExtensionRegistryEntries(entries) {
		if !strings.EqualFold(entry.ExtensionID, strings.TrimSpace(extensionID)) {
			out = append(out, entry)
			continue
		}
		kept := make([]string, 0, len(entry.ProfileIDs))
		for _, id := range entry.ProfileIDs {
			if removeIDs[id] {
				changed = true
				continue
			}
			kept = append(kept, id)
		}
		if len(kept) > 0 {
			entry.ProfileIDs = kept
			out = append(out, entry)
		}
	}
	return out, changed
}

func (a *App) removeDeletedProfileExtensionReferences(profileID string) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil
	}
	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()

	assignments, err := a.loadProfileExtensionRegistry()
	if err != nil {
		return err
	}
	assignmentChanged := false
	nextAssignments := make([]profileExtensionRegistryEntry, 0, len(assignments.Extensions))
	for _, entry := range normalizeProfileExtensionRegistryEntries(assignments.Extensions) {
		kept := make([]string, 0, len(entry.ProfileIDs))
		for _, id := range entry.ProfileIDs {
			if id == profileID {
				assignmentChanged = true
				continue
			}
			kept = append(kept, id)
		}
		if len(kept) > 0 {
			entry.ProfileIDs = kept
			nextAssignments = append(nextAssignments, entry)
		}
	}
	if assignmentChanged {
		assignments.Extensions = nextAssignments
		if err := a.saveProfileExtensionRegistry(assignments); err != nil {
			return err
		}
	}

	global, err := a.loadGlobalExtensionRegistry()
	if err != nil {
		return err
	}
	globalChanged := false
	for index := range global.Extensions {
		kept := make([]string, 0, len(global.Extensions[index].ProfileIDs))
		for _, id := range global.Extensions[index].ProfileIDs {
			if id == profileID {
				globalChanged = true
				continue
			}
			kept = append(kept, id)
		}
		global.Extensions[index].ProfileIDs = kept
	}
	if globalChanged {
		if err := a.saveGlobalExtensionRegistry(global); err != nil {
			return err
		}
	}
	return nil
}

func profileExtensionAssigned(entries []profileExtensionRegistryEntry, extensionID string) bool {
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.ExtensionID), strings.TrimSpace(extensionID)) && len(entry.ProfileIDs) > 0 {
			return true
		}
	}
	return false
}

func assignedProfilesForExtension(entries []profileExtensionRegistryEntry, extensionID string) map[string]bool {
	out := map[string]bool{}
	for _, entry := range entries {
		if !strings.EqualFold(strings.TrimSpace(entry.ExtensionID), strings.TrimSpace(extensionID)) {
			continue
		}
		for _, id := range entry.ProfileIDs {
			out[id] = true
		}
	}
	return out
}

func (a *App) globalExtensionRegistered(extensionID string) bool {
	registry, err := a.loadGlobalExtensionRegistry()
	if err != nil {
		return false
	}
	for _, entry := range registry.Extensions {
		if strings.EqualFold(strings.TrimSpace(entry.ExtensionID), strings.TrimSpace(extensionID)) {
			return true
		}
	}
	return false
}

func normalizeGlobalExtensionRegistryEntries(entries []globalExtensionRegistryEntry) []globalExtensionRegistryEntry {
	out := make([]globalExtensionRegistryEntry, 0, len(entries))
	seen := map[string]bool{}
	for _, entry := range entries {
		entry.DownloadAddress = strings.TrimSpace(entry.DownloadAddress)
		entry.ExtensionID = strings.TrimSpace(entry.ExtensionID)
		entry.ProfileIDs = normalizeProfileIDs(entry.ProfileIDs)
		if entry.ExtensionID == "" {
			continue
		}
		key := strings.ToLower(entry.ExtensionID)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, entry)
	}
	return out
}

func globalExtensionCompletedProfiles(entries []globalExtensionRegistryEntry, extensionID string, downloadAddress string) map[string]bool {
	out := map[string]bool{}
	key := extensionSourceKey(downloadAddress)
	for _, entry := range normalizeGlobalExtensionRegistryEntries(entries) {
		sameID := extensionID != "" && strings.EqualFold(entry.ExtensionID, extensionID)
		sameSource := key != "" && extensionSourceKey(entry.DownloadAddress) == key
		if !sameID && !sameSource {
			continue
		}
		for _, profileID := range entry.ProfileIDs {
			out[profileID] = true
		}
	}
	return out
}

func upsertGlobalExtensionRegistryEntry(entries []globalExtensionRegistryEntry, want globalExtensionRegistryEntry) []globalExtensionRegistryEntry {
	want.DownloadAddress = strings.TrimSpace(want.DownloadAddress)
	want.ExtensionID = strings.TrimSpace(want.ExtensionID)
	replaced := false
	out := make([]globalExtensionRegistryEntry, 0, len(entries)+1)
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.ExtensionID), want.ExtensionID) {
			if !replaced {
				out = append(out, want)
				replaced = true
			}
			continue
		}
		out = append(out, entry)
	}
	if !replaced {
		out = append(out, want)
	}
	return normalizeGlobalExtensionRegistryEntries(out)
}

func extensionManifestExists(extDir string) bool {
	info, err := os.Stat(filepath.Join(extDir, "manifest.json"))
	return err == nil && !info.IsDir()
}

func preserveAssignedExtensionArgs(existing []string, requested []string) []string {
	out := append([]string{}, requested...)
	for _, dir := range activeLoadExtensionDirs(existing) {
		out = addExtensionDirToLaunchArgs(out, dir)
	}
	if len(activeLoadExtensionDirs(out)) > 0 {
		out = removeExtensionBlockingLaunchArgs(out)
	}
	return normalizeLoadExtensionArgs(out)
}

func removeExtensionBlockingLaunchArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		lower := strings.ToLower(trimmed)
		if strings.EqualFold(trimmed, "--disable-extensions") ||
			strings.HasPrefix(lower, "--disable-extensions=") ||
			strings.HasPrefix(lower, "--disable-extensions-except=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// readManifestNameFromDir 从已解包的扩展目录里读 manifest.name（用于 toast 提示）。
func readManifestNameFromDir(extDir string) string {
	data, err := os.ReadFile(filepath.Join(extDir, "manifest.json"))
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	if name, _ := m["name"].(string); name != "" {
		return strings.TrimSpace(name)
	}
	return ""
}

// InstallExtensionFromCRXURL 实现 launchcode.ExtensionInstaller。
// helper 扩展走 LaunchServer 调进来：拉 .crx → 解包 → 绑定到指定 profile。
// 返回的 extName 给 helper 弹 toast 用，失败时也保证 extID 至少能给个 fallback。
func (a *App) InstallExtensionFromCRXURL(profileID string, crxURL string) (string, string, error) {
	if a == nil || a.browserMgr == nil {
		return "", "", fmt.Errorf("浏览器管理器未初始化")
	}
	profileID = strings.TrimSpace(profileID)
	crxURL = strings.TrimSpace(crxURL)
	if profileID == "" {
		return "", "", fmt.Errorf("profileId 为空")
	}
	if crxURL == "" {
		return "", "", fmt.Errorf("crxUrl 为空")
	}

	a.maintenanceMu.Lock()
	defer a.maintenanceMu.Unlock()

	extID := extractExtensionID(crxURL)
	if err := a.enableExtensionDeveloperModeForProfile(profileID); err != nil {
		return extID, "", err
	}
	if extID != "" && len(a.filterProfilesMissingEquivalentExtension([]string{profileID}, extID, "")) == 0 {
		return extID, "", nil
	}
	extID, extDir, _, _, err := a.downloadAndInstallExtension(crxURL)
	if err != nil {
		return extID, "", err
	}
	extName := readManifestNameFromDir(extDir)
	if len(a.filterProfilesMissingEquivalentExtension([]string{profileID}, extID, extName)) == 0 {
		return extID, extName, nil
	}
	if _, err := a.bindExtensionDirToProfiles([]string{profileID}, extDir); err != nil { // bind result unused: CRX helper only needs error
		return extID, "", err
	}
	assignments, err := a.loadProfileExtensionRegistry()
	if err != nil {
		return extID, "", err
	}
	assignments.Extensions = upsertProfileExtensionAssignments(assignments.Extensions, profileExtensionRegistryEntry{
		DownloadAddress: crxURL,
		ExtensionID:     extID,
		ProfileIDs:      []string{profileID},
	})
	if err := a.saveProfileExtensionRegistry(assignments); err != nil {
		return extID, "", err
	}
	return extID, extName, nil
}

func addExtensionDirToLaunchArgs(args []string, extDir string) []string {
	args = removeExtensionBlockingLaunchArgs(append([]string{}, args...))
	return normalizeLoadExtensionArgs(append(args, "--load-extension="+extDir))
}

func removeExtensionDirFromLaunchArgs(args []string, extDir string) ([]string, bool) {
	target := normalizeExtensionPath(extDir)
	if target == "" {
		return append([]string{}, args...), false
	}
	out := make([]string, 0, len(args))
	changed := false
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if !strings.HasPrefix(trimmed, "--load-extension=") {
			out = append(out, arg)
			continue
		}
		kept := make([]string, 0)
		for _, part := range strings.Split(strings.TrimSpace(strings.TrimPrefix(trimmed, "--load-extension=")), ",") {
			part = strings.TrimSpace(strings.Trim(part, `"`))
			if part == "" {
				continue
			}
			if normalizeExtensionPath(part) == target {
				changed = true
				continue
			}
			kept = append(kept, part)
		}
		if len(kept) > 0 {
			out = append(out, "--load-extension="+strings.Join(kept, ","))
		}
	}
	return normalizeLoadExtensionArgs(out), changed
}

func hasExtensionDirInLaunchArgs(args []string, extDir string) bool {
	target := normalizeExtensionPath(extDir)
	if target == "" {
		return false
	}
	for _, part := range activeLoadExtensionDirs(args) {
		if normalizeExtensionPath(part) == target {
			return true
		}
	}
	return false
}

func normalizeLoadExtensionArgs(args []string) []string {
	out := make([]string, 0, len(args))
	exts := []string{}
	seen := map[string]bool{}
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if strings.HasPrefix(trimmed, "--load-extension=") {
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "--load-extension="))
			for _, part := range strings.Split(value, ",") {
				part = strings.TrimSpace(part)
				if part == "" || seen[part] {
					continue
				}
				seen[part] = true
				exts = append(exts, part)
			}
			continue
		}
		out = append(out, arg)
	}
	if len(exts) > 0 {
		out = append(out, "--load-extension="+strings.Join(exts, ","))
	}
	return out
}

func activeLoadExtensionDirs(args []string) map[string]string {
	out := map[string]string{}
	const prefix = "--load-extension="
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if !strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			continue
		}
		value := strings.TrimSpace(trimmed[len(prefix):])
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(strings.Trim(part, `"`))
			if part == "" {
				continue
			}
			out[normalizeExtensionPath(part)] = part
		}
	}
	return out
}

func chromeProfilePreferencePaths(userDataDir string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(path string) {
		clean := filepath.Clean(path)
		key := strings.ToLower(clean)
		if !seen[key] {
			seen[key] = true
			out = append(out, clean)
		}
	}
	add(filepath.Join(userDataDir, "Preferences"))
	add(filepath.Join(userDataDir, "Default", "Preferences"))
	if entries, err := os.ReadDir(userDataDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if name == "Default" || strings.HasPrefix(name, "Profile ") || name == "Guest Profile" {
				add(filepath.Join(userDataDir, name, "Preferences"))
			}
		}
	}
	return out
}

func (a *App) enableExtensionDeveloperModeForProfile(profileID string) error {
	a.browserMgr.Mutex.Lock()
	profile, exists := a.browserMgr.Profiles[profileID]
	if !exists || profile == nil {
		a.browserMgr.Mutex.Unlock()
		return fmt.Errorf("实例不存在：%s", profileID)
	}
	running := profile.Running
	if cmd := a.browserMgr.BrowserProcesses[profileID]; cmd != nil && cmd.Process != nil {
		running = true
	}
	snapshot := *profile
	a.browserMgr.Mutex.Unlock()
	if running {
		// Chrome owns Preferences while the environment is live. Writing a
		// stale read-modify-write snapshot here could discard extension
		// metadata that Chrome just persisted. --load-extension remains the
		// launch authority, so skip this optional UI preference.
		return nil
	}
	return enableExtensionDeveloperMode(a.browserMgr.ResolveUserDataDir(&snapshot))
}

// enableExtensionDeveloperMode runs only when the user explicitly distributes
// an extension to an environment that does not already contain it.
func enableExtensionDeveloperMode(userDataDir string) error {
	if strings.TrimSpace(userDataDir) == "" {
		return fmt.Errorf("用户数据目录为空")
	}
	defaultPrefs := filepath.Join(userDataDir, "Default", "Preferences")
	if err := ensureChromePreferencesFile(defaultPrefs); err != nil {
		return fmt.Errorf("创建扩展首选项失败：%w", err)
	}
	for _, prefPath := range chromeProfilePreferencePaths(userDataDir) {
		data, err := os.ReadFile(prefPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("读取扩展首选项失败 %s：%w", prefPath, err)
		}
		prefs := map[string]any{}
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &prefs); err != nil {
				return fmt.Errorf("扩展首选项格式无效 %s：%w", prefPath, err)
			}
		}
		extensions, _ := prefs["extensions"].(map[string]any)
		if extensions == nil {
			extensions = map[string]any{}
			prefs["extensions"] = extensions
		}
		ui, _ := extensions["ui"].(map[string]any)
		if ui == nil {
			ui = map[string]any{}
			extensions["ui"] = ui
		}
		if ui["developer_mode"] == true {
			continue
		}
		ui["developer_mode"] = true
		out, err := json.MarshalIndent(prefs, "", "   ")
		if err != nil {
			return fmt.Errorf("生成扩展首选项失败 %s：%w", prefPath, err)
		}
		if err := fsutil.WriteFileAtomic(prefPath, out, 0644); err != nil {
			return fmt.Errorf("保存扩展首选项失败 %s：%w", prefPath, err)
		}
	}
	return nil
}

func normalizeExtensionPath(path string) string {
	path = strings.TrimSpace(strings.Trim(path, `"`))
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return strings.ToLower(filepath.Clean(path))
}

func extensionSettingManifestName(setting map[string]any) string {
	manifest, ok := setting["manifest"].(map[string]any)
	if !ok {
		return ""
	}
	name, _ := manifest["name"].(string)
	return strings.TrimSpace(strings.ToLower(name))
}

func safePathName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "extension"
	}
	return b.String()
}
