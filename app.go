package main

// App：Wails 绑定对象 + /api/* 处理器（asset handler fallback，无 TCP 监听）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type SaveRow struct {
	Key   string  `json:"key"`
	FixCN *string `json:"fix_cn"`
}

type ImportReq struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Source  string `json:"source"`
}

type MigrateReq struct {
	FromKey string `json:"from_key"`
	ToKey   string `json:"to_key"`
}

type ExportStats struct {
	OK                  bool             `json:"ok"`
	Out                 string           `json:"out,omitempty"`
	Exported            int              `json:"exported"`
	MissingKeys         []string         `json:"missing_keys"`
	PlaceholderWarnings []PlaceholderWarn `json:"placeholder_warnings"`
}

type PlaceholderWarn struct {
	Key      string `json:"key"`
	Official string `json:"official"`
	Fix      string `json:"fix"`
}

type entryJSON struct {
	Key          string  `json:"key"`
	Display      string  `json:"display"`
	ZhTw         *string `json:"zh_tw"`
	OfficialCN   *string `json:"official_cn"`
	OfficialPrev *string `json:"official_prev"`
	FixCN        *string `json:"fix_cn"`
	IsNew        int     `json:"is_new"`
	Status       string  `json:"status"`
}

type App struct {
	v_ctx   context.Context
	v_store *Store
}

func NewApp(p_store *Store) *App {
	return &App{v_store: p_store}
}

func (v_app *App) setCtx(p_ctx context.Context) { v_app.v_ctx = p_ctx }

// ---------- HTTP API（由 Wails asset handler 兜底转发 /api/*） ----------
func (v_app *App) ServeHTTP(p_writer http.ResponseWriter, p_req *http.Request) {
	if !strings.HasPrefix(p_req.URL.Path, "/api/") {
		http.NotFound(p_writer, p_req)
		return
	}
	defer func() {
		if v_rec := recover(); v_rec != nil {
			appendErrorLog(fmt.Sprintf("API 处理崩溃（%s）： %v\n\n堆栈：\n%s",
				p_req.URL.Path, v_rec, debug.Stack()))
			http.Error(p_writer, fmt.Sprintf("内部错误: %v", v_rec), 500)
		}
	}()
	switch {
	case p_req.URL.Path == "/api/entries" && p_req.Method == "GET":
		v_app.handleEntries(p_writer)
	case p_req.URL.Path == "/api/import" && p_req.Method == "POST":
		v_app.handleImport(p_writer, p_req)
	case p_req.URL.Path == "/api/save" && p_req.Method == "POST":
		v_app.handleSave(p_writer, p_req)
	case p_req.URL.Path == "/api/migrate_fix" && p_req.Method == "POST":
		v_app.handleMigrate(p_writer, p_req)
	case p_req.URL.Path == "/api/export_content" && p_req.Method == "POST":
		v_app.handleExportContent(p_writer)
	default:
		http.NotFound(p_writer, p_req)
	}
}

func writeJSON(p_writer http.ResponseWriter, p_code int, p_val any) {
	p_writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	p_writer.WriteHeader(p_code)
	_ = json.NewEncoder(p_writer).Encode(p_val)
}

func readBody[T any](p_req *http.Request, p_target *T) error {
	defer p_req.Body.Close()
	v_data, v_err := io.ReadAll(io.LimitReader(p_req.Body, 32<<20))
	if v_err != nil {
		return v_err
	}
	return json.Unmarshal(v_data, p_target)
}

func (v_app *App) handleEntries(p_writer http.ResponseWriter) {
	v_entries, v_err := v_app.v_store.Entries()
	if v_err != nil {
		writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
		return
	}
	v_list := make([]entryJSON, 0, len(v_entries))
	for _, v_entry := range v_entries {
		v_item := entryJSON{
			Key: v_entry.Key, Display: DisplayKey(v_entry.Key),
			IsNew: v_entry.IsNew, Status: v_entry.Status,
		}
		if v_entry.ZhTw.Valid {
			v_val := v_entry.ZhTw.String
			v_item.ZhTw = &v_val
		}
		if v_entry.OfficialCN.Valid {
			v_val := v_entry.OfficialCN.String
			v_item.OfficialCN = &v_val
		}
		if v_entry.OfficialPrev.Valid {
			v_val := v_entry.OfficialPrev.String
			v_item.OfficialPrev = &v_val
		}
		if v_entry.FixCN.Valid {
			v_val := v_entry.FixCN.String
			v_item.FixCN = &v_val
		}
		v_list = append(v_list, v_item)
	}
	writeJSON(p_writer, 200, map[string]any{"rows": v_list})
}

func (v_app *App) handleImport(p_writer http.ResponseWriter, p_req *http.Request) {
	var v_req ImportReq
	if v_err := readBody(p_req, &v_req); v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": "请求解析失败: " + v_err.Error()})
		return
	}
	if v_req.Source == "" {
		v_req.Source = v_req.Role
	}
	v_root, v_err := ParseOrdered([]byte(v_req.Content))
	if v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": "JSON 解析失败: " + v_err.Error()})
		return
	}
	switch v_req.Role {
	case "tw":
		v_count, v_err := v_app.v_store.ImportTw(Flatten(v_root, "", map[string]string{}))
		if v_err != nil {
			writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
			return
		}
		writeJSON(p_writer, 200, map[string]any{"ok": true, "rows": v_count})
	case "official":
		v_res, v_err := v_app.v_store.ImportOfficial(v_root, v_req.Content, v_req.Source)
		if v_err != nil {
			writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
			return
		}
		writeJSON(p_writer, 200, v_res)
	default:
		writeJSON(p_writer, 400, map[string]string{"detail": "unknown role: " + v_req.Role})
	}
}

