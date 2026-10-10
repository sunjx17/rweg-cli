package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rweg/internal/config"
	"rweg/internal/docfile"
	"rweg/internal/edit"
	"rweg/internal/hashline"
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

func anchorShapeErr(s string) error {
	return UsageErr("E_BAD_ANCHOR  %q is not a %d-character base62 anchor", s, hashline.HashLen).
		WithHint("anchors look like CTt / Um7 / 6PY — get them from read or grep")
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

// runEdit 是 edit 的实现。
//
// 流程（plan §7.1）：
//
//	加载当前文件 → 算当前锚点 → 取 served → 校验链 → 应用 → 原子落盘 → 写库 → 输出
//
// 原子性体现在两个层次：
//  1. 所有 -c 先在内存里解析 + 校验，任何一个失败 → 不写文件、不写库
//  2. 全部通过 → 一次 MapStable 重算 → 单次原子写 → 单事务写库
func runEdit(c *Ctx, args []string) error {
	a, err := parseEditArgs(args)
	if err != nil {
		return err
	}
	if n := c.Res.Cfg.Edit.MaxChanges; n > 0 && len(a.ops) > n {
		return UsageErr("E_TOO_MANY  %d changes exceed [edit].max_changes = %d", len(a.ops), n).
			WithHint("split into several edit calls")
	}

	doc, err := docfile.Load(a.file, docfile.Options{
		MaxLines:     c.Res.Cfg.Read.MaxLines,
		MaxLineBytes: c.Res.Cfg.Read.MaxLineBytes,
	})
	if err != nil {
		return translateDocError(err, a.file)
	}
	if doc.LineCount() == 0 {
		return BizErr("E_EMPTY  %s is empty", displayPath(a.file)).
			WithHint(`use write -f ` + a.file + ` -c "..." to fill it`)
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

	validated, fail := edit.Check(sv, cur, a.ops, c.Res.Cfg.Edit.ServedEchoCap)
	if fail != nil {
		return editFailure(fail, doc, c)
	}

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
		return translateDocError(err, a.file)
	}
	if err := writeFileAtomic(doc.Path, out); err != nil {
		return &Error{Code: ExitBiz, Message: "E_WRITE  " + displayPath(a.file) + ": " + err.Error()}
	}
	recordUndo(c, doc.Path, true, "edit", pre, preTooBig)

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
		return emitEditJSON(os.Stdout, doc, res)
	}
	emitEditText(os.Stdout, doc, res, c.Quiet)
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
// **回传的行只打印，不写 served** —— 见包注释与 plan §7.2 第 4 步。
func editFailure(f *edit.Failure, doc *docfile.Doc, c *Ctx) error {
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
		if f.RegionValid {
			e.Hint = fmt.Sprintf("read -f %s %d:%d  then retry", path, f.Region.Start, f.Region.End)
		} else {
			e.Hint = "re-read the file: " + path
		}
	default:
		e.Hint = "read -f " + path + " and retry"
	}

	if len(f.Lines) > 0 {
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

// emitEditText 渲染成功输出。
//
//	edited src/app.ts (6 -> 8 lines, +3 -1)
//	  CTt│function hello() {
//	- nVR│const x = 1;
//	+ EPx│const x = 42;
//	  Um7│  console.log("world");
func emitEditText(w io.Writer, doc *docfile.Doc, res *edit.Result, quiet bool) {
	var b strings.Builder
	if !quiet {
		name := "edited"
		if filepath.Base(doc.Path) == "" {
			name = "edited"
		}
		// 只报真正变了的行：两个计数都是 0 时不出 "+0 -0"。
		delta := ""
		if res.Added > 0 {
			delta += fmt.Sprintf(", +%d", res.Added)
		}
		if res.Removed > 0 {
			delta += fmt.Sprintf(" -%d", res.Removed)
		}
		fmt.Fprintf(&b, "%s %s (%d → %d lines%s)\n",
			name, displayPath(doc.Path), doc.LineCount(), len(res.Lines), delta)
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
}

// editJSONLine 是 diff 里的一行：kind 为 context / del / ins / elide。
type editJSONLine struct {
	Kind string `json:"kind"`
	Line int    `json:"line,omitempty"`
	Hash string `json:"hash,omitempty"`
	Text string `json:"text,omitempty"`
}

func emitEditJSON(w io.Writer, doc *docfile.Doc, res *edit.Result) error {
	out := editJSON{
		Path:         doc.Path,
		LineCount:    len(res.Lines),
		OldLineCount: doc.LineCount(),
		Added:        res.Added,
		Removed:      res.Removed,
		Retired:      res.Retire,
		Diff:         make([]editJSONLine, 0, len(res.Diff)),
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
