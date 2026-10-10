package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"rweg/internal/config"
	"rweg/internal/hashline"
	"rweg/internal/nsid"
	"rweg/internal/store"
)

// Run 是整个 CLI 的入口。返回退出码，main 里 os.Exit 它。
func Run(argv0 string, args []string) int {
	tool := DetectTool(argv0)
	err := dispatch(tool, args)
	if err == nil {
		return int(ExitOK)
	}
	if e, ok := err.(*Error); ok {
		e.report(os.Stderr)
		return int(e.Code)
	}
	fmt.Fprintln(os.Stderr, "rweg: "+err.Error())
	return int(ExitInternal)
}

func dispatch(tool Tool, args []string) error {
	switch tool {
	case ToolCore:
		return runCore(args)
	case ToolRead, ToolWrte, ToolEdit, ToolGrep, ToolUndo, ToolAppend, ToolPrepend:
		return runTool(tool, args)
	}
	return UsageErr("unknown tool %q", tool)
}

// runTool 处理 read/write/edit/grep/undo/append/prepend。
//
// grep 走自己的路径：`-v`（invert-match）、`-q`（quiet）、`-c`（count）
// 对 rg 都有意义，若是先过通用 flag 解析就会被吃掉；
// 而且它有若干分支需要**直接透传退出码**（os.Exit），必须最早分流。
func runTool(tool Tool, args []string) error {
	if tool == ToolGrep {
		// -h 与 --help 同义：rweg 的 help 优先于 rg 的 help，
		// 否则 `grep -h` 打的是 ripgrep 的手册（args 里含 "--" 时仍可透传）。
		if wantsHelp(args) {
			printToolHelp(tool)
			return nil
		}
		return runGrep(args)
	}

	flags := parseFlags(tool, args)
	rest := flags.rest
	if flags.help {
		printToolHelp(tool)
		return nil
	}

	c, err := Open(tool, args)
	if err != nil {
		return err
	}
	defer c.Close()

	switch tool {
	case ToolRead:
		return runRead(c, rest)
	case ToolWrte:
		return runWrite(c, rest)
	case ToolEdit:
		return runEdit(c, rest)
	case ToolUndo:
		return runUndo(c, rest)
	case ToolAppend, ToolPrepend:
		return runInsert(c, rest)
	}
	return UsageErr("unknown tool %q", tool)
}

// passthroughGrep 把参数交给系统 grep / rg。
//
// 这是 P5 的**回退路径**（plan §6.5）：rg 不在 PATH、或用户喂的是管道
// 而没能给出明确路径时，rweg 不假装自己知道锚点，直接把活交出去。
//
// 两个目标的短选项表不同，所以参数有两份：
//   - 系统 grep：用 args，只摘掉 rweg 自己的 --json。
//     -E / --extended-regexp 必须留着 —— grep 要靠它才是 ERE。
//   - rg：用 rgArgs —— -E 已经吃掉（rg 的 -E 是 --encoding）、--json 已摘掉。
//
// `-q` / `-c` / `-n` 两种形态里都原样保留 —— 它们是 grep 家族的真实语义。
//
// 找不到任何一个就报 E_NO_GREP，退出码 3——总比静默吞掉强。
func passthroughGrep(args []string, rgArgs []string) error {
	forward := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			continue
		}
		forward = append(forward, a)
	}

	for _, name := range []string{"grep", "rg"} {
		p, err := exec.LookPath(name)
		if err != nil || p == "" {
			continue
		}
		// 避免再次选中自己（PATH 首位就是 rweg 的 bin）
		if sameFile(p, currentExe()) {
			continue
		}
		use := forward
		if name == "rg" && len(rgArgs) > 0 {
			use = rgArgs
		}
		cmd := exec.Command(p, use...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				os.Exit(ee.ExitCode()) // 透传真实退出码
			}
			return &Error{Code: ExitInternal, Message: "run " + name + ": " + err.Error()}
		}
		return nil
	}
	return &Error{
		Code:    ExitInternal,
		Message: "E_NO_GREP  neither rg nor grep found on PATH",
		Hint:    "install ripgrep (rg) to enable anchored grep output",
	}
}

func currentExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// sameFile 报告两个路径是否指向同一个文件（Windows 上大小写不敏感）。
func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) {
		return true
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// wantsHelp 报告参数是否在请求帮助：-h / --help / help 三写等价。
//
// 各工具与 core-rweg 各子命令共用它，保证 `X -h` 处处打得开帮助。
func wantsHelp(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "--help", "help":
		return true
	}
	return false
}

// printToolHelp 输出某个工具的帮助，并指路完整的 Agent 约定。
func printToolHelp(tool Tool) {
	fmt.Print(toolHelp(tool))
	fmt.Print("\n给 Agent 的完整约定：core-rweg prompt\n")
}

func toolHelp(tool Tool) string {
	switch tool {
	case ToolRead:
		return `read -f FILE [RANGE ...] [--encoding ENC]

RANGE := N            单行
       | N:M          含首尾的闭区间
       | N-           N 到文件末尾
       | -M           文件开头到 M

不给 RANGE 时默认读整个文件（超过 max_lines 截断并标 truncated；单行超过
max_line_bytes 的行只给标记，不给锚点）。
输出每行 ` + hashline.Sep + `内容。读到的行记入 served，锚点可直接用于 edit。
绝对路径两种形态都认：C:/x/f.txt 与 /c/x/f.txt，MSYS 的 /tmp 也认。
`
	case ToolWrte:
		return `write [FILE] [CONTENT]
write -f FILE [-c CONTENT] [--encoding ENC]

FILE 放在最前面时可以省掉 -f，CONTENT 就是紧随其后的那个位置参数；
要把 flag 写在前面（write -f f --encoding gbk -c x）就用 -f / -c 形式。

不给 CONTENT / -c 时从 stdin 读全文；-c "" 写一个空文件。
-c 的内容若不以换行结尾，会自动补一个（命令行参数无法可靠表达
"结尾没有换行"）。要精确控制就走 stdin：printf 'a\nb' | write -f f
覆盖已有文件时保持其编码 / 行尾 / BOM。
文件不存在则创建（目录必须已存在）；存在则全量替换。
写完自动 serve 全文并回传锚点，省一次 read；这一步的内容进撤销栈，
改错了可以 undo 退回来。
`
	case ToolEdit:
		return `edit [FILE] FROM[:TO] CONTENT [FROM[:TO] CONTENT ...]
edit -f FILE -c FROM:TO CONTENT [-c FROM:TO CONTENT ...]

FILE 放在最前面时可以省掉 -f。FROM:TO 省略 :TO 时等价于 FROM:FROM。
CONTENT 里的分行必须是**真换行**（$'a\nb'、here-doc，或从 stdin 喂）；
写成 \n 两个字符只会原样插入这两个字符。"" 表示删除该区间。
一次调用的多组改动原子生效：任一失败则文件与库都不动。
每次 edit 前的内容进撤销栈，undo 可退回一步（编码 / 行尾一并回来）。

锚点必须来自本终端的 read 或 grep。任一锚点过期 →
硬拒绝并回传该区域当前锚点（回传的行**不写 served**，
必须重新 read 才能重试 —— fail-closed，防静默写错行）。
`
	case ToolGrep:
		return `grep [rg 的参数]... [--] 路径

命中行会带上锚点并把行写入 served，可直接用于 edit：

  # src/app.ts (2 matches)
  1:CTt` + hashline.Sep + `function hello() {
  7:6PY` + hashline.Sep + `export default hello;

回退路径（不做锚点处理，行为与 rg/grep 一致）：
  - 没有文件路径（管道输入 / 整目录递归）→ 完全透传
  - rg 不可用 / 执行报错（退出码 ≥2）→ 回退系统 grep，退出码透传
  - -q / -l 以及 -c（计数）→ 直接透传 rg 的原生行为

GNU 习惯的两个兼容点：
  -E / --extended-regexp   吃掉：rg 的正则默认就是 ERE（rg 的 -E 是 --encoding）
  -G / --basic-regexp      明确报错：rg 没有 BRE，别让语义悄悄变
`
	case ToolUndo:
		return `undo [FILE]
undo -f FILE

把 FILE 退回上一次改动（write / edit / append / prepend）之前：内容按当时磁盘上的**原始字节**恢复
（编码、BOM、行尾风格一并回来），随后重建该路径的 served 锚点 ——
内容回到旧样子，旧锚点重新有效。

不给 FILE 就回退最近一次改动，不限路径。

每个 namespace 保留最近 ` + fmt.Sprint(undoStackDepth) + ` 次改动的副本
（单条上限 4 MiB，超了明确报 E_UNDO_TOO_BIG）。撤销一条后记录即消费掉，
再 undo 就是继续往前退一层。
`
	case ToolAppend, ToolPrepend:
		return insertHelp(tool)
	}
	return ""
}

