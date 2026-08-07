package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/cachecleanup"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	// 自动清理周期最低 30 天（每月一次），不允许更频繁；上限 90 天（季度）。
	cacheAutoCleanDefaultIntervalDays = 30
	cacheAutoCleanMinIntervalDays     = 30
	cacheAutoCleanMaxIntervalDays     = 90
	cacheAutoCleanInitialDelay        = 2 * time.Minute
	cacheAutoCleanPollInterval        = 6 * time.Hour
)

type CacheCleanResult struct {
	ProfilesScanned int      `json:"profilesScanned"`
	ProfilesCleaned int      `json:"profilesCleaned"`
	FilesRemoved    int      `json:"filesRemoved"`
	DirsRemoved     int      `json:"dirsRemoved"`
	BytesRemoved    int64    `json:"bytesRemoved"`
	Errors          int      `json:"errors"`
	SkippedRunning  int      `json:"skippedRunning"`
	CleanedProfiles []string `json:"cleanedProfiles"`
	Message         string   `json:"message"`
}

type CacheCleanSettings struct {
	AutoCleanEnabled bool   `json:"autoCleanEnabled"`
	IntervalDays     int    `json:"intervalDays"`
	LastCleanAt      string `json:"lastCleanAt,omitempty"`
	NextCleanAt      string `json:"nextCleanAt,omitempty"`
}

type CacheAutoCleanResult struct {
	Ran    bool              `json:"ran"`
	Reason string            `json:"reason,omitempty"`
	Result *CacheCleanResult `json:"result,omitempty"`
}

func (a *App) BrowserCleanCache(_ bool) (*CacheCleanResult, error) {
	if a == nil || a.config == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("应用未完成初始化")
	}

	a.browserMgr.Mutex.Lock()
	profileIDs := make([]string, 0, len(a.browserMgr.Profiles))
	for profileID := range a.browserMgr.Profiles {
		profileIDs = append(profileIDs, profileID)
	}
	a.browserMgr.Mutex.Unlock()

	result := &CacheCleanResult{ProfilesScanned: len(profileIDs), CleanedProfiles: []string{}}
	for _, profileID := range profileIDs {
		// Hold the same state lock used by environment startup until this
		// profile's cleanup is complete. A stopped profile therefore cannot
		// transition to running between the state check and filesystem removal.
		a.browserMgr.Mutex.Lock()
		profile := a.browserMgr.Profiles[profileID]
		if profile == nil {
			a.browserMgr.Mutex.Unlock()
			continue
		}
		if profile.Running || profile.Pid > 0 {
			result.SkippedRunning++
			a.browserMgr.Mutex.Unlock()
			continue
		}
		profileRoot := a.cacheCleanProfileRoot(profile)
		if profileRoot == "" {
			a.browserMgr.Mutex.Unlock()
			continue
		}
		profileName := strings.TrimSpace(profile.ProfileName)
		res, err := cachecleanup.CleanProfileRoot(profileRoot)
		a.browserMgr.Mutex.Unlock()
		if err != nil {
			result.Errors++
			continue
		}
		result.FilesRemoved += res.FilesRemoved
		result.DirsRemoved += res.DirsRemoved
		result.BytesRemoved += res.BytesRemoved
		result.Errors += res.Errors
		if res.FilesRemoved > 0 || res.DirsRemoved > 0 {
			result.ProfilesCleaned++
			result.CleanedProfiles = append(result.CleanedProfiles, profileName)
		}
	}
	// If any environment was running, keep the cleanup due. The scheduler will
	// retry later instead of postponing that environment for another full week.
	if result.SkippedRunning == 0 {
		a.markCacheCleanedNow()
	}
	result.Message = fmt.Sprintf("已扫描 %d 个环境，清理 %d 个环境，删除 %d 个文件，释放 %.1f MB", result.ProfilesScanned, result.ProfilesCleaned, result.FilesRemoved, float64(result.BytesRemoved)/1024/1024)
	if result.SkippedRunning > 0 {
		result.Message += fmt.Sprintf("；跳过 %d 个运行中的环境", result.SkippedRunning)
	}
	return result, nil
}

func (a *App) BrowserGetCacheCleanSettings() CacheCleanSettings {
	if a == nil || a.config == nil {
		return CacheCleanSettings{IntervalDays: cacheAutoCleanDefaultIntervalDays}
	}
	return a.cacheCleanSettings()
}

// sanitizeCacheAutoCleanIntervalDays 归一化用户选择的清理周期：
// 最低 30 天（每月一次，已删除每周 7 天选项），上限 90 天（季度）。
func sanitizeCacheAutoCleanIntervalDays(intervalDays int) int {
	if intervalDays < cacheAutoCleanMinIntervalDays {
		return cacheAutoCleanMinIntervalDays
	}
	if intervalDays > cacheAutoCleanMaxIntervalDays {
		return cacheAutoCleanMaxIntervalDays
	}
	return intervalDays
}

