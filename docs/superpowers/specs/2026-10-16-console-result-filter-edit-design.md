# 控制台结果的过滤与编辑 设计文档

- 日期：2026-10-16
- 状态：已实现
- 范围：控制台查询结果支持表数据 tab 同款的过滤（`f` / `F`）与编辑（`i` `o` `dd` `u` `U` `:w`）；修订主设计 §5.7、§5.8

## 1. 背景

表数据 tab 已经能过滤和编辑（主设计 §5.7、§5.8，以及 `2026-10-14-table-filter-ux-design.md`），但控制台执行 SQL 得到的结果是只读的（§5.8："控制台查询结果只读"）。实际使用中，常见的流程是在控制台里手写 `SELECT * FROM orders WHERE …` 找到要改的行，此时还得另开一个表数据 tab，重新过滤一遍才能修改。

作者的主要用法是**单表查询**：自己写 WHERE、ORDER BY 缩小范围，然后编辑。

## 2. 目标

- 控制台的任意 SELECT 结果都能用 `f` 菜单和 `F` 过滤，在服务端重新执行。
- 能对应回单张表的结果可以直接在结果区编辑，按键、标记、预览、提交和表数据 tab 完全一致。
- 提交跟随控制台当前的事务状态：手动事务中的修改属于这个事务。
- 安全规则不变：只读连接、生产环境确认、每条修改语句必须正好影响 1 行、不在事务中悄悄重连。

**不做：**
- JOIN 结果的编辑（需要驱动提供每一列的来源表，例如 MySQL 的 `org_table`、pgx 的 `TableOID`）。
- 控制台结果的 `s` 列排序。
- 客户端（只在已加载行里）过滤。
- 往用户的 SQL 里偷偷加 `ctid`。

## 3. 可过滤与可编辑的判定

### 3.1 过滤

所有 SELECT 类的结果集都能过滤。多语句执行里的非查询结果、`SHOW`、`EXPLAIN` 等不能过滤，按 `f` / `F` 时提示 "filter works on SELECT results"。

### 3.2 编辑

判定逻辑放在 `sqlx`，是纯函数，返回 `Source` 或者不可编辑的原因：

```go
// Source is the table a console result was read from.
type Source struct {
	Table   db.TableRef // as written; unqualified names resolve against the console's namespace
	Columns []string    // the table column behind each result column
}

func ResultSource(d Dialect, stmt string, resultCols []string) (Source, string)
```

需要同时满足下面几条：

1. **单个 SELECT**：只有一个 FROM 表，没有 JOIN、FROM 中的子查询、UNION / INTERSECT / EXCEPT、GROUP BY、HAVING、DISTINCT、窗口函数、`WITH`。允许 WHERE（里面可以有子查询）、ORDER BY、LIMIT / OFFSET，表名可以带库名或 schema，表可以有别名。
2. **每一列都是普通列**：选择项只能是 `*`、`t.*`、`col`、`t.col`、`col AS x`、`t.col x`。表达式、函数调用、常量都不行。结果的每一列都要能对应到表里的一列；`*` 按结果列名对应。
3. **能找到这张表**：第一次按编辑键时，像表数据 tab 那样加载列和索引，用 `edit.ChooseKey` 得到 `Key`。找不到表时提示原因。
4. **定位行的列都在结果里**，否则已有的行只读，只能新增行：
   - `PrimaryKey` / `UniqueKey`：键的所有列都在结果里。
   - `AllColumns`（MySQL 没有键）：结果包含表的全部列。
   - `RowID`（PostgreSQL 没有键）：控制台结果里没有 ctid，已有的行只读。
5. **新增行**只能给结果里出现的列赋值，其他列使用默认值。

不满足条件时，编辑键在状态栏给出原因，例如：

- `read-only result: JOIN · edit single-table SELECTs`
- `read-only result: column 2 is an expression`
- `id (primary key) not in the result: rows can only be added`

## 4. 过滤的 SQL 改写

纯函数放在 `sqlx`，有单元测试。

