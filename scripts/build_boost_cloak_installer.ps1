$ErrorActionPreference = 'Stop'

# Cloak 版 Boost Browser 安装包构建脚本。
# 与 build_boost_installer.ps1（Chrome 144 老版本）独立，不互相覆盖。
#
# 源目录: Z:\BoostBrowser_cloak_test  (用户当前已验证可用的 cloak 部署)
# 输出  : Z:\BoostBrowser_cloak_Setup.exe
# 默认  : Chromium 146（cloak 内核），同时保留 Google Chrome 148 备用
#
# 关键 NSIS 经验（已踩过坑）：
#   - SetCompressor zlib（lzma 在某些机器上 CRC 校验失败）
#   - .nsi 用 UTF-16 LE 写出（NSIS 才能正确读 Chinese）
#   - File /r 在含点号路径下静默失败 → 用 Process-Dir 生成显式 File 列表
#   - 必须先 stage 到 C:\Temp（NSIS 处理 Z: 网络路径时偶发问题）
#   - 安装前 taskkill + 等待文件解锁，避免 chrome.dll 被 chrome 子进程锁住

$project = 'C:\Users\Administrator\Desktop\Ant-Browser-master'
$src     = 'Z:\BoostBrowser_cloak_test'
$stage   = 'C:\Temp\BoostBrowser_cloak_staging'
$publish = Join-Path $project 'publish\output'
$nsiPath = Join-Path $project 'publish\boost-browser-cloak-installer.nsi'
$nshPath = Join-Path $project 'publish\boost_cloak_nsis_files.nsh'
$outExe  = 'Z:\BoostBrowser_cloak_Setup.exe'
$icon    = Join-Path $project 'build\windows\icon.ico'
$sidebarBmp = Join-Path $project 'publish\boost_cloak_sidebar.bmp'
$headerBmp  = Join-Path $project 'publish\boost_cloak_header.bmp'

if (!(Test-Path $src))                                  { throw "Missing source dir: $src" }
if (!(Test-Path (Join-Path $src 'boost-browser.exe')))  { throw "Missing boost-browser.exe in $src" }
if (!(Test-Path $icon))                                 { throw "Missing icon: $icon" }
New-Item -ItemType Directory -Force -Path $publish      | Out-Null

Write-Host "[1/6] Staging from $src -> $stage" -ForegroundColor Cyan
Remove-Item -Recurse -Force $stage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $stage | Out-Null

# ---------------------------------------------------------------------------
# 显式选择要拷贝的顶层条目，跳过 .bak / runtime data / logs 等。
# 这比 Copy-Item *  + 后清理更可靠（避免 Z: 网络路径上 Get-ChildItem 慢的问题）。
# ---------------------------------------------------------------------------
$includeRoot = @(
    'boost-browser.exe',
    'app.ico',
    'app.png',
    'config.yaml',
    'bin',
    'extensions'
)
foreach ($name in $includeRoot) {
    $srcPath = Join-Path $src $name
    if (Test-Path -LiteralPath $srcPath) {
        Copy-Item -LiteralPath $srcPath -Destination $stage -Recurse -Force
    }
}

# chrome 内核：只复制官方版本目录（cloak-146.0.7680.177 + google-148.0.7778.167），
# 跳过 chrome\cloak\ 这个 537MB 旧重复副本。
$chromeStage = Join-Path $stage 'chrome'
New-Item -ItemType Directory -Force -Path $chromeStage | Out-Null
$cloakKernel  = Join-Path $src 'chrome\cloak-146.0.7680.177'
$googleKernel = Join-Path $src 'chrome\google-148.0.7778.167'
if (Test-Path -LiteralPath $cloakKernel) {
    Copy-Item -LiteralPath $cloakKernel  -Destination $chromeStage -Recurse -Force
} else { throw "Missing cloak kernel: $cloakKernel" }
if (Test-Path -LiteralPath $googleKernel) {
    Copy-Item -LiteralPath $googleKernel -Destination $chromeStage -Recurse -Force
}

