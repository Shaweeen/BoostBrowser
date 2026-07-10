# Windows 自用打包架构

本项目现在采用“主程序 + 外部指纹 Chromium 内核”的标准布局：

```text
BoostBrowser
├── boost-browser.exe                 # Wails/Go 主程序
├── updater.exe                       # 自动升级器
├── config.yaml                       # 干净默认配置
├── chrome
│   ├── cloak-146.0.7680.177          # 必需：CloakBrowser 指纹 Chromium
│   │   └── chrome.exe
│   └── google-148.0.7778.167         # 可选：普通 Chrome 备用
│       └── chrome.exe
├── bin                               # 可选：xray / sing-box
├── extensions
│   └── chromium-web-store            # 内置 helper 扩展，仓库自带安全清理版
└── data                              # 用户数据，首次启动自动创建
```

## 两个 GitHub 仓库的角色

- `Shaweeen/BoostBrowser`：主程序、UI、环境管理、代理管理、打包脚本。
- `CloakHQ/CloakBrowser`：CloakBrowser patched Chromium 二进制内核来源。

不要把 CloakBrowser 源码复制进 BoostBrowser。BoostBrowser 只需要它 Release 里的 Windows Chromium zip。

## 一键准备 CloakBrowser 内核

在 Windows PowerShell 里执行：

```powershell
cd D:\BoostBrowser-main
powershell -ExecutionPolicy Bypass -File scripts\install_cloakbrowser_kernel.ps1
```

脚本会下载官方免费内核：

```text
https://github.com/CloakHQ/CloakBrowser/releases/download/chromium-v146.0.7680.177.5/cloakbrowser-windows-x64.zip
```

并校验 `SHA256SUMS`，然后安装到：

```text
D:\BoostBrowser-main\chrome\cloak-146.0.7680.177\chrome.exe
```

如果你已经手动下载了 zip：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install_cloakbrowser_kernel.ps1 -SourceZip C:\Users\admin\Downloads\cloakbrowser-windows-x64.zip
```

## 可选 Google Chrome 备用内核

```powershell
mkdir D:\BoostBrowser-main\chrome\google-148.0.7778.167 -Force
robocopy "C:\Program Files\Google\Chrome\Application" "D:\BoostBrowser-main\chrome\google-148.0.7778.167" /E
```

## 标准编译流程

```powershell
cd D:\BoostBrowser-main
$env:BOOST_KERNEL_SRC = "D:\BoostBrowser-main"

cd frontend
npm ci
npm run build

cd ..
go mod download
powershell -ExecutionPolicy Bypass -File scripts\build_release.ps1
powershell -ExecutionPolicy Bypass -File scripts\build_installer.ps1
```

输出目录：

```text
D:\BoostBrowser-main\build\release
```

## 设计原则

- `build_release.ps1` 只生成轻量升级文件，不复制 Chromium 内核。
- `build_installer.ps1` 生成完整自用安装包，包含 CloakBrowser 内核。
- `BOOST_KERNEL_SRC` 可指定内核来源；不设置时默认使用仓库根目录。
- `chrome/` 和 `bin/` 是本地大文件目录，已加入 `.gitignore`，不会上传到 GitHub。
