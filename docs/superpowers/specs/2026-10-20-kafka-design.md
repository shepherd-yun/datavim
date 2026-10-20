# Kafka 支持 设计文档

- 日期：2026-10-20
- 状态：待审阅
- 范围：新增 `kafka` 连接类型，分 3 期交付（K1 排查与日常操作、K2 Schema Registry 与进阶运维、K3 治理）；修订主设计 §3（架构）、§5.3（安全规则）

## 1. 背景

作者日常除了查 SQL 和 Redis，还要处理 Kafka。最常见的场景是**查消费积压**：

1. 找到 consumer group，看 lag，确定是哪个 partition 积压了。
2. 跳到这个 group 在这个 partition 上**卡住的 offset**，看那条消息。
3. 复制消息内容（key、value、headers）去排查。
4. 必要时**重置 offset 跳过**这条消息。

目标和 Redis 一样：在同一个终端工具里、用同一套 vim 键位完成这些事，不再切换到别的工具。

作者对这次的要求：

- **按键符合本项目的设计**：所有按键走 `keymap/defaults.go`，能和 SQL / Redis 共用的动作用同一个 Action、同一个键。
- **交互好用**：上面的排查流程要顺，每一步都是一两个键。
- **功能尽量全**：除了排查，日常的 topic、group、发消息、Schema Registry 操作都要覆盖，分期交付。

参考过的开源项目：ktea（Go + Bubble Tea v2，功能和交互）、kafui（安全 / 审计 / 脱敏设计）、kaskade（消费和解码选项）。**它们只作参考**，界面、按键、功能取舍都按本项目自己的设计来，不照搬。

项目将改名为 **datavim**。改名是一个单独的改动（`refactor!`），不和 Kafka 混在一起；本文仍用 sqlvim 指代本项目。

## 2. 目标、非目标、反目标

**目标**

- Kafka 连接和 SQL / Redis 连接在**同一个连接列表、同一棵树**里，共用连接表单、钥匙串密码、SSH 隧道、workspace、`env = "prod"` 标记和只读开关。
- §1 的排查流程：从树上看到哪个 group 积压 → 打开 group → 一键跳到卡住的消息 → 看详情、复制 → 重置 offset。
- 浏览消息时**对集群零副作用**：不加入任何 consumer group，不提交 offset，不自动创建 topic。
- 大 topic（几亿条消息、单条几 MB）上浏览也不卡界面、不爆内存。
- 现有安全规则在 Kafka 上有对应的规则（§8）：只读、prod 确认、危险操作需要输入名字确认、重置 offset 必须正好作用于预期的 partition。
- 现有的 SQL / Redis 行为不变。

**非目标（整个 Kafka 系列）**

- JMX 指标（吞吐、请求延迟、磁盘用量趋势）。状态页只显示 Kafka 协议能拿到的信息（§7.2）。
- ksqlDB、Kafka Streams 拓扑、MirrorMaker。
- 事务 producer、幂等以外的 EOS 语义；消费端的事务隔离级别只提供 `read_committed` / `read_uncommitted` 开关（K2）。
- 集群管理：broker 上下线、分区重分配（reassign）、leader 选举、KRaft 元数据操作。
- quota 管理。
- Kafka 以外的消息系统（RabbitMQ、Pulsar、RocketMQ）。
- 用 SQL 查询 Kafka 消息。

**反目标**（技术上能用，但这样就算失败）

- 浏览消息时加入了 consumer group 或提交了 offset，导致生产消费者 rebalance 或者消费进度被改。
- 发消息或读元数据时，因为 topic 名打错而**自动创建**了 topic。
- 打开一个大 partition、开着 live tail 放一晚上，导致 sqlvim 内存一直涨。
- 重置 offset 时作用到了别的 group、别的 partition，或者在消费者还在线时"成功"了一半。
- 只读或 prod 连接上，通过某条没被拦截的路径执行了写操作（发消息、删 topic、改配置、重置 offset）。
- SASL 密码、Schema Registry 凭据、OAuth token 出现在日志或界面上。
- 经过 SSH 隧道时，客户端悄悄绕过隧道，直连 broker 返回的内网地址。

## 3. 架构

### 3.1 平行于 `db` 和 `rdb`

Kafka 没有表、列、语句，和 Redis 一样走平行的路：

```
ui ──→ session ──→ db    ──→ mysql / postgres / sqlserver / oracle
  │          ├───→ rdb   ──→ go-redis
  │          └───→ kafka ──→ franz-go (kgo / kadm)
  └──→ kmsgx（纯包）
```

- **`internal/kafka`**（新）：Kafka 和 sqlvim 之间唯一的接口，地位相当于 `internal/db` / `internal/rdb`。UI 不直接引用 franz-go。
- **`internal/kmsgx`**（新，纯包，不碰终端和网络）：消息解码（text / JSON / hex / base64，K2 加 Schema Registry 线格式）、起始位置表达式解析（`earliest`、`-100`、`@2026-10-20 09:12`、`p1:1180002` 等，§5.3）、producer 草稿格式的解析和生成（§6）、重置 offset 的目标计算和预览（§5.6）、消息过滤表达式、写操作分级（§8.1）。地位相当于 `sqlx` / `rcmd`。包名由实现者决定，避开 franz-go 的 `kmsg`。
- 依赖方向不变：`ui` → `session` / `kmsgx` → `kafka` → franz-go。

### 3.2 客户端库

`github.com/twmb/franz-go`（`pkg/kgo` 客户端、`pkg/kadm` 管理 API、`pkg/kfake` 内存假集群）。我的选择，理由：

