# 树展开表结构与 z 折叠键 设计文档

- 日期：2026-10-16
- 状态：已确认，待实现
- 范围：树中的表和视图可以继续展开为 Columns / Keys / Foreign Keys / Indexes 四组，子节点可以跳转和编辑；树增加 vim 风格的 `z` 折叠键；修订主设计 §4.1、§5.11、§6.2

## 1. 背景

树现在只到表这一层（连接 → 库 / schema → 表 / 视图），想看一张表有哪些列、键和索引，必须打开结构 tab。DataGrip 的树可以继续展开表，下面分 Columns、Keys、Foreign Keys、Indexes 几组，再展开查看和操作，浏览结构不用离开树。

同时，树展开的层级变深以后，逐个 `h` 折叠很麻烦，需要一组快速展开和折叠的按键。

## 2. 目标

- 表和视图可以在树中展开，查看列、主键与唯一键、外键、索引。
- 子节点可以跳到结构 tab 的对应行，也可以直接进入 Modify Table 修改或删除，复用结构 tab 已有的编辑与提交流程。
- 树支持 vim 折叠键：`zo` `zc` `za` `zO` `zC` `zM` `zR` `zx`。
- 安全规则不变：只读连接、生产环境确认、事务中不能修改结构，全部沿用现有路径。

**不做：**
- 在树中直接执行 DROP COLUMN / DROP INDEX（只在 Modify Table 中暂存，`:w` 预览确认后执行）。
- 外键的编辑（Modify Table 目前不支持外键）。
- `zO` / `zR` 递归加载尚未加载的节点。
- 检查约束、触发器、分区等其他分组。

## 3. 节点模型

`panes/tree` 新增节点类型：

| Kind | 含义 | 可展开 |
|---|---|---|
| `KindGroup` | 分组：Columns / Keys / Foreign Keys / Indexes，由 `Group` 字段区分 | 是 |
| `KindColumn` | 列 | 否 |
| `KindKey` | 主键或唯一键 | 否 |
| `KindForeignKey` | 外键 | 否 |
| `KindIndex` | 索引 | 否 |

- `Node` 增加 `Group`（分组种类）和 `Detail`（灰色显示的附加文字）。叶子节点的 `NS` 与所属表相同，`Name` 是列名 / 键名 / 索引名。外键节点另外记录被引用的表 `Ref db.TableRef`。
- `Expandable()` 覆盖连接、库、schema、表、视图和分组。`IsTable()` 仍然只对表和视图成立。
- 新增 `Node.Table()`：返回节点自身或最近的表 / 视图祖先，没有则返回 nil。现在用 `IsTable()` 取"当前表"的操作（`<Space>s`、`<Space>S`、`<Space>yd`、`<Space>c` 的表上下文、`r` 重命名、`a` 菜单）改为用 `Table()`，因此在子节点上也能用。
- compare 标记（`m` / `C`）仍然只接受表、库和 schema；在子节点上按 `m` 提示不支持。

## 4. 加载

- 展开表或视图沿用 `loadChildren` → `fetchChildren`，新增 `KindTable` / `KindView` 分支：调用一次 `MetaReader.TableSchema`，一次拿到列、索引和外键，同时生成分组节点和全部叶子节点。分组节点创建时就是 `Loaded=true`，展开分组不再查询。
- `TableSchema` 返回 `ErrNotFound`（表已被删除）时，节点显示错误，与现有加载失败一致。
- 空分组不显示。视图通常只有 Columns 一组。
- 分组内容：
  - **Columns**：按 `Position` 排序。
  - **Keys**：`Primary` 或 `Unique` 的索引（PostgreSQL 上包括 `Constraint` 为真的唯一约束），主键排在最前。
  - **Foreign Keys**：全部外键。
  - **Indexes**：全部索引，包括主键和唯一键对应的索引（与 DataGrip 一致，Keys 和 Indexes 有重叠）。
