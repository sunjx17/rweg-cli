# 更新日志

只记对外行为 / 接口 / 文档的变化；实现细节看 `git log`。

## 未发布

### 新增

- **`replace`：行内子串 / 正则替换**。`replace -f FILE -c ANCHOR OLDSTR "REPLACEMENT"`
  为精确子串替换，`replace -f FILE -cr ANCHOR REGEX "REPLACEMENT"` 用 Go 正则（RE2，
  子匹配写 `$1` / `${name}`）。`ANCHOR` 是单行锚或 `FROM:TO` 区间；匹配与替换逐行进行
  （模式不跨行，一行内命中几处换几处），`""` 替换文本表示删掉匹配。
  校验链、原子写、撤销、served 重建、diff / `--json` / `-q` 输出、编码与行尾保真
  全部复用 `edit` 那条路径，只有"新内容怎么算出来"不同：没变的行不进 `edit.Op`，
  锚点照旧保留。
  - **`-c` / `-cr` 可重复**：一次调用多组改动，与 `edit` 的多 `-c` 同规 ——
    全部原子生效，两组区间不许重叠（`E_OVERLAP`）；任何一组零命中 →
    `E_NO_MATCH`（带 `-c #N` 组号）、命中但没变 → `E_NO_CHANGE`，整单不写盘
    （此前的重复 `-c` 会静默只留最后一组）。
  - 匹配空串的正则（`x*`、`(a)?`）被拒（`E_BAD_REGEX`）：它们会把替换文本
    插进一行里的每个字符之间；`^` / `$` 这类单一空命中位置照常可用。
  - `--content` 与 `-c` 同义（通用 flag 层早已给它三元 arity，此前却被下游拒掉）。
  - 整段无命中 → `E_NO_MATCH`；命中但算出来的文本与原行相同 → `E_NO_CHANGE`；
    空模式 → `E_BAD_PATTERN`，非法正则 → `E_BAD_REGEX`（退出码 2）。前两种不写盘。
  - `FILE` 只能用 `-f` / `--file` 点名：位置参数已被锚点与两条文本占满，
    故不做其他工具那种「`FILE` 放最前」的简写。
  - 装载层随之多一个 `hreplace`（`env.sh` 不占 `replace` 这个名字 ——
    Windows 自带 `System32\replace.exe`）；`env.restore.sh --all` 一并拆它。

### 行为

- **装载层收敛为单一入口 `env.sh`**：h* 别名 / 不重名原名之外，直接把
  `read` / `write` / `edit` / `grep` 四个同名命令也接管了（原 `env.aggressive.sh`
  那一层并入 env.sh）；不再有 `cd` 包装函数 —— `cd` 全程是内置 cd。
  `env.restore.sh` 随之只认这两层（同名接管 / `--all` 的 h* 与 PATH / ns）。

- **`read` / `write` / `grep` 新增无锚形态 `-fn FILE`**（长写法 `--no-anchor FILE`
  / `--no-anchor=FILE`）。它与 `-f FILE` 等价，但完全不产生锚点：`read` 只回内容
  （每行没有「锚点│」前缀），`write` 成功时只回一行 created/replaced 确认、
  不回显内容，`grep` 只回「行号:内容」。这些行不进 served，不能直接拿去 `edit`。
  `--json` 下 `read` 的每块多一个 `content` 数组、`write` 不再输出 `hashes`。

- **未 `source env.sh` 时的降级 ns 改为确定性派生**：后 5 位固定为 `00000`，
  不再带创建时刻。同一个 `cwd|配置目录` 无论调用多少次都得到同一个 ns，
  裸调用的 `read` → `edit` 必然落在同一个 namespace 上；此前带秒级时间戳，
  两次调用跨秒就会变成两个 ns，`edit` 报 `E_NEVER_READ`。
  代价不变：同目录的不同终端共用 served 状态。`env.sh` 生成的 ns 仍带
  时刻，同目录的两个终端依旧隔离。

