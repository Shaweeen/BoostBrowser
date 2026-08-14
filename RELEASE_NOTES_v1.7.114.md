# BrowserStudio v1.7.114

## 修复内容

- 修复旧环境扩展丢失的核心启动链路：不再在环境启动时剥离已保存的 `--load-extension` 扩展包路径。旧环境的 MetaMask、Rabby 及其他扩展会按其原扩展 ID 使用既有包和既有浏览器数据启动。
- Chrome 137+ 内核只要本次启动存在既有扩展路径，都会启用所需的命令行兼容开关；不再仅限于临时恢复分支。
- 取消环境启动时自动注入 BrowserStudio 的 Chromium Web Store 辅助扩展，避免它替代或干扰用户原有扩展。
- 保持 Chromium 浏览器数据只读：不写入或替换 `Preferences`、`Local State`、Cookies、扩展 Local Extension Settings、钱包数据或登录状态；没有后台轮询或扩展弹窗监控。

## 验收方式

1. 先完整关闭同一环境的浏览器，再从客户端重新启动该环境。
2. 打开 `chrome://extensions`，确认原环境已有的扩展（例如 MetaMask/Rabby）按其原 ID 出现；此前自动出现的 Chromium Web Store 辅助扩展不应再被本客户端启动逻辑注入。
3. 打开原扩展并确认原有钱包/登录状态仍在。若扩展程序包目录已被用户或系统实际删除，客户端不会伪造新钱包数据；请使用数据恢复入口恢复对应环境文件夹后再启动。
