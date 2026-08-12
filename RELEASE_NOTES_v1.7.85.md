# BrowserStudio v1.7.85

## P0：Nym / 高延迟 VPN 下标准代理启动超时

截图错误：`标准代理本地转发启动失败 … Get "http://www.gstatic.com/generate_204": context deadline exceeded`。

- **启动探测预算 6s → 22s**，单次 HTTP 探测在长预算下最多 10s（原固定 3s），适配 Nym 混网 / 双跳 VPN + 住宅代理首包延迟。
- 启动验证改用 **多连通性 URL**（gstatic / cloudflare / msft），不再只打一个 generate_204。
- 超时错误文案明确提示：TUN 接管 / 填写本地网关 / 避免代理回环。

## P0：启动环境时自适应本机网络（不绑定任何代理工具）

产品原则：环境启动 **不指定某一款 VPN/代理软件**，而是：

1. **扫描本机已监听端口**（常见本地代理端口种子 + 实际 TCP 探测）
2. **识别流量协议**（HTTP CONNECT / SOCKS5 握手）
3. **按模式组合候选路径**：系统路由 + 本机转发
4. **对「环境配置的 IP 代理」做端到端验证**，选延迟最低的可用路径再启动本地转发

模式含义：

| 模式 | 行为 |
|------|------|
| 自动 | 检测本机环境，并行尝试系统路由与可用本机网关，选能通 IP 代理的路径 |
| 强制本机第一跳 | 必须经本机 HTTP/SOCKS |
| 系统隧道 | 不使用本机代理端口，只走系统路由/TUN |
| 直连 | 直连 IP 代理服务器 |

- 不按品牌优先级（Clash/Nym/…），只按 **端口存活 + 协议正确 + 端到端可达 + 延迟**。
- 启动探测预算加长、多连通性 URL，适配高延迟链路。

## P0：升级后登录态 / 邮箱反复登录 / OAuth 授权失败

- Preferences：**允许第三方 Cookie**（Privy/X OAuth）、默认允许站点 Cookie、禁止 clear_on_exit 清 Cookie。
- Session/Tabs 丢弃 **一次性**，绝不碰 Cookies / Local Storage / LES。
- 热启动不重写 Preferences。

## 验证

- `go test ./backend/internal/proxy/ -count=1`
- `go test ./backend/ -run 'TestPatchChrome|TestSanitize|TestDiscard|TestSessionWipe|TestSessionRestore|TestPreferLocal|TestGateway' -count=1`
