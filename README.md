# rweg-cli

> 文有常兮锚不移，行虽迁兮指不迷。

为终端编书者（人或 AI）授行锚之五器。

**English**: rweg is a zero-config CLI suite of line-anchored file tools for AI agents — `read`, `edit`, `write`, `grep`, `undo`, `append`, `prepend`. Every line carries a stable 3-char anchor, so the agent edits by short anchors instead of re-echoing whole blocks (as str_replace does), with stale-anchor rejection, atomic multi-edit, undo, and UTF-8/GBK/UTF-16 + CRLF/BOM fidelity. Built for Git Bash / MSYS agent shells. Star it if it saves you tokens.

---

## 序

吾观诸代理之有编辑之器，多str_replace式精确替换，需复述先前所见、空费输出词元，且错一字整段即废；偶遇[`Rianico/dsh-better-edit`]，惊为天人，奈何未及更新、适配新版。故思一零配置之cli套件，可按需引入；若不用本套件代替grep之方案，自行移除即可。

---

## 安装

```bash
./scripts/build.sh            # 编译 + 铺产物（落 ./dist/）+ 自检
./scripts/install.sh          # 装到 $RWEG_HOME，缺省 ~/.rweg
source ~/.rweg/scripts/env.sh # 唯一入口
core-rweg doctor              # 全 OK 方为装好
```

装于他处，或再装：

```bash
./scripts/install.sh /d/tools/rweg
RWEG_HOME=/d/tools/rweg ./scripts/install.sh
```

装成之局（不碰 `/usr/bin`，不改全局 PATH，无 `make install`）：

```
$RWEG_HOME/                缺省 ~/.rweg
├── bin/                   core-rweg.exe —— 唯一二进制（工具入口是它的子命令）
├── scripts/
│   ├── env.sh             非侵入入口：h* 别名 + undo / append / prepend（原名）
│   ├── env.aggressive.sh  侵入入口：另把 read/write/edit/grep 与 cd 提示接管
│   └── env.restore.sh     反注册入口：拆掉接管层，恢复内置
├── db/                    安装级默认库目录
└── rweg.toml              安装级默认配置
```

### source env.sh

两个入口，任选其一：

| 入口 | 给什么 | 动什么 |
|---|---|---|
| `source ~/.rweg/scripts/env.sh` | 路径 / ns / 配置钉住 + 七个工具的 `h*` 别名（`hread` `hwrite` `hedit` `hgrep` `hundo` `happend` `hprepend`）+ 与 bash 不重名的原名 `undo` `append` `prepend` | 不碰 `read` / `write` / `edit` / `grep` 这四个同名命令；`cd` 只加一行提示（不 export，子脚本里仍是内置 `cd`） |
| `source ~/.rweg/scripts/env.aggressive.sh` | 上面全部，另加 `read` `write` `edit` `grep` 强行接管 | 覆盖 bash 内置 `read` 与系统 `grep`；`cd` 的警告提示打开（仍不 export，子脚本里是内置 `cd`） |
| `source ~/.rweg/scripts/env.restore.sh` | 反注册：拆掉接管层，`read`/`write`/`edit`/`grep`/`cd` 回到 bash 内置与 PATH | 加 `--all` 连 `env.sh` 那层一起拆（`h*` / 原名 `undo` `append` `prepend` / PATH / `RWEG_NS` / 配置钉住） |

`env.aggressive.sh` 先 source `env.sh` 再叠一层。两份名字只有四个命令需要区分：`read` / `write` / `edit` / `grep` 在非侵入模式下写作 `hread` / `hwrite` / `hedit` / `hgrep`；`undo` / `append` / `prepend` 与 bash 不重名，两种模式都用原名（同时也都有 `h*` 别名：`hundo` / `happend` / `hprepend`）。工具入口都是 `core-rweg` 的子命令（`core-rweg read ...`），`bin/` 里只有 `core-rweg` 一个二进制。

成功时**一声不吭**：stdout 与 stderr 皆空，source 的输出可安全被管道 / 命令替换捕获。欲见就绪信息，`RWEG_VERBOSE=1 source ~/.rweg/scripts/env.sh`（走 stderr）。装好与否的权威判据是 `core-rweg doctor`。

