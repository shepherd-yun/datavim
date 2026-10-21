# Elasticsearch 支持 设计文档

- 日期：2026-10-21
- 状态：E1、E2 已实现
- 范围：新增 `elasticsearch` 连接类型，分 3 期交付（E1 查和改、E2 集群运维、E3 其余功能）；修订主设计 §3（架构）、§5.3（安全规则）

## 1. 背景

作者现在用 Chrome 扩展 Elasticvue 操作 Elasticsearch。功能够用，但在浏览器里，没有快捷键，交互不方便，细节粗糙，界面乱。Elasticvue 的「搜索」页和「REST」页作者都觉得不方便。

作者要连的集群：

- 公司内网的 ES **7.10.2**，HTTP 直连（`http://172.16.96.161:9200`），没有认证。规模：156 个索引、约 8900 万文档、54.6 GB、1004 个分片。
- 自己在 Docker 里起的测试集群。

最常见的场景是**按 ID 或关键字查几条记录**，看完整内容、复制、必要时改掉。

作者的要求：

- Elasticvue 的功能**尽量都做上**（集群概览、节点、分片、索引管理、文档浏览和搜索、REST 控制台、快照、用户）。
- 所有写操作**都可以做**，但要按风险严格把关。
- 按键符合本项目的设计，交互比 Elasticvue 顺，细节好。
- 分期的划分（§12）作者已经同意。

Elasticvue 只作参考，界面、按键、功能取舍按本项目自己的设计来，不照搬。

## 2. 目标、非目标、反目标

**目标**

- ES 连接和 SQL / Redis / Kafka 连接在**同一个连接列表、同一棵树**里，共用连接表单、钥匙串密码、SSH 隧道、workspace、`env = "prod"` 标记、只读开关和审计日志。
- §1 的主场景：打开索引 → 输入 ID 或关键字 → 看到命中的文档和命中字段 → 看完整 JSON、复制 → 改一个字段并提交。每一步一两个键。
- 一个 REST 控制台，写 Query DSL 和运维请求比 Kibana Dev Tools / Elasticvue 顺手：vim 编辑、补全、按请求执行、结果可以按表格看。
- 几千万文档的索引上翻页不卡，不爆内存，关掉标签页后不在服务器上留下 scroll / PIT 上下文。
- 现有安全规则在 ES 上有对应的规则（§9）：只读、prod 确认、危险操作输入名字确认、通配删除先展开、文档修改不覆盖别人的并发修改。
- 现有的 SQL / Redis / Kafka 行为不变。

**非目标（整个 ES 系列）**

- Kibana 的功能：Discover 式的时间直方图、可视化、Dashboard、Lens。
- 机器学习、Watcher、跨集群复制（CCR）、跨集群搜索的配置、Fleet / APM / Enterprise Search。
- 走势图和长期指标（只显示 API 当时返回的数值；E2 的节点页可以有现有 sparkline 的短期刷新曲线，由实现者决定）。
- 用 SQL 方言查 ES（`_sql` 可以在控制台里当普通请求发，但不做 SQL 控制台集成）。
- Elasticsearch 7.10 以下的版本（不保证，见 §3.2）。
- Elastic Cloud 的 `cloud_id` 连接方式（用 URL 连，以后需要再加）。

**反目标**（技术上能用，但这样就算失败）

- 控制台里一个会写数据的请求被当成读请求放行（比如 `POST index/_doc` 被归成"POST 也可能是查询"），在只读或 prod 连接上执行了。
- 文档编辑提交时覆盖了别人在这期间的修改。
- `DELETE logs-*` 实际删掉的索引比确认时给作者看的多。
- 浏览大索引时把 scroll / PIT 上下文留在服务器上（关标签页、断开连接、退出 datavim 都要清理），占用集群内存。
- 一个很大的响应（比如没写 `size` 的聚合、`_cat/shards` 上万行、几 MB 的文档）卡死界面或者让内存一直涨。
- 客户端做 sniffing，绕过 SSH 隧道直连节点的内网地址。
- 密码、API Key 出现在日志、界面、"复制为 curl" 的结果里。

## 3. 架构

### 3.1 平行于 `db`、`rdb`、`kafka`

```
ui ──→ session ──→ db    ──→ mysql / postgres / sqlserver / oracle
  │          ├───→ rdb   ──→ go-redis
  │          ├───→ kafka ──→ franz-go
  │          └───→ es    ──→ net/http
  └──→ esx（纯包）
```

- **`internal/es`**（新）：Elasticsearch 和 datavim 之间唯一的接口，地位相当于 `internal/db` / `internal/rdb` / `internal/kafka`。UI 不直接发 HTTP。
- **`internal/esx`**（新，纯包，不碰终端和网络）：REST 控制台文件的解析（把文本切成一个个请求，§6.1）、请求分级（§9.1）、过滤框输入到查询 DSL 的转换（§5.3）、mapping 展平成列、文档 JSON 的路径读写（编辑用）、响应到表格的转换（`_search` hits、`_cat`、聚合 buckets）、补全上下文（§6.3）。地位相当于 `sqlx` / `rcmd` / `kmsgx`。包名由实现者决定。
- 依赖方向不变：`ui` → `session` / `esx` → `es` → `net/http`。

### 3.2 客户端和版本

**不用官方 go-elasticsearch 客户端**（我的决定）。官方客户端按服务端大版本发布，v8 客户端会检查 `X-Elastic-Product` 响应头和版本，连不上 7.10 和 OpenSearch。datavim 需要的是"发任意 REST 请求 + 一小组固定的管理 API"，直接用 `net/http` + `encoding/json` 更简单，而且 REST 控制台本来就要发任意请求。

- **支持范围**：Elasticsearch **7.10 到 9.x**；OpenSearch 1.x / 2.x 尽量兼容（核心功能：浏览、搜索、编辑、控制台、索引管理），不支持的功能显示一行原因。
- 连接时调用 `GET /` 记下发行版（elasticsearch / opensearch）和版本号，按版本和发行版决定少数几处行为差异（比如翻页方式 §5.4、安全 API 路径 §8.3）。某个 API 返回 404 / 400 `no handler` / 403 时，只禁用这个功能并显示一行原因（`not supported by this cluster` / `permission denied`），其他照常。
- **不做 sniffing**：只连配置里写的地址。多个地址时按顺序重试（第一个连不上换下一个），不从集群读节点地址（反目标）。
- 每个请求带 `context.Context`，可以用 `ctrl+c` 取消。连接池用 `http.Transport`，拨号函数可以注入（SSH 隧道）。
- 响应体有大小上限（默认 50 MB，可配置），超过时停止读取并报错 `response larger than 50 MB; add size / filter_path`，不让内存一直涨。
- `Content-Type: application/json`；`_bulk` / `_msearch` 用 `application/x-ndjson`。

