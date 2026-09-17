# BrowserStudio v1.7.140

## Fix: Refresh detects visible environments

- Synchronization refresh now uses the process that is actually listening on
  each environment's live DevTools port as the Chromium process-tree root.
  This prevents inherited command-line flags on renderer/utility child
  processes from making visible environments appear as “no window”.
- Main window resolution remains fail-closed: wallet, OAuth, IME and generic
  popup windows are not substituted for the environment browser frame.

## Resource and data safety

- The listener lookup is part of the existing explicit refresh/start discovery
  pass only. No timer, background process scan, persisted PID/HWND snapshot or
  continuous polling is added.
- Browser profiles, Cookies, extension and wallet stores, login state, zoom,
  layout and other user synchronization settings are not modified.
