# SQLite 支持设计

日期：2026-11-07

## 意图

datavim 目前连接的都是有网络端口的数据库服务。用户还需要处理三类 SQLite 文件：

1. 本地开发用的应用库；
2. 从线上拉下来的数据文件；
3. SSH 服务器上的数据库文件，要能直接增删改查。

第三类是这个设计的核心难点：SQLite 没有端口，现有 SSH 隧道（`ConnConfig.Dial`）用不上；而且文件可能正被线上应用使用，访问方式不能破坏 SQLite 的文件锁。

## 目标

- 新增 `sqlite` 驱动：一个连接打开一个数据库文件，本地或 SSH 远程，由 `ssh` 字段是否为空决定。
- 两种方式下都能使用：表格浏览、单元格编辑、控制台事务、补全、导出，以及 Modify Table 的 SQLite 原生操作。
- 只读、生产确认、无 WHERE 拦截、编辑只影响一行、事务中不静默重连等安全规则全部保留。

## 非目标

- 服务器状态页：SQLite 没有服务端、会话或复制信息。入口对 SQLite 连接隐藏。
- `ATTACH` 的库进入对象树：第一版不做。在控制台里可以 `ATTACH` 后查询（远程只读连接除外，见“只读”）。
- Modify Table 不做“新建表、复制数据、替换旧表”式的重建。改列类型、改约束等返回 `manual`，界面提示不支持，用户可在控制台手写。
- 不新建数据库文件，文件不存在就报错。
- 不向服务器上传任何程序，不支持 Windows 服务器。

## 绝对不能发生的事

- **损坏或覆盖线上数据。** 因此禁止用 SFTP 挂载后在本机读写远程文件，也禁止下载、修改、再传回。文件只由和它同机的 SQLite 进程访问。
- **路径打错时悄悄生成空库。**
- **只读连接仍能写服务器上的文件。**
- **控制台输入能在服务器上执行命令。** 点命令（`.shell`、`.system` 等）和能执行程序的 SQL 函数（`edit()`、`load_extension()`，实测 `edit()` 真的能跑命令）在所有远程连接上都必须拦住，可写连接也一样。
- **用户的事务因为结果太长而丢失。** 控制台查询超过 `max_rows` 时，事务必须完好。
- **BLOB 被静默改写成字符串。**

## 已定决策

| 决策 | 决定方 |
| --- | --- |
| 支持本地和 SSH 远程，两者都能增删改查 | 用户 |
| 远程依赖服务器上已有的 `sqlite3`，没装就明确报错 | 用户 |
| 表单只留 Name、文件路径、SSH host、Env、Read-only | 用户认可 |
| 树结构为「连接 → 表/视图」，没有数据库和 schema 层 | 用户认可 |
| Modify Table 只做加列、改列名、删列、改表名、增删索引 | 用户认可 |
| `prod` 默认只读，沿用 `Connection.IsReadOnly()` | 用户认可 |
| 取消长查询时结束远程进程；断开或事务未提交时明确报错，不静默重连 | 用户认可 |
| 下文的远程协议、版本要求和其余实现选择 | 设计者判断，可改 |

## 配置

```toml
[[connection]]
name = "app-local"
driver = "sqlite"
database = "~/dev/app/app.db"

[[connection]]
name = "app-prod"
driver = "sqlite"
database = "/var/lib/app/app.db"
ssh = "prod-box"          # ~/.ssh/config 的 Host 别名，可走跳板机
env = "prod"
```

- `database` 必填，存文件路径，表单标签显示为 `File`。
- `host`、`port`、`user`、密码不适用：配置校验不要求也不补默认值，也不走 keychain 或密码提示流程。填了 `host`/`port` 的配置报错，避免用户以为生效。
- 本地路径展开 `~`。远程路径的 `~/` 由远程 shell 展开，其余部分必须做 shell 转义。
- 连接表单隐藏 Host、Port、User、Password、Save password，`Driver` 选项加上 `sqlite`。

## 架构

新增 `internal/db/sqlite`，分两层：

```
Driver / Conn / Session / MetaReader / Dialect / DDLWriter   ← 两种模式共用
                 │ 内部接口：执行一条 SQL，流式返回列和行
        ┌────────┴─────────┐
   本地引擎              命令行引擎
 modernc.org/sqlite     sqlite3 进程（经 db.ExecFunc 启动）
```

