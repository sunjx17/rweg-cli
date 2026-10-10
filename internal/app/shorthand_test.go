package app

import (
	"strings"
	"testing"
)

// 简写形态：第一个位置参数就是文件（read FILE / write FILE CONTENT /
// edit FILE FROM:TO CONTENT），已经给了 -f 时不参与判定。
func TestFileShorthand(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantFile string
		wantRest string
		wantOK   bool
	}{
		{"read 简写", []string{"src/app.ts", "1:3"}, "src/app.ts", "1:3", true},
		{"只有文件", []string{"src/app.ts"}, "src/app.ts", "", true},
		{"已给 -f", []string{"-f", "src/app.ts", "1:3"}, "", "-f src/app.ts 1:3", false},
		{"已给 --file=", []string{"--file=src/app.ts"}, "", "--file=src/app.ts", false},
		{"flag 打头", []string{"--encoding", "gbk", "f.ts"}, "", "--encoding gbk f.ts", false},
		{"无参数", nil, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, rest, ok := fileShorthand(tc.args)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK {
				if file != tc.wantFile {
					t.Errorf("file = %q, want %q", file, tc.wantFile)
				}
				if got := strings.Join(rest, " "); got != tc.wantRest {
					t.Errorf("rest = %q, want %q", got, tc.wantRest)
				}
			}
		})
	}
}

// read 简写要真的落到 file/specs 上，且 -f 写法不受影响。
func TestParseReadArgsShorthand(t *testing.T) {
	a, err := parseReadArgs([]string{"src/app.ts", "1:3", "7"})
	if err != nil {
		t.Fatal(err)
	}
	if a.file == "" || len(a.specs) != 2 {
		t.Fatalf("file=%q specs=%v", a.file, a.specs)
	}
	b, err := parseReadArgs([]string{"-f", "src/app.ts", "1:3"})
	if err != nil {
		t.Fatal(err)
	}
	if a.file != b.file || len(b.specs) != 1 {
		t.Fatalf("简写与 -f 写法结果不一致: %q/%v vs %q/%v", a.file, a.specs, b.file, b.specs)
	}
}

// write FILE CONTENT：位置参数就是内容；显式 -c 优先且不接受多余位置参数。
func TestParseWriteArgsShorthand(t *testing.T) {
	a, err := parseWriteArgs([]string{"out.txt", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if a.file == "" || !a.hasContent || a.content != "hello" {
		t.Fatalf("file=%q hasContent=%v content=%q", a.file, a.hasContent, a.content)
	}
	b, err := parseWriteArgs([]string{"out.txt", "-c", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !b.hasContent || b.content != "hello" || b.file != a.file {
		t.Fatalf("混用 -c 结果不一致: %+v", b)
	}
	if _, err := parseWriteArgs([]string{"out.txt", "a", "b"}); err == nil {
		t.Error("多余的位置参数应当报错")
	}
}

// edit FILE FROM:TO CONTENT [FROM:TO CONTENT ...]：位置参数成对出现。
func TestParseEditArgsShorthand(t *testing.T) {
	a, err := parseEditArgs([]string{"src/app.ts", "CTt:CTt", "one", "Um7", "two"})
	if err != nil {
		t.Fatal(err)
	}
	if a.file == "" || len(a.ops) != 2 {
		t.Fatalf("file=%q ops=%d", a.file, len(a.ops))
	}
	if a.ops[0].Replacement[0] != "one" || a.ops[1].Replacement[0] != "two" {
		t.Fatalf("ops = %+v", a.ops)
	}
	// 缺内容的成对参数：报用法错误，而不是把下一个范围当内容
	if _, err := parseEditArgs([]string{"src/app.ts", "CTt:CTt"}); err == nil {
		t.Error("缺内容应当报错")
	}
	// -c 形态不受简写影响
	b, err := parseEditArgs([]string{"-f", "src/app.ts", "-c", "CTt:CTt", "one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.ops) != 1 || b.ops[0].Replacement[0] != "one" {
		t.Fatalf("ops = %+v", b.ops)
	}
}

// undo 的路径参数：位置参数与 -f 等价，且归一成绝对路径（按路径查撤销栈）。
func TestParseUndoArgs(t *testing.T) {
	a, err := parseUndoArgs([]string{"src/app.ts"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseUndoArgs([]string{"-f", "src/app.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if a.file == "" || a.file != b.file {
		t.Fatalf("简写与 -f 结果不一致: %q vs %q", a.file, b.file)
	}
	c, err := parseUndoArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.file != "" {
		t.Errorf("不给 FILE 时应当是空（= 最近一次改动），got %q", c.file)
	}
	if _, err := parseUndoArgs([]string{"--nope"}); err == nil {
		t.Error("未知 flag 应当报错")
	}
}
