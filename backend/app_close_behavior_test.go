package backend

import (
	"boost-browser/backend/internal/browser"
	"boost-browser/backend/internal/config"
	"context"
	goruntime "runtime"
	"testing"
)

func TestPlatformSupportsTrayCloseFlowForOS(t *testing.T) {
	if !platformSupportsTrayCloseFlowForOS("windows") {
		t.Fatal("expected Windows to keep tray close flow enabled")
	}
	if platformSupportsTrayCloseFlowForOS("linux") {
		t.Fatal("expected Linux to skip tray close flow")
	}
}

func TestShouldBlockClose_NonWindowsDoesNotIntercept(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows keeps the tray-based close confirmation flow")
	}

	app := NewApp("")
	if ShouldBlockClose(app, context.Background()) {
		t.Fatal("expected non-Windows close to proceed without interception")
	}
}

func TestQuitAppOnlyKeepsTrackedBrowsers(t *testing.T) {
	app := NewApp("")
	app.browserMgr = browser.NewManager(config.DefaultConfig(), "")
	app.browserMgr.Profiles = map[string]*BrowserProfile{
		"profile-1": {
			ProfileId: "profile-1",
			Running:   true,
		},
	}
	app.browserMgr.BrowserProcesses["profile-1"] = nil

	app.QuitAppOnly()

	if !app.forceQuit {
		t.Fatal("expected QuitAppOnly to set forceQuit")
	}
	if app.quitMode != quitModeAppOnly {
		t.Fatalf("expected quitModeAppOnly, got %v", app.quitMode)
	}
	if app.shouldStopRuntimeServicesOnShutdown() {
		t.Fatal("expected app-only quit to skip runtime service shutdown")
	}
	if _, ok := app.browserMgr.BrowserProcesses["profile-1"]; !ok {
		t.Fatal("expected tracked browser to remain untouched before process shutdown")
	}
	if !app.browserMgr.Profiles["profile-1"].Running {
		t.Fatal("expected app-only quit to keep running profile state intact")
	}
}

func TestForceQuitStopsTrackedBrowsers(t *testing.T) {
	app := NewApp("")
	app.browserMgr = browser.NewManager(config.DefaultConfig(), "")
	app.browserMgr.Profiles = map[string]*BrowserProfile{
		"profile-1": {
			ProfileId: "profile-1",
			Running:   true,
		},
	}
	app.browserMgr.BrowserProcesses["profile-1"] = nil

	app.ForceQuit()

	if !app.forceQuit {
		t.Fatal("expected ForceQuit to set forceQuit")
	}
	if app.quitMode != quitModeFull {
		t.Fatalf("expected quitModeFull, got %v", app.quitMode)
	}
	if !app.shouldStopRuntimeServicesOnShutdown() {
		t.Fatal("expected full quit to stop runtime services")
	}
	if _, ok := app.browserMgr.BrowserProcesses["profile-1"]; ok {
		t.Fatal("expected ForceQuit to clear tracked browser processes")
	}
	if app.browserMgr.Profiles["profile-1"].Running {
		t.Fatal("expected ForceQuit to mark the profile as stopped")
	}
}

func TestSyncPanelShutdownNeverStopsSharedBrowserRuntimes(t *testing.T) {
	app := NewApp(t.TempDir(), true)
	if app.shouldStopRuntimeServicesOnShutdown() {
		t.Fatal("sync panel shutdown must not stop or rewrite shared browser runtimes")
	}
}

func TestFinalizeEnvironmentTabsHandoffPanelDelegatesToSyncStart(t *testing.T) {
	// Canonical sole-blank handoff is StartInputSync; panel list API skips on Windows.
	// Non-Windows stub also reports skipped (feature is Windows multi-open only).
	panel := NewApp(t.TempDir(), true)
	panelResult := panel.FinalizeEnvironmentTabsForUserHandoff()
	if panelResult["skipped"] != true {
		t.Fatalf("panel must skip list-style handoff: %#v", panelResult)
	}
	app := NewApp(t.TempDir(), false)
	result := app.FinalizeEnvironmentTabsForUserHandoff()
	// Windows main with zero running profiles: profiles=0; non-Windows: skipped stub.
	if result["skipped"] != true && result["profiles"] != 0 {
		t.Fatalf("idle handoff must be empty or skipped: %#v", result)
	}
}
