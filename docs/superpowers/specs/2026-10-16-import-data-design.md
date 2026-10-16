# 从文件导入数据 设计文档

- 日期：2026-10-16
- 状态：已实现
- 范围：从 CSV / TSV / XLSX 文件导入数据到已有表，或从文件新建表并导入

## 1. 背景

现在可以把数据 tab 或 console 结果导出成 TSV/CSV/JSON/Markdown/SQL/XLSX（`<Space>x …`，`internal/export`），但不能反过来把文件导入数据库。DataGrip 在表上右键有 "Import Data from File"，在库上可以从文件新建表。sqlvim 要提供类似的体验。

数据量：通常几万行以内，偶尔几十万行，不考虑百万行级。

## 2. 目标

- 树上选中表：从文件导入到这张表，追加数据。
- 树上选中库 / schema：从文件推断表结构，新建表并导入。
- 一个 Import tab 完成全部设置：文件格式、列映射（可以只选部分列，其余忽略）、转换后的预览。
- 全部成功或全部回滚：本地先 dry-run 校验整个文件，然后在一个事务里写入，任何一行出错就回滚，并报出行号、列和原因。
- 安全规则不变：只读拒绝、prod 确认、console 有事务或表有未提交修改时拒绝、断线不静默重连。

**不做：**
- 跳过坏行、把坏行写进错误文件（以后可以作为一个选项加上）。
- 主键冲突时跳过 / 覆盖（`INSERT IGNORE`、`ON DUPLICATE KEY UPDATE`、`ON CONFLICT`）。
- 导入前清空表。
- `LOAD DATA` / `COPY` 快速通道。
- JSON、SQL 文件导入（SQL 文件用 `:e`）。
- 文件路径补全、文件浏览器（和 `:e` 一样）。

## 3. 入口

- 树上 `a` 菜单：
  - 表节点加 "Import data from file…"；
  - 库 / schema 节点加 "Create table from file…"。
- Tree 上下文新增 `I`（action `ImportData`）：表节点等同 "Import data from file…"，库 / schema 节点等同 "Create table from file…"，其他节点在状态栏提示 `select a table, database or schema`。绑定写在 `keymap/defaults.go`，help、which-key、提示都由它生成。
- 选中后弹出 `overlay.NewPrompt` 输入路径，默认值是 export 目录（`settings.ExportDirectory`），支持 `~` 展开，相对路径按启动时的工作目录解析。
- 按扩展名判断格式：`.csv` → CSV，`.tsv` / `.tab` → TSV，`.xlsx` → XLSX，其他按 CSV 处理，之后可以在 tab 里修改。
- 打开 tab 前检查，不满足时拒绝并在状态栏说明原因：
  - 连接只读：`readOnlyHint`；
  - 导入已有表：`tableBlocked(conn, ref, nil)`（console 有事务、这张表有 staged 修改）；
  - 新建表：同一连接上有 console 开着事务时拒绝。

## 4. Import tab

实现 `tab` 接口，标题为 `Import → <table>`（新建表时为 `Import → <db>.<new table>`）。异步结果通过带 `TabID()` 的消息回到这个 tab。

```
 Import → app.users                                   orders.csv · CSV · UTF-8
 ┌ Source ─────────────────────────────────────────────────────────────────┐
 │ format: CSV   delimiter: ,   encoding: UTF-8   header: row 1   null: "" │
 └─────────────────────────────────────────────────────────────────────────┘
 ┌ Mapping ────────────────────────────────────────────────────────────────┐
 │ [✓] order_id     →  id           bigint        PK                       │
 │ [✓] customer     →  name         varchar(64)                            │
 │ [ ] note         →  (skip)                                              │
 │ [✓] created      →  created_at   datetime                               │
 └─────────────────────────────────────────────────────────────────────────┘
 ┌ Preview (first 20 rows, as they will be inserted) ──────────────────────┐
 │ id │ name  │ created_at          │                                      │
 │ 1  │ Alice │ 2026-01-02 10:00:00 │                                      │
 └─────────────────────────────────────────────────────────────────────────┘
```

