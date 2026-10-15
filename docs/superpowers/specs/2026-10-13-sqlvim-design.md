# sqlvim 设计文档

- 日期：2026-10-13
- 状态：待审阅
- 范围：sqlvim v0.1 → v1.0 的整体设计；每个版本另行编写实现计划

## 1. 背景与目标

sqlvim 是一个终端里的 SQL 客户端（TUI），用于对 MySQL 和 PostgreSQL 做增删改查。

- **用户**：作者本人，在 macOS + Ghostty 下日常使用；代码以 MIT 协议在 GitHub 开源。
- **定位**：日常主力工具，目标是替代 DataGrip 的日常使用场景。
- **交互**：vim 键位，lazygit 风格的多面板布局；DataGrip 的快捷键能力用 leader 键组合实现。
- **成功标准**：日常查库、写 SQL、改数据时，作者愿意打开 sqlvim 而不是 DataGrip。

### 1.1 使用环境

- 本地或 Docker 中的数据库（直连）
- 需经 SSH 隧道或跳板机访问的远程数据库（含 ProxyJump）
- 生产库：必须有防误操作的保护机制

### 1.2 非目标（v1.0 之前不做）

SQLite、ClickHouse 等其他数据库；ER 图；EXPLAIN 可视化；多结果集对比；行级 diff；AI 生成 SQL；SSL/TLS 客户端证书的专门配置界面（驱动 DSN 参数可透传，见 §5.1）；Windows 支持（不刻意破坏，但不测试）。

### 1.3 与已有项目的差异

jorgerojas26/lazysql（Go + tview）定位相近，作者用过后体验不佳。sqlvim 的差异点：内置 vim 编辑器并可与 nvim 联动；DataGrip 式全局搜索和 leader 键体系；数据修改先暂存，预览 SQL 后再提交；SSH 隧道；生产库保护。

## 2. 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 语言 | Go | 单二进制分发、编译快、TUI 和数据库生态成熟 |
| TUI 框架 | Bubble Tea + Bubbles + Lip Gloss | Elm 架构，状态清晰、易测；生态最活跃 |
| MySQL 驱动 | go-sql-driver/mysql | 事实标准 |
| PG 驱动 | jackc/pgx（v5） | 性能与类型支持最好 |
| SSH | golang.org/x/crypto/ssh，配合 ssh_config 解析库 | 原生实现隧道，不依赖外部 ssh 进程 |
| 密码存储 | macOS Keychain（经 go-keyring 一类的库访问） | 配置文件里不出现明文密码 |
| 语法高亮 | alecthomas/chroma | 支持 MySQL/PG 方言 |
| xlsx 导出 | xuri/excelize | 事实标准 |
| 本地存储 | 嵌入式 SQLite（纯 Go 实现，如 modernc.org/sqlite） | 存 SQL 历史和最近打开记录，无 CGO |
| 配置格式 | TOML | 人类可读，Go 生态支持好 |
| 测试 | 标准库 testing + testcontainers-go | 用真实数据库做集成测试 |
| 发布 | GoReleaser + Homebrew tap | 社区惯例 |

依赖的具体版本在各版本的实现计划阶段，通过 Context7 查询最新文档后确定（例如 Bubble Tea v1 与 v2 的选择）。

## 3. 架构

### 3.1 目录结构

