#!/usr/bin/env bash
# rweg 的 shell 层验收脚本。
#
# 覆盖那些**只有 source 过入口脚本才会暴露**的问题——
# Go 单测覆盖不到"bash builtin 压过 PATH"这类集成层面的坑。
# 套件主体跑在**侵入入口** env.aggressive.sh 上（read / write / edit / grep 被同名函数接管），
# 那是下面断言所依赖的那一层；[1c] 另验非侵入入口 env.sh 不碰这四个名字。
#
# 功能语义（锚点稳定性 / 校验链 / 编码 / 原子性）在 e2e-test.sh 里。
#
# 用法：
#   ./scripts/shell-test.sh
#   RWEG_HOME=/tmp/x ./scripts/shell-test.sh
#
# 退出码 0 = 全通过。

set -uo pipefail

# 调用者的 shell 若已经 source 过 env.sh / env.aggressive.sh，那些函数会随 export -f
# 进到本脚本里；本脚本验的是"source 之后有什么"，所以先把继承来的 rweg 函数擦干净，
# 子进程也不再收到 BASH_FUNC_* 导出。
for _fn in read write edit grep cd hread hwrite hedit hgrep hundo happend hprepend hundo_last_edit undo append prepend core-rweg; do
    unset -f "$_fn" 2>/dev/null || true
done
unset _fn
_home="${RWEG_HOME:-$HOME/.rweg}"
if [ -x "$_home/bin/core-rweg.exe" ]; then
	_core="$_home/bin/core-rweg.exe"
else
	_core="$_home/bin/core-rweg"
fi
if [ ! -x "$_core" ]; then
	echo "rweg not installed at $_home — run scripts/install.sh first" >&2
	exit 1
fi
_env="$_home/scripts/env.aggressive.sh"  # 套件主体用的入口：先 source env.sh 再接管同名命令
_env_plain="$_home/scripts/env.sh"       # 非侵入入口：只加 h* 与 undo / append / prepend
# 工具输出与 core-rweg 报的路径是**原生**形态（C:/... 或 C:\...），$_home 与 PATH
# 里的项却可能是 POSIX 形态（$HOME/.rweg）—— 比较前按需要过一遍 cygpath。
nat() {
	if command -v cygpath >/dev/null 2>&1; then
		cygpath -m "$1"
	else
		printf '%s' "$1"
	fi
}

posix() { # → POSIX 形态（/c/...）：MSYS 的 PATH 与 $HOME/.rweg 用的是这一种
	if command -v cygpath >/dev/null 2>&1; then
		cygpath -u "$1"
	else
		printf '%s' "$1"
	fi
}

win() { # → Windows 形态（C:\...）：core-rweg 报出来的路径是这一种
	if command -v cygpath >/dev/null 2>&1; then
		cygpath -w "$1"
	else
		printf '%s' "$1"
	fi
}

low() { # → 全小写：Windows 路径比较不该被盘符 / 目录名的大小写影响
	printf '%s' "$1" | tr 'A-Z' 'a-z'
}

pass=0
fail=0
ok()  { pass=$((pass+1)); printf '  ok    %s\n' "$1"; }
bad() { fail=$((fail+1)); printf '  FAIL  %s\n         %s\n' "$1" "${2:-}"; }
check() { # check <desc> <expected> <actual>
	if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "want [$2] got [$3]"; fi
}

path_hits() { # path_hits <目录A> <目录B> —— 数 PATH 里等于 A 或 B 的项（不借 grep）
	local n=0 d
	while IFS= read -r d; do
		case "$d" in
			"$1" | "$2") n=$((n+1)) ;;
		esac
	done
	printf '%s' "$n"
}
SEP='│'   # 锚点与内容之间的分隔符（$'\u2502' 在 MSYS bash 下不展开，故写字面量）

