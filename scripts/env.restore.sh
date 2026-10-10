#!/usr/bin/env bash
# rweg env.restore.sh — 反注册 env.aggressive.sh 装的那层同名函数，
# 默认把 env.aggressive.sh 那一层拆掉：read / write / edit / grep 与 cd 还给 bash 内置与 PATH。
#
# 用法（**必须 source**：要改的是当前 shell 的函数表）：
#     source ~/.rweg/scripts/env.restore.sh          # 只拆同名接管层
#     source ~/.rweg/scripts/env.restore.sh --all    # 连 env.sh 那层一起拆
#
# 只拆 rweg 自己装的，判据两条同时成立：同名的 h* 函数在（hread / hwrite / …），
# 且函数体里出现 h* 转发；cd 认它那条 WARN 文案。别的来源的同名函数原样保留，
# 并在 stderr 说一声是谁占着。`unset -f` 会连带清掉 BASH_FUNC_* 导出，
# 所以从那之后起的子进程也回到内置。
#
# --all 还会：unset hread / hwrite / hedit / hgrep / hundo / happend / hprepend /
# hundo_last_edit / undo / append / prepend / core-rweg 十二个函数、把 $RWEG_BIN 从 PATH 里摘掉、
# unset RWEG_NS /
# RWEG_PIN_CONFIG / RWEG_HOME / RWEG_BIN / _rweg_core —— 即回到"从未 source 过"。
# 注意 RWEG_NS 一丢，本终端已 serve 的锚点就用不了了（重 source 会另起一个 ns）。
#
# 幂等：没装过也能跑。成功时静默；RWEG_VERBOSE=1 时往 stderr 打一行结果。

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
	echo "env.restore.sh: 它要改当前 shell 的函数表，必须 source 而不能直接执行：" >&2
	echo "    source \"${BASH_SOURCE[0]}\"" >&2
	exit 1
fi

_rweg_all=0
case "${1:-}" in
	--all | -a | all) _rweg_all=1 ;;
	"") ;;
	*)
		echo "env.restore.sh: 未知参数 ${1}（只认 --all）" >&2
		return 2
		;;
esac

_rweg_dropped=""
_rweg_kept=""

# --- 1. 接管层（env.aggressive.sh 装的同名命令）：带 rweg 痕迹的才拆 ---
#      undo / append / prepend 是 env.sh 装的不重名函数，不在这一层，--all 才拆。
for _rweg_fn in read write edit grep; do
	_rweg_body="$(declare -f "$_rweg_fn" 2>/dev/null)" || continue
	if ! declare -F "h$_rweg_fn" >/dev/null 2>&1; then
		_rweg_kept="$_rweg_kept $_rweg_fn"
		continue
	fi
	case "$_rweg_body" in
		*"h$_rweg_fn"*'"$@"'*) ;;
		*) _rweg_kept="$_rweg_kept $_rweg_fn"; continue ;;
	esac
	unset -f "$_rweg_fn"
	_rweg_dropped="$_rweg_dropped $_rweg_fn"
done
_rweg_body="$(declare -f cd 2>/dev/null)" || _rweg_body=""
if [ -n "$_rweg_body" ]; then
	case "$_rweg_body" in
		*"cd command ERROR: do not call"* | *"WARN:cd command is not recommended"*)
			unset -f cd
			_rweg_dropped="$_rweg_dropped cd"
			;;
		*) _rweg_kept="$_rweg_kept cd" ;;
	esac
fi

# --- 2. --all：env.sh 那层（h* / PATH / ns / 配置钉住）也拆 ---
if [ "$_rweg_all" = 1 ]; then
	for _rweg_fn in hread hwrite hedit hgrep hundo happend hprepend hundo_last_edit undo append prepend core-rweg; do
		unset -f "$_rweg_fn" 2>/dev/null || true
	done
	if [ -n "${RWEG_BIN:-}" ]; then
		_rweg_p="$PATH"
		_rweg_p="${_rweg_p#"$RWEG_BIN:"}"      # 首位（env.sh 就是前置到首位）
		_rweg_p="${_rweg_p//":$RWEG_BIN:"/:}"  # 中间
		_rweg_p="${_rweg_p%":$RWEG_BIN"}"      # 末位
		PATH="$_rweg_p"
		export PATH
	fi
	unset RWEG_NS RWEG_PIN_CONFIG RWEG_HOME RWEG_BIN _rweg_core
fi

if [ -n "$_rweg_kept" ]; then
	echo "env.restore.sh: 这些同名函数不是 rweg 装的，保持原样:$_rweg_kept" >&2
fi
if [ -n "${RWEG_VERBOSE:-}" ] && [ "${RWEG_VERBOSE}" != "0" ]; then
	if [ "$_rweg_all" = 1 ]; then
		echo "rweg 反注册（--all）：同名命令与 h* 层都已拆，PATH / ns 恢复原状" >&2
	else
		echo "rweg 反注册：read/write/edit/grep/cd 回到内置与 PATH（env.sh 那层保留，--all 可一并拆）" >&2
	fi
fi

unset _rweg_all _rweg_dropped _rweg_kept _rweg_fn _rweg_body _rweg_p
