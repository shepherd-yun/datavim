# SQL 连接的服务器状态页 设计文档

- 日期：2026-10-19
- 状态：待审阅
- 范围：给 MySQL、PostgreSQL、SQL Server、Oracle 连接加一个和 Redis “Server status” 同类的标签页；在 `db` 层新增读取服务器状态的接口，四个驱动都实现

## 1. 背景

Redis 连接已经有服务器状态页（`internal/ui/servertab.go`），分 Status、Slow log、Clients 三部分，Status 每隔一段时间采样一次并画走势图。SQL 连接还没有对应的页面。现在想确认连接数、看谁卡住了库、查某个参数的当前值，只能自己在 console 里写系统视图查询，而且四个引擎的写法各不相同。

作者希望这个页面**同时满足运维和开发**：运维要看负载、连接、缓存命中、复制延迟这类常规指标；开发排查问题时，要能看到谁在跑什么 SQL、谁在等锁，以及确认某个参数或状态。作者要求四个引擎一次全部做完，不分阶段。

作者日常连的 SQL Server 是 2014 版（12.0）的生产库，所以 SQL Server 2014 上必须可用，见 §6.3。

## 2. 目标、非目标、反目标

**目标**

- MySQL、PostgreSQL、SQL Server、Oracle 连接都能打开服务器状态页，包含六个部分：Status、Sessions、Locks、Slow queries、Variables、Replication。
- 每个部分的内容都来自 `db` 层的驱动，UI 不写方言 SQL，也不按驱动名分支（只有 Redis / SQL 的区分）。
- 在 Sessions 部分可以取消某个会话正在执行的查询，或者断开这个会话。安全规则和 Redis 的 `CLIENT KILL` 一致，见 §5。
- 权限不够、扩展没装、版本太老时，只影响对应的部分：显示一行原因，其他部分照常可用。
- 在 80 列终端里能正常显示，更窄时也不会乱。

**非目标**

- 修改参数（`SET GLOBAL`、`ALTER SYSTEM`、`sp_configure`），Variables 部分只读。
- 历史趋势和持久化。走势图只保留这个标签页打开期间采样到的数据，和 Redis 一样。
- 告警、阈值着色、自动刷新 Sessions 等表格部分（表格部分按 Refresh 手动刷新，和 Redis 的 Slow log / Clients 一样）。
- EXPLAIN、执行计划、等待事件的细分分析。
- 集群管理：MySQL Group Replication / InnoDB Cluster 的管理操作、Oracle RAC 的多实例视图、Always On 的故障转移。Replication 部分只读，只显示状态。

**反目标**（技术上能用，但这样做就算失败）

- 在 readonly 连接上，能取消别人的查询或断开别人的会话。
- 在 prod 连接上，按一下键、不经确认就断开了会话。
- 打开页面后，服务器负载明显上升：采样太频繁，或者一次查询扫了大量数据（例如每 2 秒做一次全量 `pg_stat_statements` 排序）。
- 某个部分因为权限不够而报错，导致整个标签页打不开，或者 Status 停止采样。
- Sessions 和 Slow queries 里的 SQL 原文泄露了密码，比如 `CREATE USER … IDENTIFIED BY '…'`、`ALTER USER … PASSWORD '…'`。

## 3. 入口和标签页

### 3.1 入口

