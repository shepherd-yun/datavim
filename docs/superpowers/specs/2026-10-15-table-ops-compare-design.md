# 表操作与结构 / 数据对比 设计文档

- 日期：2026-10-15
- 状态：待审阅
- 范围：树上的表操作（Drop / Rename / Truncate）、Modify Table（可编辑结构页）、结构对比与同步、数据对比与同步。分四个阶段实现，每个阶段单独写 plan、单独发版。

## 1. 背景

DataGrip 在表上右键可以 Modify Table、Drop Table、Rename Table，还可以用 Compare With… 对比两张表（或两个 schema）的结构和数据，并生成同步脚本。sqlvim 目前只能看结构（`<Space>s` / `<Space>S`）、复制 DDL（`<Space>yd`），表结构的变更都要在 console 里手写 SQL。

作者的对比场景有三种：

1. **跨环境同步结构**：dev / staging 的表结构改了，要同步到 prod，主要按 schema 或库整体对比。
2. **同一张表在两边的数据对账**：如两个环境的配置表、字典表数据不一致，要找出差异并补齐。
3. **同库内两张表对比**：如 `users` 和 `users_bak`、迁移前后的新旧表。

三种都是同一种引擎内的对比。

## 2. 目标与非目标

**目标**

- 在树上用键盘完成 Drop、Rename、Truncate，用动作菜单代替右键菜单。
- Modify Table：结构页可以编辑列和索引，提交时生成 ALTER，预览后执行。
- 结构对比：表对表、schema 或库对 schema 或库，同连接与跨连接都支持；生成的同步脚本放进目标连接的 console。
- 数据对比：表对表，列出差异行，生成 INSERT / UPDATE / DELETE 脚本放进 console。
- 现有安全规则（readonly、prod 确认、UPDATE / DELETE 不带 WHERE 的拦截、事务中不静默重连）一条都不少。

**不做**

- 跨引擎（MySQL ↔ PostgreSQL）对比。
- 视图、函数、触发器、分区的对比与同步。
- 修改外键：外键差异只显示，不生成同步语句；结构页编辑模式也不支持编辑外键。
- 调整列顺序。
- 没有主键、也没有 unique NOT NULL 索引的表的数据对比：直接拒绝，理由与"数据编辑必须恰好影响一行"（主设计文档 §5.8）相同。
- 改类型时自动生成数据迁移。
- 在 Diff 标签页里同时向两边同步（每个对比只有一个方向）。

## 3. 架构

### 3.1 方案选择

结构对比的核心流程是：读两边的元数据 → 算差异 → 生成 ALTER。选择的方案是**差异计算与 SQL 生成分离**：

- 新增纯函数包 `internal/schemadiff`，只输出与引擎无关的变更操作。
- 方言相关的 SQL 由驱动生成（`Dialect` 新增 `DDLWriter`），类型归一化也在驱动读取元数据时完成。

这样符合"方言差异只在 `db` 层"的分层原则。放弃的方案：

- 在 `schemadiff` 里按方言 switch 生成 SQL：方言知识漏出 `db` 层。
- 对两边的 `SHOW CREATE TABLE` 做文本 diff：显示容易，但生成不了 ALTER。

Modify Table 与结构同步共用这一套：Modify Table 是 `DiffTable(编辑前, 编辑后)`，结构同步是 `DiffTable(目标, 源)`。

### 3.2 模块

依赖方向保持不变：`ui` → `schemadiff` / `edit` → `db` → 驱动。

**`internal/db`**

- 新增 `TableSchema`：

  ```go
  type TableSchema struct {
      Ref         TableRef
      Columns     []Column
      Indexes     []Index
      ForeignKeys []ForeignKey
      Comment     string
      Options     map[string]string // MySQL: engine、charset；PG 为空
  }
  ```

- `Column`、`Index` 扩展字段见 §4。
- `MetaReader` 新增 `TableSchema(ctx, t TableRef) (TableSchema, error)`，表不存在时返回 `ErrNotFound`。
- `Dialect` 新增 `DDL() DDLWriter`：

  ```go
  type DDLWriter interface {
      DropTable(t TableRef, view bool) string
      RenameTable(t TableRef, newName string) string
      TruncateTable(t TableRef) string
      CreateTable(s TableSchema) string
      // AlterTable 把同一张表的变更写成语句。MySQL 尽量合并成一条
      // ALTER TABLE；PG 每项变更各一个子句。写不出的变更返回在 manual 里。
      AlterTable(t TableRef, changes []SchemaChange) (stmts []string, manual []SchemaChange)
  }
  ```

  为了不让 `db` 依赖 `schemadiff`，变更类型定义在 `db` 里（`db.SchemaChange`，包含种类、前后值与风险标记），`schemadiff` 负责计算。下文简称 `Change`。