```
sqlvim/
├── cmd/sqlvim/main.go          # 入口：解析参数，启动 TUI
├── internal/
│   ├── config/                 # ~/.config/sqlvim/ 下的配置：连接、按键、主题
│   ├── secret/                 # 密码存取：Keychain；其他平台降级为环境变量
│   ├── db/                     # 数据库抽象层
│   │   ├── driver.go           #   Driver / Conn 接口及公共类型
│   │   ├── mysql/              #   MySQL 实现
│   │   ├── postgres/           #   PostgreSQL 实现
│   │   └── tunnel/             #   SSH 隧道
│   ├── meta/                   # 元数据缓存（库/表/列/索引），供补全和搜索使用
│   ├── sqlx/                   # SQL 工具：分句、语句分类、缺失 WHERE 检测、补全上下文
│   ├── edit/                   # 数据编辑：暂存修改 → 生成 DML
│   ├── export/                 # 复制与导出：tsv/csv/json/md/sql/xlsx
│   ├── history/                # SQL 历史、最近打开
│   └── ui/
│       ├── app.go              #   根 Model：布局、焦点、全局按键分发
│       ├── keymap/             #   模式、leader 键序列、帮助文本生成
│       ├── panes/              #   tree / console / grid / structure
│       ├── vimedit/            #   内置 vim 编辑器组件
│       └── overlay/            #   浮层：搜索 / 最近 / 帮助 / 确认 / which-key
└── docs/
```

### 3.2 分层原则

1. **`db.Driver` 是唯一的数据库接缝。** UI 不感知方言。标识符引号、分页写法、元数据查询都由 driver 实现。
2. **UI 层不直接做 IO。** 查询都封装为 `tea.Cmd` 异步执行，结果作为消息回到 Update；每个查询带一个 `context.Context`，用于取消。
3. **纯逻辑包不依赖终端。** `vimedit`（编辑器状态机部分）、`sqlx`、`edit`、`export`、`keymap` 都可以独立做单元测试。
4. **依赖方向只能向下**：`ui` → `meta`/`edit`/`export`/`history` → `db` → 驱动。下层不引用上层。

### 3.3 核心接口（草案）

```go
package db

type Driver interface {
    Name() string                                   // "mysql" | "postgres"
    Open(ctx context.Context, cfg ConnConfig, dial DialFunc) (Conn, error)
    Dialect() Dialect
}

type Conn interface {
    Query(ctx context.Context, sql string, args ...any) (*ResultSet, error)
    Exec(ctx context.Context, sql string, args ...any) (ExecResult, error)
    Begin(ctx context.Context) (Tx, error)
    Meta() MetaReader
    Close() error
}

type MetaReader interface {
    Databases(ctx context.Context) ([]string, error)
    Schemas(ctx context.Context, database string) ([]string, error) // MySQL 返回空
    Tables(ctx context.Context, ns Namespace) ([]Table, error)
    Columns(ctx context.Context, t TableRef) ([]Column, error)
    Indexes(ctx context.Context, t TableRef) ([]Index, error)
    ForeignKeys(ctx context.Context, t TableRef) ([]ForeignKey, error)
    DDL(ctx context.Context, t TableRef) (string, error)
}

type Dialect interface {
    QuoteIdent(s string) string
    Placeholder(n int) string                       // "?" 或 "$n"
    Paginate(sql string, limit, offset int) string
}

// DialFunc 由 tunnel 包提供；直连时为 nil，使用默认拨号。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)
```

`ResultSet` 保存列信息（名称、数据库类型、是否可空）和行数据。数据统一转成 `Value`：保留原始类型，并提供显示用字符串，NULL 与空字符串要能区分。

### 3.4 命名空间模型

- MySQL：连接 → database → table
- PostgreSQL：连接 → database → schema → table

`Namespace{Database, Schema}` 统一表示两者，MySQL 的 Schema 为空。树面板根据驱动决定显示几层。

## 4. 界面

### 4.1 布局

lazygit 风格的多面板布局：

```
┌ Connections ──┐┌ [1]console [2]users [3]orders ─────┐
│ ● local-mysql ││ SELECT * FROM users WHERE id > 10; │
│ ▼ prod-pg [P] ││                                    │
│   ▼ app       │├ Results 120 rows 34ms ─────────────┤
│     ▸ users   ││ id │ name  │ email         │ ctime │
│     ▸ orders  ││ 11 │ alice │ a@x.com       │ ...   │
└───────────────┘└────────────────────────────────────┘
 NORMAL  prod-pg/app  <space> leader  ? help
```

