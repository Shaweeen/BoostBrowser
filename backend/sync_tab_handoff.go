//go:build windows

package backend

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"boost-browser/backend/internal/logger"
)

// Sync-start tab handoff (sole about:blank) — once per environment process.
//
//   - First time an env (profileID+pid) joins StartInputSync → collapse to one
//     about:blank, then mark done.
//   - Envs that already completed handoff (user is actively browsing/synced)
//     are never collapsed again when other envs join or sync restarts.
//   - Stop → start yields a new pid → handoff runs once for that new process.
//
// Safety (from v1.7.63): navigate keep-tab to about:blank BEFORE closing
// siblings. Closing Chromium's last page destroys the whole window.

const soleBlankCollapseTimeout = 800 * time.Millisecond

// syncTabHandoffDone keys: "profileID:pid" for environments that already received
// the one-shot sole-blank collapse. Process-local (sync panel process).
var syncTabHandoffDone sync.Map

func syncTabHandoffKey(profileID string, pid int) string {
	return strings.TrimSpace(profileID) + ":" + strconv.Itoa(pid)
}

func isSyncTabHandoffDone(profileID string, pid int) bool {
	if strings.TrimSpace(profileID) == "" || pid <= 0 {
		return false
	}
	_, ok := syncTabHandoffDone.Load(syncTabHandoffKey(profileID, pid))
	return ok
}

func markSyncTabHandoffDone(profileID string, pid int) {
	if strings.TrimSpace(profileID) == "" || pid <= 0 {
		return
	}
	syncTabHandoffDone.Store(syncTabHandoffKey(profileID, pid), true)
}

// clearSyncTabHandoffForProfile drops all handoff marks for a profile (any pid).
// Call when the environment is fully stopped so a future start is treated as new.
func clearSyncTabHandoffForProfile(profileID string) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return
	}
	prefix := profileID + ":"
	syncTabHandoffDone.Range(func(key, _ any) bool {
		if k, ok := key.(string); ok && strings.HasPrefix(k, prefix) {
			syncTabHandoffDone.Delete(key)
		}
		return true
	})
}

// syncHandoffTarget is one running environment considered for sole-blank handoff.
type syncHandoffTarget struct {
	profileID string
	pid       int
	debugPort int
}

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

// prepareEnvironmentsForSyncHandoff collapses only environments that have not
// yet received sole-blank handoff for this process lifetime. Already-handed-off
// envs (user mid-session) are left untouched when new windows join sync.
func prepareEnvironmentsForSyncHandoff(targets []syncHandoffTarget) (applied, skipped, closedTabs int) {
	pending := make([]syncHandoffTarget, 0, len(targets))
	seenPID := map[int]struct{}{}
	for _, t := range targets {
		t.profileID = strings.TrimSpace(t.profileID)
		if t.profileID == "" || t.pid <= 0 || t.debugPort <= 0 {
			continue
		}
		if _, dup := seenPID[t.pid]; dup {
			continue
		}
		seenPID[t.pid] = struct{}{}
		if isSyncTabHandoffDone(t.profileID, t.pid) {
			skipped++
			continue
		}
		pending = append(pending, t)
	}
	if len(pending) == 0 {
		if skipped > 0 {
			logger.New("SyncAPI").Info("同步标签接管：全部参与环境已接管过，跳过关页",
				logger.F("skipped", skipped),
			)
		}
		return 0, skipped, 0
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, t := range pending {
		wg.Add(1)
		go func(target syncHandoffTarget) {
			defer wg.Done()
			n := collapseEnvironmentTabsToSoleAboutBlank(target.debugPort)
			markSyncTabHandoffDone(target.profileID, target.pid)
			mu.Lock()
			closedTabs += n
			applied++
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	logger.New("SyncAPI").Info("同步标签接管：仅对新环境收拢 about:blank",
		logger.F("applied", applied),
		logger.F("skipped_already_handed_off", skipped),
		logger.F("closed_tabs", closedTabs),
	)
	return applied, skipped, closedTabs
}

// FinalizeEnvironmentTabsForUserHandoff is API compatibility.
// Canonical trigger is StartInputSync → prepareEnvironmentsForSyncHandoff.
// Sync panel skips (handoff runs inside startInputSyncLocal).
func (a *App) FinalizeEnvironmentTabsForUserHandoff() map[string]interface{} {
	result := map[string]interface{}{
		"skipped":    false,
		"profiles":   0,
		"closedTabs": 0,
		"applied":    0,
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
	targets := make([]syncHandoffTarget, 0)
	for id, p := range a.browserMgr.Profiles {
		if p == nil || !p.Running || p.DebugPort <= 0 || p.Pid <= 0 {
			continue
		}
		targets = append(targets, syncHandoffTarget{
			profileID: id,
			pid:       p.Pid,
			debugPort: p.DebugPort,
		})
	}
	a.browserMgr.Mutex.Unlock()
	applied, skipped, closed := prepareEnvironmentsForSyncHandoff(targets)
	result["applied"] = applied
	result["skipped_already"] = skipped
	result["profiles"] = applied + skipped
	result["closedTabs"] = closed
	return result
}
