# Boost Browser - v1.2.0 升级失败修复脚本
# 适用：从 v1.2.0 点"重启更新"后无法重启的用户
# 功能：从 GitHub 下载 v1.3.0，校验 sha256，原地替换 boost-browser.exe 和 updater.exe
#
# 使用方法（任选一种）：
#   1) 双击同目录下 fix-update-from-v1.2.0.bat
#   2) PowerShell 里：powershell -ExecutionPolicy Bypass -File .\fix-update-from-v1.2.0.ps1
#   3) 指定安装目录：powershell -File .\fix-update-from-v1.2.0.ps1 -InstallRoot 'D:\BoostBrowser'

[CmdletBinding()]
param(
    [string] $InstallRoot = '',
    [string] $TargetVersion = 'v1.3.0',
    [string] $GitHubOwner = 'sdohuajia',
    [string] $GitHubRepo  = 'BoostBrowser'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'  # 让 Invoke-WebRequest 快很多

function Write-Step($msg)  { Write-Host ""; Write-Host ">>> $msg" -ForegroundColor Cyan }
function Write-Ok($msg)    { Write-Host "    [OK] $msg" -ForegroundColor Green }
function Write-Warn($msg)  { Write-Host "    [WARN] $msg" -ForegroundColor Yellow }
function Write-Err($msg)   { Write-Host "    [ERROR] $msg" -ForegroundColor Red }

function Get-Sha256($path) {
    return (Get-FileHash -Path $path -Algorithm SHA256).Hash.ToLower()
}

function Resolve-InstallRoot {
    param([string] $CandidateRoot)
    $candidates = @()
    if ($CandidateRoot)              { $candidates += $CandidateRoot }
    $candidates += (Get-Location).Path
    $candidates += (Split-Path -Parent $PSCommandPath)
    foreach ($p in $candidates) {
        if (-not $p) { continue }
        $exe = Join-Path $p 'boost-browser.exe'
        $up  = Join-Path $p 'updater.exe'
        if ((Test-Path $exe) -and (Test-Path $up)) {
            return (Resolve-Path $p).Path
        }
    }
    return $null
}

function Stop-BoostProcesses {
    foreach ($name in @('boost-browser', 'updater')) {
        $procs = Get-Process -Name $name -ErrorAction SilentlyContinue
        if ($procs) {
            Write-Warn "$name 仍在运行，正在结束..."
            $procs | Stop-Process -Force -ErrorAction SilentlyContinue
            Start-Sleep -Milliseconds 800
        }
    }
}

function Get-ReleaseAssets {
    param([string] $Owner, [string] $Repo, [string] $Tag)
    $url = "https://api.github.com/repos/$Owner/$Repo/releases/tags/$Tag"
    $headers = @{ 'User-Agent' = 'BoostBrowser-Fix-Script' }
    return Invoke-RestMethod -Uri $url -Headers $headers -TimeoutSec 30
}

function Download-Asset {
    param([string] $Url, [string] $Dest)
    $tmp = "$Dest.download"
    Invoke-WebRequest -Uri $Url -OutFile $tmp -UseBasicParsing -TimeoutSec 600
    if (Test-Path $Dest) { Remove-Item $Dest -Force }
    Move-Item $tmp $Dest
}

# ============================================================================
Write-Host "================================================================"
Write-Host "  Boost Browser - 修复 v1.2.0 升级失败"
Write-Host "================================================================"

Write-Step "1. 定位安装目录"
$root = Resolve-InstallRoot -CandidateRoot $InstallRoot
if (-not $root) {
    Write-Err "未找到安装目录。请把脚本放到 boost-browser.exe 同一目录后再运行，"
    Write-Err "或加 -InstallRoot 参数：`n  powershell -File .\fix-update-from-v1.2.0.ps1 -InstallRoot 'D:\BoostBrowser'"
    exit 1
}
Write-Ok "安装目录: $root"

$exePath     = Join-Path $root 'boost-browser.exe'
$updaterPath = Join-Path $root 'updater.exe'
$updatesDir  = Join-Path $root 'data\updates'
$bakSuffix   = ".v1.2.0.bak"

Write-Step "2. 结束可能残留的进程"
Stop-BoostProcesses
Write-Ok "已确认无残留进程"

Write-Step "3. 查询 GitHub release: $TargetVersion"
try {
    $rel = Get-ReleaseAssets -Owner $GitHubOwner -Repo $GitHubRepo -Tag $TargetVersion
} catch {
    Write-Err "GitHub API 请求失败：$($_.Exception.Message)"
    Write-Err "请确认网络或代理可访问 api.github.com，或手动下载："
    Write-Err "  https://github.com/$GitHubOwner/$GitHubRepo/releases/tag/$TargetVersion"
    exit 1
}

$exeAsset    = $rel.assets | Where-Object { $_.name -eq 'boost-browser.exe' }    | Select-Object -First 1
$shaAsset    = $rel.assets | Where-Object { $_.name -eq 'boost-browser.exe.sha256' } | Select-Object -First 1
$updAsset    = $rel.assets | Where-Object { $_.name -eq 'updater.exe' }          | Select-Object -First 1

if (-not $exeAsset -or -not $shaAsset -or -not $updAsset) {
    Write-Err "release $TargetVersion 缺少必要的 asset (boost-browser.exe / sha256 / updater.exe)"
    exit 1
}
Write-Ok "已找到 release 资源 (size: exe=$($exeAsset.size) updater=$($updAsset.size))"

Write-Step "4. 下载新版 boost-browser.exe + 校验 sha256"
$tmpDir = Join-Path $env:TEMP "BoostBrowserFix_$(Get-Random)"
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
$tmpExe = Join-Path $tmpDir 'boost-browser.exe'
$tmpUpd = Join-Path $tmpDir 'updater.exe'

Download-Asset -Url $exeAsset.browser_download_url -Dest $tmpExe
$expectedHash = (Invoke-WebRequest -Uri $shaAsset.browser_download_url -UseBasicParsing -TimeoutSec 60).Content.Trim().ToLower()
if ($expectedHash -match '^[0-9a-f]{64}$') {
    $actualHash = Get-Sha256 $tmpExe
    if ($actualHash -ne $expectedHash) {
        Write-Err "boost-browser.exe sha256 不匹配!`n  期望: $expectedHash`n  实际: $actualHash"
        Remove-Item $tmpDir -Recurse -Force
        exit 1
    }
    Write-Ok "boost-browser.exe sha256 校验通过 ($actualHash)"
} else {
    Write-Warn "无法解析 sha256 文件，跳过校验：$expectedHash"
}

Write-Step "5. 下载新版 updater.exe"
Download-Asset -Url $updAsset.browser_download_url -Dest $tmpUpd
Write-Ok "updater.exe 已下载 ($(Get-Sha256 $tmpUpd))"

Write-Step "6. 备份旧版（v1.2.0）"
$exeBak = "$exePath$bakSuffix"
$updBak = "$updaterPath$bakSuffix"
if (Test-Path $exeBak) { Remove-Item $exeBak -Force }
if (Test-Path $updBak) { Remove-Item $updBak -Force }
if (Test-Path $exePath)     { Move-Item $exePath     $exeBak -Force; Write-Ok "旧 boost-browser.exe → $exeBak" }
if (Test-Path $updaterPath) { Move-Item $updaterPath $updBak -Force; Write-Ok "旧 updater.exe       → $updBak" }

Write-Step "7. 安装新版"
Move-Item $tmpExe $exePath     -Force; Write-Ok "boost-browser.exe 已就位"
Move-Item $tmpUpd $updaterPath -Force; Write-Ok "updater.exe 已就位"
Remove-Item $tmpDir -Recurse -Force -ErrorAction SilentlyContinue

Write-Step "8. 清理 data/updates 残留"
if (Test-Path $updatesDir) {
    Get-ChildItem $updatesDir -Force -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    Write-Ok "data/updates 已清空"
} else {
    Write-Ok "data/updates 不存在，跳过"
}

Write-Step "9. 完整性自检"
$finalHash = Get-Sha256 $exePath
Write-Ok "boost-browser.exe sha256 = $finalHash"
if ($expectedHash -match '^[0-9a-f]{64}$' -and $finalHash -ne $expectedHash) {
    Write-Err "最终 sha256 与期望不符，安装可能失败！"
    exit 1
}

Write-Host ""
Write-Host "================================================================" -ForegroundColor Green
Write-Host "  修复完成。安装目录: $root" -ForegroundColor Green
Write-Host "  - 旧版本已备份为 *.v1.2.0.bak（确认正常后可删除）"
Write-Host "  - 现在双击 boost-browser.exe 即可启动 $TargetVersion"
Write-Host "================================================================" -ForegroundColor Green

# 询问是否立即启动
$ans = Read-Host "立即启动 Boost Browser? [Y/n]"
if (-not $ans -or $ans -match '^[yY]') {
    Start-Process -FilePath $exePath -WorkingDirectory $root
    Write-Ok "已启动"
}