# 留一个空 data/ 目录，让安装后第一次启动时 app 自己创建数据库 / profile。
New-Item -ItemType Directory -Force -Path (Join-Path $stage 'data') | Out-Null

# 清掉可能混进 staging 的 LOCK / LOG / *.tmp。
Get-ChildItem $stage -Recurse -File -Force | Where-Object {
    $_.Name -in @('LOCK','LOG','LOG.old') -or $_.Name -like '*.tmp'
} | Remove-Item -Force -ErrorAction SilentlyContinue

Write-Host "[2/6] Stage size:" -ForegroundColor Cyan
$stageBytes = (Get-ChildItem $stage -Recurse -File -Force | Measure-Object -Property Length -Sum).Sum
Write-Host ("       {0:N0} files, {1:N1} MB" -f `
    (Get-ChildItem $stage -Recurse -File -Force | Measure-Object).Count, `
    ($stageBytes / 1MB))

# ---------------------------------------------------------------------------
# Branded BMP 资源（深蓝渐变，简洁）
# ---------------------------------------------------------------------------
Write-Host "[3/6] Generating branded bitmaps" -ForegroundColor Cyan
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
    $white  = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(245, 250, 255))
    $muted  = New-Object System.Drawing.SolidBrush ([System.Drawing.Color]::FromArgb(175, 205, 235))
    if ($Header) {
        $font  = New-Object System.Drawing.Font 'Segoe UI', 10, ([System.Drawing.FontStyle]::Bold)
        $font2 = New-Object System.Drawing.Font 'Segoe UI', 7
        $g.DrawString('Boost Browser', $font, $white, 12, 7)
        $g.DrawString('Chromium 146 kernel ready', $font2, $muted, 12, 29)
        $g.FillEllipse($accent, $Width-44, 10, 24, 24)
        $font.Dispose(); $font2.Dispose()
    } else {
        $font  = New-Object System.Drawing.Font 'Segoe UI', 18, ([System.Drawing.FontStyle]::Bold)
        $font2 = New-Object System.Drawing.Font 'Segoe UI', 8
        $g.DrawString('Boost', $font, $white, 18, 32)
        $g.DrawString('Browser', $font, $white, 18, 60)
        $g.DrawString('Fingerprint browser', $font2, $muted, 20, 108)
        $g.DrawString('Chromium 146', $font2, $muted, 20, 128)
        $g.FillEllipse($accent, 102, 180, 34, 34)
        $g.FillEllipse($accent, 122, 205, 16, 16)
        $font.Dispose(); $font2.Dispose()
    }
    $accent.Dispose(); $white.Dispose(); $muted.Dispose(); $g.Dispose()
    $bmp.Save($Path, [System.Drawing.Imaging.ImageFormat]::Bmp)
    $bmp.Dispose()
}
New-GradientBitmap $sidebarBmp 164 314 $false
New-GradientBitmap $headerBmp  150  57 $true

# ---------------------------------------------------------------------------
# 生成 NSIS File 指令清单（避免 File /r 在含点号目录上的静默失败）
# 关键：父目录的 SetOutPath + File 必须在子目录之前，不能用 -Recurse 一把刷
# ---------------------------------------------------------------------------
Write-Host "[4/6] Generating NSIS file list" -ForegroundColor Cyan
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
Set-Content -Path $nshPath -Value $out -Encoding Unicode
Write-Host ("       wrote {0} NSIS directives -> {1}" -f $out.Count, $nshPath)

# ---------------------------------------------------------------------------
# 生成 .nsi（UTF-16 LE 让 NSIS 能正确解析中文）
# ---------------------------------------------------------------------------
Write-Host "[5/6] Generating .nsi script" -ForegroundColor Cyan
$nsi = @"
Unicode True
!define PRODUCT_NAME "Boost Browser"
!define PRODUCT_EXE "boost-browser.exe"
!define PRODUCT_VERSION "1.2.0"
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

