package docfile

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parse(t *testing.T, s string, opts Options) *Doc {
	t.Helper()
	d, err := Parse("test.txt", []byte(s), int64(len(s)), opts)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return d
}

func mustFail(t *testing.T, s string, opts Options) *Error {
	t.Helper()
	_, err := Parse("test.txt", []byte(s), int64(len(s)), opts)
	if err == nil {
		t.Fatalf("Parse(%q) should have failed", s)
	}
	e, ok := AsError(err)
	if !ok {
		t.Fatalf("err %v is not *docfile.Error", err)
	}
	return e
}

// --- 行拆分 ---

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in       string
		want     []string
		nl       string
		trailing bool
	}{
		{"", []string{}, NewlineLF, false},
		{"a", []string{"a"}, NewlineLF, false},
		{"a\n", []string{"a"}, NewlineLF, true},
		{"a\nb", []string{"a", "b"}, NewlineLF, false},
		{"a\nb\n", []string{"a", "b"}, NewlineLF, true},
		{"a\r\nb\r\n", []string{"a", "b"}, NewlineCRLF, true},
		{"a\r\nb", []string{"a", "b"}, NewlineCRLF, false},
		// 空行是真实的行，必须保留
		{"a\n\n", []string{"a", ""}, NewlineLF, true},
		{"\n", []string{""}, NewlineLF, true},
		{"\n\n", []string{"", ""}, NewlineLF, true},
		// 纯 CR（经典 Mac）
		{"a\rb\r", []string{"a", "b"}, NewlineLF, true},
	}
	for _, c := range cases {
		d := parse(t, c.in, Options{})
		if len(d.Lines) != len(c.want) {
			t.Errorf("Parse(%q).Lines = %q, want %q", c.in, d.Lines, c.want)
			continue
		}
		for i := range c.want {
			if d.Lines[i] != c.want[i] {
				t.Errorf("Parse(%q) line %d = %q, want %q", c.in, i+1, d.Lines[i], c.want[i])
			}
		}
		if d.Newline != c.nl {
			t.Errorf("Parse(%q).Newline = %q, want %q", c.in, d.Newline, c.nl)
		}
		if d.TrailingNewline != c.trailing {
			t.Errorf("Parse(%q).TrailingNewline = %v, want %v", c.in, d.TrailingNewline, c.trailing)
		}
	}
}

func TestLineCountMatchesLines(t *testing.T) {
	for _, s := range []string{"", "a", "a\n", "a\nb\n", "\n\n\n"} {
		d := parse(t, s, Options{})
		if d.LineCount() != len(d.Lines) {
			t.Errorf("Parse(%q).LineCount() = %d, len(Lines) = %d", s, d.LineCount(), len(d.Lines))
		}
	}
}

// --- CRLF / 混合行尾 ---

func TestCRLFDetected(t *testing.T) {
	d := parse(t, "a\r\nb\r\nc\r\n", Options{})
	if d.Newline != NewlineCRLF {
		t.Errorf("Newline = %q, want CRLF", d.Newline)
	}
	if d.CRLFCount != 3 || d.LFCount != 0 {
		t.Errorf("crlf=%d lf=%d, want 3/0", d.CRLFCount, d.LFCount)
	}
	if d.MixedNewline {
		t.Error("should not be mixed")
	}
	// 行内容里不能残留 \r
	for i, ln := range d.Lines {
		if strings.Contains(ln, "\r") {
			t.Errorf("line %d still has CR: %q", i+1, ln)
		}
	}
}

func TestMixedNewlineDetected(t *testing.T) {
	d := parse(t, "a\r\nb\nc\r\n", Options{})
	if !d.MixedNewline {
		t.Error("should detect mixed newlines")
	}
	// CRLF 占多数 → 写回时用 CRLF（保证多数行不被改动）
	if d.Newline != NewlineCRLF {
		t.Errorf("Newline = %q, want CRLF (majority)", d.Newline)
	}
}

func TestMajorityLFWins(t *testing.T) {
	d := parse(t, "a\r\nb\nc\nd\n", Options{})
	if d.Newline != NewlineLF {
		t.Errorf("Newline = %q, want LF (majority)", d.Newline)
	}
}

// --- BOM ---

