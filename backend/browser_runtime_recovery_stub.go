//go:build !windows

package backend

func (a *App) startBrowserRuntimeReconciler()        {}
func (a *App) reconcileBrowserRuntimeStateOnce() int { return 0 }

// RefreshBrowserRuntimeState is a no-op outside Windows (runtime takeover is a
// Windows watchdog feature). Kept so the Wails binding compiles everywhere.
func (a *App) RefreshBrowserRuntimeState() bool { return false }
