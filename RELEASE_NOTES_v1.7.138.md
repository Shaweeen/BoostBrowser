# BrowserStudio v1.7.138

## Live environment-owned window synchronization

- Opening the synchronization assistant and using its refresh action now discover
  the current Chromium environments from live processes, live DevTools ports,
  and current main browser frames only. Persisted runtime HWND/PID snapshots are
  no longer read or written by synchronization.
- The user-selected open environment set is the only sync membership source.
  A missing real main frame is skipped until refresh; wallet, OAuth, and other
  generic Chromium popups are never used as a fallback target.
- Custom rows and columns are exact and reject insufficient capacity. Automatic
  layout derives its dimensions from the actual selected count, desktop work
  area, and median live-window aspect instead of fixed count bands or a fixed
  1400×900 assumption.
- Mouse, wallet toolbar/popup actions, address-bar typing, committed navigation,
  and Ctrl+wheel zoom continue to map each selected follower through its current
  client/render geometry and DPI. Zoom converges to the master’s absolute scale.

## Data safety

- Browser profiles, Cookies, extension stores, wallet stores, and login state
  are not deleted or rewritten by this release.
