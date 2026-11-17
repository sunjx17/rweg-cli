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
# /tmp/... 会变成当前盘根下的 C:\tmp\...，编译产物就落到那里，临时套件里只剩空目录。
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
# read / write / edit / grep 在 bash 里是内置或 PATH 命令，只有 env.sh 的同名函数压得过
# 它们 —— 所以下文直接用短名；h* 别名对应 hread / hwrite / hedit / hgrep。
#
# 调用者 shell 里的 $RWEG_PIN_CONFIG 由 run 先 unset：它来自你自己 source 过的 env.sh，
# 不清掉就把沙箱的 rweg.toml 整个顶掉，配置与库都落到调用者那边。
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
		unset RWEG_PIN_CONFIG
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

# 只用二进制裸路径、不经 env.sh 的函数（子命令形态）—— 场景 L 验"没 source 也能用"。
# bin/ 里只有 core-rweg 一个二进制，工具靠子命令区分（不再有 read.exe 之类的硬链接）。
raw() { # raw <沙箱> <工具名> <参数...>
	local d="$1" tool="$2"
	shift 2
	( builtin cd "$d" || exit 1; unset RWEG_NS RWEG_PIN_CONFIG
	  "$_home/bin/core-rweg.exe" "$tool" "$@" )
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
check "edit 摘要（确认行 + 收尾行）" "edited ok
src/app.ts (6 → 8 lines, +3 -1)" "$(printf '%s\n' "$rc_out" | sed -n '1p;$p')"
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

out="$(rsh "$_sb" edit -f src/app.ts -c CTt:AU6 "function hi() {" 2>&1)"
rcv=$?
err="$out"

check "退出码" "1" "$rcv"
contains "错误码" "E_STALE_RANGE" "$err"
contains "说明行" "lines 1-3 changed since read" "$err"
contains "回传块头" "Current range (1-3):" "$err"
contains "回传的是**当前**锚点" "nEt${SEP}  console.log(\"world!!!\");" "$err"
contains "提示可直接重试" "hint: retry with the anchors above (lines 1-3 are served now)" "$err"
check "拒绝时文件未被改动" "  console.log(\"world!!!\");" "$(sed -n 2p "$_sb/src/app.ts")"

# 回显的行随即进 served → 拿回传锚点可以直接重试，不必先重新 read
out="$(rsh "$_sb" edit -f src/app.ts -c nEt:nEt 'console.log("world??");' 2>/dev/null)"
contains "回传锚点可直接重试" "edited ok" "$(printf '%s\n' "$out" | sed -n 1p)"
check "重试改到第 2 行" 'console.log("world??");' "$(sed -n 2p "$_sb/src/app.ts")"

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

printf 'l1\nl2\n' | ( builtin cd "$_sb" && export RWEG_NS="$NS" && unset RWEG_PIN_CONFIG && . "$_home/scripts/env.sh" >/dev/null 2>&1 && write -f src/pipe.ts ) >/dev/null 2>&1
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
section G2 "read 输出上限 max_lines：不参与行数判定，也不写坏长文件"
# ─────────────────────────────────────────────────────────────
new_sandbox src
# 上限压到 20 行：超过 20 行的文件足以逼出「把上限当真实行数」的老 bug。
printf '[db]
dir = ".db/"
[read]
max_lines = 20
' > "$_sb/rweg.toml"
seq 1 300 > "$_sb/long.txt"

check "头部行数是真实行数" "# long.txt (300 lines, lf)" 	"$(rsh "$_sb" read -f long.txt 1:3 2>/dev/null | sed -n 1p)"
check "上限之外的范围可读" "250:255:" 	"$(rsh "$_sb" read -f long.txt 250:255 2>/dev/null | sed -n 2p)"
check "上限之外的行内容正确" "250" 	"$(rsh "$_sb" read -f long.txt 250:250 2>/dev/null | sed -n 3p | awk -F"$SEP" '{print $2}')"
check "上限之外的 N- 可读" "298:300:" 	"$(rsh "$_sb" read -f long.txt 298- 2>/dev/null | sed -n 2p)"
check "N-M 闭区间可读" "295:300:" 	"$(rsh "$_sb" read -f long.txt 295-300 2>/dev/null | sed -n 2p)"

err="$(rsh "$_sb" read -f long.txt 301 2>&1 >/dev/null)"
contains "E_RANGE 提示真实行数" "300 line(s)" "$err"

# 整份读：头部说真实行数并标 truncated，块只到上限
check "整份读：头部真实行数 + truncated" "# long.txt (300 lines, lf, truncated)" 	"$(rsh "$_sb" read -f long.txt 2>/dev/null | sed -n 1p)"
check "整份读：块头截到上限" "1:20:" 	"$(rsh "$_sb" read -f long.txt 2>/dev/null | sed -n 2p)"
check "整份读：内容 20 行" "20" 	"$(rsh "$_sb" read -fn long.txt 2>/dev/null | sed -n '3,$p' | wc -l | tr -d ' ')"
check "多块共用一个输出额度" "1:10: 50:59: " 	"$(rsh "$_sb" read -f long.txt 1:10 50:60 2>/dev/null | grep '^[0-9]*:[0-9]*:$' | tr '
' ' ')"

# 有能力读第 250 行，就意味着它能被编辑；编辑不得把文件写短
anchor250="$(rsh "$_sb" read -f long.txt 250:250 2>/dev/null | sed -n 3p | awk -F"$SEP" '{print $1}')"
rsh "$_sb" edit -f long.txt -c "$anchor250:$anchor250" "line two five zero" >/dev/null 2>&1
check "上限之外的行可编辑（rc）" "0" "$?"
check "编辑后文件仍是 300 行" "300" "$(wc -l < "$_sb/long.txt" | tr -d ' ')"
check "编辑后目标行已改" "line two five zero" "$(sed -n 250p "$_sb/long.txt")"

rsh "$_sb" grep -n "299" long.txt >/dev/null 2>&1
check "grep 命中上限之外的行（rc）" "0" "$?"

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
out="$(cat "$_sb/src/app.ts" | (builtin cd "$_sb" && export RWEG_NS="$NS" && unset RWEG_PIN_CONFIG && . "$_home/scripts/env.sh" >/dev/null 2>&1 && grep hello ) 2>/dev/null)"
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
# -c 的 GNU 语义：只数命中行，-A/-B/-C 的上下文不算；块头同。
check "-C1 下 -c 只数命中行" "1" "$(rsh "$_sb" grep -c -C1 world src/app.ts 2>/dev/null)"
contains "命中数头只数命中行" "# src/app.ts (1 match)" \
	"$(rsh "$_sb" grep -C1 world src/app.ts 2>/dev/null | sed -n 1p)"
# GNU 的 -h（--no-filename）：带模式/路径时是搜索，不是帮助（rg 的 -h 才是帮助）。
contains "grep -h pat 是搜索" "1:CTt${SEP}" \
	"$(rsh "$_sb" grep -h hello src/app.ts 2>/dev/null)"
out="$(RWEG_RG_MISSING=1 rsh "$_sb" grep -h hello src/app.ts 2>/dev/null)"
check "回退系统 grep 时 -h 不带文件名前缀" "function hello() {" "$(printf '%s\n' "$out" | sed -n 1p)"
# --json：锚点路径也要给结构化输出（此前只有 -c 认它）
js="$(rsh "$_sb" grep --json hello src/app.ts 2>/dev/null)"
contains "grep --json 有 path" '"path"' "$js"
contains "grep --json 有锚点" '"hash": "CTt"' "$js"
contains "grep --json 有命中标记" '"match": true' "$js"

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
# ⚠️ 探测按"解码结果的字符分布"打分（Big5 → GBK → GB18030 特指优先）：
# 纯 GBK 字节现在报更精确的 gbk，而不是被 GB18030 子集关系统吞。
check "GBK 自动探测（gbk）" "# gbk.txt (1 line, lf, gbk)" "$(printf '%s\n' "$out" | sed -n 1p)"
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

# Big5：自动探测不再被 GB18030 吃掉（往返检查分不出表，按字符分布选）。
printf '\xa4\xa4\xa4\xe5\n' > "$_sb/big5.txt"   # Big5 的「中文」
out="$(rsh "$_sb" read -f big5.txt 2>/dev/null)"
check "Big5 自动探测" "# big5.txt (1 line, lf, big5)" "$(printf '%s\n' "$out" | sed -n 1p)"
check "Big5 内容解码正确" "中文" "$(printf '%s\n' "$out" | sed -n 3p | awk -F"$SEP" '{print $2}')"
b5="$(printf '%s\n' "$out" | anchor_at 1)"
rsh "$_sb" edit -f big5.txt -c "$b5:$b5" "中文繁" >/dev/null 2>&1
check "编辑 Big5 文件（rc）" "0" "$?"
if cmp -s <(printf '\xa4\xa4\xa4\xe5\xc1\x63\n') "$_sb/big5.txt"; then
	ok "Big5 写回仍是 Big5 字节"
else
	bad "Big5 写回仍是 Big5 字节" "$(od -An -tx1 "$_sb/big5.txt")"
fi

# 纯 CR 行尾（经典 Mac）：识别为 cr，写回复用 CR（此前被静默重排成 LF）
printf 'a\rb\rc\r' > "$_sb/cr.txt"
out="$(rsh "$_sb" read -f cr.txt 2>/dev/null)"
check "纯 CR 行尾标记" "# cr.txt (3 lines, cr)" "$(printf '%s\n' "$out" | sed -n 1p)"
cra="$(printf '%s\n' "$out" | anchor_at 1)"
rsh "$_sb" edit -f cr.txt -c "$cra:$cra" "A" >/dev/null 2>&1
if cmp -s <(printf 'A\rb\rc\r') "$_sb/cr.txt"; then
	ok "CR 文件写回仍是 CR"
else
	bad "CR 文件写回仍是 CR" "$(od -c "$_sb/cr.txt" | head -2)"
fi

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
contains "append -h" "append FILE ANCHOR CONTENT" "$(rsh "$_sb" append -h 2>&1 | sed -n 1p)"
contains "prepend -h" "prepend FILE ANCHOR CONTENT" "$(rsh "$_sb" prepend -h 2>&1 | sed -n 1p)"
rcv="$(rsh "$_sb" core-rweg nosuchcmd >/dev/null 2>&1; echo $?)"
check "未知子命令 → 2" "2" "$rcv"
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
contains "hundo_last_edit 别名可用" "E_NO_UNDO" "$(rsh "$_sb" hundo_last_edit u/app.ts 2>&1)"
contains "undo 不支持 --json" "not supported" "$(rsh "$_sb" undo --json 2>&1)"
# 超过 4 MiB 的前像不留副本：明确报 E_UNDO_TOO_BIG
yes "0123456789012345678901234567890123456789" | head -c 4300000 > "$_sb/u/big.txt"
check "覆盖 4.3 MB 已有文件" "0" \
    "$(rsh "$_sb" write -f u/big.txt < /dev/null >/dev/null 2>&1; echo $?)"
contains "大前像报 E_UNDO_TOO_BIG" "E_UNDO_TOO_BIG" "$(rsh "$_sb" undo u/big.txt 2>&1)"
rm -f "$_sb/u/big.txt"

# ─────────────────────────────────────────────────────────────
section K "append / prepend：单锚点插行"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1
a4="$(rsh "$_sb" read -f src/app.ts 4:4 2>/dev/null | anchor_at 1)"

# 文件放在最前（可省 -f）；被锚定的那一行原样保留、锚点不变
out="$(rsh "$_sb" append src/app.ts "$a4" "const y = 2;" 2>/dev/null)"
check "append 摘要（确认行 + 收尾行）" "appended ok
src/app.ts (6 → 7 lines, +1)" "$(printf '%s\n' "$out" | sed -n '1p;$p')"
check "append 插在锚定行之后" "const y = 2;" "$(sed -n 5p "$_sb/src/app.ts")"
check "被锚定的行锚点不变" "$a4" "$(rsh "$_sb" read -f src/app.ts 4:4 2>/dev/null | anchor_at 1)"

# prepend 插在锚定行之前；内容恰好是 rweg 认得的 flag 时用 -c 形态递进去
out="$(rsh "$_sb" prepend src/app.ts -c "$a4" "-v" 2>/dev/null)"
check "prepend 摘要（确认行 + 收尾行）" "prepended ok
src/app.ts (7 → 8 lines, +1)" "$(printf '%s\n' "$out" | sed -n '1p;$p')"
check "prepend 插在锚定行之前" "-v" "$(sed -n 4p "$_sb/src/app.ts")"
check "-c 形态保住看似 flag 的内容" "const x = 1;" "$(sed -n 5p "$_sb/src/app.ts")"

# "" = 插一个空行
rsh "$_sb" read -f src/app.ts 3:3 >/dev/null 2>&1
a3="$(rsh "$_sb" read -f src/app.ts 3:3 2>/dev/null | anchor_at 1)"
rsh "$_sb" append src/app.ts "$a3" "" >/dev/null 2>&1
check "空内容 = 插一个空行" "" "$(sed -n 4p "$_sb/src/app.ts")"

# undo 里记的 kind 用工具名
contains "undo 记 append" "undone append" "$(rsh "$_sb" undo src/app.ts 2>/dev/null)"
contains "再 undo 退 prepend" "undone prepend" "$(rsh "$_sb" undo src/app.ts 2>/dev/null)"

# 文件必填：不给文件时第一个位置参数会被当成文件名 → 用法错误
new_sandbox
printf 'a\nSHARED\n' > "$_sb/one.ts"
printf 'b\nSHARED\n' > "$_sb/two.ts"
rsh "$_sb" read -f one.ts >/dev/null 2>&1
rsh "$_sb" read -f two.ts >/dev/null 2>&1
sh="$(rsh "$_sb" read -f one.ts 2:2 2>/dev/null | anchor_at 1)"
err="$(rsh "$_sb" append "$sh" NEW 2>&1 >/dev/null)"
contains "不给文件 → 指出缺 FILE" "FILE is required" "$err"
rcv="$(rsh "$_sb" append "$sh" NEW >/dev/null 2>&1; echo $?)"
check "不给文件 → 退出码 2" "2" "$rcv"
rsh "$_sb" append -f two.ts "$sh" NEW >/dev/null 2>&1
check "-f FILE 指明后成功" "NEW" "$(sed -n 3p "$_sb/two.ts")"

# 参数错：形状非法 / 缺内容都是用法错误（2）
rcv="$(rsh "$_sb" append ZZ NEW >/dev/null 2>&1; echo $?)"
check "锚点形状非法 → 2" "2" "$rcv"
rcv="$(rsh "$_sb" append -f one.ts "$sh" >/dev/null 2>&1; echo $?)"
check "缺 CONTENT → 2" "2" "$rcv"

# ─────────────────────────────────────────────────────────────
section D "edit 的差异形态（git 风格）"
# ─────────────────────────────────────────────────────────────
new_sandbox
printf 'a\nb\nc\nd\n' > "$_sb/d.txt"
rsh "$_sb" read -f d.txt >/dev/null 2>&1
d1="$(rsh "$_sb" read -f d.txt 2:2 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
out="$(rsh "$_sb" edit -f d.txt -c "$d1" B 2>/dev/null)"
contains "收尾行计数 +1 -1" "(4 → 4 lines, +1 -1)" "$(printf '%s\n' "$out" | tail -1)"
contains "删除行带 - 前缀与旧锚点" "- ${d1}${SEP}b" "$out"
contains "新增行带 + 前缀" "+ " "$out"
contains "上下文行带两个空格前缀" "  " "$out"
# 只增不减时不出现 -N
printf 'x\ny\n' > "$_sb/ap.txt"
rsh "$_sb" read -f ap.txt >/dev/null 2>&1
ap1="$(rsh "$_sb" read -f ap.txt 1:1 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
out="$(rsh "$_sb" edit -f ap.txt -c "$ap1" $'x\nz' 2>/dev/null)"
contains "只增不减时不出 -N" "(2 → 3 lines, +1)" "$(printf '%s\n' "$out" | tail -1)"
contains "没删任何行" "+ " "$out"
# 相距较远的块之间用 ... 省略，且只省略一次
seq 1 20 | sed 's/^/line /' > "$_sb/far.txt"
rsh "$_sb" read -f far.txt >/dev/null 2>&1
f2="$(rsh "$_sb" read -f far.txt 2:2 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
f18="$(rsh "$_sb" read -f far.txt 18:18 2>/dev/null | tail -1 | awk -F"$SEP" '{print $1}')"
out="$(rsh "$_sb" edit -f far.txt -c "$f2" L2 -c "$f18" L18 2>/dev/null)"
contains "远距离块收尾行计数 +2 -2" "(20 → 20 lines, +2 -2)" "$(printf '%s\n' "$out" | tail -1)"
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
section R "无锚形态 -fn"
# ─────────────────────────────────────────────────────────────
new_sandbox
printf 'aa\nbb\ncc\n' > "$_sb/n.txt"

_out="$(rsh "$_sb" read -fn n.txt)"
check "read -fn 块头仍在" "1:3:" "$(printf '%s' "$_out" | sed -n 2p)"
check "read -fn 只给内容（无行锚）" "aa bb cc" "$(printf '%s' "$_out" | tail -n +3 | tr '\n' ' ')"
case "$_out" in
*│*) bad "read -fn 不该出行锚分隔符" "$_out" ;;
*) ok "read -fn 不出行锚分隔符" ;;
esac