!define MUI_WELCOMEPAGE_TITLE "欢迎安装 Boost Browser"
!define MUI_WELCOMEPAGE_TEXT "Boost Browser 是一款指纹浏览器，自带 Chromium 146 内核（默认）和 Google Chrome 148 备用内核，附带 xray / sing-box 代理工具。`$\r`$\n`$\r`$\n安装完成后即可使用，所有用户数据保存在安装目录。"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "`$INSTDIR\`${PRODUCT_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "立即启动 Boost Browser"
!define MUI_FINISHPAGE_TITLE "安装完成"
!define MUI_FINISHPAGE_TEXT "默认内核 Chromium 146 已就绪。`$\r`$\n如需切换内核，可在应用内的内核管理界面操作。"
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

Function CloseBoostProcesses
retry_close:
  IfFileExists "`$INSTDIR" 0 done

  ; 先杀子进程（chrome、xray、sing-box），再杀主进程。
  ; 顺序很重要：主进程被先杀的话，可能在我们回头杀子进程前再次拉起。
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM chrome.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM xray.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM sing-box.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM `${PRODUCT_EXE}'
  Sleep 600

  ; PowerShell 强制解锁：杀掉 ExecutablePath 在安装目录下的所有进程，
  ; 然后等关键文件可独占打开（chrome.dll 经常被子进程多锁一段时间）。
  FileOpen `$9 "`$TEMP\boost_cloak_unlock.ps1" w
  FileWrite `$9 "param([string]`$`$InstallDir)`$\r`$\n"
  FileWrite `$9 "`$`$ErrorActionPreference = 'SilentlyContinue'`$\r`$\n"
  FileWrite `$9 "`$`$root = [IO.Path]::GetFullPath(`$`$InstallDir).TrimEnd('\\')`$\r`$\n"
  FileWrite `$9 "`$`$rootSlash = `$`$root + '\\'`$\r`$\n"
  FileWrite `$9 "`$`$critical = @([IO.Path]::Combine(`$`$root, 'boost-browser.exe'))`$\r`$\n"
  FileWrite `$9 "Get-ChildItem -LiteralPath ([IO.Path]::Combine(`$`$root, 'chrome')) -Directory -ErrorAction SilentlyContinue | ForEach-Object { `$`$critical += [IO.Path]::Combine(`$`$_.FullName, 'chrome.exe'); `$`$critical += [IO.Path]::Combine(`$`$_.FullName, 'chrome.dll') }`$\r`$\n"
  FileWrite `$9 "for (`$`$round = 0; `$`$round -lt 5; `$`$round++) {`$\r`$\n"
  FileWrite `$9 "  `$`$procs = Get-CimInstance Win32_Process | Where-Object { `$`$_.ExecutablePath -and ([IO.Path]::GetFullPath(`$`$_.ExecutablePath).StartsWith(`$`$rootSlash, [StringComparison]::OrdinalIgnoreCase)) }`$\r`$\n"
  FileWrite `$9 "  if (`$`$procs.Count -eq 0) { break }`$\r`$\n"
  FileWrite `$9 "  foreach (`$`$p in `$`$procs) { try { & `$`$env:WINDIR\System32\taskkill.exe /F /T /PID `$`$p.ProcessId 2>&1 | Out-Null } catch {} }`$\r`$\n"
  FileWrite `$9 "  Start-Sleep -Milliseconds 800`$\r`$\n"
  FileWrite `$9 "}`$\r`$\n"
  FileWrite `$9 "for (`$`$i = 0; `$`$i -lt 60; `$`$i++) { `$`$locked = `$`$false; foreach (`$`$t in `$`$critical) { if (Test-Path -LiteralPath `$`$t) { try { `$`$fs = [IO.File]::Open(`$`$t, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None); `$`$fs.Close() } catch { `$`$locked = `$`$true; break } } }; if (-not `$`$locked) { exit 0 }; Start-Sleep -Milliseconds 500 }`$\r`$\n"
  FileWrite `$9 "exit 11`$\r`$\n"
  FileClose `$9

  nsExec::ExecToStack '"`$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "`$TEMP\boost_cloak_unlock.ps1" "`$INSTDIR"'
  Pop `$0
  Pop `$1
  Delete "`$TEMP\boost_cloak_unlock.ps1"

  `${If} `$0 != 0
    MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "Boost Browser 或内置 Chromium 仍在运行。请关闭所有浏览器窗口、扩展弹窗后重试。" IDRETRY retry_close IDCANCEL abort_install
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
  !include "$nshPath"
  SetOutPath "`$INSTDIR"
  WriteUninstaller "`$INSTDIR\Uninstall.exe"

  CreateDirectory "`$SMPROGRAMS\`${PRODUCT_NAME}"
  CreateShortcut "`$SMPROGRAMS\`${PRODUCT_NAME}\`${PRODUCT_NAME}.lnk" "`$INSTDIR\`${PRODUCT_EXE}" "" "`$INSTDIR\`${PRODUCT_EXE}" 0
  CreateShortcut "`$SMPROGRAMS\`${PRODUCT_NAME}\Uninstall `${PRODUCT_NAME}.lnk" "`$INSTDIR\Uninstall.exe"
  CreateShortcut "`$DESKTOP\`${PRODUCT_NAME}.lnk" "`$INSTDIR\`${PRODUCT_EXE}" "" "`$INSTDIR\`${PRODUCT_EXE}" 0

  WriteRegStr   HKCU "`${UNINSTALL_KEY}" "DisplayName"     "`${PRODUCT_NAME}"
  WriteRegStr   HKCU "`${UNINSTALL_KEY}" "DisplayVersion"  "`${PRODUCT_VERSION}"
  WriteRegStr   HKCU "`${UNINSTALL_KEY}" "Publisher"       "Boost Browser"
  WriteRegStr   HKCU "`${UNINSTALL_KEY}" "InstallLocation" "`$INSTDIR"
  WriteRegStr   HKCU "`${UNINSTALL_KEY}" "UninstallString" "`$INSTDIR\Uninstall.exe"
  WriteRegStr   HKCU "`${UNINSTALL_KEY}" "DisplayIcon"     "`$INSTDIR\`${PRODUCT_EXE}"
  WriteRegDWORD HKCU "`${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "`${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM chrome.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM xray.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM sing-box.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM `${PRODUCT_EXE}'
  Sleep 800
  Delete "`$DESKTOP\`${PRODUCT_NAME}.lnk"
  Delete "`$SMPROGRAMS\`${PRODUCT_NAME}\`${PRODUCT_NAME}.lnk"
  Delete "`$SMPROGRAMS\`${PRODUCT_NAME}\Uninstall `${PRODUCT_NAME}.lnk"
  RMDir  "`$SMPROGRAMS\`${PRODUCT_NAME}"
  RMDir /r "`$INSTDIR"
  DeleteRegKey HKCU "`${UNINSTALL_KEY}"
