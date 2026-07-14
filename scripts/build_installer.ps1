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

$AssetRoot = if ($env:BOOST_KERNEL_SRC) { $env:BOOST_KERNEL_SRC } else { $RepoRoot }
$CloakKernelSrc = "$AssetRoot\chrome\cloak-146.0.7680.177"
$GoogleKernelSrc = "$AssetRoot\chrome\google-148.0.7778.167"
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
New-Item -ItemType Directory -Force -Path $Publish | Out-Null
New-Item -ItemType Directory -Force -Path $ReleaseDir | Out-Null

Write-Host "==> Asset root: $AssetRoot" -ForegroundColor Cyan
Write-Host "==> [1/6] Staging files to $Stage" -ForegroundColor Yellow
Remove-Item -LiteralPath $Stage -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $Stage | Out-Null

Copy-Item -LiteralPath $BoostExe -Destination "$Stage\boost-browser.exe" -Force
Copy-Item -LiteralPath $UpdaterExe -Destination "$Stage\updater.exe" -Force
if (Test-Path -LiteralPath $ConfigSrc) { Copy-Item -LiteralPath $ConfigSrc -Destination "$Stage\config.yaml" -Force }
if (Test-Path -LiteralPath $AppIconSrc) { Copy-Item -LiteralPath $AppIconSrc -Destination $Stage -Force }
if (Test-Path -LiteralPath $AppPngSrc) { Copy-Item -LiteralPath $AppPngSrc -Destination $Stage -Force }
if (-not $ManagerOnly -and (Test-Path -LiteralPath $BinSrc)) { Copy-Dir $BinSrc "$Stage\bin" }
# Helper extension is intentionally not bundled in the clean self-use package.

if (-not $ManagerOnly) {
    New-Item -ItemType Directory -Force -Path "$Stage\chrome" | Out-Null
    Copy-Dir $CloakKernelSrc "$Stage\chrome\cloak-146.0.7680.177"
    if (Test-Path -LiteralPath $GoogleKernelSrc) {
        Copy-Dir $GoogleKernelSrc "$Stage\chrome\google-148.0.7778.167"
    } else {
        Write-Host "Optional Google fallback missing; skipped: $GoogleKernelSrc" -ForegroundColor Yellow
    }
}
New-Item -ItemType Directory -Force -Path "$Stage\data" | Out-Null

Get-ChildItem $Stage -Recurse -File -Force | Where-Object {
    $_.Name -in @('LOCK','LOG','LOG.old') -or $_.Name -like '*.tmp'
} | Remove-Item -Force -ErrorAction SilentlyContinue

$stageBytes = (Get-ChildItem $Stage -Recurse -File -Force | Measure-Object -Property Length -Sum).Sum
$stageFiles = (Get-ChildItem $Stage -Recurse -File -Force | Measure-Object).Count
Write-Host ("   Stage: {0:N0} files, {1:N1} MB" -f $stageFiles, ($stageBytes / 1MB)) -ForegroundColor Green

Write-Host '==> [2/6] Generating NSIS bitmaps' -ForegroundColor Yellow
New-BrandBitmap $SidebarBmp 164 314 $false $Version
New-BrandBitmap $HeaderBmp 150 57 $true $Version

Write-Host '==> [3/6] Generating NSIS file manifest' -ForegroundColor Yellow
$out = New-Object System.Collections.Generic.List[string]
Add-NsisDir $Stage '' $out
Set-Content -Path $NshPath -Value $out -Encoding Unicode
Write-Host ("   Wrote {0:N0} NSIS lines" -f $out.Count) -ForegroundColor Green

Write-Host '==> [4/6] Generating NSIS script' -ForegroundColor Yellow
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

Function .onInit
  InitPluginsDir
  SetOutPath `$PLUGINSDIR
  File /oname=activation-check.exe "$ActivationCheckExe"
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
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM chrome.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM xray.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM sing-box.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM updater.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM `${PRODUCT_EXE}'
  Sleep 800
done:
FunctionEnd

Section "$ProductName" SecMain
  SectionIn RO
  Call CloseBoostProcesses
  SetOutPath "`$INSTDIR"
  !include "$NshPath"
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
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM chrome.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM xray.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM sing-box.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM updater.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM `${PRODUCT_EXE}'
  Sleep 800
  Delete "`$DESKTOP\`${PRODUCT_NAME}.lnk"
  Delete "`$SMPROGRAMS\`${PRODUCT_NAME}\`${PRODUCT_NAME}.lnk"
  Delete "`$SMPROGRAMS\`${PRODUCT_NAME}\Uninstall `${PRODUCT_NAME}.lnk"
  RMDir "`$SMPROGRAMS\`${PRODUCT_NAME}"
  RMDir /r "`$INSTDIR"
  DeleteRegKey HKCU "`${UNINSTALL_KEY}"
SectionEnd
"@
Set-Content -Path $NsiPath -Value $nsi -Encoding Unicode

Write-Host '==> [5/6] Running makensis' -ForegroundColor Yellow
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

Write-Host '==> [6/6] Done' -ForegroundColor Green
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