check "write -fn 只回一行确认" "created w.txt (2 lines)" \
    "$(rsh "$_sb" write -fn w.txt -c $'x\ny')"
check "write -fn 确实落盘" "x
y" "$(cat "$_sb/w.txt")"

check "grep -fn 只给行号与内容" "# n.txt (1 match)
2:bb" "$(rsh "$_sb" grep -n -fn n.txt bb)"

contains "read -f 仍出行锚" "$SEP" "$(rsh "$_sb" read -f n.txt 2>/dev/null | tail -1)"

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
# 一次全读上限 max_lines = 20000：只截输出并标 truncated，头部行数仍是真实行数
seq 1 20001 > "$_sb/many.txt"
contains "全读上限标 truncated" "(20001 lines, lf, truncated)" \
        "$(rsh "$_sb" read -f many.txt 2>/dev/null | sed -n 1p)"
check "全读上限只裁到第 20000 行" "1:20000:" \
        "$(rsh "$_sb" read -f many.txt 2>/dev/null | sed -n 2p)"
check "第 20001 行仍可按范围读到" "20001:20001:" \
        "$(rsh "$_sb" read -f many.txt 20001 2>/dev/null | sed -n 2p)"

# ─────────────────────────────────────────────────────────────
section S "replace：范围内子串 / 正则替换"
# ─────────────────────────────────────────────────────────────
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1

