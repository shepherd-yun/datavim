# Elasticsearch E2（集群运维）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 集群状态页从只有 Overview 扩展到 Nodes / Shards / Allocation / Tasks / Pending / Settings 七个部分（未分配分片的 allocation explain、节点详情、取消任务、改集群 settings）；Reindex（可选按源索引建好目标索引）；Documents 里的 Update by query（painless 在 `$EDITOR` 里写）；树上的 Index Templates（composable / legacy / component，看、在 `$EDITOR` 里改、删）；Mapping 部分加字段。

**Architecture:** 不改 E1 的结构。`internal/es` 加集群读（`cluster.go`）、任务（`tasks.go`）、模板（`template.go`）和几个写函数，全部是 `Doer` 上的普通函数，写函数进 `es.WriteFuncs`，`session` 的只读测试跟着加。`internal/esx` 加纯函数：节点详情摘要、allocation explain 摘要、磁盘 watermark 解析、复制 settings 时去掉不能复制的项、新 mapping 和已有字段的冲突检查。UI 里 `esServerTab` 改成多部分（参照 `kafkaServerTab`），按部分懒加载；Reindex 和 Update by query 共用 E1 `esdbq.go` 的任务轮询（泛化）。`esx.Classify` 已经覆盖 E2 的全部请求，不改。

**Tech Stack:** Go 1.26，标准库 `net/http` / `encoding/json`；测试沿用 `es/fake_test.go` 的 `fakeDoer`、`ui/fakes_test.go` 的假后端、`testcontainers` 的 ES 7.10.2 / 8.x 镜像。实现 Task 1–3 前用 Context7 查 ES 7.10 和 8.x 的 `_cat/nodes`、`_cat/shards`、`_cat/allocation`、`_tasks`、`_cluster/allocation/explain`、`_cluster/pending_tasks`、`_index_template`、`_template`、`_component_template`、`_cluster/settings` 的确切响应格式。

**Spec:** `docs/superpowers/specs/2026-10-21-elasticsearch-design.md`（本计划覆盖 E2：§7 全部、§4 的 Index Templates 节点、§5.1 Mapping 的加字段、§5.9 的"跳到分片页"；安全规则仍是 §9）

## Global Constraints

- 所有请求都走 `Backend.ES` 返回的包了检查的 `Doer`；新增的写函数一律加进 `es.WriteFuncs`，并在 `internal/session/es_test.go` 的 `TestESReadOnlyRefusesEveryWrite` 的 `calls` 里加对应项（否则该测试失败）。
- 分级用现有的 `esx.Classify`，不改：取消任务、`_reindex`、`PUT <index>/_mapping`、`PUT` 模板是**写**；`_update_by_query`、删除模板、`PUT _cluster/settings` 是**危险**。确认一律走 `confirmES` / `confirmESCmd`（`esguard.go`）。危险级确认要输入的名字：Update by query 用索引目标；删除模板用模板名；集群 settings 用连接名。
- 状态页刷新沿用 `timings.eserver`（默认 10 秒），标签页不可见时停止；每次刷新**只读当前部分**加 `_cluster/health`（头部的健康警告要一直对）。
- 阈值（我的决定，常量放在 `esserver.go`）：heap ≥ 85% 警告色；磁盘用量 ≥ 集群 `cluster.routing.allocation.disk.watermark.high` 警告色（取不到时用 90%）。
- `_tasks` 只列这些动作的顶层任务（`parent_task_id` 为空）：`actions=*reindex,*byquery,*forcemerge,cluster:admin/snapshot/*,indices:admin/resize*`，`detailed=true&group_by=none`。
- 长任务（reindex、update by query）一律 `wait_for_completion=false`，进度轮询沿用 `esTaskPoll`（1 秒）。
- 节点页不做 sparkline（spec §2 留给实现者，这一期不做）。不做新建模板（spec 只要求看、改、删）。
- UI 文字英文；按键名用 `keyHint`；新按键进 `keymap/defaults.go` 并带描述；提交信息 Conventional Commits 带 scope，**不加 Co-Authored-By**。用户可见的改动在 Task 13 统一写进 `CHANGELOG.md` 的 `[Unreleased]`，README 中英文同步。

## Review Focus

1. **单节点集群上副本未分配**（作者本地 Docker：`number_of_replicas: 1` 的索引是 yellow，每个副本都是 UNASSIGNED）：在副本分片上回车，explain 请求带 `"primary": false`，显示 `cannot allocate because allocation is not permitted to any of the nodes` 一类的原因，而不是报错。测试在 Task 6 和 Task 13（集成）。
2. **任务在列出和取消之间已经结束**：`_cancel` 返回 404 `resource_not_found_exception`，提示 `task already finished`，刷新 Tasks，不显示红色错误。测试在 Task 7。
3. **Reindex 目标不合法**：目标等于源、目标含 `*` / `,`、目标是已有别名。表单直接报错不发请求（目标是别名时 ES 会写进别名的 write index，作者通常不想要）。测试在 Task 11。
4. **集群 setting 的值是数组或对象**（`cluster.routing.allocation.awareness.attributes`、`["a","b"]`）：显示为 JSON；编辑输入以 `[` 或 `{` 开头且是合法 JSON 时按 JSON 发送，否则按字符串发送。测试在 Task 2（`PutClusterSettings`）和 Task 8。
5. **某类模板接口不存在**（OpenSearch 1.x 没有 component template、ES 9 / 将来移除 legacy `_template`）：返回 404 / 400 `no handler` 的那一类跳过，其他类照常显示；三类都不支持时 Index Templates 节点显示 `not supported by this cluster`。测试在 Task 3。

---

### Task 1: `es` 集群读：节点、分片、分配、pending、explain、集群 settings

**Files:**
- Create: `internal/es/cluster.go`、`internal/esx/cluster.go`
- Test: `internal/es/cluster_test.go`、`internal/esx/cluster_test.go`（`internal/es/testdata/` 放 7.10 和 8.x 的响应样本）

