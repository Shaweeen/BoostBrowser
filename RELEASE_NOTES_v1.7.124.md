# BrowserStudio v1.7.124
## 修复 Chrome 网上应用店「改用 Chrome？」提示
- 修复打开 Chrome 网上应用店（chromewebstore.google.com）时弹出「改用 Chrome？」「Google 建议将扩展程序和主题与 Chrome 搭配使用」的问题。
- 非 Cloak 内核（Chrome for Testing 148 / 指纹内核等）启动后，自动向内核自建的初始标签页注入与真实内核版本一致的 UA-CH 品牌呈现（Google Chrome + 真实版本号）：Web Store 据此判定为品牌 Chrome，不再显示「切换到 Chrome」横幅，可正常浏览与安装扩展。
- 品牌版本号改为从浏览器自身上报的 UA 动态提取，不再硬编码 146，与 google-148 内核（148.0.7778.167）保持一致。
- 首次启动自动写入 `extension-mime-request-handling@2` flag，CRX 下载直接弹原生「添加扩展程序？」安装框。
- 补齐 chrome.webstorePrivate 私有 API 兜底：即使商城安装按钮走原生路径，「添加至 Chrome」也能转发到官方 CRX 下载完成安装。
- 不引入任何 helper 扩展、本地协议或本地服务器；不改写用户数据、登录状态、钱包或扩展存储。
## 验证
- 创建 google-148 环境 → 打开 chromewebstore.google.com：无「改用 Chrome？」提示，可正常浏览；点击「添加至 Chrome」弹原生安装框完成安装。
- go build / go vet / go test、前端构建、打包测试全部通过。
