//go:build windows

package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/windows"
)

type browserRuntimeSnapshotEntry struct {
	ProfileID   string `json:"profileId"`
	ProfileName string `json:"profileName"`
	PID         int    `json:"pid"`
	DebugPort   int    `json:"debugPort"`
	HWND        int64  `json:"hwnd"`
	WindowPID   int    `json:"windowPid"`
}

type browserRuntimeSnapshot struct {
	UpdatedAt time.Time                     `json:"updatedAt"`
	Entries   []browserRuntimeSnapshotEntry `json:"entries"`
}

func (a *App) browserRuntimeSnapshotPath() string {
	return a.resolveAppPath(filepath.Join("data", "browser-runtime.json"))
}

// The caller already owns browserMgr.Mutex. Only the main client publishes;
// the independent sync panel is a validated reader.
func (a *App) persistBrowserRuntimeSnapshotLocked() {
	if a == nil || a.panelMode || a.browserMgr == nil {
		return
	}
	rootPIDs := make([]int, 0)
	for _, profile := range a.browserMgr.Profiles {
		if profile != nil && profile.Running && profile.Pid > 0 {
			rootPIDs = append(rootPIDs, profile.Pid)
		}
	}
	resolvedWindows := findProcessTreeWindows(rootPIDs)
	snapshot := browserRuntimeSnapshot{UpdatedAt: time.Now(), Entries: make([]browserRuntimeSnapshotEntry, 0)}
	for profileID, profile := range a.browserMgr.Profiles {
		if profile == nil || !profile.Running || profile.Pid <= 0 {
			continue
		}
		hwnd := resolvedWindows[profile.Pid]
		snapshot.Entries = append(snapshot.Entries, browserRuntimeSnapshotEntry{
			ProfileID:   profileID,
			ProfileName: profile.ProfileName,
			PID:         profile.Pid,
			DebugPort:   profile.DebugPort,
			HWND:        int64(hwnd),
			WindowPID:   int(windowPID(hwnd)),
		})
	}
	sort.Slice(snapshot.Entries, func(i, j int) bool {
		return snapshot.Entries[i].ProfileID < snapshot.Entries[j].ProfileID
	})
	writeBrowserRuntimeSnapshotFile(a.browserRuntimeSnapshotPath(), snapshot)
}

// writeBrowserRuntimeSnapshotFile atomically writes the registry file.
func writeBrowserRuntimeSnapshotFile(path string, snapshot browserRuntimeSnapshot) {
	if path == "" {
		return
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		_ = os.Rename(tmp, path)
	}
}

