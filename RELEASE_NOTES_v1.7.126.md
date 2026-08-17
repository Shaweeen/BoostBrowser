# BrowserStudio v1.7.126

## 修复环境内网页加载极慢、部分页面加载不完整的问题

同一网络/代理环境下出现「有的页面正常加载、有的加载不出、数据加载极其缓慢」——典型表现为 X（Twitter）登录页报 **Something went wrong**（页面外壳加载了但 API 请求连接失败）、Discord 登录按钮长时间卡在加载中。根因在标准代理（http/socks5 代理池节点）本地 relay 的数据路径：

- **HTTP 请求不再每请求新建连接**：relay 之前为每个 HTTP 请求单独创建 Transport，等于每个资源都重新 TCP 握手 + 上游代理握手 + DNS，多资源页面极慢且个别连接容易失败。现在 relay 持有**共享连接池 transport**（keep-alive + HTTP/2），同一站点后续请求复用同一条上游连接，加载速度显著提升、连接失败大幅减少。
- **代理隧道拨号超时 45s → 25s**：Chrome 对代理隧道自身的等待约 30s，relay 等 45s 只会让失败的请求白白挂起 15s+。现在快速失败，浏览器立刻在新隧道上重试，页面不再长时间转圈。
- **关闭 Nagle 算法（TCP_NODELAY）**：Discord / X 等交互式 API 的每次请求往返不再额外等待约 40ms 数据合并，授权流程更跟手。
- 以上仅影响非 cloak 内核环境的**标准代理 relay 路径**；直连 / xray / sing-box 桥接路径不受影响，不改写用户数据与登录状态。

## 验证

- 新增回归测试 `TestStandardRelaySharedTransportReusesUpstream`：两次请求经 relay 到同一站点只建立 1 条上游连接（修复前是 2 条），证明连接复用生效。
- go build / go vet / go test ./...、打包测试全部通过。

> 提示：若你的代理池节点是 vmess / vless / trojan / ss 协议（走 xray 桥接），登录慢主要取决于节点自身延迟与 DNS，不在本次修复范围；请确认失败环境使用的是 http/socks5 代理。
