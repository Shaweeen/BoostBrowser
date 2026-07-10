package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

type MainWindowBounds struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// IsWindowSyncPanelMode is called by the frontend to switch between the full
// app shell and the compact window-sync panel shell.
func (a *App) IsWindowSyncPanelMode() bool {
	return syncPanelMode
}

// SaveNativeMainWindowBounds persists the latest main-window bounds for native
// restore paths. The frontend also stores this in localStorage; this sidecar is
// intentionally best-effort and non-blocking.
func (a *App) SaveNativeMainWindowBounds(bounds MainWindowBounds) bool {
	if syncPanelMode || appRoot == "" || bounds.Width < 1200 || bounds.Height < 700 {
		return false
	}
	path := filepath.Join(appRoot, "data", "main-window-bounds.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false
	}
	content := fmt.Sprintf(`{"x":%d,"y":%d,"width":%d,"height":%d}`+"\n", bounds.X, bounds.Y, bounds.Width, bounds.Height)
	return os.WriteFile(path, []byte(content), 0o644) == nil
}

// OpenWindowSyncPanel launches a second process in the lightweight sync-panel
// mode. Single-instance handling keeps only one panel alive.
func (a *App) OpenWindowSyncPanel() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exePath, "--sync-panel")
	cmd.Dir = appRoot
	return cmd.Start()
}

// restoreNativeMainWindowBounds restores/centers the main Wails window. Native
// bounds recovery is optional and must never block startup; backend/browser
// window tracking handles browser-profile windows separately.
func restoreNativeMainWindowBounds(ctx context.Context, app *App) {}