### 3.3 `es` 包的接口（草案，实现者可以调整）

| `db` / `rdb` / `kafka` | `es` | 说明 |
|---|---|---|
| `Conn` / `rdb.Client` / `kafka.Client`（并发安全） | `es.Client` | 一个连接一个，内部一个 `http.Client` |
| `Session` / `kafka.Reader` | `es.Cursor` | 每个 Documents 视图一个，持有 PIT 或 scroll，随标签页关闭而释放 |
| `Database` | 无 | 一个连接对应一个集群 |

`Client` 的核心是**一个方法**：

- `Do(ctx, Request) (Response, error)`：`Request` 是 method、path（含 query string）、body（`[]byte`）。`Response` 是状态码、响应头里需要的几项、body（`[]byte`，受 §3.2 的大小上限约束）、耗时。

其他操作都是 `Do` 之上的类型化封装：`Info`、`Health`、`Indices`（`_cat/indices?format=json`）、`Aliases`、`Mapping`、`Settings`、`Stats`、`Search`、`Count`、`Get`、`Index`、`Update`、`Delete`、`DeleteByQuery`、`ResolveIndex`、`Nodes`、`Shards`、`Tasks`、`Snapshots` 等。

**类型化封装必须是建立在一个只有 `Do` 的小接口（比如 `es.Doer`）之上的普通函数，不能是 `es.Client` 内部实现的方法**（我的决定）。这样 `session` 交给 UI 的那个包了检查的 `Doer`，就是所有请求的唯一出口（§9.3）。不能照搬 `guardedKafka` 那种"嵌入接口、覆盖写方法"的做法：只覆盖 `Do` 时，内部实现的类型化方法会直接调内部的 `Do`，绕过检查。

错误：连接失败、认证失败（401）、权限不足（403）、索引不存在（404 `index_not_found_exception`）、版本冲突（409 `version_conflict_engine_exception`）包装成可识别的哨兵错误。ES 的错误响应（`{"error":{"type","reason","root_cause"...}}`）显示时取 `type: reason`，原始 JSON 在详情里可以看。

### 3.4 配置和会话层

`config.Connection` 不改结构，增加 `driver = "elasticsearch"`：

| 字段 | ES 的含义 |
|---|---|
| `host` | 节点地址，逗号分隔，每项可以带协议和端口：`http://10.0.0.1:9200,10.0.0.2`；不带协议时用 `params.tls` 决定 http / https，不带端口时用 `port` |
| `port` | 默认 `9200`（加进 `defaultPorts`） |
| `user` | Basic 认证用户名；留空表示不用 Basic |
| `database` | 不用 |
| `ssh` | 和现有一样 |
| `params` | `tls = "true"`、`tls_skip_verify`、`tls_ca`、`tls_cert`、`tls_key`（文件路径）、`auth = "apikey"`（用 API Key 代替 Basic）、`path_prefix`（集群在反向代理的子路径下时用）、`show_system = "true"`（显示 `.` 开头的系统索引）、`page_size`（Documents 每次加载条数，默认 100） |

- 密码和 API Key 进钥匙串，**不写进 toml**。Basic 密码用连接表单现有的 Password 字段；API Key 不加表单字段，第一次需要时弹出输入框，存进钥匙串（复用 `session/secrets.go` 的 `SetSecret` / `secretFor` 和 Kafka 用过的流程），认证失败时重新询问。
- 作者的内网集群：`driver = "elasticsearch"`、`host = "172.16.96.161"`，什么认证都不填就能连。
- Docker 里的 ES 8.x 默认开着 HTTPS 和 `elastic` 用户：`tls = "true"` + `tls_ca` 或 `tls_skip_verify`，`user = "elastic"`。README 写一个例子。
- 连接表单：驱动选 elasticsearch 时，`Host` 标签显示为 `URLs`，`Database` 隐藏，增加 TLS 和认证方式的选项（和 Kafka 表单的做法一致）。
- `session.Manager` 增加 `ES(ctx, name) (es.Doer, error)`，返回的是包了安全检查的 `Doer`（§3.3、§9.3）；`ui.Backend` 增加同名方法。密码、隧道、`Reset`、`SetReadOnly` 走现有逻辑。
- `Makefile` 的 `test-integration` 把 `./internal/es/...` 加进去。

## 4. 树

```
▾ es-dev  [7.10.2 · green]
  ▾ Indices (156)
    ▾ logs-app-* (31)              ← 按日期滚动的索引折叠成一组
        logs-app-2026.10.21   ● 1.2M docs  830MB
        …
      orders                  ● 8.8M docs  4.1GB
      users                   ● 120k docs   88MB   [closed]
  ▸ Aliases (12)
  ▸ Data Streams (3)                ← 集群里有 data stream 时才显示
  ▸ Index Templates (8)             ← E2
  ▸ Snapshots                       ← E3，有快照仓库时才显示
```

- 连接节点旁显示版本和集群健康色（green / yellow / red，用主题里已有的颜色槽）。
- **Indices**：一次 `_cat/indices` 拿全部（索引通常几千以内）。每行显示健康色圆点、文档数、主分片大小；closed 的索引标 `[closed]`，red / yellow 的整行用警告色。
  - 系统索引（`.` 开头）默认隐藏，用现有的 ToggleInternal（`.`）切换，或者 `params.show_system`。
  - **按日期滚动的索引折叠成一组**（我的决定）：名字去掉末尾的日期或序号部分（`-2026.10.21`、`-2026-10`、`-000123` 这类）后相同的索引，超过 1 个时折叠成 `前缀-* (N)`，组内按名字倒序（最新的在上面）。其他索引不分组。具体识别规则由实现者定，表驱动测试。
  - 默认按名字排序；用现有的 SortGroups（`s`，Kafka 里是按 lag / 名字切换）在名字、文档数、大小之间切换（我的决定：复用同一个动作，描述改成通用的）。
