//go:build windows

package backend

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const walletImportWinFormsScript = `$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.OpenFileDialog
$dialog.Title = $env:BROWSERSTUDIO_DIALOG_TITLE
$dialog.Filter = 'Wallet mapping files (*.csv;*.txt)|*.csv;*.txt|CSV files (*.csv)|*.csv|Text files (*.txt)|*.txt'
$dialog.Multiselect = $false
$dialog.CheckFileExists = $true
$dialog.CheckPathExists = $true
$dialog.RestoreDirectory = $true
if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($dialog.FileName)
    [Console]::Out.Write([Convert]::ToBase64String($bytes))
}
$dialog.Dispose()
`

func openWalletImportFallbackDialog(title string) (string, error) {
	powershellPath := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(powershellPath); err != nil {
		resolved, lookErr := exec.LookPath("powershell.exe")
		if lookErr != nil {
			return "", fmt.Errorf("未找到 Windows PowerShell: %w", lookErr)
		}
		powershellPath = resolved
	}

	cmd := exec.Command(powershellPath, "-NoLogo", "-NoProfile", "-NonInteractive", "-STA", "-Command", walletImportWinFormsScript)
	cmd.Env = append(os.Environ(), "BROWSERSTUDIO_DIALOG_TITLE="+title)
	// CREATE_NO_WINDOW suppresses the PowerShell console without hiding the
	// WinForms file picker that the user must interact with.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("Windows 文件选择器启动失败: %s", detail)
	}

	encoded := strings.TrimSpace(string(output))
	if encoded == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("Windows 文件选择器返回结果无效: %w", err)
	}
	return strings.TrimSpace(string(decoded)), nil
}
