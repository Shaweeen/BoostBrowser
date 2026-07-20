//go:build !windows

package backend

// StopInputSync is a no-op outside Windows. The synchronization engine uses
// Windows hooks and is never started on other platforms, but the shared Wails
// lifecycle still calls this method during panel shutdown.
func (a *App) StopInputSync() error {
	return nil
}
