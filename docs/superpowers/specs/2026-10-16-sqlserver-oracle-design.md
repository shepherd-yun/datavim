# SQL Server 与 Oracle 支持 设计文档

- 日期：2026-10-16
- 状态：待审阅
- 范围：新增 `sqlserver`、`oracle` 两个驱动，并把 UI 和纯包里写死的「mysql / postgres 二选一」改成按驱动能力判断；修订主设计 §4（驱动层）、§5.3（安全规则）

## 1. 背景

sqlvim 目前只支持 MySQL 和 PostgreSQL。作者日常要连一台公司内网的 SQL Server：版本 12.0（SQL Server 2014），用户名密码登录，端口 1433，库名 `crm_MSCRM`（Dynamics CRM），属于生产环境。在这台库上主要是查数据、看表结构，偶尔改数据。现在只能用 DataGrip 做这些事。

作者还没有用过 Oracle。加 Oracle 是为了让主流商业库都能连。它和 SQL Server 要做的改造基本是同一套（见 §4），所以两个一起设计，按 SQL Server、Oracle 的顺序实现。

## 2. 目标

- 能连 SQL Server 2014–2022 和 Oracle 12c 及以上（测试用 Oracle Database Free 23ai），登录方式是用户名密码，可以走 SSH 隧道。
- 下面这些功能在两个新引擎上和现有引擎一样可用：连接表单、树（库 / schema / 表 / 视图 / 例程）、控制台（多语句、事务、取消、结果过滤）、数据表格（分页、过滤、排序）、结构视图（列、索引、外键、DDL、例程 DDL）、暂存修改数据（cell / 行增删改）、导出、补全和语法高亮。
- 树的 `a` 菜单里的重命名、清空、删除表，也就是 `DDLWriter` 的 `RenameTable`、`TruncateTable`、`DropTable`。（这是我的决定：这三条语句很简单，菜单里已经有入口。）
- 安全规则（只读、prod 确认、UPDATE/DELETE 缺 WHERE、暂存修改必须正好影响一行、事务中不静默重连）对新引擎同样有效。新引擎在服务端没有可靠的只读开关，所以客户端判断要更保守，见 §6。
- 现有 MySQL / PostgreSQL 的行为不变。

**第一版不做：**
- 修改表结构（Modify Table）、schema 对比、数据对比、导入，以及 `DDLWriter.CreateTable` / `AlterTable` / `TableSchema`。在新引擎上触发这些功能时，提示 `not supported for <driver> yet` 并直接返回，不能报错，也不能做一半。
- Windows / AD / Kerberos 认证、Azure AD、Oracle wallet、TNS 名、LDAP。
- SQL Server 命名实例（`host\instance`，依赖 SQL Browser）。需要的话可以直接填端口，或者通过 params 传给驱动。
- Oracle 的 SID 连接方式（只支持 service name）、RAC、多租户 CDB 根容器管理。

**反目标**（技术上能用，但这样做就算失败）：
- 对只读或 prod 的 SQL Server 连接，`EXEC 某存储过程` 或一段 T-SQL / PL/SQL 块能绕过只读拦截，执行了写操作。
- 中文数据写进 SQL Server 后变成 `???`，原因是字符串字面量没有加 `N` 前缀。
- 需要 cgo 或 Oracle Instant Client 才能构建或运行，导致 GoReleaser 交叉编译和单文件分发失效。

## 3. 驱动与依赖

| 引擎 | 驱动名（connections.toml `driver`） | Go 库 | 默认端口 |
|---|---|---|---|
| SQL Server | `sqlserver` | `github.com/microsoft/go-mssqldb` | 1433 |
| Oracle | `oracle` | `github.com/sijms/go-ora/v2` | 1521 |

两个库都是纯 Go 实现。实现时用 Context7 查当前文档，确认 DSN 参数、自定义 Dialer、取消和类型映射的具体写法。

- **SSH 隧道**：`ConnConfig.Dial` 必须用来建立所有网络连接。go-mssqldb 可以通过 `NewConnector` 加 `Dialer` 注入。go-ora 是否支持自定义 dialer 需要先确认；如果不支持，Oracle 连接配了 `ssh` 时，打开连接直接报明确错误 `ssh tunnels are not supported for oracle yet`，不能退回到直连。
- **SQL Server 加密**：SQL Server 2014 很可能不支持驱动默认的 TLS 设置。连接表单给 sqlserver 增加一个 `Encrypt` 选项（`true` / `false` / `disable`，加上 `trust server certificate` 开关），保存在 `params` 里，原样传给驱动的 DSN。如果服务器只支持 TLS 1.0/1.1，就需要放开最低 TLS 版本（具体参数以驱动文档为准）。这一项必须用作者那台 2014 实测通过才算完成。
- **Oracle 的 service name**：沿用现有的 `Database` 字段，表单里驱动为 oracle 时把标签显示成 `Service`，默认值 `FREEPDB1`。

