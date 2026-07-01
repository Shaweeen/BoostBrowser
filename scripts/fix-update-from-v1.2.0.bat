@echo off
REM Boost Browser - 修复 v1.2.0 升级失败 (一键运行)
REM 双击即可。脚本会自动以管理员模式运行 PowerShell 修复逻辑。

setlocal
cd /d "%~dp0"

echo ============================================================
echo   Boost Browser - v1.2.0 升级失败修复工具
echo ============================================================
echo.
echo 安装目录: %CD%
echo.
echo 即将下载 v1.3.0 并替换当前的 boost-browser.exe / updater.exe
echo （旧版本会自动备份为 *.v1.2.0.bak）
echo.
pause

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0fix-update-from-v1.2.0.ps1" -InstallRoot "%CD%"
set EXITCODE=%ERRORLEVEL%

echo.
if %EXITCODE% NEQ 0 (
    echo [失败] 修复脚本退出码 %EXITCODE%。请把上方红色错误截图发给开发者。
) else (
    echo [完成] 修复成功。
)
echo.
pause
endlocal
exit /b %EXITCODE%
