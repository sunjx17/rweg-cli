package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile 写一个临时文件（目录会自动创建）。
func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// envNone 什么都不返回（等价于 RWEG_WALKUP 未设置）。
func envNone(string) string { return "" }

// envMap 从 map 构造 getenv。
func envMap(m map[string]string) GetenvFunc {
	return func(k string) string { return m[k] }
}

// walkup 构造一个只设 RWEG_WALKUP=n 的环境。
func walkup(n int) GetenvFunc {
	return envMap(map[string]string{"RWEG_WALKUP": itoa(n)})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// withHome 临时把 RWEG_HOME 指到 t.TempDir()，返回该目录。
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("RWEG_HOME", home)
	return home
}

// mkdirs 造出多级子目录并返回最深那级。
func mkdirs(t *testing.T, base string, rel ...string) string {
	t.Helper()
	p := base
	for _, r := range rel {
		p = filepath.Join(p, r)
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalCfg = "[db]\ndir = \".db/\"\n"

// ─── 规则 1：cwd 有 rweg.toml 就用它（缺省行为）───

func TestCwdConfigWins(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), minimalCfg)

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != filepath.Join(proj, FileName) {
		t.Errorf("config = %q, want cwd one", r.Path)
	}
	if r.FoundAtLevel != 0 {
		t.Errorf("FoundAtLevel = %d, want 0", r.FoundAtLevel)
	}
	if got, want := r.DBPath("wK3fP-9xT2q"),
		filepath.Join(proj, ".db", "wK3fP-9xT2q.db"); got != want {
		t.Errorf("db path = %q\nwant         %q", got, want)
	}
}

// ─── 规则 2：RWEG_WALKUP 定父级深度 ───
// 0=只找 cwd，1=+父，2=+祖父。逐级验证。

func TestWalkUp0OnlyCwd(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	root := writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	sub := mkdirs(t, proj, "src")

	r, err := LoadFrom(sub, walkup(0))
	if err != nil {
		t.Fatal(err)
	}
	if r.Path == root {
		t.Error("depth=0 must not reach project root config")
	}
	if r.FoundAtLevel == 0 {
		t.Error("depth=0 must not look at parents")
	}
}

func TestWalkUp1CwdPlusParent(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	root := writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	sub := mkdirs(t, proj, "src")

	r, err := LoadFrom(sub, walkup(1))
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != root {
		t.Errorf("config = %q, want %q", r.Path, root)
	}
	if r.FoundAtLevel != 1 {
		t.Errorf("FoundAtLevel = %d, want 1", r.FoundAtLevel)
	}
	if r.BaseDir != proj {
		t.Errorf("BaseDir = %q, want %q", r.BaseDir, proj)
	}
	if got, want := r.DBPath("ns1"), filepath.Join(proj, ".db", "ns1.db"); got != want {
		t.Errorf("db = %q, want %q (相对配置目录)", got, want)
	}
}

func TestWalkUp2ReachesGrandparent(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	root := writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	deep := mkdirs(t, proj, "src", "deep")

	r, err := LoadFrom(deep, walkup(2))
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != root {
		t.Errorf("config = %q, want %q", r.Path, root)
	}
	if r.FoundAtLevel != 2 {
		t.Errorf("FoundAtLevel = %d, want 2", r.FoundAtLevel)
	}
}

// depth=2 但配置在曾祖父级 → 够不到。
func TestWalkUp2StopsShort(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	root := writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	deep := mkdirs(t, proj, "a", "b", "c") // root 之上 3 级

	r, err := LoadFrom(deep, walkup(2))
	if err != nil {
		t.Fatal(err)
	}
	if r.Path == root {
		t.Errorf("depth=2 must not reach 3 levels up (config %q)", root)
	}
}

func TestWalkUp3ReachesGreatGrandparent(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	root := writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	deep := mkdirs(t, proj, "a", "b", "c")

	r, err := LoadFrom(deep, walkup(3))
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != root {
		t.Errorf("config = %q, want %q", r.Path, root)
	}
	if r.FoundAtLevel != 3 {
		t.Errorf("FoundAtLevel = %d, want 3", r.FoundAtLevel)
	}
}

