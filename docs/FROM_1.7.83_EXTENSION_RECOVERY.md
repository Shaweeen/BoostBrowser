# 从 v1.7.83 重建：扩展故障与今晚需求总览

> **权威发布流程（必须遵守）**  
> Mac 改代码 → `git push` 到 GitHub → **Windows `git pull` → Windows 打包 → Windows 上传 Release**  
> Mac **不**作为正式二进制发布机。

---

## 1. 用户报告的核心问题

升级到 **1.7.84～1.7.86** 之后：

- 浏览器插件**无法正常获取 / 安装 / 分配**
- 分配点击无效或失败
- 环境里扩展列表空，工具栏无钱包扩展
- （旁路）曾出现迅雷扩展 = **本机系统 External 注入**，与产品无关

---

## 2. 根因（产品逻辑，已写入代码）

| # | 根因 | 引入/暴露 | 修复 |
|---|------|-----------|------|
| A | 仅凭 **LES（钱包目录）** 就判定扩展已装并 **剥离 `--load-extension`**；升级后 Preferences 里 **绝对路径失效** → Chrome 加载不了 → 无图标 | 热启动「零 CLI」策略（约 1.7.82 起）在 84–86 升级后暴露 | **v1.7.87 逻辑**：仅当 Preferences **ENABLED 且 path 可加载** 才跳过 CLI；启动 **heal path only**，永不清 LES |
| B | 重新分配时残留 Preferences ID 误报「已存在、未覆盖」 | 与 A 叠加 | 仅 **可加载** 才 skip；路径坏则允许 re-bind |
| C | 扩展 CRX 下载 HTTP 客户端 **`Proxy=nil`**，国内新机下不了 Google | 分配/新环境 | **v1.7.90**：跟随 `HTTP(S)_PROXY` / 本机转发网关 |
| D | 分配结果不清晰；热启动反复 CLI 弹扩展主页 | 体验 | **v1.7.94 行为**：分配写 Preferences；同扩展 skip；可加载则零 CLI |

**方案 A（Scheme A，本产品设计）**：共享包目录 + 每环境 Preferences `location=4`；**不**抄迅雷系统 External 注册表。

---

## 3. 今晚功能/产品要求（综合）

1. **以本产品分配模型为主**：点「分配」才写入；创建环境不自动灌扩展  
2. **有同一可加载扩展 → 跳过**；不抹 LES / Cookies / IndexedDB  
3. **停机环境**：分配时写 Preferences；成功 = 可加载  
4. **运行中环境**：只绑 LaunchArgs，关闭后再开完成写入  
5. **启动**：只 heal **已绑定**列表；不扫描用户全部扩展；可加载则硬剥 CLI（不弹扩展主页）  
6. **发布**：Mac push 源码；Windows 拉代码打包上传  

---

## 4. 本分支提交线（相对 v1.7.83 仅扩展相关）

分支：`release/1.7.95-from-1.7.83`  
基线：`v1.7.83`  
产品版本：**1.7.95**

```text
v1.7.83
  + heal Preferences path / 禁止 LES-only 剥 CLI          （原 1.7.87）
  + 扩展下载走代理                                          （原 1.7.90）
  + 公网下载单测稳定（Windows 门禁）                        （原 1.7.92 相关）
  + 分配写 Preferences / 跳过同扩展 / 热启动零 CLI 文案     （原 1.7.94）
  = 1.7.95  （干净发布号，供 Windows 打包）
```

**未合入** 1.7.84～86 的其它功能（Cloak 反检测、自适应网络、Cookie 策略等），避免再次「堆版本 thrash」。  
若后续需要那些功能：在 1.7.95 验证扩展稳定后，**逐项 cherry-pick**，仍走 Mac→GitHub→Windows 打包。

---

## 5. Windows 操作（你删掉桌面目录后）

```powershell
# 1. 全新克隆（示例）
cd D:\
git clone https://github.com/Shaweeen/BoostBrowser.git BrowserStudio-src
cd BrowserStudio-src
git fetch --tags
git checkout release/1.7.95-from-1.7.83
git pull

# 2. 确认版本
# wails.json → productVersion 应为 1.7.95

# 3. 本机打包并上传（仅 Windows）
# 门禁基线应为 v1.7.83。若仍报净增 >800 相对 v1.7.55，先 git pull 取脚本修复，或加 -ApprovedGrowthReason
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\publish_windows_github_release.ps1 -ExpectedVersion 1.7.95
```


若用私有仓：

```text
git clone https://github.com/Shaweeen/BrowserStudio.git
```

---

## 6. 升级后用户侧恢复扩展

1. 安装 **Windows 打出的 1.7.95**  
2. 开启本机代理（Clash 等），或填「本机转发网关」  
3. 扩展管理 → 再点一次「分配」  
4. **关闭环境再打开**（运行中分配会延后写 Preferences）  
5. `chrome://extensions` 应出现业务扩展；**不要**删环境里的 `Local Extension Settings`（钱包）

---

## 7. 验收清单

- [ ] `go test ./backend/ -run 'TestAssignWrites|TestGlobalExtensionDistribution|TestHealAssigned|TestApplyProfileNative|TestPublicRemote' -count=1`  
- [ ] 新环境：分配成功 → 打开有扩展 → 再分配提示跳过  
- [ ] 旧环境（升级过 84–86）：启动 heal 或再分配后扩展回来，钱包还在  
- [ ] 热启动不弹钱包主页  
- [ ] 二进制仅来自 **Windows publish** 产物  
