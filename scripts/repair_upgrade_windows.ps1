# Repair an existing BrowserStudio installation without replacing user data.
# This script is intentionally ASCII-only for Windows PowerShell 5.1.

[CmdletBinding()]
param(
    [string]$InstallRoot = '',
    [string]$TargetVersion = 'v1.7.26',
    [string]$GitHubOwner = 'Shaweeen',
    [string]$GitHubRepo = 'BoostBrowser',
    [switch]$NoLaunch
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Write-Step([string]$Message) {
    Write-Host ''
    Write-Host ">>> $Message" -ForegroundColor Cyan
}

function Require-File([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Missing required file: $Path"
    }
}

function Get-SHA256([string]$Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Resolve-InstallRoot([string]$RequestedRoot) {
    $candidates = New-Object System.Collections.Generic.List[string]
    if (-not [string]::IsNullOrWhiteSpace($RequestedRoot)) { $null = $candidates.Add($RequestedRoot) }

    foreach ($key in @(
        'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\BrowserStudio',
        'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\BoostBrowser'
    )) {
        $item = Get-ItemProperty -LiteralPath $key -ErrorAction SilentlyContinue
        if ($item -and $item.InstallLocation) { $null = $candidates.Add([string]$item.InstallLocation) }
    }

    if ($env:LOCALAPPDATA) {
        $null = $candidates.Add((Join-Path $env:LOCALAPPDATA 'Programs\BrowserStudio'))
        $null = $candidates.Add((Join-Path $env:LOCALAPPDATA 'Programs\BoostBrowser'))
    }
    if ($PSScriptRoot) { $null = $candidates.Add($PSScriptRoot) }
    $null = $candidates.Add((Get-Location).Path)

    foreach ($candidate in $candidates) {
        if ([string]::IsNullOrWhiteSpace($candidate)) { continue }
        $main = Join-Path $candidate 'boost-browser.exe'
        if (Test-Path -LiteralPath $main -PathType Leaf) {
            return [IO.Path]::GetFullPath($candidate).TrimEnd('\')
        }
    }
    throw 'Existing BrowserStudio installation was not found. Pass -InstallRoot with the folder containing boost-browser.exe.'
}

function Get-OwnedProcesses([string]$Root) {
    $prefix = [IO.Path]::GetFullPath($Root).TrimEnd('\') + '\'
    return @(
        Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
            $path = [string]$_.ExecutablePath
            $path -and $path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
        }
    )
}

function Stop-MainApplication([string]$Root) {
    $mainPath = Join-Path $Root 'boost-browser.exe'
    $mainProcesses = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
        $path = [string]$_.ExecutablePath
        $path -and $path.Equals($mainPath, [StringComparison]::OrdinalIgnoreCase)
    })
    foreach ($process in $mainProcesses) {
        $nativeProcess = Get-Process -Id $process.ProcessId -ErrorAction SilentlyContinue
        if ($nativeProcess) { $null = $nativeProcess.CloseMainWindow() }
    }

    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    do {
        $remaining = @(Get-OwnedProcesses $Root)
        if ($remaining.Count -eq 0) { return }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)

    $details = ($remaining | ForEach-Object { "$($_.Name) (PID $($_.ProcessId))" }) -join ', '
    throw "BrowserStudio or a managed browser is still running: $details. Save work, close all BrowserStudio windows, and retry. No files were changed."
}

function Get-Release([string]$Owner, [string]$Repo, [string]$Tag) {
    $headers = @{ 'User-Agent' = 'BrowserStudio-Repair-Upgrader' }
    $uri = "https://api.github.com/repos/$Owner/$Repo/releases/tags/$Tag"
    return Invoke-RestMethod -Uri $uri -Headers $headers -TimeoutSec 30
}

function Get-Asset($Release, [string]$Name) {
    $asset = $Release.assets | Where-Object { $_.name -eq $Name } | Select-Object -First 1
    if (-not $asset) { throw "Release is missing required asset: $Name" }
    return $asset
}

function Assert-TrustedAssetURL([string]$URL, [string]$Owner, [string]$Repo, [string]$Tag, [string]$Name) {
    $expected = "https://github.com/$Owner/$Repo/releases/download/$Tag/$Name"
    if (-not $URL.Equals($expected, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Untrusted release asset URL for ${Name}: $URL"
    }
}

function Download-File([string]$URL, [string]$Destination) {
    Invoke-WebRequest -Uri $URL -OutFile $Destination -UseBasicParsing -TimeoutSec 600
    Require-File $Destination
}

function Read-ExpectedHash([string]$Path) {
    $value = ([IO.File]::ReadAllText($Path)).Trim().ToLowerInvariant()
    if ($value -notmatch '^[0-9a-f]{64}$') { throw "Invalid SHA256 file: $Path" }
    return $value
}

function Assert-WindowsPE([string]$Path) {
    $stream = [IO.File]::OpenRead($Path)
    try {
        if ($stream.ReadByte() -ne 0x4D -or $stream.ReadByte() -ne 0x5A) {
            throw "Downloaded file is not a Windows executable: $Path"
        }
    } finally {
        $stream.Dispose()
    }
}

function Copy-BackupIfPresent([string]$Source, [string]$BackupRoot, [string]$RelativeName) {
    if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) { return }
    $destination = Join-Path $BackupRoot $RelativeName
    $parent = Split-Path -Parent $destination
    New-Item -ItemType Directory -Path $parent -Force | Out-Null
    Copy-Item -LiteralPath $Source -Destination $destination -Force
}

function Backup-CriticalState([string]$Root, [string]$BackupRoot) {
    Copy-BackupIfPresent (Join-Path $Root 'config.yaml') $BackupRoot 'state\config.yaml'
    Copy-BackupIfPresent (Join-Path $Root 'proxies.yaml') $BackupRoot 'state\proxies.yaml'
    Copy-BackupIfPresent (Join-Path $Root '.browserstudio-activation.json') $BackupRoot 'state\.browserstudio-activation.json'
    foreach ($name in @('app.db', 'app.db-wal', 'app.db-shm')) {
        Copy-BackupIfPresent (Join-Path $Root "data\$name") $BackupRoot "state\data\$name"
    }
}

$tempRoot = $null
$backupRoot = $null
$mainPath = $null
$updaterPath = $null
$replacementStarted = $false

try {
    Write-Host 'BrowserStudio non-destructive repair upgrade' -ForegroundColor White
    Write-Host "Target: $TargetVersion"

    Write-Step 'Locate the existing installation'
    $root = Resolve-InstallRoot $InstallRoot
    $mainPath = Join-Path $root 'boost-browser.exe'
    $updaterPath = Join-Path $root 'updater.exe'
    Write-Host "Install root: $root" -ForegroundColor Green

    Write-Step 'Download signed release assets from GitHub'
    $release = Get-Release $GitHubOwner $GitHubRepo $TargetVersion
    if ($release.draft -or $release.prerelease -or $release.tag_name -ne $TargetVersion) {
        throw "Release $TargetVersion is not a published stable release."
    }

    $tempRoot = Join-Path $env:TEMP ("BrowserStudioRepair_" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
    foreach ($name in @('boost-browser.exe', 'boost-browser.exe.sha256', 'updater.exe', 'updater.exe.sha256')) {
        $asset = Get-Asset $release $name
        Assert-TrustedAssetURL ([string]$asset.browser_download_url) $GitHubOwner $GitHubRepo $TargetVersion $name
        Download-File ([string]$asset.browser_download_url) (Join-Path $tempRoot $name)
    }

    foreach ($name in @('boost-browser.exe', 'updater.exe')) {
        $downloaded = Join-Path $tempRoot $name
        $expected = Read-ExpectedHash (Join-Path $tempRoot "$name.sha256")
        $actual = Get-SHA256 $downloaded
        if ($actual -ne $expected) { throw "SHA256 mismatch for $name" }
        Assert-WindowsPE $downloaded
    }
    Write-Host 'Release files passed SHA256 and PE validation.' -ForegroundColor Green

    Write-Step 'Close BrowserStudio safely'
    Stop-MainApplication $root
    Write-Host 'All BrowserStudio-owned processes are closed.' -ForegroundColor Green

    Write-Step 'Back up old program files and critical state'
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $backupRoot = Join-Path $root "data\repair-backups\$TargetVersion-$stamp"
    New-Item -ItemType Directory -Path $backupRoot -Force | Out-Null
    Copy-BackupIfPresent $mainPath $backupRoot 'program\boost-browser.exe'
    Copy-BackupIfPresent $updaterPath $backupRoot 'program\updater.exe'
    Backup-CriticalState $root $backupRoot
    Write-Host "Backup: $backupRoot" -ForegroundColor Green

    Write-Step 'Replace program files only'
    $replacementStarted = $true
    Copy-Item -LiteralPath (Join-Path $tempRoot 'boost-browser.exe') -Destination $mainPath -Force
    Copy-Item -LiteralPath (Join-Path $tempRoot 'updater.exe') -Destination $updaterPath -Force

    foreach ($path in @($mainPath, $updaterPath)) {
        $name = Split-Path -Leaf $path
        $expected = Read-ExpectedHash (Join-Path $tempRoot "$name.sha256")
        if ((Get-SHA256 $path) -ne $expected) { throw "Installed SHA256 mismatch for $name" }
    }

    $uninstallKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\BrowserStudio'
    if (Test-Path -LiteralPath $uninstallKey) {
        Set-ItemProperty -LiteralPath $uninstallKey -Name DisplayVersion -Value ($TargetVersion.TrimStart('v'))
    }

    Write-Host ''
    Write-Host "Repair upgrade to $TargetVersion completed." -ForegroundColor Green
    Write-Host 'Preserved: config, profiles, browser data, proxy pools, extensions, kernels, and activation state.'
    Write-Host "Rollback backup: $backupRoot"

    if (-not $NoLaunch) {
        Start-Process -FilePath $mainPath -WorkingDirectory $root
        Write-Host 'BrowserStudio started.' -ForegroundColor Green
    }
} catch {
    if ($replacementStarted -and $backupRoot) {
        Write-Host 'Upgrade failed; restoring previous program files...' -ForegroundColor Yellow
        $oldMain = Join-Path $backupRoot 'program\boost-browser.exe'
        $oldUpdater = Join-Path $backupRoot 'program\updater.exe'
        if (Test-Path -LiteralPath $oldMain) { Copy-Item -LiteralPath $oldMain -Destination $mainPath -Force }
        if (Test-Path -LiteralPath $oldUpdater) { Copy-Item -LiteralPath $oldUpdater -Destination $updaterPath -Force }
    }
    throw
} finally {
    if ($tempRoot -and (Test-Path -LiteralPath $tempRoot)) {
        Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
