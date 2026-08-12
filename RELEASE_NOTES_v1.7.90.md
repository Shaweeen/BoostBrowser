# BrowserStudio v1.7.90

## P0：新环境扩展分配失败（下载走代理）

### 原因
扩展/订阅下载使用的 HTTP 客户端强制 `Proxy=nil`，不读 `HTTP_PROXY`/`HTTPS_PROXY`，也不走本机 Clash 等网关。新 Windows 在国内网络下无法访问 Google CRX 更新接口，表现为「扩展分配失败」。

### 修复
- 公网下载在校验目标地址非内网后，**跟随环境代理**（`HTTP(S)_PROXY` / `ALL_PROXY`）。
- 允许连接 **本机代理端口**（127.0.0.1）作为第一跳，目标仍禁止内网 SSRF。
- 若客户端设置了「本机转发网关」，扩展下载也会使用该地址。
- 下载失败提示中说明如何配置代理或改用 .crx/.zip 直链。

### 使用
1. 开启 Clash/Nym 等本地代理  
2. 系统代理或环境变量：`HTTPS_PROXY=http://127.0.0.1:7897`（端口按实际）  
3. 或在客户端设置「本机转发网关」  
4. 再执行扩展分配  

## 验证
- `go test ./backend/ -run 'TestPublicRemote|TestEnvHTTP|TestValidatePublic' -count=1`
