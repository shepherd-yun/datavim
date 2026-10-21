# SQL Server 与 Oracle 支持 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `sqlserver` 和 `oracle` 驱动，查询、浏览、结构查看、暂存改数据、导出、重命名/清空/删除表都可用，安全规则对这两个引擎依然有效。

**Architecture:** 先把散在 UI 和纯包里的「mysql / postgres 二选一」改成查询 `sqlx.Dialect` 上的一张能力表（`Features`），这一步行为不变。之后按 SQL Server、Oracle 的顺序，各自实现方言（`sqlx`）、行定位（`edit`）、驱动（`internal/db/<engine>`），再接入 UI 和配置。没有服务端只读开关的引擎，由 `runner.Guard` 用更严格的客户端规则兜底。

**Tech Stack:** Go 1.26、`github.com/microsoft/go-mssqldb`、`github.com/sijms/go-ora/v2`、testcontainers-go（`modules/mssql`，Oracle 用 GenericContainer + `gvenzl/oracle-free:23-slim`）。

**Spec:** `docs/superpowers/specs/2026-10-16-sqlserver-oracle-design.md`

## Global Constraints

- 驱动名固定为 `sqlserver` 和 `oracle`；默认端口 1433、1521。
- 只用纯 Go 驱动，不能引入 cgo，也不能依赖 Oracle Instant Client。
- SQL Server 的字符串字面量一律写成 `N'…'`。
- SQL Server 和 Oracle 都是 `DatabaseScoped() == true`。
- 第一版不支持的功能（Modify Table、schema 对比、数据对比、导入、`CreateTable` / `AlterTable`）在新引擎上提示 `not supported for <driver> yet`，然后直接返回。
- 只读连接：对没有服务端只读开关的引擎，认不出的语句（`Kind == Other`）一律拦截，只有白名单里的语句放行（spec §6，作者已确认采用严格规则）。
- 对 dialect 的每个 `switch` 都要把四种 dialect 写全，不能用 default 兜底成 MySQL。
- UI 里不能用驱动名判断引擎（`cfg.Driver == "…"`），统一改用 `sqlx.DialectOf(cfg.Driver).Features()`。
- 每个用户能看到的改动，都要在 `CHANGELOG.md` 的 `[Unreleased]` 下加一行；README 改动同步到 `README.zh-CN.md`。
- 提交信息用 Conventional Commits 并带 scope，不加 Co-Authored-By。
- 日志和界面里都不能出现密码，包括 DSN 里的密码。

## Review Focus

1. **SQL Server 批次里没有分号**：`SELECT 1 DELETE FROM t` 在只读或 prod 连接上必须被识别为写操作，并且要报缺 WHERE。测试放在 Task 5。
2. **SQL Server 写入中文**：暂存修改把 `张三` 写进 `nvarchar` 列，读回来必须还是 `张三`，不能变成 `??`。测试放在 Task 7 的契约补充。
3. **Oracle 控制台事务**：执行 `SET TRANSACTION READ WRITE` 后再 `INSERT`，从另一条连接查不到这行；`ROLLBACK` 后这行消失。如果 go-ora 在事务外自动提交，这个测试会失败。测试放在 Task 11。
4. **大小写不同的 Oracle 标识符**：建表 `"MixedCase"` 后，树、数据表格和暂存修改都要能正确加引号。测试放在 Task 9（QuoteIfNeeded）和 Task 11 的契约。
5. **SQL Server 2014 的 TLS**：配了 `params.encrypt = "disable"` 时，DSN 里要带上这个参数，`tlsmin` 也要原样透传。测试放在 Task 7 的 DSN 单元测试；真机验证在 Task 8。

---

## Phase A：把引擎二选一改成按能力判断（行为不变）

### Task 1: `sqlx.Dialect` 新增两个取值和能力表

**Files:**
- Modify: `internal/sqlx/lex.go:10-24`（Dialect 常量、`DialectOf`）
- Create: `internal/sqlx/features.go`
- Test: `internal/sqlx/features_test.go`

**Interfaces:**
- Produces:
  ```go
  const ( MySQL Dialect = iota; Postgres; SQLServer; Oracle )
  func DialectOf(driver string) Dialect // "mysql"|"postgres"|"sqlserver"|"oracle"; 其他 → MySQL（配置校验会先拒绝未知驱动）
  func (d Dialect) Driver() string      // DialectOf 的逆映射
  type RowLocator int
  const ( LocateNone RowLocator = iota; LocateAllColumns; LocateRowID )
  type Features struct {
      Databases      bool       // 服务器上有多个库，树有库这一层：MySQL, PG, SQL Server
      Schemas        bool       // 命名空间有 schema 这一层：PG, SQL Server, Oracle
      ServerReadOnly bool       // 连接能在服务端设为只读：MySQL, PG
      TxDDL          bool       // DDL 能放在事务里回滚：PG, SQL Server
      AbortedTx      bool       // 事务中出错后只能 ROLLBACK：PG
      DDLCommits     bool       // DDL 会隐式提交当前事务：MySQL, Oracle
      SchemaTools    bool       // Modify Table / schema 对比 / 数据对比 / 导入：MySQL, PG
      ILike          bool       // 支持 ILIKE：PG
      RowLocator     RowLocator // 没有键时怎么定位行：MySQL AllColumns, PG RowID, SQL Server AllColumns, Oracle RowID
      DefaultSchema  string     // 未指定时用的 schema：PG "public"，SQL Server "dbo"，Oracle ""（当前用户）
  }
  func (d Dialect) Features() Features
  ```

