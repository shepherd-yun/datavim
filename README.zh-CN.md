# sqlvim

一个带 vim 键位、DataGrip 式工作流的终端 SQL 客户端，支持 MySQL、PostgreSQL、SQL Server 和 Oracle，也能浏览和查询 Redis。

[English](README.md)

![sqlvim 演示](docs/demo.gif)

- 浏览数据库，过滤、排序表数据，并**直接编辑**：改动先暂存，以 SQL 预览后在一个事务里提交。在树中展开表，查看列、键、外键和索引。
- 带语法高亮的 SQL 控制台，**内置 vim 编辑器**、自动补全，可交给 `$EDITOR` 编辑。
- `<Space><Space>` 搜索所有表、视图、列和函数；`<Space>f` 重新打开最近的 tab。
- 复制与导出结果（TSV、CSV、JSON、Markdown、INSERT、xlsx），SQL 历史。
- 把 CSV、TSV、xlsx 文件导入到表，或从文件推断结构新建表。
- SSH 隧道（agent、密钥、ProxyJump），生产环境保护，手动事务。
- 自定义按键（`keymap.toml`）与颜色（`[theme]`）。

## 安装

```sh
brew install shepherd-yun/tap/sqlvim
```

或用 Go（版本见 `go.mod`）：

```sh
go install github.com/shepherd-yun/sqlvim/cmd/sqlvim@latest
```

