//go:build !windows

package backend

// Non-Windows: sole-blank handoff is a Windows multi-open feature only.

func prepareEnvironmentsForSyncHandoff(targets []syncHandoffTarget) (applied, skipped, closedTabs int) {
	return 0, 0, 0
}

func clearSyncTabHandoffForProfile(profileID string) {}

func (a *App) FinalizeEnvironmentTabsForUserHandoff() map[string]interface{} {
	return map[string]interface{}{
		"skipped":  true,
		"reason":   "windows_only",
		"profiles": 0,
	}
}

// syncHandoffTarget is declared for non-Windows compile of call sites that
// only exist under windows build tags; keep type in stub to avoid split APIs.
type syncHandoffTarget struct {
	profileID string
	pid       int
	debugPort int
}
