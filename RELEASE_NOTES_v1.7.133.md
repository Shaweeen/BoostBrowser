# BrowserStudio v1.7.133 发布说明

## 敏感数据

本次**不删除、不改写** Cookie、登录态、扩展存储、钱包 vault、Preferences。
已分配并完成首次适配的扩展，以及用户在商城原生安装的扩展，由 Chrome 自己加载。

## 修复：装好扩展后每次启动还在读/注入，连钱包慢、Rabby Notification 铺屏

分配并成功装进环境之后，启动仍会：

1. 读 Preferences / LES 判断要不要 `--load-extension`
2. 扫描 profile 做扩展恢复
3. 再打一遍 `--load-extension` → Chrome 当成新安装 → Rabby/MetaMask 弹出 Notification 主页
4. 连钱包/签名时客户端还可能 CDP 碰到扩展页

**替换（一个 owner）：**

- 首次适配：保留 `--load-extension`，Chrome 写出 Preferences+LES 后写入 `.boost_extension_integrity_v1`
- **之后每次启动只看这个标记**：去掉 `--load-extension`，不读 Preferences/LES/钱包，不做恢复/注入/巡检
- 重新「分配」会清标记，再适配一次
- 商城自装（没有分配 CLI）同样视为已完成，启动不扫描、不注入
- 钱包连接/签名只走环境里的扩展：客户端不对 `chrome-extension://` 做 CDP attach、不同步点击、不关钱包窗

## 使用说明

1. 已经连过钱包的环境：关掉再开，不应再铺满 Notification，也不应再被客户端扫一遍扩展。
2. 新分配的扩展：打开一次完成适配，之后同样不再注入。
3. 点 dapp「连接钱包」：每扇环境自己的 Rabby/MetaMask 弹确认，客户端不代点、不注入。
