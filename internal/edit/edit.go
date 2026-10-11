// Package edit 实现 edit 的校验链与原地应用（plan §7）。
//
// 设计原则：**fail-closed**。
// 宁可多一次 read，也绝不基于未经 read 的信息写入。
// 而「serve」只认一条判据：**模型见过这行**。read / grep / write 打出来的行、
// 成功 diff 打出来的每一行（上下文也算）、失败时回显的当前区间行，都会进 served
// —— 见过即可用；真正被 fail-closed 拦下的是「没见过就拿来改」。
//
// 本包刻意只依赖 hashline 与 ranges，不依赖 store：
// 校验链是整个方案里最需要被单测钉死的部分，不能靠数据库才能测。
package edit

import (
	"fmt"
	"sort"

	"rweg/internal/hashline"
	"rweg/internal/ranges"
)

// Op 是一条 -c 编辑指令：把 FROM..TO 这段替换成 Replacement 行。
//
// Replacement 为空切片 = 删除该区间。
type Op struct {
	From        string
	To          string
	Replacement []string
}

// ServedView 是编辑需要的 served 状态的最小视图。
type ServedView struct {
	// Exists 为 false 表示该路径从未被 serve 过 → E_NEVER_READ。
	Exists bool
	// Hashes 按行号（1-based）排列；空串 = 该行未 serve。
	Hashes []string
}

// Line 是回传 / 展示用的一行。
type Line struct {
	Line int    // 1-based
	Hash string // 该行当前锚点（可能为空 = 不可锚定）
	Text string
}

// Current 是当前磁盘文件的锚点视图。
type Current struct {
	Lines  []string
	Hashes []string
	index  map[string][]int // 锚点 → 出现位置（1-based）
}

// NewCurrent 构造当前视图。lines 与 hashes 必须等长（都来自同一次加载）。
func NewCurrent(lines, hashes []string) *Current {
	c := &Current{Lines: lines, Hashes: hashes, index: make(map[string][]int, len(hashes))}
	for i, h := range hashes {
		if h == "" {
			continue
		}
		c.index[h] = append(c.index[h], i+1)
	}
	return c
}

// LineCount 返回当前文件行数。
func (c *Current) LineCount() int { return len(c.Lines) }

// LineAt 返回第 line 行（1-based）。越界返回零值 Line。
func (c *Current) LineAt(line int) Line {
	l := Line{Line: line}
	if line >= 1 && line <= len(c.Lines) {
		l.Text = c.Lines[line-1]
	}
	if line >= 1 && line <= len(c.Hashes) {
		l.Hash = c.Hashes[line-1]
	}
	return l
}

// Occurs 返回锚点在当前文件中的全部出现位置（1-based）。
//
// ⚠️ 长度 >1 时必须拒绝（参考实现 ADR-0018 的 exact-boundary 规则）：
// 锚点重复时无法判断用户想改哪一处，"选一个"就是静默写错行。
func (c *Current) Occurs(h string) []int { return c.index[h] }

// --- 失败码 ---

// 校验链上可能出现的失败码。
const (
	CodeNeverRead     = "E_NEVER_READ"
	CodeUnknownAnchor = "E_UNKNOWN_ANCHOR"
	CodeStaleAnchor   = "E_STALE_ANCHOR"
	CodeAmbiguous     = "E_AMBIGUOUS_ANCHOR"
	CodeStaleRange    = "E_STALE_RANGE"
	CodeBadRange      = "E_RANGE"
	CodeOverlap       = "E_OVERLAP"
	CodeNoChange      = "E_NO_CHANGE"
)

// Failure 描述校验链上的一次拒绝。
type Failure struct {
	Code string
	Msg  string
	// Region 是回传区域（1-based 闭区间）；RegionValid=false 表示无法定位。
	Region      ranges.Range
	RegionValid bool
	// Lines 是回传的当前文件行 —— 调用方会把它们 serve（模型见过即可用）。
	Lines []Line
}

// Error 让 Failure 满足 error（便于测试里 assert）。
func (f *Failure) Error() string { return f.Code + "  " + f.Msg }

// resolvedOp 是解析出行号之后的编辑指令。
type resolvedOp struct {
	Op
	Range ranges.Range
}

// Validated 是通过校验链的一组编辑（已按起点排序、已确认互不重叠）。
type Validated struct {
	ops []resolvedOp
}