**Interfaces:**
- Consumes: `es.Doer`、`do`、`CheckStatus`（E1）
- Produces（`internal/es/cluster.go`）:
  ```go
  type Node struct {
      ID, Name, IP, Roles, Version string // Roles 是 _cat 的缩写串 "dimr"
      Master                       bool   // 当前 master
      HeapPct, RAMPct, CPUPct      int
      Load1m                       float64 // 没有时 -1
      DiskUsedPct                  float64 // 没有时 -1
      DiskUsed, DiskTotal          int64
  }
  func Nodes(ctx context.Context, d Doer) ([]Node, error)
  // GET _cat/nodes?format=json&bytes=b&full_id=true&h=id,name,ip,node.role,master,heap.percent,ram.percent,cpu,load_1m,disk.used_percent,disk.used,disk.total,version

  type NodeAllocation struct {
      Node, IP                                  string // 未分配的那一行 Node 是 "UNASSIGNED"
      Shards                                    int
      DiskIndices, DiskUsed, DiskAvail, DiskTotal int64
      DiskPct                                   int    // 没有时 -1
  }
  func Allocation(ctx context.Context, d Doer) ([]NodeAllocation, error) // GET _cat/allocation?format=json&bytes=b

  type Shard struct {
      Index        string
      Shard        int
      Primary      bool
      State, Node  string // Node 未分配时为空
      Docs, Bytes  int64  // 没有时 -1
      Reason       string // unassigned.reason
  }
  func Shards(ctx context.Context, d Doer, index string) ([]Shard, error)
  // index 为空时全部：GET _cat/shards[/<index>]?format=json&bytes=b&h=index,shard,prirep,state,docs,store,node,unassigned.reason

  type PendingTask struct{ Order int64; Priority, Source, Waiting string }
  func PendingTasks(ctx context.Context, d Doer) ([]PendingTask, error) // GET _cluster/pending_tasks

  func AllocationExplain(ctx context.Context, d Doer, index string, shard int, primary bool) (json.RawMessage, error)
  // POST _cluster/allocation/explain {"index":…,"shard":…,"primary":…}
  func NodeStats(ctx context.Context, d Doer, id string) (json.RawMessage, error)
  // GET _nodes/<id>/stats/jvm,os,fs,process,indices,thread_pool，返回 nodes.<id> 那一项

  type ClusterSetting struct{ Key, Value, Scope string } // Scope: "persistent" | "transient" | "defaults"
  func ClusterSettings(ctx context.Context, d Doer) ([]ClusterSetting, error)
  // GET _cluster/settings?include_defaults=true&flat_settings=true；Value 用 esx.CellText（数组 / 对象是 JSON）；按 Scope 再按 Key 排序
  ```
- Produces（`internal/esx/cluster.go`）:
  ```go
  func NodeSummary(stats json.RawMessage) [][2]string
  // 节点详情的主要项，顺序固定：name、roles、heap used / max、gc young / old 次数、os cpu %、load 1m/5m/15m、ram used / total、
  // disk total / available（fs.total）、open file descriptors、docs、store size、query total、indexing total、
  // thread pool rejected（search / write）、uptime。缺的项跳过。
  func ExplainSummary(explain json.RawMessage) []string
  // 第一行 "<index>[<shard>] <primary|replica> <current_state>"；然后 allocate_explanation / unassigned_info.reason + last_allocation_status；
  // 再每个节点一行 "<node>: <decider>: <explanation>"（只取 decision 为 NO / THROTTLE 的 decider）
  func Watermark(value string, total int64) (usedPct float64, ok bool)
  // "85%" → 85；"0.85" → 85；"50gb" / "500mb" 这类剩余空间 → (total-free)/total*100；total<=0 或解析不了 ok=false
  ```

