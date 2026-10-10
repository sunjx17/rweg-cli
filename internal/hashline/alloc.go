package hashline

import (
	"errors"
	"fmt"
)

// ErrHashSpace 表示 238328 个槽位全被占满。调用方应报 E_HASH_SPACE 并提示拆分文件。
var ErrHashSpace = errors.New("E_HASH_SPACE: anchor space exhausted, split the file")

// Allocator 维护一个文件内已占槽位，保证同文件锚点唯一（需求 6）。
//
// 生命周期 = 一次「为某个文件计算锚点数组」的过程。
// 238328 位 ≈ 29KB，直接用 []bool，够用且零依赖。
type Allocator struct {
	used   []bool
	stride int
	hint   int // 探测起点游标，避免每次都从 base 开始撞一长串
}

// NewAllocator 创建一个分配器。stride<=0 时用 DefaultProbeStride。
func NewAllocator(stride int) *Allocator {
	if stride <= 0 {
		stride = DefaultProbeStride
	}
	return &Allocator{used: make([]bool, HashSpace), stride: stride}
}

// Reserve 把一个已有锚点标记为已占。
//
// 继承旧锚点时必须先 Reserve 全部旧锚点，否则继承可能与新分配的锚点撞车
// —— 见 plan §2.5。
func (a *Allocator) Reserve(h string) bool {
	idx, ok := HashToIndex(h)
	if !ok {
		return false
	}
	a.reserveSlot(idx)
	return true
}

// ReserveSlot 按槽位下标标记已占。
func (a *Allocator) ReserveSlot(idx int) {
	a.reserveSlot(idx)
}

func (a *Allocator) reserveSlot(idx int) {
	if idx < 0 || idx >= HashSpace {
		return
	}
	a.used[idx] = true
	if idx+a.stride > a.hint {
		a.hint = idx + a.stride
	}
}

// Assign 为一行内容分配锚点。
//
// base 槽位空则直接用；否则从 hint 游标开始按 stride 线性探测。
// 步长与 HashSpace 互质，保证能遍历全空间；探测满一圈说明空间耗尽。
func (a *Allocator) Assign(line string) (string, error) {
	base := BaseIndex(Canon(line))
	idx := base
	if a.used[idx] {
		idx = a.hint % HashSpace
		for n := 0; a.used[idx]; n++ {
			if n >= HashSpace {
				return "", ErrHashSpace
			}
			idx += a.stride
			if idx >= HashSpace {
				idx -= HashSpace
			}
		}
	}
	a.used[idx] = true
	a.hint = idx + a.stride
	return IndexToHash(idx), nil
}

// Pure 为整个文件从零计算锚点。reserved 里的锚点预先占位。
//
// 不返回 error：空间耗尽时退化为掺入行号的确定性哈希，
// 保证函数签名简单；CLI 层走 Assign 路径时报 E_HASH_SPACE。
func Pure(lines []string, stride int, reserved map[string]bool) []string {
	a := NewAllocator(stride)
	for h := range reserved {
		a.Reserve(h)
	}
	out := make([]string, len(lines))
	for i, ln := range lines {
		h, err := a.Assign(ln)
		if err != nil {
			h = degradedHash(ln, i)
		}
		out[i] = h
	}
	return out
}

// degradedHash 是空间耗尽时的兜底：掺入行号保证同行唯一。
// 正常路径永远走不到这里。
func degradedHash(line string, i int) string {
	mixed := Canon(line) + fmt.Sprintf("\x00%d", i)
	return IndexToHash(int(XXH32([]byte(mixed), 0) % HashSpace))
}