# --- 隔离沙箱 ---
sandbox="$(mktemp -d)"
trap 'rm -rf "$sandbox"' EXIT
cat > "$sandbox/rweg.toml" <<'EOF'
[db]
dir = ".db/"
EOF
{
	printf 'function hello() {\n'
	printf '  console.log("world");\n'
	printf '}\n'
	printf 'const x = 1;\n'
	printf 'let y = 2;\n'
	printf 'export default hello;\n'
} > "$sandbox/app.ts"

export RWEG_HOME="$_home"
export RWEG_WALKUP=0
unset RWEG_NS 2>/dev/null || true
# 调用者 shell 里的这两个变量也清掉：RWEG_PIN_CONFIG 来自他自己 source 过的 env.sh，
# 不清就会把沙箱的 rweg.toml 顶掉；RWEG_CONFIG 是显式覆盖，同样不该带进沙箱。
unset RWEG_PIN_CONFIG RWEG_CONFIG 2>/dev/null || true

# 在子 shell 里 source，避免污染调用者的环境。
# 用 bash（而非 sh）以支持函数与 builtin 语义。
rsh()  { ( cd "$sandbox" && . "$_env"       >/dev/null 2>&1 && "$@" ) }
rshp() { ( cd "$sandbox" && . "$_env_plain" >/dev/null 2>&1 && "$@" ) }

echo "rweg shell test ($_home)"
echo

# ── 1. builtin 覆盖（env.aggressive.sh 那层：read 是 bash builtin，PATH 压不过它）──
echo "[1] builtin 覆盖"
check "read 是 function（压过 builtin）" "function" "$(rsh type -t read)"
check "grep 是 function"                  "function" "$(rsh type -t grep)"
check "core-rweg 是 function"            "function" "$(rsh type -t core-rweg)"
# 派生脚本（dsh 常用 bash script.sh）也要能用 → 函数需 export -f
check "子进程也拿到函数" "function" \
	"$(rsh bash -c 'type -t read')"
# builtin 本身没被破坏。
# ⚠️ 必须用 `< file` 不能用管道：bash 的 read 要求可 seek 的 stdin，
#    管道下读到空（bash 既有行为，与本项目无关）。踩过一次。
echo builtin-probe > "$sandbox/.probe"
check "builtin read 仍可用" "builtin-probe" \
	"$(rsh bash -c 'builtin read -r _rweg_v < .probe; printf "%s" "$_rweg_v"' 2>/dev/null)"
# 不带 -f 的 read 交还 bash 内置：函数被 export -f 带进每个子 bash，
# 一律转发的话，脚本里的 `while read -r line` 会撞 unknown flag "-r"。
check "子进程 read -r 走内置（不被劫持）" "builtin-probe" \
	"$(rsh bash -c 'read -r _rweg_v < .probe; printf "%s" "$_rweg_v"' 2>/dev/null)"
check "子进程 read -a 数组语义不变" "x" \
	"$(rsh bash -c 'read -ra _rweg_a <<< "x y"; printf "%s" "${_rweg_a[0]}"' 2>/dev/null)"
pipeline="$(rsh bash -c 'printf "l1\nl2\n" | while read -r _l; do printf "[%s]" "$_l"; done' 2>/dev/null)"
check "子进程 while read -r 管道可用" "[l1][l2]" "$pipeline"

# read 的裸名判据 + 交还 builtin 那一侧的有界等待。
# 裸名（不带 . 或 /）过去被判成"变量名"而落回 builtin read：无重定向时它一直
# 等 stdin，把整条命令拖到超时，agent 侧表现为 shell 被重置、RWEG_NS 丢失。
printf 'notes-file\n' > "$sandbox/notes"
check "read 裸名（无扩展名文件）归 rweg" "# notes (1 line, lf)" \
    "$(rsh bash -c 'read notes 2>/dev/null | head -1')"
check "裸名 + 范围 与 裸名单独 同路径" "3" \
    "$(rsh bash -c 'read notes 1 2>/dev/null | wc -l')"
