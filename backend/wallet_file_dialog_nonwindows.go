//go:build !windows

package backend

import "fmt"

func openWalletImportFallbackDialog(string) (string, error) {
	return "", fmt.Errorf("当前系统不支持 Windows 后备文件选择器")
}

func saveWalletImportTemplateFallbackDialog(string, string) (string, error) {
	return "", fmt.Errorf("当前系统不支持 Windows 后备模板保存选择器")
}