- 左侧：树面板（连接 → 库/schema → 表/视图），懒加载；`<Space>b` 显示或隐藏。
- 右侧：tab 区。每个 tab 可以是：
  - 控制台：上方编辑器，下方结果
  - 表数据：表格
  - 结构：列、索引、外键、DDL
- 底部：状态栏，显示模式、当前连接/库、事务状态、按键提示。
- 浮层：全局搜索、最近打开、帮助、确认框、which-key 提示、单元格详情。

### 4.2 生产库视觉提示

连接标记 `env = "prod"` 后，tab 区边框和状态栏变红，状态栏显示 `[PROD]`。

## 5. 功能设计

### 5.1 连接管理

配置文件 `~/.config/sqlvim/connections.toml`：

```toml
[[connection]]
name     = "prod-pg"
driver   = "postgres"
host     = "10.0.1.5"
port     = 5432
user     = "app"
database = "app"
env      = "prod"            # dev | staging | prod，默认 dev
readonly = true              # env=prod 时默认为 true
ssh      = "bastion"         # ~/.ssh/config 中的 Host 别名，可选
params   = { sslmode = "require" }   # 透传给驱动 DSN
# 密码不写在这里，存在 Keychain：service="sqlvim", account=<name>
```

- `<Space>n` 打开新建连接表单，保存时把密码写入 Keychain。
- 首次连接缺少密码时弹框输入，并询问是否保存。

### 5.2 SSH 隧道

- 解析 `~/.ssh/config`，支持 `HostName`、`User`、`Port`、`IdentityFile`、`ProxyJump`（多跳）。
- 认证顺序：ssh-agent（`SSH_AUTH_SOCK`）→ IdentityFile → 默认密钥。带口令的密钥会弹框询问口令。
- 校验 `known_hosts`；遇到未知主机时弹框确认指纹（与 OpenSSH 行为一致），确认后写入。
- 隧道以 `DialFunc` 的形式注入驱动，不监听本地端口。

### 5.3 生产库保护

由 `sqlx` 对语句分类：读 / 写（DML）/ DDL / 事务控制 / 其他。

| 场景 | 行为 |
|---|---|
| `readonly = true`，执行写语句或 DDL | 拒绝执行，并提示用 `:set noreadonly` 临时解锁（仅对当前会话有效） |
| `env = prod`，执行写语句或 DDL | 弹确认框，显示语句和影响对象；需输入 `y` 确认 |
| 任何环境下，UPDATE/DELETE 不带 WHERE | 拦截；在确认框里输入表名才能继续 |
| 数据编辑提交（§5.8） | 走同样的检查 |

只读模式下，PG 连接额外设置 `default_transaction_read_only = on`，MySQL 会话设置 `SET SESSION TRANSACTION READ ONLY`，由数据库端兜底。

### 5.4 控制台与执行

- 语句分割：`sqlx` 实现一个轻量词法器，能识别字符串、标识符引号、注释、PG 的 dollar-quote，以及 MySQL 的 `DELIMITER`。
- NORMAL 模式下 `Enter` 执行光标所在语句；VISUAL 模式下 `Enter` 执行选中内容；INSERT 模式下 `Ctrl-Enter` 执行（终端不支持 kitty 键盘协议时降级为 `Ctrl-j`）。
- 多条语句依次执行；只显示最后一个结果集，其余语句的结果写进消息日志。
- 查询异步执行，状态栏显示进度动画和已用时间；`Ctrl-c` 通过 context 取消查询（PG 发送 cancel 请求，MySQL 执行 `KILL QUERY`）。
- 结果默认最多拉取 `max_rows = 1000` 行（可配置），超出时提示"已截断"，可以继续加载。
- 事务模式：默认自动提交；`<Space>t` 切换到手动提交，状态栏显示 `TX` 和未提交语句数，`:commit` / `:rollback` 结束事务。在事务未结束时退出或切换连接，会弹框询问。
- 每次执行写入 SQL 历史，记录连接名、库、SQL、耗时、成功/失败。

