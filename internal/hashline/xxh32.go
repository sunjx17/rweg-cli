package hashline

import (
	"encoding/binary"
	"math/bits"
	"strings"
)

const (
	prime1 uint32 = 2654435761
	prime2 uint32 = 2246822519
	prime3 uint32 = 3266489917
	prime4 uint32 = 668265263
	prime5 uint32 = 374761393
)

// XXH32 是 XXH32 的规范实现（0 seed 之外的 seed 也支持，便于对拍）。
//
// 不引外部依赖：cespare/xxhash v2 只导出 Sum64，v1 的 32 位 API 不带便捷函数，
// 为一个 32 位哈希不值得。官方向量已在 hashline_test.go 里锁定。
func XXH32(b []byte, seed uint32) uint32 {
	n := len(b)
	var h uint32
	if n >= 16 {
		v1 := seed + prime1 + prime2
		v2 := seed + prime2
		v3 := seed
		v4 := seed - prime1
		for len(b) >= 16 {
			v1 = bits.RotateLeft32(v1+binary.LittleEndian.Uint32(b[0:])*prime2, 13) * prime1
			v2 = bits.RotateLeft32(v2+binary.LittleEndian.Uint32(b[4:])*prime2, 13) * prime1
			v3 = bits.RotateLeft32(v3+binary.LittleEndian.Uint32(b[8:])*prime2, 13) * prime1
			v4 = bits.RotateLeft32(v4+binary.LittleEndian.Uint32(b[12:])*prime2, 13) * prime1
			b = b[16:]
		}
		h = bits.RotateLeft32(v1, 1) + bits.RotateLeft32(v2, 7) +
			bits.RotateLeft32(v3, 12) + bits.RotateLeft32(v4, 18)
	} else {
		h = seed + prime5
	}
	h += uint32(n)
	for ; len(b) >= 4; b = b[4:] {
		h += binary.LittleEndian.Uint32(b) * prime3
		h = bits.RotateLeft32(h, 17) * prime4
	}
	for ; len(b) > 0; b = b[1:] {
		h += uint32(b[0]) * prime5
		h = bits.RotateLeft32(h, 11) * prime1
	}
	h ^= h >> 15
	h *= prime2
	h ^= h >> 13
	h *= prime3
	return h ^ (h >> 16)
}

// canonWS 是规范化用的剔除表：所有 ASCII 空白 [ \t\r\n]。
// Unicode 空白保留 —— 参考实现的边界，刻意不处理。
var canonWS = strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "")

// Canon 去掉所有 ASCII 空白。
//
// 为什么剔空白：prettier/black 这类 formatter 会做纯空白重排，
// 若哈希吃空白，每次格式化后所有锚点全变、必须重读。
// 代价（已知且接受）：字符串字面量内部的空白也被剔除，
// `"x y"` 与 `"xy"` canon 相同。参考实现 ADR-0002 明确接受了这个边界。
func Canon(line string) string { return canonWS.Replace(line) }

// BaseIndex 返回 canon 后的内容哈希映射到的初始槽位。
//
// 注意先右移 14 位再取模：丢弃 xxh32 低 14 位（低质量区），
// 把 18 位有效载荷映射到 238328 个槽。
func BaseIndex(canonLine string) int {
	return int((XXH32([]byte(canonLine), 0) >> 14) % HashSpace)
}
