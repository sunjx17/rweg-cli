package app

import "fmt"

// cmdPrompt 打印给 Agent 用的提示词模板（整段贴进 system prompt / AGENTS.md 即可）。
//
// 模板随二进制走，不依赖安装目录里另存一份文件：Agent 在任何项目里
// `core-rweg prompt` 都能取到与当前版本一致的约定。
func cmdPrompt(args []string) error {
	if wantsHelp(args) {
		fmt.Print(`core-rweg prompt

打印 rweg 各工具的 Agent 提示词模板：直接把输出贴进 Agent 的 system prompt
或项目 AGENTS.md，Agent 即知道该用 read / edit / undo 而非 cat / 整文件重写。
`)
		return nil
	}
	fmt.Print(agentPrompt)
	return nil
}

// agentPrompt 是提示词模板正文。
//
// 只写"当前状态"：命令形态、锚点规则、退出码、回退路径。
// 改这里前先确认 README 的工具一节与 toolHelp 不与之冲突。
const agentPrompt = `# rweg 工具约定（贴进 Agent 的 system prompt）

本机已装 rweg。行锚（hashline）工具是 core-rweg 的子命令，由 env.sh 注入成 bash 函数：

    source ~/.rweg/scripts/env.sh             → 七个工具的 h* 函数：hread / hwrite / hedit /
                                                hgrep / hundo / happend / hprepend
                                                （undo / append / prepend 另有不带 h 的原名）
    source ~/.rweg/scripts/env.aggressive.sh  → 另把 read / write / edit / grep 强行接管
                                                （同名覆盖；cd 另加提示）

下文用短名 read / write / edit / grep / undo / append / prepend；非侵入模式下只有前四个
要加 h（hread / hwrite / hedit / hgrep），undo / append / prepend 两种模式同名。
用法与系统同名命令不同。
以下约定优先于你惯用的 cat / sed / 整文件重写。

## 何时用哪个

- 看文件：read（不要 cat / head / sed）；只读需要的行，省 token。
- 改文件：edit + 锚点（不要整体重写，也不要复述未改动的行）。
- 找位置：grep（参数同 ripgrep），命中行自带锚点，可直接 edit。
- 插一行：append / prepend + 单个锚点（位置只由锚点定，文件从基线反查）。
- 改错了：undo 退回上一次 write / edit / append / prepend 之前的内容。

## 命令

    read  FILE [RANGE ...] [--encoding ENC] [--no-serve]
          RANGE := N（单行）| N:M（闭区间）| N-（N 至文末）| -M（文首至 M）
          不给 RANGE 即读全文；多范围可乱序、可重叠，输出按行号合并排序。
          一次全读上限 max_lines = 20000 行，超出标 truncated（只影响这一次输出）。
    edit  FILE FROM:TO "lines" [FROM:TO "lines" ...]
          FROM:TO 省略 :TO 即 FROM:FROM。
          CONTENT 里的换行必须是**真换行**：$'a\nb'、here-doc，或从 stdin 喂；
          写成 \n 两个字符只会原样插入这两个字符。"" 删除该区间。
    write FILE "content"
          不给 content 则从 stdin 读全文；整文件替换；目录必须已存在。
          内容不以换行结尾时会自动补一个；要精确控制就走 stdin。
    grep  [rg 参数]... [--] 路径
    undo  [FILE]          回退该文件（不给 FILE 则最近一次）上一次改动之前
    append  ANCHOR "line" 插在 ANCHOR 那行之后；prepend 则插在它之前
           位置只由锚点定，所以文件可以省（从基线反查；多个文件都读过同一锚点时
           报 E_AMBIGUOUS_ANCHOR，用 -f FILE 指明）。"" 插一个空行。
           CONTENT 恰好是 -v / -q / -h / -f / -c / --json 时用 -c ANCHOR CONTENT 形态。

FILE 放在最前面就能省掉 -f。要把 flag 写在文件名前面（read --encoding gbk FILE）
时改用 -f FILE 形式，两者等价；旧写法 read -f FILE / write -f FILE -c CONTENT /
edit -f FILE -c FROM:TO CONTENT 照旧可用。

帮助：read -h / write -h / edit -h / grep -h / undo -h / append -h / prepend -h /
core-rweg -h。

## 输出形态

    # src/app.ts (6 lines, lf)
    1:6:
    CTt│function hello() {
    Um7│  console.log("world");

- 头部是「路径 (行数, 行尾[, bom][, 编码][, truncated])」，-q 可去掉。
- FROM:TO: 是块头（行号范围），其下每行前缀是「锚点│内容」。
- 单行超过 max_line_bytes = 6000 字节时锚点位置显示 ~~~，该行不能用于 edit
  （锚点要完整行；用 read RANGE 把它切成更小的行再改）。

edit 回传的是 git 风格的差异，前缀分别是 "  "（上下文）/"- "/"+ "，
相隔较远的块之间用单独一行 "..." 表示省略（不重复打印没变的行）：

    edited src/app.ts (6 → 7 lines, +2 -1)
      AU6│}
    - nVR│const x = 1;
    + pQ7│const x = 2;
    + m1Z│const z = 3;
      yAZ│let y = 2;

头部只计**真正改变的行数**：只增不减时不出现 -N，一处没改就不出现 +0 -0。
edit 报错时回传的当前区间仍用「锚点│内容」形态。

## 锚点规则（要紧）

- 锚点是 3 字符（如 CTt），只在本终端的 read / grep 之后有效，且只对同一文件。
  锚点由内容决定：不同文件里内容相同的行会得到同一个锚点，别跨文件复用。
- 读过的行进入基线；edit / append / prepend 只认基线里的锚点，所以 grep 或 read
  之后才能改。append / prepend 的位置由单个锚点给出，文件由基线反查。
- edit 报 E_STALE_RANGE / E_STALE_ANCHOR / E_UNKNOWN_ANCHOR /
  E_AMBIGUOUS_ANCHOR 时，该锚点作废：按 hint 重新 read 目标区间再 edit。
  工具回传的「当前区间」内容不入基线，不能拿它直接重试。
- 一次 edit 的多个改动原子生效：任一失败则文件与基线都不动。
- 未读过的行不会被顺带改动；写回保持原编码 / 行尾 / BOM，不静默转码。

## undo（撤销）

    undo FILE      退到该文件上一次改动之前（原始字节，编码/行尾一并回来）
    undo           退到最近一次改动（不限文件）

每个 namespace 记最近 16 次改动，单条上限 4 MiB（超了报 E_UNDO_TOO_BIG）。
撤销后该路径的锚点按恢复出来的内容重建：内容回到旧样子，旧锚点重新有效。
撤销一条即消费一条，再 undo 就是继续往前退一层。

## 退出码

    0  成功
    1  业务错误（E_*：锚点失效、格式不符、文件不存在……），照 hint 处理
    2  用法错误（参数写错、缺文件），改参数重试
    3  内部错误（库 / IO）

## 回退与旁路

- grep 没给路径（管道输入）或 rg 不可用时，行为等同系统 grep/rg，输出无锚点。
  grep -E / --extended-regexp 被吃掉（rg 默认就是 ERE；rg 的 -E 是 --encoding，
  编码请写 --encoding）；-G / --basic-regexp 明确报错，rg 没有 BRE。组合短
  选项同理：-qE / -cE / -nE 里的 E 一样吃掉（-A2E 这种带值的也拆得对）。
- read FILE 简写认「存在的文件」：notes、Makefile 这类无扩展名的照读；裸词
  没有同名文件时才按 bash 内置的变量名语义处理。
- 走 bash 内置的那些写法（read line、read -r line）最多等 stdin 5 秒
  （RWEG_READ_TIMEOUT 可调，0 = 不限）：等不到输入即返回 >128 并提示，
  不会挂住整条命令。要 bash 原生语义写 builtin read。
- 路径两种形态都认：C:/proj/f.ts 与 /c/proj/f.ts；MSYS 的 /tmp/... 也认。
- 配置与数据库在 source env.sh 时就**钉住**（$RWEG_PIN_CONFIG）：之后 cd 到
  哪都是同一份配置、同一个库，锚点不会因为换目录作废。想换项目就重写这个
  变量（或重开终端），想恢复"每次按 cwd 现找"就 unset RWEG_PIN_CONFIG。
  诊断用 core-rweg cfg / core-rweg doctor。
- 本模板的最新版：core-rweg prompt。
`
