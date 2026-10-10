package edit

import (
	"strconv"
	"strings"
	"testing"

	"rweg/internal/hashline"
)

// canonical 是 plan §2.4 的 6 行样例，锚点已与参考实现逐字节对齐。
func canonical() ([]string, []string) {
	lines := []string{
		"function hello() {",
		`  console.log("world");`,
		"}",
		"const x = 1;",
		"let y = 2;",
		"export default hello;",
	}
	hashes := hashline.Pure(lines, hashline.DefaultProbeStride, nil)
	return lines, hashes
}

func TestCanonicalAnchors(t *testing.T) {
	_, h := canonical()
	want := []string{"CTt", "Um7", "AU6", "nVR", "yAZ", "6PY"}
	if strings.Join(h, ",") != strings.Join(want, ",") {
		t.Fatalf("canonical anchors drifted: got %v want %v", h, want)
	}
}

func fullServed(lines, hashes []string) ServedView {
	return ServedView{Exists: true, Hashes: append([]string{}, hashes...)}
}

// --- 校验链 ---

func TestNeverRead(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	_, f := Check(ServedView{}, cur, []Op{{From: "CTt", To: "CTt", Replacement: []string{"x"}}}, 150)
	if f == nil || f.Code != CodeNeverRead {
		t.Fatalf("want %s, got %+v", CodeNeverRead, f)
	}
}

func TestUnknownAnchor(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	for _, spec := range [][2]string{{"ZZZ", "ZZZ"}, {"CTt", "QQQ"}} {
		_, f := Check(fullServed(lines, h), cur,
			[]Op{{From: spec[0], To: spec[1], Replacement: []string{"x"}}}, 150)
		if f == nil || f.Code != CodeUnknownAnchor {
			t.Fatalf("%v: want %s, got %+v", spec, CodeUnknownAnchor, f)
		}
	}
}

func TestStaleAnchor(t *testing.T) {
	lines, h := canonical()
	// 外部工具改了第 1 行的**内容**（不是空白：canon 剔空白，
	// 加个空格锚点是不会变的——那是本方案有意接受的边界）。
	cur := makeCurrent([]string{
		"function hello2() {",
		lines[1], lines[2], lines[3], lines[4], lines[5],
	})
	_, f := Check(fullServed(lines, h), cur,
		[]Op{{From: "CTt", To: "CTt", Replacement: []string{"x"}}}, 150)
	if f == nil || f.Code != CodeStaleAnchor {
		t.Fatalf("want %s, got %+v", CodeStaleAnchor, f)
	}
	if f.RegionValid {
		// 回传窗口以 served 里 CTt 的位置为中心（第 1 行）→ 1..2
		if f.Region.Start != 1 || f.Region.End != 2 {
			t.Fatalf("unexpected echo region %v", f.Region)
		}
	}
	if len(f.Lines) == 0 {
		t.Fatal("stale anchor should echo the surrounding region")
	}
}

// TestWhitespaceDriftKeepsAnchor 锁死"canon 剔空白"这条设计（plan §2.2）：
// formatter 只做空白重排时锚点不该轮换。
func TestWhitespaceDriftKeepsAnchor(t *testing.T) {
	lines, h := canonical()
	cur := makeCurrent([]string{
		"function  hello()  {", // 只多了几个空格
		lines[1], lines[2], lines[3], lines[4], lines[5],
	})
	v, f := Check(fullServed(lines, h), cur,
		[]Op{{From: "CTt", To: "CTt", Replacement: []string{"x"}}}, 150)
	if f != nil {
		t.Fatalf("whitespace-only drift should not break anchors: %+v", f)
	}
	if v.Ranges()[0].Start != 1 {
		t.Fatalf("CTt should still resolve at line 1")
	}
}

func TestAmbiguousAnchor(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	// 人为造一个重复锚点：同一行内容出现两次且哈希相同。
	dup := append([]string{}, h...)
	dup[4] = h[0] // 第 5 行也说自己是 CTt
	cur = NewCurrent(lines, dup)
	_, f := Check(fullServed(lines, h), cur,
		[]Op{{From: "CTt", To: "CTt", Replacement: []string{"x"}}}, 150)
	if f == nil || f.Code != CodeAmbiguous {
		t.Fatalf("want %s, got %+v", CodeAmbiguous, f)
	}
}

