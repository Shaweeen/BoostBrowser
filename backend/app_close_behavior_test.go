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

func TestSyncPanelNeverRunsTabHandoffCollapse(t *testing.T) {
	// Prevent panel process from racing main's Target.closeTarget handoff.
	armEnvironmentTabsUserHandoffForProfile("env-a")
	app := NewApp(t.TempDir(), true)
	if !app.panelMode {
		t.Fatal("expected panel-mode app")
	}
	result := app.FinalizeEnvironmentTabsForUserHandoff()
	if result["skipped"] != true {
		t.Fatalf("panel handoff must be skipped, got %#v", result)
	}
	if result["reason"] != "panel_never_owns_tab_handoff" {
		t.Fatalf("unexpected skip reason: %#v", result)
	}
	if !profilePendingTabHandoff("env-a") {
		t.Fatal("panel skip must not consume pending handoff for main process")
	}
	clearEnvironmentTabsUserHandoffForProfile("env-a")
}

func TestTabHandoffIsPerProfileNotGlobal(t *testing.T) {
	// Working env A already handed off; starting env B must not re-arm A.
	clearEnvironmentTabsUserHandoffForProfile("work-a")
	clearEnvironmentTabsUserHandoffForProfile("new-b")
	// Simulate A finished handoff (not pending).
	if profilePendingTabHandoff("work-a") {
		t.Fatal("work-a should not be pending")
	}
	armEnvironmentTabsUserHandoffForProfile("new-b")
	if profilePendingTabHandoff("work-a") {
		t.Fatal("arming new-b must not mark work-a pending")
	}
	if !profilePendingTabHandoff("new-b") {
		t.Fatal("new-b must be pending handoff")
	}
	// take only pending
	ids := takePendingTabHandoffProfiles()
	if len(ids) != 1 || ids[0] != "new-b" {
		t.Fatalf("handoff must only target new-b, got %v", ids)
	}
	if pendingTabHandoffCount() != 0 {
		t.Fatal("pending set must be empty after take")
	}
	// Second finalize is a no-op (no pending).
	app := NewApp(t.TempDir(), false)
	result := app.FinalizeEnvironmentTabsForUserHandoff()
	if result["skipped"] != true {
		t.Fatalf("empty pending must skip: %#v", result)
	}
}
