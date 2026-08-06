package backend

// FinalizeEnvironmentTabsForUserHandoff is a no-op API stub.
// Auto sole-blank on sync was removed: it closed users' work tabs when they
// used envs outside sync then later started sync. Keep the binding so older
// clients calling it get a safe, inert response (see DELETION_LEDGER
// CLEAN-063/074/078).
func (a *App) FinalizeEnvironmentTabsForUserHandoff() map[string]interface{} {
	return map[string]interface{}{
		"skipped":  true,
		"reason":   "sync_never_closes_user_tabs",
		"profiles": 0,
		"applied":  0,
	}
}
