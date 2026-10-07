package main

// SQLite 存储层，库文件 data.db 在 exe 旁，驱动 modernc.org/sqlite（纯 Go，无 CGO）。

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Entry struct {
	ID       int64
	Key      string
	ZhTw     sql.NullString
	ZhCN     sql.NullString
	ZhCNPrev sql.NullString
	FixCN    sql.NullString
	IsNew    int
	Status   string
}

type RenamePair struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Value string `json:"value"`
}

type SyncResult struct {
	OK      bool         `json:"ok"`
	Added   int          `json:"added"`
	Removed int          `json:"removed"`
	Changed int          `json:"changed"`
	Renamed []RenamePair `json:"renamed"`
}

const p_schema = `
CREATE TABLE IF NOT EXISTS entries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT UNIQUE NOT NULL,
    zh_tw       TEXT,
    zh_cn       TEXT,
    zh_cn_prev  TEXT,
    fix_cn      TEXT,
    is_new      INTEGER DEFAULT 0,
    status      TEXT DEFAULT 'pending'
);
CREATE TABLE IF NOT EXISTS sync_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT, source TEXT,
    added INTEGER, removed INTEGER, changed INTEGER
);
CREATE TABLE IF NOT EXISTS meta (
    k TEXT PRIMARY KEY, v TEXT
);
CREATE TABLE IF NOT EXISTS simp_dict (
    tw   TEXT,
    cn   TEXT,
    mode TEXT,
    PRIMARY KEY (tw, cn)
);
`

type Store struct {
	db       *sql.DB
	simpDict map[string]string // 简繁词表缓存："tw\x00cn" -> mode（same/match）
	simpMu   sync.RWMutex
}

// OpenStore 打开（不存在则初始化）数据库
func OpenStore(p_path string) (*Store, error) {
	v_db, v_err := sql.Open("sqlite", p_path)
	if v_err != nil {
		return nil, v_err
	}
	v_db.SetMaxOpenConns(1) // modernc sqlite 并发写易锁，单连接 + WAL 最稳
	if _, v_err = v_db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); v_err != nil {
		return nil, v_err
	}
	if _, v_err = v_db.Exec(p_schema); v_err != nil {
		return nil, v_err
	}
	v_store := &Store{db: v_db, simpDict: map[string]string{}}
	if v_err = v_store.migrateLegacy(); v_err != nil {
		return nil, v_err
	}
	if v_err = v_store.loadSimpDict(); v_err != nil {
		return nil, v_err
	}
	return v_store, nil
}

// migrateLegacy v1.0.0 旧列名/旧 meta key 自动迁移（已是新名时跳过，幂等）
func (v_store *Store) migrateLegacy() error {
	v_rows, v_err := v_store.db.Query(`PRAGMA table_info(entries)`)
	if v_err != nil {
		return v_err
	}
	var v_cols []string
	for v_rows.Next() {
		var v_cid, v_notNull, v_pk int
		var v_name, v_type string
		var v_default sql.NullString
		if v_err = v_rows.Scan(&v_cid, &v_name, &v_type, &v_notNull, &v_default, &v_pk); v_err != nil {
			v_rows.Close()
			return v_err
		}
		v_cols = append(v_cols, v_name)
	}
	v_rows.Close()
	v_hasCol := func(p_name string) bool {
		for _, v_col := range v_cols {
			if v_col == p_name {
				return true
			}
		}
		return false
	}
	if v_hasCol("official_cn") {
		if _, v_err = v_store.db.Exec(`ALTER TABLE entries RENAME COLUMN official_cn TO zh_cn`); v_err != nil {
			return v_err
		}
	}
	if v_hasCol("official_prev") {
		if _, v_err = v_store.db.Exec(`ALTER TABLE entries RENAME COLUMN official_prev TO zh_cn_prev`); v_err != nil {
			return v_err
		}
	}
	if _, v_err = v_store.db.Exec(`UPDATE meta SET k='zh_cn_json' WHERE k='official_json'`); v_err != nil {
		return v_err
	}
	return nil
}

func (v_store *Store) Close() error { return v_store.db.Close() }

// scanEntry 复用行扫描
func scanEntry(p_rows *sql.Rows) (Entry, error) {
	var v_entry Entry
	v_err := p_rows.Scan(&v_entry.ID, &v_entry.Key, &v_entry.ZhTw,
		&v_entry.ZhCN, &v_entry.ZhCNPrev,
		&v_entry.FixCN, &v_entry.IsNew, &v_entry.Status)
	return v_entry, v_err
}

const p_selectEntries = `SELECT id, key, zh_tw, zh_cn, zh_cn_prev, fix_cn, is_new, status FROM entries`

