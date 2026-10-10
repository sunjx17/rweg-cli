package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"rweg/internal/config"
	"rweg/internal/docfile"
	"rweg/internal/hashline"
	"rweg/internal/ranges"
)

// grepArgs 是从 rg 参数里挑出来的信息。
//
// 设计原则：**能透传就透传**。rweg 只关心三件事——
// 「要 JSON」「要不要计数」「有没有明确文件路径」，
// 其余参数一律原样交给 rg，绝不尝试做一个 rg 参数解析器。
type grepArgs struct {
	// json 是 rweg 自己的 --json（输出我们的结构化结果）。
	json bool
	// forward 是原样交给 rg 的参数（已剔除 rweg 自己接管的 flag）。
	forward []string
	// raw 是未经处理的原始参数（回退透传时用）。
	raw []string
	// rgArgs 是 raw 的"rg 等价"形态：GNU 的 -E / --extended-regexp 已吃掉、
	// rweg 自己的 --json 已摘掉。回退到系统 grep 时用 raw —— 两个目标的
	// 短选项表不同：系统 grep 要靠 -E 才是 ERE，rg 的 -E 是 --encoding。
	rgArgs []string

	count bool // -c / --count
	files bool // -l / --files-with-matches
	quiet bool // -q / --quiet

	// err 是解析阶段就判定「不支持 / 不能按 GNU 语义收」的错误。
	err error

	// hasPath 表示用户明确指定了搜索路径。
	hasPath bool
	// pathCount 是路径位置参数的个数（-c 的输出格式要区分单/多文件）。
	pathCount int
}

// rgValueFlagsLong 是需要吃掉下一个参数的长 flag。
var rgValueFlagsLong = map[string]bool{
	"--regexp": true, "--file": true, "--type": true, "--type-not": true,
	"--glob": true, "--iglob": true, "--max-count": true,
	"--after-context": true, "--before-context": true, "--context": true,
	"--max-depth": true, "--max-columns": true, "--max-columns-preview": false,
	"--replace": true, "--engine": true, "--encoding": true,
	"--type-add": true, "--type-clear": true, "--sort": true, "--sortr": true,
	"--threads": true, "--dfa-size-limit": true, "--path-separator": true,
	"--pre": true, "--pre-glob": true, "--context-separator": true,
	"--field-context-separator": true, "--field-match-separator": true,
	"--colors": true, "--color": true, "--hyperlink-format": true,
	"--hostname-bin": true, "--ignore-file": true, "--ignore-file-case-insensitive": false,
	"--max-filesize": true, "--crlf": false, "--stats": false,
	"--pcre2-version": false, "--generate": true,
}

// rgValueFlagsShort 是需要吃值（或以紧跟字符为值）的短 flag。
var rgValueFlagsShort = map[byte]bool{
	'e': true, 'f': true, 't': true, 'T': true, 'g': true, 'm': true,
	'A': true, 'B': true, 'C': true, 'M': true, 'r': true, 'E': true,
	'j': true,
}

// rgPatternFlags 表示"模式已经通过 flag 给了"→ 位置参数全是路径。
var rgPatternFlags = map[string]bool{"-e": true, "--regexp": true, "-f": true, "--file": true}