SectionEnd
"@
Set-Content -Path $nsiPath -Value $nsi -Encoding Unicode

# ---------------------------------------------------------------------------
# makensis 编译 + 校验
# ---------------------------------------------------------------------------
Write-Host "[6/6] Running makensis..." -ForegroundColor Cyan
$makensisCandidates = @(
    'C:\Program Files (x86)\NSIS\makensis.exe',
    'C:\Program Files\NSIS\makensis.exe'
)
$makensis = $makensisCandidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (!$makensis) { throw 'makensis.exe not found' }

Remove-Item -Force $outExe -ErrorAction SilentlyContinue
& $makensis $nsiPath
$code = $LASTEXITCODE
if ($code -ne 0) { throw "makensis failed: $code" }
Unblock-File $outExe -ErrorAction SilentlyContinue

$hash = Get-FileHash $outExe -Algorithm SHA256
$size = (Get-Item $outExe).Length
Write-Host ""
Write-Host "================ DONE ================" -ForegroundColor Green
Write-Host "OUT    = $outExe"
Write-Host ("SIZE   = {0:N0} bytes ({1:N1} MB)" -f $size, ($size / 1MB))
Write-Host "SHA256 = $($hash.Hash)"
Write-Host "STAGE  = $stage"
Write-Host ("FILES  = {0}" -f (Get-ChildItem $stage -Recurse -File).Count)
