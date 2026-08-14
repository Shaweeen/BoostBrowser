# build_installer.ps1
# Build full self-use NSIS installer for BrowserStudio.
# Produces a private full installer by default, or a redistributable manager
# installer with -ManagerOnly.

param([switch]$ManagerOnly)

$ErrorActionPreference = 'Stop'

$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

function Require-Path([string]$Path, [string]$Message) {
    if (-not (Test-Path -LiteralPath $Path)) { throw $Message }
}

function Get-MicrosoftPrerequisite([string]$Url, [string]$Path, [string]$Label) {
    $valid = $false
    if (Test-Path -LiteralPath $Path) {
        $signature = Get-AuthenticodeSignature -LiteralPath $Path
        $valid = $signature.Status -eq 'Valid' -and $signature.SignerCertificate.Subject -like '*Microsoft Corporation*'
    }
    if ($valid) {
        Write-Host "   $Label already cached and signed" -ForegroundColor Green
        return
    }

    Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Path) | Out-Null
    # Windows PowerShell 5.1 defaults can fail TLS negotiation to Microsoft CDNs.
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls11 -bor [Net.SecurityProtocolType]::Tls
    } catch {}

    $lastError = $null
    for ($attempt = 1; $attempt -le 4; $attempt++) {
        try {
            Write-Host "   Downloading $Label (attempt $attempt/4) ..." -ForegroundColor Cyan
            # curl.exe is more reliable than Invoke-WebRequest on flaky links / China networks.
            $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
            if ($curl) {
                & curl.exe -L --retry 3 --retry-delay 2 --connect-timeout 30 --max-time 300 -o $Path $Url
                if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $Path)) {
                    throw "curl.exe failed with exit $LASTEXITCODE"
                }
            } else {
                Invoke-WebRequest -Uri $Url -OutFile $Path -UseBasicParsing -TimeoutSec 300
            }
            if (-not (Test-Path -LiteralPath $Path) -or (Get-Item -LiteralPath $Path).Length -lt 1024) {
                throw "Downloaded file missing or too small: $Path"
            }
            $signature = Get-AuthenticodeSignature -LiteralPath $Path
            if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notlike '*Microsoft Corporation*') {
                Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
                throw "$Label did not have a valid Microsoft Authenticode signature"
            }
            Write-Host "   $Label ready" -ForegroundColor Green
            return
        } catch {
            $lastError = $_
            Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
            if ($attempt -lt 4) {
                Start-Sleep -Seconds (3 * $attempt)
            }
        }
    }

    throw @"
Failed to download $Label after 4 attempts.
URL: $Url
Cache path: $Path
Last error: $lastError

Manual fix (then re-run build_installer.ps1):
  1) Download the file in a browser or:
       curl.exe -L -o `"$Path`" `"$Url`"
  2) Confirm the file is Microsoft-signed.
  3) Re-run only:
       powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build_installer.ps1 -ManagerOnly
"@
}

function Copy-Dir([string]$Source, [string]$Destination) {
    if (Test-Path -LiteralPath $Destination) {
        Remove-Item -LiteralPath $Destination -Recurse -Force -ErrorAction SilentlyContinue
    }
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Destination) | Out-Null
    Copy-Item -LiteralPath $Source -Destination $Destination -Recurse -Force
}

function Escape-Nsis([string]$Value) {
    return $Value.Replace('$', '$$').Replace('`', '``')
}

