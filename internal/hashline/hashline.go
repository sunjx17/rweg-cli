// Package hashline 实现 hashline 锚点：每行 3 字符 base62 内容哈希。
//
// 算法必须与参考实现（Rianico/dsh-better-edit 的 src/hashline/hash-assign.ts）
// 逐字节一致，否则锚点对不上。本包的单测锁死了这一点。
package hashline

const (
	// Alphabet 是 base62 字符表，注意顺序不是 0-9A-Za-z。
	Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

	// HashLen 是锚点字符数。
	HashLen = 3

	// HashSpace = 62^3 = 238328，槽位总数。
	HashSpace = 62 * 62 * 62

	// DefaultProbeStride = 62^2+62+1 = 3907，与 HashSpace 互质，
	// 保证线性探测能遍历整个空间。
	DefaultProbeStride = 62*62 + 62 + 1

	// DefaultCanonVersion 参与快照键；改动 canon 语义时 +1，
	// 旧快照自动失效，不会串。
	DefaultCanonVersion = 2

	// Sep 是锚点与内容之间的分隔符 U+2502。
	Sep = "│"
)

// IndexToHash 把槽位下标编码成 3 字符锚点。
func IndexToHash(idx int) string {
	var b [HashLen]byte
	for i := HashLen - 1; i >= 0; i-- {
		b[i] = Alphabet[idx%62]
		idx /= 62
	}
	return string(b[:])
}

// HashToIndex 解码 3 字符锚点。非法字符返回 ok=false。
func HashToIndex(h string) (idx int, ok bool) {
	if len(h) != HashLen {
		return 0, false
	}
	for i := 0; i < HashLen; i++ {
		k := indexByte(h[i])
		if k < 0 {
			return 0, false
		}
		idx = idx*62 + k
	}
	return idx, true
}

func indexByte(c byte) int {
	switch {
	case c >= 'A' && c <= 'Z':
		return int(c - 'A')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 26
	case c >= '0' && c <= '9':
		return int(c-'0') + 52
	}
	return -1
}