- 沿用现有的 `ServerInfo` 动作（默认 `<leader> i`），右键菜单里也是同一个 “Server status”。`app.go` 里对 `ServerInfo` 的处理，从“只允许 Redis”改成：Redis 连接打开现有的 `serverTab`，SQL 连接打开新的 SQL 状态页。
- 右键菜单：`KindConn`、`KindDatabase`、`KindSchema` 节点上，SQL 连接也加 “Server status”。表节点上不加，避免菜单变长（我的决定）。
- `keymap/defaults.go` 里 `ServerInfo` 的描述改成 `Server status: load, sessions, locks, …`，不再写 “Redis”。
- `keymap/kind.go` 里 Global 上下文的 `ServerInfo` 现在标的是 `KindRedis`，要去掉这一项，否则 SQL 连接的帮助和 which-key 里看不到这个键。动作 ID（`redis.server`、`redis.killclient`）保持不变，以免用户在 `keymap.toml` 里的自定义失效。
- 每个连接只有一个状态页。tab key 沿用 `serverTabKey(conn)`，标题是 `<conn> · server`，和 Redis 一致。
- PostgreSQL、SQL Server、Oracle 的 `Conn` 是按库区分的（`DatabaseScoped()` 为 true）。状态页用连接配置里的库打开 `Conn`；没有配置库时，用 `Features().FallbackDatabase`（PG 是 `postgres`，SQL Server 是 `master`）。页面上读到的会话、锁、设置都是整个实例的；只有 `pg_stat_statements` 依赖“扩展装在哪个库”，见 §6.2。

### 3.2 标签页结构

新的 tab 类型 `sqlServerTab`，放在 `internal/ui/sqlservertab.go`。和 Redis 的 `serverTab` 是同一套模式：

- 顶部是部分名的标题栏：`Status · Sessions · Locks · Slow queries · Variables · Replication`，当前部分高亮。PostgreSQL 上 Variables 叫 Settings；其他引擎的叫法由驱动通过 `ServerInfo` 返回（见 §4），UI 不按驱动名分支。
- 宽度不够放下整条标题栏时，缩成 `‹ Locks 3/6 ›`。
- `[` 和 `]` 切换部分，和 Redis 状态页一样。
- Status 只在“标签页可见、当前部分是 Status”时采样，复用 `shower` 接口（`Shown` / `Hidden`）和 `gen` 计数，防止旧的采样消息混进来。
- 其他五个部分共用一个 `grid.Model`。切到某个部分时读一次，之后按 Refresh 重新读。重新读同一个部分时用 `ReplaceResult`，保持光标位置，和 Redis 一样。
- 表格部分可以用 grid 的全部只读操作：移动、`/` 搜索、`K` 看完整单元格、`yl` / `yy` 复制、导出（复用 `exportSource`）。
- 状态行和错误显示的方式跟 Redis 状态页一致：一行，错误用 `theme.Error`。

**是否和 Redis 的 `serverTab` 合并成一个 tab 类型**，留给实现者决定（§11）。约束是：Redis 状态页的现有行为和测试不能变。

### 3.3 按键

新增 keymap 上下文 `DBServer`（值为 `"db_server"`），加进 `custom.go` 的上下文列表，这样用户可以在 `keymap.toml` 里改键。不复用 `RedisServer`，因为两边的按键集合不同，而且改上下文名会让用户已有的自定义键失效。

| 键 | 动作 | 说明 |
|---|---|---|
| grid 只读键 | `gridBindings(DBServer)` | 和 Redis 状态页相同 |
| `[` / `]` | `PrevSection` / `NextSection` | |
| `<leader> r` | `Refresh` | 全局键。在 Status 上清空走势图、重新开始采样；在其他部分上重新读一次。**不重载 catalog**：`app.go` 处理 `Refresh` 时，会先对 `Target()` 的连接调用 `reloadMeta`，所以状态页上要跳过这一步（例如 `Target()` 返回空连接，或者在 `Refresh` 分支里判断 tab 类型）。否则每次刷新都会重读整个 catalog，MySQL 上是全部库 |
| `d d` | `KillClient`（复用动作） | 断开光标所在的会话，描述写 `Disconnect the session (sessions list)` |
| `c` | 新动作 `CancelSessionQuery` | 取消这个会话正在执行的查询，会话保留 |
| `a` | 新动作 `ToggleIdle` | Sessions 部分：显示 / 隐藏空闲会话。默认隐藏 |
| `F` | `FilterPrompt` | Variables 部分：按参数名过滤（子串匹配，不区分大小写，在客户端过滤）；`f x` 清除过滤 |

所有界面文字里的键名都用 `keyHint` 生成，不写死。

## 4. `db` 层接口

### 4.1 接入方式

