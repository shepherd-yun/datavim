# Elasticsearch E1（查和改）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 datavim 里连上 Elasticsearch 7.10–9.x / OpenSearch（无认证 / Basic / API Key，HTTP / HTTPS，可走 SSH 隧道）；树里浏览索引（滚动索引折叠）、别名、data stream；Index 标签页按 ID 或关键字查文档、看详情、复制、暂存编辑并带并发检查提交；`_delete_by_query`；索引管理；REST 控制台（补全、按请求执行、结果转表格）；集群概览页。所有请求经过一个按 method + path（必要时看 body）分级的检查点。

**Architecture:** 新建 `internal/es`（`net/http` + JSON，`Doer` 接口只有一个 `Do`，类型化操作都是建立在 `Doer` 上的普通函数）和纯包 `internal/esx`（请求分级、控制台文件解析、mapping 展平和默认列、响应转表格、query string 拼接、单元格输入解析、JSON 路径读写、滚动索引分组、补全上下文）。`session.Manager.ES` 只返回包了检查的 `Doer`，检查、审计都在它的 `Do` 里。UI 增加 ES 树节点、Index 标签页、REST 控制台和 ES 状态页，按键全部进 keymap。

**Tech Stack:** Go 1.26、标准库 `net/http` / `encoding/json`（**不引入** go-elasticsearch）、`github.com/testcontainers/testcontainers-go/modules/elasticsearch` 和 `modules/opensearch`（v0.44.0，和现有 testcontainers 同版本）。实现 `internal/es` 前用 Context7 查 ES 7.10 / 8.x 的 PIT、`search_after`、`_resolve/index`、scroll、OpenSearch `_search/point_in_time` 的确切请求和响应格式。

**Spec:** `docs/superpowers/specs/2026-10-21-elasticsearch-design.md`（本计划覆盖 E1：§3、§4（不含 Index Templates、Snapshots 节点）、§5、§6、§9、§10）

## Global Constraints

- 驱动名 `elasticsearch`，默认端口 `9200`。`host` 是逗号分隔的地址，每项可带协议和端口；不带协议时 `params.tls = "true"` 用 https，否则 http。`database` 不用。
- `params`：`tls`、`tls_skip_verify`、`tls_ca`、`tls_cert`、`tls_key`、`auth`（空 = 有 `user` 时 Basic，否则无认证；`"apikey"` = API Key）、`path_prefix`、`show_system`、`page_size`（默认 100）。`auth` 是别的值时报 `connection "x": elasticsearch auth "y" is not supported (want apikey or leave it empty)`。
- 支持 ES 7.10–9.x、OpenSearch 1.x/2.x。按 `GET /` 的 `version.distribution`（OpenSearch 是 `"opensearch"`）和 `version.number` 决定行为。API 返回 404 / 400 `no handler` 时只禁用这一项，显示 `not supported by this cluster`；403 显示 `permission denied`。
- **不做 sniffing**；多个地址按顺序重试连接失败（不重试 HTTP 错误状态）。
- 响应体上限 52,428,800 字节，超过报 `response larger than 50 MB; add size / filter_path`。
- 默认值：Documents 每页 100（`page_size`）；一个视图最多 10,000 行；单个 `_source` 超过 1,048,576 字节截断并禁止编辑；默认最多 12 列；PIT / scroll 保活 `5m`；命中数超时 5 秒后改用 `track_total_hits: 10000` 显示 `10,000+`；条件框历史每个连接 + 索引 50 条；状态页每 10 秒刷新；高亮每字段 1 个片段，片段大小 100；控制台 JSON 结果最多渲染 10,000 行。
- 安全（spec §9.2）：只读拒绝写 / 危险 / 特别危险，提示用现有的 `readOnlyHint`；非 prod 写按 `y`（refresh / flush / 清缓存不确认）；prod 写按 `y` 且确认框显示 method、path 和 body 前 5 行；危险输入对象名；非 prod 特别危险先展开成索引列表再输入原始表达式，执行时用展开后的具体索引名；prod 特别危险直接拒绝，提示 `refused on a prod connection: name the indices one by one`；**认不出来的请求算危险**。
- 文档写入一律带 `if_seq_no` + `if_primary_term`，写到 hit 的 `_index`，带 `_routing`；提交成功带 `refresh=wait_for`。409 提示 `<id> changed since you loaded it; reload to see it`。
- 审计：写和危险请求记 `history.AuditEntry{Kind: history.AuditES, Op: "<METHOD> <path 去掉 query>", Object: <目标索引或空>, Detail: <级别>}`，**不记 body**。`--debug` 日志只记 method、path、状态码、耗时。
- 密码、API Key 不写日志、不显示；"复制为 curl" 用 `-u <user>` 或 `-H 'Authorization: ApiKey <api-key>'` 占位。
- UI 文字用英文；按键名一律用 `keyHint`；新按键进 `keymap/defaults.go` 并带描述；复用的动作见 spec §10 的表。
- 提交信息用 Conventional Commits 带 scope，**不加 Co-Authored-By**。用户能看到的改动在 Task 24 统一写进 `CHANGELOG.md` 的 `[Unreleased]`。

## Review Focus

1. **`_source` 里字面带点的键**（`{"a.b": 1}`，和 `{"a":{"b":1}}` 共存或单独出现）：列 `a.b` 两种都能读出来；编辑时写回原来的形状，不把扁平键改成嵌套对象（否则产生两个字段）。测试在 Task 4（`esx.GetPath`）和 Task 5（`esx.SetPath`）。
2. **别名或通配跨多个索引，同一字段类型不同**（一个是 `keyword`，一个是 `long`）：列取并集，类型显示为 `keyword|long`；单元格编辑按这一行 `_index` 的 mapping 解析输入。测试在 Task 4（`MergeMappings`）和 Task 18。
3. **ff 的值带 query string 特殊字符**（`http://a/b?c=1`、`a+b`、`中文 空格`、`"引号"`、反斜杠）：拼出来的条件只精确匹配这个值，不报语法错误。测试在 Task 5（`esx.FieldEquals` 表驱动）。
4. **PIT / scroll 过期**（作者离开 5 分钟后再往下翻）：ES 返回 404 / `search_context_missing_exception`，提示 `the result cursor expired; reloading from the top` 并重新查询，不崩、不留下坏状态。测试在 Task 9（`ErrCursorExpired`）和 Task 15。
5. **空索引或刚建的索引**（0 文档、mapping 为空）：Documents 只显示 `_id` 列和 `no documents`，`o` 新建文档仍可用；Mapping 部分显示 `no fields`。测试在 Task 4 和 Task 15。

---

### Task 1: 配置和连接表单支持 `elasticsearch`

**Files:**
- Modify: `internal/config/config.go`（`defaultPorts`、`Normalize`、未知驱动的报错，新增 `normalizeES`、`ESURLs`）
- Modify: `internal/ui/overlay/connform.go`（驱动选项、`URLs` 标签、`ffTLS` 对 ES 也显示、新增 `ffAuth` 行）
- Modify: `internal/ui/app.go` 和 `internal/ui/loaders.go` 里 `isRedis` / `isKafka` 旁新增 `isES(b Backend, conn string) bool`（放在 Task 13 用到的新文件 `internal/ui/estree.go` 里也可以，二选一，保持只有一个定义）
- Test: `internal/config/config_test.go`、`internal/ui/overlay/connform_test.go`

**Interfaces:**
- Produces: `func ESURLs(c Connection) ([]string, error)` —— 按逗号切分 `Host`，去掉空白和空项；没有协议的补 `http://` 或（`params.tls == "true"`）`https://`；没有端口的补 `:<Port>`；去掉末尾的 `/`；`params.path_prefix` 非空时拼在后面（规范成以 `/` 开头、不以 `/` 结尾）。解析失败（`url.Parse` 出错或 host 为空）返回错误。
- `Normalize`：`Driver:"elasticsearch"` 时 Host 默认 `localhost`、Port 默认 9200；校验 `params.auth`（Global Constraints 的报错）、`ESURLs` 能解析、`page_size` 是正整数。未知驱动报错改成 `(want mysql, postgres, sqlserver, oracle, redis, kafka or elasticsearch)`。
- 表单：驱动选项加 `elasticsearch`；选中时 Host 标签显示 `URLs`，Database 行隐藏，显示 `TLS` 行（复用 `kafkaTLSChoices`，映射到同样的 `params.tls` / `params.tls_skip_verify`）和新的 `Auth` 行（`basic` / `api key` ↔ 不写 / `params.auth = "apikey"`）。编辑已有连接时保留其他 `params`。

- [ ] **Step 1: 写失败的测试** `TestNormalizeES`（表驱动）：`{Driver:"elasticsearch"}` → Port 9200，`ESURLs` = `["http://localhost:9200"]`；`Host:"10.0.0.1, https://es2:9243/"` → `["http://10.0.0.1:9200","https://es2:9243"]`；`params.tls="true"`、`Host:"es"` → `["https://es:9200"]`；`path_prefix:"es/"` → `"http://localhost:9200/es"`；`auth:"oauth"` 报错包含 `is not supported (want apikey`；`page_size:"0"` 报错。`TestConnFormES`：选 elasticsearch 后有 `URLs` 标签、没有 Database 行、有 `TLS` 和 `Auth` 行，没有 Kafka 的 `SASL` 行；选 `on, skip verify` 和 `api key` 提交后 `Params` = `{tls:"true", tls_skip_verify:"true", auth:"apikey"}`；编辑带 `page_size` 的连接后它还在。
- [ ] **Step 2: 运行** `go test -race ./internal/config ./internal/ui/overlay -run 'ES'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/config ./internal/ui/overlay`，预期 PASS。
- [ ] **Step 5: 提交** `feat(config): 支持 elasticsearch 连接类型`

