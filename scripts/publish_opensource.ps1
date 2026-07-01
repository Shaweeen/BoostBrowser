# publish_opensource.ps1
# 把 boost-browser 源码推到公开 GitHub 仓库（替换旧 Python 版）
#
# 默认安全设定：
#   - DryRun = $true        ← 只 stage 文件到临时目录，不 push
#   - IncludeLicense = $false   排除 license 验证代码（开源后会被绕过）
#   - IncludeKeygen = $false    排除注册码生成器（绝对不能传）
#   - IncludeCloakCore = $false 排除 cloak 反检测核心（竞品会抄）
#   - IncludeStealth = $false   排除 stealth 反检测代码
#   - IncludeTests = $true      包含单元测试（让仓库看起来专业）
#
# 流程：
#   [1] 读取本仓库（C:\Users\Administrator\Desktop\Ant-Browser-update）
#   [2] 按 include/exclude 规则 stage 到 C:\Temp\BoostBrowser_pub\
#   [3] 写入新 README + .gitignore
#   [4] DryRun=$false 时：git init / 强制覆盖 sdohuajia/BoostBrowser main 分支
#
# 用法：
#   # 1. 第一次只看会传哪些文件，不 push：
#   powershell -ExecutionPolicy Bypass -File scripts\publish_opensource.ps1
#
#   # 2. 确认无误后真正 push（会强制覆盖远端 main）：
#   powershell -ExecutionPolicy Bypass -File scripts\publish_opensource.ps1 -DryRun:$false
#
#   # 3. 如果想包含 license 验证代码：
#   powershell -ExecutionPolicy Bypass -File scripts\publish_opensource.ps1 -IncludeLicense

param(
    [bool]$DryRun           = $true,
    [bool]$IncludeLicense   = $false,
    [bool]$IncludeKeygen    = $false,
    [bool]$IncludeCloakCore = $false,
    [bool]$IncludeStealth   = $false,
    [bool]$IncludeTests     = $true,
    [string]$RemoteUrl      = 'https://github.com/sdohuajia/BoostBrowser.git',
    [string]$Branch         = 'main',
    [string]$CommitMessage  = 'Replace Python prototype with Wails/Go production source'
)

$ErrorActionPreference = 'Stop'
$RepoRoot = Split-Path -Parent $PSScriptRoot
$Stage    = 'C:\Temp\BoostBrowser_pub'

Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "  Boost Browser 开源发布脚本" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "  RepoRoot          : $RepoRoot"
Write-Host "  Stage             : $Stage"
Write-Host "  RemoteUrl         : $RemoteUrl"
Write-Host "  Branch            : $Branch"
Write-Host ""
Write-Host "  DryRun            : $DryRun        $(if($DryRun){'(只 stage，不 push)'}else{'***会强制覆盖远端!***'})" -ForegroundColor $(if($DryRun){'Green'}else{'Red'})
Write-Host "  IncludeLicense    : $IncludeLicense   (license 验证代码)"
Write-Host "  IncludeKeygen     : $IncludeKeygen   (注册码生成器)"
Write-Host "  IncludeCloakCore  : $IncludeCloakCore   (cloak 反检测核心)"
Write-Host "  IncludeStealth    : $IncludeStealth   (stealth 反检测代码)"
Write-Host "  IncludeTests      : $IncludeTests   (*_test.go 单元测试)"
Write-Host ""

# ---------------------------------------------------------------------------
# 1. 清空 stage
# ---------------------------------------------------------------------------
Write-Host "==> [1/5] 清空 stage 目录" -ForegroundColor Yellow
Remove-Item -Recurse -Force $Stage -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Force -Path $Stage | Out-Null

# ---------------------------------------------------------------------------
# 2. 定义白名单 + 黑名单
# ---------------------------------------------------------------------------

# 顶层要包含的文件（精确匹配）
$IncludeRootFiles = @(
    'main.go',
    'wails.json',
    'go.mod',
    'go.sum',
    'config.yaml',
    'single_instance_windows.go',
    'single_instance_other.go',
    'watchdog_windows.go',
    'watchdog_other.go'
)

# 顶层要包含的目录（递归）
$IncludeRootDirs = @(
    'backend',
    'frontend',
    'build',
    'scripts',
    'bat',
    'tools',
    'images',
    'publish'
)