- [ ] **Step 1: 写失败的测试** `TestFeatures`：表驱动测试，四个 dialect 各自对照上面注释里的取值；`TestDialectOf` 检查四个名字能双向映射（`DialectOf(d.Driver()) == d`）。
- [ ] **Step 2: 运行** `go test ./internal/sqlx -run 'TestFeatures|TestDialectOf'`，预期 FAIL（`SQLServer` 未定义）。
- [ ] **Step 3: 实现**。`Features()` 用一个按 Dialect 下标取值的数组即可。
- [ ] **Step 4: 运行** `go test -race ./internal/sqlx`，预期 PASS。
- [ ] **Step 5: 提交** `refactor(sqlx): Dialect 增加 SQLServer、Oracle 与能力表`

### Task 2: 用能力表替换 UI 和纯包里的引擎判断

把下面每一处的 `driver == "postgres"`、`d == sqlx.Postgres`、`d == sqlx.MySQL` 换成 `Features()` 的对应字段，MySQL / PG 的行为保持不变：

| 位置 | 改成 |
|---|---|
| `ui/completion.go:25` `defaultNamespace` | `Schemas` 为真时 schema 默认取 `DefaultSchema`；`Databases` 为假时 `Database` 置空 |
| `ui/completion.go:156,179` `qualified` / 命名空间候选 | `Schemas` |
| `ui/metadata.go:35` `metaKeyFor` | 依据 `DatabaseScoped` 语义：`Databases && Schemas` → 每库一个 key（PG、SQL Server）；只有 Databases → 整台服务器一个 key（MySQL）；没有 Databases → `{Conn}`（Oracle）。PG 的兜底库名 `"postgres"` 只在 PG 上用：给 `Features` 加 `FallbackDatabase string`（PG `"postgres"`，SQL Server `"master"`，其余为 `""`） |
| `ui/nodemenu.go:43`、`ui/importrun.go:55`、`ui/compare.go:25` | `n.Kind == tree.KindDatabase && !f.Schemas` |
| `ui/console.go:490` | `AbortedTx` |
| `ui/console.go:513` | `DDLCommits`（日志文字改成 `d.Driver()` + ` committed the open transaction implicitly before …`，MySQL 原来的文字保持不变） |
| `ui/console.go:966` `scopeSQL` | 签名改为 `scopeSQL(d sqlx.Dialect, q db.Dialect, ns db.Namespace) string`，按 dialect 用 `switch` 写全：MySQL `USE`，PG `SET search_path`，SQL Server `USE [db]`，Oracle `ALTER SESSION SET CURRENT_SCHEMA = "X"` |
| `ui/tabs.go:209` | 判断是不是视图的那段，条件改为 `f.RowLocator == sqlx.LocateRowID` |
| `ui/tabs.go:447`、`ui/tablequery.go:99` | `ILike` |
| `ui/transfer.go:70` `tableParts` | 参数改为 `sqlx.Dialect`：`Schemas` 为真时返回 `[Schema, Name]`，否则返回 `[Database, Name]`（导出的 INSERT 不带库名，SQL Server 导出为 `[dbo].[t]`） |
| `edit/key.go:51` `ChooseKey` | `switch f.RowLocator`：RowID 且不是视图 → `RowID`，AllColumns → `AllColumns` |
| `edit/apply.go:95` `ApplyDDL` | `TxDDL` |
| `edit/plan.go:153` 空 INSERT | `switch d`：PG、SQL Server、Oracle 用 `DEFAULT VALUES`（Oracle 这里先保留，Task 10 改正）；MySQL 用 `() VALUES ()` |
| `importer/*`、`schemaedit/table.go:207` | 这些只服务 SchemaTools 功能，保留现有的二选一判断，在函数开头加注释说明只会在 `Features().SchemaTools` 为真时被调用 |
| `config/config.go:109` | `defaultPorts` 加上 `"sqlserver": 1433, "oracle": 1521` |

还要做：在 Modify Table、schema 对比、数据对比、导入的 UI 入口（`ui/modify.go`、`ui/compare.go`、`ui/importrun.go`、`ui/structedit.go` 进入编辑的地方）判断 `!Features().SchemaTools`，为真时用现有的状态栏提示 `not supported for <driver> yet` 并返回。

**Files:** 上表列出的文件，以及各自的 `_test.go`。

**Interfaces:**
- Consumes: Task 1 的 `Features()`、`Driver()`
- Produces: `scopeSQL(d sqlx.Dialect, q db.Dialect, ns db.Namespace) string`；`Features.FallbackDatabase string`

