package store

import (
	"path/filepath"
	"runtime"
	"testing"

	"rweg/internal/hashline"
	"rweg/internal/ranges"
)

func openDB(t *testing.T) *DB {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ns1.db")
	db, err := Open(p, "ns1", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.TouchNamespace(t.TempDir(), false, 2, 3907); err != nil {
		t.Fatal(err)
	}
	return db
}

// demoAnchors 是 6 行示例文件的锚点（与参考实现一致）。
var demoAnchors = []string{"CTt", "Um7", "AU6", "nVR", "yAZ", "6PY"}
var demoCanons = []string{
	"functionhello(){",
	`console.log("world");`,
	"}",
	"constx=1;",
	"lety=2;",
	"exportdefaulthello;",
}

func serveSet(n int, from, to int) *ranges.Set {
	s := ranges.NewSet(n)
	s.AddRange(from, to)
	return s
}

func TestGetServedNotFound(t *testing.T) {
	db := openDB(t)
	got, err := db.GetServed("nope.ts")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestServeLinesBasic(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")

	err := db.ServeLines(p, serveSet(6, 1, 6), demoAnchors, demoCanons, 2, 3907)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetServed(p)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("served record should exist")
	}
	if got.LineCount != 6 {
		t.Errorf("LineCount = %d, want 6", got.LineCount)
	}
	for i, w := range demoAnchors {
		if got.Hashes[i] != w {
			t.Errorf("hash[%d] = %q, want %q", i, got.Hashes[i], w)
		}
		if got.Has(w) == false {
			t.Errorf("Has(%q) = false", w)
		}
	}
	if got.CanonVer != 2 || got.Stride != 3907 {
		t.Errorf("canonVer/stride = %d/%d, want 2/3907", got.CanonVer, got.Stride)
	}
}

// 场景 G：多范围读 → 范围内全部 serve，范围外的行保持未 serve。
func TestServeLinesMultipleRanges(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")

	set := ranges.NewSet(7)
	set.AddRange(1, 3)
	set.AddRange(5, 7)
	if err := db.ServeLines(p, set, demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}

	got, _ := db.GetServed(p)
	// 第 4 行（index 3）未 serve → 空串
	if got.Hashes[3] != "" {
		t.Errorf("line 4 should NOT be served, got %q", got.Hashes[3])
	}
	for _, i := range []int{0, 1, 2, 4, 5} {
		if got.Hashes[i] == "" {
			t.Errorf("line %d should be served", i+1)
		}
	}
	// 未 serve 的行不应被 Has 命中
	if got.Has("ZZZ") {
		t.Error("bogus anchor should not be found")
	}
}

// 二次 read 同一文件（不同范围）→ 合并，两次的行都 serve。
func TestServeLinesMergesWithPrevious(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")

	// 第一次只读 1:3
	if err := db.ServeLines(p, serveSet(6, 1, 3), demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetServed(p)
	if got.Hashes[3] != "" {
		t.Error("line 4 should not be served after 1:3")
	}

	// 第二次只读 4:6 → 应与第一次合并
	if err := db.ServeLines(p, serveSet(6, 4, 6), demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}
	got, _ = db.GetServed(p)
	for i, w := range demoAnchors {
		if got.Hashes[i] != w {
			t.Errorf("after merge hash[%d] = %q, want %q", i, got.Hashes[i], w)
		}
	}
	if got.LineCount != 6 {
		t.Errorf("LineCount = %d, want 6", got.LineCount)
	}
}

// 文件被外部改过（行数变了）→ 旧 served 失效，从头开始。
func TestServeLinesResetsOnLineCountChange(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")

	if err := db.ServeLines(p, serveSet(6, 1, 6), demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}
	// 文件变成 3 行
	short := demoAnchors[:3]
	if err := db.ServeLines(p, serveSet(3, 1, 3), short, demoCanons[:3], 2, 3907); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetServed(p)
	if got.LineCount != 3 {
		t.Errorf("LineCount = %d, want 3", got.LineCount)
	}
	if len(got.Hashes) != 3 {
		t.Errorf("len(Hashes) = %d, want 3", len(got.Hashes))
	}
}

func TestServeLinesNoopOnEmptySet(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")
	if err := db.ServeLines(p, ranges.NewSet(6), demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetServed(p)
	if got != nil {
		t.Errorf("empty set should not create a record, got %+v", got)
	}
}

// --- IndexOf：重复锚点必须能被发现（§7.1 exact-boundary）---

func TestIndexOf(t *testing.T) {
	s := &Served{Hashes: []string{"CTt", "Um7", "CTt", "yAZ"}}
	if got, ok := s.IndexOf("Um7"); !ok || got != 2 {
		t.Errorf("IndexOf(Um7) = %d,%v want 2,true", got, ok)
	}
	if got, ok := s.IndexOf("CTt"); !ok || got != 1 {
		t.Errorf("IndexOf(CTt) = %d,%v want 1,true (first occurrence)", got, ok)
	}
	if _, ok := s.IndexOf("ZZZ"); ok {
		t.Error("unknown anchor should not be found")
	}
	// 重复出现必须被 IndexSet 暴露出来——上层据此报错而不是"选一个"
	got := s.IndexSet("CTt")
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("IndexSet(CTt) = %v, want [1 3]", got)
	}
}

func TestCanonOf(t *testing.T) {
	s := &Served{Hashes: demoAnchors, Canons: demoCanons}
	if c, ok := s.CanonOf(1); !ok || c != demoCanons[0] {
		t.Errorf("CanonOf(1) = %q,%v", c, ok)
	}
	if _, ok := s.CanonOf(0); ok {
		t.Error("line 0 should be out of bounds")
	}
	if _, ok := s.CanonOf(99); ok {
		t.Error("line 99 should be out of bounds")
	}
}

// --- snapshot 缓存 ---

func TestSnapshotRoundTrip(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")

	cs := ContentFingerprint(demoCanons)
	got, ok, err := db.GetSnapshot(p, cs, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("cold cache should miss, got %v", got)
	}

	if err := db.PutSnapshotByFingerprint(p, demoAnchors, cs, 2); err != nil {
		t.Fatal(err)
	}
	got, ok, err = db.GetSnapshot(p, cs, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("warm cache should hit")
	}
	if len(got) != len(demoAnchors) {
		t.Fatalf("len = %d, want %d", len(got), len(demoAnchors))
	}
	for i := range demoAnchors {
		if got[i] != demoAnchors[i] {
			t.Errorf("snapshot[%d] = %q, want %q", i, got[i], demoAnchors[i])
		}
	}
}

// 同一份内容反复 read 不得在 snapshot 表里堆出多行。
// （曾经的真bug：ServeLines 用锚点派生指纹、read 用内容指纹，同内容两行。）
func TestSnapshotNoDuplicateRowsForSameContent(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")
	fp := ContentFingerprint(demoCanons)

	// 模拟连续三次 read
	for i := 0; i < 3; i++ {
		if _, ok, _ := db.GetSnapshot(p, fp, 2); !ok {
			if err := db.PutSnapshotByFingerprint(p, demoAnchors, fp, 2); err != nil {
				t.Fatal(err)
			}
		}
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM snapshot WHERE path = ?`,
		NormalizePath(p)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("snapshot rows = %d, want 1", n)
	}

	// ServeLines 之后也不该多出第二行（它不再自己写 snapshot）
	set := serveSet(6, 1, 3)
	if err := db.ServeLines(p, set, demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM snapshot WHERE path = ?`,
		NormalizePath(p)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("snapshot rows after ServeLines = %d, want 1", n)
	}
}

// 内容变了 → checksum 变 → 缓存自动失效。
func TestSnapshotCacheInvalidatesOnContentChange(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")
	if err := db.PutSnapshotByFingerprint(p, demoAnchors, ContentFingerprint(demoCanons), 2); err != nil {
		t.Fatal(err)
	}
	// 内容变了→ 指纹变了 → 必然 miss
	changed := append([]string{}, demoCanons...)
	changed[3] = "constx=42;"
	if _, ok, _ := db.GetSnapshot(p, ContentFingerprint(changed), 2); ok {
		t.Error("changed content must miss the cache")
	}
	// canon_ver 变了也 miss
	if _, ok, _ := db.GetSnapshot(p, ContentFingerprint(demoCanons), 99); ok {
		t.Error("different canon_ver must miss the cache")
	}
}

func TestFingerprintStable(t *testing.T) {
	if ContentFingerprint(demoCanons) != ContentFingerprint(demoCanons) {
		t.Error("fingerprint must be deterministic")
	}
	if ContentFingerprint(demoCanons) == ContentFingerprint(demoCanons[:5]) {
		t.Error("different lengths must give different fingerprints")
	}
	a := append([]string{}, demoCanons...)
	b := append([]string{}, demoCanons...)
	b[0] = "somethingelse"
	if ContentFingerprint(a) == ContentFingerprint(b) {
		t.Error("different content must give different fingerprints")
	}
	if len(ContentFingerprint(a)) != 16 {
		t.Errorf("fingerprint length = %d, want 16", len(ContentFingerprint(a)))
	}
	// 行边界必须被感知：["a","b"] 与 ["ab"] 指纹不同
	if ContentFingerprint([]string{"a", "b"}) == ContentFingerprint([]string{"ab"}) {
		t.Error("line boundaries must affect the fingerprint")
	}
}

// --- retired ---

func TestRetireAndRead(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")

	got, err := db.RetiredFor(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("new path should have no retired anchors, got %v", got)
	}

	if err := db.Retire(p, []string{"nVR", "yAZ"}); err != nil {
		t.Fatal(err)
	}
	got, _ = db.RetiredFor(p)
	if !got["nVR"] || !got["yAZ"] {
		t.Errorf("retired = %v, want nVR+yAZ", got)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestRetireDeduplicates(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")
	// 含空串与重复
	if err := db.Retire(p, []string{"nVR", "nVR", "", "yAZ", "nVR"}); err != nil {
		t.Fatal(err)
	}
	got, _ := db.RetiredFor(p)
	if len(got) != 2 {
		t.Errorf("retired = %v, want 2 unique", got)
	}
}

func TestRetireIdempotent(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")
	for i := 0; i < 3; i++ {
		if err := db.Retire(p, []string{"nVR"}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := db.RetiredFor(p)
	if len(got) != 1 {
		t.Errorf("retired = %v, want 1", got)
	}
}

func TestRetireNoop(t *testing.T) {
	db := openDB(t)
	if err := db.Retire("x.ts", nil); err != nil {
		t.Fatal(err)
	}
	got, _ := db.RetiredFor("x.ts")
	if len(got) != 0 {
		t.Error("empty retire should be noop")
	}
}

// --- ForgetPath ---

func TestForgetPath(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")

	if err := db.ServeLines(p, serveSet(6, 1, 6), demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}
	if err := db.Retire(p, []string{"nVR"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ForgetPath(p); err != nil {
		t.Fatal(err)
	}

	got, _ := db.GetServed(p)
	if got != nil {
		t.Error("served should be gone")
	}
	ret, _ := db.RetiredFor(p)
	if len(ret) != 0 {
		t.Error("retired should be gone")
	}
	if _, ok, _ := db.GetSnapshot(p, ContentFingerprint(demoCanons), 2); ok {
		t.Error("snapshot should be gone")
	}
}

func TestServedPaths(t *testing.T) {
	db := openDB(t)
	dir := t.TempDir()
	if got, _ := db.ServedPaths(); len(got) != 0 {
		t.Errorf("new db should have no paths, got %v", got)
	}
	for _, name := range []string{"b.ts", "a.ts"} {
		p := filepath.Join(dir, name)
		if err := db.ServeLines(p, serveSet(6, 1, 1), demoAnchors, demoCanons, 2, 3907); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.ServedPaths()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2", got)
	}
	// 应按 path 排序
	if got[0] > got[1] {
		t.Errorf("not sorted: %v", got)
	}
}

// --- NormalizePath ---

func TestNormalizePath(t *testing.T) {
	a := NormalizePath(`D:\proj\a.ts`)
	b := NormalizePath(`d:/PROJ/a.ts`)
	c := NormalizePath(`D:\proj\.\sub\..\a.ts`)
	if a != b {
		t.Errorf("case/slash variants must normalize equal:\n  %q\n  %q", a, b)
	}
	if a != c {
		t.Errorf("path with . and .. must normalize equal:\n  %q\n  %q", a, c)
	}
	if runtime.GOOS != "windows" {
		t.Log("skipping case-folding assertions on non-Windows")
	}
}

func TestNormalizePathEnablesLookup(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")
	if err := db.ServeLines(p, serveSet(6, 1, 6), demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}
	// 用不同大小写 + 混合斜杠查，必须命中
	variants := []string{
		p,
		filepath.Clean(p + "/"),
		filepath.Dir(p) + "/" + filepath.Base(p),
	}
	if runtime.GOOS == "windows" {
		variants = append(variants, swapCase(p))
	}
	for _, v := range variants {
		got, err := db.GetServed(v)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Errorf("variant %q did not match stored path", v)
		}
	}
}

func swapCase(p string) string {
	b := []byte(p)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 32
		} else if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

// ServeLines 写出的记录能被 GetServed 原样读回（含 canon）。
func TestServeThenGetCanonRoundTrip(t *testing.T) {
	db := openDB(t)
	p := filepath.Join(t.TempDir(), "app.ts")
	set := serveSet(6, 2, 4)
	if err := db.ServeLines(p, set, demoAnchors, demoCanons, 2, 3907); err != nil {
		t.Fatal(err)
	}
	got, _ := db.GetServed(p)
	for line := 2; line <= 4; line++ {
		c, ok := got.CanonOf(line)
		if !ok {
			t.Fatalf("CanonOf(%d) missing", line)
		}
		if c != demoCanons[line-1] {
			t.Errorf("CanonOf(%d) = %q, want %q", line, c, demoCanons[line-1])
		}
	}
	// 未 serve 的行 canon 也有值（写入时一并存了），但 hash 为空
	if got.Hashes[0] != "" {
		t.Error("line 1 should not be served")
	}
}

// 锚点数组与 hashline 常量一致（防止上层传错数组长度）。
func TestDemoAnchorsMatchReference(t *testing.T) {
	got := hashline.Pure([]string{
		"function hello() {",
		`  console.log("world");`,
		"}",
		"const x = 1;",
		"let y = 2;",
		"export default hello;",
	}, hashline.DefaultProbeStride, nil)
	for i := range demoAnchors {
		if got[i] != demoAnchors[i] {
			t.Errorf("anchor[%d] = %q, want %q", i, got[i], demoAnchors[i])
		}
	}
}
