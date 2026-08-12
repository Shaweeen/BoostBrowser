# BrowserStudio v1.7.87

## P0：升级后扩展全部消失 / 无法重新导入

### 原因
- 仅凭钱包 **Local Extension Settings (LES)** 就判定扩展已安装并 **剥离 `--load-extension`**。
- 客户端升级后 Preferences 里的扩展包 **绝对路径** 可能失效，Chrome 无法加载 → 工具栏无图标。
- 重新导入时又因 Preferences 残留 ID 误判「已存在、未覆盖」，拒绝重新绑定修复路径。
- **钱包/LES 数据本身并未删除**，是加载路径坏了。

### 修复（优先保护用户扩展数据）
- **跳过 CLI 的条件**：仅当 Preferences 中扩展 **ENABLED 且 package 路径仍可加载**（manifest 存在）。
- **LES  alone 不再取消 CLI**；启动前 **只修复 Preferences path/state**，**永不改写/清空 LES、Cookies、IndexedDB**。
- 导入/分配：路径失效时允许重新绑定修复；仅路径真正可加载才跳过。
- 完整性标记同样要求「可加载注册」，避免热启动零 CLI 把扩展打没。

### 使用说明
1. 升级到 1.7.87 后直接启动环境：应自动修复路径并重新出现扩展。
2. 若仍无图标：再执行一次「分配/导入」同一扩展（不会覆盖钱包 LES）。
3. 切勿手动删除环境 data 目录下的 `Local Extension Settings`。

## 验证
- `go test ./backend/ -run 'TestApplyProfileNative|TestHealAssigned|TestProfileHas|TestReadOnlyDetect' -count=1`
