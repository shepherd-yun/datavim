# Redis 支持 设计文档

- 日期：2026-10-16
- 状态：R1~R4 已实现
- 范围：新增 `redis` 连接类型，分 4 个迭代交付（R1 浏览查看、R2 命令控制台、R3 编辑、R4 运维）；修订主设计 §3（架构）、§5.3（安全规则）

## 1. 背景

作者日常除了查 MySQL / PostgreSQL，还要查 Redis。现在查 Redis 得打开图形客户端（作者用过并认可 Redisee）。目标是在 sqlvim 里统一起来：查库、查 Redis 都在同一个终端工具里，用同一套 vim 键位，不再切到图形软件。

作者在 Redis 上主要做三件事：

1. **按名字查某个 key**：已知 key 名，直接看值。
2. **模糊搜索**：按模式（如 `user:*:session`）找出一批 key，再挑一个看。
3. **执行命令**：像 redis-cli 一样敲 `HGETALL`、`TTL`、`DEL`。

使用环境：以**单机**为主，**有生产实例，key 量很大**（几十万到几百万）。都是公司内网的**自建 Redis**（不是云厂商的托管或代理版），认证方式是**只有密码**（没有 ACL 用户名）。比如测试环境是 `172.16.96.126:6379`，加密码。具体版本还没确认（作者不在内网），见 §3.2 的版本策略。

参考：Redisee 的布局是左侧按 `:` 分组的键树（文件夹带 key 数、db 切换器带 key 数、"条件扫描/筛选"输入框、类型徽标、二进制 key 转义显示、底部有收藏 / 历史页签），右侧是多标签页（key 详情、状态面板）。sqlvim 的左树右标签页结构和它一致，可以直接沿用。

## 2. 目标

- Redis 连接和 MySQL / PG 连接在**同一个连接列表、同一棵树**里，共用连接表单、钥匙串密码、SSH 隧道、workspace、`env = "prod"` 标记和只读开关。
- 浏览、搜索、查看 key 时，在几百万 key 的生产实例上也**不阻塞服务器、不卡住界面**。
- 现有安全规则在 Redis 上有对应的规则（§7）：只读、prod 确认、危险命令确认、编辑必须正好作用于预期的值。
- 键位风格和现有一致：所有新按键进 `keymap/defaults.go`，带描述，which-key / help / hints 自动生成；UI 文字里不写死按键名（用 `keyHint`）。
- 现有 SQL 引擎的行为不变。

**不做（整个 Redis 系列）：**
- Redis Cluster、Sentinel。这是我的提议，作者确认以单机为主。连接层要留出扩展余地，但不实现。
- Pub/Sub、`MONITOR`、客户端缓存（`CLIENT TRACKING`）、Lua 调试。
- Redis 模块的专用视图（RedisJSON、RediSearch、TimeSeries、Bloom）。这些 key 的类型会显示成模块类型名，只能通过控制台操作。
- 值的自定义解码器（protobuf、msgpack、Java 序列化），以及 gzip 自动解压。R1 只提供 text / JSON / hex 几种显示。
- Redis 出现在全局搜索（`<Space>f`）或 meta catalog 里。Redis 的 key 只能通过它自己的扫描搜索。
- 跨 Redis 和 SQL 的联合查询。

**反目标**（技术上能用，但这样就算失败）：
- 在生产实例上执行了 `KEYS *`，或者浏览、搜索时做了一次性的全量遍历，导致 Redis 卡顿。
- 二进制或含特殊字符的 key 名转义显示后，用转义后的字符串去操作，结果操作了另一个 key，或者报 key 不存在。
- 只读或 prod 连接上，通过 `EVAL`、`FCALL`、`MULTI` 包装、或者名字没收录的命令绕过拦截，执行了写操作。
- 打开一个几百 MB 的大 String，或者几百万元素的 Hash，导致 sqlvim 卡死或内存爆掉。
- 控制台 `SELECT 3` 之后，连接被悄悄重连回 db0，用户以为还在 db3 继续执行写命令。

## 3. 架构

### 3.1 不复用 `db.Conn`

`db.Conn`、`MetaReader`、`Dialect` 都是按 SQL 设计的（表、列、DDL、方言）。Redis 没有这些概念，强行实现会让两边都变别扭。所以 Redis 走一条平行的路：

