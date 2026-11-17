// Package docfile 负责把磁盘文件加载成可锚定的行数组。
//
// 它处理所有"读文件会踩到的坑"：二进制探测、BOM、CRLF、编码、超长行。
// 上层（read/edit/write）只关心 Lines 与 Newline，不用再操心这些。
//
// DOC 里的 Lines 恒为文件的**全部**行：行数上限是 read 的输出约束，
// 不是加载约束——截断加载会让越界检查、锚点基线与写回都以假行数工作。
package docfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"
)

// Newline 风格。
const (
	NewlineLF   = "\n"
	NewlineCRLF = "\r\n"
	NewlineCR   = "\r" // 经典 Mac，极少见
)

// 编码名（规范化后小写）。
const (
	EncodingUTF8    = "utf-8"
	EncodingGB18030 = "gb18030"
	EncodingGBK     = "gbk"
	EncodingBig5    = "big5"
	EncodingUTF16LE = "utf-16le"
	EncodingUTF16BE = "utf-16be"
)

// Doc 是一份已解码、可锚定的文件。
type Doc struct {
	// Path 是绝对路径。
	Path string
	// Lines 是不含行尾符的行内容。**长度恒等于 LineCount**。
	Lines []string
	// Newline 是原文件的主行尾风格，写回时复用。
	Newline string
	// HasBOM 为真时原文件带 BOM，写回时必须补回。
	HasBOM bool
	// Encoding 是实际使用的编码（已转小写）。
	Encoding string
	// CRLFCount / LFCount 用于诊断混合行尾。
	CRLFCount int
	LFCount   int
	// SizeBytes 是磁盘上的原始字节数。
	SizeBytes int64
	// MixedNewline 为真表示文件里 CRLF 与 LF 混用。
	MixedNewline bool
	// LineEnds 逐行记录原行尾（仅 MixedNewline 时有值，len == LineCount）。
	// 混合行尾文件必须逐行保留，否则写回时会被悄悄规范化。
	LineEnds []string
	// TrailingNewline 记录原文件是否以行尾结尾（写回时保持）。
	TrailingNewline bool
	// LongLines 记录超出 max_line_bytes 的行号（1-based）。
	// 这些行**没有锚点**——锚点需要完整行。
	LongLines []int
}

// LineCount 返回行数。恒等于文件的真实行数——加载阶段不截断。
func (d *Doc) LineCount() int { return len(d.Lines) }

// Options 控制加载行为。
type Options struct {
	// MaxLineBytes 为 0 表示不限制；超出的行只给标记不给锚点。
	MaxLineBytes int
	// Encoding 为空表示自动探测（UTF-8 优先，失败则报错列出候选）。
	Encoding string
}

// BOM 字节序列。
var (
	bomUTF8    = []byte{0xEF, 0xBB, 0xBF}
	bomUTF16LE = []byte{0xFF, 0xFE}
	bomUTF16BE = []byte{0xFE, 0xFF}
)

// Load 读取并解析文件。
func Load(path string, opts Options) (*Doc, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &Error{Code: ErrNotFound,
				Message: fmt.Sprintf("E_NO_FILE  %s does not exist", path)}
		}
		if os.IsPermission(err) {
			return nil, &Error{Code: ErrNoPerm,
				Message: fmt.Sprintf("E_NO_PERM  %s: permission denied", path)}
		}
		return nil, &Error{Code: ErrIO, Message: fmt.Sprintf("E_IO  %s: %v", path, err)}
	}
	if fi.IsDir() {
		return nil, &Error{Code: ErrIsDir,
			Message: fmt.Sprintf("E_IS_DIR  %s is a directory", path)}
	}

	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, &Error{Code: ErrIO, Message: fmt.Sprintf("E_IO  %s: %v", path, err)}
	}

	return Parse(abs, raw, fi.Size(), opts)
}