- **Aliases**：别名 → 指向的索引（多个时显示数量），标出 write index。回车打开时按别名打开 Index 标签页（搜索走别名）。
- **Data Streams**：名字、backing index 数量、模板。回车按 data stream 打开 Index 标签页。
- 树上的 `F`（FilterPrompt）在 Indices 节点上按名字过滤（支持 `*` 通配），和 Redis / Kafka 树的过滤一致。
- 索引、别名、data stream 出现在全局搜索（`<Space><Space>`）里。
- 在索引 / 别名 / data stream 节点上回车打开 Index 标签页；在连接节点上用 ServerInfo（`<Space>i`）打开集群状态页（§5.9 / §7）。

## 5. E1：查和改

### 5.1 Index 标签页

标题是索引名（或别名、通配表达式），多次打开同一个复用已有标签页。分成几个部分，用 `[` / `]`（PrevSection / NextSection）切换，和 Kafka Topic 标签页、状态页一致：

| 部分 | 内容 |
|---|---|
| **Documents** | 文档表格（§5.2） |
| **Mapping** | 字段树：路径、类型、`index` / `doc_values` 开关、analyzer、`fields` 子字段、`copy_to`。可以复制整个 mapping JSON。E1 只读，E2 可以加字段（§7.4） |
| **Settings** | 所有 settings，非默认值高亮。动态 settings（`number_of_replicas`、`refresh_interval`、`max_result_window` 等）可以改，暂存后一次提交（§5.8） |
| **Aliases** | 这个索引的别名、filter、routing、是否 write index；可以加、删别名 |
| **Stats** | 文档数、删除的文档数、主分片 / 总大小、分片数和副本数、segment 数、refresh / flush / merge 的次数、查询和写入的总次数。别名或通配时每个实际索引一行 |

头部一行：`● green · 5p / 1r · 8,812,301 docs · 4.1 GB · created 2026-03-02`。别名或通配时显示命中的索引数。

### 5.2 Documents 部分

```
[orders]  Documents  Mapping  Settings  Aliases  Stats        q: status:paid AND 88121   12 hits   sort: _score
  _id          order_no     status   amount   user.name   created_at
▶ ord-88121    88121        paid     99.00    张三        2026-10-20 09:12:01
  ord-88122    88122        paid     12.50    李四        2026-10-20 09:12:04
```

- 表格复用 `panes/grid` 和数据表格的手感。
- **列**：从 mapping 取，嵌套的 object 字段展开成 `user.name` 这种路径；`nested` 类型和数组显示 JSON 预览。`_id` 一直在第一列；别名、通配或 data stream 时第二列是 `_index`。
- **默认显示哪些列**（我的决定）：先放 keyword、数字、日期、boolean、短 text 字段，跳过 `text` 大字段、`object` 父节点、`geo`、`dense_vector`、二进制，最多先显示 12 列；其余列通过一个列选择弹窗打开（现有的 `columns.go` 绑定在 `db.TableRef` / `db.Column` 上，实现者决定是把它抽象出来复用，还是给 ES 单独做一个）。列的显示 / 隐藏 / 顺序**只在标签页里有效**，现在没有持久化列布局的机制，这一期不做。
- 单元格值：按 `_source` 里的原值显示；字符串过长截断，`K` 看完整内容。字段在 `_source` 里不存在时显示为空（区别于 `null`，用现有表格里区分 NULL 的样式）。
- **默认排序**：有过滤条件时按 `_score` 倒序；没有过滤条件时，mapping 里有 `@timestamp`（或作者在列上排过序的日期字段）就按它倒序，否则按索引顺序（`_doc`）。用现有的 SortColumn（`s`）在列上切换升序 / 降序 / 关闭；`text` 字段不能排序时提示 `cannot sort on text field; use <field>.keyword`（有 keyword 子字段时自动改用子字段）。
- **加载更多是自动的**：和 Kafka Messages 一样，光标越过最后一行（下移、半页下移）时加载下一页（`params.page_size`，默认 100）。另外在 ES 文档表格的上下文里也绑定 LoadMore（`}`，和控制台结果区同一个 Action 和键；Kafka Messages 没有这个绑定，这里是新增）。头部显示命中总数（§5.4）。没有更多时提示 `end of results`。
- **内存上限**：一个 Documents 视图最多保留 10,000 行（可以配置）；超过时丢弃最上面的行并提示。单个文档的 `_source` 超过 1 MB 时，表格只显示前 1 MB 解析出来的列预览，`K` 详情里提示被截断，复制时提示。
- 现有表格动作照常可用：`K` 看文档详情（§5.5）、`y` 系列复制（§5.5）、导出（复用 `export`，增加 NDJSON：每行一个 `{"_index","_id","_source"}`；CSV 按当前显示的列）。

### 5.3 过滤和查找

- **`F`（FilterPrompt）** 打开一行条件框，输入的是 **Lucene query string**（`query_string` 查询，`default_operator` 为 AND）：
  - 裸词：`88121`、`"张三"` → 在所有字段里搜。
  - 字段：`status:paid`、`user.name:"张三"`、`amount:>100`、`created_at:[now-1d TO now]`、`_id:ord-88121`。
  - 组合：`status:paid AND NOT user.name:test*`。
  - 条件框里**补全字段名**（来自 mapping），字段后输入 `:` 时对 keyword 字段补全取值（`terms` 聚合取前 20 个常见值，结果缓存；实现者决定是否做，至少要有字段名补全）。
  - 语法错误时（ES 返回 `query_shard_exception` / `parse_exception`）在条件框下面显示原因，不清空输入。
  - 条件框的历史用上下键翻，按连接 + 索引记住最近 50 条（我的决定）。
