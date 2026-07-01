$ErrorActionPreference = 'Stop'
$srcRoot = 'C:\Users\Administrator\Desktop\Ant-Browser-update\build\release'
$srcExe = Join-Path $srcRoot 'boost-browser-v1.4.5.exe'
$srcUpd = Join-Path $srcRoot 'updater.exe'
$dstRoot = 'Z:\BoostBrowser_v110_test'
$dstExe = Join-Path $dstRoot 'boost-browser.exe'
$dstUpd = Join-Path $dstRoot 'updater.exe'
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
if (-not (Test-Path -LiteralPath $srcExe -PathType Leaf)) { throw "missing source exe: $srcExe" }
if (-not (Test-Path -LiteralPath $srcUpd -PathType Leaf)) { throw "missing source updater: $srcUpd" }
if (-not (Test-Path -LiteralPath $dstRoot -PathType Container)) { throw "missing target dir: $dstRoot" }
Get-Process boost-browser,updater -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 2
if (Test-Path -LiteralPath $dstExe -PathType Leaf) { Copy-Item -LiteralPath $dstExe -Destination ($dstExe + '.bak-' + $stamp) -Force }
if (Test-Path -LiteralPath $dstUpd -PathType Leaf) { Copy-Item -LiteralPath $dstUpd -Destination ($dstUpd + '.bak-' + $stamp) -Force }
Copy-Item -LiteralPath $srcExe -Destination $dstExe -Force
Copy-Item -LiteralPath $srcUpd -Destination $dstUpd -Force
$hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $dstExe).Hash.ToLowerInvariant()
Write-Host 'UPGRADE_OK'
Write-Host $dstExe
Write-Host $hash
