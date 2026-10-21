# Elasticsearch E3（其余功能）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 树上的 Snapshots（仓库 → 快照，新建 / 删除快照和仓库，恢复快照并拒绝覆盖打开的索引）；树上的 ILM Policies 和 Ingest Pipelines（看、在 `$EDITOR` 里改、删），Index 标签页 Stats 显示 ILM 状态，Documents 里用选中的文档 `_simulate` 一个 pipeline；集群开了安全功能时状态页的 Users / Roles 部分（ES `_security` 和 OpenSearch `_plugins/_security`）；`[[mask]]` 支持 ES 的索引和 `_source` 路径。

**Architecture:** 不改 E1 / E2 的结构。`internal/es` 加 `snapshot.go`、`ilm.go`（ILM + pipeline）、`security.go`，全部是 `Doer` 上的普通函数，写函数进 `es.WriteFuncs`。`internal/esx` 加纯函数（恢复后的索引名、敏感请求、`Masker`、partial doc）。JSON 打码的路径算法从 `kmsgx` 搬到新的纯包 `internal/jsonmask`，Kafka 和 ES 共用。UI 里 E2 的 `esTemplateTab` 泛化成 `esObjectTab`（模板、ILM 策略、pipeline、角色共用"看 JSON / `$EDITOR` 改 / 删"）；快照仓库节点像 `KindSchemaSubject` 一样展开时才读；Users / Roles 是 `esServerTab` 的两个新部分，没开安全功能时跳过。

**Tech Stack:** Go 1.26，标准库 `net/http` / `encoding/json` / `regexp`；测试沿用 `es/fake_test.go` 的 `fakeDoer`、`ui/fakees_test.go` 的假集群、`testcontainers` 的 ES 7.10.2 / 8.x / 9.x 和 OpenSearch 2.x 镜像。实现 Task 2–4 前用 Context7 查 ES 7.10 和 8.x 的 `_snapshot`、`_cat/snapshots`、`_snapshot/<repo>/<snap>/_status`、`_restore`、`_ilm/policy`、`_ilm/explain`、`_ingest/pipeline`、`_simulate`、`_xpack`、`_security/user`、`_security/role`，以及 OpenSearch 2.x 的 `_plugins/_security/api/{account,internalusers,roles}` 的确切请求和响应格式。

**Spec:** `docs/superpowers/specs/2026-10-21-elasticsearch-design.md`（本计划覆盖 E3：§8 全部、§4 的 Snapshots 节点；安全规则仍是 §9，敏感信息是 §9.4）

## Global Constraints

- 所有请求都走 `Backend.ES` 返回的包了检查的 `Doer`；新增的写函数一律加进 `es.WriteFuncs`，并在 `internal/session/es_test.go` 的 `TestESReadOnlyRefusesEveryWrite` 的 `calls` 里加对应项，`internal/es/write_test.go` 的 `TestWriteFuncsListed` 数量跟着改（现在 21）。
- 分级用 `esx.Classify`。已有的分级不改：建快照是**写**；删快照 / 删仓库 / 建仓库（`PUT _snapshot/<repo>`）/ 恢复是**危险**；put ILM 策略 / pipeline 是**写**，删除是**危险**；`_simulate` 是**读**；`_security` 下的 put / `_password` / `_enable` / `_disable` 是**写**，删除是**危险**。Task 1 只补 OpenSearch 的 `_plugins/_security/api/...` 和 `PATCH`。确认一律走 `confirmES` / `confirmESCmd`（`esguard.go`）。危险级确认要输入的名字：快照用快照名，仓库用仓库名，恢复用快照名，ILM 策略 / pipeline / 角色 / 用户用它们自己的名字。
- **密码**（spec §9.4）：新建用户和改密码的密码只用 `overlay.NewSecret` 输入，**不进**确认框（prod 的确认框也不显示这两类请求的 body）、状态栏、日志、审计、控制台历史。`esPlan.Body` 对这两类请求一律留空。
- 恢复快照一律 `include_global_state=false`（表单里不提供；恢复全局状态会改集群 settings 和模板，作者不需要，我的决定）、`wait_for_completion=false`；建快照 `wait_for_completion=false`。
- 快照列表用 `_cat/snapshots/<repo>?format=json`，**不读每个快照的大小**（大小要对每个快照读 `_status`，仓库在 S3 上时很慢）；大小显示在快照标签页里（我的决定，和 spec §8.1 的列表列有出入，在 README 里说明）。
- "系统对象"的规则（树上 `.` 切换）：名字以 `.` 开头，或者 body 里 `_meta.managed == true`（8.x 自带的 `logs`、`metrics` 等）。模板、ILM 策略、pipeline 统一用这个规则（模板原来只看 `.`，跟着改）。
- OpenSearch 上没有 ILM（ISM 不做）：ILM Policies 节点不显示，Stats 不显示 ILM 列，不发 `_ilm` 请求。
- 不做：新建 ILM 策略 / pipeline / 角色（spec 只要求看、改、删，和 E2 的模板一致）；role mapping；API key 管理；`_snapshot/<repo>/_verify`、`_cleanup`。
- UI 文字英文；按键名用 `keyHint`；新按键进 `keymap/defaults.go` 并带描述；提交信息 Conventional Commits 带 scope，**不加 Co-Authored-By**。用户可见的改动在 Task 13 统一写进 `CHANGELOG.md` 的 `[Unreleased]`，README 中英文同步。

## Review Focus

1. **恢复时目标索引已存在**：rename 之后的名字有已存在且打开的索引 → 拒绝，状态栏列出名字（`orders, users exist and are open: close or delete them, or restore under a rename`），不发请求；已存在但 closed 的允许（ES 会恢复进去）；rename pattern 不是合法正则 → 表单报错。测试在 Task 1（`RestoreTargets`）和 Task 9。
2. **密码泄漏**：prod 连接上新建用户 / 改密码的确认框里没有 body、没有密码；控制台里手写的 `PUT _security/user/x {"password":…}` 和 `PUT _plugins/_security/api/internalusers/x` 的历史 body 是 `<redacted>`（现在只认 `_password`）。测试在 Task 1（`SensitiveRequest`）和 Task 11。
3. **一个仓库读不出来**（S3 仓库配置错，`_cat/snapshots` 返回 500 `repository_exception`）：只有那个仓库节点显示错误，其他仓库和整棵树正常；树的加载不读任何仓库的快照（展开时才读）。测试在 Task 8。
4. **打码的文档被编辑**：被打码的字段不能 `i`；整篇 `<Space>v` 被拒绝；改同一篇里没打码的字段时用 `_update` 只发改了的字段，绝不把 `******` 写回去。`yy` / `ys` / `yr` / `yu`、详情和导出看到的都是 `******`。测试在 Task 12。
5. **集群没开安全功能**（作者的内网 7.10.2）：状态页没有 Users / Roles，`[` / `]` 跳过它们；探测只在打开状态页时做一次，刷新 tick 不再发；有安全功能但账号没权限（403）时部分显示 `permission denied`。测试在 Task 11。

---

### Task 1: `esx` 分级补充、敏感请求、恢复后的索引名

**Files:**
- Modify: `internal/esx/classify.go`、`internal/esx/endpoints.go`、`internal/ui/esconsole.go`（`recordRequest`）
- Create: `internal/esx/snapshot.go`
- Test: `internal/esx/classify_test.go`、`internal/esx/snapshot_test.go`、`internal/ui/esconsole_test.go`

