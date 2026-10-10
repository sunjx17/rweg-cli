#!/usr/bin/env bash
# rweg build.sh — 编译 core-rweg 并在 /dist/ 里铺好套件布局。

# 产物布局（与 $RWEG_HOME 完全一致，install.sh 只是把它搬过去）：
#   ./dist/
#   ├── bin/
#   │   └── core-rweg.exe        ← 唯一二进制；工具入口是它的子命令
#   ├── scripts/
#   │   ├── env.sh               ← 非侵入：只加 hread / hwrite / hedit / hgrep / hundo
#   │   ├── env.aggressive.sh    ← 接管 read / write / edit / grep / undo 与 cd 提示
#   │   └── env.restore.sh       ← 反注册：把接管层拆掉，恢复内置
#   ├── db/
#   └── rweg.toml
#
# 用法：
#   ./scripts/build.sh              编译 + 铺布局 + 跑 selftest
#   ./scripts/build.sh --no-test    跳过 selftest

set -euo pipefail

_root="$(builtin cd -P "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
builtin cd "$_root"

_dist="$_root/dist"
_bin="$_dist/bin"
_exe="$_bin/core-rweg.exe"
[ "${OS:-Windows}" = "Windows_NT" ] || _exe="$_bin/core-rweg"

run_test=1
[ "${1:-}" = "--no-test" ] && run_test=0

# 交给**原生**程序（go、core-rweg.exe）的路径要转成 Windows 形态。
# MSYS 默认替我们转，但 MSYS_NO_PATHCONV=1 时（dsh 等 agent shell 就这么设）
# /c/Users/... 原样传进去 = 当前盘根下的 C:\c\Users\...：
# `go build -o` 会把产物写到那个鬼地方，dist/bin 里留的仍是旧二进制
# —— 表现为"改了代码、build 也过了，跑起来还是老行为"。
native() {
	if command -v cygpath >/dev/null 2>&1; then
		cygpath -w "$1"
	else
		printf '%s' "$1"
	fi
}

echo "==> go build"
mkdir -p "$_bin" "$_dist/db" "$_dist/scripts"

# -s -w 去掉符号表与DWARF，实测 6.85 MB（含 SQLite）
go build -trimpath -ldflags="-s -w" -o "$(native "$_exe")" ./cmd/core-rweg
echo "    $_exe  ($(du -h "$_exe" | cut -f1))"

# bin/ 里只留 core-rweg：旧版本造的 read.exe / grep.exe 之类的硬链接指着旧
# inode，留着就是"改了代码、跑起来还是老行为"。
for _legacy in read write edit grep undo undo_last_edit; do
        rm -f "$_bin/$_legacy" "$_bin/$_legacy.exe"
done

echo "==> stage scripts + config"
cp -f "$_root/scripts/env.sh" "$_dist/scripts/env.sh"
cp -f "$_root/scripts/env.aggressive.sh" "$_dist/scripts/env.aggressive.sh"
cp -f "$_root/scripts/env.restore.sh" "$_dist/scripts/env.restore.sh"
chmod +x "$_dist/scripts/env.sh" "$_dist/scripts/env.aggressive.sh" "$_dist/scripts/env.restore.sh" 2>/dev/null || true
cp -f "$_root/dist-home/rweg.toml" "$_dist/rweg.toml"

if [ "$run_test" = "1" ]; then
	echo "==> selftest"
	"$_exe" selftest
fi

echo
echo "build done -> $_dist"
echo "install:    ./scripts/install.sh          # 装到 \$RWEG_HOME（默认 ~/.rweg）"
echo "try it:     source $_dist/scripts/env.sh && core-rweg cfg"