# 任何路径下都要排除的文件 / 目录名（黑名单优先）
$ExcludePaths = @(
    # 构建产物
    'node_modules',
    'dist',
    'build/bin',
    'build/release',
    'build/darwin',
    'frontend/dist',
    'frontend/wailsjs',
    'frontend/node_modules',
    'frontend/package.json.md5',
    'publish/output',

    # 中间产物（脚本会重新生成）
    'publish/boost-browser-installer.nsi',
    'publish/boost_nsis_files.nsh',
    'publish/boost_sidebar.bmp',
    'publish/boost_header.bmp',

    # 备份 / 日志 / 内部文档
    'logs',
    'backend/logs',
    'config.yaml.bak_cloak',
    'CHANGELOG.md',
    'UPDATE_README.md'
)

# 文件后缀 / 通配排除
$ExcludePatterns = @('*.bak', '*.tmp', '*.log', '*.exe', '*.bin')

# 敏感代码（按 param 控制）
if (-not $IncludeKeygen) {
    $ExcludePaths += 'tools/keygen'
    $ExcludePaths += 'tools/public-release'   # 不确定内容，默认排除
}

if (-not $IncludeLicense) {
    $ExcludePaths += @(
        'backend/app_license.go',
        'backend/license_state.go',
        'backend/license_state_test.go',
        'backend/app_launchcode.go'
    )
}

if (-not $IncludeCloakCore) {
    $ExcludePaths += @(
        'backend/cloak_core.go',
        'backend/cloak_geoip.go',
        'backend/chrome_search_engine_cdp.go',
        'backend/chrome_search_engine_seed.go'
    )
}

if (-not $IncludeStealth) {
    $ExcludePaths += @(
        'backend/app_stealth.go',
        'backend/app_stealth_helpers.go'
    )
}

if (-not $IncludeTests) {
    $ExcludePatterns += '*_test.go'
}