**Interfaces:**
- Produces:
  ```go
  // classify.go：PUT / PATCH _plugins/_security/api/<type>/<name> → LevelWrite "put security object"；
  // DELETE 同一路径 → LevelDanger "delete security object"；其他 PATCH 仍是 opUnknown（危险）
  func SensitiveRequest(method, path string) bool
  // 带密码的请求：_security/user/<n>（PUT / POST）、_security/user/<n>/_password、
  // _plugins/_security/api/internalusers/<n>（PUT / PATCH）、_plugins/_security/api/account（PUT）
  func RestoreTargets(indices []string, pattern, replacement string) ([]string, error)
  // pattern 为空时原样返回；否则按 Go regexp 编译（不合法报错），每个名字 ReplaceAllString(name, replacement)
  // （ES 的 $1 写法和 Go 一致）；结果有重名时报错 "<a> and <b> both restore as <c>"
  func SnapshotName(now time.Time) string // "snapshot-2026.10.22-153000"（UTC）
  ```
  `Endpoints` 加：`GET _cat/snapshots`、`GET _snapshot/{id}`、`PUT _snapshot/{id}`、`POST _snapshot/{id}/{id}/_restore`、`GET _ilm/explain`（`{index}/_ilm/explain`）、`PUT _ilm/policy/{id}`、`PUT _ingest/pipeline/{id}`、`POST _ingest/pipeline/{id}/_simulate`、`GET _security/user`、`GET _security/role`、`GET _plugins/_security/api/internalusers`。
- `recordRequest` 改用 `esx.SensitiveRequest(r.Method, r.Path)` 判断是否把 body 换成 `<redacted>`。

- [ ] **Step 1: 写失败的测试** `TestClassify` 表里加：`PATCH _plugins/_security/api/internalusers/bob` → write；`PUT _plugins/_security/api/roles/r` → write；`DELETE _plugins/_security/api/internalusers/bob` → danger；`PATCH _anything` → danger `unknown request`；`PUT _snapshot/r` → danger；`PUT _snapshot/r/s` → write；`POST _snapshot/r/s/_restore` → danger。`TestEveryEndpointClassified` 自动覆盖新的 Endpoints。`TestSensitiveRequest` 表驱动：上面每个带密码的路径 true，`GET _security/user`、`PUT _security/role/r`、`POST orders/_doc` false。`TestRestoreTargets`：`["logs-1","logs-2"]`、`"logs-(.+)"`、`"restored-logs-$1"` → `["restored-logs-1","restored-logs-2"]`；pattern 空原样；`"("` 报错；`"logs-.*"` → `"x"` 重名报错。`TestSnapshotName`。`TestESConsoleHistoryRedacts`：执行 `PUT _security/user/bob {"password":"pw1"}` 后 Store 里的条目不含 `pw1`、含 `<redacted>`。
- [ ] **Step 2: 运行** `go test -race ./internal/esx ./internal/ui -run 'Classify|Endpoint|Sensitive|RestoreTargets|SnapshotName|HistoryRedacts'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/esx ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(esx): classify OpenSearch security requests, redact password requests, restore names`

### Task 2: `es` 快照和仓库

**Files:**
- Create: `internal/es/snapshot.go`
- Modify: `internal/es/write.go`（`WriteFuncs`）、`internal/es/write_test.go`、`internal/session/es_test.go`
- Test: `internal/es/snapshot_test.go`（`internal/es/testdata/` 放 7.10 和 8.x 的响应样本）

**Interfaces:**
- Consumes: `do`、`missingAPI`（`template.go`）、`ErrUnsupported`
- Produces:
  ```go
  type Repository struct{ Name, Type string; Settings map[string]string } // settings 的值用 esx.CellText
  func Repositories(ctx context.Context, d Doer) ([]Repository, error) // GET _snapshot；按名字排序；missingAPI → ErrUnsupported

  type Snapshot struct {
      Name, State string    // State: SUCCESS / IN_PROGRESS / PARTIAL / FAILED / INCOMPATIBLE
      Start     time.Time   // 没有时零值
      Duration  time.Duration
      Indices   int
      Shards, FailedShards int
  }
  func Snapshots(ctx context.Context, d Doer, repo string) ([]Snapshot, error)
  // GET _cat/snapshots/<repo>?format=json&h=id,status,start_epoch,duration,indices,total_shards,failed_shards&s=start_epoch:desc
  // duration 是 "1.2m" 这类，用 time.ParseDuration 能解析的部分（"1.2m"→72s），解析不了为 0

  type SnapshotDetail struct {
      Snapshot
      Indices            []string // 覆盖 Snapshot.Indices 的数量
      DataStreams        []string
      IncludeGlobalState bool
      Version            string
      Failures           []string // "<index>[<shard>] <reason>"
      SizeBytes          int64    // _status 的 stats.total.size_in_bytes；读不到 -1
      DoneBytes          int64    // IN_PROGRESS 时 stats.processed.size_in_bytes（7.10: stats.incremental / processed），否则 = SizeBytes
  }
  func GetSnapshot(ctx context.Context, d Doer, repo, name string) (SnapshotDetail, error)
  // GET _snapshot/<repo>/<name>，再 GET _snapshot/<repo>/<name>/_status（失败只让 SizeBytes = -1）

  type NewSnapshot struct{ Repo, Name string; Indices []string; IncludeGlobalState bool } // Indices 为空: 全部
  func CreateSnapshot(ctx context.Context, d Doer, s NewSnapshot) error
  // PUT _snapshot/<repo>/<name>?wait_for_completion=false {"indices":"a,b","include_global_state":false}
  func DeleteSnapshot(ctx context.Context, d Doer, repo, name string) error

  type RestoreSpec struct {
      Repo, Name             string
      Indices                []string // 为空: 全部
      RenamePattern, RenameReplacement string
      IncludeAliases         bool
  }
  func RestoreSnapshot(ctx context.Context, d Doer, r RestoreSpec) error
  // POST _snapshot/<repo>/<name>/_restore?wait_for_completion=false
  // {"indices":"a,b","include_global_state":false,"include_aliases":…,"rename_pattern":…,"rename_replacement":…}（rename 为空时不发）
  func PutRepository(ctx context.Context, d Doer, name string, body json.RawMessage) error // PUT _snapshot/<name>
  func DeleteRepository(ctx context.Context, d Doer, name string) error                  // DELETE _snapshot/<name>
  ```
  `WriteFuncs` 加 `CreateSnapshot`、`DeleteSnapshot`、`RestoreSnapshot`、`PutRepository`、`DeleteRepository`（共 26）。名字一律 `url.PathEscape`。

