//go:build !windows

package backend

import "fmt"

func (a *App) PrepareSyncPanelHandoff() (string, error) {
	return "", fmt.Errorf("仅 Windows 支持同步工具")
}
func (a *App) requestMainSyncCollection(bool) SyncSnapshot {
	return SyncSnapshot{Profiles: []SyncProfileInfo{}, Status: map[string]interface{}{}}
}
func (a *App) StopSyncPanelHandoff() {}
