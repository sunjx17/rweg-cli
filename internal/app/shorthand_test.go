package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rweg/internal/docfile"
	"rweg/internal/hashline"
	"rweg/internal/ranges"
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

// -fn / --no-anchor：从参数里摘掉文件路径，其余原样保留。
func TestStripNoAnchor(t *testing.T) {
	cases := []struct {
		name     string
		args     string
		wantFile string
		wantRest string
		wantOK   bool
	}{
		{"-fn 简写", "-fn src/app.ts 1:3", "src/app.ts", "1:3", true},
		{"长写法带空格", "--no-anchor src/app.ts", "src/app.ts", "", true},
		{"长写法带 =", "--no-anchor=src/app.ts 1:3", "src/app.ts", "1:3", true},
		{"flag 在前", "-n pat -fn src/app.ts", "src/app.ts", "-n pat", true},
		{"没有 -fn", "-f src/app.ts 1:3", "", "-f src/app.ts 1:3", false},
		{"缺值时留给调用方报错", "-fn", "", "-fn", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			file, rest, ok := stripNoAnchor(strings.Fields(tc.args))
			if ok != tc.wantOK || file != tc.wantFile {
				t.Fatalf("ok=%v file=%q, want ok=%v file=%q", ok, file, tc.wantOK, tc.wantFile)
			}
			if tc.wantOK {
				if got := strings.Join(rest, " "); got != tc.wantRest {
					t.Errorf("rest = %q, want %q", got, tc.wantRest)
				}
			}
		})
	}
}

// -fn 与 -f 等价地解析出文件，并置上 noAnchor。
func TestParseReadWriteArgsNoAnchor(t *testing.T) {
	a, err := parseReadArgs([]string{"-fn", "src/app.ts", "1:3"})
	if err != nil {
		t.Fatal(err)
	}
	if !a.noAnchor || a.file == "" || len(a.specs) != 1 {
		t.Fatalf("read: file=%q specs=%v noAnchor=%v", a.file, a.specs, a.noAnchor)
	}
	b, err := parseReadArgs([]string{"-f", "src/app.ts", "1:3"})
	if err != nil {
		t.Fatal(err)
	}
	if b.noAnchor || b.file != a.file {
		t.Fatalf("-f 形态不该被 -fn 影响: file=%q noAnchor=%v", b.file, b.noAnchor)
	}

	w, err := parseWriteArgs([]string{"-fn", "out.txt", "-c", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !w.noAnchor || !w.hasContent || w.content != "hello" {
		t.Fatalf("write: %+v", w)
	}
}

// -fn 的 read 输出没有行锚，也没有块头以外的锚点噪声。
func TestEmitReadPlain(t *testing.T) {
	doc := &docfile.Doc{
		Path:     "src/app.ts",
		Lines:    []string{"function hello() {", "}"},
		Newline:  docfile.NewlineLF,
		Encoding: docfile.EncodingUTF8,
	}
	var b strings.Builder
	emitReadPlain(&b, doc, []ranges.Range{{Start: 1, End: 2}}, false)
	got := b.String()
	want := "# src/app.ts (2 lines, lf)\n1:2:\nfunction hello() {\n}\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, hashline.Sep) {
		t.Errorf("输出里不该有行锚分隔符: %q", got)
	}
}

// -fn 的 write 只回一行成功信息。
func TestEmitWritePlain(t *testing.T) {
	doc := &docfile.Doc{Path: "out.txt", Lines: []string{"a", "b"}}
	var b strings.Builder
	emitWritePlain(&b, doc, false)
	if got, want := b.String(), "created out.txt (2 lines)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	b.Reset()
	emitWritePlain(&b, &docfile.Doc{Path: "out.txt"}, true)
	if got, want := b.String(), "replaced out.txt (empty)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// -fn 的 grep 输出是 `行号:内容`，没有锚点。
func TestEmitGrepPlain(t *testing.T) {
	files := []hitFile{{
		Path: "src/app.ts",
		Hits: []hitLine{{Line: 1, Text: "one"}, {Line: 7, Text: "seven"}},
	}}
	var b strings.Builder
	emitGrepPlain(&b, files, tempDir(t))
	got := b.String()
	want := "# src/app.ts (2 matches)\n1:one\n7:seven\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, hashline.Sep) {
		t.Errorf("输出里不该有行锚分隔符: %q", got)
	}
}

// tempDir 建一个临时目录并 chdir 进去，返回原 cwd（displayPath 才能相对化）。
func tempDir(t *testing.T) string {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	return dir
}