// Parse 从内存字节解析。对测试友好，也便于 edit 时对"即将写入的内容"走同一条路径。
func Parse(absPath string, raw []byte, size int64, opts Options) (*Doc, error) {
	d := &Doc{
		Path:      absPath,
		SizeBytes: size,
		Newline:   NewlineLF,
		Encoding:  EncodingUTF8,
	}

	// --- 1. BOM 剥离 ---
	switch {
	case bytes.HasPrefix(raw, bomUTF8):
		d.HasBOM = true
		raw = raw[len(bomUTF8):]
	case bytes.HasPrefix(raw, bomUTF16LE), bytes.HasPrefix(raw, bomUTF16BE):
		d.HasBOM = true
		// UTF-16 的 BOM 必须先剥掉再解码：解码器会把它当成 U+FEFF 塞进首行。
		enc := EncodingUTF16LE
		if bytes.HasPrefix(raw, bomUTF16BE) {
			enc = EncodingUTF16BE
		}
		decoded, err := decode(raw[len(bomUTF16LE):], enc, opts.Encoding)
		if err != nil {
			return nil, err
		}
		raw = decoded
		d.Encoding = enc
	}

	// --- 2. 二进制探测 ---
	// NUL 字节是最可靠的文本/二进制分界（与 git 一致）。
	// 放在 BOM 剥离之后、编码转换之前——UTF-16 里NUL 是正常的。
	if offset, binary := findNUL(raw); binary {
		return nil, &Error{Code: ErrNotText,
			Message: fmt.Sprintf("E_NOT_TEXT  %s is binary (NUL byte at offset %d)",
				filepath.Base(absPath), offset),
			Hint: "binary files cannot be anchored; use read/write with an explicit encoding",
		}
	}

	// --- 3. 编码 ---
	if d.Encoding == EncodingUTF8 {
		if !utf8.Valid(raw) {
			enc, dec, err := negotiateEncoding(raw, opts.Encoding, absPath)
			if err != nil {
				return nil, err
			}
			raw = dec
			d.Encoding = enc
		} else if opts.Encoding != "" {
			// 用户显式指定了编码：即便 UTF-8 合法也按用户说的转
			dec, err := decode(raw, opts.Encoding, opts.Encoding)
			if err != nil {
				return nil, err
			}
			raw, d.Encoding = dec, normalizeEnc(opts.Encoding)
		}
	}

	// --- 4. 行尾与分行 ---
	lines, nl, crlf, lf, _, mixed, trailing, ends := splitLines(raw)
	d.Lines = lines
	d.Newline = nl
	d.CRLFCount = crlf
	d.LFCount = lf
	d.MixedNewline = mixed
	d.TrailingNewline = trailing
	if mixed {
		d.LineEnds = ends
	}

	// --- 5. 超长行标记 ---
	if opts.MaxLineBytes > 0 {
		for i, ln := range d.Lines {
			if len(ln) > opts.MaxLineBytes {
				d.LongLines = append(d.LongLines, i+1)
			}
		}
	}

	return d, nil
}

// HasLongLine 报告某行是否超出 max_line_bytes（1-based；越界返回 false）。
func (d *Doc) HasLongLine(line int) bool {
	for _, n := range d.LongLines {
		if n == line {
			return true
		}
	}
	return false
}

// Render 把行数组重新渲染成字节，保留 BOM 与行尾风格。
//
// 行尾策略：
//   - 非混合文件 → 全部用 Doc.Newline
//   - 混合文件 → **逐行沿用原文件的行尾**
//
// 混合文件的逐行映射由 Ends 显式给出（0-based，与 lines 等长）。
// 上层（edit）在增删行后必须重算 Ends——**不能**沿用原始 LineEnds，
// 因为插入一行会让其后所有行的下标偏移一位，行尾就会错位。
// 没有 Ends 时（纯新增内容、write 覆盖）统一用多数派行尾。
//
// ⚠️ Render 只产出 **UTF-8** 字节（含 UTF-8 BOM）。非 UTF-8 文件必须走
// RenderEncoded，否则编辑一个 GBK 文件会把它悄悄变成 UTF-8。
func (d *Doc) Render(lines []string) []byte {
	return d.RenderWith(lines, nil)
}

// RenderWith 是带显式逐行行尾的 Render。ends[i] 是第 i 行的行尾；
// 元素为空或数组长度不足时回退到 Doc.Newline。
func (d *Doc) RenderWith(lines []string, ends []string) []byte {
	body := d.renderText(lines, ends)
	if d.HasBOM && (d.Encoding == "" || d.Encoding == EncodingUTF8) {
		return append(append([]byte{}, bomUTF8...), body...)
	}
	return body
}

