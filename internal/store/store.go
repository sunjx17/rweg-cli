// Package store 是 SQLite 持久层：每个 namespace 一个库文件。
//
// 按 namespace 分文件（而非单库多行）的好处：
//   - 跨终端零竞争，不需要为「两个终端同时写」设计复杂的隔离逻辑
//   - 删一个终端的 ns = 删一个文件，GC 极简
//   - served / retired 本来就是 ns 私有数据，分文件天然对齐
//
// 代价：snapshot（内容寻址、跨 ns 复用）无法跨 ns 共享。
// 这可以接受 —— snapshot 只是哈希数组，不是文件内容，重算成本极低。
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"rweg/internal/config"
)

// DB 是一个 namespace 对应的库。
type DB struct {
	sql  *sql.DB
	path string
	ns   string
}

// Open 打开（必要时创建）某个 namespace 的库。
//
// 自动建库目录 + 建表。dbPath 是 config.Resolved.DBPath(ns) 的结果。
func Open(dbPath, ns string, cfg config.DB) (*DB, error) {
	if dir := filepath.Dir(dbPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir %s: %w", dir, err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(%s)",
		dbPath, cfg.BusyTimeoutMS, journalMode(cfg.JournalMode))

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dbPath, err)
	}
	// SQLite 单写者；限制连接数避免 WAL 下的锁竞争放大
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping %s: %w", dbPath, err)
	}
	db := &DB{sql: sqlDB, path: dbPath, ns: ns}
	if err := db.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func journalMode(m string) string {
	switch m {
	case "", "WAL":
		return "WAL"
	case "DELETE", "TRUNCATE", "PERSIST", "MEMORY", "OFF":
		return m
	default:
		return "WAL"
	}
}

// Schema 是全部建表语句。
//
// snapshot 表保留但当前按 ns 分文件后用不到跨 ns 复用；
// 留着是因为它同时充当「本 ns 内的内容缓存」，且 canon_ver 参与缓存键。
var Schema = []string{
	`CREATE TABLE IF NOT EXISTS meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS namespace (
		ns           TEXT PRIMARY KEY,
		cwd          TEXT NOT NULL,
		created_at   INTEGER NOT NULL,
		last_seen_at INTEGER NOT NULL,
		pid          INTEGER,
		shell        TEXT,
		canon_ver    INTEGER NOT NULL DEFAULT 2,
		stride       INTEGER NOT NULL DEFAULT 3907
	)`,
	// 本 ns 的文件快照（内容寻址，同内容 → 同快照）
	`CREATE TABLE IF NOT EXISTS snapshot (
		path       TEXT NOT NULL,
		checksum   TEXT NOT NULL,
		line_count INTEGER NOT NULL,
		hashes     TEXT NOT NULL,
		canon_ver  INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		PRIMARY KEY (path, checksum, canon_ver)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_snapshot_created ON snapshot(created_at)`,
	// 模型"已见"的锚点镜像（按行号 1-based，null = 未 serve）
	`CREATE TABLE IF NOT EXISTS served (
		path       TEXT NOT NULL,
		hashes     TEXT NOT NULL,
		canons     TEXT,
		line_count INTEGER NOT NULL DEFAULT 0,
		canon_ver  INTEGER NOT NULL DEFAULT 2,
		stride     INTEGER NOT NULL DEFAULT 3907,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (path)
	)`,
	// 编辑释放的槽位：模型可能还持有，禁止再分配
	// 撤销栈：每次 write / edit 前记一条前像（原始字节），undo 用它回退一步
	`CREATE TABLE IF NOT EXISTS undo (
		seq        INTEGER PRIMARY KEY AUTOINCREMENT,
		path       TEXT NOT NULL,
		existed    INTEGER NOT NULL,
		too_big    INTEGER NOT NULL DEFAULT 0,
		kind       TEXT NOT NULL,
		content    BLOB,
		created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_undo_path ON undo(path, seq)`,
	`CREATE TABLE IF NOT EXISTS retired (
		path     TEXT NOT NULL,
		hash     TEXT NOT NULL,
		freed_at INTEGER NOT NULL,
		PRIMARY KEY (path, hash)
	)`,
}

