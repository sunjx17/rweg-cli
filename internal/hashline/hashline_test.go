package hashline

import (
	"strings"
	"testing"
)

// 官方向量，锁死 XXH32 实现。任何一条不过说明算法被改坏，
// 锚点会与参考实现对不上。
func TestXXH32OfficialVectors(t *testing.T) {
	cases := []struct {
		in   string
		seed uint32
		want uint32
	}{
		{"", 0, 0x02cc5d05},
		{"a", 0, 0x550d7456},
		{"abc", 0, 0x32d153ff},
		{"Nobody inspects the spammish repetition", 0, 0xe2293b2f},
		{"", 1, 0x0b2cb792},
		{"a", 1, 0xf514706f},
		{"abc", 1, 0xaa3da8ff},
		{"0123456789abcdef", 0, 0xc2c45b69},
		{"hello", 0, 0xfb0077f9},
	}
	for _, c := range cases {
		got := XXH32([]byte(c.in), c.seed)
		if got != c.want {
			t.Errorf("XXH32(%q, %d) = %08x, want %08x", c.in, c.seed, got, c.want)
		}
	}
}

func TestConstants(t *testing.T) {
	if HashSpace != 238328 {
		t.Errorf("HashSpace = %d, want 238328", HashSpace)
	}
	if DefaultProbeStride != 3907 {
		t.Errorf("stride = %d, want 3907", DefaultProbeStride)
	}
	if len(Alphabet) != 62 {
		t.Errorf("alphabet len = %d, want 62", len(Alphabet))
	}
	if Alphabet[0] != 'A' || Alphabet[25] != 'Z' || Alphabet[26] != 'a' || Alphabet[61] != '9' {
		t.Errorf("alphabet order wrong: %q", Alphabet)
	}
}

// stride 与空间互质是线性探测能遍历全空间的前提。
func TestStrideCoprimeWithSpace(t *testing.T) {
	if gcd(DefaultProbeStride, HashSpace) != 1 {
		t.Fatal("stride and hash space are not coprime")
	}
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func TestCanonStripsASCIIWhitespace(t *testing.T) {
	if Canon("a b\tc\r\nd") != "abcd" {
		t.Errorf("canon = %q", Canon("a b\tc\r\nd"))
	}
	// Unicode 空白保留（参考实现边界）
	if Canon("a\u00a0b") == "ab" {
		t.Error("unicode whitespace must be preserved")
	}
}

// 实测锚点值，与参考实现 README 完全一致。这是最关键的对拍。
func TestPureHashesMatchReference(t *testing.T) {
	lines := []string{
		"function hello() {",
		`  console.log("world");`,
		"}",
		"const x = 1;",
		"let y = 2;",
		"export default hello;",
	}
	want := []string{"CTt", "Um7", "AU6", "nVR", "yAZ", "6PY"}
	got := Pure(lines, DefaultProbeStride, nil)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d (%q): got %s, want %s", i+1, lines[i], got[i], want[i])
		}
	}
}

// 同文件唯一（需求 6）：重复内容的两行 canon 相同 → 必须拿到不同锚点。
func TestSameFileUniqueness(t *testing.T) {
	lines := []string{"}", "}", "}", "}", "}"}
	got := Pure(lines, DefaultProbeStride, nil)
	seen := map[string]bool{}
	for i, h := range got {
		if seen[h] {
			t.Errorf("line %d: duplicate anchor %s", i+1, h)
		}
		seen[h] = true
	}
}

// 格式化（纯空白重排）不轮换锚点 —— 这是 canon 剔空白的全部意义。
func TestFormatOnlyChangeKeepsAnchor(t *testing.T) {
	a := Pure([]string{"function hello() {"}, DefaultProbeStride, nil)[0]
	b := Pure([]string{"function  hello() {"}, DefaultProbeStride, nil)[0]
	if a != b {
		t.Errorf("whitespace-only change rotated anchor: %s -> %s", a, b)
	}
}