function Add-NsisDir([string]$Dir, [string]$RelPath, $Out) {
    if ($RelPath -eq '') {
        $Out.Add('SetOutPath "$INSTDIR"') | Out-Null
    } else {
        $Out.Add('SetOutPath "$INSTDIR\' + (Escape-Nsis $RelPath) + '"') | Out-Null
    }

    Get-ChildItem -LiteralPath $Dir -File -Force | Sort-Object Name | ForEach-Object {
        $Out.Add('File "' + (Escape-Nsis $_.FullName) + '"') | Out-Null
    }
    Get-ChildItem -LiteralPath $Dir -Directory -Force | Sort-Object Name | ForEach-Object {
        $childRel = if ($RelPath -eq '') { $_.Name } else { $RelPath + '\' + $_.Name }
        Add-NsisDir $_.FullName $childRel $Out
    }
}

function New-BrandBitmap([string]$Path, [int]$Width, [int]$Height, [bool]$Header, [string]$Version) {
    Add-Type -AssemblyName System.Drawing
    $bmp = New-Object System.Drawing.Bitmap $Width, $Height
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.SmoothingMode = [System.Drawing.Drawing2D.SmoothingMode]::AntiAlias
    $rect = New-Object System.Drawing.Rectangle 0,0,$Width,$Height
    $c1 = [System.Drawing.Color]::FromArgb(18, 28, 48)
    $c2 = [System.Drawing.Color]::FromArgb(42, 92, 170)
    $brush = New-Object System.Drawing.Drawing2D.LinearGradientBrush $rect,$c1,$c2,45
    $g.FillRectangle($brush, $rect)
    $brush.Dispose()
    $accent = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(90, 200, 255))
    $white  = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(245, 250, 255))
    $muted  = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(175, 205, 235))
    if ($Header) {
        $font  = New-Object System.Drawing.Font 'Segoe UI', 10, ([System.Drawing.FontStyle]::Bold)
        $font2 = New-Object System.Drawing.Font 'Segoe UI', 7
        $g.DrawString('BrowserStudio', $font, $white, 12, 7)
        $g.DrawString("v$Version", $font2, $muted, 12, 29)
        $g.FillEllipse($accent, $Width-44, 10, 24, 24)
        $font.Dispose(); $font2.Dispose()
    } else {
        $font  = New-Object System.Drawing.Font 'Segoe UI', 18, ([System.Drawing.FontStyle]::Bold)
        $font2 = New-Object System.Drawing.Font 'Segoe UI', 8
        $g.DrawString('Browser', $font, $white, 18, 32)
        $g.DrawString('Studio', $font, $white, 18, 60)
        $g.DrawString('Fingerprint browser', $font2, $muted, 20, 108)
        $g.DrawString("v$Version", $font2, $muted, 20, 128)
        $g.FillEllipse($accent, 102, 180, 34, 34)
        $g.FillEllipse($accent, 122, 205, 16, 16)
        $font.Dispose(); $font2.Dispose()
    }
    $accent.Dispose(); $white.Dispose(); $muted.Dispose(); $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Bmp)
    $bmp.Dispose()
}

$wailsJson = Get-Content "$RepoRoot\wails.json" -Raw | ConvertFrom-Json
$Version = $wailsJson.info.productVersion
if (-not $Version) { throw 'Missing info.productVersion in wails.json' }
Write-Host "==> Version: v$Version" -ForegroundColor Cyan

$ReleaseDir = "$RepoRoot\build\release"
$BoostExe = "$ReleaseDir\boost-browser.exe"
$UpdaterExe = "$ReleaseDir\updater.exe"
$ActivationCheckExe = "$ReleaseDir\activation-check.exe"
$Edition = if ($ManagerOnly) { 'Manager' } else { 'Private' }
$ProductName = if ($ManagerOnly) { 'BrowserStudio Manager' } else { 'BrowserStudio' }
$InstallDirName = if ($ManagerOnly) { 'BrowserStudio Manager' } else { 'BrowserStudio' }
$UninstallKeyName = if ($ManagerOnly) { 'BrowserStudioManager' } else { 'BrowserStudio' }
$Stage = "C:\Temp\BrowserStudio_${Edition}_installer_staging"
$Publish = "$RepoRoot\publish\output"
$NsiPath = "$RepoRoot\publish\boost-browser-installer.nsi"
$NshPath = "$RepoRoot\publish\boost_nsis_files.nsh"
$OutExe = if ($ManagerOnly) { "$ReleaseDir\BrowserStudio-Manager-Setup-v$Version.exe" } else { "$ReleaseDir\BrowserStudio-Private-Setup-v$Version.exe" }
$Icon = "$RepoRoot\build\windows\icon.ico"
$SidebarBmp = "$RepoRoot\publish\boost_sidebar.bmp"
$HeaderBmp = "$RepoRoot\publish\boost_header.bmp"
$PrerequisiteDir = "$RepoRoot\build\cache\prerequisites"
$WebView2Bootstrapper = "$PrerequisiteDir\MicrosoftEdgeWebview2Setup.exe"
$VCRedistX64 = "$PrerequisiteDir\VC_redist.x64.exe"
$ScopedProcessCleanup = "$RepoRoot\scripts\close_browserstudio_processes.ps1"