在 `db.Conn` 上加一个方法 `Server() ServerReader`，形式和 `Meta()` 一样。四个驱动都实现，所以不用可选接口加类型断言。`ui/fakes_test.go` 里的 `fakeConn` 也要实现它，用来写 UI 测试。

下面是草案，方法名和字段名实现者可以调整，但要保持职责划分：**驱动负责查询并给出原始数值，UI 负责格式化和计算速率。**

```go
// ServerReader reads server-wide status for the server tab. Each method
// is independent: one failing (permission, missing extension, old
// version) must not affect the others.
type ServerReader interface {
	// Info is what does not change while the tab is open: version, the
	// section titles the engine uses, which sections and actions work.
	Info(ctx context.Context) (ServerInfo, error)
	// Sample is one Status sample: facts plus metrics.
	Sample(ctx context.Context) (ServerSample, error)
	Sessions(ctx context.Context, includeIdle bool) (SessionList, error)
	Locks(ctx context.Context) (*ResultSet, error)
	// TopStatements is the statement summary, ordered by total time,
	// at most n rows.
	TopStatements(ctx context.Context, n int) (*ResultSet, error)
	Settings(ctx context.Context) (*ResultSet, error)
	Replication(ctx context.Context) (*ResultSet, error)
	// CancelQuery stops what the session runs; KillSession ends the
	// session. ok is false when it was already gone.
	// Both check, in the same statement or right before it, that the
	// session still matches id.Since, and return ErrSessionChanged if not.
	CancelQuery(ctx context.Context, id SessionID) (ok bool, err error)
	KillSession(ctx context.Context, id SessionID) (ok bool, err error)
}

type ServerInfo struct {
	Version       string // "8.4.2", "PostgreSQL 16.4", "SQL Server 2014 (12.0.6024)"
	SettingsTitle string // "Variables" / "Settings" / "Configuration" / "Parameters"
	CanCancel     bool   // CancelQuery works on this server (false: SQL Server, Oracle < 18c)
}

// Fact is one line of the Status summary.
type Fact struct {
	Label string
	Kind  FactKind // Text, Count, Bytes, Percent, Duration
	Text  string   // for Text
	Num   float64  // for the other kinds
	Of    float64  // optional upper bound: "87 / 500"
}

// Metric is one sparkline. Counter metrics are cumulative and the UI
// turns two samples into a per-second rate; gauges are shown as read.
type Metric struct {
	Name    string // "QPS", "TPS", "running", "rows read/s"
	Counter bool
	Value   float64
	Unit    MetricUnit // None, Bytes
}

type ServerSample struct {
	Facts   []Fact
	Metrics []Metric
	At      time.Time // server or client time of the sample, for rates
}

// SessionID names one session and when it started, so a kill cannot hit
// a later session that reused the id (§5.1).
type SessionID struct {
	ID    string // MySQL/PG/SQL Server: the id; Oracle: "sid,serial#"
	Since string // what tells this session from a later one with the same
	             // ID: PG backend_start, SQL Server login_time; MySQL and
	             // Oracle "" (MySQL connection ids only grow until a
	             // restart; Oracle's serial# already tells sessions apart)
}

type SessionList struct {
	Rows *ResultSet
	IDs  []SessionID // row by row
	// Self marks rows that are sqlvim's own connections (§5.3).
	Self []bool
	// Summary is the line above the table: "87 sessions · 12 running".
	Total, Active int
}
```

`Info` 在标签页打开时读一次，并在 `<leader> r` 时重读。`Info` 失败时（通常是连不上），整个标签页显示错误；之后各部分仍然可以各自重试。

### 4.2 错误

- 权限不足、扩展不存在、版本不支持时，驱动返回包了 `db.ErrUnavailable` 的错误（新增的 sentinel），附上一句给用户看的原因，例如 `pg_stat_statements is not installed in database "shop"`、`needs VIEW SERVER STATE`、`needs the PROCESS privilege`。UI 用 `errors.Is` 判断，把原因显示成灰色提示，而不是红色错误。
- 其他错误照常显示服务器返回的原文。

### 4.3 SQL 原文脱敏

