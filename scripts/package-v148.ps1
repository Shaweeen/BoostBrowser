$ErrorActionPreference='Stop'
$src = 'Z:\BoostBrowser_v147_extension_popup_runtime_fallback'
$dst = 'Z:\BoostBrowser_v148_main_window_title_guard'
if (Test-Path $dst) { Remove-Item $dst -Recurse -Force }
New-Item -ItemType Directory -Path $dst | Out-Null
$robocopy = Start-Process -FilePath 'C:\Windows\System32\robocopy.exe' -ArgumentList @(
  $src,
  $dst,
  '/E','/R:1','/W:1','/NFL','/NDL','/NJH','/NJS','/NP',
  '/XD','data\default\Default\Cache','data\default\Default\Code Cache','data\default\Default\GPUCache','data\default\Default\Service Worker\CacheStorage','data\default\Default\Sessions',
  '/XF','data\default\Default\Network\Cookies','data\default\Default\Network\Cookies-journal','data\default\Default\LOCK','data\default\SingletonLock','data\default\SingletonSocket','data\default\SingletonCookie'
) -Wait -PassThru -NoNewWindow
if ($robocopy.ExitCode -gt 7) { throw "robocopy failed: $($robocopy.ExitCode)" }
Copy-Item 'C:\Users\Administrator\Desktop\Ant-Browser-update\build\bin\boost-browser.exe' -Destination (Join-Path $dst 'boost-browser.exe') -Force
$hash = (Get-FileHash (Join-Path $dst 'boost-browser.exe') -Algorithm SHA256).Hash.ToLower()
Set-Content -Encoding ASCII (Join-Path $dst 'boost-browser.exe.sha256') $hash
[pscustomobject]@{ Path = $dst; Sha256 = $hash; Size = (Get-Item (Join-Path $dst 'boost-browser.exe')).Length; RobocopyExitCode = $robocopy.ExitCode } | ConvertTo-Json -Compress
