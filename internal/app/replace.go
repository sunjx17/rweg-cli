package app

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"rweg/internal/config"
	"rweg/internal/docfile"
	"rweg/internal/edit"
	"rweg/internal/ranges"
)

// replaceOp 是 replace 的一条改动：在 [from, to] 圈定的行内做替换。
//
// 与 edit 的 -c 一样可重复：一次调用多组改动，全部原子生效。
type replaceOp struct {
	from, to string
	pattern  string
	repl     string
	regex    bool
	re       *regexp.Regexp // regex 为真时已编译好
}

// replaceArgs 是 replace 的已解析参数。
type replaceArgs struct {
	file string
	ops  []replaceOp
}

// parseReplaceArgs 解析 replace 的参数。
//
//	replace -f FILE -c  ANCHOR OLDSTR       REPLACEMENT [-c ANCHOR ... ...]
//	replace -f FILE -cr ANCHOR REGEX        REPLACEMENT [-cr ANCHOR ... ...]
//
// 文件必须用 -f / --file 点名：本工具的旗标每组要吃三个值
// （锚点 + 一条文本 + 另一条文本），位置参数没有可识别的形状，
// 所以不做 `FILE` 放最前的简写（其他工具那样写，这里会直接报缺 FILE）。
// --content 与 -c 同义、--change-regex 与 -cr 同义（与 ctx.flagValueArity 对齐）。
func parseReplaceArgs(args []string) (*replaceArgs, error) {
	usage := `replace -f FILE -c ANCHOR OLDSTR "REPLACEMENT" [-c ANCHOR OLDSTR "REPLACEMENT" ...]   /   ` +
		`replace -f FILE -cr ANCHOR REGEX "REPLACEMENT" [-cr ...]`

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

		case s == "-c" || s == "--change" || s == "--content" || s == "-cr" || s == "--change-regex":
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
			op := replaceOp{
				from: from, to: to,
				pattern: pattern, repl: repl,
				regex: s == "-cr" || s == "--change-regex",
			}
			// 空模式必须拦：精确模式下它会匹配每个位置，正则模式下同样到处命中，
			// 结果是把替换文本插满整行 —— 几乎不会是调用者的本意。
			if op.pattern == "" {
				code := "E_BAD_PATTERN"
				if op.regex {
					code = "E_BAD_REGEX"
				}
				return nil, UsageErr("%s  -c #%d: empty pattern", code, len(a.ops)+1).WithHint(usage)
			}
			if op.regex {
				re, err := regexp.Compile(op.pattern)
				if err != nil {
					return nil, UsageErr("E_BAD_REGEX  %s: %v", op.pattern, err).
						WithHint(`replace 用 Go 正则（RE2），子匹配在 REPLACEMENT 里写 $1 / ${name}`)
				}
				op.re = re
			}
			a.ops = append(a.ops, op)
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
	if len(a.ops) == 0 {
		return nil, UsageErr("replace: at least one -c / -cr change is required").WithHint(usage)
	}
	return a, nil
}

// replaceScope 是一条改动加上它已解析的行区间。
type replaceScope struct {
	op  replaceOp
	rng ranges.Range
}

