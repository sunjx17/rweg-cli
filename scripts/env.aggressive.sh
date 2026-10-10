#!/usr/bin/env bash
# rweg env.aggressive.sh — 在 env.sh 之上**接管同名命令**（侵入入口）。
#
# 先 source 同目录的 env.sh（路径 / PATH / ns / 配置钉住 / h* 工具），再加一层：
#
#   read / write / edit / grep 
#       同名函数覆盖 —— read 压过 bash 内置，grep 压过系统 grep。
#       read 只在下列情形归 rweg：出现 rweg 独有 flag（-f / --file / --json /
#       --no-serve / --encoding / --quiet / -h）；零参数；或简写
#       `read FILE [RANGE ...]`（首参是存在的文件，且其后全是范围写法）。
#       其余（read line、read -r line、read -p ...）交还 builtin read，
#       并带 RWEG_READ_TIMEOUT（缺省 5 秒）有界等待。
#   cd
#       只加一行提示，**不 export -f**：导出后子脚本里的 `$(cd ... && pwd)`
#       会把提示文本一起收进变量。
#
# 这套函数随 `export -f` 进子 bash，所以脚本里的 `while read -r x` 必须仍能
# 落回 bash 语义 —— read 的转发判据就是为它留的。
#
# 成功时静默；RWEG_VERBOSE=1 时多打一行接管说明（走 stderr）。

_aggr_src="${BASH_SOURCE[0]}"
_aggr_dir="$(builtin cd -P "$(dirname "$_aggr_src")" && pwd)"
. "$_aggr_dir/env.sh" || { unset _aggr_src _aggr_dir; return 1; }
unset _aggr_src _aggr_dir

# --- read：rweg / bash 内置双语义 ---
read() {
	# 零参数：交给 rweg。它会明确报"缺文件"，而不是像 builtin read 那样
	# 在交互式终端上干等一行输入，把 agent 卡死。
	if [ "$#" -eq 0 ]; then
		hread
		return $?
	fi

	local _a
	for _a in "$@"; do
		case "$_a" in
			# rweg 自己的 flag：整体交给 rweg
			-f | --file | --file=* | -h | --help | --json | --no-serve | --encoding | --encoding=* | --quiet)
				hread "$@"
				return $?
				;;
		esac
	done

	# 简写 read FILE [RANGE ...]：第一个参数是**存在的文件**，且后面给的全是
	# 范围写法（1 / 2:5 / 3- / -9）。名字不带 . 或 / 一样算文件（notes、
	# Makefile），所以 `read notes` 与 `read notes 1:5` 行为一致。
	# `read line`、`read ans` 这类脚本写法（无同名文件）照旧走 builtin。
	if [ -f "$1" ]; then
		local _ranges=1 _r
		for _r in "${@:2}"; do
			case "$_r" in
				[0-9]* | -[0-9]*) ;;
				*) _ranges=0 ;;
			esac
		done
		if [ "$_ranges" = 1 ]; then
			hread "$@"
			return $?
		fi
	fi

	# 兜底 = bash 内置语义（read line / read -r line / read -p ... / read -a arr ...）。
	# 无重定向时内置 read 会一直等 stdin，这里统一加有界等待：
	#   - 重定向 / 管道里已有数据：立即返回，与原生 builtin read 同语义
	#   - 到点仍无数据：返回 >128，并在 stderr 指出改用 read FILE
	#   - RWEG_READ_TIMEOUT=0：恢复无限等待（慢生产者、交互式长输入）
	# RWEG_READ_TIMEOUT 解析：off / no 与 0 同义；非法值按缺省 5 秒；08 这类按
	# 十进制（read -t 拒绝八进制字面量）。
	local _t="${RWEG_READ_TIMEOUT:-5}"
	case "$_t" in
		off | no) _t=0 ;;
		*[!0-9]*) _t=5 ;;
		*) _t=$((10#$_t)) ;;
	esac
	if [ "$_t" = 0 ]; then
		builtin read "$@"
		return $?
	fi
	builtin read -t "$_t" "$@"
	local _rc=$?
	if [ "$_rc" -gt 128 ]; then
		echo "read: 等 stdin 超过 ${_t}s 未果，已放弃（RWEG_READ_TIMEOUT=0 可关掉上限）" >&2
		echo "      读文件用 'read FILE'（存在即归 rweg）或 'read -f FILE'；脚本读变量写 builtin read" >&2
	fi
	return "$_rc"
}
write()     { hwrite "$@"; }
edit()      { hedit  "$@"; }
grep()      { hgrep  "$@"; }
# --- cd：只提示，不改行为（且不 export -f）---
cd(){
	# cd 包装只给交互提示：**不 export -f**（原因见下面的 export 段），
	# 且一律走 stderr —— stdout 会被 $(cd ... && pwd) 这类命令替换收走。
	echo "WARNING:cd command is not recommended in this shell." >&2 # 给不太聪明的agent的提示，避免它在shell里过度cd；aggressive默认开启。
	last__cwd=$(pwd)
	builtin cd "$@"
	if [ $? -eq 0 ]; then
	{
		if [ "$last__cwd" == $(pwd) ]; then
		{
			# 给过度谨慎的agent的提示，避免它在shell里重复cd到同一路径（持久shell里没有必要每行都在开头加cd {cwd}）。默认开启，停用只需注释掉。
			echo "cd command ERROR: do not call 'cd $(pwd)' command more than twice." >&2
		}
		else
			# 给agent的提示，同样避免它在shell里重复cd到同一路径。默认开启，停用只需注释掉。
			echo "cd: cwd changed to '$(pwd)'" >&2
		fi
	}
	fi
}

if [ -n "${BASH_VERSION:-}" ]; then
	# ⚠️ 这份名单里**没有 cd**：cd 包装只做交互提示，一旦 export -f 就会被
	# 每个子脚本继承，把 `root="$(cd ... && pwd)"` 的结果污染成警告文本
	# （rweg 自己的 scripts/build.sh 就这么崩过）。子脚本一律用 bash 内置 cd。
	export -f read write edit grep undo 2>/dev/null || true
fi

if [ -n "${RWEG_VERBOSE:-}" ] && [ "${RWEG_VERBOSE}" != "0" ]; then
	echo "  接管：read / write / edit / grep （同名覆盖），cd 只加提示" >&2
fi