func (a *App) cacheAutoCleanIntervalDays() int {
	interval := 0
	if a != nil && a.config != nil {
		interval = a.config.Browser.CacheAutoCleanIntervalDays
	}
	return sanitizeCacheAutoCleanIntervalDays(interval)
}

// BrowserSaveCacheCleanSettings 保存缓存自动清理设置。enabled 控制是否开启；
// intervalDays 为清理周期（7=每周，30=每月），仅 enabled 时生效。
func (a *App) BrowserSaveCacheCleanSettings(enabled bool, intervalDays int) (CacheCleanSettings, error) {
	if a == nil || a.config == nil {
		return CacheCleanSettings{}, fmt.Errorf("应用未完成初始化")
	}
	a.config.Browser.CacheAutoCleanEnabled = enabled
	a.config.Browser.CacheAutoCleanIntervalDays = sanitizeCacheAutoCleanIntervalDays(intervalDays)
	if err := a.config.Save(a.resolveAppPath("config.yaml")); err != nil {
		return CacheCleanSettings{}, err
	}
	return a.cacheCleanSettings(), nil
}

func (a *App) BrowserRunDueCacheAutoClean() (*CacheAutoCleanResult, error) {
	if a == nil || a.config == nil {
		return &CacheAutoCleanResult{Ran: false, Reason: "应用未完成初始化"}, nil
	}
	if !a.config.Browser.CacheAutoCleanEnabled {
		return &CacheAutoCleanResult{Ran: false, Reason: "未开启自动清理"}, nil
	}
	if !a.cacheAutoCleanDue(time.Now()) {
		return &CacheAutoCleanResult{Ran: false, Reason: fmt.Sprintf("未到 %d 天清理周期", a.cacheAutoCleanIntervalDays())}, nil
	}
	res, err := a.BrowserCleanCache(false)
	if err != nil {
		return nil, err
	}
	return &CacheAutoCleanResult{Ran: true, Result: res}, nil
}

func (a *App) startCacheAutoCleanScheduler() {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		// Do not overlap Wails/profile/database startup. Subsequent checks are
		// inexpensive and cleanup itself only touches stopped environments.
		timer := time.NewTimer(cacheAutoCleanInitialDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		_, _ = a.BrowserRunDueCacheAutoClean()

		ticker := time.NewTicker(cacheAutoCleanPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = a.BrowserRunDueCacheAutoClean()
			}
		}
	}()
}

func (a *App) cacheCleanProfileRoot(profile *browser.Profile) string {
	userDataDir := strings.TrimSpace(profile.UserDataDir)
	if userDataDir == "" {
		userDataDir = strings.TrimSpace(profile.ProfileId)
	}
	if userDataDir == "" {
		return ""
	}
	if filepath.IsAbs(userDataDir) {
		return userDataDir
	}
	root := strings.TrimSpace(a.config.Browser.UserDataRoot)
	if root == "" {
		root = "data"
	}
	return a.resolveAppPath(filepath.Join(root, userDataDir))
}

func (a *App) cacheCleanSettings() CacheCleanSettings {
	last := strings.TrimSpace(a.config.Browser.CacheLastCleanAt)
	interval := a.cacheAutoCleanIntervalDays()
	settings := CacheCleanSettings{
		AutoCleanEnabled: a.config.Browser.CacheAutoCleanEnabled,
		IntervalDays:     interval,
		LastCleanAt:      last,
	}
	if parsed, err := time.Parse(time.RFC3339, last); err == nil {
		settings.NextCleanAt = parsed.Add(time.Duration(interval) * 24 * time.Hour).Format(time.RFC3339)
	}
	return settings
}

func (a *App) cacheAutoCleanDue(now time.Time) bool {
	last := strings.TrimSpace(a.config.Browser.CacheLastCleanAt)
	if last == "" {
		return true
	}
	parsed, err := time.Parse(time.RFC3339, last)
	if err != nil {
		return true
	}
	interval := a.cacheAutoCleanIntervalDays()
	return !now.Before(parsed.Add(time.Duration(interval) * 24 * time.Hour))
}

func (a *App) markCacheCleanedNow() {
	if a == nil || a.config == nil {
		return
	}
	a.config.Browser.CacheLastCleanAt = time.Now().Format(time.RFC3339)
	// 保留用户选择的周期，不再回写固定 7 天。
	_ = a.config.Save(a.resolveAppPath("config.yaml"))
}
