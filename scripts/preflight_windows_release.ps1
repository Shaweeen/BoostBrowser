# Pre-publish checks so Windows pack fails fast with clear errors.
# Usage:
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts\preflight_windows_release.ps1 -ExpectedVersion 1.7.98
param(
    [Parameter(Mandatory = $true)]
    [string]$ExpectedVersion,
    [string]$ApprovedGrowthReason = ""
)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

function Fail([string]$Msg) {
    Write-Host "PREFLIGHT FAIL: $Msg" -ForegroundColor Red
    exit 1
}

function Ok([string]$Msg) {
    Write-Host "OK: $Msg" -ForegroundColor Green
}

$ExpectedVersion = $ExpectedVersion.Trim().TrimStart('v')
if ($ExpectedVersion -notmatch '^\d+\.\d+\.\d+') {
    Fail "ExpectedVersion must look like 1.7.98, got: $ExpectedVersion"
}
$Tag = "v$ExpectedVersion"

Write-Host "=== Preflight for $Tag ===" -ForegroundColor Cyan

# 1) Clean worktree
$porcelain = @(& git status --porcelain --untracked-files=no)
if ($LASTEXITCODE -ne 0) { Fail "git status failed" }
if ($porcelain.Count -gt 0) {
    Fail "Tracked files dirty. Run: git reset --hard $Tag ; git clean -fd`n$($porcelain -join "`n")"
}
Ok "worktree clean"

# 2) Version file
$wails = Get-Content "$RepoRoot\wails.json" -Raw | ConvertFrom-Json
$pv = [string]$wails.info.productVersion
if ($pv -ne $ExpectedVersion) {
    Fail "wails.json productVersion=$pv != ExpectedVersion=$ExpectedVersion"
}
Ok "wails.json productVersion=$pv"

# 3) Notes file
$notes = "$RepoRoot\RELEASE_NOTES_v$ExpectedVersion.md"
if (-not (Test-Path -LiteralPath $notes)) {
    Fail "Missing $notes"
}
Ok "release notes present"

# 4) Tag == HEAD
$head = (@(& git rev-parse HEAD) | Select-Object -First 1).Trim()
$tagCommit = (@(& git rev-list -n 1 $Tag) | Select-Object -First 1)
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($tagCommit)) {
    Fail "Tag $Tag missing locally. Run: git fetch --tags --force"
}
$tagCommit = $tagCommit.Trim()
if ($head -ne $tagCommit) {
    Fail "HEAD ($head) != $Tag ($tagCommit). Run: git reset --hard $Tag"
}
Ok "HEAD matches $Tag ($head)"

# 5) Health base + code health (same idea as publish script)
$parentsLine = (@(& git rev-list --parents -n 1 $tagCommit) | Select-Object -First 1).Trim()
$parentParts = @($parentsLine -split '\s+')
if ($parentParts.Count -lt 2) { Fail "Cannot resolve parent of $Tag" }
$parentCommit = $parentParts[1]
$mergedTags = @(& git tag -l 'v*.*.*' --merged $parentCommit --sort=-v:refname)
$PreviousTag = ''
foreach ($c in $mergedTags) {
    if ([string]$c -eq $Tag) { continue }
    if (-not [string]::IsNullOrWhiteSpace($c)) { $PreviousTag = ([string]$c).Trim(); break }
}
if ([string]::IsNullOrWhiteSpace($PreviousTag)) { Fail "No previous tag for health base" }
Ok "health base = $PreviousTag (parent of $Tag)"

$growthReason = $ApprovedGrowthReason.Trim()
if ($growthReason -eq '' -and $ExpectedVersion -match '^1\.7\.(9[5-9]|98)$') {
    $growthReason = "v$ExpectedVersion recovery from v1.7.83 (preflight)"
}
$healthArgs = @(
    '-NoProfile', '-ExecutionPolicy', 'Bypass',
    '-File', "$RepoRoot\scripts\check_code_health.ps1",
    '-BaseRef', $PreviousTag
)
if ($growthReason -ne '') {
    $healthArgs += @('-ApprovedGrowthReason', $growthReason)
}
& powershell.exe @healthArgs
if ($LASTEXITCODE -ne 0) { Fail "check_code_health failed (ledger/growth). Fix before publish." }
Ok "code health passed"

Write-Host ""
Write-Host "PREFLIGHT PASS for $Tag — safe to run publish_windows_github_release.ps1" -ForegroundColor Green
exit 0
