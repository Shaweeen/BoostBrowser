$ErrorActionPreference = 'Stop'
$p = Get-Process boost-browser -ErrorAction SilentlyContinue
if ($p) {
  $p | Stop-Process -Force
  Start-Sleep -Seconds 2
}
Set-Location 'Z:\BoostBrowser_v110_test'
Start-Process -FilePath '.\boost-browser.exe' -WorkingDirectory 'Z:\BoostBrowser_v110_test'
Start-Sleep -Seconds 6
Get-Process boost-browser -ErrorAction SilentlyContinue |
  Select-Object Id, ProcessName, MainWindowTitle |
  Format-Table -AutoSize | Out-String -Width 220 | Write-Host
