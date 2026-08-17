# BrowserStudio v1.7.128

## 修复：商城下载的扩展「数据能识别但加载不成功」

- **现象**：用户在 Chrome 网上应用店下载扩展（如 Rabby/MetaMask 钱包）后，扩展的用户数据（钱包/登录状态）能被识别，但扩展本身加载不出来 / 工具栏里点了没反应 / 侧边栏空白。
- **根因**：v1.7.124 加入的 `dropStoreInstalledLoadExtensionArgs` 只凭 Preferences 里扩展条目的 location 标记（INTERNAL / from_webstore）就剔除 `--load-extension` 启动参数，**没有校验条目是否已启用、也没有校验 Chrome 商城包（Default/Extensions/<id>/）是否真实存在**。一旦商城安装不完整（中断、包缺失、条目被禁用），剔除后扩展就失去唯一的加载路径——数据还在（条目 + 钱包 LES 数据都在），但没有任何东西把它加载起来。
- **修复**：只有同时满足「条目已启用（state=1）+ Chrome 商城包真实存在」时才剔除 `--load-extension`；商城包缺失或条目被禁用时**保留 CLI 注入**，让 BrowserStudio 管理包继续加载扩展。真正的商城安装（包存在且启用）仍会剔除，避免重复安装与 onInstalled 重复弹窗。
- 不改写用户数据、钱包存储、登录状态；不新增/删除任何 Preferences 字段。

## 验证

- 新增回归测试：
  - `TestDropStoreInstalledLoadExtensionArgsKeepsCLIWhenStorePackageMissing`（商城包缺失 → 保留 CLI，复现本 bug）
  - `TestDropStoreInstalledLoadExtensionArgsKeepsCLIWhenStoreEntryDisabled`（条目禁用 → 保留 CLI）
  - 原 `TestDropStoreInstalledLoadExtensionArgs` 更新为「商城包存在且启用 → 正常剔除」。
- go build / go vet / go test ./...、打包测试全部通过。