- 三块区域用 `[` / `]` 切换焦点（和结构 tab 的 section 一致；`tab` 是全局的切换窗格），块内 `j` / `k` / `gg` / `G` 移动。新增 keymap 上下文 `Import`，底部提示用 `keyHint` 生成。
- `<Space>r`（Refresh）重新读取文件。
- 勾选框复用 `difftab.go` 的 `box`（`[✓]` / `[ ]`）。

### 4.1 Source

每行一个设置，`i` / `enter` 循环切换取值或弹出输入框：

| 设置 | 取值 | 适用 |
|---|---|---|
| format | CSV / TSV / XLSX | 全部 |
| delimiter | `,` `;` `|` 循环切换 | CSV |
| encoding | UTF-8 / GBK | CSV、TSV |
| header | 无 / 第 N 行（默认第 1 行） | 全部 |
| sheet | 文件里的 sheet 列表 | XLSX |
| null | 视为 NULL 的文本，默认空字符串，可设为 `NULL`、`\N` 等 | 全部 |
| table name | 新表名，默认取文件名（去掉扩展名；字母（含中文）、数字、`_` 以外的字符换成 `_`） | 新建表 |

- UTF-8 文件开头的 BOM 会被去掉。
- 没有表头时，文件列名为 `column_1`、`column_2`…
- 任何设置变化都会重新读取文件，刷新 Mapping 和 Preview。读取失败（文件不存在、编码错误、xlsx 损坏）时，错误显示在 Source 区，Mapping 和 Preview 为空。

### 4.2 Mapping

每个文件列一行：`[✓] 文件列 → 目标列 类型`。

- `x` 勾选 / 取消勾选该行（取消即跳过；`<Space>` 是 leader，和 diff tab 一样用 `x`），`a` 全选，`A` 全不选。
- **导入已有表**：
  - 打开时按列名自动匹配：不区分大小写，`_`、空格、`-` 视为相同。没匹配上的文件列默认不勾选。
  - `i` / `enter` 弹出 picker，从表的列里选目标列（生成列不在其中）。两个文件列不能映射到同一个表列，冲突时在状态栏提示，且不改变映射。
  - 表里没有被映射、NOT NULL、没有默认值、不是自增也不是生成列的列，在 Mapping 区底部用 error 色列出。存在这样的列时不能执行。
- **新建表**：
  - 每行是 `文件列 → 新列名 推断类型`。`i` / `enter` 改列名（默认值为文件列名，规则同表名），`t` 改类型（输入框，预填推断的类型）。
  - 新表不设主键。所有列可为 NULL，除非该列在整个文件里没有空值，这时设为 NOT NULL。
  - 列名重复或为空时不能执行。

### 4.3 Preview

显示前 20 行转换后的值（§6）。转换失败的单元格用 error 色显示原值，光标所在行的第一个错误显示在状态栏。dry-run 发现错误后，这一块改为显示错误列表（§7.1）。

## 5. 包结构

依赖方向不变：`ui` → `importer` → `sqlx` / `db`。执行逻辑（dry-run 和写入）也在 `importer` 里，和 `edit.Apply` 一样只依赖 `db.Querier`，UI 只负责连接、确认和显示。

### 5.1 `internal/importer`（新增，纯逻辑，不依赖终端）

