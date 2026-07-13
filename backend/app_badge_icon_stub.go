//go:build !windows

package backend

// Windows Explorer 才支持按窗口设置任务栏编号。非 Windows 构建保留空实现，
// 让绑定生成、单元测试和跨平台静态检查不再被 Win32 依赖阻断。
func setBadgeForInstance(pid int, displayNumber int) error {
	return nil
}
