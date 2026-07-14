//go:build windows

package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type browserRuntimeSnapshotEntry struct {
	ProfileID string `json:"profileId"`
	PID       int    `json:"pid"`
	DebugPort int    `json:"debugPort"`
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
	snapshot := browserRuntimeSnapshot{UpdatedAt: time.Now(), Entries: make([]browserRuntimeSnapshotEntry, 0)}
	for profileID, profile := range a.browserMgr.Profiles {
		if profile == nil || !profile.Running || profile.Pid <= 0 {
			continue
		}
		snapshot.Entries = append(snapshot.Entries, browserRuntimeSnapshotEntry{
			ProfileID: profileID,
			PID:       profile.Pid,
			DebugPort: profile.DebugPort,
		})
	}
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

// Apply the main client's shared state using only the cheap process-liveness
// check. Window resolution is performed once, in parallel, by GetSyncProfiles.
// Doing it here as well used to duplicate 12 expensive process-tree scans and
// then block the response on a five-second CIM fallback when only one failed.
func (a *App) applyBrowserRuntimeSnapshot() (int, int) {
	if a == nil || a.browserMgr == nil {
		return 0, 0
	}
	data, err := os.ReadFile(a.browserRuntimeSnapshotPath())
	if err != nil {
		return 0, 0
	}
	var snapshot browserRuntimeSnapshot
	if json.Unmarshal(data, &snapshot) != nil || time.Since(snapshot.UpdatedAt) > 7*24*time.Hour {
		return 0, 0
	}

	live := 0
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	for _, entry := range snapshot.Entries {
		profile := a.browserMgr.Profiles[entry.ProfileID]
		if profile == nil || entry.PID <= 0 || !isProcessAlive(entry.PID) {
			continue
		}
		profile.Running = true
		profile.Pid = entry.PID
		profile.DebugPort = entry.DebugPort
		// Input synchronization only needs HWND/PID. Do not serially dial every
		// CDP port on the critical list-loading path.
		profile.DebugReady = entry.DebugPort > 0
		live++
	}
	return live, len(snapshot.Entries)
}
