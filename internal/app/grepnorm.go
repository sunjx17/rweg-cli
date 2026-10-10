package app

import "strings"

// GNU grep 与 rg 的短选项表有两处冲突，凡是可能把参数交给 rg 的路径都要按
// 同一规则归一化：
//
//	-E / --extended-regexp  吃掉 —— rg 的正则默认就是 ERE；rg 的 -E 是
//	                        --encoding，原样递过去会报 unknown encoding
//	-G / --basic-regexp     报错 —— rg 没有 BRE，静默按 ERE 跑会把 a\|b 解错
//
// 冲突也存在于捆绑写法里（-qE、-cE、-nE、-A2E）：GNU grep 允许短选项连写，
// 必须先拆成单个 flag 再判，否则整串会被 rg 当成 -E 的编码名。

// numericShortValue 是"值必须是数字"的短 flag。它们的值读完还能继续扫捆绑里
// 剩下的 flag（-A2E = -A2 加 E）；带字符串值的（-e / -f / -g / -t / -T / -r）
// 后面全都是值，遇到就得停手。
var numericShortValue = map[byte]bool{
	'A': true, 'B': true, 'C': true, 'm': true, 'M': true, 'j': true,
}

// errBasicRegexp 是 -G / --basic-regexp 的拒绝错误。
func errBasicRegexp(long bool) error {
	name := "-G (basic regexp)"
	if long {
		name = "--basic-regexp"
	}
	return UsageErr("grep: %s is not supported", name).
		WithHint("rg 只有 ERE：交替用 a|b、分组用 ()、量词用 + * ? {}")
}

// gnuOnlyShortFlag 报告捆绑里的单个短 flag 是不是"GNU 有、rg 没有或另有含义"。
// drop=true 表示吃掉它；err != nil 表示必须拒绝。
func gnuOnlyShortFlag(ch byte) (drop bool, err error) {
	switch ch {
	case 'E':
		return true, nil
	case 'G':
		return false, errBasicRegexp(false)
	}
	return false, nil
}

// normalizeShortBundle 归一化一个短选项捆绑词（-in、-qE、-A2、-A 2）。
//
// 返回归一化后的词（"" 表示整个词都被吃掉，调用方不要传它）、值是否在下一个
// 参数里，以及错误。值一律不动：-A2 的 2、-eE 的模式 E 都原样保留。
func normalizeShortBundle(s string) (string, bool, error) {
	var b strings.Builder
	b.WriteByte('-')
	nextIsValue := false
	for k := 1; k < len(s); k++ {
		ch := s[k]
		switch ch {
		case 'E':
			continue
		case 'G':
			return "", false, errBasicRegexp(false)
		}
		b.WriteByte(ch)
		if !rgValueFlagsShort[ch] {
			continue
		}
		if k+1 >= len(s) {
			nextIsValue = true
			break
		}
		if !numericShortValue[ch] {
			b.WriteString(s[k+1:]) // 值跟在同一词里，且后面全是值
			break
		}
		start := k
		for k+1 < len(s) && s[k+1] >= '0' && s[k+1] <= '9' {
			k++
			b.WriteByte(s[k])
		}
		if k == start {
			nextIsValue = true // -AE 这种没给数字：值在下一个参数
			break
		}
	}
	if b.Len() == 1 {
		return "", nextIsValue, nil
	}
	return b.String(), nextIsValue, nil
}

// rgArgsFor 把一份用户参数改写成"交给 rg 也不会出错"的等价形态，供回退透传用。
//
// 只做三件事，其余原样（rweg 不打算做 rg 参数解析器）：
//   - 摘掉 rweg 自己的 --json
//   - 吃掉 -E / --extended-regexp（含捆绑写法里的 E）
//   - -G / --basic-regexp 报错
//
// 带值的 flag（--encoding utf-8、-A 2、-e pat）后面跟的是值，值里出现 E / G
// 也不动；-- 之后的词全是模式或路径，同样不动。
func rgArgsFor(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	afterDD := false
	for i := 0; i < len(args); i++ {
		s := args[i]
		switch {
		case afterDD:
			out = append(out, s)
		case s == "--":
			out = append(out, s)
			afterDD = true
		case s == "--json":
			// rweg 自己的 flag，rg 不认
		case strings.HasPrefix(s, "--"):
			name := s
			if eq := strings.Index(s, "="); eq >= 0 {
				name = s[:eq]
			}
			switch name {
			case "--extended-regexp":
				// 吃掉
			case "--basic-regexp":
				return nil, errBasicRegexp(true)
			default:
				out = append(out, s)
				if rgValueFlagsLong[name] && !strings.Contains(s, "=") && i+1 < len(args) {
					i++
					out = append(out, args[i])
				}
			}
		case len(s) > 1 && s[0] == '-':
			norm, nextIsValue, err := normalizeShortBundle(s)
			if err != nil {
				return nil, err
			}
			if norm != "" {
				out = append(out, norm)
			}
			if nextIsValue && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		default:
			out = append(out, s)
		}
	}
	return out, nil
}
