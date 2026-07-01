$ErrorActionPreference = 'Stop'

$project = 'C:\Users\Administrator\Desktop\Ant-Browser-master'
$src = Join-Path $project 'build\bin'
$publish = Join-Path $project 'publish\output'
$stage = 'C:\Temp\BoostBrowser_installer_staging'
$nsiPath = Join-Path $project 'publish\boost-browser-installer.nsi'
$filesPath = Join-Path $project 'publish\boost_nsis_files.nsh'
$outExe = Join-Path $publish 'BoostBrowser-Setup-1.1.0.exe'
$icon = Join-Path $project 'build\windows\icon.ico'
$sidebarBmp = Join-Path $project 'publish\boost_sidebar.bmp'
$headerBmp = Join-Path $project 'publish\boost_header.bmp'

if (!(Test-Path $src)) { throw "Missing source dir: $src" }
if (!(Test-Path (Join-Path $src 'boost-browser.exe'))) { throw "Missing boost-browser.exe in $src" }
New-Item -ItemType Directory -Force -Path $publish | Out-Null
Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $stage | Out-Null

# Copy from Wails output to clean staging dir. Include chrome kernel, proxy tools, config, current templates.
Copy-Item -Path (Join-Path $src '*') -Destination $stage -Recurse -Force

# Force default kernel to Chrome 144 in packaged config.
$stageConfig = Join-Path $stage 'config.yaml'
$repoConfig = Join-Path $project 'config.yaml'
if (Test-Path $repoConfig) {
    Copy-Item -LiteralPath $repoConfig -Destination $stageConfig -Force
}
if (Test-Path $stageConfig) {
    $cfg = Get-Content -LiteralPath $stageConfig -Raw
    $cfg = $cfg -replace '(?ms)  cores:\s*\r?\n  - core_id: core-cloak\s*\r?\n    core_name: CloakBrowser 146\s*\r?\n    core_path: chrome\\cloak\s*\r?\n    is_default: true\s*\r?\n  - core_id: core-144\s*\r?\n    core_name: Chrome 144\s*\r?\n    core_path: chrome\\144\s*\r?\n    is_default: false', "  cores:`r`n  - core_id: core-144`r`n    core_name: Chrome 144`r`n    core_path: chrome\144`r`n    is_default: true`r`n  - core_id: core-cloak`r`n    core_name: CloakBrowser 146`r`n    core_path: chrome\cloak`r`n    is_default: false"
    # Safety: if order already changed, still enforce only core-144 as default.
    $cfg = $cfg -replace '(?m)(core_id: core-144\s*\r?\n    core_name: Chrome 144\s*\r?\n    core_path: chrome\\144\s*\r?\n    is_default:) false', '$1 true'
    $cfg = $cfg -replace '(?m)(core_id: core-cloak\s*\r?\n    core_name: CloakBrowser 146\s*\r?\n    core_path: chrome\\cloak\s*\r?\n    is_default:) true', '$1 false'
    Set-Content -LiteralPath $stageConfig -Value $cfg -Encoding UTF8
}

# Remove volatile/user runtime data before packaging. Keep kernels, config, proxy tools, and extensions.
# Browser profiles/app.db/caches must be created on first launch, not baked into the installer.
if (Test-Path (Join-Path $stage 'data')) {
    Get-ChildItem (Join-Path $stage 'data') -Force | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
}
Get-ChildItem $stage -Recurse -File -Force | Where-Object {
    $_.Name -in @('LOCK','LOG','LOG.old') -or $_.Name -like '*.tmp'
} | Remove-Item -Force -ErrorAction SilentlyContinue

