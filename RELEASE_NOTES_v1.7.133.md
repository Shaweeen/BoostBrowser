# BrowserStudio v1.7.133 发布说明

## 敏感数据

本次**不删除、不改写** Cookie、登录态、扩展存储、钱包 vault、Preferences。
已分配的扩展和商城原生安装的扩展继续按原路径加载。

## 修复：打开环境弹出一堆 Rabby / MetaMask Notification，启动和连钱包很慢

1.7.132 去掉了商店 helper，但环境保存的 `--load-extension` 仍会在**每次启动**再注入一次。
Chrome 把这当成新安装，Rabby / MetaMask 触发 `onInstalled`，弹出空白「Wallet Notification」主页。
批量开环境时每个窗口再叠一块钱包页，启动和点「连接钱包」都会变慢。

**替换：**

1. 启动只读调用已有的 `applyProfileNativeExtensionLaunchArgs`。
   Preferences 已启用、包路径可加载、且 LES 已存在时，取消该包的 `--load-extension`。
   Chrome 自己加载已装扩展，不再重放 onInstalled。
   第一次适配（还没有 LES）仍带 CLI，避免「分配成功但工具栏是空的」。
2. 启动后 2.5 秒内，只对标题同时含 wallet + notification 的窗口发 `WM_CLOSE`。
   不走 CDP，不关「Rabby Wallet / MetaMask」工具栏弹窗，不关用户稍后点的连接确认窗。

## 使用说明

1. 已连过钱包的环境：直接打开，不应再铺满 Notification 白页。
2. 新分配的钱包扩展：第一次打开仍会走 CLI 适配，属正常。
3. 点 dapp「连接钱包」后弹出的确认窗请照常点；启动清理只针对自动弹出的 Notification 主页。