## 4. 把引擎二选一改成按能力判断

现在很多地方写的是 `cfg.Driver == "postgres"` / `!= "postgres"`，或者 `sqlx.Dialect` 只有 `MySQL` 和 `Postgres` 两个值。这类判断里「不是 postgres 就当 MySQL」的写法，加了新引擎之后会悄悄出错。目前已知的位置有：

- `internal/sqlx`：`DialectOf`、`lex`（引号、注释、字符串转义）、`QuoteIdent`、`QualifiedName`、`Literal`、`BeginSQL`、`TransactionEffect`、关键字表和补全、`Filtered`（MySQL 派生表 ORDER BY 的处理）
- `internal/edit`：`ChooseKey`（ctid / 全列）、`plan.go` 的 `ctid =` 和 ` LIMIT 1`、警告文字
- `internal/ui`：`tablequery.go`（`ctid::text`）、`completion.go`、`metadata.go`、`nodemenu.go`、`importrun.go`、`compare.go`、`transfer.go`、`console.go` 的 `scopeSQL`、`panes/tree` 的图标、`overlay/connform.go` 的驱动选项、`highlight`
- `internal/importer`、`internal/config` 的 `defaultPorts`

要求：

- `sqlx.Dialect` 增加 `SQLServer` 和 `Oracle`。所有对 dialect 的 `switch` 必须把四种情况都写全，不能用 default 兜底成 MySQL。如果仓库的 lint 能检查 switch 是否穷尽，就打开它。
- UI 里所有判断引擎的地方，改成询问驱动或 dialect 的能力，不再比较驱动名字符串。比如：有没有 schema 这一层、是否 `DatabaseScoped`、没有键时怎么定位行、怎么切换控制台的默认命名空间、哪些高级功能可用。能力具体放在 `db.Driver`、`db.Dialect` 还是 `sqlx` 里，由实现者决定，但依赖方向必须保持向下（见 CLAUDE.md）。
- 第一版不支持的功能（§2）也通过同一种能力判断来关闭，不在 UI 里单独写 `if driver == "oracle"`。

## 5. 各引擎的语义

### 5.1 命名空间和树

| | SQL Server | Oracle |
|---|---|---|
| `Namespace` | `{Database, Schema}`，比如 `{crm_MSCRM, dbo}` | `{Database: "", Schema: 用户名}` |
| `DatabaseScoped()` | **true**（我的决定：`sys.*` 目录视图只看当前库，一个库一个 `Conn` 和 PG 一致，元数据查询最简单） | true（一个 service 就是一个库） |
| 树 | 连接 → 库 → schema → 表 / 视图 / 例程 | 连接 → schema → 表 / 视图 / 例程，没有库这一层 |
| 隐藏系统对象 | 系统库 `master/model/msdb/tempdb` 放到最后，或者折叠（实现者决定）；隐藏 `sys`、`INFORMATION_SCHEMA` 和 `db_*` 角色 schema | 只列出 `ORACLE_MAINTAINED = 'N'` 的用户，当前用户排第一 |
| 控制台切换默认命名空间 | `USE [db]`（SQL Server 不能按会话切换默认 schema） | `ALTER SESSION SET CURRENT_SCHEMA = X` |
| 限定名 | `[db].[schema].[table]`；同库时可以只写 `[schema].[table]` | `"SCHEMA"."TABLE"` |

Oracle 会把没加引号的标识符转成大写。树和结构视图按目录里的原样显示（通常是大写）。补全插入名字时，如果名字全是大写、并且是合法标识符，就不加引号，否则加双引号。

### 5.2 SQL 文本（`sqlx`）

**SQL Server（T-SQL）**
- 标识符用 `[name]`，`]` 写成 `]]`。词法上也要认识 `"name"`。
- 字符串字面量统一写成 `N'…'`，`'` 写成 `''`，不转义反斜杠。二进制列用 `0x…` 原样写出。
- 注释：`--`、`/* */`（可以嵌套）。
- 语句切分：按 `;` 切，另外单独一行的 `GO` 也是批次分隔符。T-SQL 的分号可以省略，所以一个片段里可能有多条没有分号的语句。这会影响安全判断，见 §6。
- 事务：`BEGIN TRAN[SACTION]` 开始事务，`COMMIT` / `ROLLBACK [TRAN]` 结束，`SAVE TRAN x` / `ROLLBACK TRAN x` 对应保存点。只写一个 `BEGIN`（后面不跟 `TRAN`）是语句块，不算事务开始。`BeginSQL` 返回 `BEGIN TRANSACTION`。
- 分页：`ORDER BY … OFFSET n ROWS FETCH NEXT m ROWS ONLY`。T-SQL 要求有 ORDER BY，没有的话补上 `ORDER BY (SELECT NULL)`。