- 想写完整 DSL 时，在条件框里输入以 `{` 开头的内容，就按 JSON 当作 `query` 部分使用（我的决定：复杂条件不用切去控制台）。补全只对 query string 生效。
- **`f` 系列**：在单元格上 `ff` 加 `字段:"值"`、`f!` 加 `NOT 字段:"值"`、`fs` 加 `字段:*值*`、`fx` 清除，和数据表格一致。`filterEditBindings` 里的其他键在 ES 里这样对应（我的决定）：`f>` / `f<` → `字段:>值` / `字段:<值`；`fn` / `fN` → `NOT _exists_:字段` / `_exists_:字段`；`fl` 按现有含义处理，转换不了就在 ES 上下文里不绑定。追加到当前条件后面，用 AND 连接，值按 query string 规则转义。转义和拼接放在 `esx`，表驱动测试。
- **按 ID 打开**：复用 OpenKey（`<Space>k`，Redis 里是"按名字打开 key"），在 ES 连接 / Index 标签页上弹出输入框，输入 ID：
  - 具体索引：`GET <index>/_doc/<id>`，直接打开文档详情，不经过表格。
  - 别名、通配或 data stream：用 `ids` 查询，命中一条就直接打开详情，命中多条（不同索引里有相同 ID）就在表格里列出，`_index` 列区分。
  - 在连接节点上（没有选索引）时，输入 `索引/ID`，或者只输入 ID 在所有非系统索引里查（`ids` 查询，`_all` 只用于读）。
  - 找不到时提示 `no document with _id ord-88121 in orders`。
- **命中高亮**：有过滤条件时请求带 `highlight`（`fields: *`，`require_field_match: false`，每个字段 1 个片段，片段大小 100）。表格里命中的单元格用现有的搜索高亮颜色标出命中词，详情里同样标出；一行里不在当前显示列的字段命中时，在行尾显示 `+ user.remark`（实现者决定样式），作者能看出是哪个字段命中的。高亮请求失败（比如某些字段类型不支持）时去掉高亮重试一次，不影响查询。
- 表格里的现有 `/` 查找（在已加载的行里找）保持不变。

### 5.4 翻页和命中数

- **命中数**：请求带 `track_total_hits: true`，头部显示精确命中数（作者的索引规模下开销可以接受，我的决定）。查询超过 5 秒（可以配置）没返回总数时，改用 `track_total_hits: 10000` 重新查，显示 `10,000+`。
- **翻页**：要求无重复、无遗漏，能翻过 `max_result_window`（默认 10,000）：
  - ES 7.12+ / 8.x / 9.x：PIT + `search_after`，排序最后加 `_shard_doc` 作为 tiebreaker。
  - ES 7.10 / 7.11（作者的集群）：没有 `_shard_doc`。实现者在 PIT + `search_after`（自己加稳定的 tiebreaker）和 scroll 之间选一个，满足上面的要求。集成测试必须在 7.10.2 上翻过 10,000 条。
  - OpenSearch：用它支持的方式（PIT 从 2.4 起；更早用 scroll）。
  - PIT / scroll 的保活时间 5 分钟，加载下一页时续期；**标签页关闭、重新查询、连接断开、退出 datavim 时显式释放**（`DELETE _pit` / `DELETE _search/scroll`，反目标）。释放失败只写日志，不打扰作者。
- 前 10,000 条以内、只看第一页的情况，可以不开 PIT 直接 `from/size`，等真的要翻过去时再开（实现者决定）。

### 5.5 文档详情和复制

在一行上按 `K`（CellDetail）打开文档详情：

```
orders · ord-88121                      _seq_no 1022 · _primary_term 3 · _version 4 · _score 7.31
──────────────────────────────────────────
{
  "order_no": 88121,
  "status": "paid",
  "user": { "name": "张三", ... },
  ...
}
```

- 格式化加高亮的 JSON。对象和数组可以折叠（实现者决定按键，复用 `z` 系列折叠的 Action 优先）。有命中高亮时标出命中词。
- 在详情里用现有的上一条 / 下一条动作切换文档（实现者决定复用哪个 Action，和 Kafka 消息详情一致）。
- **复制**（`y` 前缀，第二个键由实现者决定，不能和 `gridBindings` 已经占用的 `yy` `yl` `yt` `yc` `yj` `ym` `yi` `yd` 冲突）：
  - `yl`：当前单元格的值。
  - `yy`：当前行（按现有表格的复制格式）。
  - 复制 `_id`。
  - 复制整个 `_source` JSON。
  - 复制成控制台请求：`GET orders/_doc/ord-88121`，以及 `PUT orders/_doc/ord-88121` + `_source`（方便贴到控制台改完再发）。
  - 复制成 curl（URL 用连接里的地址；**不带密码和 API Key**，用 `-u user` 或 `-H 'Authorization: ApiKey …'` 占位，反目标）。
- 一个动作把 `_source` 在 `$EDITOR` 里打开编辑（ExternalEdit，`<Space>v`），保存后暂存为整篇替换（§5.6）。

### 5.6 编辑文档

和数据表格的暂存编辑一致：改动先暂存并标记，`<Space>w`（SubmitEdits）一次提交，`u` / `U` 撤销。

| 操作 | 按键（复用） | 请求 |
|---|---|---|
| 改单元格 | EditCell（`i` / 回车） | 见下 |
| 整篇编辑 | ExternalEdit（`<Space>v`） | 见下 |
| 新文档 | InsertRow（`o`） | 在 `$EDITOR` 里写 JSON（预填 mapping 里的字段名和类型注释，实现者决定），ID 可以留空让 ES 生成。`PUT <index>/_create/<id>` 或 `POST <index>/_doc` |
| 删除文档 | DeleteRow（`dd`） | `DELETE <index>/_doc/<id>` |

- **单元格编辑的类型**：按 mapping 的类型解析输入（数字、boolean、日期按字符串原样、keyword / text 是字符串）；输入 `null` 设为 null；对象和数组单元格进入 JSON 编辑（单行或 `$EDITOR`）。类型不对时在输入框里提示，不暂存。规则放在 `esx`，表驱动测试。
- **提交方式**（我的决定）：每个改动的文档用暂存时读到的 `_source` 加上改动，生成完整的新 `_source`，`PUT <实际 _index>/_doc/<id>?if_seq_no=..&if_primary_term=..`。不用 `_update` 的局部更新，因为局部合并时数组和对象的语义容易出乎意料。
  - **实际 `_index`**：通过别名、通配、data stream 打开时，写到 hit 里的 `_index`，不写到别名上。data stream 的 backing index 不允许按 ID 改（ES 要求用 `_update_by_query`），这种情况提示并拒绝编辑。
  - **并发**：带 `if_seq_no` + `if_primary_term`。返回 409 时这条改动不提交，提示 `ord-88121 changed since you loaded it; reload to see it`，保留暂存让作者重新加载后再改（反目标：不覆盖别人的修改）。
  - `routing`：hit 里带了 `_routing` 时写请求也带上。
  - `_source` 被关闭或者有 `excludes` 时，这个索引的文档只读，提示原因（重写会丢字段）。
  - `_source` 超过 1 MB 被截断的文档（§5.2）不能编辑，原因同上。
