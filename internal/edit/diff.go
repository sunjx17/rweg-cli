package edit

// DiffKind 是 diff 行的类别。
type DiffKind uint8

const (
	DiffContext DiffKind = iota // 上下文：内容没变，只为定位
	DiffDel                     // 删除的旧行
	DiffIns                     // 新增的新行
	DiffElide                   // 省略号：两段改动之间跳过的未改动行
)

// DiffLine 是 diff 里的一行。
//
// 锚点取哪一套取决于类别：
//   - Context / Ins：新文件里的锚点（模型接下来要用的那套）
//   - Del：旧文件里那行的锚点（已释放，只用来显示删掉了什么）
type DiffLine struct {
	Kind DiffKind
	Line Line
}

// editSpan 是一条 -c 在原/新文件里占据的区间（1-based 闭区间）。
type editSpan struct {
	oldStart, oldEnd int
	newStart, newEnd int
	repl             []string
}

// diffCap 限制单个区间逐行比对的规模（旧行数 × 新行数，再加表头）。
// 超过就退化成"整段删、整段加"：那种区间本来就是整体重写，逐行比对没有意义，
// 而 DP 表会吃掉几十 MB。
const diffCap = 1_000_000

// buildDiff 生成 git 风格的统一 diff：只有**真正变了**的行才打 - / +。
//
// 编辑区间里内容没变的行算上下文，不算改动 —— 把一行换成"同一行 + 一个空行"
// 时，那一行没动，只是后面插了一行；若按区间长度直接算 +N -M，没变的行也会
// 被报成改动，模型会以为那行的锚点换了。
//
// 改动前后各带 ctx 行上下文；同一行上下文不会被两处重复打印，
// 相隔较远的两段之间插一行 DiffElide。
func buildDiff(oldLines, oldHashes, newLines, newHashes []string,
	spans []editSpan, ctx int) ([]DiffLine, int, int) {
	var flat []DiffLine
	var anchors []int // 属于编辑区间的行下标：没有真实改动时靠它定位
	added, removed := 0, 0

	cursor := 0 // 已复制的旧行数（0-based 游标）
	for _, sp := range spans {
		off := sp.newStart - sp.oldStart // 本区间之前累积的行号偏移
		for i := cursor; i < sp.oldStart-1; i++ {
			flat = append(flat, contextAt(newLines, newHashes, i+off))
		}
		start := len(flat)
		for _, op := range lcsOps(oldLines[sp.oldStart-1:sp.oldEnd], sp.repl) {
			switch op.kind {
			case opEqual:
				flat = append(flat, contextAt(newLines, newHashes, sp.newStart-1+op.newIdx))
			case opDel:
				oi := sp.oldStart - 1 + op.oldIdx
				flat = append(flat, DiffLine{Kind: DiffDel,
					Line: Line{Line: 0, Hash: at(oldHashes, oi), Text: oldLines[oi]}})
				removed++
			default:
				flat = append(flat, DiffLine{Kind: DiffIns,
					Line: Line{Line: sp.newStart + op.newIdx, Hash: at(newHashes, sp.newStart-1+op.newIdx),
						Text: newLines[sp.newStart-1+op.newIdx]}})
				added++
			}
		}
		for i := start; i < len(flat); i++ {
			anchors = append(anchors, i)
		}
		cursor = sp.oldEnd
	}
	off := len(newLines) - len(oldLines)
	for i := cursor; i < len(oldLines); i++ {
		flat = append(flat, contextAt(newLines, newHashes, i+off))
	}
	return groupHunks(flat, anchors, ctx), added, removed
}

// at 安全取哈希：哈希切片缺失（或与行数不等）时给空串，
// 调用方渲染成 ~~~，不会因为越界把工具打崩。
func at(hashes []string, i int) string {
	if i < 0 || i >= len(hashes) {
		return ""
	}
	return hashes[i]
}

func contextAt(lines, hashes []string, idx int) DiffLine {
	if idx < 0 || idx >= len(lines) {
		return DiffLine{Kind: DiffContext}
	}
	return DiffLine{Kind: DiffContext, Line: Line{Line: idx + 1, Hash: at(hashes, idx), Text: lines[idx]}}
}

