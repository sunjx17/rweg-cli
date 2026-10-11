package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"rweg/internal/config"
	"rweg/internal/docfile"
	"rweg/internal/edit"
	"rweg/internal/hashline"
	"rweg/internal/ranges"
	"rweg/internal/store"
)

// maxEditDiffContext 是成功输出里 diff 前后各带几行上下文。
//
// 带上下文不是装饰：模型靠它确认「我要改的那段确实是这段」。
// 上下文行用**新文件**的锚点，因为那是模型接下来要用的那套。
const maxEditDiffContext = 1

// editArgs 是 edit 的已解析参数。
type editArgs struct {
	file string
	ops  []edit.Op
}

// parseEditArgs 解析 edit 的参数。
//
//	edit -f FILE -c FROM:TO "lines" [-c FROM:TO "lines" ...]
//
// 约定：
//   - `-c` 吃两个参数：锚点对 + 替换文本
//   - `FROM:TO` 省略 `:TO` 时等价于 `FROM:FROM`（改单行）
//   - 替换文本用 `\n` 分行；空串 = 删除该区间
//
// 刻意不支持 `-c A:B:text` 这种单参数合体写法：文本里本来就可能有 `:`，
// 合一解析会引入一个"什么情况下冒号是分隔符"的模糊规则。
func parseEditArgs(args []string) (*editArgs, error) {
	a := &editArgs{}
	// 简写：edit FILE FROM[:TO] CONTENT [FROM[:TO] CONTENT ...]
	shorthand := false
	if f, rest, ok := fileShorthand(args); ok {
		a.file, args, shorthand = config.ResolvePath(f), rest, true
	}
	hasChangeFlag := false
	for _, s := range args {
		if s == "-c" || s == "--change" {
			hasChangeFlag = true
		}
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "-f" || s == "--file":
			if i+1 >= len(args) {
				return nil, UsageErr("-f requires a path").
					WithHint(`edit -f FILE -c FROM:TO "lines"`)
			}
			i++
			a.file = config.ResolvePath(args[i])
		case strings.HasPrefix(s, "--file="):
			a.file = config.ResolvePath(s[len("--file="):])

		case s == "-c" || s == "--change":
			if i+2 >= len(args) {
				return nil, UsageErr("-c requires FROM:TO and a replacement").
					WithHint(`edit -f FILE -c CTt:AU6 "new line"`).
					WithDetails("hint: give \"\" to delete the range")
			}
			spec, content := args[i+1], args[i+2]
			i += 2
			op, err := makeOp(spec, content)
			if err != nil {
				return nil, err
			}
			a.ops = append(a.ops, op)

		default:
			// 简写形态：位置参数成对出现（范围 + 内容），中间仍可夹 flag。
			if shorthand && !hasChangeFlag {
				if i+1 >= len(args) {
					return nil, UsageErr("%q is missing its content", s).
						WithHint(`edit FILE FROM:TO "lines"`)
				}
				op, err := makeOp(s, args[i+1])
				if err != nil {
					return nil, err
				}
				a.ops = append(a.ops, op)
				i++
				continue
			}
			return nil, UsageErr("unexpected argument %q", s).
				WithHint(`edit FILE FROM:TO "lines" [-c FROM:TO "lines" ...]`)
		}
	}
	if a.file == "" {
		return nil, UsageErr("edit: FILE is required").
			WithHint(`edit src/app.ts CTt:CTt "function hi() {"`)
	}
	if len(a.ops) == 0 {
		return nil, UsageErr("edit: at least one FROM:TO range is required").
			WithHint(`edit -f src/app.ts -c CTt:CTt "function hi() {"`)
	}
	return a, nil
}

func makeOp(spec, content string) (edit.Op, error) {
	from, to, err := splitAnchorSpec(spec)
	if err != nil {
		return edit.Op{}, err
	}
	return edit.Op{From: from, To: to, Replacement: splitReplacement(content)}, nil
}