$AssetRoot = if ($env:BOOST_KERNEL_SRC) { $env:BOOST_KERNEL_SRC } else { $RepoRoot }
$CloakKernelSrc = "$AssetRoot\chrome\cloak-146.0.7680.177"
$GoogleKernelSrc = "$AssetRoot\chrome\google-148.0.7778.167"
$GoogleKernelCompatMarker = "$GoogleKernelSrc\chrome-for-testing.marker"
$BundleGoogleKernel = (-not $ManagerOnly) -and (Test-Path -LiteralPath "$GoogleKernelSrc\chrome.exe") -and (Test-Path -LiteralPath $GoogleKernelCompatMarker)
$GoogleKernelCleanupLine = if ($BundleGoogleKernel) { '  RMDir /r "`$INSTDIR\chrome\google-148.0.7778.167"' } else { '  ; Preserve any existing optional Google 148 kernel.' }
$BinSrc = "$AssetRoot\bin"
# Optional helper extension is intentionally not staged for the self-use clean
# build. Users requested no default/search helper extension in packaged installs.
$ConfigSrc = if ($ManagerOnly) { "$RepoRoot\config.public.yaml" } else { "$RepoRoot\config.yaml" }
$AppIconSrc = if (Test-Path -LiteralPath "$AssetRoot\app.ico") { "$AssetRoot\app.ico" } else { "$RepoRoot\build\windows\icon.ico" }
$AppPngSrc = if (Test-Path -LiteralPath "$AssetRoot\app.png") { "$AssetRoot\app.png" } else { "$RepoRoot\build\appicon.png" }
Require-Path $BoostExe "Missing $BoostExe. Run scripts\build_release.ps1 first."
Require-Path $UpdaterExe "Missing $UpdaterExe. Run scripts\build_release.ps1 first."
Require-Path $ActivationCheckExe "Missing $ActivationCheckExe. Run scripts\build_release.ps1 first."
if (-not $ManagerOnly) {
    Require-Path "$CloakKernelSrc\chrome.exe" "Missing CloakBrowser kernel: $CloakKernelSrc\chrome.exe"
}
Require-Path $Icon "Missing icon: $Icon"
Require-Path $ScopedProcessCleanup "Missing scoped process cleanup helper: $ScopedProcessCleanup"
New-Item -ItemType Directory -Force -Path $Publish | Out-Null
New-Item -ItemType Directory -Force -Path $ReleaseDir | Out-Null
New-Item -ItemType Directory -Force -Path $PrerequisiteDir | Out-Null

Write-Host "==> [1/7] Preparing signed Microsoft runtime installers" -ForegroundColor Yellow
Get-MicrosoftPrerequisite 'https://go.microsoft.com/fwlink/p/?LinkId=2124703' $WebView2Bootstrapper 'Microsoft Edge WebView2 Runtime bootstrapper'
Get-MicrosoftPrerequisite 'https://aka.ms/vs/17/release/vc_redist.x64.exe' $VCRedistX64 'Microsoft Visual C++ x64 runtime'

Write-Host "==> Asset root: $AssetRoot" -ForegroundColor Cyan
Write-Host "==> [2/7] Staging files to $Stage" -ForegroundColor Yellow
Remove-Item -LiteralPath $Stage -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $Stage | Out-Null