// parseGrepArgs 把 rg 风格参数拆出来。
//
// 只做最小解析，目的是判断：
//  1. 用户是否指定了搜索路径（没有 → 认为在喂管道，整体透传）
//  2. 是不是计数 / 列文件 / 安静模式（这几种 rg 会绕过 --json，得特殊处理）
//
// 任何不认识的东西都进 forward，不做语义解释。
func parseGrepArgs(args []string) *grepArgs {
	a := &grepArgs{raw: append([]string{}, args...)}
	positionals := make([]string, 0, 4)
	posIdx := make([]int, 0, 4) // 每个位置参数在 forward 里的下标
	hasPatternFlag := false
	afterDD := false

	takeValue := func(i *int, bundled string) {
		if bundled != "" {
			return // 值已跟在短 flag 后面（-A2）
		}
		if *i+1 < len(args) {
			a.forward = append(a.forward, args[*i+1])
			*i++
		}
	}

	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case afterDD:
			// `--` 之后全是位置参数：既算进"有没有给路径"，
			// 也必须原样转给 rg —— 它们就是模式与路径本身。
			positionals = append(positionals, s)
			a.forward = append(a.forward, s)
			posIdx = append(posIdx, len(a.forward)-1)

		case s == "--":
			a.forward = append(a.forward, s)
			afterDD = true

		case strings.HasPrefix(s, "--"):
			name := s
			if eq := strings.Index(s, "="); eq >= 0 {
				name = s[:eq]
			}
			switch name {
			case "--extended-regexp":
				// GNU grep 的 -E：rg 的正则默认就是 ERE，交替与分组直接写就行，
				// 这里吃掉，别让 rg 把它当 --encoding（那才是 rg 的 -E）。
			case "--basic-regexp":
				a.err = errBasicRegexp(true)
				return a
			case "--json":
				a.json = true
				// 我们自己会加 --json，不回传
			case "--count":
				a.count = true
			case "--files-with-matches", "--files-without-match":
				a.files = true
				a.forward = append(a.forward, s)
			case "--quiet":
				a.quiet = true
				a.forward = append(a.forward, s)
			default:
				a.forward = append(a.forward, s)
				if rgPatternFlags[name] {
					hasPatternFlag = true
				}
				if rgValueFlagsLong[name] && !strings.Contains(s, "=") {
					takeValue(&i, "")
				}
			}

		case len(s) > 1 && s[0] == '-':
			// 可能是捆绑短 flag：-in / -A2 / -A 2 / -e pat
			//
			// `-c`（计数）必须从转发参数里摘掉：rg 一旦看到 -c 就绕过
			// --json 直接打印数字，我们就没法解析了。计数由我们自己数。
			//
			// 转发的**顺序**很要紧：`-A 2` 必须先给 rg 一个 `-A` 再给 `2`。
			// 早期实现把 flag 攒到最后统一 append，结果变成了 `2 -A`，
			// 于是 `-e pat -e pat file` 里的模式被当成路径（实测踩过）。
			out := make([]byte, 0, len(s))
			out = append(out, '-')
			for k := 1; k < len(s); k++ {
				ch := s[k]
				drop, gerr := gnuOnlyShortFlag(ch)
				if gerr != nil {
					a.err = gerr
					return a
				}
				if drop {
					continue
				}
				switch ch {
				case 'c':
					a.count = true
					continue
				case 'l':
					a.files = true
				case 'q':
					a.quiet = true
				}
				if rgValueFlagsShort[ch] {
					if ch == 'e' || ch == 'f' {
						hasPatternFlag = true
					}
					out = append(out, ch)
					if k+1 < len(s) {
						// 值紧跟（-A2）：flag 和值一起出去
						out = append(out, s[k+1:]...)
					} else {
						// 值在下一个参数：先把 flag 落地，再吃值
						a.forward = append(a.forward, string(out))
						out = nil
						takeValue(&i, "")
					}
					break
				}
				out = append(out, ch)
			}
			if len(out) > 1 {
				a.forward = append(a.forward, string(out))
			}

		default:
			positionals = append(positionals, s)
			a.forward = append(a.forward, s)
			posIdx = append(posIdx, len(a.forward)-1)
		}
	}

	// 位置参数里，模式 vs 路径的划分与 rg 一致：
	//   有 -e/-f → 全部位置参数都是路径
	//   否则     → 第一个是模式，其余是路径
	if hasPatternFlag {
		a.pathCount = len(positionals)
	} else if len(positionals) >= 2 {
		a.pathCount = len(positionals) - 1
	}
	// 路径位置参数交给原生 rg，必须先转成原生形态：MSYS 形态的 /tmp/x、/c/x
	// 在 MSYS_NO_PATHCONV=1 的 shell（dsh 等）里不会被转换，rg 会去找
	// C:\tmp\x 而报 os error 2。模式本身不能动（它可能长得像路径）。
	if a.pathCount > 0 {
		first := len(positionals) - a.pathCount
		for k := first; k < len(positionals); k++ {
			raw := positionals[k]
			if fixed := config.ResolvePath(raw); fixed != raw {
				a.forward[posIdx[k]] = fixed
			}
		}
	}
	// 回退透传（管道 / -q / -l / rg 起不来）要把参数交给 rg，得先换成
	// rg 的短选项表：-E 吃掉、--json 摘掉。
	rgArgs, gerr := rgArgsFor(args)
	if gerr != nil {
		a.err = gerr
		return a
	}
	a.rgArgs = rgArgs
	a.hasPath = a.pathCount > 0
	return a
}