// Entries 全量条目
func (v_store *Store) Entries() ([]Entry, error) {
	v_rows, v_err := v_store.db.Query(p_selectEntries)
	if v_err != nil {
		return nil, v_err
	}
	defer v_rows.Close()
	var v_list []Entry
	for v_rows.Next() {
		v_entry, v_err := scanEntry(v_rows)
		if v_err != nil {
			return nil, v_err
		}
		v_list = append(v_list, v_entry)
	}
	return v_list, v_rows.Err()
}

// ImportTw 导入繁体参照：更新已有行的 zh_tw，缺失 key 建占位行（zh_cn 留空，不进导出）
// 同时存 zh_tw_json 全文到 meta，供 keyOrder fallback（仅导入 zh-TW 时也能按文件顺序排）
func (v_store *Store) ImportTw(p_root *Node, p_content string, p_flat map[string]string) (int, error) {
	v_tx, v_err := v_store.db.Begin()
	if v_err != nil {
		return 0, v_err
	}
	defer v_tx.Rollback()

	for v_ptr, v_val := range p_flat {
		v_res, v_err := v_tx.Exec(`UPDATE entries SET zh_tw=? WHERE key=?`, v_val, v_ptr)
		if v_err != nil {
			return 0, v_err
		}
		v_count, _ := v_res.RowsAffected()
		if v_count == 0 {
			if _, v_err = v_tx.Exec(
				`INSERT INTO entries(key, zh_tw, status) VALUES(?,?,'pending')`, v_ptr, v_val); v_err != nil {
				return 0, v_err
			}
		}
	}
	if _, v_err = v_tx.Exec(
		`INSERT INTO meta(k,v) VALUES('zh_tw_json',?)
		 ON CONFLICT(k) DO UPDATE SET v=excluded.v`, p_content); v_err != nil {
		return 0, v_err
	}
	if v_err = v_tx.Commit(); v_err != nil {
		return 0, v_err
	}
	return len(p_flat), nil
}

// ImportCn 简中版本同步：
// 新增打 is_new、消失软删 obsolete、值漂移留底 zh_cn_prev、值相同疑似改名配对
func (v_store *Store) ImportCn(p_root *Node, p_content, p_source string) (*SyncResult, error) {
	v_flat := Flatten(p_root, "", map[string]string{})

	v_tx, v_err := v_store.db.Begin()
	if v_err != nil {
		return nil, v_err
	}
	defer v_tx.Rollback()

	// 现有行
	v_existing := map[string]Entry{}
	v_rows, v_err := v_tx.Query(p_selectEntries)
	if v_err != nil {
		return nil, v_err
	}
	for v_rows.Next() {
		v_entry, _ := scanEntry(v_rows)
		v_existing[v_entry.Key] = v_entry
	}
	v_rows.Close()

	v_result := &SyncResult{OK: true, Renamed: []RenamePair{}}
	v_added, v_removed, v_changed := 0, 0, 0
	v_vanishedVals := map[string]string{} // 消失行的简中值 -> pointer（改名检测用）

	for v_ptr, v_val := range v_flat {
		v_old, v_exists := v_existing[v_ptr]
		if !v_exists {
			if _, v_err = v_tx.Exec(
				`INSERT INTO entries(key, zh_cn, is_new, status) VALUES(?,?,1,'pending')`,
				v_ptr, v_val); v_err != nil {
				return nil, v_err
			}
			v_added++
			continue
		}
		switch {
		case !v_old.ZhCN.Valid: // tw 先行导入的占位行，简中首次填上
			if _, v_err = v_tx.Exec(
				`UPDATE entries SET zh_cn=?, is_new=1, status='pending' WHERE key=?`,
				v_val, v_ptr); v_err != nil {
				return nil, v_err
			}
			v_added++
		case v_old.ZhCN.String != v_val: // 简中值已修改（需重新核对）
			v_status := "pending"
			if v_old.FixCN.Valid && v_old.FixCN.String != "" {
				v_status = "stale"
			}
			if _, v_err = v_tx.Exec(
				`UPDATE entries SET zh_cn_prev=?, zh_cn=?, status=?, is_new=0 WHERE key=?`,
				v_old.ZhCN.String, v_val, v_status, v_ptr); v_err != nil {
				return nil, v_err
			}
			v_changed++
		case v_old.Status == "obsolete": // 复活
			if _, v_err = v_tx.Exec(
				`UPDATE entries SET status='pending', is_new=1 WHERE key=?`, v_ptr); v_err != nil {
				return nil, v_err
			}
			v_added++
		default: // 清上一轮 is_new
			if _, v_err = v_tx.Exec(`UPDATE entries SET is_new=0 WHERE key=?`, v_ptr); v_err != nil {
				return nil, v_err
			}
		}
	}

	// 简中文件消失的 key -> 软删
	for v_ptr, v_old := range v_existing {
		if _, v_exists := v_flat[v_ptr]; !v_exists {
			if _, v_err = v_tx.Exec(
				`UPDATE entries SET status='obsolete', is_new=0 WHERE key=?`, v_ptr); v_err != nil {
				return nil, v_err
			}
			v_removed++
			if v_old.ZhCN.Valid {
				v_vanishedVals[v_old.ZhCN.String] = v_ptr
			}
		}
	}

	// 疑似改名检测：消失行的简中值 == 新增 key 的简中值
	for v_ptr, v_val := range v_flat {
		if _, v_existed := v_existing[v_ptr]; v_existed {
			continue
		}
		if v_from, v_ok := v_vanishedVals[v_val]; v_ok {
			v_result.Renamed = append(v_result.Renamed,
				RenamePair{From: v_from, To: v_ptr, Value: v_val})
		}
	}

	// 简中原文与同步日志
	if _, v_err = v_tx.Exec(
		`INSERT INTO meta(k,v) VALUES('zh_cn_json',?)
		 ON CONFLICT(k) DO UPDATE SET v=excluded.v`, p_content); v_err != nil {
		return nil, v_err
	}
	v_ts := time.Now().Format("2006-01-02T15:04:05")
	if _, v_err = v_tx.Exec(
		`INSERT INTO sync_log(ts, source, added, removed, changed) VALUES(?,?,?,?,?)`,
		v_ts, p_source, v_added, v_removed, v_changed); v_err != nil {
		return nil, v_err
	}

	if v_err = v_tx.Commit(); v_err != nil {
		return nil, v_err
	}
	v_result.Added, v_result.Removed, v_result.Changed = v_added, v_removed, v_changed
	return v_result, nil
}