- `source.go`
  - `Options{Format, Delimiter, Encoding, HeaderRow, Sheet, Null}`
  - `Open(path string, o Options) (Source, error)`
  - `Source` 接口：`Header() []string`、`Next() ([]string, error)`（结束返回 `io.EOF`）、`Line() int`（当前行在文件里的行号，从 1 开始，供报错使用）、`Close() error`
  - `Sheets(path string) ([]string, error)`：列出 xlsx 的 sheet
  - CSV / TSV：`encoding/csv`，设置 `Comma`、`LazyQuotes = true`、`FieldsPerRecord = -1`；GBK 用 `golang.org/x/text/encoding/simplifiedchinese` 解码（已是间接依赖，改为直接依赖）。
  - XLSX：excelize `Rows()` 流式读取格式化后的文本。打开时设 `ShortDatePattern: "yyyy-mm-dd"`、`LongTimePattern: "hh:mm:ss"`，日期单元格读出来就是 `2026-01-02` / `2026-01-02 12:00`，不需要处理日期序列号。超过 15 位的数字在 Excel 里已经变成浮点（读出来是 `1.23456789012346E+17`），转整数列时报错并提示把这一列设为文本。
- `convert.go`
  - `Kind`：Integer / Decimal / Bool / Date / DateTime / Time / String / Other
  - `KindOf(driver string, c db.Column) Kind`：按类型名归类（含 unsigned、长度、精度）
  - `Convert(col Target, cell string) (Value, error)`：按 §6 转换和校验，返回给 INSERT 的参数（或 `Default` 标记、NULL）
  - `RowError{Line int; Column, Value, Reason string}`
- `infer.go`
  - `Inferrer`：逐行 `Add`，最后 `Schema(ref, names)` 得到 `db.TableSchema`，规则见 §6.3。
- `plan.go`
  - `Plan{Driver, Table, Columns []Column, Width, Null}`：选中的文件列及其目标列
  - `BatchSize(cols int) int = min(500, 65535/cols)`
  - `Plan.Insert(rows) (sql string, args []any)`：一条多行 INSERT，值参数化（MySQL `?`，PostgreSQL `$n`，由 `sqlx.Dialect` 决定）；NULL 写 `NULL`、`Default` 标记的格写 `DEFAULT`，都不占参数。
- `run.go`
  - `Check`：§7.1 的 dry-run。
  - `Run`：§7.3、§7.4 的写入、定位坏行、回滚和 MySQL 的 DROP。

### 5.2 `internal/db`

- 驱动不变。`dbtest/contract.go`（它已经直接使用 `edit`、`schemadiff` 等包）加用例，直接调用 `importer.Run`：参数化多行 INSERT（含 NULL、`DEFAULT`、中文、长文本、字符串参数写入整数 / 日期 / 布尔列）；失败后回滚、表里没有残留；定位到坏行；新建表后导入并读回；MySQL 新建表失败后表被删掉。

### 5.3 `internal/ui`

- `importtab.go`：Import tab 的状态、渲染、按键。
- `importrun.go`：dry-run、执行、进度和完成消息。
- `nodemenu.go`、`treekeys.go`：入口。

## 6. 转换规则

### 6.1 按目标列类别

| 类别 | 接受的输入 | 发给数据库的值 |
|---|---|---|
| Integer（tinyint…bigint、serial 等） | `-12`、`1,234`（去掉千分位）、`3.0`（小数部分为 0） | 十进制字符串，检查类型范围（含 unsigned） |
| Decimal（decimal、numeric、float、double、real） | `1.5`、`1e3`、`1,234.5` | 规范化后的字符串；decimal(p,s) 检查整数位数，小数位超出时报错，不四舍五入 |
| Bool（PG `boolean`、MySQL `tinyint(1)` / `bool`） | `true/false`、`t/f`、`yes/no`、`y/n`、`1/0`，不区分大小写 | PG `true/false`，MySQL `1/0` |
| Date | `2026-01-02`、`2026/1/2`、时间部分为 0 的日期时间（`2026-01-02 00:00:00`） | `YYYY-MM-DD` |
| DateTime（datetime、timestamp、timestamptz） | `2026-01-02 10:00`、`…:00`、`….fff`、`T` 分隔、`2026/1/2 10:00`、只有日期（补 `00:00:00`）；timestamptz 接受 `Z` 或 `±hh:mm` 时区并保留 | `YYYY-MM-DD HH:MM:SS[.ffffff]`，或带时区的原值 |
| Time | `10:00`、`10:00:00[.fff]` | `HH:MM:SS[.ffffff]` |
| String（char、varchar、text、enum、set 等） | 原样 | 原样；char/varchar 按 rune 检查长度 |
| Other（json、uuid、bytea、geometry 等） | 原样 | 原样，由数据库校验 |