```
ui ──→ session ──→ db   ──→ mysql / postgres / sqlserver / oracle
  │          └───→ rdb  ──→ go-redis
  └──→ rcmd（纯包）
```

- **`internal/rdb`**（新）：Redis 和 sqlvim 之间唯一的接口，地位相当于 `internal/db`。UI 不直接引用 go-redis。包名是我起的，避免和 go-redis 的包名 `redis` 冲突。
- **`internal/rcmd`**（新，纯包，不碰终端和网络）：命令行切分（像 redis-cli 一样处理引号和转义）、命令分级（读 / 写 / 危险 / 不支持）、补全上下文、key 模式处理（普通文本 → glob）、事务状态（`MULTI` / `EXEC` / `DISCARD`）。地位相当于 `sqlx`。
- 依赖方向不变：`ui` → `session` / `rcmd` → `rdb` → go-redis。

### 3.2 客户端库

`github.com/redis/go-redis/v9`（我的选择：官方库，纯 Go）。实现时用 Context7 查当前文档。已经确认过的点：

- `Options.Dialer` 的签名是 `func(ctx, network, addr) (net.Conn, error)`，和 `db.DialFunc` 一致，SSH 隧道可以直接注入，不监听本地端口。
- `Options.DB` 对整个连接池固定。`Do` 会把命令发到池里任意一条空闲连接，所以 **`SELECT`、`MULTI`、`CLIENT SETNAME` 这类改变连接状态的命令，不能在连接池上执行**，必须用 `client.Conn()` 拿一条专用连接。这决定了 §3.3 的结构。
- 支持 RESP3（`Protocol: 3`）。控制台用 RESP3，这样能区分 map、set、double、null 等结果类型。服务器只支持 RESP2（Redis 5 及以下）时，退回 RESP2。

**版本策略**（我的决定，因为作者暂时无法确认服务器版本）：主要目标是 **Redis 6.0+**，同时 **5.x 也要能用**，只需要下面几处小的兼容分支。连接时从 `INFO server` 读到 `redis_version` 并记下来：

| 6.0+ 的功能 | 5.x 的退路 |
|---|---|
| `SCAN … TYPE`（类型徽标） | SCAN 不带 TYPE，再用 pipeline 批量 `TYPE` |
| `SET … KEEPTTL`（R3 改 String） | 在 `MULTI` 里 `SET` 后用 `PEXPIRE` 恢复原来的 TTL |
| RESP3 | RESP2（§5.3 里成对数组的命令按已知命令表处理） |
| ACL 用户名 | 不填用户名，只用密码 |

低于 5.0 的版本不保证支持。服务器不是原生 Redis（Valkey、KeyDB 等）时，按 `redis_version` 的兼容级别处理，不做特别适配。

### 3.3 `rdb` 的接口（草案，实现者可以调整）

结构和 `db` 一一对应：

| `db` | `rdb` | 说明 |
|---|---|---|
| `Driver` | `rdb.Open(ctx, cfg)` | 只有一种引擎，不需要 Driver 注册 |
| `Conn`（池化，并发安全） | `rdb.Client` | 固定一个 db 编号的连接池，供浏览、查看、编辑使用 |
| `Session`（专用连接） | `rdb.Session` | 控制台专用，`SELECT` / `MULTI` 在上面持续有效 |
| `Database` 字段、`DatabaseScoped() == true` | db 编号 | 一个 `(连接, db 编号)` 对应一个 `Client`，复用 `session.Manager` 的 `connKey{conn, database}` |

`Client` 至少要提供：

- `Info(ctx, sections...)`：解析成 map，供 db 列表、状态页使用。
- `DBSize`、`Databases(ctx)`：返回 db 编号和各库 key 数。先用 `CONFIG GET databases` 拿库的数量；如果被 ACL 禁止，或者是托管服务不允许，就退回 `INFO keyspace` 里有 key 的库，再加上配置里的默认库。
- `Scan(ctx, cursor, match, count, type)`：一次 SCAN 调用，由上层控制节奏。
- `KeyInfo(ctx, key)`：一次往返（pipeline）拿到 `TYPE`、`PTTL`、长度（按类型选 `STRLEN` / `HLEN` / `LLEN` / `SCARD` / `ZCARD` / `XLEN`）、`OBJECT ENCODING`、`MEMORY USAGE`（可选，失败就不显示）。
- 各类型的分页读取：`GETRANGE`、`HSCAN`、`LRANGE`、`SSCAN`、`ZRANGE … WITHSCORES`（按下标）、`XRANGE … COUNT`。
- `Do(ctx, args ...[]byte)`：控制台用，返回结构化的结果值（§5.3）。
- R3 起需要的写操作（§6）。