**Oracle**
- 标识符用 `"NAME"`。字符串用 `'…'`，另外要认识 `q'[…]'` 这种替代引号写法。
- 语句切分：按 `;` 切。PL/SQL 块（`BEGIN`、`DECLARE`、`CREATE [OR REPLACE] PROCEDURE/FUNCTION/PACKAGE/TRIGGER/TYPE`）内部有分号，要像 SQL*Plus 一样以单独一行的 `/` 结束，块结尾的 `END;` 保留。普通 SQL 语句发给服务器前要去掉末尾分号，否则会报 ORA-00911。
- 事务：Oracle 没有 BEGIN 语句，第一条 DML 会隐式开启事务。见 §5.4。
- 分页：`OFFSET n ROWS FETCH NEXT m ROWS ONLY`，12c 及以上支持。
- 空字符串就是 NULL。

### 5.3 暂存修改时怎么定位行

顺序和现在一样：主键 → NOT NULL 的唯一索引 → 引擎的兜底方式。

- **Oracle 兜底**：用 `ROWID`，和 PG 的 ctid 走同一条路径（数据表格查询时多查一列 `ROWIDTOCHAR(ROWID)`，`WHERE ROWID = CHARTOROWID('…')`）。没有键的视图不能修改。
- **SQL Server 兜底**：用全部可比较的列匹配，加 `UPDATE TOP (1)` / `DELETE TOP (1)`，和 MySQL 的 `LIMIT 1` 相当。`text/ntext/image/xml/geography/geometry/sql_variant` 等不能比较的列跳过，具体清单由实现者整理。警告文字里写 `TOP (1)`。
- 两个引擎都必须保留「每条语句正好影响一行，否则整体回滚」的检查。SQL Server 返回的行数必须是匹配的行数，而不是值真正变化的行数（契约里已经有这条测试）。如果连接上开了 `SET NOCOUNT ON`，行数就不可信，驱动要保证 `Exec` 拿到的是真实行数。

### 5.4 会话、事务和取消

- 控制台的 `Session` 是一条专用连接。`USE`、`ALTER SESSION`、临时表（`#t`）在这条连接上一直有效。
- **Oracle 的事务**（我的决定）：控制台默认自动提交，和其他引擎一样。在手动事务模式下，或者执行 `SET TRANSACTION …` 之后，Session 进入事务，之后的语句不再自动提交，直到 `COMMIT` / `ROLLBACK`。Oracle 没有 BEGIN，所以 `BeginSQL` 在 Oracle 上不是一条普通语句，实现者需要让 Session 自己知道「现在处于事务中」，例如改用驱动的 Tx，或者关闭驱动的自动提交。go-ora 在事务之外执行的语句会自动提交，这一点必须用契约测试覆盖（「Session keeps a transaction open across calls」）。
- 事务中连接断开时，照常返回 `ErrConnLost`，不能静默重连。
- 取消：取消 context 时，服务器上的语句要停下来，会话还能继续用。go-mssqldb 发送 attention 包，go-ora 发 break。契约里已有的测试必须通过。
- 错误位置（`errpos`）：SQL Server 的错误带行号，定位到控制台里那一行即可。Oracle 的错误如果带偏移就用，不带就不标位置。

### 5.5 值的显示

- 所有值都转成文本显示，NULL 和空串照常区分。Oracle 的空串本身就是 NULL，所以契约里「NULL 和空串不同」这条测试要按引擎跳过或改写。
- 日期时间：SQL Server `datetime/datetime2/datetimeoffset`，Oracle `DATE`（包含时间）和 `TIMESTAMP [WITH TIME ZONE]`。格式和现有引擎保持一致，并且能原样写回（暂存修改时用文本字面量回写，必须能往返）。
- `uniqueidentifier` 显示成标准 GUID 字符串，注意 go-mssqldb 的字节序。`money`、`decimal`、`NUMBER` 不经过浮点数，直接用文本。

### 5.6 元数据

