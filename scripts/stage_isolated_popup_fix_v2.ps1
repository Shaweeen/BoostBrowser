$ErrorActionPreference = 'Stop'
$src = 'Z:\Boost Browser'
$dst = 'Z:\Boost Browser_fix_extension_startup_cleanup_hook_20260613_202000'
$releaseDir = 'C:\Users\Administrator\Desktop\Ant-Browser-update\build\release'

if (Test-Path $dst) {
    Remove-Item -LiteralPath $dst -Recurse -Force
}
New-Item -ItemType Directory -Path $dst | Out-Null

$robocopyArgs = @(
    $src,
    $dst,
    '/E','/R:1','/W:1','/NFL','/NDL','/NJH','/NJS','/NP',
    '/XF',
    'Cookies','Cookies-journal','Session_*','Tabs_*','LOCK','LOCKFILE','*.tmp'
)
& robocopy @robocopyArgs
$rc = $LASTEXITCODE
if ($rc -ge 16) {
    throw "robocopy failed with exit code $rc"
}

Copy-Item (Join-Path $releaseDir 'boost-browser.exe') (Join-Path $dst 'Boost Browser.exe') -Force
Copy-Item (Join-Path $releaseDir 'updater.exe') (Join-Path $dst 'updater.exe') -Force

$boostSha = (Get-FileHash (Join-Path $dst 'Boost Browser.exe') -Algorithm SHA256).Hash.ToLower()
$updaterSha = (Get-FileHash (Join-Path $dst 'updater.exe') -Algorithm SHA256).Hash.ToLower()

[pscustomobject]@{
    dst = $dst
    robocopy_exit = $rc
    boost_sha = $boostSha
    updater_sha = $updaterSha
} | ConvertTo-Json -Compress
