//go:build windows

package backend

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"boost-browser/backend/internal/logger"
)

// Sync-start tab handoff (sole about:blank) — user-triggered, one-shot per
// StartInputSync. After this, BrowserStudio never closes/creates tabs again
// for that session; extension clicks and new tabs are fully user-owned.
//
// Safety (from v1.7.63): navigate keep-tab to about:blank BEFORE closing
// siblings. Closing Chromium's last page destroys the whole window.

const soleBlankCollapseTimeout = 800 * time.Millisecond

func isBlankOrNewTabURL(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	return u == "about:blank" || u == "about:blank#" ||
		u == "chrome://newtab" || u == "chrome://newtab/" ||
		u == "chrome://new-tab-page" || u == "chrome://new-tab-page/" ||
		strings.HasPrefix(u, "chrome://new-tab-page/")
}

func isExactAboutBlankURL(raw string) bool {
	u := strings.ToLower(strings.TrimSpace(raw))
	return u == "about:blank" || u == "about:blank#"
}

// collapseTabPlan is last-tab-safe: always keep one page, navigate it to blank
// before closing anything else.
type collapseTabPlan struct {
	keepID             string
	navigateKeepBlank  bool
	closeIDs           []string
	createBlankIfEmpty bool
}

func planCollapseToSoleAboutBlank(targets []cdpTarget) collapseTabPlan {
	pages := make([]cdpTarget, 0, len(targets))
	for _, t := range targets {
		if t.ID == "" || !strings.EqualFold(strings.TrimSpace(t.Type), "page") {
			continue
		}
		pages = append(pages, t)
	}
	if len(pages) == 0 {
		return collapseTabPlan{createBlankIfEmpty: true}
	}

	var keep cdpTarget
	var closeIDs []string
	foundBlankLike := false
	for _, t := range pages {
		if isBlankOrNewTabURL(t.URL) {
			if !foundBlankLike {
				keep = t
				foundBlankLike = true
				continue
			}
			closeIDs = append(closeIDs, t.ID)
			continue
		}
		closeIDs = append(closeIDs, t.ID)
	}
	if !foundBlankLike {
		// No blank yet: promote first page and navigate in place. Never close it first.
		keep = pages[0]
		closeIDs = closeIDs[:0]
		for i := 1; i < len(pages); i++ {
			closeIDs = append(closeIDs, pages[i].ID)
		}
		return collapseTabPlan{
			keepID:            keep.ID,
			navigateKeepBlank: true,
			closeIDs:          closeIDs,
		}
	}
	return collapseTabPlan{
		keepID:            keep.ID,
		navigateKeepBlank: !isExactAboutBlankURL(keep.URL),
		closeIDs:          closeIDs,
	}
}

// collapseEnvironmentTabsToSoleAboutBlank leaves exactly one about:blank page.
// One-shot CDP; no watcher, no retry loop, no post-handoff recheck.
func collapseEnvironmentTabsToSoleAboutBlank(debugPort int) int {
	if debugPort <= 0 {
		return 0
	}
	browserWsURL, err := getBrowserWebSocketURL(debugPort)
	if err != nil {
		return 0
	}
	browserClient, err := newRabbyCDPClient(browserWsURL)
	if err != nil {
		return 0
	}
	defer browserClient.close()

	closeTarget := func(id string) error {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("empty target id")
		}
		_, e := browserClient.call("Target.closeTarget", map[string]any{"targetId": id}, soleBlankCollapseTimeout)
		return e
	}
	targets, err := listCDPTargets(debugPort)
	if err != nil {
		return 0
	}
	plan := planCollapseToSoleAboutBlank(targets)
	if plan.createBlankIfEmpty {
		_, _ = browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
		return 0
	}

	// 1) Survivor → about:blank before any sibling close.
	if plan.navigateKeepBlank && plan.keepID != "" {
		var keepTarget cdpTarget
		for _, t := range targets {
			if t.ID == plan.keepID {
				keepTarget = t
				break
			}
		}
		if strings.TrimSpace(keepTarget.WebSocketDebuggerUrl) == "" {
			if all, e := listCDPTargets(debugPort); e == nil {
				for _, t := range all {
					if t.ID == plan.keepID && strings.TrimSpace(t.WebSocketDebuggerUrl) != "" {
						keepTarget = t
						break
					}
				}
			}
		}
		if strings.TrimSpace(keepTarget.WebSocketDebuggerUrl) != "" {
			_, _ = cdpCallTarget(keepTarget, "Page.enable", map[string]any{})
			_, _ = cdpCallTarget(keepTarget, "Page.navigate", map[string]any{"url": "about:blank"})
		} else {
			_, _ = browserClient.call("Target.createTarget", map[string]any{"url": "about:blank"}, 1500*time.Millisecond)
		}
	}

	// 2) Close non-survivor pages only.
	closed := 0
	for _, id := range plan.closeIDs {
		if id == plan.keepID {
			continue
		}
		if closeTarget(id) == nil {
			closed++
		}
	}
	return closed
}