// 需求 1：编辑导致行号漂移，未动行的锚点必须保留。
func TestMapStableRetainsUntouchedAnchors(t *testing.T) {
	old := []string{
		"function hello() {",
		`  console.log("world");`,
		"}",
		"const x = 1;",
		"let y = 2;",
		"export default hello;",
	}
	oldH := Pure(old, DefaultProbeStride, nil)

	// 把第 4 行替换成 3 行
	newLines := append([]string{}, old[:3]...)
	newLines = append(newLines, "const x = 42;", "const z = 3;", "let w = 4;")
	newLines = append(newLines, old[4:]...)

	newH := MapStable(old, oldH, newLines, map[string]bool{oldH[3]: true}, nil, DefaultProbeStride)

	want := map[string]string{
		oldH[0]: "function hello() {",
		oldH[1]: `  console.log("world");`,
		oldH[2]: "}",
		oldH[4]: "let y = 2;",
		oldH[5]: "export default hello;",
	}
	found := 0
	for i, oh := range oldH {
		if i == 3 {
			continue // 被替换的那行
		}
		hit := false
		for j, nh := range newH {
			if nh == oh && Canon(old[i]) == Canon(newLines[j]) {
				hit = true
				found++
				if want[oh] != newLines[j] {
					t.Errorf("anchor %s landed on wrong line: %q", oh, newLines[j])
				}
			}
		}
		if !hit {
			t.Errorf("untouched line %d (%q) lost anchor %s", i+1, old[i], oh)
		}
	}
	if found != 5 {
		t.Errorf("retained %d/5 untouched anchors, want 5", found)
	}

	// plan §2.4 实测：const x = 42; 应得 EPx
	if newH[3] != "EPx" {
		t.Errorf("new line 4 anchor = %s, want EPx (plan §2.4)", newH[3])
	}
	// 新文件内不得有重复
	seen := map[string]bool{}
	for i, h := range newH {
		if seen[h] {
			t.Errorf("duplicate anchor %s at line %d after edit", h, i+1)
		}
		seen[h] = true
	}
}

// 被释放的槽位禁止再分配：模型可能还持有那个锚点。
func TestRemovedSlotsNotReallocated(t *testing.T) {
	lines := []string{"aaa", "bbb", "ccc"}
	h := Pure(lines, DefaultProbeStride, nil)

	// 只留 aaa / ccc，删掉 bbb，同时新增 ddd（其 canon 与 bbb 相同）
	newLines := []string{"aaa", "bbb", "ccc"}
	newH := MapStable(lines, h, newLines, map[string]bool{}, nil, DefaultProbeStride)
	if newH[1] != h[1] {
		t.Errorf("unchanged middle line anchor rotated")
	}

	// 显式把 h[1] 标为 removed，再插入同 canon 行 → 必须换锚点
	h2 := MapStable(lines, h, []string{"aaa", "bbb", "ddd", "ccc"},
		map[string]bool{h[1]: true}, nil, DefaultProbeStride)
	if h2[1] == h[1] {
		t.Errorf("removed anchor %s got reallocated", h[1])
	}
	if h2[3] != h[2] {
		t.Errorf("trailing anchor drifted: %s vs %s", h2[3], h[2])
	}
}

func TestHashIndexRoundTrip(t *testing.T) {
	for _, h := range []string{"CTt", "Um7", "AU6", "nVR", "000", "zzz"} {
		idx, ok := HashToIndex(h)
		if !ok {
			t.Fatalf("HashToIndex(%q) failed", h)
		}
		if got := IndexToHash(idx); got != h {
			t.Errorf("round trip %q -> %d -> %q", h, idx, got)
		}
	}
	if _, ok := HashToIndex("CT"); ok {
		t.Error("2-char hash should be rejected")
	}
	if _, ok := HashToIndex("CT!"); ok {
		t.Error("non-base62 char should be rejected")
	}
}

// nearestNew 返回的是 cands 里的下标，不是值。
func TestNearestNew(t *testing.T) {
	cands := []int{2, 5, 9, 20}
	cases := []struct{ target, wantIdx, wantVal int }{
		{0, 0, 2},
		{3, 0, 2}, // 等距取左
		{4, 1, 5},
		{7, 1, 5}, // 等距取左
		{10, 2, 9},
		{19, 3, 20},
		{100, 3, 20}, // 越界取最右
	}
	for _, c := range cases {
		got := nearestNew(cands, c.target)
		if got != c.wantIdx {
			t.Errorf("nearestNew(%v, %d) idx = %d, want %d", cands, c.target, got, c.wantIdx)
			continue
		}
		if cands[got] != c.wantVal {
			t.Errorf("nearestNew(%v, %d) value = %d, want %d", cands, c.target, cands[got], c.wantVal)
		}
	}
	if got := nearestNew(nil, 5); got != -1 {
		t.Errorf("nearestNew(nil) = %d, want -1", got)
	}
}

// 稳定性回归：同输入必须同输出（跨进程、跨机器）。
func TestDeterminism(t *testing.T) {
	lines := strings.Split("package main\n\nfunc main() {\n\tprintln(1)\n}\n", "\n")
	a := Pure(lines, DefaultProbeStride, nil)
	b := Pure(lines, DefaultProbeStride, nil)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("non-deterministic at line %d: %s vs %s", i+1, a[i], b[i])
		}
	}
}
