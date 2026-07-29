package backend

import (
	"boost-browser/backend/internal/browser"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StartupDataCompatibilityStatus describes how startup treated the installed
// client's mutable data. Program/schema migrations always run first; browser
// profile folders are then attached without rewriting their contents.
type StartupDataCompatibilityStatus struct {
	ActiveDataPath string `json:"activeDataPath"`
	ExistingData   bool   `json:"existingData"`
	AutoRecovered  int    `json:"autoRecovered"`
	RecoveryCount  int    `json:"recoveryCount"`
	RecoveryPath   string `json:"recoveryPath"`
	Message        string `json:"message"`
}

func directoryHasEntries(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

func (a *App) initializeActiveDataCompatibility(activeRoot string, existed bool) {
	status := StartupDataCompatibilityStatus{
		ActiveDataPath: activeRoot,
		ExistingData:   existed,
		Message:        "已使用当前客户端的最新数据库结构读取 data，未覆盖浏览器数据",
	}
	if a == nil || a.browserMgr == nil || !existed {
		if !existed {
			status.Message = "未发现旧 data，已创建当前版本的全新数据结构"
		}
		a.setStartupDataCompatibilityStatus(status)
		return
	}
	status.RecoveryPath = a.browserMgr.ProfileRecoveryArchiveRoot()
	if archives, err := browser.ListProfileDataArchives(status.RecoveryPath); err == nil {
		for _, archive := range archives {
			if archive.DataAvailable {
				status.RecoveryCount++
			}
		}
		if status.RecoveryCount > 0 {
			status.Message += fmt.Sprintf("；发现 %d 个由用户删除操作保留的环境恢复归档，等待你确认导入", status.RecoveryCount)
		}
	}

	a.browserMgr.Mutex.Lock()
	hasProfiles := len(a.browserMgr.Profiles) > 0
	a.browserMgr.Mutex.Unlock()
	if hasProfiles {
		a.setStartupDataCompatibilityStatus(status)
		return
	}

	// Some old/partially repaired installations contain complete Chrome profile
	// folders but no readable profile table. Attach those folders in place so
	// Cookies, IndexedDB and wallet extension storage remain byte-for-byte intact.
	profiles, err := legacyProfilesFromRawFolders(activeRoot)
	if err != nil || len(profiles) == 0 || a.browserMgr.ProfileDAO == nil {
		a.setStartupDataCompatibilityStatus(status)
		return
	}
	for _, profile := range profiles {
		if profile == nil {
			continue
		}
		profile.UserDataDir = filepath.Clean(profile.UserDataDir)
		profile.ProfileName = strings.TrimSpace(profile.ProfileName)
		if err := a.browserMgr.ProfileDAO.Upsert(profile); err != nil {
			continue
		}
		status.AutoRecovered++
	}
	if status.AutoRecovered > 0 {
		a.browserMgr.InitData()
		status.Message = "已从现有 data 自动识别浏览器环境；Cookies、扩展和钱包本地数据未被覆盖"
	}
	a.setStartupDataCompatibilityStatus(status)
}

func (a *App) setStartupDataCompatibilityStatus(status StartupDataCompatibilityStatus) {
	a.startupDataMu.Lock()
	a.startupDataStatus = status
	a.startupDataMu.Unlock()
}

// GetStartupDataCompatibilityStatus is queried after the frontend mounts so
// startup notices cannot be lost before Wails event listeners are registered.
func (a *App) GetStartupDataCompatibilityStatus() StartupDataCompatibilityStatus {
	if a == nil {
		return StartupDataCompatibilityStatus{}
	}
	a.startupDataMu.RLock()
	defer a.startupDataMu.RUnlock()
	return a.startupDataStatus
}