// 更近的配置优先：cwd 与父目录都有时取 cwd。
func TestNearerConfigWins(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), "[db]\ndir = \"root/\"\n")
	sub := mkdirs(t, proj, "src")
	subCfg := writeFile(t, filepath.Join(sub, FileName), "[db]\ndir = \"sub/\"\n")

	r, err := LoadFrom(sub, walkup(1))
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != subCfg {
		t.Errorf("config = %q, want nearer %q", r.Path, subCfg)
	}
	if r.FoundAtLevel != 0 {
		t.Errorf("FoundAtLevel = %d, want 0", r.FoundAtLevel)
	}
	if got := filepath.Base(r.DBDir()); got != "sub" {
		t.Errorf("dbdir base = %q, want sub", got)
	}
	if got, want := r.DBPath("ns1"), filepath.Join(sub, "sub", "ns1.db"); got != want {
		t.Errorf("db = %q, want %q", got, want)
	}
}

func TestWalkUpCappedAtMax(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	deep := mkdirs(t, proj, "a", "b", "c", "d", "e")
	r, err := LoadFrom(deep, walkup(9999))
	if err != nil {
		t.Fatal(err)
	}
	if r.WalkUp != MaxWalkUp {
		t.Errorf("WalkUp = %d, want capped %d", r.WalkUp, MaxWalkUp)
	}
}

func TestWalkUpDepthParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", DefaultWalkUp},   // 未设置
		{"0", 0},              // 只找 cwd
		{"1", 1},              // +父
		{"2", 2},              // +祖父
		{"10", 10},            //
		{"  3  ", 3},          // 带空格
		{"-1", DefaultWalkUp}, // 非法 → 缺省
		{"abc", DefaultWalkUp},
		{"1.5", DefaultWalkUp},
		{"999", MaxWalkUp}, // 超上限 → 截断
	}
	for _, c := range cases {
		got := WalkUpDepth(func(k string) string {
			if k == "RWEG_WALKUP" {
				return c.raw
			}
			return ""
		})
		if got != c.want {
			t.Errorf("WalkUpDepth(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

// ─── 规则 3：cwd 链都没命中 → 安装级配置 ───

func TestFallsBackToHomeConfig(t *testing.T) {
	home := withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(home, ConfigName), "[db]\ndir = \"db/\"\n")

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != filepath.Join(home, ConfigName) {
		t.Errorf("config = %q, want home one %q", r.Path, filepath.Join(home, ConfigName))
	}
	if !strings.Contains(r.Source, "$RWEG_HOME") {
		t.Errorf("Source = %q, want $RWEG_HOME marker", r.Source)
	}
	// 安装级配置的相对路径相对于安装目录，不是 cwd
	if got, want := r.DBPath("wABC-12345"), filepath.Join(home, "db", "wABC-12345.db"); got != want {
		t.Errorf("db path = %q\nwant         %q", got, want)
	}
	if r.FoundAtLevel != -1 {
		t.Errorf("FoundAtLevel = %d, want -1 (not found on cwd chain)", r.FoundAtLevel)
	}
}

// 安装级配置优先于深walk-up：cwd 链上什么都没有时，深度不影响优先级。
func TestHomeConfigBeatsDeepWalkUp(t *testing.T) {
	home := withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(home, ConfigName), "[db]\ndir = \"homedb/\"\n")
	deep := mkdirs(t, proj, "a", "b", "c")

	r, err := LoadFrom(deep, walkup(32))
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != filepath.Join(home, ConfigName) {
		t.Errorf("config = %q, want home one", r.Path)
	}
}

// ─── 规则 4：无任何配置 → 相对 $RWEG_HOME ───

func TestDefaultsRelativeToHome(t *testing.T) {
	home := withHome(t)
	proj := t.TempDir()

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "" {
		t.Errorf("Path = %q, want empty (defaults)", r.Path)
	}
	if r.BaseDir != home {
		t.Errorf("BaseDir = %q, want $RWEG_HOME %q", r.BaseDir, home)
	}
	// 关键：不相对 cwd
	if got, want := r.DBPath("ns1"), filepath.Join(home, ".db", "ns1.db"); got != want {
		t.Errorf("db = %q\nwant  %q (relative to $RWEG_HOME, NOT cwd)", got, want)
	}
	if r.DBDir() == filepath.Join(proj, ".db") {
		t.Error("defaults must NOT resolve relative to cwd")
	}
	if !strings.Contains(r.Source, "$RWEG_HOME") {
		t.Errorf("Source = %q, want mention of $RWEG_HOME", r.Source)
	}
}

// 兜底基准与 cwd 无关：换目录跑，db 位置不动。
func TestDefaultsIndependentOfCwd(t *testing.T) {
	home := withHome(t)
	a := t.TempDir()
	b := t.TempDir()

	r1, _ := LoadFrom(a, envNone)
	r2, _ := LoadFrom(b, envNone)
	if r1.DBPath("ns1") != r2.DBPath("ns1") {
		t.Errorf("db path drifted with cwd:\n  %s\n  %s", r1.DBPath("ns1"), r2.DBPath("ns1"))
	}
	if !strings.HasPrefix(r1.DBPath("ns1"), home) {
		t.Errorf("db %q not under $RWEG_HOME %q", r1.DBPath("ns1"), home)
	}
}

// ─── RWEG_CONFIG 最高优先级 ───

func TestEnvConfigWins(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	elsewhere := t.TempDir()
	explicit := writeFile(t, filepath.Join(elsewhere, "custom.toml"), "[db]\ndir = \"x/\"\n")

	r, err := LoadFrom(proj, envMap(map[string]string{
		"RWEG_CONFIG": explicit, "RWEG_WALKUP": "5",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != explicit {
		t.Errorf("config = %q, want %q", r.Path, explicit)
	}
	if got := r.DBPath("ns1"); filepath.Base(got) != "ns1.db" ||
		filepath.Base(filepath.Dir(got)) != "x" {
		t.Errorf("db path = %q, want <elsewhere>\\x\\ns1.db", got)
	}
}

func TestEnvConfigMissingIsError(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	_, err := LoadFrom(proj, envMap(map[string]string{
		"RWEG_CONFIG": filepath.Join(proj, "nope.toml"),
	}))
	if err == nil {
		t.Fatal("expected error for missing RWEG_CONFIG target")
	}
}

// ─── db 恒相对于「配置文件所在目录」───

func TestDBPathIndependentOfCwd(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	deep := mkdirs(t, proj, "a", "b", "c", "d", "e")

	want := filepath.Join(proj, ".db", "ns1.db")
	for _, cwd := range []string{proj, filepath.Join(proj, "a"), deep} {
		r, err := LoadFrom(cwd, walkup(9))
		if err != nil {
			t.Fatal(err)
		}
		if got := r.DBPath("ns1"); got != want {
			t.Errorf("cwd=%s → db=%q, want %q", cwd, got, want)
		}
	}
}

// ─── 一个 ns 一个文件（无 [db].file 字段）───

func TestPerNamespaceDBFiles(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), minimalCfg)

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if r.DBPath("nsA") == r.DBPath("nsB") {
		t.Error("different namespaces must get different db files")
	}
	if got := filepath.Base(r.DBPath("nsA")); got != "nsA.db" {
		t.Errorf("db file = %q, want nsA.db", got)
	}
}

// 配置里写了 [db].file 也无效 —— 字段已移除。
func TestDBFileFieldRemoved(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), "[db]\nfile = \"shared.db\"\n")

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(r.DBPath("nsA")); got != "nsA.db" {
		t.Errorf("db file = %q, want nsA.db ([db].file must be ignored)", got)
	}
	if r.DBPath("nsA") == r.DBPath("nsB") {
		t.Error("must never share one db file across namespaces")
	}
}

// ─── 绝对 db.dir 不做相对解析 ───

func TestAbsoluteDBDirPreserved(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	abs := filepath.Join(t.TempDir(), "shared-db")
	writeFile(t, filepath.Join(proj, FileName), "[db]\ndir = \""+filepathToToml(abs)+"\"\n")

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.DBPath("ns1"); got != filepath.Join(abs, "ns1.db") {
		t.Errorf("db = %q, want %q", got, filepath.Join(abs, "ns1.db"))
	}
}

func filepathToToml(p string) string {
	r := []rune(p)
	for i, c := range r {
		if c == '\\' {
			r[i] = '/'
		}
	}
	return string(r)
}

// ─── 配置文件损坏不阻塞 ───