### Task 2: `esx`：请求分级

**Files:**
- Create: `internal/esx/classify.go`、`internal/esx/endpoints.go`
- Test: `internal/esx/classify_test.go`

**Interfaces:**
- Produces:
  ```go
  type Level int
  const ( LevelRead Level = iota; LevelWrite; LevelDanger; LevelCritical )
  func (l Level) String() string // "read", "write", "danger", "critical"

  type Class struct {
      Level   Level
      Op      string   // 简短名字，用于确认框和审计："delete index", "delete by query", "bulk", "index document", "refresh"…
      Targets []string // path 第一段按逗号切开的索引表达式；没有索引段时为空
      Quiet   bool     // 非 prod 不需要确认：refresh、flush、cache clear
      Counts  map[string]int // _bulk 的 index/create/update/delete 各几条
  }
  func Classify(method, path string, body []byte) Class

  type Endpoint struct { Method, Path string } // Path 用 {index} {id} 占位，比如 "{index}/_search"
  var Endpoints []Endpoint // 补全和测试共用的接口表（spec §6.3 列出的全部）
  ```
- 规则（spec §9.1，表驱动全覆盖）：`GET`/`HEAD` 读；POST 只读接口列表读；`DELETE _pit`、`DELETE _search/scroll`（body 或 path 带具体 id）、`DELETE _search/point_in_time`（body 带 `pit_id`）读；`DELETE _search/scroll/_all`、`DELETE _search/point_in_time/_all` 危险；`_bulk` 有 `delete` 行 → 危险，否则写；`_aliases` 里有 `remove_index` → 删除索引的规则；删除索引 / `_close` / `_delete_by_query` 的 `Targets` 含 `*`、`_all`、多于一个、或（这里判断不了的）别名 → 由 `Targets` 决定：含 `*`/`_all`/逗号时 `LevelCritical`；`_delete_by_query` body 没有 `query` 或是 `match_all` → `LevelCritical`；**其他认不出来的 → `LevelDanger`**。"别名能匹配多个索引"在 UI 里用 `_resolve/index` 结果再升级（Task 20），不在这里判断。

- [ ] **Step 1: 写失败的测试** `TestClassify`（表驱动，至少覆盖）：`GET orders/_search` 读；`POST orders/_search` 读；`POST _sql` 读；`POST orders/_doc` 写；`PUT orders/_doc/1` 写；`DELETE orders/_doc/1` 写；`POST _refresh` 写且 `Quiet`；`DELETE orders` 危险 `Op:"delete index"` `Targets:["orders"]`；`DELETE logs-*` 特别危险；`DELETE a,b` 特别危险；`DELETE _all` 特别危险；`POST orders/_close` 危险；`POST orders/_delete_by_query {"query":{"term":{"a":1}}}` 危险；同上 body 为空或 `match_all` 特别危险；`POST _bulk` 两行 index 写、`Counts["index"]==1`（一对行算一条）；带一行 delete 的 `_bulk` 危险；`POST _aliases {"actions":[{"remove_index":{"index":"old"}}]}` 危险；`remove_index` 的 index 是 `old-*` 特别危险；`DELETE _search/scroll/_all` 危险；`DELETE _search/scroll {"scroll_id":"x"}` 读；`POST orders/_forcemerge` 危险；`PUT _cluster/settings` 危险；`POST _foo/bar` 危险。`TestEveryEndpointClassified`：遍历 `Endpoints`，用 `orders` 和 `1` 填占位，断言 `Classify` 的 `Op` 不是兜底的 `"unknown request"`。
- [ ] **Step 2: 运行** `go test -race ./internal/esx -run Classify`，预期 FAIL。
- [ ] **Step 3: 实现**。path 先去掉 query string 和前导 `/` 再按 `/` 切段；`_bulk` 按行解析（跳过空行，动作行是单键对象）。
- [ ] **Step 4: 运行** `go test -race ./internal/esx`，预期 PASS。
- [ ] **Step 5: 提交** `feat(esx): 按 method 和 path 给 ES 请求分级`

### Task 3: `esx`：控制台文件解析

**Files:**
- Create: `internal/esx/console.go`
- Test: `internal/esx/console_test.go`

**Interfaces:**
- Produces:
  ```go
  type Request struct {
      Method, Path string // Method 大写；Path 去掉前导 "/"
      Body        []byte   // 发送用：JSON 原样（三引号已转换）；NDJSON 每行压成一行、末尾带 "\n"；没有 body 为 nil
      NDJSON      bool     // _bulk / _msearch
      Line, End   int      // 请求行和最后一行（从 0 开始），用于定位光标和 RunStatement
  }
  type ParseError struct { Line int; Msg string } // Error(): "line N: msg"（N 从 1 开始）
  func ParseConsole(text string) ([]Request, error)
  func RequestAt(reqs []Request, line int) (Request, bool) // 光标所在的请求（在 [Line, End] 里，或者在它后面的空行 / 注释上时取上一个）
  ```
