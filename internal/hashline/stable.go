package hashline

import "sort"

// MapStable 编辑后重算锚点：先按 canon 把未删除的旧行配对到新位置，
// 配对成功的直接继承旧锚点，只有配不上的（新行）才走分配。
//
// 这是「锚点跟内容走，不跟行号走」的实现（需求 1）。
//
// removed  是本次编辑释放掉的旧锚点集合；这些槽位禁止再分配
//
//	（模型可能还持有它们）。
//
// reserved 是额外禁用的锚点（如另一分支遗留）。
func MapStable(oldLines, oldHashes, newLines []string, removed, reserved map[string]bool, stride int) []string {
	a := NewAllocator(stride)

	// 1. 全部旧锚点先占位——继承路径与新分配路径共用一张表。
	oldHashIndex := make(map[string]int, len(oldHashes))
	for i, h := range oldHashes {
		oldHashIndex[h] = i
		a.Reserve(h)
	}
	// 2. 被释放 / 被预留的锚点同样占位。
	for h := range removed {
		a.Reserve(h)
	}
	for h := range reserved {
		a.Reserve(h)
	}

	// 3. 找出被删除的旧行区间，用来估算整体行号偏移。
	removedIdx := make(map[int]bool, len(removed))
	for h := range removed {
		if i, ok := oldHashIndex[h]; ok {
			removedIdx[i] = true
		}
	}
	spanStart, spanEnd := len(oldLines), -1
	for i := range removedIdx {
		if i < spanStart {
			spanStart = i
		}
		if i > spanEnd {
			spanEnd = i
		}
	}
	spanLen := 0
	if spanEnd >= spanStart {
		spanLen = spanEnd - spanStart + 1
	}
	replacementLen := len(newLines) - len(oldLines) + spanLen
	shift := 0
	if spanEnd >= spanStart {
		shift = replacementLen - spanLen
	}

	// 4. canon 内容 → 新行下标列表（按 canon 分桶，就近配对）。
	newByContent := make(map[string][]int, len(newLines))
	for i, ln := range newLines {
		c := Canon(ln)
		newByContent[c] = append(newByContent[c], i)
	}

	// 5. 逐个未删除的旧行找最近的新位置，继承锚点。
	newHashes := make([]string, len(newLines))
	for i := 0; i < len(oldLines); i++ {
		if removedIdx[i] {
			continue
		}
		c := Canon(oldLines[i])
		cands := newByContent[c]
		if len(cands) == 0 {
			continue
		}
		// 删除区间之后的旧行，目标位置要加上偏移。
		target := i
		if i > spanEnd {
			target = i + shift
		}
		p := nearestNew(cands, target)
		if p < 0 {
			continue
		}
		newIdx := cands[p]
		newByContent[c] = append(cands[:p:p], cands[p+1:]...)
		newHashes[newIdx] = oldHashes[i]
	}

	// 6. 剩下的（真正的新行）走分配。
	for i := 0; i < len(newLines); i++ {
		if newHashes[i] != "" {
			continue
		}
		h, err := a.Assign(newLines[i])
		if err != nil {
			h = degradedHash(newLines[i], i)
		}
		newHashes[i] = h
	}
	return newHashes
}

// nearestNew 在有序候选中找离 target 最近的；等距时取左（更靠前，保守）。
func nearestNew(cands []int, target int) int {
	if len(cands) == 0 {
		return -1
	}
	pos := sort.SearchInts(cands, target)
	left, right := pos-1, pos
	if left >= 0 && (right >= len(cands) || target-cands[left] <= cands[right]-target) {
		return left
	}
	if right < len(cands) {
		return right
	}
	return -1
}
