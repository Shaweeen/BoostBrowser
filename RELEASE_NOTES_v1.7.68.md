# BrowserStudio v1.7.68

## 紧急修复：扩展消失与启动过慢（1.7.67 回归）

### 原因
- v1.7.67 方案 A 将扩展整包拷贝进每个环境，并手写不完整的 Preferences 后去掉 `--load-extension`。
- Chromium **不认** 空权限的假 INTERNAL 扩展条目 → 工具栏无扩展。
- 多钱包整包拷贝 + 每次启动重写 Preferences → 启动卡住十余秒仍不出窗。

### 修复
- **不再整包拷贝**到 `Default/Extensions`（启动关键路径恢复轻量）。
- Preferences 以 **unpacked（location=4）** 指向共享扩展包路径，并从 manifest 填充权限。
- **只有** Chrome 已写出持久化扩展 data（LES 等）时才跳过 `--load-extension`；否则继续 CLI 注入，保证工具栏一定有扩展。
- 仍保留：启动 about:blank、丢弃可恢复扩展标签 Session、解绑禁用（保留钱包）、同步不误刷页、通知窗 force-fit 安全策略。

## 升级说明

- 支持从 v1.7.67 及更早版本升级到 v1.7.68。
- 已有钱包/LES 的环境：首次启动会带 CLI 加载扩展（恢复可用），之后有 data 的包可跳过 CLI 以减少重复开页。
