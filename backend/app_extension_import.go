package backend

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
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
	Message          string   `json:"message"`
}

// GlobalManagedExtension is the backend-authoritative global extension policy.
// Extension directories are resolved from ExtensionID at runtime so an installed
// application can be moved without leaving stale absolute paths in the registry.
type GlobalManagedExtension struct {
	DownloadAddress string `json:"downloadAddress"`
	ExtensionID     string `json:"extensionId"`
	ExtensionDir    string `json:"extensionDir"`
	Installed       bool   `json:"installed"`
}

type globalExtensionRegistry struct {
	Extensions []globalExtensionRegistryEntry `json:"extensions"`
}

type globalExtensionRegistryEntry struct {
	DownloadAddress string `json:"downloadAddress"`
	ExtensionID     string `json:"extensionId"`
}

// profileExtensionRegistry is the backend-authoritative manual assignment map.
// Do not rely only on Profile.LaunchArgs: profile editing, legacy recovery and
// migrations legitimately replace that slice. The registry lets every launch
// reconstruct the current extension arguments from stable profile IDs.
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
	updated := make([]string, 0, len(profileIds))
	for _, id := range profileIds {
		profile, ok := a.browserMgr.Profiles[id]
		if !ok || profile == nil {
			continue
		}
		nextArgs, changed := removeExtensionDirFromLaunchArgs(profile.LaunchArgs, extDir)
		if changed {
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
		a.browserMgr.Mutex.Unlock()
		return nil, fmt.Errorf("保存实例扩展配置失败：%w", err)
	}
	a.browserMgr.Mutex.Unlock()

	if !stillReferenced && !a.globalExtensionRegistered(extID) {
		_ = os.RemoveAll(extDir)
		_ = os.RemoveAll(extDir + ".previous")
	}

	return &ExtensionImportResult{
		ExtensionDir:    extDir,
		ExtensionID:     extID,
		UpdatedProfiles: updated,
		Message:         fmt.Sprintf("扩展已从 %d 个实例解绑，重启实例后不再出现", len(updated)),
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

	for _, id := range profileIds {
		a.enableExtensionDeveloperModeForProfile(id)
	}
	candidateID := a.registeredExtensionIDForAddress(downloadAddress)
	if candidateID == "" {
		candidateID = extractExtensionID(downloadAddress)
	}
	if candidateID != "" {
		missing := a.filterProfilesMissingEquivalentExtension(profileIds, candidateID, "", downloadAddress)
		if len(missing) == 0 {
			return &ExtensionImportResult{
				ExtensionID:     candidateID,
				UpdatedProfiles: []string{},
				Message:         fmt.Sprintf("所选 %d 个环境已存在同一扩展，未覆盖原扩展", len(profileIds)),
			}, nil
		}
		profileIds = missing
	}

	extID, extDir, previousVersion, extensionVersion, err := a.downloadAndInstallExtension(downloadAddress)
	if err != nil {
		return nil, err
	}
	manifestName := readManifestNameFromDir(extDir)
	profileIds = a.filterProfilesMissingEquivalentExtension(profileIds, extID, manifestName, downloadAddress)
	if len(profileIds) == 0 {
		return &ExtensionImportResult{
			ExtensionDir:     extDir,
			ExtensionID:      extID,
			ExtensionVersion: extensionVersion,
			PreviousVersion:  previousVersion,
			UpdatedProfiles:  []string{},
			Message:          "所选环境已存在同类型、名称或来源的扩展，未覆盖原扩展",
		}, nil
	}

	updated, err := a.bindExtensionDirToProfiles(profileIds, extDir)
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
		ProfileIDs:      updated,
	})
	if err := a.saveProfileExtensionRegistry(assignments); err != nil {
		return nil, err
	}
	return &ExtensionImportResult{
		ExtensionDir:     extDir,
		ExtensionID:      extID,
		ExtensionVersion: extensionVersion,
		PreviousVersion:  previousVersion,
		UpdatedProfiles:  updated,
		Message:          extensionInstallMessage(previousVersion, extensionVersion, len(updated)),
	}, nil
}