Copy-Item -LiteralPath $BoostExe -Destination "$Stage\boost-browser.exe" -Force
Copy-Item -LiteralPath $UpdaterExe -Destination "$Stage\updater.exe" -Force
# config.yaml is mutable user state. It is installed separately only when the
# destination does not already exist, so a full installer can safely be used as
# an in-place repair/upgrade.
if (Test-Path -LiteralPath $AppIconSrc) { Copy-Item -LiteralPath $AppIconSrc -Destination $Stage -Force }
if (Test-Path -LiteralPath $AppPngSrc) { Copy-Item -LiteralPath $AppPngSrc -Destination $Stage -Force }
if (-not $ManagerOnly -and (Test-Path -LiteralPath $BinSrc)) { Copy-Dir $BinSrc "$Stage\bin" }
# Helper extension is intentionally not bundled in the clean self-use package.

if (-not $ManagerOnly) {
    New-Item -ItemType Directory -Force -Path "$Stage\chrome" | Out-Null
    Copy-Dir $CloakKernelSrc "$Stage\chrome\cloak-146.0.7680.177"
    if ($BundleGoogleKernel) {
        Copy-Dir $GoogleKernelSrc "$Stage\chrome\google-148.0.7778.167"
    } else {
        Write-Host "Optional extension-compatible Chrome fallback missing; skipped: $GoogleKernelSrc" -ForegroundColor Yellow
    }
}
# Never package a data directory. Existing browser profiles, Cookies and wallet
# extension state must remain owned by the installed client.

Get-ChildItem $Stage -Recurse -File -Force | Where-Object {
    $_.Name -in @('LOCK','LOG','LOG.old') -or $_.Name -like '*.tmp'
} | Remove-Item -Force -ErrorAction SilentlyContinue

$stageBytes = (Get-ChildItem $Stage -Recurse -File -Force | Measure-Object -Property Length -Sum).Sum
$stageFiles = (Get-ChildItem $Stage -Recurse -File -Force | Measure-Object).Count
Write-Host ("   Stage: {0:N0} files, {1:N1} MB" -f $stageFiles, ($stageBytes / 1MB)) -ForegroundColor Green

Write-Host '==> [3/7] Generating NSIS bitmaps' -ForegroundColor Yellow
New-BrandBitmap $SidebarBmp 164 314 $false $Version
New-BrandBitmap $HeaderBmp 150 57 $true $Version

Write-Host '==> [4/7] Generating NSIS file manifest' -ForegroundColor Yellow
$out = New-Object System.Collections.Generic.List[string]
Add-NsisDir $Stage '' $out
Set-Content -Path $NshPath -Value $out -Encoding Unicode
Write-Host ("   Wrote {0:N0} NSIS lines" -f $out.Count) -ForegroundColor Green

Write-Host '==> [5/7] Generating NSIS script' -ForegroundColor Yellow
$nsi = @"
Unicode True
!define PRODUCT_NAME "$ProductName"
!define PRODUCT_EXE "boost-browser.exe"
!define PRODUCT_VERSION "$Version"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\$UninstallKeyName"
!define APP_ICON "$Icon"
!define SIDEBAR_BMP "$SidebarBmp"
!define HEADER_BMP "$HeaderBmp"

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "nsDialogs.nsh"

Name "`${PRODUCT_NAME} `${PRODUCT_VERSION}"
OutFile "$OutExe"
InstallDir "`$LOCALAPPDATA\Programs\$InstallDirName"
InstallDirRegKey HKCU "`${UNINSTALL_KEY}" "InstallLocation"
RequestExecutionLevel user
SetCompressor zlib
Icon "`${APP_ICON}"
UninstallIcon "`${APP_ICON}"
!define MUI_ICON "`${APP_ICON}"
!define MUI_UNICON "`${APP_ICON}"
!define MUI_WELCOMEFINISHPAGE_BITMAP "`${SIDEBAR_BMP}"
!define MUI_HEADERIMAGE
!define MUI_HEADERIMAGE_BITMAP "`${HEADER_BMP}"
!define MUI_HEADERIMAGE_UNBITMAP "`${HEADER_BMP}"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "`$INSTDIR\`${PRODUCT_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Start BrowserStudio"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
Page custom ActivationPage ActivationPageLeave
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

Var ActivationDialog
Var ActivationInput

