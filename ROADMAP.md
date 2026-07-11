# BrowserStudio Roadmap

## Current hardening

- Preserve raw mouse-wheel deltas and DPI-aware coordinates in window sync.
- Keep browser-window discovery restricted to real Chrome top-level windows.
- Render numbered taskbar icons from a 3x source canvas with high-DPI ICO layers.
- Keep legacy data paths and updater markers compatible during the brand migration.

## Next modules

- Replace the offline installer activation provider with signed entitlements or a server-backed provider.
- Add per-device activation lifecycle, revocation, expiry and offline grace periods.
- Move input synchronization from global hooks to a serialized event worker with latency telemetry.
- Add configurable pointer sampling profiles for large window groups.
- Sign the application, updater, installer and activation payloads in CI.