// runReplace 是 replace 的实现。
//
// 流程：加载 → 算锚点 → 逐组校验区间（edit.ResolveRange）→ 拒重叠
// → 逐行替换 → 有变化才把变化行表达成 edit.Op 交给 applyEdit。
//
// 落盘 / 撤销 / served 重建 / diff 输出全部沿用 applyEdit，
// 于是 replace 与 edit 在锚点语义、原子性、编码保真上完全一致；
// 区别只在"新内容怎么算出来的"：由工具算，不需要模型复述上下文行。
// 多组改动与 edit 同规：任一组校验失败 → 什么都不写。
func runReplace(c *Ctx, args []string) error {
	a, err := parseReplaceArgs(args)
	if err != nil {
		return err
	}

	doc, err := docfile.Load(a.file, docfile.Options{
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

	// 每组区间整体先过校验链：任何一行未 serve / 锚点不可解析 / 与基线不符
	// 都在这里被拒。不能只校验"最终变了的那些行" —— 那等于拿没见过的行当作用域。
	scopes := make([]replaceScope, 0, len(a.ops))
	for i := range a.ops {
		rng, fail := edit.ResolveRange(sv, cur, a.ops[i].from, a.ops[i].to, c.Res.Cfg.Edit.ServedEchoCap)
		if fail != nil {
			return editFailure(fail, doc, c, anchors)
		}
		scopes = append(scopes, replaceScope{op: a.ops[i], rng: rng})
	}

	// 区间互不重叠：两组改同一行时"先替谁"没有定义，与 edit 的 -c 同规硬拒。
	sort.SliceStable(scopes, func(i, j int) bool { return scopes[i].rng.Start < scopes[j].rng.Start })
	for k := 1; k < len(scopes); k++ {
		if scopes[k].rng.Start <= scopes[k-1].rng.End {
			return UsageErr("E_OVERLAP  %s: -c #%d range %s overlaps -c #%d (%s)",
				displayPath(doc.Path), k+1, scopes[k].rng.String(), k, scopes[k-1].rng.String()).
				WithHint("一次调用的多组改动不许重叠：合并区间，或拆成两次调用")
		}
	}

	ops, _, _, err := replaceOps(scopes, doc, cur)
	if err != nil {
		return err
	}

	return applyEdit(c, a.file, ops, "replaced")
}

// replaceOps 在各组区间内逐行替换，返回 edit.Op、命中次数与真正改变的行数。
//
// **每组区间必须命中且必须产生变化**（与单组形态同规，也是原子语义的一半）：
// 任何一组零命中 → E_NO_MATCH、命中但文本没变 → E_NO_CHANGE，整单拒绝、
// 不写盘 —— 否则多组改动会静默地"应用了一半"，正是 -c 可重复之后
// 最要防的坑。命中数与改变行数是全部组之和。
//
// 只给**变了的行**造 op：没变的行不进 op，于是 diff 里不会出现它们，
// 它们的锚点也照旧保留（edit.Apply 的 retire 只覆盖 op 的区间）。
// 一行变空（如把整行内容替掉）用 []string{""} 表达，那是"一个空行"而非删除。
//
// 正则的零长匹配（x*、(a)? 这类）在这里拦下：它们会在一行里的每个字符之间
// 各插一遍替换文本，几乎不会是调用者的本意 —— 与空模式同罪，整单拒绝、
// 不写盘。行首/行尾锚（^ / $，各只有一个空命中位置）不受影响。
func replaceOps(scopes []replaceScope, doc *docfile.Doc, cur *edit.Current) (ops []edit.Op, hits, changed int, err error) {
	path := displayPath(doc.Path)
	for si := range scopes {
		sc := &scopes[si]
		opHits, opChanged := 0, 0
		for ln := sc.rng.Start; ln <= sc.rng.End; ln++ {
			old := doc.Lines[ln-1]

			var out string
			var n int
			if sc.op.re != nil {
				found := sc.op.re.FindAllString(old, -1)
				if len(found) == 0 {
					continue
				}
				zero := 0
				for _, m := range found {
					if m == "" {
						zero++
					}
				}
				if zero > 1 {
					return nil, 0, 0, UsageErr(
						"E_BAD_REGEX  -c #%d: %q matches the empty string at %d positions in line %d",
						si+1, sc.op.pattern, zero, ln).
						WithHint("it would splice the replacement between characters; " +
							"anchor the match (a+ rather than a*), or give the regex non-empty context")
				}
				n = len(found)
				out = sc.op.re.ReplaceAllString(old, sc.op.repl)
			} else {
				n = strings.Count(old, sc.op.pattern)
				if n == 0 {
					continue
				}
				out = strings.ReplaceAll(old, sc.op.pattern, sc.op.repl)
			}
			opHits += n
			if out == old {
				continue
			}
			opChanged++
			h := cur.Hashes[ln-1]
			ops = append(ops, edit.Op{From: h, To: h, Replacement: []string{out}})
		}

		// 多组时给每条消息带上组号；单组保持原形态。
		label := ""
		if len(scopes) > 1 {
			label = fmt.Sprintf("-c #%d ", si+1)
		}
		if opHits == 0 {
			return nil, 0, 0, BizErr("E_NO_MATCH  %s  %s%q matches nothing in lines %d-%d",
				path, label, sc.op.pattern, sc.rng.Start, sc.rng.End).
				WithHint("check the pattern, or read the range to see the current text: read -f " +
					path + " " + sc.rng.String())
		}
		if opChanged == 0 {
			return nil, 0, 0, BizErr("E_NO_CHANGE  %s  %s%d match(es) in lines %d-%d already read %q",
				path, label, opHits, sc.rng.Start, sc.rng.End, sc.op.pattern).
				WithHint("nothing was written; the replacement produces the same text")
		}
		hits += opHits
		changed += opChanged
	}
	return ops, hits, changed, nil
}
