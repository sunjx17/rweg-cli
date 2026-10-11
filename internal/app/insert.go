package app

import (
	"strings"

	"rweg/internal/config"
	"rweg/internal/docfile"
	"rweg/internal/edit"
)

// insertArgs 是 append / prepend 的已解析参数。
type insertArgs struct {
	file    string
	anchor  string
	content []string // 要插入的行；空内容 = 一个空行
}

// parseInsertArgs 解析 append / prepend 的参数（方向由工具名决定）。
//
//	append FILE ANCHOR CONTENT
//	append -f FILE ANCHOR CONTENT
//	append FILE -c ANCHOR CONTENT
//
// FILE 必填：放在最前面时可以省掉 -f（与 read / write / edit 一致）。ANCHOR 是单个
// 3 字符裸锚，CONTENT 紧跟其后；分行用**真换行**（$'a\nb'、here-doc、命令替换），
// 与 edit 的替换文本同源。`-c` 形态给内容恰好是 rweg 认得的 flag
// （-v / -q / -h / -f / -c / --json）时用 —— 那种内容会被通用 flag 解析摘走，
// 只有 `-c ANCHOR CONTENT` 能原样递进来（与 edit 的 `-c` 同理）。
func parseInsertArgs(tool Tool, args []string) (*insertArgs, error) {
	name := string(tool)
	usage := name + ` FILE ANCHOR CONTENT   /   ` + name + ` -f FILE -c ANCHOR CONTENT`

	a := &insertArgs{}
	if f, rest, ok := fileShorthand(args); ok {
		a.file, args = config.ResolvePath(f), rest
		// 不给文件时第一个位置参数会被当成文件名 —— 与其报「内容不是锚点」或
		// 「文件不存在」，不如直接指出缺 FILE。真要操作一个 3 字符同名文件，
		// 用 -f 形式就不走简写判定。
		if validAnchorShape(a.file) {
			return nil, UsageErr("%s: FILE is required", name).WithHint(usage).
				WithDetails("hint: " + a.file + " looks like an anchor, not a path — " + name + " FILE ANCHOR CONTENT")
		}
	}
	hasContent := false
	useChange := false
	for _, s := range args {
		if s == "-c" || s == "--change" {
			useChange = true
		}
	}

	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "-f" || s == "--file":
			if i+1 >= len(args) {
				return nil, UsageErr("-f requires a path").WithHint(usage)
			}
			i++
			a.file = config.ResolvePath(args[i])
		case strings.HasPrefix(s, "--file="):
			a.file = config.ResolvePath(s[len("--file="):])

		case s == "-c" || s == "--change":
			if i+2 >= len(args) {
				return nil, UsageErr("-c requires ANCHOR and CONTENT").WithHint(usage).
					WithDetails(`hint: give "" to insert one empty line`)
			}
			a.anchor = strings.TrimSpace(args[i+1])
			a.content, hasContent = splitInsertContent(args[i+2]), true
			i += 2

		default:
			if useChange {
				return nil, UsageErr("unexpected argument %q", s).WithHint(usage)
			}
			if a.anchor == "" {
				if !validAnchorShape(s) {
					return nil, anchorShapeErr(s)
				}
				a.anchor = s
				continue
			}
			if !hasContent {
				a.content, hasContent = splitInsertContent(s), true
				continue
			}
			return nil, UsageErr("unexpected argument %q", s).WithHint(usage)
		}
	}

	if a.file == "" {
		return nil, UsageErr("%s: FILE is required", name).WithHint(usage)
	}
	if a.anchor == "" {
		return nil, UsageErr("%s: ANCHOR is required", name).WithHint(usage)
	}
	if !validAnchorShape(a.anchor) {
		return nil, anchorShapeErr(a.anchor)
	}
	if !hasContent {
		return nil, UsageErr("%s: CONTENT is required", name).WithHint(usage).
			WithDetails(`hint: give "" to insert one empty line`)
	}
	return a, nil
}

// splitInsertContent 把插入内容切成行。
//
// 与 edit 的 splitReplacement 相反：这里 "" 不是「删除」，而是「插入一个空行」——
// 插入动作没有"删掉"可言，空内容只能理解成空行（strings.Split 正好如此）。
func splitInsertContent(content string) []string {
	return strings.Split(content, "\n")
}

// runInsert 是 append / prepend 的实现（方向由 c.Tool 决定）。
//
// FILE 必填（位置参数或 -f FILE）：锚点只在 served 基线里有效，而基线是逐路径记的，
// 不给文件就只能跨路径反查，内容相同的行在不同文件里锚点相同、无从判断改哪个。
//
// 插入在内部表达成「把被锚定的那一行替换成 它自己 + 新行」（prepend 反过来）：
// 于是校验链、原子写、undo、served 重建全部沿用 applyEdit，不为插入另开分支。
func runInsert(c *Ctx, args []string) error {
	before := c.Tool == ToolPrepend
	a, err := parseInsertArgs(c.Tool, args)
	if err != nil {
		return err
	}
	file := a.file

	doc, err := docfile.Load(file, docfile.Options{
		MaxLines:     c.Res.Cfg.Read.MaxLines,
		MaxLineBytes: c.Res.Cfg.Read.MaxLineBytes,
	})
	if err != nil {
		return translateDocError(err, file)
	}
	anchors, err := anchorsFor(c, doc)
	if err != nil {
		return err
	}
	cur := edit.NewCurrent(doc.Lines, anchors)

	// 被锚定行的原文：append 把它摆在新行之前，prepend 摆在之后。
	// 锚点解析不出来（过期 / 重复）时不猜：这里留空串，紧随其后的校验链会先报
	// E_STALE_ANCHOR / E_AMBIGUOUS_ANCHOR，报法与 edit 一致，也不会写盘。
	anchorLine := ""
	if occ := cur.Occurs(a.anchor); len(occ) == 1 {
		anchorLine = doc.Lines[occ[0]-1]
	}
	repl := make([]string, 0, len(a.content)+1)
	if before {
		repl = append(repl, a.content...)
		repl = append(repl, anchorLine)
	} else {
		repl = append(repl, anchorLine)
		repl = append(repl, a.content...)
	}

	verb := "appended"
	if before {
		verb = "prepended"
	}
	return applyEdit(c, file, []edit.Op{{From: a.anchor, To: a.anchor, Replacement: repl}}, verb)
}