func (d *DB) migrate() error {
	for _, stmt := range Schema {
		if _, err := d.sql.Exec(stmt); err != nil {
			return fmt.Errorf("migrate %s: %w", d.path, err)
		}
	}
	// 轻量迁移：给早期建的库补列。
	// CREATE TABLE IF NOT EXISTS 不会给已存在的表加列，所以要单独 ALTER。
	//
	// 数据库是派生物（TTL 24h 后被清），所以这里只保证"不崩"：
	// 加不上就靠查询侧的默认值兜底。见 GetServed 的 canon_ver/stride 回落。
	for _, m := range []string{
		`ALTER TABLE served ADD COLUMN canon_ver INTEGER NOT NULL DEFAULT 2`,
		`ALTER TABLE served ADD COLUMN stride INTEGER NOT NULL DEFAULT 3907`,
	} {
		if _, err := d.sql.Exec(m); err != nil {
			// "duplicate column name" = 已经加过了，忽略
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("migrate %s: %s: %w", d.path, m, err)
			}
		}
	}
	return nil
}

// Close 关闭库。WAL 模式下会顺便 checkpoint。
func (d *DB) Close() error {
	if d.sql == nil {
		return nil
	}
	// 尽力 checkpoint，失败不致命（进程退出时 SQLite 自己会处理）
	_, _ = d.sql.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return d.sql.Close()
}

// Path 返回库文件路径。
func (d *DB) Path() string { return d.path }

// NS 返回本库所属 namespace。
func (d *DB) NS() string { return d.ns }

// SQL 暴露底层句柄给上层查询。
func (d *DB) SQL() *sql.DB { return d.sql }