接管层（`env.aggressive.sh`）为什么要立函数：`read` / `write` / `edit` 在 Git Bash 中为 bash 内置，而 bash 解析之序为函数 > 内置 > PATH。故只把 `$RWEG_HOME/bin` 前置到 PATH 无效（何况 `bin/` 里现在只有 `core-rweg`，没有 `read` / `grep` 可抢）；不改的话 `read -f app.ts 1:3` 会为内置 `read` 所吞，报 `read: -f: invalid option`。

`env.aggressive.sh` 为 `read` / `write` / `edit` / `grep` 这四个同名命令各立转发函数（转发到对应的 `h*`），并 `export -f`；否则 `bash script.sh` 子进程中复为内置。`cd` 包装函数是**例外，不 export**：它只在本 shell 做交互提示，导出后每个子脚本都会继承它，`root="$(cd … && pwd)"` 这类命令替换会把警告文本一起收进变量（rweg 自己的 `build.sh` 曾因此崩）；提示本身也走 stderr，不脏 stdout。

接管层的 `read` 转发判据 —— **只在下列情形归 rweg**：出现 `-f` / `--file` / `--json` / `--no-serve` / `--encoding` / `--quiet` / `-h`；零参数；或简写 `read FILE [RANGE ...]`（首个参数是**存在的文件**，且其后全是范围写法；名字带不带 `.` 或 `/` 都认，`notes`、`Makefile` 这类无扩展名文件同样是文件）。其余（`read line`、`read -r line`、`read -p …`、`read -a arr`）原样交还 `builtin read` —— 函数随 `export -f` 进入每个子 bash，若一律转发，脚本里遍地都是的 `while read -r x` 会撞 `unknown flag "-r" for read`。零参数归 rweg（报缺 `-f`，退出码 2）：交给内置会在交互式终端上一直等一行输入，agent 一敲即卡。要 bash 原生语义写 `builtin read`；`grep` 本非内置，要系统 grep 走 `/usr/bin/grep`。非侵入模式下不存在这个问题：`read` / `grep` 根本没被碰。

同上，接管层交还 builtin 的那一侧带**有界等待**（`RWEG_READ_TIMEOUT`，缺省 5 秒，`0` / `off` / `no` 关闭）：agent 的 stdin 是挂在控制台上的字符设备，`[ -t 0 ]` 为真却无人输入，无重定向的 `builtin read` 会一直等一行永不来临的输入直到外层命令超时（shell 被重置，`RWEG_NS` 一并丢）。超时返回码 >128，stderr 提示改用 `read FILE`。代价是空闲超过该值的管道（如 `tail -f log | while read -r l`）会提前结束：这类写法用 `builtin read`（不经本函数）或把上限置 0。

`env.sh` 导出的两种路径形态各归其位：`RWEG_HOME` 为原生形态（`C:/Users/sun/.rweg`），供 core-rweg 等原生程序使用 —— MSYS 不替其它环境变量做转换，若给 `/c/...`，`filepath.Join` 会拼出 `C:\c\...`（当前盘根），安装级 `rweg.toml` 找不到、库落错地方；`RWEG_BIN` 保持 POSIX 形态（`/c/Users/sun/.rweg/bin`），因为 MSYS 的 PATH 以 `:` 分隔，`C:/...` 会被盘符冒号切开。工具侧另有 `config.NativePath`，`read -f /c/proj/x.ts` 这类参数写法也照收。

doctor 检 PATH 首位（两种形态皆认）；shell 层由 `scripts/shell-test.sh` 验之。

`env.sh` 还会在 source 的那一刻把生效配置钉进 `RWEG_PIN_CONFIG`（`core-rweg cfg --path` 的结果；没有配置文件则留空）：此后 `cd` 到哪都是同一份配置、同一个库，锚点不因换目录作废。想换项目就重写这个变量（或重开终端），想回到"每次按 cwd 现找"就 `unset RWEG_PIN_CONFIG`。

### 用

```bash
read src/app.ts 1:6                          # 简写：文件放最前，省掉 -f
append  src/app.ts nVR "  // 插在这行之后"     # 文件 + 单锚点定位置
prepend src/app.ts nVR "  // 插在这行之前"
edit src/app.ts nVR:nVR "const x = 42;"
write src/new.ts "export const a = 1;"
grep -n hello src/
undo src/app.ts                              # 退回上一次改动之前

read -f src/app.ts 1:6                       # 等价写法：要 flag 打头时用它
edit -f src/app.ts -c nVR:nVR "const x = 42;"
write -f src/new.ts -c "export const a = 1;"
```