- [ ] **Step 1: 写失败的测试**。用新 dialect 驱动出新行为，旧 dialect 的现有测试保持不变：
  - `TestDefaultNamespace`：`sqlserver` 配置、空 ns → `{Database: cfg.Database, Schema: "dbo"}`；`oracle` → `{Schema: ""}`
  - `TestMetaKeyFor`：`sqlserver` 加库 `crm` → `{Conn, "crm"}`；`sqlserver` 加空库 → `{Conn, "master"}`；`oracle` → `{Conn}`
  - `TestScopeSQL`：SQL Server `{crm, dbo}` → `USE [crm]`；Oracle `{"", "HR"}` → `ALTER SESSION SET CURRENT_SCHEMA = "HR"`（db.Dialect 用测试里的假实现）
  - `TestChooseKeyFallback`：SQLServer 无键 → `AllColumns`；Oracle 无键的表 → `RowID`，无键的视图 → `NoKey`
  - `TestSchemaToolsGate`：在 fake backend 上建一个 `sqlserver` 连接，在表节点上触发 Modify Table 的 action，断言没有打开新 tab，状态栏是 `not supported for sqlserver yet`
- [ ] **Step 2: 运行** `go test -race ./internal/ui ./internal/edit ./internal/config`，预期上面这些测试 FAIL。
- [ ] **Step 3: 按上表实现**。
- [ ] **Step 4: 运行** `make test`，预期全部 PASS（MySQL / PG 的已有测试一条都不用改）。
- [ ] **Step 5: 运行** `grep -rnE 'Driver *[!=]= *"|"(mysql|postgres)"' internal/ui internal/edit internal/export`，结果只剩连接表单的选项、树图标和测试文件。
- [ ] **Step 6: 提交** `refactor(ui): 用方言能力表代替 mysql/postgres 判断`

### Task 3: 契约测试改成按能力跳过

**Files:**
- Modify: `internal/db/dbtest/contract.go`（`Fixture`，以及 :67、:136、:276、:556、:708、:751、:804、:855、:870、:902、:1058 这些子测试）
- Modify: `internal/db/mysql/integration_test.go`、`internal/db/postgres/integration_test.go`

**Interfaces:**
- Produces:`Fixture` 新增字段
  ```go
  Dialect sqlx.Dialect // 代替 Driver string；能力从 Dialect.Features() 读
  EmptyIsNull bool     // Oracle：'' 存进去读回来是 NULL
  NVarcharSQL string   // 建表 uni(id 主键, name 存 Unicode 的列)，供中文往返测试用
  ```
  删除 `Driver string`，原来读 `f.Driver` 的地方改用 `f.Dialect`。

- [ ] **Step 1: 改契约**：
  - `!Features().SchemaTools` 时跳过（`t.Skip("schema tools are not supported on " + d.Driver())`）TableSchema 规范化、schema sync、NormalizeType、ApplyDDL、Modify Table、data sync；
  - `!ServerReadOnly` 时跳过「ReadOnly connections reject writes on the server」；
  - `EmptyIsNull` 时，「NULL 和空串不同」改为断言空串读回来是 NULL；
  - 「rows without a key」按 `RowLocator` 分支，不再看 Driver；
  - 新子测试「Unicode text round-trips through staged edits」：在 `NVarcharSQL` 建的表里用 `edit` 计划插入 `张三`，再改成 `李四`，每一步读回来都要一致。MySQL / PG 的 fixture 也要提供这张表（utf8mb4 / text）。
- [ ] **Step 2: 运行** `make test-integration`，预期 MySQL / PG 全部 PASS，新子测试也通过。
- [ ] **Step 3: 提交** `test(db): 契约测试按方言能力跳过，新增 Unicode 往返测试`

## Phase B：SQL Server

### Task 4: T-SQL 方言（`sqlx`）

**Files:**
- Modify: `internal/sqlx/lex.go`、`literal.go`、`split.go`、`tx.go`、`keywords.go`、`next.go`（`LIMIT` 提示）、`filtered.go`
- Test: 对应的 `_test.go`

**Interfaces:**
- Produces（SQLServer 下各函数的行为）：
  - `QuoteIdent(SQLServer, "a]b")` → `[a]]b]`
  - `QualifiedName(SQLServer, {crm, dbo, t})` → `[crm].[dbo].[t]`；库名为空时 → `[dbo].[t]`
  - `QuoteIfNeeded`：合法的普通标识符不加引号，其他用 `[…]`
  - `Literal(SQLServer, "nvarchar", "it's")` → `N'it''s'`；binary 加 `0x…` → 原样；数字 → 原样；反斜杠不转义
  - `BeginSQL(SQLServer)` → `BEGIN TRANSACTION`
  - 词法：`[ident]`（`]]` 是转义）、`"ident"`、`N'…'`、`--`、可嵌套的 `/* */`
  - `Split`：单独一行的 `GO`（大小写不敏感，前后可以有空白，后面可以跟次数，次数忽略）也是分隔符
  - `TransactionEffect`：`BEGIN TRAN[SACTION] [name]` → TxBegin；`BEGIN` 单独出现或 `BEGIN TRY` → TxNone；`COMMIT [TRAN[SACTION]] [WORK]` → TxEnd；`ROLLBACK TRAN x`（带名字）→ TxResume；`ROLLBACK [TRAN]` → TxEnd；`SAVE TRAN x` → TxNone
  - 关键字表加入 `TOP`、`OFFSET`、`FETCH`、`NEXT`、`ROWS`、`ONLY`、`NOLOCK`、`OUTPUT`、`MERGE`、`EXEC` 等；SQL Server 的下一个关键字建议里不出现 `LIMIT`，改成 `TOP` / `OFFSET`
  - `Filtered`：SQL Server 和 MySQL 一样，派生表里不能有不带 `TOP`/`OFFSET` 的 ORDER BY，要把它移到外面

