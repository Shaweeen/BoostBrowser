$ErrorActionPreference = 'Stop'
$json = (Invoke-WebRequest -UseBasicParsing http://127.0.0.1:62897/json/list).Content | ConvertFrom-Json
$out = 'C:\Users\Administrator\Desktop\Ant-Browser-update\tmp-cdp-targets.json'
$json | ConvertTo-Json -Depth 6 | Set-Content -Encoding UTF8 $out
Write-Host $out