func TestBrokenConfigFallsBackToDefaults(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), "this is not = = valid toml [[[")

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatalf("broken config must not error, got %v", err)
	}
	if r.Cfg.DB.Dir != ".db/" {
		t.Errorf("db dir = %q, want default", r.Cfg.DB.Dir)
	}
	if !strings.Contains(r.Source, "PARSE ERROR") {
		t.Errorf("Source = %q, want PARSE ERROR marker", r.Source)
	}
}

// 损坏的配置仍决定基准目录（不能退回 $RWEG_HOME）。
func TestBrokenConfigStillSetsBaseDir(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), "= = broken [")

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if r.BaseDir != proj {
		t.Errorf("BaseDir = %q, want %q even for broken config", r.BaseDir, proj)
	}
}

// ─── 部分配置用默认值补齐 ───

func TestPartialConfigGetsDefaults(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), "[db]\ndir = \"custom/\"\n")

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cfg.DB.Dir != "custom/" {
		t.Errorf("db dir = %q, want custom/", r.Cfg.DB.Dir)
	}
	if r.Cfg.Hash.ProbeStride != 3907 || r.Cfg.Read.MaxLines != 20000 {
		t.Errorf("defaults not applied: %+v %+v", r.Cfg.Hash, r.Cfg.Read)
	}
	if !r.Cfg.Grep.FallbackEnabled() {
		t.Error("fallback_to_rg should default true when omitted")
	}
	if !r.Cfg.Namespace.StrictCwdEnabled() {
		t.Error("strict_cwd should default true when omitted")
	}
}

func TestExplicitFalseBoolIsHonored(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName),
		"[grep]\nfallback_to_rg = false\n[namespace]\nstrict_cwd = false\n")

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cfg.Grep.FallbackEnabled() {
		t.Error("fallback_to_rg = false was not honored")
	}
	if r.Cfg.Namespace.StrictCwdEnabled() {
		t.Error("strict_cwd = false was not honored")
	}
}

// ─── 安装目录相关 ───

func TestHomeDefaultsToDotRweg(t *testing.T) {
	t.Setenv("RWEG_HOME", "")
	if got := Home(); filepath.Base(got) != ".rweg" {
		t.Errorf("Home() = %q, want ending in .rweg", got)
	}
}

func TestHomeOverridable(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "rweg-alt")
	t.Setenv("RWEG_HOME", custom)
	if got := Home(); got != custom {
		t.Errorf("Home() = %q, want %q", got, custom)
	}
	if got := BinDir(); got != filepath.Join(custom, "bin") {
		t.Errorf("BinDir() = %q", got)
	}
	if got := ScriptsDir(); got != filepath.Join(custom, "scripts") {
		t.Errorf("ScriptsDir() = %q", got)
	}
	if got := HomeConfigPath(); got != filepath.Join(custom, "rweg.toml") {
		t.Errorf("HomeConfigPath() = %q", got)
	}
}

// RWEG_HOME 改了，兜底 db 跟着改。
func TestHomeOverrideChangesDefaultDB(t *testing.T) {
	withHome(t)
	custom := filepath.Join(t.TempDir(), "alt-home")
	t.Setenv("RWEG_HOME", custom)
	proj := t.TempDir()

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.DBPath("ns1"); got != filepath.Join(custom, ".db", "ns1.db") {
		t.Errorf("db = %q, want under %q", got, custom)
	}
}

// ─── SanitizeNS ───