Sessions、Locks（等待方和阻塞方的 SQL）和 TopStatements 里的 SQL 原文，都要经过脱敏函数再进入 ResultSet。函数放在 `sqlx`，是纯函数，用表驱动测试。规则是：把下面这些语句里 `IDENTIFIED BY`、`PASSWORD`、`WITH PASSWORD =` 后面的字符串字面量换成 `'***'`：

- `CREATE / ALTER USER`、`CREATE / ALTER ROLE`、`CREATE / ALTER LOGIN`、`SET PASSWORD`
- Oracle 的 `ALTER USER … IDENTIFIED BY`（这里密码不加引号）

做法和 Redis 的 `rcmd.Redact` 一致：宁可多遮。

### 4.4 契约测试

`dbtest/contract.go` 加 `RunServerContract`（或者放进 `RunContract` 的子测试，由实现者定），四个驱动的集成测试都调用它。至少覆盖：

- `Info` 返回非空的 `Version`。
- `Sample` 连续两次都成功；每个 `Metric` 的 `Name` 在两次采样之间保持一致，顺序也不变；Counter 类型的值不会减小。
- 用另一个连接执行 fixture 里的 `SleepSQL`。之后在 `Sessions(includeIdle=false)` 里能找到这个会话，SQL 列包含这条语句。对它调用 `CancelQuery`：引擎支持取消时，那条查询在远少于 30 秒内结束，会话还能用（MySQL 的 `SLEEP` 被 `KILL QUERY` 打断时返回 1，不报错，所以只断言“提前结束”，不断言错误类型）；`CanCancel=false` 时，返回 `ErrUnavailable`。再调用 `KillSession`，那个连接随后报 `ErrConnLost`。Oracle 被 kill 的会话报 ORA-00028 / ORA-03113 等错误，驱动的错误包装要把这些也认成 `ErrConnLost`。用一个 `Since` 不对的 `SessionID` 调用 `KillSession`，返回 `ErrSessionChanged`，那个会话不受影响。对已经不存在的会话再 kill 一次，返回 `ok=false` 且没有 error。
- 锁等待：连接 A 开事务，更新 fixture 表的一行，不提交；连接 B 更新同一行，被阻塞。`Locks` 返回一行，阻塞方是 A 的会话，等待方是 B 的会话。测试结束时回滚 A。
- `Settings` 里包含引擎的一个已知参数，例如 MySQL 的 `max_connections`、PG 的 `max_connections`、SQL Server 的 `max server memory (MB)`、Oracle 的 `processes`。
- `TopStatements` 和 `Replication` 返回 ResultSet，或者返回 `ErrUnavailable`，不能返回其他错误。
- Oracle 的测试用户是 PDB 里的普通用户，没有 `v$` 视图的权限，也没有 `ALTER SYSTEM`。集成测试的初始化要先给它授 `SELECT_CATALOG_ROLE` 和 `ALTER SYSTEM`，否则契约测试会全部变成 `ErrUnavailable`。其他三个引擎的测试账号已经是 root / sa / 超级用户。测试容器都是单机，Replication 可以是空表。
- 脱敏在 `sqlx` 的单元测试里测，契约测试不测。

## 5. 取消查询和断开会话

### 5.1 规则

规则和 Redis 的 `askKill` 一致。判断逻辑写成纯函数（放在 `sqlx`，或者跟 Redis 共用一个守卫，实现者定），用表驱动测试：

| 连接 | 取消查询 / 断开会话 |
|---|---|
| readonly（含默认 readonly 的 prod） | 拒绝，状态栏显示 `read-only connection: cannot cancel or disconnect sessions` |
| 普通连接 | 每次都弹确认框 |
| prod 且明确设置 `readonly = false` | 弹确认框，并要求输入连接名 |

**会话 id 被复用**：从读出列表到按下确认之间，原来的会话可能已经结束，id 被新会话占用（SQL Server 的 session_id、PG 的 pid 都会很快复用）。所以 `SessionID` 带上能区分会话的信息（§4.1），驱动执行前核对；不一致时返回 `ErrSessionChanged`（新增的 sentinel），不执行。UI 显示 `session 48211 changed since the list was read; refreshed`，并重读 Sessions。宁可拒绝，也不能断开一个用户没见过的会话。

