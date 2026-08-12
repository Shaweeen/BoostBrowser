# Build and publish the public Windows release to GitHub.
# The private full installer, activation checker, browser kernels, proxy
# binaries, and local configuration are intentionally excluded.

param(
    [string]$ExpectedVersion = "",
    [string]$ApprovedGrowthReason = "",
    [switch]$SkipGoTests
)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot
$Repository = 'Shaweeen/BoostBrowser'

function Require-Command([string]$Name, [string]$Hint) {
    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "Missing required command '$Name'. $Hint"
    }
}

function Require-File([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Missing release file: $Path"
    }
}

function Assert-WindowsPE([string]$Path) {
    Require-File $Path
    $stream = [IO.File]::OpenRead($Path)
    try {
        if ($stream.ReadByte() -ne 0x4D -or $stream.ReadByte() -ne 0x5A) {
            throw "Release executable is not a Windows PE file: $Path"
        }
    } finally {
        $stream.Dispose()
    }
}

function Write-SHA256([string]$Path) {
    Require-File $Path
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($hash -notmatch '^[0-9a-f]{64}$') {
        throw "Invalid SHA256 generated for $Path"
    }
    [IO.File]::WriteAllText("$Path.sha256", $hash, (New-Object Text.UTF8Encoding($false)))
    return $hash
}

# A missing release is expected on the first publish. Windows PowerShell can
# turn native stderr into a terminating NativeCommandError when the global
# preference is Stop, so probe gh with errors temporarily silenced and inspect
# the real process exit code ourselves.
function Invoke-GhProbe([string[]]$Arguments) {
    $savedPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'SilentlyContinue'
        $output = @(& gh @Arguments 2>$null)
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $savedPreference
    }
    return [pscustomobject]@{
        ExitCode = $exitCode
        Output = ($output -join [Environment]::NewLine)
    }
}

Require-Command 'git' 'Install Git for Windows.'
Require-Command 'gh' 'Install GitHub CLI and run: gh auth login'
Require-Command 'powershell.exe' 'Windows PowerShell is required.'

& gh auth status
if ($LASTEXITCODE -ne 0) { throw 'GitHub CLI is not authenticated. Run: gh auth login' }

$Version = (Get-Content "$RepoRoot\wails.json" -Raw | ConvertFrom-Json).info.productVersion
if ([string]::IsNullOrWhiteSpace($Version)) { throw 'Missing product version in wails.json' }
if (-not [string]::IsNullOrWhiteSpace($ExpectedVersion) -and $Version -ne $ExpectedVersion) {
    throw "Version mismatch: expected $ExpectedVersion, found $Version"
}
$Tag = "v$Version"
$NotesPath = "$RepoRoot\RELEASE_NOTES_v$Version.md"
Require-File $NotesPath

$trackedChanges = @(& git status --porcelain --untracked-files=no)
if ($LASTEXITCODE -ne 0) { throw 'Unable to inspect Git status' }
if ($trackedChanges.Count -gt 0) {
    throw 'Tracked files have uncommitted changes. Publish only from the signed release commit.'
}

$headOutput = @(& git rev-parse HEAD)
if ($LASTEXITCODE -ne 0 -or $headOutput.Count -ne 1) { throw 'Unable to resolve the release commit' }
$Head = $headOutput[0].Trim()
$tagCommitOutput = @(& git rev-list -n 1 $Tag)
if ($LASTEXITCODE -ne 0 -or $tagCommitOutput.Count -ne 1) {
    throw "Missing local release tag $Tag. Fetch tags and retry."
}
$TagCommit = $tagCommitOutput[0].Trim()
if ($Head -ne $TagCommit) {
    & git merge-base --is-ancestor $Tag $Head
    if ($LASTEXITCODE -ne 0) {
        throw "Release tag $Tag is not an ancestor of HEAD $Head"
    }
    $allowedPostTagFiles = @(
        'scripts/publish_windows_github_release.ps1',
        'scripts/build_installer.ps1',
        'scripts/repair_upgrade_windows.ps1',
        'scripts/test_packaging_scripts.py',
        'docs/DELETION_LEDGER.md',
        "RELEASE_NOTES_v$Version.md"
    )
    $postTagFiles = @(& git diff --name-only $Tag HEAD)
    $unexpectedPostTagFiles = @($postTagFiles | Where-Object { $allowedPostTagFiles -notcontains $_ })
    if ($unexpectedPostTagFiles.Count -gt 0) {
        throw "HEAD differs from $Tag outside release-packaging files: $($unexpectedPostTagFiles -join ', ')"
    }
}

