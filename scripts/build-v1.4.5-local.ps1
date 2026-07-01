$ErrorActionPreference = 'Stop'
$repo = 'C:\Users\Administrator\Desktop\Ant-Browser-update'
Set-Location $repo
$cfg = Join-Path $repo 'wails.json'
$backupBytes = [System.IO.File]::ReadAllBytes($cfg)
try {
    $json = Get-Content -LiteralPath $cfg -Raw | ConvertFrom-Json
    $json.info.productVersion = '1.4.5'
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($cfg, ($json | ConvertTo-Json -Depth 100), $utf8NoBom)

    & 'C:\Users\Administrator\go\bin\wails.exe' build -clean -platform windows/amd64
    if ($LASTEXITCODE -ne 0) { throw "wails build failed: $LASTEXITCODE" }

    $releaseDir = Join-Path $repo 'build\release'
    $binExe = Join-Path $repo 'build\bin\boost-browser.exe'
    $binUpdater = Join-Path $repo 'build\bin\updater.exe'
    $fallbackUpdater = Join-Path $releaseDir 'updater.exe'
    if (!(Test-Path $binExe)) { throw "missing $binExe" }
    if (!(Test-Path $binUpdater)) {
        if (Test-Path $fallbackUpdater) {
            $binUpdater = $fallbackUpdater
        } else {
            throw "missing updater.exe in both build\\bin and build\\release"
        }
    }

    New-Item -ItemType Directory -Force -Path $releaseDir | Out-Null
    $releaseExe = Join-Path $releaseDir 'boost-browser-v1.4.5.exe'
    Copy-Item -LiteralPath $binExe -Destination $releaseExe -Force
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binExe).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $releaseDir 'boost-browser-v1.4.5.exe.sha256') -Value $hash -NoNewline -Encoding ascii
    Copy-Item -LiteralPath $binExe -Destination (Join-Path $releaseDir 'boost-browser.exe') -Force
    Set-Content -LiteralPath (Join-Path $releaseDir 'boost-browser.exe.sha256') -Value $hash -NoNewline -Encoding ascii

    $destUpdater = Join-Path $releaseDir 'updater.exe'
    $srcUpdaterFull = [System.IO.Path]::GetFullPath($binUpdater)
    $destUpdaterFull = [System.IO.Path]::GetFullPath($destUpdater)
    if ($srcUpdaterFull -ne $destUpdaterFull) {
        Copy-Item -LiteralPath $binUpdater -Destination $destUpdater -Force
    }

    Write-Host 'BUILD_OK'
    Write-Host $releaseExe
    Write-Host $hash
}
finally {
    [System.IO.File]::WriteAllBytes($cfg, $backupBytes)
}