- `kgo.Dialer(func(ctx, network, host) (net.Conn, error))` 可以注入拨号函数。SSH 隧道直接注入，**所有 broker 连接**（包括 metadata 返回的 advertised 地址）都走同一条隧道，不监听本地端口。这和 `db.DialFunc` 的用法一致。
- `kgo.ConsumePartitions` 可以直接指定 partition 和 offset 读取，不加入 group；配合 `kgo.ConsumeResetOffset(kgo.NoResetOffset())`，offset 越界时报错而不是悄悄跳到别处。
- `kadm` 提供 `Lag`、`DescribeGroups`、`FetchOffsets`、`CommitOffsets`、`ListStartOffsets` / `ListEndOffsets`、按时间查 offset、`CreateTopics`、`DeleteTopics`、`CreatePartitions`、`AlterTopicConfigs`、`DeleteRecords`、ACL 等，覆盖本文所有管理操作。
- SASL：`sasl/plain`、`sasl/scram`（SHA-256 / SHA-512）、`sasl/oauth`、`sasl/aws`（MSK IAM）都是官方子包。K1 只做 PLAIN 和 SCRAM（作者的决定），OAuth 和 MSK IAM 放到 K2（§7.5）。
- `kfake` 可以在进程内起一个假集群，单元测试不需要 Docker。

实现时用 Context7 查当前文档，确认上面这些 API 的确切签名（特别是按时间查 offset 的函数名）。

**客户端必须关闭自动创建 topic**（`kgo.AllowAutoTopicCreation` 不开启，保持默认关闭），并有测试覆盖（反目标）。

### 3.2.1 版本和发行版

作者的集群各种都可能有：自建 Apache Kafka 的不同版本、Confluent、Amazon MSK、Redpanda。所以策略是（我的决定）：

- **支持范围**：Apache Kafka **2.8 及以上到 4.x**，以及兼容 Kafka 协议的 Confluent Platform / Confluent Cloud、MSK、Redpanda。低于 2.8 的版本不保证。
- 连接时用 ApiVersions 记下 broker 支持的请求和版本，**按能力判断，不按版本号判断**。某个功能需要的请求 broker 不支持时，只禁用这个功能并显示一行原因（比如 `not supported by this cluster`），其他功能照常可用。
- **两种 consumer group 都要支持**：
  - 经典 group（`DescribeGroups`）：所有版本都有。
  - 新协议 group（KIP-848，Kafka 4.0 起正式可用，用 `ConsumerGroupDescribe`）：broker 支持时，`ListGroups` 按类型列出，新协议 group 用 `ConsumerGroupDescribe` 取成员和分配。树和 Group 标签页里标明 group 的类型（classic / consumer），其他交互完全一样。
  - "group 必须是 Empty 才能重置 offset"（§5.8）对两种 group 都适用，各自按自己的状态字段判断。判断不出状态（比如 share group 或未知类型）时**拒绝重置**，不猜。
  - share group（KIP-932）、Connect 的 group 只列出名字和类型，不提供 lag 和重置（非目标）。
- Redpanda 不支持的管理请求（以实际测试为准）按上面"按能力禁用"的规则处理。
- 集成测试的镜像见 §10。

### 3.3 `kafka` 包的接口（草案，实现者可以调整）

| `db` / `rdb` | `kafka` | 说明 |
|---|---|---|
| `Conn` / `rdb.Client`（池化，并发安全） | `kafka.Client` | 一个连接一个 `kgo.Client` + `kadm.Client`，供元数据、管理操作、发消息使用 |
| `Session` | `kafka.Reader` | **每个消息视图一个**，内部是一个只做 `ConsumePartitions` 的独立 `kgo.Client`，随标签页关闭而关闭 |
| `Database` | 无 | Kafka 没有库的概念，一个连接对应一个集群 |

`Client` 至少提供：

- `Cluster(ctx)`：cluster ID、controller、broker 列表（id、host:port、rack）、协议版本（ApiVersions 推断出的 Kafka 版本范围）。
- `Topics(ctx)`：topic 名、partition 数、副本因子、是否内部 topic；`TopicDetail(ctx, topic)`：每个 partition 的 leader、replicas、ISR、offline replicas、start / end offset。
- `TopicConfigs(ctx, topic)`：配置项、当前值、是否默认值、来源、是否敏感。
- `Groups(ctx)`：group 名、状态、协议类型、成员数；`GroupDetail(ctx, group)`：成员（member id、client id、host、分到的 partition）、每个 partition 的 committed / end / lag。
- `GroupsLag(ctx, groups...)`：批量算 lag，供树和 topic 的 Consumers 部分使用。
- `OffsetsAt(ctx, topic, partitions, ts)`：按时间查 offset。
- `Produce(ctx, records)`：同步发送，返回每条的 partition、offset、时间戳或错误。
- 写操作（§8.1 列出的全部）。**所有写操作只能通过一个检查点调用**（§8.3）。

`Reader` 至少提供：

- `Seek(ctx, positions)`：按 partition 设置起始 offset。
- `Fetch(ctx, max int)`：拉取最多 `max` 条，返回原始 `[]byte` 的 key、value、headers 以及 partition、offset、时间戳、时间戳类型、leader epoch。
- `Tail(ctx) <-chan Batch` 或等价的方式，供 live tail 使用（§5.4）。

key、value、header 一律用 `[]byte` 原样保存，**不做编码转换**。解码只在显示时进行（§5.5），复制时按用户选的格式输出。

错误：连接断开、认证失败、权限不足（`TOPIC_AUTHORIZATION_FAILED`、`GROUP_AUTHORIZATION_FAILED`、`CLUSTER_AUTHORIZATION_FAILED`）要包装成可识别的哨兵错误。权限不足只影响对应的部分，显示一行原因，不让整个标签页打不开（和 SQL 状态页一致）。

