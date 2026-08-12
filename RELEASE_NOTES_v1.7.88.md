# BrowserStudio v1.7.88

## P0：扩展路径修复可发布（DELETION_LEDGER）

- 相对 v1.7.87：补齐 `docs/DELETION_LEDGER.md` CLEAN-087，通过 Windows 发布 code-health 门禁。
- 功能与 v1.7.87 相同：启动修复 Preferences 扩展包路径；LES  alone 不再取消 CLI；永不覆盖钱包 LES。

## 使用

升级到 1.7.88 后启动环境应自动恢复扩展图标；必要时再分配一次扩展（不覆盖钱包数据）。

## 验证

- `go test ./backend/ -run 'TestApplyProfileNative|TestHealAssigned|TestProfileHas|TestReadOnlyDetect' -count=1`
- Windows publish: `-ExpectedVersion 1.7.88`
