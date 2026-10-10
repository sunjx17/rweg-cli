package config

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// MSYS 形态只在 Windows 上翻转；其它平台一律原样返回。
func wantNative(in string) string {
	if runtime.GOOS != "windows" {
		return in
	}
	switch in {
	case "/c/Users/sun/.rweg":
		return `C:\Users\sun\.rweg`
	case "/d/tools/rweg":
		return `D:\tools\rweg`
	case "/C/windows":
		return `C:\windows`
	}
	return in
}

func TestNativePath(t *testing.T) {
	cases := []string{
		"/c/Users/sun/.rweg", // Git Bash 的盘符写法
		"/d/tools/rweg",
		"/C/windows",     // 大写盘符
		`C:\Users\sun`,   // 已是原生
		"C:/Users/sun",   // 原生 + 正斜杠
		".db/",           // 相对路径
		"/tmp/x",         // 无盘符，推不出盘，原样
		"/c",             // 太短
		"",               //
		"//server/share", // UNC 风格，不动
	}
	for _, in := range cases {
		if got := NativePath(in); got != wantNative(in) {
			t.Errorf("NativePath(%q) = %q, want %q", in, got, wantNative(in))
		}
	}
}

// $RWEG_HOME 允许写成 /c/... ：MSYS_NO_PATHCONV=1 的 shell（dsh）不替
// 环境变量做转换，若原样使用会拼出 C:\c\... 这种盘根怪目录。
func TestHomeAcceptsMsysPath(t *testing.T) {
	t.Setenv("RWEG_HOME", "/c/Users/sun/.rweg")
	want := wantNative("/c/Users/sun/.rweg")
	if got := Home(); got != want {
		t.Errorf("Home() = %q, want %q", got, want)
	}
	if got, want := HomeConfigPath(), want+"\\"+ConfigName; runtime.GOOS == "windows" && got != want {
		t.Errorf("HomeConfigPath() = %q, want %q", got, want)
	}
}

// CleanAbs 也要认 /c/... 为绝对路径（否则会被当成相对路径拼到 base 上）。
func TestCleanAbsAcceptsMsysPath(t *testing.T) {
	got := CleanAbs("/c/proj/rweg.toml", `D:\somewhere`)
	if runtime.GOOS != "windows" {
		return
	}
	if want := `C:\proj\rweg.toml`; got != want {
		t.Errorf("CleanAbs = %q, want %q", got, want)
	}
}

// ResolvePath 在 NativePath 之上补一层 cygpath 兜底：
// /c/... 之外的 MSYS 绝对路径（/tmp、/usr、/mingw64…）映射到哪只有 cygpath 知道，
// Go 侧只能问它一次。非 Windows 上必须原样返回，不做任何猜测。
func TestResolvePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		for _, in := range []string{"/tmp/x", "/c/proj/f.ts", "rel/f.ts"} {
			if got := ResolvePath(in); got != in {
				t.Errorf("ResolvePath(%q) = %q, want unchanged", in, got)
			}
		}
		return
	}
	// 盘符形态不依赖 cygpath
	if got, want := ResolvePath("/c/proj/f.ts"), `C:\proj\f.ts`; got != want {
		t.Errorf("ResolvePath = %q, want %q", got, want)
	}
	// 普通相对路径不该被改写
	if got := ResolvePath("rel/f.ts"); got != "rel/f.ts" {
		t.Errorf("ResolvePath(rel) = %q, want unchanged", got)
	}
	if _, err := exec.LookPath("cygpath"); err != nil {
		t.Skip("cygpath not on PATH")
	}
	got := ResolvePath("/tmp/rweg-resolvepath-probe.txt")
	if strings.HasPrefix(got, "/") {
		t.Errorf("ResolvePath(/tmp/...) = %q, want a native path", got)
	}
	if !strings.Contains(strings.ToLower(got), "temp") {
		t.Errorf("ResolvePath(/tmp/...) = %q, want the MSYS temp dir", got)
	}
}
