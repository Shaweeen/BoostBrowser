Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$ErrorActionPreference = 'Stop'
$p = Get-Process boost-browser | Select-Object -First 1
if (-not $p) { throw 'boost-browser not running' }
$root = [System.Windows.Automation.AutomationElement]::FromHandle($p.MainWindowHandle)
$trueCond = [System.Windows.Automation.Condition]::TrueCondition
$els = $root.FindAll([System.Windows.Automation.TreeScope]::Descendants, $trueCond)
$rows = New-Object System.Collections.Generic.List[string]
foreach ($el in $els) {
  try {
    $name = [string]$el.Current.Name
    $type = [string]$el.Current.LocalizedControlType
    $r = $el.Current.BoundingRectangle
    if ($name -or $type) {
      $rows.Add(($name -replace "`r|`n", ' ') + "\t" + $type + "\t" + [int]$r.Left + "," + [int]$r.Top + "," + [int]$r.Right + "," + [int]$r.Bottom)
    }
  } catch {}
}
$out = 'C:\Users\Administrator\Desktop\Ant-Browser-update\tmp-uia.tsv'
[System.IO.File]::WriteAllLines($out, $rows, (New-Object System.Text.UTF8Encoding($false)))
Write-Host $out