- `Driver.DatabaseScoped()` 返回 true，每个 `Conn` 只对应一个文件。
- `db` 包不能依赖 SSH：`ConnConfig` 增加一个抽象的启动进程函数 `Exec`（暂名 `db.ExecFunc`，给出命令行，返回 stdin、stdout、等待和关闭）。`tunnel.Pool` 增加对应实现，使用现有 chain（跳板机、host key、口令流程都沿用），`session.Manager` 负责注入。`Dial` 对 SQLite 无用。
- Tunneler 接口相应扩展；未知 host、口令提示这些错误类型和现有隧道一致，UI 已有的处理流程继续生效。

### 本地引擎

- 用 `modernc.org/sqlite`（已是直接依赖，`CGO_ENABLED=0` 不变）。
- 用 `mode=rw`（只读时 `mode=ro`）打开，不会新建文件。
- 每个连接设置 `busy_timeout=5000`、`foreign_keys=ON`。只读时再加 `PRAGMA query_only=ON`。
- 取消用 context，驱动负责中断语句，Session 保持可用。

### 命令行引擎（远程）

以下行为已于 2026-11-07 用本机 `sqlite3` 3.54 实测确认。

**启动**

```sh
sh -c 'echo PID=$$; exec sqlite3 -batch [-readonly] [-safe] "file:<路径>?mode=<rw|ro>" 2>&1'
```

- `mode=rw` 确认不会创建缺失文件，错误信息为 `unable to open database`。只读连接必须用 `mode=ro`：`-readonly` 配 `mode=rw` 实测报错 `access mode not allowed: rw`。
- `exec` 后 PID 不变，用于取消。`2>&1` 在服务器一侧合并两路输出，实测错误和结果顺序正确。
- 路径要先按 URI 规则编码（`?`、`#`、`%`），再做 shell 转义。
- 启动后读取版本并设置 `.mode quote`、`.headers on`、`.timeout 5000`、`PRAGMA foreign_keys=ON`。
- `sqlite3` 不存在（退出码 127 或 `command not found`）时报错：`sqlite3 not found on <ssh host>: install it on the server`。

**分帧**：每条语句后发送 `.print <随机标记>`，读到标记即为结束，标记每个进程随机生成。实测管道模式下输出会及时刷新，不需要 PTY。

**结果解析**：用 `quote` 模式，不用 JSON。实测 JSON 模式把 BLOB 输出成字符串，类型丢失；`quote` 模式保留整数、实数、文本、BLOB（`x'00ff'`）和 `NULL`，大整数不丢精度，重名列也能保留。

解析器必须是流式的，且不限行长，并处理这些情况：

- 新版本把含换行的文本写成 `unistr('it''s\u000aline2')`；
- 旧版本可能输出字面换行或 `replace(replace('…','\n',char(10)),'\r',char(13))`；
- 其他无法解析为行的输出，按错误信息处理（如 `Parse error near line N: …`、`Error near line N: …`）。第一版用这些信息定位错误位置（`errpos`）；解析不到位置时只显示消息。

**列名和列类型**：`quote` 模式不输出声明类型，结果为空时连表头也没有，而 `Stream` 必须调用 `onColumns`，契约也要求 `DatabaseType` 非空。对 `SELECT`、`WITH`、`VALUES` 开头的语句，在同一批输入里、语句之前发送 `CREATE TEMP VIEW _dv_<随机> AS <查询>`、`SELECT name, type FROM pragma_table_info(...)` 和 `DROP VIEW`，各用独立标记分帧，不多等一次往返。实测在 `-readonly` 和 `-safe` 下可用。

- 视图创建失败（如 `WITH … INSERT`）不算错误，跳过即可，真正的错误由语句本身报告；
- `PRAGMA`、`RETURNING` 等没有视图的语句，列名来自表头，类型为 `ANY`；
- 声明类型为空（表达式列、无类型列）时 `DatabaseType` 也是 `ANY`。

本地引擎同样用声明类型，空值报 `ANY`，让两种模式的行为一致。

**Exec 结果**：语句后执行 `SELECT changes(), last_insert_rowid()`，用作 `ExecResult`。

**参数绑定**：`args` 由驱动按 SQLite 语法转成字面量，发送前完成。BLOB 用 `x''`，文本转义单引号。不使用 `.parameter`。

