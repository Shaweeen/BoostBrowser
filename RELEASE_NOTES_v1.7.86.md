# BrowserStudio v1.7.86

## 相对 v1.7.85

- 正式 **Windows 本机打包/发布通道** 版本（顺序：1.7.84 → 1.7.85 → **1.7.86**）。
- 功能与 v1.7.85 一致，请优先安装本版 Windows 构建产物。

## 自 v1.7.85 起包含的修复（本版一并生效）

### 自适应本机网络路径（不绑定任何代理工具）

- 启动环境时扫描本机已监听端口，识别 HTTP CONNECT / SOCKS5。
- 按「系统路由 + 本机转发」端到端验证环境配置的 IP 代理，选延迟最低的可用路径。
- 加长高延迟链路探测预算；避免双跳/回环误杀启动。

### 登录态 / Cookie 持久化

- 允许第三方 Cookie（OAuth 回跳），禁止 clear_on_exit 清 Cookie。
- Session 清理一次性，不碰 Cookies / Local Storage / 钱包 LES。

## 验证

- `go test ./backend/internal/proxy/ -count=1`
- Windows: `scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.86`