// updateBrowserRuntimeSnapshotEntryLocked refreshes one environment's registry
// row (hwnd==0 removes it) without re-scanning the whole system. Caller must
// hold browserMgr.Mutex. The registry file is tiny (atomic tmp+rename) and the
// read retry is bounded (≈15ms worst case), so holding the manager lock for
// this short write is acceptable and keeps the read-modify-write serialized.
func (a *App) updateBrowserRuntimeSnapshotEntryLocked(profileID string, pid int, hwnd uintptr) {
	if a == nil || a.panelMode || a.browserMgr == nil {
		return
	}
	profile, ok := a.browserMgr.Profiles[profileID]
	if !ok || profile == nil {
		return
	}
	snapshot, _ := a.readBrowserRuntimeSnapshot()
	entries := make([]browserRuntimeSnapshotEntry, 0, len(snapshot.Entries)+1)
	for _, e := range snapshot.Entries {
		if e.ProfileID != profileID {
			entries = append(entries, e)
		}
	}
	if hwnd != 0 && profile.Running && pid > 0 {
		entries = append(entries, browserRuntimeSnapshotEntry{
			ProfileID:   profileID,
			ProfileName: profile.ProfileName,
			PID:         pid,
			DebugPort:   profile.DebugPort,
			HWND:        int64(hwnd),
			WindowPID:   int(windowPID(windows.HWND(hwnd))),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ProfileID < entries[j].ProfileID
	})
	writeBrowserRuntimeSnapshotFile(a.browserRuntimeSnapshotPath(), browserRuntimeSnapshot{UpdatedAt: time.Now(), Entries: entries})
}

// publishProfileRuntimeSnapshotAsync records one environment's verified main
// frame in the window-sync registry shortly after start, off the start critical
// path. This is the authoritative “runtime tag” the assistant and tile prefer.
// The main frame can appear a few hundred ms after the browser process spawns,
// so the lookup retries briefly instead of silently dropping the entry.
func (a *App) publishProfileRuntimeSnapshotAsync(profileID string, pid int) {
	if a == nil || a.panelMode || profileID == "" || pid <= 0 {
		return
	}
	go func() {
		defer func() { _ = recover() }()
		var hwnd windows.HWND
		for attempt := 0; attempt < 4; attempt++ {
			if hwnd = findMainEnvironmentBrowserWindow(pid); hwnd != 0 {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if hwnd == 0 {
			return
		}
		a.browserMgr.Mutex.Lock()
		defer a.browserMgr.Mutex.Unlock()
		a.updateBrowserRuntimeSnapshotEntryLocked(profileID, pid, uintptr(hwnd))
	}()
}

func (a *App) readBrowserRuntimeSnapshot() (browserRuntimeSnapshot, bool) {
	path := a.browserRuntimeSnapshotPath()
	for attempt := 0; attempt < 3; attempt++ {
		data, err := os.ReadFile(path)
		if err == nil {
			var snapshot browserRuntimeSnapshot
			if json.Unmarshal(data, &snapshot) == nil && time.Since(snapshot.UpdatedAt) <= 7*24*time.Hour {
				return snapshot, true
			}
		}
		if attempt < 2 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	return browserRuntimeSnapshot{}, false
}

// PrepareWindowSyncRuntimeSnapshot is invoked by the main client immediately
// before it launches the assistant. The main environment list is the sole
// runtime owner; the assistant only consumes this snapshot.
func (a *App) PrepareWindowSyncRuntimeSnapshot() int {
	if a == nil || a.panelMode || a.browserMgr == nil {
		return 0
	}
	a.browserMgr.Mutex.Lock()
	a.persistBrowserRuntimeSnapshotLocked()
	count := 0
	missingHWND := 0
	for _, profile := range a.browserMgr.Profiles {
		if profile == nil || !profile.Running || profile.Pid <= 0 {
			continue
		}
		count++
	}
	// Re-resolve when windows are still settling after multi-open. Dense batches
	// (50–200) need a longer settle; first EnumWindows often misses late frames.
	path := a.browserRuntimeSnapshotPath()
	if data, err := os.ReadFile(path); err == nil {
		var snap browserRuntimeSnapshot
		if json.Unmarshal(data, &snap) == nil {
			for _, entry := range snap.Entries {
				if entry.PID > 0 && (entry.HWND == 0 || !isWindow(windows.HWND(entry.HWND))) {
					missingHWND++
				}
			}
		}
	}
	needRetry := count > 0 && (missingHWND > 0 || count >= 40)
	if needRetry {
		a.browserMgr.Mutex.Unlock()
		settle := 180 * time.Millisecond
		if count >= 40 {
			settle = 350 * time.Millisecond
		}
		if count >= 100 {
			settle = 500 * time.Millisecond
		}
		time.Sleep(settle)
		a.browserMgr.Mutex.Lock()
		a.persistBrowserRuntimeSnapshotLocked()
	}
	a.browserMgr.Mutex.Unlock()
	return count
}

func validRuntimeSnapshotWindow(entry browserRuntimeSnapshotEntry) windows.HWND {
	hwnd := windows.HWND(entry.HWND)
	if hwnd == 0 || !isWindow(hwnd) {
		return 0
	}
	if entry.WindowPID > 0 && int(windowPID(hwnd)) != entry.WindowPID {
		return 0
	}
	return hwnd
}