**发送前校验（必须）**：实测未闭合的字符串或注释会把后面的标记吞进语句里，若 stdin 保持打开，读取端将永远等不到标记。发送前必须做到：

1. 每一行输入都不能被当作点命令：逐条拆分语句，拒绝以 `.` 开头的语句，报错 `dot commands are not supported`；
2. 判断语句是否完整（等价于 `sqlite3_complete`，包括字符串、注释、`CREATE TRIGGER … BEGIN … END;`），不完整就直接报错，不发送；
3. 语句末尾补 `;\n`。末尾的 `--` 注释不能吃掉分号，所以先换行再补分号。

**取消**：通过新的 exec 通道在服务器执行 `kill -INT <pid>`，不依赖 SSH signal 请求（要求 OpenSSH 7.9+）。关闭通道只会关闭 stdin，正在运行的查询不会停止，仍占着锁，因此不能只关通道。实测 SIGINT 后进程退出，未提交的事务会回滚。所以：

- 没有打开事务：透明启动新进程，Session 继续可用。临时表和 `PRAGMA` 状态丢失；
- 打开了事务：Session 返回包装 `ErrConnLost` 的错误，走 UI 现有的“事务中不重连”路径，并提示事务已回滚。

用户主动取消后临时表和 `PRAGMA` 状态会丢失，这是 `Session` 约定的唯一放宽。契约的 “Session keeps state, survives cancel and early stop” 通过 `Fixture` 开关，只对命令行引擎的取消放宽。

**连接断开**：`ErrConnLost` 向上传递。没有 PTY 时进程收不到 SIGHUP，`kill -INT` 也发不出去（这是推理，未在真实 SSH 上测过，实现时要验证）。所以正在执行的语句可能继续跑完：

- 事务中：语句结束后 `sqlite3` 读到 stdin EOF 并退出，事务回滚；
- 事务外：长 `UPDATE` 可能跑完并提交，其间一直占着写锁。

错误提示不能写“已回滚”或“未执行”，要写成：`connection lost; the running statement may still finish on the server`，事务中再说明未提交的修改不会保留。README 写明。第一版不做看门狗。

**提前停止（`ErrStop`）**：`runner.RunOne` 超过 `max_rows` 时会用到，属于常见路径。

- **`Session`（控制台）**：不结束进程，读完并丢弃剩余输出，事务和临时表都保留，`Stream` 返回 nil，契约不放宽。大结果可能较慢，读取期间状态要显示，用户可取消（按“取消”规则处理）。
- **`Conn` 连接池的进程（表格分页、导出）**：没有状态需要保留，直接结束并由连接池换新进程，否则 `sqlite3` 会阻塞在写输出上，一直持有读锁。

**并发**：`Conn` 维护最多 4 个按需启动的进程作连接池，空闲 60 秒后关闭；每个 `Session` 独占一个进程。OpenSSH 默认 `MaxSessions` 是 10，超限错误要原样呈现，不无限重试。

**版本要求**：

- 最低 3.26（RHEL 8），低于此版本明确报错并显示版本号；
- `DROP COLUMN` 需要 3.35，`RENAME COLUMN` 需要 3.25。旧版本上由服务器报错，消息直接呈现即可。

### 只读

分三层：

1. **所有模式**：现有 `runner.Guard` 客户端拦截。`sqlx` 要把 `PRAGMA` 写操作、`VACUUM`、`REINDEX`、`ATTACH`、`DETACH` 正确分类，见下文；
2. **本地**：`mode=ro` + `query_only`；
3. **远程**：`-readonly`。实测它也让 `ATTACH` 的库只读，但挡不住 `writefile()` 写服务器文件，所以 3.37 及以上再加 `-safe`。

`-safe` 的两个实测副作用：`.shell` 和 `ATTACH` 等违规操作会让进程直接退出（退出码 1），以及 `writefile`、`load_extension` 被禁用。处理方式：

- 只读远程连接在发送前就拒绝 `ATTACH`，报错说明原因，避免进程退出；
- 其他原因导致进程退出时，走“取消”一节的规则。

**函数拦截（所有远程连接）**：发送前用 `sqlx` 词法器扫描 token，发现“标识符（含带引号的形式）后跟 `(`”且名字（不分大小写）命中下列函数时，拒绝并说明原因。