func TestUTF8BOMStripped(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello\nworld\n")...)
	d, err := Parse("t.txt", raw, int64(len(raw)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !d.HasBOM {
		t.Error("HasBOM should be true")
	}
	if d.Lines[0] != "hello" {
		t.Errorf("line 1 = %q, want hello (BOM must not leak into content)", d.Lines[0])
	}
	if bytes.Contains([]byte(d.Lines[0]), []byte{0xEF, 0xBB, 0xBF}) {
		t.Error("BOM leaked into line content")
	}
}

// 读进来再渲染回去，字节必须完全一致。
// Render round-trip：非混合文件必须字节一致。
// 混合文件必须显式传 LineEnds（见 TestMixedNewlinePreservedPerLine）。
func TestRenderRoundTrip(t *testing.T) {
	for _, s := range []string{
		"", "a", "a\n", "a\nb\n", "a\r\nb\r\n", "a\n\n", "\n",
	} {
		d := parse(t, s, Options{})
		got := string(d.Render(d.Lines))
		if got != s {
			t.Errorf("Render round-trip failed:\n  in  %q\n  got %q", s, got)
		}
	}
}

func TestRenderRoundTripWithBOM(t *testing.T) {
	raw := append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello\r\nworld\r\n")...)
	d, err := Parse("t.txt", raw, int64(len(raw)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := d.Render(d.Lines)
	if !bytes.Equal(got, raw) {
		t.Errorf("BOM round-trip failed:\n  in  %v\n  got %v", raw, got)
	}
	if !bytes.HasPrefix(got, bomUTF8) {
		t.Error("BOM not restored")
	}
}

// 混合行尾文件：未动的行必须逐行保留原行尾。
func TestMixedNewlinePreservedPerLine(t *testing.T) {
	src := "a\r\nb\nc\r\nd\n"
	d := parse(t, src, Options{})
	if !d.MixedNewline {
		t.Fatal("should be mixed")
	}
	if len(d.LineEnds) != d.LineCount() {
		t.Fatalf("LineEnds len = %d, want %d", len(d.LineEnds), d.LineCount())
	}
	want := []string{NewlineCRLF, NewlineLF, NewlineCRLF, NewlineLF}
	for i, w := range want {
		if d.LineEnds[i] != w {
			t.Errorf("LineEnds[%d] = %q, want %q", i, d.LineEnds[i], w)
		}
	}
	// 渲染时必须显式提供 ends，否则混合行尾会被规范化掉
	if got := string(d.RenderWith(d.Lines, d.LineEnds)); got != src {
		t.Errorf("round-trip:\n  in  %q\n  got %q", src, got)
	}
}

// 混合行尾文件里插入新行 → 显式传入 ends 时逐行正确。
// 注意：增删行后 LineEnds 的下标会错位，上层必须重算 ends（见 RenderWith）。
func TestMixedNewlineInsertWithExplicitEnds(t *testing.T) {
	d := parse(t, "a\r\nb\nc\r\nd\n", Options{}) // 2 CRLF vs 2 LF → CRLF 多数
	if d.Newline != NewlineCRLF {
		t.Fatalf("Newline = %q, want CRLF", d.Newline)
	}
	// 上层算好的映射：a→CRLF, NEW→CRLF(多数派), b→LF, c→CRLF, d→LF
	ends := []string{NewlineCRLF, NewlineCRLF, NewlineLF, NewlineCRLF, NewlineLF}
	edited := []string{"a", "NEW", "b", "c", "d"}
	got := string(d.RenderWith(edited, ends))
	want := "a\r\nNEW\r\nb\nc\r\nd\n"
	if got != want {
		t.Errorf("insert with explicit ends:\n  got  %q\n  want %q", got, want)
	}
}

// 不传 ends 且行数变了 → 全部用多数派行尾（至少不会错位）。
func TestMixedNewlineInsertWithoutEnds(t *testing.T) {
	d := parse(t, "a\r\nb\nc\r\nd\n", Options{})
	edited := []string{"a", "NEW", "b", "c", "d"}
	got := string(d.Render(edited))
	want := "a\r\nNEW\r\nb\r\nc\r\nd\r\n" // 全部 CRLF
	if got != want {
		t.Errorf("insert without ends:\n  got  %q\n  want %q", got, want)
	}
}

// 行数不变的原地修改也必须显式传 ends —— 靠"长度没变"猜是错的，
// 因为编辑可能删一行又加一行，总数恰好不变但下标已错位。
func TestMixedNewlineSameLengthStillNeedsEnds(t *testing.T) {
	src := "a\r\nb\nc\r\nd\n"
	d := parse(t, src, Options{})
	// 模拟"删掉 b、插入 X"：长度不变但内容换了位置
	edited := []string{"a", "X", "c", "d"}
	// 不传 ends → 全用多数派（CRLF），这正是安全回退
	got := string(d.Render(edited))
	want := "a\r\nX\r\nc\r\nd\r\n"
	if got != want {
		t.Errorf("same-length edit without ends:\n  got  %q\n  want %q", got, want)
	}
	// 显式传正确的 ends → 逐行保留
	ends := []string{NewlineCRLF, NewlineLF, NewlineCRLF, NewlineLF}
	if got := string(d.RenderWith(edited, ends)); got != "a\r\nX\nc\r\nd\n" {
		t.Errorf("same-length edit with ends:\n  got  %q", got)
	}
}

// --- 二进制探测 ---

func TestBinaryRejected(t *testing.T) {
	raw := []byte("MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xfe\x00\x00")
	e := mustFail(t, string(raw), Options{})
	if e.Code != ErrNotText {
		t.Errorf("Code = %d, want ErrNotText", e.Code)
	}
	if !strings.Contains(e.Message, "E_NOT_TEXT") {
		t.Errorf("message = %q", e.Message)
	}
	// 应报出 NUL 偏移
	if !strings.Contains(e.Message, "offset") {
		t.Errorf("message should mention offset: %q", e.Message)
	}
}

func TestNULDeepInFileStillDetected(t *testing.T) {
	raw := append(bytes.Repeat([]byte("a"), 1024), 0x00)
	e := mustFail(t, string(raw), Options{})
	if !strings.Contains(e.Message, "offset 1024") {
		t.Errorf("expected offset 1024, got %q", e.Message)
	}
}

// UTF-16 里的 NUL 是正常的，不该被误判为二进制。
func TestUTF16NotMistakenForBinary(t *testing.T) {
	// "hi\n" 的 UTF-16LE + BOM
	raw := []byte{0xFF, 0xFE, 'h', 0, 'i', 0, '\n', 0}
	d, err := Parse("t.txt", raw, int64(len(raw)), Options{})
	if err != nil {
		t.Fatalf("UTF-16 should not be treated as binary: %v", err)
	}
	if d.Encoding != EncodingUTF16LE {
		t.Errorf("Encoding = %q, want utf-16le", d.Encoding)
	}
	if d.Lines[0] != "hi" {
		t.Errorf("line 1 = %q, want hi", d.Lines[0])
	}
}

func TestUTF16BEBOM(t *testing.T) {
	raw := []byte{0xFE, 0xFF, 0, 'h', 0, 'i', 0, '\n'}
	d, err := Parse("t.txt", raw, int64(len(raw)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Encoding != EncodingUTF16BE {
		t.Errorf("Encoding = %q, want utf-16be", d.Encoding)
	}
	if d.Lines[0] != "hi" {
		t.Errorf("line 1 = %q, want hi", d.Lines[0])
	}
}

// --- 编码 ---

func TestUTF8ValidNoConversion(t *testing.T) {
	d := parse(t, "中文注释\n第二行\n", Options{})
	if d.Encoding != EncodingUTF8 {
		t.Errorf("Encoding = %q, want utf-8", d.Encoding)
	}
	if d.Lines[0] != "中文注释" {
		t.Errorf("line 1 = %q", d.Lines[0])
	}
}

// 造一个真正的 GBK 文件：中文"中文"在 GBK 下是 4 字节，UTF-8 下非法。
func TestGBKAutoDetected(t *testing.T) {
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4} // "中文"
	if utf8Valid(gbk) {
		t.Skip("test fixture is accidentally valid UTF-8")
	}
	raw := append(gbk, '\n')
	d, err := Parse("gbk.txt", raw, int64(len(raw)), Options{})
	if err != nil {
		t.Fatalf("GBK should be auto-detected: %v", err)
	}
	if d.Encoding != EncodingGB18030 {
		t.Errorf("Encoding = %q, want gb18030", d.Encoding)
	}
	if d.Lines[0] != "中文" {
		t.Errorf("line 1 = %q, want 中文", d.Lines[0])
	}
}

func TestForcedEncoding(t *testing.T) {
	// Shift-JIS 风格的字节，用 GBK 解码会得到可打印内容（不保证语义）
	raw := []byte{0x82, 0xA0, '\n'}
	d, err := Parse("sjis.txt", raw, int64(len(raw)), Options{Encoding: "gbk"})
	if err != nil {
		t.Fatalf("forced encoding failed: %v", err)
	}
	if d.Encoding != EncodingGBK {
		t.Errorf("Encoding = %q, want gbk", d.Encoding)
	}
}

func TestUnknownEncodingRejected(t *testing.T) {
	raw := []byte{0xFF, 0xFE, 'a', 0}
	_, err := Parse("t.txt", raw, int64(len(raw)), Options{Encoding: "ebcdic"})
	if err == nil {
		t.Fatal("unknown encoding should fail")
	}
	if !strings.Contains(err.Error(), "unsupported encoding") {
		t.Errorf("err = %v", err)
	}
}

// 内容是有效 UTF-8 但用户显式说 gb18030 —— 按用户说的转。
func TestForcedEncodingOnValidUTF8(t *testing.T) {
	raw := []byte("plain\n")
	d, err := Parse("t.txt", raw, int64(len(raw)), Options{Encoding: "gb18030"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Encoding != EncodingGB18030 {
		t.Errorf("Encoding = %q, want gb18030", d.Encoding)
	}
	if d.Lines[0] != "plain" {
		t.Errorf("line 1 = %q, want plain", d.Lines[0])
	}
}

func TestNormalizeEnc(t *testing.T) {
	cases := map[string]string{
		"UTF-8": EncodingUTF8, "utf8": EncodingUTF8, "U8": EncodingUTF8,
		"GB18030": EncodingGB18030, "gb_18030": EncodingGB18030,
		"GBK": EncodingGBK, "cp936": EncodingGBK, "MS936": EncodingGBK,
		"Big5": EncodingBig5, "CP950": EncodingBig5,
		"UTF-16LE": EncodingUTF16LE, "utf16le": EncodingUTF16LE,
	}
	for in, want := range cases {
		if got := normalizeEnc(in); got != want {
			t.Errorf("normalizeEnc(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- 上限 ---

func TestMaxLinesTruncates(t *testing.T) {
	d := parse(t, "1\n2\n3\n4\n5\n", Options{MaxLines: 3})
	if d.LineCount() != 3 {
		t.Errorf("LineCount = %d, want 3", d.LineCount())
	}
	if !d.Truncated {
		t.Error("Truncated should be true")
	}
	if d.Lines[2] != "3" {
		t.Errorf("last line = %q, want 3", d.Lines[2])
	}
}

func TestMaxLinesNotTruncatedWhenUnder(t *testing.T) {
	d := parse(t, "1\n2\n", Options{MaxLines: 10})
	if d.Truncated {
		t.Error("should not truncate")
	}
	if d.LineCount() != 2 {
		t.Errorf("LineCount = %d, want 2", d.LineCount())
	}
}

func TestLongLinesMarked(t *testing.T) {
	d := parse(t, "short\n"+strings.Repeat("x", 100)+"\nshort2\n",
		Options{MaxLineBytes: 10})
	if len(d.LongLines) != 1 || d.LongLines[0] != 2 {
		t.Fatalf("LongLines = %v, want [2]", d.LongLines)
	}
	if !d.HasLongLine(2) {
		t.Error("HasLongLine(2) should be true")
	}
	if d.HasLongLine(1) || d.HasLongLine(3) || d.HasLongLine(99) {
		t.Error("HasLongLine false positives")
	}
}

// --- Load（真实文件）---

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.txt"), Options{})
	e, ok := AsError(err)
	if !ok || e.Code != ErrNotFound {
		t.Fatalf("err = %v, want E_NO_FILE", err)
	}
	if !strings.Contains(e.Message, "E_NO_FILE") {
		t.Errorf("message = %q", e.Message)
	}
}

func TestLoadDirectory(t *testing.T) {
	_, err := Load(t.TempDir(), Options{})
	e, ok := AsError(err)
	if !ok || e.Code != ErrIsDir {
		t.Fatalf("err = %v, want E_IS_DIR", err)
	}
}

func TestLoadRealFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(p, []byte("function hello() {\n  x\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if d.LineCount() != 3 {
		t.Errorf("LineCount = %d, want 3", d.LineCount())
	}
	if !filepath.IsAbs(d.Path) {
		t.Errorf("Path = %q, want absolute", d.Path)
	}
	if d.SizeBytes != int64(len("function hello() {\n  x\n}\n")) {
		t.Errorf("SizeBytes = %d", d.SizeBytes)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(p, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if d.LineCount() != 0 {
		t.Errorf("LineCount = %d, want 0", d.LineCount())
	}
	if len(d.Render(d.Lines)) != 0 {
		t.Error("empty file should render to empty bytes")
	}
}

func utf8Valid(b []byte) bool {
	for _, r := range string(b) {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}

// --- RenderEncoded：写回必须保持原编码 ---
//
// 这几条锁死一个很容易被忽略、后果很严重的失败模式：
// 编辑一个 GBK 文件把它静默变成 UTF-8。

func TestRenderEncodedKeepsGBK(t *testing.T) {
	gbk, err := encode([]byte("中文号省"), EncodingGBK)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Parse("gbk.txt", gbk, int64(len(gbk)), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 探测会先试 gb18030（GBK 的超集，必然也能往返），所以编码名是 gb18030。
	// 对 GBK 范围内的字符，两者的字节完全一致 —— 往返仍然逐字节相同。
	if d.Encoding != EncodingGB18030 {
		t.Fatalf("encoding = %q, want gb18030", d.Encoding)
	}
	out, err := d.RenderEncoded(d.Lines, nil)
	if err != nil {
		t.Fatalf("RenderEncoded: %v", err)
	}
	if !bytes.Equal(out, gbk) {
		t.Fatalf("round trip changed bytes: % x vs % x", out, gbk)
	}
	if utf8Valid(out) {
		t.Fatal("output must stay invalid UTF-8 (i.e. still GBK bytes)")
	}
}

func TestRenderEncodedKeepsUTF16LEWithBOM(t *testing.T) {
	le, err := encode([]byte("hello\nworld\n"), EncodingUTF16LE)
	if err != nil {
		t.Fatal(err)
	}
	raw := append(append([]byte{}, bomUTF16LE...), le...)
	d, err := Parse("u16.txt", raw, int64(len(raw)), Options{})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := d.RenderEncoded(d.Lines, nil)
	if err != nil {
		t.Fatalf("RenderEncoded: %v", err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("utf-16 round trip changed bytes:\n got % x\nwant % x", out, raw)
	}
}

func TestRenderEncodedKeepsUTF8BOM(t *testing.T) {
	raw := append(append([]byte{}, bomUTF8...), []byte("a\nb\n")...)
	d, err := Parse("bom.txt", raw, int64(len(raw)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.RenderEncoded(d.Lines, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("utf-8 BOM round trip changed bytes: % x vs % x", out, raw)
	}
}

func TestRenderEncodedCRLFPreserved(t *testing.T) {
	raw := []byte("a\r\nb\r\n")
	d, err := Parse("crlf.txt", raw, int64(len(raw)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.RenderEncoded(d.Lines, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, raw) {
		t.Fatalf("CRLF round trip changed bytes: %q vs %q", out, raw)
	}
}

// Render（旧入口）遇到非 UTF-8 只会给 UTF-8 字节 —— 这是刻意保留的
// "纯渲染"语义，写盘必须用 RenderEncoded。这里把差异钉住，免得有人改回去。
func TestRenderIsPlainUTF8(t *testing.T) {
	gbk, _ := encode([]byte("中文"), EncodingGBK)
	d, err := Parse("gbk.txt", gbk, int64(len(gbk)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !utf8Valid(d.Render(d.Lines)) {
		t.Fatal("Render should produce UTF-8 regardless of source encoding")
	}
}