// splitAnchorSpec 把 "FROM:TO" 拆成两个锚点。无冒号时 TO = FROM。
func splitAnchorSpec(spec string) (string, string, error) {
	spec = strings.TrimSpace(spec)
	i := strings.Index(spec, ":")
	if i < 0 {
		if !validAnchorShape(spec) {
			return "", "", anchorShapeErr(spec)
		}
		return spec, spec, nil
	}
	from, to := strings.TrimSpace(spec[:i]), strings.TrimSpace(spec[i+1:])
	if !validAnchorShape(from) {
		return "", "", anchorShapeErr(from)
	}
	if !validAnchorShape(to) {
		return "", "", anchorShapeErr(to)
	}
	return from, to, nil
}

// validAnchorShape 检查锚点形状：3 个 base62 字符。
//
// 形状不对（`ZZ`、`CTtX`、空串）时**不**去查 served——
// 那一定是打字错误，早报早改，比"不在 served 里"更有指导性。
func validAnchorShape(s string) bool {
	if len(s) != hashline.HashLen {
		return false
	}
	_, ok := hashline.HashToIndex(s)
	return ok
}

// anchorShapeErr 报锚点形状错误，并按写错的形状给定向诊断。
//
// 模型最常见的两种误传都在这里认出来：把 read 输出的整行（`CTt│function hi() {`）
// 当锚点，或把行号当锚点。
func anchorShapeErr(s string) error {
	e := UsageErr("E_BAD_ANCHOR  %q is not a %d-character base62 anchor", s, hashline.HashLen)
	switch {
	case strings.Contains(s, hashline.Sep) && strings.Contains(s, "\n"):
		e.Hint = "a replacement is FROM:TO plus content — use the first hash as FROM, the last as TO, and put the lines in the content (never paste the HASH" + hashline.Sep + " rows)"
	case strings.Contains(s, hashline.Sep):
		e.Hint = `anchors are the 3-char hash only — drop everything from "` + hashline.Sep + `" onward: CTt` + hashline.Sep + `function hi() { → CTt`
	case s != "" && s[0] >= '0' && s[0] <= '9':
		e.Hint = "that looks like a line number; anchors are 3 base62 chars (e.g. CTt) taken from read / grep output"
	default:
		e.Hint = "anchors look like CTt / Um7 / 6PY — get them from read or grep"
	}
	return e
}