- 方法限定 `GET` `POST` `PUT` `DELETE` `HEAD`（大小写都认）。`#` 和 `//` 开头的行是注释（body 里也是）。三引号 `"""…"""` 转成 JSON 字符串（转义 `"`、`\`、换行）。非 NDJSON 的 body 必须是一个合法 JSON 值，否则 `ParseError` 指向 body 第一行，`Msg` 包含 `invalid JSON`。

- [ ] **Step 1: 写失败的测试** `TestParseConsole`（表驱动）：两个请求加注释和空行 → 2 个，`Line`/`End` 正确；`get /orders/_search` → Method `GET` Path `orders/_search`；body 跨多行 → 压缩前等价的 JSON；`POST _bulk` 后两行 → `NDJSON` 为真，Body 为 `"{...}\n{...}\n"`；三引号脚本 `{"script":{"source":"""ctx._source.a = "x"\n"""}}` → 合法 JSON，`source` 值为 `ctx._source.a = "x"\n`；body 不合法 → `line 3: invalid JSON …`；第一行不是方法 → `line 1: expected METHOD path`。`TestRequestAt`：光标在 body 中间、在两个请求之间的空行上、在第一个请求之前的注释上（返回 false）。
- [ ] **Step 2: 运行** `go test -race ./internal/esx -run 'ParseConsole|RequestAt'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/esx`，预期 PASS。
- [ ] **Step 5: 提交** `feat(esx): 解析 REST 控制台文件`

### Task 4: `esx`：mapping、列和响应转表格

**Files:**
- Create: `internal/esx/mapping.go`、`internal/esx/table.go`、`internal/esx/path.go`
- Test: `internal/esx/mapping_test.go`、`internal/esx/table_test.go`、`internal/esx/path_test.go`

**Interfaces:**
- Produces:
  ```go
  type Field struct {
      Path     string   // "user.name"
      Type     string   // "keyword"；多个索引类型不同时 "keyword|long"
      Keyword  string   // 有 keyword 子字段时它的完整路径（"title.keyword"），排序用
      Indexed, DocValues bool
      Analyzer string
      Sub      []string // fields 子字段的完整路径
      CopyTo   []string
      Object   bool     // object / nested 父节点
  }
  func FlattenMapping(raw json.RawMessage) ([]Field, error)   // 一个索引的 "mappings" 对象（7.x 起无 type 层）
  func MergeMappings(per map[string][]Field) []Field          // 多个索引取并集，按 Path 排序
  func DefaultColumns(fields []Field, max int) []string        // spec §5.2 的挑选规则，max=12

  type Hit struct {
      Index, ID, Routing string
      SeqNo, PrimaryTerm int64
      Score   *float64
      Source  json.RawMessage
      Truncated bool                  // Source 超过上限被截断（由 es 层设置）
      Highlight map[string][]string
      Sort    []json.RawMessage       // search_after 用
  }
  func GetPath(src json.RawMessage, path string) (json.RawMessage, bool) // 同时认 {"a":{"b":..}} 和 {"a.b":..}；数组按 JSON 返回
  func CellText(v json.RawMessage, ok bool) string                      // 不存在 → ""，null → 交给 grid 的 NULL 样式（返回 grid 认的 NULL 值，看 panes/grid 现有约定），字符串去引号，其余紧凑 JSON

  type Table struct { Columns []string; Rows [][]string }
  func HitsTable(resp []byte) (Table, error)  // _search 响应：_index _id 加 _source 展平的列（出现顺序）
  func CatTable(resp []byte) (Table, error)   // _cat?format=json 的对象数组
  func AggTable(resp []byte) (Table, error)   // 第一个聚合；嵌套 buckets 展平成 key1 · key2 · doc_count · 指标…
  ```

- [ ] **Step 1: 写失败的测试** `TestFlattenMapping`：带 object、nested、`fields.keyword`、`copy_to`、`index:false` 的 mapping → 预期的 `[]Field`；空 mapping `{}` → 空切片（Review Focus 5）。`TestMergeMappings`：两个索引 `code` 一个 keyword 一个 long → `Type:"keyword|long"`（Review Focus 2）。`TestDefaultColumns`：keyword/数字/日期/boolean 在前，`text`（无 keyword 子字段）、object 父节点、`geo_point`、`dense_vector`、`binary` 排除，最多 12。`TestGetPath`：嵌套、字面带点的键、两者都有时优先字面键、数组下标不支持（`a.0` 不存在）、缺失（Review Focus 1）。`TestHitsTable` / `TestCatTable` / `TestAggTable`：各一个真实形状的响应（terms 嵌套 terms + avg 指标）。
- [ ] **Step 2: 运行** `go test -race ./internal/esx -run 'Mapping|Columns|GetPath|Table'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/esx`，预期 PASS。
- [ ] **Step 5: 提交** `feat(esx): mapping 展平、默认列和响应转表格`

### Task 5: `esx`：条件、单元格输入、写回和滚动索引分组

**Files:**
- Create: `internal/esx/query.go`、`internal/esx/edit.go`、`internal/esx/rollover.go`
- Modify: `internal/esx/path.go`（加 `SetPath`）
- Test: `internal/esx/query_test.go`、`internal/esx/edit_test.go`、`internal/esx/rollover_test.go`、`internal/esx/path_test.go`

**Interfaces:**
- Produces:
  ```go
  // 条件框（spec §5.3）
  func SearchQuery(input string) (json.RawMessage, error) // "" → match_all；以 "{" 开头 → 原样（必须是 JSON 对象）；否则 query_string{query, default_operator:"AND"}
  func FieldEquals(field, value string) string    // `field:"value"`，按 query string 规则转义（field 里的特殊字符也转义）
  func FieldNot(field, value string) string       // `NOT field:"value"`
  func FieldContains(field, value string) string  // `field:*value*`，value 里的通配和特殊字符转义
  func FieldCompare(field, op, value string) string // op ">" / "<" → `field:>value`
  func FieldExists(field string, exists bool) string // `_exists_:field` / `NOT _exists_:field`
  func AndQuery(cur, clause string) string        // cur 为空 → clause；否则 "(cur) AND clause"（cur 已经是单个子句时不加括号）

  // 单元格输入（spec §5.6）
  func ParseCell(fieldType, input string) (json.RawMessage, error) // "null" → null；long/integer/short/byte/double/float/half_float/scaled_float → 数字；boolean → true/false；object/nested 或 "a|b" 混合类型 → 必须是合法 JSON；其他（keyword/text/date/ip…）→ JSON 字符串
  func SetPath(src json.RawMessage, path string, v json.RawMessage) (json.RawMessage, error) // 写回原来的形状：字面带点的键存在就改它，否则走嵌套对象，中间对象不存在就新建；保留其他键的顺序

  // 树（spec §4）
  func RolloverPrefix(index string) (string, bool) // "logs-app-2026.10.21" → ("logs-app-", true)；"-2026-10"、"_20261021"、"-000123" 同理；"orders" → ("", false)
  ```

- [ ] **Step 1: 写失败的测试** `TestSearchQuery`：空、`88121`、`status:paid AND x`、`{"term":{"a":1}}`、`{bad` 报错、`[1]` 报错。`TestFieldEquals`（表驱动，Review Focus 3）：`http://a/b?c=1`、`a+b`、`中文 空格`、`say "hi"`、`C:\path`、字段名 `user.name`、字段名带空格 → 预期的转义字符串；再加一条：把每个结果用 `SearchQuery` 包一层仍是合法 JSON。`TestAndQuery`、`TestFieldContains`、`TestFieldExists`。`TestParseCell`：`long` + `"12"` → `12`，`long` + `"x"` 报错，`boolean` + `"true"`，`keyword` + `"12"` → `"12"`，任意类型 + `"null"` → `null`，`object` + `{"a":1}`，`keyword|long` + `"12"` → 报错要求 JSON（输入 `12` 合法时返回 `12`）。`TestSetPath`：嵌套改值、字面带点的键改值不新建嵌套（Review Focus 1）、中间对象不存在时新建、其他键顺序不变。`TestRolloverPrefix`：上面的例子加 `logs-2026.10`、`a-1`（不算，序号至少 6 位）、`.ds-logs-2026.10.21-000001`。
- [ ] **Step 2: 运行** `go test -race ./internal/esx -run 'SearchQuery|Field|AndQuery|ParseCell|SetPath|Rollover'`，预期 FAIL。
- [ ] **Step 3: 实现**。query string 需要转义的字符：`+ - = && || > < ! ( ) { } [ ] ^ " ~ * ? : \ /` 和空白；双引号内只需转义 `"` 和 `\`。`SetPath` 用保序的 JSON 对象（逐 token 解码成 key/value 列表）实现。
- [ ] **Step 4: 运行** `go test -race ./internal/esx`，预期 PASS。
- [ ] **Step 5: 提交** `feat(esx): 条件拼接、单元格输入解析、写回路径和滚动索引分组`

### Task 6: `esx`：控制台补全上下文

**Files:**
- Create: `internal/esx/complete.go`、`internal/esx/dsl.go`（关键字表）
- Test: `internal/esx/complete_test.go`

**Interfaces:**
- Produces:
  ```go
  type CompleteKind int
  const ( CompleteNone CompleteKind = iota; CompleteMethod; CompletePath; CompleteIndex; CompleteKey; CompleteField )
  type CompleteCtx struct {
      Kind   CompleteKind
      Prefix string   // 光标前正在输入的部分
      Index  string   // 当前请求 path 里的索引表达式（字段补全用）
      Keys   []string // CompleteKey 时的候选（按 JSON 路径上下文从关键字表取）
  }
  func CompleteAt(text string, line, col int) CompleteCtx
  ```
- 规则（spec §6.3）：请求行的开头 → Method；方法后的第一段 → Index 和以 `_` 开头的接口（接口候选来自 `Endpoints`，由 UI 合并）；之后的段 → Path；body 里在对象键位置 → Key（顶层 / `query` 下 / `bool` 下 / 某种查询下 / `aggs` 下各一组）；在 `term`、`match`、`range` 等查询下的键位置，以及 `sort`、`_source`、`"field":` 值的位置 → Field。

- [ ] **Step 1: 写失败的测试** `TestCompleteAt`（表驱动：文本 + 光标 → 期望的 Kind / Prefix / Index / Keys 里包含某些项）：`GE|` → Method；`GET or|` → Index Prefix `or`；`GET orders/_se|` → Path；body `{"qu|` → Key 含 `query`；`{"query":{"b|` → Key 含 `bool`；`{"query":{"bool":{"|` → Key 含 `must` `filter`；`{"query":{"term":{"|` → Field，Index `orders`；`{"sort":[{"|` → Field；`{"aggs":{"x":{"|` → Key 含 `terms` `avg`；字符串值中间（不是 field 的位置）→ None。
- [ ] **Step 2: 运行** `go test -race ./internal/esx -run CompleteAt`，预期 FAIL。
- [ ] **Step 3: 实现**。从请求行开始到光标做一个容错的 JSON 扫描，维护键路径栈，不要求 JSON 完整。
- [ ] **Step 4: 运行** `go test -race ./internal/esx`，预期 PASS。
- [ ] **Step 5: 提交** `feat(esx): 控制台补全上下文`

### Task 7: `internal/es`：客户端、`Doer` 和错误

**Files:**
- Create: `internal/es/es.go`、`internal/es/errors.go`、`internal/es/info.go`
- Test: `internal/es/es_test.go`（`httptest.Server`）

**Interfaces:**
- Produces:
  ```go
  type Request struct { Method, Path string; Body []byte; NDJSON bool } // Path 可以带 query string，不带前导 "/" 也行
  type Response struct { Status int; Body []byte; Took time.Duration }
  type Doer interface { Do(ctx context.Context, r Request) (Response, error) }

  type Config struct {
      URLs []string
      User, Password, APIKey string
      TLSSkipVerify bool; TLSCA, TLSCert, TLSKey string
      Dial func(ctx context.Context, network, addr string) (net.Conn, error) // nil：直连
      MaxBody int64 // 0：52,428,800
  }
  type Client struct { /* … */ }
  func Open(cfg Config) (*Client, error)        // 只组装，不发请求
  func (c *Client) Do(ctx context.Context, r Request) (Response, error)
  func (c *Client) Close()                      // CloseIdleConnections

  type Info struct { Name, ClusterName, UUID, Version, Distribution string; Major, Minor int }
  func (i Info) OpenSearch() bool
  func GetInfo(ctx context.Context, d Doer) (Info, error)

  var ( ErrAuthFailed, ErrForbidden, ErrNotFound, ErrConflict, ErrUnsupported, ErrTooLarge, ErrCursorExpired error )
  type APIError struct { Status int; Type, Reason string; Raw []byte } // Error(): "type: reason"；Is() 映射到上面的哨兵（401→ErrAuthFailed，403→ErrForbidden，404→ErrNotFound，409→ErrConflict，400 且 reason 含 "no handler"→ErrUnsupported，404/400 且 type 为 search_context_missing_exception→ErrCursorExpired）
  func CheckStatus(resp Response) error         // 2xx → nil，否则解析成 *APIError
  ```
- `Do`：按顺序试 `URLs`，**只在连接失败时**换下一个，记住上次成功的地址；`Content-Type` 按 `NDJSON`；Basic 或 `Authorization: ApiKey <key>`；响应体超过 `MaxBody` 返回 `ErrTooLarge`（消息见 Global Constraints）；`Do` 本身不把非 2xx 当错误（控制台要看原始响应），类型化函数用 `CheckStatus`。debug 日志只记 method、path、status、耗时。错误信息里不出现密码或 URL 里的 userinfo。

- [ ] **Step 1: 写失败的测试** `TestDoBasicAndAPIKey`：服务端断言 `Authorization` 头。`TestDoFailover`：第一个 URL 是关掉的端口，第二个成功；第二次调用直接用第二个（服务端计数）。`TestDoNoRetryOnHTTPError`：第一个返回 500 时不试第二个。`TestDoBodyLimit`：`MaxBody: 10` 时返回 `ErrTooLarge`。`TestDial`：注入的 `Dial` 被调用，`URLs` 里写一个不存在的域名也能连上（拨号函数把它导到 httptest 地址）。`TestCheckStatus`（表驱动）：401/403/404/409/400 no handler/search_context_missing_exception 各映射到哨兵，`Error()` 为 `type: reason`。`TestGetInfo`：ES 7.10.2 和 OpenSearch 2.11 的 `GET /` 响应。`TestErrorHasNoPassword`：连接失败的错误字符串里没有密码。
- [ ] **Step 2: 运行** `go test -race ./internal/es`，预期 FAIL。
- [ ] **Step 3: 实现**。TLS 用 `tls_ca` / `tls_cert` / `tls_key` 组 `tls.Config`；`Dial` 非空时设到 `http.Transport.DialContext`（https 也是先 Dial 再 TLS）。
- [ ] **Step 4: 运行** `go test -race ./internal/es`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): Elasticsearch HTTP 客户端`

### Task 8: `internal/es`：读操作

**Files:**
- Create: `internal/es/read.go`
- Test: `internal/es/read_test.go`（`httptest.Server` 回放真实形状的响应，放在 `internal/es/testdata/`）

**Interfaces:**
- Consumes: `Doer`、`CheckStatus`（Task 7）；`esx.Hit`、`esx.Field`、`esx.FlattenMapping`（Task 4）
- Produces（都是 `func X(ctx context.Context, d Doer, …)`）：
  ```go
  type IndexInfo struct { Name, Health, Status, UUID string; Docs, Deleted, StoreBytes, PriStoreBytes int64; Primaries, Replicas int; Created time.Time; System bool }
  func Indices(ctx, d) ([]IndexInfo, error)                // _cat/indices?format=json&bytes=b&expand_wildcards=all + creation.date
  type Alias struct { Name, Index string; IsWrite bool; Filter json.RawMessage; Routing string }
  func Aliases(ctx, d) ([]Alias, error)
  type DataStream struct { Name, Template string; Indices []string }
  func DataStreams(ctx, d) ([]DataStream, error)           // 404 / no handler → nil, nil
  func Mappings(ctx, d, target string) (map[string][]esx.Field, error)
  type Setting struct { Key, Value string; Default bool; Dynamic bool }
  func Settings(ctx, d, index string) ([]Setting, error)   // include_defaults=true&flat_settings=true；Dynamic 按 esx 里的已知动态 settings 表
  type IndexStats struct { Index string; /* spec §5.1 Stats 列出的数值 */ }
  func Stats(ctx, d, target string) ([]IndexStats, error)
  type Health struct { Status string; Nodes, DataNodes, ActivePrimary, Active, Relocating, Initializing, Unassigned, PendingTasks int }
  func ClusterHealth(ctx, d) (Health, error)
  type ClusterStats struct { Indices, Shards int; Docs, StoreBytes int64 }
  func GetClusterStats(ctx, d) (ClusterStats, error)
  func GetDoc(ctx, d, index, id string) (esx.Hit, error)    // 不存在 → ErrNotFound
  func Count(ctx, d, target string, query json.RawMessage) (int64, error)
  type Resolved struct { Indices, Aliases, DataStreams []string }
  func ResolveIndex(ctx, d, expr string) (Resolved, error)   // _resolve/index；不支持时用 _cat/indices/<expr> 回退
  ```
- `GetDoc` 和 hits 解析：`_source` 超过 1,048,576 字节时只保留前 1 MB 能解析的部分不现实，改为：保留原始字节前 1 MB 供详情显示，`Source` 设为 `{}` 之外的列预览由 UI 用截断标记处理，`Truncated = true`（实现者选具体做法，要求：列表能显示、详情标明截断、编辑被拒绝）。

- [ ] **Step 1: 写失败的测试**：每个函数一个用例，用 `testdata` 里 7.10.2 和 8.x 的响应（两个版本格式不同的地方，比如 `_cat/indices` 的字段，各一个）；`DataStreams` 在 7.10 返回空不报错；`ResolveIndex` 在服务端返回 400 no handler 时走回退；`GetDoc` 404 → `ErrNotFound`；`Settings` 把 `index.refresh_interval` 标为 Dynamic、`index.number_of_shards` 不是。
- [ ] **Step 2: 运行** `go test -race ./internal/es -run 'Indices|Aliases|DataStreams|Mappings|Settings|Stats|Health|GetDoc|Count|Resolve'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/es`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): 索引、mapping、settings、文档和集群的读操作`

### Task 9: `internal/es`：`Cursor`（翻页）

**Files:**
- Create: `internal/es/cursor.go`
- Test: `internal/es/cursor_test.go`

**Interfaces:**
- Consumes: `Doer`、`Info`（Task 7）
- Produces:
  ```go
  type SearchSpec struct {
      Target    string          // 索引、别名、通配或 data stream
      Query     json.RawMessage // esx.SearchQuery 的结果
      Sort      []SortField     // 空：spec §5.2 的默认排序由 UI 决定后传进来；空且无 query 时按 _doc
      PageSize  int
      Highlight bool
      ExactTotal bool           // track_total_hits: true；false 时 10000
  }
  type SortField struct { Field string; Desc bool } // Field "_score" 合法
  type Page struct { Hits []esx.Hit; Total int64; TotalExact bool; Done bool }
  type Cursor interface {
      Next(ctx context.Context) (Page, error) // 第一次调用就是第一页
      Close(ctx context.Context) error        // 释放 PIT / scroll；重复调用无害
  }
  func OpenCursor(info Info, d Doer, spec SearchSpec) Cursor
  ```
- 翻页方式（spec §5.4，实现者在这三种里按版本选，要求无重复无遗漏，能翻过 10,000）：ES ≥ 7.12 用 PIT + `search_after` + `_shard_doc`；ES 7.10 / 7.11 用 scroll（我的决定：没有 `_shard_doc` 时 PIT 自己加 tiebreaker 不可靠，scroll 在 7.10 上行为确定；只往下翻，正好满足 Documents 的需要）；OpenSearch ≥ 2.4 用 `_search/point_in_time`，更早用 scroll。PIT / scroll 都用 `keep_alive=5m`。`Next` 返回 `ErrCursorExpired` 时由 UI 重新打开（Review Focus 4）。**`Cursor` 只发读请求**：构造时传入的 `Doer` 会被包一层，任何 `esx.Classify` 不是 `LevelRead` 的请求都直接返回错误（spec §9.3）。
- 高亮参数见 Global Constraints；`Highlight` 的请求被 ES 拒绝（400）时去掉高亮重试一次，`Page` 不带高亮。

- [ ] **Step 1: 写失败的测试**（fake `Doer` 记录请求）：`TestCursorPIT`（Info 8.11）：第一页请求打开 PIT、带 `_shard_doc`，第二页带上一页最后一条的 `sort` 作为 `search_after`；`Close` 发 `DELETE _pit`。`TestCursorScroll710`（Info 7.10.2）：第一页带 `scroll=5m`，第二页 `POST _search/scroll`，`Close` 发 `DELETE _search/scroll` 带 id。`TestCursorOpenSearch`（2.11 和 2.3 各一）。`TestCursorExpired`：第二页返回 404 `search_context_missing_exception` → `ErrCursorExpired`。`TestCursorReadOnly`：断言它发出的所有请求 `Classify` 都是读。`TestCursorHighlightRetry`。`TestCursorClosesTwice`。
- [ ] **Step 2: 运行** `go test -race ./internal/es -run Cursor`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/es`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): 文档翻页（PIT / scroll）`

### Task 10: `internal/es`：写操作和集成测试

**Files:**
- Create: `internal/es/write.go`、`internal/es/integration_test.go`（`//go:build integration`）
- Modify: `Makefile`（`test-integration` 加 `./internal/es/...`）、`go.mod`（testcontainers 的 elasticsearch、opensearch 模块）
- Test: `internal/es/write_test.go`

**Interfaces:**
- Produces（全部 `func X(ctx context.Context, d Doer, …)`，写在 `es` 包里、只经过 `d.Do`）：
  ```go
  type Version struct { SeqNo, PrimaryTerm int64 }
  func PutDoc(ctx, d, index, id, routing string, src json.RawMessage, v Version) (Version, error) // ?if_seq_no&if_primary_term&refresh=wait_for；409 → ErrConflict
  func CreateDoc(ctx, d, index, id string, src json.RawMessage) (string, error)                  // id 空 → POST _doc；返回 ES 给的 id
  func DeleteDoc(ctx, d, index, id, routing string, v Version) error
  func DeleteByQuery(ctx, d, indices []string, query json.RawMessage) (taskID string, err error)  // wait_for_completion=false&conflicts=proceed
  type TaskStatus struct { Completed bool; Total, Deleted, Updated, Created, Failures int64; Err string }
  func GetTask(ctx, d, id string) (TaskStatus, error)
  type NewIndex struct { Name string; Shards, Replicas int; Mappings, Settings json.RawMessage }
  func CreateIndex(ctx, d, n NewIndex) error
  func DeleteIndices(ctx, d, names []string) error   // 只接受不含 * 和 _all 的具体名字，否则返回错误（spec §9.2）
  func OpenIndex(ctx, d, name string) error
  func CloseIndex(ctx, d, name string) error
  func Refresh(ctx, d, target string) error
  func Flush(ctx, d, target string) error
  func ClearCache(ctx, d, target string) error
  func ForceMerge(ctx, d, target string, maxSegments int) error
  type AliasAction struct { Add bool; Index, Alias string; IsWrite *bool; Filter json.RawMessage; Routing string }
  func UpdateAliases(ctx, d, acts []AliasAction) error
  func PutSettings(ctx, d, index string, changed map[string]*string) error // nil 值 = 恢复默认（发 null）
  // 写函数清单（session 的测试用）：
  var WriteFuncs = []string{"PutDoc", "CreateDoc", "DeleteDoc", "DeleteByQuery", "CreateIndex", "DeleteIndices", "OpenIndex", "CloseIndex", "Refresh", "Flush", "ClearCache", "ForceMerge", "UpdateAliases", "PutSettings"}
  ```

- [ ] **Step 1: 写失败的单元测试**（fake `Doer`）：每个函数发出的 method、path、query 参数、body 正确；`PutDoc` 409 → `ErrConflict`；`DeleteIndices([]string{"logs-*"})` 不发请求直接报错；`PutSettings` 只包含 `changed` 里的键。
- [ ] **Step 2: 写集成测试** `TestIntegration`，镜像矩阵：`docker.elastic.co/elasticsearch/elasticsearch:7.10.2`（必须）、`elasticsearch:8.15.x`（开安全功能和 HTTPS，测 Basic 和 API Key）、`elasticsearch:9.x` 最新、`opensearchproject/opensearch:2.x` 最新（只跑核心子测试）。子测试：`GetInfo`；`Indices`/`Mappings`/`Settings`；写入 25,000 条后用 `Cursor` 全部翻完，ID 不重复、总数等于 25,000，`Close` 后 `GET _nodes/stats/indices/search` 的 `open_contexts` 回到 0（spec §11）；`PutDoc` 并发：先 `GetDoc`，另一个请求改掉，再 `PutDoc` → `ErrConflict` 且文档是另一个请求的内容；`DeleteByQuery` + `GetTask` 完成后计数正确；`CreateIndex`/`CloseIndex`/`OpenIndex`/`DeleteIndices`；`UpdateAliases` 后通过别名 `GetDoc` 再 `PutDoc` 到 hit 的 `_index`；SSH 隧道（沿用 `tunnel` 包内存 SSH 服务器的测试帮助函数，`Dial` 注入）。
- [ ] **Step 3: 运行** `go test -race ./internal/es`，预期单元测试 FAIL。
- [ ] **Step 4: 实现**。
- [ ] **Step 5: 运行** `go test -race ./internal/es` 和 `go test -race -tags integration ./internal/es/...`（需要 Docker，OrbStack 先 `orb start`），预期 PASS。
- [ ] **Step 6: 提交** `feat(es): 文档和索引的写操作与集成测试`

### Task 11: 会话层：`Manager.ES`、检查点、审计、`Backend`

**Files:**
- Create: `internal/session/es.go`
- Modify: `internal/session/manager.go`（字段 `ESOpen`、`es map[string]*es.Client`、`esGen`；`resetLocked` 关闭并删除；`CloseAll`）、`internal/session/secrets.go`（`SecretAPIKey = "apikey"`）、`internal/history/audit.go`（`AuditES = "es"`，审计页的种类过滤加上它）、`internal/ui/backend.go`（`ES(ctx, name) (es.Doer, error)`）、`internal/ui/fakes_test.go`（假 backend 实现 `ES`）、`cmd/…` 里组装 `Manager` 的地方（`ESOpen = es.Open` 的等价物）
- Test: `internal/session/es_test.go`

**Interfaces:**
- Consumes: `es.Open`、`es.Doer`、`es.WriteFuncs`（Task 7、10）、`esx.Classify`（Task 2）、`config.ESURLs`（Task 1）
- Produces:
  - `func (m *Manager) ES(ctx context.Context, name string) (es.Doer, error)` —— 打开（首次）并缓存底层 `*es.Client`，返回 `*guardedES{m, name, raw}`；并发打开和重置的处理和 `Manager.Kafka` 一样（`esGen`）。第一次打开时调用 `es.GetInfo` 验证连接；`ErrAuthFailed` → `ErrPasswordRequired`（Basic）或 `m.refusedSecret(name, SecretAPIKey)`（API Key）。
  - `func (m *Manager) ESInfo(name string) (es.Info, bool)` —— 打开时记下的 `Info`（UI 和 `Cursor` 用）；`ui.Backend` 也加上。
  - `guardedES.Do`：`esx.Classify` → 非读且连接只读 → 返回 `session.ErrReadOnly`（复用 Kafka 的同一个变量）；非读时按 Global Constraints 记审计。**UI 只能拿到 `guardedES`**。
  - `esConfig`：Basic 用 `m.password`；`params.auth == "apikey"` 用 `m.secretFor(name, SecretAPIKey)`；`Dial` 用 `m.dial`。

- [ ] **Step 1: 写失败的测试** `TestESReadOnlyRefusesEveryWrite`：底层是计数的假 `Doer`，连接设为只读；对 `es.WriteFuncs` 里的**每个名字**，通过一个 `map[string]func(es.Doer) error`（测试里写全）用 `guardedES` 调用对应的 `es` 写函数，断言返回 `ErrReadOnly` 且底层计数为 0；再断言 map 的键集合等于 `es.WriteFuncs`（以后加写函数忘了更新测试时失败）。`TestESReadsPassOnReadOnly`：`GET`、`POST _search` 放行。`TestESAudit`：`PUT orders/_doc/1` 记一条 `AuditES`，`Op` 为 `PUT orders/_doc/1`，`Detail` 为 `write`，不含 body；`GET` 不记。`TestESAPIKeyPrompt`：`auth=apikey` 且钥匙串里没有 → `*SecretRequiredError`。`TestESResetReopens`。
- [ ] **Step 2: 运行** `go test -race ./internal/session -run ES`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/session ./internal/history ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(session): ES 连接和写请求的唯一检查点`

### Task 12: keymap：ES 的 kind、上下文和新动作

**Files:**
- Modify: `internal/ui/keymap/kind.go`、`internal/ui/keymap/keymap.go`（新上下文和 Action）、`internal/ui/keymap/defaults.go`、`internal/ui/app.go`（返回 `keymap.KindES` 的地方，第 544 行附近）
- Test: `internal/ui/keymap/keymap_test.go`、`internal/ui/keymap/custom_test.go`（已有的"每个 Action 有描述"测试会覆盖新动作）

**Interfaces:**
- Produces:
  - `KindES`；`kindDescs` 的值类型从 `[3]string` 改成 `[4]string`（`[SQL, Redis, Kafka, ES]`），已有条目补第四项：`History` → `"Request history"`、`OpenConsole` → `"REST console"`、Tree `Open` → `"Open the index / alias / data stream"`、`DeleteNode` → `"Delete connection / delete index"`、`NewKey` → `"New index"`、`FilterPrompt` → `"Filter indices by name (* wildcard)"`、`FilterClear` → `"Clear the index filter"`；Global `OpenKey` → `"Open a document by _id"`、`ServerInfo` 保持通用。
  - `kinds` 表：`OpenKey` 改为 redis + ES；Tree 的 `NewKey`、`FilterPrompt`、`FilterClear` 加 ES；`ToggleInternal`、`SortGroups` 改为 kafka + ES（`SortGroups` 的 ES 描述 `"Sort indices: name / docs / size"`，`ToggleInternal` 的 ES 描述 `"Show / hide system indices"`，用 `kindDescs`）。
  - 新上下文：`ESDocs Context = "es_docs"`（Documents 表格：`gridBindings` + `filterEditBindings` 里 spec 用到的 + 下面的键）、`ESIndex Context = "es_index"`（Mapping / Aliases / Stats 部分：`gridBindings` + `[` `]`）、`ESSettings Context = "es_settings"`（和 `KafkaConfig` 同一组编辑键）、`ESServer Context = "es_server"`、`ESResults Context = "es_results"`（控制台结果区，和 `RedisResults` 同一组 + `gd`）。
  - 新 Action：`DeleteByQuery`（`ESDocs` 上 `X`）、`YankID`（`yk`）、`YankSource`（`ys`）、`YankRequest`（`yr`，复制成 GET / PUT 请求）、`YankCurl`（`yu`）。`ESDocs` 上还绑：`[`/`]` PrevSection/NextSection、`}` LoadMore、`K` CellDetail、`s` SortColumn、`F` FilterPrompt、`ff` `f!` `fs` `f>` `f<` `fn` `fN` `fx`、`i`/回车 EditCell、`o` InsertRow、`dd` DeleteRow、`u` `U`、`<Space>w` SubmitEdits、`<Space>v` ExternalEdit。`ESResults` 也绑 `yu`。
  - `filterEditBindings` 里 `fl` 在 ES 上下文不绑定（spec 修订版 §5.3）。

- [ ] **Step 1: 写失败的测试** `TestESBindings`：在 `ESDocs` 里 `yk` `ys` `yr` `yu` `X` `}` 解析到对应 Action，`yy` `yl` 仍是 YankRow / YankCell；`Help(ESDocs, KindES)` 里有 `Open a document by _id`、不含 SQL-only 的 `CopyDDL`；`Help(Tree, KindES)` 的 `NewKey` 描述是 `New index`；`Help(Tree, KindKafka)` 的 `NewKey` 仍是 `New topic`。默认 keymap 加载无 "bound twice" 错误（已有测试覆盖）。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/keymap`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(keymap): Elasticsearch 的上下文和按键`

### Task 13: 树：ES 节点

**Files:**
- Create: `internal/ui/estree.go`
- Modify: `internal/ui/panes/tree/tree.go`（新 Kind：`KindESSection`、`KindIndexFolder`、`KindESIndex`、`KindESAlias`、`KindDataStream`）、`internal/ui/loaders.go`（`kind == tree.KindConn && isES(...)` 分支）、`internal/ui/app.go`（Open / Refresh / FilterPrompt / ToggleInternal / SortGroups 对 ES 节点的分派）、`internal/ui/nodemenu.go`（`isESNode`，ES 节点的菜单在 Task 20 填）、`internal/ui/theme`（若健康色没有合适的槽：复用 `ok` / `warn` / `error` 一类现有槽，不新增除非没有）
- Test: `internal/ui/estree_test.go`、`internal/ui/fakees_test.go`（假的 ES 集群：一个按 method + path 返回固定响应的 `es.Doer`，供后面所有 UI 测试用）

**Interfaces:**
- Consumes: `es.Indices`、`es.Aliases`、`es.DataStreams`、`es.ClusterHealth`（Task 8）、`esx.RolloverPrefix`（Task 5）、`Backend.ES` / `ESInfo`（Task 11）
- Produces: `type esTree struct { info es.Info; health es.Health; indices []es.IndexInfo; aliases []es.Alias; streams []es.DataStream; errs map[string]string; showSystem bool; sortBy esSort; filter string }`；`func withES(ctx context.Context, b Backend, conn string, fn func(es.Doer) error) error`（连接丢失重置重试一次，和 `withKafka` 一样）；`isES(b Backend, conn string) bool`。
- 结构见 spec §4：连接节点显示 `[<version> · <health>]`；Indices、Aliases、Data Streams（有才显示）三个 section；滚动索引按 `RolloverPrefix` 分组（组内名字倒序，单个不分组），组名 `前缀* (N)`；行内显示健康圆点、`1.2M docs`、`830MB`、`[closed]`；red / yellow 行用警告色；`.` 开头的系统索引默认隐藏（`params.show_system` 或 `.` 切换）；`s` 在 name / docs / size 间循环；`F` 在 Indices 上按名字过滤（`path.Match`，不带 `*` 时按子串）。
- 在索引 / 别名 / data stream 上回车发 `openIndexMsg{conn, target string, kind indexKind}`（Task 14 处理）。

- [ ] **Step 1: 写失败的测试**：`TestESTreeLoads`（假集群：12 个索引，含 `logs-app-2026.10.19/20/21`、`.kibana_1`、一个 closed、一个 yellow）：展开连接后 Indices 下有 `logs-app-* (3)` 组、组内第一个是 `.21`；`.kibana_1` 不在；按 `.` 后出现；closed 行有 `[closed]`。`TestESTreeSort`：`s` 一次按 docs 倒序，两次按 size，三次回到 name。`TestESTreeFilter`：`F` 输入 `log` 只剩日志组。`TestESTreeNoDataStreams`（7.10 假集群）：没有 Data Streams 节点。`TestESTreeAuthPrompt`：`ES` 返回 `ErrPasswordRequired` 时弹出密码框，和 Kafka 一致。`TestESTreeOpenIndex`：回车发出 `openIndexMsg`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESTree`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch 树`

### Task 14: Index 标签页：框架、Mapping、Settings（只读）、Aliases（只读）、Stats

**Files:**
- Create: `internal/ui/esindex.go`
- Modify: `internal/ui/app.go`（处理 `openIndexMsg`，已有同 key 的标签页就切过去）
- Test: `internal/ui/esindex_test.go`

**Interfaces:**
- Consumes: `es.Mappings`、`es.Settings`、`es.Stats`、`es.Aliases`（Task 8）
- Produces: `type esIndexTab struct { … section esSection; fields []esx.Field; … }`，`esSection` 常量 `secDocuments, secMapping, secSettings, secAliases, secStats`，名字 `"Documents" "Mapping" "Settings" "Aliases" "Stats"`；`func esIndexTabKey(conn, target string) string { return "esindex\x00" + conn + "\x00" + target }`；`Context()` 按部分返回 `ESDocs` / `ESIndex` / `ESSettings`；实现 `tab`、`exportSource`、`Find`（和 `topicTab` 一样）。标签页打开时并行读 mapping（Documents 要用）和头部信息；其他部分在第一次切到时读。
- 头部一行：`● green · 5p / 1r · 8,812,301 docs · 4.1 GB · created 2026-03-02`；别名 / 通配时 `alias → 3 indices · …`。
- Mapping 部分是 grid：`field  type  index  doc_values  analyzer  fields  copy_to`，无字段时显示 `no fields`（Review Focus 5）；`yy` 复制整个 mapping JSON（覆盖 grid 的 YankRow，和结构页 `yd` 复制 DDL 的方式一致，实现者选其一并写进描述）。Settings 在本任务只读（编辑在 Task 20）。

- [ ] **Step 1: 写失败的测试**：`TestESIndexSections`：打开后在 Documents（本任务只是占位），`]` 依次到 Mapping / Settings / Aliases / Stats，`[` 回来；Mapping 行数等于展平后的字段数；空 mapping 显示 `no fields`；头部文字匹配上面的格式；同一个索引再打开复用标签页；别名打开时 Stats 每个实际索引一行。`TestESIndexMappingError`：403 时只有 Mapping 部分显示 `permission denied`，其他部分照常。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESIndex`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch Index 标签页`

### Task 15: Documents：加载、自动加载更多、排序、详情、复制、导出

**Files:**
- Create: `internal/ui/esdocs.go`、`internal/ui/esdoc_detail.go`
- Modify: `internal/ui/esindex.go`（Documents 部分接上）、`internal/export/*`（加 `NDJSON Format = "ndjson"`，`kinds` 里只对 ES 显示）、`internal/ui/keymap/kind.go`（`ExportAs("ndjson")` 只给 ES）
- Test: `internal/ui/esdocs_test.go`

**Interfaces:**
- Consumes: `es.OpenCursor`、`es.Cursor`、`es.Page`（Task 9）、`esx.DefaultColumns`、`esx.GetPath`、`esx.CellText`（Task 4）
- Produces: `type esDocs struct { cursor es.Cursor; hits []esx.Hit; cols []string; query string; sort []es.SortField; total int64; exact bool; done bool; … }`，供 Task 16–19 使用：`func (d *esDocs) reload() tea.Cmd`（关闭旧 cursor 再开新的）、`func (d *esDocs) selected() (esx.Hit, bool)`。
- 行为（spec §5.2、§5.4、§5.5）：第一列 `_id`，多索引时第二列 `_index`；默认列 `DefaultColumns(fields, 12)`；默认排序：有 query → `_score` desc，否则有 `@timestamp` → 它 desc，否则不传（`_doc`）；`s` 在 `text` 列上有 `Keyword` 时改用它，没有时提示 `cannot sort on text field; use <field>.keyword`；光标越过末行或 `}` 加载下一页，`Done` 时提示 `end of results`；超过 10,000 行丢弃最上面的行并提示 `dropped the first N rows (keeping 10,000)`；`ErrCursorExpired` → 提示 `the result cursor expired; reloading from the top` 并 `reload()`（Review Focus 4）；0 条显示 `no documents`（Review Focus 5）；头部右侧 `q: <query>  <N> hits  sort: <field>`，非精确时 `10,000+ hits`；命中数请求超过 5 秒用 `ExactTotal:false` 重查。**标签页关闭、连接重置、退出时调用 `cursor.Close`**（在 `tab` 关闭的钩子里，退出时 `Model` 关闭所有标签页的路径上；实现者确认现有关闭钩子的名字）。
- 详情（`K`）：spec §5.5 的头部 + 高亮 JSON（复用 `kafkadecode.go` 的 JSON 高亮）；对象 / 数组折叠用 `za`/`zo`/`zc`（`FoldToggle`/`FoldOpen`/`FoldClose`，在详情的 overlay 里解析）；`Truncated` 时标 `showing first 1 MB of N`；上一条 / 下一条复用 Kafka 消息详情用的同一组 Action。
- 复制：`yk` `_id`；`ys` `_source`（格式化 JSON）；`yr` 两段：`GET <index>/_doc/<id>` 和 `PUT <index>/_doc/<id>\n<_source>`；`yu` curl（`curl -X GET '<url>/<index>/_doc/<id>'` + 认证占位）。导出 NDJSON 每行 `{"_index","_id","_source"}`。

- [ ] **Step 1: 写失败的测试**：`TestESDocsLoad`（假集群返回 3 页，每页 100）：首屏 100 行、`_id` 在第一列；光标到底再 `j` → 200 行；`}` → 300 行并提示 `end of results`。`TestESDocsMemoryCap`（把上限调成 250）：第三页后保留 250 行并有提示。`TestESDocsDefaultSort`（有 `@timestamp` 的 mapping、无 query）：第一次请求的 sort 是 `@timestamp desc`。`TestESDocsSortText`：在 `title`（text + keyword 子字段）上 `s` → 请求用 `title.keyword`；在无子字段的 text 上 `s` → 提示。`TestESDocsExpired`（Review Focus 4）：第二页返回 404 `search_context_missing_exception` → 提示并重新从第一页开始。`TestESDocsEmpty`（Review Focus 5）。`TestESDocsCloseReleases`：关闭标签页后假 `Doer` 收到 `DELETE _pit`（8.x 假集群）。`TestESDocDetail`：`K` 显示 `_seq_no` 等；`Truncated` 显示截断提示。`TestESDocsYank`：`yk` `ys` `yr` `yu` 的剪贴板内容，`yu` 不含密码。`TestESDocsMultiIndex`：别名打开时第二列是 `_index`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESDoc`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/... ./internal/export`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch 文档浏览、详情和复制`

### Task 16: Documents：条件框、`f` 系列、命中高亮

**Files:**
- Modify: `internal/ui/esdocs.go`
- Create: `internal/ui/esfilter.go`（条件框：复用 `overlay` 的单行输入 + `panes/complete` 字段名补全 + 历史）
- Modify: `internal/history`（条件框历史：按 `conn + "\x00" + target` 存最近 50 条；能复用现有的过滤历史就复用，实现者确认 `table-filter-ux` 的实现）
- Test: `internal/ui/esfilter_test.go`

**Interfaces:**
- Consumes: `esx.SearchQuery`、`esx.FieldEquals` 等（Task 5）、`esDocs.reload`（Task 15）
- 行为（spec §5.3）：`F` 打开条件框，初始文字是当前条件；补全来源是 `esIndexTab.fields` 的 `Path`；上下键翻历史；回车执行，ES 返回 `query_shard_exception` / `parse_exception` / `x_content_parse_exception` 时条件框不关闭，下方显示 `reason`；`ff` 等在当前单元格上用 `AndQuery(cur, FieldEquals(col, cell))` 并 reload（`_id` 列用 `_id`，`_index` 列用 `_index`）；`fx` 清空；有条件时 `SearchSpec.Highlight = true`，grid 用现有搜索高亮颜色标出 `<em>` 包住的词（解析 highlight 片段得到词表，在单元格文字里标出），不在显示列里的命中字段在行尾显示 `+ field1, field2`。

- [ ] **Step 1: 写失败的测试**：`TestESFilterPrompt`：`F` 输入 `status:paid` 回车 → 请求 body 的 query 是 `query_string`；`F` 输入 `{"term":{"a":1}}` → 原样；语法错误时条件框仍打开且显示 reason。`TestESFilterCellKeys`：在 `user.name` = `张三` 的格子上 `ff` → 条件 `user.name:"张三"`；再 `f!` 在另一格 → `(user.name:"张三") AND NOT …` 的形状；`fn` → `NOT _exists_:col`；`fx` 清空。`TestESFilterComplete`：输入 `us` 弹出 `user.name`。`TestESFilterHistory`：执行两次后上键回到上一次。`TestESHighlight`：假集群返回 highlight，单元格里命中词带高亮样式；不在显示列的命中字段出现在 `+ …` 里。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run 'ESFilter|ESHighlight'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch 条件框、按格过滤和命中高亮`

### Task 17: 按 ID 打开（`<Space>k`）

**Files:**
- Create: `internal/ui/esopenid.go`
- Modify: `internal/ui/app.go`（`keymap.OpenKey` 的分派加 ES 分支，第 656 行附近）、`internal/ui/nodemenu.go`（ES 连接和索引的菜单加 `Open document by _id…`）
- Test: `internal/ui/esopenid_test.go`

**Interfaces:**
- Consumes: `es.GetDoc`（Task 8）、`es.OpenCursor` 的 `ids` 查询（Task 9）、详情（Task 15）
- 行为（spec §5.3）：在具体索引的 Index 标签页或索引节点上 → `GetDoc` → 直接打开详情 overlay；在别名 / 通配 / data stream 上 → `{"ids":{"values":[id]}}`：1 条打开详情，多条在 Documents 里用这个条件显示；在连接节点上输入 `索引/ID` 或只输入 ID（目标 `*,-.*`）；找不到提示 `no document with _id <id> in <target>`。

- [ ] **Step 1: 写失败的测试**：具体索引命中 → 详情打开且请求是 `GET orders/_doc/ord-1`；别名两个索引都有这个 ID → Documents 显示 2 行、条件是 ids；连接节点输入 `orders/ord-1`；找不到的提示文字。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESOpenID`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): 按 _id 打开 Elasticsearch 文档`

### Task 18: Documents：暂存编辑和提交

**Files:**
- Create: `internal/ui/esedit.go`
- Modify: `internal/ui/esdocs.go`
- Test: `internal/ui/esedit_test.go`

**Interfaces:**
- Consumes: `esx.ParseCell`、`esx.SetPath`（Task 5）、`es.PutDoc`、`es.CreateDoc`、`es.DeleteDoc`、`es.Version`（Task 10）、`overlay.NewConfirm`、现有的 `celledit` overlay
- Produces: `type esChange struct { hit esx.Hit; kind changeKind /* edit, create, delete */; src json.RawMessage /* 新 _source */; fields []fieldChange /* 旧值 → 新值，确认框用 */ }`
- 行为（spec §5.6）：`i`/回车按这一行 `_index` 的字段类型调 `ParseCell`（混合类型 Review Focus 2），对象 / 数组格子用 JSON 编辑；`<Space>v` 在 `$EDITOR` 里编辑整篇 `_source`，保存后必须是 JSON 对象；`o` 在 `$EDITOR` 里写新文档（预填 `{"_id": "", …mapping 字段: null}` 的形式，实现者决定；ID 空由 ES 生成）；`dd` 标记删除；`u` / `U` 撤销；改过的格子用现有的暂存样式。拒绝编辑的情况：`_source` 关闭或有 excludes（从 mapping 的 `_source` 设置判断）、`Truncated`、data stream 的 backing index；提示原因。`<Space>w`：按 §9.2 确认（只读直接拒绝；非 prod / prod 都按 `y`，确认框列出每个文档 `field: old → new`、新建、删除和请求数）→ 逐条 `PutDoc`/`CreateDoc`/`DeleteDoc`（`Version` 取 hit 的 `SeqNo`/`PrimaryTerm`，`routing` 取 hit 的）→ 汇总结果；`ErrConflict` 的那条保留暂存并提示 Global Constraints 里的文字；全部完成后 reload。

- [ ] **Step 1: 写失败的测试**：`TestESEditCell`：改 `amount`（long）为 `12.5` → 暂存；`<Space>w` → 确认框含 `amount: 99 → 12.5`；`y` 后假集群收到 `PUT orders/_doc/ord-1?if_seq_no=1022&if_primary_term=3&refresh=wait_for`，body 只有 amount 变了。`TestESEditBadType`：long 输入 `x` → 提示，不暂存。`TestESEditConflict`：假集群返回 409 → 提示 `ord-1 changed since you loaded it; reload to see it`，暂存还在。`TestESEditAliasWritesRealIndex`（Review Focus 2）：别名下 hit 的 `_index` 是 `orders-2`，请求路径是 `orders-2/_doc/…`，输入按 `orders-2` 的 mapping 解析。`TestESEditDottedKey`（Review Focus 1）：`_source` 是 `{"a.b":1}`，改 `a.b` 后 body 是 `{"a.b":2}`。`TestESEditRefused`：Truncated 行、`_source` 关闭的索引按 `i` 都提示原因。`TestESEditReadOnly`：只读连接 `<Space>w` 显示 `readOnlyHint`，没有发请求。`TestESNewAndDelete`：`o` 后（假 `$EDITOR` 写入 `{"a":1}`）提交发 `POST orders/_doc`；`dd` 提交发 `DELETE orders/_doc/ord-1?if_seq_no=…`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESEdit`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch 文档的暂存编辑和带并发检查的提交`

### Task 19: Documents：按条件删除（`X`）

**Files:**
- Create: `internal/ui/esdbq.go`
- Test: `internal/ui/esdbq_test.go`

**Interfaces:**
- Consumes: `es.Count`、`es.ResolveIndex`、`es.DeleteByQuery`、`es.GetTask`（Task 8、10）、`esx.Classify`（Task 2）
- 行为（spec §5.6、§9.2）：先 `ResolveIndex(target)` 得到实际索引列表，再 `Count`；有条件 → 危险：确认框 `delete N documents matching <query> in <indices>`，输入目标名（单个索引名；别名时输入别名）确认；没有条件、或目标展开成多个索引 → 特别危险：prod 直接拒绝（Global Constraints 的文字），非 prod 列出索引和 `deletes ALL N documents in <index>`，输入原始目标表达式确认；执行时 `DeleteByQuery(展开后的具体索引, query)`；之后每秒 `GetTask`，状态栏 `deleting… 12,000 / 48,000`，完成后提示结果并 reload。

- [ ] **Step 1: 写失败的测试**：有条件、单索引、非 prod：确认框里的条数等于 Count，输入错名字不执行，输入对后请求路径是 `orders/_delete_by_query?wait_for_completion=false&conflicts=proceed`；任务轮询直到完成并 reload。无条件、prod：拒绝且没有发删除请求。别名展开成 `a,b`、非 prod：确认框列出 a、b，请求路径是 `a,b/_delete_by_query…` 而不是别名（spec §9.2）。只读：`readOnlyHint`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESDBQ`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): 按条件删除 Elasticsearch 文档`

### Task 20: 索引管理和通配展开确认

**Files:**
- Create: `internal/ui/esindexops.go`、`internal/ui/esguard.go`（UI 侧的确认流程：输入 `esx.Class` + 连接的 prod / 只读状态 → 拒绝、`y` 确认、输入名字确认、展开后确认；控制台 Task 21 共用）、`internal/ui/overlay/indexform.go`（新建索引表单）
- Modify: `internal/ui/nodemenu.go`（ES 索引的菜单项）、`internal/ui/esindex.go`（Settings 编辑、Aliases 增删）、`internal/ui/app.go`（DeleteNode / NewKey 的 ES 分支）
- Test: `internal/ui/esindexops_test.go`、`internal/ui/esguard_test.go`、`internal/ui/overlay/indexform_test.go`

**Interfaces:**
- Produces:
  ```go
  type esPlan struct { Class esx.Class; Resolved []string; Count int64; Lines []string /* 确认框正文 */ }
  // confirmES 按 spec §9.2 决定：拒绝（返回状态文字）、直接执行、或打开确认 overlay；ok 在确认后发出
  func (m *Model) confirmES(conn string, p esPlan, ok tea.Msg) tea.Cmd
  ```
  `confirmES` 的规则只看 `p.Class.Level`、`p.Class.Quiet`、连接的 `IsReadOnly()` / `env == "prod"`；需要展开时由调用方先 `ResolveIndex` 填 `Resolved`（单个别名展开成多个索引时，调用方把 Level 升到 `LevelCritical`）。
- 操作（spec §5.8）：NewKey（`o`，Indices 节点）→ 表单（名字、shards 默认 1、replicas 默认 1、mappings / settings 用 `$EDITOR` 填 JSON，可以留空）→ `CreateIndex`；DeleteNode（`dd`）→ 确认框显示文档数、大小、别名、data stream → `DeleteIndices`（单个具体名字）；在 `logs-app-* (3)` 组节点上 `dd` → 特别危险流程，执行用展开后的名字；NodeActions 菜单：Open / Close / Refresh / Flush / Clear cache / Force merge（输入 max segments，默认 1）/ Open document by _id，每项显示 `keyHint`；Aliases 部分 `o` 新增（别名名 + 是否 write）、`dd` 删除 → `UpdateAliases`；Settings 部分和 `KafkaConfig` 一样的暂存编辑，只允许 `Dynamic` 的项，`<Space>w` → `PutSettings`，`number_of_replicas` 改成 0 或改 `index.blocks.*` 时按危险确认（输入索引名）。

- [ ] **Step 1: 写失败的测试** `TestConfirmES`（表驱动，覆盖 §9.2 每一行）：只读 × 各级别 → 拒绝；非 prod 写 → `y`；非 prod Quiet → 直接执行；prod 写 → `y` 且正文含 method 和 path；危险 → 输入名字；非 prod 特别危险 → 正文列出 `Resolved`、要求输入原始表达式；prod 特别危险 → 拒绝文字。`TestESDeleteIndex`：`dd` 输入名字后发 `DELETE orders`。`TestESDeleteRolloverGroup`：组节点 `dd`，非 prod，展开为 3 个，执行请求是 `DELETE logs-app-2026.10.19,logs-app-2026.10.20,logs-app-2026.10.21`；在展开和执行之间假集群多了一个 `logs-app-2026.10.22`，它不在请求里。`TestESCreateIndex`、`TestESIndexMenu`（Close 输入名字、Refresh 非 prod 不确认）、`TestESAliasesEdit`、`TestESSettingsEdit`（只提交改动项；非 Dynamic 项按 `i` 提示 `static setting; needs a closed index`）。`TestIndexForm`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/... -run 'ConfirmES|ESDelete|ESCreate|ESIndexMenu|ESAliases|ESSettings|IndexForm'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch 索引管理`

### Task 21: REST 控制台：执行和结果

**Files:**
- Create: `internal/ui/esconsole.go`
- Modify: `internal/ui/app.go`（OpenConsole 的 ES 分支，第 670 行附近）、`internal/ui/consolefile.go`（ES 控制台文件扩展名 `.es`）、`internal/ui/store.go` / `history`（请求历史复用现有历史，`Kind` 区分）
- Test: `internal/ui/esconsole_test.go`

**Interfaces:**
- Consumes: `esx.ParseConsole`、`esx.RequestAt`、`esx.Classify`（Task 2、3）、`esx.HitsTable`/`CatTable`/`AggTable`（Task 4）、`confirmES`（Task 20）、`Backend.ES`
- Produces: `type esConsoleTab struct { … }`，结构参照 `redisConsoleTab`（编辑器、结果标签、草稿、`UseRegisters`、`CapturesInput`、`TakeKey`、`cancelRun`）；`func esConsoleTabKey(conn string) string`。
- 行为（spec §6.1、§6.2）：回车执行光标所在请求、`R` 全部、VISUAL 选中的；先解析全部，有错不发，光标跳到出错行；分级后：全部是读直接执行；有写 / 危险 → 用 `confirmES`，规则见 spec §9.2 的 RunAll 段（写一次确认列出全部，危险以上每个单独确认，特别危险先展开）；`_cat/*` 没有 `format` 时自动加 `format=json`；每个请求一个结果标签（`H`/`L`），默认显示格式化 JSON（状态码 + 耗时在标题），错误响应显示 `type: reason` 用错误色；`[`/`]` 在 JSON 和表格之间切换（`HitsTable` → `CatTable` → `AggTable` 依次尝试，都不行就提示 `no table view for this response`）；表格里 `_id` 格子上 `gd` 打开文档详情（用同一行的 `_index`）；`yu` 复制当前请求为 curl；JSON 超过 10,000 行只渲染前 10,000 行并提示；`ctrl+c` 取消。

- [ ] **Step 1: 写失败的测试**：`TestESConsoleRunAtCursor`（两个 GET，光标在第二个）只发第二个。`TestESConsoleParseError`：有错时不发任何请求、光标到出错行。`TestESConsoleCatFormat`：`GET _cat/indices` 发出的是 `_cat/indices?format=json`。`TestESConsoleTableView`：`_search` 结果 `]` 后是表格，`gd` 打开详情。`TestESConsoleWriteConfirm`：非 prod `PUT x/_doc/1` 要 `y`；只读拒绝且没有发请求；`R` 里两个写一个读 → 一次确认、正文列出两个写。`TestESConsoleDangerEach`：`R` 里两个 `DELETE` 索引 → 两次输入名字。`TestESConsoleWildcardProd`：prod 上 `DELETE logs-*` 拒绝。`TestESConsoleUnknownIsDanger`：`POST _foo` 要输入名字（对象名用 path 第一段）。`TestESConsoleCurl`：`yu` 内容不含密码。`TestESConsoleError`：404 显示 `index_not_found_exception: no such index [x]`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESConsole`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch REST 控制台`

### Task 22: REST 控制台：补全和高亮

**Files:**
- Modify: `internal/ui/esconsole.go`、`internal/ui/completion.go`（ES 的补全来源）、`internal/ui/highlight/highlight.go`（新增 ES 控制台的语言：请求行的方法 / path、JSON 键、字符串、数字、注释）
- Test: `internal/ui/esconsole_test.go`、`internal/ui/highlight/highlight_test.go`

**Interfaces:**
- Consumes: `esx.CompleteAt`、`esx.Endpoints`（Task 2、6）；索引名来自树的 `esTree`（没加载时现读 `es.Indices`）；字段名来自 `es.Mappings(请求里的索引)`，按连接 + 索引缓存，Refresh 时清。

- [ ] **Step 1: 写失败的测试**：`TestESConsoleComplete`：插入模式输入 `GET or` 弹出 `orders`；`GET orders/_se` 弹出 `_search`；body 里 `"term": {"` 弹出 `orders` 的字段；`"query": {"b` 弹出 `bool`。`TestHighlightES`：方法、path、JSON 键、注释各自的样式。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/... -run 'ESConsoleComplete|HighlightES'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch 控制台的补全和高亮`

### Task 23: 集群概览页、全局搜索、最近打开、工作区恢复

**Files:**
- Create: `internal/ui/esserver.go`
- Modify: `internal/ui/app.go`（ServerInfo 的 ES 分支，第 544 / 600 行附近）、`internal/ui/search.go`（`searchES`：已加载的 ES 树里的索引、别名、data stream）、`internal/ui/recent.go`、工作区序列化的地方（`internal/config/workspace.go` 和 UI 里对应的恢复代码）
- Test: `internal/ui/esserver_test.go`、`internal/ui/search_test.go`、`internal/ui/workspace_test.go`

**Interfaces:**
- Consumes: `es.GetInfo`、`es.ClusterHealth`、`es.GetClusterStats`（Task 7、8）
- 行为：状态页参照 `kafkaServerTab`，E1 只有 Overview 一个部分（spec §5.9 的字段），每 10 秒刷新，不可见时停止（`Shown`/`Hidden`），健康不是 green 时顶部警告色写原因（`3 unassigned shards` 等）。全局搜索：索引、别名、data stream 作为结果，选中打开 Index 标签页。最近打开：Index 标签页和控制台。工作区（spec §10.1）：恢复 Index 标签页的 target、部分和条件（重新读一次），不恢复暂存编辑；控制台恢复草稿不执行。

- [ ] **Step 1: 写失败的测试**：`TestESServerOverview`（yellow 假集群）：显示版本、节点数、`3 unassigned shards` 警告；切走后不再发请求。`TestSearchES`：搜 `ord` 出现 `orders` 索引和 `orders-alias` 别名，选中打开标签页。`TestWorkspaceES`：保存后恢复，标签页在 Settings 部分、条件是 `status:paid`，没有暂存编辑。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run 'ESServer|SearchES|WorkspaceES'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch 集群概览、搜索和工作区`

### Task 24: 文档、CHANGELOG 和真机验证

**Files:**
- Modify: `CHANGELOG.md`（`[Unreleased]` 下 Added：Elasticsearch / OpenSearch 连接；树；Index 标签页的查找、编辑、按条件删除；索引管理；REST 控制台；集群概览）、`README.md` 和 `README.zh-CN.md`（支持的数据库列表、ES 连接配置例子：无认证内网、Docker 8.x 的 HTTPS + `elastic` 用户 + `tls_ca`、API Key；按键表；安全规则；建议生产用只读角色）、`docs/superpowers/specs/2026-10-13-datavim-design.md`（架构图和 §5.3 加指向 ES 设计的说明）、`internal/ui/hints.go` 的欢迎文字（如果它按连接类型列内容）

- [ ] **Step 1: 运行** `make lint`、`make test`、`make test-integration`，全部 PASS。
- [ ] **Step 2: 写文档和 CHANGELOG**。
- [ ] **Step 3: 真机验证**（spec §11 手工部分）：`make build`，`./bin/datavim --config <dir> --debug`：(a) 本地 Docker ES 7.10.2 和 8.x：走一遍 spec §2 的主场景（打开索引 → `F` 输入 ID / 关键字 → `K` → `ys` → `i` 改一个字段 → `<Space>w`）；(b) 关闭 Index 标签页后 `GET _nodes/stats/indices/search` 的 `open_contexts` 为 0；(c) 设 `env = "prod"`，控制台 `DELETE logs-*` 被拒绝；(d) 作者的内网 7.10.2 集群**设成只读**连一次，浏览和搜索正常，任何写操作被拒绝；(e) `~/.local/state/datavim/datavim.log` 里没有密码、API Key、请求 body。
- [ ] **Step 4: 提交** `docs: Elasticsearch 支持的文档和 CHANGELOG`
