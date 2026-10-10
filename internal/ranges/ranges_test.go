package ranges

import (
	"errors"
	"strings"
	"testing"
)

func TestParseForms(t *testing.T) {
	const n = 10
	cases := []struct {
		in        string
		wantStart int
		wantEnd   int
	}{
		{"1", 1, 1},
		{"5", 5, 5},
		{"10", 10, 10},
		{"3:7", 3, 7},
		{"1:10", 1, 10},
		{"7:7", 7, 7}, // 单元素闭区间
		{"8-", 8, 10},
		{"1-", 1, 10},
		{"10-", 10, 10},
		{"-3", 1, 3},
		{"-1", 1, 1},
		{"-10", 1, 10},
		// 容错：超出末尾截断
		{"-999", 1, 10},
		// 空格容忍
		{" 3 ", 3, 3},
		{" 3 : 7 ", 3, 7},
	}
	for _, c := range cases {
		r, err := Parse(c.in, n)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", c.in, err)
			continue
		}
		if r.Start != c.wantStart || r.End != c.wantEnd {
			t.Errorf("Parse(%q) = %d:%d, want %d:%d", c.in, r.Start, r.End, c.wantStart, c.wantEnd)
		}
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"", "  ", "abc", "0", "-0", "1:abc", "abc:1", "7:3",
		"1:0", "0:5", "-", "1:2:3", "1.5", "1e3", "--3", "3-5-",
	}
	for _, s := range bad {
		if r, err := Parse(s, 10); err == nil {
			t.Errorf("Parse(%q) = %d:%d, want error", s, r.Start, r.End)
		} else if !strings.HasPrefix(err.Error(), "E_RANGE") {
			t.Errorf("Parse(%q) err = %q, want E_RANGE prefix", s, err)
		}
	}
}

