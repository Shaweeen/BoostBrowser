# BrowserStudio v1.7.41

## Windows 打包稳定性

- Windows 发布工具链固定为 Node.js 22 LTS，避免 Node 24 在 Vite 已成功生成 `dist` 后触发 libuv `UV_HANDLE_CLOSING` 退出断言。
- 全新 Windows 构建脚本改为安装 `OpenJS.NodeJS.22`，不再跟随可能产生不兼容变化的 Node.js LTS 浮动主版本。
- 构建前严格检查 Node.js 主版本；发现 Node 24 或其他主版本时提前停止，并显示可直接执行的 winget 替换命令。
- 保留对真实 npm、TypeScript 和 Vite 失败的严格退出检查，不会仅因 `dist` 文件存在而错误放行损坏构建。

## 升级

- 包含 v1.7.40 的环境代理、本地 VPN 非 TUN/TUN 链路及全部兼容性更新。
- v1.7.18–v1.7.40 可直接在线升级到 v1.7.41，继续保留现有 `data` 与用户环境数据。
