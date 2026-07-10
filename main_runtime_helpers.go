package main

import (
	"context"
	"os"
	"strings"
)

var syncPanelMode = hasCLIArg("--sync-panel") || hasCLIArg("--window-sync-panel")

func hasCLIArg(want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, arg := range os.Args[1:] {
		arg = strings.TrimSpace(arg)
		if arg == want || strings.HasPrefix(arg, want+"=") {
			return true
		}
	}
	return false
}

// takeoverExistingMainInstanceForPostUpdate is a best-effort hook used during
// post-update restarts. The old implementation was platform-specific; keeping
// this function as a safe no-op preserves update startup when no takeover is
// needed while allowing the new binary to compile from a clean checkout.
func takeoverExistingMainInstanceForPostUpdate(appRoot string) {}

// restoreNativeMainWindowBounds restores/centers the main Wails window. Native
// bounds recovery is optional and must never block startup; backend/browser
// window tracking handles browser-profile windows separately.
func restoreNativeMainWindowBounds(ctx context.Context, app *App) {}