- [ ] **Step 1: 写失败的测试** `TestRepositories`（两个仓库，`fs` 的 `location` 在 Settings 里；空 `{}` → 空切片无错误；404 → `ErrUnsupported`）。`TestSnapshots`：path 完全等于上面写的；`start_epoch` `"1729600000"` → 对应时间；duration `"1.2m"` → 72s、`"500ms"` → 0.5s。`TestGetSnapshot`：7.10 和 8.x 样本，`Failures` 一行格式正确；`_status` 返回 500 → `SizeBytes == -1`、没有错误。`TestCreateSnapshot`：body `{"indices":"a,b","include_global_state":false}`；`Indices` 空时 body 没有 `indices`。`TestRestoreSnapshot`：body 不含 rename 字段 / 含 rename 字段两种，`include_global_state` 恒为 false。`TestWriteFuncsListed` 改成 26，session `calls` 加 5 项。
- [ ] **Step 2: 运行** `go test -race ./internal/es ./internal/session -run 'Repositor|Snapshot|Restore|WriteFuncs|ReadOnly'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/es ./internal/session`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): snapshots and snapshot repositories`

### Task 3: `es` ILM 策略和 ingest pipeline

**Files:**
- Create: `internal/es/ilm.go`
- Modify: `internal/es/write.go`、`internal/es/write_test.go`、`internal/session/es_test.go`、`internal/es/template.go`（`Template` 加 `System bool`）
- Test: `internal/es/ilm_test.go`、`internal/es/template_test.go`

**Interfaces:**
- Produces:
  ```go
  func systemObject(name string, body json.RawMessage) bool // "." 开头或 _meta.managed == true（Global Constraints）

  type Policy struct {
      Name   string
      Phases []string        // 按 hot, warm, cold, frozen, delete 的顺序
      InUse  int             // in_use_by.indices 的数量；7.10 没有时 -1
      System bool
      Body   json.RawMessage // PUT 要的 body：{"policy": …}
  }
  func Policies(ctx context.Context, d Doer) ([]Policy, error)   // GET _ilm/policy；missingAPI → ErrUnsupported；按名字排序
  func PutPolicy(ctx context.Context, d Doer, name string, body json.RawMessage) error
  func DeletePolicy(ctx context.Context, d Doer, name string) error

  type ILMState struct {
      Index, Policy, Phase, Action, Step string // 不受管时 Policy 为空
      Failed string // step == "ERROR" 时 step_info.reason（没有时 type）
  }
  func ILMExplain(ctx context.Context, d Doer, target string) (map[string]ILMState, error) // GET <target>/_ilm/explain

  type Pipeline struct {
      Name, Description string
      Processors        int
      System            bool
      Body              json.RawMessage // GET 返回的定义本身，就是 PUT 的 body
  }
  func Pipelines(ctx context.Context, d Doer) ([]Pipeline, error) // GET _ingest/pipeline；空 {} 或 404 → 空切片
  func PutPipeline(ctx context.Context, d Doer, name string, body json.RawMessage) error
  func DeletePipeline(ctx context.Context, d Doer, name string) error
  func SimulatePipeline(ctx context.Context, d Doer, name string, docs []esx.Hit) (json.RawMessage, error)
  // POST _ingest/pipeline/<name>/_simulate?verbose=true {"docs":[{"_index":…,"_id":…,"_source":…}]}；返回响应原样
  ```
  `Template.System` 用 `systemObject` 填。`WriteFuncs` 加 `PutPolicy`、`DeletePolicy`、`PutPipeline`、`DeletePipeline`（共 30）。

- [ ] **Step 1: 写失败的测试** `TestPolicies`：7.10 样本（没有 `in_use_by` → `InUse == -1`）和 8.x 样本（`_meta.managed: true` 的 `logs` → `System`）；`Phases` 顺序是 `hot, warm, delete` 而不是 JSON 里的顺序；`Body` 是 `{"policy":…}` 且不含 `version`、`modified_date`。`TestILMExplain`：受管、不受管、ERROR 三种。`TestPipelines`：两个 pipeline，`Processors` 数量；`{}` → 空。`TestSimulatePipeline`：body 的 `docs[0]._source` 等于传入的 Source，path 带 `verbose=true`。`TestTemplatesSystem`：`.monitoring` 和 `_meta.managed` 的模板 `System` 为 true。`TestWriteFuncsListed` 改成 30，session `calls` 加 4 项。
- [ ] **Step 2: 运行** `go test -race ./internal/es ./internal/session -run 'Polic|ILM|Pipeline|TemplatesSystem|WriteFuncs|ReadOnly'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/es ./internal/session`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): ILM policies, ILM explain and ingest pipelines`

### Task 4: `es` 用户和角色（ES 和 OpenSearch）

**Files:**
- Create: `internal/es/security.go`
- Modify: `internal/es/write.go`、`internal/es/write_test.go`、`internal/session/es_test.go`
- Test: `internal/es/security_test.go`

**Interfaces:**
- Produces:
  ```go
  type SecurityAPI int
  const ( SecurityNone SecurityAPI = iota; SecurityES; SecurityOpenSearch )
  func Security(ctx context.Context, d Doer, info Info) (SecurityAPI, error)
  // ES: GET _xpack?categories=features → features.security.enabled；missingAPI → SecurityNone
  // OpenSearch: GET _plugins/_security/api/account：200 / 401 / 403 → SecurityOpenSearch；missingAPI → SecurityNone
  // 其他错误返回

  type User struct {
      Name               string
      Roles              []string // ES roles；OpenSearch opendistro_security_roles
      BackendRoles       []string // 只有 OpenSearch
      FullName, Email    string   // 只有 ES
      Enabled            bool     // OpenSearch 恒为 true
      Reserved           bool     // ES metadata._reserved；OpenSearch reserved
      Metadata           json.RawMessage // ES，UpdateUser 原样发回
  }
  func Users(ctx context.Context, d Doer, api SecurityAPI) ([]User, error) // 按名字排序；OpenSearch 跳过 hidden
  func CreateUser(ctx context.Context, d Doer, api SecurityAPI, u User, password string) error
  // ES: PUT _security/user/<n> {"password","roles","full_name","email"}；OpenSearch: PUT …/internalusers/<n> {"password","opendistro_security_roles","backend_roles"}
  func UpdateUser(ctx context.Context, d Doer, api SecurityAPI, u User) error
  // ES: PUT _security/user/<n> 不带 password（ES 保留原密码），带 roles/full_name/email/metadata；
  // OpenSearch: PATCH …/internalusers/<n> [{"op":"add","path":"/opendistro_security_roles","value":…},{"op":"add","path":"/backend_roles","value":…}]
  func SetUserEnabled(ctx context.Context, d Doer, api SecurityAPI, name string, enabled bool) error
  // ES: PUT _security/user/<n>/_enable|_disable；OpenSearch: 不发请求，返回 ErrUnsupported
  func ChangePassword(ctx context.Context, d Doer, api SecurityAPI, name, password string) error
  // ES: POST _security/user/<n>/_password {"password"}；OpenSearch: PATCH …/internalusers/<n> [{"op":"add","path":"/password","value":…}]
  func DeleteUser(ctx context.Context, d Doer, api SecurityAPI, name string) error

  type Role struct {
      Name     string
      Cluster  []string // ES cluster；OpenSearch cluster_permissions
      Indices  int      // index 权限条数
      Reserved bool
      Body     json.RawMessage // PUT 的 body：去掉 transient_metadata / metadata._reserved（ES）、reserved / hidden / static（OpenSearch）
  }
  func Roles(ctx context.Context, d Doer, api SecurityAPI) ([]Role, error)
  func PutRole(ctx context.Context, d Doer, api SecurityAPI, name string, body json.RawMessage) error
  func DeleteRole(ctx context.Context, d Doer, api SecurityAPI, name string) error
  ```
  `WriteFuncs` 加 `CreateUser`、`UpdateUser`、`SetUserEnabled`、`ChangePassword`、`DeleteUser`、`PutRole`、`DeleteRole`（共 37）。session 的 `calls` 用 `SecurityES` 调。

