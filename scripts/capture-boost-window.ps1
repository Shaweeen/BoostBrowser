Add-Type -AssemblyName System.Drawing
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class WinRectCapture {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);
}
"@
$ErrorActionPreference='Stop'
$p = Get-Process boost-browser | Select-Object -First 1
if (-not $p) { throw 'boost-browser not running' }
$rect = New-Object WinRectCapture+RECT
[WinRectCapture]::GetWindowRect($p.MainWindowHandle, [ref]$rect) | Out-Null
$w = [Math]::Max(1, $rect.Right - $rect.Left)
$h = [Math]::Max(1, $rect.Bottom - $rect.Top)
$bmp = New-Object System.Drawing.Bitmap($w, $h)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen($rect.Left, $rect.Top, 0, 0, $bmp.Size)
$out = 'C:\Users\Administrator\Desktop\Ant-Browser-update\tmp-boost-window.png'
$bmp.Save($out, [System.Drawing.Imaging.ImageFormat]::Png)
$g.Dispose(); $bmp.Dispose()
Write-Host $out
