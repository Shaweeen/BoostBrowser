//go:build windows

package backend

// stopPanelOwnedSync releases the low-level hooks owned by the isolated sync
// panel. The main client never owns this state after v1.7.13.
func stopPanelOwnedSync(a *App) {
	if a == nil || !a.panelMode {
		return
	}
	_ = a.stopInputSyncLocal()
}