- [ ] **Step 1: 写失败的表驱动测试**：`TestQuoteIdentSQLServer`、`TestQualifiedNameSQLServer`、`TestLiteralSQLServer`（包括中文 `N'张三'`）、`TestSplitGO`（`"select 1\nGO\nselect 2"` → 2 条；`"select 'GO'\n"` → 1 条；`"  go 5  \n"` 也算分隔）、`TestTransactionEffectSQLServer`（上面列的每一种）、`TestFilteredSQLServerOrderBy`。
- [ ] **Step 2: 运行** `go test ./internal/sqlx`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/sqlx`，预期 PASS。
- [ ] **Step 5: 提交** `feat(sqlx): T-SQL 方言`

### Task 5: 没有服务端只读的引擎，客户端守卫更严格

**Files:**
- Modify: `internal/sqlx/analyze.go`
- Modify: `internal/runner/runner.go`（`Guard`）
- Test: `internal/sqlx/analyze_test.go`、`internal/runner/runner_test.go`

**Interfaces:**
- Produces:
  ```go
  // AnalyzeBatch classifies every statement in a T-SQL batch or PL/SQL block:
  // stmt may hold several statements without semicolons. It returns one Info
  // per DML/DDL keyword found at statement-start positions (outside strings
  // and comments), or a single Analyze(stmt) when there is none.
  func AnalyzeBatch(stmt string, d Dialect) []Info
  // SafeOther reports a statement of Kind Other that cannot write: the
  // dialect's allowlist (spec §6).
  func SafeOther(stmt string, d Dialect) bool
  ```
  `Guard` 在 `!d.Features().ServerReadOnly` 时，对每条语句改用 `AnalyzeBatch` 的结果：任意一项是 Write/DDL 就算写；`MissingWhere` 逐项检查；只读时如果 `Kind == Other && !SafeOther` 就 Reject，原因是 `read-only connection: <first word> statements are blocked on <driver> (:set noreadonly allows them for this session)`。
- SQL Server 白名单：`USE`、`SET <选项>`（`SET` 之后不跟 `@var =` 的赋值；`SET @x = …` 也算安全）、`DECLARE @…`、`PRINT`、`EXEC[UTE] sp_help|sp_helptext|sp_helpindex|sp_columns|sp_tables|sp_who|sp_who2|sp_spaceused|sp_databases|sp_server_info`。（Oracle 的白名单在 Task 9 补充。）

- [ ] **Step 1: 写失败的测试** `TestGuardSQLServerReadOnly`（表驱动，`Policy{ReadOnly: true}`）：
  - Allow：`SELECT 1`、`USE crm`、`SET NOCOUNT ON`、`DECLARE @a int`、`EXEC sp_help 'dbo.t'`、`select 'DELETE FROM t'`、`-- delete from t\nselect 1`
  - Reject：`EXEC dbo.usp_clean`、`EXECUTE('delete from t')`、`SELECT 1 DELETE FROM t WHERE id=1`、`BEGIN DELETE FROM t WHERE id = 1 END`、`MERGE t USING …`、`SELECT * INTO x FROM t`
  
  再写 `TestGuardSQLServerMissingWhere`（`Policy{Prod: true}`）：`SELECT 1 UPDATE t SET a=1` → Confirm，`Expect == "t"`。
- [ ] **Step 2: 运行** `go test ./internal/runner ./internal/sqlx`，预期 FAIL。
- [ ] **Step 3: 实现**。语句开头位置指：片段开头、`;` 之后、`BEGIN` / `END` / `ELSE` / `THEN` 之后，以及前一个语句完整结束之后。实现时可以简化成「深度 0、不在字符串和注释里、前一个 token 不是 `.` 的 DML/DDL 关键字」，但要排除 `notDMLPrev`（`FOR UPDATE`、`ON DELETE`）。MySQL / PG 仍然只用 `Analyze`。
- [ ] **Step 4: 运行** `go test -race ./internal/runner ./internal/sqlx`，预期 PASS。
- [ ] **Step 5: 提交** `feat(runner): 无服务端只读的方言用整批扫描与白名单守卫`

### Task 6: SQL Server 的行定位（`edit`）

**Files:**
- Modify: `internal/edit/plan.go`（`limit()`、`warning()`、UPDATE/DELETE 的写法）
- Test: `internal/edit/plan_test.go`

**Interfaces:**
- Produces：`Key.Kind == AllColumns` 且 dialect 是 SQLServer 时，生成 `UPDATE TOP (1) [dbo].[t] SET … WHERE …` 和 `DELETE TOP (1) FROM [dbo].[t] WHERE …`。警告文字是 `… rows are matched by all their values, one row at most (TOP (1))`。不可比较的类型加上 `TEXT NTEXT IMAGE XML GEOGRAPHY GEOMETRY HIERARCHYID SQL_VARIANT`。

- [ ] **Step 1: 写失败的测试** `TestPlanSQLServerAllColumns`：对无键表，期望的 UPDATE/DELETE 文本精确到字符；在 `name nvarchar` 列里写入 `张三` 时，期望出现 `N'张三'`；`xml` 列不出现在 WHERE 里。
- [ ] **Step 2–4: 运行**，先 FAIL，实现后 PASS（`go test -race ./internal/edit`）。
- [ ] **Step 5: 提交** `feat(edit): SQL Server 无键表按全列匹配 TOP (1)`

### Task 7: `internal/db/sqlserver` 驱动

**Files:**
- Create: `internal/db/sqlserver/sqlserver.go`（Driver、Open、DSN、conn、Query/Exec/Stream、值转换）
- Create: `internal/db/sqlserver/session.go`
- Create: `internal/db/sqlserver/meta.go`、`catalog.go`、`schema.go`（`TableSchema` 用 Columns/Indexes/ForeignKeys 加表注释组合，不保证规范化）、`ddl.go`（从 `sys.*` 拼出 DDL）
- Create: `internal/db/sqlserver/ddlwriter.go`、`errpos.go`、`normalize.go`（`NormalizeType` 只做小写化和去空白）
- Create: `internal/db/sqlserver/sqlserver_test.go`（单元测试）、`integration_test.go`（`//go:build integration`）
- Modify: `go.mod`

