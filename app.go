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
	"sync"

	"github.com/longbridgeapp/opencc"
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
	OK                  bool              `json:"ok"`
	Out                 string            `json:"out,omitempty"`
	Exported            int               `json:"exported"`
	MissingKeys         []string          `json:"missing_keys"`
	PlaceholderWarnings []PlaceholderWarn `json:"placeholder_warnings"`
}

type PlaceholderWarn struct {
	Key string `json:"key"`
	Cn  string `json:"cn"`
	Fix string `json:"fix"`
}

type entryJSON struct {
	Key      string  `json:"key"`
	Display  string  `json:"display"`
	ZhTw     *string `json:"zh_tw"`
	ZhCN     *string `json:"zh_cn"`
	ZhCNPrev *string `json:"zh_cn_prev"`
	FixCN    *string `json:"fix_cn"`
	En       *string `json:"en,omitempty"`
	IsNew    int     `json:"is_new"`
	Status   string  `json:"status"`
	Order    int     `json:"order"`
	Simp     string  `json:"simp,omitempty"`
}

type App struct {
	v_ctx       context.Context
	v_store     *Store
	v_tw2s      *opencc.OpenCC // 字面转换（台湾正体→简体）
	v_tw2sp     *opencc.OpenCC // 词汇转换（台湾用词→大陆用词）
	v_simpMu    sync.Mutex
	v_simpCache map[string]string // 简中+繁体 -> 三色状态（导入后两列不变，命中缓存免重复转换）
}

func NewApp(p_store *Store) *App {
	return &App{v_store: p_store, v_simpCache: map[string]string{}}
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
	case p_req.URL.Path == "/api/simp_dict" && p_req.Method == "GET":
		v_app.handleListSimpDict(p_writer)
	case p_req.URL.Path == "/api/simp_dict/delete" && p_req.Method == "POST":
		v_app.handleDeleteSimpDict(p_writer, p_req)
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
	v_order := v_app.keyOrder()
	v_en := v_app.enMap()
	v_list := make([]entryJSON, 0, len(v_entries))
	for _, v_entry := range v_entries {
		v_item := entryJSON{
			Key: v_entry.Key, Display: DisplayKey(v_entry.Key),
			IsNew: v_entry.IsNew, Status: v_entry.Status,
		}
		if v_idx, v_ok := v_order[v_entry.Key]; v_ok {
			v_item.Order = v_idx
		} else {
			v_item.Order = 1<<30 + int(v_entry.ID) // 不在简中文件里的行排最后，按 id 保持稳定
		}
		v_item.Simp = v_app.simpState(v_entry)
		if v_entry.ZhTw.Valid {
			v_val := v_entry.ZhTw.String
			v_item.ZhTw = &v_val
		}
		if v_entry.ZhCN.Valid {
			v_val := v_entry.ZhCN.String
			v_item.ZhCN = &v_val
		}
		if v_entry.ZhCNPrev.Valid {
			v_val := v_entry.ZhCNPrev.String
			v_item.ZhCNPrev = &v_val
		}
		if v_entry.FixCN.Valid {
			v_val := v_entry.FixCN.String
			v_item.FixCN = &v_val
		}
		if v_enVal, v_ok := v_en[v_entry.Key]; v_ok {
			v_item.En = &v_enVal
		}
		v_list = append(v_list, v_item)
	}
	writeJSON(p_writer, 200, map[string]any{"rows": v_list})
}

// keyOrder 简中文件保序展平后的 key 顺序（表格按 json 中顺序排序用）
// 仅导入 zh-TW 时 fallback 到 zh_tw_json 的 key 顺序
func (v_app *App) keyOrder() map[string]int {
	v_order := map[string]int{}
	v_json, v_ok, v_err := v_app.v_store.ZhCNJSON()
	if v_err != nil || !v_ok {
		// fallback：简中未导入时用繁体文件顺序
		v_json, v_ok, v_err = v_app.v_store.ZhTWJSON()
		if v_err != nil || !v_ok {
			return v_order
		}
	}
	v_root, v_err := ParseOrdered([]byte(v_json))
	if v_err != nil {
		return v_order
	}
	var v_list []string
	FlattenOrder(v_root, "", &v_list)
	for v_idx, v_ptr := range v_list {
		v_order[v_ptr] = v_idx
	}
	return v_order
}

