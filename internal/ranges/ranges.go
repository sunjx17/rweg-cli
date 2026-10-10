// Package ranges 解析并合并行范围。
//
// 语法（plan §6.2）：
//
//	N       单行
//	N:M     含首尾的闭区间
//	N-      N 到文件末尾
//	-M      文件开头到 M
//
// 设计要点：
//   - 1-based，闭区间。
//   - `-` 与负数不冲突：`-M` 表示 1..M，不存在负行号。
//   - 合并重叠/相邻区间（相邻也合并：输出两块连续的区间没有意义）。
//   - 合并后按起点排序，保证输出稳定可预测。
package ranges

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Range 是 1-based 闭区间。
type Range struct {
	Start int
	End   int // 含
}

// Len 返回行数。
func (r Range) Len() int { return r.End - r.Start + 1 }

// String 渲染成 "N:M"。
func (r Range) String() string { return fmt.Sprintf("%d:%d", r.Start, r.End) }

// Contains 报告 line（1-based）是否落在区间内。
func (r Range) Contains(line int) bool { return line >= r.Start && line <= r.End }

// Parse 解析单个范围串。lineCount 用于把 "N-" / "-M" 落到具体数字。
func Parse(s string, lineCount int) (Range, error) {
	raw := s
	s = strings.TrimSpace(s)
	if s == "" {
		return Range{}, fmt.Errorf("E_RANGE  empty range %q", raw)
	}

	// 负号在前：-M → 1..M
	if strings.HasPrefix(s, "-") {
		end, err := atoi(s[1:])
		if err != nil {
			return Range{}, fmt.Errorf("E_RANGE  %q: %w", raw, err)
		}
		if end < 1 {
			return Range{}, fmt.Errorf("E_RANGE  %q: line numbers are 1-based, got %d", raw, end)
		}
		if lineCount > 0 && end > lineCount {
			// 容错：截到文件末尾而不是报错——用户说 "-999" 意图很明确
			end = lineCount
		}
		return Range{Start: 1, End: end}, nil
	}

	// 负号在后：N- → N..EOF
	if strings.HasSuffix(s, "-") {
		start, err := atoi(s[:len(s)-1])
		if err != nil {
			return Range{}, fmt.Errorf("E_RANGE  %q: %w", raw, err)
		}
		if start < 1 {
			return Range{}, fmt.Errorf("E_RANGE  %q: line numbers are 1-based, got %d", raw, start)
		}
		end := lineCount
		if end == 0 {
			// 文件为空或行数未知：N- 退化成单行，交由上层做越界检查
			end = start
		}
		return Range{Start: start, End: end}, nil
	}

	// 闭区间 N:M
	if i := strings.Index(s, ":"); i >= 0 {
		start, err := atoi(s[:i])
		if err != nil {
			return Range{}, fmt.Errorf("E_RANGE  %q: %w", raw, err)
		}
		end, err := atoi(s[i+1:])
		if err != nil {
			return Range{}, fmt.Errorf("E_RANGE  %q: %w", raw, err)
		}
		if start < 1 || end < 1 {
			return Range{}, fmt.Errorf("E_RANGE  %q: line numbers are 1-based", raw)
		}
		if start > end {
			return Range{}, fmt.Errorf("E_RANGE  %q: start %d > end %d", raw, start, end)
		}
		return Range{Start: start, End: end}, nil
	}

	// 单行 N
	n, err := atoi(s)
	if err != nil {
		return Range{}, fmt.Errorf("E_RANGE  %q: %w", raw, err)
	}
	if n < 1 {
		return Range{}, fmt.Errorf("E_RANGE  %q: line numbers are 1-based, got %d", raw, n)
	}
	return Range{Start: n, End: n}, nil
}

// atoi 解析正整数，附带清晰的错误信息。
func atoi(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("missing number")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	return n, nil
}

// ParseAll 解析一组范围串。
func ParseAll(specs []string, lineCount int) ([]Range, error) {
	if len(specs) == 0 {
		// 不给范围 = 读整个文件
		if lineCount > 0 {
			return []Range{{Start: 1, End: lineCount}}, nil
		}
		return nil, nil
	}
	out := make([]Range, 0, len(specs))
	for _, s := range specs {
		r, err := Parse(s, lineCount)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Normalize 做越界检查 + 重叠合并，输出稳定有序的结果。
//
// lineCount <= 0 表示行数未知（空文件），此时只合并不去越界。
func Normalize(rs []Range, lineCount int) ([]Range, error) {
	if len(rs) == 0 {
		return nil, nil
	}
	out := make([]Range, 0, len(rs))
	for _, r := range rs {
		if lineCount > 0 {
			if r.Start > lineCount {
				return nil, fmt.Errorf("E_RANGE  line %d out of range (file has %d line(s))",
					r.Start, lineCount)
			}
			if r.End > lineCount {
				r.End = lineCount
			}
		}
		if r.End < r.Start {
			continue // 空区间，丢掉
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, nil
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})

	merged := make([]Range, 0, len(out))
	for _, r := range out {
		last := len(merged) - 1
		// 重叠或相邻（r.Start <= last.End+1）→合并
		if last >= 0 && r.Start <= merged[last].End+1 {
			if r.End > merged[last].End {
				merged[last].End = r.End
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged, nil
}

// Total 返回合并后的总行数（去重后）。
func Total(rs []Range) int {
	n := 0
	for _, r := range rs {
		n += r.Len()
	}
	return n
}

// Set 是被 serve 的行号集合。
//
// 用 bitset 而不是 map[int]bool：200k 行的文件约 25KB，
// 且判断与插入都是 O(1) 无哈希开销。
type Set struct {
	words []uint64
	// cap 是能容纳的最大行号（0 表示空集合，连第1 行都装不下）。
	cap int
}

// NewSet 创建一个能容纳 1..n 行的集合。n=0 时是空集合，
// 任何 Add 都会被忽略——空文件不该有"第 1 行"。
func NewSet(n int) *Set {
	if n < 0 {
		n = 0
	}
	return &Set{words: make([]uint64, (n+63)/64), cap: n}
}

// Add 把 1-based 行号加入集合。超出容量时静默忽略。
func (s *Set) Add(line int) {
	if line < 1 || line > s.cap {
		return
	}
	i := line - 1
	s.words[i/64] |= 1 << (uint(i) % 64)
}

// Has 报告 1-based 行号是否在集合里。
func (s *Set) Has(line int) bool {
	if line < 1 || line > s.cap {
		return false
	}
	i := line - 1
	return s.words[i/64]&(1<<(uint(i)%64)) != 0
}

// AddRange 把 [start,end]（1-based）整段加入。
func (s *Set) AddRange(start, end int) {
	for i := start; i <= end; i++ {
		s.Add(i)
	}
}

// Cap 返回能容纳的最大行号。
func (s *Set) Cap() int { return s.cap }

// Count 返回集合内行数。
func (s *Set) Count() int {
	n := 0
	for _, w := range s.words {
		n += popcount(w)
	}
	return n
}

func popcount(w uint64) int {
	n := 0
	for w != 0 {
		w &= w - 1
		n++
	}
	return n
}

// Slice 把集合导出成按行号 1-based 的切片（0 表示未 serve）。
// 长度固定为 maxLine，便于上层直接转 JSON 数组。
func (s *Set) Slice(maxLine int) []bool {
	out := make([]bool, maxLine+1)
	for line := 1; line <= maxLine; line++ {
		out[line] = s.Has(line)
	}
	return out
}
