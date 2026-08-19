# BrowserStudio v1.7.132 发布说明

## 敏感数据

本次**不删除、不改写** Cookie、登录态、扩展存储、钱包 vault、Preferences。
已分配的 `--load-extension` 和用户在商城原生安装的扩展继续按原路径加载。

## 修复：启动环境很慢（扩展注入）

每个环境启动时都会：

1. 解压并 `--load-extension` 一套 Web Store helper
2. 阻塞打 `/json/list` 做商店兼容
3. 挂上常驻 `Target.setDiscoverTargets` 调试连接
4. 若配置了启动 URL，还走 `about:blank → UA/stealth 注入 → 再跳转`

这些都不碰用户数据，但会拖慢打开、并留下 CDP 痕迹。

**替换：** 启动热路径不再注入 helper、不再开商店调试、启动 URL 只 `Target.createTarget`。
新扩展请用「扩展管理 → 分配」，或在已打开的环境里自行安装。已装扩展不受影响。

## 修复：授权页仍被 CDP 探测

1.7.131 已停止同步 OAuth URL。焦点探测仍会对授权页 `Runtime.evaluate`。
现在 OAuth/consent 页只看 `/json` 的 URL，不再 attach；Authorize 点击/滚轮/Win32 回退都不再同步。

X `/i/flow/login`、Google `/signin`、GitHub `/login` 视为账号密码登录，**仍可同步**，方便批量登号。

## 修复：TUN 下绑了节点启动却不是那个 IP

绑定了远程 HTTP/SOCKS 节点时，TUN 不再丢掉 `--proxy-server`。
经本机 Clash 混合端口（回环，不被 TUN 截获）到达该节点。
只有「没绑远程节点 / 直连 / 本地 127.0.0.1」才把出口交给系统 TUN。
`auto` 启动若探测到本地网关，不再先死等 6 秒直连。

## 其它

- 更新维护：运行中环境不写 `closed`；Cloak 环境不改 `core_id`
- 同步工具提示：Connect 会带到每扇窗，Authorize 请各点一次
- 编辑页：环境在跑时改代理，明确「重启后生效」

## 使用说明

1. 扩展：已装的不用动。新装走「分配」或环境内安装，不要指望启动时自动塞 helper。
2. 授权：主控点 Connect 会各开各的授权窗；Authorize 各点一次。
3. 代理：Clash TUN + 住宅节点请保持绑定；要共用 Clash 当前节点就不要绑远程节点。
