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
	"rweg/internal/hashline"
	"rweg/internal/store"
)

// writeArgs 是 write 的已解析参数。
type writeArgs struct {
	file       string
	content    string
	hasContent bool
	encoding   string
}

// parseWriteArgs 解析 write 的参数。
//
//	write -f FILE              # 从 stdin 读全文
//	write -f FILE -c CONTENT   # 或从参数读
//
// `-c ""` 是**合法**的：写一个空文件。所以用 hasContent 而不是
// `content != ""` 来判断，否则空文件这个用例永远表达不出来。
func parseWriteArgs(args []string) (*writeArgs, error) {
	a := &writeArgs{}
	// 简写：write FILE [CONTENT]
	shorthand := false
	if f, rest, ok := fileShorthand(args); ok {
		a.file, args, shorthand = config.ResolvePath(f), rest, true
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case s == "-f" || s == "--file":
			if i+1 >= len(args) {
				return nil, UsageErr("-f requires a path").
					WithHint(`write -f FILE -c "content"`)
			}
			i++
			a.file = config.ResolvePath(args[i])
		case strings.HasPrefix(s, "--file="):
			a.file = config.ResolvePath(s[len("--file="):])

		case s == "-c" || s == "--content":
			if i+1 >= len(args) {
				return nil, UsageErr("-c requires a value").
					WithHint(`write -f FILE -c "content"`)
			}
			i++
			a.content, a.hasContent = args[i], true
		case strings.HasPrefix(s, "--content="):
			a.content, a.hasContent = s[len("--content="):], true

		case s == "--encoding":
			if i+1 >= len(args) {
				return nil, UsageErr("--encoding requires a value").
					WithHint("write -f FILE --encoding gb18030 -c \"...\"")
			}
			i++
			a.encoding = args[i]
		case strings.HasPrefix(s, "--encoding="):
			a.encoding = s[len("--encoding="):]

		default:
			// 简写形态下，位置参数就是内容：write FILE CONTENT
			if shorthand && !a.hasContent {
				a.content, a.hasContent = s, true
				continue
			}
			return nil, UsageErr("unexpected argument %q", s).
				WithHint(`write FILE [CONTENT]   （等价写法：write -f FILE -c CONTENT）`)
		}
	}
	if a.file == "" {
		return nil, UsageErr("write: FILE is required").
			WithHint(`write src/new.ts "export const a = 1;"`)
	}
	return a, nil
}