**key 名和值一律用 `[]byte` / `string` 原样传递，不做任何编码转换。** 转义只在显示时进行（§4.4）。

错误：连接断开的错误要包装成可识别的哨兵错误（相当于 `db.ErrConnLost`）。`ctx` 取消时，正在执行的命令要停下来。go-redis 取消时会关闭那条连接，所以 Session 被取消后**状态会丢失**，处理见 §5.4。

### 3.4 配置和会话层

`config.Connection` 不改结构，只增加 `driver = "redis"`：

| 字段 | Redis 的含义 |
|---|---|
| `host` / `port` | 默认 `localhost` / `6379`（加进 `defaultPorts`） |
| `user` | ACL 用户名（Redis 6+），可以留空 |
| `database` | 默认 db 编号，留空就是 `0`；必须是非负整数，`Normalize` 负责校验 |
| `ssh` | 和现有一样 |
| `params` | `separator`（键树分隔符，默认 `:`）、`tls = "true"`、`tls_skip_verify = "true"` |

- 连接表单：驱动选 redis 时，`Database` 标签显示成 `DB`，`User` 显示为可选。分隔符和 TLS 是否放进表单由实现者决定，至少要能在 `connections.toml` 的 `params` 里配。
- **无密码的 Redis 很常见。** 现在 `session.Manager` 在钥匙串里找不到密码时，会返回 `ErrPasswordRequired` 并弹出输入框。Redis 必须做到"没设密码就直接连，服务器回 `NOAUTH` 再问密码"，不能每次打开都弹框。具体怎么实现由实现者决定，比如先无密码尝试，或者在表单里记一个"无密码"标记。
- `session.Manager` 增加 `Redis(ctx, name string, db int) (rdb.Client, error)`，`ui.Backend` 增加同名方法。密码、隧道、`Reset`、`SetReadOnly` 走现有逻辑。`Manager` 里 `drivers` 只装 SQL 驱动，判断"驱动是否可用"的地方要认识 `redis`。
- 测试配置和 `Makefile` 的 `test-integration` 要把 `./internal/rdb/...` 加进去。

## 4. R1：浏览与查看（只读）

R1 交付后，作者就可以不再打开图形客户端，完成"查某个 key"和"模糊搜索"。

### 4.1 树

```
▾ cache-prod  [prod]
  ▾ db0  192,569
    ▸ user:      8,123
    ▸ sess:      ≥10,000
      config:app             [S]
    ↓ load more (scanned 10,000 / 192,569)
  ▸ db1  44
  / user:*:sess            ← 当前过滤条件
```

- 连接展开后显示 db 列表，每个库带 `DBSIZE`。没有 key 的库放在后面或者折叠，由实现者决定；配置里的默认库总是显示。
- db 展开后开始 **SCAN**：每批 `COUNT 1000`（可以调），一次加载最多扫到一个上限（默认 10,000 个 key，实现者可以放进 `config.toml`）。达到上限后停下，在末尾显示 "load more" 节点，并写明已扫描多少 / 总共多少。扫描在后台 `tea.Cmd` 里进行，可以用取消键（和控制台取消同一个动作）停止，每批结果到达就刷新树。
- 扫到的 key 按分隔符分组成文件夹。文件夹显示已扫到的 key 数；扫描没完成时显示 `≥N`。只有一个子节点的文件夹可以合并显示（实现者决定）。
- 每个 key 前面有类型徽标（S / H / L / Z / T(set) / X(stream)，具体字母和颜色由实现者决定，颜色放进 `ui/theme` 的命名槽）。类型来自 `SCAN … TYPE` 或者在扫描后用 pipeline 批量 `TYPE`；如果后者开销太大，可以等节点可见时再懒加载。实现者要量一下，在几十万 key 时界面要保持流畅。
- 树里的 key 节点数量必须有上限，不能把几百万个 key 全部塞进树。规则是（我的决定）：每次加载（展开或 "load more"）**最多扫描** 10,000 个 key，扫完就停。"load more" 会继续往树里追加，但整棵 db 子树最多保留 100,000 个 key（总上限）。到达总上限后，"load more" 换成提示 `too many keys; filter with <filter 键>`，不再加载，也不丢弃已经加载的 key。文件夹的 `≥N` 表示"目前扫到的数量"。key 补全（§5.2）使用同一份缓存，所以同样受这个上限约束。

