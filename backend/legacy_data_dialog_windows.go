//go:build windows

package backend

const legacyDataWinFormsScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = $env:BROWSERSTUDIO_DIALOG_TITLE
$dialog.ShowNewFolderButton = $false
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($dialog.SelectedPath)
    [Console]::Out.Write([Convert]::ToBase64String($bytes))
}
$dialog.Dispose()
`

func openLegacyDataFallbackDialog(title string) (string, error) {
	return runWalletWinFormsDialog(legacyDataWinFormsScript, title, "")
}