- 以上行为全部写进 `dbtest/contract.go`，MySQL 与 PG 都要通过。

**`internal/schemadiff`（新增，纯函数）**

- `DiffTable(target, source db.TableSchema, renames map[string]string) []Change`：把 target 改成与 source 一致所需的变更。列按名字配对；`renames`（旧名 → 新名）只由 Modify Table 传入，用来产生 `RenameColumn`，对比时传 nil。
- `DiffNamespace(target, source []db.TableSchema) []ObjectDiff`：按表名配对。每个 `ObjectDiff` 是"源有目标无"（新增表）、"目标有源无"（删除表）或"两边都有"（附带 `DiffTable` 的结果）之一。
- `Change` 的种类：`AddColumn`、`DropColumn`、`AlterColumn`（类型 / 可空 / 默认值 / 注释 / 排序规则分别标记）、`RenameColumn`（只在传入 `renames` 时产生）、`AddIndex`、`DropIndex`、`AlterTableComment`、`AlterTableOption`，以及只显示不同步的 `ForeignKeyDiff`。
- 每项变更带风险标记：
  - `Destructive`：可能丢数据（删表、删列、删索引中的唯一约束）。
  - `Lossy`：类型变窄（如 `varchar(255)` → `varchar(100)`、`bigint` → `int`）、可空改为 NOT NULL。
- 索引变化（列、唯一性、方法、条件不同）表示为 DropIndex + AddIndex。

**`internal/ui`**

- 树：动作菜单、按节点类型区分的 `dd` / `e`、`r`、对比标记 `m` / `C`。
- 结构页（`ui/panes/structure` 与对应的 tab）：编辑模式。
- 新增 `diffTab`：结构视图与数据视图。keymap 新增 `Diff` 上下文。
- 数据对比的纯逻辑放在新包 `internal/datadiff`（输入两边的行流与键列，输出差异行），不依赖终端。

### 3.3 执行路径

**Drop / Rename / Truncate / Modify Table**

1. 检查与这张表相关的标签页：数据页有未提交的修改时拒绝，提示提交或丢弃（同现有的 `canChange` 规则）。
2. readonly 连接直接拒绝。
3. 用 `overlay/preview` 显示将执行的 SQL。prod 连接要输入表名确认（`overlay.Confirm` 的输入名字模式），其它连接按 `y`。Drop 与 Truncate 在任何连接上都要输入表名确认。
4. 通过连接池的 `Conn` 执行（不使用 console 的 Session）。
   - PG：多条语句在一个事务里执行。
   - MySQL：Modify Table 合并成一条 `ALTER TABLE`；DDL 会隐式提交，所以不包在事务里。
5. 执行后：
   - 刷新这张表所在命名空间的元数据缓存（`meta`）与树节点。
   - Drop：关闭这张表的数据页和结构页。
   - Rename：更新这些标签页的 `TableRef` 与标题。

**Compare**

生成的同步脚本放进**目标连接的新 console**，不直接执行。readonly、prod 确认、无 WHERE 拦截、事务检测都由 console 现有的执行路径负责；用户可以先修改脚本再运行。

## 4. 元数据扩展

| 结构 | 新字段 | 说明 |
|---|---|---|
| `Column` | `AutoIncrement bool` | MySQL `auto_increment`；PG identity 与 `serial`（默认值为 `nextval(...)` 的列） |
| | `Collation string` | 只在与表的默认排序规则不同时填写 |
| | `Generated string` | 生成列的表达式，非生成列为空 |
| | `Position int` | 从 1 开始，只读；MySQL 新增列时用来生成 `AFTER` |
| `Index` | `Method string` | btree / hash / gin / gist / fulltext …，归一化为小写 |
| | `Where string` | PG 部分索引的条件，MySQL 为空 |
| `TableSchema` | `Comment`、`Options` | 见 §3.2 |

**类型归一化**由驱动在读取时完成，归一化后再比较，避免假的差异：

- 一律小写。
- PG：`character varying(n)` → `varchar(n)`，`character(n)` → `char(n)`，`timestamp without time zone` → `timestamp`，`timestamp with time zone` → `timestamptz`，`integer` → `int`，`boolean` → `bool`。
- MySQL 8：去掉整数类型的显示宽度（`int(11)` → `int`），`tinyint(1)` 保留。
- 默认值同样归一化（去掉 PG 的 `::type` 类型转换后缀）。