// insertHelp 是 append / prepend 的帮助。两者只差插入方向，正文共用。
func insertHelp(tool Tool) string {
	name, where := "append", "之后"
	if tool == ToolPrepend {
		name, where = "prepend", "之前"
	}
	return name + ` ANCHOR CONTENT
` + name + ` -f FILE ANCHOR CONTENT
` + name + ` -c ANCHOR CONTENT

把 CONTENT 插到 ANCHOR 所指那一行**` + where + `**（append 在后，prepend 在前）。

ANCHOR 是单行锚点（3 个 base62 字符），必须来自本终端的 read / grep。
位置只由锚点决定，所以 FILE 可以省：工具从 served 基线反查锚点属于哪个文件，
多个文件都 serve 过同一锚点时报 E_AMBIGUOUS_ANCHOR，用 -f FILE 指明。

CONTENT 里的分行必须是**真换行**（$'a\nb'、here-doc）；"" 表示插入一个空行。
被锚定的那一行原样保留、锚点不变：实现上就是"把它替换成它自己 + 新行"，
所以校验链、undo、served 重建与 edit 完全一致（锚点过期同样报 E_STALE_*）。
CONTENT 恰好是 -v / -q / -h / -f / -c / --json 这类 rweg 认得的 flag 时，
改用 -c ANCHOR CONTENT 形态，免得被通用 flag 解析摘走。
`
}