// splitReplacement 把替换文本切成行。
//
//	""      → 无行（删除）
//	"a"     → ["a"]
//	"a\nb"  → ["a", "b"]
func splitReplacement(content string) []string {
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

// runEdit 是 edit 的实现：解析参数，把活交给 applyEdit。
func runEdit(c *Ctx, args []string) error {
	a, err := parseEditArgs(args)
	if err != nil {
		return err
	}
	return applyEdit(c, a.file, a.ops, "edited")
}

// applyEdit 是 edit / append / prepend 共用的写入链路。
//
// 调用方只负责把动作表达成若干 edit.Op：append / prepend 传进来的那条 op
// 把被锚定的行替换成「它自己 + 新行」（prepend 反过来），所以校验链、原子写、
// undo、served 重建沿用同一条路径，不为插入另开分支。
// verb 是成功输出的动词（edited / appended / prepended）。
//
// 流程（plan §7.1）：
//
//	加载当前文件 → 算当前锚点 → 取 served → 校验链 → 应用 → 原子落盘 → 写库 → 输出
//
// 原子性体现在两个层次：
//  1. 所有 op 先在内存里解析 + 校验，任何一个失败 → 不写文件、不写库
//  2. 全部通过 → 一次 MapStable 重算 → 单次原子写 → 单事务写库
func applyEdit(c *Ctx, file string, ops []edit.Op, verb string) error {
	if n := c.Res.Cfg.Edit.MaxChanges; n > 0 && len(ops) > n {
		return UsageErr("E_TOO_MANY  %d changes exceed [edit].max_changes = %d", len(ops), n).
			WithHint("split into several edit calls")
	}

	doc, err := docfile.Load(file, docfile.Options{
		MaxLines:     c.Res.Cfg.Read.MaxLines,
		MaxLineBytes: c.Res.Cfg.Read.MaxLineBytes,
	})
	if err != nil {
		return translateDocError(err, file)
	}
	if doc.LineCount() == 0 {
		return BizErr("E_EMPTY  %s is empty", displayPath(file)).
			WithHint(`use write -f ` + file + ` -c "..." to fill it`)
	}

	anchors, err := anchorsFor(c, doc)
	if err != nil {
		return err
	}
	cur := edit.NewCurrent(doc.Lines, anchors)

	sv, err := servedView(c, doc.Path)
	if err != nil {
		return err
	}

	validated, fail := edit.Check(sv, cur, ops, c.Res.Cfg.Edit.ServedEchoCap)
	if fail != nil {
		return editFailure(fail, doc, c, anchors)
	}

	// 替换文本里误带行首锚点（把 read 输出的整行连锚点一起贴回来）：命中本文件
	// **当前**锚点的行硬拒 —— 写进去就是把 `CTt│...` 当内容落盘；其余的只记软警告，
	// 内容照写（前缀本来可能就是字面内容）。
	found := edit.ScanContentAnchors(ops, cur.Hashes, sv.Hashes)
	if bad := edit.BlockingContentAnchors(found); len(bad) > 0 {
		return headAnchorInContentErr(doc, bad)
	}
	warnings := softHeadAnchorWarnings(doc, found)

	// retired 里的锚点禁止再分配（模型可能还持有）。校验通过后取一次，
	// 保证"校验用的语义"与"分配用的语义"来自同一份快照。
	reserved, err := c.DB.RetiredFor(doc.Path)
	if err != nil {
		return &Error{Code: ExitInternal, Message: "edit: " + err.Error()}
	}

	res := edit.Apply(cur, validated, sv, reserved,
		c.Res.Cfg.Hash.ProbeStride, maxEditDiffContext)

	// 落盘。混合行尾要逐行继承旧行尾——插入一行会让其后所有行下标偏移，
	// 不能沿用原始 LineEnds（见 docfile.RenderWith 的注释）。
	// RenderEncoded 而不是 Render：必须保持文件原来的编码（GBK 文件不能
	// 因为一次编辑就静默变成 UTF-8）。
	pre, preTooBig, preErr := preImage(doc.Path, true)
	if preErr != nil {
		pre, preTooBig = nil, true
	}

	out, err := doc.RenderEncoded(res.Lines, remapEnds(doc, res.Mapping))
	if err != nil {
		return translateDocError(err, file)
	}
	if err := writeFileAtomic(doc.Path, out); err != nil {
		return &Error{Code: ExitBiz, Message: "E_WRITE  " + displayPath(file) + ": " + err.Error()}
	}
	recordUndo(c, doc.Path, true, string(c.Tool), pre, preTooBig)

	// 写库：served 整份替换（行号会位移）+ retired 追加。
	canonVer := c.Res.Cfg.Hash.CanonVersion
	if err := c.DB.PutServed(doc.Path, res.Served, res.Canons, canonVer, c.Res.Cfg.Hash.ProbeStride); err != nil {
		return &Error{Code: ExitInternal, Message: "edit: persist served: " + err.Error()}
	}
	if err := c.DB.Retire(doc.Path, res.Retire); err != nil {
		return &Error{Code: ExitInternal, Message: "edit: persist retired: " + err.Error()}
	}
	// 新内容的锚点顺手回填缓存（下次 read 直接命中）。
	fp := store.ContentFingerprint(res.Lines)
	if err := c.DB.PutSnapshotByFingerprint(doc.Path, res.Hashes, fp, canonVer); err != nil {
		return &Error{Code: ExitInternal, Message: "edit: cache snapshot: " + err.Error()}
	}

	if c.JSON {
		return emitEditJSON(os.Stdout, doc, res, warnings)
	}
	emitEditText(os.Stdout, doc, res, c.Quiet, verb)
	emitWarnings(warnings)
	return nil
}

// servedView 把 store 的 served 记录转成 edit 包的最小视图。
func servedView(c *Ctx, path string) (edit.ServedView, error) {
	s, err := c.DB.GetServed(path)
	if err != nil {
		return edit.ServedView{}, &Error{Code: ExitInternal, Message: "edit: " + err.Error()}
	}
	if s == nil {
		return edit.ServedView{}, nil
	}
	return edit.ServedView{Exists: true, Hashes: s.Hashes}, nil
}

// remapEnds 按旧→新映射重建逐行行尾。
//
// 只在混合行尾文件上需要；新插入的行留空，由 RenderWith 回退到多数派行尾。
func remapEnds(doc *docfile.Doc, mapping []int) []string {
	if !doc.MixedNewline || len(doc.LineEnds) == 0 {
		return nil
	}
	newLen := 0
	for _, np := range mapping {
		if np+1 > newLen {
			newLen = np + 1
		}
	}
	if newLen < len(doc.LineEnds) {
		newLen = len(doc.LineEnds)
	}
	ends := make([]string, newLen)
	for k, np := range mapping {
		if np < 0 || k >= len(doc.LineEnds) {
			continue
		}
		ends[np] = doc.LineEnds[k]
	}
	return ends
}

// editFailure 把校验链的拒绝渲染成 CLI 错误。
//
// 回显给模型看过的行一律进 served（见 serveEchoedRows）—— 失败回显里的当前锚点
// 立刻可用，模型可以直接拿它们重试，不必先重新 read。
func editFailure(f *edit.Failure, doc *docfile.Doc, c *Ctx, anchors []string) error {
	path := displayPath(doc.Path)
	msg := f.Code + "  " + path
	switch f.Code {
	case edit.CodeNeverRead:
		msg += " " + f.Msg
	case edit.CodeUnknownAnchor, edit.CodeStaleAnchor, edit.CodeAmbiguous:
		msg += "  " + f.Msg
	case edit.CodeStaleRange:
		msg += "  " + f.Msg
	default:
		msg += "  " + f.Msg
	}

	e := &Error{Code: ExitBiz, Message: msg}

	switch f.Code {
	case edit.CodeNeverRead:
		e.Hint = "read -f " + path + " first"
	case edit.CodeUnknownAnchor:
		e.Hint = "anchors must come from read/grep in this namespace; re-read the file"
	case edit.CodeStaleAnchor, edit.CodeAmbiguous:
		// 回传的窗口只是"这个锚点原来在哪"的线索，不是建议的读取范围：
		// 锚点解析不出来时我们并不知道模型原本想改哪一段，只能让它整份重读。
		e.Hint = "re-read the whole file: read -f " + path
	case edit.CodeStaleRange:
		if f.RegionValid && f.Region.End-f.Region.Start+1 <= len(f.Lines) {
			// 整个区间都回显了 → 回显的锚点已进 served，可以直接重试。
			e.Hint = fmt.Sprintf("retry with the anchors above (lines %d-%d are served now)", f.Region.Start, f.Region.End)
		} else if f.RegionValid {
			e.Hint = fmt.Sprintf("read -f %s %d:%d  then retry (only part of the range was echoed)",
				path, f.Region.Start, f.Region.End)
		} else {
			e.Hint = "re-read the file: " + path
		}
	default:
		e.Hint = "read -f " + path + " and retry"
	}

	if len(f.Lines) > 0 {
		// 回显给模型看过的行一律进 served：见过即可用。
		if err := serveEchoedRows(c, doc, anchors, f.Lines); err != nil {
			return err
		}
		var b strings.Builder
		if f.RegionValid {
			fmt.Fprintf(&b, "Current %s (%d-%d):\n",
				regionWord(f.Code), f.Region.Start, f.Region.End)
		} else {
			b.WriteString("Current content:\n")
		}
		writeAnchorLines(&b, f.Lines, "")
		if c.Res.Cfg.Edit.ServedEchoCap > 0 {
			if total := f.Region.End - f.Region.Start + 1; total > len(f.Lines) {
				fmt.Fprintf(&b, "... (%d more line(s) omitted)\n", total-len(f.Lines))
			}
		}
		e.Details = b.String()
	}
	return e
}

// serveEchoedRows 把失败回显里给模型看过的行写进 served。
//
// 「模型见过即可用」：回显的每一行都带着当前锚点，所以模型可以直接拿它们重试。
// 空锚点（超长行的 ~~~）没有锚点可给，跳过。
func serveEchoedRows(c *Ctx, doc *docfile.Doc, anchors []string, lines []edit.Line) error {
	set := ranges.NewSet(doc.LineCount())
	for _, l := range lines {
		if l.Hash == "" || l.Line < 1 || l.Line > doc.LineCount() {
			continue
		}
		set.AddRange(l.Line, l.Line)
	}
	if set.Count() == 0 {
		return nil
	}
	canons := make([]string, doc.LineCount())
	for i, ln := range doc.Lines {
		canons[i] = hashline.Canon(ln)
	}
	if err := c.DB.ServeLines(doc.Path, set, anchors, canons,
		c.Res.Cfg.Hash.CanonVersion, c.Res.Cfg.Hash.ProbeStride); err != nil {
		return &Error{Code: ExitInternal, Message: "edit: persist served: " + err.Error()}
	}
	return nil
}

// headAnchorInContentErr 拒绝把 read 输出整行当替换内容写回文件。
func headAnchorInContentErr(doc *docfile.Doc, bad []edit.ContentAnchor) *Error {
	e := BizErr("E_ANCHOR_IN_CONTENT  %s: %d replacement line(s) start with a line-head anchor",
		displayPath(doc.Path), len(bad)).
		WithHint(`retry with the bare line content — drop the "HASH` + hashline.Sep + `" prefix (nothing was written)`)
	var b strings.Builder
	for _, ca := range bad {
		fmt.Fprintf(&b, "-c #%d line %d: %s%s is an anchor of this file; the line should be:\n  %s\n",
			ca.Change, ca.K, ca.Anchor, hashline.Sep, ca.Bare)
	}
	e.Details = strings.TrimRight(b.String(), "\n")
	return e
}

// softHeadAnchorWarnings 把不阻塞的「行首像锚点」的行变成提示行。
//
// 前缀命中的是基线里的锚点（那行已被改过 / 删过）或压根没见过的锚点形状——
// 内容照写，只提醒模型这个前缀大概是多余的。
func softHeadAnchorWarnings(doc *docfile.Doc, found []edit.ContentAnchor) []string {
	var out []string
	for _, ca := range found {
		if ca.Kind == edit.AnchorInFile {
			continue // 这类已经在 headAnchorInContentErr 拦下了
		}
		code, why := "W_NEVER_SERVED_SHAPE", "that anchor was never served for this file"
		if ca.Kind == edit.AnchorServed {
			code, why = "W_SERVED_PREFIX_MISMATCH", "that anchor is in the baseline, but this is not the line it was served for"
		}
		out = append(out, fmt.Sprintf("%s  %s: -c #%d line %d opens with %q — %s; written literally",
			code, displayPath(doc.Path), ca.Change, ca.K, ca.Anchor+hashline.Sep, why))
	}
	if len(out) > 0 {
		out = append(out, `hint: if the prefix was unintended, undo and retry with the bare content`)
	}
	return out
}

// emitWarnings 把软警告写到 stderr —— stdout 只留锚点输出（--json 时进 JSON 字段）。
func emitWarnings(warnings []string) {
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, w)
	}
}

