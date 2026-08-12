# BrowserStudio v1.7.94

## P0：扩展分配稳定 + 热启动不弹扩展主页

以本产品 Scheme A 为准（不抄系统 External 注册表）：

- **点分配**才写入；创建环境不自动灌扩展
- **已有同一可加载扩展 → 跳过**，不覆盖 Preferences / 钱包 LES
- **停机环境**：分配时写 Preferences（location=4 + 共享包 path），成功以可加载为准
- **运行中环境**：只绑定 LaunchArgs，关闭后再开完成写入
- **启动**：只 heal **已绑定**包路径；Preferences 可加载则 **硬剥 `--load-extension`**，避免 `onInstalled` 打开扩展主页
- 分配 toast 文案分清：新绑定 / 写入 Profile / 跳过 / 延后 / 失败
- 下载失败前缀：`扩展分配失败：…`（沿用 1.7.90 代理下载）

## 验证

```text
go test ./backend/ -run 'TestAssignWrites|TestGlobalExtensionDistribution|TestRegisterAssigned' -count=1
```

## Windows 发布

```text
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.94
```