**Interfaces:**
- Consumes: Task 3 的 `Fixture`、Task 4 的方言、Task 6
- Produces: `sqlserver.Driver{}`，`Name() == "sqlserver"`，`DatabaseScoped() == true`
- `dsn(cfg db.ConnConfig) string`：`sqlserver://user:pass@host:port?database=<db>&dial+timeout=10&app+name=datavim` 再加 `cfg.Params` 原样透传（`encrypt`、`TrustServerCertificate`、`tlsmin` 等都通过这里）。`cfg.Database` 为空时用 `master`。DSN 不能出现在日志或错误信息里。
- `Dial`：用 `mssql.NewConnector(dsn)`，把 `connector.Dialer` 设成包装了 `cfg.Dial` 的值。字段名和接口以 `go doc github.com/microsoft/go-mssqldb Connector` 为准。
- `ReadOnly`：服务端不做任何处理（客户端守卫见 Task 5）。在 Open 的注释里说明原因。
- `Paginate(sql, limit, offset)`：如果 sql 在深度 0 处没有 ORDER BY，就补上 ` ORDER BY (SELECT NULL)`；然后加 ` OFFSET n ROWS FETCH NEXT m ROWS ONLY`。
- 值：所有类型都转成文本；`uniqueidentifier` 转成标准 GUID 字符串（注意 go-mssqldb 返回的 16 字节是混合字节序，用 `mssql.UniqueIdentifier` 转换）；`datetime2` → `2006-01-02 15:04:05.9999999`；`datetimeoffset` 带 `-07:00`；`date` → `2006-01-02`；`time` → `15:04:05.9999999`；`decimal` / `money` 直接用驱动返回的文本，不经过 float；binary → `0x` 加大写十六进制。
- `Exec` 的行数：返回 `RowsAffected`，契约要求的是匹配行数，SQL Server 默认就是这样。会话里用户执行 `SET NOCOUNT ON` 后行数会变成 0，`edit.Apply` 会把它当成「没影响任何行」报错并回滚。这是安全的方向，不需要特殊处理，在注释里写明。
- Session：用 `*sql.Conn`；取消时依赖驱动发 attention，契约「Session keeps state, survives cancel」必须通过。
- 元数据：`Databases` 用 `sys.databases WHERE HAS_DBACCESS(name) = 1`，`master/model/msdb/tempdb` 排在最后；`Schemas` 排除 `sys`、`INFORMATION_SCHEMA`、`guest` 和 `db_*`；`Catalog` 只读当前库，表、列、例程各用一条查询，不能每张表单独查。例程 DDL 用 `OBJECT_DEFINITION(OBJECT_ID(…))`。
- `errpos`：取 `mssql.Error` 的 `LineNo`，换算成语句内的行首偏移。
- DDLWriter：`DropTable` → `DROP TABLE|VIEW [s].[t]`；`RenameTable` → `EXEC sp_rename N's.t', N'new'`；`TruncateTable` → `TRUNCATE TABLE [s].[t]`；`CreateTable` / `AlterTable` 把所有内容放进 manual 返回。

