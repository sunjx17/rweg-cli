package app

import (
	"fmt"
	"os"
	"path/filepath"

	"rweg/internal/store"
)

// writeFileAtomic 原子替换文件内容：先写同目录下的临时文件，再 rename 覆盖。
//
// 为什么需要：`os.WriteFile` 在中途失败（磁盘满、被 kill）会留下半截文件，
// 而 edit / write 的语义是"要么全对，要么完全不动"。
// rename 在同一卷上是原子的（Windows 走 MoveFileEx + REPLACE_EXISTING）。
//
// 保留原文件权限位：新建时用 0644，覆盖时沿用旧文件的 mode。
// 临时文件刻意用 `.` 前缀 + 目标名：出事后一眼看出是谁的残留，
// 且 rg / git 默认都会忽略点文件。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".rweg-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	// 先落盘再改权限/改名：崩溃时最多留下临时文件，不会动到目标。
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}

	firstErr := os.Rename(tmpName, path)
	if firstErr == nil {
		return nil
	}
	// Windows 上目标被独占打开时 rename 会失败。删掉目标再试一次——
	// 此时原子性已经破了，但"能写进去"比"绝对原子"更重要。
	if err := os.Remove(path); err == nil {
		if err := os.Rename(tmpName, path); err == nil {
			return nil
		}
	}
	cleanup()
	return fmt.Errorf("replace %s: %w", filepath.Base(path), firstErr)
}

// preImage 读取某路径**改动前**的原始字节，供 undo 用。
//
// 直接读磁盘而不是拿 doc 重新渲染：撤销要恢复的是"当时文件里到底是什么"，
// 不该假设渲染一定逐字节还原（截断读、超长行都会让这个假设不成立）。
// 超过 store.UndoMaxBytes 就不读，返回 tooBig 让调用方记一条"没副本"的记录。
func preImage(path string, existed bool) (content []byte, tooBig bool, err error) {
	if !existed {
		return nil, false, nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, false, err
	}
	if fi.Size() > store.UndoMaxBytes {
		return nil, true, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return raw, false, nil
}

// recordUndo 记一条撤销前像；失败只当警告，不阻断已经成功的写入。
func recordUndo(c *Ctx, path string, existed bool, kind string, content []byte, tooBig bool) {
	if c == nil || c.DB == nil {
		return
	}
	if err := c.DB.PushUndo(path, existed, tooBig, kind, content); err != nil {
		fmt.Fprintf(os.Stderr, "warn: undo record for %s: %v\n", displayPath(path), err)
	}
}
