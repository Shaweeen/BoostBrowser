//go:build !windows

package backend

func (a *App) persistBrowserRuntimeSnapshotLocked() {}

func (a *App) applyBrowserRuntimeSnapshot() int { return 0 }