- 刷新：
  - `<Space>r` 在表或其子节点上重新加载这张表的子节点，展开状态按名字保留（分组按 `Group` 匹配）。
  - 结构 tab 通过 Modify Table 提交成功后，如果树中这张表已经加载过子节点，一并重新加载（挂在现有的 `refreshTabsOfTable` 旁边）。
  - 重命名、删除表之后，现有的 `reloadTables` 会重建表节点，子节点随之丢弃，不需要额外处理。

## 5. 显示

```
▾ • users
    ▾ Columns
        id  bigint PK
        email  varchar(255)
        org_id  bigint
    ▾ Keys
        PRIMARY  (id)
        uk_email  (email)
    ▾ Foreign Keys
        fk_org  (org_id) → orgs(id)
    ▾ Indexes
        PRIMARY  (id) unique
        uk_email  (email) unique
        idx_org  (org_id)
▸ ◦ active_users
```

- 表和视图前面加展开箭头，原来的 `•` / `◦` 保留，用来区分表和视图。
- 叶子节点名字后面跟灰色（`theme.Muted`）的 `Detail`：
  - 列：类型；主键列加 `PK`；不可空不标注，保持简洁。
  - 键 / 索引：`(列, 列)`；索引再加 `unique`、非 btree 的方法（如 `gin`）、PostgreSQL 部分索引加 `where …`。
  - 外键：`(列) → 表(列)`；被引用表与当前表不在同一命名空间时写限定名。
- `Detail` 和名字一样经过 `textutil.Sanitize`，宽度不够时随整行截断。
- `/` `n` `N` 搜索只匹配 `Name`，不匹配 `Detail`。

## 6. 按键

### 6.1 展开与打开

现在 `l` `Enter` `→` 都绑定到 `tree.expand`，在表上打开数据。改为两个动作：

| 动作 | 默认键 | 表 / 视图 | 库 / schema / 分组 | 叶子 |
|---|---|---|---|---|
| `tree.expand` | `l` `→` | 展开 | 展开 | 无操作 |
| `tree.open`（新增） | `Enter` | 打开数据 | 展开 | 见 6.2 |

`h` `←`（`tree.collapse`）不变。连接断开时，`Enter` 和 `l` 都会像现在一样先重试。CHANGELOG 在 `Changed` 中说明：表上的 `l` / `→` 改为展开，打开数据用 `Enter`。

### 6.2 子节点上的操作

| 节点 | `Enter` | `e` | `dd` | `yy` / `yq` |
|---|---|---|---|---|
| 列 | 打开结构 tab，光标停在该列 | 打开 Modify Table，光标停在该列 | 打开 Modify Table 并暂存删除该列 | 列名 / `表.列` |
| 键、索引 | 打开结构 tab 的 Indexes 子页，光标停在该行 | 打开 Modify Table，光标停在该索引 | 打开 Modify Table 并暂存删除该索引 | 名字 / `表.名字` |
| 外键 | 在树中定位被引用的表 | 提示 "foreign keys cannot be modified yet · change them in a console" | 同左 | 名字 / `表.名字` |
| 分组 | 展开 / 折叠 | 打开 Modify Table（整张表） | 提示 "select a column or index to drop it" | 复制所属表的名字 / 限定名 |

- 跳转与编辑全部经过 `openStructure` / `modifyTable`，二者增加可选的目标位置（子页 + 名字）。结构数据尚未加载时，位置在加载完成后应用（与数据 tab 的 `focusColumn` 相同的做法）。
- `dd` 只是暂存：结构 tab 进入 Modify Table，对目标行调用 `DeleteColumn` / `DeleteIndex`，状态栏提示 `:w` 提交、`U` 放弃。不新增执行路径，只读、生产确认、事务检查、"表有未保存修改" 的检查都由 Modify Table 现有逻辑负责。
- 视图上的子节点：`e` / `dd` 提示视图不能修改（沿用 `errViewNotModifiable`）。
- 键节点在 MySQL 上对应同名索引；在 PostgreSQL 上对应约束背后的索引，Modify Table 删除时生成的是 DROP CONSTRAINT（PostgreSQL 的 `DDLWriter` 已按 `Constraint` 处理）。
- `a` 菜单为子节点列出对应的条目。
- 外键的 `Enter` 用现有的 `reveal` 展开路径并选中被引用的表；该表所在的库未加载时按 `Reveal` 的 `NeedsLoad` 流程逐级加载。

