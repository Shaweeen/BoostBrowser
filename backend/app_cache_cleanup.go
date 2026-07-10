package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/cachecleanup"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const cacheAutoCleanFixedIntervalDays = 30

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

func (a *App) BrowserCleanCache(includeRunning bool) (*CacheCleanResult, error) {
	if a == nil || a.config == nil || a.browserMgr == nil {
		return nil, fmt.Errorf("应用未完成初始化")
	}
	profiles := a.cacheCleanProfiles()
	result := &CacheCleanResult{ProfilesScanned: len(profiles), CleanedProfiles: []string{}}
	for _, profile := range profiles {
		if profile == nil {
			continue
		}
		if profile.Running && !includeRunning {
			result.SkippedRunning++
			continue
		}
		profileRoot := a.cacheCleanProfileRoot(profile)
		if profileRoot == "" {
			continue
		}
		res, err := cachecleanup.CleanProfileRoot(profileRoot)
		if err != nil {
			result.Errors++
			continue
		}
		if profile.Running {
			_ = a.BrowserClearCookies(profile.ProfileId)
		}
		result.FilesRemoved += res.FilesRemoved
		result.DirsRemoved += res.DirsRemoved
		result.BytesRemoved += res.BytesRemoved
		result.Errors += res.Errors
		if res.FilesRemoved > 0 || res.DirsRemoved > 0 {
			result.ProfilesCleaned++
			result.CleanedProfiles = append(result.CleanedProfiles, strings.TrimSpace(profile.ProfileName))
		}
	}
	a.markCacheCleanedNow()
	result.Message = fmt.Sprintf("已扫描 %d 个环境，清理 %d 个环境，删除 %d 个文件，释放 %.1f MB", result.ProfilesScanned, result.ProfilesCleaned, result.FilesRemoved, float64(result.BytesRemoved)/1024/1024)
	if result.SkippedRunning > 0 {
		result.Message += fmt.Sprintf("；跳过 %d 个运行中的环境", result.SkippedRunning)
	}
	return result, nil
}

func (a *App) BrowserGetCacheCleanSettings() CacheCleanSettings {
	if a == nil || a.config == nil {
		return CacheCleanSettings{IntervalDays: cacheAutoCleanFixedIntervalDays}
	}
	return a.cacheCleanSettings()
}

func (a *App) BrowserSaveCacheCleanSettings(enabled bool) (CacheCleanSettings, error) {
	if a == nil || a.config == nil {
		return CacheCleanSettings{}, fmt.Errorf("应用未完成初始化")
	}
	a.config.Browser.CacheAutoCleanEnabled = enabled
	a.config.Browser.CacheAutoCleanIntervalDays = cacheAutoCleanFixedIntervalDays
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
		return &CacheAutoCleanResult{Ran: false, Reason: "未到30天清理周期"}, nil
	}
	res, err := a.BrowserCleanCache(false)
	if err != nil {
		return nil, err
	}
	return &CacheAutoCleanResult{Ran: true, Result: res}, nil
}

func (a *App) startCacheAutoCleanScheduler() {
	go func() {
		// Give startup/reconciliation a short moment; this is best-effort and skips
		// running profiles, so it will not interrupt active browser windows.
		time.Sleep(5 * time.Second)
		_, _ = a.BrowserRunDueCacheAutoClean()
	}()
}

func (a *App) cacheCleanProfiles() []*browser.Profile {
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	profiles := make([]*browser.Profile, 0, len(a.browserMgr.Profiles))
	for _, profile := range a.browserMgr.Profiles {
		profiles = append(profiles, profile)
	}
	return profiles
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
	settings := CacheCleanSettings{
		AutoCleanEnabled: a.config.Browser.CacheAutoCleanEnabled,
		IntervalDays:     cacheAutoCleanFixedIntervalDays,
		LastCleanAt:      last,
	}
	if parsed, err := time.Parse(time.RFC3339, last); err == nil {
		settings.NextCleanAt = parsed.Add(cacheAutoCleanFixedIntervalDays * 24 * time.Hour).Format(time.RFC3339)
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
	return !now.Before(parsed.Add(cacheAutoCleanFixedIntervalDays * 24 * time.Hour))
}

func (a *App) markCacheCleanedNow() {
	if a == nil || a.config == nil {
		return
	}
	a.config.Browser.CacheLastCleanAt = time.Now().Format(time.RFC3339)
	a.config.Browser.CacheAutoCleanIntervalDays = cacheAutoCleanFixedIntervalDays
	_ = a.config.Save(a.resolveAppPath("config.yaml"))
}