// TestStaleRange：区间**内部**的内容变了才算 stale。
//
// 注意不要把"区间外的行变了"当成拒绝理由：模型改的是它读到的那一段，
// 只要那一段还是原样，写入就是安全的。位置漂移由 served 的
// 逐行比对兜住（服务过的那一行换了位置 → 比对失败）。
func TestStaleRange(t *testing.T) {
	oldLines, oldH := canonical()
	newLines := append([]string{}, oldLines...)
	newLines[4] = "let y = 200;" // 第 5 行被外部改掉
	cur := makeCurrent(newLines)

	// 用 AU6..6PY（第 3..6 行）当目标区间 —— 第 5 行就在区间里。
	_, f := Check(fullServed(oldLines, oldH), cur,
		[]Op{{From: "AU6", To: "6PY", Replacement: []string{"x"}}}, 150)
	if f == nil || f.Code != CodeStaleRange {
		t.Fatalf("want %s, got %+v", CodeStaleRange, f)
	}
	if !f.RegionValid || f.Region.Start != 3 || f.Region.End != 6 {
		t.Fatalf("unexpected region %v (valid=%v)", f.Region, f.RegionValid)
	}
	if len(f.Lines) != 4 || f.Lines[2].Text != "let y = 200;" {
		t.Fatalf("unexpected echo lines %+v", f.Lines)
	}
}

// TestRangeOutsideDriftAllowed 是上一条的反面：目标区间完好时应当放行，
// 即便文件别处被改过。否则"改一行"会被无关的改动反复拒绝。
func TestRangeOutsideDriftAllowed(t *testing.T) {
	oldLines, oldH := canonical()
	newLines := append([]string{}, oldLines...)
	newLines[4] = "let y = 200;"
	cur := makeCurrent(newLines)

	v, f := Check(fullServed(oldLines, oldH), cur,
		[]Op{{From: "CTt", To: "AU6", Replacement: []string{"function hi() {"}}}, 150)
	if f != nil {
		t.Fatalf("drift outside the target range must not block: %+v", f)
	}
	if v.Ranges()[0].Start != 1 || v.Ranges()[0].End != 3 {
		t.Fatalf("range = %v", v.Ranges()[0])
	}
}

// TestStaleRangeUnservedMiddle 是关键用例：
// 区间里有一行从未 serve 过（served 记空串），即便锚点对得上也必须拒绝。
// 否则模型可以拿"没读过的一段"当"没变的一段"来写。
func TestStaleRangeUnservedMiddle(t *testing.T) {
	lines, h := canonical()
	sv := ServedView{Exists: true, Hashes: []string{h[0], h[1], h[2], "", "", h[5]}} // 4、5 行未 serve
	cur := NewCurrent(lines, h)
	_, f := Check(sv, cur, []Op{{From: "AU6", To: "6PY", Replacement: []string{"x"}}}, 150)
	if f == nil || f.Code != CodeStaleRange {
		t.Fatalf("unserved line inside range must be rejected; got %+v", f)
	}
}

// TestServedPartialCoverage 顺带锁死校验链的**顺序**：
// 先问"这个锚点在不在 served 里"（第 2 步），再问"区间里逐行一致吗"（第 4 步）。
// 顺序反了的话，一个从没读过的锚点会被报成"内容变了"，误导性很强。
func TestServedPartialCoverage(t *testing.T) {
	lines, h := canonical()
	sv := ServedView{Exists: true, Hashes: h[:2]} // 只 serve 过前两行
	cur := NewCurrent(lines, h)

	if _, f := Check(sv, cur, []Op{{From: "CTt", To: "AU6", Replacement: []string{"x"}}}, 150); f == nil || f.Code != CodeUnknownAnchor {
		t.Fatalf("want %s, got %+v", CodeUnknownAnchor, f)
	}
	if _, f := Check(sv, cur, []Op{{From: "CTt", To: "Um7", Replacement: []string{"x"}}}, 150); f != nil {
		t.Fatalf("range fully inside served should pass: %+v", f)
	}
}