MySQL 8 会去掉派生表里没有 LIMIT 的 ORDER BY，所以不能一律包成子查询：

- **可编辑的单表结果**：把条件合并进原语句顶层的 WHERE。
  - 原来有 WHERE：`WHERE (原条件) AND (新条件\n)`。
  - 原来没有：在顶层的 ORDER BY / LIMIT / OFFSET / FOR / 语句末尾之前插入 `WHERE (新条件\n)`。
  - 条件后换行再闭合括号，和 `tableQuery.sql` 一样，防止行尾的 `--` 注释吞掉后面的内容。
- **其他结果**：`SELECT * FROM (\n<原SQL>\n) q WHERE (条件\n)`。
  - 原语句末尾有顶层 ORDER BY（后面没有 LIMIT / OFFSET），且排序项都是结果里的列名或列序号时，把 ORDER BY 挪到外层。
  - 挪不了时照常执行，状态栏提示 `order may not be kept`。
- 改写后的语句经过 `checkSelect` 校验，防止一个过滤条件把它变成多条语句或写操作。`F` 的 `WithValidate` 用同一个校验。

行为：

- 过滤条件属于"当前显示的结果"，记下原始语句和当前条件；执行新的语句就清掉。
- `f` 菜单的每一项把条件用 AND 追加到当前条件上（`andFilter`），`fc` 清除过滤，和表数据 tab 一致。
- 加载更多（`LoadMore`）沿用当前条件。
- 状态栏显示当前过滤条件。
- 服务端拒绝的条件记进 `failedWhere`，再按 `F` 时带回来。
- 过滤后重新执行的查询照常写入历史。

## 5. 组件拆分

### 5.1 `editableResult`（`internal/ui/editable.go`）

把 `dataTab` 中和"一个可编辑结果"有关的状态和逻辑搬过来：

- 状态：`grid`、`table *edit.Table`、`key`、`cols`、`ref`、`dialect`、`submitting`、`gen`、`pendingSQL`、`retrySubmit`、`failedWhere`、`status`。
- 方法：`mark`、`editBlocked`、`editCell`、`cellEdited`、`insertRow`、`deleteRow`、`undoEdit`、`undoAll`、`submit`（到预览为止）、`changes`、`unsavedHint`、`filterValuePrompt`、`filterPrompt`、`checkFilter`、`completeFilter`，以及编辑键和 `f` 系列按键的分发。

两处行为不同，由宿主 tab 提供：

```go
type resultHost interface {
	// applyEdits runs the confirmed statements and returns editsAppliedMsg.
	applyEdits(stmts []string) tea.Cmd
	// refilter reloads the result with where ("" for no filter).
	refilter(where string) tea.Cmd
}
```

- `dataTab`：`applyEdits` 就是现有的 `apply`（单独开会话）；`refilter` 修改 `tableQuery.where` 后调用 `load(0, q)`。
- `consoleTab`：见 5.2 和第 6 节。

`dataTab` 内嵌 `editableResult`，对外行为和现有测试都不变。**这一步单独提交，是纯重构。**

`edit.Target` 增加 `Columns []string`（结果列 → 表列），`edit.Plan` 生成语句时用表列名；为空时按结果列名，表数据 tab 不需要改。

### 5.2 控制台

1. `runDoneMsg` 带回 SELECT 结果集时，调用 `sqlx.ResultSource`，为这个结果创建 `editableResult`，记下 `Source` 或不可编辑的原因。
2. 结果显示时，如果能对应回单表，就异步加载该表的列和索引（按表缓存在控制台里，执行 DDL 后清空）；加载完成前按编辑键提示 `reading the keys of <table>…`。复用 `dataTab` 的 meta 加载，结果通过带 `TabID()` 的消息送回。
3. 光标在结果区时，`consoleTab.Handle` 的顺序是 `gridAction` → `editableResult` → `LoadMore`。
4. 有暂存修改时：
   - 编辑器照常可以编辑。
   - 执行新语句、过滤、加载更多都被拦下，提示 `N unsaved changes · <space>w submits · U discards`（按键名通过 `keyHint` 生成）。
   - 控制台实现 `stager` 接口，关闭 tab、退出程序、断开连接时也会提醒，和表数据 tab 相同。
