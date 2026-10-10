package app

import (
	"runtime"
	"testing"

	"rweg/internal/config"
)

// read / write / edit 的 -f 都要吃 Git Bash 形态的路径。
// MSYS_NO_PATHCONV=1 的 shell（dsh）不会替参数做转换，原样交给 Go 的话
// /c/proj/x.ts 会被解释成 <当前盘>:\c\proj\x.ts，工具报"文件不存在"。
func TestFileArgsAcceptMsysPath(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"-f 分开写", "/c/proj/a.ts"},
		{"--file= 合体", "/c/proj/b.ts"},
	}
	for _, c := range cases {
		switch c.name {
		case "-f 分开写":
			a, err := parseReadArgs([]string{"-f", c.arg, "1:2"})
			if err != nil {
				t.Fatalf("parseReadArgs: %v", err)
			}
			if want := config.NativePath(c.arg); a.file != want {
				t.Errorf("read file = %q, want %q", a.file, want)
			}
		case "--file= 合体":
			a, err := parseWriteArgs([]string{"--file=" + c.arg, "-c", "x"})
			if err != nil {
				t.Fatalf("parseWriteArgs: %v", err)
			}
			if want := config.NativePath(c.arg); a.file != want {
				t.Errorf("write file = %q, want %q", a.file, want)
			}
		}
	}

	e, err := parseEditArgs([]string{"-f", "/c/proj/c.ts", "-c", "CTt:CTt", "x"})
	if err != nil {
		t.Fatalf("parseEditArgs: %v", err)
	}
	if want := config.NativePath("/c/proj/c.ts"); e.file != want {
		t.Errorf("edit file = %q, want %q", e.file, want)
	}

	// 非 Windows 平台不该被改写
	if runtime.GOOS != "windows" && e.file != "/c/proj/c.ts" {
		t.Errorf("non-windows file = %q, want unchanged", e.file)
	}
}