func TestOverlapRejected(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	_, f := Check(fullServed(lines, h), cur, []Op{
		{From: "CTt", To: "AU6", Replacement: []string{"x"}},
		{From: "AU6", To: "yAZ", Replacement: []string{"y"}},
	}, 150)
	if f == nil || f.Code != CodeOverlap {
		t.Fatalf("want %s, got %+v", CodeOverlap, f)
	}
}

func TestReversedRangeRejected(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	_, f := Check(fullServed(lines, h), cur,
		[]Op{{From: "yAZ", To: "nVR", Replacement: []string{"x"}}}, 150)
	if f == nil || f.Code != CodeBadRange {
		t.Fatalf("want %s, got %+v", CodeBadRange, f)
	}
}

func TestUnsortedOpsAreSorted(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	v, f := Check(fullServed(lines, h), cur, []Op{
		{From: "6PY", To: "6PY", Replacement: []string{"b"}},
		{From: "CTt", To: "CTt", Replacement: []string{"a"}},
	}, 150)
	if f != nil {
		t.Fatalf("unexpected failure %+v", f)
	}
	rs := v.Ranges()
	if rs[0].Start != 1 || rs[1].Start != 6 {
		t.Fatalf("ops not sorted: %v", rs)
	}
}

// --- 应用 ---

// TestApplyReplacesWithStableAnchors 锁死 plan §2.4 的实测值：
// 第 4 行替换成 3 行后，未动的 5 行锚点 100% 保留。
func TestApplyReplacesWithStableAnchors(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	v, f := Check(fullServed(lines, h), cur, []Op{
		{From: "nVR", To: "nVR", Replacement: []string{"const x = 42;", "const z = 3;", "let w = 4;"}},
	}, 150)
	if f != nil {
		t.Fatalf("unexpected failure %+v", f)
	}
	res := Apply(cur, v, fullServed(lines, h), nil, hashline.DefaultProbeStride, 1)

	wantLines := []string{
		"function hello() {", `  console.log("world");`, "}",
		"const x = 42;", "const z = 3;", "let w = 4;",
		"let y = 2;", "export default hello;",
	}
	if strings.Join(res.Lines, "|") != strings.Join(wantLines, "|") {
		t.Fatalf("lines mismatch:\n got %v\nwant %v", res.Lines, wantLines)
	}
	wantHashes := []string{"CTt", "Um7", "AU6", "EPx", "jDo", "BlU", "yAZ", "6PY"}
	if strings.Join(res.Hashes, ",") != strings.Join(wantHashes, ",") {
		t.Fatalf("anchors drifted:\n got %v\nwant %v", res.Hashes, wantHashes)
	}
	if res.Added != 3 || res.Removed != 1 {
		t.Fatalf("counts: added=%d removed=%d", res.Added, res.Removed)
	}
	// retire 只装"释放掉且没被继承"的锚点：nVR。
	if len(res.Retire) != 1 || res.Retire[0] != "nVR" {
		t.Fatalf("retire = %v, want [nVR]", res.Retire)
	}
}

// TestApplyKeepsUnservedHoles 是 fail-closed 的核心断言：
// 只读过 1:3 与 5:6 的文件，编辑第 4 行之后，第 7/8 行（原第 5/6 行）
// 仍是 served，但**原本没读过的行不能因为位移而变成 served**。
func TestApplyKeepsUnservedHoles(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	// 第 4 行没读过 → served 里是空串
	sv := ServedView{Exists: true, Hashes: []string{h[0], h[1], h[2], "", h[4], h[5]}}

	v, f := Check(sv, cur, []Op{
		{From: "CTt", To: "AU6", Replacement: []string{"function hi() {"}},
	}, 150)
	if f != nil {
		t.Fatalf("unexpected failure %+v", f)
	}
	res := Apply(cur, v, sv, nil, hashline.DefaultProbeStride, 1)

	if len(res.Served) != len(res.Lines) {
		t.Fatalf("served length %d != lines %d", len(res.Served), len(res.Lines))
	}
	// 原第 4 行（未 serve）现在落在第 2 个位置，必须还是空串。
	if res.Served[1] != "" {
		t.Fatalf("unserved hole leaked into served: %q", res.Served[1])
	}
	// 编辑产生的行必须 served
	if res.Served[0] != res.Hashes[0] {
		t.Fatalf("edited line should be served: %q vs %q", res.Served[0], res.Hashes[0])
	}
	// 位移之后的旧行仍是 served，且锚点未变
	if res.Served[2] != "yAZ" || res.Served[3] != "6PY" {
		t.Fatalf("shifted lines lost served: %v", res.Served)
	}
}