### 3.4 配置和会话层

`config.Connection` 不改结构，只增加 `driver = "kafka"`：

| 字段 | Kafka 的含义 |
|---|---|
| `host` | bootstrap broker 列表，逗号分隔，每项可以带端口：`b1:9092,b2:9092`；不带端口时用 `port` |
| `port` | 默认 `9092`（加进 `defaultPorts`） |
| `user` | SASL 用户名；留空表示不用 SASL（除非是 MSK IAM / OAuth） |
| `database` | 不用 |
| `ssh` | 和现有一样 |
| `params` | `sasl`（K1：`plain` / `scram-sha-256` / `scram-sha-512`；K2 加 `oauth` / `aws-msk-iam`）、`tls = "true"`、`tls_skip_verify`、`tls_ca`、`tls_cert`、`tls_key`（文件路径）、`aws_region`、`aws_profile`、`oauth_token_url`、`oauth_client_id`、`oauth_scope`、`schema_registry_url`、`schema_registry_user`、`client_id`（默认 `sqlvim`）、`show_internal = "true"` |

- 密码：SASL 密码、OAuth client secret、Schema Registry 密码都进钥匙串，**不写进 toml**。钥匙串里一个连接需要存多个秘密，key 的命名方式由实现者决定（比如 `<连接名>`、`<连接名>#schema-registry`）。
- 秘密怎么输入（我的决定）：SASL 密码用连接表单现有的 Password 字段，和 SQL / Redis 一样。其他秘密（K2 的 Schema Registry 密码、OAuth client secret）不加表单字段，**第一次需要时弹出输入框**，输入后存进钥匙串，和现有的 `ErrPasswordRequired` 流程一致；认证失败时重新询问。
- MSK IAM（K2）：凭据走 AWS 标准凭据链（环境变量、`~/.aws`、SSO），sqlvim 不保存 AWS 密钥。
- 连接表单：驱动选 kafka 时，`Host` 标签显示为 `Brokers`，`Database` 隐藏，增加 SASL 机制和 TLS 的选项。Schema Registry 和 TLS 文件路径是否放进表单由实现者决定，至少要能在 `params` 里配。
- `session.Manager` 增加 `Kafka(ctx, name) (kafka.Client, error)` 和 `KafkaReader(ctx, name) (kafka.Reader, error)`，`ui.Backend` 增加同名方法。密码、隧道、`Reset`、`SetReadOnly` 走现有逻辑。
- **隧道和 advertised 地址**：通过 SSH 隧道连接时，broker 返回的 advertised 地址由跳板机去解析和连接，所以跳板机必须能访问这些地址。在 README 里写明这一点。如果作者的环境里需要把 advertised 地址改写成别的地址，再加一个 `params.broker_map`（K2，实现者决定格式）。
- `Makefile` 的 `test-integration` 要把 `./internal/kafka/...` 加进去。

## 4. 树

```
▾ prod-kafka  [prod]
  ▸ Brokers (3)
  ▾ Topics (128)
      orders            12p
      refunds            3p
    ▸ …
  ▾ Consumer Groups (41)
      order-svc         lag 48,188   ← 有积压，标红
      billing-svc       lag 0
      notify-svc        lag 12        · Empty
  ▸ Schema Registry (57)             ← 只有配置了 schema_registry_url 时出现（K2）
```

- 连接展开后显示四个固定节点：Brokers、Topics、Consumer Groups、Schema Registry（K2，没有配置时不显示）。
- **Topics**：一次拿到全部 topic 的元数据（Kafka 的 topic 数量通常在几千以内，不需要像 Redis 那样分批扫描）。内部 topic（`__consumer_offsets`、`__transaction_state`、`_schemas` 等）默认隐藏，`params.show_internal` 或一个切换动作可以显示。topic 名按 `.` / `-` / `_` 分组成文件夹（我的决定：和 Redis 按 `:` 分组一致；只有一层分组，单子节点不分组，具体规则由实现者决定）。
- topic 节点显示 partition 数；有副本不足（ISR < replicas）或离线 partition 时标红。
- **Consumer Groups**：显示 group 名、总 lag、状态（`Stable` 不显示，`Empty` / `Rebalancing` / `Dead` 显示）。lag 在展开节点时批量计算，结果缓存，手动刷新（现有 Refresh 动作）时重算。group 很多（超过 500 个，阈值可调）时，先只列出名字，lag 等节点可见时再懒加载。lag 大于 0 的用主题色里的警告色，排序默认按 lag 从大到小（我的决定：服务于"找积压"这个首要场景），可以切换成按名字排序。
- **Brokers**：每个 broker 一行：id、host:port、rack，controller 标一个记号。
- 树上的 `/` 保持现有的"在已加载节点里查找"；`FilterPrompt` 在 Topics / Groups 节点上按名字过滤（和 Redis 树的过滤一致）。
- topic 和 group 出现在全局搜索（`<Space><Space>`）里（我的决定：数量有限，而且"按名字直接跳到 topic / group"是高频操作）。
- 在 topic 节点上回车打开 Topic 标签页，在 group 节点上回车打开 Group 标签页。

## 5. K1：排查与日常操作

### 5.1 Topic 标签页

标题是 topic 名，多次打开同一个 topic 复用已有标签页。分成几个部分，用现有的 `[` / `]`（PrevSection / NextSection）切换，和服务器状态页、结构页一致：