### 4.2 搜索和直接打开

- **过滤**：在 db 节点或它的子节点上，用 `FilterPrompt` 这个 Action（数据表格里默认绑 `F`）在 Tree 上下文里输入模式（我的决定）。树上的 `/` 仍然是现有的"在已加载节点里查找跳转"，不改成过滤，以免同一个键在 SQL 树和 Redis 树上含义不同。服务端用 `SCAN MATCH`，从头重新扫描。
  - 过滤时，一次加载的停止条件是（我的决定）：**找到 500 个匹配**，或者**扫描满 100,000 个 key**，二者先到为准。之后用 "load more" 继续。上限按"扫描数"而不是"匹配数"计，避免一个罕见的模式变成一次全量遍历。prod 连接上每批 SCAN 之间停顿一小段时间（比如 10 ms，实现者可以调），把对服务器的压力摊开。
  - 输入里含有 glob 字符（`* ? [`）时原样当模式用；不含时变成 `*输入*`（我的决定：和"模糊搜索"的直觉一致）。
  - 注意 `SCAN MATCH` 是在服务器遍历后再过滤的：在几百万 key 里搜一个罕见的模式，可能扫很多批才找到几个。所以界面要显示扫描进度（已扫描 / 总数，以及已找到的数量），并且允许中途停止。
  - 过滤条件显示在树里（见 §4.1 草图），清除过滤就回到普通浏览。
- **直接打开 key**：一个动作，弹出输入框，输入完整 key 名直接打开详情页，不经过扫描。key 不存在时提示 `key not found`。输入框里可以用 Tab 补全已扫描过的 key 名。
- `SCAN` 的游标语义保证同一个 key 可能返回多次。合并到树时要去重。

### 4.3 Key 详情页（新标签页类型）

在 key 节点上回车打开。标题是 key 名（转义后），多次打开同一个 key 时复用已有标签页（用 `tab.Key()`）。

头部一行：`类型 · TTL · 长度 · 大小 · 编码`，比如 `hash · TTL 3593s · 12 fields · 1.2 KB · listpack`。TTL 为 -1 时显示 `no expiry`。key 被删除或过期后，刷新时显示 `key no longer exists`，不报错。刷新是手动的（一个动作），R1 不做自动刷新；TTL 可以在客户端按秒倒数显示（实现者决定）。

各类型的展示：

| 类型 | 展示方式 | 加载方式 |
|---|---|---|
| String | 值的查看器（§4.4） | 先看 `STRLEN`。不超过 1 MB 时整个读取；超过时只读前 1 MB（`GETRANGE`），并提示 `showing first 1 MB of N`。阈值可以配置 |
| Hash | 表格：field / value | `HSCAN` 分页；可以按 field 过滤（`HSCAN MATCH`） |
| List | 表格：index / value | `LRANGE` 按页取 |
| Set | 表格：member | `SSCAN` 分页；可以过滤 |
| ZSet | 表格：rank / member / score | `ZRANGE … WITHSCORES` 按下标分页；R1 只按 rank 顺序 |
| Stream | 表格：ID / 字段（展开成 `k=v …`） | `XRANGE - + COUNT n` 分页；消费组信息不做 |
| 其他（模块类型） | 显示类型名和"在控制台里操作" | 不读值 |

表格复用 `panes/grid`。分页的手感和数据表格一致（翻到底自动加载下一页，或者用现有的翻页动作）。表格里的单元格可以用现有的详情 / 复制动作，查看长值或 JSON。每页大小沿用 `page_size`。

### 4.4 值的显示和二进制