- [ ] **Step 1: 写失败的测试** `TestNodes`、`TestAllocation`、`TestShards`、`TestPendingTasks`、`TestClusterSettings`（`fakeDoer` + testdata 样本）：断言请求 path 完全等于上面写的；`_cat` 里 `null` 的数字字段（7.10 未分配分片的 `docs`/`store`）解析成 -1；`Master` 来自 `master == "*"`；`Shards(ctx, d, "logs-*")` 的 path 是 `_cat/shards/logs-*?…`；`ClusterSettings` 里 `cluster.routing.allocation.awareness.attributes: ["zone"]` 的 Value 是 `["zone"]`，persistent 排在 transient、defaults 前面。`TestAllocationExplain`：body 是 `{"index":"o","shard":0,"primary":false}`。`esx`：`TestNodeSummary`（7.10 和 8.x 样本各一，断言 `heap` 行是 `1.2 GB / 2.0 GB (60%)` 这种格式、缺 `load_average` 时没有 load 行）；`TestExplainSummary`（未分配副本样本，含 `same_shard` decider 一行）；`TestWatermark` 表驱动：`"85%"`、`"0.9"`、`"50gb"`（total 100gb → 50）、`"abc"` false、total 0 false。
- [ ] **Step 2: 运行** `go test -race ./internal/es ./internal/esx -run 'Nodes|Allocation|Shards|Pending|ClusterSettings|Explain|NodeSummary|Watermark'`，预期 FAIL。
- [ ] **Step 3: 实现**。数字字段用 E1 `read.go` 的 `num(*string)` 那种写法（`_cat` 的 JSON 里数字是字符串）。
- [ ] **Step 4: 运行** `go test -race ./internal/es ./internal/esx`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): read nodes, shards, allocation, pending tasks and cluster settings`

### Task 2: `es` 任务和写函数

**Files:**
- Create: `internal/es/tasks.go`
- Modify: `internal/es/write.go`（新增写函数、`WriteFuncs`）、`internal/es/write_test.go`（`TestWriteFuncsListed` 的数量）、`internal/session/es_test.go`（`calls`）、`internal/esx/mapping.go`、`internal/esx/settings.go`
- Test: `internal/es/tasks_test.go`、`internal/es/write_test.go`、`internal/esx/mapping_test.go`、`internal/esx/settings_test.go`

**Interfaces:**
- Produces（`internal/es`）:
  ```go
  type Task struct {
      ID, Node, Action, Description string
      Running                       time.Duration
      Cancellable                   bool
      Total, Done                   int64 // Done = created+updated+deleted+noops+version_conflicts；没有 status 时都是 -1
  }
  func Tasks(ctx context.Context, d Doer) ([]Task, error) // Global Constraints 里的 actions；去掉有 parent_task_id 的；按 Running 倒序
  func CancelTask(ctx context.Context, d Doer, id string) error // POST _tasks/<id>/_cancel；404 → ErrTaskGone
  var ErrTaskGone = errors.New("task already finished")

  type ReindexSpec struct {
      Source, Dest string
      Query        json.RawMessage // nil: 全部
      OpType       string          // "index" | "create"
      Conflicts    string          // "abort" | "proceed"
  }
  func Reindex(ctx context.Context, d Doer, r ReindexSpec) (task string, err error) // POST _reindex?wait_for_completion=false
  func UpdateByQuery(ctx context.Context, d Doer, indices []string, query json.RawMessage, script string) (string, error)
  // 用 concrete() 拒绝通配；POST <a,b>/_update_by_query?wait_for_completion=false，body {"query":…,"script":{"source":…,"lang":"painless"}}
  func PutMapping(ctx context.Context, d Doer, index string, properties json.RawMessage) error // PUT <index>/_mapping {"properties":…}
  func PutClusterSettings(ctx context.Context, d Doer, persistent, transient map[string]*string) error
  // nil 值发 null；值以 [ 或 { 开头且是合法 JSON 时原样发 JSON，否则发字符串；两个 map 都空时不发请求
  func IndexExists(ctx context.Context, d Doer, name string) (bool, error) // HEAD <name>：200 true，404 false
  func RawSettings(ctx context.Context, d Doer, index string) (map[string]json.RawMessage, error) // GET <index>/_settings?flat_settings=true 的 settings
  ```
  `WriteFuncs` 加：`CancelTask`、`Reindex`、`UpdateByQuery`、`PutMapping`、`PutClusterSettings`（Task 3 再加两个模板函数）。
- Produces（`internal/esx`）:
  ```go
  func CopyableSettings(flat map[string]json.RawMessage) (settings map[string]json.RawMessage, shards, replicas int)
  // 去掉 index.uuid、index.creation_date、index.provided_name、index.version.*、index.history.uuid、index.resize.*、
  // index.routing.allocation.initial_recovery.*、index.blocks.*、index.lifecycle.indexing_complete、
  // index.number_of_shards、index.number_of_replicas（后两个解析出来返回）；去掉 "index." 前缀以外的不动
  func MappingConflicts(existing []Field, properties json.RawMessage) (added []string, conflicts []string, err error)
  // properties 展平（复用 FlattenMapping 的逻辑）后：已有字段且 type 不同 → conflicts（"status: keyword → long"）；新路径 → added
  ```

- [ ] **Step 1: 写失败的测试** `TestTasks`：两个 reindex 任务、一个子任务 → 2 个，`Done` 按公式；`Running` 来自 `running_time_in_nanos`。`TestCancelTask`：path `_tasks/n1:12/_cancel`；404 → `errors.Is(err, ErrTaskGone)`。`TestReindex`：body `{"source":{"index":"a","query":{…}},"dest":{"index":"b","op_type":"create"},"conflicts":"proceed"}`，返回 task。`TestUpdateByQuery`：`[]string{"logs-*"}` 被拒绝且不发请求；`[]string{"a","b"}` 的 path 是 `a,b/_update_by_query?wait_for_completion=false`。`TestPutClusterSettings`：`{"a":"1","b":nil,"c":"[\"x\"]","d":"[oops"}` → body `{"persistent":{"a":"1","b":null,"c":["x"],"d":"[oops"}}`；两个空 map 不发请求。`TestIndexExists`：200 / 404 / 500（错误）。`TestWriteFuncsListed` 数量改成 19。session 的 `calls` 加 5 项。`esx`：`TestCopyableSettings`（7.10 的 `_settings` 样本，断言 uuid / creation_date / provided_name / version.created 不在结果里，shards=5、replicas=1，`index.analysis.*` 保留）；`TestMappingConflicts`：已有 `status: keyword`，新 `{"status":{"type":"long"},"note":{"type":"text"}}` → added `[note]`、conflicts `[status: keyword → long]`；不合法 JSON 报错。
- [ ] **Step 2: 运行** `go test -race ./internal/es ./internal/esx ./internal/session -run 'Task|Reindex|UpdateByQuery|ClusterSettings|IndexExists|WriteFuncs|ReadOnly|Copyable|MappingConflicts'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/es ./internal/esx ./internal/session`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): tasks, reindex, update by query, mappings and cluster settings writes`

### Task 3: `es` 模板

**Files:**
- Create: `internal/es/template.go`
- Modify: `internal/es/write.go`（`WriteFuncs`）、`internal/es/write_test.go`、`internal/session/es_test.go`
- Test: `internal/es/template_test.go`

**Interfaces:**
- Produces:
  ```go
  type TemplateKind int
  const ( TemplateComposable TemplateKind = iota; TemplateLegacy; TemplateComponent )
  func (k TemplateKind) String() string // "composable", "legacy", "component"

  type Template struct {
      Kind     TemplateKind
      Name     string
      Patterns []string        // component 为空
      Priority int             // composable 的 priority，legacy 的 order
      Body     json.RawMessage // 原样就是 PUT 要的 body（composable: index_template；legacy: 名字下的对象；component: component_template）
  }
  func Templates(ctx context.Context, d Doer) ([]Template, error)
  // GET _index_template、_template、_component_template；某一类返回 ErrNotSupported 时跳过；三类都不支持时返回 ErrNotSupported；
  // 其他错误直接返回。按 Kind 再按 Name 排序
  func PutTemplate(ctx context.Context, d Doer, kind TemplateKind, name string, body json.RawMessage) error
  func DeleteTemplate(ctx context.Context, d Doer, kind TemplateKind, name string) error
  ```
  `WriteFuncs` 加 `PutTemplate`、`DeleteTemplate`（共 21）。

