# BrowserStudio v1.7.131 发布说明

## 修复：打开环境异常慢 + 同步时 X / OAuth 授权全部失败

### 问题现象
1. 非 Cloak 内核打开环境停在空的 `about:blank`，DevTools 里只有空 `<html>`，体感明显变慢。
2. 同步开着点 GenLayer 等站点的「Connect X」，会弹出十几扇 `x.com/i/oauth2/authorize` 窗，全部是
   “You weren't able to give access to the App. Go back and try logging in again.”

### 根因（两处独立故障，叠在一起）
1. **Web Store 兼容对每个标签页做 `Target.setAutoAttach`。**
   v1.7.125 为了让新标签页打开 chromewebstore 不再出现「改用 Chrome？」，在每个环境启动后
   给浏览器级 CDP 打开 `autoAttach`，并对**每一个** page target（包括启动 `about:blank`、
   OAuth 弹窗、普通网页）做 `Emulation.setUserAgentOverride` + `Page.addScriptToEvaluateOnNewDocument`。
   CDP 附加本身就是 nodriver / tampering 信号；UA 覆写还会留在初始空白标签的后续导航上。
   结果是：环境打开变慢，X 授权页判定自动化并拒绝。
2. **URL 同步把一次性 OAuth 地址镜像到所有从控。**
   主控一点 Connect X，地址变成 `x.com/i/oauth2/authorize?state=…`。`urlSyncLoop` 只跳过
   `chrome-extension://` 和 `about:blank`，于是对每个从控 `Page.navigate` 到**同一条**
   一次性 state。点击同步再把 Connect X 点到从控上，窗口继续爆炸。state 用过一次就作废，
   所以每一扇窗都失败。

嵌入的 `chromium-web-store` 扩展本身只匹配商店域名，不是这次授权失败的注入源。

### 修复内容
- 新增单一策略入口 `backend/navigation_guards.go`：
  - `shouldInjectWebStoreCompat`：只有 Chrome / Edge / Opera 商店 URL 才注入。
  - `shouldMirrorSyncNavigation`：空白页、chrome 内部页、扩展页、OAuth/SSO 同意页一律不 URL 同步。
  - `shouldReplaySyncInput`：主控停在授权页时不回放点击 / 键盘。
- Web Store 监听改为 `Target.setDiscoverTargets`：只发现、不自动附加；看到商店 URL 再连那个 page
  注入。启动时不再对 `about:blank` / `chrome://newtab` 做 UA 覆写。
- `urlSyncLoop` / `navigateFollower` 记录授权 URL 作为基线，但不再 `Page.navigate` 从控。
  主控点 Connect X 仍会按普通页面点击同步（每个环境打开自己的授权窗、各自的 state）；
  授权页上的同意 / 输入不再镜像。
- 不改写用户 Profile、Cookie、扩展、钱包数据。Cloak 内核路径不变（本来就跳过 wrapper 注入）。

### 验证
- `navigation_guards_test.go`：X / Discord / Google / GitHub OAuth 不镜像；GenLayer、X 首页、
  Discord 频道仍同步；商店 URL 才注入。
- 打包测试禁止 `Target.setAutoAttach`，要求商店 URL 门闩。
- 仓库内可运行的 Go / 打包测试已执行。Windows 打包仍只在 Windows 机器上进行。

### 使用说明
- 同步浏览普通网页（含 GenLayer Portal）行为不变。
- 授权（X / Discord / Google 等）请在**每个环境自己的窗口**完成，不要指望主控点一次 Authorize
  就能替所有从控过 OAuth。
- 非 Cloak 内核打开 Chrome 网上应用店，新标签页仍可安装扩展，不再对每个空白页预注入。
