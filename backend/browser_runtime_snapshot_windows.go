//go:build windows

package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	path := a.browserRuntimeSnapshotPath()
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

func (a *App) applyBrowserRuntimeSnapshotData(snapshot browserRuntimeSnapshot) (int, int) {
	if a == nil || a.browserMgr == nil {
		return 0, 0
	}
	live := 0
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	// The panel's browser manager is only a configuration reader. Clear every
	// process-local runtime value before applying the main client's current
	// list so an older panel scan can never override a later Start/Stop state.
	for _, profile := range a.browserMgr.Profiles {
		if profile == nil {
			continue
		}
		profile.Running = false
		profile.Pid = 0
		profile.DebugPort = 0
		profile.DebugReady = false
	}
	for _, entry := range snapshot.Entries {
		if entry.PID <= 0 {
			continue
		}
		// Accept snapshot row if process is alive OR published HWND is still a
		// real window. Strict isProcessAlive-only dropped many multi-open envs
		// under load and left the assistant stuck around ~30 while 100+ ran.
		hwndOK := entry.HWND != 0 && isWindow(windows.HWND(entry.HWND))
		if !isProcessAlive(entry.PID) && !hwndOK {
			continue
		}
		profile := a.browserMgr.Profiles[entry.ProfileID]
		if profile == nil {
			// Snapshot can reference profiles the panel map has not loaded yet
			// (panel opened before batch create finished). Surface them anyway.
			name := strings.TrimSpace(entry.ProfileName)
			if name == "" {
				name = entry.ProfileID
			}
			profile = &BrowserProfile{
				ProfileId:   entry.ProfileID,
				ProfileName: name,
			}
			a.browserMgr.Profiles[entry.ProfileID] = profile
		}
		profile.Running = true
		profile.Pid = entry.PID
		profile.DebugPort = entry.DebugPort
		profile.DebugReady = entry.DebugPort > 0
		if strings.TrimSpace(profile.ProfileName) == "" && strings.TrimSpace(entry.ProfileName) != "" {
			profile.ProfileName = entry.ProfileName
		}
		live++
	}
	return live, len(snapshot.Entries)
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