// BrowserGlobalExtensionImport installs an extension as a real global policy.
// It binds all existing profiles immediately and is also injected at every
// future profile launch, including profiles created after this call.
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

	extID, extDir, previousVersion, extensionVersion, err := a.downloadAndInstallExtension(downloadAddress)
	if err != nil {
		return nil, err
	}
	registry, err := a.loadGlobalExtensionRegistry()
	if err != nil {
		return nil, err
	}
	registry.Extensions = upsertGlobalExtensionRegistryEntry(registry.Extensions, globalExtensionRegistryEntry{
		DownloadAddress: downloadAddress,
		ExtensionID:     extID,
	})
	if err := a.saveGlobalExtensionRegistry(registry); err != nil {
		return nil, err
	}

	profiles := a.browserMgr.List()
	targetIDs := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		targetIDs = append(targetIDs, profile.ProfileId)
		a.enableExtensionDeveloperModeForProfile(profile.ProfileId)
	}
	missing := a.filterProfilesMissingEquivalentExtension(targetIDs, extID, readManifestNameFromDir(extDir), downloadAddress)
	updated := []string{}
	if len(missing) > 0 {
		updated, err = a.bindExtensionDirToProfiles(missing, extDir)
		if err != nil {
			return nil, err
		}
	}
	return &ExtensionImportResult{
		ExtensionDir:     extDir,
		ExtensionID:      extID,
		ExtensionVersion: extensionVersion,
		PreviousVersion:  previousVersion,
		UpdatedProfiles:  updated,
		Message:          fmt.Sprintf("全局扩展已应用：新增 %d 个环境，跳过 %d 个已有同类扩展的环境；后续新建环境在创建时继承", len(updated), len(targetIDs)-len(updated)),
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
	for _, entry := range registry.Extensions {
		extDir := a.globalExtensionDir(entry.ExtensionID)
		out = append(out, GlobalManagedExtension{
			DownloadAddress: entry.DownloadAddress,
			ExtensionID:     entry.ExtensionID,
			ExtensionDir:    extDir,
			Installed:       extensionManifestExists(extDir),
		})
	}
	return out, nil
}

func (a *App) downloadAndInstallExtension(downloadAddress string) (string, string, string, string, error) {
	extID := extractExtensionID(downloadAddress)
	if extID == "" {
		extID = a.registeredExtensionIDForAddress(downloadAddress)
	}
	if extID != "" {
		extDir := a.globalExtensionDir(extID)
		if validateUnpackedExtensionManifest(extDir) == nil {
			version := readManifestVersionFromDir(extDir)
			return extID, extDir, version, version, nil
		}
	}
	downloadURL := resolveExtensionDownloadURL(downloadAddress, extID)
	if err := validateExtensionDownloadURL(downloadURL); err != nil {
		return "", "", "", "", err
	}
	payload, err := downloadExtensionPayload(downloadURL)
	if err != nil {
		return "", "", "", "", err
	}
	zipPayload, err := extractZipPayloadFromCRX(payload)
	if err != nil {
		return "", "", "", "", err
	}
	if extID == "" {
		sum := sha256.Sum256(payload)
		extID = "external-" + hex.EncodeToString(sum[:])[:16]
	}
	extDir := a.globalExtensionDir(extID)
	if validateUnpackedExtensionManifest(extDir) == nil {
		version := readManifestVersionFromDir(extDir)
		return extID, extDir, version, version, nil
	}
	extDir, previousVersion, extensionVersion, err := installUnpackedExtension(a.appRoot, extID, zipPayload)
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
	if previousVersion != "" && previousVersion != extensionVersion {
		return fmt.Sprintf("扩展已从 %s 更新到 %s，并绑定到选中的实例；各环境钱包/Cookies 数据未改动，重启实例后生效", previousVersion, extensionVersion)
	}
	return fmt.Sprintf("扩展 %s 已绑定到 %d 个实例；各环境钱包/Cookies 数据未改动，重启实例后生效", extensionVersion, count)
}

func (a *App) filterProfilesMissingEquivalentExtension(profileIDs []string, extensionID string, manifestName string, downloadAddress string) []string {
	assignments, _ := a.loadProfileExtensionRegistry()
	sourceKey := extensionSourceKey(downloadAddress)
	assigned := map[string]bool{}
	for _, entry := range assignments.Extensions {
		sameID := extensionID != "" && strings.EqualFold(entry.ExtensionID, extensionID)
		sameSource := sourceKey != "" && extensionSourceKey(entry.DownloadAddress) == sourceKey
		if !sameID && !sameSource {
			continue
		}
		for _, profileID := range entry.ProfileIDs {
			assigned[profileID] = true
		}
	}

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
		if assigned[profileID] || (extDir != "" && hasExtensionDirInLaunchArgs(profile.LaunchArgs, extDir)) {
			continue
		}
		userDataDir := a.browserMgr.ResolveUserDataDir(&profile)
		if profileHasEquivalentExtension(userDataDir, extensionID, manifestName) {
			continue
		}
		missing = append(missing, profileID)
	}
	return missing
}