（非侵入模式下 `read` / `write` / `edit` / `grep` 要写 `hread` / `hwrite` / `hedit` / `hgrep`；`undo` / `append` / `prepend` 两种模式同名。下文各节同此。）

换项目：改钉住的配置路径（或重开终端）。`source` 时钉住，之后 `cd` 不改判落点：

```bash
$ cd d:/proj-a && source ~/.rweg/scripts/env.sh
$ core-rweg cfg | grep '^\[cfg\] db'
[cfg] db      D:\proj-a\.db\wgiAj-OcoN2.db
$ cd /tmp                                    # 同一个 shell 里 cd 不再换库
$ core-rweg cfg | grep '^\[cfg\] db'
[cfg] db      D:\proj-a\.db\wgiAj-OcoN2.db
$ export RWEG_PIN_CONFIG=d:/proj-b/rweg.toml # 显式换项目
$ core-rweg cfg | grep '^\[cfg\] db'
[cfg] db      D:\proj-b\.db\wlaGq-OcoN2.db
$ unset RWEG_PIN_CONFIG                      # 回到"每次按 cwd 现找"
```

---

## 工具

七个工具与 `core-rweg` 及其子命令皆认 `-h` / `--help` / `help`：`read -h`、`grep -h`、`core-rweg cfg -h` 打的都是 rweg 自己的用法（`grep -h` 不再落到 ripgrep 的手册）。下文用接管后的短名 `read` / `write` / `edit` / `grep` / `undo` / `append` / `prepend`；非侵入模式下前四个写作 `hread` / `hwrite` / `hedit` / `hgrep`，后三个同名，参数与输出完全一致。给 Agent 的完整约定：`core-rweg prompt`。

### `read` —— 取锚

```
read -f FILE [RANGE ...]
```

`FILE` 放在最前面即可省略 `-f`；要把 flag 写在文件名前面（`read --encoding gbk FILE`）就用 `-f` 形式。名字带不带 `.` / `/` 都认：`read notes` 与 `read notes 1:5` 行为一致（无同名文件时才按 bash 内置的变量名语义处理）。

```
RANGE := N       单行
       | N:M     含首尾闭区间
       | N-      N 至文末
       | -M      文首至 M
```

不予 RANGE，则读全文。多范围可乱序、可重叠；重叠与相邻者合并，合并后按起点排序。输出与输入顺序无涉。

```bash
$ read -f src/app.ts 1:6
# src/app.ts (6 lines, lf)
1:6:
CTt│function hello() {
Um7│  console.log("world");
AU6│}
nVR│const x = 1;
yAZ│let y = 2;
6PY│export default hello;

$ read -f src/app.ts 4-        # 4 至末尾
$ read -f src/app.ts -3        # 开头至 3（"-3" 以 - 开头，非 flag）
$ read -f src/app.ts 1:3 5:6   # 两块；第 4 行未 serve
```

| flag | 作用 |
|---|---|
| `--json` | 结构化输出（`blocks[].lines[]` 即锚点数组） |
| `-q` / `--quiet` | 去 `# 路径 (行数)` 头部 |
| `-v` / `--verbose` | 配置诊断走 **stderr**；stdout 唯锚点内容 |
| `--encoding` | 强制编码：`utf-8` `gb18030` `gbk` `big5` `utf-16le` `utf-16be` |
| `--no-serve` | 只读不建基线 |

边界皆有处置：CRLF / LF / CR / 混合行尾、UTF-8 BOM、UTF-16 BOM、NUL 二进制（报 `E_NOT_TEXT` 与偏移）、GBK 系自动探测、空文件、目录、行数超限（`truncated` 标记）、单行超长（锚点位置示 `~~~`；锚点需完整行）。

写回时编码、行尾、BOM 三者一律保持原样：编辑 GBK 文件后仍为 GBK，不因经 UTF-8 而静默转码。

### `edit` —— 以锚易文

```
edit [FILE] FROM:TO "lines" [FROM:TO "lines" ...]
edit -f FILE -c FROM:TO "lines" [-c FROM:TO "lines" ...]
```

