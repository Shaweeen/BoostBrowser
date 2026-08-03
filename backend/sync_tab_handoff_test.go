//go:build windows

package backend

import "testing"

func TestPlanCollapseToSoleAboutBlankKeepsOneBlankClosesRest(t *testing.T) {
	plan := planCollapseToSoleAboutBlank([]cdpTarget{
		{ID: "a", Type: "page", URL: "about:blank"},
		{ID: "b", Type: "page", URL: "https://example.com/"},
		{ID: "c", Type: "page", URL: "chrome-extension://mm/home.html"},
		{ID: "svc", Type: "service_worker", URL: "chrome-extension://mm/sw.js"},
	})
	if plan.keepID != "a" || plan.navigateKeepBlank {
		t.Fatalf("keep blank as-is: %+v", plan)
	}
	if len(plan.closeIDs) != 2 {
		t.Fatalf("close only extra pages: %+v", plan.closeIDs)
	}
	for _, id := range plan.closeIDs {
		if id == "a" || id == "svc" {
			t.Fatalf("must not close keep or service worker: %s", id)
		}
	}
}

func TestPlanCollapseToSoleAboutBlankNavigatesFirstWhenNoBlank(t *testing.T) {
	plan := planCollapseToSoleAboutBlank([]cdpTarget{
		{ID: "m", Type: "page", URL: "chrome-extension://mm/home.html"},
		{ID: "w", Type: "page", URL: "https://dapp.test/"},
	})
	if plan.keepID != "m" || !plan.navigateKeepBlank {
		t.Fatalf("promote first page and navigate: %+v", plan)
	}
	if len(plan.closeIDs) != 1 || plan.closeIDs[0] != "w" {
		t.Fatalf("close only siblings: %+v", plan)
	}
	// Never empty keep — last-tab kill would destroy the window.
	if plan.keepID == "" {
		t.Fatal("keepID required")
	}
}

func TestPlanCollapseToSoleAboutBlankNeverClosesLastTabFirst(t *testing.T) {
	plan := planCollapseToSoleAboutBlank([]cdpTarget{
		{ID: "only", Type: "page", URL: "https://example.com/"},
	})
	if plan.keepID != "only" || !plan.navigateKeepBlank {
		t.Fatalf("single content tab must navigate, not close: %+v", plan)
	}
	if len(plan.closeIDs) != 0 {
		t.Fatalf("must not schedule close of last tab: %+v", plan.closeIDs)
	}
}

func TestPlanCollapseRewritesNewTabPageInPlace(t *testing.T) {
	plan := planCollapseToSoleAboutBlank([]cdpTarget{
		{ID: "ntp", Type: "page", URL: "chrome://new-tab-page/"},
		{ID: "ext", Type: "page", URL: "chrome-extension://x/popup.html"},
	})
	if plan.keepID != "ntp" || !plan.navigateKeepBlank {
		t.Fatalf("NTP should become about:blank in place: %+v", plan)
	}
	if len(plan.closeIDs) != 1 || plan.closeIDs[0] != "ext" {
		t.Fatalf("close extension sibling: %+v", plan)
	}
}

func TestFinalizeHandoffPanelSkipsOwnCollapse(t *testing.T) {
	panel := NewApp(t.TempDir(), true)
	result := panel.FinalizeEnvironmentTabsForUserHandoff()
	if result["skipped"] != true {
		t.Fatalf("panel must skip list-style handoff: %#v", result)
	}
	if result["reason"] != "panel_uses_sync_start_handoff" {
		t.Fatalf("unexpected reason: %#v", result)
	}
}

func TestSyncTabHandoffDoneIsPerProfilePid(t *testing.T) {
	// Isolate from other tests by using unique ids.
	id := "handoff-test-profile-xyz"
	clearSyncTabHandoffForProfile(id)
	if isSyncTabHandoffDone(id, 1001) {
		t.Fatal("expected not done")
	}
	markSyncTabHandoffDone(id, 1001)
	if !isSyncTabHandoffDone(id, 1001) {
		t.Fatal("same pid must be done")
	}
	// New browser process (new pid) must hand off again.
	if isSyncTabHandoffDone(id, 1002) {
		t.Fatal("new pid must not inherit handoff mark")
	}
	clearSyncTabHandoffForProfile(id)
	if isSyncTabHandoffDone(id, 1001) {
		t.Fatal("clear must drop all pids for profile")
	}
}

func TestPrepareHandoffSkipsAlreadyDone(t *testing.T) {
	idA, idB := "handoff-a-skip", "handoff-b-new"
	clearSyncTabHandoffForProfile(idA)
	clearSyncTabHandoffForProfile(idB)
	markSyncTabHandoffDone(idA, 2001)
	// Dead debug ports: collapse no-ops, but applied/skipped accounting still runs.
	applied, skipped, _ := prepareEnvironmentsForSyncHandoff([]syncHandoffTarget{
		{profileID: idA, pid: 2001, debugPort: 59991}, // already done → skip
		{profileID: idB, pid: 2002, debugPort: 59992}, // new → applied + mark
	})
	if skipped != 1 {
		t.Fatalf("expected 1 skip for already-handed-off: applied=%d skipped=%d", applied, skipped)
	}
	if applied != 1 {
		t.Fatalf("expected 1 apply for new env: applied=%d", applied)
	}
	if !isSyncTabHandoffDone(idB, 2002) {
		t.Fatal("new env must be marked after attempt")
	}
	// Second sync with same set: both skipped.
	applied2, skipped2, _ := prepareEnvironmentsForSyncHandoff([]syncHandoffTarget{
		{profileID: idA, pid: 2001, debugPort: 59991},
		{profileID: idB, pid: 2002, debugPort: 59992},
	})
	if applied2 != 0 || skipped2 != 2 {
		t.Fatalf("second pass must skip all: applied=%d skipped=%d", applied2, skipped2)
	}
	clearSyncTabHandoffForProfile(idA)
	clearSyncTabHandoffForProfile(idB)
}
