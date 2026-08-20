# BrowserStudio v1.7.134

## Multi-environment sync stability

- A transient or slow Windows process scan no longer marks a still-live environment as stopped.
- The sync assistant refreshes live runtime discovery while open and preserves the active master/follower session.
- The master window now mirrors ordinary pages, password login, OAuth consent, wallet connect/sign, and wallet extension popup navigation/input.
- Existing coordinate mapping, DPI scaling, click delivery, mouse movement coalescing, vertical/horizontal wheel handling, and fallback Win32 dispatch remain enabled.

## User data

This release does not delete or rewrite Cookies, login state, wallet vaults, extension storage, or browser profile data. Extension startup optimization is limited to launch/runtime discovery; explicit extension assignment remains unchanged.
