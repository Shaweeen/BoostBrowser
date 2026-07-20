# BrowserStudio v1.7.17

## Main improvements

- Fixed global Chrome extension deployment and profile binding for the bundled
  extension-compatible Chrome for Testing kernel.
- Improved master-window ownership, mouse/keyboard/wheel synchronization,
  tiled layouts, and taskbar number badges.
- Added one-way batch import for official Rabby, Jupiter, and MetaMask wallet
  extensions with short-lived in-memory sessions and no mnemonic export API.
- Simplified proxy imports, including `host:port:user:password`, subscription
  traffic/expiry metadata, latency validation, health status, and exact
  environment assignment.
- Disabled Google sign-in prompts by default for managed browser profiles.
- Fixed proxy-pool select-all toggling and portable backend test failures.

## Security and integrity

- Restricted update downloads to the stable
  `Shaweeen/BoostBrowser` GitHub Release channel over HTTPS.
- Added strict SHA-256 syntax checks, Windows PE validation, and a one-use
  verified-download session before executable replacement.
- Upgraded vulnerable Go networking and cryptography dependencies; the final
  `govulncheck` result reports zero reachable vulnerabilities.
- Installer process cleanup is now scoped to executables inside the selected
  BrowserStudio installation directory and no longer terminates unrelated
  Chrome, xray, or sing-box processes by image name.
- Release builds now emit per-file SHA-256 files and a machine-readable release
  manifest.

## Compatibility

- Windows 10/11 x64.
- Existing profiles, browser kernels, proxy pools, extensions, and local data
  remain in place during the lightweight in-app upgrade.
- Building from source now requires Go 1.25 or newer and Node.js 20 or newer.
