#!/usr/bin/env bash
# rweg env.sh — 把 rweg 套件引入当前 shell 会话（**非侵入**入口）。
#
# 入口有两个，任选其一：
#     source ~/.rweg/scripts/env.sh              # 本文件：只加 h* 名字
#     source ~/.rweg/scripts/env.aggressive.sh   # 另把 read/write/edit/grep/
#                                                # undo 与 cd 提示接管（同名覆盖）
#
# 本文件做四件事：
#   1. PATH 前置 $RWEG_HOME/bin —— 只为 `core-rweg` 能直接敲
#   2. 生成当前终端专属的 namespace（RWEG_NS）—— 终端关闭即消失，无全局状态
#   3. 记下 RWEG_HOME / RWEG_BIN / 工具路径，供函数与脚本定位
#   4. 后台跑一次 GC —— 清理过期 namespace / 快照
#
# 工具是 core-rweg 的子命令（bin 里只有这一个二进制），本层给 h 前缀别名：
#     hread / hwrite / hedit / hgrep / hundo / hundo_last_edit
# read / write / edit / grep / undo 与 bash 原生 cd **一个都不动**。
#
# 幂等：同一终端重复 source 不产生第二个 ns、PATH 不堆积。
# 可在任意目录 source：配置与数据库跟随 cwd 自动切换。
#
# **成功时静默**：stdout 与 stderr 都不输出，source 的输出可安全被管道 /
# 命令替换捕获。要人就绪信息：RWEG_VERBOSE=1 source env.sh（走 stderr）。
#
# 向上查找 rweg.toml 的父级深度由 RWEG_WALKUP 决定（默认 0 = 只找 cwd）：
#   export RWEG_WALKUP=1      # cwd + 父目录
#   RWEG_WALKUP=2 core-rweg cfg# 单次调用