// Ops 返回按起点排序后的编辑指令。
func (v *Validated) Ops() []resolvedOp { return v.ops }

// Ranges 返回解析出的区间。
func (v *Validated) Ranges() []ranges.Range {
	out := make([]ranges.Range, len(v.ops))
	for i, op := range v.ops {
		out[i] = op.Range
	}
	return out
}

// Check 执行校验链第 1–4 步（plan §7.1）：
//
//  1. F 是否被 serve 过？       否 → E_NEVER_READ
//  2. h1/h2 是否在 served 里？  否 → E_UNKNOWN_ANCHOR
//  3. 能否在当前文件里唯一解析？ 否 → E_STALE_ANCHOR（+ 回传当前区域）
//  4. 区间内每行当前锚点是否等于 served 记录的？否 → E_STALE_RANGE（+ 回传）
//
// maxEcho 限制回传行数（[edit].served_echo_cap）。
func Check(sv ServedView, cur *Current, ops []Op, maxEcho int) (*Validated, *Failure) {
	// --- 1 ---
	if !sv.Exists {
		return nil, &Failure{Code: CodeNeverRead,
			Msg: "has never been read in this namespace"}
	}
	if len(ops) == 0 {
		return nil, &Failure{Code: CodeBadRange, Msg: "-c is required at least once"}
	}

	resolved := make([]resolvedOp, 0, len(ops))
	for _, op := range ops {
		// --- 2 ---
		if !servedHas(sv, op.From) {
			return nil, &Failure{Code: CodeUnknownAnchor,
				Msg: op.From + " not in served anchors"}
		}
		if !servedHas(sv, op.To) {
			return nil, &Failure{Code: CodeUnknownAnchor,
				Msg: op.To + " not in served anchors"}
		}

		// --- 3 ---
		fromPos := cur.Occurs(op.From)
		toPos := cur.Occurs(op.To)
		if len(fromPos) > 1 {
			return nil, ambiguousFailure(cur, op.From, maxEcho)
		}
		if len(toPos) > 1 {
			return nil, ambiguousFailure(cur, op.To, maxEcho)
		}
		start, okS := firstOrZero(fromPos)
		end, okE := firstOrZero(toPos)
		if !okS || !okE {
			// 哪个解不出来就报哪个；能解出来的那个用来定位回传窗口。
			bad, at := op.From, servedPos(sv, op.From)
			if okS {
				bad, at = op.To, servedPos(sv, op.To)
			}
			f := &Failure{Code: CodeStaleAnchor,
				Msg: bad + " no longer resolves in this file"}
			if at >= 1 {
				lo := at - 1
				if lo < 1 {
					lo = 1
				}
				hi := at + 1
				if hi > cur.LineCount() {
					hi = cur.LineCount()
				}
				if lo <= hi {
					f.Region = ranges.Range{Start: lo, End: hi}
					f.RegionValid = true
					f.Lines = collect(cur, lo, hi, maxEcho)
				}
			}
			return nil, f
		}
		if start > end {
			return nil, &Failure{Code: CodeBadRange,
				Msg: fmt.Sprintf("%s:%s is reversed (%d > %d)", op.From, op.To, start, end)}
		}

		// --- 4 ---
		if !rangeMatches(sv, cur, start, end) {
			f := &Failure{Code: CodeStaleRange,
				Msg: fmt.Sprintf("lines %d-%d changed since read", start, end)}
			f.Region = ranges.Range{Start: start, End: end}
			f.RegionValid = true
			f.Lines = collect(cur, start, end, maxEcho)
			return nil, f
		}

		resolved = append(resolved, resolvedOp{Op: op, Range: ranges.Range{Start: start, End: end}})
	}

	// --- 区间之间不允许重叠：一次调用里的两个 -c 改同一段无法定义顺序 ---
	sort.SliceStable(resolved, func(i, j int) bool {
		return resolved[i].Range.Start < resolved[j].Range.Start
	})
	for i := 1; i < len(resolved); i++ {
		if resolved[i].Range.Start <= resolved[i-1].Range.End {
			return nil, &Failure{Code: CodeOverlap,
				Msg: fmt.Sprintf("ranges %s and %s overlap",
					resolved[i-1].Range.String(), resolved[i].Range.String())}
		}
	}
	return &Validated{ops: resolved}, nil
}

