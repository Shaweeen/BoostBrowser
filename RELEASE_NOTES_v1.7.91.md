# BrowserStudio v1.7.91

## 发布门禁修复（必过）

- 补齐 `docs/DELETION_LEDGER.md` **CLEAN-088**（扩展下载 HTTP 客户端去掉强制直连）。
- 功能与 **v1.7.90** 相同：扩展/订阅下载可走 `HTTPS_PROXY` 或本机转发网关。

## 本机使用（新 Windows 扩展分配）

1. 开启 Clash 等本地代理  
2. 终端/用户环境设置 `HTTPS_PROXY=http://127.0.0.1:端口`，或在客户端填「本机转发网关」  
3. 再分配扩展  

## Windows 发布

```text
-ExpectedVersion 1.7.91
```
