# BrowserStudio v1.7.127

## 代理池「延迟」显示更真实（纯 RTT，与 Clash unified-delay 一致）

- **修复**：代理池「延迟」列之前显示的是「完整建连 + 请求」耗时——把 TCP 握手 + SOCKS5/HTTP 握手 + 远端 DNS + 请求往返全部算进去，对慢速住宅 IP 会把真实延迟放大约 3 倍（你池里 1300–2400ms 的节点，真实往返多数只有 400–800ms），误导节点挑选。
- **现在**：先建立连接并完成一次预热请求（不计时），再在同一连接上计时第二次请求，得到**纯 HTTP RTT**，与 Clash 客户端的 unified-delay 数字可比。服务端在预热后主动关闭连接等异常场景自动回退到建连耗时，不会误判「不可用」。
- 探测 UA 同步为当前内核版本 Chrome/148。

## 代理池协议兼容与网络路径审计结论

| 类目 | 结论 |
|---|---|
| 协议探测 | 声明协议优先 → 备用协议（http/socks5）并发探测 → 平局偏好 SOCKS5（住宅节点常见协议）✅ |
| socks5h | 规范化为 socks5，DNS 统一经本地 relay 走远端 ✅（Chromium 不支持 socks5 认证，全部外部代理经 relay 处理认证/协议） |
| 连通性测试 | 4 个测试站兜底（gstatic/cloudflare/msft），3s 请求切片 + 6s 总预算，TCP ping 降级，407 认证失败明确拒绝 ✅ |
| IP 健康 | 出口查询自动尝试备用协议，20s 超时 ✅ |
| 批量测速 | 8 并发 worker + 实时结果推送 + panic 恢复 ✅ |
| 环境启动探测 | 30 分钟粘性缓存 + 失败自动重探，不阻塞多开 ✅ |
| 数据路径性能 | relay 连接复用 + 25s 拨号超时 + TCP_NODELAY 已在 v1.7.126 修复 ✅ |

## 验证

- 新增 `TestSingleHTTPProxyTestReportsPureRTT`：上游代理拨号故意延迟 300ms，断言显示延迟 <250ms（修复前会把 300ms 建连算进延迟）。
- go build / go vet / go test ./...、打包测试全部通过。
