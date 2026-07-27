# Release-time source growth and reversible-deletion guard.
# Keep this script ASCII-only for Windows PowerShell 5.1.

param(
    [string]$BaseRef = "HEAD^",
    [int]$MaxNetGrowth = 800,
    [int]$LedgerDeletionThreshold = 80,
    [string]$ApprovedGrowthReason = ""
)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

$null = & git rev-parse --verify $BaseRef
if ($LASTEXITCODE -ne 0) {
    throw "Unable to resolve code-health base revision: $BaseRef"
}

$sourceExtensions = @(
    '.go', '.js', '.jsx', '.ts', '.tsx', '.vue', '.css', '.scss',
    '.ps1', '.py', '.nsi', '.yaml', '.yml', '.json'
)
$ignoredPrefixes = @('build/', 'frontend/dist/', 'frontend/node_modules/', 'vendor/')
$added = 0
$deleted = 0
$changes = @()

$numstat = @(& git diff --numstat --find-renames $BaseRef HEAD --)
if ($LASTEXITCODE -ne 0) {
    throw "Unable to inspect source changes from $BaseRef"
}

foreach ($line in $numstat) {
    $parts = $line -split "`t", 3
    if ($parts.Count -ne 3 -or $parts[0] -eq '-' -or $parts[1] -eq '-') {
        continue
    }
    $path = $parts[2] -replace '\\', '/'
    $ignored = $false
    foreach ($prefix in $ignoredPrefixes) {
        if ($path.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
            $ignored = $true
            break
        }
    }
    if ($ignored -or $sourceExtensions -notcontains [IO.Path]::GetExtension($path).ToLowerInvariant()) {
        continue
    }
    $fileAdded = [int]$parts[0]
    $fileDeleted = [int]$parts[1]
    $added += $fileAdded
    $deleted += $fileDeleted
    $changes += [pscustomobject]@{
        Path = $path
        Added = $fileAdded
        Deleted = $fileDeleted
        Net = $fileAdded - $fileDeleted
    }
}

$net = $added - $deleted
Write-Host "Code health: +$added -$deleted (net $net) from $BaseRef"
$changes |
    Sort-Object { [Math]::Abs($_.Net) } -Descending |
    Select-Object -First 10 |
    Format-Table Path, Added, Deleted, Net -AutoSize

$changedFiles = @(& git diff --name-only $BaseRef HEAD --)
if ($LASTEXITCODE -ne 0) {
    throw 'Unable to inspect changed file names'
}
if ($deleted -ge $LedgerDeletionThreshold -and $changedFiles -notcontains 'docs/DELETION_LEDGER.md') {
    throw "Material deletion ($deleted lines) is missing docs/DELETION_LEDGER.md"
}

$retiredSymbols = @(
    'StartGlobalSerializedWindowWatchers',
    'StartGlobalExtensionPopupSizer',
    'StartGlobalServiceWorkerDevToolsRestorer',
    'restoreBrowserWindowsAfterStartup',
    'startExtensionPopupSizer'
)
$retiredPattern = ($retiredSymbols | ForEach-Object { [Regex]::Escape($_) }) -join '|'
$savedPreference = $ErrorActionPreference
try {
    $ErrorActionPreference = 'SilentlyContinue'
    $retiredOutput = @(& git grep -n -E $retiredPattern HEAD -- backend main.go 2>$null)
    $grepExit = $LASTEXITCODE
} finally {
    $ErrorActionPreference = $savedPreference
}
if ($grepExit -eq 0 -and $retiredOutput.Count -gt 0) {
    throw "Retired window-watcher code was reintroduced:`n$($retiredOutput -join [Environment]::NewLine)"
}
if ($grepExit -ne 0 -and $grepExit -ne 1) {
    throw 'Unable to scan for retired source symbols'
}

if ($net -gt $MaxNetGrowth -and [string]::IsNullOrWhiteSpace($ApprovedGrowthReason)) {
    throw "Net source growth $net exceeds $MaxNetGrowth lines. Refactor it or pass -ApprovedGrowthReason with a reviewed reason."
}
if (-not [string]::IsNullOrWhiteSpace($ApprovedGrowthReason)) {
    Write-Host "Reviewed growth exception: $ApprovedGrowthReason" -ForegroundColor Yellow
}

Write-Host 'Code health checks passed.' -ForegroundColor Green