| 部分 | 内容 |
|---|---|
| **Messages** | 消息列表（§5.2） |
| **Partitions** | partition、leader、replicas、ISR、start offset、end offset、消息数（end − start，压缩 topic 上是近似值，标明 `≈`）。ISR 不足或 leader 离线的行标红 |
| **Config** | 所有配置项：名字、值、来源（default / topic / broker），非默认值高亮，敏感配置不显示值。K1 只读，K2 可以改（§7.1） |
| **Consumers** | 正在消费这个 topic 的 group：group、状态、每个 partition 的 committed / lag，总 lag。在一行上回车打开那个 group 的标签页 |

头部一行：`12 partitions · RF 3 · 1.2 M msgs · retention 7d · cleanup delete`。

### 5.2 Messages 部分

```
[orders]  Messages  Partitions  Config  Consumers          from: latest 200 · all partitions   live: off
  PART  OFFSET      TIME                   KEY          SIZE   VALUE
  1     1,179,999   10-20 09:12:01.220     ord-88121    412B   {"id":88121,"amount":99,...}
▶ 1     1,180,002   10-20 09:12:01.410     ord-88124    2.1M   {"id":88124,"items":[{"sku":...
```

- 表格复用 `panes/grid`。列：partition、offset、时间（本地时区，到毫秒）、key、大小、value 的单行预览；headers 列默认隐藏，可以显示（实现者决定怎么切换）。value 预览按 §5.5 解码后截断。
- **默认起始位置**：所有 partition 合起来最新的 N 条（N 默认 200，可以配置），按时间排序。我的决定：打开 topic 最常见的意图是"看看最近有什么"。
- 多个 partition 合在一起时按时间戳排序；只看一个 partition 时按 offset 排序。
- **加载更多是自动的**（我的决定）：光标移到表格最下面一行再往下移时，向更新的方向加载 N 条；移到最上面一行再往上移时，向更旧的方向加载 N 条。加载时状态栏显示进度。没有更多消息时（到了 start 或 end offset）提示 `beginning of partition` / `end of partition`。`[` / `]` 在 Topic 标签页里统一用来切换部分（§5.1），Messages 里也一样，不用来加载。
- **内存上限**：一个 Messages 视图最多保留 10,000 条（可以配置）。超过时丢弃离光标最远的一端，并在状态栏提示。单条消息的 value 超过 1 MB（可以配置）时，表格和详情只保存前 1 MB，标明 `showing first 1 MB of N`，复制时提示被截断（我的决定：和 Redis 大 String 的处理一致）。
- 现有表格动作照常可用：`K` 看详情（§5.5）、`y` 系列复制、导出（复用 `export`，加一种 JSONL 格式，每行一条消息，包含 partition、offset、时间、key、headers、value）。

### 5.3 跳转和起始位置

一个"跳转"动作（我的决定：复用 SQL 树/表格里"跳转到位置"类的现有 Action，没有合适的就新增 `Seek`；默认键由实现者决定，不和现有键冲突）弹出输入框，输入一个起始位置表达式，`kmsgx` 负责解析，表驱动测试覆盖：

| 输入 | 含义 |
|---|---|
| `latest` / 空 | 每个 partition 最新的 N 条 |
| `earliest` | 每个 partition 的开头 |
| `-500` | 每个 partition 从末尾往前 500 条 |
| `1180002` | 当前选中 partition（或唯一的 partition）的绝对 offset |
| `p1:1180002` | partition 1 的 offset 1180002；可以写多个，空格分隔 |
| `p1` / `p1,p3` | 只看这些 partition（从最新开始） |
| `@2026-10-20 09:12` / `@09:12` / `@-15m` | 每个 partition 上时间戳不早于这个时间的第一条 |

- 输入框里显示解析结果的预览（比如 `p1 @ 1,180,002 (start 1,002,113 · end 1,228,190)`），offset 越界时直接提示，不发请求。
- offset 已经被 retention 删掉（小于 start offset）时：提示 `offset 120 is gone; earliest is 1,002,113`，并提供跳到 earliest 的选项，**不悄悄换位置**。
- 跳转后光标停在目标消息上，前面带几条上下文（默认 3 条）。

### 5.4 Live tail

- 一个切换动作开关 live tail（默认键由实现者决定）。开启后从当前末尾开始持续接收新消息，追加到表格底部；光标在最后一行时跟随滚动，用户往上移动光标后不再跟随（和 `tail -f` 加 less 的手感一致），状态栏显示 `live · +1,240 new`。
- 同一个切换键暂停 / 继续；暂停期间不接收（不在后台堆积）。
- 内存上限同 §5.2：超过上限时丢弃最旧的消息。
- 标签页不可见时自动暂停接收，回到标签页后继续，并提示中间可能漏掉的消息数（根据 end offset 估算）。我的决定：避免后台标签页一直占用内存和网络。

### 5.5 消息详情和解码

在消息上按 `K`（现有的 CellDetail）打开详情：

```
orders · p1 · offset 1,180,002              ts 2026-10-20 09:12:01.410 (CreateTime)  size 2.1M
key      ord-88124
headers  trace-id=7f3a…  source=checkout   (2)
──────────────────────────────────────────
{
  "id": 88124,
  ...
```

- 显示格式：`auto`（默认）/ `text` / `json` / `hex` / `base64`，用现有的 ValueFormat 动作（`v`）循环切换，和 Redis 一致。key 和 value 分别判断。
  - `auto`：合法 UTF-8 且是 JSON 对象或数组时格式化加高亮；合法 UTF-8 但不是 JSON 时按文本；否则按 hex。
  - K2 加 Schema Registry：value（或 key）以 Confluent 线格式（第一个字节 0，后跟 4 字节 schema id）开头时，`auto` 自动用 Schema Registry 解码（§7.3）。