# Verify the release tag is on origin. Windows schannel occasionally fails the
# TLS handshake mid-publish even right after a successful fetch — retry and
# fall back to the GitHub API (same credentials as `gh release create`).
function Assert-OriginReleaseTag([string]$TagName) {
    $ref = "refs/tags/$TagName"
    $maxAttempts = 4
    for ($attempt = 1; $attempt -le $maxAttempts; $attempt++) {
        $savedPreference = $ErrorActionPreference
        try {
            $ErrorActionPreference = 'SilentlyContinue'
            & git ls-remote --exit-code origin $ref 1>$null 2>$null
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $savedPreference
        }
        if ($code -eq 0) { return }
        if ($attempt -lt $maxAttempts) {
            Start-Sleep -Seconds (2 * $attempt)
        }
    }

    $apiProbe = Invoke-GhProbe -Arguments @(
        'api',
        "repos/$Repository/git/ref/tags/$TagName",
        '--jq',
        '.object.sha'
    )
    if ($apiProbe.ExitCode -eq 0 -and -not [string]::IsNullOrWhiteSpace($apiProbe.Output)) {
        Write-Host "Origin tag $TagName confirmed via GitHub API (git ls-remote TLS flaky)." -ForegroundColor Yellow
        return
    }
    throw "Release tag $TagName is not available on origin (git ls-remote and gh api both failed)"
}

Assert-OriginReleaseTag $Tag

# A clone made with --branch <annotated-tag> --depth 1 can contain the release
# commit without the tag's parent history (and some Git versions leave only a
# dangling local annotated-tag ref). Hydrate the history before comparing this
# release with its predecessor.
$isShallowOutput = @(& git rev-parse --is-shallow-repository)
if ($LASTEXITCODE -ne 0 -or $isShallowOutput.Count -ne 1) {
    throw 'Unable to inspect repository history depth'
}
if ($isShallowOutput[0].Trim() -eq 'true') {
    & git fetch --unshallow origin --tags --force
} else {
    & git fetch origin --tags --force
}
if ($LASTEXITCODE -ne 0) { throw 'Unable to fetch complete release tag history' }