// enMap 英文参考展平（meta.en_json；未导入返回空 map）
func (v_app *App) enMap() map[string]string {
	v_map := map[string]string{}
	v_json, v_ok, v_err := v_app.v_store.EnJSON()
	if v_err != nil || !v_ok {
		return v_map
	}
	v_root, v_err := ParseOrdered([]byte(v_json))
	if v_err != nil {
		return v_map
	}
	return Flatten(v_root, "", v_map)
}

// tw2s / tw2sp 懒加载繁→简转换器（词典编译期内嵌，失败返回 nil 则不判定）
func (v_app *App) tw2s() *opencc.OpenCC {
	v_app.v_simpMu.Lock()
	defer v_app.v_simpMu.Unlock()
	if v_app.v_tw2s == nil {
		if v_cc, v_err := opencc.New("tw2s"); v_err == nil {
			v_app.v_tw2s = v_cc
		}
	}
	return v_app.v_tw2s
}

func (v_app *App) tw2sp() *opencc.OpenCC {
	v_app.v_simpMu.Lock()
	defer v_app.v_simpMu.Unlock()
	if v_app.v_tw2sp == nil {
		if v_cc, v_err := opencc.New("tw2sp"); v_err == nil {
			v_app.v_tw2sp = v_cc
		}
	}
	return v_app.v_tw2sp
}

// simpClass 三色判定（带缓存）：
// 优先查自定义词表（校对保存时自动捕获），命中跳过 OpenCC；未命中走 OpenCC
//
//	same  字面转换即相同（淡蓝：简繁字面相同，直转残留重点）
//	match 字面不同、按台湾用词转换后一致（淡黄：无需校对）
//	diff  两种转换都不同（淡红：需人工校正）
func (v_app *App) simpClass(p_cn, p_tw string) string {
	if p_cn == "" || p_tw == "" {
		return ""
	}
	v_key := p_cn + "\x00" + p_tw
	v_app.v_simpMu.Lock()
	v_hit, v_ok := v_app.v_simpCache[v_key]
	v_app.v_simpMu.Unlock()
	if v_ok {
		return v_hit
	}
	// 1. 自定义词表优先
	if v_mode := v_app.v_store.LookupSimpDict(p_tw, p_cn); v_mode != "" {
		v_app.v_simpMu.Lock()
		v_app.v_simpCache[v_key] = v_mode
		v_app.v_simpMu.Unlock()
		return v_mode
	}
	// 2. OpenCC 判定
	v_state := "diff"
	if v_out, v_err := v_app.tw2s().Convert(p_tw); v_err == nil && v_out == p_cn {
		v_state = "same"
	} else if v_out2, v_err2 := v_app.tw2sp().Convert(p_tw); v_err2 == nil && v_out2 == p_cn {
		v_state = "match"
	}
	v_app.v_simpMu.Lock()
	v_app.v_simpCache[v_key] = v_state
	v_app.v_simpMu.Unlock()
	return v_state
}

// simpState 行级状态：无法判定（未导入 zh-TW 或简中为空）返回空
func (v_app *App) simpState(p_entry Entry) string {
	if !p_entry.ZhTw.Valid || !p_entry.ZhCN.Valid {
		return ""
	}
	if v_app.tw2s() == nil || v_app.tw2sp() == nil {
		return ""
	}
	return v_app.simpClass(p_entry.ZhCN.String, p_entry.ZhTw.String)
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
		v_count, v_err := v_app.v_store.ImportTw(v_root, v_req.Content, Flatten(v_root, "", map[string]string{}))
		if v_err != nil {
			writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
			return
		}
		writeJSON(p_writer, 200, map[string]any{"ok": true, "rows": v_count})
	case "en":
		v_count, v_err := v_app.v_store.ImportEn(v_root, v_req.Content)
		if v_err != nil {
			writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
			return
		}
		writeJSON(p_writer, 200, map[string]any{"ok": true, "rows": v_count})
	case "cn":
		v_res, v_err := v_app.v_store.ImportCn(v_root, v_req.Content, v_req.Source)
		if v_err != nil {
			writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
			return
		}
		writeJSON(p_writer, 200, v_res)
	default:
		writeJSON(p_writer, 400, map[string]string{"detail": "unknown role: " + v_req.Role})
	}
}