# 无重定向的 stdin 换成"一直开着但不产出数据"的进程替换（sleep 6），RWEG_READ_TIMEOUT=1
# 让它等 1 秒就返回；断言只看"是否 >128"（具体值随信号 14 浮动）。
# ⚠️ 不能用 `( sleep 3 ) | rsh ...`：source env.sh 本身要一秒多（原生进程 + cygpath），
#    读操作因此晚开始，写端一退出就是 EOF（rc 1），而那时刻界还没到 —— 断言会误报。
#    < <(sleep 6) 不拖住命令退出（bash 不等进程替换的子进程），界一定先到。
# 两条路径各起一条，不在同一个写端上连读两次：第一次超时后再读，可能直接是 EOF。
_toerr="$sandbox/.read-timeout.err"
: > "$_toerr"
_a="$(rsh bash -c 'RWEG_READ_TIMEOUT=1; read -r _v; echo $?' < <(sleep 6) 2>"$_toerr")"
_b="$(rsh bash -c 'RWEG_READ_TIMEOUT=1; read nosuchfile; echo $?' < <(sleep 6) 2>>"$_toerr")"
check "无输入：read -r 有界等待（>128，不挂死）" "yes" \
    "$([ "${_a:-0}" -gt 128 ] 2>/dev/null && echo yes || echo no)"
check "无输入：裸词 read nosuchfile 同样不挂死（>128）" "yes" \
    "$([ "${_b:-0}" -gt 128 ] 2>/dev/null && echo yes || echo no)"
case "$(cat "$_toerr" 2>/dev/null)" in
    *RWEG_READ_TIMEOUT*) ok "超时提示里给出关上限的开关" ;;
    *) bad "超时提示里给出关上限的开关" "stderr=[$(cat "$_toerr" 2>/dev/null)]" ;;
esac
check "RWEG_READ_TIMEOUT=0 关上限后重定向语义不变" "builtin-probe" \
    "$(rsh bash -c 'RWEG_READ_TIMEOUT=0; read -r _rweg_v < .probe; printf "%s" "$_rweg_v"' 2>/dev/null)"

# ── 1b. source 的输出面 ──
echo
echo "[1b] source 的输出"
check "env.sh 默认静默（stdout + stderr 皆空）" "" "$( ( . "$_env_plain" ) 2>&1 )"
check "env.aggressive.sh 默认静默" "" "$( ( . "$_env" ) 2>&1 )"
# 不用管道接 grep：banner 是多行，grep -q 命中首行即退出，写方吃 SIGPIPE，
# 管道形态在负载下会偶发"没看到 banner"。先收进变量再判，顺带留证。
_vout="$( ( export RWEG_VERBOSE=1; . "$_env_plain" ) 2>&1 >/dev/null )"
case "$_vout" in
    *rweg\ ready*) ok "RWEG_VERBOSE=1 打就绪信息" ;;
    *) bad "RWEG_VERBOSE=1 打就绪信息" "got [$_vout]" ;;
esac
case "$( ( export RWEG_VERBOSE=1; . "$_env" ) 2>&1 >/dev/null )" in
    *接管*) ok "aggressive 就绪信息里点出接管了哪四个名字" ;;
    *) bad "aggressive 就绪信息里点出接管了哪四个名字" "got [$( ( export RWEG_VERBOSE=1; . "$_env" ) 2>&1 >/dev/null )]" ;;
esac
check "就绪信息走 stderr，不脏 stdout" "" \
    "$( ( export RWEG_VERBOSE=1; . "$_env" ) 2>/dev/null )"
check "RWEG_VERBOSE=0 仍静默" "" "$( ( export RWEG_VERBOSE=0; . "$_env" ) 2>&1 )"