- `FILE` 放在最前面即可省略 `-f`；简写形态下范围与内容成对出现，可给多组
- `-c` 食二参数：**锚点对** + **替换文本**
- 省略 `:TO` 等价于 `FROM:FROM`（改单行）
- 替换文本里的分行必须是**真换行**（`$'a\nb'`、here-doc，或从命令替换来）；
  写成 `\n` 两个字符只会原样插入这两个字符
- `""` 表示删除该区间
- 一次调用的多组改动**原子生效**：任一失败，则文件与库皆不动
- 改动前的内容进撤销栈，改错了可 `undo` 退回一步

```bash
$ edit -f src/app.ts -c nVR:nVR "$(printf 'const x = 42;\nconst z = 3;')"
edited ok
  AU6│}
- nVR│const x = 1;
+ EPx│const x = 42;
+ jDo│const z = 3;
  yAZ│let y = 2;
src/app.ts (6 → 7 lines, +2 -1)
```

未动之行，锚点皆留；diff 打出来的每一行（上下文与新增）都随即进基线。回传的是 git 风格的差异：`  ` 上下文、`- ` 删、`+ ` 增，
相距较远的块之间用单独一行 `...` 省略（没变的行不重复打印）。收尾行的计数只算**真正改变的行数**：只增不减时不出现 `-N`，一处没改则不出现 `+0 -0`。`-q` / `--quiet` 只留 diff 正文，确认行与收尾行都不打。

锚已过期，则硬拒，并回传该区域当前锚点：

```bash
$ edit -f src/app.ts -c CTt:AU6 "function hi() {"
E_STALE_RANGE  src/app.ts  lines 1-3 changed since read
hint: retry with the anchors above (lines 1-3 are served now)
Current range (1-3):
CTt│function hello() {
nEt│  console.log("world!!!");
AU6│}
```

回传之行随即入基线：回显的锚点可以直接拿来重试，不必先重新 read（重新 read 当然也可以）。

错误码（除标 2 者外，退出码皆 1）：

| 码 | 含义 |
|---|---|
| `E_NEVER_READ` | 本终端未读此文件 |
| `E_UNKNOWN_ANCHOR` | 锚不在本终端基线 |
| `E_STALE_ANCHOR` | 锚在当前文件中已不可解析 |
| `E_AMBIGUOUS_ANCHOR` | 锚在文件中多次出现，拒猜 |
| `E_STALE_RANGE` | 目标区间内之行与基线不符 |
| `E_OVERLAP` | 同次调用两 `-c` 改同一段 |
| `E_BAD_ANCHOR` | 锚形非 3 位 base62 → 退出码 **2**（带 `│` 的整行会给出定向提示） |
| `E_ANCHOR_IN_CONTENT` | 替换文本行首误带本文件锚点（如 `CTt│foo`），拒写 |

替换文本里行首像锚点、但**不属于本文件当前锚点**时（例如贴回来的是旧锚点），内容照写，只往 stderr 打一条 `W_SERVED_PREFIX_MISMATCH` / `W_NEVER_SERVED_SHAPE` 软警告；`--json` 时这些警告进 `warnings` 字段。

### `append` / `prepend` —— 单锚点插行

```
append  FILE ANCHOR CONTENT
prepend FILE ANCHOR CONTENT
append  -f FILE ANCHOR CONTENT
append  -f FILE -c ANCHOR CONTENT
```

把 `CONTENT` 插到 `ANCHOR` 所指那一行**之后**（`prepend` 则之前）。`FILE` 必填：放在最前面时可以省掉 `-f`（与 `read` / `write` / `edit` 一致）。位置只由那**单个锚点**定，所以文件必须点名 —— 工具不做跨文件反查。`CONTENT` 里的分行必须是**真换行**（`$'a\nb'`、here-doc、命令替换），与 `edit` 的替换文本同源；`""` 表示插入一个空行；被锚定的那一行原样保留、锚点不变。

```bash
$ read -f src/app.ts 1:3
nVR│const x = 1;
$ append src/app.ts nVR "const y = 2;"           # 插在 nVR 那行之后
appended ok
  AU6│}
  nVR│const x = 1;
+ 7Kd│const y = 2;
  m1Z│const z = 3;
src/app.ts (3 → 4 lines, +1)
```

