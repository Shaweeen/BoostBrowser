# BrowserStudio v1.7.125

## 修复 Chrome 网上应用店「改用 Chrome？」提示仍会出现的场景

- v1.7.124 只对启动时的**初始标签页**注入了真实内核版本的 UA-CH 品牌；用户在**新建标签页 / 新窗口 / 弹窗**中打开 chromewebstore.google.com 时，「改用 Chrome？」「Google 建议将扩展程序和主题与 Chrome 搭配使用」仍会出现。
- 本次改为浏览器级 `Target.setAutoAttach` 自动附加：环境启动后持续监听内核创建的所有标签页（含用户新建标签页、`window.open` 弹窗），对每个新页面注入与真实内核版本一致的 Google Chrome UA-CH 品牌 + `chrome.webstorePrivate` 补齐。
- 任意标签页打开商城均不再提示「改用 Chrome？」，可正常浏览；「添加至 Chrome」按钮触发官方 CRX 下载并弹原生「添加扩展程序？」安装框。
- 监听随浏览器进程退出自动结束，不常驻、不引入任何 helper 扩展或本地协议；不改写用户数据、登录状态、钱包或扩展存储。
- 仅作用于非 Cloak 内核（google-148 Chrome for Testing 等）；Cloak 内核保持 C++ 层品牌处理不变。

## 验证

- 创建 google-148 环境 → 在**新标签页**打开 chromewebstore.google.com：无「改用 Chrome？」提示；点击「添加至 Chrome」弹原生安装框完成安装。
- go build / go vet / go test（含新增 `TestBrowserDebugAlive`）、打包测试全部通过。