# 取某几行的锚点（逗号分隔）——read 输出里每行的「锚点│内容」取第一段。
anchors_at() { # anchors_at <相对路径> <范围...>
	local f="$1"
	shift
	rsh "$_sb" read -f "$f" "$@" 2>/dev/null | awk -F"$SEP" 'NF>1{print $1}' | tr '\n' ','
}

# --- 精确子串：只动目标行，行数不变 ---
out="$(rsh "$_sb" hreplace -f src/app.ts -c nVR "const x = 1;" "const x = 42;" 2>/dev/null)"
check "replace 摘要（确认行 + 收尾行）" "replaced ok
src/app.ts (6 → 6 lines, +1 -1)" "$(printf '%s\n' "$out" | sed -n '1p;$p')"
contains "差异里有删除行（旧锚点）" "- nVR${SEP}const x = 1;" "$out"
check "目标行已改" "const x = 42;" "$(sed -n 4p "$_sb/src/app.ts")"
check "行数不变" "6" "$(wc -l < "$_sb/src/app.ts" | tr -d ' ')"
check "未动行的锚点全保留" "CTt,Um7,AU6,yAZ,6PY," \
	"$(anchors_at src/app.ts 1:3)$(anchors_at src/app.ts 5:6)"
check "改过的行锚点已换新" "no" \
	"$([ "$(anchors_at src/app.ts 4:4)" = "nVR," ] && echo yes || echo no)"