# ── 1c. 非侵入入口 env.sh：同名命令一个都不碰 ──
echo
echo "[1c] 非侵入入口 env.sh"
check "read 仍是 builtin（未被接管）" "builtin" "$(rshp type -t read)"
check "grep 未被接管（仍是 PATH 上的命令）" "file" "$(rshp type -t grep)"
check "hread 就绪" "function" "$(rshp type -t hread)"
check "hwrite / hedit / hgrep 就绪" "function/function/function" \
    "$(rshp bash -c 'printf "%s/%s/%s" "$(type -t hwrite)" "$(type -t hedit)" "$(type -t hgrep)"')"
check "undo / append / prepend（与 bash 不重名）就绪" "function/function/function" \
    "$(rshp bash -c 'printf "%s/%s/%s" "$(type -t undo)" "$(type -t append)" "$(type -t prepend)"')"
check "子进程也拿到 h*（export -f）" "function" "$(rshp bash -c 'type -t hread')"
# ── 2. 命令可用性 ──
echo
echo "[2] 命令可用性"
check "read -f app.ts 1:1" "CTt${SEP}function hello() {" \
	"$(rsh read -f app.ts 1:1 2>/dev/null | tail -1)"
check "read 范围 1:3 输出 3 行" "3" \
	"$(rsh read -f app.ts 1:3 2>/dev/null | tail -n +3 | grep -c "$SEP")"
check "grep -c hello（锚点化计数）" "2" "$(rsh grep -c hello app.ts 2>/dev/null)"
check "grep -n hello 带行号" "1" \
	"$(rsh grep -n hello app.ts 2>/dev/null | sed -n 2p | cut -d: -f1)"
check "grep 输出带锚点" "1:CTt${SEP}function hello() {" \
	"$(rsh grep -n hello app.ts 2>/dev/null | sed -n 2p)"

# 组合短选项：透传分支（无路径 / -q / -l）把参数交给 rg 时，`-qE` 必须拆成 `-q`
# 再把 E 吃掉；整串落到 rg 会被当成 `-E` = --encoding。
# ⚠️ 模式只用交替（`|`），不用 `{n}` 量词：rweg 是原生程序，起系统 grep 时参数要经
#    MSYS 的命令行解析，`{4}` 这类会被吃掉、grep 于是报"无匹配"—— 那是取参层面的
#    环境限制，与本条要验的短选项拆分无关；带上它，断言会随 PATH 上是系统 grep 还是
#    原生 grep（如 coreutils 的）而变。
check "grep -qE 组合短选项（透传分支）" "yes" \
    "$(echo "wAB12-Oc2hc.db" | rsh bash -c 'grep -qE "^(wAB12|zzzz)-Oc2hc[.]db$" && echo yes || echo no' 2>/dev/null)"
check "grep -cE 组合短选项（计数分支）" "1" \
    "$(rsh grep -cE "l+og" app.ts 2>/dev/null)"
check "grep -qG 组合里也有 BRE 报错" "2" \
    "$(rsh grep -qG foo app.ts >/dev/null 2>&1; echo $?)"
# write：创建 + 回传锚点
out="$(rsh write -f fresh.ts -c "$(printf 'alpha\nbeta')" 2>/dev/null)"
check "write 创建文件" "created fresh.ts (2 lines)" "$(printf '%s\n' "$out" | sed -n 1p)"
check "write 回传全文锚点" "2" "$(printf '%s\n' "$out" | tail -n +2 | grep -c "$SEP")"

