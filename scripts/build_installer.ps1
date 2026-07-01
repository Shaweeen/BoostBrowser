# build_installer.ps1
# 完整安装包构建脚本 —— 给新用户首次安装用的 Setup.exe
#
# 与 build_release.ps1（升级用 38MB 主程序）配合使用：
#   - build_release.ps1   产出 boost-browser.exe / .sha256 / updater.exe（自动升级链路）
#   - build_installer.ps1 产出 BoostBrowser-Setup-vX.X.X.exe（新用户首次安装，含完整 chromium 内核）
#
# 用法：
#   1. 先跑 build_release.ps1 生成最新 boost-browser.exe + updater.exe
#   2. 再跑 build_installer.ps1 打包安装包
#   3. 最终 build\release\ 下会有 4 个文件，全部上传到 GitHub Release

$ErrorActionPreference = 'Stop'

$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

# 1. 读版本号
$wailsJson = Get-Content "$RepoRoot\wails.json" -Raw | ConvertFrom-Json
$Version = $wailsJson.info.productVersion
if (-not $Version) { throw "wails.json 中未找到 info.productVersion" }
Write-Host "==> 当前版本: v$Version" -ForegroundColor Cyan

# 2. 路径定义
$ReleaseDir   = "$RepoRoot\build\release"
$BoostExe     = "$ReleaseDir\boost-browser.exe"
$UpdaterExe   = "$ReleaseDir\updater.exe"
$Stage        = "C:\Temp\BoostBrowser_installer_staging"
$Publish      = "$RepoRoot\publish\output"
$NsiPath      = "$RepoRoot\publish\boost-browser-installer.nsi"
$NshPath      = "$RepoRoot\publish\boost_nsis_files.nsh"
$OutExe       = "$ReleaseDir\BoostBrowser-Setup-v$Version.exe"
$Icon         = "$RepoRoot\build\windows\icon.ico"
$SidebarBmp   = "$RepoRoot\publish\boost_sidebar.bmp"
$HeaderBmp    = "$RepoRoot\publish\boost_header.bmp"

# Chrome 内核源（复用 cloak_test 已部署的，不重新下载）
$CloakKernelSrc  = 'Z:\BoostBrowser_v110_test\chrome\cloak-146.0.7680.177'
$GoogleKernelSrc = 'Z:\BoostBrowser_v110_test\chrome\google-148.0.7778.167'
$BinSrc          = 'Z:\BoostBrowser_v110_test\bin'             # xray / sing-box
$ConfigSrc       = "$RepoRoot\config.yaml"                     # 用仓库内干净的 config（不含 extensions、默认 cloak-146）
$AppIconSrc      = 'Z:\BoostBrowser_v110_test\app.ico'
$AppPngSrc       = 'Z:\BoostBrowser_v110_test\app.png'

# 3. 前置校验
if (-not (Test-Path $BoostExe))        { throw "缺少 $BoostExe，请先运行 build_release.ps1" }
if (-not (Test-Path $UpdaterExe))      { throw "缺少 $UpdaterExe，请先运行 build_release.ps1" }
if (-not (Test-Path $CloakKernelSrc))  { throw "缺少 cloak 内核: $CloakKernelSrc" }
if (-not (Test-Path $Icon))            { throw "缺少图标: $Icon" }
New-Item -ItemType Directory -Force -Path $Publish | Out-Null

# 4. Stage 文件（拷到 C:\Temp 比直接从 Z: 网络盘做 NSIS 稳）
Write-Host "==> [1/6] Staging 到 $Stage ..." -ForegroundColor Yellow
Remove-Item -Recurse -Force $Stage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $Stage | Out-Null

# 主程序 + updater
Copy-Item -LiteralPath $BoostExe   -Destination "$Stage\boost-browser.exe" -Force
Copy-Item -LiteralPath $UpdaterExe -Destination "$Stage\updater.exe"       -Force

# 配置 / 图标
if (Test-Path $ConfigSrc)  { Copy-Item -LiteralPath $ConfigSrc  -Destination "$Stage\config.yaml" -Force }
if (Test-Path $AppIconSrc) { Copy-Item -LiteralPath $AppIconSrc -Destination $Stage -Force }
if (Test-Path $AppPngSrc)  { Copy-Item -LiteralPath $AppPngSrc  -Destination $Stage -Force }

# bin（xray、sing-box）
if (Test-Path $BinSrc) {
    Copy-Item -LiteralPath $BinSrc -Destination $Stage -Recurse -Force
}

# 注意：故意不拷 extensions/，初始安装版无内置扩展，由用户自行加载
# （Z 盘的 extensions 是开发机调试用的，路径硬编码在那份 config.yaml 里，给新用户会报错）

# chrome 内核（cloak 默认 + google 备用）
$ChromeStage = "$Stage\chrome"
New-Item -ItemType Directory -Force -Path $ChromeStage | Out-Null
Copy-Item -LiteralPath $CloakKernelSrc -Destination $ChromeStage -Recurse -Force
if (Test-Path $GoogleKernelSrc) {
    Copy-Item -LiteralPath $GoogleKernelSrc -Destination $ChromeStage -Recurse -Force
}

# 留空 data 目录，安装后第一次启动时 app 自己创建数据库
New-Item -ItemType Directory -Force -Path "$Stage\data" | Out-Null

# 清掉残留 lock / log / tmp
Get-ChildItem $Stage -Recurse -File -Force | Where-Object {
    $_.Name -in @('LOCK','LOG','LOG.old') -or $_.Name -like '*.tmp'
} | Remove-Item -Force -ErrorAction SilentlyContinue