只有 Sessions 部分能触发这两个操作。在其他部分按下时，状态栏提示 `works in the sessions list`。光标不在任何一行上时，提示 `no session selected`。

### 5.2 确认框内容

确认框的标题和正文要写清楚：

- 动作：`Cancel the query of session 48211?` 或 `Disconnect session 48211?`
- 会话的用户、主机、库、状态、已运行时长
- 正在跑的 SQL，截断到确认框宽度
- 断开会话时额外加一句：`Its open transaction, if any, is rolled back.`

### 5.3 sqlvim 自己的会话

- 四个驱动在建连时都设置应用名 `sqlvim`。SQL Server 已经通过 `app name` 设置了；MySQL 用连接属性 `program_name`；PG 用 `application_name`；Oracle 用 go-ora 的 program / 模块名。具体参数写法，实现时用 Context7 查。用户在 `Params` 里自己配置的值优先。
- `SessionList.Self` 标记应用名是 `sqlvim` 的会话。在这样的会话上取消或断开时，确认框加一行：`This is a sqlvim connection (maybe this one).`。只提醒，不拒绝。
- 页面自己用来查询的那个连接，被断开后会走现有的连接丢失处理：下次操作时重连。因为这个页面不在事务里，这样做不违反“事务中不静默重连”。

### 5.4 执行之后

- 成功：状态栏显示 `cancelled the query of session 48211` 或 `disconnected session 48211`，然后重读 Sessions。
- 会话已经不在了：显示 `session 48211 is already gone`，然后重读。
- 没有权限：显示驱动返回的原因。

## 6. 每个引擎读什么

下面是每个部分的**最低要求**和建议的数据来源，不是最终 SQL。最终 SQL 由实现者写，并用集成测试验证。表格的列可以按引擎不同，但都要按“先放最有用的列”排列，并在 80 列宽度下截断后，仍能看出关键信息。

**通用约定**

- 时长列统一格式化成 `850 ms`、`41s`、`4m12s`、`3h05m`，在驱动里格式化成文本。表格部分没有 Kind 信息，UI 不再二次处理。驱动不能 import `ui`，所以这个格式化函数要新写在 `db` 包里，四个驱动共用。`ui` 里现有的 `durationText` / `uptime` 格式不同，Status 摘要继续用它们，不要求统一。
- 只读取少量数据：TopStatements 默认取 50 条；Sessions 不限条数，但 SQL 列在驱动里截断到 1024 个字符。

### 6.1 MySQL（目标 5.7 和 8.0+，测试用 8.4）

