# BrowserStudio

BrowserStudio is a Windows desktop environment manager built with Go, Wails,
React, and TypeScript. It provides isolated browser profiles, bundled Chromium
kernels, proxy routing, automation APIs, and precise multi-window input sync.

## Highlights

- Isolated browser environments with independent profile data
- CloakBrowser and Google Chromium kernel support
- Proxy pools with xray and sing-box bridges
- DPI-aware window tiling and main/follower window synchronization
- Exact mouse button, pointer, keyboard, and high-resolution wheel forwarding
- High-DPI numbered taskbar icons generated from a 3x source canvas
- Provider-based activation design with an upgrade path to signed entitlements
- Per-user NSIS installation and an independent updater

## Repository layout

| Path | Purpose |
| --- | --- |
| `backend/` | Go application services, browser runtime, sync, activation, and updater |
| `frontend/src/` | React and TypeScript desktop interface |
| `scripts/` | Windows PowerShell build, staging, and installer scripts |
| `build/windows/` | Windows manifest, icon, and installer assets |
| `bat/` | Development and recovery helpers |
| `ROADMAP.md` | Planned security, activation, sync, and release improvements |

## Requirements

- Windows 10 or Windows 11, x64
- Go 1.22 or newer
- Node.js 20 or newer
- Wails CLI v2
- NSIS with `makensis.exe`
- A local CloakBrowser kernel at
  `chrome\cloak-146.0.7680.177\chrome.exe` when kernel installation is skipped

Install Wails:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

## Activation input

The private installer seed is never stored in source code. Supply it only as
the `BROWSERSTUDIO_INSTALL_SEED` environment variable or as a GitHub Actions
secret with the same name. The packaged application contains only a one-way
verification value.

For a local PowerShell session:

```powershell
$env:BROWSERSTUDIO_INSTALL_SEED = '<private installer seed>'
```

Do not commit the value to configuration files, scripts, workflow YAML, logs,
or release notes.

## One-command Windows package

From the repository root:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build_windows_selfuse.ps1 -SkipKernelInstall
```

To install or refresh the CloakBrowser kernel during the build:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build_windows_selfuse.ps1
```

To omit the optional local Google Chrome fallback:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build_windows_selfuse.ps1 -SkipKernelInstall -SkipGoogleFallback
```

To include Go tests before packaging:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build_windows_selfuse.ps1 -SkipKernelInstall -RunGoTests
```

## Manual Windows build

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install_cloakbrowser_kernel.ps1

Push-Location .\frontend
npm ci
npm run build
Pop-Location

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build_release.ps1
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build_installer.ps1
```

## Build output

The complete installer is generated at:

```text
build\release\BrowserStudio-Setup-v<version>.exe
```

The lightweight updater assets remain named `boost-browser.exe` and
`boost-browser.exe.sha256` for compatibility with existing installations.

Verify an installer:

```powershell
Get-Item .\build\release\BrowserStudio-Setup-v*.exe |
  Select-Object FullName, Length, LastWriteTime

Get-FileHash .\build\release\BrowserStudio-Setup-v*.exe -Algorithm SHA256
```

## Validation

```powershell
Push-Location .\frontend
npm ci
npm run build
Pop-Location

go test .\backend\internal\activation .\backend\internal\config
go test -c .\backend -o "$env:TEMP\browserstudio-backend-tests.exe"
go test -c . -o "$env:TEMP\browserstudio-main-tests.exe"
```

## Security notes

- An offline verifier can slow casual inspection but cannot provide the same
  security as signed server-issued licenses.
- Production releases should sign the application, updater, installer, and
  entitlement payloads.
- Do not publish browser kernels, profile data, local license files, proxies,
  API keys, or build secrets.

## Upgrade compatibility

The visible product name is BrowserStudio. Selected legacy executable names,
data-path recognition, mutex names, and updater asset names are intentionally
retained so existing installations can upgrade without losing profile data.