// collapseEnvironmentsToSoleAboutBlankParallel collapses each debug port once.
// Used only from StartInputSync so multi-open stays bounded.
func collapseEnvironmentsToSoleAboutBlankParallel(debugPorts []int) (profiles, closedTabs int) {
	ports := make([]int, 0, len(debugPorts))
	seen := map[int]struct{}{}
	for _, p := range debugPorts {
		if p <= 0 {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		ports = append(ports, p)
	}
	if len(ports) == 0 {
		return 0, 0
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, port := range ports {
		wg.Add(1)
		go func(debugPort int) {
			defer wg.Done()
			n := collapseEnvironmentTabsToSoleAboutBlank(debugPort)
			mu.Lock()
			closedTabs += n
			mu.Unlock()
		}(port)
	}
	wg.Wait()
	return len(ports), closedTabs
}

// prepareEnvironmentsForSyncHandoff is the only post-start tab action: on sync
// start, every participating environment is reduced to one about:blank. After
// return, no tab management runs until the next StartInputSync.
func prepareEnvironmentsForSyncHandoff(masterDebugPort int, followerDebugPorts []int) (profiles, closedTabs int) {
	ports := make([]int, 0, 1+len(followerDebugPorts))
	if masterDebugPort > 0 {
		ports = append(ports, masterDebugPort)
	}
	ports = append(ports, followerDebugPorts...)
	profiles, closedTabs = collapseEnvironmentsToSoleAboutBlankParallel(ports)
	if profiles > 0 {
		logger.New("SyncAPI").Info("同步启动：各环境仅保留一个 about:blank，此后标签由用户完全控制",
			logger.F("profiles", profiles),
			logger.F("closed_tabs", closedTabs),
		)
	}
	return profiles, closedTabs
}

// FinalizeEnvironmentTabsForUserHandoff is API compatibility.
// Canonical trigger is StartInputSync → prepareEnvironmentsForSyncHandoff.
// Sync panel skips (handoff runs inside startInputSyncLocal).
func (a *App) FinalizeEnvironmentTabsForUserHandoff() map[string]interface{} {
	result := map[string]interface{}{
		"skipped":    false,
		"profiles":   0,
		"closedTabs": 0,
	}
	if a != nil && a.panelMode {
		result["skipped"] = true
		result["reason"] = "panel_uses_sync_start_handoff"
		return result
	}
	if a == nil || a.browserMgr == nil {
		result["skipped"] = true
		result["reason"] = "no_app"
		return result
	}
	a.browserMgr.Mutex.Lock()
	ports := make([]int, 0)
	for _, p := range a.browserMgr.Profiles {
		if p == nil || !p.Running || p.DebugPort <= 0 {
			continue
		}
		ports = append(ports, p.DebugPort)
	}
	a.browserMgr.Mutex.Unlock()
	profiles, closed := collapseEnvironmentsToSoleAboutBlankParallel(ports)
	result["profiles"] = profiles
	result["closedTabs"] = closed
	return result
}