## 5. 交互

所有提示文字都用 `keyHint` 生成，新按键都加进 `keymap/defaults.go`，help、which-key 与提示随之更新。

### 5.1 树

| 键 | 节点 | 动作 |
|---|---|---|
| `a` | 任意节点 | 动作菜单（which-key 样式），列出当前节点可用的全部操作及其按键。Truncate 只在这里出现 |
| `dd` | 连接 | 删除连接（现有行为） |
| `dd` | 表 / 视图 | `DROP TABLE` / `DROP VIEW`，输入名字确认 |
| `e` | 连接 | 编辑连接（现有行为） |
| `e` | 表 | Modify Table：打开结构页并进入编辑模式 |
| `r` | 表 / 视图 | 重命名：输入框预填原名 → 预览 SQL → 执行 |
| `m` | 表 / schema / 库 | 标记为对比源，再按一次取消。节点前显示 `◆`，状态栏提示 |
| `C` | 表 / schema / 库 | 已标记的节点作为源、当前节点作为目标进行对比。没有标记时弹出 picker 选源 |

`C` 的限制：

- 源与目标的节点类型必须相同（表对表、schema 对 schema、库对库）。MySQL 的库与 PG 的 schema 都视为"命名空间"，但两边引擎不同时一律拒绝。
- 源与目标相同时拒绝。

### 5.2 结构页的编辑模式

用树上的 `e` 进入，按键与数据表格的编辑一致：

| 键 | 动作 |
|---|---|
| `i` / `enter` | 编辑当前单元格：列名、类型、可空、默认值、注释；索引的名字、列、唯一性 |
| `o` | 在当前区（列 / 索引）新增一行 |
| `dd` | 标记删除 |
| `u` / `U` | 撤销上一项 / 全部 |
| `<Space>w` | 提交 |

- 改过的项标黄，新增的标绿，删除的标红并加删除线（配色同数据编辑）。
- 提交时把编辑前后的 `TableSchema` 交给 `schemadiff.DiffTable`，再由 `DDLWriter` 生成 ALTER。预览里 `Destructive` 与 `Lossy` 的变更标红。
- 结构页有未提交的编辑时，关闭标签页或删除这张表的行为同数据页（提示提交或丢弃）。
- 外键区只读。

### 5.3 Diff 标签页：结构视图

一个可折叠的变更列表：

```
 prod.app ──▶ staging.app        3 objects differ · 1 destructive skipped
 ▾ ~ users
     [x] ~ column name    varchar(100) → varchar(255)
     [x] + column email   varchar(255) NULL
     [ ] - column legacy  int                         ⚠ destructive
 ▸ + audit_log            (new table, 12 columns)
 ▸ - tmp_import                                       ⚠ destructive
     ⚠ manual  ~ column price  numeric → int  (needs USING)
```

- 标题行：源 ──▶ 目标。生成的脚本把**目标**改成与源一致。
- 对象行：`+` 源有目标无（建表）、`-` 目标有源无（删表）、`~` 两边都有且有差异。没有差异的表不列出，标题行给出数量。

| 键 | 动作 |
|---|---|
| `j` / `k` 等 | 移动 |
| `l` / `h` | 展开 / 折叠对象 |
| `]c` / `[c` | 下一处 / 上一处变更 |
| `x` | 切换当前变更是否纳入同步；在对象行上时作用于它下面的全部变更 |
| `~` | 交换源与目标，重新计算 |
| `D` | 在当前这对表上切换到数据视图（§5.4） |
| `<Space>w` | 用勾选的变更生成脚本，打开到目标连接的新 console |
| `<Space>r` | 重新读取两边并对比 |

- `Destructive` 的变更**默认不勾选**，其余默认勾选。
- `⚠ manual` 的变更不能勾选；脚本里只留一行注释说明要手动处理。
- 外键差异显示为只读行，不能勾选。
- 脚本的顺序：建表 → 各表 ALTER → 删表。脚本开头的注释写明源、目标与生成时间。

### 5.4 Diff 标签页：数据视图

```
 prod.app.settings ──▶ staging.app.settings   key: id   12 differ (3 + · 1 - · 8 ~)
     id   name           value
 [x] +  7  feature_x      on
 [x] ~ 12  timeout        30 → 60
 [ ] -  40 legacy_flag    1
```

