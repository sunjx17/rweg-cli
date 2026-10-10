package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"rweg/internal/config"
	"rweg/internal/docfile"
	"rweg/internal/hashline"
	"rweg/internal/ranges"
	"rweg/internal/store"
)

// readArgs 是 read 的已解析参数。
type readArgs struct {
	file     string
	specs    []string
	encoding string
	noServe  bool
}

// parseReadArgs 解析 read 的参数。
//
// 用法：
//
//	read -f FILE [RANGE ...] [--encoding ENC] [--no-serve]
//
// RANGE 是位置参数：1:3 / 5 / 8- / -4。
//
// **以 `-` 开头的位置参数必须放行**：`-4` 是"文件开头到 4 行"，
// 不是 flag。所以这里不按 `-` 前缀一律拒绝，而是先试着用 ranges 解析，
// 解析成功就是范围，失败才当 flag 报错。
func parseReadArgs(args []string) (*readArgs, error) {
	a := &readArgs{}
	// 简写：read FILE [RANGE ...]
	if f, rest, ok := fileShorthand(args); ok {
		a.file, args = config.ResolvePath(f), rest
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "-f" || s == "--file":
			if i+1 >= len(args) {
				return nil, UsageErr("-f requires a path").WithHint("read -f FILE [RANGE ...]")
			}
			i++
			a.file = config.ResolvePath(args[i])
		case strings.HasPrefix(s, "--file="):
			a.file = config.ResolvePath(s[len("--file="):])
		case s == "--encoding":
			if i+1 >= len(args) {
				return nil, UsageErr("--encoding requires a value").
					WithHint("read -f FILE --encoding gb18030")
			}
			i++
			a.encoding = args[i]
		case strings.HasPrefix(s, "--encoding="):
			a.encoding = s[len("--encoding="):]
		case s == "--no-serve":
			a.noServe = true
		case strings.HasPrefix(s, "-") && len(s) > 1:
			// 可能是范围（-4 / -999），交给 ranges 判定
			if _, err := ranges.Parse(s, 0); err == nil {
				a.specs = append(a.specs, s)
				continue
			}
			return nil, UsageErr("unknown flag %q for read", s).
				WithHint("read -f FILE [RANGE ...] [--encoding ENC]")
		default:
			a.specs = append(a.specs, s)
		}
	}
	if a.file == "" {
		hint := "read src/app.ts 1:10   （等价写法：read -f src/app.ts 1:10）"
		if len(args) == 0 {
			// 裸 read：env.sh 的 read 函数把它判给 rweg，免得交互式终端
			// 上等一行输入把 agent 卡死。这里指路 bash 原生语义。
			hint = "read -f FILE [RANGE ...]；要 bash 的读一行语义请写 builtin read"
		}
		return nil, UsageErr("read: FILE is required").WithHint(hint)
	}
	return a, nil
}

