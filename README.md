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

## Windows system components

### Components required to run BrowserStudio

Install the Microsoft Edge WebView2 Runtime and the Microsoft Visual C++ x64
runtime before launching BrowserStudio. Open PowerShell or Windows Terminal and
run:

```powershell
winget install --id Microsoft.EdgeWebView2Runtime --exact --accept-package-agreements --accept-source-agreements
winget install --id Microsoft.VCRedist.2015+.x64 --exact --accept-package-agreements --accept-source-agreements
```

Restart Windows after installing or updating the Visual C++ runtime. WebView2
is used by the Wails desktop interface; the Visual C++ runtime is required by
native browser and proxy components.

If `winget` is unavailable, install **App Installer** from Microsoft Store, or
download the Evergreen WebView2 Runtime and Visual C++ 2015-2022 x64
Redistributable directly from Microsoft.

### Components required to build the installer

Install Git, Go, Node.js LTS, and NSIS:

```powershell
winget install --id Git.Git --exact --accept-package-agreements --accept-source-agreements
winget install --id GoLang.Go --exact --accept-package-agreements --accept-source-agreements
winget install --id OpenJS.NodeJS.LTS --exact --accept-package-agreements --accept-source-agreements
winget install --id NSIS.NSIS --exact --accept-package-agreements --accept-source-agreements
```

Close and reopen PowerShell so the updated `PATH` is loaded. Then install the
Wails CLI:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

If `wails` is not found after installation, add the Go binary directory to the
current user `PATH`:

```powershell
$GoBin = Join-Path $HOME 'go\bin'
$UserPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($UserPath -split ';') -notcontains $GoBin) {
  [Environment]::SetEnvironmentVariable('Path', "$UserPath;$GoBin", 'User')
}
$env:Path += ";$GoBin"
```

Verify every required build component:

```powershell
git --version
go version
node --version
npm --version
wails version
& 'C:\Program Files (x86)\NSIS\makensis.exe' /VERSION
```

If NSIS was installed under `C:\Program Files\NSIS`, use this verification
command instead:

```powershell
& 'C:\Program Files\NSIS\makensis.exe' /VERSION
```

### Optional automated prerequisite check

The self-use build script checks Node.js, npm, Go, Wails, NSIS, and the selected
browser kernel before building. Run it from the repository root after the
components above are installed:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\build_windows_selfuse.ps1 -SkipKernelInstall
```

## Installer activation

Packaging does not require an activation environment variable. The generated
NSIS installer displays an activation page before any application files are
installed. A valid installation key is required to continue. Validation is
performed by a small embedded checker; only a one-way verifier is shipped and
the entered key is deleted from the installer temporary directory immediately
after validation.

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
