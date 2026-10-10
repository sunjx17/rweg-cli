package app

import (
	"fmt"
	"os"
	"strings"

	"rweg/internal/config"
	"rweg/internal/nsid"
	"rweg/internal/store"
)

// Ctx 是一次工具调用的运行时上下文。
type Ctx struct {
	Tool Tool
	Args []string
	Res  config.Resolved
	NS   string
	// Degraded 为真表示 $RWEG_NS 未设置，ns 由 cwd 派生，隔离性减弱。
	Degraded bool
	Warn     string
	JSON     bool
	Quiet    bool
	Verbose  bool
	DB       *store.DB
}

// Open 解析配置 + namespace + 数据库，并应用通用 flag。
//
// 顺序刻意如此：先配置（决定 db 位置），再 ns（决定 db 文件名），最后开库。
// 任何一步失败都在这里就返回 error，不留半开的资源。
func Open(tool Tool, args []string) (*Ctx, error) {
	return openWith(tool, parseFlags(tool, args))
}

// openWith 是 Open 的显式 flag 变体。
//
// grep 需要它：`-v` 对 rg 是 invert-match、`-q` 是 quiet，都不是 rweg 的
// 通用 flag，若走 parseFlags 会被误解并污染输出。
func openWith(tool Tool, flags flagSet) (*Ctx, error) {
	c := &Ctx{Tool: tool}

	c.JSON, c.Quiet, c.Verbose = flags.json, flags.quiet, flags.verbose

	res, err := config.Load()
	if err != nil {
		return nil, &Error{Code: ExitUsage, Message: err.Error(),
			Hint: "unset RWEG_CONFIG, or point it at an existing rweg.toml"}
	}
	c.Res = res

	ns, degraded, warn := nsid.Resolve(res.Cwd, res.BaseDir)
	c.NS, c.Degraded, c.Warn = ns, degraded, warn

	if c.Verbose {
		fmt.Fprint(os.Stderr, res.Diagnostics(ns))
		if degraded {
			fmt.Fprintln(os.Stderr, strings.TrimRight(warn, "\n"))
		}
	} else if degraded && !c.Quiet {
		// 非 verbose 模式也给一次提示，但走 stderr 且只提醒不阻塞
		fmt.Fprintln(os.Stderr, strings.TrimRight(warn, "\n"))
	}

	db, err := store.Open(res.DBPath(ns), ns, res.Cfg.DB)
	if err != nil {
		return nil, &Error{Code: ExitInternal, Message: "open db: " + err.Error(),
			Hint: "check [db].dir is writable: " + res.DBDir()}
	}
	if err := db.TouchNamespace(res.Cwd, res.StrictCwdCheck(),
		res.Cfg.Hash.CanonVersion, res.Cfg.Hash.ProbeStride); err != nil {
		db.Close()
		return nil, &Error{Code: ExitBiz, Message: err.Error(),
			Hint: "cd back to where the ns was created, or unset RWEG_NS to re-derive it in this shell " +
				"(verified to recover), or pin the project config with RWEG_PIN_CONFIG=<path>; " +
				"[namespace].strict_cwd = false turns this check off"}
	}
	c.DB = db
	return c, nil
}

// Close 释放数据库。
func (c *Ctx) Close() {
	if c.DB != nil {
		_ = c.DB.Close()
	}
}

type flagSet struct {
	json    bool
	quiet   bool
	verbose bool
	help    bool
	rest    []string
}

// parseFlags 从参数里摘掉通用 flag，返回其余位置参数。
//
// 刻意不引第三方 flag 包：rweg 的参数形态（read 的 RANGE、edit 的重复 -c、
// grep 的透传）都不是标准 flag 能表达的，手写更直白，也不污染透传路径。
//
// **只摘认识的 flag**：`-4`（= 1:4）、`8-`（= 8..EOF）这类位置参数
// 以 `-` 开头但不是 flag，必须原样保留。所以这里不按 `-` 前缀猜。
//
// **必须传 tool**：edit 的 `-c A:B "text"` 后面跟的是**内容**，
// 而内容本身可能长得像 flag（`edit -f x -c A:B "-v"`）。
// 不知道每个 flag 吃几个值的话，`-v` 会被当成 verbose 摘掉 ——
// 结果是把 "-v" 那行文字写进文件，还静默丢了 verbose 语义。
func parseFlags(tool Tool, args []string) flagSet {
	var f flagSet
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--json":
			f.json = true
		case "-q", "--quiet":
			f.quiet = true
		case "-v", "--verbose":
			f.verbose = true
		case "-h", "--help":
			f.help = true
		case "-f", "--file":
			// 值原样留在 rest 里，由各工具自己解析
			f.rest = append(f.rest, a)
			if i+1 < len(args) {
				i++
				f.rest = append(f.rest, args[i])
			}
		case "-c", "--content", "--change":
			n := flagValueArity(tool, a)
			f.rest = append(f.rest, a)
			for k := 0; k < n && i+1 < len(args); k++ {
				i++
				f.rest = append(f.rest, args[i])
			}
		case "--encoding":
			f.rest = append(f.rest, a)
			if i+1 < len(args) {
				i++
				f.rest = append(f.rest, args[i])
			}
		default:
			f.rest = append(f.rest, a)
		}
	}
	return f
}

// flagValueArity 返回某 flag 后面跟几个值。
//
// edit 的 `-c` 是两个（锚点对 + 替换文本）；write 的 `-c` 是一个。
func flagValueArity(tool Tool, flag string) int {
	switch flag {
	case "-c", "--change", "--content":
		if tool == ToolEdit {
			return 2
		}
		return 1
	}
	return 0
}
