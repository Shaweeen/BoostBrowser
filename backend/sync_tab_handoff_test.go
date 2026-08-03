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
