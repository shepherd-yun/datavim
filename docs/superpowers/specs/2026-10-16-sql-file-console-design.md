# SQL 文件 设计文档

- 日期：2026-10-16
- 状态：设计中
- 范围：A：在 console 里打开、编辑、执行和保存 `.sql` 文件（本文档）；B：流式执行大脚本（只写梗概，单独出设计）

## 1. 背景

现在 SQL 只能在 console 里执行，console 的内容来自用户手敲、草稿、历史或 diff 生成的脚本。没有办法打开磁盘上的 `.sql` 文件，`<Space>v` 只是借一个临时文件给 `$EDITOR` 用。

拿到一个 SQL 文件时，常见的两种情况：

- **A**：文件不大，想边看边改边跑，跑完把修改存回去。
- **B**：迁移脚本或 dump，几十到几百 MB，只想可靠地跑完，看进度和出错情况。

先做 A。B 见 §9。

## 2. 目标

- `:e <path>` 把文件打开成一个**文件 console**，连接和库继承当前上下文。
- 文件 console 的执行方式和普通 console 完全一样，安全规则（只读、prod 确认、无 WHERE、事务、不静默重连）不变。
- 新增"从光标执行到末尾"，脚本中途失败、修好后可以接着跑。
- `:w` 存文件，`:w <path>` 另存，外部改动和未保存修改都不会被悄悄覆盖或丢掉。
- 打开过的文件进入 `<Space>f` 最近列表。

**不做：**
- 在 console 里切换连接或库。要换，就在别的节点上再 `:e` 一次。
- 外部修改后自动重新加载（只在保存时检查）。
- 出错后继续执行、大文件流式执行（属于 B）。
- 文件路径补全、文件浏览器。

## 3. 打开文件

### 3.1 `:e <path>` / `:edit <path>`

- 路径支持 `~` 展开，相对路径按 sqlvim 启动时的工作目录解析，然后转成绝对路径并 `filepath.Clean`。
- 连接和库取当前上下文：
  - 当前 tab 是 console、数据 tab 或结构 tab：用它的 `Target()`（连接 + Namespace）。
  - 焦点在树上：用光标所在节点的连接和库（连接节点用连接的默认库，PostgreSQL 没有默认库时和 `<Space>c` 的处理一致）。
  - 都没有：状态栏报错 `open a console or select a connection first`。
- 文件不存在：打开一个空的文件 console，标题显示 `name.sql [new]`，第一次 `:w` 时创建文件（和 vim 一致）。父目录不存在时 `:w` 报错。
- 文件超过 2 MB：拒绝打开，提示 `<name> is <size>: too large for the editor`。B 完成后改成引导到脚本执行。
- 文件不是合法 UTF-8 或含 NUL：拒绝打开，提示 `<name> is not a UTF-8 text file`。
- 同一文件已经在同一连接和库上打开：切换到那个 tab，不重复打开。同一文件在不同连接上可以同时打开，各自是独立的 tab。

### 3.2 最近列表

打开文件 console 时记一条 `history.Recent{Kind: RecentFile, Conn, Database, Schema, Name: <绝对路径>}`。`Name` 字段原本存表名，这里存路径，`recent` 表结构不变，不需要迁移。

`<Space>f` 里显示为 `file  <path>  <conn>/<db>`，选中后用记录的连接和库重新打开（文件已被删除时，和 `:e` 不存在的文件一样打开为新文件）。

## 4. 文件 console

文件 console 就是 `consoleTab`，多一个 `file *consoleFile` 字段：

```go
type consoleFile struct {
	path    string    // 绝对路径
	modTime time.Time // 读入或上次保存时的 mtime
	size    int64
	saved   string    // 读入或上次保存时的内容，用来判断是否修改过
	isNew   bool      // 磁盘上还没有这个文件
}
```

- tab key：`file:<path>|<conn>|<ns>`。和 `scratchKey` 一样，不读也不存草稿，内容以文件为准。
- 标题：文件名（`base`），修改过加 ` [+]`，新文件加 ` [new]`。状态栏显示完整路径（`~` 缩写）。
- 是否修改过：比较 `editor.Value()` 和 `saved`。

### 4.1 执行

- `Enter`（光标处或选区）和 `R`（全部）照旧，Guard、确认、事务全部复用。
- 新增动作 `RunFromCursor`：从光标所在语句执行到最后一条（光标在两条语句之间时从下一条开始）。默认绑定在 Console 上下文，键位在实现计划里选一个没被占用的，所有提示都用 `keyHint` 生成。普通 console 里也能用。
- 多条执行失败时，状态栏写成 `statement N/M failed: <错误>`，光标移到失败语句的出错位置（现有 `locateError` 已经能定位，这里补上 N/M 和光标跳转）。

## 5. 保存

### 5.1 命令