// --- Result / Block ---

// Result 是一次成功应用的结果。
//
// Served 是**新的 served 数组**（长度 = len(Lines)）：
//   - 编辑区间里的新行标记为 served；
//   - diff 会打印出来的每一行（上下文 + 新增）也按新行号标记为 served；
//   - 其余未编辑的旧行沿用旧的 served 值（可能仍是空串 = 未 serve）。
//
// 这样既不让"没读过的行"凭空变 served（fail-closed），
// 又让"读完改一行、顺手再改相邻行"这种常见链路不必重读。
type Result struct {
	Lines   []string
	Hashes  []string
	Served  []string
	Canons  []string
	Retire  []string
	Mapping []int // 旧行（0-based）→ 新行（0-based）；-1 = 被删除
	Added   int   // 真正新增的行数（逐行比对得出，不是区间长度）
	Removed int   // 真正删除的行数
	Diff    []DiffLine
}

// Apply 应用一组已通过校验的编辑。
//
//   - reserved 是额外禁止分配的锚点（retired 表）
//   - stride 是探测步长
//   - context 是 diff 里前后各带的上下文行数
func Apply(cur *Current, v *Validated, sv ServedView, reserved map[string]bool,
	stride, context int) *Result {
	// 1. 拼新行数组 + 旧→新映射
	newLines := make([]string, 0, len(cur.Lines)+len(v.ops))
	mapping := make([]int, len(cur.Lines))
	for i := range mapping {
		mapping[i] = -1
	}
	spans := make([]editSpan, 0, len(v.ops))

	cursor := 0
	for _, op := range v.ops {
		for cursor < op.Range.Start-1 {
			mapping[cursor] = len(newLines)
			newLines = append(newLines, cur.Lines[cursor])
			cursor++
		}
		ns := len(newLines) + 1
		newLines = append(newLines, op.Replacement...)
		ne := len(newLines)
		spans = append(spans, editSpan{op.Range.Start, op.Range.End, ns, ne, op.Replacement})
		cursor = op.Range.End
	}
	for ; cursor < len(cur.Lines); cursor++ {
		mapping[cursor] = len(newLines)
		newLines = append(newLines, cur.Lines[cursor])
	}

	// 2. 编辑区间里原来的锚点。
	//
	// 这一集有两个不同用途，**不能混用**：
	//
	//	removedForAlloc  喂给 MapStable 的"这些旧行没了"集合 —— 它决定
	//	                 哪些旧行不参与「按 canon 就近继承」。所以当替换文本
	//	                 与原行 canon 相同时（只改空白、或"原样重写"一行），
	//	                 该行**必须不在**这里面：否则白白换掉一个还能用的锚点。
	//
	//	retireCandidate  只要"原本属于被编辑区间"就算，用于持久化 retired。
	//	                 它比 removedForAlloc 更宽：MapStable 万一没能配对成功，
	//	                 锚点其实已经释放，也得挡住不许再分配（模型可能还持有）。
	removedForAlloc := map[string]bool{}
	retireCandidate := map[string]bool{}
	for _, sp := range spans {
		replCanon := make(map[string]bool, len(sp.repl))
		for _, r := range sp.repl {
			replCanon[hashline.Canon(r)] = true
		}
		for k := sp.oldStart - 1; k < sp.oldEnd; k++ {
			h := cur.Hashes[k]
			if h == "" {
				continue
			}
			retireCandidate[h] = true
			if !replCanon[hashline.Canon(cur.Lines[k])] {
				removedForAlloc[h] = true
			}
		}
	}

	// 3. 重算锚点：未编辑的行按 canon 就近继承旧锚点，新行才走分配。
	newHashes := hashline.MapStable(cur.Lines, cur.Hashes, newLines,
		removedForAlloc, reserved, stride)

	// 4. 新的 served 数组
	editedNew := make(map[int]bool, len(newLines))
	for _, sp := range spans {
		for x := sp.newStart; x <= sp.newEnd; x++ {
			editedNew[x-1] = true
		}
	}
	newServed := make([]string, len(newLines))
	for k, np := range mapping {
		if np < 0 || editedNew[np] {
			continue
		}
		if k < len(sv.Hashes) {
			newServed[np] = sv.Hashes[k]
		}
	}
	for _, sp := range spans {
		for x := sp.newStart; x <= sp.newEnd; x++ {
			newServed[x-1] = newHashes[x-1]
		}
	}

	// 5. canons（供后续漂移检测 / 诊断）
	canons := make([]string, len(newLines))
	for i, ln := range newLines {
		canons[i] = hashline.Canon(ln)
	}

	// 6. 需要持久化的 retired：原本在编辑区间里、编辑后**没有**继续被使用的锚点。
	inUse := make(map[string]bool, len(newHashes))
	for _, h := range newHashes {
		inUse[h] = true
	}
	retire := make([]string, 0, len(retireCandidate))
	for h := range retireCandidate {
		if !inUse[h] {
			retire = append(retire, h)
		}
	}
	sort.Strings(retire)

	// 7. diff：逐行比对，只有真正变了的行才打 - / +
	diff, added, removed := buildDiff(cur.Lines, cur.Hashes, newLines, newHashes, spans, context)

	//  8. diff 打印出来的每一行都是模型见过的行 → 一律进 served。
	//     Del 行是已删除的旧行（锚点已 release，行本身也不在了），Elide 只是省略号，
	//     两者都不是"模型能接着用的行"，不打标记。
	for _, d := range diff {
		if d.Kind == DiffDel || d.Kind == DiffElide || d.Line.Hash == "" {
			continue
		}
		if i := d.Line.Line - 1; i >= 0 && i < len(newServed) {
			newServed[i] = d.Line.Hash
		}
	}
	res := &Result{
		Lines:   newLines,
		Hashes:  newHashes,
		Served:  newServed,
		Canons:  canons,
		Retire:  retire,
		Mapping: mapping,
		Added:   added,
		Removed: removed,
		Diff:    diff,
	}
	return res
}