# --- 定位本文件所在目录（含软链接解析）---
_rweg_src="${BASH_SOURCE[0]}"
while [ -L "$_rweg_src" ]; do
	_rweg_dir="$(builtin cd -P "$(dirname "$_rweg_src")" && pwd)"
	_rweg_src="$(readlink "$_rweg_src")"
	[[ $_rweg_src != /* ]] && _rweg_src="$_rweg_dir/$_rweg_src"
done
_rweg_dir="$(builtin cd -P "$(dirname "$_rweg_src")" && pwd)"

# scripts/ 的上一级就是安装目录。两种形态各归其位：
#
#   RWEG_HOME —— 导出**原生**形态（C:/Users/...），因为 core-rweg 是原生程序：
#     MSYS 只替 PATH 这类变量做转换，别的环境变量原样递过去。给 /c/... 或
#     /tmp/... 的话，filepath.Join 会拼出 C:\c\...、C:\tmp\...（当前盘根下的
#     怪路径）：安装级 rweg.toml 找不到、库落错地方。cygpath 还知道 /tmp
#     映射到哪，纯 Go 侧的字符串规则做不到。
#
#   RWEG_BIN —— 保持 **POSIX** 形态：MSYS 的 PATH 用 ':' 分隔，塞进 C:/...
#     会被按盘符冒号切开（实测 PATH 首项变成 "C;C:\Program Files\Git\Users\..."）。
#     它同时用于 exec "$_rweg_core"（core-rweg），POSIX 形态 exec 没有问题。
#
# 非 MSYS（没有 cygpath）时两者同形，行为与普通 Unix 一致。
_rweg_home_posix="$(builtin cd -P "$_rweg_dir/.." && pwd)"
_rweg_home_native="$_rweg_home_posix"
if command -v cygpath >/dev/null 2>&1; then
	_rweg_home_native="$(cygpath -m "$_rweg_home_posix")"
fi
export RWEG_HOME="$_rweg_home_native"
export RWEG_BIN="$_rweg_home_posix/bin"

_rweg_core="$RWEG_BIN/core-rweg.exe"
[ -x "$_rweg_core" ] || _rweg_core="$RWEG_BIN/core-rweg"

# _rweg_core 必须 export：下面的 h* 函数会随 export -f 进子进程，
# 函数体在**调用时**才展开这个路径变量。
export _rweg_core

# --- 前置检查：二进制在不在 ---
if [ ! -x "$_rweg_core" ]; then
	echo "rweg: core-rweg not found at $_rweg_core" >&2
	echo "       expected layout: \$RWEG_HOME/{bin,scripts,db,rweg.toml}" >&2
	echo "       RWEG_HOME is currently: $RWEG_HOME" >&2
	unset _rweg_src _rweg_dir _rweg_core
	return 1 2>/dev/null || exit 1
fi

# --- 0. 钉住配置文件路径 ---
#
# source 的这一刻就把"生效配置是哪一个"算出来钉进 $RWEG_PIN_CONFIG：
#   - 之后 cd 到别处（子目录里往往没有 rweg.toml）不再改判落点，库位置与
#     served 基线都跟着不变 —— 锚点不会因为换个目录就作废
#   - 同一 shell 里想换项目：export RWEG_PIN_CONFIG=<另一个 rweg.toml 路径>
#     （或重 source）；设成空串则回到"每次按 cwd 现找"
# 找不到配置文件（走内置默认）时留空不导出，工具照旧现找。
RWEG_PIN_CONFIG="$("$_rweg_core" cfg --path 2>/dev/null || true)"
if [ -n "$RWEG_PIN_CONFIG" ]; then
export RWEG_PIN_CONFIG
else
unset RWEG_PIN_CONFIG
fi

# --- 1. PATH 前置（去重，避免反复 source 堆积）---
# bin 里只有 core-rweg 一个二进制，前置只图 `core-rweg` 能直接敲；
# 不再有 grep.exe / read 之类的名字抢系统命令（那是旧硬链接布局的事）。
case ":${PATH}:" in
	*":$RWEG_BIN:"*) ;;
	*) export PATH="$RWEG_BIN:${PATH}" ;;
esac

# --- 1b. h* 工具函数（不覆盖任何同名命令）---
#
# 工具入口统一走 core-rweg 的子命令形态：装出来的 bin 里只有 core-rweg 一个
# 二进制，没有 read.exe / grep.exe 之类的 argv[0] 硬链接。
# 这一层只加 h 前缀名；要 read / write / edit / grep / undo 与 cd 提示被接管，
# source env.aggressive.sh（它在 env.sh 之上再加一层）。
#
# **`export -f` 不能省**：不 export 的话，子进程（`bash script.sh`、
# dsh 派生的脚本、make 里的命令行）里就没有这些函数。
# ⚠️ **顺序必须是先定义、再 export**：export 一个还不存在的函数是静默 no-op。
hread()  { "$_rweg_core" read  "$@"; }
hwrite() { "$_rweg_core" write "$@"; }
hedit()  { "$_rweg_core" edit  "$@"; }
hgrep()  { "$_rweg_core" grep  "$@"; }
hundo()  { "$_rweg_core" undo  "$@"; }
# dsh-hashline-edittool 里的工具名，给个同名入口，省得两套名字来回记。
hundo_last_edit() { "$_rweg_core" undo "$@"; }
core-rweg() { "$_rweg_core" "$@"; }

if [ -n "${BASH_VERSION:-}" ]; then
	export -f hread hwrite hedit hgrep hundo hundo_last_edit core-rweg 2>/dev/null || true
fi

# --- 2. namespace：已存在则复用 ---
# 三种情况都靠这一条覆盖：
#   - 首次 source → 生成
#   - 同一终端重复 source → 复用（否则自己把自己搞乱）
#   - 手动 export RWEG_NS=... → 保留（支持多终端共享 served 状态）
#
# 注意：先赋值、再判断非空。不要写成
#   if RWEG_NS="$("$_rweg_core" ns new)"; then
# 嵌套引号会让 bash 解析错乱（实测踩过：ns 被赋成字面量）。
if [ -z "${RWEG_NS:-}" ]; then
	RWEG_NS="$("$_rweg_core" ns new 2>/dev/null)"
	if [ -n "$RWEG_NS" ]; then
		export RWEG_NS
	else
		# 生成失败不阻塞使用：unset 掉，工具内部按 cwd 派生稳定 ns（隔离性降级）
		unset RWEG_NS
		echo "rweg: ns generation failed; falling back to cwd-derived namespace" >&2
	fi
fi

# --- 3. 惰性 GC（后台，失败静默——不影响本次使用）---
#
# ⚠️ GC 必须在 ns 生成之后跑，且用 `ns gc` 而不是裸 `gc`：
# 它要操作"本终端所在配置目录"下的库，而不是随便哪个库。
( "$_rweg_core" ns gc >/dev/null 2>&1 & ) 2>/dev/null

unset _rweg_src _rweg_dir _rweg_home_posix _rweg_home_native

# --- 自清理钩子：**刻意不做** ---
#
# 曾经用`trap 'unset RWEG_NS' EXIT` 在 shell 退出时立刻标记失效，
# 但实测这个做法**有害**：
#
#   bash 的子 shell（`$(...)` / `(...)` / 管道）退出时会**继承并触发** EXIT trap。
#   也就是说 `x=$(read -f f.txt)` 跑完，RWEG_NS 就被清掉了；
#   下一次调用只好重新派生 ns，于是撞上 strict_cwd 检查报
#   E_NS_CWD_MISMATCH —— 一个"退出即清理"的优化，坑掉了同一终端内的正常使用。
#
# namespace 的实际回收本来就靠 TTL（默认 24h，见 §5.4），
# 终端关掉后数据库过期被清掉即可。**这里不做任何 EXIT 钩子。**
#
# 若确实要立刻释放：手动 `unset RWEG_NS` 或 `core-rweg gc --force`。

# --- 4. 就绪提示：默认静默 ---
#
# source 的输出常被管道 / 命令替换 / 日志捕获，几行欢迎语就是噪声，
# 所以成功时一个字都不打。人类想确认装好了：RWEG_VERBOSE=1 source env.sh；
# 提示走 stderr，不脏 stdout。装没装好的权威判据是 `core-rweg doctor`。
if [ -n "${RWEG_VERBOSE:-}" ] && [ "${RWEG_VERBOSE}" != "0" ]; then
	{
		echo "rweg ready (home=$RWEG_HOME, ns=${RWEG_NS:-<cwd-derived>}, walkup=${RWEG_WALKUP:-0})"
		echo "  h* 工具就绪：hread / hwrite / hedit / hgrep / hundo / hundo_last_edit"
		echo "  要同名接管（read/write/edit/grep/undo + cd 提示）：source env.aggressive.sh"
		echo "  诊断：core-rweg cfg | core-rweg doctor；Agent 提示词：core-rweg prompt"
		echo "  配置钉住：${RWEG_PIN_CONFIG:-<无，按 cwd 现找>}"
echo "  向上找配置：RWEG_WALKUP=2 core-rweg cfg"
	} >&2
fi