- [ ] **Step 1: 写失败的测试** `TestTemplates`：三类各一个 → 3 个，Kind / Patterns / Priority 正确，`Body` 重新 `PUT` 时和 GET 的定义等价；`_component_template` 返回 400 `no handler found` → 只有 2 个、没有错误；三类都 404 → `ErrNotSupported`；`_template` 返回 500 → 错误。`TestPutDeleteTemplate`：三类的 path 是 `_index_template/n`、`_template/n`、`_component_template/n`，名字被 `url.PathEscape`。`TestWriteFuncsListed` 改成 21，session `calls` 加 2 项。
- [ ] **Step 2: 运行** `go test -race ./internal/es ./internal/session -run 'Template|WriteFuncs|ReadOnly'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/es ./internal/session`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): read, put and delete index templates`

### Task 4: 按键

**Files:**
- Modify: `internal/ui/keymap/keymap.go`（新上下文、新动作）、`internal/ui/keymap/defaults.go`（`esBindings`）、`internal/ui/keymap/kind.go`
- Test: `internal/ui/keymap/keymap_test.go`

**Interfaces:**
- Produces:
  - 上下文：`ESClusterSettings Context = "es_cluster_settings"`（状态页的 Settings 部分）、`ESTemplate Context = "es_template"`（模板标签页）。
  - 动作：`UpdateByQuery Action = "es.update_by_query"`。
  - 绑定（全部带描述，下面引号里就是描述）：
    - `ESServer`：`enter` Open "Nodes: node details · Shards: why it is unassigned · Overview: the unhealthy shards"；`.` ToggleInternal "Shards: all / only not STARTED"；`F` FilterPrompt "Shards: filter by index (* pattern)"；`f x` FilterClear "Shards: clear the filter"；`d d` KillClient "Tasks: cancel the task"。
    - `ESClusterSettings`：`gridBindings` + `[` `]` + `i` / `enter` EditCell "Edit the setting (persistent unless it is transient)"、`d d` DeleteRow "Remove the setting (back to the default)"、`u` UndoEdit、`U` UndoAllEdits、`<Space>w` SubmitEdits "Apply the cluster settings changes (:w)"、`.` ToggleInternal "Show / hide the defaults"。
    - `ESTemplate`：`gridBindings` + `<Space>v` ExternalEdit "Edit the template in $EDITOR"、`y y` YankRow "Copy the template (JSON)"、`<Space>r` Refresh。
    - `ESDocs`：`<Space>u` UpdateByQuery "Update every document the filter matches (painless in $EDITOR)…"。
    - `ESIndex`：`g d` OpenCellKey "Stats: this index's shards on the cluster tab"；`o` InsertRow 的描述改成 "Add: an alias (Aliases), fields in $EDITOR (Mapping)"。
  - `kind.go`：`KillClient` 加 `KindES` 并用 `kindDescs` 给 ES 描述 "Cancel the task"；`ToggleInternal` 已含 ES，ES 的描述 "Show / hide system indices, defaults, healthy shards"。

- [ ] **Step 1: 写失败的测试** `TestESE2Bindings`：上面每个（context, 按键）解析到对应动作；`TestNoConflicts`（现有的冲突检查，如果没有就加：同一上下文同一按键序列只有一个动作、不是另一个的前缀）覆盖新上下文；help 按 `KindES` 过滤时能看到 `UpdateByQuery`、`KillClient`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/keymap`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS（help / which-key 的快照测试如有变化一并更新）。
- [ ] **Step 5: 提交** `feat(keymap): Elasticsearch cluster, template and update-by-query keys`

### Task 5: 状态页分部分：Overview、Nodes、Allocation、Pending

**Files:**
- Modify: `internal/ui/esserver.go`
- Test: `internal/ui/esserver_test.go`（假集群加 `_cat/nodes` 等响应，放在现有 `fakeserver_test.go` 或 ES 的假后端里，跟现有写法一致）

**Interfaces:**
- Consumes: `es.Nodes`、`es.Allocation`、`es.PendingTasks`、`es.ClusterSettings`、`esx.Watermark`（Task 1）
- Produces:
  ```go
  type eserverSection int
  const ( secESOverview eserverSection = iota; secESNodes; secESShards; secESAllocation; secESTasks; secESPending; secESSettings; eserverSections )
  var eserverSectionNames = [eserverSections]string{"Overview", "Nodes", "Shards", "Allocation", "Tasks", "Pending", "Settings"}
  func (t *esServerTab) setSection(s eserverSection) tea.Cmd // 切换并立刻读这一部分
  ```
  `esServerLoadedMsg` 加 `section eserverSection` 和各部分的数据字段；`load()` 每次读 `_cluster/health`，再按 `t.section` 只读那一部分（Overview 另外读 info 和 stats，Nodes 另外读 allocation 取分片数、读 `ClusterSettings` 取 watermark——watermark 只在第一次读，缓存在 tab 上）。每个部分一个 `*grid.Model`（参照 `kafkaServerTab.grids`）。`Context()`：Settings 部分返回 `keymap.ESClusterSettings`，其余 `keymap.ESServer`。