### 5.5 编辑器

- v0.2–v0.4：使用 Bubbles 的 textarea。
- v0.5：替换为 `vimedit`：
  - 模式：normal / insert / visual / visual-line
  - motion：`h j k l w b e W B E 0 ^ $ gg G f F t T % { }`
  - operator：`d c y > <`，可与 motion 和 text object（`iw aw i" a" i( a(`）组合
  - 其他：`x p P o O u Ctrl-r . dd yy cc`、计数前缀、寄存器 `"a`–`"z`、`"+` 映射系统剪贴板
  - 不支持：宏、ex 区间命令、多窗口，这些交给 nvim
- `<Space>v`：把当前内容写入临时文件 `*.sql`，挂起 TUI，用 `$EDITOR`（默认 nvim）打开；编辑器退出后读回内容并恢复 TUI。

### 5.6 补全与高亮

- `meta` 缓存每个连接的库/表/列，连接时后台加载，`<Space>r` 刷新；执行 DDL 后自动让相关缓存失效。
- `sqlx` 根据光标处的上下文给出候选：
  - 在 `FROM` / `JOIN` / `UPDATE` / `INTO` 之后：表名
  - 在 `SELECT` / `WHERE` / `ON` / `SET` / `ORDER BY` 之后：当前语句引用表的列；`alias.` 之后只给该别名对应表的列
  - 其他位置：关键字
- 候选用前缀加模糊匹配排序。INSERT 模式下自动弹出；`Ctrl-n/p`、`Tab` 选择，`Ctrl-y` 确认，`Esc` 关闭。
- 语法高亮用 Chroma，按驱动选择 MySQL 或 PG 词法。

### 5.7 结果表格

- 列宽自适应，最大宽度可配置；`h l` 逐列移动，`zh zl` 横向滚动，`gg G` 跳到首尾行。
- NULL 显示为灰色的 `NULL`，与空字符串区分。
- `K`：浮层显示单元格完整内容；JSON 自动格式化。
- 表数据 tab：`[` `]` 翻页，`f` 按当前单元格的值过滤，`F` 输入 WHERE 条件，`s` 按当前列切换排序（升序 → 降序 → 无）。控制台的 SELECT 结果同样可以 `f` / `F` 过滤，见 `2026-10-16-console-result-filter-edit-design.md`。

### 5.8 数据编辑

- 表数据 tab 中可用；控制台里能对应回单张表的 SELECT 结果也可以编辑，条件和事务规则见 `2026-10-16-console-result-filter-edit-design.md` 第 3、6 节。
- 操作：`i` 或 `Enter` 编辑单元格（`Ctrl-n` 设为 NULL），`o` 新增行，`dd` 标记删除，`u` 撤销上一项修改，`U` 撤销全部修改。
- 修改先暂存：修改过的单元格标黄，新增行标绿，待删除行标红并加删除线。
- 行定位：优先用主键；没有主键时用唯一索引；都没有时用所有列匹配并加 `LIMIT 1`（PG 改用 `ctid`），同时在预览中给出警告。
- 提交：`:w` 或 `<Space>w` → 预览生成的 SQL → 确认 → 在一个事务里执行；任一语句影响行数不为 1 时整体回滚并报告；成功后刷新当前页。
- 提交前同样经过 §5.3 的保护检查。

### 5.9 复制与导出

| 格式 | 复制（`<Space>y?`） | 导出（`<Space>x?`） |
|---|---|---|
| TSV | `yt`（粘贴到 Excel/Numbers 即为表格） | — |
| CSV | `yc` | `xc` |
| JSON（对象数组） | `yj` | `xj` |
| Markdown 表格 | `ym` | — |
| INSERT 语句 | `yi` | `xs` |
| xlsx | — | `xx` |