5. `refilter` 按第 4 节改写 SQL，再走 `execute`，所以确认、保护检查、取消、密码重试都和普通执行一样。

## 6. 提交与事务

提交流程：`:w` / `<Space>w` → `edit.Plan` → `runner.Guard`（只读、生产环境）→ 预览（生产环境标题加 `PROD ·`）→ 确认后执行。

都在控制台自己的会话上执行，复用 `execute` 里打开会话、密码、只读切换的逻辑：

| 控制台状态 | 执行方式 |
|---|---|
| 没有打开的事务，自动提交 | `edit.Apply`：BEGIN → 逐条执行并检查影响行数 → COMMIT；失败整体回滚 |
| 没有打开的事务，手动模式 | 和手动模式下执行任意写语句一样：开启事务，用 `edit.ApplyInTx` 执行，计入 `txWrites`，等 `:commit` |
| 事务已经打开 | `edit.ApplyInTx`，计入 `txWrites` |
| PostgreSQL 事务已出错（`txAborted`） | 拒绝提交，提示先 `:rollback` |

`edit.ApplyInTx`：

1. `SAVEPOINT datavim_edit`
2. 逐条执行，检查影响行数是否为 1
3. 全部成功：`RELEASE SAVEPOINT datavim_edit`；任何一条失败：`ROLLBACK TO SAVEPOINT datavim_edit`，事务里之前的写操作保留

其他规则：

- **连接断开**：不重试，不在事务里悄悄重连（沿用 `errTxLost` 的处理）；暂存的修改保留，状态栏提示 `✗ changes kept · …`。
- **成功后**：清空暂存，在同一个会话里重新执行当前显示的语句（带过滤条件）。手动事务中因此能看到还没提交的结果。
- **只读连接**：编辑键直接提示只读；`:set noreadonly` 之后可以编辑。
- **历史**：修改语句写入历史，和表数据 tab 一样。

## 7. 测试

- **`sqlx`（表驱动）**
  - `ResultSource`：单表、别名、`*`、`t.*`、`col AS x`、带库名或 schema 的表名、WHERE 里的子查询；拒绝 JOIN、FROM 子查询、UNION、GROUP BY、HAVING、DISTINCT、窗口函数、`WITH`、表达式列和常量列。
  - 过滤改写：合并进 WHERE（原来有 / 没有 WHERE，有 ORDER BY、LIMIT、行尾 `--` 注释）；包装子查询；把 ORDER BY 挪到外层；挪不了时给出提示。
- **`edit`**
  - `ApplyInTx`：用 fake Querier 检查 SAVEPOINT / RELEASE / ROLLBACK TO 的语句顺序，以及影响行数不为 1 时的回滚。
  - 列对应：结果列名和表列名不同时，`Plan` 生成的语句用表列名。
  - `dbtest` 契约：增加 SAVEPOINT 回滚用例，MySQL 和 PostgreSQL 都要通过。
- **`ui`**
  - 重构后现有的 `datatab_edit_test`、`filter_test`、`submit_test` 不改动，全部通过。
  - 控制台：单表 SELECT 后 `f` 过滤实际执行的 SQL；编辑 → 预览 → 提交；手动事务走 SAVEPOINT 路径并计入 `txWrites`；`txAborted` 时拒绝提交；JOIN 结果和缺键的结果按编辑键的提示；有暂存修改时执行新语句被拦下；关闭 tab 时的提醒。

## 8. 文档

- 主设计 §5.7：过滤也适用于控制台的 SELECT 结果。
- 主设计 §5.8：删掉"控制台查询结果只读"，改为指向本文第 3 节的条件和第 6 节的事务规则。
- `CHANGELOG.md` 的 `[Unreleased]` 加一条。
- `README.md` 和 `README.zh-CN.md` 同步更新。
