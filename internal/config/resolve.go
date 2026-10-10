package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Resolved 是一次查找的完整结果，含来源与已解析的绝对路径。
type Resolved struct {
	// Cfg 是生效配置（内置默认值 + 配置文件覆盖）。
	Cfg Config
	// Path 是生效配置文件绝对路径；空 = 用了内置默认值。
	Path string
	// BaseDir 是相对路径的解析基准 = 配置文件所在目录。
	// Path 为空（内置默认值）时基准是 Home，见 LoadFrom 第 5 步。
	BaseDir string
	// Source 是人类可读的来源说明，用于 --verbose / cfg / doctor。
	Source string
	// Home 是安装目录。
	Home string
	// Cwd 是查找起点的 cwd。
	Cwd string
	// WalkUp 是本次使用的父级深度（$RWEG_WALKUP 的值）。
	WalkUp int
	// FoundAtLevel 是命中层级：0 = cwd 本身，N = 往上 N 级。
	// 未命中 cwd 链（走了安装级或内置默认）时为 -1。
	FoundAtLevel int
	// Pinned 表示配置路径来自 $RWEG_PIN_CONFIG（由 env.sh 在 source 时钉住）。
	// 钉住之后 cd 不再改判配置落点：库位置与 served 基线都跟着不变。
	Pinned bool
}

// DBDir 返回数据库**目录**的绝对路径。
//
// 永远是 <基准目录>/<cfg.db.dir>。因为基准是 BaseDir 而非 cwd，
// cd 到别处跑不会改变 db 位置。
func (r Resolved) DBDir() string {
	return CleanAbs(r.Cfg.DB.Dir, r.BaseDir)
}

// DBPath 返回本次 namespace 对应的数据库文件绝对路径。
//
// **一个 namespace 一个文件**：<db.dir>/<namespace>.db
// 例：配置文件 d:\nginx\2dsim\rweg.toml、db.dir = ".db/"、ns = wK3fP-9xT2q
//
//	→ d:\nginx\2dsim\.db\wK3fP-9xT2q.db
//
// 刻意不提供「固定文件名」模式：那会让多个 namespace 指向同一个库文件，
// 重新引入 served 状态的跨终端竞争。要共享就显式复用同一个 $RWEG_NS。
func (r Resolved) DBPath(ns string) string {
	return filepath.Join(r.DBDir(), SanitizeNS(ns)+".db")
}

// Load 按查找顺序解析出生效配置。
//
// 查找顺序：
//  1. $RWEG_CONFIG           显式指定，最高优先级；指向不存在则报错
//  2. $RWEG_PIN_CONFIG        source 时钉住的路径；存在即用，不存在则忽略
//  2. <cwd>/rweg.toml
//  3. <cwd>/../rweg.toml ... 向上最多 $RWEG_WALKUP 级父目录
//     $RWEG_WALKUP=0（缺省）→ 只找 cwd
//     $RWEG_WALKUP=1         → cwd + 父目录
//     $RWEG_WALKUP=2         → cwd + 父 + 祖父
//     $RWEG_WALKUP=N         → cwd + N 级父目录（上限 32）
//  4. $RWEG_HOME/rweg.toml   安装目录下的默认配置
//  5. 内置默认值             找不到任何配置时的兜底
//
// 任何一步失败都不阻塞：解析不了的键忽略、文件读不到就跳过，
// 最终一定返回一份可用配置。唯一例外是 $RWEG_CONFIG 指向的文件不存在 ——
// 那是用户明确指定的路径，静默忽略比报错更坏。
func Load() (Resolved, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	return LoadFrom(cwd, os.Getenv)
}

// GetenvFunc 是环境变量读取函数签名，便于测试注入。
type GetenvFunc func(string) string