// RenderEncoded 渲染并按原编码编码，必要时补回对应 BOM。
//
// 这是 edit / write 该用的入口：**必须保持文件原来的编码**。
// 只认 UTF-8 的话，第一次编辑就把 GBK 项目里的文件全变成 UTF-8，
// 而且是静默的——正是这套工具要消灭的那类失败。
func (d *Doc) RenderEncoded(lines []string, ends []string) ([]byte, error) {
	body := d.renderText(lines, ends)
	encName := normalizeEnc(d.Encoding)
	if encName == "" || encName == EncodingUTF8 {
		if d.HasBOM {
			return append(append([]byte{}, bomUTF8...), body...), nil
		}
		return body, nil
	}

	enc, err := encode(body, encName)
	if err != nil {
		return nil, &Error{Code: ErrUnknownEncoding,
			Message: fmt.Sprintf("E_UNKNOWN_ENCODING  cannot write back as %s", d.Encoding),
			Hint:    "supported: utf-8, gb18030, gbk, big5, utf-16le, utf-16be",
		}
	}
	if !d.HasBOM {
		return enc, nil
	}
	switch encName {
	case EncodingUTF16LE:
		return append(append([]byte{}, bomUTF16LE...), enc...), nil
	case EncodingUTF16BE:
		return append(append([]byte{}, bomUTF16BE...), enc...), nil
	default:
		return append(append([]byte{}, bomUTF8...), enc...), nil
	}
}

// renderText 拼出 UTF-8 正文（不含 BOM）。
func (d *Doc) renderText(lines []string, ends []string) []byte {
	var b bytes.Buffer
	for i, ln := range lines {
		b.WriteString(ln)
		if i < len(lines)-1 || d.TrailingNewline {
			b.WriteString(d.endingFor(i, ends))
		}
	}
	return b.Bytes()
}

// MajorityNewline 返回多数派行尾（新增行用这个）。
func (d *Doc) MajorityNewline() string {
	if d.Newline == "" {
		return NewlineLF
	}
	return d.Newline
}

// endingFor 返回第 i 行（0-based）该用的行尾。
func (d *Doc) endingFor(i int, ends []string) string {
	// 显式传入优先（调用方已算好增删后的映射）
	if i < len(ends) && ends[i] != "" {
		return ends[i]
	}
	return d.MajorityNewline()
}

// splitLines 按行尾分行，并统计行尾风格。
//
// 规则：
//   - 内容完全为空 → 0 行（空文件）
//   - "a" → ["a"]
//   - "a\n" → ["a"]，trailing = true
//   - "a\n\n" → ["a", ""]，trailing = true
//
// 三种行尾（LF / CRLF / 裸 CR）都算分行符 —— 与旧行为一致（旧实现把 \r
// 归一成 \n 分行），但行尾**逐行记账**：纯 CR 文件（经典 Mac）现在能被认出
// NewlineCR，写回复用 CR，而不是被悄悄重排成 LF；CR 与 LF 混用也会进
// mixed 路径，逐行保真。
func splitLines(raw []byte) (lines []string, nl string, crlf, lf, cr int, mixed, trailing bool, ends []string) {
	if len(raw) == 0 {
		return []string{}, NewlineLF, 0, 0, 0, false, false, nil
	}
	s := string(raw)
	trailing = strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\r")

	// 一次扫描：既分行，也按行记录原始行尾。
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '\n':
			lines = append(lines, b.String())
			b.Reset()
			if i > 0 && raw[i-1] == '\r' {
				crlf++
				ends = append(ends, NewlineCRLF)
			} else {
				lf++
				ends = append(ends, NewlineLF)
			}
		case '\r':
			if i+1 < len(raw) && raw[i+1] == '\n' {
				continue // CRLF 的前半截，等遇到 \n 一起记账
			}
			lines = append(lines, b.String())
			b.Reset()
			cr++
			ends = append(ends, NewlineCR)
		default:
			b.WriteByte(raw[i])
		}
	}
	if !trailing {
		// 末行没有行尾：单独补上
		lines = append(lines, b.String())
	}

	kinds := 0
	if crlf > 0 {
		kinds++
	}
	if lf > 0 {
		kinds++
	}
	if cr > 0 {
		kinds++
	}
	mixed = kinds > 1
	switch {
	case crlf > 0 && crlf >= lf && crlf >= cr:
		nl = NewlineCRLF
	case cr > 0 && cr >= lf:
		nl = NewlineCR
	case lf > 0:
		nl = NewlineLF
	default:
		// 无行尾（单行无换行文件）：回落 LF
		nl = NewlineLF
	}
	return lines, nl, crlf, lf, cr, mixed, trailing, ends
}

