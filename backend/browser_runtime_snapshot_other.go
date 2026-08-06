//go:build !windows

package backend

func (a *App) persistBrowserRuntimeSnapshotLocked() {}

func (a *App) updateBrowserRuntimeSnapshotEntryLocked(profileID string, pid int, hwnd uintptr) {}

func (a *App) publishProfileRuntimeSnapshotAsync(profileID string, pid int) {}

func (a *App) PrepareWindowSyncRuntimeSnapshot() int { return 0 }
