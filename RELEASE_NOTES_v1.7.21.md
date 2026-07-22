# BrowserStudio v1.7.21

## 主要更新

- 修复钱包 CSV 模板只能识别内部 `profile_id`、无法按界面可见环境编号指定导入环境的问题。
- 新模板增加 `environment_number` 字段，并继续支持 `profile_id`、`profile_name`；三种标识任选一种即可精确匹配环境。
- 兼容旧两列 CSV/TXT：原 `profile_id` 列也可填写界面环境编号（例如 `1`、`#1`）或唯一环境名称。
- 同一行填写多个环境标识时会校验它们是否指向同一环境；编号或名称重复、标识冲突时拒绝导入，防止助记词进入错误环境。
- 导入预览显示环境编号、环境名称和内部 ID，用户可在正式导入前再次核对。
- 修复部分全新 Windows 电脑无法打开钱包模板保存窗口的问题；Wails 对话框不可用时自动使用 Windows 内置 WinForms 对话框。

## 模板格式

```csv
environment_number,profile_id,profile_name,mnemonic
1,,,word1 word2 word3 ...
2,,,word1 word2 word3 ...
```

- 推荐只保留模板预填的环境信息，在对应行填写助记词。
- `environment_number`、`profile_id`、`profile_name` 至少填写一项。
- 助记词仍只在 Go 后端的一次性内存会话中处理，不写入前端、数据库或日志。

## 升级说明

- 已安装 v1.7.x 的用户可以直接在客户端在线升级到 v1.7.21，不需要先安装 v1.7.20，也不需要卸载。
- 在线升级保留环境、浏览器数据、代理池、扩展、内核、配置和激活状态。
- 如果旧客户端更新器异常，可使用 `BrowserStudio-Repair-Upgrade-v1.7.21.ps1` 执行无损修复升级。

## 兼容性

- Windows 10/11 x64。
- 文件选择后备方案使用 Windows 自带的 Windows PowerShell 和 .NET WinForms，无需安装 Wails、Go、Git 或 GitHub CLI。