; Keep mutable state attached to the installation that created it. The current
; product key is already handled by InstallDirRegKey. When that selected root
; is empty (for example, a Manager install following a legacy BrowserStudio
; install), inspect only earlier client-owned uninstall records. Never scan a
; drive or guess a machine-specific path. More than one valid legacy root is
; deliberately left for the user to choose on the directory page.
Function ResolveExistingDataRoot
  IfFileExists "`$INSTDIR\data\app.db" keep_selected_root
  IfFileExists "`$INSTDIR\data\*\Default\*.*" keep_selected_root

  StrCpy `$R0 ""
  StrCpy `$R1 0

  ReadRegStr `$R2 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\BrowserStudio" "InstallLocation"
  StrCmp `$R2 "" check_legacy_boostbrowser
  StrCmp `$R2 `$INSTDIR check_legacy_boostbrowser
  IfFileExists "`$R2\data\app.db" legacy_browserstudio_valid
  IfFileExists "`$R2\data\*\Default\*.*" legacy_browserstudio_valid check_legacy_boostbrowser
legacy_browserstudio_valid:
  StrCpy `$R0 `$R2
  IntOp `$R1 `$R1 + 1

check_legacy_boostbrowser:
  ReadRegStr `$R2 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\BoostBrowser" "InstallLocation"
  StrCmp `$R2 "" choose_unique_legacy_root
  StrCmp `$R2 `$INSTDIR choose_unique_legacy_root
  IfFileExists "`$R2\data\app.db" legacy_boostbrowser_valid
  IfFileExists "`$R2\data\*\Default\*.*" legacy_boostbrowser_valid choose_unique_legacy_root
legacy_boostbrowser_valid:
  StrCpy `$R0 `$R2
  IntOp `$R1 `$R1 + 1

choose_unique_legacy_root:
  IntCmp `$R1 1 use_unique_legacy_root keep_selected_root keep_selected_root
use_unique_legacy_root:
  StrCpy `$INSTDIR `$R0
  DetailPrint "Reusing BrowserStudio data root registered by an earlier client: `$INSTDIR"
keep_selected_root:
FunctionEnd

Function .onInit
  Call ResolveExistingDataRoot
  InitPluginsDir
  SetOutPath `$PLUGINSDIR
  File /oname=activation-check.exe "$ActivationCheckExe"
  File /oname=MicrosoftEdgeWebview2Setup.exe "$WebView2Bootstrapper"
  File /oname=VC_redist.x64.exe "$VCRedistX64"
  File /oname=close-browserstudio-processes.ps1 "$ScopedProcessCleanup"
FunctionEnd

Function un.onInit
  InitPluginsDir
  SetOutPath `$PLUGINSDIR
  File /oname=close-browserstudio-processes.ps1 "$ScopedProcessCleanup"
FunctionEnd

Function ActivationPage
  nsDialogs::Create 1018
  Pop `$ActivationDialog
  `$`{If} `$ActivationDialog == error
    Abort
  `$`{EndIf}
  !insertmacro MUI_HEADER_TEXT "Activate BrowserStudio" "Enter a valid installation key to continue."
  `$`{NSD_CreateLabel} 0 8u 100% 24u "Installation key"
  Pop `$0
  `$`{NSD_CreatePassword} 0 34u 100% 14u ""
  Pop `$ActivationInput
  `$`{NSD_SetFocus} `$ActivationInput
  nsDialogs::Show
FunctionEnd

Function ActivationPageLeave
  `$`{NSD_GetText} `$ActivationInput `$0
  StrCmp `$0 "" activation_failed
  FileOpen `$1 "`$PLUGINSDIR\activation.input" w
  FileWrite `$1 `$0
  FileClose `$1
  ExecWait '"`$PLUGINSDIR\activation-check.exe" "`$PLUGINSDIR\activation.input" "`$PLUGINSDIR\activation.marker"' `$2
  Delete "`$PLUGINSDIR\activation.input"
  IntCmp `$2 0 activation_ok activation_failed activation_failed
activation_failed:
  MessageBox MB_ICONSTOP|MB_OK "Invalid installation key. Installation cannot continue."
  Abort
activation_ok:
FunctionEnd

Function CloseBoostProcesses
  IfFileExists "`$INSTDIR" 0 done
  ExecWait '"`$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "`$PLUGINSDIR\close-browserstudio-processes.ps1" -InstallRoot "`$INSTDIR"' `$0
  IntCmp `$0 0 done
  MessageBox MB_ICONSTOP|MB_OK "BrowserStudio processes are still running. Close BrowserStudio and its managed browser windows, then retry."
  Abort
