#!/usr/bin/env bash
#
# 与 shell-test.sh 的分工：
#   shell-test.sh  验"装好之后可用"（builtin 覆盖 / PATH / 退出码 / 幂等）
#   e2e-test.sh    验"功能语义对不对"（锚点稳定性 / 校验链 / 原子性 / 编码）
#
# 本脚本**自带编译**（编到临时目录），不依赖已安装的 $RWEG_HOME——
# 因为脚本本身要跑它刚编出来的二进制，避免"测的是旧版本"这种事故。
#
# 用法：
#   ./scripts/e2e-test.sh
#   ./scripts/e2e-test.sh --keep     # 保留临时目录（排障用）

set -uo pipefail

_root="$(builtin cd -P "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
builtin cd "$_root" || exit 1

_keep=0
[ "${1:-}" = "--keep" ] && _keep=1

pass=0
fail=0
ok()  { pass=$((pass+1)); printf '  ok    %s\n' "$1"; }
bad() { fail=$((fail+1)); printf '  FAIL  %s\n         %s\n' "$1" "${2:-}"; }
check() { # check <desc> <expected> <actual>
	if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "want [$2] got [$3]"; fi
}
contains() { # contains <desc> <needle> <haystack>
	case "$3" in
		*"$2"*) ok "$1" ;;
		*) bad "$1" "want substring [$2] in [$3]" ;;
	esac
}
section() { printf '\n[%s] %s\n' "$1" "$2"; }

# ── 编译一个一次性的套件 ──
_work="$(mktemp -d)"
cleanup() {
	if [ "$_keep" = "1" ]; then
		printf '\nkept: %s\n' "$_work"
	else
		rm -rf "$_work"
	fi
}
trap cleanup EXIT

_home="$_work/home"
mkdir -p "$_home/bin" "$_home/scripts" "$_home/db"

# 给**原生**程序（go、core-rweg.exe）的路径先转 Windows 形态：
# MSYS_NO_PATHCONV=1 的 shell（dsh 等 agent shell）不替参数做转换，
# /tmp/... 会变成当前盘根下的 C:\tmp\...，编译产物与 install-links 全落空。
native() {
	if command -v cygpath >/dev/null 2>&1; then
		cygpath -w "$1"
	else
		printf '%s' "$1"
	fi
}
if ! go build -trimpath -o "$(native "$_home/bin/core-rweg.exe")" ./cmd/core-rweg 2>"$_work/build.log"; then
	echo "build failed:" >&2
	cat "$_work/build.log" >&2
	exit 1
fi
"$_home/bin/core-rweg.exe" install-links "$(native "$_home/bin")" >/dev/null
cp -f "$_root/dist-home/rweg.toml" "$_home/rweg.toml"
cp -f "$_root/scripts/env.sh" "$_home/scripts/env.sh"

echo "rweg e2e test ($_root)"

# ── 沙箱与 namespace ──
# 每个场景一个沙箱目录 + 一个独立 ns：状态绝不跨场景泄漏。
_sb=""
NS=""
_ns_chars="ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
_ns_n=0
new_sandbox() { # new_sandbox [子目录...]
	_ns_n=$((_ns_n+1))
	NS="wE2E${_ns_chars:$_ns_n:1}-00001"
	_sb="$(mktemp -d "$_work/sb-XXXXXX")"
	printf '[db]\ndir = ".db/"\n' > "$_sb/rweg.toml"
	local d
	for d in "$@"; do mkdir -p "$_sb/$d"; done
}

# 在沙箱里跑命令。source env.sh 是**真实使用路径**：
# read/write/edit/grep 是 bash 内置命令，只有 env.sh 的函数能压过它们。
#
# ⚠️ 变量必须在 source **之前 export**：
# 写成 `RWEG_NS=x . env.sh` 是"给 source 加临时变量"，被 source 的脚本
# 看不到它，于是 env.sh 又自己生成了一个 ns（实测踩过，表现为隔离场景全红）。
run() { # run <沙箱> <额外 export 串（可为空）> <命令...>
	local d="$1" envs="$2"
	shift 2
	(
		builtin cd "$d" || exit 1
		if [ -n "$envs" ]; then export $envs; fi
		. "$_home/scripts/env.sh" >/dev/null 2>&1
		"$@"
	)
}

rsh() { # rsh <沙箱> <命令...> —— 用本场景的 ns
	local d="$1"
	shift
	run "$d" "RWEG_NS=$NS" "$@"
}

rsh_env() { # rsh_env <沙箱> <export 串> <命令...>
	local d="$1" envs="$2"
	shift 2
	run "$d" "$envs" "$@"
}

# 只用 exe 裸路径（不 source env.sh）—— 场景 L 验"没 source 也能用"。
raw() { # raw <沙箱> <工具名> <参数...>
	local d="$1" tool="$2"
	shift 2
	( builtin cd "$d" || exit 1; unset RWEG_NS; "$_home/bin/$tool.exe" "$@" )
}

SEP='│'   # 锚点与内容之间的分隔符（$'\u2502' 在 MSYS bash 下不展开，故写字面量）
APP_TS_LINES=6
write_app_ts() { # write_app_ts <沙箱> <相对路径>
	mkdir -p "$(dirname "$1/$2")"
	{
		printf 'function hello() {\n'
		printf '  console.log("world");\n'
		printf '}\n'
		printf 'const x = 1;\n'
		printf 'let y = 2;\n'
		printf 'export default hello;\n'
	} > "$1/$2"
}

# 取 read 输出里某一行的锚点（第 n 个内容行）。
anchor_at() { # anchor_at <n> <<< read输出
	awk -F"$SEP" -v n="$1" 'NF>1{c++; if(c==n){print $1; exit}}'
}

# ─────────────────────────────────────────────────────────────
section A "read → edit → 继续 edit（锚点跟内容走）"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts

out="$(rsh "$_sb" read -f src/app.ts 1:6 2>/dev/null)"
check "read 头部" "# src/app.ts (6 lines, lf)" "$(printf '%s\n' "$out" | sed -n 1p)"
check "6 行锚点序列（与参考实现一致）" "CTt,Um7,AU6,nVR,yAZ,6PY," \
	"$(printf '%s\n' "$out" | awk -F"$SEP" 'NF>1{print $1}' | tr '\n' ',')"

