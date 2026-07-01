$ErrorActionPreference = 'Stop'
$repo = 'C:\Users\Administrator\Desktop\Ant-Browser-update'
Set-Location $repo
$cfg = Join-Path $repo 'wails.json'
$backupBytes = [System.IO.File]::ReadAllBytes($cfg)
try {
    $json = Get-Content -LiteralPath $cfg -Raw | ConvertFrom-Json
    $json.info.productVersion = '1.4.4'
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($cfg, (($json | ConvertTo-Json -Depth 100) + "`n"), $utf8NoBom)

    & 'C:\Users\Administrator\go\bin\wails.exe' build
    if ($LASTEXITCODE -ne 0) {
        throw "wails build failed with exit code $LASTEXITCODE"
    }

    $binExe = Join-Path $repo 'build\bin\boost-browser.exe'
    if (-not (Test-Path -LiteralPath $binExe -PathType Leaf)) {
        throw "missing built exe: $binExe"
    }

    $releaseDir = Join-Path $repo 'build\release'
    New-Item -ItemType Directory -Force -Path $releaseDir | Out-Null
    $releaseExe = Join-Path $releaseDir 'boost-browser-v1.4.4.exe'
    Copy-Item -LiteralPath $binExe -Destination $releaseExe -Force
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binExe).Hash.ToLowerInvariant()
    Set-Content -LiteralPath (Join-Path $releaseDir 'boost-browser-v1.4.4.exe.sha256') -Value $hash -NoNewline -Encoding ascii

    Write-Host "BUILD_OK"
    Write-Host $releaseExe
    Write-Host $hash
}
finally {
    [System.IO.File]::WriteAllBytes($cfg, $backupBytes)
}