// runWrite 是 write 的实现。
//
// 语义（plan §6.3）：
//   - 文件不存在 → 创建（**目录必须已存在**，否则 E_NO_DIR）
//   - 文件存在 → 全量替换（保留原文件的行尾风格与 BOM）
//   - 写完**自动 serve 全文**并回传锚点，省一次 read
//
// 自动 serve 全文在这里是安全的：内容就是模型自己给的，没有"意图 vs 现状"
// 的偏差可言 —— 与 edit 拒绝时回传行的处理恰好相反。
func runWrite(c *Ctx, args []string) error {
	a, err := parseWriteArgs(args)
	if err != nil {
		return err
	}

	abs, err := filepath.Abs(a.file)
	if err != nil {
		return &Error{Code: ExitUsage, Message: "resolve " + a.file + ": " + err.Error()}
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		return BizErr("E_IS_DIR  %s is a directory", displayPath(a.file))
	}

	// 目录必须已存在：rweg 只写文件，不替用户决定目录结构。
	dir := filepath.Dir(abs)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return BizErr("E_NO_DIR  %s does not exist (rweg does not create directories)",
			displayPath(dir)).
			WithHint("mkdir -p " + displayPath(dir) + " && retry")
	}

	content := a.content
	if !a.hasContent {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return &Error{Code: ExitBiz, Message: "E_IO  read stdin: " + err.Error()}
		}
		content = string(raw)
	} else if content != "" && !strings.HasSuffix(content, "\n") {
		// 命令行参数**无法可靠表达**"结尾没有换行"：shell 的命令替换
		// `$(printf 'a\nb')` 会把结尾换行吃掉，于是 write 出来的文件少一个换行，
		// 而所有正经文本文件都是以换行结尾的。所以 -c 的内容一律补一个。
		//
		// 要精确控制（比如刻意写一个不以换行结尾的文件），走 stdin：
		//   printf 'a\nb' | write -f f
		content += "\n"
	}

	// 用 docfile.Parse 走同一条加载路径：BOM 剥离、二进制探测、分行规则
	// 全部与 read/edit 一致。否则"写进去 3 行、读出来 4 行"这种事迟早发生。
	//
	// 注意这里解析的是**内存里的 UTF-8 文本**；最终落盘的字节由
	// RenderEncoded 按 doc.Encoding 编码 —— 编码只在最后一步起作用。
	doc, err := docfile.Parse(abs, []byte(content), int64(len(content)), docfile.Options{
		MaxLines:     c.Res.Cfg.Read.MaxLines,
		MaxLineBytes: c.Res.Cfg.Read.MaxLineBytes,
	})
	if err != nil {
		return translateDocError(err, a.file)
	}
	if a.encoding != "" {
		enc := docfile.NormalizeEncoding(a.encoding)
		if enc == "" {
			return UsageErr("E_UNKNOWN_ENCODING  unsupported encoding %q", a.encoding).
				WithHint("supported: utf-8, gb18030, gbk, big5, utf-16le, utf-16be")
		}
		doc.Encoding = enc
	}

	// 覆盖已有文件时继承它的编码、行尾风格与 BOM，避免把 CRLF/GBK 文件
	// 悄悄改写成 LF/UTF-8。
	existed := false
	if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
		existed = true
		if old, err := docfile.Load(abs, docfile.Options{
			MaxLines:     c.Res.Cfg.Read.MaxLines,
			MaxLineBytes: c.Res.Cfg.Read.MaxLineBytes,
		}); err == nil {
			doc.Newline = old.MajorityNewline()
			doc.MixedNewline = false
			doc.LineEnds = nil
			doc.HasBOM = doc.HasBOM || old.HasBOM
			if a.encoding == "" {
				doc.Encoding = old.Encoding
			}
		}
	}

	stride := c.Res.Cfg.Hash.ProbeStride
	reserved, err := c.DB.RetiredFor(abs)
	if err != nil {
		return &Error{Code: ExitInternal, Message: "write: " + err.Error()}
	}
	anchors := hashline.Pure(doc.Lines, stride, reserved)

	pre, preTooBig, preErr := preImage(abs, existed)
	if preErr != nil {
		pre, preTooBig = nil, true
	}

	out, err := doc.RenderEncoded(doc.Lines, nil)
	if err != nil {
		return translateDocError(err, a.file)
	}
	if err := writeFileAtomic(abs, out); err != nil {
		return &Error{Code: ExitBiz, Message: "E_WRITE  " + displayPath(a.file) + ": " + err.Error()}
	}
	recordUndo(c, abs, existed, "write", pre, preTooBig)

	// 全文 serve + 回填快照；旧指纹的快照顺手清掉（内容已整体换过）。
	canonVer := c.Res.Cfg.Hash.CanonVersion
	canons := make([]string, len(doc.Lines))
	for i, ln := range doc.Lines {
		canons[i] = hashline.Canon(ln)
	}
	fp := store.ContentFingerprint(doc.Lines)
	if err := c.DB.PutServed(abs, anchors, canons, canonVer, stride); err != nil {
		return &Error{Code: ExitInternal, Message: "write: persist served: " + err.Error()}
	}
	if err := c.DB.PutSnapshotByFingerprint(abs, anchors, fp, canonVer); err != nil {
		return &Error{Code: ExitInternal, Message: "write: cache snapshot: " + err.Error()}
	}
	if existed {
		if err := c.DB.PruneSnapshotsExcept(abs, fp); err != nil {
			return &Error{Code: ExitInternal, Message: "write: prune snapshots: " + err.Error()}
		}
	}

	if c.JSON {
		return emitWriteJSON(os.Stdout, doc, anchors, existed)
	}
	emitWriteText(os.Stdout, doc, anchors, existed, c.Quiet)
	return nil
}

// emitWriteText 渲染写成功的结果。
//
//	created src/new.ts (2 lines)
//	EPx│export const a = 1;
//	jDo│export const b = 2;
func emitWriteText(w io.Writer, doc *docfile.Doc, anchors []string, existed, quiet bool) {
	var b strings.Builder
	if !quiet {
		verb := "created"
		if existed {
			verb = "replaced"
		}
		n := len(doc.Lines)
		unit := "lines"
		if n == 1 {
			unit = "line"
		}
		if n == 0 {
			fmt.Fprintf(&b, "%s %s (empty)\n", verb, displayPath(doc.Path))
		} else {
			fmt.Fprintf(&b, "%s %s (%d %s)\n", verb, displayPath(doc.Path), n, unit)
		}
	}
	for i, ln := range doc.Lines {
		h := anchors[i]
		if doc.HasLongLine(i + 1) {
			h = strings.Repeat("~", hashline.HashLen)
		}
		b.WriteString(h + hashline.Sep + ln + "\n")
	}
	io.WriteString(w, b.String())
}

type writeJSON struct {
	Path      string            `json:"path"`
	Created   bool              `json:"created"`
	LineCount int               `json:"line_count"`
	Newline   string            `json:"newline"`
	Encoding  string            `json:"encoding"`
	Hashes    []string          `json:"hashes"`
	Meta      map[string]string `json:"meta,omitempty"`
}

func emitWriteJSON(w io.Writer, doc *docfile.Doc, anchors []string, existed bool) error {
	out := writeJSON{
		Path:      doc.Path,
		Created:   !existed,
		LineCount: len(doc.Lines),
		Newline:   doc.Newline,
		Encoding:  doc.Encoding,
		Hashes:    anchors,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
