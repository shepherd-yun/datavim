# 工作空间 设计文档

- 日期：2026-10-16
- 状态：已实现
- 范围：启动时选择工作空间，每个空间有独立的连接、密码和本地状态；修订主设计 §5.1、§7

## 1. 背景

DataGrip 可以切换工作空间，例如工作相关的数据库放在一个空间，个人的放在另一个空间，彼此看不到。

datavim 现在只有一个 `connections.toml`。虽然可以用 `--config <dir>` 换一个配置目录，但这样有两个问题：

- `config.toml` 和 `keymap.toml` 跟着一起被换掉，个人偏好要维护好几份。
- keychain（`service="datavim", account=<conn>`）和 `state.db`（`history.conn`、`recent.conn`、`drafts.key`）仍然只按连接名区分。两套配置里出现同名连接时，密码、历史、草稿和最近打开会混在一起。

## 2. 目标

- 用 `datavim -w <name>` 或环境变量 `DATAVIM_WORKSPACE` 在启动时选择工作空间。
- 每个空间有自己的连接、密码、SQL 历史、控制台草稿和最近打开记录。不同空间里的同名连接互不影响。
- `config.toml`、`keymap.toml`（设置、主题、按键）在所有空间之间共享。
- 不指定空间时进入默认空间，行为和文件位置都和现在完全一样，现有用户不需要迁移。
- 当前空间名必须清楚可见，避免把空间搞错。

**不做：**
- 在应用内热切换空间。要切换，就退出后用另一个 `-w` 重新启动。
- 让一个连接同时属于多个空间。需要的话，就在两个空间里各写一份。
- 自动把现有连接迁移到其他空间，也不自动清理 keychain 里的旧条目。
- 按空间覆盖设置或按键。
- 每个空间记住并恢复打开的标签页。

## 3. 命令行

| 参数 | 说明 |
|---|---|
| `-w, --workspace <name>` | 选择工作空间。优先级高于 `DATAVIM_WORKSPACE`。 |
| `--create` | 空间不存在时先创建，然后正常启动。空间已存在时不报错。必须配合 `-w` 或 `DATAVIM_WORKSPACE` 使用，不能用于 `default`。 |
| `--list-workspaces` | 每行输出一个空间名后退出。`default` 永远在第一行，其余按字母排序。 |

空间名的解析顺序：`-w` 参数 > `DATAVIM_WORKSPACE` > `default`。

### 3.1 名字规则

- 只允许 `[a-z0-9_-]`，长度 1–32。
- 不自动转换大小写，`Work` 直接报错。这样两个名字不会指向同一个目录，名字也可以直接用作目录名和 keychain 前缀，不会有路径注入（`../`）。
- `default` 是保留名，表示默认空间。

### 3.2 错误

```
datavim: workspace "personal" does not exist (have: default, work)
create it with: datavim -w personal --create
```

```
datavim: invalid workspace name "Work": use 1-32 characters from a-z, 0-9, '_' and '-'
```

```
datavim: --create needs a workspace name (-w or DATAVIM_WORKSPACE)
```

`--create` 配合 `default` 使用时，也按上面这种格式报错。

## 4. 目录与数据归属

`<cfg>` 是配置根目录：默认为 `$XDG_CONFIG_HOME/datavim` 或 `~/.config/datavim`，传了 `--config` 时用它的值。`<state>` 是状态根目录：`$XDG_STATE_HOME/datavim` 或 `~/.local/state/datavim`。

```
<cfg>/
  config.toml  keymap.toml            # 所有空间共享
  connections.toml                    # 默认空间
  workspaces/
    work/connections.toml
    personal/connections.toml
<state>/
  datavim.log                          # 共享
  state.db                            # 默认空间
  workspaces/
    personal/state.db
```

| 数据 | 默认空间 | 空间 `personal` |
|---|---|---|
| 连接 | `<cfg>/connections.toml` | `<cfg>/workspaces/personal/connections.toml` |
| 设置、主题、按键 | `<cfg>/config.toml`、`<cfg>/keymap.toml` | 同左（共享） |
| 历史、草稿、最近打开 | `<state>/state.db` | `<state>/workspaces/personal/state.db` |
| keychain account | `<conn>` | `personal/<conn>` |
| 密码环境变量 | `DATAVIM_PASSWORD_<CONN>` | 同左，不加前缀 |
| 日志 | `<state>/datavim.log` | 同左，启动日志带 `workspace=personal` |

判断一个空间是否存在，只看 `<cfg>/workspaces/<name>/` 是否为目录。状态目录在启动时按需创建。

密码环境变量不加前缀：环境变量只对本次进程有效，而一次进程只会处于一个空间，所以不会跨空间冲突。

`--create` 用权限 0700 创建 `<cfg>/workspaces/<name>/` 和 `<state>/workspaces/<name>/`，不写入任何文件。连接仍然通过 UI 新建，或者手动编辑 toml。

## 5. 代码改动

### 5.1 `internal/config`

新增 `workspace.go`：