- 复制范围：表格中有选区时复制选区，否则复制整个结果集。表格内 `y` 复制单元格，`yy` 复制整行。
- 导出范围：当前结果集。表数据 tab 中会询问"当前页 / 全表"；全表导出以流式方式处理，不全部载入内存。
- 默认导出目录为 `~/Downloads`，文件名为 `<表名或 query>-<时间戳>.<扩展名>`，导出前可修改路径。
- 剪贴板：macOS 使用 `pbcopy`；终端支持时使用 OSC 52（便于在 SSH 会话中使用）。

### 5.10 全局搜索与最近打开

- `<Space><Space>`：模糊搜索当前连接（加 `@` 前缀可搜所有已连接的连接）的库、schema、表、视图、列、函数。结果按类型着色，回车跳转（打开表数据或结构），列结果跳转到所属表并定位到该列。
- `<Space>e`：最近打开的表、结构和控制台，按时间排序，跨会话保存在 `history` 中。
- 搜索使用 sahilm/fuzzy 一类的库在 `meta` 缓存上做匹配，不查询数据库。

### 5.11 结构视图

`<Space>s` 打开树中光标所在对象或控制台光标下的表名的结构 tab，分为几个子页：列（名称、类型、可空、默认值、注释）、索引、外键、DDL（带语法高亮，可复制）。

## 6. 按键体系

### 6.1 模式

| 模式 | 适用位置 |
|---|---|
| NORMAL | 所有面板默认；leader 键只在此模式生效 |
| INSERT | 编辑器、单元格编辑 |
| VISUAL | 编辑器选文本，表格框选单元格 |
| COMMAND | `:` 命令行 |

### 6.2 全局与导航

- `Tab` / `Shift-Tab` 切换面板，`1`–`9` 跳到对应 tab，`Ctrl-h/j/k/l` 在面板间移动
- 列表和表格通用：`h j k l`、`gg`、`G`、`Ctrl-d/u`、`/` 搜索、`n/N`
- `?` 显示当前上下文的按键帮助
- `:q` 关闭当前 tab；`:qa` 退出（有未提交修改或事务时询问）

### 6.3 Leader 键（默认 `<Space>`）

按下 leader 后停顿 300ms，弹出 which-key 提示面板。

| 按键 | 功能 | 对应 DataGrip |
|---|---|---|
| `<Space><Space>` | 全局搜索 | 双 Shift |
| `<Space>e` | 最近打开 | Cmd+E |
| `<Space>s` | 查看结构 | Cmd+F12 |
| `<Space>c` | 打开控制台 | Cmd+Shift+F10 |
| `<Space>n` | 新建连接 | — |
| `<Space>o` | 切换连接或数据库 | — |
| `<Space>b` | 显示或隐藏树 | — |
| `<Space>h` | SQL 历史 | — |
| `<Space>v` | 在 nvim 中编辑 | — |
| `<Space>w` | 提交数据修改 | Submit |
| `<Space>t` | 切换事务模式 | Tx mode |
| `<Space>r` | 刷新元数据 | Synchronize |
| `<Space>y?` | 复制为…（见 §5.9） | Copy as |
| `<Space>x?` | 导出为…（见 §5.9） | Export Data |

### 6.4 实现

- `keymap` 包维护一棵按键树：模式 × 上下文 → 按键序列 → 动作 ID。动作带描述文本，帮助浮层和 which-key 都由这棵树生成，保证提示和实际绑定一致。
- 用户可在 `~/.config/sqlvim/keymap.toml` 中覆盖绑定和 leader 键。启动时校验冲突，有冲突则报错并指出位置。
- 文档提供 Ghostty 配置片段，把 Cmd 快捷键映射为 leader 序列，例如 `keybind = cmd+e=text:\x20e`。

## 7. 本地数据