func regionWord(code string) string {
	if code == edit.CodeStaleAnchor || code == edit.CodeAmbiguous {
		return "region"
	}
	return "range"
}

// writeAnchorLines 渲染若干 `hash│text` 行。mark 非空时作为行首标记（diff 用）。
func writeAnchorLines(w io.Writer, lines []edit.Line, mark string) {
	for _, l := range lines {
		h := l.Hash
		if h == "" {
			h = strings.Repeat("~", hashline.HashLen)
		}
		if mark != "" {
			fmt.Fprintf(w, "%s %s%s%s\n", mark, h, hashline.Sep, l.Text)
			continue
		}
		fmt.Fprintf(w, "%s%s%s\n", h, hashline.Sep, l.Text)
	}
}

// emitEditText 渲染成功输出：一行确认 + diff 正文 + 一行收尾（路径与行数变化）。
//
//	appended ok
//	  Wot│a
//	+ 7O8│33
//	  rKa│b
//	t.txt (2 → 3 lines, +1)
//
// -q 只留 diff 正文（确认行与收尾行都不打）。
func emitEditText(w io.Writer, doc *docfile.Doc, res *edit.Result, quiet bool, verb string) {
	var b strings.Builder
	if !quiet {
		fmt.Fprintf(&b, "%s ok\n", verb)
	}
	for _, d := range res.Diff {
		switch d.Kind {
		case edit.DiffElide:
			b.WriteString("...\n")
		case edit.DiffDel:
			writeAnchorLines(&b, []edit.Line{d.Line}, "-")
		case edit.DiffIns:
			writeAnchorLines(&b, []edit.Line{d.Line}, "+")
		default:
			writeAnchorLines(&b, []edit.Line{d.Line}, " ")
		}
	}
	if !quiet {
		// 只报真正变了的行：两个计数都是 0 时不出 "+0 -0"。
		delta := ""
		if res.Added > 0 {
			delta += fmt.Sprintf(", +%d", res.Added)
		}
		if res.Removed > 0 {
			delta += fmt.Sprintf(" -%d", res.Removed)
		}
		fmt.Fprintf(&b, "%s (%d → %d lines%s)\n", displayPath(doc.Path), doc.LineCount(), len(res.Lines), delta)
	}
	io.WriteString(w, b.String())
}

