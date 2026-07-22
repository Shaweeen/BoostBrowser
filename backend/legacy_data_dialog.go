package backend

import (
	"context"
	"fmt"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var (
	showLegacyDataWailsDialog = func(ctx context.Context, options wailsruntime.OpenDialogOptions) (string, error) {
		return wailsruntime.OpenDirectoryDialog(ctx, options)
	}
	showLegacyDataFallbackDialog = openLegacyDataFallbackDialog
)

func (a *App) selectLegacyDataDirectory() (string, error) {
	options := wailsruntime.OpenDialogOptions{Title: "选择旧版 BrowserStudio data 文件夹"}
	path, primaryErr := showLegacyDataWailsDialog(a.ctx, options)
	if primaryErr == nil {
		return strings.TrimSpace(path), nil
	}
	fallbackPath, fallbackErr := showLegacyDataFallbackDialog(options.Title)
	if fallbackErr != nil {
		return "", fmt.Errorf("系统文件夹选择器不可用（Wails: %v；Windows 后备: %v）", primaryErr, fallbackErr)
	}
	return strings.TrimSpace(fallbackPath), nil
}
