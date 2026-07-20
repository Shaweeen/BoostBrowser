# build_windows_selfuse.ps1
# One-click Windows self-use build:
#   1. Ensure CloakBrowser free v146 kernel is installed
#   2. Optionally install extension-compatible Chrome for Testing 148
#   3. Build frontend
#   4. Build lightweight release files
#   5. Build full NSIS installer with bundled kernels
#
# Usage:
#   powershell -ExecutionPolicy Bypass -File scripts\build_windows_selfuse.ps1
# Options:
#   -SkipKernelInstall        Do not download/install CloakBrowser kernel
#   -SkipGoogleFallback      Do not install Chrome for Testing fallback
#   -RunGoTests              Run go test ./... before packaging
#   -SourceZip <path>        Use an already downloaded cloakbrowser-windows-x64.zip
#   -AssetRoot <path>        Asset root containing chrome\..., defaults to repo root
#   -ManagerOnly             Build redistributable manager without bundled runtimes
#   -NoInstall               Build Setup only; do not launch it after packaging

param(
    [switch]$SkipKernelInstall,
    [switch]$SkipGoogleFallback,
    [switch]$RunGoTests,
    [switch]$ManagerOnly,
    [switch]$NoInstall,
    [string]$SourceZip = "",
    [string]$AssetRoot = ""
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot
$BuildVersion = (Get-Content "$RepoRoot\wails.json" -Raw | ConvertFrom-Json).info.productVersion
if ([string]::IsNullOrWhiteSpace($BuildVersion)) { throw "Missing info.productVersion in wails.json" }

if ([string]::IsNullOrWhiteSpace($AssetRoot)) { $AssetRoot = $RepoRoot }
$env:BOOST_KERNEL_SRC = $AssetRoot

function Require-Command([string]$Name, [string]$InstallHint) {
    $cmd = Get-Command $Name -ErrorAction SilentlyContinue
    if (-not $cmd) { throw "Missing required command '$Name'. $InstallHint" }
}

function Require-MinimumVersion([string]$Label, [string]$Value, [version]$Minimum) {
    $match = [regex]::Match($Value, '(\d+)\.(\d+)(?:\.(\d+))?')
    if (-not $match.Success) { throw "Unable to parse $Label version: $Value" }
    $patch = if ($match.Groups[3].Success) { $match.Groups[3].Value } else { '0' }
    $actual = [version]("{0}.{1}.{2}" -f $match.Groups[1].Value, $match.Groups[2].Value, $patch)
    if ($actual -lt $Minimum) { throw "$Label $Minimum or newer is required; found $actual" }
}

function Run-Step([string]$Title, [scriptblock]$Block) {
    Write-Host ""
    Write-Host "================================================================" -ForegroundColor Cyan
    Write-Host "  $Title" -ForegroundColor Cyan
    Write-Host "================================================================" -ForegroundColor Cyan
    & $Block
}

Run-Step "Checking toolchain" {
    Require-Command node "Install Node.js 20+ from https://nodejs.org/"
    Require-Command npm "Install Node.js 20+ from https://nodejs.org/"
    Require-Command go "Install Go 1.22+ from https://go.dev/dl/"
    Require-Command wails "Run: go install github.com/wailsapp/wails/v2/cmd/wails@latest ; then add %USERPROFILE%\go\bin to PATH"
    $makensis = Get-Command makensis -ErrorAction SilentlyContinue
    if (-not $makensis) {
        $candidates = @('C:\Program Files (x86)\NSIS\makensis.exe', 'C:\Program Files\NSIS\makensis.exe')
        $makensis = $candidates | Where-Object { Test-Path $_ } | Select-Object -First 1
    }
    if (-not $makensis) { throw "Missing makensis. Install NSIS from https://nsis.sourceforge.io/Download" }
    Require-MinimumVersion "Node.js" (node -v) ([version]'20.0.0')
    Require-MinimumVersion "Go" (go version) ([version]'1.25.0')
    Write-Host "node:     $((node -v))" -ForegroundColor Green
    Write-Host "npm:      $((npm -v))" -ForegroundColor Green
    Write-Host "go:       $((go version))" -ForegroundColor Green
    Write-Host "wails:    $((wails version))" -ForegroundColor Green
    Write-Host "makensis: found" -ForegroundColor Green
}

Run-Step "Preparing CloakBrowser kernel" {
    if ($ManagerOnly) {
        Write-Host "Manager-only edition: no browser kernel will be installed or bundled" -ForegroundColor Green
        return
    }
    if (-not $SkipKernelInstall) {
        $args = @('-ExecutionPolicy', 'Bypass', '-File', "$RepoRoot\scripts\install_cloakbrowser_kernel.ps1", '-InstallRoot', $AssetRoot)
        if (-not [string]::IsNullOrWhiteSpace($SourceZip)) { $args += @('-SourceZip', $SourceZip) }
        & powershell @args
        if ($LASTEXITCODE -ne 0) { throw "install_cloakbrowser_kernel.ps1 failed" }
    } else {
        Write-Host "Skipping CloakBrowser kernel installer by request" -ForegroundColor Yellow
    }

    $cloakChrome = Join-Path $AssetRoot 'chrome\cloak-146.0.7680.177\chrome.exe'
    if (-not (Test-Path $cloakChrome)) { throw "Missing required CloakBrowser kernel: $cloakChrome" }
    Write-Host "CloakBrowser kernel OK: $cloakChrome" -ForegroundColor Green
}

Run-Step "Preparing optional extension-compatible Chrome fallback" {
    if ($ManagerOnly) {
        Write-Host "Manager-only edition: Chrome fallback redistribution is disabled" -ForegroundColor Green
        return
    }
    $googleDst = Join-Path $AssetRoot 'chrome\google-148.0.7778.167'
    $googleExe = Join-Path $googleDst 'chrome.exe'
    $compatMarker = Join-Path $googleDst 'chrome-for-testing.marker'
    if ($SkipGoogleFallback) {
        Write-Host "Skipping Google fallback by request" -ForegroundColor Yellow
    } elseif ((Test-Path $googleExe) -and (Test-Path $compatMarker)) {
        Write-Host "Extension-compatible Chrome fallback already exists: $googleExe" -ForegroundColor Green
    } else {
        & powershell -ExecutionPolicy Bypass -File "$RepoRoot\scripts\install_chrome_for_testing_kernel.ps1" -InstallRoot $AssetRoot -Version '148.0.7778.167'
        if ($LASTEXITCODE -ne 0) { throw "install_chrome_for_testing_kernel.ps1 failed" }
        if (-not (Test-Path $googleExe) -or -not (Test-Path $compatMarker)) {
            throw "Extension-compatible Chrome fallback is incomplete: $googleDst"
        }
    }
}

Run-Step "Building frontend" {
    Push-Location "$RepoRoot\frontend"
    try {
        npm ci
        if ($LASTEXITCODE -ne 0) {
            Write-Host "npm ci failed; retrying with npm install" -ForegroundColor Yellow
            npm install
            if ($LASTEXITCODE -ne 0) { throw "npm install failed" }
        }
        npm run build
        if ($LASTEXITCODE -ne 0) { throw "npm run build failed" }
    } finally {
        Pop-Location
    }
}

Run-Step "Preparing Go dependencies and preflight compile" {
    go mod download
    if ($LASTEXITCODE -ne 0) { throw "go mod download failed" }

    # Fast preflight: catch missing Go symbols before the slower Wails build.
    # This compiles the same Windows/amd64 targets but does not run tests.
    $preflightDir = Join-Path $env:TEMP "boost-browser-preflight"
    New-Item -ItemType Directory -Force -Path $preflightDir | Out-Null
    $targets = @(
        @{ Package = "."; Output = "boost-main.test.exe" },
        @{ Package = "./backend"; Output = "boost-backend.test.exe" },
        @{ Package = "./backend/cmd/updater"; Output = "boost-updater.test.exe" }
    )
    foreach ($target in $targets) {
        $out = Join-Path $preflightDir $target.Output
        Write-Host "Preflight compiling $($target.Package) ..." -ForegroundColor Yellow
        & go test -c $target.Package -o $out
        if ($LASTEXITCODE -ne 0) { throw "Go preflight compile failed for $($target.Package)" }
    }

    if ($RunGoTests) {
        go test ./...
        if ($LASTEXITCODE -ne 0) { throw "go test ./... failed" }
    } else {
        Write-Host "Skipping go test ./... (pass -RunGoTests to enable)" -ForegroundColor Yellow
    }
}

Run-Step "Building release binaries" {
    & powershell -ExecutionPolicy Bypass -File "$RepoRoot\scripts\build_release.ps1"
    if ($LASTEXITCODE -ne 0) { throw "build_release.ps1 failed" }
}

Run-Step "Building installer" {
    $installerArgs = @('-ExecutionPolicy', 'Bypass', '-File', "$RepoRoot\scripts\build_installer.ps1")
    if ($ManagerOnly) { $installerArgs += '-ManagerOnly' }
    & powershell @installerArgs
    if ($LASTEXITCODE -ne 0) { throw "build_installer.ps1 failed" }
}

Run-Step "Build output" {
    Get-ChildItem "$RepoRoot\build\release" | Sort-Object Name | ForEach-Object {
        Write-Host ("  - {0,-45} {1,12:N0} bytes" -f $_.Name, $_.Length) -ForegroundColor White
    }
    Write-Host ""
    $edition = if ($ManagerOnly) { 'public manager' } else { 'private full' }
    Write-Host "Done. $edition installer is in: $RepoRoot\build\release" -ForegroundColor Green
}

if (-not $NoInstall) {
    Run-Step "Starting Setup installer" {
        $setupName = if ($ManagerOnly) {
            "BrowserStudio-Manager-Setup-v$BuildVersion.exe"
        } else {
            "BrowserStudio-Private-Setup-v$BuildVersion.exe"
        }
        $setupPath = Join-Path "$RepoRoot\build\release" $setupName
        if (-not (Test-Path -LiteralPath $setupPath)) { throw "Missing completed Setup installer: $setupPath" }
        Write-Host "Starting: $setupPath" -ForegroundColor Green
        $setupProcess = Start-Process -FilePath $setupPath -Wait -PassThru
        if ($setupProcess.ExitCode -ne 0) { throw "Setup installer failed or was cancelled: exit $($setupProcess.ExitCode)" }
    }
} else {
    Write-Host "Setup launch skipped by -NoInstall" -ForegroundColor Yellow
}