- 表格列：
  - Nodes：`NAME`、`IP`、`ROLES`、`MASTER`（`★`）、`HEAP %`、`RAM %`、`CPU %`、`LOAD 1M`、`DISK %`、`SHARDS`、`VERSION`；heap ≥ 85 或 disk ≥ watermark 的行用警告色（grid 的行 mark，参照 `esIndexTab.settingMark`）。
  - Allocation：`NODE`、`SHARDS`、`DISK INDICES`、`DISK USED`、`DISK AVAIL`、`DISK TOTAL`、`DISK %`（字节用 `humanBytes`）。
  - Pending：`ORDER`、`PRIORITY`、`SOURCE`、`WAITING`；空时显示 `no pending cluster tasks`。
  - Overview 上回车：健康不是 green 时切到 Shards 部分；是 green 时无动作。
  - 部分标题行和 `kafkaServerTab.View` 一样（当前部分高亮），健康警告行一直在最上面。

- [ ] **Step 1: 写失败的测试** `TestESServerSections`：`]` 依次到 Nodes、Shards…，到 Settings 时 `Context()` 是 `ESClusterSettings`；切到 Nodes 后假后端只收到 `_cluster/health`、`_cat/nodes`、`_cat/allocation`（和第一次的 `_cluster/settings`），**没有** `_cat/shards`。`TestESServerNodesWarn`：heap 90 的节点那一行是警告 mark；disk 88%、watermark `"85%"` 时警告，watermark 取不到时 88% 不警告。`TestESServerOverviewJump`：yellow 时 Overview 上回车到 Shards 部分。`TestESServerHiddenStops`（已有的话扩展）：切走后 tick 不再发请求。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESServer`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch cluster tab sections: nodes, allocation, pending tasks`

### Task 6: Shards 部分、allocation explain、节点详情

**Files:**
- Modify: `internal/ui/esserver.go`
- Create: `internal/ui/esserver_detail.go`（explain 和节点详情的弹窗加载）
- Test: `internal/ui/esserver_test.go`

**Interfaces:**
- Consumes: `es.Shards`、`es.AllocationExplain`、`es.NodeStats`、`esx.ExplainSummary`、`esx.NodeSummary`（Task 1）；`overlay.NewDetail(title, text string)`；`indexNameMatch`（`estree.go`）
- Produces: `esServerTab` 上的 `shardsAll bool`、`shardFilter string`；`func (m *Model) openESShards(conn, index string) tea.Cmd` —— 打开（或切到）状态页、切到 Shards、`shardFilter = index`、`shardsAll = true`（Task 9 用）。
- 行为：Shards 列 `INDEX`、`SHARD`、`P/R`（`p` / `r`）、`STATE`、`NODE`、`DOCS`、`SIZE`、`REASON`；默认只显示 `State != "STARTED"`，`.` 切换全部；只显示非 STARTED 且一个都没有时显示 `every shard is started`。`F` 弹出过滤框（`*` 通配，复用 `indexNameMatch`），`fx` 清除；过滤条件显示在部分标题行右边。读 `_cat/shards` 时 path 不带索引（过滤在本地做，`shardFilter` 是具体索引名且不含 `*` 时才带上，减少响应）。UNASSIGNED 行上回车 → 读 explain → `overlay.NewDetail("<index>[<shard>] <p|r>", strings.Join(ExplainSummary, "\n"))`；非 UNASSIGNED 行上回车提示 `only unassigned shards have an allocation explanation`。Nodes 行上回车 → 读 `NodeStats(id)` → `overlay.NewDetail(<node name>, 两列对齐的 NodeSummary)`。explain / 节点详情的读取失败显示在状态栏。

- [ ] **Step 1: 写失败的测试** `TestESServerShardsDefault`：假集群 3 个 STARTED、1 个 UNASSIGNED 副本 → 只有 1 行；`.` 后 4 行。`TestESServerShardsFilter`：`F` 输入 `logs-*` 只剩匹配的；`fx` 恢复。`TestESServerExplain`：UNASSIGNED 副本上回车，假后端收到 `POST _cluster/allocation/explain` 且 body 的 `primary` 是 `false`，弹窗文字包含 `same_shard`；STARTED 行上回车提示上面的文字、不发请求。`TestESServerNodeDetail`：Nodes 上回车，请求 `_nodes/<id>/stats/…`，弹窗包含 `heap`。`TestOpenESShards`：`openESShards("e","orders")` 后当前标签页是状态页、Shards 部分、过滤是 `orders`、显示全部。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run 'ESServerShards|ESServerExplain|ESServerNodeDetail|OpenESShards'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch shards, allocation explain and node details`

### Task 7: Tasks 部分和取消

**Files:**
- Modify: `internal/ui/esserver.go`
- Test: `internal/ui/esserver_test.go`

**Interfaces:**
- Consumes: `es.Tasks`、`es.CancelTask`、`es.ErrTaskGone`（Task 2）、`confirmESCmd`、`esx.Classify`
- Produces: `func (m *Model) openESTasks(conn string) tea.Cmd` —— 打开（或切到）状态页的 Tasks 部分（Task 10、11 提交任务后用）。消息 `esCancelTaskMsg{tabID int; id string}`（确认后发）、`esTaskCancelledMsg{tabID int; conn, id string; err error}`。
- 行为：列 `ACTION`、`DESCRIPTION`（截断，`K` 看全文）、`PROGRESS`（`12,000 / 88,000 (13%)`，Total ≤ 0 时空）、`RUNNING`（`durationText`）、`NODE`、`ID`；空时 `no long-running tasks`。`dd`：不可取消的任务提示 `this task cannot be cancelled`；否则按 `Classify("POST", "_tasks/<id>/_cancel", nil)`（写）确认，确认框正文 `cancel <action> <description>`。取消成功提示 `cancelled <id>` 并刷新；`ErrTaskGone` 提示 `task already finished` 并刷新（不是错误色）。只读连接按 `dd` 提示 `readOnlyHint`，不发请求。

- [ ] **Step 1: 写失败的测试** `TestESServerTasks`：两个任务，进度列是 `12,000 / 88,000 (13%)`。`TestESServerCancelTask`：`dd` → 确认框 → `y` → 假后端收到 `POST _tasks/n1:7/_cancel`，状态 `cancelled n1:7`。`TestESServerCancelGone`：后端返回 404 → 状态 `task already finished`、不是错误、发出刷新。`TestESServerCancelReadOnly`：只读时没有确认框、没有请求。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run 'ESServerTasks|ESServerCancel'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch tasks and task cancel`