- 复制（复用 `y` 前缀，具体第二个键由实现者决定，不和现有的 `yy` / `yl` 冲突）：整条消息（JSON：partition、offset、时间、key、headers、value）、只复制 value、只复制 key、只复制 headers。复制的是当前显示格式的内容；hex / base64 下复制的就是 hex / base64 字符串。
- 详情里可以用现有的上一条 / 下一条动作切换消息（实现者决定复用哪个 Action）。
- 一个动作把 value 在 `$EDITOR` 里只读打开（复用 ExternalEdit 的机制，写到临时文件），方便看很大的 JSON。

### 5.6 过滤和查找

Kafka 服务器不支持按内容过滤，所有过滤都在客户端完成。分两种：

- **过滤已加载的消息**：复用现有表格的 `f` 系列（`ff` 等于这个值、`fs` 包含、`f!` 不等于、`fx` 清除）和 `F`（输入过滤表达式）。表达式由 `kmsgx` 解析，支持 `key`、`value`、`header.<名字>`、`partition`、`offset`、`ts`、`size` 这几个字段，运算符 `=`、`!=`、`~`（包含）、`>`、`<`，`and` / `or`；value 是 JSON 时可以用 `value.<路径>`（如 `value.user.id = 42`）。具体语法由实现者定，但要和数据表格的过滤手感一致，并在帮助里写明。
- **向后查找**：在过滤条件下按"查找下一个"（复用 `n` / `N` 或新增动作，实现者决定）时，如果已加载的消息里没有匹配，就继续向后拉取并过滤。停止条件（我的决定，和 Redis 的 SCAN 过滤一样按"扫描数"封顶）：找到 1 条匹配，或者扫描了 100,000 条（可以配置），先到为准。状态栏显示扫描进度，可以用取消动作停止。

### 5.7 Consumer group 标签页

标题是 group 名。部分：

| 部分 | 内容 |
|---|---|
| **Lag** | topic、partition、committed、end、lag、当前分到的 member。默认按 lag 从大到小排序。头部一行：`state Stable · members 3 · protocol range · total lag 48,212` |
| **Members** | member id、client id、host、分到的 partition 列表 |

- 自动刷新：Lag 部分每 5 秒刷新一次（可以配置，0 表示关闭），标签页不可见时停止。lag 比上次增大的行有一个上升记号。我的决定：查积压时作者最关心 lag 是在涨还是在降。
- **跳到卡住的消息**：在 Lag 的一行上按 `gd`（复用 Redis 里"打开单元格里的 key"的 OpenCellKey 动作，含义都是"跳转到这一行指向的对象"），打开这个 topic 的标签页，Messages 部分定位到这个 partition 的 committed offset，光标停在这条消息上，前面带 3 条上下文。标题旁显示 `from group order-svc`。committed offset 等于 end（没有积压）时，提示 `no lag on this partition` 并定位到最后一条。
- 在 Messages 里通过 `gd` 进来时，再按一次 `gd` 回到来源的 group 标签页（实现者决定用 `gd` 还是现有的"返回"动作）。
- group 没有 committed offset 的 partition（从来没提交过）：committed 显示 `-`，lag 按 end − start 显示并标明。

### 5.8 重置 offset

在 Lag 部分按 `R`（新增 ResetOffsets 动作，Group 上下文默认绑 `R`；`R` 在控制台里是 RunAll，不同上下文不冲突）打开重置对话框：

```
┌ Reset offset · group order-svc ──────────────────────────────┐
│ scope   orders p1            (all partitions of orders / all topics)
│ to      next  (1,180,003)    earliest / latest / offset / time / shift ±N / next
│ preview orders p1  1,180,002 → 1,180,003   lag 48,188 → 48,187
│ group has 3 active members — stop the consumers first
│ prod: type the group name to confirm  [            ]
└──────────────────────────────────────────────────────────────┘
```

- **范围**：光标所在的 partition（默认）、这个 topic 的所有 partition、这个 group 的所有 topic。VISUAL 选中多行时就是选中的这些 partition。
- **目标**：`next`（committed + 1，就是"跳过这条"，默认值，我的决定：对应作者说的"跳过卡住的消息"）、`earliest`、`latest`、指定 offset、指定时间、往前或往后移 N 条。
- **预览**：每个 partition 显示 `旧 committed → 新 committed` 和 lag 变化。目标超出 [start, end] 时自动夹到边界，并在预览里标出来。预览计算放在 `kmsgx`，表驱动测试。
- **group 必须是 Empty 状态**。Kafka 不允许给有活跃成员的 group 提交 offset，所以 group 不是 Empty 时，对话框显示成员数和 `stop the consumers first`，确认按钮不可用（我的决定：不提供"强制"选项，因为强制也会失败，或者被在线的消费者马上覆盖）。确认前**重新读一次** group 状态和 committed offset，和预览时不一致（状态变了，或者 committed 被别人改了）就放弃，提示 `group changed since preview; review again`，这是 SQL 编辑"必须正好影响一行"在 Kafka 上的对应规则。
- 执行用 `kadm` 的 `CommitOffsets`，只提交预览里列出的 partition。执行后报告每个 partition 的结果，部分失败时列出失败的 partition 和原因，不重试。
- 安全规则见 §8.2：只读拒绝；prod 要输入 group 名；非 prod 按 `y`。

### 5.9 发消息（producer console）

在 topic 节点或 Topic 标签页上，用现有的打开控制台动作（`<Space>c`）打开这个 topic 的 producer console。编辑器复用 `vimedit`。

**草稿格式**（我的决定：一个消息就是一个"语句"，这样执行当前消息、执行全部的手感和 SQL 控制台一致）：

```
# topic: orders            ← 注释行；topic 由标签页决定，这行只是提示
key: ord-1
partition: 2               ← 可选；不写就按 key 的哈希分配
header: trace-id=abc123
header: source=sqlvim

{"id": 1, "amount": 99}
===
key: ord-2
{"id": 2, "amount": 12}
```

