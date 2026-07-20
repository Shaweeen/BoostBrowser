param(
    [Parameter(Mandatory = $true)]
    [string]$InstallRoot
)

$ErrorActionPreference = 'SilentlyContinue'

if ([string]::IsNullOrWhiteSpace($InstallRoot)) { exit 0 }
$root = [System.IO.Path]::GetFullPath($InstallRoot).TrimEnd('\')
$rootPrefix = $root + '\'
$mainExe = $rootPrefix + 'boost-browser.exe'
$updaterExe = $rootPrefix + 'updater.exe'

function Get-BrowserStudioProcesses {
    @(
        Get-CimInstance Win32_Process | Where-Object {
            $path = [string]$_.ExecutablePath
            $path -and (
                $path.Equals($mainExe, [System.StringComparison]::OrdinalIgnoreCase) -or
                $path.Equals($updaterExe, [System.StringComparison]::OrdinalIgnoreCase) -or
                $path.StartsWith(($rootPrefix + 'chrome\'), [System.StringComparison]::OrdinalIgnoreCase) -or
                $path.StartsWith(($rootPrefix + 'bin\'), [System.StringComparison]::OrdinalIgnoreCase)
            )
        }
    )
}

$targets = @(Get-BrowserStudioProcesses | Sort-Object ProcessId -Descending)
foreach ($process in $targets) {
    Stop-Process -Id $process.ProcessId -Force -ErrorAction SilentlyContinue
}

Start-Sleep -Milliseconds 600
$remaining = @(Get-BrowserStudioProcesses)
if ($remaining.Count -gt 0) { exit 1 }
exit 0