func (v_app *App) handleSave(p_writer http.ResponseWriter, p_req *http.Request) {
	var v_req struct {
		Rows []SaveRow `json:"rows"`
	}
	if v_err := readBody(p_req, &v_req); v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": "请求解析失败: " + v_err.Error()})
		return
	}
	v_count, v_err := v_app.v_store.SaveRows(v_req.Rows)
	if v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": v_err.Error()})
		return
	}
	writeJSON(p_writer, 200, map[string]any{"ok": true, "saved": v_count})
}

func (v_app *App) handleMigrate(p_writer http.ResponseWriter, p_req *http.Request) {
	var v_req MigrateReq
	if v_err := readBody(p_req, &v_req); v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": "请求解析失败: " + v_err.Error()})
		return
	}
	v_ok, v_msg, v_err := v_app.v_store.MigrateFix(v_req.FromKey, v_req.ToKey)
	if v_err != nil {
		writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
		return
	}
	writeJSON(p_writer, 200, map[string]any{"ok": v_ok, "msg": v_msg})
}

func (v_app *App) handleExportContent(p_writer http.ResponseWriter) {
	v_text, v_stats, v_err := v_app.buildExport()
	if v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": v_err.Error()})
		return
	}
	writeJSON(p_writer, 200, map[string]any{
		"ok": v_stats.OK, "exported": v_stats.Exported,
		"missing_keys":        v_stats.MissingKeys,
		"placeholder_warnings": v_stats.PlaceholderWarnings,
		"content":             v_text,
	})
}

// ---------- 导出 ----------
func (v_app *App) buildExport() (string, *ExportStats, error) {
	v_official, v_ok, v_err := v_app.v_store.OfficialJSON()
	if v_err != nil {
		return "", nil, v_err
	}
	if !v_ok {
		return "", nil, fmt.Errorf("尚未导入官方 zh-CN.json，无法导出")
	}
	v_root, v_err := ParseOrdered([]byte(v_official))
	if v_err != nil {
		return "", nil, v_err
	}
	v_indent := DetectIndent(v_official)
	v_officialPtrs := Flatten(v_root, "", map[string]string{})

	v_entries, v_err := v_app.v_store.Entries()
	if v_err != nil {
		return "", nil, v_err
	}
	v_stats := &ExportStats{
		OK:                  true,
		MissingKeys:         []string{},
		PlaceholderWarnings: []PlaceholderWarn{},
	}
	for _, v_entry := range v_entries {
		if v_entry.Status == "obsolete" {
			continue
		}
		if _, v_exists := v_officialPtrs[v_entry.Key]; !v_exists {
			continue
		}
		// 第 4 列与官方相同就复制（fix_cn 有值即用，无值回退官方）
		v_val := ""
		if v_entry.FixCN.Valid && v_entry.FixCN.String != "" {
			v_val = v_entry.FixCN.String
		} else if v_entry.OfficialCN.Valid {
			v_val = v_entry.OfficialCN.String
		} else {
			continue
		}
		if !v_root.SetByPointer(v_entry.Key, v_val) {
			v_stats.MissingKeys = append(v_stats.MissingKeys, DisplayKey(v_entry.Key))
			continue
		}
		v_stats.Exported++
		if v_entry.FixCN.Valid && v_entry.FixCN.String != "" && v_entry.OfficialCN.Valid {
			if PlaceholderMismatch(v_entry.FixCN.String, v_entry.OfficialCN.String) {
				v_stats.PlaceholderWarnings = append(v_stats.PlaceholderWarnings, PlaceholderWarn{
					Key: DisplayKey(v_entry.Key), Official: v_entry.OfficialCN.String, Fix: v_entry.FixCN.String,
				})
			}
		}
	}
	v_text := string(v_root.Serialize(v_indent))
	return v_text, v_stats, nil
}

// ExportWrite 导出到用户指定的完整文件路径（目录不存在时自动创建）
func (v_app *App) ExportWrite(p_path string) (*ExportStats, error) {
	v_text, v_stats, v_err := v_app.buildExport()
	if v_err != nil {
		return nil, v_err
	}
	if v_err = os.MkdirAll(filepath.Dir(p_path), 0o755); v_err != nil {
		return nil, v_err
	}
	if v_err = os.WriteFile(p_path, []byte(v_text), 0o644); v_err != nil {
		return nil, v_err
	}
	v_stats.Out = p_path
	return v_stats, nil
}

// ---------- Wails 原生绑定方法（前端 window.go.main.App.XXX 调用） ----------

// ExportWithDialog 弹出 Windows 原生另存为窗口导出（默认文件名 zh-CN.json，路径与文件名由用户自由选择；
// 目标已存在时系统自带“是否替换”确认）
func (v_app *App) ExportWithDialog() (map[string]any, error) {
	v_path, v_err := runtime.SaveFileDialog(v_app.v_ctx, runtime.SaveDialogOptions{
		Title:           "导出校对结果",
		DefaultFilename: "zh-CN.json",
	})
	if v_err != nil {
		return map[string]any{"ok": false, "msg": v_err.Error()}, nil
	}
	if v_path == "" {
		return map[string]any{"canceled": true}, nil
	}
	v_stats, v_err := v_app.ExportWrite(v_path)
	if v_err != nil {
		return map[string]any{"ok": false, "msg": v_err.Error()}, nil
	}
	return map[string]any{
		"ok": v_stats.OK, "out": v_stats.Out, "exported": v_stats.Exported,
		"missing_keys":        v_stats.MissingKeys,
		"placeholder_warnings": v_stats.PlaceholderWarnings,
	}, nil
}