- `DDL`：SQL Server 没有 `SHOW CREATE TABLE`，要根据 `sys.*` 拼出 CREATE TABLE 加索引（参考 PG 的 `ddl.go`），视图和例程用 `OBJECT_DEFINITION`。Oracle 用 `DBMS_METADATA.GET_DDL`，并去掉 storage 子句。
- `Catalog`：只读当前 `Conn` 所在的库（SQL Server）或 service（Oracle）里用户可见的对象，供搜索和补全使用。CRM 库有几千张表，Catalog 查询必须是少量几条批量查询，不能每张表查一次。
- `TableSchema` 第一版返回一个 sentinel 错误（不支持），调用方据此显示 §2 的提示。

## 6. 安全规则（修订主设计 §5.3）

MySQL 和 PG 的只读有两层：服务端的会话只读开关，加上客户端 `runner.Guard`。SQL Server 没有按会话设置的只读开关（`ApplicationIntent=ReadOnly` 只对可用性组有意义）。Oracle 的 `SET TRANSACTION READ ONLY` 只对一个事务有效，连接池里的查询用不上。所以新引擎**只有客户端这一层**，这一层必须更保守：

- 对 `sqlserver` / `oracle` 的只读连接，`Analyze` 结果为 `Other`（认不出来）的语句一律当成写操作拦截。只放行一个白名单：`USE`、`SET` 会话选项（`SET NOCOUNT`、`SET STATISTICS …`、`ALTER SESSION SET CURRENT_SCHEMA / NLS_*`）、`DECLARE` 变量声明（SQL Server），以及常见的只读系统过程（`sp_help`、`sp_helptext`、`sp_columns`、`sp_who`……）。`EXEC` 其他过程、匿名 PL/SQL 块、`CALL` 都要拦截。
- 因为 T-SQL 可以不写分号，一条「语句」里可能有多条语句。只读判断和 prod 确认必须扫描整个片段：任何位置出现 DML / DDL 关键字（不在字符串和注释里）就算写操作。同一片段里的 `UPDATE` / `DELETE`，每一条都要检查缺 WHERE。
- 补全 `UnlocksReadOnly` 在新引擎下的含义：没有服务端开关可以「解锁」，所以这个函数不需要新增规则。
- 契约测试「ReadOnly connections reject writes on the server」对这两个引擎跳过，改成在 `runner` 包里加针对这两个 dialect 的表驱动测试，覆盖上面的放行和拦截情况。
- 文档（README 两种语言）要说明：SQL Server / Oracle 的只读只在客户端生效，生产库建议再配一个数据库层面只读的账号。

作者已确认采用这套严格规则：宁可误拦（比如执行只读的自定义存储过程也要先 `:set noreadonly`），也不放过写操作。

## 7. 测试

- 契约：两个驱动都跑 `dbtest.RunContract`。`Fixture` 增加 `Driver` 的新取值，并加入一组能力标记，用来跳过第一版不做的项目（TableSchema、schema sync、Modify Table、data sync、ApplyDDL、服务端只读）和引擎差异项（Oracle 空串）。原来写死 `LIMIT`、`ctid` 的测试按 dialect 分支。
- 集成测试容器：SQL Server 用 testcontainers 的 `modules/mssql`（`mcr.microsoft.com/mssql/server:2022-latest`，只有 amd64 镜像，在 Apple Silicon 上依赖 OrbStack / Rosetta）。Oracle 用 `gvenzl/oracle-free:23-slim`（generic container，等待日志就绪，启动比较慢，超时要放宽）。CI 的集成任务里加上这两个，必要时拆成单独的 job。
- 纯包测试：`sqlx` 的词法、切分（`GO`、`/`、`q'[...]'`、`N'...'`）、`Analyze`、`TransactionEffect`、`Literal`、补全，`edit` 的 `TOP (1)` / `ROWID` 计划，`runner` 的 §6 规则，全部用表驱动测试。
- 手工验证：连作者那台 SQL Server 2014，用只读 + prod 配置，验证连接（加密选项）、浏览 `crm_MSCRM`、查询、结构、导出能正常使用，以及写操作被拦截。

## 8. 交付顺序

1. 能力改造（§4）：MySQL / PG 行为不变，现有测试全部通过。
2. `sqlserver` 驱动、sqlx 方言、安全规则（§6）、连接表单、文档，连作者那台 2014 实测。
3. `oracle` 驱动和方言。

每一步都要在 `CHANGELOG.md` 的 `[Unreleased]` 下加一行，README 中英文同步更新。

## 9. 留给实现者决定的

- 能力接口具体长什么样，放在哪一层。
- 系统库和系统 schema 在树里的具体呈现方式。
- 每个引擎不可比较的列类型清单、补全关键字表、chroma 词法器（`tsql`、`plsql`）的接法。
- Oracle Session 实现事务的具体方式（§5.4）。
- 例程（存储过程、函数、包）在树里的分组方式。