# Generate cleaner branded MUI bitmaps (standard installer UI, not custom ugly controls).
Add-Type -AssemblyName System.Drawing
function New-GradientBitmap([string]$Path, [int]$Width, [int]$Height, [bool]$Header) {
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
    $white = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(245, 250, 255))
    $muted = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(175, 205, 235))
    if ($Header) {
        $font = New-Object System.Drawing.Font 'Segoe UI', 10, ([System.Drawing.FontStyle]::Bold)
        $g.DrawString('Boost Browser', $font, $white, 12, 7)
        $font2 = New-Object System.Drawing.Font 'Segoe UI', 7
        $g.DrawString('Chrome 144 kernel ready', $font2, $muted, 12, 29)
        $g.FillEllipse($accent, $Width-44, 10, 24, 24)
        $font.Dispose(); $font2.Dispose()
    } else {
        $font = New-Object System.Drawing.Font 'Segoe UI', 18, ([System.Drawing.FontStyle]::Bold)
        $font2 = New-Object System.Drawing.Font 'Segoe UI', 8
        $g.DrawString('Boost', $font, $white, 18, 32)
        $g.DrawString('Browser', $font, $white, 18, 60)
        $g.DrawString('Fingerprint browser', $font2, $muted, 20, 108)
        $g.DrawString('Chrome 144', $font2, $muted, 20, 128)
        $g.DrawString('@ferdie_jhovie', $font2, $muted, 20, 272)
        $g.FillEllipse($accent, 102, 180, 34, 34)
        $g.FillEllipse($accent, 122, 205, 16, 16)
        $font.Dispose(); $font2.Dispose()
    }
    $accent.Dispose(); $white.Dispose(); $muted.Dispose(); $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Bmp)
    $bmp.Dispose()
}
New-GradientBitmap $sidebarBmp 164 314 $false
New-GradientBitmap $headerBmp 150 57 $true