| 路径 | 内容 |
|---|---|
| `~/.config/sqlvim/connections.toml` | 连接配置 |
| `~/.config/sqlvim/config.toml` | 通用设置（max_rows、主题、编辑器命令、导出目录） |
| `~/.config/sqlvim/keymap.toml` | 自定义按键 |
| `~/.local/state/sqlvim/state.db` | SQL 历史、最近打开、各连接的控制台草稿 |
| `~/.local/state/sqlvim/sqlvim.log` | 日志（slog，默认 info 级别，`--debug` 时为 debug 级别） |

遵循 XDG 规范，相应环境变量存在时以环境变量为准。控制台草稿会自动保存，重启后恢复。

## 8. 错误处理

- 数据库和网络错误不会导致程序退出：在结果区显示错误信息（包含数据库返回的错误码和位置）；如能定位到出错位置，就在编辑器中跳到对应行列。
- 连接断开：执行时检测到连接已断开，自动重连一次；重连失败时，树中该连接标红，提示按 `Enter` 重试。处在手动事务中时不自动重连，而是提示事务已丢失。
- 配置文件解析失败：启动时打印出错的文件、行号和原因，然后退出（这是唯一允许退出的错误）。
- panic：在 Bubble Tea 程序外层 recover，恢复终端状态，把堆栈写入日志，并提示日志路径。
- 日志和错误信息中不出现密码。

## 9. 测试策略

| 层 | 方式 |
|---|---|
| `sqlx`、`edit`、`export`、`keymap`、`config` | 表格驱动的单元测试 |
| `vimedit` | 表格驱动：初始文本和光标 + 按键序列 → 期望文本、光标和模式 |
| `db/mysql`、`db/postgres` | testcontainers-go 启动 MySQL 8 和 PG 16，跑同一套 driver 合约测试 |
| `db/tunnel` | 在测试中启动内存 SSH server（x/crypto/ssh）做转发测试 |
| `ui` | 对 Model 发送消息后断言状态；关键视图用 teatest 做黄金文件快照 |

- CI（GitHub Actions）：`go vet`、golangci-lint、单元测试、集成测试（需 Docker）、macOS 和 Linux 构建。
- 开发采用 TDD；每个版本的实现计划中列出具体测试。

## 10. 迭代路线

| 版本 | 主题 | 内容 |
|---|---|---|
| v0.1 | 能连上、能看 | 项目骨架与 CI；config + Keychain；MySQL/PG driver + 合约测试；多面板布局、焦点、`?` 帮助、which-key；树（懒加载）；表数据浏览（分页、横向滚动、`K`）；结构视图 |
| v0.2 | 写 SQL、拿结果 | textarea 控制台 + `<Space>v` nvim；分句、执行光标处语句、异步执行与取消；复制（全部格式）；导出（全部格式）；SQL 历史；草稿自动保存；**基础保护**：`readonly` 生效（含数据库会话级只读兜底）、缺失 WHERE 拦截（控制台一旦能执行任意 SQL，保护就必须同时到位） |
| v0.3 | 远程与安全 | SSH 隧道（agent/密钥/ProxyJump/known_hosts）；env=prod 红色提示、写操作确认弹框、`:set noreadonly`；事务模式 |
| v0.4 | DataGrip 核心体验 | meta 缓存；全局搜索；最近打开；补全；语法高亮 |
| v0.5 | 内置 vim 编辑器 | `vimedit` 替换 textarea（§5.5） |
| v0.6 | 数据编辑 | §5.8 全部；过滤与排序 |
| v1.0 | 打磨与发布 | 自定义 keymap 和主题；README 与演示动图；Ghostty 配置片段；GoReleaser + Homebrew tap |

每个版本开始前单独编写实现计划；当前先为 v0.1 编写计划。

## 11. 开源规范

- 仓库：`github.com/<user>/sqlvim`，MIT 许可证
- 包含 README（中英文）、CONTRIBUTING、CHANGELOG（Keep a Changelog 格式）、Issue/PR 模板
- 提交信息遵循 Conventional Commits；版本号遵循 SemVer
- 代码遵循 Effective Go；`internal/` 下的包不对外暴露 API
