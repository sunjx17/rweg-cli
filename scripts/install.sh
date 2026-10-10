#!/usr/bin/env bash
# rweg install.sh — 把 ./dist/ 铺到安装目录。
#
# 来源是 `./dist/`（build.sh 的产物目录，不跟踪）。
#
# 安装目录：$RWEG_HOME，缺省 ~/.rweg（可改，export RWEG_HOME=/d/tools/rweg 即可）。
#
# 装完的布局：
#   $RWEG_HOME/
#   ├── bin/         core-rweg —— 唯一二进制（工具入口是它的子命令）
#   ├── scripts/
#   │   ├── env.sh              非侵入入口：hread / hwrite / hedit / hgrep / hundo
#   │   ├── env.aggressive.sh   侵入入口：同名接管 + cd 提示
#   │   └── env.restore.sh      反注册：source 它恢复内置
#   ├── db/          安装级默认数据库目录
#   └── rweg.toml    安装级默认配置（cwd 没有 rweg.toml 时生效）
#
# 不碰 /usr/bin、不覆盖全局 PATH —— 用法是 source $RWEG_HOME/scripts/env.sh
# （或 env.aggressive.sh）。目标 bin 里旧版本留下的 read.exe / grep.exe 之类的
# 硬链接会被清掉。
#
# 用法：
#   ./scripts/install.sh              装到 $RWEG_HOME（默认 ~/.rweg）
#   ./scripts/install.sh /d/tools/rweg 装到指定目录
#   RWEG_HOME=/d/tools/rweg ./scripts/install.sh

set -euo pipefail

_root="$(builtin cd -P "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
_dist="$_root/dist"

# 目标目录：命令行参数 > RWEG_HOME > ~/.rweg
if [ "$#" -ge 1 ]; then
	_home="$1"
elif [ -n "${RWEG_HOME:-}" ]; then
	_home="$RWEG_HOME"
else
	_home="$HOME/.rweg"
fi

if [ ! -x "$_dist/bin/core-rweg.exe" ] && [ ! -x "$_dist/bin/core-rweg" ]; then
	echo "install: $_dist not built. Run ./scripts/build.sh first." >&2
	exit 1
fi
echo "==> RWEG_HOME = $_home"

mkdir -p "$_home/bin" "$_home/scripts" "$_home/db"

# --- bin ---
# 只装 core-rweg 一个二进制：read / write / edit / grep / undo 是 env.sh 里的
# bash 函数（core-rweg 的子命令），不再有 argv[0] 硬链接。
# 旧版本装出来的工具名文件一并清掉 —— 它们指向旧 inode，留着就是"改了代码、
# 跑起来还是老行为"。
for _legacy in read write edit grep undo undo_last_edit; do
        rm -f "$_home/bin/$_legacy" "$_home/bin/$_legacy.exe"
done
if [ -f "$_dist/bin/core-rweg.exe" ]; then
        cp -f "$_dist/bin/core-rweg.exe" "$_home/bin/"
else
        cp -f "$_dist/bin/core-rweg" "$_home/bin/"
fi

# --- scripts ---
# 从 scripts/ 直接拷，不走 ./dist/ 的副本。
# 产物目录只是 build 的中间结果，可能因「改了 scripts/ 但没重新 build」而陈旧——
# 踩过一次：export -f 的修复没生效，装出来的 env.sh 还是旧的。
# env.sh 的真源在 scripts/，直接用。
cp -f "$_root/scripts/env.sh" "$_home/scripts/env.sh"
cp -f "$_root/scripts/env.aggressive.sh" "$_home/scripts/env.aggressive.sh"
cp -f "$_root/scripts/env.restore.sh" "$_home/scripts/env.restore.sh"
cp -f "$_root/scripts/shell-test.sh" "$_home/scripts/shell-test.sh" 2>/dev/null || true
cp -f "$_root/scripts/e2e-test.sh" "$_home/scripts/e2e-test.sh" 2>/dev/null || true
chmod +x "$_home/scripts/env.sh" "$_home/scripts/env.aggressive.sh" "$_home/scripts/env.restore.sh" 2>/dev/null || true

# --- rweg.toml ---
# 已存在则**不覆盖**：用户可能已经调过 dbpath 之类。
if [ -f "$_home/rweg.toml" ]; then
	echo "    rweg.toml exists, kept as-is"
	cp -f "$_home/rweg.toml" "$_home/rweg.toml.bak.$(date +%Y%m%d%H%M%S)" 2>/dev/null || true
	cp -f "$_dist/rweg.toml" "$_home/rweg.toml.new" 2>/dev/null || true
	echo "    (new default written to rweg.toml.new for your reference)"
else
	cp -f "$_dist/rweg.toml" "$_home/rweg.toml"
	echo "    rweg.toml installed"
fi

# --- 验证 ---
_core="$_home/bin/core-rweg.exe"
[ -x "$_core" ] || _core="$_home/bin/core-rweg"

echo "==> verify"
"$_core" selftest >/dev/null && echo "    selftest OK"
"$_core" cfg | sed 's/^/    /'

echo
echo "install complete -> $_home"
echo
echo "用法（两种 source 方式，任选其一）："
echo "  export RWEG_HOME=\"$_home\"          # 可选，env.sh 会自己算"
echo "  source \"$_home/scripts/env.sh\"            # 非侵入：hread / hwrite / hedit / hgrep / hundo"
echo "  source \"$_home/scripts/env.aggressive.sh\"  # 另把 read / write / edit / grep / undo 与 cd 提示接管"
echo "  source \"$_home/scripts/env.restore.sh\"     # 反注册：拆掉接管层，恢复内置（--all 连 h* 层一起拆）"
echo "  core-rweg doctor"
echo
echo "项目级配置：在项目根放一个 rweg.toml（可只写 [db] dir），"
echo "            cwd 在那里时工具就用它，数据库跟着配置走。"
echo "  例：d:\\nginx\\2dsim\\rweg.toml  ->  d:\\nginx\\2dsim\\.db\\<ns>.db"