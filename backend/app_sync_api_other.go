//go:build !windows

package backend

// StopInputSync is a no-op outside Windows. The synchronization engine uses
// Windows hooks and is never started on other platforms, but the shared Wails
// lifecycle still calls this method during panel shutdown.
func (a *App) StopInputSync() error {
	return nil
}

// RefreshSyncSnapshot is a no-op outside Windows (the assistant is a Windows
// feature). Kept so the Wails binding compiles on every platform.
func (a *App) RefreshSyncSnapshot() SyncSnapshot {
	return SyncSnapshot{
		Profiles:   []SyncProfileInfo{},
		Status:     map[string]interface{}{},
		Generation: 0,
	}
}

// AddFollowerToSync is a no-op outside Windows.
func (a *App) AddFollowerToSync(profileId string) error {
	return nil
}

// RemoveFollowerFromSync is a no-op outside Windows.
func (a *App) RemoveFollowerFromSync(profileId string) error {
	return nil
}

// GetSyncFollowerIds returns nil outside Windows.
func (a *App) GetSyncFollowerIds() []string {
	return nil
}