```go
const DefaultWorkspace = "default"

type Workspace struct {
	Name      string // "default" 或合法空间名
	ConfigDir string // 存放 connections.toml 的目录
	StateDir  string // 存放 state.db 的目录
}

// ValidWorkspaceName 校验 §3.1 的规则。
func ValidWorkspaceName(name string) error

// ResolveWorkspace 校验名字并拼出路径，不访问文件系统。
func ResolveWorkspace(cfgRoot, stateRoot, name string) (Workspace, error)

// ListWorkspaces 返回 "default" 加上 cfgRoot/workspaces 下合法名字的子目录（排序后）。
// 文件和不合法的目录名会被忽略；workspaces 目录不存在时只返回 ["default"]。
func ListWorkspaces(cfgRoot string) ([]string, error)

// Exists 报告空间是否存在；default 永远存在。
func (w Workspace) Exists() (bool, error)

// Create 创建 ConfigDir 和 StateDir（0700）。
func (w Workspace) Create() error

// IsDefault 报告是否为默认空间。
func (w Workspace) IsDefault() bool
```

新增 `LoadIn(root, connDir string)`，`Load(dir)` 保留为 `LoadIn(dir, dir)`：`connections.toml` 从 `connDir` 读，`config.toml`、`keymap.toml` 从 `root` 读。默认空间的 `connDir == root`。`AppendConnection`、`UpdateConnection`、`RemoveConnection` 本来就接收 dir 参数，调用方改传 `ws.ConfigDir` 即可。

### 5.2 `internal/secret`

`Keyring` 新增字段 `Prefix string`。`Get`、`Set`、`Delete` 访问 keychain 时使用 account `Prefix + name`。环境变量仍然用 `EnvVar(name)`，不受前缀影响。非默认空间的前缀是 `"<ws>/"`，默认空间为空。`session.Manager` 不用改。

测试用 `keyring.MockInit()` 检查前缀。

### 5.3 `cmd/datavim/main.go`

1. 解析 `-w/--workspace`、`--create` 和 `--list-workspaces`。
2. `--list-workspaces`：输出后返回。
3. 解析名字，调用 `ResolveWorkspace`；不存在时，带 `--create` 就创建，否则按 §3.2 报错。
4. `config.LoadIn(root, ws.ConfigDir)`，`openStore(ws.StateDir)`，`newManager(cfg, ws, tunnels)`（由 `ws` 得出连接文件目录和 keychain 前缀）。
5. `slog.Info("starting", ..., "workspace", ws.Name)`。
6. `ui.New(...)` 传入空间名（非默认空间才非空）。

日志仍然写到 `<state>/datavim.log`，和空间无关。

### 5.4 `internal/ui`

- `Model` 增加 `workspace string`（为空表示默认空间）。
- `renderStatus`：非默认空间时，在模式块后面加上 `theme.Accent.Render("[personal]")`。位置在 `[PROD]` 前面，这样空间和生产标记能同时看到。
- 欢迎页：非默认空间时，欢迎页框的标题为 `datavim — personal`。
- 窗口标题：非默认空间时为 `datavim — personal`，默认空间保持现状。

## 6. 安全

- 工作空间只决定读哪些配置和状态。readonly、生产确认、无 WHERE 的 UPDATE/DELETE 拦截、暂存修改只影响一行、事务中拒绝静默重连，这些规则仍然按连接自身的配置生效。
- 密码不进日志，也不显示。keychain 前缀只出现在 account 名里。
- 空间名经过 §3.1 校验后才用来拼路径，不会越出 `workspaces/` 目录。

## 7. 迁移（写进 README）

把默认空间里的个人连接挪到 `personal` 空间：

1. `datavim -w personal --create`，然后退出。
2. 把相应的 `[[connection]]` 段落从 `<cfg>/connections.toml` 剪切到 `<cfg>/workspaces/personal/connections.toml`。
3. 用 `datavim -w personal` 启动，首次连接时重新输入密码（会存为 `personal/<conn>`）。旧 keychain 条目可以在"钥匙串访问"里手动删除。
4. 旧连接的历史和草稿留在默认空间的 `state.db` 里，不会跟过去。

README 还要给一个 direnv 示例：在工作项目目录的 `.envrc` 里写 `export DATAVIM_WORKSPACE=work`。

## 8. 测试

先写测试。

- `config`（表驱动）：
  - `ValidWorkspaceName`：合法、空、超长、大写、`../x`、`a/b`、`default`。
  - `ResolveWorkspace`：default 的路径等于根目录；`personal` 的路径；`--config` 自定义根目录。
  - `ListWorkspaces`：没有 workspaces 目录；忽略文件和不合法名字的目录；排序后 default 排在第一个。
  - `LoadIn(root, connDir)`：连接从 connDir 读，设置和 keymap 从 root 读。
  - `Create`：目录权限为 0700；重复调用不报错。
- `secret`：用 `keyring.MockInit()` 检查 Prefix 拼接；环境变量覆盖时忽略前缀。
- `cmd/datavim/main_test.go`：
  - 空间不存在时报错，错误信息包含现有空间列表和 `--create` 提示。
  - 带 `--create` 时创建两个目录。
  - 不给名字时用 `--create` 报错；`--create` 用于 `default` 时报错。
  - `--list-workspaces` 的输出。
  - `-w` 优先于 `DATAVIM_WORKSPACE`。
  - 不合法名字报错。
- `ui`：非默认空间时状态栏和欢迎页显示空间名；默认空间时不显示。

## 9. 文档

- `CHANGELOG.md` 的 `[Unreleased]` 下增加 Added 条目：工作空间（`-w`、`DATAVIM_WORKSPACE`、`--create`、`--list-workspaces`）。
- `README.md` 和 `README.zh-CN.md` 增加"工作空间"一节，内容是 §3、§4 的摘要、§7 的迁移步骤和 direnv 示例。
- 主设计文档 §5.1 和 §7 补一句话指向本文。