# -q 只留差异正文（锚点用**改过之后**那一行的，旧锚点已 retired）
r4="$(anchors_at src/app.ts 4:4)"
out="$(rsh "$_sb" hreplace -f src/app.ts -q -c "${r4%,}" "const x = 42;" "const x = 7;" 2>/dev/null)"
contains "-q 只给差异正文（无确认行）" "+ " "$out"
check "-q 不打确认行" "no" "$(printf '%s\n' "$out" | grep -q '^replaced ok$' && echo yes || echo no)"
check "-q 改到了目标行" "const x = 7;" "$(sed -n 4p "$_sb/src/app.ts")"

# --- 行内局部替换：该行其余部分原样保留 ---
y5="$(anchors_at src/app.ts 5:5)"
rsh "$_sb" hreplace -f src/app.ts -c "${y5%,}" "= 2;" "= 300;" >/dev/null 2>&1
check "行内局部替换" "let y = 300;" "$(sed -n 5p "$_sb/src/app.ts")"
check "只动目标行（上一处不受影响）" "const x = 7;" "$(sed -n 4p "$_sb/src/app.ts")"

# --- 一行里命中几处就替换几处 ---
printf 'aaa aaa aaa\nbbb\n' > "$_sb/multi.txt"
rsh "$_sb" read -f multi.txt >/dev/null 2>&1
m1="$(anchors_at multi.txt 1:1)"
rsh "$_sb" hreplace -f multi.txt -c "${m1%,}" "aaa" "z" >/dev/null 2>&1
check "一行内多处命中全换" "z z z" "$(sed -n 1p "$_sb/multi.txt")"
check "未命中的行不动" "bbb" "$(sed -n 2p "$_sb/multi.txt")"

