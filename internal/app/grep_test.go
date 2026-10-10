package app

import (
	"strings"
	"testing"
)

func TestParseGrepArgs(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		forward   string
		hasPath   bool
		pathCount int
		count     bool
		files     bool
		quiet     bool
		json      bool
	}{
		{
			name: "pattern + path", args: []string{"-n", "hello", "src/"},
			forward: "-n hello src/", hasPath: true, pathCount: 1,
		},
		{
			name: "no path → passthrough", args: []string{"hello"},
			forward: "hello", hasPath: false,
		},
		{
			name: "pipe style", args: []string{},
			forward: "", hasPath: false,
		},
		{
			name: "two paths", args: []string{"hello", "a.ts", "b.ts"},
			forward: "hello a.ts b.ts", hasPath: true, pathCount: 2,
		},
		{
			name: "-e pattern -e pattern + path", args: []string{"-n", "-e", "hello", "-e", "world", "src/app.ts"},
			forward: "-n -e hello -e world src/app.ts", hasPath: true, pathCount: 1,
		},
		{
			name: "-e with several paths (no pattern positional)", args: []string{"-e", "x", "a", "b"},
			forward: "-e x a b", hasPath: true, pathCount: 2,
		},
		{
			name: "bundled -in", args: []string{"-in", "hello", "src/"},
			forward: "-in hello src/", hasPath: true, pathCount: 1,
		},
		{
			name: "count is stripped from forward", args: []string{"-c", "hello", "src/"},
			forward: "hello src/", hasPath: true, pathCount: 1, count: true,
		},
		{
			name: "long --count", args: []string{"--count", "hello", "src/"},
			forward: "hello src/", hasPath: true, pathCount: 1, count: true,
		},
		{
			name: "count inside bundle", args: []string{"-nc", "hello", "src/"},
			forward: "-n hello src/", hasPath: true, pathCount: 1, count: true,
		},
		{
			name: "-A2 (attached value)", args: []string{"-A2", "hello", "src/"},
			forward: "-A2 hello src/", hasPath: true, pathCount: 1,
		},
		{
			name: "-A 2 (separate value)", args: []string{"-A", "2", "hello", "src/"},
			forward: "-A 2 hello src/", hasPath: true, pathCount: 1,
		},
		{
			name: "--glob with value", args: []string{"--glob", "*.ts", "hello", "src/"},
			forward: "--glob *.ts hello src/", hasPath: true, pathCount: 1,
		},
		{
			name: "--glob=value", args: []string{"--glob=*.ts", "hello", "src/"},
			forward: "--glob=*.ts hello src/", hasPath: true, pathCount: 1,
		},
		{
			name: "-- separator", args: []string{"-n", "hello", "--", "src/"},
			forward: "-n hello -- src/", hasPath: true, pathCount: 1,
		},
		{
			name: "quiet", args: []string{"-q", "hello", "src/"},
			forward: "-q hello src/", hasPath: true, pathCount: 1, quiet: true,
		},
		{
			name: "files with matches", args: []string{"-l", "hello", "src/"},
			forward: "-l hello src/", hasPath: true, pathCount: 1, files: true,
		},
		{
			name: "our --json", args: []string{"--json", "hello", "src/"},
			forward: "hello src/", hasPath: true, pathCount: 1, json: true,
		},
		{
			name: "-v is invert-match, forwarded verbatim", args: []string{"-v", "hello", "src/"},
			forward: "-v hello src/", hasPath: true, pathCount: 1,
		},
		{
			name: "--max-count=3", args: []string{"--max-count=3", "hello", "src/"},
			forward: "--max-count=3 hello src/", hasPath: true, pathCount: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := parseGrepArgs(tc.args)
			got := strings.Join(a.forward, " ")
			if got != tc.forward {
				t.Errorf("forward = %q, want %q", got, tc.forward)
			}
			if a.hasPath != tc.hasPath {
				t.Errorf("hasPath = %v, want %v", a.hasPath, tc.hasPath)
			}
			if a.pathCount != tc.pathCount {
				t.Errorf("pathCount = %d, want %d", a.pathCount, tc.pathCount)
			}
			if a.count != tc.count || a.files != tc.files || a.quiet != tc.quiet || a.json != tc.json {
				t.Errorf("flags: count=%v files=%v quiet=%v json=%v (want %v %v %v %v)",
					a.count, a.files, a.quiet, a.json, tc.count, tc.files, tc.quiet, tc.json)
			}
		})
	}
}