// 行数未知时 "N-" 退化成单行，不报错。
func TestOpenEndedWithoutLineCount(t *testing.T) {
	r, err := Parse("5-", 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Start != 5 || r.End != 5 {
		t.Errorf("Parse(%q, 0) = %d:%d, want 5:5", "5-", r.Start, r.End)
	}
}

func TestRangeLenContainsString(t *testing.T) {
	r := Range{3, 7}
	if r.Len() != 5 {
		t.Errorf("Len = %d, want 5", r.Len())
	}
	if r.String() != "3:7" {
		t.Errorf("String = %q", r.String())
	}
	for _, c := range []struct {
		line int
		want bool
	}{{2, false}, {3, true}, {5, true}, {7, true}, {8, false}} {
		if got := r.Contains(c.line); got != c.want {
			t.Errorf("Contains(%d) = %v, want %v", c.line, got, c.want)
		}
	}
}

// --- 合并 ---

func TestNormalizeMergesOverlapping(t *testing.T) {
	got, err := Normalize([]Range{{1, 5}, {3, 8}}, 20)
	if err != nil {
		t.Fatal(err)
	}
	want := []Range{{1, 8}}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNormalizeMergesAdjacent(t *testing.T) {
	// 1:3 + 4:6 → 相邻，应合并成 1:6（分两块输出没意义）
	got, _ := Normalize([]Range{{1, 3}, {4, 6}}, 20)
	if len(got) != 1 || got[0] != (Range{1, 6}) {
		t.Errorf("got %v, want [1:6]", got)
	}
}

func TestNormalizeKeepsGap(t *testing.T) {
	// 1:3 + 5:7 中间有空行 → 不能合并
	got, _ := Normalize([]Range{{1, 3}, {5, 7}}, 20)
	if len(got) != 2 || got[0] != (Range{1, 3}) || got[1] != (Range{5, 7}) {
		t.Errorf("got %v, want [1:3 5:7]", got)
	}
}

func TestNormalizeSortsStably(t *testing.T) {
	// 5:6 与 7:9 相邻 → 合并成 5:9（分两块输出没意义）
	got, _ := Normalize([]Range{{7, 9}, {1, 3}, {5, 6}}, 20)
	want := []Range{{1, 3}, {5, 9}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestNormalizeFullyOverlapping(t *testing.T) {
	got, _ := Normalize([]Range{{1, 10}, {2, 5}, {3, 8}}, 20)
	if len(got) != 1 || got[0] != (Range{1, 10}) {
		t.Errorf("got %v, want [1:10]", got)
	}
}

func TestNormalizeDuplicates(t *testing.T) {
	got, _ := Normalize([]Range{{2, 4}, {2, 4}}, 20)
	if len(got) != 1 || got[0] != (Range{2, 4}) {
		t.Errorf("got %v, want [2:4]", got)
	}
}

func TestNormalizeClampsToLineCount(t *testing.T) {
	got, err := Normalize([]Range{{1, 999}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Range{1, 10}) {
		t.Errorf("got %v, want [1:10]", got)
	}
}

func TestNormalizeStartOutOfRangeErrors(t *testing.T) {
	_, err := Normalize([]Range{{20, 30}}, 10)
	if err == nil {
		t.Fatal("expected out-of-range error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("err = %q", err)
	}
	var e interface{ Error() string }
	if !errors.As(err, &e) {
		t.Error("err should be an error")
	}
}

func TestNormalizeEmptyInput(t *testing.T) {
	got, err := Normalize(nil, 10)
	if err != nil || got != nil {
		t.Errorf("Normalize(nil) = %v, %v", got, err)
	}
}

// --- ParseAll ---

func TestParseAllNoSpecsMeansWholeFile(t *testing.T) {
	got, err := ParseAll(nil, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Range{1, 25}) {
		t.Errorf("got %v, want [1:25]", got)
	}
}

func TestParseAllNoSpecsEmptyFile(t *testing.T) {
	got, err := ParseAll(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestParseAllMultiple(t *testing.T) {
	got, err := ParseAll([]string{"1:3", "5:7", "9"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %v, want 3 ranges", got)
	}
	if got[0] != (Range{1, 3}) || got[1] != (Range{5, 7}) || got[2] != (Range{9, 9}) {
		t.Errorf("got %v", got)
	}
	if Total(got) != 7 {
		t.Errorf("Total = %d, want 7", Total(got))
	}
}

func TestParseAllPropagatesError(t *testing.T) {
	if _, err := ParseAll([]string{"1:3", "bad"}, 10); err == nil {
		t.Error("expected error")
	}
}

// --- Set ---

func TestSet(t *testing.T) {
	s := NewSet(100)
	if s.Count() != 0 {
		t.Errorf("new set count = %d", s.Count())
	}
	s.Add(1)
	s.Add(64)
	s.Add(65)
	s.Add(100)
	if !s.Has(1) || !s.Has(64) || !s.Has(65) || !s.Has(100) {
		t.Error("added lines should be present")
	}
	if s.Has(2) || s.Has(63) || s.Has(66) || s.Has(99) || s.Has(101) {
		t.Error("non-added lines should be absent")
	}
	if s.Count() != 4 {
		t.Errorf("Count = %d, want 4", s.Count())
	}
}

func TestSetBoundaryLines(t *testing.T) {
	s := NewSet(200)
	// 位边界：1 / 64 / 65 / 128 / 129
	for _, n := range []int{1, 64, 65, 128, 129, 200} {
		s.Add(n)
	}
	if s.Count() != 6 {
		t.Errorf("Count = %d, want 6", s.Count())
	}
	for _, n := range []int{1, 64, 65, 128, 129, 200} {
		if !s.Has(n) {
			t.Errorf("Has(%d) = false", n)
		}
	}
	// 行号越界不应panic
	if s.Has(1000) || s.Has(0) || s.Has(-1) {
		t.Error("out-of-range query should be false")
	}
	s.Add(0)
	s.Add(-5)
	if s.Count() != 6 {
		t.Errorf("Count changed after invalid adds: %d", s.Count())
	}
}

func TestSetAddRange(t *testing.T) {
	s := NewSet(10)
	s.AddRange(3, 6)
	for i := 1; i <= 10; i++ {
		want := i >= 3 && i <= 6
		if got := s.Has(i); got != want {
			t.Errorf("Has(%d) = %v, want %v", i, got, want)
		}
	}
	if s.Count() != 4 {
		t.Errorf("Count = %d, want 4", s.Count())
	}
}

func TestSetAddRangeOutOfBounds(t *testing.T) {
	s := NewSet(10)
	s.AddRange(8, 50) // 超出容量
	if !s.Has(8) || !s.Has(10) {
		t.Error("in-bounds lines should be added")
	}
	if s.Has(11) {
		t.Error("line beyond capacity should not be added")
	}
}

func TestSetSlice(t *testing.T) {
	s := NewSet(6)
	s.AddRange(2, 4)
	got := s.Slice(6)
	// 下标 0 未用
	want := []bool{false, false, true, true, true, false, false}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSetEmptyFile(t *testing.T) {
	s := NewSet(0)
	s.Add(1) // 不应panic
	if s.Has(1) || s.Count() != 0 {
		t.Error("empty set should stay empty")
	}
}

// --- 组合场景：与 PLAN 场景 G 对齐 ---

func TestScenarioG(t *testing.T) {
	// read -f src/app.ts 1:3 5:7  →  两块都 serve
	specs := []string{"1:3", "5:7"}
	raw, err := ParseAll(specs, 7)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := Normalize(raw, 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) != 2 {
		t.Fatalf("got %v, want 2 blocks", merged)
	}
	set := NewSet(7)
	for _, r := range merged {
		set.AddRange(r.Start, r.End)
	}
	if set.Count() != 6 {
		t.Errorf("served = %d lines, want 6", set.Count())
	}
	if set.Has(4) {
		t.Error("line 4 must NOT be served")
	}
	// 任意范围内的锚点都能用于后续 edit → 1,2,3,5,6,7 都 serve 了
	for _, l := range []int{1, 2, 3, 5, 6, 7} {
		if !set.Has(l) {
			t.Errorf("line %d should be served", l)
		}
	}
}