func runCore(args []string) error {
	if len(args) == 0 {
		fmt.Print(coreHelp())
		return nil
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Print(coreHelp())
		return nil
	case "selftest":
		return cmdSelftest(args[1:])
	case "cfg":
		return cmdCfg(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "ns":
		return cmdNS(args[1:])
	case "gc":
		return cmdGC(args[1:])
	case "read", "write", "edit", "grep", "undo", "append", "prepend":
		// 工具名当子命令：装出来的 bin 里只有 core-rweg 一个二进制，
		// env.sh 的 h* 函数与 env.aggressive.sh 的同名接管都走这条路。
		return runTool(Tool(args[0]), args[1:])
	case "prompt":
		return cmdPrompt(args[1:])
	}
	return UsageErr("unknown subcommand %q", args[0]).
		WithHint("run: core-rweg --help")
}

func coreHelp() string {
	return `core-rweg — hashline 锚点工具的核心二进制

用法：
  core-rweg <子命令>          （-h / --help / help 亦可）

子命令：
  selftest              哈希算法自检（对照内置向量与参考实现锚点）
  cfg [--no-ns]         打印当前生效配置
  doctor                环境自检（rg / db / 配置 / PATH / selftest）
  ns new                生成一个 namespace 并打印（env.sh 调用）
  ns gc                 清理过期数据
  gc [--force]          显式 GC
  prompt                打印给 Agent 用的提示词模板

环境变量：
  RWEG_HOME           安装目录，默认 ~/.rweg
  RWEG_CONFIG         显式指定配置文件（唯一会因配置失败而报错的变量）
  RWEG_WALKUP         向上查找 rweg.toml 的父级深度，默认 0
                      0 = 只找 cwd；1 = +父目录；2 = +祖父……（上限 32）
  RWEG_NS             当前终端的 namespace，由 env.sh 生成
  RWEG_VERBOSE        置非 0 值后，source env.sh 会往 stderr 打就绪信息

工具入口：core-rweg read|write|edit|grep|undo|append|prepend ...（各有 -h），
	与 argv[0]
分发（read.exe / grep.exe 之类的硬链接）等价 —— 现在只装 core-rweg 一个二进制。
`
}

// --- selftest ---

func cmdSelftest(args []string) error {
	if wantsHelp(args) {
		fmt.Print(`core-rweg selftest

跑哈希自检：xxh32 官方向量、哈希空间常量、参考实现锚点对齐、锚点保持。
无参数；全部通过时退出码 0。
`)
		return nil
	}
	fails := 0
	check := func(name string, got, want any) {
		if fmt.Sprint(got) != fmt.Sprint(want) {
			fmt.Printf("  FAIL  %-28s got %v, want %v\n", name, got, want)
			fails++
			return
		}
		fmt.Printf("  ok    %-28s %v\n", name, got)
	}

	fmt.Println("rweg selftest")
	fmt.Println("  -- xxh32 官方向量")
	check(`XXH32("", 0)`, fmt.Sprintf("%08x", hashline.XXH32(nil, 0)), "02cc5d05")
	check(`XXH32("a", 0)`, fmt.Sprintf("%08x", hashline.XXH32([]byte("a"), 0)), "550d7456")
	check(`XXH32("Nobody...", 0)`,
		fmt.Sprintf("%08x", hashline.XXH32([]byte("Nobody inspects the spammish repetition"), 0)),
		"e2293b2f")

	fmt.Println("  -- 常量")
	check("hash space", hashline.HashSpace, 238328)
	check("probe stride", hashline.DefaultProbeStride, 3907)

	fmt.Println("  -- 参考实现锚点对齐")
	lines := []string{
		"function hello() {",
		`  console.log("world");`,
		"}",
		"const x = 1;",
		"let y = 2;",
		"export default hello;",
	}
	want := []string{"CTt", "Um7", "AU6", "nVR", "yAZ", "6PY"}
	got := hashline.Pure(lines, hashline.DefaultProbeStride, nil)
	for i := range want {
		check(fmt.Sprintf("line %d", i+1), got[i], want[i])
	}

	fmt.Println("  -- mapStableHashes 锚点保持")
	old := lines
	oldH := hashline.Pure(old, hashline.DefaultProbeStride, nil)
	newLines := append(append([]string{}, old[:3]...),
		"const x = 42;", "const z = 3;", "let w = 4;")
	newLines = append(newLines, old[4:]...)
	newH := hashline.MapStable(old, oldH, newLines,
		map[string]bool{oldH[3]: true}, nil, hashline.DefaultProbeStride)
	keep := 0
	for i, oh := range oldH {
		if i == 3 {
			continue
		}
		for j, nh := range newH {
			if nh == oh && hashline.Canon(old[i]) == hashline.Canon(newLines[j]) {
				keep++
			}
		}
	}
	check("untouched anchors kept", fmt.Sprintf("%d/5", keep), "5/5")

	if fails > 0 {
		return &Error{Code: ExitInternal,
			Message: fmt.Sprintf("selftest FAILED: %d check(s)", fails)}
	}
	fmt.Println("selftest OK")
	return nil
}

// --- cfg ---

func cmdCfg(args []string) error {
	if wantsHelp(args) {
		fmt.Print(`core-rweg cfg [--no-ns] [--path]

打印当前生效的配置与来源：home / cwd / walkup / config / basedir / db / ns，
末行给出 rg 的位置。

  --no-ns   不解析 namespace（跳过硬校验，只看配置落点）
  --path    只打印生效配置文件的绝对路径（没有则什么都不打印，退出码 0）；
            env.sh 用它把配置钉进 $RWEG_PIN_CONFIG
`)
		return nil
	}
	pathOnly := false
	noNS := false
	for _, a := range args {
		switch a {
		case "--no-ns":
			noNS = true
		case "--path":
			pathOnly = true
		default:
			return UsageErr("cfg: unexpected argument %q", a).
				WithHint("core-rweg cfg [--no-ns] [--path]")
		}
	}
	res, err := config.Load()
	if err != nil {
		return &Error{Code: ExitUsage, Message: err.Error()}
	}
	if pathOnly {
		// 只有真的存在配置文件才输出：$RWEG_PIN_CONFIG 空串表示"没钉"。
		if res.Path != "" {
			fmt.Println(res.Path)
		}
		return nil
	}
	ns, degraded, _ := nsid.Resolve(res.Cwd, res.BaseDir)
	if noNS {
		ns = ""
	}
	fmt.Print(res.Diagnostics(ns))
	if degraded {
		fmt.Printf("[cfg] note    %s unset; ns is cwd-derived (isolation reduced)\n", nsid.EnvVar)
	}
	fmt.Printf("[cfg] rgrep   %s\n", rgLocation(res.Cfg.Grep))
	return nil
}

func rgLocation(g config.Grep) string {
	if g.RgPath != "" {
		return g.RgPath
	}
	if p, err := exec.LookPath("rg"); err == nil {
		return p
	}
	return "<not found in PATH>"
}

// --- doctor ---

func cmdDoctor(args []string) error {
	if wantsHelp(args) {
		fmt.Print(`core-rweg doctor

环境自检：二进制 / cwd / walkup / 配置来源 / basedir / home / PATH 首位 /
namespace / rg / 数据库，最后跑一遍 selftest。
无参数；有 FAIL 时退出码 3，只有 WARN 时退出码 0。
`)
		return nil
	}
	res, err := config.Load()
	if err != nil {
		return &Error{Code: ExitUsage, Message: err.Error()}
	}
	ns, degraded, _ := nsid.Resolve(res.Cwd, res.BaseDir)

	warn, fail := 0, 0
	row := func(label, val, status string) {
		mark := "OK"
		switch status {
		case "WARN":
			mark, warn = "WARN", warn+1
		case "FAIL":
			mark, fail = "FAIL", fail+1
		}
		fmt.Printf("  %-10s %-52s %s\n", label, val, mark)
	}

	fmt.Println("rweg doctor")

	exe, _ := os.Executable()
	row("binary", exe, "OK")

	cfgStatus := "OK"
	if strings.Contains(res.Source, "PARSE ERROR") {
		cfgStatus = "FAIL"
	}
	row("cwd", res.Cwd, "OK")
	row("walkup", walkupRow(res), "OK")
	row("config", res.Source, cfgStatus)
	row("basedir", res.BaseDir, "OK")
	row("home", res.Home, "OK")

	// bin 目录是否在 PATH 首位（只为 `core-rweg` 能直接敲；bin 里已无 read.exe / grep.exe）
	binStatus := "WARN"
	if samePath(firstPathEntry(), config.BinDir()) {
		binStatus = "OK"
	}
	row("PATH", "first entry = "+firstPathEntry(), binStatus)

	nsStatus := "OK"
	nsLabel := ns + " (from $" + nsid.EnvVar + ")"
	if degraded {
		nsStatus = "WARN"
		nsLabel = ns + " (derived from cwd; isolation reduced)"
	}
	row("ns", nsLabel, nsStatus)

	rgStatus := "OK"
	if config.OnPath("rg") || res.Cfg.Grep.RgPath != "" {
		row("rg", rgLocation(res.Cfg.Grep), rgStatus)
	} else {
		row("rg", "<not found in PATH>", "WARN")
	}

	// 数据库
	db, err := store.Open(res.DBPath(ns), ns, res.Cfg.DB)
	if err != nil {
		row("db", res.DBPath(ns)+"  "+err.Error(), "FAIL")
	} else {
		st, _ := db.Stats()
		if err := db.TouchNamespace(res.Cwd, res.StrictCwdCheck(),
			res.Cfg.Hash.CanonVersion, res.Cfg.Hash.ProbeStride); err != nil {
			row("db", res.DBPath(ns)+"  "+err.Error(), "WARN")
		} else {
			row("db", fmt.Sprintf("%s  (%d paths, %d served, %d retired, %s)",
				res.DBPath(ns), st.Paths, st.ServedRows, st.Retired, humanBytes(st.SizeBytes)), "OK")
		}
		db.Close()
	}

	// selftest
	if err := cmdSelftest(nil); err != nil {
		fail++
	} else {
		fmt.Println()
	}

	if fail > 0 {
		return &Error{Code: ExitInternal,
			Message: fmt.Sprintf("doctor: %d FAIL, %d WARN", fail, warn)}
	}
	if warn > 0 {
		fmt.Printf("\ndoctor: %d WARN, 0 FAIL\n", warn)
	} else {
		fmt.Println("\ndoctor: all OK")
	}
	return nil
}

func firstPathEntry() string {
	for _, p := range splitPathList(os.Getenv("PATH")) {
		if p != "" {
			return p
		}
	}
	return "<empty PATH>"
}

// samePath 比较两个目录是否指同一处，容忍两种写法：
// MSYS 形态（/c/Users/x）与原生形态（C:\Users\x）在 MSYS_NO_PATHCONV=1
// 的 shell（dsh）里会同时出现 —— PATH 是 MSYS 写法、$RWEG_HOME 算出来的
// bin 是另一种，直接字符串比较会把装好的环境判成 WARN。
func samePath(a, b string) bool {
	na := filepath.Clean(config.NativePath(a))
	nb := filepath.Clean(config.NativePath(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(na, nb)
	}
	return na == nb
}

// walkupRow 渲染 doctor 里的 walk-up 行：深度 + 命中层级。
func walkupRow(res config.Resolved) string {
	s := fmt.Sprintf("RWEG_WALKUP=%d", res.WalkUp)
	switch {
	case res.WalkUp == 0:
		s += " (cwd only)"
	case res.FoundAtLevel > 0:
		s += fmt.Sprintf(" (found %d level(s) up)", res.FoundAtLevel)
	case res.FoundAtLevel == 0:
		s += " (found in cwd)"
	default:
		s += " (not found on cwd chain)"
	}
	return s
}

func splitPathList(s string) []string { return strings.Split(s, string(os.PathListSeparator)) }

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// --- ns ---

func cmdNS(args []string) error {
	if wantsHelp(args) {
		fmt.Print(`core-rweg ns <子命令>

  ns new    生成一个 namespace 并打印（前绑 cwd|配置目录，后接时间戳）
  ns gc     清理过期数据，同 core-rweg gc
`)
		return nil
	}
	if len(args) == 0 {
		return UsageErr("ns: need a subcommand (new | gc)").
			WithHint("core-rweg ns -h")
	}
	switch args[0] {
	case "new":
		if wantsHelp(args[1:]) {
			fmt.Print(`core-rweg ns new

生成一个 namespace 并打印（形如 wK3fP-9xT2q）：前段绑 cwd|配置目录，
后段是时间戳。无参数；env.sh 在 source 时调用它。
`)
			return nil
		}
		res, err := config.Load()
		if err != nil {
			return &Error{Code: ExitUsage, Message: err.Error()}
		}
		fmt.Println(nsid.NewFor(res.Cwd, res.BaseDir))
		return nil
	case "gc":
		return cmdGC(args[1:])
	}
	return UsageErr("ns: unknown subcommand %q", args[0]).
		WithHint("core-rweg ns -h")
}

// --- gc ---

func cmdGC(args []string) error {
	if wantsHelp(args) {
		fmt.Print(`core-rweg gc [--force]

清理过期的 served / retired / 快照。未到 namespace.gc_interval_minutes 时
只打印 "nothing to do"，加 --force（或 -f）才强制清理。
`)
		return nil
	}
	force := false
	for _, a := range args {
		if a == "--force" || a == "-f" {
			force = true
		}
	}
	c, err := Open(ToolCore, args)
	if err != nil {
		return err
	}
	defer c.Close()

	res, err := c.DB.GC(c.Res.Cfg.Namespace.TTLHours,
		c.Res.Cfg.Namespace.GCIntervalMinutes, force)
	if err != nil {
		return &Error{Code: ExitInternal, Message: "gc: " + err.Error()}
	}
	if res.DeletedServed == 0 && res.DeletedRetired == 0 && res.DeletedSnaps == 0 && !force {
		fmt.Println("GC: nothing to do (within gc_interval; use --force to override)")
		return nil
	}
	fmt.Printf("GC deleted: %d served rows, %d retired, %d snapshots\n",
		res.DeletedServed, res.DeletedRetired, res.DeletedSnaps)
	fmt.Printf("    db: %s\n", c.Res.DBPath(c.NS))
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
