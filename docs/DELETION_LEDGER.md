# BrowserStudio deletion ledger

This ledger makes code removal reversible. A release cleanup is accepted only
when the removed path has no active caller, a tested replacement exists, or the
behavior is intentionally retired.

## Rules

1. Search all tracked source files for callers before deleting an implementation.
2. Preserve or add tests for the replacement behavior before removing old tests.
3. Record the last known revision, reason, replacement, verification, and restore
   command for every material deletion.
4. Restore only the smallest required function or file. Do not revert a later
   release wholesale because that can overwrite user data and unrelated fixes.
5. Large deletions without an entry in this ledger fail the release health check.

## v1.7.33 cleanup

| ID | Removed path | Last known revision | Reason | Replacement | Verification | Precise recovery |
| --- | --- | --- | --- | --- | --- | --- |
| CLEAN-001 | Global and per-profile extension-popup polling loops in `backend/extension_popup_sizer_windows.go` | `v1.7.32` (`134d520`) | Zero active production callers; they duplicated window enumeration, could fight the synchronizer, and kept background listeners alive after startup. | `backend/sync_popup_confinement_windows.go` constrains owned wallet/extension popups only while synchronization is active. | Windows cross-compile and retained popup classification/confinement tests. | Inspect with `git show 134d520:backend/extension_popup_sizer_windows.go`; restore only the required function and its direct dependencies. |
| CLEAN-002 | Off-screen browser and service-worker DevTools restoration watchers | `v1.7.32` (`134d520`) | The browser no longer launches windows off-screen. These recovery loops were legacy compensation and could unexpectedly move user windows. | Bounded `enforceBrowserWindowBounds` during startup plus sync-owned popup confinement. | Windows cross-compile, browser-window startup tests, and synchronization tests. | Inspect with `git show 134d520:backend/extension_popup_sizer_windows.go`; reintroduce behind a focused regression test only. |
| CLEAN-003 | `BOOST_BROWSER_ENABLE_GLOBAL_WINDOW_WATCHERS`, its startup branch, non-Windows stubs, and stale crash-probe comments | `v1.7.32` (`134d520`) | The option activated only the retired duplicate watchers and was disabled by default. | No replacement option is needed; active synchronization owns its listener lifecycle. | Repository symbol scan and full Go tests. | Inspect `main.go`, `backend/extension_popup_sizer_stub.go`, and `backend/app_instance.go` at `134d520`. |
| CLEAN-004 | Unused `ensureDefaultSearchProvider` and `asJSONString` compatibility helpers | `v1.7.32` (`134d520`) | Zero callers; current search-provider setup uses the maintained launch/CDP path. | `seedDefaultSearchEngine` and current core launch helpers. | Repository symbol scan and full Go tests. | Inspect with `git show 134d520:backend/extension_startup_cleanup.go`. |
| CLEAN-005 | Tests that exercised only the retired global/off-screen popup watchers | `v1.7.32` (`134d520`) | They prevented removal of dead code but did not cover the active synchronization path. | Compact tests for sync popup confinement, title classification, main-window exclusion, DevTools classification, and IME exclusion. | Windows test binary compilation and full Go tests. | Inspect with `git show 134d520:backend/extension_popup_sizer_windows_test.go`. |

## v1.7.37 sync collection cleanup

| ID | Removed path | Last known revision | Reason | Replacement | Verification | Precise recovery |
| --- | --- | --- | --- | --- | --- | --- |
| CLEAN-006 | One-second synchronization profile polling plus focus, visibility, and browser lifecycle event refresh paths in `frontend/src/modules/browser/pages/WindowSyncPage.tsx` | `v1.7.36` (`2573373`) | Concurrent automatic refreshes could apply an older PID/window snapshot after the user stopped, reconfigured, closed a window, or selected a new master. The idle panel also scanned window state continuously. | Action-driven `loadProfiles`: initial open, manual refresh, select all, choose master, and start are explicit collection boundaries. `releaseCollectedSyncData` clears the session on stop, reconfigure, and exit. | Frontend production build, full Go tests, Windows cross-compile, and `test_sync_window_collection_is_action_driven`. | Inspect with `git show 2573373:frontend/src/modules/browser/pages/WindowSyncPage.tsx`; restore only a specific event trigger after adding an ordering/cancellation regression test. Do not restore the interval as a group. |
| CLEAN-007 | Throttled asynchronous `syncRuntimeDiscovery` state and `reconcileSyncRuntimeStateAsync` goroutine in `backend/app_sync_api.go` | `v1.7.36` (`2573373`) | The async second scan returned `no_window` first and could mutate profile state after a later user action, creating stale master/follower selections. | `getSyncProfilesLocal` reconciles once synchronously only when explicitly requested; `startInputSyncLocal` performs a final authoritative reconciliation before resolving HWNDs. | Full Go tests, Windows cross-compile, code-health tests, and manual state-transition review. | Inspect with `git show 2573373:backend/app_sync_api.go`; recover the two symbols only if a future non-blocking design carries a generation token and cancellation test. |