# 第 4 行替换成 3 行：plan §2.4 的实测值 → EPx / jDo / BlU，未动行锚点全保留。
rc_out="$(rsh "$_sb" edit -f src/app.ts -c nVR:nVR "$(printf 'const x = 42;\nconst z = 3;\nlet w = 4;')" 2>/dev/null)"
check "edit 行数摘要" "edited src/app.ts (6 → 8 lines, +3 -1)" "$(printf '%s\n' "$rc_out" | sed -n 1p)"
check "替换后的新锚点" "EPx,jDo,BlU," \
	"$(printf '%s\n' "$rc_out" | grep '^+' | awk -F"$SEP" '{print $1}' | sed 's/^+ //' | tr '\n' ',')"

edit_after="$(rsh "$_sb" read -f src/app.ts 1:8 2>/dev/null)"
check "未动的 5 行锚点全保留" "CTt,Um7,AU6,EPx,jDo,BlU,yAZ,6PY," \
	"$(printf '%s\n' "$edit_after" | awk -F"$SEP" 'NF>1{print $1}' | tr '\n' ',')"

# 继续 edit：yAZ 是"没动过的行"，它的锚点必须仍然可用。
rsh "$_sb" edit -f src/app.ts -c yAZ:yAZ "let y = 300;" >/dev/null 2>&1
check "位移后的旧锚点仍可 edit（rc）" "0" "$?"
check "yAZ 那行已改" "let y = 300;" "$(sed -n 7p "$_sb/src/app.ts")"

# ─────────────────────────────────────────────────────────────
section B "锚点过期 → 硬拒绝 + 回传当前锚点（reject-and-serve）"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1
# 外部工具（formatter / git checkout）改了区间内的第 2 行
sed -i 's/console\.log("world");/console.log("world!!!");/' "$_sb/src/app.ts"