- **Status 摘要**：版本；运行时长；角色（`read_only` / `super_read_only`，是否配置了复制源）；连接数、`max_connections`、`Threads_running`、`Max_used_connections`；InnoDB buffer pool 大小和命中率（由 `Innodb_buffer_pool_read_requests` 和 `Innodb_buffer_pool_reads` 算出）；`Aborted_clients` / `Aborted_connects`。来源：`SHOW GLOBAL STATUS` 和 `SHOW GLOBAL VARIABLES`，一次往返内取需要的那几项。
- **走势图**：QPS（`Questions`，counter）、commits/s（`Handler_commit`，counter。不用 `Com_commit + Com_rollback`，因为 autocommit 的语句不计入它们，典型应用上会是一条平线；`Handler_commit` 包括 autocommit，但一条语句可能计入多次，所以叫 commits/s 而不叫 TPS）、running（`Threads_running`，gauge）、slow/s（`Slow_queries`，counter）、net in / out（`Bytes_received` / `Bytes_sent`，counter，Bytes）。
- **Sessions**：8.0.22 起用 `performance_schema.processlist`，否则用 `information_schema.PROCESSLIST`；左连接 `information_schema.INNODB_TRX` 得到事务开始时间。列：id、user、host、db、command/state、time、trx（事务已持续多久）、sql。“空闲”指 `Command = 'Sleep'` 且不在事务里。
- **Locks**：8.0 用 `performance_schema.data_lock_waits` 联 `data_locks` 和 `INNODB_TRX`；5.7 用 `information_schema.INNODB_LOCK_WAITS`。列：waiting id、wait time、blocking id、blocker 的状态和已运行时长、lock mode/type、表、等待方 SQL、阻塞方 SQL（阻塞方可能空闲，SQL 为空）。另外显示一行元数据锁等待：`performance_schema.metadata_locks` 里 `PENDING` 的锁。这一项可选，表没开 instrument 时跳过。
- **Slow queries**：`performance_schema.events_statements_summary_by_digest`，按 `SUM_TIMER_WAIT` 倒序。列：digest 文本、次数、总耗时、平均耗时、扫描行数 / 返回行数、库。`performance_schema` 关闭时返回 `ErrUnavailable`。
- **Variables**：`SHOW GLOBAL VARIABLES`。列：name、value。
- **Replication**：8.0.22 起用 `SHOW REPLICA STATUS` 和 `SHOW REPLICAS`，更早的版本用 `SHOW SLAVE STATUS` 和 `SHOW SLAVE HOSTS`。列：方向（replica of / source to）、对端、IO/SQL 线程状态、`Seconds_Behind_Source`、最后的错误、GTID 位置。
- **取消 / 断开**：`KILL QUERY <id>` / `KILL <id>`。云上托管的 MySQL（RDS 等）可能要用 `CALL mysql.rds_kill`，第一版不做，只显示权限错误。
- **权限**：`PROCESS` 权限（看别人的会话、INNODB_TRX）；`performance_schema` 的 SELECT 权限；`CONNECTION_ADMIN` 或 `SUPER`（kill 别人的会话）。

### 6.2 PostgreSQL（目标 12+，测试用 16）

- **Status 摘要**：版本；运行时长（`pg_postmaster_start_time()`）；角色（`pg_is_in_recovery()`，主库上统计副本数）；连接数、`max_connections`、active 和 `idle in transaction` 的数量；当前库的缓存命中率（`pg_stat_database` 里的 `blks_hit` / `blks_read`）；库大小；deadlocks；temp files 及其大小；最老事务的时长和 pid。
- **走势图**：TPS（`xact_commit + xact_rollback`，对所有库求和，counter）、active（gauge）、rows read/s（`tup_returned + tup_fetched`，counter）、rows written/s（`tup_inserted + tup_updated + tup_deleted`，counter）。
- **Sessions**：`pg_stat_activity` 中 `backend_type = 'client backend'` 的行。列：pid、user、application、client、db、state、wait event、query 已运行时长、xact 已持续时长、query。“空闲”指 `state = 'idle'`。
- **Locks**：`pg_stat_activity` 里用 `pg_blocking_pids(pid)` 找出被阻塞的会话，再联出阻塞方。列：waiting pid、wait、blocked by、阻塞方的 state 和时长、锁类型 / 模式、关系名、等待方 query、阻塞方 query。
- **Slow queries**：`pg_stat_statements`。要先检查扩展是否装在 `Conn` 所在的库里；没装时返回 `ErrUnavailable`，原因写清楚是哪个库。PG 13 起列名从 `total_time` 改成 `total_exec_time`，两种都要支持。列：query、calls、total、mean、rows、db。
- **Settings**：`pg_settings`。列：name、setting（带 unit）、source，以及 `pending_restart`。
- **Replication**：主库用 `pg_stat_replication`（副本、state、sent / write / flush / replay lag）；备库用 `pg_stat_wal_receiver` 加 `now() - pg_last_xact_replay_timestamp()`。另外列出复制槽 `pg_replication_slots` 及其滞留的 WAL 大小，单独一组行。
- **取消 / 断开**：`pg_cancel_backend(pid)` / `pg_terminate_backend(pid)`，返回 false 时表示 `ok=false`。
- **权限**：看别人的 query 文本需要 `pg_read_all_stats`（或超级用户）；没有这个权限时 query 列显示 `<insufficient privilege>`，不当作错误。kill 别人的会话需要 `pg_signal_backend`。