// TestParseRJSONContext 锁死"上下文行也算命中行"：
// `-C2` 不能被静默降级成"只给命中行"。
func TestParseRJSONContext(t *testing.T) {
	raw := `{"type":"begin","data":{"path":{"text":"a.ts"}}}
{"type":"match","data":{"path":{"text":"a.ts"},"lines":{"text":"hit\n"},"line_number":3,"absolute_offset":0,"submatches":[]}}
{"type":"context","data":{"path":{"text":"a.ts"},"lines":{"text":"ctx\n"},"line_number":4,"absolute_offset":0,"submatches":[]}}
{"type":"end","data":{"path":{"text":"a.ts"},"binary_offset":null,"stats":{}}}
not-json-at-all
{"type":"match","data":{"path":{"text":"b/c.ts"},"lines":{"text":"x\n"},"line_number":1,"absolute_offset":0,"submatches":[]}}
`
	files, err := parseRJSON([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files, got %d: %+v", len(files), files)
	}
	if files[0].Path != "a.ts" || len(files[0].Hits) != 2 {
		t.Fatalf("file 0 = %+v", files[0])
	}
	if files[0].Hits[1].Line != 4 || files[0].Hits[1].Text != "ctx" {
		t.Fatalf("context line lost: %+v", files[0].Hits[1])
	}
	if files[1].Path != "b/c.ts" || files[1].Hits[0].Line != 1 {
		t.Fatalf("file 1 = %+v", files[1])
	}
}

// GNU grep 习惯：-E / --extended-regexp 是"吃掉"（rg 的正则默认就是 ERE，
// 而 rg 自己的 -E 是 --encoding）；-G / --basic-regexp 明确报错。
func TestParseGrepGnuFlags(t *testing.T) {
	a := parseGrepArgs([]string{"-E", "foo|bar", "src/"})
	if a.err != nil {
		t.Fatalf("-E should be accepted as a no-op, got %v", a.err)
	}
	if got, want := strings.Join(a.forward, " "), "foo|bar src/"; got != want {
		t.Errorf("forward = %q, want %q", got, want)
	}
	if !a.hasPath || a.pathCount != 1 {
		t.Errorf("hasPath=%v pathCount=%d, want true/1", a.hasPath, a.pathCount)
	}
	// 捆绑写法 -E 后面跟别的短 flag：-i 要留下
	b := parseGrepArgs([]string{"-iE", "foo|bar", "src/"})
	if b.err != nil {
		t.Fatalf("-iE: %v", b.err)
	}
	if got, want := strings.Join(b.forward, " "), "-i foo|bar src/"; got != want {
		t.Errorf("forward = %q, want %q", got, want)
	}
	// 长写法同样吃掉
	c := parseGrepArgs([]string{"--extended-regexp", "foo", "src/"})
	if c.err != nil || strings.Join(c.forward, " ") != "foo src/" {
		t.Errorf("--extended-regexp: err=%v forward=%q", c.err, strings.Join(c.forward, " "))
	}
	// BRE 明确拒绝：不能让语义悄悄变
	for _, args := range [][]string{{"-G", "foo", "src/"}, {"--basic-regexp", "foo", "src/"}} {
		d := parseGrepArgs(args)
		if d.err == nil {
			t.Errorf("%v should be rejected", args)
		}
	}
	// --encoding 仍然能带值（不能被 -E 的兼容逻辑牵连）
	e := parseGrepArgs([]string{"--encoding", "utf-8", "foo", "src/"})
	if e.err != nil || strings.Join(e.forward, " ") != "--encoding utf-8 foo src/" {
		t.Errorf("--encoding: err=%v forward=%q", e.err, strings.Join(e.forward, " "))
	}
}

func TestRgArgsFor(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"单独 -E 被吃掉", []string{"-E", "foo", "src/"}, "foo src/"},
		{"长写法同样吃掉", []string{"--extended-regexp", "foo", "src/"}, "foo src/"},
		{"捆绑 -qE", []string{"-qE", "^x$"}, "-q ^x$"},
		{"捆绑 -cE", []string{"-cE", "foo", "src/"}, "-c foo src/"},
		{"捆绑 -iEn", []string{"-iEn", "foo", "src/"}, "-in foo src/"},
		{"数值值读完继续扫：-A2E", []string{"-A2E", "foo", "src/"}, "-A2 foo src/"},
		{"数值值读完继续扫：-m2E", []string{"-m2E", "foo", "src/"}, "-m2 foo src/"},
		{"数值值读完继续扫：-j4E", []string{"-j4E", "foo", "src/"}, "-j4 foo src/"},
		{"字符串值后面全是值：-eE", []string{"-eE"}, "-eE"},
		{"值在下一个参数：-A 2 -qE", []string{"-A", "2", "-qE", "foo"}, "-A 2 -q foo"},
		{"长 flag 的值不动", []string{"--encoding", "utf-8", "-qE", "foo"}, "--encoding utf-8 -q foo"},
		{"--encoding= 写法", []string{"--encoding=utf-8", "-E", "foo"}, "--encoding=utf-8 foo"},
		{"摘掉 rweg 自己的 --json", []string{"--json", "-q", "foo"}, "-q foo"},
		{"-- 之后一律不动", []string{"-e", "pat", "--", "-E"}, "-e pat -- -E"},
		{"整词被吃掉", []string{"-E"}, ""},
		{"无关参数原样", []string{"-n", "hello", "src/"}, "-n hello src/"},
	}
	for _, tc := range cases {
		got, err := rgArgsFor(tc.args)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if s := strings.Join(got, " "); s != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, s, tc.want)
		}
	}

	// BRE 一律拒绝，捆绑写法也不例外
	for _, args := range [][]string{{"-G", "foo"}, {"--basic-regexp", "foo"}, {"-qG", "foo"}, {"-nGE", "foo"}} {
		if _, err := rgArgsFor(args); err == nil {
			t.Errorf("%v should be rejected", args)
		}
	}
}