err="$(rsh "$_sb" edit -f src/app.ts -c CTt:AU6 "function hi() {" 2>&1 >/dev/null)"
rcv="$(rsh "$_sb" edit -f src/app.ts -c CTt:AU6 "function hi() {" >/dev/null 2>&1; echo $?)"
check "退出码" "1" "$rcv"
contains "错误码" "E_STALE_RANGE" "$err"
contains "说明行" "lines 1-3 changed since read" "$err"
contains "回传块头" "Current range (1-3):" "$err"
contains "回传的是**当前**锚点" "nEt${SEP}  console.log(\"world!!!\");" "$err"
contains "提示先 read" "hint: read -f src/app.ts 1:3" "$err"
check "拒绝时文件未被改动" "  console.log(\"world!!!\");" "$(sed -n 2p "$_sb/src/app.ts")"

# 回传的行**不写 served** → 拿回传锚点直接重试必须再被拒
rerr="$(rsh "$_sb" edit -f src/app.ts -c nEt:nEt "x" 2>&1 >/dev/null)"
rcv="$(rsh "$_sb" edit -f src/app.ts -c nEt:nEt "x" >/dev/null 2>&1; echo $?)"
check "回传锚点不能直接重试（rc）" "1" "$rcv"
contains "回传锚点不在 served" "E_UNKNOWN_ANCHOR" "$rerr"

# 先 read 建立新基线，再用新锚点重试 → 成功
rsh "$_sb" read -f src/app.ts 1:3 >/dev/null 2>&1
new1="$(rsh "$_sb" read -f src/app.ts 1:3 2>/dev/null | anchor_at 1)"
new3="$(rsh "$_sb" read -f src/app.ts 1:3 2>/dev/null | anchor_at 3)"
rsh "$_sb" edit -f src/app.ts -c "$new1:$new3" "function hi() {" >/dev/null 2>&1
check "read 之后重试成功（rc）" "0" "$?"
check "第 1 行已改" "function hi() {" "$(sed -n 1p "$_sb/src/app.ts")"

# ─────────────────────────────────────────────────────────────
section C "文件从未读过 → E_NEVER_READ"
# ─────────────────────────────────────────────────────────────
new_sandbox src
printf 'a\nb\n' > "$_sb/src/never.ts"
err="$(rsh "$_sb" edit -f src/never.ts -c CTt:CTt "x" 2>&1 >/dev/null)"
rcv="$(rsh "$_sb" edit -f src/never.ts -c CTt:CTt "x" >/dev/null 2>&1; echo $?)"
check "退出码" "1" "$rcv"
contains "错误码" "E_NEVER_READ" "$err"
contains "提示" "hint: read -f src/never.ts first" "$err"

# ─────────────────────────────────────────────────────────────
section D "锚点不存在 / 形状非法"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1
err="$(rsh "$_sb" edit -f src/app.ts -c ZZZ:ZZZ "x" 2>&1 >/dev/null)"
contains "不存在的锚点" "E_UNKNOWN_ANCHOR" "$err"
err="$(rsh "$_sb" edit -f src/app.ts -c CTt:QQQ "x" 2>&1 >/dev/null)"
contains "TO 侧不存在" "QQQ not in served anchors" "$err"
rcv="$(rsh "$_sb" edit -f src/app.ts -c ZZ:ZZ "x" >/dev/null 2>&1; echo $?)"
check "形状非法 → 用法错误(2)" "2" "$rcv"
# 省略 `:TO` 等价于单行替换（FROM:FROM）
rsh "$_sb" edit -f src/app.ts -c CTt "x" >/dev/null 2>&1
check "省略 :TO 等价于单行（rc）" "0" "$?"
check "省略 :TO 改的是那一行" "x" "$(sed -n 1p "$_sb/src/app.ts")"

# ─────────────────────────────────────────────────────────────
section E "多 -c 原子性"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1
cp "$_sb/src/app.ts" "$_work/before.ts"

rcv="$(rsh "$_sb" edit -f src/app.ts -c CTt:CTt "function hi() {" -c ZZ0:ZZ0 "boom" >/dev/null 2>&1; echo $?)"
check "含非法 -c 时整次失败" "1" "$rcv"
if cmp -s "$_work/before.ts" "$_sb/src/app.ts"; then
	ok "第一个 -c 未被执行（文件字节不变）"
else
	bad "第一个 -c 未被执行（文件字节不变）" "file changed"
fi

rsh "$_sb" edit -f src/app.ts -c CTt:CTt "function hi() {" -c yAZ:yAZ "let y = 0;" >/dev/null 2>&1
check "两个都合法 → 一起生效（rc）" "0" "$?"
check "第 1 行" "function hi() {" "$(sed -n 1p "$_sb/src/app.ts")"
check "第 5 行" "let y = 0;" "$(sed -n 5p "$_sb/src/app.ts")"
check "行数不变" "6" "$(wc -l < "$_sb/src/app.ts" | tr -d ' ')"

# 删除：替换文本为 "" 表示删掉该区间
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1
v1="$(rsh "$_sb" read -f src/app.ts 1:2 2>/dev/null | anchor_at 1)"
v2="$(rsh "$_sb" read -f src/app.ts 1:2 2>/dev/null | anchor_at 2)"
rsh "$_sb" edit -f src/app.ts -c "$v1:$v2" "" >/dev/null 2>&1
check "区间删除（rc）" "0" "$?"
check "删后行数 6 → 4" "4" "$(wc -l < "$_sb/src/app.ts" | tr -d ' ')"

# ─────────────────────────────────────────────────────────────
section F "write：创建 / 覆盖 / 自动 serve"
# ─────────────────────────────────────────────────────────────
new_sandbox src
out="$(rsh "$_sb" write -f src/new.ts -c "$(printf 'export const a = 1;\nexport const b = 2;')" 2>/dev/null)"
check "创建摘要" "created src/new.ts (2 lines)" "$(printf '%s\n' "$out" | sed -n 1p)"
check "创建时给的锚点" "Wb0,2Os," "$(printf '%s\n' "$out" | tail -n +2 | awk -F"$SEP" '{print $1}' | tr '\n' ',')"
check "文件已落盘" "2" "$(wc -l < "$_sb/src/new.ts" | tr -d ' ')"

rcv="$(rsh "$_sb" write -f src/nodir/x.ts -c "x" >/dev/null 2>&1; echo $?)"
err="$(rsh "$_sb" write -f src/nodir/x.ts -c "x" 2>&1 >/dev/null)"
check "目录不存在 → 1" "1" "$rcv"
contains "E_NO_DIR" "E_NO_DIR" "$err"

out="$(rsh "$_sb" write -f src/new.ts -c "export const a = 1;" 2>/dev/null)"
check "覆盖摘要" "replaced src/new.ts (1 line)" "$(printf '%s\n' "$out" | sed -n 1p)"
first="$(printf '%s\n' "$out" | tail -n +2 | awk -F"$SEP" '{print $1}')"
rsh "$_sb" edit -f src/new.ts -c "$first:$first" "export const a = 42;" >/dev/null 2>&1
check "write 后无需 read 即可 edit（rc）" "0" "$?"
check "内容已改" "export const a = 42;" "$(sed -n 1p "$_sb/src/new.ts")"

rsh "$_sb" write -f src/empty.ts -c "" >/dev/null 2>&1
check "写空文件（rc）" "0" "$?"
check "空文件大小" "0" "$(wc -c < "$_sb/src/empty.ts" | tr -d ' ')"

printf 'l1\nl2\n' | ( builtin cd "$_sb" && RWEG_NS="$NS" . "$_home/scripts/env.sh" >/dev/null 2>&1 && write -f src/pipe.ts ) >/dev/null 2>&1
check "从 stdin 写（rc）" "0" "$?"
check "stdin 内容行数" "2" "$(wc -l < "$_sb/src/pipe.ts" | tr -d ' ')"

rsh "$_sb" write -f src/crlf.ts -c "a
b" >/dev/null 2>&1
printf 'a\r\nb\r\n' > "$_sb/src/host.ts"
rsh "$_sb" write -f src/host.ts -c "x
y" >/dev/null 2>&1
check "覆盖已有 CRLF 文件仍保持 CRLF" "2" "$(grep -c $'\r$' "$_sb/src/host.ts")"

# ─────────────────────────────────────────────────────────────
section G "read 多范围 / 范围形态 / serve 集合"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
blk() { # blk <期望块头> <范围...>
	local want="$1"
	shift
	local got
	got="$(rsh "$_sb" read -f src/app.ts "$@" 2>/dev/null | sed -n 2p)"
	check "read $*" "$want" "$got"
}
blk "4:6:" "4-"
blk "1:3:" "-3"
blk "3:3:" "3"
blk "1:4:" "1:3" "2:4"
blk "1:6:" "1:3" "4:6"
blk "1:3:" "5:6" "1:3"
check "不相交范围分两块" "2" \
	"$(rsh "$_sb" read -f src/app.ts "1:3" "5:6" 2>/dev/null | grep -c '^[0-9]*:[0-9]*:$')"

new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:3 5:6 >/dev/null 2>&1
# 第 4 行没被 serve → 它的锚点不能用于 edit（fail-closed）
err="$(rsh "$_sb" edit -f src/app.ts -c nVR:nVR "const x = 9;" 2>&1 >/dev/null)"
contains "未 serve 的行不能编辑" "E_UNKNOWN_ANCHOR" "$err"
# 已 serve 的两段都可以
rsh "$_sb" edit -f src/app.ts -c yAZ:yAZ "let y = 9;" >/dev/null 2>&1
check "已 serve 的第 5 行可以编辑（rc）" "0" "$?"

# --json
new_sandbox src
write_app_ts "$_sb" src/app.ts
js="$(rsh "$_sb" read -f src/app.ts 1:2 --json 2>/dev/null)"
contains "json 有 blocks" '"blocks"' "$js"
contains "json 有锚点数组" '"CTt"' "$js"
contains "json 有 line_count" '"line_count": 6' "$js"

# ─────────────────────────────────────────────────────────────
section H "grep：锚点化 / 三条回退路径"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
printf '// says hello\nconst z = 9;\n' > "$_sb/src/other.ts"

out="$(rsh "$_sb" grep -n hello src/ 2>/dev/null)"
contains "块头（命中数）" "# src/app.ts (2 matches)" "$out"
contains "行号 + 锚点 + 内容" "1:CTt${SEP}function hello() {" "$out"
contains "第二个命中" "6:6PY${SEP}export default hello;" "$out"

# grep 出来的锚点可直接 edit
rsh "$_sb" edit -f src/app.ts -c 6PY:6PY "export default hello as greet;" >/dev/null 2>&1
check "grep 锚点直接 edit（rc）" "0" "$?"
check "文件已改" "export default hello as greet;" "$(sed -n 6p "$_sb/src/app.ts")"

# 管道输入 → 完全透传
out="$(cat "$_sb/src/app.ts" | (builtin  cd "$_sb" && RWEG_NS="$NS" . "$_home/scripts/env.sh" >/dev/null 2>&1 && grep hello ) 2>/dev/null)"
check "管道输入透传（原始行为）" "function hello() {
export default hello as greet;" "$out"

# rg 不可用 → 回退系统 grep
out="$(RWEG_RG_MISSING=1 rsh "$_sb" grep -n hello src/app.ts 2>/dev/null)"
check "rg 缺失 → 系统 grep 输出" "1:function hello() {" "$(printf '%s\n' "$out" | sed -n 1p)"

# rg 报错 → 透传（退出码一并给）
rcv="$(rsh "$_sb" grep --bad-flag foo src/app.ts >/dev/null 2>&1; echo $?)"
check "rg 报错 → 退出码透传（2）" "2" "$rcv"

# -c 计数
check "-c 单文件计数" "2" "$(rsh "$_sb" grep -c hello src/app.ts 2>/dev/null)"
check "-c 多文件计数" "src/other.ts:1" "$(rsh "$_sb" grep -c hello src/ 2>/dev/null | sed -n 1p)"
rcv="$(rsh "$_sb" grep -n zzzznotfound src/ >/dev/null 2>&1; echo $?)"
check "无匹配 → 1" "1" "$rcv"
check "-C1 上下文行也带上锚点" "3" "$(rsh "$_sb" grep -C1 world src/app.ts 2>/dev/null | grep -c "$SEP")"

# ─────────────────────────────────────────────────────────────
section I "编码 / 行尾 / BOM（read 与写回）"
# ─────────────────────────────────────────────────────────────
new_sandbox

# CRLF + UTF-8 BOM
printf '\xef\xbb\xbfhello\r\nworld\r\n' > "$_sb/win.txt"
out="$(rsh "$_sb" read -f win.txt 2>/dev/null)"
check "头部同时标出 crlf 与 bom" "# win.txt (2 lines, crlf, bom)" "$(printf '%s\n' "$out" | sed -n 1p)"
check "BOM 不混进内容" "hello" "$(printf '%s\n' "$out" | sed -n 3p | awk -F"$SEP" '{print $2}')"
rsh "$_sb" read -f win.txt >/dev/null 2>&1
a="$(rsh "$_sb" read -f win.txt 2>/dev/null | anchor_at 1)"
rsh "$_sb" edit -f win.txt -c "$a:$a" "HELLO" >/dev/null 2>&1
check "编辑 CRLF+BOM 文件（rc）" "0" "$?"
if cmp -s <(printf '\xef\xbb\xbfHELLO\r\nworld\r\n') "$_sb/win.txt"; then
	ok "写回保持 CRLF + BOM（逐字节）"
else
	bad "写回保持 CRLF + BOM（逐字节）" "$(od -c "$_sb/win.txt" | head -2)"
fi

# 混合行尾：**新增行用多数派**，被保留的行逐行沿用原行尾。
new_sandbox
printf 'a\r\nb\nc\r\n' > "$_sb/mixed.txt"
out="$(rsh "$_sb" read -f mixed.txt 2>/dev/null)"
check "混合行尾标记" "# mixed.txt (3 lines, crlf, mixed-eol)" "$(printf '%s\n' "$out" | sed -n 1p)"
rsh "$_sb" read -f mixed.txt >/dev/null 2>&1
a1="$(rsh "$_sb" read -f mixed.txt 2>/dev/null | anchor_at 1)"
# 第 1 行（CRLF）替换成 2 行 → 新行用多数派 CRLF；
# 第 2 行（LF）和第 3 行（CRLF）必须**各自保持原样**（这正是 P2 修掉的行尾错位 bug）。
rsh "$_sb" edit -f mixed.txt -c "$a1:$a1" "$(printf 'a1\na2')" >/dev/null 2>&1
if cmp -s <(printf 'a1\r\na2\r\nb\nc\r\n') "$_sb/mixed.txt"; then
	ok "混合行尾：新增行用多数派、保留行不错位"
else
	bad "混合行尾：新增行用多数派、保留行不错位" "$(od -c "$_sb/mixed.txt" | head -3)"
fi

# 二进制
printf 'a\x00b\n' > "$_sb/bin.dat"
err="$(rsh "$_sb" read -f bin.dat 2>&1 >/dev/null)"
rcv="$(rsh "$_sb" read -f bin.dat >/dev/null 2>&1; echo $?)"
check "二进制 → 1" "1" "$rcv"
contains "报出 NUL 偏移" "NUL byte at offset 1" "$err"

# GBK：自动探测 + 写回仍是 GBK 字节
#
# ⚠️ 替换文本必须是**合法 UTF-8**（命令行参数就是 UTF-8）：
# 直接塞原始 GBK 字节进去属于未定义输入。工具负责把 UTF-8 文本
# 按原文件编码写回。
printf '\xd6\xd0\xce\xc4\xba\xc5\xca\xa1\n' > "$_sb/gbk.txt"   # 中文号省
out="$(rsh "$_sb" read -f gbk.txt 2>/dev/null)"
check "GBK 自动探测（gb18030）" "# gbk.txt (1 line, lf, gb18030)" "$(printf '%s\n' "$out" | sed -n 1p)"
check "GBK 内容解码正确" "中文号省" "$(printf '%s\n' "$out" | sed -n 3p | awk -F"$SEP" '{print $2}')"
a3="$(printf '%s\n' "$out" | anchor_at 1)"
rsh "$_sb" edit -f gbk.txt -c "$a3:$a3" "中文号省二号" >/dev/null 2>&1
check "编辑 GBK 文件（rc）" "0" "$?"
# 中文号省二号 的 GBK 字节
if cmp -s <(printf '\xd6\xd0\xce\xc4\xba\xc5\xca\xa1\xb6\xfe\xba\xc5\n') "$_sb/gbk.txt"; then
	ok "写回仍是 GBK 字节（未静默转成 UTF-8）"
else
	bad "写回仍是 GBK 字节（未静默转成 UTF-8）" "$(od -An -tx1 "$_sb/gbk.txt")"
fi
check "改完还能读回来" "中文号省二号" "$(rsh "$_sb" read -f gbk.txt 2>/dev/null | sed -n 3p | awk -F"$SEP" '{print $2}')"

# 强制指定编码
out="$(rsh "$_sb" read -f gbk.txt --encoding gb18030 2>/dev/null)"
check "强制编码" "# gbk.txt (1 line, lf, gb18030)" "$(printf '%s\n' "$out" | sed -n 1p)"

# ─────────────────────────────────────────────────────────────
section J "namespace 隔离（同项目双终端）"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
NS_A="wE2Ezy-00001" # 终端 A：read 过
NS_B="wE2Ezx-00001" # 终端 B：从没读过
NS_C="wE2Ezw-00001" # 终端 C：read 过（用来证明 B 的拒不影响别人）

# 注意取 `tail -1`：read 的第一行是 `# 路径 (6 lines, lf)` 头部。
out="$(rsh_env "$_sb" "RWEG_NS=$NS_A" read -f src/app.ts 1:1 2>/dev/null | tail -1)"
check "终端 A 读到锚点" "CTt${SEP}function hello() {" "$out"
out="$(rsh_env "$_sb" "RWEG_NS=$NS_C" read -f src/app.ts 1:1 2>/dev/null | tail -1)"
check "终端 C 读到**相同**锚点（内容寻址）" "CTt${SEP}function hello() {" "$out"

# B 从没 read 过 → 用 A 的锚点编辑被拒（ns 隔离 served，不隔离锚点本身）
err="$(rsh_env "$_sb" "RWEG_NS=$NS_B" edit -f src/app.ts -c CTt:CTt "x" 2>&1 >/dev/null)"
rcv="$(rsh_env "$_sb" "RWEG_NS=$NS_B" edit -f src/app.ts -c CTt:CTt "x" >/dev/null 2>&1; echo $?)"
check "B 用 A 的锚点 → 退出码 1" "1" "$rcv"
contains "B 用 A 的锚点 → E_NEVER_READ" "E_NEVER_READ" "$err"
check "B 的拒绝没有动文件" "function hello() {" "$(sed -n 1p "$_sb/src/app.ts")"

# A 的 served 状态没被 B 影响，锚点仍然有效
rsh_env "$_sb" "RWEG_NS=$NS_A" edit -f src/app.ts -c CTt:CTt "function hi() {" >/dev/null 2>&1
check "A 的锚点仍有效（rc）" "0" "$?"
check "A 改成功" "function hi() {" "$(sed -n 1p "$_sb/src/app.ts")"

# ─────────────────────────────────────────────────────────────
section L "没有 source env.sh（ns 降级）"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
out="$(raw "$_sb" read -f src/app.ts 1:1 2>/dev/null | tail -1)"
check "裸调用 read 仍可用" "CTt${SEP}function hello() {" "$out"
err="$(raw "$_sb" read -f src/app.ts 1:1 2>&1 >/dev/null)"
contains "stderr 有降级警告" "RWEG_NS unset" "$err"
check "降级 ns 是确定性的（read→edit 链路通）" "0" \
	"$( raw "$_sb" read -f src/app.ts 1:1 >/dev/null 2>&1
	    raw "$_sb" edit -f src/app.ts -c CTt:CTt "function hi() {" >/dev/null 2>&1
	    echo $? )"

# ─────────────────────────────────────────────────────────────
section M "配置查找与诊断"
# ─────────────────────────────────────────────────────────────
new_sandbox src
mkdir -p "$_sb/deep/a/b" "$_sb/sub"
cfg="$(rsh "$_sb" core-rweg cfg 2>/dev/null)"
# env.sh 在 source 时把生效配置钉进 $RWEG_PIN_CONFIG；不钉（手动 unset）时
# 才回到"按 cwd 现找"的裸行为，两条都验。
contains "config 来源是钉住的项目配置" "\$RWEG_PIN_CONFIG" "$cfg"
cfg_nopin="$(rsh "$_sb" bash -c 'unset RWEG_PIN_CONFIG; core-rweg cfg' 2>/dev/null)"
contains "不钉时 config 来源是 cwd" "[cfg] config  cwd " "$cfg_nopin"
contains "dbdir 是 .db" "[cfg] dbdir   " "$cfg"
check "db 文件名 = <ns>.db" "yes" \
	"$(printf '%s\n' "$cfg" | sed -n 's/^\[cfg\] db  *//p' | grep -qE '\.db$' && echo yes || echo no)"
contains "walkup 显示 cwd only" "0 (cwd only)" "$cfg"

# 下一级子目录 + RWEG_WALKUP=1 → 命中 cwd 的上一级配置
cfg="$(rsh_env "$_sb/sub" "RWEG_NS=$NS RWEG_WALKUP=1" core-rweg cfg 2>/dev/null)"
contains "walkup=1 钉住的是上一级配置" "\$RWEG_PIN_CONFIG" "$cfg"
cfg1_raw="$(rsh_env "$_sb/sub" "RWEG_NS=$NS RWEG_WALKUP=1" bash -c 'unset RWEG_PIN_CONFIG; core-rweg cfg' 2>/dev/null)"
contains "不钉时 walkup=1 命中上一级配置" "RWEG_WALKUP+1" "$cfg1_raw"

cfg="$(rsh "$_sb/deep/a/b" bash -c 'unset RWEG_PIN_CONFIG; core-rweg cfg' 2>/dev/null)"
contains "不钉时深层子目录 walkup=0 回落安装级" "\$RWEG_HOME" "$cfg"

# 深层目录（4 层）用 walkup=3 命中项目根配置；
# 且 db 锚在**配置目录**下，不随 cwd 漂。
deepcfg="$(rsh_env "$_sb/deep/a/b" "RWEG_NS=$NS RWEG_WALKUP=3" core-rweg cfg 2>/dev/null)"
contains "walkup=3 钉住项目配置" "\$RWEG_PIN_CONFIG" \
	"$(printf '%s\n' "$deepcfg" | sed -n 's/^\[cfg\] config  *//p')"
deep_raw="$(rsh_env "$_sb/deep/a/b" "RWEG_NS=$NS RWEG_WALKUP=3" bash -c 'unset RWEG_PIN_CONFIG; core-rweg cfg' 2>/dev/null)"
contains "不钉时 walkup=3 命中项目配置" "RWEG_WALKUP+3" \
	"$(printf '%s\n' "$deep_raw" | sed -n 's/^\[cfg\] config  *//p')"
# basedir / db 都是 Windows 形式（C:\...\Temp\...），不能用 $TMPDIR 的 /tmp 形式比，
# 所以拿 cfg 自己报的 basedir 做前缀比较。
base="$(printf '%s\n' "$deepcfg" | sed -n 's/^\[cfg\] basedir *//p')"
dbpath="$(printf '%s\n' "$deepcfg" | sed -n 's/^\[cfg\] db  *//p')"
case "$base" in
	*deep*) bad "basedir 应是配置所在目录（不是 cwd）" "base=[$base]" ;;
	*) ok "basedir 是配置所在目录（不是 cwd）" ;;
