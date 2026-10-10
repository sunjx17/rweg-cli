// Package config 负责 rweg.toml 的查找、解析与路径解析。
//
// 四条不可动摇的规则：
//  1. 每个工具每次调用都独立查找配置文件，找不到就用内置默认值——不报错、不阻塞。
//  2. 查找只看 cwd 及其**指定层数**的父目录，由 $RWEG_WALKUP 决定深度。
//     找不到再看安装级 $RWEG_HOME/rweg.toml。
//  3. 所有相对路径都相对于「配置文件所在目录」解析，不是 cwd。
//     换目录跑 read/edit 不影响 db 位置。
//  4. 每个 namespace 一个库文件，不提供固定文件名选项——避免多终端抢同一个库。
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// DefaultHome 是安装目录的默认位置，可用 RWEG_HOME 覆盖。
const DefaultHome = ".rweg"

// FileName 是配置文件名。
const FileName = "rweg.toml"

// ConfigName 是安装级默认配置（cwd 及其父级都没有 rweg.toml 时生效）。
const ConfigName = "rweg.toml"

// DefaultWalkUp 是 $RWEG_WALKUP 的缺省值：0 = 只找 cwd，不向父级扩展。
const DefaultWalkUp = 0

// MaxWalkUp 是 $RWEG_WALKUP 的上限，防止一路查到磁盘根。
const MaxWalkUp = 32

// DB 是 [db] 段。
type DB struct {
	// Dir 是数据库**目录**，相对路径相对于配置文件所在目录。
	// 默认 ".db/"。数据库文件 = Dir/<namespace>.db。
	//
	// 刻意不提供固定文件名选项：一旦多个 namespace 指向同一个文件，
	// 就会重新引入 served 状态的跨终端竞争。需要共享就手动指定同一个
	// RWEG_NS（env.sh 已支持复用），而不是共享库文件。
	Dir string `toml:"dir"`
	// BusyTimeoutMS 是 SQLite busy_timeout。
	BusyTimeoutMS int `toml:"busy_timeout_ms"`
	// JournalMode 默认 WAL。
	JournalMode string `toml:"journal_mode"`
}

// Namespace 是 [namespace] 段。
type Namespace struct {
	// TTLHours 是 namespace 存活小时数，-1 = 永久。
	TTLHours int `toml:"ttl_hours"`
	// GCIntervalMinutes 是惰性 GC 间隔。
	GCIntervalMinutes int `toml:"gc_interval_minutes"`
	// StrictCwd 为真时 ns 记录的 cwd 与当前 cwd 不符则拒绝（防跨项目串锚点）。
	// 指针：区分「没写」与「显式写 false」。
	StrictCwd *bool `toml:"strict_cwd"`
}

// StrictCwdEnabled 返回生效值，默认 true。
func (n Namespace) StrictCwdEnabled() bool {
	if n.StrictCwd == nil {
		return true
	}
	return *n.StrictCwd
}

// StrictCwdCheck 返回本次调用是否要做 "ns 记录的 cwd 与当前 cwd 是否一致" 的检查。
//
// 配置路径被 $RWEG_PIN_CONFIG 钉住时不做：钉住即"这就是同一个项目"，
// 此后 cd 到哪都是同一份配置、同一个库，锚点不该因为换了个目录就作废。
// 手工跨终端共享 $RWEG_NS（没钉配置）时检查照旧，防串锚点。
func (r Resolved) StrictCwdCheck() bool {
	return r.Cfg.Namespace.StrictCwdEnabled() && !r.Pinned
}

// Hash 是 [hash] 段。
type Hash struct {
	CanonVersion int `toml:"canon_version"`
	ProbeStride  int `toml:"probe_stride"`
}

// Read 是 [read] 段。
type Read struct {
	MaxLineBytes int `toml:"max_line_bytes"`
	MaxLines     int `toml:"max_lines"`
}

// Edit 是 [edit] 段。
type Edit struct {
	MaxChanges    int `toml:"max_changes"`
	ServedEchoCap int `toml:"served_echo_cap"`
}

// Grep 是 [grep] 段。
type Grep struct {
	RgPath string `toml:"rg_path"`
	// FallbackToRg 为真时 rg 不可用/报错则退化为原生行为。
	// 指针：区分「没写」与「显式写 false」。
	FallbackToRg *bool `toml:"fallback_to_rg"`
}

// FallbackEnabled 返回生效值，默认 true。
func (g Grep) FallbackEnabled() bool {
	if g.FallbackToRg == nil {
		return true
	}
	return *g.FallbackToRg
}

// Output 是 [output] 段。
type Output struct {
	Encoding string `toml:"encoding"`
	Newline  string `toml:"newline"`
}