// runRead 是 read 的实现。
//
// 流程：
//  1. 加载文件（编码/BOM/行尾/二进制探测）
//  2. 解析并合并范围
//  3. 计算锚点（优先用 snapshot 缓存 + retired 预留）
//  4. 写库：把范围内的行标记为 served
//  5. 输出：每块 FROM:TO: + 每行 锚点│内容
func runRead(c *Ctx, args []string) error {
	a, err := parseReadArgs(args)
	if err != nil {
		return err
	}

	doc, err := docfile.Load(a.file, docfile.Options{
		MaxLines:     c.Res.Cfg.Read.MaxLines,
		MaxLineBytes: c.Res.Cfg.Read.MaxLineBytes,
		Encoding:     a.encoding,
	})
	if err != nil {
		return translateDocError(err, a.file)
	}

	lineCount := doc.LineCount()
	if lineCount == 0 {
		return BizErr("E_EMPTY  %s is empty", displayPath(a.file)).
			WithHint("nothing to anchor; use write -f " + a.file + " -c \"...\"")
	}

	raw, err := ranges.ParseAll(a.specs, lineCount)
	if err != nil {
		return bizRangeError(err, a.file, lineCount)
	}
	merged, err := ranges.Normalize(raw, lineCount)
	if err != nil {
		return bizRangeError(err, a.file, lineCount)
	}

	anchors, err := anchorsFor(c, doc)
	if err != nil {
		return err
	}

	// 4. 标记 served（多范围的所有行）
	if !a.noServe {
		set := ranges.NewSet(lineCount)
		for _, r := range merged {
			set.AddRange(r.Start, r.End)
		}
		canons := make([]string, lineCount)
		for i, ln := range doc.Lines {
			canons[i] = hashline.Canon(ln)
		}
		if err := c.DB.ServeLines(doc.Path, set, anchors, canons,
			c.Res.Cfg.Hash.CanonVersion, c.Res.Cfg.Hash.ProbeStride); err != nil {
			return &Error{Code: ExitInternal, Message: "read: " + err.Error()}
		}
	}

	// 5. 输出
	if c.JSON {
		return emitReadJSON(os.Stdout, doc, merged, anchors)
	}
	emitReadText(os.Stdout, doc, merged, anchors, c.Quiet)
	return nil
}

// anchorsFor 计算全文件锚点。
//
// 先查 snapshot 缓存（按**行内容指纹**，查缓存时不需要先算锚点），
// 命中就直接用；未命中则重算，并把 retired 锚点预留占位，
// 避免把模型还持有的已释放锚点重新分配出去。算完回填缓存。
func anchorsFor(c *Ctx, doc *docfile.Doc) ([]string, error) {
	lineCount := doc.LineCount()
	canonVer := c.Res.Cfg.Hash.CanonVersion
	fp := store.ContentFingerprint(doc.Lines)

	if cached, ok, err := c.DB.GetSnapshotByFingerprint(doc.Path, fp, canonVer); err != nil {
		// 缓存读失败不是致命错误，退化成重算
		_ = err
	} else if ok && len(cached) == lineCount {
		return cached, nil
	}

	retired, err := c.DB.RetiredFor(doc.Path)
	if err != nil {
		return nil, &Error{Code: ExitInternal, Message: "read: " + err.Error()}
	}
	anchors := hashline.Pure(doc.Lines, c.Res.Cfg.Hash.ProbeStride, retired)

	if err := c.DB.PutSnapshotByFingerprint(doc.Path, anchors, fp, canonVer); err != nil {
		return nil, &Error{Code: ExitInternal, Message: "read: cache snapshot: " + err.Error()}
	}
	return anchors, nil
}

// emitReadText 渲染人读的锚点输出。
//
// 格式（plan §6.2）：
//
//	# src/app.ts (6 lines, lf)
//	1:6:
//	CTt│function hello() {
//	Um7│  console.log("world");
//
// 超长行不给锚点，只给占位标记 —— 锚点需要完整行。
func emitReadText(w io.Writer, doc *docfile.Doc, blocks []ranges.Range, anchors []string, quiet bool) {
	var b strings.Builder

	if !quiet {
		b.WriteString("# " + displayPath(doc.Path))
		b.WriteString(" (" + formatLineCount(doc) + ")")
		b.WriteString("\n")
	}

	sep := hashline.Sep
	for _, r := range blocks {
		fmt.Fprintf(&b, "%s:%s:\n", strconv.Itoa(r.Start), strconv.Itoa(r.End))
		for line := r.Start; line <= r.End; line++ {
			prefix := anchors[line-1] + sep
			if doc.HasLongLine(line) {
				prefix = strings.Repeat("~", hashline.HashLen) + sep
			}
			b.WriteString(prefix)
			b.WriteString(doc.Lines[line-1])
			b.WriteString("\n")
		}
	}
	io.WriteString(w, b.String())
}

