// Package nsid 生成与解析 namespace。
//
// namespace 的生命周期 = 终端的生命周期：值存在终端环境变量 RWEG_NS 里，
// 终端关闭 → 环境变量消失 → ns 自动失效。**无需任何清理钩子**，
// DB 里的 ns 行只是历史残留，TTL 到期删掉即可。
//
// 格式：w<5位>-<5位>，例 wK3fP-9xT2q
//
//	前 5 位绑定 cwd + 配置目录：不同项目天然隔离，
//	  即使 DB 路径配错也不会串锚点。
//	后 5 位是时间戳：同一目录的两个终端也隔离。
package nsid

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// EnvVar 是存放 ns 的环境变量名。
	EnvVar = "RWEG_NS"

	// Prefix 是所有自动生成 ns 的前缀，肉眼可辨识。
	Prefix = "w"

	// alphabet 是 base62 字符表（与 hashline 同一套）。
	alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

	// hashDigits / timeDigits 是 ns 两段的长度。
	//
	// plan §4.2 正文写"前 5 位 + 后 5 位，长度 11"——自相矛盾。
	// 以它给的示例 wK3fP-9xT2q 为准：hash 4 位 + 时间 5 位 = 11 字符。
	// 4 位 base62 = 1477633 种组合，对 cwd+配置目录 足够。
	hashDigits = 4
	timeDigits = 5

	// padChar 是高位补位字符，必须是 alphabet 的成员（否则 Valid 会拒）。
	padChar = '0'

	// epoch 是时间戳基准。
	//
	// 为什么要偏移：62^5 = 916,132,831，装不下当前 unix 秒（~1.79e9）。
	// 以 2020-01-01 为纪元后，当前值 ~1.79e8，宽裕；
	// 可用到 916M 秒之后 ≈ 2049 年，够本工具用。
	epoch = 1577836800 // 2020-01-01T00:00:00Z
)

// New 生成一个新 ns：前 4 位 = base62(sha1(scope))，后 5 位 = base62(相对纪元的秒数)。
//
// scope 通常是 "<cwd>|<配置目录>"，让不同项目/不同配置天然隔离。
func New(scope string, now time.Time) string {
	sum := sha1.Sum([]byte(scope))
	// 取前 8 字节当 uint64，再折成 4 位 base62（62^4 = 1477633，够用）
	head := binary.BigEndian.Uint64(sum[:8])
	tail := uint64(now.Unix() - epoch)
	if now.Unix() < epoch {
		tail = 0 // 时钟早于纪元（几乎不可能），退化为 0 而不是下溢成天文数字
	}
	return fmt.Sprintf("%s%s-%s", Prefix, encode(head, hashDigits), encode(tail, timeDigits))
}

// NewFor 按当前 cwd + 配置目录生成 ns。
func NewFor(cwd, cfgDir string) string {
	return New(cwd+"|"+cfgDir, time.Now())
}

// encode 把 uint64 编成定长 base62 串。
//
// 高位补 '0' 而不是 'A' —— ns 是给人看的，'0' 补位在视觉上更接近「数字」，
// 且不会与 alphabet 首字母混淆。
func encode(v uint64, width int) string {
	buf := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		buf[i] = padChar
		if v > 0 {
			buf[i] = alphabet[v%62]
			v /= 62
		}
	}
	return string(buf)
}

// Valid 报告 ns 是否形状合法（不校验是否真实存在过）。
//
// 长度 11 = 前缀 1 + 4 位 hash + 分隔符 1 + 5 位时间戳。
func Valid(ns string) bool {
	if len(ns) != hashDigits+timeDigits+2 {
		return false
	}
	if ns[0] != Prefix[0] || ns[1+hashDigits] != '-' {
		return false
	}
	return allBase62(ns[1:1+hashDigits]) && allBase62(ns[hashDigits+2:])
}

func allBase62(s string) bool {
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// FromEnv 读取 $RWEG_NS。第二个返回值 false 表示未设置。
func FromEnv() (string, bool) {
	v := strings.TrimSpace(os.Getenv(EnvVar))
	if v == "" {
		return "", false
	}
	return v, true
}

// Resolve 返回本次调用应使用的 ns，以及它是否降级。
//
// 优先级：
//  1. $RWEG_NS 已设置 → 直接用（支持手动固定，实现多终端共享 served）
//  2. 未设置 → 按 cwd + 配置目录派生一个**确定性** ns
//
// 降级语义（plan §4.4）：派生 ns 是 cwd 的函数，同一个终端里反复调用得到同一个 ns，
// 所以「read → edit」链路仍然通。不像「每进程一个随机 ns」那样让 edit 永远查不到 served。
// 代价是隔离性变弱：不同终端在同目录会共用 served 状态。
//
// 第三个返回值是提示语（可能为空），用于 stderr 的一次性 WARN。
func Resolve(cwd, cfgDir string) (ns string, degraded bool, warn string) {
	if v, ok := FromEnv(); ok {
		return v, false, ""
	}
	return NewFor(cwd, cfgDir), true,
		"[WARN] " + EnvVar + " unset — served-state isolation disabled (cwd-derived ns).\n" +
			"       Run: source $RWEG_HOME/scripts/env.sh"
}

// SetEnv 把 ns 写进当前进程的环境变量（env.sh 之外的兜底路径用）。
func SetEnv(ns string) error { return os.Setenv(EnvVar, ns) }

// Age 返回 ns 的时间部分对应的时刻。
//
// 后 5 位是 base62(相对 2020 纪元的秒数)，取出来加回纪元即得创建时间。
// hash 段不可逆，只能靠这个。
func Age(ns string, now time.Time) (time.Time, bool) {
	if !Valid(ns) {
		return time.Time{}, false
	}
	tail := ns[hashDigits+2:]
	var v int64
	for i := 0; i < len(tail); i++ {
		v = v*62 + int64(strings.IndexByte(alphabet, tail[i]))
	}
	return time.Unix(v+epoch, 0), true
}
