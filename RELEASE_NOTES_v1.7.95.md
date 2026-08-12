# BrowserStudio v1.7.95

## 定位

- **基线**：`v1.7.83`（已知同步/面板功能稳定点）
- **目标**：修复用户升级到 **1.7.84～1.7.86** 后扩展获取/安装/分配失效
- **发布流程**：Mac 只推源码；**Windows 拉代码后打包上传**（见 `docs/FROM_1.7.83_EXTENSION_RECOVERY.md`）

## 相对 v1.7.83 的扩展修复（全部）

### 1. 升级后扩展消失 / 无法重装（原 1.7.87）

- 不再仅凭 LES 就剥离 `--load-extension`
- 仅 Preferences **可加载** 才跳过 CLI；启动 **heal path**，不碰钱包 LES
- 路径失效时允许再次分配修复

### 2. 分配下载失败（原 1.7.90）

- CRX/订阅下载跟随 `HTTP(S)_PROXY` / 本机转发网关

### 3. 分配语义稳定（原 1.7.94）

- 点分配才写入；同扩展可加载则跳过
- 停机写 Preferences；运行中延后
- 热启动零 CLI，减少扩展主页弹窗
- 清晰 toast（绑定 / 跳过 / 延后 / 失败）

## 未包含（刻意）

1.7.84～86 的 Cloak 反检测、自适应本机网络等：**本版不混入**，扩展稳定后再逐项合入。

## Windows 打包

```text
git checkout release/1.7.95-from-1.7.83
git pull
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.95
```

门禁基线应为 **v1.7.83**（相对净增约 700+ 行，&lt; 800）。  
若仍误用更老 tag，可显式放行：

```text
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.95 -ApprovedGrowthReason "1.7.95 extension recovery from v1.7.83 only (heal path + proxy CRX + assign); not full-tree thrash"
```

## 验证

```text
go test ./backend/ -run 'TestAssignWrites|TestGlobalExtensionDistribution|TestHealAssigned|TestApplyProfileNative|TestPublicRemote|TestRegisterAssigned' -count=1
```
