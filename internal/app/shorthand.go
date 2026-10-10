package app

import "strings"

// fileShorthand 支持省略 -f/--file 的简写：第一个位置参数就是文件。
//
// read  FILE [RANGE ...]
// write FILE [CONTENT]
// edit  FILE FROM[:TO] CONTENT [FROM[:TO] CONTENT ...]
//
// 已经写了 -f/--file/--file= 时不动；第一个参数本身是 flag（-x、--x）时也不动，
// 于是 `read -f FILE ...`、`read --no-serve FILE ...` 这些写法照旧。
// 想要 flag 打在文件名前面（`read --encoding gbk FILE`）就用 -f 形式。
func fileShorthand(args []string) (file string, rest []string, ok bool) {
	if len(args) == 0 {
		return "", args, false
	}
	for _, s := range args {
		if s == "-f" || s == "--file" || strings.HasPrefix(s, "--file=") {
			return "", args, false
		}
	}
	if strings.HasPrefix(args[0], "-") {
		return "", args, false
	}
	return args[0], args[1:], true
}
