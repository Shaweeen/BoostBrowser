package backend

import (
	"boost-browser/backend/internal/browser"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// reconcileProfileUUIDData keeps the existing environment identity invariant:
// one profile UUID owns data/<the same UUID>. It never moves, copies or deletes
// browser data. A missing database row is attached to the directory in place;
// a stale database path is realigned only when that old path no longer exists.
func (a *App) reconcileProfileUUIDData() (int, error) {
	if a == nil || a.browserMgr == nil || a.browserMgr.ProfileDAO == nil {
		return 0, nil
	}
	dataRoot := a.resolveAppPath("data")
	entries, err := os.ReadDir(dataRoot)
	if err != nil {
		return 0, fmt.Errorf("读取 data 目录失败: %w", err)
	}

	reconciled := 0
	for _, entry := range entries {
		folderID := strings.ToLower(strings.TrimSpace(entry.Name()))
		if !entry.IsDir() || uuid.Validate(folderID) != nil {
			continue
		}
		dataDir := filepath.Join(dataRoot, entry.Name())
		if info, statErr := os.Lstat(dataDir); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		pointer, pointerErr := browser.ReadProfileDataPointer(dataDir)
		if pointerErr == nil && !strings.EqualFold(strings.TrimSpace(pointer.ProfileID), folderID) {
			return reconciled, fmt.Errorf("环境数据身份冲突: 文件夹 %s 内记录的是 %s", entry.Name(), pointer.ProfileID)
		}

		a.browserMgr.Mutex.Lock()
		existing := a.browserMgr.Profiles[folderID]
		if existing == nil {
			for profileID, candidate := range a.browserMgr.Profiles {
				if strings.EqualFold(profileID, folderID) {
					existing = candidate
					break
				}
			}
		}
		if existing != nil {
			currentDir := filepath.Clean(a.browserMgr.ResolveUserDataDir(existing))
			if !strings.EqualFold(currentDir, filepath.Clean(dataDir)) {
				if pathExists(currentDir) {
					a.browserMgr.Mutex.Unlock()
					return reconciled, fmt.Errorf("环境 %s 同时对应两个数据目录，已取消自动对齐: %s；%s", folderID, currentDir, dataDir)
				}
				updated := *existing
				updated.UserDataDir = entry.Name()
				updated.UpdatedAt = time.Now().Format(time.RFC3339)
				if err := a.browserMgr.ProfileDAO.Upsert(&updated); err != nil {
					a.browserMgr.Mutex.Unlock()
					return reconciled, fmt.Errorf("对齐环境 %s 数据目录失败: %w", folderID, err)
				}
				a.browserMgr.Profiles[updated.ProfileId] = &updated
				reconciled++
			}
			a.browserMgr.Mutex.Unlock()
			continue
		}

		// Do not invent environments from empty UUID-shaped folders. A pointer or
		// actual Chromium state is required before attaching the existing data.
		if pointerErr != nil && !looksLikeUUIDChromeData(dataDir) {
			a.browserMgr.Mutex.Unlock()
			continue
		}
		recovered := &browser.Profile{
			ProfileId:   folderID,
			ProfileName: "恢复环境-" + folderID[:8],
			UserDataDir: entry.Name(),
			CreatedAt:   time.Now().Format(time.RFC3339),
			UpdatedAt:   time.Now().Format(time.RFC3339),
		}
		if pointerErr == nil {
			recovered.ProfileName = strings.TrimSpace(pointer.ProfileName)
			if recovered.ProfileName == "" {
				recovered.ProfileName = "恢复环境-" + folderID[:8]
			}
			recovered.CoreId = pointer.CoreID
			recovered.FingerprintArgs = append([]string{}, pointer.FingerprintArgs...)
			recovered.ProxyId = pointer.ProxyID
			recovered.LaunchArgs = append([]string{}, pointer.LaunchArgs...)
			recovered.Tags = append([]string{}, pointer.Tags...)
			recovered.Keywords = append([]string{}, pointer.Keywords...)
			recovered.GroupId = pointer.GroupID
			if strings.TrimSpace(pointer.CreatedAt) != "" {
				recovered.CreatedAt = pointer.CreatedAt
			}
			if strings.TrimSpace(pointer.UpdatedAt) != "" {
				recovered.UpdatedAt = pointer.UpdatedAt
			}
		}
		if err := a.browserMgr.ProfileDAO.Upsert(recovered); err != nil {
			a.browserMgr.Mutex.Unlock()
			return reconciled, fmt.Errorf("挂接 UUID 环境 %s 失败: %w", folderID, err)
		}
		a.browserMgr.Profiles[recovered.ProfileId] = recovered
		a.browserMgr.Mutex.Unlock()
		reconciled++
	}
	return reconciled, nil
}

func looksLikeUUIDChromeData(dataDir string) bool {
	for _, path := range []string{
		filepath.Join(dataDir, "Local State"),
		filepath.Join(dataDir, "Default", "Preferences"),
		filepath.Join(dataDir, "Default", "Network", "Cookies"),
		filepath.Join(dataDir, "Default", "Cookies"),
	} {
		if info, err := os.Lstat(path); err == nil && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return true
		}
	}
	return false
}

func pathExists(path string) bool {
	_, err := os.Lstat(strings.TrimSpace(path))
	return err == nil
}