# --- 区间：范围内全换，范围外一个不动（四行内容相同，锚点按出现次序区分） ---
printf 'foo\nfoo\nfoo\nfoo\n' > "$_sb/range.txt"
rsh "$_sb" read -f range.txt >/dev/null 2>&1
r1="$(anchors_at range.txt 1:1)"; r2="$(anchors_at range.txt 2:2)"
rsh "$_sb" hreplace -f range.txt -c "${r1%,}:${r2%,}" "foo" "bar" >/dev/null 2>&1
check "区间内两行都换" "bar
bar" "$(sed -n 1,2p "$_sb/range.txt")"
check "区间外两行不动" "foo
foo" "$(sed -n 3,4p "$_sb/range.txt")"
check "行数不变" "4" "$(wc -l < "$_sb/range.txt" | tr -d ' ')"

# --- 多组改动：-c / -cr 可重复，一次调用原子生效（与 edit 的多 -c 同规） ---
printf 'one\ntwo\nthree\nfour\n' > "$_sb/multi2.txt"
rsh "$_sb" read -f multi2.txt >/dev/null 2>&1
k1="$(anchors_at multi2.txt 1:1)"; k1="${k1%,}"
k3="$(anchors_at multi2.txt 3:3)"; k3="${k3%,}"
k4="$(anchors_at multi2.txt 4:4)"; k4="${k4%,}"
out="$(rsh "$_sb" hreplace -f multi2.txt -c "$k1" "one" "1" -cr "$k3:$k4" "t" "T" 2>/dev/null)"
check "多组 replace 摘要" "replaced ok
multi2.txt (4 → 4 lines, +2 -2)" "$(printf '%s\n' "$out" | sed -n '1p;$p')"
check "第一组生效" "1" "$(sed -n 1p "$_sb/multi2.txt")"
check "第二组区间内命中行生效" "Three" "$(sed -n 3p "$_sb/multi2.txt")"
check "第二组区间内未命中行不动" "four" "$(sed -n 4p "$_sb/multi2.txt")"