func TestApplyDelete(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	sv := fullServed(lines, h)
	v, f := Check(sv, cur, []Op{{From: "AU6", To: "nVR", Replacement: nil}}, 150)
	if f != nil {
		t.Fatalf("unexpected failure %+v", f)
	}
	res := Apply(cur, v, sv, nil, hashline.DefaultProbeStride, 1)
	if len(res.Lines) != 4 {
		t.Fatalf("want 4 lines, got %d: %v", len(res.Lines), res.Lines)
	}
	if res.Removed != 2 || res.Added != 0 {
		t.Fatalf("counts added=%d removed=%d", res.Added, res.Removed)
	}
	// 被删两行的锚点必须 retired
	if strings.Join(res.Retire, ",") != "AU6,nVR" {
		t.Fatalf("retire = %v", res.Retire)
	}
	// 保留下来的行 served 仍在（且因前面删了两行而整体前移两位）
	if res.Served[0] != "CTt" || res.Served[1] != "Um7" ||
		res.Served[2] != "yAZ" || res.Served[3] != "6PY" {
		t.Fatalf("served = %v", res.Served)
	}
}

// TestApplyInheritsIdenticalReplacement 覆盖一个容易搞错的边界：
// 替换文本与原行 canon 相同时，MapStable 会继承同一个锚点，
// 此时该锚点**不算被释放**，不该进 retired（否则槽位白白浪费）。
func TestApplyInheritsIdenticalReplacement(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	sv := fullServed(lines, h)
	v, f := Check(sv, cur, []Op{{From: "nVR", To: "nVR", Replacement: []string{"const  x  =  1;"}}}, 150)
	if f != nil {
		t.Fatalf("unexpected failure %+v", f)
	}
	res := Apply(cur, v, sv, nil, hashline.DefaultProbeStride, 1)
	if res.Hashes[3] != "nVR" {
		t.Fatalf("canon-identical replacement should inherit nVR, got %s", res.Hashes[3])
	}
	if len(res.Retire) != 0 {
		t.Fatalf("inherited anchor must not be retired: %v", res.Retire)
	}
}

func TestApplyRespectsReserved(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	sv := fullServed(lines, h)
	v, _ := Check(sv, cur, []Op{{From: "nVR", To: "nVR", Replacement: []string{"brand new line"}}}, 150)
	res := Apply(cur, v, sv, nil, hashline.DefaultProbeStride, 1)
	free := res.Hashes[3]

	// 第二次：把上次拿到的锚点标记为 retired，同样内容必须拿到不同锚点。
	cur2 := NewCurrent(res.Lines, res.Hashes)
	sv2 := ServedView{Exists: true, Hashes: append([]string{}, res.Hashes...)}
	v2, _ := Check(sv2, cur2, []Op{{From: free, To: free, Replacement: []string{"brand new line 2"}}}, 150)
	res2 := Apply(cur2, v2, sv2, map[string]bool{free: true}, hashline.DefaultProbeStride, 1)
	if res2.Hashes[3] == free {
		t.Fatalf("reserved anchor was reallocated: %s", free)
	}
}

