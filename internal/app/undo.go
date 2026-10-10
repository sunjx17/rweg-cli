package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"rweg/internal/config"
	"rweg/internal/docfile"
	"rweg/internal/hashline"
	"rweg/internal/store"
)

// undoStackDepth 是库里保留的撤销层数，与 store 内部的栈深一致，
// 只用于提示文案。
const undoStackDepth = 16

// runUndo 把某个路径退回上一次 write / edit 之前的状态。
//
// 只认本 namespace 里 rweg 自己写过的改动：前像存在库里（undo 表），
// 恢复的是**原始字节**，所以编码、BOM、行尾风格一并回来。
// 恢复之后这个路径的 served / retired / snapshot 全部按内容重建 ——
// 内容回到旧样子，旧内容的锚点也就重新有效（锚点是内容哈希，不看历史）。
func runUndo(c *Ctx, args []string) error {
	if c.JSON {
		return UsageErr("undo: --json is not supported").
			WithHint("undo 只回一行结果；要看内容用 read")
	}
	w, err := parseUndoArgs(args)
	if err != nil {
		return err
	}

	entry, err := c.DB.PeekUndo(w.file)
	if err != nil {
		return &Error{Code: ExitInternal, Message: "undo: " + err.Error()}
	}
	if entry == nil {
		if w.file == "" {
			return BizErr("E_NO_UNDO  no recorded write/edit to undo").
				WithHint(fmt.Sprintf("undo 只回退本 namespace 里 rweg 自己写过的最近 %d 次改动；更早的历史交给版本控制", undoStackDepth))
		}
		return BizErr("E_NO_UNDO  no recorded write/edit to undo for %s", displayPath(w.file)).
			WithHint(fmt.Sprintf("undo 只回退本 namespace 里 rweg 自己写过的最近 %d 次改动；更早的历史交给版本控制", undoStackDepth))
	}
	if entry.TooBig {
		return BizErr("E_UNDO_TOO_BIG  %s was larger than %d MiB, so no copy was kept",
			displayPath(entry.Path), store.UndoMaxBytes>>20).
			WithHint("撤销副本只为改错了退一步；大文件请交给版本控制")
	}

	target := entry.Path
	if entry.Existed {
		if err := writeFileAtomic(target, entry.Content); err != nil {
			return BizErr("E_WRITE  %s: %s", displayPath(target), err.Error())
		}
	} else if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		// 改动之前这个文件不存在：撤销就是把它删掉。
		return BizErr("E_DELETE  %s: %s", displayPath(target), err.Error())
	}

	// 旧痕迹全清：served / retired / snapshot 一律按恢复后的内容重建。
	if err := c.DB.ForgetPath(target); err != nil {
		return &Error{Code: ExitInternal, Message: "undo: " + err.Error()}
	}

	lines := 0
	if entry.Existed {
		doc, err := docfile.Load(target, docfile.Options{
			MaxLines:     c.Res.Cfg.Read.MaxLines,
			MaxLineBytes: c.Res.Cfg.Read.MaxLineBytes,
		})
		if err != nil {
			// 字节已经恢复了，只是没能重新 serve：说清楚，别让人以为没恢复。
			derr := translateDocError(err, target)
			if e, ok := derr.(*Error); ok {
				return e.WithHint("文件已恢复，重新 serve 失败；read 一次即可继续编辑")
			}
			return derr
		}
		lines = len(doc.Lines)
		stride := c.Res.Cfg.Hash.ProbeStride
		canonVer := c.Res.Cfg.Hash.CanonVersion
		// 传空的 retired：恢复的是旧内容，旧内容该拿回它原本的锚点。
		anchors := hashline.Pure(doc.Lines, stride, map[string]bool{})
		canons := make([]string, len(doc.Lines))
		for i, ln := range doc.Lines {
			canons[i] = hashline.Canon(ln)
		}
		if err := c.DB.PutServed(target, anchors, canons, canonVer, stride); err != nil {
			return &Error{Code: ExitInternal, Message: "undo: persist served: " + err.Error()}
		}
		fp := store.ContentFingerprint(doc.Lines)
		if err := c.DB.PutSnapshotByFingerprint(target, anchors, fp, canonVer); err != nil {
			return &Error{Code: ExitInternal, Message: "undo: cache snapshot: " + err.Error()}
		}
	}

	if err := c.DB.DropUndo(entry.Seq); err != nil {
		return &Error{Code: ExitInternal, Message: "undo: " + err.Error()}
	}

	if entry.Existed {
		fmt.Printf("undone %s  %s (%d lines restored)\n", entry.Kind, displayPath(target), lines)
	} else {
		fmt.Printf("undone %s  %s (file removed; it did not exist before)\n", entry.Kind, displayPath(target))
	}
	return nil
}

// undoWants 是解析后的 undo 参数。
type undoWants struct {
	// file 是要撤销的路径；空 = 最新一条，不限路径。
	file string
}

// parseUndoArgs 解析 undo 参数。
//
// undo [FILE]
// undo -f FILE
func parseUndoArgs(args []string) (*undoWants, error) {
	w := &undoWants{}
	if f, rest, ok := fileShorthand(args); ok {
		w.file, args = f, rest
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "-f" || s == "--file":
			if i+1 >= len(args) {
				return nil, UsageErr("-f requires a path").WithHint("undo [FILE]")
			}
			i++
			w.file = args[i]
		case strings.HasPrefix(s, "--file="):
			w.file = s[len("--file="):]
		default:
			return nil, UsageErr("unexpected argument %q", s).WithHint("undo [FILE]")
		}
	}
	if w.file != "" {
		abs, err := filepath.Abs(config.ResolvePath(w.file))
		if err != nil {
			return nil, &Error{Code: ExitUsage, Message: "resolve " + w.file + ": " + err.Error()}
		}
		w.file = abs
	}
	return w, nil
}
