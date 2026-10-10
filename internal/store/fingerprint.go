package store

import "fmt"

// ContentFingerprint 由**行内容**算出稳定指纹（FNV-1a 64）。
//
// 为什么不用原文：原文包含行尾/BOM，同一份内容在不同平台会有不同字节，
// 会导致同一文件在不同机器上缓存不命中。
// 为什么不用锚点数组当键：查缓存时锚点还没算出来。
//
// 指纹相同 → 内容相同 → 锚点必然相同（锚点是内容的纯函数），
// 所以用指纹查缓存是安全的。
func ContentFingerprint(lines []string) string {
	h := uint64(1469598103934665603) // FNV-1a offset basis
	for _, ln := range lines {
		for i := 0; i < len(ln); i++ {
			h ^= uint64(ln[i])
			h *= 1099511628211
		}
		h ^= '\n'
		h *= 1099511628211
	}
	return fmt.Sprintf("%016x", h)
}