# 区间重叠 → E_OVERLAP，不写盘。
# ⚠️ 锚点要在**上一步改完之后**现取：line 1/3 已变，旧锚点会先撞 E_UNKNOWN_ANCHOR。
k1b="$(anchors_at multi2.txt 1:1)"; k1b="${k1b%,}"
k3b="$(anchors_at multi2.txt 3:3)"; k3b="${k3b%,}"
k4b="$(anchors_at multi2.txt 4:4)"; k4b="${k4b%,}"
err="$(rsh "$_sb" hreplace -f multi2.txt -c "$k1b:$k3b" "e" "E" -c "$k3b:$k4b" "o" "0" 2>&1 >/dev/null)"
contains "区间重叠 → E_OVERLAP" "E_OVERLAP" "$err"
check "重叠不写盘" "Three" "$(sed -n 3p "$_sb/multi2.txt")"

# 重复 -c 曾被静默覆盖：现在两组都生效
printf 'aaa\nbbb\n' > "$_sb/two.txt"
rsh "$_sb" read -f two.txt >/dev/null 2>&1
t1="$(anchors_at two.txt 1:1)"; t1="${t1%,}"
t2="$(anchors_at two.txt 2:2)"; t2="${t2%,}"
rsh "$_sb" hreplace -f two.txt -c "$t1" "aaa" "A" -c "$t2" "bbb" "B" >/dev/null 2>&1
check "两组 -c 都生效（不静默覆盖）" "A
B" "$(sed -n 1,2p "$_sb/two.txt")"

# 多组中一组无命中 → E_NO_MATCH，全不写盘（原子）
printf 'p\nq\n' > "$_sb/atom.txt"
rsh "$_sb" read -f atom.txt >/dev/null 2>&1
a1="$(anchors_at atom.txt 1:1)"; a1="${a1%,}"
a2="$(anchors_at atom.txt 2:2)"; a2="${a2%,}"
err="$(rsh "$_sb" hreplace -f atom.txt -c "$a1" "p" "P" -c "$a2" "zzznope" "Q" 2>&1 >/dev/null)"
contains "多组中一组无命中 → E_NO_MATCH" "E_NO_MATCH" "$err"
check "多组原子：无写盘" "p" "$(sed -n 1p "$_sb/atom.txt")"

# 匹配空串的正则 → E_BAD_REGEX（否则替换文本插满整行）
printf 'hello\n' > "$_sb/zerom.txt"
rsh "$_sb" read -f zerom.txt >/dev/null 2>&1
z1="$(anchors_at zerom.txt 1:1)"; z1="${z1%,}"
err="$(rsh "$_sb" hreplace -f zerom.txt -cr "$z1" "x*" "Y" 2>&1 >/dev/null)"
contains "零长匹配正则 → E_BAD_REGEX" "E_BAD_REGEX" "$err"
check "零长匹配不写盘" "hello" "$(sed -n 1p "$_sb/zerom.txt")"
# 行尾锚（单个空命中位置）照常可用
rsh "$_sb" hreplace -f zerom.txt -cr "$z1" '$' '!' >/dev/null 2>&1
check "行尾锚追加照常" "hello!" "$(sed -n 1p "$_sb/zerom.txt")"

# --- 正则：子匹配展开（$1 / ${name}）---
#
# ⚠️ 子匹配紧随字母 / 数字 / 下划线时必须用 ${1}：`$1_` 会被 Go 读成组名 "1_"。
printf 'v1.2.3\nv2.0.0\n' > "$_sb/sem.txt"
rsh "$_sb" read -f sem.txt >/dev/null 2>&1
s1="$(anchors_at sem.txt 1:1)"
rsh "$_sb" hreplace -f sem.txt -cr "${s1%,}" 'v(\d+)\.(\d+)' 'V${1}_${2}' >/dev/null 2>&1
check "正则子匹配展开" "V1_2.3" "$(sed -n 1p "$_sb/sem.txt")"
# 字符类 + 整段命中
s2="$(anchors_at sem.txt 2:2)"
rsh "$_sb" hreplace -f sem.txt -cr "${s2%,}" '[0-9]+' 'N' >/dev/null 2>&1
check "正则字符类全换" "vN.N.N" "$(sed -n 2p "$_sb/sem.txt")"
# 命名子匹配
printf 'key=value\n' > "$_sb/named.txt"
rsh "$_sb" read -f named.txt >/dev/null 2>&1
n1="$(anchors_at named.txt 1:1)"
rsh "$_sb" hreplace -f named.txt -cr "${n1%,}" '(?P<k>\w+)=(?P<v>\w+)' '${v}/${k}' >/dev/null 2>&1
check "命名子匹配展开" "value/key" "$(sed -n 1p "$_sb/named.txt")"

