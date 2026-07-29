package browser

import (
	"boost-browser/backend/internal/logger"
	"fmt"
	"path/filepath"
	"strings"
)

// GetProxyConfigById 根据代理 ID 获取代理配置
func (m *Manager) GetProxyConfigById(proxyId string) (string, bool) {
	if proxy, ok := m.GetProxyByID(proxyId); ok {
		return strings.TrimSpace(proxy.ProxyConfig), true
	}
	return "", false
}

// ResolveUserDataDir 解析用户数据目录
func (m *Manager) ResolveUserDataDir(profile *Profile) string {
	userDataDir := strings.TrimSpace(profile.UserDataDir)
	if userDataDir == "" {
		userDataDir = profile.ProfileId
	}
	if filepath.IsAbs(userDataDir) {
		return userDataDir
	}
	root := strings.TrimSpace(m.Config.Browser.UserDataRoot)
	if root == "" {
		root = "data"
	}
	root = m.ResolveRelativePath(root)
	return filepath.Join(root, userDataDir)
}

// userDataDirKey returns the physical storage identity used to isolate browser
// profiles. Windows paths are case-insensitive, and treating them that way on
// every platform also prevents a backup created on macOS/Linux from producing
// colliding environments after it is restored on Windows.
func (m *Manager) userDataDirKey(profile *Profile) string {
	if profile == nil {
		return ""
	}
	resolved := filepath.Clean(m.ResolveUserDataDir(profile))
	if absolute, err := filepath.Abs(resolved); err == nil {
		resolved = filepath.Clean(absolute)
	}
	return strings.ToLower(filepath.ToSlash(resolved))
}

// validateUserDataDirOwnerLocked enforces the core data invariant: exactly one
// environment owns each Chromium user-data directory. The caller must hold
// Manager.Mutex.
func (m *Manager) validateUserDataDirOwnerLocked(profile *Profile, excludeProfileID string) error {
	key := m.userDataDirKey(profile)
	if key == "" {
		return fmt.Errorf("环境用户数据目录为空")
	}
	for profileID, existing := range m.Profiles {
		if existing == nil || profileID == excludeProfileID {
			continue
		}
		if m.userDataDirKey(existing) == key {
			return fmt.Errorf("用户数据目录已由环境 %s 使用；每个环境必须使用独立目录，以保护 Cookie、扩展和钱包数据", existing.ProfileName)
		}
	}
	return nil
}

// ValidateUserDataDirOwnership is used before launch to detect legacy database
// collisions without rewriting or relocating either environment.
func (m *Manager) ValidateUserDataDirOwnership(profileID string) error {
	m.InitData()
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	profile, ok := m.Profiles[profileID]
	if !ok || profile == nil {
		return fmt.Errorf("profile not found")
	}
	return m.validateUserDataDirOwnerLocked(profile, profileID)
}

// MigrateConfig 迁移旧配置到新格式
func (m *Manager) MigrateConfig() bool {
	log := logger.New("Browser")

	// 如果存在 environments 但没有 cores，执行迁移
	if len(m.Config.Browser.Environments) > 0 && len(m.Config.Browser.Cores) == 0 {
		log.Info("检测到旧配置格式，开始迁移")

		for _, env := range m.Config.Browser.Environments {
			m.Config.Browser.Cores = append(m.Config.Browser.Cores, Core{
				CoreId:    env.CoreId,
				CoreName:  env.CoreName,
				CorePath:  env.CorePath,
				IsDefault: env.IsDefault,
			})
		}

		// 清空旧字段
		m.Config.Browser.Environments = nil
		m.Config.Browser.ChromeBinaryPath = ""
		m.Config.Browser.CoreRoot = ""
		m.Config.Browser.DefaultCoreId = ""
		m.Config.Browser.DefaultConnectorType = ""

		if err := m.Config.Save(m.ResolveRelativePath("config.yaml")); err != nil {
			log.Error("配置迁移保存失败", logger.F("error", err.Error()))
			return false
		}

		log.Info("配置迁移完成", logger.F("cores_count", len(m.Config.Browser.Cores)))
		return true
	}

	return false
}