- 提交前弹出确认（§9.2），列出每个文档的改动（字段：旧值 → 新值；删除和新增分别标出）和将要发出的请求数。提交后逐条报告结果，部分失败时列出失败的文档和原因，不重试。
- 提交成功后带 `refresh=wait_for`（我的决定：提交后马上重新查询能看到修改），刷新表格。
- **按条件删除**：Documents 里一个动作（新增 DeleteByQuery，默认键由实现者决定），对当前过滤条件执行 `_delete_by_query`。执行前先 `_count` 显示将删除的条数和涉及的索引；没有过滤条件时按"删除全部"处理（§9.2 的特别危险级）。大量删除用 `wait_for_completion=false` 提交成任务，状态栏显示进度（E1 简单轮询 `_tasks/<id>`，E2 的任务页可以看和取消）。

### 5.7 REST 控制台

见 §6。

### 5.8 索引管理（E1）

| 操作 | 入口 | 请求 | 级别（§9.1） |
|---|---|---|---|
| 新建索引 | Indices 节点上 NewKey（`o`，树上现在是"新建 Redis key"，描述改成通用的） | 表单：名字、分片数、副本数，mapping 和 settings 在 `$EDITOR` 里写 JSON（可以留空）。`PUT <index>` | 写 |
| 删除索引 | DeleteNode（`dd`） | `DELETE <index>` | 危险 |
| 打开 / 关闭 | NodeActions（`a`）菜单 | `_open` / `_close` | 写 / 危险（关闭） |
| refresh / flush / 清缓存 | NodeActions 菜单 | `_refresh` / `_flush` / `_cache/clear` | 写 |
| force merge | NodeActions 菜单 | `_forcemerge?max_num_segments=N` | 危险（开销大，阻塞写入） |
| 别名 | Index 标签页 Aliases 部分：`o` 新增、`dd` 删除 | `_aliases` 的 add / remove（一个请求里原子完成） | 写 |
| 改 settings | Index 标签页 Settings 部分，和 Kafka Config 的编辑手感一样（`i` 改、`dd` 恢复默认、`<Space>w` 提交） | `PUT <index>/_settings`，**只提交改动的项** | 写；`index.blocks.*`、`number_of_replicas` 改成 0 算危险 |

- 删除索引对话框显示：文档数、大小、别名、是否是某个 data stream 的 backing index。
- 通配和 `_all` 的规则见 §9.2。
- NodeActions（`a`）菜单里列出所有能对这个节点做的操作，每项旁边显示快捷键（`keyHint`）。

### 5.9 集群状态页（E1 只做概览）

在连接节点上 ServerInfo（`<Space>i`），复用 `servertab.go` 的框架。E1 只做 **Overview** 一部分：集群名、UUID、版本、发行版、健康状态、节点数（data / master）、索引数、分片数（主 / 副本 / relocating / initializing / unassigned）、文档总数、总大小、pending tasks 数。每 10 秒刷新（可以配置），标签页不可见时停止。健康不是 green 时，在顶部用警告色写出原因（unassigned 分片数等），E2 里可以跳到分片页。

## 6. REST 控制台

### 6.1 文件格式

在 ES 连接上用 OpenConsole（`<Space>c`）打开。编辑器复用现有控制台和 `vimedit`。格式和 Kibana Dev Tools 一致（作者熟悉，也方便从网上复制）：

```
# 查订单
GET orders/_search
{
  "query": { "term": { "order_no": 88121 } }
}

GET _cat/indices?v&s=store.size:desc

POST _bulk
{"index":{"_index":"t","_id":"1"}}
{"a":1}
```

- 一个请求 = 一行 `METHOD path` + 可选的 body。body 到下一个 `METHOD` 行或文件末尾为止。`#` 和 `//` 开头的行是注释。
- path 可以有前导 `/`，也可以没有。`_bulk`、`_msearch` 的 body 是多行 NDJSON，原样发送（每行压成一行，末尾补换行）。
- body 允许 Kibana 的 `"""` 三引号字符串（多行脚本常用），发送前转成 JSON 字符串（我的决定：网上的例子大量使用）。
- 解析放在 `esx`，表驱动测试：多个请求、注释、空行、NDJSON、三引号、语法错误报行号。

### 6.2 执行和结果

- 光标所在的请求用 RunStatement（回车），全部用 RunAll（`R`），VISUAL 选中的按顺序执行，和 SQL 控制台同一组动作。执行前先解析全部，有任何一条解析失败就一条都不发。JSON body 不合法时指出位置，不发送。
- 执行前每个请求都按 §9 分级和确认。RunAll 里有写请求时，按 §9.2 的规则**一次**确认，确认框列出所有写请求。
- 结果区复用现有的结果标签页（每个请求一个结果标签，`H` / `L` 切换，和 SQL 控制台一致）：
  - 默认显示格式化加高亮的 JSON，可以折叠，显示状态码和耗时。
  - **可以切换成表格**（ResultView，`[` / `]`）：`_search` 的 hits（列按 `_source` 展平）、`_cat/*`（自动加 `format=json`，除非作者写了 `format`）、聚合的 buckets（第一层聚合，多层时展平成 `key1 · key2 · doc_count · 指标…`）。表格里可以用表格的复制和导出。
  - 错误响应用错误颜色显示 `type: reason`，原始 JSON 在下面。
  - 表格里 `_id` 单元格上 `gd`（OpenCellKey）打开那个文档的详情。
- 结果大小受 §3.2 的上限约束；JSON 显示超过 10,000 行时只渲染前 10,000 行，提示可以导出或复制全部（实现者决定）。
- 控制台文件和历史：复用现有的控制台文件（`consolefile.go`）、草稿自动保存和历史（`<Space>h`）。扩展名由实现者决定（建议 `.es`），按连接区分。
- "复制为 curl"：结果区或编辑器里一个动作，把当前请求转成 curl，不带密码（§5.5）。

### 6.3 补全

复用现有补全框架（`completion.go`、`panes/complete`）：

