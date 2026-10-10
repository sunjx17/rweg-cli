package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"rweg/internal/config"
)

func testCfg() config.DB {
	c := config.Default()
	return c.DB
}

func TestOpenCreatesDirAndSchema(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", ".db", "ns1.db")

	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := os.Stat(p); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
	for _, tbl := range []string{"meta", "namespace", "snapshot", "served", "retired", "undo"} {
		var n int
		if err := db.SQL().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
}

// 重复打开必须幂等（每个 CLI 调用都是独立进程）。
func TestOpenIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	for i := 0; i < 3; i++ {
		db, err := Open(p, "ns1", testCfg())
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		db.Close()
	}
}

func TestTouchNamespaceRegisters(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cwd := `D:\nginx\2dsim`
	if err := db.TouchNamespace(cwd, true, 2, 3907); err != nil {
		t.Fatal(err)
	}
	var gotCwd string
	var gotPID int
	if err := db.SQL().QueryRow(`SELECT cwd, pid FROM namespace WHERE ns = ?`, "ns1").
		Scan(&gotCwd, &gotPID); err != nil {
		t.Fatal(err)
	}
	if gotCwd != cwd {
		t.Errorf("cwd = %q, want %q", gotCwd, cwd)
	}
	if gotPID != os.Getpid() {
		t.Errorf("pid = %d, want %d", gotPID, os.Getpid())
	}

	// 再触碰只更新 last_seen_at，不报错
	if err := db.TouchNamespace(cwd, true, 2, 3907); err != nil {
		t.Fatalf("second touch: %v", err)
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM namespace`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("namespace rows = %d, want 1", n)
	}
}

// strict_cwd：ns 记录的 cwd 与当前不符 → 拒绝（防跨项目串锚点）。
func TestStrictCwdRejectsMismatch(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.TouchNamespace(`D:\a`, true, 2, 3907); err != nil {
		t.Fatal(err)
	}
	err = db.TouchNamespace(`D:\b`, true, 2, 3907)
	if err == nil {
		t.Fatal("expected E_NS_CWD_MISMATCH")
	}
	if got := err.Error(); !contains(got, "E_NS_CWD_MISMATCH") {
		t.Errorf("err = %q", got)
	}
	// 关掉 strict 就该放行
	if err := db.TouchNamespace(`D:\b`, false, 2, 3907); err != nil {
		t.Errorf("strict=false should allow: %v", err)
	}
}

func TestStats(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.SQL().Exec(
		`INSERT INTO served (path, hashes, line_count, updated_at) VALUES ('a.ts', '[null,"CTt","Um7"]', 3, ?)`,
		time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(
		`INSERT INTO retired (path, hash, freed_at) VALUES ('a.ts', 'ZZ0', ?)`,
		time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	s, err := db.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if s.Paths != 1 {
		t.Errorf("Paths = %d, want 1", s.Paths)
	}
	if s.ServedRows != 3 {
		t.Errorf("ServedRows = %d, want 3", s.ServedRows)
	}
	if s.Retired != 1 {
		t.Errorf("Retired = %d, want 1", s.Retired)
	}
	if s.SizeBytes <= 0 {
		t.Error("SizeBytes should be > 0")
	}
}

func TestGCLazyInterval(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	old := time.Now().Add(-48 * time.Hour).Unix()
	insert := func(path string) {
		t.Helper()
		if _, err := db.SQL().Exec(
			`INSERT INTO served (path, hashes, line_count, updated_at) VALUES (?, '["CTt"]', 1, ?)`,
			path, old); err != nil {
			t.Fatal(err)
		}
	}

	// 第一次跑：还没有 last_gc 戳，应该真删
	insert("old1.ts")
	res, err := db.GC(24, 60, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedServed != 1 {
		t.Fatalf("first GC deleted %d, want 1", res.DeletedServed)
	}

	// 第二次：距上次 GC 不足 gc_interval，应被跳过（哪怕有新超龄数据）
	insert("old2.ts")
	res, err = db.GC(24, 60, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedServed != 0 {
		t.Errorf("second GC should be skipped by interval, deleted %d", res.DeletedServed)
	}
	var n int
	db.SQL().QueryRow(`SELECT COUNT(*) FROM served`).Scan(&n)
	if n != 1 {
		t.Errorf("served rows = %d, want 1 (skipped GC must not delete)", n)
	}

	// force 必须无视间隔清掉
	res, err = db.GC(24, 60, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedServed != 1 {
		t.Errorf("forced GC deleted %d served rows, want 1", res.DeletedServed)
	}
	db.SQL().QueryRow(`SELECT COUNT(*) FROM served`).Scan(&n)
	if n != 0 {
		t.Errorf("served rows left = %d", n)
	}
}

// force 也该无视 gc_interval。
func TestGCForceIgnoresInterval(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 先盖一个「刚跑过 GC」的戳
	if _, err := db.SQL().Exec(
		`INSERT INTO meta (key, value) VALUES ('last_gc', ?)`,
		time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).Unix()
	db.SQL().Exec(
		`INSERT INTO served (path, hashes, line_count, updated_at) VALUES ('old.ts','["CTt"]',1,?)`, old)

	res, err := db.GC(24, 60, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedServed != 1 {
		t.Errorf("force GC deleted %d, want 1", res.DeletedServed)
	}
}

// ttl_hours = -1 → 永久保留，served 与 snapshot 都不删。
func TestGCTTLMinusOneKeepsEverything(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	old := time.Now().Add(-1000 * time.Hour).Unix()
	db.SQL().Exec(`INSERT INTO served (path, hashes, line_count, updated_at) VALUES ('x.ts','["CTt"]',1,?)`, old)
	db.SQL().Exec(`INSERT INTO snapshot (path,checksum,line_count,hashes,canon_ver,created_at)
	               VALUES ('x.ts','abc',1,'["CTt"]',2,?)`, old)
	db.SQL().Exec(`INSERT INTO retired (path, hash, freed_at) VALUES ('x.ts','ZZ0',?)`, old)

	res, err := db.GC(-1, 60, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.DeletedServed != 0 || res.DeletedRetired != 0 || res.DeletedSnaps != 0 {
		t.Errorf("ttl=-1 must delete nothing, got %+v", res)
	}
	for _, tbl := range []string{"served", "snapshot", "retired"} {
		var n int
		if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s rows = %d, want 1 (ttl=-1 must keep)", tbl, n)
		}
	}
}

func TestJournalModeFallback(t *testing.T) {
	for in, want := range map[string]string{
		"":        "WAL",
		"WAL":     "WAL",
		"DELETE":  "DELETE",
		"garbage": "WAL",
	} {
		if got := journalMode(in); got != want {
			t.Errorf("journalMode(%q) = %q, want %q", in, got, want)
		}
	}
}

// 并发打开同一库文件不应报错（WAL + busy_timeout 的意义）。
func TestConcurrentOpen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	dbs := make([]*DB, 0, 4)
	for i := 0; i < 4; i++ {
		db, err := Open(p, "ns1", testCfg())
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		dbs = append(dbs, db)
	}
	for _, db := range dbs {
		db.Close()
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 并发 TouchNamespace 不得报 UNIQUE constraint。
//
// env.sh 会在后台跑 `ns gc`，它与前台的 read/edit 对同一个 ns
// 并发调用 TouchNamespace。曾经的 SELECT-then-INSERT 实现存在竞态窗口，
// 两个进程都读到 ErrNoRows 然后都 INSERT → 后者撞 UNIQUE 约束。
// （实测表现为 shell-test.sh 偶发失败，很难定位。）
func TestTouchNamespaceConcurrent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const workers = 8
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			errs <- db.TouchNamespace(`D:\proj`, true, 2, 3907)
		}()
	}
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent TouchNamespace failed: %v", err)
		}
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM namespace WHERE ns = 'ns1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("namespace rows = %d, want 1", n)
	}
}

// 反复 TouchNamespace 应幂等，且不覆盖已记录的 cwd。
func TestTouchNamespaceIdempotentKeepsCwd(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.TouchNamespace(`D:\orig`, true, 2, 3907); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := db.TouchNamespace(`D:\orig`, true, 2, 3907); err != nil {
			t.Fatal(err)
		}
	}
	var got string
	db.SQL().QueryRow(`SELECT cwd FROM namespace WHERE ns='ns1'`).Scan(&got)
	if got != `D:\orig` {
		t.Errorf("cwd = %q, want unchanged", got)
	}
}
