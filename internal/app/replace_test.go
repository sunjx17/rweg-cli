package app

import (
	"regexp"
	"strings"
	"testing"

	"rweg/internal/docfile"
	"rweg/internal/edit"
	"rweg/internal/ranges"
)

func TestParseReplaceArgs(t *testing.T) {
	t.Run("单组 -c", func(t *testing.T) {
		a, err := parseReplaceArgs([]string{"-f", "a.ts", "-c", "CTt:AU6", "old", "new"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if a.file != "a.ts" || len(a.ops) != 1 {
			t.Fatalf("got %+v", a)
		}
		op := a.ops[0]
		if op.from != "CTt" || op.to != "AU6" || op.pattern != "old" || op.repl != "new" || op.regex {
			t.Fatalf("op = %+v", op)
		}
	})

	t.Run("无冒号锚点 = 单行", func(t *testing.T) {
		a, err := parseReplaceArgs([]string{"-f", "a.ts", "-cr", "nVR", "x+", "y"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if a.ops[0].from != "nVR" || a.ops[0].to != "nVR" || !a.ops[0].regex || a.ops[0].re == nil {
			t.Fatalf("op = %+v", a.ops[0])
		}
	})

	t.Run("-c 可重复：多组改动各自保留", func(t *testing.T) {
		a, err := parseReplaceArgs([]string{
			"-f", "a.ts",
			"-c", "CTt", "foo", "F",
			"-cr", "AU6:yAZ", "x+", "X",
			"-c", "6PY", "bar", "B",
		})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(a.ops) != 3 {
			t.Fatalf("want 3 ops, got %d: %+v", len(a.ops), a.ops)
		}
		if a.ops[0].pattern != "foo" || a.ops[0].regex {
			t.Fatalf("op1 = %+v", a.ops[0])
		}
		if a.ops[1].from != "AU6" || a.ops[1].to != "yAZ" || !a.ops[1].regex {
			t.Fatalf("op2 = %+v（-cr 只作用于自己这一组）", a.ops[1])
		}
		if a.ops[2].pattern != "bar" || a.ops[2].regex {
			t.Fatalf("op3 = %+v", a.ops[2])
		}
	})

	t.Run("--content 是 -c 的别名", func(t *testing.T) {
		a, err := parseReplaceArgs([]string{"-f", "a.ts", "--content", "CTt", "x", "y"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if a.ops[0].pattern != "x" || a.ops[0].repl != "y" || a.ops[0].regex {
			t.Fatalf("op = %+v", a.ops[0])
		}
	})

	t.Run("重复 -c 不再静默覆盖（曾报的高危问题）", func(t *testing.T) {
		a, err := parseReplaceArgs([]string{"-f", "a.ts", "-c", "CTt", "a", "b", "-c", "AU6", "c", "d"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(a.ops) != 2 || a.ops[0].pattern == a.ops[1].pattern {
			t.Fatalf("两组都要在：%+v", a.ops)
		}
	})

	t.Run("缺 -f", func(t *testing.T) {
		// 位置参数开头 → 提示必须用 -f 点名
		_, err := parseReplaceArgs([]string{"nVR", "a", "b"})
		if err == nil || !strings.Contains(err.Error(), "FILE must be named with -f") {
			t.Fatalf("err = %v", err)
		}
		// 只有 -c 走到收尾 → 提示缺 FILE
		_, err = parseReplaceArgs([]string{"-c", "CTt", "a", "b"})
		if err == nil || !strings.Contains(err.Error(), "FILE is required") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("没给任何 -c", func(t *testing.T) {
		_, err := parseReplaceArgs([]string{"-f", "a.ts"})
		if err == nil || !strings.Contains(err.Error(), "at least one -c") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("-c 值不全", func(t *testing.T) {
		for _, args := range [][]string{
			{"-f", "a.ts", "-c", "CTt", "a"},
			{"-f", "a.ts", "-c", "CTt"},
			{"-f", "a.ts", "-cr", "CTt"},
		} {
			if _, err := parseReplaceArgs(args); err == nil ||
				!strings.Contains(err.Error(), "requires ANCHOR") {
				t.Fatalf("%v: err = %v", args, err)
			}
		}
	})

	t.Run("空模式", func(t *testing.T) {
		if _, err := parseReplaceArgs([]string{"-f", "a.ts", "-c", "CTt", "", "x"}); err == nil ||
			!strings.Contains(err.Error(), "E_BAD_PATTERN") {
			t.Fatalf("err = %v", err)
		}
		if _, err := parseReplaceArgs([]string{"-f", "a.ts", "-cr", "CTt", "", "x"}); err == nil ||
			!strings.Contains(err.Error(), "E_BAD_REGEX") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("非法正则", func(t *testing.T) {
		if _, err := parseReplaceArgs([]string{"-f", "a.ts", "-cr", "CTt", "(", "x"}); err == nil ||
			!strings.Contains(err.Error(), "E_BAD_REGEX") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestReplaceOps 钉住逐行替换的核心账目：命中数、改变行数、op 的形状。
func TestReplaceOps(t *testing.T) {
	mkScope := func(op replaceOp, start, end int) replaceScope {
		return replaceScope{op: op, rng: ranges.Range{Start: start, End: end}}
	}
	doc := &docfile.Doc{Lines: []string{
		"aaa aaa",
		"bbb",
		"",
		"const x = 1;",
	}}
	cur := edit.NewCurrent(doc.Lines, []string{"A1", "B2", "C3", "D4"})

	t.Run("一行多处命中", func(t *testing.T) {
		ops, hits, changed, err := replaceOps(
			[]replaceScope{mkScope(replaceOp{pattern: "aaa", repl: "z"}, 1, 1)}, doc, cur)
		if err != nil {
			t.Fatalf("replaceOps: %v", err)
		}
		if hits != 2 || changed != 1 {
			t.Fatalf("hits=%d changed=%d", hits, changed)
		}
		if len(ops) != 1 || ops[0].From != "A1" || len(ops[0].Replacement) != 1 ||
			ops[0].Replacement[0] != "z z" {
			t.Fatalf("ops = %+v", ops)
		}
	})

	t.Run("整行替空 = 空行而非删行", func(t *testing.T) {
		ops, hits, changed, err := replaceOps(
			[]replaceScope{mkScope(replaceOp{pattern: "bbb", repl: ""}, 2, 2)}, doc, cur)
		if err != nil {
			t.Fatalf("replaceOps: %v", err)
		}
		if hits != 1 || changed != 1 || len(ops[0].Replacement) != 1 || ops[0].Replacement[0] != "" {
			t.Fatalf("hits=%d changed=%d ops=%+v", hits, changed, ops)
		}
	})

	t.Run("命中但没变 → E_NO_CHANGE", func(t *testing.T) {
		_, _, _, err := replaceOps(
			[]replaceScope{mkScope(replaceOp{pattern: "bbb", repl: "bbb"}, 2, 2)}, doc, cur)
		if err == nil || !strings.Contains(err.Error(), "E_NO_CHANGE") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("多组改动合账", func(t *testing.T) {
		ops, hits, changed, err := replaceOps([]replaceScope{
			mkScope(replaceOp{pattern: "aaa", repl: "z"}, 1, 1),
			mkScope(replaceOp{pattern: "const x = 1;", repl: "const x = 2;"}, 4, 4),
		}, doc, cur)
		if err != nil {
			t.Fatalf("replaceOps: %v", err)
		}
		if len(ops) != 2 || hits != 3 || changed != 2 {
			t.Fatalf("ops=%d hits=%d changed=%d（aaa 一行两处 + const 一处）", len(ops), hits, changed)
		}
		if ops[1].From != "D4" {
			t.Fatalf("op2 = %+v", ops[1])
		}
	})

	t.Run("多组中一组零命中 → 整单 E_NO_MATCH（不静默应用一半）", func(t *testing.T) {
		_, _, _, err := replaceOps([]replaceScope{
			mkScope(replaceOp{pattern: "aaa", repl: "z"}, 1, 1),
			mkScope(replaceOp{pattern: "zzznope", repl: "Q"}, 4, 4),
		}, doc, cur)
		if err == nil || !strings.Contains(err.Error(), "E_NO_MATCH") {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(err.Error(), "-c #2") {
			t.Fatalf("多组消息要带组号: %v", err)
		}
	})

	t.Run("多组中一组命中但没变 → 整单 E_NO_CHANGE", func(t *testing.T) {
		_, _, _, err := replaceOps([]replaceScope{
			mkScope(replaceOp{pattern: "aaa", repl: "z"}, 1, 1),
			mkScope(replaceOp{pattern: "bbb", repl: "bbb"}, 2, 2),
		}, doc, cur)
		if err == nil || !strings.Contains(err.Error(), "E_NO_CHANGE") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("区间内未命中的行跳过", func(t *testing.T) {
		_, hits, changed, err := replaceOps(
			[]replaceScope{mkScope(replaceOp{pattern: "aaa", repl: "z"}, 1, 3)}, doc, cur)
		if err != nil {
			t.Fatalf("replaceOps: %v", err)
		}
		if hits != 2 || changed != 1 {
			t.Fatalf("hits=%d changed=%d", hits, changed)
		}
	})

	t.Run("零长匹配的正则整单拒绝", func(t *testing.T) {
		// x* 在 "bbb" 上产生 4 个空命中 —— 会把替换文本插进每个字符之间
		_, _, _, err := replaceOps(
			[]replaceScope{mkScope(replaceOp{pattern: "x*", repl: "Y", regex: true,
				re: compileMust(t, "x*")}, 2, 2)}, doc, cur)
		if err == nil || !strings.Contains(err.Error(), "E_BAD_REGEX") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("行首锚 ^（单个空命中位置）放行", func(t *testing.T) {
		ops, hits, changed, err := replaceOps(
			[]replaceScope{mkScope(replaceOp{pattern: "^", repl: "# ", regex: true,
				re: compileMust(t, "^")}, 2, 2)}, doc, cur)
		if err != nil {
			t.Fatalf("^ 应放行: %v", err)
		}
		if hits != 1 || changed != 1 || ops[0].Replacement[0] != "# bbb" {
			t.Fatalf("hits=%d changed=%d ops=%+v", hits, changed, ops)
		}
	})
}

func compileMust(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return re
}