# --- 空替换文本 = 删掉匹配；正则命中整行则为空行（不是删行）---
printf 'key:value\n' > "$_sb/del.txt"
rsh "$_sb" read -f del.txt >/dev/null 2>&1
d1="$(anchors_at del.txt 1:1)"
rsh "$_sb" hreplace -f del.txt -c "${d1%,}" ":value" "" >/dev/null 2>&1
check "空替换 = 删掉匹配" "key" "$(sed -n 1p "$_sb/del.txt")"

printf 'drop\nkeep\n' > "$_sb/blank.txt"
rsh "$_sb" read -f blank.txt >/dev/null 2>&1
b1="$(anchors_at blank.txt 1:1)"
rsh "$_sb" hreplace -f blank.txt -cr "${b1%,}" '^\S+$' '' >/dev/null 2>&1
check "整行命中换空 = 空行（不删行）" "2" "$(wc -l < "$_sb/blank.txt" | tr -d ' ')"
check "第 1 行成空行" "" "$(sed -n 1p "$_sb/blank.txt")"
check "第 2 行不动" "keep" "$(sed -n 2p "$_sb/blank.txt")"

# --- 替换文本 / 正则长得像 rweg 的 flag 时不能被吞掉 ---
printf 'a-v-b\n' > "$_sb/flag.txt"
rsh "$_sb" read -f flag.txt >/dev/null 2>&1
f1="$(anchors_at flag.txt 1:1)"
rsh "$_sb" hreplace -f flag.txt -cr "${f1%,}" "-v" "V" >/dev/null 2>&1
check "正则本身像 flag（-v）不被吞" "aV-b" "$(sed -n 1p "$_sb/flag.txt")"
printf 'x\n' > "$_sb/flag2.txt"
rsh "$_sb" read -f flag2.txt >/dev/null 2>&1
f2="$(anchors_at flag2.txt 1:1)"
rsh "$_sb" hreplace -f flag2.txt -c "${f2%,}" "x" "-q" >/dev/null 2>&1
check "替换文本像 flag（-q）不被吞" "-q" "$(sed -n 1p "$_sb/flag2.txt")"

# --- 编码保真：GBK 文件替换后仍是 GBK 字节 ---
printf '\xd6\xd0\xce\xc4\xba\xc5\xca\xa1\n' > "$_sb/gbk.txt"   # 中文号省
rsh "$_sb" read -f gbk.txt >/dev/null 2>&1
g1="$(anchors_at gbk.txt 1:1)"
rsh "$_sb" hreplace -f gbk.txt -c "${g1%,}" "号省" "号省二号" >/dev/null 2>&1
if cmp -s <(printf '\xd6\xd0\xce\xc4\xba\xc5\xca\xa1\xb6\xfe\xba\xc5\n') "$_sb/gbk.txt"; then
	ok "GBK 文件替换后仍是 GBK 字节"
else
	bad "GBK 文件替换后仍是 GBK 字节" "$(od -An -tx1 "$_sb/gbk.txt")"
fi

# --- undo 记的 kind 是 replace ---
rsh "$_sb" read -f src/app.ts 4:4 >/dev/null 2>&1
contains "undo 记 replace" "undone replace" "$(rsh "$_sb" undo src/app.ts 2>/dev/null)"

# --- 拒绝路径 ---
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1

# ⚠️ 与 stale-range 场景同理：出错路径的 stdout/stderr 与退出码在同一次调用里收齐，
# 不跑第二遍 —— 就算这条错误路径本身无 serve 副作用，也统一按惯例来。
out="$(rsh "$_sb" hreplace -f src/app.ts -c nVR "zzzznope" "x" 2>&1)"
rcv=$?
check "无命中 → 退出码 1" "1" "$rcv"
contains "无命中 → E_NO_MATCH" "E_NO_MATCH" "$out"
contains "无命中给出范围" "lines 4-4" "$out"

out="$(rsh "$_sb" hreplace -f src/app.ts -c nVR "const x" "const x" 2>&1)"
rcv=$?
check "命中但文本没变 → 1" "1" "$rcv"
contains "命中但文本没变 → E_NO_CHANGE" "E_NO_CHANGE" "$out"
check "E_NO_MATCH 不写盘" "const x = 1;" "$(sed -n 4p "$_sb/src/app.ts")"

# 锚点形状非法 / 缺 -f / 简写形态 → 用法错误 2
rcv="$(rsh "$_sb" hreplace -f src/app.ts -c ZZ "a" "b" >/dev/null 2>&1; echo $?)"
check "锚点形状非法 → 2" "2" "$rcv"
rcv="$(rsh "$_sb" hreplace -c nVR "a" "b" >/dev/null 2>&1; echo $?)"
check "缺 -f → 2" "2" "$rcv"
contains "缺 -f 时点出必须用 -f" "FILE must be named with -f" \
	"$(rsh "$_sb" hreplace src/app.ts nVR "a" "b" 2>&1)"
