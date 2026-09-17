# BrowserStudio v1.7.139

## Synchronization stability

- Starting synchronization now fails closed if any selected follower is no
  longer running, has no current main browser frame, or resolves to a duplicate
  window. It never silently starts a smaller, different follower set.
- A refresh-time repair retains the profile-to-HWND association even when an
  earlier follower has closed, preventing a later follower from shifting into
  the wrong environment slot.
- Adding or removing one follower during an active session no longer scans and
  rebuilds every existing follower target. Existing active HWND/debug-port
  pairs remain in place; only the user-selected membership change is applied.

## Data and resource safety

- Live process/window discovery remains action-bound: open, Refresh, Start and
  explicit layout/membership actions. No background window scanner or stored
  PID/HWND snapshot path is introduced.
- Browser profiles, Cookies, extensions, wallet stores, login state, layout
  preferences, zoom and synchronization settings are not deleted or rewritten.
