package nsid

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestNewShape(t *testing.T) {
	ns := NewFor(`D:\nginx\2dsim`, `D:\nginx\2dsim`)
	// w + 4 + - + 5 = 11
	if len(ns) != 11 {
		t.Fatalf("len = %d, want 11 (%q)", len(ns), ns)
	}
	if !Valid(ns) {
		t.Errorf("generated ns is not Valid: %q", ns)
	}
	if ns[0] != 'w' || ns[5] != '-' {
		t.Errorf("format wrong: %q", ns)
	}
}

// 不同 scope 必须产出不同 ns 前缀 —— 这是「DB 路径配错也不串」的基础。
func TestDifferentScopesDifferentNS(t *testing.T) {
	now := time.Unix(1750000000, 0)
	a := New(`D:\a|x`, now)
	b := New(`D:\b|x`, now)
	if a == b {
		t.Fatalf("collision: %q", a)
	}
	if strings.Split(a, "-")[0] == strings.Split(b, "-")[0] {
		t.Errorf("scope hash prefix collided: %q vs %q", a, b)
	}
}

// 同一 scope + 同一时刻 → 同一 ns（可复现）。
func TestDeterministic(t *testing.T) {
	now := time.Unix(1750000000, 0)
	if New("s", now) != New("s", now) {
		t.Error("New is not deterministic")
	}
}

// 同一 scope、不同时刻 → 后缀不同（两个终端隔离）。
func TestTimeSuffixDiffers(t *testing.T) {
	a := New("s", time.Unix(1750000000, 0))
	b := New("s", time.Unix(1750003600, 0))
	if a == b {
		t.Error("two terminals got same ns")
	}
	if !Valid(a) || !Valid(b) {
		t.Errorf("invalid shapes: %q %q", a, b)
	}
}

func TestValid(t *testing.T) {
	good := []string{"wK3fP-9xT2q", "w0000-00000", "w9999-99999", "wABCD-abcde"}
	for _, g := range good {
		if !Valid(g) {
			t.Errorf("Valid(%q) = false", g)
		}
	}
	bad := []string{"", "wK3fP", "xK3fP-9xT2q", "wK3fP9xT2q", "wK3fP-9xT2!", "wK3fP-9xT2qq", "W1234-12345", "wK3f-9xT2q"}
	for _, b := range bad {
		if Valid(b) {
			t.Errorf("Valid(%q) = true", b)
		}
	}
}

func TestEncode(t *testing.T) {
	// 高位补 '0'
	if got := encode(0, 4); got != "0000" {
		t.Errorf("encode(0) = %q, want 0000", got)
	}
	// alphabet 末位是 '9'（A-Z a-z 0-9，index 61 = '9'）
	if got := encode(61, 4); got != "0009" {
		t.Errorf("encode(61) = %q, want 0009", got)
	}
	if got := encode(62, 4); got != "00BA" {
		t.Errorf("encode(62) = %q, want 00BA", got)
	}
	// 62^4 = 14776336，-1 应全为 '9'，不溢出
	if got := encode(14776335, 4); got != "9999" {
		t.Errorf("encode(62^4-1) = %q, want 9999", got)
	}
	// 位宽不丢高位：长度固定
	if got := encode(14776335, 5); got != "09999" {
		t.Errorf("encode(62^4-1, 5) = %q, want 09999", got)
	}
}

// 5 位 base62 装不下当前 unix 秒（62^5=9.16亿 < 1.79e9）—— 这就是纪元偏移的理由。
func TestFiveDigitsCannotHoldRawUnixTime(t *testing.T) {
	const maxFive = 916132831 // 62^5 - 1
	now := time.Now().Unix()
	if now <= maxFive {
		t.Skip("unix time still fits in 5 base62 digits; epoch offset no longer required")
	}
	// 偏移后必须装得下，且留有充足余量
	if now-epoch > maxFive {
		t.Errorf("epoch offset insufficient: now-epoch = %d > %d", now-epoch, maxFive)
	}
}

// Age 能从 ns 反解出创建时间（GC 判据）。
func TestAgeRoundTrip(t *testing.T) {
	created := time.Unix(1750000000, 0)
	ns := New("s", created)
	got, ok := Age(ns, time.Now())
	if !ok {
		t.Fatal("Age failed on valid ns")
	}
	if got.Unix() != created.Unix() {
		t.Errorf("Age = %d, want %d", got.Unix(), created.Unix())
	}
	if _, ok := Age("garbage", time.Now()); ok {
		t.Error("Age should reject invalid ns")
	}
}

// Resolve：$RWEG_NS 已设置则原样复用（支持多终端共享 served）。
func TestResolveReusesEnv(t *testing.T) {
	t.Setenv(EnvVar, "wK3fP-9xT2q")
	ns, degraded, warn := Resolve(`D:\a`, `D:\a`)
	if ns != "wK3fP-9xT2q" {
		t.Errorf("ns = %q, want the env value", ns)
	}
	if degraded || warn != "" {
		t.Errorf("should not degrade: degraded=%v warn=%q", degraded, warn)
	}
}

// 未设置时按 cwd 派生，且**确定性** —— 同终端内 read→edit 链路必须通。
func TestResolveDegradedButStable(t *testing.T) {
	t.Setenv(EnvVar, "")
	ns1, degraded, warn := Resolve(`D:\proj`, `D:\proj`)
	ns2, _, _ := Resolve(`D:\proj`, `D:\proj`)
	if !degraded {
		t.Error("should report degraded")
	}
	if !strings.Contains(warn, EnvVar) {
		t.Errorf("warn should mention %s: %q", EnvVar, warn)
	}
	if ns1 != ns2 {
		t.Errorf("degraded ns not stable: %q vs %q", ns1, ns2)
	}
	if !Valid(ns1) {
		t.Errorf("degraded ns invalid: %q", ns1)
	}
}

func TestResolveDifferentCwdDifferentNS(t *testing.T) {
	t.Setenv(EnvVar, "")
	a, _, _ := Resolve(`D:\a`, `D:\a`)
	b, _, _ := Resolve(`D:\b`, `D:\b`)
	if a == b {
		t.Error("different cwds must not share degraded ns")
	}
}

func TestSetEnv(t *testing.T) {
	if err := SetEnv("wABCDE-12345"); err != nil {
		t.Fatal(err)
	}
	v, ok := FromEnv()
	if !ok || v != "wABCDE-12345" {
		t.Errorf("FromEnv = %q %v", v, ok)
	}
	_ = os.Unsetenv(EnvVar)
	if _, ok := FromEnv(); ok {
		t.Error("FromEnv should report unset")
	}
}

func TestFromEnvTrims(t *testing.T) {
	t.Setenv(EnvVar, "  wABCDE-12345\n")
	v, ok := FromEnv()
	if !ok || v != "wABCDE-12345" {
		t.Errorf("FromEnv = %q %v", v, ok)
	}
}