// classifySimp 用 OpenCC 判定繁体与简中的关系（词表捕获回调）
// 注意：参数顺序是 (tw, fix)，判定 fix 是否为 tw 的简体或大陆用词转换结果
func (v_app *App) classifySimp(p_tw, p_fix string) string {
	if p_tw == "" || p_fix == "" {
		return ""
	}
	if v_out, v_err := v_app.tw2s().Convert(p_tw); v_err == nil && v_out == p_fix {
		return "same"
	}
	if v_out2, v_err2 := v_app.tw2sp().Convert(p_tw); v_err2 == nil && v_out2 == p_fix {
		return "match"
	}
	return "diff"
}

func (v_app *App) handleSave(p_writer http.ResponseWriter, p_req *http.Request) {
	var v_req struct {
		Rows []SaveRow `json:"rows"`
	}
	if v_err := readBody(p_req, &v_req); v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": "请求解析失败: " + v_err.Error()})
		return
	}
	v_count, v_err := v_app.v_store.SaveRows(v_req.Rows, v_app.classifySimp)
	if v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": v_err.Error()})
		return
	}
	// 清 simpCache，让词表新规则在重算时生效
	v_app.v_simpMu.Lock()
	v_app.v_simpCache = map[string]string{}
	v_app.v_simpMu.Unlock()
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
		"missing_keys":         v_stats.MissingKeys,
		"placeholder_warnings": v_stats.PlaceholderWarnings,
		"content":              v_text,
	})
}

// ---------- 导出 ----------
func (v_app *App) buildExport() (string, *ExportStats, error) {
	v_cnJson, v_ok, v_err := v_app.v_store.ZhCNJSON()
	if v_err != nil {
		return "", nil, v_err
	}
	if !v_ok {
		return "", nil, fmt.Errorf("尚未导入 zh-CN.json，无法导出")
	}
	v_root, v_err := ParseOrdered([]byte(v_cnJson))
	if v_err != nil {
		return "", nil, v_err
	}
	v_indent := DetectIndent(v_cnJson)
	v_cnPtrs := Flatten(v_root, "", map[string]string{})

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
		if _, v_exists := v_cnPtrs[v_entry.Key]; !v_exists {
			continue
		}
		// 第 4 列与简中相同就复制（fix_cn 有值即用，无值回退简中）
		v_val := ""
		if v_entry.FixCN.Valid && v_entry.FixCN.String != "" {
			v_val = v_entry.FixCN.String
		} else if v_entry.ZhCN.Valid {
			v_val = v_entry.ZhCN.String
		} else {
			continue
		}
		if !v_root.SetByPointer(v_entry.Key, v_val) {
			v_stats.MissingKeys = append(v_stats.MissingKeys, DisplayKey(v_entry.Key))
			continue
		}
		v_stats.Exported++
		if v_entry.FixCN.Valid && v_entry.FixCN.String != "" && v_entry.ZhCN.Valid {
			if PlaceholderMismatch(v_entry.FixCN.String, v_entry.ZhCN.String) {
				v_stats.PlaceholderWarnings = append(v_stats.PlaceholderWarnings, PlaceholderWarn{
					Key: DisplayKey(v_entry.Key), Cn: v_entry.ZhCN.String, Fix: v_entry.FixCN.String,
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
		"missing_keys":         v_stats.MissingKeys,
		"placeholder_warnings": v_stats.PlaceholderWarnings,
	}, nil
}

// handleListSimpDict 词表列表（前端管理用）
func (v_app *App) handleListSimpDict(p_writer http.ResponseWriter) {
	v_list, v_err := v_app.v_store.ListSimpDict()
	if v_err != nil {
		writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
		return
	}
	if v_list == nil {
		v_list = []map[string]string{}
	}
	writeJSON(p_writer, 200, map[string]any{"rows": v_list})
}

// handleDeleteSimpDict 删除词表规则
func (v_app *App) handleDeleteSimpDict(p_writer http.ResponseWriter, p_req *http.Request) {
	var v_req struct {
		Tw string `json:"tw"`
		Cn string `json:"cn"`
	}
	if v_err := readBody(p_req, &v_req); v_err != nil {
		writeJSON(p_writer, 400, map[string]string{"detail": "请求解析失败"})
		return
	}
	if v_err := v_app.v_store.DeleteSimpDict(v_req.Tw, v_req.Cn); v_err != nil {
		writeJSON(p_writer, 500, map[string]string{"detail": v_err.Error()})
		return
	}
	// 清 simpCache 让重算生效
	v_app.v_simpMu.Lock()
	v_app.v_simpCache = map[string]string{}
	v_app.v_simpMu.Unlock()
	writeJSON(p_writer, 200, map[string]any{"ok": true})
}
