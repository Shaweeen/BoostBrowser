$ErrorActionPreference='Stop'
$src = 'Z:\BoostBrowser_v147_extension_popup_runtime_fallback'
$dst = 'Z:\BoostBrowser_v148_main_window_title_guard_shareddata'
if (Test-Path $dst) { Remove-Item $dst -Recurse -Force }
New-Item -ItemType Directory -Path $dst | Out-Null
Get-ChildItem $src -Force | Where-Object { $_.Name -ne 'data' } | ForEach-Object {
  Copy-Item $_.FullName -Destination $dst -Recurse -Force
}
New-Item -ItemType Junction -Path (Join-Path $dst 'data') -Target (Join-Path $src 'data') | Out-Null
Copy-Item 'C:\Users\Administrator\Desktop\Ant-Browser-update\build\bin\boost-browser.exe' -Destination (Join-Path $dst 'boost-browser.exe') -Force
$hash = (Get-FileHash (Join-Path $dst 'boost-browser.exe') -Algorithm SHA256).Hash.ToLower()
Set-Content -Encoding ASCII (Join-Path $dst 'boost-browser.exe.sha256') $hash
[pscustomobject]@{ Path = $dst; Sha256 = $hash; Size = (Get-Item (Join-Path $dst 'boost-browser.exe')).Length; DataMode = 'junction->v147/data' } | ConvertTo-Json -Compress
