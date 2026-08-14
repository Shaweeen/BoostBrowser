package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/logger"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	bundledChrome148CoreID      = "bundled-google-chrome-latest"
	bundledChrome148Version     = "148.0.7778.167"
	bundledChrome148RelativeDir = "chrome/google-148.0.7778.167"
)

// RunPostUpdateMaintenance owns the only automatic integrity pass. It is
// called exclusively for the updater's --post-update process, never by normal
// client startup and never when an environment is opened.
func (a *App) RunPostUpdateMaintenance() error {
	if a == nil || a.panelMode || a.browserMgr == nil || a.db == nil {
		return nil
	}
	log := logger.New("Updater")
	if count, err := a.reconcileProfileUUIDData(); err != nil {
		return fmt.Errorf("更新后环境 UUID 数据完整性校验失败: %w", err)
	} else {
		a.lifecycleLog("post-update-profile-uuid-data", "state=complete", fmt.Sprintf("reconciled=%d", count))
	}

	activeDataRoot := a.resolveAppPath("data")
	a.initializeActiveDataCompatibility(activeDataRoot, directoryHasEntries(activeDataRoot))
	a.migrateLegacyExtensionPackageStore()
	if err := a.captureProfileExtensionInventory(); err != nil {
		return fmt.Errorf("更新后保存扩展身份清单失败: %w", err)
	}

	core, err := a.setChrome148AsGlobalDefault(true)
	if err != nil {
		return err
	}
	recovered, recoveryErr := a.prepareAllProfileExtensionsAfterUpdate(core.CoreId)
	if recoveryErr != nil {
		log.Warn("更新后部分扩展按原 ID 恢复失败（Cookies、扩展存储和应用数据未改动）", logger.F("error", recoveryErr.Error()))
	}
	if pointerErr := a.persistAllProfileDataPointersAfterUpdate(); pointerErr != nil {
		if recoveryErr != nil {
			recoveryErr = fmt.Errorf("%v；%w", recoveryErr, pointerErr)
		} else {
			recoveryErr = pointerErr
		}
	}
	if result, cleanupErr := a.browserMgr.CleanupExpiredDeletedEnvironmentData(time.Now()); cleanupErr != nil {
		log.Warn("更新后恢复归档保留期检查失败", logger.F("error", cleanupErr.Error()))
	} else {
		a.lifecycleLog("post-update-deleted-data-retention", fmt.Sprintf("removed=%d", result.Removed), fmt.Sprintf("retained=%d", result.Skipped))
	}
	a.lifecycleLog("post-update-maintenance", "state=complete", "core="+core.CoreId, fmt.Sprintf("extension_profiles_recovered=%d", recovered))
	return recoveryErr
}

// setChrome148AsGlobalDefault registers only the fixed bundled Chrome for
// Testing path. alignProfiles is true only during an update; it changes the
// BrowserStudio-owned core_id metadata and never browser profile contents.
func (a *App) setChrome148AsGlobalDefault(alignProfiles bool) (browser.Core, error) {
	if a == nil || a.browserMgr == nil {
		return browser.Core{}, fmt.Errorf("浏览器管理器尚未初始化")
	}
	relativePath := filepath.FromSlash(bundledChrome148RelativeDir)
	absDir := a.resolveAppPath(relativePath)
	if !isChromeForTestingKernelDir(absDir) {
		return browser.Core{}, fmt.Errorf("Chrome 148 缺少 Chrome for Testing 标记: %s", absDir)
	}
	if _, _, ok := browser.FindCoreExecutable(absDir); !ok {
		return browser.Core{}, fmt.Errorf("Chrome 148 内核不可执行: %s", absDir)
	}
	core := browser.Core{
		CoreId:    bundledChrome148CoreID,
		CoreName:  fmt.Sprintf("Google Chrome %s（内置隔离）", bundledChrome148Version),
		CorePath:  relativePath,
		IsDefault: true,
	}
	if err := a.browserMgr.SaveCore(browser.CoreInput{
		CoreId: core.CoreId, CoreName: core.CoreName, CorePath: core.CorePath, IsDefault: true,
	}); err != nil {
		return browser.Core{}, fmt.Errorf("注册 Chrome 148 内核失败: %w", err)
	}
	if err := a.browserMgr.SetDefaultCore(core.CoreId); err != nil {
		return browser.Core{}, fmt.Errorf("设置 Chrome 148 默认内核失败: %w", err)
	}
	if !alignProfiles {
		return core, nil
	}
	conn := a.db.GetConn()
	if conn == nil {
		return browser.Core{}, fmt.Errorf("应用数据库尚未初始化")
	}
	if _, err := conn.Exec("UPDATE browser_profiles SET core_id = ?", core.CoreId); err != nil {
		return browser.Core{}, fmt.Errorf("更新全部环境 Chrome 148 内核失败: %w", err)
	}
	a.browserMgr.ReloadProfilesFromDAO()
	if a.config != nil {
		a.config.Browser.DefaultCoreId = core.CoreId
		_ = a.config.Save(a.resolveAppPath("config.yaml"))
	}
	return core, nil
}

func (a *App) prepareAllProfileExtensionsAfterUpdate(coreID string) (int, error) {
	profiles := a.browserMgr.List()
	changed := make([]*browser.Profile, 0)
	errorsFound := make([]string, 0)
	recovered := 0
	for index := range profiles {
		profile := profiles[index]
		if profile.Running {
			errorsFound = append(errorsFound, profile.ProfileId+": 环境仍在运行")
			continue
		}
		userDataDir := a.browserMgr.ResolveUserDataDir(&profile)
		launchArgs := a.resolveLegacyManagedExtensionLaunchArgs(profile.LaunchArgs)
		packageDirs, _, err := a.prepareProfileExtensionRecovery(profile.ProfileId, userDataDir, launchArgs)
		if err != nil {
			errorsFound = append(errorsFound, profile.ProfileId+": "+err.Error())
		}
		if len(packageDirs) > 0 {
			launchArgs = appendPreparedExtensionRecoveryArgs(launchArgs, packageDirs)
			recovered++
		}
		if !sameStringSlice(profile.LaunchArgs, launchArgs) || !strings.EqualFold(profile.CoreId, coreID) {
			profile.LaunchArgs = launchArgs
			profile.CoreId = coreID
			copy := profile
			changed = append(changed, &copy)
		}
	}
	if len(changed) > 0 {
		if err := a.browserMgr.ProfileDAO.UpsertMany(changed); err != nil {
			return recovered, fmt.Errorf("保存更新后扩展恢复结果失败: %w", err)
		}
		a.browserMgr.ReloadProfilesFromDAO()
	}
	if len(errorsFound) > 0 {
		return recovered, fmt.Errorf("%s", strings.Join(errorsFound, "; "))
	}
	return recovered, nil
}

func (a *App) persistAllProfileDataPointersAfterUpdate() error {
	for _, profile := range a.browserMgr.List() {
		if err := a.browserMgr.WriteProfileDataPointer(&profile, "closed", 0, time.Now()); err != nil {
			return fmt.Errorf("更新环境 %s 身份指向失败: %w", profile.ProfileId, err)
		}
	}
	return nil
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