# read → edit 主链路。⚠️ 必须在**同一次调用**里做：
# 每次 rsh 是独立子 shell（= 独立终端），会拿到不同的 ns，
# 而 ns 的时间戳粒度是秒，跨调用比较是 flaky 的。
# 顺便验证 export -f 生效：这一串跑在 bash -c 子进程里。
cp "$sandbox/app.ts" "$sandbox/copy.ts"
out="$(rsh bash -c 'read -f copy.ts 1:1 >/dev/null && edit -f copy.ts -c CTt:CTt "function hi() {" >/dev/null && sed -n 1p copy.ts' 2>/dev/null)"
check "read → edit 主链路（子进程里也成立）" "function hi() {" "$out"

# ── 3. 锚点与参考实现一致 ──
echo
echo "[3] 锚点对齐（与 dsh-better-edit 一致）"
anchors="$(rsh read -f app.ts 1:6 2>/dev/null | awk -F"$SEP" 'NF>1{print $1}' | tr '\n' ',')"
check "6 行锚点序列" "CTt,Um7,AU6,nVR,yAZ,6PY," "$anchors"

bodies="$(rsh read -f app.ts 1:6 2>/dev/null | awk -F"$SEP" 'NF>1{print $2}' | tr '\n' '|')"
check "6 行内容不变" \
	'function hello() {|  console.log("world");|}|const x = 1;|let y = 2;|export default hello;|' \
	"$bodies"

# ── 4. 范围语义 ──
echo
echo "[4] 范围语义"
# ⚠️ 范围参数一律**加引号**：`-3` / `4-` 这类以 - 开头的 token
# 在未加引号的上下文里可能被外层 shell 或命令替换吃掉。
blk() { # blk <期望> <范围参数...>
	local want="$1"; shift
	local got
	got="$(rsh read -f app.ts "$@" 2>/dev/null | sed -n 2p)"
	check "$*" "$want" "$got"
}
blk "4:6:" "4-"        # N- = N 到末尾
blk "1:3:" "-3"        # -M = 开头到 M
blk "3:3:" "3"         # 单行
blk "1:6:" "1:6"       # 闭区间
blk "1:4:" "1:3" "2:4" # 重叠 → 合并成 1:4
blk "1:6:" "1:3" "4:6" # 相邻 → 合并成 1:6
blk "1:3:" "5:6" "1:3" # 乱序 → 按起点排序
check "不相交范围分两块" "2" \
	"$(rsh read -f app.ts "1:3" "5:6" 2>/dev/null | grep -c '^[0-9]*:[0-9]*:$')"

# ── 5. 幂等 ──
echo
echo "[5] 幂等"
# 同一个 shell 内source 两次 → 第二次必须复用第一个 ns。
# 必须在**同一个** bash 进程里比：两个独立子 shell 是两个"终端"，
# 本来就该有不同 ns，拿它们比较是错的（踩过一次）。
ns_pair="$(rsh bash -c "unset RWEG_NS; . '$_env' >/dev/null 2>&1; a=\$RWEG_NS; . '$_env' >/dev/null 2>&1; b=\$RWEG_NS; echo \"\$a/\$b\"")"
check "重复 source ns 不变" "$(echo "$ns_pair" | cut -d/ -f1)/$(echo "$ns_pair" | cut -d/ -f2)" "$ns_pair"
check "ns 非空" "yes" "$([ -n "$(echo "$ns_pair" | cut -d/ -f1)" ] && echo yes || echo no)"
# PATH 里的 bin 是 POSIX 形态（MSYS 的 PATH 只认 ':' 分隔），而 $_home 两种写法都可能
# （RWEG_HOME 由调用者给：$HOME/.rweg 或 C:/...）—— 两种形态都算命中。
pcount="$(rsh bash -c ". '$_env' >/dev/null 2>&1; . '$_env' >/dev/null 2>&1; echo \$PATH" |
	tr ':' '\n' | path_hits "$_home/bin" "$(posix "$_home")/bin")"
check "PATH 未重复堆积" "1" "$pcount"
# 预置 RWEG_NS 不被覆盖
pre="wPRE0-00001"
got="$(rsh env RWEG_NS="$pre" bash -c "echo \$RWEG_NS")"
check "预置 RWEG_NS 不被覆盖" "$pre" "$got"

# ── 6. 配置跟随 cwd ──
echo
echo "[6] 配置跟随 cwd"
mkdir -p "$sandbox/deep/a/b"

