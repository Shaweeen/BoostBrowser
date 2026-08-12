# BrowserStudio v1.7.99

## P0：扩展分配后环境里真的能加载

### 根因
1. 仅写 Preferences 指向共享目录，部分内核/路径下 Chrome 不加载。
2. Chrome 137+ 默认禁用 `--load-extension`（需 `DisableLoadExtensionCommandLineSwitch`）。
3. 用户机 `extensions\imported` 若只有 `lfoeajg…`（Web Store 助手），说明业务扩展从未下载成功。

### 修复
- **分配时物化**：把扩展复制到环境  
  `Default/Extensions/<id>/<version>/`，Preferences 指向该路径。
- LaunchArgs 绑定 **环境内路径**（并保留共享路径供 heal）。
- 启动时若仍有 `--load-extension`，自动加  
  `--disable-features=DisableLoadExtensionCommandLineSwitch`。
- 有 LES 前强制 CLI；禁止把 Web Store 助手当业务扩展分配。

### 使用
1. 开代理，**重新导入** MetaMask/Rabby（确认 `imported\` 出现新 ID，不是只有 lfoeajg）。
2. **关闭全部环境** → 点分配。
3. 再打开环境 → `chrome://extensions` 应出现扩展。

## Windows 单行

```text
cd D:\BrowserStudio-src; git fetch --tags --force; git checkout release/1.7.95-from-1.7.83; git reset --hard v1.7.99; git clean -fd; git log -1 --oneline; powershell -NoProfile -ExecutionPolicy Bypass -File scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.99
```