func TestSanitizeNS(t *testing.T) {
	if got := SanitizeNS("wK3fP-9xT2q"); got != "wK3fP-9xT2q" {
		t.Errorf("normal ns mangled: %q", got)
	}
	if got := SanitizeNS(""); got != "unnamed" {
		t.Errorf("empty ns = %q", got)
	}
	if got := SanitizeNS("../../etc/passwd"); got != "______etc_passwd" {
		t.Errorf("path traversal not neutralized: %q", got)
	}
	if strings.ContainsAny(SanitizeNS("../../x"), `/\`) {
		t.Error("sanitized ns must not contain separators")
	}
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'a'
	}
	if got := SanitizeNS(string(long)); len(got) != 64 {
		t.Errorf("long ns len = %d, want 64", len(got))
	}
}

// ─── Diagnostics ───

func TestDiagnosticsRenders(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), minimalCfg)

	r, err := LoadFrom(proj, envNone)
	if err != nil {
		t.Fatal(err)
	}
	out := r.Diagnostics("wK3fP-9xT2q")
	for _, want := range []string{"home", "cwd", "walkup", "config", "basedir",
		"dbdir", "ns", "ttl", "hashlib", "wK3fP-9xT2q.db"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnostics missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "0 (cwd only)") {
		t.Errorf("diagnostics should state walkup depth 0:\n%s", out)
	}
	if out := r.Diagnostics(""); !strings.Contains(out, "<unset>") {
		t.Errorf("empty ns diagnostics:\n%s", out)
	}
}

func TestDiagnosticsShowsWalkUpDepth(t *testing.T) {
	withHome(t)
	proj := t.TempDir()
	r, _ := LoadFrom(proj, walkup(2))
	if !strings.Contains(r.Diagnostics(""), "2 (cwd + 2 parent level(s))") {
		t.Errorf("depth 2 not shown:\n%s", r.Diagnostics(""))
	}
}

// $RWEG_PIN_CONFIG 是 env.sh 在 source 时钉住的配置路径：它排在"按 cwd 现找"
// 之前，只认**存在**的文件 —— 于是 cd 到没有 rweg.toml 的子目录也不再改判落点。
func TestPinConfigWins(t *testing.T) {
	proj := t.TempDir()
	pinned := writeFile(t, filepath.Join(proj, "proj", FileName), minimalCfg)
	// deep 里没有 rweg.toml：不钉的话 walkup=0 会一路回落到安装级配置，
	// 钉住之后必须仍然用项目那份。
	deep := mkdirs(t, proj, "proj", "src", "deep")

	r, err := LoadFrom(deep, envMap(map[string]string{"RWEG_PIN_CONFIG": pinned}))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Pinned {
		t.Error("Pinned should be true")
	}
	if r.Path != pinned {
		t.Errorf("config = %q, want pinned %q", r.Path, pinned)
	}
	if r.FoundAtLevel != -1 {
		t.Errorf("FoundAtLevel = %d, want -1 (not found on the cwd chain)", r.FoundAtLevel)
	}
	if !strings.Contains(r.Source, "$RWEG_PIN_CONFIG") {
		t.Errorf("source = %q, want $RWEG_PIN_CONFIG", r.Source)
	}
}

func TestPinConfigIgnoredWhenMissing(t *testing.T) {
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), minimalCfg)

	// 钉住的路径不存在（比如项目被删了）→ 静默回落到常规查找
	r, err := LoadFrom(proj, envMap(map[string]string{
		"RWEG_PIN_CONFIG": filepath.Join(proj, "gone", FileName),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Pinned {
		t.Error("Pinned should be false when the pinned file is gone")
	}
	if r.Path != filepath.Join(proj, FileName) {
		t.Errorf("config = %q, want the cwd one", r.Path)
	}
}

func TestExplicitConfigBeatsPin(t *testing.T) {
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	pinned := writeFile(t, filepath.Join(proj, "other", FileName), minimalCfg)

	r, err := LoadFrom(proj, envMap(map[string]string{
		"RWEG_CONFIG":     filepath.Join(proj, FileName),
		"RWEG_PIN_CONFIG": pinned,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Pinned {
		t.Error("RWEG_CONFIG is explicit; it must not be marked as pinned")
	}
	if r.Path != filepath.Join(proj, FileName) {
		t.Errorf("config = %q, want $RWEG_CONFIG one", r.Path)
	}
}

// 配置被钉住时不做 "ns 记录的 cwd 与当前 cwd 一致" 的检查：
// 钉住即同一个项目，cd 之后仍是同一份配置与同一个库。
func TestStrictCwdCheckSkipsPinned(t *testing.T) {
	proj := t.TempDir()
	writeFile(t, filepath.Join(proj, FileName), minimalCfg)
	pinned := writeFile(t, filepath.Join(proj, "proj", FileName), minimalCfg)

	r, err := LoadFrom(proj, envMap(map[string]string{"RWEG_PIN_CONFIG": pinned}))
	if err != nil {
		t.Fatal(err)
	}
	if r.StrictCwdCheck() {
		t.Error("pinned config: strict cwd check should be off")
	}
	r.Pinned = false
	if !r.StrictCwdCheck() {
		t.Error("unpinned config: strict cwd check should be on (default)")
	}
}