- 一条消息由头部（`key:`、`partition:`、`header:`，都可选）、一个空行、正文组成；正文直到下一个 `===` 行或文件末尾。没有头部时整段都是正文。`key: null` 表示 key 为 null（和空字符串区分），正文只有 `null` 表示 tombstone（value 为 null）。精确语法由实现者定，解析放在 `kmsgx`，表驱动测试，解析错误要指出行号。
- 执行：光标所在的消息用 RunStatement（回车），全部消息用 RunAll（`R`），和 SQL 控制台同一组动作。VISUAL 选中多条时按顺序发送。执行前先解析全部，有任何一条解析失败就一条都不发。
- 发送是同步的：等 broker 确认后，在结果区显示每条的 `partition · offset · 时间`，失败的显示原因（序列化、权限、超时、消息过大、topic 不存在）。**topic 不存在时报错，不创建**（反目标）。
- 正文是 JSON 时，执行前检查格式；不合法时提示，用户确认后可以按原文发送（实现者决定交互）。
- 草稿自动保存，复用控制台的草稿机制，按连接 + topic 区分。
- K1 的序列化：key 和 value 都按正文原样作为 UTF-8 字节发送。K2 加 Schema Registry 序列化和 hex / base64 正文（§7.3）。
- 安全规则见 §8.2。

### 5.10 Topic 管理

- **新建 topic**：在 Topics 节点上用现有的新建动作（实现者决定复用哪个），弹出表单：名字、partition 数、副本因子、常用配置（`retention.ms`、`cleanup.policy`、`min.insync.replicas`），可以加任意配置项。复用 `ui/overlay` 的表单。
- **删除 topic**：在 topic 节点上用现有的删除动作（Redis 删 key 用的 DeleteNode）。按 §8.2 的危险操作确认：**任何连接都要输入 topic 名**（我的决定：删 topic 不可恢复，和删表同级）。
- 删除前对话框显示：partition 数、消息数、正在消费它的 group 数。

### 5.11 状态页

在连接节点上用现有的 ServerInfo 动作（`<Space>i`）打开，复用 `internal/ui/servertab.go` 的框架。部分：

| 部分 | 内容 |
|---|---|
| **Status** | cluster ID、controller、broker 数、Kafka 版本范围、topic 数、partition 总数、副本不足的 partition 数、离线 partition 数、group 数、有积压的 group 数 |
| **Brokers** | 每个 broker：id、host:port、rack、是否 controller、是这个 broker 当 leader 的 partition 数 |
| **Under-replicated** | 副本不足和离线的 partition 列表：topic、partition、leader、replicas、ISR。回车打开那个 topic 的 Partitions 部分 |

Status 每 10 秒刷新一次（可以配置），标签页不可见时停止。不画走势图（没有 JMX 指标，非目标）。

### 5.12 只读

K1 里的写操作有：发消息、新建 topic、删除 topic、重置 offset。它们都要经过 §8 的检查。只读和 prod 标记的显示和 SQL / Redis 连接一致。

## 6. 按键

- 所有新动作都加进 `keymap/defaults.go`，带描述；which-key、help、hints 自动生成。UI 文字里用 `keyHint`，不写死按键名。
- 新增 keymap 上下文（名字由实现者决定）：Kafka 消息表格、Kafka group、Kafka producer console。消息表格和 group 表格要包含 `gridBindings` 的通用表格动作（移动、复制、过滤、`K`）。
- 能和 SQL / Redis 共用的动作，**必须**用同一个 Action、同一个默认键：

| 场景 | 复用的动作 | 默认键 |
|---|---|---|
| 切换部分 | PrevSection / NextSection | `[` / `]`（Topic、Group、状态页的所有部分里都是这个含义，包括 Messages） |
| 加载更多 | 无单独按键 | Messages 里光标越过首行 / 末行时自动加载（§5.2） |
| 看详情 | CellDetail | `K` |
| 切换显示格式 | ValueFormat | `v` |
| 过滤 | `f` 系列、FilterPrompt | `ff` `fs` `f!` `fx`、`F` |
| 复制 | YankCell / YankRow 和 `y` 前缀 | `yl` `yy` 等 |
| 跳到行指向的对象 | OpenCellKey | `gd` |
| 刷新 | Refresh | `<Space>r` |
| 打开控制台 | OpenConsole | `<Space>c` |
| 执行当前 / 全部 | RunStatement / RunAll | 回车 / `R`（producer console 里） |
| 状态页 | ServerInfo | `<Space>i` |
| 删除节点 | DeleteNode | 和 Redis 删 key 相同 |
| 取消 | Cancel | `ctrl+c` |

- 新增的动作：重置 offset（Group 上下文默认 `R`）、跳转（§5.3）、live tail 开关（§5.4）。默认键由实现者决定，要求不和同一上下文里现有的键冲突，并写进 help。

### 6.1 工作区恢复

工作区重新打开时（我的决定）：Topic 标签页只恢复 topic 和当时所在的部分，Messages 回到默认起始位置（最新 N 条）。**不恢复** live tail、跳转位置、"来自哪个 group"的关联，避免一打开工作区就在 prod 集群上自动开始消费。Group 标签页恢复 group 和部分；producer console 恢复草稿（草稿本来就会自动保存），但不会自动发送。

## 7. K2：Schema Registry 与进阶运维

### 7.1 修改 topic 配置和 partition

