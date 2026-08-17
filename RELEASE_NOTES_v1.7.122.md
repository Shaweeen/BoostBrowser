# BrowserStudio v1.7.122

## Chrome Web Store 无账号安装兼容

- 默认仍不登录 Google 账号，也不会提示用户必须登录后才能安装扩展。
- 为内置 Chrome for Testing 148 和 Cloak Chromium 恢复 Chrome Web Store 安装能力；正式 Google Chrome 继续使用自己的原生商店安装器。
- 用户在环境内打开 Chrome Web Store 后，可以直接点击安装。扩展下载完成后，关闭并重新启动当前环境即可启用。
- 商店兼容组件属于 BrowserStudio 系统组件，不进入扩展中心业务插件列表，不会在创建环境时询问或自动分配业务扩展。

## 多环境隔离与数据保护

- 商店兼容组件按环境 UUID 独立存放，并携带准确的 Profile ID；同时打开多个环境时，A 环境下载的扩展不会被绑定到 B 环境。
- 扩展安装只保存经过验证的程序包和该环境的启动关联，不改写 Chromium `Preferences`、`Local State`、Cookies、登录状态、钱包或扩展存储。
- 已有扩展及其原始扩展 ID 保持不变。

## 验证

- Chrome for Testing 148/Cloak 会获得商城兼容组件，正式 Google Chrome 不重复注入。
- 不同 UUID 环境使用不同的兼容组件端点和 Profile ID。
- Google Web Store 下载请求明确绑定到发起操作的环境。
