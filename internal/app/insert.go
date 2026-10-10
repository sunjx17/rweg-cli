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
//	append ANCHOR CONTENT
//	append -f FILE ANCHOR CONTENT
//	append -c ANCHOR CONTENT
//
// 位置形态是主用法：单锚点在最前，内容紧跟其后。`-c` 形态给内容恰好是 rweg
// 认得的 flag（-v / -q / -h / -f / -c / --json）时用 —— 那种内容会被通用 flag
// 解析摘走，只有 `-c ANCHOR CONTENT` 能原样递进来（与 edit 的 `-c` 同理）。
func parseInsertArgs(tool Tool, args []string) (*insertArgs, error) {
	name := string(tool)
	usage := name + ` ANCHOR CONTENT   /   ` + name + ` -f FILE -c ANCHOR CONTENT`

	a := &insertArgs{}
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
// 位置只由**单个锚点**给出，文件可以省：锚点只在 served 基线里有效，而基线是
// 逐路径记的，所以反查一次就知道它属于哪个文件（见 resolveInsertPath）。
//
// 插入在内部表达成「把被锚定的那一行替换成 它自己 + 新行」（prepend 反过来）：
// 于是校验链、原子写、undo、served 重建全部沿用 applyEdit，不为插入另开分支。
func runInsert(c *Ctx, args []string) error {
	before := c.Tool == ToolPrepend
	a, err := parseInsertArgs(c.Tool, args)
	if err != nil {
		return err
	}
	file, err := resolveInsertPath(c, a.file, a.anchor)
	if err != nil {
		return err
	}

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

// resolveInsertPath 决定这次插入作用在哪个文件上。
//
// -f 给了就用它（锚点是否属于它是校验链的事）；没给就从 served 基线反查锚点。
// 反查只在**唯一命中**时认账：锚点由内容决定，不同文件里内容相同的行会得到同一个
// 锚点，因此多个文件都 serve 过它时报 E_AMBIGUOUS_ANCHOR，让调用方用 -f 说清楚 ——
// 绝不"选一个"，那正是静默写错行的来源。
func resolveInsertPath(c *Ctx, file, anchor string) (string, error) {
	if file != "" {
		return file, nil
	}
	paths, err := c.DB.ServedPaths()
	if err != nil {
		return "", &Error{Code: ExitInternal, Message: "lookup anchor: " + err.Error()}
	}
	var hits []string
	for _, p := range paths {
		sv, err := c.DB.GetServed(p)
		if err != nil {
			return "", &Error{Code: ExitInternal, Message: "lookup anchor: " + err.Error()}
		}
		if sv != nil && sv.Has(anchor) {
			hits = append(hits, p)
		}
	}
	switch len(hits) {
	case 0:
		return "", BizErr("E_UNKNOWN_ANCHOR  %s is not in this namespace's baseline", anchor).
			WithHint("read -f FILE first: an anchor is usable only right after read / grep")
	case 1:
		return hits[0], nil
	}
	return "", BizErr("E_AMBIGUOUS_ANCHOR  %s is served for %d files", anchor, len(hits)).
		WithHint("pass -f FILE to pick one").
		WithDetails("candidates:\n" + strings.Join(hits, "\n"))
}
