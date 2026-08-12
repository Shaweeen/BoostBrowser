# BrowserStudio v1.7.93

## P0：发布门禁不再被测试文件改写卡死

- `check_code_health.ps1`：统计「实质删除」时 **忽略 `*_test.go` / 前端 test/spec**，避免测试重构反复挡住 Windows 打包。
- 补 CLEAN-089：记录 v1.7.92 去掉的 flaky 代理网络测试。
- 功能：同 1.7.90–92（扩展下载走代理 + 稳定单测）。

## Windows 发布

```text
-ExpectedVersion 1.7.93
```
