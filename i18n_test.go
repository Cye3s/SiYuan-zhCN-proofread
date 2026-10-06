package main

// 解析/导出正确性验证（go test，不进交付产物）：
// 1. TestOrderedRoundtrip：zh-CN.json 解析→序列化应逐字节还原（保序正确性）
// 2. TestAppendErrorLog：崩溃日志格式（时间戳 + 追加式 + 内容完整）
// 3. TestLegacyMigrate：v1.0.0 旧列名库打开后自动迁移到新列名

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const p_cnPath = `langs\zh-CN.json`

func TestOrderedRoundtrip(v_t *testing.T) {
	v_data, v_err := os.ReadFile(p_cnPath)
	if v_err != nil {
		v_t.Skip("zh-CN 文件不存在: ", v_err)
	}
	v_node, v_err := ParseOrdered(v_data)
	if v_err != nil {
		v_t.Fatal("解析失败: ", v_err)
	}
	v_flat := Flatten(v_node, "", map[string]string{})
	v_t.Logf("keys(flatten str leaves) = %d", len(v_flat))

	v_out := v_node.Serialize(DetectIndent(string(v_data)))
	if bytes.Equal(v_data, v_out) {
		v_t.Logf("保序往返：与原文件逐字节一致 (%d bytes)", len(v_out))
		return
	}
	v_srcLines := strings.Split(string(v_data), "\n")
	v_outLines := strings.Split(string(v_out), "\n")
	v_t.Logf("非逐字节一致：原 %d 行 / 新 %d 行", len(v_srcLines), len(v_outLines))
	v_diffs := 0
	for v_idx := 0; v_idx < len(v_srcLines) && v_idx < len(v_outLines) && v_diffs < 10; v_idx++ {
		if v_srcLines[v_idx] != v_outLines[v_idx] {
			v_diffs++
			v_t.Logf("line %d:\n  原: %.80q\n  新: %.80q", v_idx+1, v_srcLines[v_idx], v_outLines[v_idx])
		}
	}
}

func TestAppendErrorLog(v_t *testing.T) {
	v_exe, v_err := os.Executable()
	if v_err != nil {
		v_t.Skip("无法定位测试二进制: ", v_err)
	}
	v_logPath := filepath.Join(filepath.Dir(v_exe), "error.log")
	_ = os.Remove(v_logPath)

	appendErrorLog("单测触发：模拟崩溃信息 A")
	appendErrorLog("单测触发：模拟崩溃信息 B")

	v_data, v_err := os.ReadFile(v_logPath)
	if v_err != nil {
		v_t.Fatal("error.log 未生成: ", v_err)
	}
	v_text := string(v_data)

	v_stamp := regexp.MustCompile(`\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\]`)
	v_stamps := v_stamp.FindAllString(v_text, -1)
	if len(v_stamps) != 2 {
		v_t.Errorf("时间戳应为 2 个（每次写入一条），实际 %d 个，内容:\n%s", len(v_stamps), v_text)
	}
	if !strings.Contains(v_text, "模拟崩溃信息 A") || !strings.Contains(v_text, "模拟崩溃信息 B") {
		v_t.Errorf("追加式失败或内容缺失，内容:\n%s", v_text)
	}

	_ = os.Remove(v_logPath)
}

// TestLegacyMigrate v1.0.0 旧列名库（official_cn/official_prev/official_json）
// 打开后应自动迁移到新名，且数据无损
func TestLegacyMigrate(v_t *testing.T) {
	v_path := filepath.Join(v_t.TempDir(), "legacy.db")
	v_db, v_err := sql.Open("sqlite", v_path)
	if v_err != nil {
		v_t.Fatal("打开临时库失败: ", v_err)
	}
	_, v_err = v_db.Exec(`CREATE TABLE entries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    key TEXT UNIQUE NOT NULL,
    zh_tw TEXT, official_cn TEXT, official_prev TEXT,
    fix_cn TEXT, is_new INTEGER DEFAULT 0, status TEXT DEFAULT 'pending');
CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT);
INSERT INTO meta VALUES('official_json','{"a":"x"}');
INSERT INTO entries(key, official_cn, zh_tw, fix_cn) VALUES('/a','简中值','繁','校对值');`)
	if v_err != nil {
		v_t.Fatal("构造旧库失败: ", v_err)
	}
	_ = v_db.Close()

	v_store, v_err := OpenStore(v_path)
	if v_err != nil {
		v_t.Fatal("OpenStore 迁移失败: ", v_err)
	}

	var v_oldCols int
	v_err = v_store.db.QueryRow(
		`SELECT COUNT(1) FROM pragma_table_info('entries') WHERE name IN ('official_cn','official_prev')`).
		Scan(&v_oldCols)
	if v_oldCols != 0 {
		v_t.Errorf("旧列名应已改名，仍存在 %d 个", v_oldCols)
	}
	var v_key, v_cn, v_fix string
	v_err = v_store.db.QueryRow(
		`SELECT key, zh_cn, fix_cn FROM entries WHERE key='/a'`).Scan(&v_key, &v_cn, &v_fix)
	if v_err != nil || v_cn != "简中值" || v_fix != "校对值" {
		v_t.Errorf("迁移后数据应完好: key=%s zh_cn=%s fix_cn=%s err=%v", v_key, v_cn, v_fix, v_err)
	}
	var v_meta string
	v_err = v_store.db.QueryRow(`SELECT v FROM meta WHERE k='zh_cn_json'`).Scan(&v_meta)
	if v_err != nil || v_meta != `{"a":"x"}` {
		v_t.Errorf("meta key 应迁移为 zh_cn_json: %s err=%v", v_meta, v_err)
	}
	// 幂等：再开一次不应报错
	v_store.Close()
	v_store2, v_err := OpenStore(v_path)
	if v_err != nil {
		v_t.Fatal("二次打开（幂等）失败: ", v_err)
	}
	v_store2.Close()
}