func TestParseGrepArgsKeepsRGArgs(t *testing.T) {
	// -q / -l 走透传分支：交给 rg 的那份参数里 -E 必须已经吃掉，
	// 否则 rg 会把后面的模式当成 --encoding 的名字。
	pat := "^w[A-Za-z0-9]{4}-[A-Za-z0-9]{5}[.]db$"
	a := parseGrepArgs([]string{"-qE", pat})
	if a.err != nil {
		t.Fatalf("err = %v", a.err)
	}
	if !a.quiet {
		t.Errorf("quiet = false, want true")
	}
	if got, want := strings.Join(a.rgArgs, " "), "-q "+pat; got != want {
		t.Errorf("rgArgs = %q, want %q", got, want)
	}
	if got, want := strings.Join(a.raw, " "), "-qE "+pat; got != want {
		t.Errorf("raw = %q, want %q（系统 grep 要靠 -E 才是 ERE）", got, want)
	}

	// -c 由 rweg 自数，但透传形态里要留着（rg 的 -c 语义不变）
	c := parseGrepArgs([]string{"-cE", "foo", "src/"})
	if !c.count {
		t.Errorf("-cE: count = false")
	}
	if got, want := strings.Join(c.rgArgs, " "), "-c foo src/"; got != want {
		t.Errorf("-cE: rgArgs = %q, want %q", got, want)
	}
}