实现上就是「把被锚定的那一行替换成 它自己 + 新行」，所以校验链、撤销、基线重建与 `edit` 完全一致：锚点过期同样报 `E_STALE_*`，`undo` 里记的 kind 是 `append` / `prepend`。`CONTENT` 恰好是 `-v` / `-q` / `-h` / `-f` / `-c` / `--json` 这类 rweg 认得的 flag 时，改用 `-c ANCHOR CONTENT` 形态传（`append FILE -c ANCHOR CONTENT` 或 `append -f FILE -c ANCHOR CONTENT`）。

### `write` —— 全量落笔

```
write [FILE] [CONTENT]
write -f FILE [-c CONTENT] [--encoding ENC]
```

不予 `-c`，则从 stdin 读全文；`-c ""` 写空文件。
文件不存在则创建（目录须先存，否则 `E_NO_DIR`）；存在则全量替换，并继承其编码 / 行尾 / BOM。写毕自动建立全文基线，故随后可直接 `edit`，不必先 `read`。

```bash
$ write -f src/new.ts -c "export const a = 1;
export const b = 2;"
created src/new.ts (2 lines)
Wb0│export const a = 1;
2Os│export const b = 2;

$ write -f src/new.ts -c "export const a = 1;"
replaced src/new.ts (1 line)
Wb0│export const a = 1;
```

`-c` 内容若不以换行结尾，自动补一换行。欲精确控制，走 stdin：`printf 'a\nb' | write -f f`。
与 `edit` 同类的坑：`-c 'a\nb'` 里的 `\n` 是**两个字符**，要真换行就写 `$'a\nb'`、here-doc 或走 stdin。

写完的内容进撤销栈：`undo FILE` 可退回这次写入之前（新建的文件则被删掉）。

### `grep` —— 命中即得锚

```
grep [rg 参数]... [--] 路径
```

命中行带锚点，并入基线，故 grep 所得锚点可直接 `edit`：

```bash
$ grep -n hello src/
# src/other.ts (1 match)
1:gZm│// says hello

# src/app.ts (2 matches)
1:CTt│function hello() {
6:6PY│export default hello;

$ edit -f src/app.ts -c 6PY:6PY "export default hello as greet;"
edited ok
src/app.ts (6 → 6 lines, +1 -1)
```

回退之法（rweg 只作最小解析，余参数原样交 `rg`）：

| 情形 | 行为 |
|---|---|
| 无文件路径（管道输入 / 整目录递归） | 完全透传系统 grep/rg |
| rg 不在 PATH（含 `RWEG_RG_MISSING=1`）/ 起不来 | 回退系统 `grep`，退出码一并透传 |
| rg 退出码 1（无匹配） | 原样返回 1 |
| rg 退出码 ≥2（自报错） | 透传 stderr 与退出码 |
| `-q` / `-l` | 直接透传 |
| `-c` / `--count` | 自数（rg 见 `-c` 会绕 `--json`）：单文件给数字，多文件给 `path:count` |
| `-E` / `--extended-regexp` | 吃掉：rg 的正则默认就是 ERE（rg 的 `-E` 是 `--encoding`） |
| `-G` / `--basic-regexp` | 明确报错（退出码 2）：rg 没有 BRE，不静默改语义 |
| 组合短选项（`-qE` / `-cE` / `-nE` / `-A2E`） | 拆成单个 flag 再判，`-E` 照吃、`-G` 照报错；带值的 flag（`-A2`、`-e pat`）里的值不动 |

透传时按目标换参数：交给系统 grep 的那份保留 `-E`（grep 要靠它才是 ERE），交给 rg 的那份已把 `-E` / `--json` 摘掉（rg 的 `-E` 是 `--encoding`）。

路径位置参数会先归一成原生形态，故 `grep pat /tmp/x.txt`、`grep pat /c/proj/x.ts` 都能用（见 `config.ResolvePath`）。

`-C/-A/-B` 上下文行亦算命中行，同样带锚点、入基线。

### `undo` —— 退回一步

```
undo [FILE]
undo -f FILE
```

把 `FILE` 退回上一次改动（`write` / `edit` / `append` / `prepend`）之前；不给 `FILE` 就退最近一次改动，不限路径。