- [ ] **Step 1: 写失败的测试** `TestSecurity`：ES `enabled:false` → None、`true` → ES、`_xpack` 400 → None；OpenSearch 200 → OpenSearch、403 → OpenSearch、400 `no handler found` → None；500 → 错误。`TestUsers`：ES 样本（`elastic` 是 Reserved、一个 disabled 用户）、OpenSearch 样本（hidden 的被跳过、`backend_roles`）。`TestCreateUser` / `TestUpdateUser` / `TestChangePassword`：两种 API 的 method、path、body 完全等于上面写的；`UpdateUser` 的 ES body **没有** `password`。`TestSetUserEnabledOpenSearch`：返回 `ErrUnsupported`、`fakeDoer` 没收到请求。`TestRoles`：`Body` 不含 `transient_metadata`、`reserved`。`TestWriteFuncsListed` 改成 37，session `calls` 加 7 项；session 的 `TestESAudit` 加一条：`ChangePassword` 之后审计 `Detail` 不含密码。
- [ ] **Step 2: 运行** `go test -race ./internal/es ./internal/session -run 'Security|User|Password|Role|WriteFuncs|ReadOnly|Audit'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/es ./internal/session`，预期 PASS。
- [ ] **Step 5: 提交** `feat(es): users and roles on Elasticsearch and OpenSearch security`

### Task 5: 打码：`jsonmask`、按 driver 校验的规则、`esx.Masker`

**Files:**
- Create: `internal/jsonmask/jsonmask.go`、`internal/esx/mask.go`
- Modify: `internal/kmsgx/mask.go`（路径算法搬走，跳过 ES 规则）、`internal/config/mask.go`
- Test: `internal/jsonmask/jsonmask_test.go`（`kmsgx` 里路径相关的用例搬过来）、`internal/esx/mask_test.go`、`internal/config/mask_test.go`、`internal/kmsgx/mask_test.go`

**Interfaces:**
- Produces:
  ```go
  // jsonmask
  const Masked = "******"
  func Paths(raw []byte, paths [][]string) ([]byte, bool) // 原 kmsgx.maskJSON 的行为，段语法不变（key、key[]、[]、*）
  // kmsgx.Masked = jsonmask.Masked 保留

  // config
  type MaskRule struct { …; Indices []string `toml:"indices"` } // path.Match 通配
  func (r MaskRule) ForES() bool // 有 Indices，或任一 field 以 "_source." 开头
  // checkMasks 的新报错（rule N 前缀照旧）：
  //   fields mix Kafka (key / value) and Elasticsearch (_source.<path>) paths
  //   indices go with _source.<path> fields
  //   topics and headers are for Kafka rules, not _source.<path> fields
  //   field %q: want key, value, key.<path>, value.<path> or _source.<path>
  // ES 规则不允许 bare "_source"（整篇打码没有意义）

  // esx
  type Masker struct{ … }
  func NewMasker(rules []config.MaskRule, conn, env string) *Masker // 只取 ForES 的规则；没有命中时 nil
  func (m *Masker) MaskHit(h Hit) (Hit, bool)            // 按 h.Index 匹配 indices（空 = 全部）；Truncated 的 Source 整个换成 "******"
  func (m *Masker) Covers(index, field string) bool      // 这个字段（点分路径）被打码：编辑前检查
  func (m *Masker) MaskResponse(body []byte) ([]byte, bool)
  // 控制台响应：hits.hits[]、docs[]（_mget）、顶层 _index + _source（GET _doc）三种形状里的 _source；别的形状原样
  func PartialDoc(src json.RawMessage, fields []string) (json.RawMessage, error)
  // 从新的 _source 里只取这些点分路径，拼成 {"a":{"b":…}}，给 _update 用
  ```
  `kmsgx.NewMasker` 跳过 `ForES()` 的规则（否则没写 `topics` 的 ES 规则会作用于所有 topic）。

- [ ] **Step 1: 写失败的测试** `TestPaths`（从 `kmsgx` 搬来的表，全部照旧通过）。`TestLoadMasksInvalid` 加四种新报错；`TestLoadMasks` 加一条 `connections=["es"]`、`indices=["users*"]`、`fields=["_source.password","_source.cards[].no"]` 的规则能读出来。`TestNewMaskerSkipsES`（kmsgx）：只有 ES 规则时返回 nil。`TestESMasker`：`users-1` 的 hit 打码 `password` 和 `cards[].no`，`orders` 的 hit 不动；没有 indices 的规则作用于所有索引；Truncated hit 的 Source 是 `"******"`；`Covers("users-1","cards.no")` true。`TestMaskResponse`：三种形状各一，`aggregations` 里的值不动。`TestPartialDoc`：`{"a":{"b":1,"c":2},"d":3}` 取 `["a.b"]` → `{"a":{"b":1}}`。
- [ ] **Step 2: 运行** `go test -race ./internal/jsonmask ./internal/config ./internal/kmsgx ./internal/esx`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/jsonmask ./internal/config ./internal/kmsgx ./internal/esx ./internal/ui`，预期 PASS（Kafka 的打码 UI 测试不变）。
- [ ] **Step 5: 提交** `feat(config): mask rules for Elasticsearch indices and _source paths`

### Task 6: 按键

**Files:**
- Modify: `internal/ui/keymap/keymap.go`、`internal/ui/keymap/defaults.go`（`esBindings`）、`internal/ui/keymap/kind.go`
- Test: `internal/ui/keymap/keymap_test.go`

**Interfaces:**
- Produces:
  - 上下文：E2 的 `ESTemplate` 改名 `ESObject`，**字符串仍是 `"es_template"`**（不破坏用户的 `keymap.toml`）；新增 `ESSnapshot Context = "es_snapshot"`。
  - 动作：`SimulatePipeline Action = "es.simulate_pipeline"`、`RestoreSnapshot Action = "es.restore_snapshot"`、`ToggleEnabled Action = "es.toggle_enabled"`、`ChangePassword Action = "es.change_password"`。
  - 绑定（引号里是描述）：
    - `ESObject`：`yy` 的描述改成 "Copy the JSON"，`<Space>v` "Edit in $EDITOR"，`<Space>r` "Reload"。
    - `ESSnapshot`：`gridBindings` + `<Space>r` Refresh "Reload the snapshot"、`R` RestoreSnapshot "Restore the snapshot…"、`d d` DeleteRow "Delete the snapshot…"。
    - `ESDocs`：`<Space>p` SimulatePipeline "Run an ingest pipeline on the selected document (_simulate)…"。
    - `ESServer`：`o` InsertRow "Users: new user"；`i` EditCell "Users: change roles"；`x` ToggleEnabled "Users: enable / disable"；`P` ChangePassword "Users: change the password…"；`enter` Open 的描述加 " · Roles: open the role"；`d d` KillClient 的描述改成 "Tasks: cancel the task · Users / Roles: delete…"。
    - Tree：`R` 在 Tree 上没有绑定，恢复走 NodeActions 菜单（Task 9）；`o` NewKey 的 ES 描述改成 "New index · new snapshot (on a repository) · new repository (on Snapshots)"。
  - `kind.go`：四个新动作都只标 `KindES`；`InsertRow`、`EditCell` 在 `ESServer` 上标 `KindES`。

- [ ] **Step 1: 写失败的测试** `TestESE3Bindings`：上面每个（context, 按键）解析到对应动作；`ESObject == "es_template"`；`TestNoConflicts` 覆盖 `ESSnapshot` 和改过的 `ESServer`、`ESDocs`（`<Space>p` 不和全局 leader 键冲突）；help 按 `KindES` 过滤时能看到四个新动作，按 `KindKafka` 过滤时看不到。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/keymap`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS（help / which-key 的快照测试如有变化一并更新）。
- [ ] **Step 5: 提交** `feat(keymap): Elasticsearch snapshot, pipeline and user keys`