// groupHunks 只保留改动（或 anchors）前后 ctx 行内的上下文：
// 远端未改动的行整段丢掉，两段之间插一行省略号；首尾被丢掉的行不插。
func groupHunks(flat []DiffLine, anchors []int, ctx int) []DiffLine {
	if len(flat) == 0 {
		return nil
	}
	keep := make([]bool, len(flat))
	mark := func(i int) {
		lo, hi := i-ctx, i+ctx
		if lo < 0 {
			lo = 0
		}
		if hi >= len(flat) {
			hi = len(flat) - 1
		}
		for k := lo; k <= hi; k++ {
			keep[k] = true
		}
	}
	for i, l := range flat {
		if l.Kind == DiffDel || l.Kind == DiffIns {
			mark(i)
		}
	}
	for _, i := range anchors {
		mark(i)
	}
	out := make([]DiffLine, 0, len(flat))
	elided := false
	for i, l := range flat {
		if keep[i] {
			if elided {
				out = append(out, DiffLine{Kind: DiffElide})
				elided = false
			}
			out = append(out, l)
			continue
		}
		if len(out) > 0 {
			elided = true
		}
	}
	return out
}

// --- 行级 LCS ---

const (
	opEqual = iota
	opDel
	opIns
)

type lcsOp struct {
	kind           int
	oldIdx, newIdx int
}

// lcsOps 求两段行的最短编辑脚本（先剥公共前后缀，再在剩下的部分上做 LCS）。
//
// 记录顺序即输出顺序：equal / del / ins 交错出现，调用方按顺序铺成 diff。
func lcsOps(old, new []string) []lcsOp {
	p := 0
	for p < len(old) && p < len(new) && old[p] == new[p] {
		p++
	}
	s := 0
	for s < len(old)-p && s < len(new)-p && old[len(old)-1-s] == new[len(new)-1-s] {
		s++
	}
	oldMid := old[p : len(old)-s]
	newMid := new[p : len(new)-s]

	ops := make([]lcsOp, 0, len(old)+len(new))
	for i := 0; i < p; i++ {
		ops = append(ops, lcsOp{kind: opEqual, oldIdx: i, newIdx: i})
	}
	if (len(oldMid)+1)*(len(newMid)+1) > diffCap {
		// 太大：不逐行配对，整段删 + 整段加。
		for i := range oldMid {
			ops = append(ops, lcsOp{kind: opDel, oldIdx: p + i})
		}
		for i := range newMid {
			ops = append(ops, lcsOp{kind: opIns, newIdx: p + i})
		}
	} else {
		ops = append(ops, lcsBacktrack(oldMid, newMid, p)...)
	}
	for i := len(old) - s; i < len(old); i++ {
		ops = append(ops, lcsOp{kind: opEqual, oldIdx: i, newIdx: len(new) - (len(old) - i)})
	}
	return ops
}

func lcsBacktrack(old, new []string, base int) []lcsOp {
	m, n := len(old), len(new)
	// dp[i][j] = old[i:] 与 new[j:] 的最长公共子序列长度
	dp := make([][]int32, m+1)
	for i := range dp {
		dp[i] = make([]int32, n+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := n - 1; j >= 0; j-- {
			if old[i] == new[j] {
				dp[i][j] = dp[i+1][j+1] + 1
				continue
			}
			if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := make([]lcsOp, 0, m+n)
	i, j := 0, 0
	for i < m && j < n {
		switch {
		case old[i] == new[j]:
			ops = append(ops, lcsOp{kind: opEqual, oldIdx: base + i, newIdx: base + j})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, lcsOp{kind: opDel, oldIdx: base + i})
			i++
		default:
			ops = append(ops, lcsOp{kind: opIns, newIdx: base + j})
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, lcsOp{kind: opDel, oldIdx: base + i})
	}
	for ; j < n; j++ {
		ops = append(ops, lcsOp{kind: opIns, newIdx: base + j})
	}
	return ops
}
