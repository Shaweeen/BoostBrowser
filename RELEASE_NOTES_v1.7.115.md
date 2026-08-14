# BrowserStudio v1.7.115

## 修复内容

- 修复从 v1.7.84 升级后的旧环境扩展恢复判断：恢复逻辑现在只读取本次 Chromium 实际启动的子配置，不再把 `Default`、`Profile 1` 等不同配置的扩展登记混在一起。
- 当启动命令没有指定 `--profile-directory` 时，恢复逻辑会只读 `Local State` 的最后使用配置，与 Chromium 的启动选择保持一致；不会把其他配置的扩展状态误判为当前环境已加载。
- 对恢复到的旧扩展启动参数清除遗留的 `--disable-extensions` / `--disable-extensions-except` 阻断项，并兼容大小写混用的旧 `--LOAD-EXTENSION` 参数。
- 旧版扩展程序包迁移不再仅扫描 `Default`：也会保留 `Profile 1`、`Profile 2` 等标准 Chromium 子配置中的有效扩展程序包，供原环境按既有扩展 ID 启动。

## 数据保护

- 不写入 Chromium 的 `Preferences`、`Local State`、Cookies、`Local Extension Settings`、IndexedDB 或任何钱包/账号数据。
- 不覆盖、不替换用户自行安装的扩展；本次仅在用户启动环境时进行一次内存中的启动参数恢复，没有轮询、后台监控或扩展弹窗控制。

## 验收方式

1. 完整关闭需要验证环境的所有浏览器窗口。
2. 从 BrowserStudio 重新启动该环境，打开 `chrome://extensions`。
3. 确认 v1.7.84 时已有的扩展按原 ID 出现，并打开扩展确认原钱包/登录状态仍在。
4. 若仍没有扩展，请在 `data` 中保留该环境文件夹并提供该环境启动日志中 `已为本次启动临时恢复用户已有扩展` 的记录；该记录会显示实际读取的 Chromium 子配置名，便于继续精确定位，且不需要导出任何助记词或私钥。