### Task 7: `esObjectTab`；树上的 ILM Policies 和 Ingest Pipelines

**Files:**
- Rename: `internal/ui/estemplate.go` → `internal/ui/esobject.go`、`internal/ui/estemplate_test.go` → `internal/ui/esobject_test.go`
- Modify: `internal/ui/estree.go`（新部分、`systemObject` 规则）、`internal/ui/panes/tree/tree.go`（`KindESPolicy`、`KindESPipeline`：叶子，样式同 `KindESTemplate`）、`internal/ui/nodemenu.go`、`internal/ui/esindexops.go`（`esTreeWrite` 的 DeleteNode）、`internal/ui/app.go`（消息改名后的路由）
- Test: `internal/ui/esobject_test.go`、`internal/ui/estree_test.go`

**Interfaces:**
- Consumes: `es.Templates` / `PutTemplate` / `DeleteTemplate`（E2）、`es.Policies` / `PutPolicy` / `DeletePolicy`、`es.Pipelines` / `PutPipeline` / `DeletePipeline`（Task 3）、`es.Roles` / `PutRole` / `DeleteRole`（Task 4，Task 11 用）
- Produces:
  ```go
  type esObjKind int
  const ( objComposable esObjKind = iota; objLegacy; objComponent; objPolicy; objPipeline; objRole )
  // key "<kind>/<name>"，kind 字符串：composable / legacy / component / ilm / pipeline / role（模板的三个和 E2 一样，树和工作区里的老 key 不变）
  func (k esObjKind) label() string // "composable template" … "ILM policy", "ingest pipeline", "role"
  func (k esObjKind) path(name string) string // PUT / DELETE 的路径（role 按 esServerTab 缓存的 SecurityAPI 选 ES / OpenSearch 路径）
  func parseObjKey(key string) (esObjKind, string)
  func esObjectTabKey(conn, key string) string // "estemplate\x00…" 前缀保留不变（工作区恢复兼容）
  type esObjectTab struct{ …; kind esObjKind; api es.SecurityAPI /* role 用 */ }
  type esOpenObjectMsg struct{ conn, key string; api es.SecurityAPI }
  func (m *Model) askDeleteObject(conn, key string, api es.SecurityAPI) tea.Cmd // 危险级，输入名字
  ```
  E2 里 `esTemplate*Msg` 一律改名 `esObject*Msg`，行为不变；读 / 写 / 删按 kind 分派到对应的 `es` 函数；临时文件 `datavim-<kind>-*.json`；标题 `<name> · <label>`；状态 `updated <label> <name>` / `deleted <label> <name>`。
- 树（spec §8.2）：顺序 Indices、Aliases、Data Streams、Index Templates、**ILM Policies**、**Ingest Pipelines**、Snapshots（Task 8）。ILM 节点 Detail：`hot → warm → delete · 12 indices`（`InUse < 0` 时不写数量）；pipeline 节点 Detail：description（空时 `N processors`）。`System` 的对象跟随 `showSystem`。OpenSearch 上没有 ILM Policies 节点、不调用 `es.Policies`。`dd` 在这两类节点上走 `askDeleteObject`。节点菜单：Open、Delete…。

- [ ] **Step 1: 写失败的测试** E2 的 `TestESTemplate*` 改名后全部照旧通过。`TestESTreePolicies`：两个策略、一个 managed → `ILM Policies (1)`，`.` 后 2；Detail 是 `hot → delete · 3 indices`。`TestESTreeNoILMOnOpenSearch`：OpenSearch 假集群没有这个节点、假后端没收到 `_ilm` 请求。`TestESTreePipelines`。`TestESPolicyEdit`：打开策略、模拟编辑结果 → 确认（写）→ `PUT _ilm/policy/p1` body 是新内容。`TestESPipelineDelete`：树上 `dd` → 输入 `p1` → `DELETE _ingest/pipeline/p1`，树重读，开着的标签页被关掉。`TestESObjectReadOnly`：只读连接上 `<Space>v` 和 `dd` 都提示 `readOnlyHint`。`TestESTemplateWorkspaceKey`：E2 存下的模板标签页 key 照样能恢复。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/... -run 'ESTemplate|ESObject|ESTreePolic|ESTreeNoILM|ESTreePipeline|ESPolicy|ESPipeline'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch ILM policies and ingest pipelines`

### Task 8: 树上的 Snapshots、快照标签页、新建 / 删除快照和仓库

**Files:**
- Create: `internal/ui/essnapshot.go`、`internal/ui/overlay/snapshotform.go`
- Modify: `internal/ui/estree.go`、`internal/ui/loaders.go`（`KindESRepo` 展开时读快照）、`internal/ui/panes/tree/tree.go`（`KindESRepo` 可展开、`KindESSnapshot` 叶子）、`internal/ui/esindexops.go`（`esTreeWrite` 的 NewKey / DeleteNode）、`internal/ui/nodemenu.go`、`internal/ui/app.go`
- Test: `internal/ui/essnapshot_test.go`、`internal/ui/overlay/snapshotform_test.go`、`internal/ui/estree_test.go`

**Interfaces:**
- Consumes: Task 2 的全部函数；`esx.SnapshotName`（Task 1）；`writeTempEdit` 一族；`confirmES` / `confirmESCmd`；`overlay.NewPrompt`；`openESTasks`（E2）
- Produces:
  ```go
  // overlay
  type SnapshotFormMsg struct{ Name, Indices string }
  func NewSnapshotForm(keys *keymap.Keymap, repo, defaultName string) *SnapshotForm
  // 字段 Name（默认 esx.SnapshotName(now)）、Indices（逗号分隔，可以 *，空 = 全部）；Name 校验：非空、小写、不含空格 , " * \ / ? < > |

  // ui
  func loadESSnapshots(b Backend, n *tree.Node) tea.Cmd // 像 loadSubjectVersions：只读 n 这一个仓库，返回 childrenLoadedMsg
  type esSnapshotTab struct{ …; repo, name string }     // Context() 是 keymap.ESSnapshot
  func esSnapshotTabKey(conn, repo, name string) string
  ```
- 行为（spec §4、§8.1）：
  - 树加载时 `es.Repositories`；没有仓库时不显示 Snapshots 节点；`ErrUnsupported` / 403 时也不显示（不是错误）。节点 `Snapshots (N)`，下面每个仓库 `KindESRepo`（Key 是仓库名，Detail 是 type，`fs` 的 Detail 加上 location）。仓库节点展开时才 `loadESSnapshots`；读失败只在那个仓库节点上显示 `Err`。
  - 快照节点 `KindESSnapshot`（Key `<repo>/<name>`），Detail `SUCCESS · 2026-10-22 15:30 · 12s · 8 indices`；非 SUCCESS 的用警告色（`Warn`）。
  - 回车打开快照标签页：两列表格（state、start、end、duration、indices 数、shards ok / failed、size（`humanBytes`，-1 时 `unknown`）、include global state、version），下面列出索引、data stream 和 failures。`IN_PROGRESS` 时每 2 秒重读，标签页不可见时停止（照 `esServerTab` 的 `Shown` / `Hidden`），进度行 `1.2 GB / 4.0 GB (30%)`。
  - `o`（NewKey）：在仓库或快照节点上 → `NewSnapshotForm` → 确认（写），正文 `snapshot <indices | all indices> into <repo> as <name>` → `CreateSnapshot` → 刷新仓库节点、打开新快照的标签页。在 Snapshots 节点上，或者连接节点的 NodeActions 菜单项 `New snapshot repository…`（**没有仓库时只能从这里建**）→ `NewPrompt("Repository name")` → `$EDITOR` 编辑 `{"type": "fs", "settings": {"location": ""}}` → 确认（危险，输入仓库名）→ `PutRepository` → 重读树。
  - `dd`（DeleteNode）：快照节点 → 危险，输入快照名，正文 `delete snapshot <name> from <repo>`；仓库节点 → 危险，输入仓库名，正文 `unregister repository <name> (the snapshot files stay in the storage)`。快照标签页上 `dd` 等同于树上删除这个快照。成功后重读、关掉被删快照的标签页。
  - 节点菜单：仓库 → New snapshot…、Delete repository…；快照 → Open、Restore…（Task 9）、Delete…。

- [ ] **Step 1: 写失败的测试** `TestSnapshotForm`（默认名、非法名报错、Indices 解析）。`TestESTreeSnapshots`：两个仓库 → `Snapshots (2)`；树加载时假后端**没有**收到 `_cat/snapshots`；展开 `repo1` 后才收到，且只收到 `repo1` 的。`TestESTreeNoRepos`：没有仓库时没有节点。`TestESTreeRepoError`：`repo2` 返回 500 → `repo2` 节点有 `Err`，`repo1` 正常。`TestESSnapshotTab`：打开后显示 size；`IN_PROGRESS` 时 tick 重读、`SUCCESS` 后不再重读、切走后停止。`TestESCreateSnapshot`：仓库节点 `o` → 表单 → `y` → `PUT _snapshot/repo1/snap-x?wait_for_completion=false`。`TestESCreateRepository`：连接节点菜单 → 名字 → 编辑结果 → 输入名字 → `PUT _snapshot/backups`。`TestESDeleteSnapshot`：`dd` → 输入快照名 → `DELETE _snapshot/repo1/s1`。`TestESSnapshotReadOnly`：只读时 `o` / `dd` 提示 `readOnlyHint`，没有写请求。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/... -run 'SnapshotForm|ESTreeSnapshots|ESTreeNoRepos|ESTreeRepoError|ESSnapshot|ESCreateSnapshot|ESCreateRepository|ESDeleteSnapshot'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch snapshots and snapshot repositories`

