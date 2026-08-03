//go:build !windows

package backend

// Non-Windows: sole-blank handoff is a Windows multi-open feature only.

func prepareEnvironmentsForSyncHandoff(masterDebugPort int, followerDebugPorts []int) (profiles, closedTabs int) {
	return 0, 0
}

func (a *App) FinalizeEnvironmentTabsForUserHandoff() map[string]interface{} {
	return map[string]interface{}{
		"skipped":  true,
		"reason":   "windows_only",
		"profiles": 0,
	}
}