# 把相对路径标准化为反斜杠 + 小写，方便比对
$ExcludeSet = New-Object System.Collections.Generic.HashSet[string]
foreach ($p in $ExcludePaths) {
    [void]$ExcludeSet.Add($p.Replace('/', '\').ToLower())
}

function Should-Exclude([string]$relPath) {
    $norm = $relPath.Replace('/', '\').ToLower()
    foreach ($ex in $ExcludeSet) {
        if ($norm -eq $ex)        { return $true }
        if ($norm.StartsWith($ex + '\')) { return $true }
    }
    foreach ($pat in $ExcludePatterns) {
        if ($norm -like ("*\$pat").ToLower() -or $norm -like $pat.ToLower()) { return $true }
    }
    return $false
}

# ---------------------------------------------------------------------------
# 3. Stage 文件
# ---------------------------------------------------------------------------
Write-Host "==> [2/5] 拷贝白名单文件到 stage" -ForegroundColor Yellow

$copiedCount = 0
$skippedCount = 0

function Copy-Filtered([string]$srcPath, [string]$relPath) {
    $script:copiedCount++
    $dst = Join-Path $Stage $relPath
    $dstDir = Split-Path -Parent $dst
    if ($dstDir -and -not (Test-Path -LiteralPath $dstDir)) {
        New-Item -ItemType Directory -Force -Path $dstDir | Out-Null
    }
    Copy-Item -LiteralPath $srcPath -Destination $dst -Force
}

function Walk-Dir([string]$absDir, [string]$relDir) {
    Get-ChildItem -LiteralPath $absDir -Force -ErrorAction SilentlyContinue | ForEach-Object {
        $childRel = if ($relDir -eq '') { $_.Name } else { $relDir + '\' + $_.Name }
        if (Should-Exclude $childRel) {
            $script:skippedCount++
            return
        }
        if ($_.PSIsContainer) {
            Walk-Dir $_.FullName $childRel
        } else {
            Copy-Filtered $_.FullName $childRel
        }
    }
}

# 顶层文件
foreach ($f in $IncludeRootFiles) {
    $abs = Join-Path $RepoRoot $f
    if (Test-Path -LiteralPath $abs) {
        if (Should-Exclude $f) { $skippedCount++; continue }
        Copy-Filtered $abs $f
    }
}

# 顶层目录递归
foreach ($d in $IncludeRootDirs) {
    $abs = Join-Path $RepoRoot $d
    if (Test-Path -LiteralPath $abs) {
        Walk-Dir $abs $d
    }
}

$totalBytes = (Get-ChildItem $Stage -Recurse -File -Force | Measure-Object -Property Length -Sum).Sum
$totalFiles = (Get-ChildItem $Stage -Recurse -File -Force | Measure-Object).Count
Write-Host ("   stage: {0:N0} files, {1:N1} MB（拷贝 {2}，排除 {3}）" -f `
    $totalFiles, ($totalBytes / 1MB), $copiedCount, $skippedCount) -ForegroundColor Green

# ---------------------------------------------------------------------------
# 4. 写 .gitignore + LICENSE 占位 + README 占位（不覆盖已有 README）
# ---------------------------------------------------------------------------
Write-Host "==> [3/5] 写 .gitignore / LICENSE / README" -ForegroundColor Yellow

$gitignore = @'
# Build artifacts
build/bin/
build/release/
build/darwin/
publish/output/
publish/*.nsi
publish/*.nsh
publish/*.bmp
frontend/dist/
frontend/wailsjs/
frontend/node_modules/
frontend/package.json.md5

# Dependencies
node_modules/

# Logs / temp
logs/
*.log
*.tmp
*.bak

# Binaries
*.exe
*.dll
*.so
*.dylib

# IDE
.idea/
.vscode/
*.swp
.DS_Store

# Local env
.env
.env.local
config.yaml.bak*
'@
Set-Content -Path (Join-Path $Stage '.gitignore') -Value $gitignore -Encoding UTF8

$readmeStub = @'
# Boost Browser

A Wails/Go-based fingerprint browser with Chromium 146 kernel and built-in proxy tooling.

## Features

- Multi-profile management with isolated fingerprints
- Chromium 146 kernel + Google Chrome 148 fallback
- Built-in xray / sing-box proxy tooling
- Automatic update via GitHub Releases
- Multi-window operation sync (master/follower)

## Download

Get the latest installer from [Releases](https://github.com/sdohuajia/BoostBrowser/releases/latest).

## Build from source

Prerequisites: Go 1.22+, Node 20+, Wails CLI v2.

```bash
# Backend + frontend
wails build

# Full installer (requires Z:\BoostBrowser_cloak_test\ with chromium kernel)
powershell -ExecutionPolicy Bypass -File scripts\build_release.ps1
powershell -ExecutionPolicy Bypass -File scripts\build_installer.ps1
```

## Project layout

- `main.go` / `backend/` — Go backend (Wails)
- `frontend/src/` — React + TypeScript UI
- `scripts/` — PowerShell build / release scripts
- `build/windows/` — Windows installer assets

## License

See LICENSE.
'@
# 只在 stage 没有 README 时才写 stub
if (-not (Test-Path (Join-Path $Stage 'README.md'))) {
    Set-Content -Path (Join-Path $Stage 'README.md') -Value $readmeStub -Encoding UTF8
    Write-Host "   写入新 README.md（原仓库无 README）" -ForegroundColor Green
} else {
    Write-Host "   保留原 README.md" -ForegroundColor Green
}

# ---------------------------------------------------------------------------
# 5. Push (or DryRun)
# ---------------------------------------------------------------------------
Write-Host "==> [4/5] 列出 stage 顶层结构" -ForegroundColor Yellow
Get-ChildItem $Stage | Sort-Object PSIsContainer, Name | ForEach-Object {
    $type = if ($_.PSIsContainer) { 'DIR ' } else { 'FILE' }
    Write-Host ("   [$type] {0}" -f $_.Name)
}

Write-Host ""
if ($DryRun) {
    Write-Host "==> [5/5] DryRun 模式：未 push" -ForegroundColor Green
    Write-Host ""
    Write-Host "   stage 目录: $Stage"
    Write-Host "   你可以手动检查里面的内容，确认无误后用："
    Write-Host ""
    Write-Host "     powershell -ExecutionPolicy Bypass -File scripts\publish_opensource.ps1 -DryRun:`$false" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "   ↑ 真正执行 push（会强制覆盖远端 $Branch 分支）"
    return
}

Write-Host "==> [5/5] 正式 push（强制覆盖远端 $Branch）" -ForegroundColor Red
Write-Host ""
Write-Host "   ⚠️  这会把远端 $RemoteUrl 的 $Branch 分支整个替换"
Write-Host "   ⚠️  旧的 Python 版本仅在 git 历史中保留（如果远端启用了 reflog/PR 备份）"
Write-Host ""
$confirm = Read-Host "   输入 'YES' 继续，其他取消"
if ($confirm -ne 'YES') {
    Write-Host "   已取消" -ForegroundColor Yellow
    return
}

Push-Location $Stage
try {
    git init -b $Branch | Out-Host
    git add -A | Out-Host
    git -c user.name='Boost Browser' -c user.email='boost@local' commit -m $CommitMessage | Out-Host
    git remote add origin $RemoteUrl | Out-Host
    git push --force origin $Branch | Out-Host
    Write-Host ""
    Write-Host "   ✓ 已强制推送到 $RemoteUrl ($Branch)" -ForegroundColor Green
}
finally {
    Pop-Location
}
