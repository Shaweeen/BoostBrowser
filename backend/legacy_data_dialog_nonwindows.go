//go:build !windows

package backend

import "fmt"

func openLegacyDataFallbackDialog(string) (string, error) {
	return "", fmt.Errorf("当前系统没有可用的后备文件夹选择器")
}