done:
FunctionEnd

Function un.CloseBoostProcesses
  IfFileExists "`$INSTDIR" 0 un_done
  ExecWait '"`$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "`$PLUGINSDIR\close-browserstudio-processes.ps1" -InstallRoot "`$INSTDIR"' `$0
  IntCmp `$0 0 un_done
  MessageBox MB_ICONSTOP|MB_OK "BrowserStudio processes are still running. Close BrowserStudio and its managed browser windows, then retry."
  Abort
un_done:
FunctionEnd

Function EnsureVCRuntime
  SetRegView 64
  ClearErrors
  ReadRegDWORD `$0 HKLM "SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64" "Installed"
  IfErrors vc_try_32
  IntCmp `$0 1 vc_try_32 vc_done vc_done
vc_try_32:
  SetRegView 32
  ClearErrors
  ReadRegDWORD `$0 HKLM "SOFTWARE\Microsoft\VisualStudio\14.0\VC\Runtimes\x64" "Installed"
  IfErrors vc_install
  IntCmp `$0 1 vc_install vc_done vc_done
vc_install:
  DetailPrint "Installing Microsoft Visual C++ x64 Runtime ..."
  ExecWait '"`$PLUGINSDIR\VC_redist.x64.exe" /install /quiet /norestart' `$1
  IntCmp `$1 0 vc_done
  IntCmp `$1 3010 vc_done
  IntCmp `$1 1638 vc_done
  MessageBox MB_ICONSTOP|MB_OK "Microsoft Visual C++ x64 Runtime installation failed (exit `$1)."
  Abort
vc_done:
  SetRegView 32
FunctionEnd

Function EnsureWebView2Runtime
  StrCpy `$0 ""
  SetRegView 64
  ReadRegStr `$0 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  StrCmp `$0 "" webview_try_hklm_32 webview_done
webview_try_hklm_32:
  SetRegView 32
  ReadRegStr `$0 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  StrCmp `$0 "" webview_try_hkcu_64 webview_done
webview_try_hkcu_64:
  SetRegView 64
  ReadRegStr `$0 HKCU "SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  StrCmp `$0 "" webview_try_hkcu_32 webview_done
webview_try_hkcu_32:
  SetRegView 32
  ReadRegStr `$0 HKCU "SOFTWARE\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  StrCmp `$0 "" webview_install webview_done
webview_install:
  DetailPrint "Installing Microsoft Edge WebView2 Runtime ..."
  ExecWait '"`$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install' `$1
  IntCmp `$1 0 webview_done
  MessageBox MB_ICONSTOP|MB_OK "Microsoft Edge WebView2 Runtime installation failed (exit `$1)."
  Abort
webview_done:
  SetRegView 32
FunctionEnd

Section "$ProductName" SecMain
  SectionIn RO
  Call CloseBoostProcesses
  Call EnsureVCRuntime
  Call EnsureWebView2Runtime
$GoogleKernelCleanupLine
  SetOutPath "`$INSTDIR"
  !include "$NshPath"
  IfFileExists "`$INSTDIR\config.yaml" config_present
  File /oname=config.yaml "$ConfigSrc"
