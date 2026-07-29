package browser

import (
	"boost-browser/backend/internal/fsutil"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const ProfileDataPointerFileName = ".browserstudio-environment.json"

// ProfileDataPointer is a non-secret identity link stored beside one
// environment's Chromium data. It never reads or copies Cookies, extension
// storage, wallet vaults, mnemonics, private keys or page content.
type ProfileDataPointer struct {
	Version          int      `json:"version"`
	ProfileID        string   `json:"profileId"`
	ProfileName      string   `json:"profileName"`
	UserDataDir      string   `json:"userDataDir"`
	CoreID           string   `json:"coreId,omitempty"`
	FingerprintArgs  []string `json:"fingerprintArgs,omitempty"`
	ProxyID          string   `json:"proxyId,omitempty"`
	LaunchArgs       []string `json:"launchArgs,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Keywords         []string `json:"keywords,omitempty"`
	GroupID          string   `json:"groupId,omitempty"`
	CreatedAt        string   `json:"createdAt,omitempty"`
	UpdatedAt        string   `json:"updatedAt,omitempty"`
	CloseState       string   `json:"closeState"`
	ClosePID         int      `json:"closePid,omitempty"`
	CloseRequestedAt string   `json:"closeRequestedAt,omitempty"`
	LastCleanCloseAt string   `json:"lastCleanCloseAt,omitempty"`
}

// WriteProfileDataPointer atomically records the one-to-one relationship
// between a Profile ID and its physical Chromium directory. "closing" is
// written before asking Chromium to exit; "closed" is written only after the
// process, debug endpoint and Singleton profile lock have all disappeared.
func (m *Manager) WriteProfileDataPointer(profile *Profile, state string, pid int, at time.Time) error {
	if profile == nil {
		return fmt.Errorf("环境配置为空")
	}
	state = strings.ToLower(strings.TrimSpace(state))
	if state != "closing" && state != "closed" {
		return fmt.Errorf("无效的数据关闭状态: %s", state)
	}
	dataDir := m.ResolveUserDataDir(profile)
	info, err := os.Lstat(dataDir)
	if err != nil {
		return fmt.Errorf("环境数据目录不可用: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("环境数据目录不是普通文件夹")
	}

	pointerPath := filepath.Join(dataDir, ProfileDataPointerFileName)
	pointer, _ := ReadProfileDataPointer(dataDir)
	pointer.Version = 1
	pointer.ProfileID = profile.ProfileId
	pointer.ProfileName = profile.ProfileName
	pointer.UserDataDir = profile.UserDataDir
	pointer.CoreID = profile.CoreId
	pointer.FingerprintArgs = append([]string{}, profile.FingerprintArgs...)
	pointer.ProxyID = profile.ProxyId
	pointer.LaunchArgs = append([]string{}, profile.LaunchArgs...)
	pointer.Tags = append([]string{}, profile.Tags...)
	pointer.Keywords = append([]string{}, profile.Keywords...)
	pointer.GroupID = profile.GroupId
	pointer.CreatedAt = profile.CreatedAt
	pointer.UpdatedAt = profile.UpdatedAt
	pointer.CloseState = state
	pointer.ClosePID = pid
	if state == "closing" {
		pointer.CloseRequestedAt = at.Format(time.RFC3339Nano)
	} else {
		pointer.LastCleanCloseAt = at.Format(time.RFC3339Nano)
	}
	data, err := json.MarshalIndent(pointer, "", "  ")
	if err != nil {
		return fmt.Errorf("生成环境数据指向失败: %w", err)
	}
	if err := fsutil.WriteFileAtomic(pointerPath, data, 0600); err != nil {
		return fmt.Errorf("保存环境数据指向失败: %w", err)
	}
	return nil
}

func ReadProfileDataPointer(dataDir string) (ProfileDataPointer, error) {
	var pointer ProfileDataPointer
	data, err := os.ReadFile(filepath.Join(dataDir, ProfileDataPointerFileName))
	if err != nil {
		return pointer, err
	}
	if err := json.Unmarshal(data, &pointer); err != nil {
		return ProfileDataPointer{}, err
	}
	if pointer.Version != 1 || strings.TrimSpace(pointer.ProfileID) == "" {
		return ProfileDataPointer{}, fmt.Errorf("环境数据指向格式无效")
	}
	return pointer, nil
}
