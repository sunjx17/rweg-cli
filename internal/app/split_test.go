package app

import (
	"reflect"
	"testing"
)

// splitReplacement 的分行规则：结尾一个换行是**行结束符**（模型常把 read
// 输出 / here-doc 原样贴回来，不该被当成"再插一个空行"）；每行结尾的 \r
// 剥掉（CRLF 来源的内容不剥会落成 \r\r\n）。
func TestSplitReplacement(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a\nb", []string{"a", "b"}},
		{"a\n", []string{"a"}},
		{"a\nb\n", []string{"a", "b"}},
		{"a\n\n", []string{"a", ""}}, // 要末尾空行就多写一个换行
		{"\n", []string{""}},
		{"a\r\nb\r\n", []string{"a", "b"}},
		{"a\r\n", []string{"a"}},
	}
	for _, tc := range cases {
		if got := splitReplacement(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitReplacement(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// splitInsertContent 同规则，区别只在 "" = 一个空行（插入没有"删除"可言）。
func TestSplitInsertContent(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{""}},
		{"a", []string{"a"}},
		{"a\n", []string{"a"}},
		{"\n", []string{""}},
		{"a\r\n", []string{"a"}},
		{"a\nb", []string{"a", "b"}},
	}
	for _, tc := range cases {
		if got := splitInsertContent(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitInsertContent(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