- **显示格式**：`auto`（默认）/ `text` / `json` / `hex`，可以用一个动作循环切换。
  - `auto`：合法 UTF-8 且能解析成 JSON 对象或数组时，格式化显示（缩进加高亮，复用现有的 `highlight` 包或者 JSON 词法）；合法 UTF-8 但不是 JSON 时按文本显示；不是合法 UTF-8 时按 hex 显示。
  - 判断只针对已加载的部分（大值只看前 1 MB）。
- **key 名的转义**：key 名里的不可打印字节和非法 UTF-8 显示成 `\xNN`，和 Redisee、redis-cli 一致。树、标签页标题、表格都用同一个转义函数（放在 `rcmd`）。**每个节点、每个标签页都保存原始字节**，所有命令都用原始字节；转义后的字符串只用来显示（见反目标）。
- 复制 key 名时复制的是什么：默认复制 redis-cli 能直接粘贴的形式（需要时加引号和转义）。我的决定，理由是复制出来通常要贴进控制台或终端。

### 4.5 只读

R1 没有写操作。但连接本身的只读开关、prod 标记要和 SQL 连接一样显示（树上的标记、红色边框）。

## 5. R2：命令控制台

### 5.1 入口和编辑

- 在 Redis 连接或 db 节点上打开控制台，和 SQL 控制台用同一个动作。控制台标签页显示 `连接/dbN`。
- 编辑器复用 `vimedit`。**一行一条命令**，不需要 `;`（我的决定，和 redis-cli 一致）。空行和 `#` / `//` 开头的行忽略（实现者决定支持哪种注释）。
- 执行：光标所在行（和 SQL 控制台的"执行当前语句"同一个动作）；VISUAL 选中多行时按顺序执行；`<Space>R` 风格的"从当前行执行到末尾"也保留。多条中有一条失败就停下，状态栏写明是第几条，光标跳过去（和 SQL 控制台一致）。
- 命令行切分规则和 redis-cli 一致：空白分隔，`"…"` 里支持 `\n \t \" \\ \xNN`，`'…'` 里只转义 `\'`。引号没闭合时报错，并显示位置。切分放在 `rcmd`，用表驱动测试。
- 草稿自动保存、历史记录复用 `internal/history`，按连接类型区分即可。`:e` / `:w` 打开、保存文件（`.redis` 或任意文本）是否支持，由实现者决定；能低成本复用就支持。

### 5.2 补全和高亮

- **命令名和参数**：`rcmd` 内置一张命令表，包含名字、参数提示、分级、是否带 key 参数及其位置、所属组。补全弹出命令名并附带参数提示（如 `HSET key field value [field value ...]`）。这张表是内置的静态数据，不依赖服务器（我的决定：可测试，离线可用，不会被 ACL 挡住）。可以从 Redis 官方 `commands.json` 生成，生成脚本和来源版本写在文件头。
- **key 名**：在带 key 参数的位置，补全已扫描到的 key（来自同一个 db 的树缓存）。输入前缀后可以触发一次后台 `SCAN MATCH 前缀*`（有上限）补充结果。
- 高亮：命令名、字符串、数字区分颜色即可。

### 5.3 结果显示

结果来自 RESP3 结构，显示规则如下：

| 结果 | 显示 |
|---|---|
| 简单字符串 / bulk 字符串 | 一个值；用 §4.4 的规则显示（JSON 格式化、二进制转 hex） |
| 整数、double、bool | 一个值，带类型提示，如 `(integer) 3` |
| null | `(nil)` |
| 错误 | 红色错误文字，不弹框 |
| 数组 / set，元素都是标量 | 表格：index / value |
| map；或者是已知返回成对数组的命令（`HGETALL`、`CONFIG GET`、`ZRANGE … WITHSCORES`、RESP2 下的这些命令） | 两列表格：field / value（或 member / score） |
| 嵌套结构（`XRANGE`、`SCAN` 的 `[cursor, [keys]]`、`COMMAND INFO` 等） | 缩进的树状文本，和 redis-cli 类似 |

- 表格复用 `panes/grid`，可以复制、导出（复用 `export`）。显示行数受 `max_rows` 限制，超出时提示被截断。
- 每条命令的结果都保留在结果区（和 SQL 控制台多结果一样），状态栏显示耗时。
- 结果里的 key 名（比如 `SCAN`、`KEYS` 的结果）可以用一个动作直接打开详情页（我的决定：这是控制台和浏览之间最常用的跳转）。

