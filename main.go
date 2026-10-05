package main

// Wails 入口：单进程原生窗口（WebView2 内嵌），无 TCP 端口、无浏览器。
// /api/* 由 asset handler fallback 交给 App.ServeHTTP，静态资源直接内嵌。
// 数据库：exe 旁 data.db（独立库，从零一步步导入）。

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

const (
	p_dbFileName = "data.db"
	p_appTitle   = "思源简中校对 · SiYuan-zhCN-proofread"
)

// dbPath 数据库路径：exe 旁 data.db（go run 开发模式下退回工作目录）
func dbPath() string {
	v_exe, v_err := os.Executable()
	if v_err == nil && !strings.Contains(v_exe, "go-build") {
		return filepath.Join(filepath.Dir(v_exe), p_dbFileName)
	}
	v_wd, _ := os.Getwd()
	return filepath.Join(v_wd, p_dbFileName)
}

func fileExists(p_path string) bool {
	_, v_err := os.Stat(p_path)
	return v_err == nil
}

// fatalBox GUI 模式（-H windowsgui）无控制台，致命错误必须弹原生窗口 + 落盘，
// 否则双击运行出错时会无声闪退。
// appendErrorLog 崩溃/错误日志：时间 + 内容，追加式写入 exe 旁 error.log（保留历史）
func appendErrorLog(p_msg string) {
	v_line := "\n[" + time.Now().Format("2006-01-02 15:04:05") + "]\n" + p_msg + "\n"
	v_dir := "."
	if v_exe, v_err := os.Executable(); v_err == nil {
		v_dir = filepath.Dir(v_exe)
	}
	v_file, v_err := os.OpenFile(filepath.Join(v_dir, "error.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if v_err != nil {
		return
	}
	defer v_file.Close()
	_, _ = v_file.WriteString(v_line)
}

func fatalBox(p_msg string) {
	appendErrorLog(p_msg)
	v_user32 := syscall.NewLazyDLL("user32.dll")
	v_msgBox := v_user32.NewProc("MessageBoxW")
	v_title, _ := syscall.UTF16PtrFromString("SiYuan-zhCN-proofread 启动失败")
	v_text, _ := syscall.UTF16PtrFromString(p_msg + "\n\n详情见同目录 error.log")
	_, _, _ = v_msgBox.Call(0,
		uintptr(unsafe.Pointer(v_text)),
		uintptr(unsafe.Pointer(v_title)),
		0x10) // MB_ICONERROR
	os.Exit(1)
}

func main() {
	// panic 兜底：时间 + 崩溃内容 + 堆栈全部落盘，再弹窗退出
	defer func() {
		if v_rec := recover(); v_rec != nil {
			v_panicInfo := fmt.Sprintf("程序崩溃： %v\n\n堆栈：\n%s", v_rec, debug.Stack())
			appendErrorLog(v_panicInfo)
			v_user32 := syscall.NewLazyDLL("user32.dll")
			v_msgBox := v_user32.NewProc("MessageBoxW")
			v_title, _ := syscall.UTF16PtrFromString("SiYuan-zhCN-proofread 崩溃退出")
			v_text, _ := syscall.UTF16PtrFromString("程序发生内部错误已退出，详情见同目录 error.log")
			_, _, _ = v_msgBox.Call(0,
				uintptr(unsafe.Pointer(v_text)),
				uintptr(unsafe.Pointer(v_title)),
				0x10)
			os.Exit(1)
		}
	}()

	v_store, v_err := OpenStore(dbPath())
	if v_err != nil {
		fatalBox("打开数据库失败: " + v_err.Error())
	}
	defer v_store.Close()

	v_app := NewApp(v_store)

	v_err = wails.Run(&options.App{
		Title:     p_appTitle,
		Width:     1280,
		Height:    820,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: v_app, // /api/* 等非静态资源请求由此接管
		},
		OnStartup: func(p_ctx context.Context) {
			v_app.setCtx(p_ctx)
			runtime.WindowCenter(p_ctx) // 强制居中，防止窗口出现在屏幕可视区外
		},
		Bind: []interface{}{v_app},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})
	if v_err != nil {
		fatalBox("启动失败: " + v_err.Error())
	}
}
