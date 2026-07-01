param(
  [Parameter(Mandatory=$true)][string]$HwndHex,
  [string]$OutPath = 'C:\Users\Administrator\Desktop\Ant-Browser-update\tmp-hwnd-capture.png'
)
Add-Type -AssemblyName System.Drawing
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class WinRectCapture2 {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr hWnd, out RECT rect);
}
"@
$ErrorActionPreference='Stop'
$hwnd = [IntPtr]([Convert]::ToInt64($HwndHex.Replace('0x',''),16))
$rect = New-Object WinRectCapture2+RECT
[WinRectCapture2]::GetWindowRect($hwnd, [ref]$rect) | Out-Null
$w = [Math]::Max(1, $rect.Right - $rect.Left)
$h = [Math]::Max(1, $rect.Bottom - $rect.Top)
$bmp = New-Object System.Drawing.Bitmap($w, $h)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen($rect.Left, $rect.Top, 0, 0, $bmp.Size)
$bmp.Save($OutPath, [System.Drawing.Imaging.ImageFormat]::Png)
$g.Dispose(); $bmp.Dispose()
Write-Host $OutPath