// editJSON 是 edit --json 的输出结构。
type editJSON struct {
	Path         string         `json:"path"`
	LineCount    int            `json:"line_count"`
	OldLineCount int            `json:"old_line_count"`
	Added        int            `json:"added"`
	Removed      int            `json:"removed"`
	Retired      []string       `json:"retired,omitempty"`
	Diff         []editJSONLine `json:"diff"`
	Warnings     []string       `json:"warnings,omitempty"`
}

// editJSONLine 是 diff 里的一行：kind 为 context / del / ins / elide。
type editJSONLine struct {
	Kind string `json:"kind"`
	Line int    `json:"line,omitempty"`
	Hash string `json:"hash,omitempty"`
	Text string `json:"text,omitempty"`
}

func emitEditJSON(w io.Writer, doc *docfile.Doc, res *edit.Result, warnings []string) error {
	out := editJSON{
		Path:         doc.Path,
		LineCount:    len(res.Lines),
		OldLineCount: doc.LineCount(),
		Added:        res.Added,
		Removed:      res.Removed,
		Retired:      res.Retire,
		Diff:         make([]editJSONLine, 0, len(res.Diff)),
		Warnings:     warnings,
	}
	kinds := map[edit.DiffKind]string{
		edit.DiffContext: "context",
		edit.DiffDel:     "del",
		edit.DiffIns:     "ins",
		edit.DiffElide:   "elide",
	}
	for _, d := range res.Diff {
		out.Diff = append(out.Diff, editJSONLine{
			Kind: kinds[d.Kind], Line: d.Line.Line, Hash: d.Line.Hash, Text: d.Line.Text,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