# ⚠️ 不能写成 `RWEG_WALKUP=3 . env.sh`：那是给 source 命令加临时变量，
# source 之后 env.sh 里的子进程看不到它（实测踩过，walkup 仍是 0）。
# 正确写法：`export RWEG_WALKUP=3` 或 `RWEG_WALKUP=3 <要执行的命令>`。
cfg_at() { # cfg_at <深度> <子目录> <要提取的字段> [raw]
	(
		cd "$2" || exit 1
		export RWEG_WALKUP="$1"
		. "$_env" >/dev/null 2>&1
		# env.sh 在 source 时就把生效配置钉进 $RWEG_PIN_CONFIG；raw 模式
		# 手动取消，用来观察"没钉住"时的裸查找行为。
		[ "${4:-}" = raw ] && unset RWEG_PIN_CONFIG
		core-rweg cfg 2>/dev/null | sed -n "s/^\[cfg\] $3 *//p"
	)
}

out="$(cfg_at 0 "$sandbox/deep/a/b" config)"
case "$out" in
	*'$RWEG_PIN_CONFIG'*'rweg.toml'*) ok "walk-up=0 时深层子目录钉住安装级配置" ;;
	*) bad "walk-up=0 时深层子目录钉住安装级配置" "got [$out]" ;;
esac

# 反证：不钉（source 后 unset）时才回到"每次按 cwd 现找"的裸行为
out="$(cfg_at 0 "$sandbox/deep/a/b" config raw)"
case "$out" in
	*'$RWEG_HOME'*) ok "不钉时 walk-up=0 回落安装级（裸查找）" ;;
	*) bad "不钉时 walk-up=0 回落安装级（裸查找）" "got [$out]" ;;
esac

out="$(cfg_at 3 "$sandbox/deep/a/b" config)"
case "$out" in
	*rweg.toml*) ok "walk-up=3 命中项目配置" ;;
	*) bad "walk-up=3 命中项目配置" "got [$out]" ;;
esac

# 命中项目配置后 db 锚在**配置目录**，且用的是项目配置里的 .db/
out="$(cfg_at 3 "$sandbox/deep/a/b" dbdir)"
check "walk-up=3 时 dbdir 目录名 = .db（项目配置的值）" ".db" "$(basename "$out")"

# 反证：walk-up=0 回落安装级，取安装级 rweg.toml 里 [db] dir 声明的目录名。
# 不写死 "db"：安装级配置是用户可改的（install.sh 不覆盖已有的 rweg.toml）。
install_dbdir="$(sed -n 's/^[[:space:]]*dir[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' \
	"$_home/rweg.toml" 2>/dev/null | head -1)"
out0="$(cfg_at 0 "$sandbox/deep/a/b" dbdir)"
if [ -z "$install_dbdir" ]; then
	bad "安装级配置声明了 [db] dir" "no dir= in $_home/rweg.toml"
else
	check "walk-up=0 时 dbdir 目录名 = 安装级配置的值" \
		"$(basename "${install_dbdir%/}")" "$(basename "$out0")"
fi
# 两者必须是不同目录 —— 这正是"配置生效则db 跟着配置走"的证据
if [ "$out" != "$out0" ]; then
	ok "配置不同 → db 目录不同"
else
	bad "配置不同 → db 目录不同" "both [$out]"
fi

# ── 6b. 配置钉住：cd 不改判落点 ──
echo
echo "[6b] 配置钉住：cd 不改判落点"
# 这是 env.sh source 时钉配置的全部意义：进了没有 rweg.toml 的深层子目录，
# 配置与库都不换，cd 前 read 拿到的锚点 cd 后照旧能用。
printf 'p1\np2\np3\n' > "$sandbox/pin.txt"
out="$(rsh bash -c "cd '$sandbox' && . '$_env' >/dev/null 2>&1 && { \
	read -f pin.txt 2:2 >/dev/null; \
	a=\$(read -f pin.txt 2:2 | tail -1 | awk -F'$SEP' '{print \$1}'); \
	p1=\$RWEG_PIN_CONFIG; \
	d1=\$(core-rweg cfg 2>/dev/null | sed -n 's/^\[cfg\] db  *//p'); \
	cd deep/a/b; \
	d2=\$(core-rweg cfg 2>/dev/null | sed -n 's/^\[cfg\] db  *//p'); \
	edit -f ../../../pin.txt -c \"\$a\" P2 >/dev/null 2>&1; \
	echo \"PIN=\$p1\"; echo \"DB1=\$d1\"; echo \"DB2=\$d2\"; sed -n 2p ../../../pin.txt; }")"
