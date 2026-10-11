package edit

import (
	"strings"

	"rweg/internal/hashline"
)

// ContentAnchorKind 给「替换文本里以锚点开头」的行分类。
type ContentAnchorKind uint8

const (
	// AnchorInFile：行首那个 3 字符是目标文件**当前**的锚点 —— 这行不是内容，
	// 是从 read / grep 输出里连锚点一起贴回来的。调用方应硬拒。
	AnchorInFile ContentAnchorKind = iota
	// AnchorServed：行首那个锚点在本终端的 served 基线里，但已不是本文件当前的
	// 锚点（对应的行被改过或删过）。写入无害，只提醒模型前缀大概是多余的。
	AnchorServed
	// AnchorUnserved：长得像锚点，但本文件当前锚点与本终端基线里都没有它。
	AnchorUnserved
)

// ContentAnchor 是替换文本里以「锚点│」开头的一行。
type ContentAnchor struct {
	Change int    // 第几条 -c（1-based，与调用里的出现顺序一致）
	K      int    // 该条替换文本里的第几行（1-based）
	Anchor string // 行首那个 3 字符锚点
	Bare   string // 去掉「锚点│」前缀（含其前导空白）之后的内容
	Kind   ContentAnchorKind
}

// ScanContentAnchors 找出替换文本里误带行首锚点前缀的行。
//
// 判据与参考实现（Rianico/dsh-better-edit 的 stripBarePrefixes / HL_BARE_PREFIX_RE）
// 一致：`(可选空白) + 3 字符 base62 + │`。diff 预览里的上下文行是
// `"  CTt│..."`，前导空白照吃，所以那种形状也认得出来。
//
// fileHashes 是目标文件当前的锚点数组，servedHashes 是本终端的 served 基线
// （可以是 nil = 本终端还没读过这个文件）。命中 fileHashes 的行是「拿 read 输出
// 当内容」，命中 servedHashes 的行是「拿旧锚点当内容」，两者都不该原样写进文件。
func ScanContentAnchors(ops []Op, fileHashes, servedHashes []string) []ContentAnchor {
	inFile := make(map[string]bool, len(fileHashes))
	for _, h := range fileHashes {
		if h != "" {
			inFile[h] = true
		}
	}
	inServed := make(map[string]bool, len(servedHashes))
	for _, h := range servedHashes {
		if h != "" {
			inServed[h] = true
		}
	}

	var out []ContentAnchor
	for i, op := range ops {
		for k, line := range op.Replacement {
			anchor, bare, ok := splitHeadAnchor(line)
			if !ok {
				continue
			}
			kind := AnchorUnserved
			switch {
			case inFile[anchor]:
				kind = AnchorInFile
			case inServed[anchor]:
				kind = AnchorServed
			}
			out = append(out, ContentAnchor{
				Change: i + 1, K: k + 1, Anchor: anchor, Bare: bare, Kind: kind,
			})
		}
	}
	return out
}

// BlockingContentAnchors 只留下必须硬拒的那些行（前缀命中本文件当前锚点）。
func BlockingContentAnchors(found []ContentAnchor) []ContentAnchor {
	var out []ContentAnchor
	for _, ca := range found {
		if ca.Kind == AnchorInFile {
			out = append(out, ca)
		}
	}
	return out
}

// splitHeadAnchor 解析「(可选空白) + 3 字符锚点 + │ + 内容」。
func splitHeadAnchor(line string) (anchor, bare string, ok bool) {
	rest := strings.TrimLeft(line, " \t")
	if len(rest) < hashline.HashLen+len(hashline.Sep) {
		return "", "", false
	}
	if !strings.HasPrefix(rest[hashline.HashLen:], hashline.Sep) {
		return "", "", false
	}
	anchor = rest[:hashline.HashLen]
	if _, valid := hashline.HashToIndex(anchor); !valid {
		return "", "", false
	}
	return anchor, rest[hashline.HashLen+len(hashline.Sep):], true
}