// SaveRows 保存校对值并标记 verified（key 必须已存在）
// 保存时自动捕获简繁词表：若 fix_cn 与 zh_tw 构成 same/match 关系，
// 记录 tw->fix 映射到 simp_dict，下次同类繁简对自动命中
// p_classify 回调由 app 层提供（用 OpenCC 判定 tw 与 fix 的关系），可为 nil
func (v_store *Store) SaveRows(p_rows []SaveRow, p_classify func(p_tw, p_fix string) string) (int, error) {
	v_tx, v_err := v_store.db.Begin()
	if v_err != nil {
		return 0, v_err
	}
	defer v_tx.Rollback()

	for _, v_row := range p_rows {
		var v_count int
		if v_err = v_tx.QueryRow(
			`SELECT COUNT(1) FROM entries WHERE key=?`, v_row.Key).Scan(&v_count); v_err != nil {
			return 0, v_err
		}
		if v_count == 0 {
			return 0, fmt.Errorf("key 不在库中: %s", v_row.Key)
		}
		var v_tw sql.NullString
		_ = v_tx.QueryRow(`SELECT zh_tw FROM entries WHERE key=?`, v_row.Key).Scan(&v_tw)
		var v_fix any
		if v_row.FixCN != nil {
			v_fix = *v_row.FixCN
		}
		if _, v_err = v_tx.Exec(
			`UPDATE entries SET fix_cn=?, status='verified' WHERE key=?`, v_fix, v_row.Key); v_err != nil {
			return 0, v_err
		}
		// 词表捕获：fix_cn 非空且 zh_tw 有值，用回调判定关系后入库
		if p_classify != nil && v_row.FixCN != nil && *v_row.FixCN != "" && v_tw.Valid && v_tw.String != "" {
			v_mode := p_classify(v_tw.String, *v_row.FixCN)
			if v_mode == "same" || v_mode == "match" {
				v_store.captureSimpDict(v_tx, v_tw.String, *v_row.FixCN, v_mode)
			}
		}
	}
	if v_err = v_tx.Commit(); v_err != nil {
		return 0, v_err
	}
	return len(p_rows), nil
}

// MigrateFix 改名迁移：把 from 行的 fix_cn 复制到 to 行
func (v_store *Store) MigrateFix(p_from, p_to string) (bool, string, error) {
	var v_fix sql.NullString
	v_err := v_store.db.QueryRow(
		`SELECT fix_cn FROM entries WHERE key=?`, p_from).Scan(&v_fix)
	if v_err != nil {
		if errors.Is(v_err, sql.ErrNoRows) {
			return false, "源行不存在", nil
		}
		return false, "", v_err
	}
	if !v_fix.Valid || v_fix.String == "" {
		return false, "源行无校对值", nil
	}
	if _, v_err = v_store.db.Exec(
		`UPDATE entries SET fix_cn=?, status='verified' WHERE key=?`, v_fix.String, p_to); v_err != nil {
		return false, "", v_err
	}
	return true, "", nil
}