- 第一行：HTTP 方法；path 里补全索引名、别名、data stream（来自 meta 缓存）和常用接口（`_search`、`_count`、`_doc`、`_mapping`、`_settings`、`_cat/*`、`_cluster/*`、`_nodes/*`、`_tasks`、`_reindex`、`_delete_by_query`、`_update_by_query`、`_aliases`、`_snapshot` 等，接口表放在 `esx`）。
- body 里按 JSON 路径判断上下文：
  - `query` 下补全查询类型（`bool`、`term`、`terms`、`match`、`match_phrase`、`range`、`exists`、`ids`、`wildcard`、`prefix`、`nested`、`query_string`…）和各自的参数。
  - `bool` 下补全 `must` / `should` / `filter` / `must_not`。
  - 查询里需要字段名的地方补全**字段名**（来自 path 里索引的 mapping）。
  - 顶层补全 `query`、`size`、`from`、`sort`、`_source`、`aggs`、`highlight`、`track_total_hits` 等。
  - `aggs` 下补全常用聚合类型。
- 关键字表放在 `esx`，表驱动测试（输入：文本 + 光标位置；输出：上下文和候选）。E1 覆盖上面列出的；更完整的 DSL 表以后按需补。
- 高亮：请求行和 JSON body 用现有的高亮框架（`ui/highlight`），实现者决定是否新增一种语言定义。

## 7. E2：集群运维

集群状态页增加部分（`[` / `]` 切换）：

| 部分 | 内容 | 操作 |
|---|---|---|
| **Nodes** | 名字、IP、角色（m / d / i …）、是否 master、heap %、RAM %、CPU %、load 1m、磁盘用量 %、分片数、版本。超过阈值（heap 85%、磁盘超过 watermark）用警告色 | 回车看节点详情（`_nodes/<id>/stats` 的主要项） |
| **Shards** | 每个分片一行：索引、分片号、主 / 副本、状态、节点、文档数、大小。默认只显示非 STARTED 的分片（可以切换显示全部），这是作者最需要看的 | 在 UNASSIGNED 分片上回车运行 `_cluster/allocation/explain`，显示原因 |
| **Allocation** | 每个节点的分片数和磁盘用量（`_cat/allocation`） | — |
| **Tasks** | 正在运行的任务（`_tasks?detailed&actions=*reindex,*byquery*,*forcemerge*` 等）：动作、索引、进度（已处理 / 总数）、运行时间 | 取消任务（写操作），复用 KillClient（`dd`）或新增，实现者决定 |
| **Pending** | pending cluster tasks | — |

- 自动刷新同 §5.9。
- 分片页可以按索引过滤（`F`），从 Index 标签页的 Stats 部分用一个动作跳到"这个索引的分片"。

### 7.1 Reindex

- 在索引节点上 NodeActions 菜单里的 Reindex：表单填目标索引、可选的 query（复用 §5.3 的条件框语法）、`op_type`（index / create）、`conflicts`（abort / proceed）。
- 目标索引不存在时提示（ES 会用动态 mapping 自动建索引，作者通常不想要），可以选择先按源索引的 mapping 和 settings 建好目标索引（我的决定）。
- 用 `wait_for_completion=false` 提交，跳到 Tasks 部分看进度。

### 7.2 Update by query

Documents 里的一个动作，对当前过滤条件执行 `_update_by_query`，在 `$EDITOR` 里写 painless 脚本。执行前 `_count` 显示条数。危险级（§9.1）。

### 7.3 索引模板

- 树上 Index Templates 节点：composable templates（`_index_template`）和旧式 templates（`_template`，7.x 还常用）都列出，标明类型。
- 打开显示 JSON（patterns、priority、settings、mappings、aliases），可以在 `$EDITOR` 里修改后提交（写），删除是危险操作。component templates 一样处理。

### 7.4 Mapping 加字段

Mapping 部分的一个动作：在 `$EDITOR` 里写新字段的 mapping，`PUT <index>/_mapping`。ES 不允许修改已有字段的类型，提示作者改类型需要 reindex。写级。

### 7.5 集群 settings

状态页的 Settings 部分：persistent / transient / defaults（defaults 默认隐藏）。可以改 persistent 和 transient，和索引 Settings 的编辑方式一致。危险级（影响整个集群）。

## 8. E3：其余功能

### 8.1 快照

- 树上 Snapshots 节点：仓库列表 → 每个仓库的快照（名字、状态、开始时间、耗时、索引数、大小）。
- 新建快照（选索引，写）、删除快照（危险）、恢复快照（危险：可能覆盖同名索引，表单里提供 `rename_pattern` / `rename_replacement`，目标索引已存在且打开时拒绝）。新建和删除仓库（危险）。
- 进度在 E2 的 Tasks / 快照状态里看。

### 8.2 ILM 和 ingest pipeline

- ILM：策略列表和 JSON，可以编辑（写）、删除（危险）；索引的 ILM 状态（`_ilm/explain`）显示在 Index 标签页的 Stats 部分。OpenSearch 的 ISM 不做（不支持时按 §3.2 禁用）。
- Ingest pipeline：列表和 JSON，可以编辑和删除；一个动作对 pipeline 运行 `_simulate`（用当前选中的文档作为输入，我的决定）。

### 8.3 用户和角色

- 对应 Elasticvue 的「用户」页。集群开了安全功能时（ES 的 `_security`；OpenSearch 的 `_plugins/_security`，尽量兼容）在状态页加 Users / Roles 部分。
- 用户：列表、角色、是否启用；新建、改角色、启用 / 禁用（写），删除（危险），改密码（新密码用密码输入框，**不进日志、不进审计详情**）。
- 角色：列表和 JSON，可以编辑（写）和删除（危险）。
- 集群没开安全功能时整个部分不显示（作者的内网集群就是这种情况）。

### 8.4 打码

复用 `config.toml` 的 `[[mask]]`：增加 `indices`（`path.Match` 通配），`fields` 允许写 `_source` 里的路径。现在的 `checkMasks` 只接受 `key` / `value` 开头的字段，而且没写 `topics` 的规则会作用于所有 topic，所以要明确规则作用于哪种连接（按连接的 driver 判断：kafka 连接用 `topics` 和 `key`/`value` 路径，elasticsearch 连接用 `indices` 和 `_source` 路径），并按 driver 分别校验。匹配的字段在表格、详情、复制、导出里显示成 `******`，和 Kafka 消息打码一致。只影响显示。被打码的字段不能编辑（编辑会把 `******` 写回去）。

## 9. 安全规则（修订主设计 §5.3）

ES 服务器端可以有角色权限，但很多内网集群（包括作者的）没开安全功能，datavim 在**客户端这一层**做保守的拦截。README（中英文）建议生产集群使用只读角色的账号。

