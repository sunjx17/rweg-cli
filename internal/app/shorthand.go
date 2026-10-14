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

// stripNoAnchor 摘掉 rweg 自己的无锚文件 flag（-fn / --no-anchor）及其值。
//
// `-fn FILE`（长写法 `--no-anchor FILE` / `--no-anchor=FILE`）是
// `-f FILE` 的等价写法，区别只在输出：内容不带行锚，也就没有锚点可生成
// —— 适合只想拿纯内容（脚本、管道、只看一眼）的场景。
//
// 返回值：file 为 -fn 给出的路径（没写则空），rest 是摘掉该 flag 后的参数。
func stripNoAnchor(args []string) (file string, rest []string, ok bool) {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case (s == "-fn" || s == "--no-anchor") && ok == false:
			if i+1 >= len(args) {
				// 没给值：原样留着，由各工具的参数解析报用法错误
				out = append(out, s)
				continue
			}
			i++
			file, ok = args[i], true
		case ok && s == "--no-anchor" && strings.HasPrefix(s, "--no-anchor="):
			out = append(out, s)
		case !ok && strings.HasPrefix(s, "--no-anchor="):
			file, ok = s[len("--no-anchor="):], true
		default:
			out = append(out, s)
		}
	}
	return file, out, ok
}
