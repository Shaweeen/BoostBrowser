//go:build !windows

package backend

func (a *App) persistBrowserRuntimeSnapshotLocked() {}

func (a *App) PrepareWindowSyncRuntimeSnapshot() int { return 0 }