// ZhCNJSON 取简中原文（导出骨架）
func (v_store *Store) ZhCNJSON() (string, bool, error) {
	var v_content string
	v_err := v_store.db.QueryRow(
		`SELECT v FROM meta WHERE k='zh_cn_json'`).Scan(&v_content)
	if v_err != nil {
		if errors.Is(v_err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, v_err
	}
	return v_content, true, nil
}

// ImportEn 导入英文参考：仅存 meta 全文（不进 entries 状态机，双击校对时参考）
func (v_store *Store) ImportEn(p_root *Node, p_content string) (int, error) {
	v_flat := Flatten(p_root, "", map[string]string{})
	v_tx, v_err := v_store.db.Begin()
	if v_err != nil {
		return 0, v_err
	}
	defer v_tx.Rollback()
	if _, v_err = v_tx.Exec(
		`INSERT INTO meta(k,v) VALUES('en_json',?)
		 ON CONFLICT(k) DO UPDATE SET v=excluded.v`, p_content); v_err != nil {
		return 0, v_err
	}
	if v_err = v_tx.Commit(); v_err != nil {
		return 0, v_err
	}
	return len(v_flat), nil
}

// EnJSON 取英文参考全文（未导入时 ok=false）
func (v_store *Store) EnJSON() (string, bool, error) {
	var v_content string
	v_err := v_store.db.QueryRow(
		`SELECT v FROM meta WHERE k='en_json'`).Scan(&v_content)
	if v_err != nil {
		if errors.Is(v_err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, v_err
	}
	return v_content, true, nil
}

// ZhTWJSON 取繁体原文全文（未导入时 ok=false）
func (v_store *Store) ZhTWJSON() (string, bool, error) {
	var v_content string
	v_err := v_store.db.QueryRow(
		`SELECT v FROM meta WHERE k='zh_tw_json'`).Scan(&v_content)
	if v_err != nil {
		if errors.Is(v_err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, v_err
	}
	return v_content, true, nil
}

// ---------- 简繁词表（校对保存时自动捕获） ----------

// loadSimpDict 启动时全表加载到内存
func (v_store *Store) loadSimpDict() error {
	v_rows, v_err := v_store.db.Query(`SELECT tw, cn, mode FROM simp_dict`)
	if v_err != nil {
		return v_err
	}
	defer v_rows.Close()
	v_store.simpMu.Lock()
	defer v_store.simpMu.Unlock()
	v_store.simpDict = map[string]string{}
	for v_rows.Next() {
		var v_tw, v_cn, v_mode string
		if v_err = v_rows.Scan(&v_tw, &v_cn, &v_mode); v_err != nil {
			return v_err
		}
		v_store.simpDict[v_tw+"\x00"+v_cn] = v_mode
	}
	return v_rows.Err()
}

// LookupSimpDict 查词表：返回 mode（same/match）或空
func (v_store *Store) LookupSimpDict(p_tw, p_cn string) string {
	v_store.simpMu.RLock()
	defer v_store.simpMu.RUnlock()
	return v_store.simpDict[p_tw+"\x00"+p_cn]
}

// captureSimpDict 保存时记录词表规则（事务内）
func (v_store *Store) captureSimpDict(p_tx *sql.Tx, p_tw, p_fix, p_mode string) {
	if _, v_err := p_tx.Exec(
		`INSERT INTO simp_dict(tw, cn, mode) VALUES(?,?,?)
		 ON CONFLICT(tw, cn) DO UPDATE SET mode=excluded.mode`,
		p_tw, p_fix, p_mode); v_err != nil {
		return // 词表是优化不是数据，失败不阻保存
	}
	v_store.simpMu.Lock()
	v_store.simpDict[p_tw+"\x00"+p_fix] = p_mode
	v_store.simpMu.Unlock()
}

// ListSimpDict 词表列表（前端管理用）
func (v_store *Store) ListSimpDict() ([]map[string]string, error) {
	v_rows, v_err := v_store.db.Query(`SELECT tw, cn, mode FROM simp_dict ORDER BY tw`)
	if v_err != nil {
		return nil, v_err
	}
	defer v_rows.Close()
	var v_list []map[string]string
	for v_rows.Next() {
		var v_tw, v_cn, v_mode string
		if v_err = v_rows.Scan(&v_tw, &v_cn, &v_mode); v_err != nil {
			return nil, v_err
		}
		v_list = append(v_list, map[string]string{"tw": v_tw, "cn": v_cn, "mode": v_mode})
	}
	return v_list, v_rows.Err()
}

// DeleteSimpDict 删除词表规则
func (v_store *Store) DeleteSimpDict(p_tw, p_cn string) error {
	if _, v_err := v_store.db.Exec(`DELETE FROM simp_dict WHERE tw=? AND cn=?`, p_tw, p_cn); v_err != nil {
		return v_err
	}
	v_store.simpMu.Lock()
	delete(v_store.simpDict, p_tw+"\x00"+p_cn)
	v_store.simpMu.Unlock()
	return nil
}