### Task 9: 恢复快照

**Files:**
- Create: `internal/ui/overlay/restoreform.go`、`internal/ui/esrestore.go`
- Modify: `internal/ui/essnapshot.go`（`R`）、`internal/ui/nodemenu.go`（快照节点的 Restore…）
- Test: `internal/ui/overlay/restoreform_test.go`、`internal/ui/esrestore_test.go`

**Interfaces:**
- Consumes: `es.GetSnapshot`、`es.RestoreSnapshot`、`es.Indices`（E1）、`esx.RestoreTargets`（Task 1）、`indexNameMatch`（`estree.go`）、`confirmES`
- Produces:
  ```go
  // overlay
  type RestoreFormMsg struct{ Indices, RenamePattern, RenameReplacement string; IncludeAliases bool }
  func NewRestoreForm(keys *keymap.Keymap, snapshot string, indices []string) *RestoreForm
  // 字段 Indices（默认空 = 快照里全部；可以 * 和逗号）、Rename pattern、Rename replacement（默认 "restored-$1"，
  // pattern 默认 "(.+)"）、Include aliases（yes / no，默认 no——恢复别名会把别名从现有索引上抢走，我的决定）；
  // 提交时用 esx.RestoreTargets 校验 pattern；表单下方实时显示前 3 个 "a → restored-a"
  type esRestorePlanMsg struct{ conn, repo, name string; spec es.RestoreSpec; targets, open []string; err error }
  ```
- 行为（spec §8.1）：快照标签页 `R` 或菜单 Restore… → 读 `GetSnapshot`（拿索引列表）→ 表单 → 按 Indices 过滤快照里的索引（`indexNameMatch`，系统索引 `.` 开头的只有显式写出来才包含）→ `RestoreTargets` → `es.Indices` 找已存在的目标：有打开的 → 状态栏 Review Focus 1 的文字，不确认不发请求；过滤后一个索引都没有 → `no index of <snapshot> matches <indices>`。否则确认（危险，输入快照名），正文 `restore N indices from <repo>/<name>` 加最多 20 行 `src → dest`（已存在但 closed 的标 `(closed: restored into it)`）、`aliases: yes|no`、`global state: no`。`RestoreSnapshot` 用过滤后的具体索引名（不发通配）。成功后状态栏 `restore started: N indices · <keyHint ServerInfo> → Shards to watch`。

