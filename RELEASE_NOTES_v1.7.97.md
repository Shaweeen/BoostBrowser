# BrowserStudio v1.7.97

## 变更

### 1. 遗留数据提醒（按你的契约）

- **日常启动不再扫描**未关联数据文件夹。
- **仅当用户删除环境后** 设置 `LegacyScanPending` 并触发一次检查；有残留才弹窗。
- **忽略 / 不导入** → **永久删除**这些残留文件夹 + 记入 dismiss，**永不再问**。
- 设置页「立即扫描」仍可用（`force=true`）。

### 2. 扩展分配

- 禁止把内置 **Web Store 助手**（`lfoeajgcchlidpicbabpmckkejpckcfb`）当业务扩展分配。
  - 你机器上 `extensions\imported` 若只有该 ID，说明业务扩展从未成功下载到 imported；需代理后重新从商店/crx 导入 MetaMask 等。
- 保留 1.7.96：分配必须写入 Preferences；首次启动保留 CLI 直到 Chrome 写出 LES。

### 3. 平铺间隙

- 仍为 `defaultTileGapPx = 0`（整数像素最小间隙）。

## 关键发现（1.7.96 环境）

`D:\BrowserStudio\extensions\imported` 仅有 `lfoeajgc…`（Web Store helper），**没有**钱包扩展包 → 分配「成功」若绑的是 helper 或空路径，环境里不会出现 MetaMask/Rabby。

## Windows 打包

```text
git fetch --tags
git checkout release/1.7.95-from-1.7.83
git pull
git checkout v1.7.97   # 若已打 tag
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.97
```

## 验证

```text
go test ./backend/ -run 'TestLegacy|TestAssignWrites|TestApplyProfileNative' -count=1
```
