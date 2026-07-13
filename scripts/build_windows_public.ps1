# Builds the redistributable BrowserStudio Manager edition.
# No browser kernel or third-party proxy executable is included.
param([switch]$RunGoTests)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $RepoRoot

$args = @(
    '-NoProfile', '-ExecutionPolicy', 'Bypass',
    '-File', "$RepoRoot\scripts\build_windows_selfuse.ps1",
    '-ManagerOnly', '-SkipKernelInstall', '-SkipGoogleFallback'
)
if ($RunGoTests) { $args += '-RunGoTests' }

& powershell.exe @args
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

