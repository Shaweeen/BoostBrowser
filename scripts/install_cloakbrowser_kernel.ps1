# install_cloakbrowser_kernel.ps1
# Download and install the official CloakBrowser free Windows Chromium kernel
# into this BoostBrowser repo layout:
#   chrome\cloak-146.0.7680.177\chrome.exe
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\install_cloakbrowser_kernel.ps1
# Optional:
#   powershell -ExecutionPolicy Bypass -File scripts\install_cloakbrowser_kernel.ps1 -Force
#   powershell -ExecutionPolicy Bypass -File scripts\install_cloakbrowser_kernel.ps1 -SourceZip C:\Users\admin\Downloads\cloakbrowser-windows-x64.zip

param(
    [string]$InstallRoot = "",
    [string]$DownloadDir = "",
    [string]$SourceZip = "",
    [switch]$Force
)

$ErrorActionPreference = "Stop"

$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

if ([string]::IsNullOrWhiteSpace($InstallRoot)) { $InstallRoot = $RepoRoot }
if ([string]::IsNullOrWhiteSpace($DownloadDir)) { $DownloadDir = Join-Path $env:USERPROFILE "Downloads" }

$ReleaseTag = "chromium-v146.0.7680.177.5"
$ArchiveName = "cloakbrowser-windows-x64.zip"
$KernelDirName = "cloak-146.0.7680.177"
$OwnerRepo = "CloakHQ/CloakBrowser"
$BaseUrl = "https://github.com/$OwnerRepo/releases/download/$ReleaseTag"
$ZipUrl = "$BaseUrl/$ArchiveName"
$ShaUrl = "$BaseUrl/SHA256SUMS"

$ChromeRoot = Join-Path $InstallRoot "chrome"
$TargetDir = Join-Path $ChromeRoot $KernelDirName
$TargetChrome = Join-Path $TargetDir "chrome.exe"

Write-Host "==> CloakBrowser kernel installer" -ForegroundColor Cyan
Write-Host "    official repo : https://github.com/$OwnerRepo" -ForegroundColor Gray
Write-Host "    release       : $ReleaseTag" -ForegroundColor Gray
Write-Host "    target        : $TargetDir" -ForegroundColor Gray

if ((Test-Path $TargetChrome) -and -not $Force) {
    Write-Host "==> Kernel already installed: $TargetChrome" -ForegroundColor Green
    exit 0
}

New-Item -ItemType Directory -Force -Path $DownloadDir | Out-Null
New-Item -ItemType Directory -Force -Path $ChromeRoot | Out-Null

$ZipPath = if ([string]::IsNullOrWhiteSpace($SourceZip)) { Join-Path $DownloadDir $ArchiveName } else { $SourceZip }
$ShaPath = Join-Path $DownloadDir "SHA256SUMS-$ReleaseTag"

if (-not (Test-Path $ZipPath)) {
    Write-Host "==> Downloading $ArchiveName (~562 MB) ..." -ForegroundColor Yellow
    Invoke-WebRequest -Uri $ZipUrl -OutFile $ZipPath
} else {
    Write-Host "==> Using existing archive: $ZipPath" -ForegroundColor Yellow
}

Write-Host "==> Downloading SHA256SUMS ..." -ForegroundColor Yellow
Invoke-WebRequest -Uri $ShaUrl -OutFile $ShaPath

$expected = $null
$archivePattern = [regex]::Escape($ArchiveName)
foreach ($line in Get-Content $ShaPath) {
    if ($line -match "^([a-fA-F0-9]{64})\s+\*?$archivePattern$") {
        $expected = $Matches[1].ToLower()
        break
    }
}
if (-not $expected) { throw "Cannot find $ArchiveName in SHA256SUMS" }

$actual = (Get-FileHash $ZipPath -Algorithm SHA256).Hash.ToLower()
if ($actual -ne $expected) {
    throw "SHA256 mismatch for $ZipPath. expected=$expected actual=$actual"
}
Write-Host "    SHA256 OK: $actual" -ForegroundColor Green

$ExtractRoot = Join-Path $DownloadDir "cloakbrowser_extract_$ReleaseTag"
Remove-Item $ExtractRoot -Recurse -Force -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $ExtractRoot | Out-Null

Write-Host "==> Extracting archive ..." -ForegroundColor Yellow
Expand-Archive -Path $ZipPath -DestinationPath $ExtractRoot -Force

$chrome = Get-ChildItem $ExtractRoot -Recurse -Filter chrome.exe -File | Select-Object -First 1
if (-not $chrome) { throw "chrome.exe not found after extracting $ZipPath" }
$SourceDir = $chrome.Directory.FullName
Write-Host "    source chrome.exe: $($chrome.FullName)" -ForegroundColor Gray

if (Test-Path $TargetDir) {
    Write-Host "==> Cleaning old target dir ..." -ForegroundColor Yellow
    Remove-Item $TargetDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null

Write-Host "==> Copying kernel to $TargetDir ..." -ForegroundColor Yellow
robocopy $SourceDir $TargetDir /E /NFL /NDL /NJH /NJS /NP | Out-Null
if ($LASTEXITCODE -ge 8) { throw "robocopy failed: exit $LASTEXITCODE" }

if (-not (Test-Path $TargetChrome)) { throw "install failed: missing $TargetChrome" }

Write-Host "==> CloakBrowser kernel installed" -ForegroundColor Green
Write-Host "    $TargetChrome" -ForegroundColor Green
Write-Host ""
Write-Host "Next:" -ForegroundColor Cyan
Write-Host "    `$env:BOOST_KERNEL_SRC = '$InstallRoot'"
Write-Host "    powershell -ExecutionPolicy Bypass -File scripts\build_release.ps1"
Write-Host "    powershell -ExecutionPolicy Bypass -File scripts\build_installer.ps1"