# Generate explicit File directives instead of File /r to avoid NSIS recursion/path edge cases.
$out = New-Object System.Collections.Generic.List[string]
function Escape-Nsis([string]$s) { return $s.Replace('$', '$$').Replace('`', '``') }
function Process-Dir([string]$dir, [string]$relPath) {
    if ($relPath -eq '') { $out.Add('SetOutPath "$INSTDIR"') }
    else { $out.Add('SetOutPath "$INSTDIR\' + (Escape-Nsis $relPath) + '"') }

    Get-ChildItem -LiteralPath $dir -File -Force | Sort-Object Name | ForEach-Object {
        $out.Add('File "' + (Escape-Nsis $_.FullName) + '"')
    }
    Get-ChildItem -LiteralPath $dir -Directory -Force | Sort-Object Name | ForEach-Object {
        $childRel = if ($relPath -eq '') { $_.Name } else { $relPath + '\' + $_.Name }
        Process-Dir $_.FullName $childRel
    }
}
Process-Dir $stage ''
Set-Content -Path $filesPath -Value $out -Encoding Unicode

$nsi = @"
Unicode True
!define PRODUCT_NAME "Boost Browser"
!define PRODUCT_EXE "boost-browser.exe"
!define PRODUCT_VERSION "1.1.0"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\BoostBrowser"
!define APP_ICON "$icon"
!define SIDEBAR_BMP "$sidebarBmp"
!define HEADER_BMP "$headerBmp"

!include "MUI2.nsh"
!include "LogicLib.nsh"

Name "`${PRODUCT_NAME} `${PRODUCT_VERSION}"
OutFile "$outExe"
InstallDir "`$LOCALAPPDATA\Programs\Boost Browser"
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

!define MUI_WELCOMEPAGE_TITLE "Welcome to Boost Browser"
!define MUI_WELCOMEPAGE_TEXT "Boost Browser includes the full runtime package and is ready to use after installation. Default browser kernel: Chrome 144. Includes the app, Chromium kernel, xray / sing-box, config templates, and sync features. Follow @ferdie_jhovie on X/Twitter for updates."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "`$INSTDIR\`${PRODUCT_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Launch Boost Browser"
!define MUI_FINISHPAGE_TITLE "Boost Browser Setup Complete"
!define MUI_FINISHPAGE_TEXT "Installation complete. Default kernel is Chrome 144. Single-instance protection is enabled."
!define MUI_FINISHPAGE_LINK "关注 @ferdie_jhovie"
!define MUI_FINISHPAGE_LINK_LOCATION "https://x.com/ferdie_jhovie"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

Function CloseBoostProcesses
retry_close:
  ; Close the old app and bundled Chromium before NSIS starts overwriting files.
  ; Chrome child processes can keep chrome\\\\144\\\\chrome.dll locked even after the
  ; main Wails process exits, so unlock the full install tree and wait on critical files.
  IfFileExists "`$INSTDIR" 0 done

  ; Kill by image name first (fast, covers most cases).
  ; Use /FI to filter session if needed; /T kills child processes.
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM `${PRODUCT_EXE}'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM chrome.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM xray.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM sing-box.exe'
  Sleep 500

  ; Also kill any chrome processes via WMIC by PID (handles edge cases where
  ; taskkill /IM misses processes in other sessions or with different exe names).
  ; The PowerShell script uses WMI to find all processes whose path or command
  ; line references the install directory, then force-kills them by PID.
  FileOpen `$9 "`$TEMP\boost_install_unlock.ps1" w
  FileWrite `$9 "param([string]`$`$InstallDir)`$\r`$`n"
  FileWrite `$9 "`$`$ErrorActionPreference = 'SilentlyContinue'`$\r`$`n"
  FileWrite `$9 "`$`$installRoot = [IO.Path]::GetFullPath(`$`$InstallDir).TrimEnd('\\\\')`$\r`$`n"
  FileWrite `$9 "`$`$installRootSlash = `$`$installRoot + '\\\\'`$\r`$`n"
  FileWrite `$9 "`$`$critical = @([IO.Path]::Combine(`$`$installRoot, 'boost-browser.exe'), [IO.Path]::Combine(`$`$installRoot, 'chrome', '144', 'chrome.exe'), [IO.Path]::Combine(`$`$installRoot, 'chrome', '144', 'chrome.dll'))`$\r`$`n"
  ; Phase 1: Kill all processes whose EXE path starts with the install dir (3 rounds).
  ; This catches chrome/renderer/gpu broker child processes that taskkill /IM may miss.
  FileWrite `$9 "for (`$`$round = 0; `$`$round -lt 5; `$`$round++) {`$\r`$`n"
  FileWrite `$9 "  `$`$procs = Get-CimInstance Win32_Process | Where-Object { `$`$_.ExecutablePath -and ([IO.Path]::GetFullPath(`$`$_.ExecutablePath).StartsWith(`$`$installRootSlash, [StringComparison]::OrdinalIgnoreCase)) }`$\r`$`n"
  FileWrite `$9 "  if (`$`$procs.Count -eq 0) { break }`$\r`$`n"
  FileWrite `$9 "  foreach (`$`$p in `$`$procs) { try { & `$`$env:WINDIR\System32\taskkill.exe /F /T /PID `$`$p.ProcessId 2>&1 | Out-Null } catch {} }`$\r`$`n"
  FileWrite `$9 "  Start-Sleep -Milliseconds 800`$\r`$`n"
  FileWrite `$9 "}`$\r`$`n"
  ; Phase 2: Also kill by known exe names as a safety net (in case some spawned
  ; from the dir but WMI path detection missed them).
  FileWrite `$9 "`$`$names = @('boost-browser','chrome','xray','sing-box')`$\r`$`n"
  FileWrite `$9 "foreach (`$`$nm in `$`$names) {`$\r`$`n"
  FileWrite `$9 "  `$`$procs = Get-Process -Name `$`$nm -ErrorAction SilentlyContinue | Where-Object { `$`$_.Path -and `$`$_.Path.StartsWith(`$`$installRootSlash, [StringComparison]::OrdinalIgnoreCase) }`$\r`$`n"
  FileWrite `$9 "  foreach (`$`$p in `$`$procs) { try { Stop-Process -Id `$`$p.Id -Force -ErrorAction SilentlyContinue } catch {} }`$\r`$`n"
  FileWrite `$9 "}`$\r`$`n"
  FileWrite `$9 "Start-Sleep -Milliseconds 500`$\r`$`n"
  ; Phase 3: Wait for critical files to become unlocked (up to 60s).
  FileWrite `$9 "for (`$`$i = 0; `$`$i -lt 120; `$`$i++) { `$`$locked = `$`$false; foreach (`$`$target in `$`$critical) { if (Test-Path -LiteralPath `$`$target) { try { `$`$fs = [IO.File]::Open(`$`$target, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None); `$`$fs.Close() } catch { `$`$locked = `$`$true; break } } }; if (-not `$`$locked) { exit 0 }; Start-Sleep -Milliseconds 500 }`$\r`$`n"
  FileWrite `$9 "exit 11`$\r`$`n"
  FileClose `$9

  nsExec::ExecToStack '"`$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "`$TEMP\boost_install_unlock.ps1" "`$INSTDIR"'
  Pop `$0
  Pop `$1
  Delete "`$TEMP\boost_install_unlock.ps1"

  `${If} `$0 != 0
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Boost Browser or bundled Chrome is still running. Please close all Boost Browser windows, browser pages, and wallet popups, then click Retry." IDRETRY retry_close IDCANCEL abort_install
  `${EndIf}
  Sleep 300
  Goto done

abort_install:
  Abort
done:
FunctionEnd

Section "Boost Browser" SecMain
  SectionIn RO
  Call CloseBoostProcesses
  SetOutPath "`$INSTDIR"
  !include "$filesPath"
  SetOutPath "`$INSTDIR"
  WriteUninstaller "`$INSTDIR\Uninstall.exe"

  CreateDirectory "`$SMPROGRAMS\`${PRODUCT_NAME}"
  CreateShortcut "`$SMPROGRAMS\`${PRODUCT_NAME}\`${PRODUCT_NAME}.lnk" "`$INSTDIR\`${PRODUCT_EXE}" "" "`$INSTDIR\`${PRODUCT_EXE}" 0
  CreateShortcut "`$SMPROGRAMS\`${PRODUCT_NAME}\Uninstall `${PRODUCT_NAME}.lnk" "`$INSTDIR\Uninstall.exe"
  CreateShortcut "`$DESKTOP\`${PRODUCT_NAME}.lnk" "`$INSTDIR\`${PRODUCT_EXE}" "" "`$INSTDIR\`${PRODUCT_EXE}" 0

  WriteRegStr HKCU "`${UNINSTALL_KEY}" "DisplayName" "`${PRODUCT_NAME}"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "DisplayVersion" "`${PRODUCT_VERSION}"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "Publisher" "Boost Browser"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "InstallLocation" "`$INSTDIR"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "UninstallString" "`$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "`${UNINSTALL_KEY}" "DisplayIcon" "`$INSTDIR\`${PRODUCT_EXE}"
  WriteRegDWORD HKCU "`${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "`${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM `${PRODUCT_EXE}'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM xray.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM sing-box.exe'
  Sleep 800
  Delete "`$DESKTOP\`${PRODUCT_NAME}.lnk"
  Delete "`$SMPROGRAMS\`${PRODUCT_NAME}\`${PRODUCT_NAME}.lnk"
  Delete "`$SMPROGRAMS\`${PRODUCT_NAME}\Uninstall `${PRODUCT_NAME}.lnk"
  RMDir "`$SMPROGRAMS\`${PRODUCT_NAME}"
  RMDir /r "`$INSTDIR"
  DeleteRegKey HKCU "`${UNINSTALL_KEY}"
SectionEnd
"@
Set-Content -Path $nsiPath -Value $nsi -Encoding Unicode

$makensisCandidates = @(
  'C:\Program Files (x86)\NSIS\makensis.exe',
  'C:\Program Files\NSIS\makensis.exe'
)
$makensis = $makensisCandidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (!$makensis) { throw 'makensis.exe not found after NSIS install' }

Remove-Item -Force $outExe -ErrorAction SilentlyContinue
& $makensis $nsiPath
$code = $LASTEXITCODE
if ($code -ne 0) { throw "makensis failed: $code" }
Unblock-File $outExe -ErrorAction SilentlyContinue

$hash = Get-FileHash $outExe -Algorithm SHA256
$size = (Get-Item $outExe).Length
Write-Host "OUT=$outExe"
Write-Host "SIZE=$size"
Write-Host "SHA256=$($hash.Hash)"
Write-Host "STAGE=$stage"
Write-Host "FILES=$((Get-ChildItem $stage -Recurse -File).Count)"