- 所有远程连接：`edit`、`load_extension`；
- 只读远程连接再加：`writefile`、`readfile`、`fsdir`。这同时覆盖 3.37 以下没有 `-safe` 的情况。

SQLite 没有动态 SQL，词法扫描不会被拼接字符串绕过。

可写远程连接不加 `-safe`，因为要支持 `ATTACH`。此时 `writefile()` 仍可用：用户本来就有该服务器的 SSH 权限，这不是越权，README 写明。

安全测试同时覆盖可写和只读连接。

### 元数据

全部用 SQL 读取，两种引擎共用：

- `sqlite_schema`（旧版本回退 `sqlite_master`）、`pragma_table_xinfo`、`pragma_index_list`、`pragma_index_info`、`pragma_foreign_key_list`；
- `Databases()` 和 `Schemas()` 返回 nil，`Tables(Namespace{})` 返回 `main` 下的表和视图。用户表以外的 `sqlite_*` 不列出；
- `DDL` 取 `sqlite_schema.sql`，`Catalog` 一次读完；
- 没有存储过程：`Catalog` 的 routine 为空，`RoutineDDL` 返回 `ErrNotFound`；
- `Server()` 的每个方法都返回 `db.Unavailable`。

### 方言和 `sqlx`

新增 `sqlx.SQLite`，`driverNames` 加 `"sqlite"`，然后补齐所有 `switch`：

- **词法**：`--` 和 `/* */` 注释（`#` 不是注释）；标识符 `"x"`、`` `x` ``、`[x]`；字符串 `''`；BLOB `x''`。
- **拆分**：`CREATE TRIGGER … BEGIN … END;` 内部的分号不拆开。
- **分类**：`PRAGMA name` 和 `PRAGMA name(arg)` 若是只读 pragma 视为读，其余（尤其 `PRAGMA x = y`）视为写；`VACUUM`、`REINDEX`、`ATTACH`、`DETACH` 视为写。
- **事务**：`BEGIN [DEFERRED|IMMEDIATE|EXCLUSIVE]`、`COMMIT`、`END`、`ROLLBACK`、`SAVEPOINT`。
- **Features**：

```go
SQLite: {
    ServerReadOnly: true, TxDDL: true, SchemaTools: true,
    RowLocator: LocateRowID,
}
```

- 补全关键字、literal、`edit/plan.go` 的 `DEFAULT VALUES`（SQLite 支持）、`analyze.go`、highlight（用 Chroma 的 `sql` lexer）。
- 新增能力位（如 `Features.ServerStatus`），隐藏服务器状态页入口，不要比较驱动名。
- `TruncateTable` 写成 `DELETE FROM t`，安全确认仍然要走。

### 单元格类型

SQLite 是动态类型：声明类型不决定单元格实际存的是什么，而 `db.Value` 只有 `Text` 和 `Null`。

- `db.Value` 增加 `Blob bool`，只有 SQLite 驱动会设置。其他驱动的零值不受影响，现有比较也不变。
- BLOB 单元格的 `Text` 是 `x'00FF'`（两种引擎一致），`Blob` 为 true。文本 `x'00ff'` 的 `Blob` 为 false，两者不会混淆。
- `sqlx.Literal`：`v.Blob` 时原样输出 `Text`；文本一律加引号并转义。`IsBinaryType` 不参与 SQLite 判断，因为 BLOB 列里也可能存文本。这样行定位、INSERT 导出、数据比较同步都能写回真正的 BLOB。
- **BLOB 单元格在表格里只读**：第一版不能直接编辑，提示可在控制台用 `x'…'` 修改；同一行的其他列仍可编辑，整行删除也可以。
- 数字：声明类型是数字或 `ANY`，且文本是数字时，写成裸数字；其他声明类型写成带引号的字符串，由列亲和性转换。
- 已知限制：无类型列里存的文本 `'5'` 经导出或同步会变成整数 `5`。行定位不受影响，匹配不到时“必须恰好影响一行”的规则会拒绝这次编辑。README 写明。

### 行定位

编辑行的定位顺序沿用现有逻辑：主键 → 唯一非空索引 → 行 id。

`WITHOUT ROWID` 表必有主键，所以永远走不到行 id 这一步，不需要整行比对作兜底（这比和用户讨论时的说法更简单）。行 id 的 `RowIDSelect`、`RowIDMatch` 要按 SQLite 实现，只使用 `_rowid_`。无键的表如果有列叫 `_rowid_`（不分大小写），视为无法定位，编辑不可用并说明原因。