### 6.3 SQL Server（目标 2014 起，测试用 2022；2014 必须可用）

- **Status 摘要**：版本（`SERVERPROPERTY('ProductVersion')`、`ProductLevel`、`Edition`）；运行时长（`sys.dm_os_sys_info.sqlserver_start_time`）；角色（有 Always On 时显示 AG 角色）；用户会话数、running 请求数、`max worker threads` 或 user connections 配置；buffer cache hit ratio、page life expectancy（`sys.dm_os_performance_counters`；ratio 类计数器要配合 base 计数器计算）；目标 / 已用内存。
- **走势图**：batch requests/s、transactions/s、running requests、user connections。来源同样是 `dm_os_performance_counters`，其中 `/sec` 类计数器是累计值，作为 counter 处理。
- **Sessions**：`sys.dm_exec_sessions` 左连接 `sys.dm_exec_requests` 和 `sys.dm_exec_sql_text`，只取 `is_user_process = 1`。列：session_id、login、host、program、db、status、wait type、已运行时长、open tran、sql。
- **Locks**：`dm_exec_requests.blocking_session_id <> 0` 的行，加上阻塞方的会话信息和 SQL（阻塞方可能空闲，就从 `most_recent_sql_handle` 取它最后一条 SQL）。
- **Slow queries**：`sys.dm_exec_query_stats` 联 `dm_exec_sql_text`，按 `total_elapsed_time` 倒序。这个视图 2014 就有；第一版不用 Query Store。
- **Configuration**：`sys.configurations`。列：name、value_in_use、value、是否需要重启。
- **Replication**：Always On 用 `sys.dm_hadr_database_replica_states` 联 `availability_replicas`；没有 AG 时返回空表，不当错误。老式事务复制和日志传送第一版不做。
- **取消 / 断开**：`KILL <session_id>`。SQL Server 没有“只取消当前查询、保留会话”的命令，所以 `CanCancel=false`。这时 `c` 键提示 `cancelling a query is not supported on SQL Server; use <断开会话的键> to disconnect`，其中的键名用 `keyHint` 生成。
- **权限**：`VIEW SERVER STATE`，没有时大多数部分都返回 `ErrUnavailable`；kill 需要 `ALTER ANY CONNECTION`。

### 6.4 Oracle（目标 12c 起，测试用 23ai Free）

- **Status 摘要**：版本（`v$instance`）、启动时间、`database_role` 和 `open_mode`（`v$database`）；会话数和 `processes` / `sessions` 上限；buffer cache 命中率（`v$sysstat`）；SGA / PGA 大小。
- **走势图**：user calls/s、executions/s、commits/s（`user commits + user rollbacks`）、active sessions（gauge）。来源是 `v$sysstat` 和 `v$session`。
- **Sessions**：`v$session` 中 `type = 'USER'` 的行，联 `v$sql` 取 SQL 文本。列：sid,serial#、username、machine、program、status、event、已运行时长（`last_call_et`）、sql。
- **Locks**：`v$session.blocking_session` 不为空的行，加上阻塞方信息。
- **Slow queries**：`v$sqlstats` 或 `v$sql`，按 `elapsed_time` 倒序，取前 n 条。不用 AWR / ASH（要额外的 license）。
- **Parameters**：`v$parameter`。列：name、value、isdefault、是否可动态修改。
- **Replication**：Data Guard 用 `v$dataguard_stats` 和 `v$archive_dest_status`；没有配置时返回空表。
- **取消 / 断开**：断开用 `ALTER SYSTEM KILL SESSION 'sid,serial#' IMMEDIATE`；取消用 18c 起才有的 `ALTER SYSTEM CANCEL SQL 'sid,serial#'`。`CanCancel` 按版本判断。
- **权限**：`SELECT_CATALOG_ROLE` 或对应 `v$` 视图的 SELECT 权限；kill 需要 `ALTER SYSTEM`。
- **多租户**：在 PDB 里只能看到本 PDB 的会话，这样就行；第一版不处理 CDB 根容器。

## 7. Status 部分的显示

