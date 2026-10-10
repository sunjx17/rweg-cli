package store

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
)

func undoDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "ns.db"), "ns", testCfg())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestUndoPushPeekDrop(t *testing.T) {
	db := undoDB(t)

	if e, err := db.PeekUndo(""); err != nil || e != nil {
		t.Fatalf("empty stack: entry=%v err=%v", e, err)
	}

	if err := db.PushUndo("/p/a.ts", true, false, "edit", []byte("old\n")); err != nil {
		t.Fatal(err)
	}
	e, err := db.PeekUndo("/p/a.ts")
	if err != nil || e == nil {
		t.Fatalf("peek: %v %v", e, err)
	}
	if !e.Existed || e.TooBig || e.Kind != "edit" || !bytes.Equal(e.Content, []byte("old\n")) {
		t.Fatalf("entry mismatch: %+v", e)
	}
	if e.Path != NormalizePath("/p/a.ts") {
		t.Errorf("path not normalized: %q", e.Path)
	}

	// 另一条路径的改动：按路径查仍是各查各的，"最新一条"跟着变
	if err := db.PushUndo("/p/b.ts", false, true, "write", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.PeekUndo("/p/a.ts"); got == nil || got.Kind != "edit" {
		t.Errorf("per-path peek should still see a.ts: %+v", got)
	}
	latest, _ := db.PeekUndo("")
	if latest == nil || latest.Kind != "write" || !latest.TooBig || latest.Existed {
		t.Fatalf("latest peek: %+v", latest)
	}

	if err := db.DropUndo(latest.Seq); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.PeekUndo(""); got == nil || got.Kind != "edit" {
		t.Fatalf("after drop latest should fall back to a.ts: %+v", got)
	}
	// 查不区分大小写（Windows 路径）
	if got, _ := db.PeekUndo(`\P\A.TS`); got == nil {
		t.Error("peek should be case-insensitive")
	}
}

func TestUndoStackIsBounded(t *testing.T) {
	db := undoDB(t)
	for i := 0; i < undoMaxEntries+5; i++ {
		if err := db.PushUndo(fmt.Sprintf("/p/f%d.ts", i), true, false, "edit",
			[]byte(fmt.Sprintf("v%d\n", i))); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM undo`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != undoMaxEntries {
		t.Fatalf("stack size = %d, want %d", n, undoMaxEntries)
	}
	// 留下的必须是最新的那批：最新一条是最后压进去的
	e, _ := db.PeekUndo("")
	if e == nil || !bytes.Equal(e.Content, []byte(fmt.Sprintf("v%d\n", undoMaxEntries+4))) {
		t.Fatalf("newest entry lost: %+v", e)
	}
}