- [ ] **Step 1: 写失败的单元测试** `TestDSN`（表驱动测试：密码含 `@:/` 时要正确转义；`Params{"encrypt":"disable","tlsmin":"1.0"}` 要出现在 query 里；库名为空时是 master）、`TestPaginateSQLServer`（没有 ORDER BY 时补 `ORDER BY (SELECT NULL)`；已有 ORDER BY 时不补；ORDER BY 出现在子查询里时仍然要补）、`TestFormatGUID`。
- [ ] **Step 2: 运行** `go test ./internal/db/sqlserver`，预期 FAIL。
- [ ] **Step 3: 实现驱动**（用 Context7 / `go doc` 核对 go-mssqldb 的 API）。
- [ ] **Step 4: 运行单元测试**，预期 PASS。
- [ ] **Step 5: 写集成测试**：用 `modules/mssql` 启动 `mcr.microsoft.com/mssql/server:2022-latest`（`ACCEPT_EULA=Y`），按 MySQL / PG 的 integration_test 建 fixture（schema `dbo`、库 `datavim_test`），调用 `dbtest.RunContract`。
- [ ] **Step 6: 运行** `go test -race -tags integration ./internal/db/sqlserver`，预期 PASS（需要 OrbStack；镜像是 amd64，走 Rosetta）。
- [ ] **Step 7: 提交** `feat(db): SQL Server 驱动`

### Task 8: 把 SQL Server 接进应用，补文档

**Files:**
- Modify: `cmd/datavim/main.go:171`（注册 `sqlserver.Driver{}`）
- Modify: `internal/ui/overlay/connform.go`（Driver 选项加上 `sqlserver`；新增 `Encrypt` 字段，选项是 `default`、`true`、`false`、`disable`，以及 `Trust cert`（`no`/`yes`），这两个字段只在驱动为 sqlserver 时显示；保存时写进 `Params["encrypt"]` 和 `Params["TrustServerCertificate"]`，`default` 和 `no` 不写）
- Modify: `internal/ui/panes/tree/tree.go:462`（图标：Nerd 用 `` 或通用数据库图标，具体选哪个由实现者决定；plain 图标沿用现有规则）
- Modify: `internal/ui/highlight/highlight.go:34`（`sqlx.SQLServer: lexer("tsql")`）
- Modify: `internal/export/export.go:47` 的注释
- Modify: `.github/workflows/ci.yml`（integration job 不用改，命令已经覆盖 `./internal/db/...`；如果单个 job 超时，就拆出 `integration-sqlserver`）
- Modify: `CHANGELOG.md`、`README.md`、`README.zh-CN.md`（支持的数据库、连接示例、加密参数、「SQL Server/Oracle 只读只在客户端生效，生产库建议再配一个只读账号」）
- Test: `internal/ui/overlay/connform_test.go`、`internal/ui/highlight/highlight_test.go`

- [ ] **Step 1: 写失败的测试** `TestConnFormSQLServerEncrypt`（选 sqlserver，再选 Encrypt=disable 和 Trust cert=yes，保存后得到 `Params{"encrypt":"disable","TrustServerCertificate":"true"}`；选 mysql 时这两个字段不显示）、`TestHighlightTSQL`（`[dbo].[t]` 被高亮成标识符）。
- [ ] **Step 2–4: 运行** `go test -race ./internal/ui/...`，先 FAIL，实现后 PASS。
- [ ] **Step 5: 运行** `make lint`、`make test`，预期全部通过。
- [ ] **Step 6: 提交** `feat(ui): 支持 SQL Server 连接`
- [ ] **Step 7: 真机验证（需要作者在场）**：`make build`，在 `connections.toml` 里配上 `driver = "sqlserver"`、`host = "192.168.100.57"`、`database = "crm_MSCRM"`、`env = "prod"`。依次确认：能连上（不行的话依次试 `encrypt=disable`、`TrustServerCertificate=true`、`tlsmin=1.0`，并把需要的组合写进 README）；树能展开到 `dbo` 下的表；查询、过滤、排序、结构、DDL 都正常；导出 CSV 正常；`DELETE FROM x` 和 `EXEC 任意过程` 被拦截。把结论记进 CHANGELOG 的说明里。

## Phase C：Oracle

### Task 9: Oracle 方言（`sqlx`）

**Files:**
- Modify: `internal/sqlx/lex.go`、`literal.go`、`split.go`、`tx.go`、`keywords.go`、`next.go`、`analyze.go`（`SafeOther` 白名单）
- Test: 对应的 `_test.go`