func profileHasEquivalentExtension(userDataDir string, extensionID string, manifestName string) bool {
	extensionID = strings.ToLower(strings.TrimSpace(extensionID))
	manifestName = strings.ToLower(strings.TrimSpace(manifestName))
	if extensionID != "" {
		for _, profileDir := range chromeProfileDirs(userDataDir) {
			if info, err := os.Stat(filepath.Join(profileDir, "Extensions", extensionID)); err == nil && info.IsDir() {
				return true
			}
		}
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
		for id, raw := range settings {
			if extensionID != "" && strings.EqualFold(strings.TrimSpace(id), extensionID) {
				return true
			}
			setting, _ := raw.(map[string]any)
			if manifestName != "" && extensionSettingManifestName(setting) == manifestName {
				return true
			}
		}
	}
	return false
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

func isBlockedExtensionDownloadHost(host string) bool {
	return isBlockedRemoteHostname(host)
}

func downloadExtensionPayload(downloadURL string) ([]byte, error) {
	const maxExtensionDownloadBytes = 128 * 1024 * 1024
	client := newPublicRemoteHTTPClient(90*time.Second, false)
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("扩展下载地址无效：%w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/"+managedExtensionChromeVersion+" Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("扩展下载失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("扩展下载失败：HTTP %d", resp.StatusCode)
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
	if len(data) >= 4 && bytes.Equal(data[:4], []byte("PK\x03\x04")) {
		return data, nil
	}
	if len(data) < 16 || !bytes.Equal(data[:4], []byte("Cr24")) {
		return nil, fmt.Errorf("扩展格式不支持：请提供 Chrome Web Store 地址、.crx 或 .zip 下载地址")
	}
	version := binary.LittleEndian.Uint32(data[4:8])
	var offset uint32
	switch version {
	case 2:
		pubLen := binary.LittleEndian.Uint32(data[8:12])
		sigLen := binary.LittleEndian.Uint32(data[12:16])
		offset = 16 + pubLen + sigLen
	case 3:
		headerLen := binary.LittleEndian.Uint32(data[8:12])
		offset = 12 + headerLen
	default:
		return nil, fmt.Errorf("扩展格式不支持：CRX version %d", version)
	}
	if int(offset)+4 > len(data) || !bytes.Equal(data[offset:offset+4], []byte("PK\x03\x04")) {
		return nil, fmt.Errorf("CRX 解包失败：未找到 ZIP 数据")
	}
	return data[offset:], nil
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

func installUnpackedExtension(appRoot string, extID string, zipPayload []byte) (string, string, string, error) {
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
	newVersion := readManifestVersionFromDir(stageDir)
	previousVersion := readManifestVersionFromDir(extDir)

	backupDir := extDir + ".previous"
	_ = os.RemoveAll(backupDir)
	if _, statErr := os.Stat(extDir); statErr == nil {
		if err := os.Rename(extDir, backupDir); err != nil {
			return "", "", "", fmt.Errorf("替换旧扩展目录失败：%w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return "", "", "", fmt.Errorf("检查旧扩展目录失败：%w", statErr)
	}
	if err := os.Rename(stageDir, extDir); err != nil {
		_ = os.Rename(backupDir, extDir)
		return "", "", "", fmt.Errorf("安装扩展目录失败：%w", err)
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

func (a *App) bindExtensionDirToProfiles(profileIds []string, extDir string) ([]string, error) {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	updated := make([]string, 0, len(profileIds))
	for _, id := range profileIds {
		profile, ok := a.browserMgr.Profiles[id]
		if !ok || profile == nil {
			continue
		}
		profile.LaunchArgs = addExtensionDirToLaunchArgs(profile.LaunchArgs, extDir)
		profile.UpdatedAt = time.Now().Format(time.RFC3339)
		updated = append(updated, id)
	}
	if len(updated) == 0 {
		return nil, fmt.Errorf("未找到可更新的实例")
	}
	if err := a.browserMgr.SaveProfiles(); err != nil {
		return nil, fmt.Errorf("保存实例扩展配置失败：%w", err)
	}
	return updated, nil
}

func (a *App) removeExtensionDirFromProfilesExcept(extDir string, keepProfiles map[string]bool) ([]string, error) {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
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
		profile.LaunchArgs = nextArgs
		profile.UpdatedAt = time.Now().Format(time.RFC3339)
		updated = append(updated, id)
	}
	if len(updated) == 0 {
		return updated, nil
	}
	if err := a.browserMgr.SaveProfiles(); err != nil {
		return nil, fmt.Errorf("保存全局扩展配置失败：%w", err)
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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("创建全局扩展配置目录失败：%w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".global-extensions-*.tmp")
	if err != nil {
		return fmt.Errorf("创建全局扩展临时配置失败：%w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置全局扩展配置权限失败：%w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入全局扩展临时配置失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭全局扩展临时配置失败：%w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("创建环境扩展分配目录失败：%w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profile-extensions-*.tmp")
	if err != nil {
		return fmt.Errorf("创建环境扩展分配临时配置失败：%w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置环境扩展分配配置权限失败：%w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入环境扩展分配临时配置失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭环境扩展分配临时配置失败：%w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
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

// appendGlobalExtensionArgsForNewProfile is called only by the explicit
// profile-creation workflow. Browser startup never reads registries or scans
// extension directories.
func (a *App) appendGlobalExtensionArgsForNewProfile(args []string) []string {
	registry, err := a.loadGlobalExtensionRegistry()
	if err != nil {
		return normalizeLoadExtensionArgs(args)
	}
	for _, entry := range registry.Extensions {
		extDir := a.globalExtensionDir(entry.ExtensionID)
		if extensionManifestExists(extDir) {
			args = addExtensionDirToLaunchArgs(args, extDir)
		}
	}
	if len(activeLoadExtensionDirs(args)) > 0 {
		args = removeExtensionBlockingLaunchArgs(args)
	}
	return normalizeLoadExtensionArgs(args)
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
		if strings.EqualFold(trimmed, "--disable-extensions") || strings.HasPrefix(strings.ToLower(trimmed), "--disable-extensions=") {
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
	a.enableExtensionDeveloperModeForProfile(profileID)
	if extID != "" && len(a.filterProfilesMissingEquivalentExtension([]string{profileID}, extID, "", crxURL)) == 0 {
		return extID, "", nil
	}
	extID, extDir, _, _, err := a.downloadAndInstallExtension(crxURL)
	if err != nil {
		return extID, "", err
	}
	extName := readManifestNameFromDir(extDir)
	if len(a.filterProfilesMissingEquivalentExtension([]string{profileID}, extID, extName, crxURL)) == 0 {
		return extID, extName, nil
	}
	if _, err := a.bindExtensionDirToProfiles([]string{profileID}, extDir); err != nil {
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
	return normalizeLoadExtensionArgs(append(append([]string{}, args...), "--load-extension="+extDir))
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

func (a *App) enableExtensionDeveloperModeForProfile(profileID string) {
	for _, profile := range a.browserMgr.List() {
		if profile.ProfileId == profileID {
			enableExtensionDeveloperMode(a.browserMgr.ResolveUserDataDir(&profile))
			return
		}
	}
}

// enableExtensionDeveloperMode runs only when the user explicitly distributes
// an extension or when a new profile inherits a global choice.
func enableExtensionDeveloperMode(userDataDir string) {
	if strings.TrimSpace(userDataDir) == "" {
		return
	}
	defaultPrefs := filepath.Join(userDataDir, "Default", "Preferences")
	_ = ensureChromePreferencesFile(defaultPrefs)
	for _, prefPath := range chromeProfilePreferencePaths(userDataDir) {
		data, err := os.ReadFile(prefPath)
		if err != nil {
			continue
		}
		prefs := map[string]any{}
		if len(strings.TrimSpace(string(data))) > 0 && json.Unmarshal(data, &prefs) != nil {
			continue
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
		if out, marshalErr := json.MarshalIndent(prefs, "", "   "); marshalErr == nil {
			_ = os.WriteFile(prefPath, out, 0644)
		}
	}
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
