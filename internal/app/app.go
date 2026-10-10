// Package app 是 rweg 的 CLI 层：子命令分发（工具名也是子命令，argv[0] 分发兼容保留）+ 公共运行时。
package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExitCode 是约定的退出码。
type ExitCode int

const (
	ExitOK       ExitCode = 0 // 成功
	ExitBiz      ExitCode = 1 // 业务错误（锚点失效、范围未 serve、文件不存在……）
	ExitUsage    ExitCode = 2 // 用法错误（参数不合法）
	ExitInternal ExitCode = 3 // 内部错误
)

// Error 是带退出码的错误。
//
// Details 是**逐字打印到 stderr** 的多行正文（不缩进、不加前缀），
// 用于 reject-and-serve 回传的锚点块 —— 那是给人看的载荷，不是提示。
type Error struct {
	Code    ExitCode
	Message string
	Hint    string
	Details string
}

func (e *Error) Error() string { return e.Message }

// BizErr 造一个业务错误。
func BizErr(format string, a ...any) *Error {
	return &Error{Code: ExitBiz, Message: fmt.Sprintf(format, a...)}
}

// UsageErr 造一个用法错误。
func UsageErr(format string, a ...any) *Error {
	return &Error{Code: ExitUsage, Message: fmt.Sprintf(format, a...)}
}

// WithHint 附加提示行。
func (e *Error) WithHint(h string) *Error { e.Hint = h; return e }

// WithDetails 附加多行正文。
func (e *Error) WithDetails(d string) *Error { e.Details = d; return e }

// report 把错误写到 w（Message / hint / Details 三段，固定顺序）。
func (e *Error) report(w io.Writer) {
	fmt.Fprintln(w, e.Message)
	if e.Hint != "" {
		fmt.Fprintln(w, "hint: "+e.Hint)
	}
	if e.Details != "" {
		fmt.Fprintln(w, strings.TrimRight(e.Details, "\n"))
	}
}

// Tool 是 argv[0] 分发识别出的工具名。
type Tool string

const (
	ToolCore Tool = "core-rweg"
	ToolRead Tool = "read"
	ToolWrte Tool = "write"
	ToolEdit Tool = "edit"
	ToolGrep Tool = "grep"
	ToolUndo Tool = "undo"
)

// DetectTool 从 os.Args[0] 判断当前是哪个工具。
//
// 分发规则：取 basename 去 .exe，若形如 core-rweg / core_rweg / core.rweg
// 则视为 core；read/write/edit/grep 直接匹配；其余一律当 core
// （这样 `go run ./cmd/core-rweg` 也能工作）。
func DetectTool(argv0 string) Tool {
	base := filepath.Base(argv0)
	base = strings.TrimSuffix(base, ".exe")

	low := strings.ToLower(base)
	low = strings.NewReplacer("_", "-", ".", "-").Replace(low)
	switch low {
	case "read":
		return ToolRead
	case "write":
		return ToolWrte
	case "edit":
		return ToolEdit
	case "grep":
		return ToolGrep
	case "undo", "undo-last-edit":
		return ToolUndo
	}
	return ToolCore
}

// Exit 打印错误并以对应码退出。
func Exit(err error) {
	if err == nil {
		os.Exit(int(ExitOK))
	}
	if e, ok := err.(*Error); ok {
		e.report(os.Stderr)
		os.Exit(int(e.Code))
	}
	fmt.Fprintln(os.Stderr, "rweg: "+err.Error())
	os.Exit(int(ExitInternal))
}