- Config 部分可以修改：在配置项上用 EditCell（`i` / 回车）改值，改动先暂存并标记，用 SubmitEdits（`<Space>w`）一次提交，撤销用 UndoEdit / UndoAllEdits。和数据表格的编辑手感一致。可以把配置项恢复为默认值（删除覆盖）。
- 提交前弹出确认，列出每项的旧值 → 新值。用 `kadm` 的增量修改（incremental alter），**只提交改动的项**，不能把其他配置覆盖成默认值（反目标的一种）。
- 增加 partition：Partitions 部分里的一个动作，输入新的总数（只能增加）。确认框提示"按 key 分配的消息会改变 partition"。

### 7.2 清理消息和删除 group

- **删除某个 offset 之前的消息**（DeleteRecords）：在 Partitions 部分或 Messages 里某条消息上触发，范围是一个 partition 或全部 partition，目标是指定 offset 或"到当前光标这条"。危险操作，要输入 topic 名。
- **删除 consumer group**：在 group 节点上用 DeleteNode。group 必须是 Empty。危险操作，要输入 group 名。
- **删除 group 在某个 topic 上的 offset**：在 Lag 部分的一个动作。group 必须是 Empty，或者不再订阅这个 topic。危险操作。

### 7.3 Schema Registry

配置了 `schema_registry_url` 之后启用。兼容 Confluent Schema Registry API（Apicurio 的 Confluent 兼容模式、Redpanda 的 Schema Registry 也走这个 API；Apicurio 原生 API 不做）。

- **解码**：消息以 Confluent 线格式开头时，按 schema id 取 schema（缓存，按连接区分），支持 Avro、Protobuf、JSON Schema。解码后按 JSON 显示，详情里显示 `schema id · subject · version · 类型`。schema 取不到或解码失败时，按 hex 显示原始字节，并在旁边显示错误，**不影响其他消息**。Avro 和 Protobuf 的解码库由实现者选（纯 Go；Protobuf 需要支持从 Registry 拿到的 schema 文本动态解析，包括引用的其他 schema）。
- **树**：Schema Registry 节点下是 subject 列表，显示最新版本号和类型；展开显示各版本。
- **Schema 标签页**：显示 schema 内容（格式化、高亮）、版本、id、兼容级别、引用。可以对比两个版本（文本 diff，实现者决定是否复用 `schemadiff` 的显示）。
- **写操作**：注册新版本（在 `$EDITOR` 里编辑 schema 文本，提交前先调用兼容性检查接口并显示结果）、修改兼容级别、软删除版本。都按 §8 的写操作处理；硬删除不做。
- **发消息时的序列化**：producer console 的头部可以写 `value-schema: <subject>[@版本]`（以及 `key-schema:`），正文是 JSON，发送前按 schema 序列化成线格式。序列化失败时指出原因，不发送。

### 7.4 重发消息和死信队列

- 在 Messages 里选中一条或多条消息，用一个动作"复制到 producer console"：打开（或切到）目标 topic 的 producer console，把这些消息按 §5.9 的草稿格式追加进去，保留 key、headers、value。目标 topic 默认是当前 topic，可以选别的 topic（我的决定：死信队列回放最常见的做法是从 DLQ topic 读出来，发回原 topic）。
- 二进制的 key / value 在草稿里写成 `key-hex:` / `value-hex:` 或 base64（实现者定语法），保证原样发出去。Schema Registry 线格式的消息默认按原始字节重发（不重新序列化）。
- 批量发送：producer console 里一个命令从 JSONL 文件读入消息（格式和 §5.2 的 JSONL 导出一致，所以导出的文件可以直接发回去）。条数超过 1,000（可以配置）时，先预览条数和前几条，再确认。

### 7.5 其他

- 消费的隔离级别：Messages 头部可以切换 `read_committed`（默认）/ `read_uncommitted`。
- `params.broker_map`（§3.4），如果作者的环境需要。
- **OAuth 和 AWS MSK IAM 认证**（作者的决定：从 K1 挪到 K2）：
  - OAuth：`sasl = "oauth"`，sqlvim 用 client credentials 流程向 `oauth_token_url` 取 token，token 快过期时自动刷新。client secret 按 §3.4 存钥匙串。token 不写日志。
  - MSK IAM：`sasl = "aws-msk-iam"`，凭据走 AWS 标准凭据链（`aws_profile`、`aws_region`），凭据过期时用 franz-go 的回调重新取。sqlvim 不保存 AWS 密钥。这一项没法用容器做集成测试，用单元测试覆盖配置组装，再连作者的 MSK 手工验证。

## 8. 安全规则（修订主设计 §5.3）

Kafka 服务器端有 ACL，但那是服务器配置，sqlvim 不改。和 Redis 一样，sqlvim 在**客户端这一层**做保守的拦截。README（中英文）建议生产集群使用只有读权限的 ACL 账号。

### 8.1 写操作分级（`kmsgx`）

| 级别 | 操作 |
|---|---|
| 读 | 元数据、配置、group 详情、lag、浏览消息、Schema Registry 读取 |
| 写 | 发消息、新建 topic、修改 topic 配置、增加 partition、重置 offset、注册 schema、修改兼容级别 |
| 危险 | 删除 topic、删除 group、删除 group 的 offset、删除消息（DeleteRecords）、软删除 schema 版本 |

### 8.2 规则

| 情况 | 行为 |
|---|---|
| 只读连接，任何写或危险操作 | 拒绝，提示用 `:set noreadonly` 临时解锁 |
| prod 连接，写操作 | 确认框显示要做什么（发几条消息到哪个 topic、改哪些配置等），按 `y` 确认 |
| prod 连接，重置 offset | 输入 group 名确认（重置 offset 直接影响线上消费进度，按危险操作的确认方式处理，我的决定） |
| 任何连接，危险操作 | 输入对象名确认（topic 名或 group 名） |
| 非 prod 连接，写操作 | 发消息不确认（我的决定：测试环境发消息是高频操作）；其他写操作按 `y` 确认 |
| 批量发消息（多条或从文件） | 先解析全部，再按上面的规则确认一次，确认框里写明条数 |

