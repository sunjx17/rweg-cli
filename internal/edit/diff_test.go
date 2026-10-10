package edit

import (
	"fmt"
	"strings"
	"testing"
)

// render 把 diff 压成便于断言的串：上下文 " ctx"、删 "-x"、增 "+x"、省略 "..."
func render(diff []DiffLine) string {
	parts := make([]string, 0, len(diff))
	for _, d := range diff {
		switch d.Kind {
		case DiffElide:
			parts = append(parts, "...")
		case DiffDel:
			parts = append(parts, "-"+d.Line.Text)
		case DiffIns:
			parts = append(parts, "+"+d.Line.Text)
		default:
			parts = append(parts, " "+d.Line.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// 单处改动：两侧各留一行上下文，计数只算真正变了的行。
func TestBuildDiffSingleHunk(t *testing.T) {
	oldLines := []string{"a", "b", "c", "d"}
	newLines := []string{"a", "B", "c", "d"}
	spans := []editSpan{{oldStart: 2, oldEnd: 2, newStart: 2, newEnd: 2,
		repl: []string{"B"}}}

	diff, added, removed := buildDiff(oldLines, nil, newLines, nil, spans, 1)
	if got, want := render(diff), " a\n-b\n+B\n c"; got != want {
		t.Errorf("diff =\n%s\nwant\n%s", got, want)
	}
	if added != 1 || removed != 1 {
		t.Errorf("added/removed = %d/%d, want 1/1", added, removed)
	}
}

// 纯插入：只增不减时计数里不能出现删除。
func TestBuildDiffInsertOnly(t *testing.T) {
	oldLines := []string{"a", "b"}
	newLines := []string{"a", "b", "c"}
	spans := []editSpan{{oldStart: 3, oldEnd: 2, newStart: 3, newEnd: 3,
		repl: []string{"c"}}}

	diff, added, removed := buildDiff(oldLines, nil, newLines, nil, spans, 1)
	// 上下文只留改动前后各一行：更早的 "a" 属于被省略的头部，不进结果
	if got, want := render(diff), " b\n+c"; got != want {
		t.Errorf("diff =\n%s\nwant\n%s", got, want)
	}
	if added != 1 || removed != 0 {
		t.Errorf("added/removed = %d/%d, want 1/0", added, removed)
	}
}

// 替换成"同样的内容 + 一行"：原行是上下文，只算一行插入
// （这是"写回两行、只多一个空行"那类改动的形态）。
func TestBuildDiffKeepThenInsert(t *testing.T) {
	oldLines := []string{"a", "b"}
	newLines := []string{"a", "b", ""}
	spans := []editSpan{{oldStart: 2, oldEnd: 2, newStart: 2, newEnd: 3,
		repl: []string{"b", ""}}}

	diff, added, removed := buildDiff(oldLines, nil, newLines, nil, spans, 1)
	if added != 1 || removed != 0 {
		t.Errorf("added/removed = %d/%d, want 1/0", added, removed)
	}
	// b 必须作为上下文出现（它其实没变），而不是 -b/+b
	if strings.Contains(render(diff), "-b") {
		t.Errorf("unchanged line should be context:\n%s", render(diff))
	}
}

// 相隔较远的块：中间只插一个省略标记，首尾不省略。
func TestBuildDiffElidesMiddleOnly(t *testing.T) {
	oldLines := make([]string, 20)
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("line %d", i+1)
	}
	newLines := append([]string{}, oldLines...)
	newLines[1] = "L2"
	newLines[17] = "L18"
	spans := []editSpan{
		{oldStart: 2, oldEnd: 2, newStart: 2, newEnd: 2, repl: []string{"L2"}},
		{oldStart: 18, oldEnd: 18, newStart: 18, newEnd: 18, repl: []string{"L18"}},
	}

	diff, added, removed := buildDiff(oldLines, nil, newLines, nil, spans, 1)
	got := render(diff)
	if strings.Count(got, "...") != 1 {
		t.Errorf("want exactly one elision:\n%s", got)
	}
	if strings.HasPrefix(got, "...") || strings.HasSuffix(got, "...") {
		t.Errorf("head/tail must not be elided:\n%s", got)
	}
	// 首尾被丢掉的行不插省略号（省略号只在中间），窗口内的行要在
	if !strings.Contains(got, " line 1") || !strings.Contains(got, " line 19") {
		t.Errorf("context around changes missing:\n%s", got)
	}
	if added != 2 || removed != 2 {
		t.Errorf("added/removed = %d/%d, want 2/2", added, removed)
	}
}