// TouchNamespace 登记/触碰本 ns，并做 strict_cwd 检查。
//
// strict_cwd 的意义：ns 的 cwd 与当前 cwd 不符时拒绝，防止模型在 A 项目
// 拿到的锚点去改 B 项目的文件（那会静默写错行）。
//
// ⚠️ **必须用单条 upsert，不能"先 SELECT 再 INSERT"**：
// env.sh 会在后台跑一次 `ns gc`，它与前台的 read/edit 会对同一个 ns
// 同时调用本函数。SELECT-then-INSERT 存在竞态窗口：
// 两边都读到 ErrNoRows，然后都 INSERT，后一个撞
// `UNIQUE constraint failed: namespace.ns`（实测踩过，表现为偶发失败）。
// `INSERT ... ON CONFLICT DO UPDATE` 由 SQLite 保证原子性。
func (d *DB) TouchNamespace(cwd string, strict bool, canonVer, stride int) error {
	now := time.Now().Unix()
	shell := os.Getenv("SHELL")

	// 一次 upsert 完成"不存在则插入、存在则触碰"。
	// cwd 不在冲突时更新——它是身份的一部分，不该被后来者改写。
	if _, err := d.sql.Exec(
		`INSERT INTO namespace (ns, cwd, created_at, last_seen_at, pid, shell, canon_ver, stride)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(ns) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		d.ns, cwd, now, now, os.Getpid(), shell, canonVer, stride); err != nil {
		return fmt.Errorf("register ns %s: %w", d.ns, err)
	}

	// strict_cwd 检查放到写之后：读的是权威值（可能是别人刚写的）。
	var oldCwd string
	err := d.sql.QueryRow(`SELECT cwd FROM namespace WHERE ns = ?`, d.ns).Scan(&oldCwd)
	if err != nil {
		return fmt.Errorf("touch ns %s: %w", d.ns, err)
	}
	if strict && oldCwd != "" && !samePath(oldCwd, cwd) {
		return fmt.Errorf("E_NS_CWD_MISMATCH  ns %s was created in %s, now in %s (set [namespace].strict_cwd = false to allow)",
			d.ns, oldCwd, cwd)
	}
	return nil
}

// Stats 是 doctor 用的库统计。
type Stats struct {
	Paths      int
	ServedRows int
	Retired    int
	Snapshots  int
	Lines      int64
	SizeBytes  int64
}

// Stats 汇总库状态。
func (d *DB) Stats() (Stats, error) {
	var s Stats
	queries := []struct {
		dest *int
		sql  string
	}{
		{&s.Paths, `SELECT COUNT(*) FROM served`},
		{&s.ServedRows, `SELECT COALESCE(SUM(json_array_length(hashes)), 0) FROM served`},
		{&s.Retired, `SELECT COUNT(*) FROM retired`},
		{&s.Snapshots, `SELECT COUNT(*) FROM snapshot`},
	}
	for _, q := range queries {
		if err := d.sql.QueryRow(q.sql).Scan(q.dest); err != nil {
			return s, fmt.Errorf("stats: %w", err)
		}
	}
	if fi, err := os.Stat(d.path); err == nil {
		s.SizeBytes = fi.Size()
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if fi, err := os.Stat(d.path + suffix); err == nil {
			s.SizeBytes += fi.Size()
		}
	}
	return s, nil
}

// GCResult 是一次 GC 的结果。
type GCResult struct {
	ExpiredNS      int
	DeletedServed  int64
	DeletedRetired int64
	DeletedSnaps   int64
	FreedBytes     int64
}

// GC 清理超过 ttl 的数据。ttlHours < 0 表示永久保留（只删超龄快照）。
//
// 单库模式下 ns 就是本库自己，所以「删过期 ns」等价于「整库该不该留」；
// 这里仍按时间清理 served/retired/snapshot，交给上层决定是否删库文件。
func (d *DB) GC(ttlHours, gcIntervalMinutes int, force bool) (GCResult, error) {
	var res GCResult
	now := time.Now().Unix()

	if !force {
		// 惰性：距上次 GC 不足 gc_interval 就跳过，零额外扫描
		var last int64
		err := d.sql.QueryRow(`SELECT value FROM meta WHERE key = 'last_gc'`).Scan(&last)
		if err == nil && now-last < int64(gcIntervalMinutes)*60 {
			return res, nil
		}
	}

	ttl := int64(ttlHours) * 3600
	// ttlHours < 0 = 永久保留：只更新 GC 时间戳，不删任何行。
	if ttlHours >= 0 {
		cut := now - ttl
		r1, err := d.sql.Exec(`DELETE FROM served  WHERE updated_at < ?`, cut)
		if err != nil {
			return res, fmt.Errorf("gc served: %w", err)
		}
		n1, _ := r1.RowsAffected()
		r2, err := d.sql.Exec(`DELETE FROM retired WHERE freed_at  < ?`, cut)
		if err != nil {
			return res, fmt.Errorf("gc retired: %w", err)
		}
		n2, _ := r2.RowsAffected()
		res.DeletedServed, res.DeletedRetired = n1, n2
		if _, err := d.sql.Exec(`DELETE FROM namespace WHERE last_seen_at < ?`, cut); err != nil {
			return res, fmt.Errorf("gc namespace: %w", err)
		}
		// 快照比 ns 多留一份：同一 ns 内多次 read 可复用
		r3, err := d.sql.Exec(`DELETE FROM snapshot WHERE created_at < ?`, cut)
		if err != nil {
			return res, fmt.Errorf("gc snapshot: %w", err)
		}
		n3, _ := r3.RowsAffected()
		res.DeletedSnaps = n3
	}

	if _, err := d.sql.Exec(
		`INSERT INTO meta (key, value) VALUES ('last_gc', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, now); err != nil {
		return res, fmt.Errorf("gc stamp: %w", err)
	}
	return res, nil
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ca, err1 := filepath.Abs(a)
	cb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return filepath.Clean(ca) == filepath.Clean(cb)
}
