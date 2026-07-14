//go:build !windows

package backend

func (a *App) persistBrowserRuntimeSnapshotLocked() {}

func (a *App) applyBrowserRuntimeSnapshot() (int, int) { return 0, 0 }
