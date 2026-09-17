# BrowserStudio v1.7.140

## Main-client-owned synchronization windows

- Opening the synchronization tool now makes the main client collect the
  currently running environments as one complete PID, HWND and DevTools-port
  generation before the assistant starts.
- Refresh performs the same one-shot collection again and atomically replaces
  the assistant's available list. There is no background polling, periodic
  SQLite reload, repeated Chromium scan, or automatic target replacement.
- Start sync, layout, add follower and remove follower consume the collected
  generation. Invalid or closed selected windows fail closed and ask the user
  to refresh instead of silently dropping or shifting targets.
- An active session keeps aligned master/follower HWND and debug-port data, so
  refresh updates the available list without changing the running session.

## Existing synchronization and data safety

- Mouse, keyboard, address-bar/navigation, absolute zoom, extension toolbar,
  extension popup/secondary menu mapping and grid/horizontal/vertical layout
  continue through the existing input synchronizer.
- Browser profiles, Cookies, extension stores, wallet stores and login state
  are not deleted or rewritten by this change.

## Windows acceptance before publication

- Open 2, 10 and 20 environments, open the sync tool, refresh after adding and
  stopping environments, then verify master/follower input, zoom, toolbar and
  popup actions, layouts, stop/restart sync, and stable idle CPU usage.
