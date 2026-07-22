package backend

import (
	"context"
	"fmt"
	"strings"

	"boost-browser/backend/internal/logger"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var (
	showWalletImportWailsDialog = func(ctx context.Context, options wailsruntime.OpenDialogOptions) (string, error) {
		return wailsruntime.OpenFileDialog(ctx, options)
	}
	showWalletImportFallbackDialog = openWalletImportFallbackDialog
)

// selectWalletImportFile keeps wallet contents out of the WebView. The Wails
// common-file dialog is preferred, while the Windows implementation provides
// a system WinForms fallback for machines where COM dialog initialisation is
// unavailable. Only the selected path crosses the fallback process boundary.
func (a *App) selectWalletImportFile(spec walletImportSpec) (string, error) {
	options := wailsruntime.OpenDialogOptions{
		Title: "选择 " + spec.Name + " 钱包映射文件",
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "钱包映射文件 (*.csv;*.txt)", Pattern: "*.csv;*.txt"},
			{DisplayName: "CSV 文件 (*.csv)", Pattern: "*.csv"},
			{DisplayName: "文本文件 (*.txt)", Pattern: "*.txt"},
		},
	}

	filePath, primaryErr := showWalletImportWailsDialog(a.ctx, options)
	if primaryErr == nil {
		return strings.TrimSpace(filePath), nil
	}

	logger.New("WalletImport").Warn("Wails 文件选择器不可用，尝试 Windows 系统后备选择器",
		logger.F("wallet_type", spec.Type),
		logger.F("error", primaryErr.Error()),
	)
	fallbackPath, fallbackErr := showWalletImportFallbackDialog(options.Title)
	if fallbackErr != nil {
		return "", fmt.Errorf("系统文件选择器不可用（Wails: %v；Windows 后备: %v）", primaryErr, fallbackErr)
	}
	return strings.TrimSpace(fallbackPath), nil
}