### 树

`Databases` 和 `Schemas` 都为 false 是新组合。对象树从连接节点直接展开到表和视图。现有 UI 有约 48 处判断 `KindConn`/`KindDatabase`/`KindSchema`，包括导入、比较、节点菜单、补全和控制台的 `scopeSQL`。实现者自行决定是真的少一层，还是内部保留一个隐藏的 namespace 节点，只要用户看到的是两层，且这些入口都可用。

### Modify Table 和 `DDLWriter`

- `CreateTable`：完整输出，注释不支持。
- `AlterTable`：原生支持加列、改列名、删列、改表名、增删索引，每项一条语句。其他变更放入 `manual`。
- 外键差异放入 `manual`。
- `NormalizeType`：大小写归一，保留原样的声明类型，不做亲和性折叠（`VARCHAR(50)` 仍是 `varchar(50)`）。
- 因为 `SchemaTools: true`，schema 比较、数据比较、导入也会开放。它们都建立在 `DDLWriter` 和 `Querier` 之上，契约测试通过即可用；过不了的部分，第一版把对应入口对 SQLite 关掉并在 README 说明，不做重建。

## 测试

- `internal/db/dbtest/contract.go` 在本地和命令行两种引擎上都运行。命令行引擎的契约测试用 `os/exec` 实现 `db.ExecFunc`，启动本机 `sqlite3`，不需要 Docker。
  - SQLite 没有 `Fixture` 所需的某些能力（表注释、`SleepSQL`），用 `Fixture` 和 `Features` 跳过。`SleepSQL` 可用足够长的递归 CTE。
- `tunnel`：扩展现有内存 SSH 服务器的测试，覆盖 exec 通道、`sqlite3` 不存在和 `kill` 取消。
- 纯函数表格测试：
  - `quote` 解析器的黄金样例，包括 `unistr`、字面换行、`replace(...)` 三种文本形式、BLOB、大整数、重名列；
  - 语句完整性判断和点命令拦截；
  - 路径转义（空格、引号、`?`、`#`、`%`、`~/`）；
  - `sqlx.SQLite` 的词法、拆分和分类。
- 安全测试：可写和只读连接上的点命令、`edit`、`load_extension`（含 `"edit"(…)` 这类带引号写法和中间夹注释的写法），只读连接的写入、`writefile`、`ATTACH`，以及未闭合字符串不会挂起。
- 取消：事务中和事务外各一个，检查结果和回滚。
- 提前停止：事务中查询超过 `max_rows` 后，事务和未提交的修改都还在。
- BLOB：读取、行定位、INSERT 导出、数据比较写回后类型仍是 BLOB；文本 `x'00ff'` 仍是文本；表格拒绝编辑 BLOB 单元格。
- CI 确保 `sqlite3` 可用，缺失时安装。旧版本输出形式靠黄金样例覆盖，黄金样例要来自官方旧版本实际运行结果。
- UI 测试：表单字段显隐、树两层展开、状态页入口隐藏。

## 文档

- `CHANGELOG.md` 的 `[Unreleased]` 加一条。
- `README.md` 和 `README.zh-CN.md`：增加 SQLite 配置示例和限制说明（远程依赖 `sqlite3`、最低版本、Modify Table 范围、被拦截的函数、BLOB 单元格只读、断线时语句可能继续执行、无类型列的数字文本）。
- `CLAUDE.md` 架构部分同步提到 `db/sqlite` 和 `db.ExecFunc`。

## 留给实现者

- 引擎内部接口、`db.ExecFunc` 的精确签名、文件划分。
- 连接池的具体实现，只要满足上面的上限和空闲回收。
- 树的实现方式（见“树”）。
- 本地连接打开数据库时用不用 `database/sql` 的连接池，只要 `Conn` 并发安全、`Session` 独占连接。

## 已知风险

- **错误识别靠文本：** 不同版本的错误格式可能变化。解析不了的输出都按错误显示，不会被当作数据，也不会丢失。
- **远程性能：** 每次结果都走文本和 SSH，大表导出比本地慢。可以接受。
- **`sqlite3` 被替换或定制：** 比如发行版打了补丁的版本。启动时校验版本输出，不符合预期就明确报错。