func TestApplyMultipleOpsPositions(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	sv := fullServed(lines, h)
	v, f := Check(sv, cur, []Op{
		{From: "CTt", To: "CTt", Replacement: []string{"function hi() {"}},
		{From: "yAZ", To: "yAZ", Replacement: []string{"let y = 0;"}},
	}, 150)
	if f != nil {
		t.Fatalf("unexpected failure %+v", f)
	}
	res := Apply(cur, v, sv, nil, hashline.DefaultProbeStride, 1)
	if len(res.Lines) != 6 {
		t.Fatalf("line count %d", len(res.Lines))
	}
	if res.Lines[0] != "function hi() {" || res.Lines[4] != "let y = 0;" {
		t.Fatalf("lines = %v", res.Lines)
	}
	if res.Added != 2 || res.Removed != 2 {
		t.Fatalf("added=%d removed=%d", res.Added, res.Removed)
	}
	// 中间未动的行锚点必须保持
	if res.Hashes[1] != "Um7" || res.Hashes[2] != "AU6" || res.Hashes[3] != "nVR" {
		t.Fatalf("middle anchors drifted: %v", res.Hashes)
	}
}

func TestApplyMappingAndContext(t *testing.T) {
	lines, h := canonical()
	cur := NewCurrent(lines, h)
	sv := fullServed(lines, h)
	v, _ := Check(sv, cur, []Op{{From: "nVR", To: "nVR", Replacement: []string{"a", "b"}}}, 150)
	res := Apply(cur, v, sv, nil, hashline.DefaultProbeStride, 1)

	// 旧 3 号（}）→ 新 2 号；旧 5 号（let y）→ 新 5 号（前面多了一行 b）
	if res.Mapping[2] != 2 || res.Mapping[4] != 5 {
		t.Fatalf("mapping = %v", res.Mapping)
	}
	if res.Mapping[3] != -1 {
		t.Fatalf("replaced line should map to -1: %v", res.Mapping)
	}
	// diff 是 git 式的：删 1 行（旧锚点）+ 插 2 行，前后各 1 行上下文。
	want := []struct {
		kind DiffKind
		text string
	}{
		{DiffContext, "}"},
		{DiffDel, "const x = 1;"},
		{DiffIns, "a"},
		{DiffIns, "b"},
		{DiffContext, "let y = 2;"},
	}
	if len(res.Diff) != len(want) {
		t.Fatalf("diff = %+v", res.Diff)
	}
	for i, w := range want {
		if res.Diff[i].Kind != w.kind || res.Diff[i].Line.Text != w.text {
			t.Fatalf("diff[%d] = %+v, want %v %q", i, res.Diff[i], w.kind, w.text)
		}
	}
	if res.Added != 2 || res.Removed != 1 {
		t.Fatalf("added=%d removed=%d", res.Added, res.Removed)
	}
	// 上下文用新文件锚点，删除行用旧锚点
	if res.Diff[0].Line.Hash != res.Hashes[2] || res.Diff[4].Line.Hash != res.Hashes[5] {
		t.Fatalf("context anchors out of sync: %+v", res.Diff)
	}
	if res.Diff[1].Line.Hash != "nVR" {
		t.Fatalf("removed line anchor = %q, want nVR", res.Diff[1].Line.Hash)
	}
}

func TestEchoCap(t *testing.T) {
	// 构造 10 行文件，全部 serve，然后制造 drift。
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = "line number " + strconv.Itoa(i+1)
	}
	h := hashline.Pure(lines, hashline.DefaultProbeStride, nil)
	sv := ServedView{Exists: true, Hashes: append([]string{}, h...)}

	drift := append([]string{}, lines...)
	drift[4] = "changed"
	cur := NewCurrent(drift, hashline.Pure(drift, hashline.DefaultProbeStride, nil))

	_, f := Check(sv, cur, []Op{{From: h[0], To: h[9], Replacement: []string{"x"}}}, 3)
	if f == nil || f.Code != CodeStaleRange {
		t.Fatalf("got %+v", f)
	}
	if len(f.Lines) != 3 {
		t.Fatalf("echo cap not applied: %d lines", len(f.Lines))
	}
}

// makeCurrent 从行内容算出当前视图（模拟 read 的锚点计算路径）。
func makeCurrent(lines []string) *Current {
	return NewCurrent(lines, hashline.Pure(lines, hashline.DefaultProbeStride, nil))
}
