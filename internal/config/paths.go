package config

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// NativePath 把 MSYS / Git Bash 风格路径翻成 Windows 原生路径：
//
//	/c/Users/sun/.rweg   ->  C:\Users\sun\.rweg
//	/d/tools/rweg/bin    ->  D:\tools\rweg\bin
//
// 其它形态（C:\x、C:/x、相对路径、/tmp 这类无盘符者）原样返回；
// 非 Windows 平台一律原样返回。
//
// 为什么必须做这一步：MSYS 通常替原生子进程转换**参数**与部分环境变量，
// 但 MSYS_NO_PATHCONV=1 时（dsh 之类的 agent shell 就这么设）什么都不转。
// 于是 $RWEG_HOME=/c/Users/sun/.rweg 原样进来，filepath.Join 拼出
// "\c\Users\..."（= 当前盘根下的 C:\c\...）：安装级 rweg.toml 找不到、
// 库落到盘根怪目录、read -f /c/proj/x.ts 报"文件不存在"。
func NativePath(p string) string {
	if runtime.GOOS != "windows" || len(p) < 3 {
		return p
	}
	if (p[0] != '/' && p[0] != '\\') || (p[2] != '/' && p[2] != '\\') {
		return p
	}
	c := p[1]
	if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
		return p
	}
	return strings.ToUpper(string(c)) + ":" + string(os.PathSeparator) +
		strings.ReplaceAll(p[3:], "/", string(os.PathSeparator))
}

// ResolvePath 把用户给的路径转成**原生程序**能用的形态，用于文件类参数
// （read/write/edit 的 -f、grep 的路径位置参数）。
//
//   - /c/Users/x → C:\Users\x：纯字符串规则就能算（NativePath）
//   - /tmp/x、/usr/bin 这类 MSYS 挂载点：映射到哪只有 cygpath 知道，
//     Go 侧算不出来（实测 MSYS_NO_PATHCONV=1 的 shell 里 /tmp/x 会被
//     rg 当成 C:\tmp\x 而报 os error 2）。所以剩下仍是绝对 POSIX 形态的，
//     问一次 cygpath；它不在（非 MSYS 环境）就原样返回。
func ResolvePath(p string) string {
	n := NativePath(p)
	if runtime.GOOS != "windows" || !strings.HasPrefix(n, "/") {
		return n
	}
	out, err := exec.Command("cygpath", "-w", p).Output()
	if err != nil {
		return n
	}
	if alt := strings.TrimSpace(string(out)); alt != "" && !strings.HasPrefix(alt, "/") {
		return alt
	}
	return n
}