- 输入先去掉首尾空白，String 和 Other 除外。

### 6.2 空值和缺失

- 单元格等于 Source 的 null 文本（默认空字符串）时视为空值：
  - 列可为 NULL：NULL；
  - 列 NOT NULL 且有默认值或是自增：`DEFAULT`；
  - 否则报错 `cannot be empty`。
- 某行列数少于表头：缺失的列视为空值；多于表头且多出的格不全为空：报错 `N cells, header has M`（行尾多一个逗号这种全空的多余格忽略）。
- 全部单元格为空的行跳过，不算错误。

### 6.3 推断（新建表）

逐列扫描全部非空值，取能容纳所有值的最窄类别：

| 条件 | MySQL | PostgreSQL |
|---|---|---|
| 全部是整数，在 int64 范围内 | `bigint` | `bigint` |
| 全部是数字 | `decimal(p,s)`，p、s 取观察到的最大值，p ≤ 65 | `numeric(p,s)`，p ≤ 1000 |
| 全部是日期 | `date` | `date` |
| 全部是日期或日期时间 | `datetime` | `timestamp` |
| 其他，最长 ≤ 255 字符 | `varchar(n)`，n 为最长长度向上取到 16 的倍数 | `varchar(n)`，同左 |
| 其他 | `text` | `text` |
| 整列都为空 | `varchar(255)` | `text` |

Bool 不推断（`0/1` 会推断为 `bigint`），用户可以用 `t` 修改。

## 7. 执行

提交键沿用 `SubmitEdits`（`<Space>w` / `:w`），取消沿用 `Cancel`（`ctrl+c`）。

### 7.1 Dry-run

- 在 `tea.Cmd` 里重新打开文件，逐行用 §6 转换，不连接数据库。新建表时同时完成 §6.3 的推断（打开 tab 时已在后台做过一次，结果显示在 Mapping 里；用户改过的类型以用户为准）。
- 进度通过原子计数器 + `tea.Tick`（200ms）显示在 tab 底部：`checking 12,000 rows…`。
- 有错误时：在 tab 里列出前 50 条 `line 1234, created: "2026/13/01" is not a datetime`，以及总数，不进入下一步。

### 7.2 确认

- 导入已有表：`Insert 48,213 rows into app.users`。
- 新建表：显示 `CreateTable` 生成的 DDL 和 `Insert 48,213 rows`。新表在确认前已存在（`Meta().Tables` 里有同名表）时拒绝：`table app.orders already exists`。
- prod 连接：沿用 `runner.Policy` 的危险确认样式（`WithDanger`）。
- 确认时重新检查 §3 的拒绝条件，这期间状态可能已经变化。

### 7.3 写入

- 取一个专用 `Session`，第二次流式读取文件，每 `BatchSize` 行执行一条 `importer.Insert`。
- PostgreSQL：`BEGIN` →（新建表时 `CREATE TABLE …`）→ 全部 INSERT → `COMMIT`。
- MySQL：（新建表时 `CREATE TABLE …`，隐式提交）→ `BEGIN` → 全部 INSERT → `COMMIT`。
- 进度：`inserting 12,000 / 48,213 rows`。
- 写入期间 tab 只响应 `Cancel`。

### 7.4 出错

