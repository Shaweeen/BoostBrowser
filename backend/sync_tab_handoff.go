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

// Optional sole-blank utilities (retained for API / diagnostics only).
//
// Product policy (v1.7.80+, AdsPower/MoreLogin-aligned):
//   - StartInputSync does NOT call collapse — never wipe user work tabs.
//   - Extensions are Profile-native; hot start keeps last session tabs.
//   - prepareEnvironmentsForSyncHandoff remains available but is not wired to
//     StartInputSync. Safety: navigate keep-tab to about:blank BEFORE closing
//     siblings if ever re-enabled behind an explicit user setting.

const soleBlankCollapseTimeout = 800 * time.Millisecond

// syncTabHandoffDone keys: "profileID:pid" for environments that already received
// (or claimed) the one-shot sole-blank collapse. Process-local (sync panel).
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

// claimSyncTabHandoff marks the env as handed-off atomically. Returns true only
// for the first claim so concurrent StartInputSync cannot collapse twice.
func claimSyncTabHandoff(profileID string, pid int) bool {
	if strings.TrimSpace(profileID) == "" || pid <= 0 {
		return false
	}
	_, loaded := syncTabHandoffDone.LoadOrStore(syncTabHandoffKey(profileID, pid), true)
	return !loaded
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
	// Only top-level page tabs. Never touch service_worker, background_page,
	// shared_worker, etc. — those keep wallet/extension runtimes alive.
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

// prepareEnvironmentsForSyncHandoff collapses only brand-new env processes.
// Already-handed-off envs are never re-managed — user tabs/extensions stay as-is.
// Claim-before-collapse guarantees “execute once then stop” under concurrent sync.
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
		// Atomic claim: first claim wins; already claimed → skip forever for this pid.
		if !claimSyncTabHandoff(t.profileID, t.pid) {
			skipped++
			continue
		}
		pending = append(pending, t)
	}
	if len(pending) == 0 {
		if skipped > 0 {
			logger.New("SyncAPI").Info("同步标签接管：已接管环境保持用户标签不动",
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
			// One-shot collapse for this new process only. No retry watcher after.
			n := collapseEnvironmentTabsToSoleAboutBlank(target.debugPort)
			mu.Lock()
			closedTabs += n
			applied++
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	logger.New("SyncAPI").Info("同步标签接管：仅新开环境收拢一次 about:blank，随后完全由用户控制",
		logger.F("applied", applied),
		logger.F("skipped_user_owned", skipped),
		logger.F("closed_tabs", closedTabs),
	)
	return applied, skipped, closedTabs
}

// FinalizeEnvironmentTabsForUserHandoff is a no-op API stub.
// Auto sole-blank on sync was removed: it closed users' work tabs when they
// used envs outside sync then later started sync. Keep binding for old clients.
func (a *App) FinalizeEnvironmentTabsForUserHandoff() map[string]interface{} {
	return map[string]interface{}{
		"skipped":  true,
		"reason":   "sync_never_closes_user_tabs",
		"profiles": 0,
		"applied":  0,
	}
}