// LoadFrom 是可注入的 Load 变体，供测试与 doctor 使用。
func LoadFrom(cwd string, getenv GetenvFunc) (Resolved, error) {
	depth := WalkUpDepth(getenv)
	r := Resolved{
		Cfg:          Default(),
		Home:         Home(),
		Cwd:          cwd,
		BaseDir:      cwd,
		WalkUp:       depth,
		FoundAtLevel: -1,
	}

	// 1. RWEG_CONFIG
	if p := strings.TrimSpace(getenv("RWEG_CONFIG")); p != "" {
		abs := CleanAbs(p, cwd)
		if _, err := os.Stat(abs); err != nil {
			return r, fmt.Errorf("RWEG_CONFIG=%s not found", abs)
		}
		res := apply(r, abs, "$RWEG_CONFIG")
		res.FoundAtLevel = 0
		return res, nil
	}

	// 2. $RWEG_PIN_CONFIG：env.sh 在 source 时算好的配置文件路径。
	//
	// 摆在"现找"之前，且只认**存在**的文件：cd 到子目录后（那里往往没有
	// rweg.toml）不再改判落点，库与 served 基线自然跟着不变。同一 shell 里
	// 想换项目，重写这个变量即可；它不存在时静默回落到下面的查找。
	if p := strings.TrimSpace(getenv("RWEG_PIN_CONFIG")); p != "" {
		if abs := CleanAbs(p, cwd); fileExists(abs) {
			res := apply(r, abs, "$RWEG_PIN_CONFIG")
			res.Pinned = true
			res.FoundAtLevel = -1
			return res, nil
		}
	}

	// 2 + 3. cwd 及其 $RWEG_WALKUP 级父目录，由近及远
	if found, level, ok := findUpward(cwd, depth); ok {
		res := apply(r, found, sourceLabel(level))
		res.FoundAtLevel = level
		return res, nil
	}

	// 4. 安装目录下的默认配置
	if hc := HomeConfigPath(); fileExists(hc) {
		res := apply(r, hc, "$RWEG_HOME")
		res.FoundAtLevel = -1
		return res, nil
	}

	// 5. 内置默认值
	// 基准是**安装目录**而非 cwd —— 安装级配置一定在，兜底也归安装目录，
	// 这样「配置在哪，数据库就在哪下面」这条规则始终成立。
	r.BaseDir = r.Home
	r.Source = "<defaults> (no " + FileName + " found; paths relative to $RWEG_HOME)"
	return r, nil
}

// findUpward 从 cwd 开始向上找 rweg.toml，最多 depth 级父目录。
//
//	depth=0 → 只看 cwd
//	depth=1 → cwd + 父目录
//	depth=N → cwd + N 级父目录
//
// 返回命中路径、命中层级（0=cwd）、是否命中。越靠近 cwd 优先级越高。
func findUpward(cwd string, depth int) (string, int, bool) {
	dir := cwd
	for level := 0; level <= depth; level++ {
		if p := filepath.Join(dir, FileName); fileExists(p) {
			return p, level, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // 到根了
		}
		dir = parent
	}
	return "", -1, false
}

// sourceLabel 渲染查找来源标签。
func sourceLabel(level int) string {
	if level == 0 {
		return "cwd"
	}
	return fmt.Sprintf("RWEG_WALKUP+%d", level)
}

// pathDepth 返回 from 到 to 之间隔了几级。
func pathDepth(from, to string) int {
	f, err := filepath.Abs(from)
	if err != nil {
		f = from
	}
	t, err := filepath.Abs(to)
	if err != nil {
		t = to
	}
	depth := 0
	for cur := f; cur != t && cur != filepath.Dir(cur); cur = filepath.Dir(cur) {
		depth++
	}
	return depth
}