- **`grep` 的 GNU 习惯兼容补齐**：带模式 / 路径的 `-h` 按 GNU 语义翻译成
  rg 的 `--no-filename`（rg 的 `-h` 是帮助），回退系统 grep 时原样保留；
  裸 `grep -h` / `grep help` / `grep --help` 才是 rweg 帮助。此前
  `grep -h pat FILE`、`grep help FILE` 会打印帮助并 exit 0 —— 静默没搜。
- **`grep -c` 只数命中行**（GNU 语义）：`-A/-B/-C` 的上下文行此前被折进命中数；
  `(N matches)` 块头同。展示与 serve 仍两段合并。
- **`grep --json` 在锚点路径生效**：此前只有 `-c` 认它，锚点输出静默忽略。
  现在锚点 / `-fn` 两条路径都输出 `[{path, lines:[{line, hash, text, match}]}]`，
  `hash` 空 = 不可用于 edit。
- **替换 / 插入文本的结尾换行按行结束符处理**（`edit` / `append` / `prepend`）：
  把 read 输出 / here-doc 原样贴回来不再静默多一个空行；要在末尾补空行就多写
  一个换行。每行结尾的 `\r` 一并剥掉（CRLF 来源的内容此前会落成 `\r\r\n`）。
- **纯 CR 行尾（经典 Mac）如实识别**：此前被静默当 LF，编辑后整个文件被
  重排行尾；现在 read 头部标 `cr`，写回复用 CR。
- **编码探测按解码结果的字符分布选表**（Big5 → GBK → GB18030 特指优先）：
  往返一致分不出三张 CJK 表（各自双射，用哪张都能无损往返）—— GBK 文件
  现在报更精确的 `gbk`，Big5 文件不再被吞成乱码。
- **`edit` 未编辑旧行的 served 改记当前锚点**：重复内容行被就近配对跨位继承时，
  旧实现照抄基线位置锚点，下一轮 edit 未动过的行会莫名 `E_STALE_RANGE`
  （fail-closed 的误拒陷阱）。

### 修复

- **`read` 把 `[read].max_lines` 当成文件真实行数**：加载阶段按上限截断 `Doc.Lines`，
  于是 1665 行的文件报 `(200 lines, lf, truncated)`，第 201 行往后一律
  `E_RANGE line 943 out of range (file has 200 line(s))` —— 显式范围、`N-`、编辑、
  undo 全部够不到，头部与错误提示还一起说谎。现在装载层**永不截断**，
  `max_lines` 只在 `read` 物化输出时施加（`ranges.Limit`：块内截断、其后的块丢弃），
  行数判定、`E_RANGE` 提示、头部行数一律用真实行数；被截掉的行不 serve。
  同一处隐患一并消掉：`edit` / `replace` / `append` / `prepend` 此前也在截断后的
  行数组上编辑并整份写回，长文件一次编辑就会把文件写成前 `max_lines` 行。
- `read` 接受 `N-M`（与 `N:M` 同义）：此前 `read 295-300` 报
  `E_RANGE "295-300": not a number`，`N-` 与 `-M` 都收，唯独最自然的闭区间写法被拒，
  还被误报成越界。
- `env.restore.sh` 反注册失效（两层都拆不掉）：同名接管的判据改为源码形状
  `"$_rweg_core"` + `" 工具名"`（旧判据找 `hread` 字样，转发函数体里根本没有）。
  `shell-test.sh` 新增 [9] 反注册一节（此前零覆盖，正是失效漏网的原因）。
- `ServeLines`（read 的落库）包进 `BEGIN IMMEDIATE` 事务：多终端共享
  `RWEG_NS` 时，两个终端同时 read 同一文件会互相冲掉刚 serve 的行，
  之后 edit 莫名 `E_STALE_RANGE`。
- 捆绑数值 flag 只吃数字：`-A2h` = `-A2` 加 `-h`；`-Ah` 这种非数字尾巴按
  GNU 语义是它的值（交给 rg 报错），不再反吞下一个参数（可能正是要搜的模式）。