### Task 8: 集群 Settings 部分

**Files:**
- Create: `internal/ui/esclustersettings.go`
- Modify: `internal/ui/esserver.go`
- Test: `internal/ui/esclustersettings_test.go`

**Interfaces:**
- Consumes: `es.ClusterSettings`、`es.PutClusterSettings`（Task 1、2）、`confirmESCmd`
- Produces: `esServerTab` 上 `clusterEdits []clusterEdit`（`clusterEdit{key, scope string; value *string}`）、`showDefaults bool`；`func (t *esServerTab) handleClusterSettings(a keymap.Action) (tea.Cmd, bool)`，写法照抄 `esIndexTab.handleSettings`（暂存、`u`/`U`、`<Space>w`）。
- 行为（spec §7.5）：列 `SCOPE`、`KEY`、`VALUE`；defaults 默认隐藏，`.` 切换（标题行写 `defaults shown`）。`i`：persistent / transient 的行改原 scope；defaults 的行改成新的 persistent 设置（提示输入框标题 `<key> (persistent)`）。`dd`：persistent / transient 的行暂存为 null（删除）；defaults 行提示 `already the default`。暂存的格子用 changed mark。`<Space>w`：按 scope 分成两个 map，`Classify("PUT", "_cluster/settings", body)`（危险）确认，`Expect` 是连接名，正文每行 `<scope> <key>: <value | (default)>`，prod 显示 body。成功后清空暂存、重读。有暂存时切走 Settings 部分不丢暂存；刷新 tick 不覆盖暂存的显示。

- [ ] **Step 1: 写失败的测试** `TestESClusterSettingsDefaultsHidden`：defaults 行默认看不到，`.` 后看到。`TestESClusterSettingsEdit`：改一个 transient、删一个 persistent、在 defaults 行上改一个 → `<Space>w` → 确认框要输入连接名 → 假后端收到 body `{"persistent":{"a":null,"c":"5"},"transient":{"b":"2"}}`。`TestESClusterSettingsArray`：输入 `["zone","rack"]` 发出 JSON 数组。`TestESClusterSettingsReadOnly`：只读时 `<Space>w` 提示 `readOnlyHint`、没有请求。`TestESClusterSettingsTickKeepsEdits`：暂存后来一次 tick 刷新，暂存还在。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run ESClusterSettings`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): edit Elasticsearch cluster settings`

### Task 9: Index 标签页：Mapping 加字段、Stats 跳到分片

**Files:**
- Create: `internal/ui/esmappingedit.go`
- Modify: `internal/ui/esindex.go`（`Handle` 里 Mapping / Stats 部分的分支）
- Test: `internal/ui/esmappingedit_test.go`

**Interfaces:**
- Consumes: `es.PutMapping`（Task 2）、`esx.MappingConflicts`（Task 2）、`openESShards`（Task 6）、`writeTempEdit` / `readEditedFile` / `editFileMsg` / `editDoneMsg` / `editTextMsg`（现有）、`t.write(section, done, fn)`（`esindexedit.go`）
- 行为（spec §7.4）：
  - Mapping 部分 `o`：target 不是单个具体索引（别名、通配、data stream）时提示 `add fields on one index (open it from Indices)`。否则写临时文件 `datavim-mapping-*.json`，内容是模板 `{\n  "new_field": { "type": "keyword" }\n}\n`（顶层就是 properties），打开 `$EDITOR`。保存后：不是合法 JSON → 状态栏报错；`MappingConflicts` 有 conflicts → 状态栏 `cannot change an existing field's type (status: keyword → long); reindex into a new index instead`，不发请求；added 为空 → `no new fields`；否则按 `Classify("PUT", <index>/_mapping, body)`（写）确认，正文 `add fields to <index>: a, b`，成功提示 `added 2 fields` 并重读 mapping。
  - Stats 部分 `gd`：当前行的索引（单索引 target 时就是它）→ `openESShards(conn, index)`。

- [ ] **Step 1: 写失败的测试** `TestESAddFields`：模拟编辑结果 `{"note":{"type":"text"}}` → 确认 → `PUT orders/_mapping` body `{"properties":{"note":{"type":"text"}}}`，之后重读 `_mapping`。`TestESAddFieldsConflict`：`{"status":{"type":"long"}}` → 状态栏包含 `reindex into a new index`、没有 PUT。`TestESAddFieldsAlias`：别名 target 上 `o` 提示上面的文字。`TestESStatsShards`：Stats 第二行上 `gd` → 当前标签页是状态页、Shards 过滤为那一行的索引。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run 'ESAddFields|ESStatsShards'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): add Elasticsearch mapping fields and jump to an index's shards`

### Task 10: Update by query

**Files:**
- Modify: `internal/ui/esdbq.go`（泛化任务轮询，加 update by query）、`internal/ui/esdocs.go`（`UpdateByQuery` 分支）
- Test: `internal/ui/esdbq_test.go`