esac
case "$dbpath" in
	"$base"/*|"$base"\\*) ok "db 落在项目配置目录下（4 层深也不漂）" ;;
	*) bad "db 落在项目配置目录下（4 层深也不漂）" "db=[$dbpath] base=[$base]" ;;
esac

rcv="$(RWEG_CONFIG="$_sb/nope.toml" rsh "$_sb" core-rweg cfg >/dev/null 2>&1; echo $?)"
check "RWEG_CONFIG 指向不存在 → 2" "2" "$rcv"

doc="$(rsh "$_sb" core-rweg doctor 2>&1)"
contains "doctor 无 FAIL" "doctor:" "$doc"
case "$doc" in
	*FAIL*) bad "doctor 无 FAIL" "$(printf '%s\n' "$doc" | grep FAIL)" ;;
	*) ok "doctor 无 FAIL" ;;
esac
contains "doctor 列出 cwd 行" "cwd" "$doc"
contains "doctor 列出 walkup 行" "walkup" "$doc"

# ─────────────────────────────────────────────────────────────
section N "TTL / GC"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1
out="$(rsh "$_sb" core-rweg gc 2>/dev/null | sed -n 1p)"
contains "gc 幂等提示或统计" "GC:" "$out"
out="$(rsh "$_sb" core-rweg gc --force 2>/dev/null | sed -n 1p)"
contains "gc --force 给出统计" "GC deleted:" "$out"

# ─────────────────────────────────────────────────────────────
section Z "其它契约"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
rcv="$(rsh "$_sb" read >/dev/null 2>&1; echo $?)"
check "read 缺 -f → 2" "2" "$rcv"
rcv="$(rsh "$_sb" read -f src/app.ts 999 >/dev/null 2>&1; echo $?)"
check "read 越界 → 2" "2" "$rcv"
rcv="$(rsh "$_sb" edit -f src/app.ts >/dev/null 2>&1; echo $?)"
check "edit 缺 -c → 2" "2" "$rcv"
rcv="$(rsh "$_sb" write >/dev/null 2>&1; echo $?)"
check "write 缺 -f → 2" "2" "$rcv"
js="$(rsh "$_sb" write -f src/j.ts -c "a" --json 2>/dev/null)"
contains "write --json" '"created": true' "$js"
# -c 的内容长得像 flag 时不能被吞掉
rsh "$_sb" write -f src/flag.ts -c "keep -v here" >/dev/null 2>&1
check "-c 内容里的 -v 不被当 flag" "keep -v here" "$(sed -n 1p "$_sb/src/flag.ts")"

# ─────────────────────────────────────────────────────────────
section H "-h 帮助与 prompt"
# ─────────────────────────────────────────────────────────────
new_sandbox
contains "write -h" "write [FILE] [CONTENT]" "$(rsh "$_sb" write -h 2>&1 | sed -n 1p)"
contains "edit -h" "edit [FILE] FROM" "$(rsh "$_sb" edit -h 2>&1 | sed -n 1p)"
# grep 必须打自己的帮助：`-h` 若漏给 rg，用户看到的是 ripgrep 的手册。
contains "grep -h 出 rweg 帮助" "grep [rg 的参数]" "$(rsh "$_sb" grep -h 2>&1 | sed -n 1p)"
contains "grep --help 同 -h" "grep [rg 的参数]" "$(rsh "$_sb" grep --help 2>&1 | sed -n 1p)"
contains "core-rweg -h" "core-rweg —" "$(rsh "$_sb" core-rweg -h 2>&1 | sed -n 1p)"
contains "core-rweg cfg -h" "core-rweg cfg" "$(rsh "$_sb" core-rweg cfg -h 2>&1 | sed -n 1p)"
contains "core-rweg ns -h" "core-rweg ns" "$(rsh "$_sb" core-rweg ns -h 2>&1 | sed -n 1p)"
contains "core-rweg gc -h" "core-rweg gc" "$(rsh "$_sb" core-rweg gc -h 2>&1 | sed -n 1p)"
contains "undo -h" "undo [FILE]" "$(rsh "$_sb" undo -h 2>&1 | sed -n 1p)"
contains "core-rweg install-links -h" "core-rweg install-links" \
	"$(rsh "$_sb" core-rweg install-links -h 2>&1 | sed -n 1p)"
check "install-links -h 不建 -h 目录" "no" "$([ -e "$_sb/-h" ] && echo yes || echo no)"
rcv="$(rsh "$_sb" core-rweg install-links -x >/dev/null 2>&1; echo $?)"
check "install-links 拒绝 - 开头的目录 → 2" "2" "$rcv"
prompt="$(rsh "$_sb" core-rweg prompt 2>/dev/null)"
contains "prompt 含 read 用法" "read  FILE [RANGE ...]" "$prompt"
contains "prompt 含 edit 用法" 'edit  FILE FROM:TO' "$prompt"
contains "prompt 含 undo" "undo  [FILE]" "$prompt"
contains "prompt 含真换行要求" "真换行" "$prompt"
contains "prompt 含 diff 形状" '"+ "' "$prompt"
contains "prompt 含锚点失效处置" "E_STALE_RANGE" "$prompt"
contains "prompt 含 builtin read 旁路" "builtin read" "$prompt"
contains "prompt -h 是帮助" "core-rweg prompt" "$(rsh "$_sb" core-rweg prompt -h 2>&1 | sed -n 1p)"

# ─────────────────────────────────────────────────────────────
section U "undo：退回一步"
# ─────────────────────────────────────────────────────────────
new_sandbox
write_app_ts "$_sb" u/app.ts
rsh "$_sb" read -f u/app.ts 3:3 >/dev/null 2>&1
anchor3="$(rsh "$_sb" read -f u/app.ts 3:3 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
rsh "$_sb" edit -f u/app.ts -c "$anchor3" CHANGED >/dev/null 2>&1
check "edit 已生效" "CHANGED" "$(sed -n 3p "$_sb/u/app.ts")"
contains "undo 回一行结果" "undone edit" "$(rsh "$_sb" undo u/app.ts 2>/dev/null)"
check "undo 恢复原内容" "}" "$(sed -n 3p "$_sb/u/app.ts")"
check "undo 后旧锚点重新有效" "0" \
    "$(rsh "$_sb" edit -f u/app.ts -c "$anchor3" AGAIN >/dev/null 2>&1; echo $?)"
check "再 undo 退一层" "}" "$(rsh "$_sb" undo u/app.ts >/dev/null 2>&1; sed -n 3p "$_sb/u/app.ts")"
# write 新建的文件：undo 就是删掉它
rsh "$_sb" write -f u/new.ts -c temp >/dev/null 2>&1
check "write 建出的文件在" "yes" "$([ -f "$_sb/u/new.ts" ] && echo yes || echo no)"
contains "undo write 说明文件被删" "file removed" "$(rsh "$_sb" undo u/new.ts 2>/dev/null)"
check "undo 删掉了 write 新建的文件" "no" "$([ -f "$_sb/u/new.ts" ] && echo yes || echo no)"
# 不带路径 = 最近一次改动，不限文件
rsh "$_sb" write -f u/latest.ts -c x >/dev/null 2>&1
rsh "$_sb" undo >/dev/null 2>&1
check "undo 不带路径回退最近一次" "no" "$([ -f "$_sb/u/latest.ts" ] && echo yes || echo no)"
# 空栈与别名
check "没有记录可撤销 → 1" "1" "$(rsh "$_sb" undo u/app.ts >/dev/null 2>&1; echo $?)"
contains "空栈报 E_NO_UNDO" "E_NO_UNDO" "$(rsh "$_sb" undo u/app.ts 2>&1)"
contains "undo_last_edit 别名可用" "E_NO_UNDO" "$(rsh "$_sb" undo_last_edit u/app.ts 2>&1)"
contains "undo 不支持 --json" "not supported" "$(rsh "$_sb" undo --json 2>&1)"
# 超过 4 MiB 的前像不留副本：明确报 E_UNDO_TOO_BIG
yes "0123456789012345678901234567890123456789" | head -c 4300000 > "$_sb/u/big.txt"
check "覆盖 4.3 MB 已有文件" "0" \
    "$(rsh "$_sb" write -f u/big.txt < /dev/null >/dev/null 2>&1; echo $?)"
contains "大前像报 E_UNDO_TOO_BIG" "E_UNDO_TOO_BIG" "$(rsh "$_sb" undo u/big.txt 2>&1)"
rm -f "$_sb/u/big.txt"

# ─────────────────────────────────────────────────────────────
section D "edit 的差异形态（git 风格）"
# ─────────────────────────────────────────────────────────────
new_sandbox
printf 'a\nb\nc\nd\n' > "$_sb/d.txt"
rsh "$_sb" read -f d.txt >/dev/null 2>&1
d1="$(rsh "$_sb" read -f d.txt 2:2 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
out="$(rsh "$_sb" edit -f d.txt -c "$d1" B 2>/dev/null)"
contains "头部计数 +1 -1" "(4 → 4 lines, +1 -1)" "$(printf '%s\n' "$out" | sed -n 1p)"
contains "删除行带 - 前缀与旧锚点" "- ${d1}${SEP}b" "$out"
contains "新增行带 + 前缀" "+ " "$out"
contains "上下文行带两个空格前缀" "  " "$out"
# 只增不减时不出现 -N
printf 'x\ny\n' > "$_sb/ap.txt"
rsh "$_sb" read -f ap.txt >/dev/null 2>&1
ap1="$(rsh "$_sb" read -f ap.txt 1:1 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
out="$(rsh "$_sb" edit -f ap.txt -c "$ap1" $'x\nz' 2>/dev/null)"
contains "只增不减时不出 -N" "(2 → 3 lines, +1)" "$(printf '%s\n' "$out" | sed -n 1p)"
contains "没删任何行" "+ " "$out"
# 相距较远的块之间用 ... 省略，且只省略一次
seq 1 20 | sed 's/^/line /' > "$_sb/far.txt"
rsh "$_sb" read -f far.txt >/dev/null 2>&1
f2="$(rsh "$_sb" read -f far.txt 2:2 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
f18="$(rsh "$_sb" read -f far.txt 18:18 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
out="$(rsh "$_sb" edit -f far.txt -c "$f2" L2 -c "$f18" L18 2>/dev/null)"
contains "远距离块头部计数 +2 -2" "(20 → 20 lines, +2 -2)" "$(printf '%s\n' "$out" | sed -n 1p)"
check "省略行只出现一次" "1" "$(printf '%s\n' "$out" | grep -c '^\.\.\.$')"
contains "未改动的行不重复打印" "line 1" "$out"

# ─────────────────────────────────────────────────────────────
section F "简写形态（FILE 提到最前）"
# ─────────────────────────────────────────────────────────────
new_sandbox
printf 'a\nb\nc\n' > "$_sb/s.txt"
check "read FILE 读全文" "1:3:" "$(rsh "$_sb" read s.txt 2>/dev/null | sed -n 2p)"
contains "read FILE 带锚点" "$SEP" "$(rsh "$_sb" read s.txt 2>/dev/null | tail -1)"
check "read FILE RANGE" "2:2:" "$(rsh "$_sb" read s.txt 2 2>/dev/null | sed -n 2p)"
check "read FILE 多范围" "1:3:" "$(rsh "$_sb" read s.txt 1 2:3 2>/dev/null | sed -n 2p)"
check "read -f 旧写法仍可用" "2:2:" "$(rsh "$_sb" read -f s.txt 2 2>/dev/null | sed -n 2p)"
check "write FILE CONTENT" "created w.txt (1 line)" "$(rsh "$_sb" write w.txt hi 2>/dev/null | sed -n 1p)"
check "write FILE 内容落盘" "hi" "$(sed -n 1p "$_sb/w.txt")"
w1="$(rsh "$_sb" read w.txt 1:1 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
check "edit FILE FROM CONTENT" "0" "$(rsh "$_sb" edit w.txt "$w1" HI >/dev/null 2>&1; echo $?)"
check "edit 简写改对了" "HI" "$(sed -n 1p "$_sb/w.txt")"
# 一次多组：位置参数成对出现
printf 'p\nq\nr\n' > "$_sb/m.txt"
rsh "$_sb" read m.txt >/dev/null 2>&1
m1="$(rsh "$_sb" read m.txt 1:1 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
m3="$(rsh "$_sb" read m.txt 3:3 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
check "edit FILE 多对（简写）" "0" \
    "$(rsh "$_sb" edit m.txt "$m1" P "$m3" R >/dev/null 2>&1; echo $?)"
check "多对第一处生效" "P" "$(sed -n 1p "$_sb/m.txt")"
check "多对第二处生效" "R" "$(sed -n 3p "$_sb/m.txt")"
check "简写缺内容 → 2" "2" "$(rsh "$_sb" edit m.txt "$m1" >/dev/null 2>&1; echo $?)"

# ─────────────────────────────────────────────────────────────
section E "grep 的 GNU 习惯兼容"
# ─────────────────────────────────────────────────────────────
printf 'foo\nbar\nbaz\n' > "$_sb/e.txt"
check "grep -E 交替可用" "2" "$(rsh "$_sb" grep -c -E 'foo|bar' e.txt 2>/dev/null)"
check "grep --extended-regexp 同 -E" "2" \
    "$(rsh "$_sb" grep -c --extended-regexp 'foo|bar' e.txt 2>/dev/null)"
contains "grep -E 命中行带锚点" "$SEP" "$(rsh "$_sb" grep -E 'foo|bar' e.txt 2>/dev/null | tail -1)"
check "grep -G 明确报错 → 2" "2" "$(rsh "$_sb" grep -G foo e.txt >/dev/null 2>&1; echo $?)"
contains "grep -G 提示只有 ERE" "rg 只有 ERE" "$(rsh "$_sb" grep -G foo e.txt 2>&1)"
check "grep --encoding 仍是编码" "1" \
    "$(rsh "$_sb" grep -c --encoding utf-8 foo e.txt 2>/dev/null)"

# ─────────────────────────────────────────────────────────────
section P "路径形态与读取上限"
# ─────────────────────────────────────────────────────────────
new_sandbox
# MSYS 绝对路径（/tmp/...）在 MSYS_NO_PATHCONV=1 的 shell 里要靠 cygpath 兜底
if command -v cygpath >/dev/null 2>&1; then
        absdir="$(mktemp -d)"
        printf 'absolute hit\n' > "$absdir/abs.txt"
        contains "read 认 /tmp 形态（cygpath 兜底）" "absolute hit" \
                "$(rsh "$_sb" read -f "$absdir/abs.txt" 2>/dev/null | tail -1)"
        contains "grep 认 /tmp 形态" "absolute hit" \
                "$(rsh "$_sb" grep hit "$absdir/abs.txt" 2>/dev/null | tail -1)"
        rm -rf "$absdir"
fi
# 单行超过 max_line_bytes = 6000 → 锚点位置是 ~~~（不能用于 edit）
{ printf 'ok\n'; head -c 7000 /dev/zero | tr '\0' 'y'; printf '\n'; printf 'tail\n'; } > "$_sb/long.txt"
out="$(rsh "$_sb" read -f long.txt 2>/dev/null)"
contains "超长行为 ~~~（不给锚点）" "~~~${SEP}yyy" "$out"
contains "超长行的邻居照旧有锚点" "${SEP}tail" "$out"
# 一次全读上限 max_lines = 20000 → 标 truncated
seq 1 20001 > "$_sb/many.txt"
contains "全读上限标 truncated" "(20000 lines, lf, truncated)" \
        "$(rsh "$_sb" read -f many.txt 2>/dev/null | sed -n 1p)"

# ─────────────────────────────────────────────────────────────
printf '\n'
if [ "$fail" -eq 0 ]; then
	echo "e2e test: $pass passed, 0 failed"
	exit 0
fi
echo "e2e test: $pass passed, $fail FAILED"
exit 1