- 恢复的是**原始字节**：编码、BOM、行尾风格一并回来
- 每个 namespace 记最近 16 次改动，单条上限 4 MiB（超了报 `E_UNDO_TOO_BIG`）
- 撤销后该路径的基线按恢复出来的内容重建：内容回到旧样子，**旧锚点重新有效**
- 撤销一条即消费一条，再 `undo` 就是继续往前退一层
- `write` 新建的文件撤销后即被删除
- 别名：`hundo_last_edit`

```bash
$ edit src/app.ts nVR:nVR "const x = 42;"
edited ok
src/app.ts (6 → 6 lines, +1 -1)
$ undo src/app.ts
undone edit  src/app.ts (6 lines restored)
$ undo src/app.ts
E_NO_UNDO  no recorded write/edit to undo for src/app.ts
hint: undo 只回退本 namespace 里 rweg 自己写过的最近 16 次改动；更早的历史交给版本控制
```

---

## 给 Agent 的提示词

```bash
core-rweg prompt          # 打印整段模板
```

输出是一份可直接贴进 system prompt 或项目 `AGENTS.md` 的约定：各工具何时用、命令形态（含 `read FILE` / `edit FILE FROM:TO` / `write FILE` / `append FILE ANCHOR` 简写）、输出读法（`# 路径 (行数, 行尾)` / `FROM:TO:` / `锚点│内容`，以及 `edit` 的 git 风格差异与 `...` 省略）、锚点规则（只在本终端、见到的行一律入基线、报 `E_STALE_*` 按 hint 处理、多组改动原子）、撤销语义、退出码 0/1/2/3、回退路径（`builtin read`、grep 透传、`-E`/`-G` 兼容）、以及配置钉住（`RWEG_PIN_CONFIG`）。

模板随二进制走（`internal/app/prompt.go`），不依赖安装目录里另存文件，故 `core-rweg prompt` 与当前版本永远一致；各工具自己的 `-h` 末尾也会指到这里。

---

## 锚之律

此物之由唯一：静默误书，其几为零。赖以下几条：

| 失败模式 | 拦之者 |
|---|---|
| 旧锚改新行 | 编辑释放之锚入 `retired`，禁再分配 |
| 锚在文件中多次出现而“随便挑一” | `E_AMBIGUOUS_ANCHOR` 直拒 |
| 内容外改仍照旧落笔 | 逐行比基线；`E_STALE_RANGE` + 回传现状 |
| 见过的行却不可编辑（被迫重读） | `read` / `grep` / `write` / diff / 失败回显一律入基线 |
| 把 read 输出整行当替换内容 | 行首锚点命中本文件 → `E_ANCHOR_IN_CONTENT` 直拒 |
| 未读之行顺为“已读” | 基线以空串示“未 serve”，位移后仍空串 |
| 区间边界靠“行数未变”猜 | 逐行记行尾 + 显式映射，不猜 |
| 半截文件（写半而崩） | 临时文件 + `rename`；库写单事务 |
| `-c` 内容似 flag 而被吞 | 参数解析知每 flag 食几值 |
| 编辑 GBK 文件而静默变 UTF-8 | 按原编码写回，BOM 与行尾并保 |

三边界，非缺陷：

- 空白不参与锚点。`canon` 剔所有 ASCII 空白，故 formatter 只重排空白时锚不轮换。代价：字符串字面量内空白变化亦不可见（`"x y"` 与 `"xy"` 同码）。
- 区间外改动不阻塞。唯目标区间内部变则拒；区间外漂移不影响写入正确性，位置漂移由逐行比对兜住。
- 锚点值 = 行内容的哈希 + 该行在文件内的出现序号（3 位 base62），故**内容相同的行在不同文件里会得到同一个锚点值**；各文件各算基线，写时文件名与锚点都要对上。
- 一 ns 一库；读取上限缺省 `max_lines = 20000` 行（超出标 `truncated`），单行上限 `max_line_bytes = 6000` 字节（超出该行只给 `~~~`，不能用于 `edit`）。

---

## 配置

查找次序（`$RWEG_WALKUP` 只认环境变量）：