check "cd 后 db 落点不变（钉住生效）" \
	"$(printf '%s\n' "$out" | sed -n 's/^DB1=//p')" "$(printf '%s\n' "$out" | sed -n 's/^DB2=//p')"
check "cd 后用 cd 前拿到的锚点仍可编辑" "P2" "$(printf '%s\n' "$out" | tail -1)"
check "钉住的是项目配置" "rweg.toml" \
	"$(printf '%s\n' "$out" | sed -n 's/^PIN=//p' | sed 's/.*[\\/]//')"

# ── 7. 数据库落点 ──
echo
echo "[7] 数据库落点"
out="$(rsh core-rweg cfg 2>/dev/null | sed -n 's/^\[cfg\] db  *//p')"
# db 路径形如 <配置目录>/.db/<ns>.db。
# 分隔符按平台：Windows 反斜杠，Unix 正斜杠 —— 所以用字符类而非字面量。
dbtail="${out##*[\\/]}"          # 最后一段 = <ns>.db
dbdir="${out%[\\/]$dbtail}"      # 去掉文件名 = <配置目录>/.db
check "db 文件名 = <ns>.db" "yes" \
	"$(echo "$dbtail" | grep -qE '^w[A-Za-z0-9]{4}-[A-Za-z0-9]{5}\.db$' && echo yes || echo "no ($dbtail)")"
check "db 目录名 = .db" ".db" "$(basename "$dbdir")"
# db 落在**沙箱**里（不是安装目录）：配置随 cwd 走，库跟着配置走。
# 比前缀而不是走 grep：core-rweg 报的是 Windows 形态（反斜杠），大小写不作数。
sb_win="$(low "$(win "$sandbox")")"
case "$(low "$out")" in
	"$sb_win"*) ok "db 在沙箱目录内（随沙箱的 rweg.toml）" ;;
	*) bad "db 在沙箱目录内（随沙箱的 rweg.toml）" "got [$out] want under $sb_win" ;;
esac

# ── 8. 退出码 ──
echo
echo "[8] 退出码"
# ⚠️ 必须用 `rc=$?` 单独取：`rsh ... ; check ... "$?"` 里
# rsh 是函数调用，函数返回后 $? 已被后续命令污染（踩过一次）。
rc() { rsh "$@" >/dev/null 2>&1; echo $?; }
check "文件不存在 → 1" "1" "$(rc read -f nosuch.txt 1:1)"
check "范围越界 → 2"     "2" "$(rc read -f app.ts 999)"
check "缺 -f → 2"        "2" "$(rc read)"
check "正常 → 0"         "0" "$(rc read -f app.ts 1:1)"
check "目录 → 1"         "1" "$(rc read -f .)"
# grep 退出码透传。
# ⚠️ 路径一律用**相对**的：rsh 已 cd 进沙箱。绝对 MSYS 路径（/tmp/...）在
#    MSYS_NO_PATHCONV=1 的环境里（dsh 等 agent shell）会原样传给原生 rg，
#    于是报 IO error、退出码 2（实测）。
rsh grep -q zzzznotfound app.ts 2>/dev/null
check "grep 无匹配 → 1（退出码透传）" "1" "$?"
rsh grep -q hello app.ts 2>/dev/null
check "grep 有匹配 → 0（退出码透传）" "0" "$?"

# ── 汇总 ──
echo
if [ "$fail" -eq 0 ]; then
	echo "shell test: $pass passed, 0 failed"
	exit 0
fi
echo "shell test: $pass passed, $fail FAILED"
exit 1