| 命令 | 文件 console | 普通 console | 其他 tab |
|---|---|---|---|
| `:w` | 存文件 | 不变：有暂存修改就提交，否则报 nothing to write | 不变 |
| `:w <path>` | 另存为 `<path>`，tab 改绑新路径 | 存到 `<path>`，tab 变成文件 console | 报错 |
| `:w! [path]` | 跳过外部修改检查，强制写 | 同左 | 报错 |
| `:wq` | 存成功后关闭 tab | 报错 | 报错 |

文件 console 里，结果表的暂存修改只能用 `<Space>w`（`SubmitEdits`）提交，`:w` 不再提交它们。

另存时如果目标文件已经在同一连接和库上被另一个 tab 打开，报错 `<path> is open in another tab`。目标已存在且不是当前文件时，弹确认 `overwrite <path>?`。

### 5.2 写入

- 在 `tea.Cmd` 里完成：同目录下 `CreateTemp` → 写入 → `Sync` → `Chmod` 为原文件权限（新文件 0644）→ `Rename`。返回 `fileSavedMsg{tabID, path, modTime, size, text, err}`。
- 保存前 `Stat` 原文件，mtime 或 size 和记录的不一致时，弹确认 `<name> changed on disk since it was opened — overwrite?`，确认后按 `:w!` 写入。文件被删除时直接写（相当于重新创建）。
- 保存成功后更新 `saved`、`modTime`、`size`，清掉 `[+]` / `[new]`，状态栏显示 `"<name>" written, <n> lines`。
- 失败时状态栏报错，内容和修改状态不变。
- 写入的是编辑器原文，不做换行或编码转换。读入时去掉 UTF-8 BOM，保存时也不加回去（在 CHANGELOG 里说明）。

### 5.3 关闭和退出

`requestClose` / `requestQuit` 现在会在有未结束事务、有暂存修改时确认。再加一种原因：文件 console 有未保存修改，提示 `<name> has unsaved changes`。`:q!`、`:qa!` 直接丢弃。

## 6. 分层

- `internal/ui/consolefile.go`：`consoleFile`、读写文件的 `tea.Cmd`、`fileLoadedMsg` / `fileSavedMsg`、外部修改检查。UI 不直接做 IO。
- `app.go`：`runCommand` 解析 `e` / `edit` / `w <path>` / `w!` / `wq`，`openFile` 处理上下文继承和去重，关闭/退出确认加未保存原因。
- `console.go`：标题、key、草稿跳过，`RunFromCursor`，执行失败时的 N/M 和光标跳转。
- `recent.go` / `history`：`RecentFile` 类型和显示。
- `keymap/defaults.go`：`RunFromCursor`。

## 7. 测试

UI 测试，文件放在 `t.TempDir()`：

- `:e` 在 console、数据 tab、树节点上分别继承正确的连接和库；没有上下文时报错。
- 同文件同连接重复 `:e` 切到已有 tab；不同连接各开一个。
- 不存在的文件打开为 `[new]`，`:w` 后创建；父目录不存在时报错。
- 超过 2 MB 和非 UTF-8 文件被拒绝。
- 修改后标题有 `[+]`，`:w` 后消失，磁盘内容正确，权限保持。
- 文件 console 里 `:w` 不提交暂存修改，`<Space>w` 提交；普通 console 里 `:w` 行为不变。
- 外部修改后 `:w` 弹确认，`:w!` 直接写。
- `:w <path>` 让普通 console 变成文件 console；另存到已打开的路径报错。
- 有未保存修改时 `:q`、`:qa` 要确认，`:q!` 不确认。
- `RunFromCursor` 执行的语句范围；多条失败时的 N/M 状态和光标位置。
- 最近列表记录文件并能重新打开。

## 8. 文档

- CHANGELOG `[Unreleased]`：打开/保存 SQL 文件、从光标执行到末尾。
- README 和 README.zh-CN 都加上 `:e` / `:w` 的说明。
- 主设计文档的 `:w` 说明加一句文件 console 的例外。

## 9. 后续：B 脚本执行（梗概）

单独出设计，这里只记方向：

- 入口：连接/库节点菜单 "Run SQL script…"、`:source <path>`，以及 §3.1 里文件太大时的引导。
- 一个只读的"Script run" tab：边读文件边切分边执行（`sqlx.Split` 需要改成流式），显示进度（第几条、字节百分比、耗时、当前语句），可以暂停或取消。
- 出错时让用户选停止、跳过、全部跳过；可选整体包在一个事务里。
- 执行前扫一遍整个文件，按语句类型计数，交给 Guard：只读连接拒绝，prod 弹确认。
- 结束后给汇总（成功、失败、影响行数），失败的语句能在文件 console 里定位。
- 之后再考虑命令行 `sqlvim run -c <conn> file.sql`。