**Interfaces:**
- Consumes: `es.UpdateByQuery`（Task 2）、`es.ResolveIndex`、`es.Count`、`esx.SearchQuery`、`confirmESCmd`、`writeTempEdit` 一族、`openESTasks`（Task 7）
- Produces: `esTaskMsg` 加 `op string`（`"delete"` / `"update"`）；`pollTask(task, op string)`；`tasked` 按 op 写 `deleting… N / M` / `updating… N / M`、`deleted N documents` / `updated N documents`（update 的完成数用 `Updated`）。新消息 `esUBQPlanMsg{tabID int; conn string; query json.RawMessage; script string; resolved []string; count int64; err error}`、`esUBQRunMsg{tabID int; indices []string; query json.RawMessage; script string}`。
- 行为（spec §7.2）：`<Space>u`：只读先拒绝；写临时文件 `datavim-ubq-*.painless`，内容是注释头（`// update by query on <target> · filter: <query 或 (all documents)>`，`// ctx._source.field = value;`）加空行；`$EDITOR` 退出后去掉 `//` 注释行，剩下为空 → `no script; nothing to do`。然后和 delete by query 一样先 `ResolveIndex` + `Count`，用 `esPlan{Class: Classify("POST", target+"/_update_by_query", nil)（危险）, Resolved, Count, Expect: target}` 确认，正文 `update N documents matching <q> in <indices>` 和脚本前 5 行；没有过滤条件时正文写 `updates ALL N documents in <indices>`（仍是危险级，不是特别危险——spec §9.1 只把 delete 列为特别危险）。执行用展开后的索引名。提交成功后状态栏 `updating…` 并开始轮询，同时状态栏提示 `<keyHint ServerInfo> → Tasks to watch or cancel`。

- [ ] **Step 1: 写失败的测试** `TestESUpdateByQuery`：过滤 `status:new`，脚本 `ctx._source.status = 'old'` → 确认框正文包含 `update 3 documents` 和脚本行，要求输入 `orders` → 假后端收到 `POST orders/_update_by_query?wait_for_completion=false`，body 的 `script.source` 是脚本、`query` 是过滤条件；之后 `_tasks/<id>` 轮询到完成，状态 `updated 3 documents`。`TestESUpdateByQueryEmptyScript`：只有注释 → `no script; nothing to do`、没有请求。`TestESUpdateByQueryReadOnly`：只读时不打开编辑器。`TestESDeleteByQueryStillWorks`：E1 的 delete by query 测试保持通过（op 泛化后）。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run 'ESUpdateByQuery|ESDeleteByQuery|DBQ'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch update by query`

### Task 11: Reindex

**Files:**
- Create: `internal/ui/overlay/reindexform.go`、`internal/ui/esreindex.go`
- Modify: `internal/ui/esindexops.go`（`esIndexMenu` 加 `Reindex…`）
- Test: `internal/ui/overlay/reindexform_test.go`、`internal/ui/esreindex_test.go`

**Interfaces:**
- Consumes: `es.Reindex`、`es.IndexExists`、`es.RawSettings`、`es.RawMappings`、`es.CreateIndex`、`es.Aliases`（Task 2、E1）、`esx.CopyableSettings`、`esx.SearchQuery`、`confirmES`、`openESTasks`
- Produces:
  ```go
  // overlay
  type ReindexFormMsg struct{ Dest, Query, OpType, Conflicts string }
  func NewReindexForm(keys *keymap.Keymap, source string) *ReindexForm
  // 字段：Dest（默认 "<source>-reindex"）、Query（条件框语法，空 = 全部）、Op type（index / create，左右切换）、Conflicts（abort / proceed）；
  // 提交时校验：Dest 非空、!= source、不含 * , 空格和大写（复用 validIndexName）；Query 用 esx.SearchQuery 校验
  ```
  `esReindexMsg`（菜单项发出：conn、source）、`esReindexPlanMsg`（dest 是否存在、是否是别名、源的 mappings / settings）、`esReindexRunMsg{conn string; spec es.ReindexSpec; create *es.NewIndex}`。
- 行为（spec §7.1）：菜单 `Reindex…` → 表单 → 读 `IndexExists(dest)` 和别名列表：dest 是别名 → 状态栏 `<dest> is an alias; reindex into an index`；dest 存在 → 直接确认（写），正文 `reindex <source> → <dest> · op_type <x> · conflicts <y>` 和 query；dest 不存在 → `overlay.NewChoice("<dest> does not exist", ["Create it with <source>'s mapping and settings", "Let Elasticsearch create it (dynamic mapping)"], …)`，选第一项时正文多一行 `create <dest>: N shards, M replicas, <source>'s mapping`，执行时先 `CreateIndex(NewIndex{Name, Shards, Replicas, Mappings: 源 mapping, Settings: CopyableSettings})` 再 `Reindex`（建索引失败不发 reindex）。成功后 `openESTasks(conn)` 并提示 `reindex started: <task>`。