- 只列出有差异的行。状态列：`+` 仅源有、`-` 仅目标有、`~` 值不同；值不同的单元格高亮并显示 `目标 → 源`。
- 键列：主键，没有主键时用第一个 unique NOT NULL 索引（规则同 `internal/edit`）；两边的键列必须一致，否则拒绝。都没有时拒绝并说明原因。
- 两张表的列集合不一致时，只比较共有的列，并在标题行下提示"结构不一致，仅比较 N 个共有列"。
- 按键：`]c` / `[c`、`x`、`<Space>w`、`<Space>r` 同结构视图；`F` 设置两边共用的 WHERE 条件（复用表数据过滤的输入框与补全）；`D` 回到结构视图。
- DELETE（`-` 行）默认不勾选。
- 生成的 INSERT / UPDATE / DELETE 复用 `edit` 的 DML plan，每条 UPDATE / DELETE 都带键列的 WHERE。

**算法**

- 不用服务端 ORDER BY 后归并：两个环境的排序规则可能不同（如 MySQL 的 `_ci` 排序规则），归并结果会出错。
- 读入源端的全部行（带 WHERE），按键列的文本值建哈希表；再流式读取目标端逐行比对，最后剩下的就是仅源有的行。
- 每边最多读 `compare_max_rows` 行（`config.toml`，默认 100000）。超过时中止，提示用 `F` 缩小范围。不做只覆盖部分数据的对比。
- 值的比较用 `db.Value`：`Null` 与空串区分；文本按服务端返回的文本表示逐字节比较。

## 6. 错误处理

- 对比时某一边加载元数据或数据失败：标签页里标明是哪一边、什么错误，`<Space>r` 重试。
- 加载过程可以用 `Ctrl-c` 取消，与查询共用 context 取消机制。
- 驱动写不出的变更（如 PG 改类型需要 `USING`）显示为 `⚠ manual`，见 §5.3。
- Modify Table 执行失败：PG 整个事务回滚；MySQL 只有一条 ALTER，失败即未生效。两种情况都保留暂存的编辑，显示错误，修正后可再次提交。
- 执行 DDL 时连接断开（`ErrConnLost`）：报错，不自动重连重试。
- Drop / Rename / Truncate 失败：显示数据库错误，树与标签页保持不变。

## 7. 测试

- `schemadiff`：表驱动测试，覆盖每种 `Change`、每种风险标记、索引的 Drop + Add 表示、`DiffNamespace` 的配对。
- `DDLWriter`：每种方言一组 golden SQL 单元测试。
- **contract 往返测试**（`dbtest.RunContract`，MySQL 8 与 PG 16 都跑）：建表 A、B → 读取 → `DiffTable(B, A)` → `AlterTable` 生成语句 → 在 B 上执行 → 重新读取 B → `DiffTable(B, A)` 必须只剩 `manual` 与外键项。另覆盖 `TableSchema` 的新字段、类型归一化、drop / rename / truncate。
- `datadiff`：纯函数测试，覆盖键匹配、NULL 与空串、只比较共有列、超过行数上限中止、无可用键时拒绝。
- UI（fake backend）：`dd` 在不同节点上的行为、`r` 重命名后标签页更新、有未提交修改时拒绝 Drop、`m` / `C` 的配对与限制、`x` 勾选与默认值、`<Space>w` 生成的脚本进入目标连接的 console、结构页编辑模式的暂存与提交。

## 8. 阶段

| 阶段 | 版本 | 内容 | 验收 |
|---|---|---|---|
| 1 | v1.2 | 树的动作菜单 `a`；Drop、Rename、Truncate；`DDLWriter` 先实现 `DropTable`、`RenameTable`、`TruncateTable` | contract 覆盖 drop / rename / truncate；相关标签页的关闭与更新；prod 与 Drop 要输入名字确认 |
| 2 | v1.3 | 元数据扩展与类型归一化、`TableSchema`、`schemadiff`、`DDLWriter.AlterTable` / `CreateTable`、Diff 标签页的结构视图（表、schema 或库）、`m` / `C`、脚本进入 console | 两个引擎上的往返测试通过 |
| 3 | v1.4 | 结构页的编辑模式（Modify Table） | 复用第 2 阶段的 `schemadiff` 与 `DDLWriter`，不新增方言代码 |
| 4 | v1.5 | `datadiff` 与 Diff 标签页的数据视图 | 行数上限、两边共用的 WHERE、DELETE 默认不勾选 |

每个阶段开始时再写 plan（`docs/superpowers/plans/`），写之前回顾上一阶段的实际结果，必要时修订本文档的对应章节。

## 9. 待定

- 第 4 阶段：表很大时是否按键分段做校验和、只展开不一致的段，以突破 `compare_max_rows`。等第 4 阶段实际使用后再决定。