- 布局和 Redis 的 `statusLines` 一样：上面是摘要，每行“标签 + 值”；空一行；下面每个指标一行“名称 + 当前值 + sparkline”。
- 格式化函数复用 `servertab.go` / `serverstatus.go` 里已有的 `humanBytes`、`groupDigits`、`uptime`、`durationText`、`padRight`，以及 sparkline 组件。这些只用于 Status 部分（UI 根据 `Fact.Kind` 格式化）。
- Counter 指标：第一次采样时还没有速率，值显示 `missing`（沿用现有的占位符），从第二次采样开始显示。两次采样的值变小（服务器重启或统计被重置）时，丢弃这一个点，不画负数。
- 采样间隔沿用 `m.timings.server`（和 Redis 相同，默认 2 秒）。每次采样的查询要轻：每个引擎一次 `Sample` 最多两三次往返。
- 采样出错：停止采样，显示错误，`<leader> r` 重新开始，和 Redis 一样。

## 8. Sessions 部分的额外显示

- 表格上方一行：`87 sessions · 12 running · idle hidden (a)`，其中 `(a)` 用 `keyHint` 生成。
- `Self` 的行在 id 列加一个标记，比如 `*`，表格上方的说明行里解释这个 `*`。
- 长 SQL 在表格里截断，`K` 显示完整内容（脱敏之后的）。

## 9. 测试

- **纯包，表驱动**：SQL 脱敏（§4.3）；取消 / 断开的守卫（§5.1）；`db` 包里的时长格式化；counter 转速率，包括第一次采样和值变小的情况；标题栏收缩（§3.2）。
- **UI**（`fakeConn` 的 `Server()` 返回一个可配置的假 `ServerReader`）：
  - SQL 连接上 `<leader> i` 打开状态页；在 Redis 连接上仍然打开原来的页面。
  - 采样只在可见且位于 Status 部分时进行，切走就停。
  - 某个部分返回 `ErrUnavailable` 时，显示灰色原因，其他部分不受影响。
  - readonly 连接上 `dd` 和 `c` 被拒绝；普通连接上弹确认框；prod 且可写时要求输入连接名。
  - 驱动返回 `ErrSessionChanged` 时显示提示并重读 Sessions。
  - 在状态页上按 Refresh 不会触发 catalog 重载。
  - SQL 连接的帮助和 which-key 里能看到 `ServerInfo`。
  - `CanCancel=false` 时 `c` 的提示。
  - `a` 切换空闲会话后重读。
  - Variables 部分的 `F` 过滤。
- **集成**（`integration` tag）：四个驱动都跑 §4.4 的契约测试。MySQL 另外跑一次 5.7 容器（`mysql:5.7`），测 Sessions、Locks、Replication 的老版本分支。SQL Server 2014 没有官方容器，只做下面的手工验证。
- **手工验证**：
  - 连作者的 SQL Server 2014 生产库（prod，readonly），确认六个部分都能显示或给出合理的 `ErrUnavailable` 原因，`dd` 被拒绝，打开页面后服务器负载没有明显变化。
  - 在 MySQL 和 PG 的测试库上，制造一次锁等待，确认 Locks 能看到它，然后断开阻塞方，等待方随之继续执行。

## 10. 文档和发布

- `CHANGELOG.md` 的 `[Unreleased]` 下加一条 Added。
- README 的功能列表里加一行，同时改 `README.zh-CN.md`。
- 帮助、which-key 和提示由 keymap 自动生成，不用另外写。

## 11. 留给实现者决定的

- 是否把 Redis 的 `serverTab` 和 SQL 的状态页合并成一个类型，用一个“数据源”接口区分；或者保持两个类型，抽出共用的显示代码。约束：Redis 状态页的行为和现有测试都不变。
- `ServerReader` 的方法名、字段名、`FactKind` 的种类，以及表格的具体列名和列顺序（按 §6 的最低要求）。
- 每个引擎的具体 SQL，以及老版本分支怎么判断（按版本号，还是先试新视图、失败再退回）。
- 守卫函数放在哪个包。
- 一次 `Sample` 的具体查询怎么合并。