- `writeFileAtomic` 在 Windows 上对 rename 先做有界重试（杀毒 / 索引服务
  短暂握住目标文件的常见场景），再走"删目标再改名"的破坏性兜底。
- `edit` / `replace` 落盘后的库写失败：错误明确"文件已写盘"并指路 read 重建
  基线（此前报 `edit: persist served`，读错误方会误以为没写）。
- `E_TOO_MANY` 提示按工具分岔：replace 说收窄区间而非"拆成几次 edit"。
- `Home()` 的 `$RWEG_HOME` 支持 `/tmp` 类 MSYS 挂载点（cygpath 兜底）。

### 文档

- README：新增 `replace` 多组改动与 grep GNU 兼容一节；装载说明（单入口 /
  restore 判据）对齐当前脚本；测验数字对齐当前套件。
- `core-rweg prompt`、各工具 `-h` 同步到上述全部行为。

## v0.2-alpha

### 新增

- `append` / `prepend`：单锚点插行（`append FILE ANCHOR CONTENT`），校验链、undo、
  served 重建与 `edit` 共用同一条路径。
- 装载层分三档：`env.sh`（`h*` 别名 + 与 bash 不重名的原名 `undo` / `append` /
  `prepend`）、`env.aggressive.sh`（另接管 `read` / `write` / `edit` / `grep`）、
  `env.restore.sh`（反注册，`--all` 连 `h*` / PATH / ns 一起拆）。

### 行为

- **serve 判据改为「模型见过即入基线」**：`read` / `grep` 的输出、`write` 回传的
  全文、`edit` / `append` / `prepend` 回传的 diff 正文（含上下文行）、锚点失效时
  回显的当前区间，都随即进 served，可直接拿这些锚点接着改。`--no-serve` 仍是不
  入基线的显式退出；`- ` 删除行、`...` 省略、`~~~` 超长行没有可用的锚点。
- **`append` / `prepend` 的 `FILE` 必填**（放在最前可省 `-f`，与 `read` / `write` /
  `edit` 一致），取消「从 served 基线反查锚点属于哪个文件」。可用形态：
  `append FILE ANCHOR CONTENT` / `append -f FILE ANCHOR CONTENT` /
  `append FILE -c ANCHOR CONTENT`。
- **替换文本里误带行首锚点**（把 read 输出整行贴回来，如 `CTt│foo`）：前缀命中本
  文件当前锚点 → `E_ANCHOR_IN_CONTENT` 拒写、不落盘；其余只给
  `W_SERVED_PREFIX_MISMATCH` / `W_NEVER_SERVED_SHAPE` 软警告（stderr；`--json` 时
  进 `warnings` 字段），内容照写。
- `E_BAD_ANCHOR` 按写错的形状给定向提示：带 `│` 的整行、多行块、行号各有说法。
- **回显改为三段**：一行确认（`edited ok` / `appended ok` / `prepended ok`）+
  git 风格差异正文 + 一行收尾（`路径 (旧行数 → 新行数[, +N][ -M])`）。收尾行的
  计数只算真正改变的行数；`-q` / `--quiet` 只留差异正文。
- 锚点失效（`E_STALE_RANGE` / `E_STALE_ANCHOR` / `E_AMBIGUOUS_ANCHOR`）的回显行
  随即进 served，hint 相应改为「可直接用回显的锚点重试」。
- 全仓行尾统一为 LF。

### 文档

- README、各工具 `-h`、`core-rweg prompt` 同步到上述行为。
- shell 层验收套件改跑 `env.aggressive.sh`（同名接管那一层），非侵入入口另立一节
  验证；`cd` 重复提示的措辞调整。

## v0.1-alpha

首个可跑版本：`read` / `write` / `edit` / `grep` / `undo` 五器，3 字符 base62 行锚、
served 基线、retired 槽位、原子写、UTF-8 / GBK / UTF-16 与 CRLF / BOM 保真，以及
`core-rweg` 的 `doctor` / `cfg` / `ns` / `selftest` / `prompt` 子命令。