### 9.1 请求分级（`esx`）

所有请求（类型化的方法和控制台里手写的）都按 **method + path** 分级，表驱动测试覆盖：

| 级别 | 请求 |
|---|---|
| 读 | 所有 `GET` / `HEAD`；`POST` 的只读接口：`_search`、`_msearch`、`_count`、`_mget`、`_explain`、`_validate/query`、`_field_caps`、`_analyze`、`_search/template`、`_render/template`、`_sql`、`_query`（ES\|QL）、`_eql/search`、`_pit`（打开）、`_search/point_in_time`（OpenSearch 的 PIT，打开和按 id 释放）、`_search/scroll`；`DELETE _pit`、按 id 的 `DELETE _search/scroll`（释放自己的上下文；`DELETE _search/scroll/_all` 和 `DELETE _search/point_in_time/_all` 会释放所有客户端的上下文，算**危险**）；`_ingest/pipeline/*/_simulate` |
| 写 | 文档的增、改、删（`_doc`、`_create`、`_update`、单条 `DELETE`）、`_bulk`（看 body 里有没有 delete，有就算危险，§9.2）、建索引、改 mapping、改索引 settings、`_open`、别名、`_refresh`、`_flush`、`_cache/clear`、`_reindex`、模板、pipeline、ILM 策略、建快照、建用户 / 角色、取消任务 |
| 危险 | 删除索引、`_close`、`_delete_by_query`、`_update_by_query`、`_forcemerge`、改集群 settings、删除模板 / pipeline / ILM 策略 / 快照 / 仓库 / 用户 / 角色、恢复快照、`_shrink` / `_split` / `_clone` / `_rollover` |
| 特别危险 | 目标里有通配（`*`）、`_all`、多个逗号分隔的索引、或者能匹配多个索引的别名的**删除索引 / 关闭 / `_delete_by_query`**；没有 query（或 `match_all`）的 `_delete_by_query` |

- **认不出来的请求一律算危险**（我的决定：比"POST 不认识就当查询"安全，反目标）。分级函数要有一个测试，列出 §6.3 接口表里所有接口，断言每个都有明确的分级。
- 需要看 body 的请求：`_bulk` 里只要有一行 `delete` 就按危险处理，确认框里写出各类操作的条数；`_aliases` 里有 `remove_index` 动作（会删除索引）时按删除索引处理（危险，目标有通配时特别危险）。

### 9.2 规则

| 情况 | 行为 |
|---|---|
| 只读连接，任何写 / 危险 / 特别危险 | 拒绝，提示用 `:set noreadonly` 临时解锁 |
| 非 prod 连接，写 | 文档编辑和单条增删、控制台里的写请求：按 `y` 确认（确认框显示将要发出的请求）。refresh / flush / 清缓存不确认（我的决定：无副作用的维护操作） |
| prod 连接，写 | 按 `y` 确认，确认框显示将要发出的请求（method、path、body 的前几行） |
| 任何连接，危险 | 输入对象名确认（索引名、快照名、用户名…）。`_delete_by_query` / `_update_by_query` 的确认框显示 `_count` 得到的条数 |
| 非 prod 连接，特别危险 | 先用 `_resolve/index`（7.9+；不支持时用 `_cat/indices`）把目标**展开成实际的索引列表**给作者看，然后输入**原始表达式**（比如 `logs-*`）确认。执行时用**展开后的具体索引名**发请求，不发通配（反目标：执行时比确认时多删） |
| prod 连接，特别危险 | **直接拒绝**，提示一个一个地操作（我的决定） |
| 没有 query 的 `_delete_by_query` | 当作"清空索引"处理：特别危险；确认框写明 `deletes ALL N documents in <index>` |

- 控制台 RunAll 里有多个写请求时，按最高的那个级别确认一次，确认框列出全部写请求。危险级以上的请求在 RunAll 里**每个单独确认**（我的决定：一次输入名字不能代表多个索引）。
- 文档编辑必须"正好作用于这一篇"：用 `if_seq_no` / `if_primary_term`（§5.6），这是 SQL 编辑"必须正好影响一行"在 ES 上的对应规则。

### 9.3 单一检查点

目的和 Kafka 的 `guardedKafka` 一样，但做法不同（见 §3.3）：`session` 里一个包装类型实现 `es.Doer`，**在 `Do` 里**按 §9.1 分级，检查只读状态，并记录审计日志（复用 `session/audit.go`，写和危险操作都记录：时间、连接、method、path、级别、结果；**不记录 body**，避免把文档内容和密码写进审计库）。`session.Manager.ES` 只返回这个包装后的 `Doer`，UI 拿不到底层客户端。确认由 UI 在调用前完成。

- 测试要证明没有绕过的路径：给包装类型一个假的底层 `Do`，**用包装后的 `Doer` 调用每一个类型化的写函数**（不只是直接调 `Do`），断言只读时底层 `Do` 一次都没被调到。写函数的清单要能在新增写函数时让测试失败（实现者决定用显式列表还是别的方式，参考 `kafkaWriteMethods`）。
- `es.Cursor`（翻页）只能发 §9.1 里的读请求，构造时就限定。

### 9.4 敏感信息

- 密码、API Key 不写日志、不显示；错误信息里如果包含 URL 里的凭据，显示前去掉。
- 请求日志（`--debug`）只记录 method、path、状态码、耗时，不记录 body 和 `Authorization` 头。
- 改用户密码的请求 body 不出现在结果区和历史里（控制台里手写的 `_security/user/*/_password` 请求，历史里把 body 换成 `<redacted>`，我的决定）。

## 10. 按键

- 所有新动作加进 `keymap/defaults.go`，带描述；which-key、help、hints 自动生成。UI 文字里用 `keyHint`，不写死按键名。
- 新增 keymap 上下文（名字由实现者决定），比如：ES 文档表格、ES 文档详情、ES 状态页、ES Settings 编辑、ES 控制台结果。文档表格包含 `gridBindings` 和 `filterEditBindings` 的通用动作。
- 能和 SQL / Redis / Kafka 共用的动作，**必须**用同一个 Action、同一个默认键：

