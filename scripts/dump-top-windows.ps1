param(
  [string]$Root = 'Z:\BoostBrowser_v147_extension_popup_runtime_fallback'
)
$ErrorActionPreference = 'Stop'
Add-Type @"
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class WinEnum {
  public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowTextW(IntPtr hWnd, StringBuilder text, int maxCount);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassNameW(IntPtr hWnd, StringBuilder text, int maxCount);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint pid);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
}
"@
$procs = @{}
Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -like "$Root*" } | ForEach-Object { $procs[[uint32]$_.ProcessId] = $_ }
$rows = New-Object System.Collections.Generic.List[object]
$cb = [WinEnum+EnumWindowsProc]{
  param($hWnd, $lParam)
  $hwndPid = [uint32]0
  [WinEnum]::GetWindowThreadProcessId($hWnd, [ref]$hwndPid) | Out-Null
  if (-not $procs.ContainsKey([uint32]$hwndPid)) { return $true }
  $titleSb = New-Object System.Text.StringBuilder 512
  $classSb = New-Object System.Text.StringBuilder 256
  [WinEnum]::GetWindowTextW($hWnd, $titleSb, $titleSb.Capacity) | Out-Null
  [WinEnum]::GetClassNameW($hWnd, $classSb, $classSb.Capacity) | Out-Null
  $rect = New-Object WinEnum+RECT
  [WinEnum]::GetWindowRect($hWnd, [ref]$rect) | Out-Null
  $proc = $procs[[uint32]$hwndPid]
  $rows.Add([pscustomobject]@{
    HWND = ('0x{0:X}' -f $hWnd.ToInt64())
    PID = [uint32]$hwndPid
    Visible = [WinEnum]::IsWindowVisible($hWnd)
    Class = $classSb.ToString()
    Title = $titleSb.ToString()
    Left = $rect.Left
    Top = $rect.Top
    Right = $rect.Right
    Bottom = $rect.Bottom
    Path = $proc.ExecutablePath
    Cmd = $proc.CommandLine
  }) | Out-Null
  return $true
}
[WinEnum]::EnumWindows($cb, [IntPtr]::Zero) | Out-Null
$rows | Sort-Object PID, HWND | ConvertTo-Json -Depth 4
