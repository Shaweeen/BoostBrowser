# install_chrome_for_testing_kernel.ps1
# Installs the extension-compatible Chrome for Testing build into the legacy
# google-<version> asset directory used by existing BrowserStudio profiles.
#
# Official Google Chrome builds ignore --load-extension starting with M137.
# Chrome for Testing is published by Google for automation/testing and keeps
# command-line unpacked extension loading enabled.

[CmdletBinding()]
param(
    [string]$InstallRoot = "",
    [string]$Version = "148.0.7778.167",
    [string]$SourceZip = ""
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
if ([string]::IsNullOrWhiteSpace($InstallRoot)) { $InstallRoot = $RepoRoot }

$KernelName = "google-$Version"
$ChromeRoot = Join-Path $InstallRoot "chrome"
$Destination = Join-Path $ChromeRoot $KernelName
$MarkerName = "chrome-for-testing.marker"
$MarkerPath = Join-Path $Destination $MarkerName
$ExpectedExe = Join-Path $Destination "chrome.exe"
$DownloadUrl = "https://storage.googleapis.com/chrome-for-testing-public/$Version/win64/chrome-win64.zip"

if ((Test-Path -LiteralPath $ExpectedExe) -and (Test-Path -LiteralPath $MarkerPath)) {
    Write-Host "Chrome for Testing already installed: $ExpectedExe" -ForegroundColor Green
    exit 0
}

$TempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("BrowserStudio_CfT_" + [Guid]::NewGuid().ToString("N"))
$ArchivePath = Join-Path $TempRoot "chrome-for-testing.zip"
$ExtractRoot = Join-Path $TempRoot "extract"

try {
    New-Item -ItemType Directory -Force -Path $TempRoot | Out-Null
    New-Item -ItemType Directory -Force -Path $ExtractRoot | Out-Null

    if (-not [string]::IsNullOrWhiteSpace($SourceZip)) {
        if (-not (Test-Path -LiteralPath $SourceZip)) { throw "Chrome for Testing archive not found: $SourceZip" }
        Copy-Item -LiteralPath $SourceZip -Destination $ArchivePath -Force
    } else {
        Write-Host "Downloading Chrome for Testing $Version ..." -ForegroundColor Cyan
        Invoke-WebRequest -Uri $DownloadUrl -OutFile $ArchivePath -UseBasicParsing
    }

    Expand-Archive -LiteralPath $ArchivePath -DestinationPath $ExtractRoot -Force
    $ExtractedDir = Join-Path $ExtractRoot "chrome-win64"
    $ExtractedExe = Join-Path $ExtractedDir "chrome.exe"
    if (-not (Test-Path -LiteralPath $ExtractedExe)) {
        throw "Invalid Chrome for Testing archive: chrome-win64\chrome.exe is missing"
    }

    New-Item -ItemType Directory -Force -Path $ChromeRoot | Out-Null
    if (Test-Path -LiteralPath $Destination) {
        Remove-Item -LiteralPath $Destination -Recurse -Force
    }
    Move-Item -LiteralPath $ExtractedDir -Destination $Destination
    Set-Content -LiteralPath $MarkerPath -Encoding ASCII -Value @(
        "product=chrome-for-testing",
        "version=$Version",
        "platform=win64",
        "source=$DownloadUrl"
    )

    if (-not (Test-Path -LiteralPath $ExpectedExe)) { throw "Chrome executable was not installed: $ExpectedExe" }
    Write-Host "Chrome for Testing installed: $ExpectedExe" -ForegroundColor Green
} finally {
    Remove-Item -LiteralPath $TempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
