# BrowserStudio v1.7.119

## 更新内容

- 更新完成后只执行一次环境完整性维护，不再在每次启动客户端或每次打开环境时扫描 UUID、扩展、Cookie/应用状态或联网准备扩展恢复包。
- 将全局默认内核和全部现有环境统一设置为客户端相对路径 `chrome\google-148.0.7778.167` 的 Chrome for Testing 148。
- 更新时按环境 UUID 对齐 `data/<UUID>`，只读取扩展 ID 和浏览器状态存在性；不重写 Chromium `Preferences`、`Secure Preferences`、`Local State`、Cookies、IndexedDB 或钱包数据。
- 检测到扩展登记丢失但本地账户数据仍存在时，在更新阶段准备并校验相同 ID 的 Web Store 扩展包，将恢复路径持久化到对应环境；后续环境启动直接使用结果。
- 正式私有 Windows 安装包强制包含带兼容标记的 Chrome for Testing 148；缺少内核、`chrome.exe` 或标记时拒绝构建。

## 用户效果

- 普通客户端启动和环境启动不再承担重复完整性扫描或扩展下载，减少升级后的环境启动等待。
- Cookies、扩展账户、钱包本地存储和应用状态继续位于原 UUID 数据目录，更新流程不移动、不删除、不覆盖这些数据。
