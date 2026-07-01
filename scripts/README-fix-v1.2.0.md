# Boost Browser v1.2.0 升级失败修复

## 现象

v1.2.0 用户点击「重启更新」按钮后，旧进程退出了，新版本没有起来，**应用直接消失**，再次双击启动还是 v1.2.0，更新检查又会让你重新下载、重新点「重启更新」，永远卡在 v1.2.0。

## 根因

v1.2.0 的 `ApplyUpdate` 实现在 `os.Exit(0)` 前没有调用 `cmd.Process.Release()`。当父进程退出时，Windows 把 Go runtime 仍持有 handle 的子进程（updater.exe）一并终止，导致 updater 没机会替换 .exe 文件。

v1.3.0 已修复（`backend/app_updater.go` 加了 `cmd.Process.Release()` + 同步应急日志）。但 v1.2.0 二进制本身已经发布到 GitHub，无法在用户那一端"自愈"——必须外部脚本介入。

## 修复方法（给用户的指令）

把 `fix-update-from-v1.2.0.bat` 和 `fix-update-from-v1.2.0.ps1` **两个文件**复制到 BoostBrowser 安装目录（即和 `boost-browser.exe` 同一文件夹），双击 `.bat` 即可。

脚本会：

1. 结束所有残留 `boost-browser.exe` / `updater.exe` 进程
2. 从 GitHub `sdohuajia/BoostBrowser` v1.3.0 release 下载 `boost-browser.exe` + `updater.exe`
3. 比对 sha256（GitHub 提供的 `.sha256` asset）
4. 把当前 v1.2.0 的两个 .exe 重命名为 `*.v1.2.0.bak`
5. 安装新版
6. 清理 `data\updates\` 里的下载残留
7. 询问是否立即启动

用户数据（profile、cookies、收藏）都保留在 `data\<uuid>\`，**不会丢失**。

## 测试已通过的场景

- Windows 10 / 11，PowerShell 5.1（系统自带）
- 安装目录在网络盘 (`Z:\BoostBrowser_cloak_test`)
- 安装目录在本地盘 (`D:\BoostBrowser`、`C:\Program Files\BoostBrowser`)

## 发布建议

把这两个文件作为额外 asset 挂到 v1.3.0 release，或单独建一个 v1.3.0-fix release 公告：

```
gh release upload v1.3.0 \
  scripts/fix-update-from-v1.2.0.ps1 \
  scripts/fix-update-from-v1.2.0.bat \
  --repo sdohuajia/BoostBrowser
```

公告里贴一句：

> v1.2.0 用户如果点击「重启更新」后无法启动 v1.3.0，请下载本页 `fix-update-from-v1.2.0.bat` 和 `fix-update-from-v1.2.0.ps1`，放到安装目录后双击 `.bat`。

## 长期防护（已在 v1.3.0 中实现）

`backend/app_updater.go` ApplyUpdate 现在做了：

- `cmd.Process.Release()` —— 让 Go runtime 释放对子进程 handle，父进程 os.Exit 不再带走 updater
- `applyUpdateDebugLog` —— 同步写 `data/logs/apply_update.debug.log`，绕过 async logger 的 1s flush 间隔，下次再有问题能立刻看到错误位置
- `logger.Close()` —— 主退出前 flush 所有 log
- 800ms 后 `runtime.Quit` + 2s 后 `os.Exit(0)` —— 给 Wails 优雅关窗时间

这些组合保证 v1.3.0 → v1.4.0 升级不会再复现这个 bug。