### 5.4 会话状态

- 控制台用 `rdb.Session`（专用连接）。`SELECT n` 后，标签页标题和状态栏要跟着显示新的 db 编号。
- **事务**：`rcmd` 跟踪 `MULTI` / `EXEC` / `DISCARD`（还有 `WATCH`），状态栏显示 `[TX]`，结果区把排队中的命令标成 `QUEUED`。和 SQL 控制台的事务一样，关闭标签页或退出时，如果事务没结束要先确认。
- **断线和取消**：go-redis 取消命令时会关闭连接，所以取消一条阻塞命令（比如 `BLPOP 0`）之后，Session 要重建连接。规则：
  - 不在事务中：自动重连，并**恢复 db 编号**（重新 `SELECT`），状态栏提示 `reconnected (db N)`。
  - 在事务或 `WATCH` 中：不自动重连，提示事务已丢失，状态回到非事务，下一条命令需要用户再执行一次才会连上。这条对应主设计"事务中不静默重连"。
- `CLIENT SETNAME` 等其他连接状态在重连后不恢复，这是已知限制，写进帮助文档即可。

### 5.5 不支持的命令

`SUBSCRIBE`、`PSUBSCRIBE`、`SSUBSCRIBE`、`MONITOR`、`SYNC`、`PSYNC`、`CLIENT TRACKING`、`RESET`，以及 `HELLO`（会改变协议）：控制台直接拒绝，提示 `<命令> is not supported in sqlvim`，不发给服务器。

## 6. R3：编辑

编辑都在 Key 详情页里进行，手感沿用数据表格：先暂存，单元格或行标成待提交，用现有的提交动作（`<Space>w`）一次提交，撤销暂存的动作也一样。

### 6.1 支持的修改

| 类型 | 修改 | 命令 |
|---|---|---|
| String | 改值（短值用单元格编辑，长值或 JSON 用 `$EDITOR`，和现有的长文本编辑一致） | `SET … KEEPTTL` |
| Hash | 改 value、增加 field、删除 field、改 field 名 | `HSET`、`HDEL`（改名就是 `HDEL` 加 `HSET`） |
| List | 改元素、删除元素、在头尾追加 | `LSET`；删除用 `LSET` 一个占位值再 `LREM` 这个占位值（实现者可以换方法，但必须只删这一个位置）；`LPUSH` / `RPUSH` |
| Set | 增加、删除、改成员 | `SADD`、`SREM` |
| ZSet | 改 score、增加、删除、改成员 | `ZADD`（改 score 用 `XX`）、`ZREM` |
| Stream | 只能追加和删除条目 | `XADD`、`XDEL` |
| 任意 key | 设 TTL / 去掉 TTL、重命名、删除、新建（选类型并填第一个值） | `EXPIRE` / `PERSIST`、`RENAME`（默认用 `RENAMENX`，目标已存在时要确认才覆盖）、`UNLINK` |

大 String 只加载了前 1 MB 时，不允许编辑值（反目标：写回被截断的值）。

### 6.2 "正好作用于预期的值"

SQL 编辑的规则是每条语句必须正好影响一行。Redis 上对应的规则（我的设计）：

- 提交时，先 `WATCH key`，读出暂存修改依赖的旧值（Hash field 的当前值、List 下标位置的当前元素、ZSet 成员当前的 score 等），和打开时看到的值比较。**不一致就整批放弃**，提示 `key changed since loaded; refresh and retry`。
- 一致就在 `MULTI` / `EXEC` 里执行全部修改。`EXEC` 返回 nil（`WATCH` 的 key 被别人改了）也按上面的方式放弃。
- 这条检查要放在 `rdb` 或一个纯包里，用表驱动测试加集成测试覆盖，比如"打开后另一个客户端改了 List，`LSET` 必须被拒绝"。

### 6.3 按模式批量删除

在树的文件夹节点、过滤结果或 db 节点上可以批量删除：

