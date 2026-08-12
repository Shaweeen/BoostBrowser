# BrowserStudio v1.7.98

## 修复：检查更新走代理 + 更长超时

- 检查更新 / 下载更新 使用与扩展下载相同的 HTTP 客户端：跟随 `HTTP(S)_PROXY` / `ALL_PROXY` 与客户端「本机转发网关」。
- 检查超时 15s → **45s**，减少国内经代理仍误报超时。
- 错误文案区分：GitHub 限流/拒绝 vs **网络连不上 github.com**。

## 说明

**v1.7.97 已成功发布到 GitHub**（BoostBrowser 含 11 个 Windows 资产）。  
若应用内「检查更新」失败，多半是本机访问 GitHub 超时，不是发布没推上去。

## Windows 打包

```text
git fetch --tags --force
git reset --hard v1.7.98
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.98
```