```
1. $RWEG_CONFIG              显式指定；指向不存在则报错（唯一因配置失败而终止者）
2. $RWEG_PIN_CONFIG          env.sh 在 source 时钉住的路径；文件存在即用，不存在则忽略
3. <cwd>/rweg.toml           主力
4. <cwd>/../rweg.toml …      向上 $RWEG_WALKUP 级父目录，缺省 0（只找 cwd），上限 32
5. $RWEG_HOME/rweg.toml      安装级回落
6. 内置默认值                 基准为 $RWEG_HOME
```

配置文件相对路径皆相对于配置文件所在目录解析。

```toml
[db]
dir = ".db/"              # 库目录；库文件 = <dir>/<namespace>.db，每 ns 一文件

[namespace]
ttl_hours = 24            # 基线存活时长；-1 = 永久
gc_interval_minutes = 60  # 惰性 GC 间隔
strict_cwd = true         # ns 记录之 cwd 与当前不符则拒（配置被钉住时跳过此检查）

[hash]
canon_version = 2         # 改动即作废全部旧快照
probe_stride = 3907

[read]
max_line_bytes = 6000     # 超出者只给标记，不给锚点（超长行无法构造锚点）
max_lines = 20000         # 一次全读的行数上限，超出截断并标 truncated

[edit]
max_changes = 32          # 单次 -c 数量上限
served_echo_cap = 150     # 拒绝时回传行数上限

[grep]
rg_path = ""              # 空 = 从 PATH 找
fallback_to_rg = true

[output]
encoding = "utf-8"
newline = "preserve"      # 写回保持原文件行尾
```

诊断：

```bash
core-rweg cfg         # 打印生效配置与来源
core-rweg cfg --no-ns # 同上，但不显示 ns
core-rweg doctor      # 环境自检（路径 / 配置 / 库 / rg / 自检向量）
```

环境变量：

| 变量 | 用途 |
|---|---|
| `RWEG_HOME` | 安装目录，缺省 `~/.rweg`；`/c/...` 与 `C:\...` 两种写法都认（见 `config.NativePath`） |
| `RWEG_CONFIG` | 显式指定配置文件（唯一因配置失败而报错者） |
| `RWEG_PIN_CONFIG` | `env.sh` 在 source 时写入的配置文件路径；之后 `cd` 不再改判落点。重写此变量即换项目，`unset` 则回到"每次按 cwd 现找" |
| `RWEG_WALKUP` | 向上查找父目录层数，缺省 0（钉住配置后不再参与改判） |
| `RWEG_NS` | 当前终端 namespace，由 `env.sh` 生成；手动固定可多终端共享基线 |
| `RWEG_VERBOSE` | 置非 0 值后，`source env.sh` 往 stderr 打就绪信息（缺省静默） |
| `RWEG_READ_TIMEOUT` | 交还 `builtin read` 的 read 调用等待 stdin 的秒数上限，缺省 5；`0` / `off` / `no` = 无限等待（调用方自带的 `-t` 照旧生效） |
| `_rweg_core` | `env.sh` 导出的 core-rweg 可执行文件路径；h* 函数靠它调用（导出是为了让 `export -f` 的函数在子 bash 里也能用） |
| `RWEG_RG_MISSING` | 置任意值即假装 rg 不存在 |

### namespace 与数据库

- ns 形如 `wK3fP-9xT2q`：前 4 位绑 `cwd|配置目录`，后 5 位是创建时刻。生命周期 = 终端生命周期：存于环境变量，终端关则灭，不留全局状态。
- 同一目录开两个终端得不同 ns（后 5 位不同），served 状态互不干扰。
- 一 ns 一库文件（`<db.dir>/<ns>.db`）。多终端接力，显式复用同一 `RWEG_NS`。
- 配置被 `$RWEG_PIN_CONFIG` 钉住时不做 `cwd` 一致性检查（钉住即同一个项目，库不随 `cd` 变）。没钉而跨了目录报 `E_NS_CWD_MISMATCH` 时，`unset RWEG_NS` 让本 shell 按当前 cwd 重新派生即可恢复。
- 回收以 TTL（缺省 24h）与惰性 GC；不设退出钩子。bash 子 shell 继承并触发 `EXIT` trap；`x=$(read -f f)` 毕则 ns 清，再调用撞 `E_NS_CWD_MISMATCH`。
- 未 `source env.sh` 亦可用：此时 ns 按 `cwd|配置目录` **确定性派生**（后 5 位固定为 `00000`，不取当前时刻），故裸调用的 `read` → `edit` 必然落在同一个 ns 上，链路通；代价是同目录的不同终端共用 served 状态，隔离性减弱。stderr 一 WARN。

