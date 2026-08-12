# BrowserStudio v1.7.92

## Fix: Windows `go test` 发布失败

- 去掉不稳定的扩展下载「真实网络/环境代理」集成测试（在 Windows 用户已设置 `HTTPS_PROXY=7897` 时会误连真实代理端口而失败）。
- 改为只测固定本机网关选择与目标校验（确定性、不拨号）。

## 功能

与 v1.7.90/91 相同：扩展下载可走 `HTTPS_PROXY` / 本机转发网关。

## 发布

```text
-ExpectedVersion 1.7.92
```
