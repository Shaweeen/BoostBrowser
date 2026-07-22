# Build and publish the public Windows release to GitHub.
# The private full installer, activation checker, browser kernels, proxy
# binaries, and local configuration are intentionally excluded.

param(
    [string]$ExpectedVersion = "",
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
        'scripts/repair_upgrade_windows.ps1',
        'scripts/test_packaging_scripts.py',
        "RELEASE_NOTES_v$Version.md"
    )
    $postTagFiles = @(& git diff --name-only $Tag HEAD)
    $unexpectedPostTagFiles = @($postTagFiles | Where-Object { $allowedPostTagFiles -notcontains $_ })
    if ($unexpectedPostTagFiles.Count -gt 0) {
        throw "HEAD differs from $Tag outside release-packaging files: $($unexpectedPostTagFiles -join ', ')"
    }
}

& git ls-remote --exit-code origin "refs/tags/$Tag" | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Release tag $Tag is not available on origin" }

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

$existing = $null
$existingProbe = Invoke-GhProbe -Arguments @('release', 'view', $Tag, '--repo', $Repository, '--json', 'tagName,isDraft,isPrerelease')
$existingText = $existingProbe.Output
if ($existingProbe.ExitCode -eq 0 -and -not [string]::IsNullOrWhiteSpace($existingText)) {
    $existing = $existingText | ConvertFrom-Json
    if (-not $existing.isDraft) {
        throw "Release $Tag is already published and will not be overwritten"
    }
} else {
    & gh release create $Tag --repo $Repository --title "BrowserStudio $Tag" --notes-file $NotesPath --verify-tag --draft
    if ($LASTEXITCODE -ne 0) { throw "Unable to create draft release $Tag" }
}

& gh release upload $Tag @assets --repo $Repository --clobber
if ($LASTEXITCODE -ne 0) { throw "Unable to upload release assets for $Tag" }

$release = (& gh release view $Tag --repo $Repository --json assets,isDraft,url) | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or -not $release.isDraft) { throw 'Release verification expected a draft release' }
$uploadedNames = @($release.assets | ForEach-Object { $_.name })
foreach ($asset in $assets) {
    $name = Split-Path -Leaf $asset
    if ($uploadedNames -notcontains $name) { throw "Uploaded release is missing asset: $name" }
}
foreach ($forbidden in @('activation-check.exe', "BrowserStudio-Private-Setup-v$Version.exe")) {
    if ($uploadedNames -contains $forbidden) { throw "Forbidden private asset was uploaded: $forbidden" }
}

& gh release edit $Tag --repo $Repository --draft=false --prerelease=false --latest
if ($LASTEXITCODE -ne 0) { throw "Unable to publish release $Tag" }

$published = (& gh release view $Tag --repo $Repository --json isDraft,isPrerelease,url) | ConvertFrom-Json
if ($LASTEXITCODE -ne 0 -or $published.isDraft -or $published.isPrerelease) {
    throw "Release $Tag was not published successfully"
}

Write-Host ''
Write-Host "Published BrowserStudio $Tag" -ForegroundColor Green
Write-Host "Commit: $Head"
Write-Host "URL: $($published.url)"
Write-Host 'Private installer and activation checker were not uploaded.' -ForegroundColor Yellow