---

## 仓库

```
.
├── cmd/core-rweg/          唯一二进制入口
├── internal/
│   ├── hashline/           xxh32 / canon / 槽位分配 / MapStable
│   ├── config/             rweg.toml 查找（含 $RWEG_PIN_CONFIG）/ NativePath / ResolvePath（cygpath 兜底）
│   ├── nsid/               namespace 生成 / 校验 / 降级派生
│   ├── store/              SQLite（served / retired / snapshot / undo）+ 惰性 GC
│   ├── docfile/            文件加载（BOM / 行尾 / 编码 / 二进制探测）
│   ├── ranges/             RANGE 解析 / 合并 / 基线集合
│   ├── edit/               校验链 + 原地应用 + buildDiff（git 风格统一 diff）+ 内容行首锚点扫描
│   └── app/                CLI：子命令分发（read/write/edit/grep/undo/append/prepend + cfg/doctor/prompt…）/ grep 参数归一化
├── scripts/
│   ├── build.sh            编译 + 铺产物（./dist/）+ selftest
│   ├── install.sh          装到 $RWEG_HOME
│   ├── env.sh              非侵入入口：h* 名字
│   ├── env.aggressive.sh   侵入入口：同名接管 + cd 提示
│   ├── env.restore.sh      反注册：source 它恢复内置
│   ├── shell-test.sh       shell 层验收 70 项
│   └── e2e-test.sh         e2e 202 项
├── dist-home/rweg.toml     安装级配置模板
├── examples/project-rweg.toml  项目级配置模板
├── rweg.toml               本仓库配置
└── .gitignore              通用忽略清单
```

### 测验

```bash
go test ./internal/...        # 220 个用例（194 个测试函数）
./scripts/shell-test.sh       # 70 项：builtin 覆盖 / source 输出 / PATH / 幂等 / 配置钉住 / 退出码
./scripts/e2e-test.sh         # 202 项：锚点稳定性 / 校验链 / 原子性 / 编码 / diff 形态 / 简写 / undo / -h / prompt
```

二者都真跑已装的 `$RWEG_HOME`，故改完先 `./scripts/build.sh && ./scripts/install.sh`。`build.sh` 与 `e2e-test.sh` 会把自己算出的路径经 `cygpath` 交给原生 `go`/`core-rweg.exe`（见其中 `native()`）：`MSYS_NO_PATHCONV=1` 的 shell（dsh 等 agent shell）里，`go build -o /c/...` 会写到 `C:\c\...`，dist 里留旧货 —— 表现为"改了代码、编译也过了，跑起来还是老行为"。

---

## 致谢

- [`Rianico/dsh-better-edit`](https://github.com/Rianico/dsh-better-edit)（MIT）之 `src/hashline/hash-assign.ts`，其本为 `@oh-my-pi/hashline` 之逐字节移植。

### 并谢（节选自 `Rianico/dsh-better-edit` 之 README）：

- [**pi-hashline-edit**](https://github.com/RimuruW/pi-hashline-edit)（RimuruW）——引入 3 字符哈希与冲突消解之原创 pi-coding-agent 扩展。
- [**pi-hashline-edit-pro**](https://github.com/YuGiMob/pi-hashline-edit-pro)（YuGiMob）——本仓库 hashline 核心所移植之加固版 fork。
- [**pi-hashline-edit-lsz**](https://github.com/Rianico/pi-hashline-edit-lsz)——本项目所跟随之自维护 fork。hashline 核心逐字节移植；工具层基于 dsh 插件 API 重写。

延伸阅读：[Hash anchors + Myers diff + single-token anchors（dirac.run）](https://dirac.run/posts/hash-anchors-myers-diff-single-token) 及独立 [hashline 与 replace 对比基准测试](https://nwyin.com/blogs/hashline-vs-replace-edit-bench.html)。

## Vibe Coding 用具

[dsh](https://github.com/deepseek-ai/deepseek-harness)

## 许可

[MIT](LICENSE)。