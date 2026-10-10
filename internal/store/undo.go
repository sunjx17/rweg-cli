package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// undoMaxEntries 是撤销栈的深度：保留最近这么多次改动的前像。
//
// 撤销栈不是备份系统，只用来把"刚刚这一步改错"退回去；再往前的历史靠
// 版本控制。上限同时约束了库体积（最坏 undoMaxEntries × 单条上限）。
const undoMaxEntries = 16

// UndoMaxBytes 是单条撤销记录保存的原始字节上限。
//
// 超过就不留副本（记 too_big），撤销时明确报出来 —— 比静默不留、到撤销时
// 才说"没有记录"要诚实。
const UndoMaxBytes = 4 << 20

// UndoEntry 是一次改动的"前像"：改之前这个文件在磁盘上是什么样。
type UndoEntry struct {
	// Seq 是栈内序号，越大越新。撤销一条后按它删除。
	Seq int64
	// Path 是数据库键形态的路径。
	Path string
	// Existed 为假表示改动之前这个文件根本不存在（撤销 = 删掉它）。
	Existed bool
	// TooBig 为真表示前像超过 UndoMaxBytes，没留副本。
	TooBig bool
	// Kind 是产生这条记录的动作用的名字：write / edit。
	Kind string
	// Content 是改动前的原始字节（编码、BOM、行尾原样）。
	Content []byte
	// CreatedAt 是记录时间（Unix 秒）。
	CreatedAt int64
}

// PushUndo 记下一条前像，并把栈裁剪到 undoMaxEntries 条。
//
// content 传 nil 且 tooBig 为真时表示"这次改动太大，没留副本"。
func (d *DB) PushUndo(path string, existed, tooBig bool, kind string, content []byte) error {
	if _, err := d.sql.Exec(
		`INSERT INTO undo (path, existed, too_big, kind, content, created_at)
 VALUES (?, ?, ?, ?, ?, ?)`,
		NormalizePath(path), boolToInt(existed), boolToInt(tooBig), kind, content, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("push undo %s: %w", path, err)
	}
	if _, err := d.sql.Exec(
		`DELETE FROM undo WHERE seq NOT IN (SELECT seq FROM undo ORDER BY seq DESC LIMIT ?)`,
		undoMaxEntries,
	); err != nil {
		return fmt.Errorf("prune undo: %w", err)
	}
	return nil
}

// PeekUndo 取最近一条前像；path 为空表示"最新的一条，不限路径"。
//
// 只取不删：撤销动作可能失败（比如文件被别的进程占着），失败时记录得留着。
func (d *DB) PeekUndo(path string) (*UndoEntry, error) {
	q := `SELECT seq, path, existed, too_big, kind, content, created_at
      FROM undo ORDER BY seq DESC LIMIT 1`
	args := []any{}
	if path != "" {
		q = `SELECT seq, path, existed, too_big, kind, content, created_at
     FROM undo WHERE path = ? ORDER BY seq DESC LIMIT 1`
		args = append(args, NormalizePath(path))
	}
	var (
		e       UndoEntry
		existed int
		tooBig  int
	)
	err := d.sql.QueryRow(q, args...).Scan(
		&e.Seq, &e.Path, &existed, &tooBig, &e.Kind, &e.Content, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("peek undo: %w", err)
	}
	e.Existed, e.TooBig = existed != 0, tooBig != 0
	return &e, nil
}

// DropUndo 删掉一条前像（撤销成功后调用）。
func (d *DB) DropUndo(seq int64) error {
	if _, err := d.sql.Exec(`DELETE FROM undo WHERE seq = ?`, seq); err != nil {
		return fmt.Errorf("drop undo %d: %w", seq, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