| 场景 | 复用的动作 | 默认键 |
|---|---|---|
| 切换部分 | PrevSection / NextSection | `[` / `]` |
| 加载更多 | LoadMore；光标越过末行时自动加载 | `}` |
| 看详情 | CellDetail | `K` |
| 过滤 | FilterPrompt、`f` 系列 | `F`、`ff` `f!` `fs` `fx` |
| 排序 | SortColumn | `s` |
| 编辑 | EditCell / InsertRow / DeleteRow / UndoEdit / UndoAllEdits / SubmitEdits | `i` 回车 / `o` / `dd` / `u` / `U` / `<Space>w` |
| `$EDITOR` 编辑 | ExternalEdit | `<Space>v` |
| 复制 | `y` 前缀 | `yl` `yy` 等 |
| 按 ID 打开 | OpenKey | `<Space>k` |
| 跳到行指向的对象 | OpenCellKey | `gd` |
| 刷新 | Refresh | `<Space>r` |
| 打开控制台 | OpenConsole | `<Space>c` |
| 执行当前 / 全部 | RunStatement / RunAll | 回车 / `R` |
| 结果切换 | PrevResult / NextResult / ResultView | `H` / `L` / `[` `]` |
| 状态页 | ServerInfo | `<Space>i` |
| 树：删除 / 新建 / 操作菜单 | DeleteNode / NewKey / NodeActions | `dd` / `o` / `a` |
| 树：系统索引 / 排序 | ToggleInternal / SortGroups | `.` / `s` |
| 取消 | Cancel | `ctrl+c` |

- **连接类型**：help 和 which-key 按 `keymap.Kind`（`kind.go`）过滤按键，现在只有 SQL、Redis、Kafka 三种，`kindDescs` 是固定长度 3 的数组。要新增 `KindES`，把 `kindDescs` 扩成 4 项，并把 ES 复用的动作（`OpenKey`、`NewKey`、`FilterPrompt`、`FilterClear`、`ToggleInternal`、`SortGroups` 等，现在只标了 Redis 或 Kafka）加到 ES 的表里。不加的话键能用，但 help 里看不到。
- 复用的动作如果描述只写了 Redis 或 Kafka（比如 OpenKey 的 "Open a Redis key by name"、NewKey 的 "New Redis key"），用按 Kind 区分的描述（`kindDescs`）给 ES 单独的描述。
- 新增的动作：DeleteByQuery（§5.6）、复制成请求 / curl / `_id`（§5.5）、Reindex / UpdateByQuery（E2）等。默认键由实现者决定，不和同一上下文里的键冲突，并写进 help。

### 10.1 工作区恢复

工作区重新打开时（我的决定）：Index 标签页恢复索引、所在的部分和过滤条件（只是重新执行一次读查询），**不恢复**暂存的编辑；控制台恢复草稿，不自动执行。

## 11. 测试

- **纯包**（表驱动）：`esx` 的控制台文件解析、请求分级（§9.1 的每一类，包括 `_bulk` 里有 delete、认不出来的请求）、`f` 系列的转义和拼接、mapping 展平和默认列选择、单元格输入按类型解析、JSON 路径读写（编辑）、滚动索引分组规则、`_search` / `_cat` / 聚合响应转表格、补全上下文。
- **`es` 单元测试**（不需要 Docker）：用 `httptest.Server` 模拟 ES：版本探测、错误包装（401 / 403 / 404 / 409）、响应大小上限、多地址重试、拨号函数注入后所有请求都经过注入的函数、不做 sniffing。
- **`session` 测试**：只读时每个写方法都被拒绝且底层没收到请求；审计记录不含 body。
- **集成测试**（`integration` tag）：testcontainers 起 ES。镜像矩阵（我的决定）：**ES 7.10.2**（作者的生产版本，必须）、ES 8.x 最新版（开安全功能和 HTTPS，测 Basic 和 API Key）、ES 9.x 最新版、OpenSearch 2.x 最新版（只跑核心用例：浏览、搜索、编辑、控制台、索引管理）。测试：
  - 在 7.10.2 上写入 25,000 条文档，Documents 翻页翻过 10,000 条，无重复无遗漏；关闭后 PIT / scroll 被释放（`_nodes/stats` 里 open contexts 回到 0）。
  - 编辑时带 `if_seq_no`，并发修改后提交返回冲突、文档没被覆盖。
  - 通配删除展开后只删展开列表里的索引（在展开和执行之间新建一个匹配的索引，断言它没被删）。
  - 通过别名打开、编辑写到实际索引。
  - SSH 隧道（沿用 `tunnel` 包的内存 SSH 服务器）。
- **UI 测试**：`fakes_test.go` 加假的 `es.Client`，驱动 `Model.Update` 测：树的滚动索引分组和排序、`F` 过滤和语法错误提示、`ff` 拼接条件、`<Space>k` 按 ID 打开（单个索引 / 别名多命中）、自动加载更多和内存上限、暂存编辑和冲突提示、控制台执行当前 / 全部和分级确认、只读和 prod 的拦截、特别危险操作的展开确认和 prod 拒绝。
- **手工验证**：连作者的 7.10.2 内网集群（只读打开）和本地 Docker，按 §2 的主场景走一遍；关掉标签页后确认集群上没有残留的 scroll / PIT。

## 12. 交付顺序

E1 → E2 → E3，每期单独写实施计划（`docs/superpowers/plans/`），各自可以发布。版本号在发布时决定。

- **E1**：§3、§4（不含 Index Templates、Snapshots 节点）、§5、§6、§9、§10。
- **E2**：§7。
- **E3**：§8，各项之间互不依赖，可以分开做。

每期都要在 `CHANGELOG.md` 的 `[Unreleased]` 下加条目，README 中英文同步更新，主设计文档的架构图和安全规则一节加上指向本文的说明。

## 13. 留给实现者决定的

- `es` 接口的具体形状、类型化方法放在哪个包；`esx` 的包名；ES 节点怎么接入 `panes/tree`（新增 `Kind`）。
- 7.10 / 7.11 上的翻页方式（§5.4 的约束内）。
- 新增 keymap 上下文的划分和新动作的默认键（§10 的约束内）；复用动作的描述怎么改成通用。
- 滚动索引的识别规则、默认列的挑选细节、各种默认值（页大小、内存上限、响应大小上限、刷新间隔、超时）以及是否放进 `config.toml`。
- JSON 详情的折叠实现和按键；控制台文件扩展名；是否新增高亮语言定义。
- keyword 取值补全做不做（§5.3）。
- 钥匙串里 API Key 的命名方式。