1. 先用 `SCAN MATCH` 数出匹配的 key 数（可以取消），弹出预览，显示数量和前 N 个 key。
2. 确认后用 `UNLINK` 分批删除（每批 500 个，实现者可以调），显示进度，可以中途取消，最后报告删除了多少个。
3. 非 prod 连接按 `y` 确认；prod 连接要输入连接名（和现有删表确认一致）。
4. 删除过程中新写入的匹配 key 不保证删掉，预览里写明这一点。

### 6.4 只读

只读连接上所有编辑入口都拒绝，提示用 `:set noreadonly` 临时解锁，和 SQL 连接一样。

## 7. 安全规则（R2 起，修订主设计 §5.3）

Redis 没有按连接设置的只读开关（`READONLY` 命令只用于 Cluster 副本）。ACL 可以限制，但那是服务器配置，sqlvim 不改。所以和 SQL Server / Oracle 一样，**只有客户端这一层**，规则要保守。

### 7.1 命令分级（`rcmd`）

| 级别 | 例子 |
|---|---|
| 读 | `GET`、`HGETALL`、`SCAN`、`TTL`、`TYPE`、`INFO`、`DBSIZE`、`EVAL_RO`、`FCALL_RO`、`SELECT`、`PING` |
| 写 | `SET`、`DEL`、`UNLINK`、`HSET`、`EXPIRE`、`RENAME`、`EVAL`、`EVALSHA`、`FCALL`（脚本一律按写处理） |
| 危险 | `FLUSHDB`、`FLUSHALL`、`KEYS`、`CONFIG SET`、`CONFIG REWRITE`、`CONFIG RESETSTAT`、`SHUTDOWN`、`DEBUG`、`SCRIPT FLUSH`、`FUNCTION FLUSH` / `DELETE` / `RESTORE`、`CLIENT KILL`、`CLIENT PAUSE`、`REPLICAOF` / `SLAVEOF`、`MIGRATE`、`ACL SETUSER` / `DELUSER`、`SAVE`、`BGSAVE`、`BGREWRITEAOF`、`SWAPDB`、`MODULE LOAD` |
| 不支持 | §5.5 列出的命令 |
| 未知 | 命令表里没有的名字（包括模块命令） |

有子命令的命令（`CONFIG`、`CLIENT`、`SCRIPT`、`FUNCTION`、`ACL`、`OBJECT`、`MEMORY`、`XINFO` 等）按子命令分级。完整清单由实现者根据命令表的 flags 整理（官方 `commands.json` 里有 `readonly`、`write`、`admin`、`dangerous` 等 flag 和 ACL 分类），这里只列出必须正确的例子。

### 7.2 规则

| 情况 | 行为 |
|---|---|
| 只读连接，执行写 / 危险 / 未知命令 | 拒绝，提示 `:set noreadonly` |
| prod 连接，执行写命令或**未知命令** | 弹确认框，显示命令，按 `y` 确认（未知命令的处理和提交 `5ea2eaf` 里 SQL Server / Oracle 认不出的语句一致） |
| 任何连接，执行危险命令（除 `KEYS`） | 弹确认框；prod 上要输入连接名 |
| `KEYS` | `DBSIZE` 大于 10,000 时（阈值可以配置）直接拒绝，提示改用 `SCAN` 或树过滤；不超过时按危险命令确认 |
| `MULTI` … `EXEC` | 每条排队的命令单独按上面的规则检查（在 `MULTI` 之后、发送之前），所以 `MULTI` 不能用来绕过 |
| 多行一起执行 | 先检查全部，任何一条被拒绝就一条都不执行；需要确认的合并成一个确认框 |

- 规则放在 `rcmd`（或者一个类似 `runner.Guard` 的函数），和 SQL 的 `runner` 一样用表驱动测试覆盖放行和拦截的情况。
- 编辑（R3）和批量删除走的是 sqlvim 自己生成的命令，同样要过只读和 prod 检查。
- README（中英文）说明：Redis 的只读只在客户端生效，生产实例建议再配一个只读的 ACL 用户（`+@read`）。

## 8. R4：运维与增强