// formatLineCount 渲染行数与文件特征，例 "6 lines, lf, bom"。
func formatLineCount(doc *docfile.Doc) string {
	parts := []string{fmt.Sprintf("%d line", doc.LineCount())}
	if doc.LineCount() != 1 {
		parts[0] += "s"
	}
	switch doc.Newline {
	case docfile.NewlineCRLF:
		parts = append(parts, "crlf")
	case docfile.NewlineCR:
		parts = append(parts, "cr")
	default:
		parts = append(parts, "lf")
	}
	if doc.MixedNewline {
		parts = append(parts, "mixed-eol")
	}
	if doc.HasBOM {
		parts = append(parts, "bom")
	}
	if doc.Encoding != docfile.EncodingUTF8 {
		parts = append(parts, doc.Encoding)
	}
	if doc.Truncated {
		parts = append(parts, "truncated")
	}
	return strings.Join(parts, ", ")
}

// readJSON 是 --json 的输出结构。
type readJSON struct {
	Path      string            `json:"path"`
	LineCount int               `json:"line_count"`
	Truncated bool              `json:"truncated,omitempty"`
	Newline   string            `json:"newline"`
	BOM       bool              `json:"bom,omitempty"`
	Encoding  string            `json:"encoding"`
	LongLines []int             `json:"long_lines,omitempty"`
	Blocks    []readJSONBlock   `json:"blocks"`
	Meta      map[string]string `json:"meta,omitempty"`
}

type readJSONBlock struct {
	Start int      `json:"start"`
	End   int      `json:"end"`
	Lines []string `json:"lines"`
}

func emitReadJSON(w io.Writer, doc *docfile.Doc, blocks []ranges.Range, anchors []string) error {
	out := readJSON{
		Path:      doc.Path,
		LineCount: doc.LineCount(),
		Truncated: doc.Truncated,
		Newline:   doc.Newline,
		BOM:       doc.HasBOM,
		Encoding:  doc.Encoding,
		LongLines: doc.LongLines,
		Blocks:    make([]readJSONBlock, 0, len(blocks)),
	}
	for _, r := range blocks {
		blk := readJSONBlock{Start: r.Start, End: r.End}
		for line := r.Start; line <= r.End; line++ {
			if doc.HasLongLine(line) {
				// 超长行：内容照给，锚点留空表示"不可用于 edit"
				blk.Lines = append(blk.Lines, "")
				continue
			}
			blk.Lines = append(blk.Lines, anchors[line-1])
		}
		out.Blocks = append(out.Blocks, blk)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// displayPath 返回给用户看的路径（相对 cwd 的更短形式）。
func displayPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	cwd, err := os.Getwd()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return abs
}

// bizRangeError 把 ranges 的错误包装成业务错误（退出码 1，不是 2）。
//
// 参数写错（E_RANGE）算用法错误还是业务错误？
// 决定：算用法错误（退出码 2）—— 用户少写了字符，改一下就好，
// 不需要理解文件状态。用法错误才能让模型立刻自我纠正。
func bizRangeError(err error, file string, lineCount int) error {
	return &Error{
		Code:    ExitUsage,
		Message: err.Error(),
		Hint: fmt.Sprintf("%s has %d line(s); valid forms: N | N:M | N- | -M",
			displayPath(file), lineCount),
	}
}

// translateDocError 把 docfile 的错误码映射到 rweg 退出码。
func translateDocError(err error, file string) error {
	de, ok := docfile.AsError(err)
	if !ok {
		return &Error{Code: ExitInternal, Message: "read: " + err.Error()}
	}
	code := ExitBiz
	switch de.Code {
	case docfile.ErrNotFound:
		code = ExitBiz
	case docfile.ErrIsDir, docfile.ErrNoPerm:
		code = ExitBiz
	case docfile.ErrUnknownEncoding:
		code = ExitUsage
	}
	e := &Error{Code: code, Message: de.Message}
	if de.Hint != "" {
		e.Hint = de.Hint
	}
	return e
}
