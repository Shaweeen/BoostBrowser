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