// runGrep 是 grep 的实现。
//
// 四条路径（plan §6.5）：
//
//  1. 无路径参数（管道输入）       → 完全透传 rg
//  2. rg 不可用 / 执行失败         → 回退系统 grep，退出码一并透传
//  3. 正常，rg 退出码 0            → 解析 --json，转锚点格式并 serve 命中行
//  4. 其他退出码                   → 视为 rg 报错，回退默认行为
func runGrep(args []string) error {
	res, err := config.Load()
	if err != nil {
		return &Error{Code: ExitUsage, Message: err.Error(),
			Hint: "unset RWEG_CONFIG, or point it at an existing rweg.toml"}
	}

	a := parseGrepArgs(args)
	if a.err != nil {
		return a.err
	}

	// 计数/列文件/安静：都不产出可锚定的行，直接透传（保留 rg 的原生行为）。
	if a.quiet || a.files {
		return passthroughGrep(a.raw, a.rgArgs)
	}
	// 没有明确路径 → 用户在喂管道，或者在整目录递归搜；
	// 这两种情况锚点都无从谈起（命中的可能是 stdin），透传。
	if !a.hasPath {
		return passthroughGrep(a.raw, a.rgArgs)
	}

	rg, ok := rgBinary(res)
	if !ok {
		return passthroughGrep(a.raw, a.rgArgs)
	}

	argv := append([]string{"--json"}, a.forward...)
	cmd := exec.Command(rg, argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Stdin = os.Stdin

	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			code = ee.ExitCode()
		} else {
			// 起不来（rg_path 指错、权限不够）→ 回退
			return passthroughGrep(a.raw, a.rgArgs)
		}
	}

	switch code {
	case 0:
		// 继续解析
	case 1:
		// 无匹配：rg 的 stderr 照给，退出码 1。没有任何锚点要输出。
		os.Stderr.Write(stderr.Bytes())
		os.Exit(int(ExitBiz))
	default:
		// rg 自己报错（非法 flag / 文件不存在……）：整段透传，别自作聪明。
		os.Stderr.Write(stderr.Bytes())
		os.Exit(code)
	}

	files, err := parseRJSON(stdout.Bytes())
	if err != nil {
		return &Error{Code: ExitInternal, Message: "grep: parse rg --json: " + err.Error()}
	}
	if len(files) == 0 {
		os.Exit(int(ExitBiz))
	}

	// 只有在确实有命中行要锚定时才开库 —— 这条路径开销最大，
	// 前面所有回退分支都不该被它拖累（也避免无谓地建库）。
	c, err := openWith(ToolGrep, flagSet{json: a.json})
	if err != nil {
		return err
	}
	defer c.Close()

	if a.count {
		return emitGrepCount(os.Stdout, c, files, a.pathCount, a.json)
	}
	return emitGrepAnchored(os.Stdout, c, files)
}

// rgBinary 定位可用的 ripgrep。
//
// RWEG_RG_MISSING 是给测试用的开关（场景 H 要求能模拟"rg 不可用"）。
func rgBinary(res config.Resolved) (string, bool) {
	if os.Getenv("RWEG_RG_MISSING") != "" {
		return "", false
	}
	if p := res.Cfg.Grep.RgPath; p != "" {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() {
			return "", false
		}
		if sameFile(p, currentExe()) {
			return "", false
		}
		return p, true
	}
	p, err := exec.LookPath("rg")
	if err != nil || p == "" {
		return "", false
	}
	if sameFile(p, currentExe()) {
		return "", false
	}
	return p, true
}

// --- rg --json 解析 ---

type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber *int `json:"line_number"`
	} `json:"data"`
}

// hitFile 是一个命中文件。
type hitFile struct {
	Path string
	Hits []hitLine
}

type hitLine struct {
	Line int
	Text string
}

// parseRJSON 把 rg --json 的事件流折叠成"文件 → 命中行"。
//
// context 事件（-C/-A/-B 产生）也算命中行：用户要了上下文，
// 就得把上下文给全，否则 `-C2` 会静默变成"只给命中行"。
func parseRJSON(data []byte) ([]hitFile, error) {
	var out []hitFile
	index := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev rgEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			// 单行坏掉不该毁掉整次输出（rg 偶尔会掺入非 JSON 提示）
			continue
		}
		if ev.Type != "match" && ev.Type != "context" {
			continue
		}
		p := ev.Data.Path.Text
		if p == "" || ev.Data.LineNumber == nil {
			continue
		}
		i, ok := index[p]
		if !ok {
			i = len(out)
			index[p] = i
			out = append(out, hitFile{Path: p})
		}
		out[i].Hits = append(out[i].Hits, hitLine{
			Line: *ev.Data.LineNumber,
			Text: strings.TrimRight(ev.Data.Lines.Text, "\r\n"),
		})
	}
	return out, sc.Err()
}

