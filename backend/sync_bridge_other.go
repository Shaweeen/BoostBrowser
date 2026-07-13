//go:build !windows

package backend

// 输入同步桥仅在 Windows 上启用。保留非 Windows 空实现，使开发环境的
// Wails 绑定生成和后端单元测试可以完整编译。
func (a *App) startSyncBridge() {}
