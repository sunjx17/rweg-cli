package app

import (
	"regexp"
	"strings"

	"rweg/internal/config"
	"rweg/internal/docfile"
	"rweg/internal/edit"
	"rweg/internal/ranges"
)

// replaceArgs 是 replace 的已解析参数。
//
// ANCHOR 是单行锚或 `FROM:TO` 区间；PATTERN / REPLACEMENT 随 -c（精确子串）
// 或 -cr（Go 正则）给出。
type replaceArgs struct {
	file    string
	from    string
	to      string
	pattern string
	repl    string
	regex   bool
}

// parseReplaceArgs 解析 replace 的参数。
//
//	replace -f FILE -c  ANCHOR OLDSTR       REPLACEMENT
//	replace -f FILE -cr ANCHOR REGEX        REPLACEMENT
//
// 文件必须用 -f / --file 点名：本工具的两个旗标都要吃三个值
// （锚点 + 一条文本 + 另一条文本），位置参数没有可识别的形状，
// 所以不做 `FILE` 放最前的简写（其他工具那样写，这里会直接报缺 FILE）。
func parseReplaceArgs(args []string) (*replaceArgs, error) {
	usage := `replace -f FILE -c ANCHOR OLDSTR "REPLACEMENT"   /   ` +
		`replace -f FILE -cr ANCHOR REGEX "REPLACEMENT"`

	a := &replaceArgs{}
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

		case s == "-c" || s == "--change" || s == "-cr" || s == "--change-regex":
			if i+3 >= len(args) {
				return nil, UsageErr("%s requires ANCHOR, a pattern and a replacement", s).
					WithHint(usage).
					WithDetails(`hint: give "" as the replacement to delete every match`)
			}
			spec, pattern, repl := args[i+1], args[i+2], args[i+3]
			from, to, err := splitAnchorSpec(spec)
			if err != nil {
				return nil, err
			}
			a.from, a.to, a.pattern, a.repl = from, to, pattern, repl
			a.regex = s == "-cr" || s == "--change-regex"
			i += 3

		default:
			if a.file == "" {
				return nil, UsageErr("replace: FILE must be named with -f").WithHint(usage).
					WithDetails("hint: 与 read / write / edit 不同，replace 的位置参数是锚点与两条文本，文件只能用 -f 点名")
			}
			return nil, UsageErr("unexpected argument %q", s).WithHint(usage)
		}
	}

	if a.file == "" {
		return nil, UsageErr("replace: FILE is required").WithHint(usage)
	}
	if a.from == "" {
		return nil, UsageErr("replace: ANCHOR is required").WithHint(usage)
	}
	// 空模式必须拦：精确模式下它会匹配每个位置，正则模式下同样到处命中，
	// 结果是把替换文本插满整行 —— 几乎不会是调用者的本意。
	if a.pattern == "" {
		code := "E_BAD_PATTERN"
		if a.regex {
			code = "E_BAD_REGEX"
		}
		return nil, UsageErr("%s  empty pattern", code).WithHint(usage)
	}
	return a, nil
}

// runReplace 是 replace 的实现。
//
// 流程：加载 → 算锚点 → 校验区间（edit.ResolveRange）→ 逐行替换
// → 有变化才把变化行表达成 edit.Op 交给 applyEdit。
//
// 落盘 / 撤销 / served 重建 / diff 输出全部沿用 applyEdit，
// 于是 replace 与 edit 在锚点语义、原子性、编码保真上完全一致；
// 区别只在"新内容怎么算出来的"：由工具算，不需要模型复述上下文行。
func runReplace(c *Ctx, args []string) error {
	a, err := parseReplaceArgs(args)
	if err != nil {
		return err
	}

	var re *regexp.Regexp
	if a.regex {
		re, err = regexp.Compile(a.pattern)
		if err != nil {
			return UsageErr("E_BAD_REGEX  %s: %v", a.pattern, err).
				WithHint(`replace 用 Go 正则（RE2），子匹配在 REPLACEMENT 里写 $1 / ${name}`)
		}
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

	// 区间整体先过校验链：任何一行未 serve / 锚点不可解析 / 与基线不符都在这里被拒。
	// 不能只校验"最终变了的那些行" —— 那等于拿没见过的行当作用域。
	rng, fail := edit.ResolveRange(sv, cur, a.from, a.to, c.Res.Cfg.Edit.ServedEchoCap)
	if fail != nil {
		return editFailure(fail, doc, c, anchors)
	}

	ops, hits, changed := replaceOps(a, re, doc, cur, rng)
	if changed == 0 {
		path := displayPath(doc.Path)
		if hits == 0 {
			return BizErr("E_NO_MATCH  %s  %q matches nothing in lines %d-%d",
				path, a.pattern, rng.Start, rng.End).
				WithHint("check the pattern, or read the range to see the current text: read -f " +
					path + " " + rng.String())
		}
		return BizErr("E_NO_CHANGE  %s  %d match(es) in lines %d-%d already read %q",
			path, hits, rng.Start, rng.End, a.pattern).
			WithHint("nothing was written; the replacement produces the same text")
	}

	return applyEdit(c, a.file, ops, "replaced")
}

// replaceOps 在区间内逐行替换，返回 edit.Op、命中次数与真正改变的行数。
//
// 只给**变了的行**造 op：没变的行不进 op，于是 diff 里不会出现它们，
// 它们的锚点也照旧保留（edit.Apply 的 retire 只覆盖 op 的区间）。
// 一行变空（如把整行内容替掉）用 []string{""} 表达，那是"一个空行"而非删除。
func replaceOps(a *replaceArgs, re *regexp.Regexp, doc *docfile.Doc,
	cur *edit.Current, rng ranges.Range) (ops []edit.Op, hits, changed int) {
	for ln := rng.Start; ln <= rng.End; ln++ {
		old := doc.Lines[ln-1]

		var out string
		if re != nil {
			found := re.FindAllString(old, -1)
			if len(found) == 0 {
				continue
			}
			hits += len(found)
			out = re.ReplaceAllString(old, a.repl)
		} else {
			n := strings.Count(old, a.pattern)
			if n == 0 {
				continue
			}
			hits += n
			out = strings.ReplaceAll(old, a.pattern, a.repl)
		}
		if out == old {
			continue
		}
		changed++
		h := cur.Hashes[ln-1]
		ops = append(ops, edit.Op{From: h, To: h, Replacement: []string{out}})
	}
	return ops, hits, changed
}