### 8.3 单一检查点

参考 kafui 的做法：`kafka.Client` 的**所有写方法**都要经过同一个检查点（比如在 `session` 里用一个包装类型实现 `kafka.Client`，读方法直接透传，每个写方法先检查只读状态，确认动作由 UI 在调用前完成），不能在 UI 的每个入口分别判断。要求：

- 有一个测试列出 `kafka.Client` 接口的全部写方法，断言只读状态下每一个都被拒绝。以后给接口加写方法而忘了加检查时，这个测试要能失败（具体做法由实现者定，比如在测试里用反射对比方法列表和已知的读方法清单）。
- 浏览消息用的 `kafka.Reader` 没有任何写方法，也不持有 group ID（反目标：浏览时加入 group 或提交 offset）。

### 8.4 敏感信息

- 密码、token、Schema Registry 凭据不写日志、不显示。连接错误信息里如果包含凭据（比如 URL 里带了用户名密码），显示前要去掉。
- 配置项里标记为敏感的（`sensitive = true`），值显示成 `******`。

## 9. K3：治理

- **ACL**：浏览（按 principal、资源类型、资源名过滤），新建和删除 ACL（删除是危险操作）。
- **Kafka Connect**：配置 Connect 集群的 URL 后，在树上显示 connectors 和状态；可以暂停、继续、重启 connector 和 task，查看和修改配置（修改按写操作处理）。
- **消息内容打码**：在 `config.toml` 里按连接（或 env）配置规则，按 JSON 路径或 header 名把敏感字段显示成 `******`，复制和导出时也打码。只影响显示，不改变发消息。
- **写操作审计**：所有写和危险操作（SQL、Redis、Kafka 都可以）记录到 `internal/history` 的 SQLite：时间、连接、操作、对象、结果。提供一个查看页。是否扩展到 SQL 和 Redis 在 K3 计划阶段决定。

## 10. 测试

- **纯包**（表驱动）：`kmsgx` 的起始位置表达式解析、过滤表达式、解码（各种格式和边界：空值、null、非 UTF-8、截断、Confluent 线格式头）、producer 草稿解析（包括行号报错、null key、tombstone、多条消息）、重置 offset 的目标计算和越界处理、写操作分级。
- **`kfake` 单元测试**（不需要 Docker）：`internal/kafka` 的元数据、`Reader` 的 seek / fetch / 越界报错、lag 计算、重置 offset（包括 group 不是 Empty 时拒绝、预览后 committed 被改时放弃）、发消息到不存在的 topic 报错且**没有创建 topic**、浏览后 group 列表里没有出现新的 group、Dialer 注入后所有连接都经过注入的函数。
- **集成测试**（`integration` tag）：用 testcontainers 起 Kafka（KRaft 单节点；SASL SCRAM + TLS 再起一个配置）。镜像矩阵（我的决定，对应 §3.2.1）：Apache Kafka 3.x 最新版（经典 group）、Apache Kafka 4.x 最新版（经典 group 和 KIP-848 新协议 group 都要测）、Redpanda 最新版（只跑一组核心用例：元数据、浏览、发消息、lag、重置 offset，用来发现不支持的请求）。2.8 不进 CI，发布前手工验证一次。测试连接认证、按时间查 offset、配置的增量修改不覆盖其他项、DeleteRecords、SSH 隧道（沿用 `tunnel` 包的内存 SSH 服务器）。K2 再加一个 Schema Registry 容器，测试 Avro / Protobuf / JSON Schema 的解码和序列化往返。
- **UI 测试**：`fakes_test.go` 增加假的 `kafka.Client` / `Reader`，驱动 `Model.Update` 测：树的 group 按 lag 排序、Lag 部分 `gd` 跳到 committed offset 并定位光标、加载更多和内存上限、live tail 的跟随和暂停、重置对话框的预览和确认、只读和 prod 的拦截与确认、producer console 执行当前 / 全部。
- **手工验证**：连本地 Kafka 和作者的测试集群，按 §1 的排查流程走一遍；在 prod 连接上确认写操作被拦截，浏览后用 `kafka-consumer-groups.sh --list` 确认没有多出 group。

## 11. 交付顺序

K1 → K2 → K3，每期单独写实施计划（`docs/superpowers/plans/`），各自可以发布。版本号在发布时决定。

- **K1**：§3（认证只做无认证、PLAIN、SCRAM、TLS / mTLS）、§4（不含 Schema Registry 节点）、§5、§6、§8。
- **K2**：§7。
- **K3**：§9，各项之间互不依赖，可以分开做。

每期都要在 `CHANGELOG.md` 的 `[Unreleased]` 下加条目，README 中英文同步更新，主设计文档的架构图和安全规则一节加上指向本文的说明。改名 datavim 是单独的改动，可以在 K1 之前或之后做，由作者决定。

## 12. 留给实现者决定的

- `kafka` 接口的具体形状；`kmsgx` 的包名；Kafka 节点怎么接入 `panes/tree`（新增 `Kind`）。
- 新增 keymap 上下文的划分，以及跳转、live tail 两个新动作的默认键（§6 的约束内）。
- topic 名分组的规则、group 很多时懒加载的阈值。
- 各种默认值（每次加载条数、内存上限、单条大小上限、查找的扫描上限、刷新间隔）以及是否放进 `config.toml`。
- 过滤表达式和 producer 草稿的精确语法（§5.6、§5.9 的约束内）。
- 钥匙串里多个秘密的命名方式。
- Avro / Protobuf 解码库的选择。
