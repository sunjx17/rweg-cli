# 更新日志

只记对外行为 / 接口 / 文档的变化；实现细节看 `git log`。

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