// findNUL 在前 64KB 里找 NUL 字节。
// 只扫前64KB 是为了性能：大二进制文件不该被整读一遍才判定。
func findNUL(raw []byte) (int, bool) {
	limit := len(raw)
	if limit > 64*1024 {
		limit = 64 * 1024
	}
	for i := 0; i < limit; i++ {
		if raw[i] == 0 {
			return i, true
		}
	}
	return 0, false
}

// negotiateEncoding 在非 UTF-8 内容里挑一个最合适的编码。
// nameHint 只用于错误信息里的文件名。
//
// 只看"能无损往返"是不够的：三张 CJK 编码表的 decode/encode 各自构成双射，
// 用任何一张都能把任何一张的字节无损往返（实测 GBK 字节按 Big5 解出
// "笢恅瘍吽"、Big5 字节按 GBK 解出假名"いゅ"，往返都成立）—— 往返一致
// 只证明表闭合，不证明选对了表。所以再按解码结果的**字符分布**打分：
// 高频汉字加分，假名 / 注音 / 谚文 / 私用区这类"解读错了的典型产物"扣分，
// 分高者胜；同分按候选顺序（特指 → 泛化：Big5 → GBK → GB18030）。
func negotiateEncoding(raw []byte, forced, nameHint string) (enc string, out []byte, err error) {
	if forced != "" {
		decoded, err := decode(raw, forced, forced)
		if err != nil {
			return "", nil, err
		}
		return normalizeEnc(forced), decoded, nil
	}

	// 特指 → 泛化：GBK 是 GB18030 的子集，Big5 与两者大面积字节重叠，
	// 把更特指的排前面，同分时优先报更精确的名字。
	candidates := []string{EncodingBig5, EncodingGBK, EncodingGB18030}
	best := -1
	bestScore := 0
	for i, c := range candidates {
		decoded, err := decode(raw, c, c)
		if err != nil {
			continue
		}
		// 再编码回去必须字节一致——否则不是真的这个编码
		back, err := encode(decoded, c)
		if err != nil || !bytes.Equal(back, raw) {
			continue
		}
		if s := scoreDecoded(string(decoded)); best < 0 || s > bestScore {
			best, bestScore = i, s
		}
	}
	if best >= 0 {
		return normalizeEnc(candidates[best]), decodeMustOK(raw, candidates[best]), nil
	}

	return "", nil, &Error{
		Code: ErrEncoding,
		Message: fmt.Sprintf("E_ENCODING  %s is not valid UTF-8 and no known encoding round-trips",
			filepath.Base(nameHint)),
		Hint: fmt.Sprintf("candidates: %s\nhint: read -f <file> --encoding %s",
			strings.Join(candidates, ", "), EncodingGB18030),
	}
}

// decodeMustOK 是 negotiateEncoding 内部的二次解码：能进到这里说明同一候选
// 刚刚解码往返成功过，出错只可能是内存异常，直接 panic 让测试暴露。
func decodeMustOK(raw []byte, enc string) []byte {
	decoded, err := decode(raw, enc, enc)
	if err != nil {
		panic(fmt.Sprintf("docfile: re-decode %s failed after successful round-trip: %v", enc, err))
	}
	return decoded
}

// commonHanzi 是最高频汉字的子集（简体为主，带常用繁体），够排序用、不求全。
// 用途见 negotiateEncoding：解读对了的文本会大量命中这里，解读错了的
// 乱码大多落不到。
const commonHanzi = "的一是不了在有人我他这个们中来上大为和地到以说时要就出会可也你对生能而子那得于着下自之年过发后作里用道行所然家种事成方多经么去法学如都同现当没动面起看定天分还进好小部其些主样理心她本前开但因只从想实日军者意无力它与长把机十民第公此已工使情明性知全三又关点正业外将两高间由问很最重并物手应战向头文体政美相见被利什二等产或新己制身果加西斯月话合回特代内信表化老给世位次度门任常先海通教儿原东声提立及比员解水名真论处走义各入几口认条平系气题活尔更别打女变四神总何电数安少报才结反受目太量再感建务做接必场件计管期市直德资命山金指克许统区保至队形社便空决治展马科司五基眼书非则听白却界达光放强即像难且权思王象完设式色路记南品住告类求据程北边死张该交规万取拉格望觉术领共确传师观清今切院让识候带导争运笑飞风步改收根干造言联持组每济车亲极林服快办议往元英士证近失转夫令准布始怎呢存未远叫台单影具罗字爱击流备兵连调深商算质团集百需价花党华城石级整府离况亚请技际约示复病息究线似官火断精满支视消越器容照须九增研写称企八功包片史委乎查轻易早曾除农找装广显吧标谈吃图念六引历首医局突专费号另岁州考" +
	// 常用繁体（Big5 文件的正确解读会命中）
	"國說對們東馬車學會來後開門間長問時點頭無為兩語書體愛龍鳳藝廠豐縣礦礙確議廳農"

