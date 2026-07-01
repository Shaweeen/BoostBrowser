$ErrorActionPreference = 'Stop'
$out = 'C:\Users\Administrator\Desktop\Ant-Browser-update\tmp-cdp-targets-raw.json'
$raw = (Invoke-WebRequest -UseBasicParsing http://127.0.0.1:62897/json/list).Content
[System.IO.File]::WriteAllText($out, $raw, (New-Object System.Text.UTF8Encoding($false)))
Write-Host $out
