package browser

import (
	"boost-browser/backend/internal/fsutil"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ProfileRecoveryDirectory = ".browserstudio-recovery"
	profileArchiveDirectory  = "deleted-environments"
)

// ProfileDataArchiveRetention is deliberately measured in calendar months,
// not a fixed day count: an archive created on the 31st expires six calendar
// months later. Expiry is evaluated only during normal main-client startup;
// there is no continuous archive watcher.
const ProfileDataArchiveRetentionMonths = 6

// ProfileDataArchiveManifest records only the environment-to-folder linkage
// needed for user-confirmed recovery. It never reads or stores page content,
// Cookies, extension storage, wallet vaults, mnemonics or private keys.
type ProfileDataArchiveManifest struct {
	Version             int      `json:"version"`
	ProfileID           string   `json:"profileId"`
	ProfileName         string   `json:"profileName"`
	OriginalUserDataDir string   `json:"originalUserDataDir"`
	ArchivedDataDir     string   `json:"archivedDataDir"`
	DataAvailable       bool     `json:"dataAvailable"`
	CoreID              string   `json:"coreId,omitempty"`
	FingerprintArgs     []string `json:"fingerprintArgs,omitempty"`
	ProxyID             string   `json:"proxyId,omitempty"`
	LaunchArgs          []string `json:"launchArgs,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	Keywords            []string `json:"keywords,omitempty"`
	GroupID             string   `json:"groupId,omitempty"`
	CreatedAt           string   `json:"createdAt,omitempty"`
	UpdatedAt           string   `json:"updatedAt,omitempty"`
	ArchivedAt          string   `json:"archivedAt"`
	Reason              string   `json:"reason"`
	// IgnoredAt records an explicit "do not import" decision. Ignored archives
	// are never offered again, but stay recoverable until their retention date.
	IgnoredAt string `json:"ignoredAt,omitempty"`
}

// ProfileDataArchiveOffer is deliberately non-secret. It is sufficient to let
// the UI ask whether a freshly-created environment should reuse a deleted
// environment's browser folder without reading Cookies or wallet storage.
type ProfileDataArchiveOffer struct {
	ArchiveKey          string `json:"archiveKey"`
	TargetProfileID     string `json:"targetProfileId"`
	TargetProfileName   string `json:"targetProfileName"`
	ArchivedProfileName string `json:"archivedProfileName"`
	ArchivedAt          string `json:"archivedAt"`
	MatchReason         string `json:"matchReason"`
}

type ProfileDataArchiveCleanupResult struct {
	Removed int      `json:"removed"`
	Skipped int      `json:"skipped"`
	Keys    []string `json:"keys"`
}

type ProfileDataArchiveMove struct {
	originalDir string
	archiveDir  string
	manifest    string
	hadData     bool
}

func (m *Manager) ProfileRecoveryArchiveRoot() string {
	root := strings.TrimSpace(m.Config.Browser.UserDataRoot)
	if root == "" {
		root = "data"
	}
	return filepath.Join(m.ResolveRelativePath(root), ProfileRecoveryDirectory, profileArchiveDirectory)
}

func (m *Manager) stageProfileDataArchiveLocked(profile *Profile, reason string) (*ProfileDataArchiveMove, error) {
	if profile == nil {
		return nil, fmt.Errorf("环境配置为空")
	}
	originalDir := m.ResolveUserDataDir(profile)
	if err := validateUserDataDirForDelete(originalDir, m.ResolveRelativePath(strings.TrimSpace(m.Config.Browser.UserDataRoot))); err != nil {
		return nil, err
	}
	archiveRoot := m.ProfileRecoveryArchiveRoot()
	if err := os.MkdirAll(archiveRoot, 0700); err != nil {
		return nil, fmt.Errorf("创建环境恢复归档目录失败: %w", err)
	}
	archiveName := time.Now().Format("20060102-150405.000000000") + "-" + profileArchiveSafeName(profile.ProfileName) + "-" + profile.ProfileId
	archiveDir := filepath.Join(archiveRoot, archiveName)
	manifestPath := archiveDir + ".profile.json"
	if _, err := os.Stat(archiveDir); err == nil {
		return nil, fmt.Errorf("环境恢复归档已存在，已停止删除")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("检查环境恢复归档失败: %w", err)
	}

	move := &ProfileDataArchiveMove{
		originalDir: originalDir,
		archiveDir:  archiveDir,
		manifest:    manifestPath,
	}
	if info, err := os.Lstat(originalDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("环境数据目录不是可归档的普通文件夹")
		}
		if err := os.Rename(originalDir, archiveDir); err != nil {
			return nil, fmt.Errorf("归档环境数据失败，环境未删除: %w", err)
		}
		move.hadData = true
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("检查环境数据失败，环境未删除: %w", err)
	}

	manifest := ProfileDataArchiveManifest{
		Version:             1,
		ProfileID:           profile.ProfileId,
		ProfileName:         profile.ProfileName,
		OriginalUserDataDir: profile.UserDataDir,
		ArchivedDataDir:     archiveName,
		DataAvailable:       move.hadData,
		CoreID:              profile.CoreId,
		FingerprintArgs:     append([]string{}, profile.FingerprintArgs...),
		ProxyID:             profile.ProxyId,
		LaunchArgs:          append([]string{}, profile.LaunchArgs...),
		Tags:                append([]string{}, profile.Tags...),
		Keywords:            append([]string{}, profile.Keywords...),
		GroupID:             profile.GroupId,
		CreatedAt:           profile.CreatedAt,
		UpdatedAt:           profile.UpdatedAt,
		ArchivedAt:          time.Now().Format(time.RFC3339Nano),
		Reason:              strings.TrimSpace(reason),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, errors.Join(fmt.Errorf("生成环境恢复索引失败: %w", err), move.Rollback())
	}
	if err := fsutil.WriteFileAtomic(manifestPath, data, 0600); err != nil {
		return nil, errors.Join(fmt.Errorf("写入环境恢复索引失败: %w", err), move.Rollback())
	}
	return move, nil
}

func (move *ProfileDataArchiveMove) Rollback() error {
	if move == nil {
		return nil
	}
	var rollbackErrs []error
	dataRestored := !move.hadData
	if move.hadData {
		if _, err := os.Lstat(move.originalDir); err == nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("原环境目录已被占用，归档数据仍保留在 %s", move.archiveDir))
		} else if !os.IsNotExist(err) {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("检查原环境目录失败，归档数据仍保留在 %s: %w", move.archiveDir, err))
		} else if err := os.Rename(move.archiveDir, move.originalDir); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("恢复原环境数据失败，完整归档仍保留在 %s: %w", move.archiveDir, err))
		} else {
			dataRestored = true
		}
	}
	// Keep the sidecar index whenever the data could not be moved back. Without
	// it, the recovery UI would lose the only profile-to-archive mapping even
	// though the complete wallet/Cookie directory still exists in the archive.
	if dataRestored {
		if err := os.Remove(move.manifest); err != nil && !os.IsNotExist(err) {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("删除未完成恢复索引失败: %w", err))
		}
	}
	return errors.Join(rollbackErrs...)
}

func (move *ProfileDataArchiveMove) ArchiveDir() string {
	if move == nil {
		return ""
	}
	return move.archiveDir
}

func (move *ProfileDataArchiveMove) DataAvailable() bool {
	return move != nil && move.hadData
}

// ArchiveProfileDataForReplacement preserves the current physical data before
// an explicit user-confirmed restore replaces it.
func (m *Manager) ArchiveProfileDataForReplacement(profileID, reason string) (*ProfileDataArchiveMove, error) {
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	profile := m.Profiles[strings.TrimSpace(profileID)]
	if profile == nil {
		return nil, fmt.Errorf("profile not found")
	}
	if profile.Running || m.BrowserProcesses[profile.ProfileId] != nil {
		return nil, fmt.Errorf("请先停止环境再替换环境数据")
	}
	return m.stageProfileDataArchiveLocked(profile, reason)
}

func profileArchiveSafeName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "environment"
	}
	var out strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			out.WriteRune(r)
		default:
			out.WriteRune('_')
		}
	}
	result := strings.Trim(out.String(), "._-")
	if result == "" {
		return "environment"
	}
	return result
}

// ListProfileDataArchives reads only BrowserStudio's sidecar indexes. It does
// not inspect any archived browser or wallet file.
func ListProfileDataArchives(archiveRoot string) ([]ProfileDataArchiveManifest, error) {
	entries, err := os.ReadDir(archiveRoot)
	if os.IsNotExist(err) {
		return []ProfileDataArchiveManifest{}, nil
	}
	if err != nil {
		return nil, err
	}
	archives := make([]ProfileDataArchiveManifest, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".profile.json") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(archiveRoot, entry.Name()))
		if readErr != nil {
			continue
		}
		var manifest ProfileDataArchiveManifest
		if json.Unmarshal(data, &manifest) != nil || manifest.Version != 1 {
			continue
		}
		if manifest.DataAvailable {
			info, statErr := os.Lstat(filepath.Join(archiveRoot, manifest.ArchivedDataDir))
			manifest.DataAvailable = statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
		}
		archives = append(archives, manifest)
	}
	return archives, nil
}

// FindDeletedEnvironmentDataOffers finds one exact, user-actionable recovery
// archive for each freshly-created profile. The match is intentionally narrow:
// same user-data identity when the user chose one, otherwise the same visible
// environment name. It never inspects archived Chrome, Cookie, extension or
// wallet content.
func (m *Manager) FindDeletedEnvironmentDataOffers(profileIDs []string) ([]ProfileDataArchiveOffer, error) {
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()

	offers := make([]ProfileDataArchiveOffer, 0, len(profileIDs))
	seenProfiles := make(map[string]bool, len(profileIDs))
	for _, rawID := range profileIDs {
		profileID := strings.TrimSpace(rawID)
		if profileID == "" || seenProfiles[profileID] {
			continue
		}
		seenProfiles[profileID] = true
		profile := m.Profiles[profileID]
		if profile == nil || profile.Running || m.BrowserProcesses[profileID] != nil {
			continue
		}
		offer, err := m.findDeletedEnvironmentDataOfferLocked(profile)
		if err != nil {
			return nil, err
		}
		if offer != nil {
			offers = append(offers, *offer)
		}
	}
	return offers, nil
}

func (m *Manager) findDeletedEnvironmentDataOfferLocked(target *Profile) (*ProfileDataArchiveOffer, error) {
	archiveRoot := m.ProfileRecoveryArchiveRoot()
	archives, err := ListProfileDataArchives(archiveRoot)
	if err != nil {
		return nil, err
	}
	var chosen *ProfileDataArchiveManifest
	var chosenReason string
	for i := range archives {
		archive := archives[i]
		if !archive.DataAvailable || strings.TrimSpace(archive.IgnoredAt) != "" || archive.ProfileID == target.ProfileId {
			continue
		}
		reason := archiveMatchReason(target, &archive)
		if reason == "" {
			continue
		}
		if chosen == nil || archiveArchivedTime(archive).After(archiveArchivedTime(*chosen)) {
			candidate := archive
			chosen = &candidate
			chosenReason = reason
		}
	}
	if chosen == nil {
		return nil, nil
	}
	return &ProfileDataArchiveOffer{
		ArchiveKey:          chosen.ArchivedDataDir,
		TargetProfileID:     target.ProfileId,
		TargetProfileName:   target.ProfileName,
		ArchivedProfileName: chosen.ProfileName,
		ArchivedAt:          chosen.ArchivedAt,
		MatchReason:         chosenReason,
	}, nil
}

// IgnoreDeletedEnvironmentDataOffer records an explicit user choice. Ignored
// archives remain on disk until their normal six-month retention expires, but
// are never shown automatically again.
func (m *Manager) IgnoreDeletedEnvironmentDataOffer(archiveKey string) error {
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	archiveRoot := m.ProfileRecoveryArchiveRoot()
	manifest, err := readProfileDataArchiveManifest(archiveRoot, archiveKey)
	if err != nil {
		return err
	}
	if !manifest.DataAvailable {
		return fmt.Errorf("恢复归档已不存在")
	}
	manifest.IgnoredAt = time.Now().UTC().Format(time.RFC3339Nano)
	return writeProfileDataArchiveManifest(archiveRoot, manifest)
}

// RestoreDeletedEnvironmentDataOffer moves an archived browser profile into a
// freshly-created, still-empty target environment. It never merges browser
// folders: if the target contains anything besides BrowserStudio's identity
// pointer, it refuses rather than risking Cookies, extensions or wallet data.
func (m *Manager) RestoreDeletedEnvironmentDataOffer(targetProfileID, archiveKey string) error {
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	target := m.Profiles[strings.TrimSpace(targetProfileID)]
	if target == nil {
		return fmt.Errorf("目标环境不存在")
	}
	if target.Running || m.BrowserProcesses[target.ProfileId] != nil {
		return fmt.Errorf("请先停止目标环境再导入已删除环境的数据")
	}
	archiveRoot := m.ProfileRecoveryArchiveRoot()
	manifest, err := readProfileDataArchiveManifest(archiveRoot, archiveKey)
	if err != nil {
		return err
	}
	if !manifest.DataAvailable || strings.TrimSpace(manifest.IgnoredAt) != "" {
		return fmt.Errorf("该恢复归档不可用或已被忽略")
	}
	if archiveMatchReason(target, &manifest) == "" {
		return fmt.Errorf("恢复归档与新建环境不匹配，已取消导入以保护数据")
	}

	archiveDir, err := profileArchiveDataPath(archiveRoot, manifest.ArchivedDataDir)
	if err != nil {
		return err
	}
	info, err := os.Lstat(archiveDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("恢复归档目录不可用")
	}
	destination := m.ResolveUserDataDir(target)
	if err := removeFreshProfileDataShell(destination); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return fmt.Errorf("创建目标数据目录失败: %w", err)
	}
	if err := os.Rename(archiveDir, destination); err != nil {
		return fmt.Errorf("导入恢复归档失败，原数据仍保留: %w", err)
	}
	if err := m.WriteProfileDataPointer(target, "closed", 0, time.Now()); err != nil {
		// Do not leave a moved archive without its recovery index when the new
		// environment pointer cannot be written. Put the complete directory back
		// first; a later manual recovery remains possible and no wallet/Cookie
		// data is stranded in a half-imported state.
		rollbackErr := os.Rename(destination, archiveDir)
		if rollbackErr == nil {
			_ = os.MkdirAll(destination, 0700)
			_ = m.WriteProfileDataPointer(target, "closed", 0, time.Now())
		}
		return errors.Join(
			fmt.Errorf("更新新建环境的数据指向失败，已取消导入: %w", err),
			rollbackErr,
		)
	}
	if err := os.Remove(profileDataArchiveManifestPath(archiveRoot, manifest.ArchivedDataDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("数据已导入，但清理恢复索引失败: %w", err)
	}
	return nil
}

// CleanupExpiredDeletedEnvironmentData removes only BrowserStudio-created
// deletion archives that have existed for at least six calendar months. It is
// called once during ordinary main-client startup, never by a
// continuous watcher. Active profile data is not inside this archive root.
func (m *Manager) CleanupExpiredDeletedEnvironmentData(now time.Time) (ProfileDataArchiveCleanupResult, error) {
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	result := ProfileDataArchiveCleanupResult{Keys: []string{}}
	archiveRoot := m.ProfileRecoveryArchiveRoot()
	archives, err := ListProfileDataArchives(archiveRoot)
	if err != nil {
		return result, err
	}
	for _, archive := range archives {
		if !archive.DataAvailable || !profileDataArchiveExpired(archive, now) {
			result.Skipped++
			continue
		}
		archiveDir, pathErr := profileArchiveDataPath(archiveRoot, archive.ArchivedDataDir)
		if pathErr != nil {
			result.Skipped++
			continue
		}
		info, statErr := os.Lstat(archiveDir)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			result.Skipped++
			continue
		}
		if m.archiveDirectoryInUseLocked(archiveDir) {
			// A user may deliberately configure an absolute custom data folder.
			// Never treat a directory currently owned by an active environment as
			// disposable merely because an old sidecar happens to reference it.
			result.Skipped++
			continue
		}
		if err := os.RemoveAll(archiveDir); err != nil {
			return result, fmt.Errorf("删除过期环境恢复归档失败: %w", err)
		}
		if err := os.Remove(profileDataArchiveManifestPath(archiveRoot, archive.ArchivedDataDir)); err != nil && !os.IsNotExist(err) {
			return result, fmt.Errorf("删除过期环境恢复索引失败: %w", err)
		}
		result.Removed++
		result.Keys = append(result.Keys, archive.ArchivedDataDir)
	}
	return result, nil
}

func (m *Manager) archiveDirectoryInUseLocked(archiveDir string) bool {
	archiveDir = strings.ToLower(filepath.ToSlash(filepath.Clean(archiveDir)))
	for _, profile := range m.Profiles {
		if profile == nil {
			continue
		}
		resolved := strings.ToLower(filepath.ToSlash(filepath.Clean(m.ResolveUserDataDir(profile))))
		if resolved == archiveDir {
			return true
		}
	}
	return false
}

func archiveMatchReason(target *Profile, archive *ProfileDataArchiveManifest) string {
	if target == nil || archive == nil {
		return ""
	}
	if original := strings.TrimSpace(archive.OriginalUserDataDir); original != "" &&
		strings.TrimSpace(target.UserDataDir) != "" &&
		strings.EqualFold(filepath.Clean(original), filepath.Clean(target.UserDataDir)) {
		return "数据目录一致"
	}
	if name := strings.TrimSpace(target.ProfileName); name != "" && strings.EqualFold(name, strings.TrimSpace(archive.ProfileName)) {
		return "环境名称一致"
	}
	return ""
}

func archiveArchivedTime(archive ProfileDataArchiveManifest) time.Time {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(archive.ArchivedAt))
	if err != nil {
		at, _ = time.Parse(time.RFC3339, strings.TrimSpace(archive.ArchivedAt))
	}
	return at
}

func profileDataArchiveExpired(archive ProfileDataArchiveManifest, now time.Time) bool {
	anchor := archiveArchivedTime(archive)
	return !anchor.IsZero() && !now.Before(anchor.AddDate(0, ProfileDataArchiveRetentionMonths, 0))
}

func profileDataArchiveManifestPath(archiveRoot, archiveKey string) string {
	return filepath.Join(archiveRoot, archiveKey+".profile.json")
}

func profileArchiveDataPath(archiveRoot, archiveKey string) (string, error) {
	archiveKey = strings.TrimSpace(archiveKey)
	if archiveKey == "" || archiveKey == "." || archiveKey == ".." || filepath.Base(archiveKey) != archiveKey {
		return "", fmt.Errorf("恢复归档标识无效")
	}
	return filepath.Join(archiveRoot, archiveKey), nil
}

func readProfileDataArchiveManifest(archiveRoot, archiveKey string) (ProfileDataArchiveManifest, error) {
	var manifest ProfileDataArchiveManifest
	if _, err := profileArchiveDataPath(archiveRoot, archiveKey); err != nil {
		return manifest, err
	}
	data, err := os.ReadFile(profileDataArchiveManifestPath(archiveRoot, archiveKey))
	if err != nil {
		return manifest, fmt.Errorf("读取恢复归档索引失败: %w", err)
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Version != 1 || manifest.ArchivedDataDir != archiveKey {
		return ProfileDataArchiveManifest{}, fmt.Errorf("恢复归档索引无效")
	}
	return manifest, nil
}

func writeProfileDataArchiveManifest(archiveRoot string, manifest ProfileDataArchiveManifest) error {
	if _, err := profileArchiveDataPath(archiveRoot, manifest.ArchivedDataDir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(profileDataArchiveManifestPath(archiveRoot, manifest.ArchivedDataDir), data, 0600)
}

func removeFreshProfileDataShell(destination string) error {
	info, err := os.Lstat(destination)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("新建环境的数据目录不可安全替换")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return fmt.Errorf("读取新建环境数据目录失败: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != ProfileDataPointerFileName || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("新建环境已开始使用，已取消导入以保护现有数据")
		}
	}
	if len(entries) == 1 {
		if err := os.Remove(filepath.Join(destination, ProfileDataPointerFileName)); err != nil {
			return fmt.Errorf("清理新建环境身份指向失败: %w", err)
		}
	}
	if err := os.Remove(destination); err != nil {
		return fmt.Errorf("清理空的新建环境数据目录失败: %w", err)
	}
	return nil
}