// --- 输出 ---

// emitGrepAnchored 给每个命中行加锚点，并把命中行写入 served。
func emitGrepAnchored(w io.Writer, c *Ctx, files []hitFile) error {
	var b strings.Builder
	first := true
	for _, f := range files {
		abs := absPath(f.Path, c.Res.Cwd)
		doc, anchors, canons, err := loadForGrep(c, abs)
		if err != nil {
			// 加载不了（二进制 / 编码识别不了）：仍然把行给出去，
			// 只是没有锚点 —— 比整条 grep 失败有用。
			if !first {
				b.WriteString("\n")
			}
			first = false
			fmt.Fprintf(&b, "# %s (%s)\n", displayPath(abs), matchCount(f.Hits))
			for _, h := range f.Hits {
				fmt.Fprintf(&b, "%d:%s%s%s\n", h.Line, strings.Repeat("~", hashline.HashLen),
					hashline.Sep, h.Text)
			}
			continue
		}

		// 命中行（含 -C 的上下文行）全部 serve → 锚点可直接用于 edit。
		set := ranges.NewSet(len(anchors))
		for _, h := range f.Hits {
			set.Add(h.Line)
		}
		if err := c.DB.ServeLines(abs, set, anchors, canons,
			c.Res.Cfg.Hash.CanonVersion, c.Res.Cfg.Hash.ProbeStride); err != nil {
			return &Error{Code: ExitInternal, Message: "grep: serve: " + err.Error()}
		}

		if !first {
			b.WriteString("\n")
		}
		first = false
		fmt.Fprintf(&b, "# %s (%s)\n", displayPath(abs), matchCount(f.Hits))
		for _, h := range f.Hits {
			text := h.Text
			if h.Line >= 1 && h.Line <= len(doc.Lines) {
				text = doc.Lines[h.Line-1]
			}
			hash := ""
			if h.Line >= 1 && h.Line <= len(anchors) {
				hash = anchors[h.Line-1]
			}
			if hash == "" || doc.HasLongLine(h.Line) {
				hash = strings.Repeat("~", hashline.HashLen)
			}
			fmt.Fprintf(&b, "%d:%s%s%s\n", h.Line, hash, hashline.Sep, text)
		}
	}
	io.WriteString(w, b.String())
	return nil
}

// emitGrepCount 输出计数（-c）。
//
// rg 在 -c 下绕过 --json，所以计数由我们自己数。
// 格式与 rg 对齐：单文件只给数字，多文件给 `path:count`。
func emitGrepCount(w io.Writer, c *Ctx, files []hitFile, pathCount int, asJSON bool) error {
	single := pathCount == 1 && len(files) == 1
	if asJSON {
		type countJSON struct {
			Path  string `json:"path"`
			Count int    `json:"count"`
		}
		out := make([]countJSON, 0, len(files))
		for _, f := range files {
			out = append(out, countJSON{Path: absPath(f.Path, c.Res.Cwd), Count: len(f.Hits)})
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	for _, f := range files {
		if single {
			fmt.Fprintf(w, "%d\n", len(f.Hits))
			continue
		}
		fmt.Fprintf(w, "%s:%d\n", displayPath(absPath(f.Path, c.Res.Cwd)), len(f.Hits))
	}
	return nil
}

func matchCount(hits []hitLine) string {
	n := len(hits)
	if n == 1 {
		return "1 match"
	}
	return strconv.Itoa(n) + " matches"
}

// loadForGrep 加载文件并算出锚点/ canons。
func loadForGrep(c *Ctx, abs string) (*docfile.Doc, []string, []string, error) {
	doc, err := docfile.Load(abs, docfile.Options{
		MaxLines:     c.Res.Cfg.Read.MaxLines,
		MaxLineBytes: c.Res.Cfg.Read.MaxLineBytes,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	anchors, err := anchorsFor(c, doc)
	if err != nil {
		return nil, nil, nil, err
	}
	canons := make([]string, len(doc.Lines))
	for i, ln := range doc.Lines {
		canons[i] = hashline.Canon(ln)
	}
	return doc, anchors, canons, nil
}

// absPath 把 rg 给的路径解析成绝对路径（rg 给的是相对于 cwd 的短路径）。
func absPath(p, cwd string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(cwd, p))
}
