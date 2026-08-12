# BrowserStudio v1.7.96

## P0：关闭启动扩展自检弹窗 + 分配后必须能加载扩展

基于 `release/1.7.95-from-1.7.83` 扩展修复线。

### 1. 关闭客户端打开时的扩展完整性自检 toast

- 不再在启动约 1s 后自动 `BrowserExtensionIntegrityScanAll` 弹「首次适配 / 未完整」警告。
- 后端巡检 API 仍保留，供以后设置页手动触发（默认不弹）。

### 2. 分配成功 = Preferences 可写；首次打开保留 CLI 直到真加载

- **停机环境**：分配必须成功写入 Preferences（可加载）；否则返回错误，不再假成功。
- **全部在运行**：直接报错，要求先关闭环境再分配。
- **跳过 CLI 条件**（更严）：
  1. Preferences ENABLED + 包路径可加载  
  2. **且** Chrome 已写出该扩展的 durable 数据（LES 等）  
  - 仅 Preferences（刚分配完）→ **仍带 `--load-extension` 首次适配**，避免「分配成功但 chrome://extensions 为空」。
  - 仅 LES、路径失效 → 仍不剥 CLI（保留 1.7.87 升级保护）。

### 使用

1. Windows：`git pull` 本分支后打包 `1.7.96`  
2. 安装后：**先关闭所有环境** → 再点分配 → 再打开环境  
3. 不应再出现启动自检黄条  

## Windows 打包

```text
git fetch --tags
git checkout release/1.7.95-from-1.7.83
git pull
# wails.json 应为 1.7.96
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.96
```

若 Release 已存在，新版 `publish_windows_github_release.ps1` 会 **clobber 上传资产并确保正式发布**，不再要求必须是 draft。

## 验证

```text
go test ./backend/ -run 'TestAssignWrites|TestApplyProfileNative|TestRegisterAssigned|TestHealAssigned|TestReadOnlyDetect' -count=1
```
