#!/usr/bin/env bash
# rweg env.restore.sh — 反注册 env.sh 装进当前 shell 的一切。
#
# 用法（**必须 source**：要改的是当前 shell 的函数表）：
#     source ~/.rweg/scripts/env.restore.sh          # 拆掉 env.sh 装的函数（h* 与 undo / append / prepend / core-rweg）
#     source ~/.rweg/scripts/env.restore.sh --all    # PATH / ns / 配置钉住也一并还原，回到"从未 source 过"
#
# env.sh 只装一层，且**不接管任何已有名字** —— read / write / edit / grep / cd
# 从头到尾没被碰过，反注册自然也不必碰它们：你自带的同名函数一律原样保留。
#
# 只拆 rweg 自己装的：判据是**源码形状**（declare -f 打印的就是源码，变量不展开）
# —— 函数体里出现 `"$_rweg_core"` 才是 rweg 的转发函数。刻意写成带引号的字面量
# 而非依赖 $_rweg_core 的**值**：值在这一层可能为空，空值会让模式退化成 *" 空"*
# 之类的宽匹配，把恰好含该字样的别的函数误拆。
# `unset -f` 会连带清掉 BASH_FUNC_* 导出，从那之后起的子进程也拿不到这些函数。
#
# --all 还会：把 $RWEG_BIN 从 PATH 里摘掉、unset RWEG_NS / RWEG_PIN_CONFIG /
# RWEG_HOME / RWEG_BIN / _rweg_core。
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

# --- 1. env.sh 装的那层：带 rweg 痕迹的才拆，别人的同名函数原样留着 ---
#
# ⚠️ 名单里没有 read / write / edit / grep / cd：env.sh 没定义它们，
# 这一层也不去 unset 它们 —— 那是用户自己的东西。
for _rweg_fn in hread hwrite hedit hreplace hgrep hundo happend hprepend hundo_last_edit undo append prepend core-rweg; do
	_rweg_body="$(declare -f "$_rweg_fn" 2>/dev/null)" || continue
	case "$_rweg_body" in
		*'"$_rweg_core"'*) ;;
		*) _rweg_kept="$_rweg_kept $_rweg_fn"; continue ;;
	esac
	unset -f "$_rweg_fn"
	_rweg_dropped="$_rweg_dropped $_rweg_fn"
done

# --- 2. --all：PATH 与环境变量也还原 ---
if [ "$_rweg_all" = 1 ]; then
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
		echo "rweg 反注册（--all）：h* 层与 PATH / ns / 配置钉住都拆了，回到从未 source 过" >&2
	else
		echo "rweg 反注册：env.sh 装的函数已拆（PATH / ns 保留，--all 可一并还原）" >&2
	fi
fi

unset _rweg_all _rweg_dropped _rweg_kept _rweg_fn _rweg_body _rweg_p