// scoreDecoded 给解码出的文本打"像不像正常中文"的分。
//
// 加分：常用汉字（+4）、中日韩标点与全角字符（+2）、ASCII（+1，保持原样）。
// 扣分：假名 / 注音 / 谚文（-8，用错表时的典型产物）、私用区 / 彝文（-6，
// GB18030 四字节或生僻映射的产物）、CJK 扩展 A 等生僻区（-2）、其余（-2）。
func scoreDecoded(s string) int {
	score := 0
	for _, r := range s {
		switch {
		case r < 0x80:
			score++
		case strings.ContainsRune(commonHanzi, r):
			score += 4
		case r >= 0x4E00 && r <= 0x9FFF:
			score += 1 // CJK 统一表意区，但不常见
		case (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF):
			score += 2 // 中日韩标点 / 全角形式
		case r >= 0x3040 && r <= 0x30FF:
			score -= 8 // 假名
		case r >= 0x3100 && r <= 0x312F:
			score -= 8 // 注音符号
		case r >= 0xAC00 && r <= 0xD7AF:
			score -= 8 // 谚文
		case r >= 0xE000 && r <= 0xF8FF:
			score -= 6 // 私用区
		case r >= 0xA000 && r <= 0xA4CF:
			score -= 6 // 彝文
		default:
			score -= 2
		}
	}
	return score
}

// decode 按指定编码解码到 UTF-8。
func decode(raw []byte, from, override string) ([]byte, error) {
	enc := from
	if override != "" {
		enc = override
	}
	switch normalizeEnc(enc) {
	case EncodingUTF8:
		return raw, nil
	case EncodingGB18030:
		return simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	case EncodingGBK:
		return simplifiedchinese.GBK.NewDecoder().Bytes(raw)
	case EncodingBig5:
		return traditionalchinese.Big5.NewDecoder().Bytes(raw)
	case EncodingUTF16LE:
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder().Bytes(raw)
	case EncodingUTF16BE:
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder().Bytes(raw)
	}
	return nil, fmt.Errorf("E_UNKNOWN_ENCODING  unsupported encoding %q", enc)
}

// encode 从 UTF-8 编码到目标编码。
func encode(raw []byte, to string) ([]byte, error) {
	switch normalizeEnc(to) {
	case EncodingUTF8:
		return raw, nil
	case EncodingGB18030:
		return simplifiedchinese.GB18030.NewEncoder().Bytes(raw)
	case EncodingGBK:
		return simplifiedchinese.GBK.NewEncoder().Bytes(raw)
	case EncodingBig5:
		return traditionalchinese.Big5.NewEncoder().Bytes(raw)
	case EncodingUTF16LE:
		return unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewEncoder().Bytes(raw)
	case EncodingUTF16BE:
		return unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewEncoder().Bytes(raw)
	}
	return nil, fmt.Errorf("unsupported encoding %q", to)
}

// NormalizeEncoding 把编码别名规整成规范名（供上层写回时使用）。
func NormalizeEncoding(s string) string { return normalizeEnc(s) }

func normalizeEnc(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	switch s {
	case "utf8", "utf-8", "u8":
		return EncodingUTF8
	case "gb18030", "gb-18030":
		return EncodingGB18030
	case "gbk", "cp936", "ms936", "windows-936":
		return EncodingGBK
	case "big5", "big-5", "cp950":
		return EncodingBig5
	case "utf-16le", "utf16le", "ucs-2le":
		return EncodingUTF16LE
	case "utf-16be", "utf16be":
		return EncodingUTF16BE
	}
	return s
}

// 错误码，供上层映射到退出码。
const (
	ErrIO = iota + 1
	ErrNotFound
	ErrIsDir
	ErrNoPerm
	ErrNotText
	ErrEncoding
	ErrUnknownEncoding
	ErrLargeFile
)

// Error 是带错误码的加载错误。
type Error struct {
	Code    int
	Message string
	Hint    string
}

func (e *Error) Error() string { return e.Message }

// AsError 提取 *Error。
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