**Interfaces:**
- Produces（Oracle 下各函数的行为）：
  - `QuoteIdent(Oracle, "a\"b")` → `"a""b"`；`QualifiedName(Oracle, {"", HR, EMP})` → `"HR"."EMP"`（忽略 Database）
  - `QuoteIfNeeded(name, Oracle)`：全大写的合法标识符、且不是保留字时不加引号，否则加双引号（`Emp` → `"Emp"`）
  - `Literal(Oracle, …)`：`'…'`，`'` 写成 `''`，不转义反斜杠；RAW 类型 → `HEXTORAW('…')`
  - 词法：`q'[…]'`、`q'{…}'`、`q'(…)'`、`q'<…>'` 以及 `q'!…!'` 这类任意分隔符；`--`；不嵌套的 `/* */`
  - `Split`：普通语句按 `;` 切，`Statement.Text` 去掉末尾的 `;`。语句以 `BEGIN`、`DECLARE`、`CREATE [OR REPLACE] [EDITIONABLE|NONEDITIONABLE] (PROCEDURE|FUNCTION|PACKAGE [BODY]|TRIGGER|TYPE [BODY])` 开头时进入块模式，一直到单独一行的 `/` 为止，Text 保留 `END;`，不包括 `/`。如果到末尾都没有 `/`，剩下的整体算一个块。
  - `TransactionEffect(Oracle)`：`SET TRANSACTION …` → TxBegin；`COMMIT [WORK]` → TxEnd；`ROLLBACK [WORK]` → TxEnd；`ROLLBACK TO [SAVEPOINT] x` → TxResume；`BEGIN` 是 PL/SQL 块 → TxNone
  - `BeginSQL(Oracle)` → `SET TRANSACTION READ WRITE`
  - `SafeOther(Oracle)`：`ALTER SESSION SET CURRENT_SCHEMA|NLS_*|TIME_ZONE`、`SET TRANSACTION READ ONLY`、`EXPLAIN PLAN`（这是 Read）；匿名块、`CALL`、`EXEC` 都不放行
  - `AnalyzeBatch(Oracle)` 能扫描 PL/SQL 块内部的 DML/DDL（包括 `EXECUTE IMMEDIATE`，一律算 DDL）
  - 关键字：`FETCH FIRST`、`ROWNUM`、`ROWID`、`NVL`、`SYSDATE`、`DUAL`、`CONNECT BY`……；下一个关键字建议里不出现 `LIMIT`

- [ ] **Step 1: 写失败的表驱动测试**：`TestSplitOracle`（`"select 1 from dual;\nbegin\n  null;\nend;\n/\nselect 2 from dual;"` 切出 3 条，第 2 条是 `begin\n  null;\nend;`，第 1、3 条没有末尾 `;`；`select ';' from dual` 是 1 条；`q'[a;b]'` 不会被切开）、`TestQuoteIfNeededOracle`、`TestTransactionEffectOracle`、`TestGuardOracleReadOnly`（Allow：`ALTER SESSION SET CURRENT_SCHEMA = HR`、`select * from dual`；Reject：`begin delete from t where id=1; end;`、`call p()`、`begin execute immediate 'drop table t'; end;`）。
- [ ] **Step 2–4: 运行** `go test -race ./internal/sqlx ./internal/runner`，先 FAIL，实现后 PASS。
- [ ] **Step 5: 提交** `feat(sqlx): Oracle 方言与 PL/SQL 块切分`

### Task 10: Oracle 的 ROWID 行定位

**Files:**
- Modify: `internal/ui/tablequery.go:28`（`rowID` 的查询列）
- Modify: `internal/edit/plan.go:88`（`where` 的 RowID 分支）、`insert` 的空 INSERT
- Modify: `internal/ui/tabs.go:82`、`edit/table.go:25`、`edit/key.go:16` 的注释（不再只写 ctid）
- Test: `internal/edit/plan_test.go`、`internal/ui/tablequery_test.go`

**Interfaces:**
- Produces：
  - `tableQuery.sql`：PG 是 `SELECT ctid::text, * FROM`；Oracle 是 `SELECT ROWIDTOCHAR(t.ROWID), t.* FROM "HR"."EMP" t`（Oracle 不支持 `ROWID, *`，必须给表起别名）
  - `where`：PG 是 `ctid = '…'`；Oracle 是 `ROWID = CHARTOROWID('…')`
  - Oracle 的空 INSERT：`INSERT INTO t (<第一列>) VALUES (DEFAULT)`
  - 警告文字：`… rows are matched by their ROWID`

- [ ] **Step 1: 写失败的测试** `TestTableQueryOracleRowID`、`TestPlanOracleRowID`（期望的文本精确到字符）。
- [ ] **Step 2–4: 运行** `go test -race ./internal/edit ./internal/ui`，先 FAIL，实现后 PASS。
- [ ] **Step 5: 提交** `feat(edit): Oracle 无键表按 ROWID 定位`

### Task 11: `internal/db/oracle` 驱动

**Files:**
- Create: `internal/db/oracle/oracle.go`、`session.go`、`meta.go`、`catalog.go`、`schema.go`、`ddl.go`、`ddlwriter.go`、`errpos.go`、`normalize.go`
- Create: `internal/db/oracle/oracle_test.go`、`integration_test.go`
- Modify: `go.mod`