- **某一批失败**：每批执行前先 `SAVEPOINT sqlvim_import`（PostgreSQL 语句失败后事务处于 aborted 状态，必须事先设好 savepoint 才能继续）。失败后 `ROLLBACK TO SAVEPOINT sqlvim_import`，把这一批逐行重新执行，第一条失败的行就是坏行，报 `line 1234: <数据库错误>`，然后 ROLLBACK 整个事务。逐行重试没有找到失败的行（例如错误与批量有关）时，报 `lines 1001–1500: <原错误>`。
- **取消**：取消 context，ROLLBACK，提示 `import cancelled, nothing was written`。
- **断线**（错误 wrap `ErrConnLost`）：报 `connection lost, the transaction is gone and nothing was committed`，不重连重试。
- **MySQL 新建表**时，以上三种情况在 ROLLBACK 后执行 `DROP TABLE` 删掉刚建的表。DROP 失败时报 `table app.orders was created but is empty; drop it manually`。
- 日志只记录连接、表、行数、耗时、错误摘要，不记录单元格的值。

### 7.5 完成

- 状态栏：`imported 48,213 rows into app.users in 3.2s`。
- 刷新树上对应的节点（新建表时刷新库 / schema 节点，新表出现在树上），并让 `meta` 缓存失效。
- 已打开的这张表的数据 tab 不自动刷新，用户用现有的 Refresh 刷新。
- Import tab 保留，可以调整后再次导入；`q` 关闭。新建表导入成功后，再次提交会因表已存在而被拒绝，需要先改表名。

## 8. 测试

- `importer`（表驱动）：
  - source：CSV 引号、字段内换行、`;` 分隔、TSV、BOM、GBK、无表头、列数不齐；xlsx（测试里用 excelize 生成）的多 sheet、日期单元格、数字单元格。
  - convert：§6.1 每一类的合法值和边界（int 范围、unsigned、decimal 精度、日期格式、bool 写法、varchar 长度按 rune）；§6.2 的 NULL / `DEFAULT` / 报错。
  - infer：§6.3 每一行规则，以及两种引擎的类型写法。
  - plan：`BatchSize` 的占位符上限；`Insert` 的 SQL 和参数（含 `DEFAULT`、两种占位符）。
- `dbtest/contract.go`：§5.2 的用例，MySQL 8 和 PG 16 都跑。
- UI（fake backend，驱动 `Model.Update`）：
  - 入口：`I` 和 `a` 菜单在各种节点上的行为；只读、console 有事务、有 staged 修改时拒绝。
  - Mapping：自动匹配、`x` / `a` / `A`、`i` 选目标列和冲突提示、缺少必填列时不能执行、新建表的列名和类型修改。
  - 执行：dry-run 有错误时不执行；prod 确认；进度消息；取消后 ROLLBACK；某批失败时报具体行；MySQL 新建表失败后 DROP；完成后刷新树。

## 9. 文档

- `CHANGELOG.md` 的 `[Unreleased]` 加一条。
- `README.md` 和 `README.zh-CN.md` 说明导入功能和 `I` 键。

## 10. 实现差异

规格没有定下、实现时确定的几点：

- `Float` 单独作为一个类别（float、double、real），只有 `decimal` / `numeric` 检查精度。
- `timetz` 和 `time with time zone` 归为 Other，原样传给数据库。
- 推断为整数的列如果有前导零（`007`、邮编、电话号码），推断为 `varchar`，保留前导零。
- MySQL 上值带小数秒的日期时间列推断为 `datetime(6)`。
- 重新读取文件时，表头不变就保留映射（改 NULL 文本或编码不会丢掉已选的映射）；表头变了就重新映射。
- Source 区多一行 `file`，路径输错时可以在 tab 里直接改。
- 导入已有表时，如果表的主键只有一列、是整数、NOT NULL、没有默认值也不是自增，而文件里没有映射到它的列，Mapping 区会多一行 `[✓] (generate) → id … MAX(id)+1…`（默认勾选，`x` 取消）。写入时在事务里先锁表（MySQL `SELECT COALESCE(MAX(id), 0) … FOR UPDATE`，PostgreSQL `LOCK TABLE … IN SHARE ROW EXCLUSIVE MODE` 后再取 MAX），再按文件顺序从 MAX+1 编号；最后一个号超出列类型范围时，在写入任何一行之前报错。预览里这一列显示 `auto`。