- [ ] **Step 1: 写失败的测试** `TestReindexForm`（overlay）：默认 dest 是 `orders-reindex`；dest = source、dest 含 `*`、query 语法错误各自报错不提交；切换 op type / conflicts 后 `ReindexFormMsg` 正确。`TestESReindexCreateDest`：dest 不存在、选第一项 → 确认 → 假后端依次收到 `PUT orders-v2`（body 的 settings 没有 `uuid`、有源的 analysis，mappings 等于源）和 `POST _reindex?wait_for_completion=false`；当前标签页是状态页 Tasks 部分。`TestESReindexExistingDest`：只有 `POST _reindex`。`TestESReindexAliasDest`：dest 是别名 → 提示、没有写请求。`TestESReindexCreateFails`：`PUT` 返回 400 → 状态栏错误、没有 `_reindex`。`TestESReindexReadOnly`：菜单项提示 `readOnlyHint`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/... -run 'Reindex'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch reindex`

### Task 12: Index Templates：树节点和模板标签页

**Files:**
- Create: `internal/ui/estemplate.go`
- Modify: `internal/ui/estree.go`（`esSecTemplates`、读取、节点、`openESNode`、`esTreeWrite` 的 DeleteNode）、`internal/ui/panes/tree/tree.go`（`KindESTemplate`：叶子、图标 / 样式和 `KindESIndex` 一致）、`internal/ui/nodemenu.go`（模板节点的菜单：Open、Delete）、`internal/ui/app.go`（新消息的路由）
- Test: `internal/ui/estemplate_test.go`、`internal/ui/estree_test.go`

**Interfaces:**
- Consumes: `es.Templates`、`es.PutTemplate`、`es.DeleteTemplate`（Task 3）、`confirmES` / `confirmESCmd`、`writeTempEdit` 一族
- Produces: `esTree.templates []es.Template`；节点 `tree.Node{Kind: tree.KindESTemplate, Section: esSecTemplates, Key: kind.String()+"/"+name, Name: name, Detail: "<kind> · <patterns, 逗号分隔> · p<priority>"}`；`func esTemplateTabKey(conn, key string) string`；`type esTemplateTab struct{…}`（`Context()` 是 `keymap.ESTemplate`）。
- 行为（spec §4、§7.3）：
  - 树在 Aliases / Data Streams 后加 `Index Templates (N)`；`.` 开头的模板跟随 `showSystem` 隐藏；`Templates` 返回 `ErrNotSupported` 时节点显示 `not supported by this cluster`（`st.errs`，和其他部分一致）。
  - 回车打开模板标签页：标题 `<name> · <kind> template`，内容是缩进 2 的 JSON，一行一格放进 grid（单列，可以 `/` 查找、`yy` 复制整个 JSON）。`<Space>r` 重读。
  - `<Space>v`：JSON 写到 `datavim-template-*.json` → `$EDITOR` → 不是合法 JSON 报错；没有变化 → `no changes`；否则 `Classify("PUT", path, body)`（写）确认，正文 `update <kind> template <name>`，prod 显示 body 前 5 行；成功后重读标签页和树。
  - 树上 `dd`：`Classify("DELETE", path, nil)`（危险），输入模板名确认；成功后重读树，关掉这个模板的标签页（如果开着）。

- [ ] **Step 1: 写失败的测试** `TestESTreeTemplates`：假集群三类各一个、一个 `.` 开头的 → 节点 `Index Templates (3)`，`.` 后 4；Detail 是 `composable · logs-* · p100`。`TestESTreeTemplatesUnsupported`：三类都 404 → 节点带 `not supported by this cluster`，其他部分正常。`TestESTemplateTab`：回车打开，`yy` 复制的 JSON 等价于 `Body`。`TestESTemplateEdit`：模拟编辑结果改了 priority → 确认 → `PUT _index_template/logs`，body 是新内容；内容不变 → `no changes`；不合法 JSON → 报错无请求。`TestESTemplateDelete`：`dd` → 输入 `logs` → `DELETE _template/logs`（legacy），树重读。`TestESTemplateReadOnly`：`<Space>v` 和 `dd` 都提示 `readOnlyHint`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/... -run 'ESTreeTemplates|ESTemplate'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch index templates`

### Task 13: 集成测试、文档、CHANGELOG 和真机验证

**Files:**
- Modify: `internal/es/integration_test.go`、`CHANGELOG.md`、`README.md`、`README.zh-CN.md`、`docs/superpowers/specs/2026-10-21-elasticsearch-design.md`（状态行：E2 已实现）

- [ ] **Step 1: 写集成测试**（`integration` tag，在现有 `TestIntegration` 的镜像矩阵里，ES 7.10.2 和 8.x 必跑，OpenSearch 只跑模板的读）：
  - `Shards` / `AllocationExplain`：单节点上建 `number_of_replicas: 1` 的索引，`Shards` 里有 UNASSIGNED 副本，explain（`primary:false`）返回的 JSON 经 `ExplainSummary` 后包含 `same_shard`。
  - `Nodes`、`Allocation`、`PendingTasks`、`ClusterSettings` 都能解析（非空、无错误）。
  - `Reindex`：5,000 条文档，`CopyableSettings` 建目标后 reindex，轮询 `GetTask` 到完成，目标 `_count` = 5,000，目标 mapping 等于源。
  - `UpdateByQuery`：按 term 更新 10 条，`GetTask` 的 `Updated` = 10。
  - `Tasks` + `CancelTask`：用 `requests_per_second=1` 的 reindex 造一个慢任务，`Tasks` 里能看到，取消后 `GetTask` 完成且被取消；再取消一次返回 `ErrTaskGone`。
  - `PutMapping` 加字段成功；改已有字段类型返回 400（这是 ES 的行为，UI 在发之前就拦住）。
  - 模板：三类 put → `Templates` 里有 → delete → 没有。
  - `PutClusterSettings`：transient `cluster.routing.allocation.cluster_concurrent_rebalance: "3"`，读回是 `3`，再设 null 恢复。
- [ ] **Step 2: 运行** `make lint`、`make test`、`make test-integration`，全部 PASS。
- [ ] **Step 3: 写文档**：`CHANGELOG.md` `[Unreleased]` 的 Added 加：集群状态页的 Nodes / Shards（allocation explain）/ Allocation / Tasks（取消）/ Pending / Settings；Reindex；Update by query；Index Templates；Mapping 加字段。README 中英文：ES 功能列表和按键表加上这些（按键名照 `defaults.go`）。
- [ ] **Step 4: 真机验证**：`make build`，`./bin/datavim --config <dir> --debug`：(a) 本地 Docker ES 7.10.2 单节点：状态页 yellow → Overview 回车到 Shards → 未分配副本上回车看到原因；(b) Reindex 一个索引到新索引（选按源建），Tasks 里看到进度，完成后两边 `_count` 相同；(c) Documents 里 `<Space>u` 改几条，再 `F` 查到改后的值；(d) 改一个模板的 priority 后在控制台 `GET _index_template/<name>` 看到新值；(e) 设 `env = "prod"`：集群 settings 提交要输入连接名、确认框显示 body；(f) 作者的内网 7.10.2 集群**设成只读**打开状态页的每个部分，能看，所有写操作被拒绝；(g) `~/.local/state/datavim/datavim.log` 里没有请求 body。
- [ ] **Step 5: 提交** `docs: Elasticsearch cluster operations in the readmes and the changelog`（集成测试单独提交 `test(es): integration tests for cluster operations`）