macOS 与 Linux 的二进制包在 [Releases 页面](https://github.com/shepherd-yun/sqlvim/releases)。

## 配置

创建 `~/.config/sqlvim/connections.toml`，参见 [docs/examples/connections.toml](docs/examples/connections.toml)。
首次连接时 sqlvim 会询问密码，并可保存到 macOS 钥匙串。

可选的 `~/.config/sqlvim/config.toml`（参见 [docs/examples/config.toml](docs/examples/config.toml)）：

```toml
page_size        = 200
max_column_width = 40
max_rows         = 1000          # rows fetched per console query
compare_max_rows = 100000        # rows a data compare reads per side
editor           = "nvim"        # <space>v; default $VISUAL, $EDITOR, nvim
export_dir       = "~/Downloads"
icons            = "nerd"        # tree icons: "nerd" (needs a Nerd Font) or "plain"
```

树使用 [Nerd Font](https://www.nerdfonts.com)（v3）图标：连接前显示 MySQL / PostgreSQL / Redis 的标志，
下面的库、schema、表、文件夹、键也各有图标。终端没有 Nerd Font 时会显示成方框，设置 `icons = "plain"` 即可。

还没有配置？启动 sqlvim 后按 `<space>n` 添加连接。

**SQL Server**（2014–2022，用户名密码登录）：`driver = "sqlserver"`，端口 1433。`params` 里的值原样传给驱动。
较老的服务器通常要放宽 TLS：`params = { encrypt = "disable" }`；自签名证书用 `TrustServerCertificate = "true"`；
只支持 TLS 1.0/1.1 的服务器加 `tlsmin = "1.0"`。连接表单（`<space>n`）里有对应的 `Encrypt` 和 `Trust cert` 字段。
不支持命名实例（`host\instance`），请直接填实例端口。SQL Server 上暂不支持修改表结构、schema 和数据对比、导入。

**Oracle**（12c 及以上，用户名密码登录，不需要 Instant Client）：`driver = "oracle"`，端口 1521，`database` 填 service name
（留空时为 `FREEPDB1`，表单里显示为 `Service`）。不支持 SID、TNS 名和 wallet。树里 schema 直接列在连接下面。
语句以 `;` 结尾；PL/SQL 块（`BEGIN`、`DECLARE`、`CREATE PROCEDURE` 等）和 SQL*Plus 一样，以单独一行的 `/` 结束。
语句默认自动提交；`<Space>t` 或 `SET TRANSACTION` 会保持事务直到 `COMMIT` 或 `ROLLBACK`。Oracle 上暂不支持修改表结构、对比和导入。

**Redis**（5.0 及以上，单机）：`driver = "redis"`，端口 6379，`database` 是默认的 db 编号
（留空为 0，表单里显示为 `DB`）。密码可选：sqlvim 先不带密码连接，服务器回 `NOAUTH` 时才询问。
`params`：`separator` 是 key 分组用的分隔符（默认 `:`），`tls = "true"`、`tls_skip_verify = "true"`。
展开 db 时用 `SCAN` 每批 1,000 个扫描 key（从不用 `KEYS`）：一次加载扫到 10,000 个就停，`↓ load more` 继续
（每个 db 最多 100,000 个），`Ctrl-c` 停止扫描，prod 连接每批之间会停顿一下。在 db、文件夹或 key 上按 `F`
按模式过滤（`user:*:sess`；不含 `*` `?` `[` 的文字匹配任意位置），找到 500 个或扫描满 100,000 个就停；`fx` 清除过滤。
`<Space>k` 按名字打开 key（`Tab` 补全已扫描到的 key）。在 key 上按 `Enter` 打开详情页：类型、TTL、长度、大小、编码，
然后是值——String（超过 1 MB 只显示前 1 MB；`v` 在 auto / text / JSON / hex 之间切换），或 Hash、List、Set、ZSet、
Stream 的字段、元素、成员，分页显示（`]` 加载更多，`F` 过滤 Hash 字段和 Set 成员，`K` 查看完整值）。
二进制 key 名显示成 `\xNN`；树上 `yy` 复制的是 redis-cli 能直接使用的 key 名。

Redis 控制台（在 Redis 连接、db 或 key 上按 `<Space>c`）一行一条命令，引号规则和 redis-cli 一样
（`SET "a b" "line\n"`、`'it\'s'`）；以 `#` 或 `//` 开头的行是注释。`Enter` 执行光标所在行或选中的行，`R` 执行全部，
`<Space>R` 从光标所在行执行到末尾；有一条失败就停下。结果显示成一个值（JSON 自动缩进，二进制显示为 hex）、
表格（数组；`HGETALL`、`CONFIG GET` 的 field / value，`ZRANGE … WITHSCORES` 的 member / score，`SCAN` 返回的 key）
或 redis-cli 那样的缩进文本；`M` 切换到消息日志，在表格单元格上按 `g d` 打开那个 key。控制台有自己的专用连接，
所以 `SELECT`（标签页标题显示当前 db）和 `MULTI` / `WATCH` 会一直有效；状态栏显示 `TX n` 或 `WATCH`，
关闭标签页或退出时会先询问。`Ctrl-c` 可以取消 `BLPOP q 0` 这类阻塞命令；取消会断开连接，下一条命令自动重连并回到原来的 db。
在 `MULTI` 或 `WATCH` 中不会自动重连，sqlvim 会提示事务已丢失。输入时补全命令、子命令和 key
（树里已扫描到的 key，再加一次小范围的 `SCAN`），并显示命令的参数提示。已知限制：重连后不恢复 `CLIENT SETNAME`
等其他连接状态；`MULTI` 里不能 `SELECT`；Redis 控制台不支持 `:e` / `:w`；不支持 `SUBSCRIBE`、`MONITOR`、`HELLO`、
`RESET`、`CLIENT TRACKING` 等命令。Redis 不支持结构、对比和导入。

Redis key 在详情页里像表数据一样编辑：`i` / `Enter` 改单元格（String 的值用单元格编辑框，值很长或含换行时用 `$EDITOR`；
`<Space>v` 总是打开 `$EDITOR`），`o` 加一个 field、元素（index 列写 `head` 或 `tail` 决定加在哪头）、成员或 Stream 条目，
`dd` 删除一行，`u` / `U` 撤销，`<Space>w` 先显示要做的检查和命令再提交。sqlvim 会 `WATCH` 这个 key，并检查修改所依赖的值
（field 的值、某个下标上的元素、成员的 score）是否仍和加载时一样；不一样，或者 `EXEC` 之前 key 被别人改了，就一条都不写。
Redis 不会回滚：`EXEC` 里某条命令失败时，它前后的命令仍然生效，sqlvim 会指出是哪一条。超过 1 MB（只加载了一部分）的 String
和二进制值不能在这里改，请用控制台。详情页上 `t` 设置或去掉 TTL，`r` 重命名（`RENAMENX`，目标已存在时先确认再覆盖），
`D` 删除（`UNLINK`）；树上对 key 按 `r` / `dd` 效果一样，`o` 新建 key，在文件夹或 db 上按 `dd` 删除匹配的 key（文件夹的前缀，
或 db 的过滤条件）：sqlvim 先数出数量、列出前 20 个，再每批 500 个删除，`Ctrl-c` 可以停止。删除过程中新写入的 key 可能删不到。

服务器页（在 Redis 连接上按 `<Space>i`，或节点菜单里的 `Server status`）用 `INFO` 显示服务器状态——版本、角色、运行时间、
客户端数、key 数、内存、命中率、淘汰和过期的 key 数——以及 ops/sec、内存、网络流量的 sparkline。每 2 秒采样一次，
而且只在状态分区显示在屏幕上时采样：切到别的标签页或分区就停止。`[` / `]` 切到慢查询和客户端列表（`<Space>r` 重新读取）；
在客户端列表里按 `dd` 确认后杀掉客户端。服务器 ACL 不允许的部分显示 `permission denied`。`*` 收藏光标处或当前页的 key，
`<Space>F` 列出收藏的 key，打开过的 key 会出现在 `<Space>f` 里。key 详情页上 `<Space>x…` 导出值（已加载的行或整个 key；
二进制值写成 `\xNN`）。在 db 或文件夹上按 `B` 找出每种类型最大的 key：它会遍历匹配 db 过滤条件或文件夹的所有 key
（每批 1,000 个，批与批之间会停顿，`Ctrl-c` 停止），所以生产实例上请在低峰时运行。

## 编辑表数据

改动会先暂存：改过的单元格变黄，新增行变绿，标记删除的行变红。
`<Space>w`（或 `:w`）显示将执行的语句；`y` 在一个事务里执行它们，任何一条失败或没有恰好改动一行都会整体回滚。
行按主键定位，没有主键时用全部列 NOT NULL 的唯一索引；两者都没有的表按所有列的值匹配
（MySQL 加 `LIMIT 1`，SQL Server 加 `TOP (1)`）或按 `ctid`（PostgreSQL）、`ROWID`（Oracle），预览里会注明。
只读连接拒绝改动，PROD 连接在预览中显示 `PROD ·`；有暂存改动时，翻页、刷新、过滤、排序、关闭 tab 或退出都会询问或拒绝。
控制台里单表 SELECT 的结果也能用同样的按键过滤和编辑。

## 服务器状态

在 MySQL、PostgreSQL、SQL Server 或 Oracle 连接上按 `<Space>i`（或在连接、数据库节点菜单里选 `Server status`）打开服务器页；
`[` / `]` 切换分区，`<Space>r` 重新读取当前分区。

- **Status** —— 版本、运行时间、角色、连接数和上限、缓存命中率等，以及负载的 sparkline（QPS / TPS、运行中的会话、
  每秒读写行数或网络流量）。每 2 秒采样一次，只在显示在屏幕上时采样。
- **Sessions** —— 谁连着、在跑什么、跑了多久、是否在事务里（默认隐藏空闲会话，`a` 显示；`*` 标出 sqlvim 自己的）。
  `c` 取消会话正在执行的查询，`dd` 断开会话：只读连接上拒绝，其他连接都要确认，prod 上要输入连接名；
  执行前 sqlvim 会核对这个 id 还是原来那个会话。SQL Server 不能只取消查询（只能断开），Oracle 18c 以前也不能。
- **Locks** —— 谁在等锁、谁持有锁，以及双方的语句。
- **Slow queries** —— 按总耗时排序的语句：MySQL 用 `performance_schema` 的摘要，PostgreSQL 用 `pg_stat_statements`（装了才有），
  SQL Server 用 `dm_exec_query_stats`，Oracle 用 `v$sql`。
- **Variables / Settings / Configuration / Parameters** —— 服务器参数；`F` 按名称过滤。
- **Replication** —— 副本和延迟、PostgreSQL 复制槽、可用性组、Data Guard。

账号没有权限读取的分区（MySQL 的 `PROCESS`、SQL Server 的 `VIEW SERVER STATE`、Oracle 的 `SELECT_CATALOG_ROLE`）
会说明缺什么，其他分区照常可用。显示的 SQL 里的密码（`IDENTIFIED BY`、`PASSWORD`）会被遮住。

## SSH 隧道

把 `ssh` 设为 `~/.ssh/config` 里的 `Host` 别名；此时 `host` 由 SSH 服务器解析：

```toml
[[connection]]
name     = "prod-pg"
driver   = "postgres"
host     = "10.0.1.5"   # as seen from the bastion
user     = "app"
database = "app"
env      = "prod"
ssh      = "bastion"
```

sqlvim 读取 `HostName`、`Port`、`User`、`IdentityFile`、`ProxyJump`（任意跳数）和 `UserKnownHostsFile`。
先尝试 ssh-agent，再尝试你的密钥文件，都不行时才询问密钥口令（口令只保存在内存里）。
未知主机会显示指纹，输入 `yes` 后加入 `known_hosts`；主机密钥变化视为错误。不支持 `ProxyCommand`。

## 工作空间

工作空间是一组独立的连接，比如一个放工作数据库，一个放个人数据库。每个空间有自己的
`connections.toml`、钥匙串密码、历史和控制台草稿；设置、主题和按键在所有空间共享。

```sh
sqlvim -w personal --create   # 第一次先创建
sqlvim -w personal            # 或：SQLVIM_WORKSPACE=personal sqlvim
sqlvim --list-workspaces
```

不加 `-w` 时进入 `default` 空间，也就是一直以来用的那些文件。当前空间名会显示在状态栏和窗口标题里。

| | default | `personal` |
|---|---|---|
| 连接 | `~/.config/sqlvim/connections.toml` | `~/.config/sqlvim/workspaces/personal/connections.toml` |
| 历史、草稿 | `~/.local/state/sqlvim/state.db` | `~/.local/state/sqlvim/workspaces/personal/state.db` |
| 钥匙串账户 | `<连接名>` | `personal/<连接名>` |

要把连接挪到某个空间：先创建空间，把对应的 `[[connection]]` 段落剪切到它的
`connections.toml`，首次连接时重新输入一次密码。这些连接的历史留在默认空间。

配合 [direnv](https://direnv.net)，在项目的 `.envrc` 里写 `export SQLVIM_WORKSPACE=work`，就能按目录自动选空间。

## 按键

| 按键 | 动作 |
|---|---|
| `j` `k` `h` `l`, `gg` `G`, `Ctrl-d` `Ctrl-u` | 移动 |
| `l` / `→` | 展开节点；表或视图展开为 Columns、Keys、Foreign Keys、Indexes（对断开的连接重试） |
| `Enter` | 打开表数据；在列、键、索引上打开结构页并定位到它，在外键上跳到被引用的表 |
| `zo` `zc` `za` | 展开 / 折叠（在列上折叠所在分组）/ 切换 |
| `zO` `zC` | 展开光标下所有已加载的节点 / 折叠光标下的全部节点 |
| `zR` `zM`（或 `Z`）`zx` | 展开所有已加载的节点 / 全部折叠 / 只保留到光标的路径 |
| `h` | 折叠 / 回到父节点 |
| `Tab`, `Ctrl-h` `Ctrl-l` | 切换面板 |
| `1`–`9` | 跳到第 n 个 tab（控制台编辑器里数字是计数：用 `gt` / `gT` / `{n}gt`） |
| `H` `L`, `gT` `gt` | 上一个 / 下一个 tab，首尾循环（任何面板或 tab 中都可用） |
| `/` `n` `N` | 在树或结果表格中搜索 |
| `[` `]` | 上一页 / 下一页（数据）、上一节 / 下一节（结构）；`]` 在控制台加载更多行 |
| `0` / `$` | 表格：第一列 / 最后一列 |
| `zh` `zl` / `zH` `zL` | 表格横向滚动半屏 / 一列 |
| `K` | 显示单元格完整内容 |
| `ff` / `f!` | 过滤：该列等于 / 不等于单元格的值（NULL → `IS NULL` / `IS NOT NULL`） |
| `fs` 或 `f~` | 过滤：该列包含……（会询问，初始值为单元格；忽略大小写；`%` `_` 按字面匹配） |
| `fl` | 过滤：该列 LIKE 你输入的模式（`%` 任意文本，`_` 一个字符；初始为 `%cell%`；忽略大小写） |
| `f>` / `f<` | 过滤：该列大于 / 小于……（会询问，初始值为单元格） |
| `fn` / `fN` | 过滤：该列为 / 不为 NULL |
| `fx` | 清除过滤 |
| `F` | 以 SQL 编辑过滤条件（`WHERE …`）：从当前列开始，`Tab` 补全列名和关键字 |
| `s` | 表数据：按该列排序——升序、降序、取消 |
| `i` / `Enter` | 表数据：在 vim 编辑器中编辑单元格（`Enter` 保存，`Ctrl-n` 设为 NULL，`Esc` 回到 NORMAL / 取消） |
| `o` / `dd` | 表数据：在下方新增一行 / 标记该行删除 |
| `u` / `U` | 表数据：撤销上一处改动 / 放弃全部改动 |
| `<Space>w`, `:w` | 表数据：以 SQL 预览改动，`y` 在一个事务里提交 |
| `yl` / `yy` | 复制单元格 / 行 |
| `yy` / `yq` | 树：复制连接、数据库、schema 或表的名字 / 按 SQL 写法限定的全名（`app.users`） |
| `yy` | 结构页：复制光标处列、索引或外键的名字（在 DDL 页复制整段 DDL） |
| `p` | 重新输入连接密码（树） |
| `<Space>c` | 为选中的连接 / 数据库打开 SQL 控制台 |
| `Enter` / `R` | 控制台：执行光标处语句或可视选区 / 执行全部（插入模式下 `Ctrl-Enter` 或 `Ctrl-j`） |
| `<Space>R` | 控制台：从光标所在语句执行到末尾 |
| `Ctrl-c` | 取消正在执行的查询（从不退出） |
| `Ctrl-j` / `Ctrl-k` | 控制台：切到结果 / 编辑器；`M` 切换消息日志 |
| `<Space>v` | 在 `$EDITOR` 中编辑控制台内容 |
| `<Space>h` | SQL 历史 |
| `<Space>y t/c/j/m/i` | 复制结果为 TSV / CSV / JSON / Markdown / INSERT；在结果表格里也可直接按 `y t/c/j/m/i` |
| `<Space>yd` | 复制光标所在表的 DDL（树、数据 tab 或结构 tab，这些地方也可直接按 `yd`）；在函数定义弹窗里按 `y` 复制其定义 |
| `<Space>x c/j/s/x` | 导出结果为 CSV / JSON / SQL / xlsx |
| `I` | 树：把 CSV / TSV / xlsx 文件导入到这张表；在库或 schema 上则新建表 |
| `x` / `a` `A` / `i` / `t` | Import tab：导入或跳过一列 / 全选 / 全不选 / 选目标列（新表：改列名）/ 新表：改类型；`[` `]` 切换区域，`<Space>w` 校验并导入 |
| `<Space>n` | 新建连接 |
| `e` | 树：编辑选中的连接（保存到 `connections.toml`），或修改选中的表；在列或索引上，光标停在该行 |
| `e` | 结构页：修改表——`i` 编辑字段（`h` / `l` 选择字段），`o` 新增列或索引，`dd` 删除，`u` / `U` 撤销，`<Space>w` 预览并执行 ALTER |
| `dd` | 树：删除选中的连接，或删除选中的表 / 视图（需输入名字确认）；在列或索引上，在 Modify Table 中暂存删除 |
| `r` | 树：重命名选中的表或视图 |
| `T` | 树：清空选中的表（需输入表名确认） |
| `a` | 树：当前节点的动作菜单；按条目对应的键直接执行（与树里的键相同） |
| `m` / `C` | 树：把表、MySQL 库或 PostgreSQL schema 标记为对比源 / 与当前节点对比（没有标记时从已加载的元数据里选源） |
| `x` / `]c` `[c` / `~` | Diff 标签页：变更纳入或不纳入脚本 / 下一处、上一处变更 / 交换源与目标 |
| `<Space>w` | Diff 标签页：把勾选的变更写成脚本，放进目标连接的新 console（在那里运行才会执行） |
| `D` / `F` | Diff 标签页：按键列对比两张表的数据，或回到结构 / 数据视图里设置两边共用的 WHERE |
| `<Space>s` | 列弹窗：输入即过滤，`Enter` 让数据 tab 的光标跳到该列（必要时先打开 tab），`Ctrl-o` 打开完整结构（索引、外键、DDL）。在控制台中列出结果的列，并移动结果光标 |
| `<Space>S` | 直接打开光标所在表的完整结构，不经过列弹窗 |
| `<Space>e` | 显示 / 隐藏树 |
| `+` / `=` | 全屏：当前焦点所在的面板占满屏幕（在控制台中是编辑器或结果）。切换焦点时全屏会跟着走，切换 tab 时保持全屏 |
| `-` / `_` | 退出全屏 |
| `<Space>l` | 在树中定位当前 tab：表，或 console 所在的数据库 / schema（也可用 `:locate`） |
| `F` / `fx` | 树里 Redis 的 db、文件夹或 key 上：按模式过滤 key / 清除过滤 |
| `<Space>k` | 按名字打开 Redis key |
| `v` | Redis key 详情页：String 按 auto / text / JSON / hex 显示 |
| `<Space>v` | Redis key 详情页：用 `$EDITOR` 编辑光标处的值 |
| `t` / `r` / `D` | Redis key 详情页：设置或去掉 TTL / 重命名 / 删除 key |
| `o` | 树里 Redis 的 db、文件夹或 key 上：新建 key |
| `g d` | Redis 控制台结果：打开单元格里的 key |
| `<Space>i` | 服务器页：状态、会话、锁等（Redis：状态、慢查询、客户端） |
| `[` / `]` | 服务器页：上一个 / 下一个分区 |
| `dd` | 服务器页：断开会话 / 杀掉 Redis 客户端 |
| `c` | SQL 服务器页的会话列表：取消会话正在执行的查询 |
| `a` | SQL 服务器页的会话列表：显示 / 隐藏空闲会话 |
| `F` / `fx` | SQL 服务器页的参数列表：按名称过滤 / 清除过滤 |
| `*` | 树或 Redis key 详情页：收藏 / 取消收藏 key |
| `<Space>F` | 收藏的 Redis key |
| `B` | 树里 Redis 的 db 或文件夹上：找出最大的 key（`Enter` 打开） |
| `<Space>r` | 刷新树节点或 tab，并重新加载该连接的元数据 |
| `?` | 当前面板的帮助 |
| `:q` / `:qa` | 关闭 tab / 退出 |
| `:e <file>` | 在当前上下文的连接 / 数据库上，用控制台打开 `.sql` 文件 |
| `:w` / `:w <file>` / `:wq` | 文件控制台：保存 / 另存为（普通控制台会变成文件控制台）/ 保存并关闭；`:w!` 覆盖磁盘上已被修改的文件 |
| `<Space>t` | 控制台：切换手动提交（状态栏显示 `TX n`） |
| `:commit` / `:rollback` | 控制台：结束事务 |
| `:set noreadonly` / `:set readonly` | 本次会话解锁 / 锁定连接 |
| `:q!` / `:qa!` | 关闭 / 退出，不询问未结束的事务 |
| `<Space><Space>` | 搜索表、视图、列、函数、schema 和数据库，范围是所有连接，并加载尚未打开的连接的元数据（可以用 `db.table.column`、`table.column` 或其中一段按上级模糊限定，打开列时光标直接落在该列；`@` 开头只搜当前上下文的连接；`Enter` 打开，`Ctrl-o` 打开结构）。`Tab` / `Shift-Tab` 切换到搜索已打开 console 的内容（`Enter` 跳到那一行）或 SQL 历史（查询为空时列出最近的语句，`Enter` 像 `<Space>h` 一样插入语句） |
| `<Space>f` | 最近打开的表、结构、控制台、文件和 Redis key |
| `↓` / `↑`, `Ctrl-n` / `Ctrl-p`, `Ctrl-j` / `Ctrl-k`, `Tab` / `Shift-Tab` | 控制台插入模式：下一个 / 上一个补全（弹窗关闭时 `Ctrl-n` / `Ctrl-p` 打开它） |
| `Enter` / `Ctrl-y` | 控制台插入模式：接受补全（`Esc` 关闭弹窗，再按一次 `Esc` 离开插入模式） |

`f…` 生成的过滤条件以 `AND` 叠加；`F` 显示并编辑完整条件。文本输入框支持 readline 键：
`←` `→`、`Home` `End` / `Ctrl-a` `Ctrl-e`、`Alt-b` `Alt-f`、`Ctrl-w`、`Ctrl-u`、`Ctrl-k`。

按下 `<Space>` 稍等即可看到所有 leader 键。以上是默认按键，见「自定义」。

## 自定义

**按键** —— `~/.config/sqlvim/keymap.toml` 可以更换 leader，并按上下文绑定、改绑或解绑按键
（[包含全部动作的示例](docs/examples/keymap.toml)）：

```toml
leader = ","

[grid]
"S" = "sort.column"
"s" = ""
```

sqlvim 启动时检查该文件，指出未知或冲突的绑定。帮助（`?`）、which-key 和各处提示都会跟随你的按键。
控制台编辑器的 vim 键、文本输入框和弹窗列表的按键保持不变。

**颜色** —— `config.toml` 中的 `[theme]` 表；值为 ANSI 色号 `"0"`–`"255"` 或 `"#rrggbb"`。
槽位：`border` `focus` `error` `accent` `on_error` `visual_bg` `visual_fg`
`changed` `inserted` `view` `builtin` `string` `number` `operator`，以及 Redis 类型徽标
`key_string` `key_hash` `key_list` `key_set` `key_zset` `key_stream` `key_other`。

```toml
[theme]
focus  = "#7aa2f7"
accent = "14"
```

**Ghostty** —— 用 [docs/examples/ghostty.conf](docs/examples/ghostty.conf) 把 Cmd 快捷键映射到 leader 键，
例如 `keybind = cmd+e=text:\x20e`。

## 控制台编辑器

控制台是一个 vim 编辑器，打开时处于 INSERT 模式，可以直接输入（`Esc` 进入 NORMAL 模式，使用 leader 键和切换 tab）：
NORMAL、INSERT、VISUAL（`v`）和 V-LINE（`V`）模式；
移动 `h j k l w b e W B E 0 ^ $ gg G f F t T % { }` 与 `Ctrl-d` `Ctrl-u`；操作符
`d c y > <` 搭配移动、文本对象（`iw aw i" a" i' a' i( a( i[ a[ i{ a{`）或重复一次
（`dd` `cc` `yy` `>>` `<<`）；`x D C p P o O i a I A`；`u` / `Ctrl-r` 撤销与重做；`.`
重复上一次修改；计数（`3dd`、`2dw`）；寄存器 `"a`–`"z`，`"+` 为系统剪贴板。
不带寄存器的 `y` 也会复制到系统剪贴板，`x`、`d`、`c` 则不会覆盖它；`"+p` 从系统剪贴板粘贴。编辑器里数字是计数，
所以用 `gt` / `gT` / `{n}gt` 或 `H` / `L` 切换 tab。VISUAL 模式下 `Enter` 执行选中的语句。
`>` 和 `<` 缩进四个空格；INSERT 模式下 `Tab` 输入四个空格，`Ctrl-a` / `Ctrl-e` 移到行首 / 行尾。
VISUAL 模式下 `<Space><Space>` 用选中的文本搜索。

不支持：宏、ex 范围、窗口、`/` 搜索——需要时用 `<Space>v` 在 `$EDITOR` 中打开控制台。

## 搜索与补全

sqlvim 在你第一次使用某个连接时于后台读取其目录（表、视图、列、函数）——MySQL 读整个服务器，
PostgreSQL 每次一个数据库——并保存在内存中。`<Space>r` 重新加载；在控制台执行 DDL 会自动重新加载。

在控制台中，补全随输入弹出：`FROM`/`JOIN`/`UPDATE`/`INTO` 之后是表，语句中各表（及 `alias.`）的列，
其他位置是关键字。输入空格会弹出接下来常用的内容，默认不选中（`Tab` 或 `↓` 选择，`Enter` 仍然换行）：
`SELECT` 之后是 `*` 和列，选择列表之后是 `FROM`，表之后是 `WHERE` / `JOIN` / `ORDER BY` 等，
条件里的列之后是运算符，条件之后是 `AND` / `OR`。继续输入会缩小范围，合适的词排在其他关键字前面。需要引号的名字（`order`，PostgreSQL 上的 `MixedCase`）会带引号插入。
语句因语法错误失败时，光标会跳到服务器报告的出错位置。

## 安全

- `readonly = true`（`env = "prod"` 时默认开启）在控制台中阻止 INSERT/UPDATE/DELETE 和 DDL，
  数据库会话本身也设为只读，作为第二道防线。`:set noreadonly` 在 sqlvim 退出前解锁该连接；
  不会修改 `connections.toml`。要长期修改，在新建 / 编辑连接表单里设置 `Read-only`（`auto` 跟随 `env`）。
- SQL Server 和 Oracle 没有只读会话，所以只有 sqlvim 这一道防线，规则也更严：它检查一个批次里的每条语句（T-SQL 可以不写分号），
  认不出是否安全的一律拦截——除少数只读系统过程（`sp_help`、`sp_helptext`、`sp_columns`、`sp_who` 等）以外的 `EXEC`、
  `IF` 块、`DBCC`、PL/SQL 块、`CALL` 等（允许 `ALTER SESSION SET CURRENT_SCHEMA` / `NLS_…`）。生产库建议再配一个数据库层面只读的账号。
- Redis 同样没有只读会话：浏览只发送读命令，控制台里的只读完全由 sqlvim 在客户端保证——拒绝写命令、危险命令和
  认不出的命令（包括模块命令）。prod 上写命令和认不出的命令要按 `y` 确认；危险命令（`FLUSHDB`、`FLUSHALL`、`CONFIG SET`、
  `CLIENT KILL`、`SHUTDOWN`、`DEBUG` 等）在任何连接上都要确认，prod 上要输入连接名；db 里超过 10,000 个 key 时拒绝 `KEYS`
  （请用 `SCAN` 或树的过滤）。多行一起执行时先检查全部再执行，所以 `MULTI` 绕不过这些规则。
  在详情页和树上做的编辑、TTL、重命名和删除同样要过这些检查。生产实例建议再配一个只读的 ACL 用户（`+@read`）。
- `env = "prod"` 用红色框住 tab 区域，并在状态栏显示 `[PROD]`；prod 连接上的每条写入或 DDL 语句都会先要求按 `y` 确认。
- 不带 `WHERE` 的 `UPDATE` / `DELETE` 需要先输入表名才会执行。
- 手动提交模式（`<Space>t`）下，退出或关闭有未结束事务的控制台会先询问。若事务期间连接断开，
  sqlvim 会提示事务已丢失，而不是悄悄重连。请用 `<Space>t` 而不是 `SET autocommit = 0`。
- 导入在一个事务里完成，只要有一行失败就什么都不写；MySQL 上导入新建的表会被删掉。

## 开发

```sh
make test               # unit tests
make test-integration   # needs Docker (OrbStack: `orb start`)
make lint
make snapshot           # build release archives locally with GoReleaser
make demo               # re-record docs/demo.gif (needs vhs and Docker)
```

日志：`~/.local/state/sqlvim/sqlvim.log`（`--debug` 输出更多细节）。
提交 pull request 前请阅读 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可证

MIT —— 见 [LICENSE](LICENSE)。