### 6.3 z 折叠键

Tree 上下文新增以下动作，绑定在 `keymap/defaults.go`，可在 `keymap.toml` 中重新绑定：

| 键 | 动作 | 说明 |
|---|---|---|
| `zo` | `tree.fold.open` | 展开当前节点，需要时加载（与 `l` 相同） |
| `zc` | `tree.fold.close` | 折叠当前节点；在叶子或已折叠的节点上，折叠父节点并移到父节点 |
| `za` | `tree.fold.toggle` | 展开或折叠当前节点 |
| `zO` | `tree.fold.open_all` | 递归展开当前节点下已加载的子树 |
| `zC` | `tree.fold.close_all` | 递归折叠当前节点下的子树 |
| `zM` | `tree.fold.close_tree` | 折叠全部，只剩连接列表，光标移到所在连接 |
| `zR` | `tree.fold.open_tree` | 展开全部已加载的节点 |
| `zx` | `tree.fold.focus` | 折叠其他所有节点，只保留到光标的路径 |

- `zO` / `zR` 只展开 `Loaded=true` 的节点，不发出任何查询；未加载的节点保持折叠。正在加载或加载失败的节点不动。
- 折叠只改 `Expanded`，不丢弃已加载的子节点，再次展开不需要重新查询。
- 递归折叠后，如果光标所在节点被隐藏，光标移到最近的可见祖先（`cursor()` 已有这个行为）。
- 这些操作实现为 `tree.Model` 的纯方法：`OpenLoaded(n *Node)`、`CloseAll(n *Node)`、`CloseTree()`、`OpenTree()`、`FocusPath()`。`n` 为 nil 时作用于全部根节点。

## 7. 测试

- `panes/tree`（表驱动）：
  - 表、视图、分组可展开，叶子不可展开；`Table()` 从各层返回正确的表。
  - 图标与 `Detail` 渲染；`Detail` 经过清理；搜索不匹配 `Detail`。
  - 每个折叠操作：`zO` / `zR` 不展开未加载的节点；`zC` 后光标回到可见祖先；`zx` 只保留路径；`zM` 后光标在连接上。
  - 刷新后按名字保留展开状态。
- `ui`：
  - `fetchChildren` 从 `TableSchema` 生成四组，空组隐藏，视图只有 Columns，Keys 排序正确，外键 `Detail` 在跨命名空间时写限定名。
  - `l` 展开表，`Enter` 仍然打开数据。
  - `Enter` 在列上打开结构 tab 并停在该列；在索引上停在 Indexes 子页对应行；在外键上定位被引用的表。
  - `dd` 在列上进入 Modify Table 并暂存删除，不执行任何 SQL；只读连接被拒绝。
  - `e` / `dd` 在视图子节点和外键上给出提示。
  - Modify Table 提交后，树中已展开的表重新加载。
- `keymap`：新动作能解析，与现有树按键不冲突；帮助和 which-key 显示 `z` 组。

## 8. 文档

- README 与 README.zh-CN 的功能列表和按键表。
- CHANGELOG `[Unreleased]`：`Added`（展开表结构、z 折叠键）、`Changed`（表上 `l` / `→` 改为展开）。
- 主设计：§4.1 树的层级、§5.11 结构视图（树中也能查看）、§6.2 导航键；顺带把 §4.1 和 §6.3 中残留的 `<Space>b` 改为 `<Space>e`，`<Space>e`（最近打开）改为 `<Space>f`。