rcv="$(rsh "$_sb" hreplace src/app.ts nVR "a" "b" >/dev/null 2>&1; echo $?)"
check "不做 FILE 放最前的简写 → 2" "2" "$rcv"
rcv="$(rsh "$_sb" hreplace -f src/app.ts -c nVR "a" >/dev/null 2>&1; echo $?)"
check "-c 缺值 → 2" "2" "$rcv"

# 空模式 / 非法正则 → 用法错误 2
contains "空模式 → E_BAD_PATTERN" "E_BAD_PATTERN" \
	"$(rsh "$_sb" hreplace -f src/app.ts -c nVR "" "x" 2>&1)"
rcv="$(rsh "$_sb" hreplace -f src/app.ts -c nVR "" "x" >/dev/null 2>&1; echo $?)"
check "空模式 → 2" "2" "$rcv"
contains "非法正则 → E_BAD_REGEX" "E_BAD_REGEX" \
	"$(rsh "$_sb" hreplace -f src/app.ts -cr nVR "(" "x" 2>&1)"
rcv="$(rsh "$_sb" hreplace -f src/app.ts -cr nVR "(" "x" >/dev/null 2>&1; echo $?)"
check "非法正则 → 2" "2" "$rcv"

# 未读过的文件
printf 'a\nb\n' > "$_sb/src/never.ts"
out="$(rsh "$_sb" hreplace -f src/never.ts -c CTt "a" "b" 2>&1)"
rcv=$?
check "未读过 → 1" "1" "$rcv"
contains "未读过 → E_NEVER_READ" "E_NEVER_READ" "$out"

# 区间里有行被外部改过 → E_STALE_RANGE + 不写盘
#
# ⚠️ 只跑**一次**并同时收 stdout/stderr 与退出码：失败回显会把当前区间写进
# served（reject-and-serve），再跑第二次时同样的锚点反而能过 —— 那是设计行为，
# 不是"该失败却没失败"。
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1
sed -i 's/console\.log("world");/console.log("world!!!");/' "$_sb/src/app.ts"
# 副本要在外部改动**之后**取：要比的是"这次 replace 有没有动文件"，
# 不是"文件与 read 时是否一致"（sed 那一步本来就该让它不一致）。
cp "$_sb/src/app.ts" "$_work/re_before.ts"
out="$(rsh "$_sb" hreplace -f src/app.ts -c CTt:AU6 "console" "LOG" 2>&1)"
rcv=$?
check "区间过期 → 1" "1" "$rcv"
contains "区间过期 → E_STALE_RANGE" "E_STALE_RANGE" "$out"
contains "回传的是当前锚点" "nEt${SEP}" "$out"
if cmp -s "$_work/re_before.ts" "$_sb/src/app.ts"; then
	ok "区间过期时不写盘"
else
	bad "区间过期时不写盘" "file changed"
fi
# 区间跨到未 serve 的行（只读了 1:3 / 5:6）→ 拒
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:3 5:6 >/dev/null 2>&1
c3="$(anchors_at src/app.ts 3:3)"; c5="$(anchors_at src/app.ts 5:5)"
err="$(rsh "$_sb" hreplace -f src/app.ts -c "${c3%,}:${c5%,}" "let" "LET" 2>&1 >/dev/null)"
contains "区间整体过校验链（未 serve 的行也拒）" "E_STALE_RANGE" "$err"

# --json 可用
new_sandbox src
write_app_ts "$_sb" src/app.ts
rsh "$_sb" read -f src/app.ts 1:6 >/dev/null 2>&1
js="$(rsh "$_sb" hreplace -f src/app.ts -c nVR "const x = 1;" "const x = 42;" --json 2>/dev/null)"
contains "replace --json 有 path" '"path"' "$js"
contains "replace --json 有 diff" '"diff"' "$js"
contains "replace --json 计数" '"added": 1' "$js"

# -h 帮助与 core-rweg 子命令形态
contains "replace -h 出用法" "replace -f FILE -c ANCHOR OLDSTR" \
	"$(rsh "$_sb" hreplace -h 2>&1 | sed -n 1p)"
rsh "$_sb" read -f src/app.ts 4:4 >/dev/null 2>&1
c4="$(anchors_at src/app.ts 4:4)"
contains "core-rweg replace 子命令可用" "replaced ok" \
	"$(rsh "$_sb" core-rweg replace -f src/app.ts -c "${c4%,}" "const x = 42;" "const x = 43;" 2>&1 | sed -n 1p)"

# ─────────────────────────────────────────────────────────────
printf '\n'
if [ "$fail" -eq 0 ]; then
	echo "e2e test: $pass passed, 0 failed"
	exit 0
fi
echo "e2e test: $pass passed, $fail FAILED"
exit 1