**Interfaces:**
- Consumes: Task 3、Task 9、Task 10
- Produces: `oracle.Driver{}`，`Name() == "oracle"`，`DatabaseScoped() == true`
- 连接：`go_ora.BuildUrl(host, port, service, user, password, params)`，service 为空时用 `FREEPDB1`；用 `go_ora.NewConnector(url)`，`cfg.Dial != nil` 时调用 `connector.Dialer(…)`，再 `sql.OpenDB(connector)`。
- `Databases()` 返回 nil；树的 KindConn 节点在 `!Features().Databases` 时直接列出 `Schemas(ctx, "")` 的结果（改 `ui/loaders.go:44` 的 `fetchChildren`，测试放在本任务）。`Schemas` 用 `SELECT username FROM all_users WHERE oracle_maintained = 'N' ORDER BY CASE WHEN username = USER THEN 0 ELSE 1 END, username`。
- Session 的事务：Session 保存一个 `tx *sql.Tx`。如果 `sqlx.TransactionEffect(stmt, Oracle) == TxBegin` 且没有 tx，就 `c.BeginTx(ctx, nil)` 并在 tx 上执行该语句；有 tx 时所有调用都走 tx；`TxEnd` 调用 `tx.Commit()` / `tx.Rollback()`（看语句是 COMMIT 还是 ROLLBACK），然后把 tx 置空；`TxResume` 原样在 tx 上执行。这样契约「Session keeps a transaction open across calls」和 `edit.Apply` 用的 `BeginSQL` 路径都成立。
- `Paginate`：`<sql> OFFSET n ROWS FETCH NEXT m ROWS ONLY`。
- 值：`DATE` → `2006-01-02 15:04:05`；`TIMESTAMP` → 加 `.999999999`；`TIMESTAMP WITH TIME ZONE` 加 `-07:00`；`NUMBER` 用文本；`RAW` 用十六进制；`CLOB` 用全文；`BLOB` 用十六进制，超过现有截断上限时按现有规则处理。
- `Query` / `Exec` 前去掉末尾的 `;`，但以 `END;` 结尾的 PL/SQL 块保留（复用 `sqlx` 的判断，不要重写一套）。
- 元数据：`ALL_TABLES` / `ALL_VIEWS` / `ALL_TAB_COLUMNS` / `ALL_INDEXES` / `ALL_IND_COLUMNS` / `ALL_CONSTRAINTS` / `ALL_CONS_COLUMNS` / `ALL_PROCEDURES`；`DDL` 用 `DBMS_METADATA.GET_DDL`，执行前先 `DBMS_METADATA.SET_TRANSFORM_PARAM(…, 'STORAGE', false)` 和 `SEGMENT_ATTRIBUTES` 设为 false；`Catalog` 只查 `oracle_maintained = 'N'` 的 owner。
- `errpos`：go-ora 的错误如果带位置就使用，否则不标位置。
- DDLWriter：`DROP TABLE "S"."T"` / `DROP VIEW`；`ALTER TABLE "S"."T" RENAME TO "N"`（视图用 `RENAME`，只在当前 schema 下可用，否则放进 manual）；`TRUNCATE TABLE`。

- [ ] **Step 1: 写失败的单元测试** `TestURL`（密码含 `@` 时会被转义；service 为空时用 FREEPDB1）、`TestTrimSemicolon`（`select 1 from dual;` 去掉分号，`begin null; end;` 保留）；在 `internal/ui` 里写 `TestFetchChildrenNoDatabases`（fake meta 的 `Databases()` 返回 nil，Oracle 连接节点展开后直接得到 schema 节点，`NS{Schema: "HR"}`）。
- [ ] **Step 2–4: 运行**，先 FAIL，实现后 PASS。
- [ ] **Step 5: 写集成测试**：用 GenericContainer 启动 `gvenzl/oracle-free:23-slim`，环境变量 `ORACLE_PASSWORD`、`APP_USER=datavim`、`APP_USER_PASSWORD`，等待日志 `DATABASE IS READY TO USE!`，超时 5 分钟。fixture 建在 `DATAVIM` schema 下，要包含一张 `"MixedCase"` 表；调用 `dbtest.RunContract`。另加一个只针对 Oracle 的子测试：Session 执行 `SET TRANSACTION READ WRITE` 和 `INSERT` 之后，pooled conn 查不到这行；Session 执行 `ROLLBACK` 之后，这行也不存在。
- [ ] **Step 6: 运行** `go test -race -tags integration ./internal/db/oracle`，预期 PASS。
- [ ] **Step 7: 提交** `feat(db): Oracle 驱动`

### Task 12: 把 Oracle 接进应用，补文档

**Files:**
- Modify: `cmd/datavim/main.go`（注册 `oracle.Driver{}`）
- Modify: `internal/ui/overlay/connform.go`（Driver 选项加上 `oracle`；驱动为 oracle 时，`Database` 字段的标签显示为 `Service`，占位文字是 `FREEPDB1`）
- Modify: `internal/ui/panes/tree/tree.go`（图标）、`internal/ui/highlight/highlight.go`（`sqlx.Oracle: lexer("plsql")`）
- Modify: `CHANGELOG.md`、`README.md`、`README.zh-CN.md`
- Test: `connform_test.go`、`highlight_test.go`

- [ ] **Step 1: 写失败的测试** `TestConnFormOracleServiceLabel`、`TestHighlightPLSQL`。
- [ ] **Step 2–4: 运行**，先 FAIL，实现后 PASS。
- [ ] **Step 5: 运行** `make lint && make test && make test-integration`，预期全部通过。
- [ ] **Step 6: 用 Docker 里的 Oracle 手工验证**：`./bin/datavim --config <dir> --debug` 连上 `gvenzl/oracle-free`，依次确认：树显示 schema → 表；查询正常；暂存修改一个无键表并提交；PL/SQL 块能执行；手动事务模式下的 COMMIT / ROLLBACK 正常；只读连接下匿名块被拦截。
- [ ] **Step 7: 提交** `feat(ui): 支持 Oracle 连接`