config_present:
  CopyFiles /SILENT "`$PLUGINSDIR\activation.marker" "`$INSTDIR\.browserstudio-activation.json"
  SetOutPath "`$INSTDIR"
  WriteUninstaller "`$INSTDIR\Uninstall.exe"
  CreateDirectory "`$SMPROGRAMS\`${PRODUCT_NAME}"
  CreateShortcut "`$SMPROGRAMS\`${PRODUCT_NAME}\`${PRODUCT_NAME}.lnk" "`$INSTDIR\`${PRODUCT_EXE}" "" "`$INSTDIR\`${PRODUCT_EXE}" 0
  CreateShortcut "`$SMPROGRAMS\`${PRODUCT_NAME}\Uninstall `${PRODUCT_NAME}.lnk" "`$INSTDIR\Uninstall.exe"
  CreateShortcut "`$DESKTOP\`${PRODUCT_NAME}.lnk" "`$INSTDIR\`${PRODUCT_EXE}" "" "`$INSTDIR\`${PRODUCT_EXE}" 0
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "DisplayName" "`${PRODUCT_NAME}"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "DisplayVersion" "`${PRODUCT_VERSION}"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "Publisher" "BrowserStudio"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "InstallLocation" "`$INSTDIR"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "UninstallString" "`$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "DisplayIcon" "`$INSTDIR\`${PRODUCT_EXE}"
  WriteRegDWORD HKCU "`${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "`${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  Call un.CloseBoostProcesses
  Delete "`$DESKTOP\`${PRODUCT_NAME}.lnk"
  Delete "`$SMPROGRAMS\`${PRODUCT_NAME}\`${PRODUCT_NAME}.lnk"
  Delete "`$SMPROGRAMS\`${PRODUCT_NAME}\Uninstall `${PRODUCT_NAME}.lnk"
  RMDir "`$SMPROGRAMS\`${PRODUCT_NAME}"
  ; Preserve mutable state for accidental uninstall/reinstall recovery.
  ; A later installer will reuse config/data/extensions/chrome in this folder.
  Delete "`$INSTDIR\boost-browser.exe"
  Delete "`$INSTDIR\updater.exe"
  Delete "`$INSTDIR\app.ico"
  Delete "`$INSTDIR\app.png"
  Delete "`$INSTDIR\Uninstall.exe"
  RMDir /r "`$INSTDIR\bin"
  RMDir "`$INSTDIR"
  DeleteRegKey HKCU "`${UNINSTALL_KEY}"
SectionEnd
"@
Set-Content -Path $NsiPath -Value $nsi -Encoding Unicode

Write-Host '==> [6/7] Running makensis' -ForegroundColor Yellow
$makensisCandidates = @(
    'C:\Program Files (x86)\NSIS\makensis.exe',
    'C:\Program Files\NSIS\makensis.exe'
)
$makensis = $makensisCandidates | Where-Object { Test-Path -LiteralPath $_ } | Select-Object -First 1
if (-not $makensis) {
    $cmd = Get-Command makensis -ErrorAction SilentlyContinue
    if ($cmd) { $makensis = $cmd.Source }
}
if (-not $makensis) { throw 'makensis.exe not found. Install NSIS from https://nsis.sourceforge.io/Download' }

Remove-Item -Force $OutExe -ErrorAction SilentlyContinue
& $makensis $NsiPath
if ($LASTEXITCODE -ne 0) { throw "makensis failed: exit $LASTEXITCODE" }
Require-Path $OutExe "Installer was not generated: $OutExe"
Unblock-File $OutExe -ErrorAction SilentlyContinue

Write-Host '==> [7/7] Done' -ForegroundColor Green
$hash = (Get-FileHash $OutExe -Algorithm SHA256).Hash.ToLower()
$hashPath = "$OutExe.sha256"
[IO.File]::WriteAllText($hashPath, $hash, (New-Object Text.UTF8Encoding($false)))
if ((Get-Item $hashPath).Length -ne 64) { throw "Installer SHA256 file is invalid: $hashPath" }
$size = (Get-Item $OutExe).Length
Write-Host ''
Write-Host '================================================================' -ForegroundColor Cyan
Write-Host '  Full installer build completed' -ForegroundColor Cyan
Write-Host '================================================================' -ForegroundColor Cyan
Write-Host "  File: $OutExe"
Write-Host ("  Size: {0:N1} MB" -f ($size / 1MB))
Write-Host "  SHA256: $hash"
Write-Host ''
Write-Host 'build\release contents:' -ForegroundColor Yellow
Get-ChildItem $ReleaseDir | Sort-Object Name | ForEach-Object {
    Write-Host ("  - {0,-45} {1,12:N0} bytes" -f $_.Name, $_.Length) -ForegroundColor White
}
