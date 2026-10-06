@echo off
REM ============================================================
REM  SiYuan-zhCN-proofread 思源简中校对 - 打包脚本
REM  前提：已安装 Go（1.21+），app.ico 与本脚本同目录
REM  产物：本目录 SiYuan-zhCN-proofread.exe（单文件，库 data.db 在 exe 旁）
REM ============================================================

cd /d "%~dp0"

if not exist "app.ico" (
    echo [错误] 缺少 app.ico，请把图标文件放到本目录后重试
    pause
    exit /b 1
)

echo [1/3] 生成图标与版本资源（winres.json 含双图标组：ID=1 资源管理器 / ID=32512 窗口标题栏）...
go run github.com/tc-hib/go-winres@latest make --in winres.json --arch amd64
if errorlevel 1 (
    echo [错误] 资源生成失败，请检查 Go 环境
    pause
    exit /b 1
)

echo [2/3] 编译单文件 exe（Wails 必须带 desktop,production tags；-H windowsgui 隐藏控制台黑框）...
go build -tags desktop,production -trimpath -ldflags "-s -w -H windowsgui" -o SiYuan-zhCN-proofread.exe .
if errorlevel 1 (
    echo [错误] 编译失败
    pause
    exit /b 1
)

echo [3/3] 完成：%cd%\SiYuan-zhCN-proofread.exe
echo  双击运行为原生窗口；库 data.db 建在 exe 旁。
pause