// --- 内部辅助 ---

func servedHas(sv ServedView, h string) bool {
	for _, x := range sv.Hashes {
		if x == h {
			return true
		}
	}
	return false
}

func servedPos(sv ServedView, h string) int {
	for i, x := range sv.Hashes {
		if x == h {
			return i + 1
		}
	}
	return 0
}

// servedAt 返回第 line 行（1-based）serve 时的锚点；越界或未 serve 返回 ""。
func servedAt(sv ServedView, line int) string {
	if line < 1 || line > len(sv.Hashes) {
		return ""
	}
	return sv.Hashes[line-1]
}

func firstOrZero(ps []int) (int, bool) {
	if len(ps) == 1 {
		return ps[0], true
	}
	return 0, false
}

// rangeMatches 报告 [start,end] 内每一行的当前锚点是否都等于 served 记录。
//
// 要求**逐行**相等：中间某行未被 serve（served 记的是空串）也算不匹配——
// 那说明模型没见过这一行，不能拿它当"没变"的证据。
func rangeMatches(sv ServedView, cur *Current, start, end int) bool {
	for ln := start; ln <= end; ln++ {
		if ln-1 >= len(cur.Hashes) {
			return false
		}
		sh := servedAt(sv, ln)
		if sh == "" || sh != cur.Hashes[ln-1] {
			return false
		}
	}
	return true
}

func ambiguousFailure(cur *Current, h string, maxEcho int) *Failure {
	ps := cur.Occurs(h)
	lo, hi := ps[0], ps[len(ps)-1]
	f := &Failure{Code: CodeAmbiguous,
		Msg: fmt.Sprintf("%s appears %d times (lines %s) — refusing to guess",
			h, len(ps), joinInts(ps))}
	f.Region = ranges.Range{Start: lo, End: hi}
	f.RegionValid = true
	f.Lines = collect(cur, lo, hi, maxEcho)
	return f
}

func collect(cur *Current, start, end, maxEcho int) []Line {
	if start < 1 {
		start = 1
	}
	if end > cur.LineCount() {
		end = cur.LineCount()
	}
	if maxEcho > 0 && end-start+1 > maxEcho {
		end = start + maxEcho - 1
	}
	out := make([]Line, 0, end-start+1)
	for ln := start; ln <= end; ln++ {
		out = append(out, cur.LineAt(ln))
	}
	return out
}

func joinInts(xs []int) string {
	s := ""
	for i, x := range xs {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprint(x)
	}
	return s
}