// apply 读配置文件并合并到默认值上。
func apply(r Resolved, path, source string) Resolved {
	r.Path = path
	r.BaseDir = filepath.Dir(path)
	r.Source = fmt.Sprintf("%s %s", source, path)
	if _, err := toml.DecodeFile(path, &r.Cfg); err != nil {
		// 配置文件坏了不阻塞：退回默认值继续跑，但把错误标出来让 doctor 能发现。
		r.Cfg = Default()
		r.Source += "  [PARSE ERROR: " + err.Error() + "]"
		return r
	}
	r.Cfg = withFallbacks(r.Cfg)
	return r
}

// withFallbacks 补齐零值字段（配置文件只写了一部分时用默认值补）。
// 刻意不覆盖 bool：Go 的 bool 零值 false 无法区分「没写」与「显式写 false」，
// 所以需要显式关闭的开关一律用 *bool 承载。
func withFallbacks(c Config) Config {
	d := Default()
	if c.DB.Dir == "" {
		c.DB.Dir = d.DB.Dir
	}
	if c.DB.BusyTimeoutMS <= 0 {
		c.DB.BusyTimeoutMS = d.DB.BusyTimeoutMS
	}
	if c.DB.JournalMode == "" {
		c.DB.JournalMode = d.DB.JournalMode
	}
	if c.Namespace.TTLHours == 0 {
		c.Namespace.TTLHours = d.Namespace.TTLHours
	}
	if c.Namespace.GCIntervalMinutes <= 0 {
		c.Namespace.GCIntervalMinutes = d.Namespace.GCIntervalMinutes
	}
	if c.Hash.CanonVersion <= 0 {
		c.Hash.CanonVersion = d.Hash.CanonVersion
	}
	if c.Hash.ProbeStride <= 0 {
		c.Hash.ProbeStride = d.Hash.ProbeStride
	}
	if c.Read.MaxLineBytes <= 0 {
		c.Read.MaxLineBytes = d.Read.MaxLineBytes
	}
	if c.Read.MaxLines <= 0 {
		c.Read.MaxLines = d.Read.MaxLines
	}
	if c.Edit.MaxChanges <= 0 {
		c.Edit.MaxChanges = d.Edit.MaxChanges
	}
	if c.Edit.ServedEchoCap <= 0 {
		c.Edit.ServedEchoCap = d.Edit.ServedEchoCap
	}
	if c.Output.Encoding == "" {
		c.Output.Encoding = d.Output.Encoding
	}
	if c.Output.Newline == "" {
		c.Output.Newline = d.Output.Newline
	}
	return c
}

// Diagnostics 渲染 --verbose / cfg / doctor 用的诊断块（走 stderr）。
func (r Resolved) Diagnostics(ns string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[cfg] home    %s\n", r.Home)
	fmt.Fprintf(&b, "[cfg] cwd     %s\n", r.Cwd)
	fmt.Fprintf(&b, "[cfg] walkup  %s\n", walkupLabel(r.WalkUp))
	fmt.Fprintf(&b, "[cfg] config  %s\n", r.Source)
	fmt.Fprintf(&b, "[cfg] basedir %s\n", r.BaseDir)
	fmt.Fprintf(&b, "[cfg] dbdir   %s\n", r.DBDir())
	if ns != "" {
		fmt.Fprintf(&b, "[cfg] db      %s\n", r.DBPath(ns))
	}
	fmt.Fprintf(&b, "[cfg] ns      %s\n", displayNS(ns))
	fmt.Fprintf(&b, "[cfg] ttl     %dh\n", r.Cfg.Namespace.TTLHours)
	fmt.Fprintf(&b, "[cfg] hashlib canon v%d, stride %d, space %d\n",
		r.Cfg.Hash.CanonVersion, r.Cfg.Hash.ProbeStride, 62*62*62)
	return b.String()
}

func walkupLabel(depth int) string {
	if depth == 0 {
		return "0 (cwd only)"
	}
	return fmt.Sprintf("%d (cwd + %d parent level(s))", depth, depth)
}

func displayNS(ns string) string {
	if ns == "" {
		return "<unset>"
	}
	return ns
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
