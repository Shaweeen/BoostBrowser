# setup_new_windows_private.ps1
# Prepare a clean Windows 10/11 machine and build the private full installer.
# Run this script from a Git clone of the private repository.

param(
    [switch]$SkipGoogleFallback,
    [switch]$RunGoTests,
    [switch]$NoLaunchInstaller
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

function Refresh-Path {
    $machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $env:Path = "$machinePath;$userPath"
}

function Require-Winget {
    if (-not (Get-Command winget -ErrorAction SilentlyContinue)) {
        throw "winget was not found. Install or update 'App Installer' from Microsoft Store, then run this script again."
    }
}

function Install-WingetPackage([string]$Id, [string]$CommandName) {
    if ($CommandName -and (Get-Command $CommandName -ErrorAction SilentlyContinue)) {
        Write-Host "$CommandName is already installed; skipping $Id" -ForegroundColor Green
        return
    }

    Write-Host "Installing $Id ..." -ForegroundColor Cyan
    & winget install --id $Id --exact --silent --disable-interactivity --accept-package-agreements --accept-source-agreements
    if ($LASTEXITCODE -ne 0) { throw "winget failed to install $Id (exit $LASTEXITCODE)" }
    Refresh-Path
}

if (-not (Test-Path (Join-Path $RepoRoot ".git"))) {
    throw "This is not a Git checkout. Clone the private repository first, then run this script from its repository folder."
}

Require-Winget
Refresh-Path

Install-WingetPackage "Git.Git" "git"
Install-WingetPackage "GoLang.Go" "go"
Install-WingetPackage "OpenJS.NodeJS.LTS" "node"
Install-WingetPackage "NSIS.NSIS" "makensis"
Install-WingetPackage "Microsoft.EdgeWebView2Runtime" ""
Install-WingetPackage "Microsoft.VCRedist.2015+.x64" ""

Refresh-Path

$goMod = Get-Content (Join-Path $RepoRoot "go.mod") -Raw
$wailsMatch = [regex]::Match($goMod, 'github\.com/wailsapp/wails/v2\s+v([^\s]+)')
if (-not $wailsMatch.Success) { throw "Unable to determine the Wails version from go.mod" }
$wailsVersion = $wailsMatch.Groups[1].Value

Write-Host "Installing Wails CLI v$wailsVersion ..." -ForegroundColor Cyan
& go install "github.com/wailsapp/wails/v2/cmd/wails@v$wailsVersion"
if ($LASTEXITCODE -ne 0) { throw "Wails CLI installation failed" }

$goBin = Join-Path (& go env GOPATH) "bin"
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($userPath -split ';') -notcontains $goBin) {
    $newUserPath = if ([string]::IsNullOrWhiteSpace($userPath)) { $goBin } else { "$userPath;$goBin" }
    [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
}
Refresh-Path

$buildArgs = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", (Join-Path $RepoRoot "scripts\build_windows_selfuse.ps1"))
if ($SkipGoogleFallback) { $buildArgs += "-SkipGoogleFallback" }
if ($RunGoTests) { $buildArgs += "-RunGoTests" }

Write-Host "Building the private full installer ..." -ForegroundColor Cyan
& powershell.exe @buildArgs
if ($LASTEXITCODE -ne 0) { throw "Private installer build failed" }

$installer = Get-ChildItem (Join-Path $RepoRoot "build\release\BrowserStudio-Private-Setup-v*.exe") |
    Sort-Object LastWriteTime -Descending |
    Select-Object -First 1
if (-not $installer) { throw "The private installer was not created" }

$hash = Get-FileHash $installer.FullName -Algorithm SHA256
Write-Host "" 
Write-Host "Private installer ready:" -ForegroundColor Green
Write-Host "  $($installer.FullName)"
Write-Host "  SHA256: $($hash.Hash)"

if (-not $NoLaunchInstaller) {
    Write-Host "Launching the installer. Enter your valid installation key when prompted." -ForegroundColor Cyan
    Start-Process -FilePath $installer.FullName -Wait
}