$stageBytes = (Get-ChildItem $Stage -Recurse -File -Force | Measure-Object -Property Length -Sum).Sum
$stageFiles = (Get-ChildItem $Stage -Recurse -File -Force | Measure-Object).Count
Write-Host ("   Stage: {0:N0} files, {1:N1} MB" -f $stageFiles, ($stageBytes / 1MB)) -ForegroundColor Green

# 5. 生成品牌 BMP（NSIS 欢迎页 + 头图）
Write-Host "==> [2/6] 生成 NSIS 品牌图..." -ForegroundColor Yellow
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
        $g.DrawString("v$Version", $font2, $muted, 12, 29)
        $g.FillEllipse($accent, $Width-44, 10, 24, 24)
        $font.Dispose(); $font2.Dispose()
    } else {
        $font  = New-Object System.Drawing.Font 'Segoe UI', 18, ([System.Drawing.FontStyle]::Bold)
        $font2 = New-Object System.Drawing.Font 'Segoe UI', 8
        $g.DrawString('Boost', $font, $white, 18, 32)
        $g.DrawString('Browser', $font, $white, 18, 60)
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
New-GradientBitmap $SidebarBmp 164 314 $false
New-GradientBitmap $HeaderBmp  150  57 $true

# 6. 生成显式 File 指令（解决 NSIS File /r 在含点号目录上的静默失败）
Write-Host "==> [3/6] 生成 NSIS 文件清单..." -ForegroundColor Yellow
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
Process-Dir $Stage ''
Set-Content -Path $NshPath -Value $out -Encoding Unicode
Write-Host ("   写入 {0} 条 NSIS 指令" -f $out.Count) -ForegroundColor Green

# 7. 生成 .nsi 脚本（UTF-16 LE 让 NSIS 正确解析中文）
Write-Host "==> [4/6] 生成 NSIS 脚本..." -ForegroundColor Yellow
$nsi = @"
Unicode True
!define PRODUCT_NAME "Boost Browser"
!define PRODUCT_EXE "boost-browser.exe"
!define PRODUCT_VERSION "$Version"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\BoostBrowser"
!define APP_ICON "$Icon"
!define SIDEBAR_BMP "$SidebarBmp"
!define HEADER_BMP "$HeaderBmp"

!include "MUI2.nsh"
!include "LogicLib.nsh"

Name "`${PRODUCT_NAME} `${PRODUCT_VERSION}"
OutFile "$OutExe"
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

!define MUI_WELCOMEPAGE_TITLE "欢迎安装 Boost Browser v$Version"
!define MUI_WELCOMEPAGE_TEXT "Boost Browser 是一款指纹浏览器，自带 Chromium 146 内核（默认）和 Google Chrome 148 备用内核，附带 xray / sing-box 代理工具。`$\r`$\n`$\r`$\n安装完成后即可使用，所有用户数据保存在安装目录。`$\r`$\n`$\r`$\n本版本支持自动升级，未来新版本会在启动时自动提示。"
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

  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM chrome.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM xray.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM sing-box.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM updater.exe'
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM `${PRODUCT_EXE}'
  Sleep 600

  ; PowerShell 强制解锁 + 等待文件可独占打开（避免 chrome.dll 锁住）
  FileOpen `$9 "`$TEMP\boost_unlock.ps1" w
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

  nsExec::ExecToStack '"`$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -ExecutionPolicy Bypass -File "`$TEMP\boost_unlock.ps1" "`$INSTDIR"'
  Pop `$0
  Pop `$1
  Delete "`$TEMP\boost_unlock.ps1"

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
  !include "$NshPath"
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
  ExecWait '"`$SYSDIR\taskkill.exe" /F /T /IM updater.exe'
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
Set-Content -Path $NsiPath -Value $nsi -Encoding Unicode

# 8. makensis 编译
Write-Host "==> [5/6] 调用 makensis 编译..." -ForegroundColor Yellow
$makensisCandidates = @(
    'C:\Program Files (x86)\NSIS\makensis.exe',
    'C:\Program Files\NSIS\makensis.exe'
)
$makensis = $makensisCandidates | Where-Object { Test-Path $_ } | Select-Object -First 1
if (-not $makensis) { throw 'makensis.exe 未找到，请先安装 NSIS（https://nsis.sourceforge.io/）' }

Remove-Item -Force $OutExe -ErrorAction SilentlyContinue
& $makensis $NsiPath
if ($LASTEXITCODE -ne 0) { throw "makensis 失败: exit $LASTEXITCODE" }
Unblock-File $OutExe -ErrorAction SilentlyContinue

# 9. 输出汇总
Write-Host "==> [6/6] 完成 ✓" -ForegroundColor Green
$hash = (Get-FileHash $OutExe -Algorithm SHA256).Hash.ToLower()
$size = (Get-Item $OutExe).Length

Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "  完整安装包打包完成" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ("  文件: $OutExe")
Write-Host ("  大小: {0:N1} MB" -f ($size / 1MB))
Write-Host ("  SHA256: $hash")
Write-Host ""
Write-Host "build\release\ 完整列表（4 个文件，全部传 GitHub Release）：" -ForegroundColor Yellow
Get-ChildItem $ReleaseDir | Sort-Object Name | ForEach-Object {
    Write-Host ("  - {0,-40} {1,12:N0} bytes" -f $_.Name, $_.Length) -ForegroundColor White
}
Write-Host ""
Write-Host "用户使用方式：" -ForegroundColor Yellow
Write-Host "  - 新用户首次安装：下载 BoostBrowser-Setup-v$Version.exe 双击安装"
Write-Host "  - 老用户自动升级：app 启动后 5s 自动检查，弹窗下载 boost-browser.exe（38MB）"