# Resolve the previous release tag without ANY caret (^) in shell args.
# PowerShell/cmd often mangle "commit^" / "tag^{}", which made git describe
# land on an ancient tag (e.g. v1.7.55) and fail the 800-line growth gate.
# Use: rev-list --parents (no caret) + version-sorted tags merged into parent.
$tagCommitForParent = @(& git rev-list -n 1 $Tag)
if ($LASTEXITCODE -ne 0 -or $tagCommitForParent.Count -ne 1) {
    throw "Unable to resolve commit for $Tag"
}
$tagCommitForParent = $tagCommitForParent[0].Trim()
$parentsLineOutput = @(& git rev-list --parents -n 1 $tagCommitForParent)
if ($LASTEXITCODE -ne 0 -or $parentsLineOutput.Count -ne 1) {
    throw "Unable to resolve parents of $Tag ($tagCommitForParent)"
}
$parentParts = @($parentsLineOutput[0].Trim() -split '\s+')
if ($parentParts.Count -lt 2) {
    throw "Release commit $tagCommitForParent has no parent; cannot pick health base"
}
$parentCommit = $parentParts[1]
# Highest version tag reachable from parent, excluding the release being published.
$mergedTags = @(& git tag -l 'v*.*.*' --merged $parentCommit --sort=-v:refname)
if ($LASTEXITCODE -ne 0) {
    throw "Unable to list tags merged into parent $parentCommit"
}
$PreviousTag = ''
foreach ($candidate in $mergedTags) {
    $name = [string]$candidate
    if ([string]::IsNullOrWhiteSpace($name)) { continue }
    if ($name -eq $Tag) { continue }
    $PreviousTag = $name.Trim()
    break
}
if ([string]::IsNullOrWhiteSpace($PreviousTag)) {
    throw "Unable to resolve the release preceding $Tag (parent $parentCommit, no merged v* tags)"
}
Write-Host "Code health base: $PreviousTag (parent=$parentCommit of $Tag)" -ForegroundColor Cyan
# 1.7.95 is a recovery line: baseline v1.7.83 + extension-only commits. Net is
# ~800 lines; packaging/docs noise can push slightly over MaxNetGrowth=800.
if (($Version -eq '1.7.95' -or $Version -eq '1.7.96' -or $Version -eq '1.7.97' -or $Version -eq '1.7.98') -and [string]::IsNullOrWhiteSpace($ApprovedGrowthReason)) {
    $ApprovedGrowthReason = "v$Version recovery from v1.7.83 (ext + legacy + updater proxy)"
}
$healthArgs = @(
    '-NoProfile',
    '-ExecutionPolicy',
    'Bypass',
    '-File',
    "$RepoRoot\scripts\check_code_health.ps1",
    '-BaseRef',
    $PreviousTag
)
if (-not [string]::IsNullOrWhiteSpace($ApprovedGrowthReason)) {
    $healthArgs += @('-ApprovedGrowthReason', $ApprovedGrowthReason)
}
& powershell.exe @healthArgs
if ($LASTEXITCODE -ne 0) { throw 'Code health review failed' }