- [ ] **Step 1: 写失败的测试** `TestRestoreForm`：默认值；pattern `"("` 报错不提交；预览行。`TestESRestore`：快照有 `orders`、`users`，默认 rename → 确认框列出 `orders → restored-orders`，输入快照名 → 假后端收到 `POST _snapshot/r/s/_restore?wait_for_completion=false`，body 的 `indices` 是 `orders,users`、`include_global_state` 是 false。`TestESRestoreOpenTarget`：pattern 空（不改名）且 `orders` 已存在打开 → 状态栏包含 `orders exist and are open`，没有 `_restore`。`TestESRestoreClosedTarget`：目标 closed → 允许，正文带 `(closed`。`TestESRestoreNoMatch`。`TestESRestoreProd`：prod 连接上照样弹出输入快照名的确认框（危险级，不会被 `esProdRefusal` 拒绝）。`TestESRestoreReadOnly`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/... -run 'RestoreForm|ESRestore'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): restore Elasticsearch snapshots without overwriting open indices`

### Task 10: Stats 的 ILM 状态；Documents 里 `_simulate` pipeline

**Files:**
- Create: `internal/ui/essimulate.go`
- Modify: `internal/ui/esindex.go`（Stats 读 ILM、`statsResult` 加列、mark）、`internal/ui/esdocs.go`（`SimulatePipeline` 分支）
- Test: `internal/ui/esindex_test.go`、`internal/ui/essimulate_test.go`

**Interfaces:**
- Consumes: `es.ILMExplain`、`es.Pipelines`、`es.SimulatePipeline`（Task 3）、`es.Settings`（E1，取 `index.default_pipeline` / `index.final_pipeline`）、`overlay.NewPicker`、`overlay.NewDetail`
- Produces: `esIndexTab.ilm map[string]es.ILMState`；消息 `esPipelinesMsg{tabID int; conn string; names []string; defaults []string; err error}`、`esSimulatedMsg{tabID int; conn, pipeline string; out json.RawMessage; err error}`。
- 行为：
  - Stats（spec §8.2）：`loadSection(idxStats)` 在非 OpenSearch 上同时读 `ILMExplain(target)`（`ErrUnsupported` / 403 时忽略，不显示列）。列 `ilm` 插在 `shards` 后：不受管 `—`，受管 `<policy> · <phase>/<action>`，失败 `<policy> · ERROR: <reason>`（失败行用警告 mark）。
  - Simulate（spec §8.2，"用当前选中的文档"）：`<Space>p`：没有选中的文档提示 `select a document first`；读 pipeline 列表和当前文档所在索引的 default / final pipeline → `NewPicker("Simulate pipeline on <_id>", names)`，default / final 排在最前面并标 `(default)` / `(final)`；没有 pipeline → `no ingest pipelines`。选中后 `SimulatePipeline(name, []esx.Hit{当前 hit})`（读，不确认），结果用 `overlay.NewDetail("<pipeline> on <_id>", 缩进 2 的 JSON)`；verbose 结果里有 `error` 的处理器在标题后加 ` · failed at <processor type>`。被打码的文档（Task 12）用它打码后的 `_source` 作为输入，标题加 ` · masked fields sent as ******`。

- [ ] **Step 1: 写失败的测试** `TestESStatsILM`：两个索引一个受管一个不受管，`ilm` 列的值；ERROR 行是警告 mark；OpenSearch 上没有 `ilm` 列、没有 `_ilm` 请求；`_ilm/explain` 返回 403 时 Stats 照常显示、没有 `ilm` 列。`TestESSimulate`：第二行上 `<Space>p` → picker 第一项是 `ingest-a (default)` → 选中 → 假后端收到 `POST _ingest/pipeline/ingest-a/_simulate?verbose=true`，body 的 `_source` 等于那一行的 Source → 弹窗。`TestESSimulateNoPipelines`、`TestESSimulateNoSelection`。`TestESSimulateReadOnly`：只读连接上照样能运行（读请求）。
- [ ] **Step 2: 运行** `go test -race ./internal/ui -run 'ESStatsILM|ESSimulate'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): ILM state in Elasticsearch index stats, simulate an ingest pipeline on a document`

### Task 11: 状态页的 Users 和 Roles

**Files:**
- Create: `internal/ui/essecurity.go`、`internal/ui/overlay/userform.go`
- Modify: `internal/ui/esserver.go`（两个新部分、跳过逻辑、`Handle` 分派）
- Test: `internal/ui/essecurity_test.go`、`internal/ui/overlay/userform_test.go`

**Interfaces:**
- Consumes: Task 4 的全部函数；`esObjectTab` / `esOpenObjectMsg` / `askDeleteObject`（Task 7，`objRole`）；`overlay.NewSecret`、`overlay.NewPrompt`；`confirmESCmd`
- Produces:
  ```go
  // esserver.go：secESUsers、secESRoles 加在 secESSettings 后面；eserverSectionNames 跟着加 "Users", "Roles"
  // esServerTab 加 security es.SecurityAPI、securityRead bool（只在第一次 load 时探测）、users []es.User、roles []es.Role
  func (t *esServerTab) sectionShown(s eserverSection) bool // Users / Roles 只在 security != SecurityNone 时显示
  // overlay
  type UserFormMsg struct{ Name, Password, Roles, BackendRoles, FullName, Email string }
  func NewUserForm(keys *keymap.Keymap, api es.SecurityAPI) *UserForm
  // 字段：Username、Password、Confirm（两个都是掩码输入）、Roles（逗号）、ES: Full name、Email；OpenSearch: Backend roles；
  // 校验：用户名非空不含空格、密码 ≥ 6 位（ES 的下限）且两次一致
  ```
- 行为（spec §8.3）：
  - 第一次 load（任何部分）时调 `es.Security` 并缓存；`SecurityNone` 时 `[` / `]` 跳过两个部分，部分标题行不显示它们。读 Users / Roles 返回 403 时部分里显示 `permission denied`。
  - Users 列：`USER`、`ROLES`、ES: `FULL NAME`、`EMAIL`、`ENABLED`（`yes` / `no`）；OpenSearch: `BACKEND ROLES`；`RESERVED`（`★`）。禁用的用户整行用 muted mark。
  - `o`：`NewUserForm` → 确认（写），正文 `create user <name> with roles <a, b>`，**`esPlan.Body` 为空** → `CreateUser`。`i`：`NewPrompt("Roles of <name>", 逗号分隔的现有角色)` → 确认（写），正文 `roles of <name>: a, b → a, c` → `UpdateUser`。`x`：OpenSearch 提示 `OpenSearch users cannot be disabled`；ES：确认（写），正文 `disable user <name>` / `enable user <name>` → `SetUserEnabled`。`P`：`NewSecret("New password for <name>")` 两次（第二次标题 `Again`，不一致提示 `passwords differ` 并结束）→ 确认（写），正文 `change the password of <name>`，Body 为空 → `ChangePassword`。`dd`：危险，输入用户名 → `DeleteUser`。Reserved 用户上 `i` / `x` / `dd` 提示 `<name> is a reserved user`（`P` 允许）。当前连接所用的账号（`cfg.User`）上 `x` / `dd` 额外在正文写 `this is the user datavim is connected as`。
  - Roles 列：`ROLE`、`CLUSTER`（逗号，截断）、`INDEX PRIVILEGES`（条数）、`RESERVED`。回车 → `esOpenObjectMsg{key: "role/<name>", api}` 打开 `esObjectTab`（看、`<Space>v` 改）；`dd` → `askDeleteObject`。Reserved 角色上 `dd` 和对象标签页的 `<Space>v` 提示 `<name> is a reserved role`。
  - 所有写成功后重读当前部分，状态栏 `created user bob` / `updated roles of bob` / `disabled bob` / `changed the password of bob` / `deleted user bob`。**密码只存在于 `UserFormMsg` / 闭包里，不写进 tab 的任何字段**。

- [ ] **Step 1: 写失败的测试** `TestESSecurityHidden`：`SecurityNone` 的假集群上 `]` 从 Settings 回到 Overview，`_xpack` 只在第一次 load 请求过一次（再来两次 tick 不再请求）。`TestESUsers`：ES 和 OpenSearch 两种假集群的列。`TestESUsersForbidden`：403 → `permission denied`。`TestESCreateUser`：表单 → 确认 → 假后端收到 `PUT _security/user/bob`，body 有 password；prod 连接上确认框的文字**不含**密码和 `"password"`。`TestESChangePassword`：两次输入一致 → `POST _security/user/bob/_password`；不一致 → `passwords differ`、没有请求；之后 `Model` 的状态栏、`esServerTab` 的所有字符串字段都不含密码（用 `fmt.Sprintf("%+v", tab)` 断言）。`TestESToggleUser`（ES）/ `TestESToggleUserOpenSearch`。`TestESReservedUser`。`TestESRoles`：回车打开 `role/admin` 的对象标签页；`<Space>v` 编辑后 `PUT _security/role/admin`；OpenSearch 上是 `PUT _plugins/_security/api/roles/admin`。`TestESSecurityReadOnly`：`o` / `i` / `x` / `P` / `dd` 都提示 `readOnlyHint`。
- [ ] **Step 2: 运行** `go test -race ./internal/ui/... -run 'ESSecurity|ESUsers|ESCreateUser|ESChangePassword|ESToggleUser|ESReserved|ESRoles|UserForm'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/ui/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): Elasticsearch users and roles`

### Task 12: 打码接进 Documents、按 ID 打开和控制台

**Files:**
- Create: `internal/ui/esmask.go`
- Modify: `internal/ui/esdocs.go`（`paged`）、`internal/ui/esopenid.go`（`idFound`）、`internal/ui/esedit.go`（`editRefusal`、`editCell`、`send`）、`internal/ui/esconsole.go`（保存结果前）、`internal/ui/esindex.go`（建 tab 时取 masker）、`internal/es/write.go`（`UpdateDoc`）、`internal/es/write_test.go`、`internal/session/es_test.go`
- Test: `internal/ui/esmask_test.go`、`internal/es/write_test.go`

**Interfaces:**
- Consumes: `esx.NewMasker` / `MaskHit` / `Covers` / `MaskResponse` / `PartialDoc`（Task 5）、`Model.masks`（`kafkamask.go` 的 `SetMasks`）
- Produces:
  ```go
  func (m *Model) esMaskerFor(conn string) *esx.Masker // esx.NewMasker(m.masks, conn, string(cfg.Env))
  // es：
  func UpdateDoc(ctx context.Context, d Doer, index, id, routing string, partial json.RawMessage, v Version) (Version, error)
  // POST <index>/_update/<id>?if_seq_no=…&if_primary_term=…&refresh=wait_for&routing=… {"doc": partial}；冲突 → ErrConflict
  ```
  `esIndexTab` 和 `esConsoleTab` 打开时各取一次 masker；`esDocs` 记 `masked map[string]bool`（docKey → 这篇有字段被打码）。`WriteFuncs` 加 `UpdateDoc`（共 38）。
- 行为（spec §8.4，"只影响显示"）：
  - Documents 每一页、`<Space>k` 打开的文档在进入 `d.hits` **之前**经过 `MaskHit`，表格、详情、`y` 系列、导出都从 `d.hits` 来，所以都是 `******`。
  - 编辑：`i` 在 `Covers(index, col)` 的格子上 → `masked fields cannot be edited`；`<Space>v` 在 masked 文档上 → `this document has masked fields: edit single fields instead`。masked 文档上改了没打码的字段，提交时用 `UpdateDoc(PartialDoc(新 _source, 改过的字段))` 代替 `PutDoc`（同样带 `if_seq_no`，冲突提示照旧）；没打码的文档仍用 `PutDoc`。`dd` 删除不受影响。
  - 控制台：每个响应在存进结果前 `MaskResponse`，JSON 视图和表格视图都是打码后的；状态行加 ` · masked`。控制台**不**禁止发请求（只影响显示）。

- [ ] **Step 1: 写失败的测试** `TestUpdateDoc`（es）：path 和 body；409 → `ErrConflict`；`TestWriteFuncsListed` 改成 38，session `calls` 加 1。`TestESMaskDocs`：规则 `indices=["users*"]`、`fields=["_source.password"]` → 表格里 password 列是 `******`；`ys` 复制的 JSON、`yu` 的 curl、ndjson 导出、`K` 详情都不含原值。`TestESMaskOtherIndex`：`orders` 不受影响。`TestESMaskEdit`：password 格子上 `i` 提示上面的文字；`<Space>v` 被拒绝；改 `name` 后提交 → 假后端收到 `POST users-1/_update/1?if_seq_no=…`，body 是 `{"doc":{"name":"x"}}`，**没有** `PUT users-1/_doc/1`，body 里没有 `******`。`TestESMaskOpenID`：`<Space>k` 打开的文档也被打码。`TestESMaskConsole`：控制台 `GET users-1/_search` 的 JSON 和表格都是 `******`；`GET _cat/indices` 原样。`TestESMaskKafkaUnaffected`：同一份配置里的 ES 规则不影响 Kafka topic 标签页。
- [ ] **Step 2: 运行** `go test -race ./internal/es ./internal/session ./internal/ui -run 'UpdateDoc|WriteFuncs|ReadOnly|ESMask'`，预期 FAIL。
- [ ] **Step 3: 实现**。
- [ ] **Step 4: 运行** `go test -race ./internal/...`，预期 PASS。
- [ ] **Step 5: 提交** `feat(ui): mask Elasticsearch document fields`

### Task 13: 集成测试、文档、CHANGELOG 和真机验证

**Files:**
- Modify: `internal/es/integration_test.go`（ES 容器加 `path.repo=/tmp/snapshots` 环境变量）、`CHANGELOG.md`、`README.md`、`README.zh-CN.md`、`docs/superpowers/specs/2026-10-21-elasticsearch-design.md`（状态行：E1、E2、E3 已实现）

- [ ] **Step 1: 写集成测试**（`integration` tag，在 `TestIntegration` 的镜像矩阵里；7.10.2、8.x、9.x 必跑；OpenSearch 只跑 Repositories / Snapshots 读和 Pipelines，安全插件在 testcontainers 的 OpenSearch 模块里是关的，OpenSearch 的安全接口只有单元测试）：
  - 快照：`PutRepository` 建 `fs` 仓库 → `CreateSnapshot`（两个索引）→ 轮询 `GetSnapshot` 到 `SUCCESS`，`SizeBytes > 0` → `RestoreSnapshot` 用 rename `(.+)` → `restored-$1` → 等 `restored-*` 的 `_count` 和源相同 → `DeleteSnapshot` → `Snapshots` 为空 → `DeleteRepository`。
  - ILM（只 ES）：`PutPolicy` → `Policies` 里有 → 建一个用它的索引 → `ILMExplain` 的 `Policy` 正确 → `DeletePolicy` 之前先删索引。
  - Pipeline：`PutPipeline`（`set` 处理器）→ `SimulatePipeline` 的结果里有设上的字段 → `DeletePipeline`。
  - 安全：7.10.2（没开安全）`Security` 是 `SecurityNone`；8.x / 9.x 是 `SecurityES`，`CreateUser` → 用新用户的密码连接成功 → `ChangePassword` → 旧密码 401、新密码成功 → `SetUserEnabled(false)` → 401 → `PutRole` / `Roles` / `DeleteRole` → `DeleteUser`。
  - 打码不需要集成测试（纯显示，UI 测试已覆盖）。
- [ ] **Step 2: 运行** `make lint`、`make test`、`make test-integration`，全部 PASS。
- [ ] **Step 3: 写文档**：`CHANGELOG.md` `[Unreleased]` 的 Added 加：Snapshots（仓库、快照、新建、删除、恢复和 rename）；ILM Policies 和 Stats 里的 ILM 状态；Ingest Pipelines 和 `<Space>p` 的 simulate；Users / Roles（ES 和 OpenSearch）；`[[mask]]` 的 `indices` 和 `_source.<path>`。README 中英文：ES 功能列表、按键表（按键名照 `defaults.go`）、`[[mask]]` 的 ES 例子（`connections`、`indices`、`fields = ["_source.password"]`，说明控制台里的 `_search` / `_doc` 响应也打码、被打码的字段不能编辑）、快照列表不显示大小的说明。
- [ ] **Step 4: 真机验证**：`make build`，`./bin/datavim --config <dir> --debug`：(a) 本地 Docker ES 8.x（`path.repo` 已配）：连接节点菜单建 `fs` 仓库 → 仓库节点 `o` 建快照 → 快照标签页看到进度到 SUCCESS → Restore 用默认 rename → 状态页 Shards 里看到 `restored-*` 恢复 → 不改名再恢复一次被拒绝；(b) 改一个 ILM 策略的 delete 天数，控制台 `GET _ilm/policy/<name>` 看到新值；Stats 里看到 ILM 列；(c) 建一个 `set` pipeline，Documents 里 `<Space>p` 看到结果；(d) 状态页 Users：新建用户、改角色、禁用、改密码（用新密码另开一个连接能连上），`~/.local/state/datavim/datavim.log` 和 `<Space>` 审计标签页里都没有密码；(e) `config.toml` 加 ES 打码规则后打开索引、详情、复制、导出、控制台 `_search` 都是 `******`，改别的字段提交成功、被打码的字段原值没变；(f) 作者的内网 7.10.2 集群**设成只读**：树上没有 Snapshots（没有仓库时）、状态页没有 Users / Roles、ILM Policies 能看，所有写操作被拒绝。
- [ ] **Step 5: 提交** 集成测试 `test(es): integration tests for snapshots, ILM, pipelines and security`；文档 `docs: Elasticsearch snapshots, ILM, pipelines, users and masking in the readmes and the changelog`