- **状态页**：连接节点上打开（或打开连接后默认显示，实现者决定）。显示 `INFO` 的关键指标：版本、角色、运行时间、客户端数、key 总数、已用内存 / 峰值 / `maxmemory`、命中率、淘汰数。每 2 秒采样一次 `instantaneous_ops_per_sec`、内存、网络输入输出，用终端字符画 sparkline 显示最近几分钟。标签页不可见时停止采样。
- **慢查询**：`SLOWLOG GET` 的表格（时间、耗时、命令、客户端）。
- **客户端列表**：`CLIENT LIST` 的表格。是否提供 `CLIENT KILL` 由实现者决定；提供的话按危险命令确认。
- 上面三项任何一个被 ACL 禁止时，只显示能拿到的部分，并标明 `permission denied`。
- **收藏和最近打开**：收藏 key、最近打开的 key，存进 `internal/history` 的 SQLite，按连接和 db 区分；接入现有的最近打开列表。
- **导出**：把一个 key 的值（或一个表格视图）导出成 JSON / CSV，复用 `export`。
- **大 key 分析**：类似 `redis-cli --bigkeys`，用 SCAN 遍历，对每个 key 取类型和长度（可以加 `MEMORY USAGE … SAMPLES 0`），列出各类型最大的 N 个。必须限速（比如每批之间停一下），可以取消，显示进度。生产实例上运行前要确认。

## 9. 测试

- **纯包**（表驱动）：`rcmd` 的命令行切分、分级（包括子命令、未知命令、`MULTI` 里的检查）、模式处理、key 名转义、事务状态跟踪；R3 的"旧值比较"逻辑。
- **集成测试**（`integration` tag）：用 testcontainers 的 `modules/redis` 启动 Redis 7，在 `internal/rdb` 里测：SCAN 分批和去重、`KeyInfo`、各类型分页读取、二进制 key 往返、Session 的 `SELECT` 持续生效、取消阻塞命令后的重连和 db 恢复、事务中断线不自动重连、R3 的 `WATCH` 冲突被拒绝、`UNLINK` 批量删除。SSH 隧道沿用 `tunnel` 包里的内存 SSH 服务器测一次。再加一个 Redis 5 容器（`redis:5`），测 §3.2 的兼容分支：类型徽标、RESP2 的结果显示、R3 改 String 时保留 TTL。
- **UI 测试**：`fakes_test.go` 增加一个假的 `rdb.Client` / `Session`，驱动 `Model.Update` 测树的分批加载、过滤、"load more"、详情页分页、控制台确认和拒绝。
- **手工验证**：先连内网测试实例 `172.16.96.126:6379` 确认版本（`INFO server`），再连作者的生产实例（只读 + prod），验证浏览几百万 key 时界面流畅、服务器无明显压力（观察 `INFO` 的 ops），写命令被拦截。

## 10. 交付顺序

R1 → R2 → R3 → R4，每个迭代单独写实施计划（`docs/superpowers/plans/`），各自可以发布。版本号在发布时决定。

- **R1**：`config` / `session` / 连接表单支持 redis、`rdb`（Client、SCAN、读取）、`rcmd` 的转义和模式处理、树、过滤、直接打开、Key 详情页。
- **R2**：`rdb.Session`、`rcmd` 的命令表 / 切分 / 分级 / 事务跟踪、控制台、补全、结果显示、§7 的安全规则。
- **R3**：§6 全部。
- **R4**：§8 全部，各项之间互不依赖，可以分开做。

每个迭代都要在 `CHANGELOG.md` 的 `[Unreleased]` 下加条目，README 中英文同步更新，主设计文档的架构图和安全规则一节加上指向本文的说明。

## 11. 留给实现者决定的

- `rdb` 接口的具体形状，以及 Redis 节点怎么接入 `panes/tree`（新增 `Kind`，还是独立的节点类型）。
- 新的 keymap 上下文（比如 Redis 键详情、Redis 控制台）怎么划分，各动作默认绑哪个键。要求：能和 SQL 共用的动作（打开、过滤、刷新、提交、取消、复制、导出）用同一个 Action 和同一个键。
- 类型徽标的字母和颜色、空 db 的显示方式、单子节点文件夹是否合并。
- 扫描批量大小、上限（§4.1、§4.2 给的是默认值）、大 String 阈值、`KEYS` 阈值、批量删除每批数量的默认值，以及是否放进 `config.toml`。
- 无密码连接的具体处理方式（§3.4）。
- 命令表的生成方式和收录范围。