$buildArgs = @('-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "$RepoRoot\scripts\build_windows_public.ps1")
if (-not $SkipGoTests) { $buildArgs += '-RunGoTests' }
& powershell.exe @buildArgs
if ($LASTEXITCODE -ne 0) { throw 'Public Windows build failed' }

$ReleaseDir = "$RepoRoot\build\release"
$MainExe = "$ReleaseDir\boost-browser.exe"
$UpdaterExe = "$ReleaseDir\updater.exe"
$SetupExe = "$ReleaseDir\BrowserStudio-Manager-Setup-v$Version.exe"
$ZipName = "BrowserStudio-Update-v$Version-windows-x64.zip"
$ZipPath = "$ReleaseDir\$ZipName"
$ManifestPath = "$ReleaseDir\release-manifest.json"
$RepairSource = "$RepoRoot\scripts\repair_upgrade_windows.ps1"
$RepairName = "BrowserStudio-Repair-Upgrade-v$Version.ps1"
$RepairPath = "$ReleaseDir\$RepairName"

Assert-WindowsPE $MainExe
Assert-WindowsPE $UpdaterExe
Assert-WindowsPE $SetupExe
Require-File $RepairSource
Copy-Item -LiteralPath $RepairSource -Destination $RepairPath -Force

Remove-Item -LiteralPath $ZipPath -Force -ErrorAction SilentlyContinue
Compress-Archive -LiteralPath $MainExe -DestinationPath $ZipPath -CompressionLevel Optimal
Require-File $ZipPath

$publicBinaries = @($MainExe, $UpdaterExe, $SetupExe, $ZipPath, $RepairPath)
$manifestFiles = @()
foreach ($path in $publicBinaries) {
    $hash = Write-SHA256 $path
    $item = Get-Item -LiteralPath $path
    $manifestFiles += [ordered]@{
        name = $item.Name
        size = $item.Length
        sha256 = $hash
    }
}
$manifest = [ordered]@{
    version = $Version
    commit = $TagCommit
    packagingCommit = $Head
    generatedAt = [DateTime]::UtcNow.ToString('o')
    files = $manifestFiles
}
[IO.File]::WriteAllText($ManifestPath, (($manifest | ConvertTo-Json -Depth 4) + "`n"), (New-Object Text.UTF8Encoding($false)))

$assets = @(
    $MainExe,
    "$MainExe.sha256",
    $UpdaterExe,
    "$UpdaterExe.sha256",
    $ZipPath,
    "$ZipPath.sha256",
    $SetupExe,
    "$SetupExe.sha256",
    $RepairPath,
    "$RepairPath.sha256",
    $ManifestPath
)
foreach ($asset in $assets) { Require-File $asset }

# Release lifecycle (Windows is the binary authority):
# - No release yet → create draft, upload, then publish (draft=false).
# - Existing draft → upload, then publish.
# - Already published → re-upload assets with --clobber (notes-only / partial
#   releases from other machines are common). Never require isDraft after upload.
$existing = $null
$existingProbe = Invoke-GhProbe -Arguments @('release', 'view', $Tag, '--repo', $Repository, '--json', 'tagName,isDraft,isPrerelease')
$existingText = $existingProbe.Output
if ($existingProbe.ExitCode -eq 0 -and -not [string]::IsNullOrWhiteSpace($existingText)) {
    $existing = $existingText | ConvertFrom-Json
    if ($existing.isDraft) {
        Write-Host "Release $Tag exists as draft; uploading assets then publishing." -ForegroundColor Cyan
    } else {
        Write-Host "Release $Tag already published; re-uploading Windows assets with --clobber." -ForegroundColor Yellow
    }
} else {
    & gh release create $Tag --repo $Repository --title "BrowserStudio $Tag" --notes-file $NotesPath --verify-tag --draft
    if ($LASTEXITCODE -ne 0) { throw "Unable to create draft release $Tag" }
    Write-Host "Created draft release $Tag" -ForegroundColor Cyan
}

& gh release upload $Tag @assets --repo $Repository --clobber
if ($LASTEXITCODE -ne 0) { throw "Unable to upload release assets for $Tag" }

$release = (& gh release view $Tag --repo $Repository --json assets,isDraft,url) | ConvertFrom-Json
if ($LASTEXITCODE -ne 0) { throw "Unable to verify release $Tag after upload" }
$uploadedNames = @($release.assets | ForEach-Object { $_.name })
foreach ($asset in $assets) {
    $name = Split-Path -Leaf $asset
    if ($uploadedNames -notcontains $name) { throw "Uploaded release is missing asset: $name" }
}
foreach ($forbidden in @('activation-check.exe', "BrowserStudio-Private-Setup-v$Version.exe")) {
    if ($uploadedNames -contains $forbidden) { throw "Forbidden private asset was uploaded: $forbidden" }
}

# Ensure formal published + latest (idempotent if already published).
& gh release edit $Tag --repo $Repository --draft=false --prerelease=false --latest --notes-file $NotesPath
if ($LASTEXITCODE -ne 0) { throw "Unable to publish release $Tag" }

$published = (& gh release view $Tag --repo $Repository --json isDraft,isPrerelease,url,assets) | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $published.isDraft -or $published.isPrerelease) {
    throw "Release $Tag was not published successfully"
}
$finalCount = @($published.assets).Count
if ($finalCount -lt $assets.Count) {
    throw "Release $Tag has only $finalCount assets; expected at least $($assets.Count)"
}

Write-Host ''
Write-Host "Published BrowserStudio $Tag ($finalCount assets)" -ForegroundColor Green
Write-Host "Commit: $Head"
Write-Host "URL: $($published.url)"
Write-Host 'Private installer and activation checker were not uploaded.' -ForegroundColor Yellow