// Config 是一份完整的生效配置（已填默认值）。
//
// 刻意**没有 [lookup] 段** —— 查找深度只由 $RWEG_WALKUP 环境变量决定。
// 理由：查找行为需要在读到任何配置文件**之前**就确定，
// 放进配置文件里会造成「配置自己才能改变自己是否被找到」的循环依赖。
type Config struct {
	DB        DB        `toml:"db"`
	Namespace Namespace `toml:"namespace"`
	Hash      Hash      `toml:"hash"`
	Read      Read      `toml:"read"`
	Edit      Edit      `toml:"edit"`
	Grep      Grep      `toml:"grep"`
	Output    Output    `toml:"output"`
}

// Default 返回内置默认配置。这是「找不到任何 rweg.toml」时的兜底，
// 本身就是一份能跑的配置。
func Default() Config {
	yes := true
	return Config{
		DB: DB{
			Dir:           ".db/",
			BusyTimeoutMS: 3000,
			JournalMode:   "WAL",
		},
		Namespace: Namespace{
			TTLHours:          24, // ns 生命周期 = 终端生命周期
			GCIntervalMinutes: 60,
			StrictCwd:         &yes,
		},
		Hash: Hash{
			CanonVersion: 2,
			ProbeStride:  3907,
		},
		Read: Read{
			MaxLineBytes: 6000,
			MaxLines:     20000,
		},
		Edit: Edit{
			MaxChanges:    32,
			ServedEchoCap: 150,
		},
		Grep: Grep{
			RgPath:       "", // 空 = 从 PATH 找
			FallbackToRg: &yes,
		},
		Output: Output{
			Encoding: "utf-8",
			Newline:  "preserve",
		},
	}
}

// Home 返回安装目录：$RWEG_HOME，缺省 ~/.rweg。
// $RWEG_HOME 允许写 Git Bash 的 /c/... 形态（见 NativePath）。
func Home() string {
	if h := os.Getenv("RWEG_HOME"); h != "" {
		return NativePath(h)
	}
	return filepath.Join(userHome(), DefaultHome)
}

// WalkUpDepth 解析 $RWEG_WALKUP：向上查找的父目录层数。
//
//	未设置 / 非法 → DefaultWalkUp（0 = 只找 cwd）
//	0 → 只找 cwd
//	1 → cwd + 父目录
//	2 → cwd + 父+ 祖父
//	…  最大 MaxWalkUp（32）
//
// 刻意只认环境变量、不认配置文件：查找行为必须在读到任何配置**之前**确定，
// 否则「配置自己决定自己是否被找到」构成循环依赖。
func WalkUpDepth(getenv func(string) string) int {
	raw := strings.TrimSpace(getenv("RWEG_WALKUP"))
	if raw == "" {
		return DefaultWalkUp
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return DefaultWalkUp
	}
	if n > MaxWalkUp {
		return MaxWalkUp
	}
	return n
}

func userHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	if h := os.Getenv("USERPROFILE"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return "."
}

// BinDir 返回安装目录下的 bin/。
func BinDir() string { return filepath.Join(Home(), "bin") }

// ScriptsDir 返回安装目录下的 scripts/。
func ScriptsDir() string { return filepath.Join(Home(), "scripts") }

// HomeDBDir 返回安装目录下的 db/（安装级默认数据库目录）。
func HomeDBDir() string { return filepath.Join(Home(), "db") }

// HomeConfigPath 返回安装级默认配置路径 <home>/rweg.toml。
func HomeConfigPath() string { return filepath.Join(Home(), ConfigName) }

// CleanAbs 归一化路径：转绝对路径、清掉分隔符噪音。Windows 下统一用反斜杠。
// 先过 NativePath，故 /c/proj/x.ts 这类 Git Bash 写法也算绝对路径。
func CleanAbs(p, base string) string {
	p = NativePath(p)
	if p == "" {
		p = base
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Join(base, p)
	}
	return filepath.Clean(abs)
}

// OnPath 报告 exe 是否在 PATH 里（doctor 用）。
// PATH 的条目按 NativePath 归一 —— MSYS_NO_PATHCONV=1 的 shell 里
// 它们是 /c/... 形态，直接 filepath.Join 会拼出 C:\c\... 而查不到。
func OnPath(exe string) bool {
	pathext := ".exe"
	if runtime.GOOS != "windows" {
		pathext = ""
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(NativePath(dir), exe+pathext)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return true
		}
	}
	return false
}

// IsDirEmpty 判断目录是否为空或不存在。
func IsDirEmpty(dir string) bool {
	ents, err := os.ReadDir(dir)
	return err != nil || len(ents) == 0
}

// SanitizeNS 把 namespace 规整成可安全用作文件名的形式。
// ns 形如 wK3fP-9xT2q，只含 base62 与连字符；这里额外兜住异常输入。
func SanitizeNS(ns string) string {
	if ns == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range ns {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := b.String()
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
