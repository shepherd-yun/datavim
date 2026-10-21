# Modify Table 第 3 阶段（v1.4）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在树上对表按 `e`（或在结构页按 `e`）进入结构页的编辑模式，用与数据表格一致的按键暂存列和索引的修改，`<Space>w` 预览生成的 ALTER 后执行。

**Architecture:** 新增纯函数包 `internal/schemaedit` 暂存编辑（列、索引的行，撤销栈），`Plan` 把暂存的编辑变成 `db.SchemaChange`：列的部分交给第 2 阶段的 `schemadiff.DiffTable(编辑前, 编辑后, renames)`，索引按"从哪一行编辑而来"配对（新增 `schemadiff.DiffIndexEdits`），不再按名字或定义配对。SQL 仍由第 2 阶段的 `DDLWriter.AlterTable` 写；`DDLWriter` 只新增 `NormalizeType`（暴露驱动已有的 `normType`），让键入的类型与读出的类型可比较。执行走新的 `edit.ApplyDDL`：PG 在一个事务里执行，MySQL 的 DDL 会隐式提交、逐条执行。UI 侧 `structure` pane 加行 / 字段光标与编辑标记，`structureTab` 加编辑模式；安全检查（readonly、prod 输入表名、其它标签页的未提交修改、console 的事务）由 `Model` 统一做。

**Tech Stack:** Go、Bubble Tea v2（`charm.land/bubbletea/v2`）、Lip Gloss v2（`charm.land/lipgloss/v2`）、pgx v5、go-sql-driver/mysql、testcontainers（集成测试）；不新增依赖。

**Spec:** `docs/superpowers/specs/2026-10-15-table-ops-compare-design.md`（本 plan 实现 §3.3 的 Modify Table 部分、§5.1 中 `e` 的两行、§5.2、§6 中 Modify Table 的条目、§8 第 3 阶段；第 2 阶段的调整见 spec §8.1，本阶段的调整在 Task 10 写进 spec §8.2）

## Global Constraints

- **UI 文案用英文。** 提示里的按键一律用 `keyHint` 或 `Keymap.KeysFor` 生成，不写死键名；新按键全部加进 `keymap/defaults.go`。
- UI 层不做 IO：所有数据库访问都在 `tea.Cmd` 里，带可取消的 `context.Context`。
- 方言差异只在 `internal/db/mysql`、`internal/db/postgres` 里。本阶段**唯一**的驱动改动是 `DDLWriter.NormalizeType`，它只转调驱动已有的 `normType`；不写新的方言 SQL。
- 依赖方向：`ui` → `schemaedit` → `schemadiff` → `db`；`ui/panes/structure` 可以 import `schemaedit`（纯包）。
- 外键区只读；不支持调整列顺序；不编辑生成列的表达式、自增属性、排序规则（保留原值）。
- 安全规则一条不少：readonly 拒绝；prod 必须输入表名确认，其它连接按 `y`；这张表的数据页有未提交修改、或同连接的 console 有未结束的事务时拒绝；执行中断开（`ErrConnLost`）不自动重试；失败时保留暂存的编辑。
- 不新增依赖；`go.mod` 不变。
- 每个任务结束时：`gofumpt -l .` 无输出、`go vet ./...` 通过、`go test -race ./...` 全绿；改到 `internal/db` 或 `dbtest` 的任务还要跑 `make test-integration`（需要 Docker；OrbStack 先 `orb start`）。
- Conventional Commits，带 scope，描述用中文（与仓库历史一致）。**commit message 不加 `Co-Authored-By` 行。**
- 每个用户可见的改动在 `CHANGELOG.md` 的 `[Unreleased]` 下记一行；README 的改动同时改 `README.zh-CN.md`。

## Review Focus

1. **一次提交里既改列名又做别的事**：没动过的索引要跟着改名后的列走（不能被当成"索引变了"）；MySQL 一条 `ALTER TABLE` 里 `CHANGE COLUMN name full_name …, ADD COLUMN email … AFTER full_name` 要能执行；把 `a` 改成 `b`、同时把 `b` 改名或删掉（交换名字）必须在提交前拒绝，因为 PG 逐条执行会撞名。→ Task 3 `TestRenamedColumnFollowsIntoIndexes`，Task 4 `TestPlanRenameCarriesIntoIndexes`、`TestPlanChecks` 的 swap 用例，Task 5 的往返测试。
2. **键入的类型换了写法**：`BIGINT` 对 `bigint`、PG 的 `integer` 对 `int`、MySQL 的 `int(11)` 对 `int` 都不算修改；PG 上不能因此变成"需要 USING"的 manual 而拒绝提交。→ Task 1 `TestNormalizeType*`，Task 3 `TestTypeInAnotherCaseIsNoChange`，Task 4 `TestPlanNormalizesTypedTypes`。
3. **只改了索引名、或改完又改回原样**：只改名要生成 Drop + Add（第 2 阶段的按定义配对会把它当成"没变"）；改回原样不生成任何语句。→ Task 2 `TestDiffIndexEdits`，Task 4 `TestPlanIndexRenameIsDropAndAdd`。
4. **提交前就能看出来做不成的事**：删掉一个仍被索引使用的列（PG 会连带删索引、MySQL 会改索引，都不是用户要的）、驱动写不出的变更（PG 改类型需要 `USING`）——都要在执行任何语句之前拒绝，说清楚原因，暂存的编辑原样保留。→ Task 4 `TestPlanRefusesDeletedColumnInIndex`，Task 9 `TestModifyRefusesManualChanges`。
5. **执行失败、被取消、连接断开、确认被重放**：PG 整个事务回滚，MySQL 一条 ALTER 失败即未生效；暂存的编辑保留；`Ctrl-c` 能取消一个跑很久的 ALTER（不设 30 秒超时）；断线不自动重试；同一个预览的 `y` 只执行一次。→ Task 1 的 contract 子测试 `ApplyDDL rolls back PostgreSQL DDL and stops at the first failure`，Task 9 `TestModifyFailureKeepsEdits`、`TestModifyCancelKeepsEdits`、`TestModifyConnLostIsNotRetried`、`TestModifyConfirmRunsOnce`。

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/db/driver.go` | `DDLWriter.NormalizeType` | 1 |
| `internal/db/mysql/ddlwriter.go`、`internal/db/postgres/ddlwriter.go` | `NormalizeType` 转调 `normType` | 1 |
| `internal/edit/apply.go` | `ApplyDDL`、`DDLError` | 1 |
| `internal/db/dbtest/contract.go` | `ApplyDDL` 与 `NormalizeType` 子测试；Modify Table 往返测试 | 1、5 |
| `internal/schemadiff/diff.go` | 抽出 `dropIndex` / `addIndex` | 2 |
| `internal/schemadiff/edits.go`（新） | `DiffIndexEdits` | 2 |
| `internal/schemaedit/table.go`（新） | 暂存模型：行、字段、增删改、撤销 | 3 |
| `internal/schemaedit/list.go`（新） | `splitList`、`unquote`、自动索引名 | 3 |
| `internal/schemaedit/plan.go`（新） | `check`、`Plan` | 4 |
| `internal/ui/keymap/*` | `node.edit`（兼容 `conn.edit`）、`table.modify`、Structure 上下文的编辑键 | 6 |
| `internal/ui/panes/structure/structure.go` | 行 / 字段光标、编辑模式渲染 | 7 |
| `internal/ui/panes/structure/table.go`（新） | 逐格着色的表格排版 | 7 |
| `internal/ui/structedit.go`（新） | `structureTab` 的编辑模式、提交、执行 | 8、9 |
| `internal/ui/modify.go`（新） | `Model` 侧：`stager`、`tableBlocked`、`modifyTable`、`modifySubmit`、`modifyApplied` | 8、9 |
| `internal/ui/tabs.go` | `structureTab` 新字段、`Handle`、`Update`、`View` | 8、9 |
| `internal/ui/tableops.go`、`internal/ui/app.go`、`internal/ui/nodemenu.go` | 改用 `stager` / `tableBlocked`；`e` 的分派；菜单项；`:w` | 6、8、9 |
| `internal/ui/overlay/preview.go`、`confirm.go`、`danger.go`（新） | `WithDanger` 标红指定行 | 9 |
| `internal/ui/fakes_test.go` | fake 的 `NormalizeType`、manual 类型、Exec 尊重 ctx | 1、9 |
| `CHANGELOG.md`、`README.md`、`README.zh-CN.md`、spec §8.2 | 文档 | 10 |

---

### Task 1: `DDLWriter.NormalizeType` 与 `edit.ApplyDDL`

**Files:**
- Modify: `internal/db/driver.go`（`DDLWriter` 加 `NormalizeType`）
- Modify: `internal/db/mysql/ddlwriter.go`、`internal/db/postgres/ddlwriter.go`
- Modify: `internal/db/mysql/mysql_test.go`、`internal/db/postgres/postgres_test.go`
- Modify: `internal/edit/apply.go`、`internal/edit/apply_test.go`
- Modify: `internal/db/dbtest/contract.go`（两个子测试）
- Modify: `internal/ui/fakes_test.go`（`fakeDDL.NormalizeType`）

**Interfaces:**
- Produces（Task 4、5、9 使用）：

  ```go
  // in package db, DDLWriter gains:
  // NormalizeType spells a column type the way TableSchema reports it
  // (INTEGER → int on PostgreSQL, INT(11) → int on MySQL), so that a typed
  // type compares equal to the one read.
  NormalizeType(t string) string

  // in package edit
  type DDLError struct {
      Index      int // 0-based
      SQL        string
      Err        error
      RolledBack bool // PostgreSQL: the statements before it were undone
  }
  func ApplyDDL(ctx context.Context, s db.Querier, d sqlx.Dialect, stmts []string) error
  ```

- [ ] **Step 1: 写驱动的单元测试**

`internal/db/mysql/mysql_test.go` 末尾追加：

```go
func TestNormalizeTypeIsTableSchemas(t *testing.T) {
	w := dialect{}.DDL()
	for in, want := range map[string]string{"INT(11)": "int", "VARCHAR(255)": "varchar(255)", "tinyint(1)": "tinyint(1)"} {
		if got := w.NormalizeType(in); got != want {
			t.Errorf("NormalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}
```

`internal/db/postgres/postgres_test.go` 末尾追加：

```go
func TestNormalizeTypeIsTableSchemas(t *testing.T) {
	w := dialect{}.DDL()
	for in, want := range map[string]string{
		"INTEGER":                  "int",
		"character varying(20)":    "varchar(20)",
		"TIMESTAMP WITH TIME ZONE": "timestamptz",
		"text[]":                   "text[]",
	} {
		if got := w.NormalizeType(in); got != want {
			t.Errorf("NormalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: 运行，确认编译失败**

Run: `go test ./internal/db/mysql ./internal/db/postgres -run TestNormalizeType`
Expected: FAIL，`w.NormalizeType undefined`

- [ ] **Step 3: 实现 `NormalizeType`**

`internal/db/driver.go` 的 `DDLWriter` 接口末尾（`AlterTable` 之后）加：

```go
	// NormalizeType spells a column type the way TableSchema reports it
	// (INTEGER → int on PostgreSQL, INT(11) → int on MySQL), so that a
	// typed type compares equal to the one read.
	NormalizeType(t string) string
```

`internal/db/mysql/ddlwriter.go` 与 `internal/db/postgres/ddlwriter.go` 各加一行（放在 `TruncateTable` 之后）：

```go
func (ddlWriter) NormalizeType(t string) string { return normType(t) }
```

`internal/ui/fakes_test.go` 的 `fakeDDL` 加：

```go
func (fakeDDL) NormalizeType(t string) string { return strings.ToLower(strings.TrimSpace(t)) }
```

（`fakes_test.go` 已 import `strings`；没有的话补上。）

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/db/... ./internal/ui/...`
Expected: PASS

- [ ] **Step 5: 写 `ApplyDDL` 的单元测试**

`internal/edit/apply_test.go` 末尾追加（`fakeQuerier` 已在该文件里）：

```go
func TestApplyDDLOnPostgresIsOneTransaction(t *testing.T) {
	q := &fakeQuerier{affected: map[string]int64{"ALTER a": 0, "ALTER b": 0}}
	if err := ApplyDDL(context.Background(), q, sqlx.Postgres, []string{"ALTER a", "ALTER b"}); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"BEGIN", "ALTER a", "ALTER b", "COMMIT"}, q.execs); diff != "" {
		t.Fatal(diff) // DDL reports no rows: ApplyDDL must not count them
	}
}

func TestApplyDDLOnPostgresRollsBack(t *testing.T) {
	boom := errors.New("boom")
	q := &fakeQuerier{fail: map[string]error{"ALTER b": boom}}
	err := ApplyDDL(context.Background(), q, sqlx.Postgres, []string{"ALTER a", "ALTER b", "ALTER c"})
	var de *DDLError
	if !errors.As(err, &de) || de.Index != 1 || !de.RolledBack || !errors.Is(err, boom) {
		t.Fatalf("err = %#v", err)
	}
	if diff := cmp.Diff([]string{"BEGIN", "ALTER a", "ALTER b", "ROLLBACK"}, q.execs); diff != "" {
		t.Fatal(diff)
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("message: %v", err)
	}
}

func TestApplyDDLOnMySQLStopsAtTheFirstFailure(t *testing.T) {
	boom := errors.New("boom")
	q := &fakeQuerier{fail: map[string]error{"ALTER b": boom}}
	err := ApplyDDL(context.Background(), q, sqlx.MySQL, []string{"ALTER a", "ALTER b", "ALTER c"})
	var de *DDLError
	if !errors.As(err, &de) || de.Index != 1 || de.RolledBack {
		t.Fatalf("err = %#v", err)
	}
	// MySQL commits each DDL statement: no transaction to begin or roll back.
	if diff := cmp.Diff([]string{"ALTER a", "ALTER b"}, q.execs); diff != "" {
		t.Fatal(diff)
	}
	if !strings.Contains(err.Error(), "the 1 before it were applied") {
		t.Fatalf("message: %v", err)
	}
	q = &fakeQuerier{fail: map[string]error{"ALTER a": boom}}
	if err := ApplyDDL(context.Background(), q, sqlx.MySQL, []string{"ALTER a"}); !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("message: %v", err)
	}
}
```

在 import 里补 `"strings"`（若未引入）。`rollback` 发的是 `"ROLLBACK"`（`internal/edit/apply.go:61`）。

- [ ] **Step 6: 运行，确认失败**

Run: `go test ./internal/edit -run TestApplyDDL`
Expected: FAIL，`undefined: ApplyDDL`

- [ ] **Step 7: 实现 `ApplyDDL`**

`internal/edit/apply.go` 末尾追加：

```go
// DDLError is a DDL statement that failed. RolledBack says whether the
// statements before it were undone (PostgreSQL) or stay applied (MySQL,
// whose DDL commits implicitly).
type DDLError struct {
	Index      int // 0-based
	SQL        string
	Err        error
	RolledBack bool
}

func (e *DDLError) Error() string {
	switch {
	case e.RolledBack:
		return fmt.Sprintf("statement %d: %v · rolled back, nothing was changed", e.Index+1, e.Err)
	case e.Index == 0:
		return fmt.Sprintf("statement 1: %v · nothing was changed", e.Err)
	}
	return fmt.Sprintf("statement %d: %v · the %d before it were applied", e.Index+1, e.Err, e.Index)
}

func (e *DDLError) Unwrap() error { return e.Err }

// ApplyDDL runs DDL statements on s (a db.Session). PostgreSQL runs them in
// one transaction; MySQL commits each DDL statement implicitly, so they run
// one by one and stop at the first failure. Unlike Apply it does not count
// rows: DDL reports none.
func ApplyDDL(ctx context.Context, s db.Querier, d sqlx.Dialect, stmts []string) error {
	tx := d == sqlx.Postgres
	if tx {
		if _, err := s.Exec(ctx, sqlx.BeginSQL(d)); err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
	}
	for i, q := range stmts {
		if _, err := s.Exec(ctx, q); err != nil {
			if tx {
				rollback(s)
			}
			return &DDLError{Index: i, SQL: q, Err: err, RolledBack: tx}
		}
	}
	if !tx {
		return nil
	}
	if _, err := s.Exec(ctx, "COMMIT"); err != nil {
		rollback(s)
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
```

- [ ] **Step 8: 运行，确认通过**

Run: `go test -race ./internal/edit`
Expected: PASS

- [ ] **Step 9: 写 contract 子测试**

`internal/db/dbtest/contract.go` 在 `"schema sync round-trips …"` 子测试之后、`RunContract` 结束之前追加：

```go
	t.Run("NormalizeType agrees with TableSchema", func(t *testing.T) {
		w := d.DDL()
		for col, want := range f.NormTypes {
			if got := w.NormalizeType(want); got != want {
				t.Errorf("%s: NormalizeType(%q) = %q", col, want, got)
			}
			if strings.ContainsAny(want, "'\"") {
				continue // enum values keep their case
			}
			if got := w.NormalizeType(strings.ToUpper(want)); got != want {
				t.Errorf("%s: NormalizeType(%q) = %q, want %q", col, strings.ToUpper(want), got, want)
			}
		}
	})

	t.Run("ApplyDDL rolls back PostgreSQL DDL and stops at the first failure", func(t *testing.T) {
		tbl := d.QualifiedName(ref("mod_tx"))
		for _, q := range []string{"DROP TABLE IF EXISTS " + tbl, "CREATE TABLE " + tbl + " (id int)"} {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		add := "ALTER TABLE " + tbl + " ADD COLUMN extra int"
		err = edit.ApplyDDL(ctx, s, sqlx.DialectOf(f.Driver), []string{add, add})
		var de *edit.DDLError
		if !errors.As(err, &de) || de.Index != 1 {
			t.Fatalf("err = %v", err)
		}
		cols, err := conn.Meta().Columns(ctx, ref("mod_tx"))
		if err != nil {
			t.Fatal(err)
		}
		kept := slices.ContainsFunc(cols, func(c db.Column) bool { return c.Name == "extra" })
		mysql := f.Driver == "mysql"
		if kept != mysql || de.RolledBack == mysql {
			t.Fatalf("extra kept = %v, rolled back = %v on %s", kept, de.RolledBack, f.Driver)
		}
		if _, err := s.Query(ctx, "SELECT 1"); err != nil {
			t.Fatalf("the session is unusable after the failure: %v", err)
		}
	})
```

- [ ] **Step 10: 跑集成测试**

Run: `make test-integration`
Expected: PASS（两个引擎）

- [ ] **Step 11: 检查并提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/db internal/edit internal/ui/fakes_test.go
git commit -m "feat(db): DDLWriter.NormalizeType 与 edit.ApplyDDL，PG 的 DDL 在一个事务里执行"
```

---

### Task 2: `schemadiff.DiffIndexEdits`——按来源配对索引

**Files:**
- Modify: `internal/schemadiff/diff.go`（抽出 `dropIndex`、`addIndex`）
- Create: `internal/schemadiff/edits.go`
- Create: `internal/schemadiff/edits_test.go`

**Interfaces:**
- Consumes: `sameIndex`（`diff.go`，已有）
- Produces（Task 4 使用）：

  ```go
  // origin[i] is the position in before of the index after[i] was edited
  // from, or -1 for a new index.
  func DiffIndexEdits(before, after []db.Index, origin []int) (drops, adds []db.SchemaChange)
  ```

- [ ] **Step 1: 写测试**

`internal/schemadiff/edits_test.go`：

```go
package schemadiff

import (
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestDiffIndexEdits(t *testing.T) {
	pk := db.Index{Name: "t_pkey", Columns: []string{"id"}, Unique: true, Primary: true}
	byName := db.Index{Name: "t_name", Columns: []string{"name"}}
	uniq := db.Index{Name: "t_email", Columns: []string{"email"}, Unique: true}
	before := []db.Index{pk, byName, uniq}
	with := func(ix db.Index, f func(*db.Index)) db.Index {
		ix.Columns = append([]string(nil), ix.Columns...)
		f(&ix)
		return ix
	}
	type change struct {
		kind db.ChangeKind
		name string
		risk db.Risk
	}
	for _, tc := range []struct {
		name   string
		after  []db.Index
		origin []int
		want   []change // drops, then adds
	}{
		{"unchanged", []db.Index{pk, byName, uniq}, []int{0, 1, 2}, nil},
		{"renamed only", []db.Index{pk, with(byName, func(i *db.Index) { i.Name = "t_full_name" }), uniq}, []int{0, 1, 2},
			[]change{{db.DropIndex, "t_name", 0}, {db.AddIndex, "t_full_name", 0}}},
		{"unique keeps its columns' uniqueness", []db.Index{pk, byName, with(uniq, func(i *db.Index) { i.Columns = []string{"email", "id"} })}, []int{0, 1, 2},
			[]change{{db.DropIndex, "t_email", 0}, {db.AddIndex, "t_email", db.Lossy}}},
		{"unique made plain", []db.Index{pk, byName, with(uniq, func(i *db.Index) { i.Unique = false })}, []int{0, 1, 2},
			[]change{{db.DropIndex, "t_email", db.Destructive}, {db.AddIndex, "t_email", 0}}},
		{"deleted", []db.Index{pk, byName}, []int{0, 1},
			[]change{{db.DropIndex, "t_email", db.Destructive}}},
		{"new", []db.Index{pk, byName, uniq, {Name: "t_qty_idx", Columns: []string{"qty"}}}, []int{0, 1, 2, -1},
			[]change{{db.AddIndex, "t_qty_idx", 0}}},
		{"edited back to the original", []db.Index{pk, with(byName, func(*db.Index) {}), uniq}, []int{0, 1, 2}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			drops, adds := DiffIndexEdits(before, tc.after, tc.origin)
			var got []change
			for _, c := range append(drops, adds...) {
				name := ""
				if c.OldIndex != nil {
					name = c.OldIndex.Name
				} else {
					name = c.NewIndex.Name
				}
				got = append(got, change{c.Kind, name, c.Risk})
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/schemadiff -run TestDiffIndexEdits`
Expected: FAIL，`undefined: DiffIndexEdits`

- [ ] **Step 3: 抽出 `dropIndex` / `addIndex`**

`internal/schemadiff/diff.go` 里，`diffIndexes` 末尾的两个闭包 `drop`、`add` 改成包级函数，`diffIndexes` 调用它们：

```go
// dropIndex is the change that drops old; replacement is the index that
// takes its place, if any. Dropping the only unique index on its columns
// is destructive.
func dropIndex(old, replacement *db.Index) db.SchemaChange {
	ch := db.SchemaChange{Kind: db.DropIndex, OldIndex: old}
	if old.Unique && (replacement == nil || !replacement.Unique) {
		ch.Risk = db.Destructive
	}
	return ch
}

// addIndex is the change that adds a copy of ix named name. A unique index
// fails on duplicate rows, so it is lossy.
func addIndex(ix *db.Index, name string) db.SchemaChange {
	cp := *ix
	cp.Name = name
	ch := db.SchemaChange{Kind: db.AddIndex, NewIndex: &cp}
	if cp.Unique {
		ch.Risk = db.Lossy
	}
	return ch
}
```

`diffIndexes` 末尾改成：

```go
	for _, p := range pairs {
		if !sameIndex(*p[0], *p[1]) {
			drops = append(drops, dropIndex(p[0], p[1]))
			adds = append(adds, addIndex(p[1], p[0].Name))
		}
	}
	for _, t := range tgt {
		if t != nil {
			drops = append(drops, dropIndex(t, nil))
		}
	}
	for _, s := range src {
		if s != nil {
			adds = append(adds, addIndex(s, indexName(s.Name, source.Ref.Name, target.Ref.Name)))
		}
	}
	return drops, adds
```

- [ ] **Step 4: 实现 `DiffIndexEdits`**

`internal/schemadiff/edits.go`：

```go
package schemadiff

import "github.com/shepherd-yun/datavim/internal/db"

// DiffIndexEdits lists the index changes of a table edited in place
// (Modify Table). Unlike DiffTable, which pairs indexes by name and then
// by definition, it pairs them by where they came from: origin[i] is the
// position in before of the index after[i] was edited from, or -1 for a
// new one. An index of before that nothing came from is dropped; one whose
// name or definition changed is dropped and added again, so renaming an
// index is a change too. Drops come in before's order, adds in after's.
// The changes point into before and after.
func DiffIndexEdits(before, after []db.Index, origin []int) (drops, adds []db.SchemaChange) {
	kept := make([]bool, len(before))
	replacement := map[int]*db.Index{} // position in before → the changed index
	for i := range after {
		o := origin[i]
		if o < 0 {
			continue
		}
		kept[o] = true
		if before[o].Name != after[i].Name || !sameIndex(before[o], after[i]) {
			replacement[o] = &after[i]
		}
	}
	for o := range before {
		if r, changed := replacement[o]; !kept[o] || changed {
			drops = append(drops, dropIndex(&before[o], r))
		}
	}
	for i := range after {
		if o := origin[i]; o < 0 || replacement[o] == &after[i] {
			adds = append(adds, addIndex(&after[i], after[i].Name))
		}
	}
	return drops, adds
}
```

- [ ] **Step 5: 运行，确认通过（含第 2 阶段的测试）**

Run: `go test -race ./internal/schemadiff`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/schemadiff
git commit -m "feat(schemadiff): DiffIndexEdits 按来源配对索引，只改名也算变更"
```

---

### Task 3: `internal/schemaedit`——暂存列与索引的编辑

**Files:**
- Create: `internal/schemaedit/table.go`
- Create: `internal/schemaedit/list.go`
- Create: `internal/schemaedit/table_test.go`

**Interfaces:**
- Consumes: `db.TableSchema`、`db.Column`、`db.Index`；`sqlx.Dialect`、`sqlx.QuoteIfNeeded`
- Produces（Task 4、5、7、8 使用）：

  ```go
  var ErrDeleted error // "marked for deletion · u undoes that"

  type State int
  const (Unchanged State = iota; Changed; Added; Deleted)

  type ColumnField int
  const (ColName ColumnField = iota; ColType; ColNullable; ColDefault; ColComment; ColumnFields)
  type IndexField int
  const (IdxName IndexField = iota; IdxKind; IdxColumns; IndexFields)

  type ColumnRow struct {
      Orig    *db.Column // nil for a new column
      Col     db.Column
      Deleted bool
  }
  func (r ColumnRow) State() State
  func (r ColumnRow) Changed(f ColumnField) bool
  func (r ColumnRow) Text(f ColumnField) string

  type IndexRow struct {
      Orig    *db.Index // nil for a new index
      Index   db.Index
      Deleted bool
      // unexported: origin int, base []string, touched, autoName bool
  }
  func (r IndexRow) State() State
  func (r IndexRow) Changed(f IndexField) bool
  func (r IndexRow) Text(f IndexField) string

  type Pos struct {
      Indexes bool // the Indexes section, else Columns
      Row     int
  }

  func New(s db.TableSchema, d sqlx.Dialect) *Table
  func (t *Table) Before() db.TableSchema
  func (t *Table) Columns() []ColumnRow
  func (t *Table) Indexes() []IndexRow
  func (t *Table) Changes() int
  func (t *Table) SetColumn(i int, f ColumnField, text string) error // not ColNullable
  func (t *Table) ToggleNullable(i int) error
  func (t *Table) AddColumn(after int) int  // after -1 puts it first; returns its row
  func (t *Table) DeleteColumn(i int) error
  func (t *Table) SetIndexName(i int, name string) error
  func (t *Table) SetIndexColumns(i int, text string) error
  func (t *Table) CycleIndexKind(i int) error
  func (t *Table) AddIndex(after int) int
  func (t *Table) DeleteIndex(i int) error
  func (t *Table) Undo() (Pos, bool)
  func (t *Table) Reset()
  ```

  约定：`Col.Default` 是能跟在 `DEFAULT` 后面的 SQL 文本，空输入表示"没有默认值"，空串默认值要写 `''`。索引的 `Columns` 与 `TableSchema` 读出的写法一致：MySQL 是原始列名，PG 是 `pg_get_indexdef` 的文本（需要时带双引号），所以 `New` 要知道方言。

- [ ] **Step 1: 写测试**

`internal/schemaedit/table_test.go`：

```go
package schemaedit

import (
	"errors"
	"slices"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

func users() db.TableSchema {
	zero := "0"
	return db.TableSchema{
		Ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"},
		Columns: []db.Column{
			{Name: "id", Type: "bigint", PrimaryKey: true, AutoIncrement: true, Position: 1},
			{Name: "name", Type: "varchar(100)", Nullable: true, Position: 2},
			{Name: "qty", Type: "int", Default: &zero, Position: 3},
			{Name: "total", Type: "int", Nullable: true, Generated: "qty * 2", Position: 4},
		},
		Indexes: []db.Index{
			{Name: "users_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Constraint: true},
			{Name: "users_name", Columns: []string{"name"}},
			{Name: "users_qty_key", Columns: []string{"qty"}, Unique: true},
		},
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetColumnMarksTheField(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, " full_name "))
	r := e.Columns()[1]
	if r.Col.Name != "full_name" || r.State() != Changed || !r.Changed(ColName) || r.Changed(ColType) {
		t.Fatalf("row = %+v", r)
	}
	if e.Changes() != 1 {
		t.Fatalf("Changes = %d", e.Changes())
	}
	if e.Before().Columns[1].Name != "name" {
		t.Fatal("the table as read is left alone")
	}
	must(t, e.SetColumn(1, ColName, "name"))
	if e.Columns()[1].State() != Unchanged || e.Changes() != 0 {
		t.Fatal("setting the old name back is no change")
	}
}

func TestTypeInAnotherCaseIsNoChange(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColType, "VARCHAR(100)"))
	if r := e.Columns()[1]; r.Col.Type != "varchar(100)" || r.State() != Unchanged {
		t.Fatalf("row = %+v", r)
	}
}

func TestEmptyFieldsAreRefused(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	if e.SetColumn(1, ColName, "  ") == nil || e.SetColumn(1, ColType, "") == nil {
		t.Fatal("a column needs a name and a type")
	}
	if e.SetIndexName(1, "") == nil || e.SetIndexColumns(1, " , ") == nil {
		t.Fatal("an index needs a name and columns")
	}
	if e.Changes() != 0 {
		t.Fatal("refused edits change nothing")
	}
}

func TestDefaultEmptyMeansNone(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(2, ColDefault, ""))
	if r := e.Columns()[2]; r.Col.Default != nil || !r.Changed(ColDefault) || r.Text(ColDefault) != "" {
		t.Fatalf("row = %+v", r)
	}
	must(t, e.SetColumn(1, ColDefault, "''"))
	if d := e.Columns()[1].Col.Default; d == nil || *d != "''" {
		t.Fatalf("default = %v", d)
	}
}

func TestGeneratedAndAutoIncrementColumnsRefuse(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	if err := e.SetColumn(3, ColType, "bigint"); err == nil {
		t.Fatal("a generated column's type is left to a console")
	}
	if err := e.SetColumn(3, ColDefault, "1"); err == nil {
		t.Fatal("a generated column has no default")
	}
	if err := e.SetColumn(0, ColDefault, "1"); err == nil {
		t.Fatal("an auto-increment column has no default")
	}
	must(t, e.SetColumn(3, ColComment, "twice qty")) // comments are fine
}

func TestToggleNullable(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.ToggleNullable(1))
	if r := e.Columns()[1]; r.Col.Nullable || r.Text(ColNullable) != "NOT NULL" || !r.Changed(ColNullable) {
		t.Fatalf("row = %+v", r)
	}
}

func TestAddAndDeleteColumns(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	at := e.AddColumn(1) // id, name, (new), qty, total
	if at != 2 || e.Columns()[2].State() != Added || !e.Columns()[2].Col.Nullable {
		t.Fatalf("added at %d: %+v", at, e.Columns()[2])
	}
	if first := e.AddColumn(-1); first != 0 || e.Columns()[0].State() != Added {
		t.Fatal("after -1 puts the column first")
	}
	must(t, e.DeleteColumn(0)) // a new column just goes
	if len(e.Columns()) != 5 || e.Columns()[2].State() != Added {
		t.Fatalf("columns = %+v", e.Columns())
	}
	must(t, e.DeleteColumn(1)) // name, read from the server, is marked
	if r := e.Columns()[1]; r.State() != Deleted {
		t.Fatalf("row = %+v", r)
	}
	if err := e.SetColumn(1, ColName, "x"); !errors.Is(err, ErrDeleted) {
		t.Fatalf("editing a deleted column: %v", err)
	}
	if err := e.DeleteColumn(1); err == nil {
		t.Fatal("deleting twice says so")
	}
}

func TestATableKeepsOneColumn(t *testing.T) {
	s := users()
	s.Columns, s.Indexes = s.Columns[:1], nil
	e := New(s, sqlx.MySQL)
	if err := e.DeleteColumn(0); err == nil {
		t.Fatal("the last column cannot go")
	}
}

func TestUndoRestoresAndReportsPosition(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, "full_name"))
	at := e.AddIndex(2)
	must(t, e.SetIndexColumns(at, "qty"))
	pos, ok := e.Undo()
	if !ok || pos != (Pos{Indexes: true, Row: at}) || len(e.Indexes()[at].Index.Columns) != 0 {
		t.Fatalf("undo = %+v %v, index %+v", pos, ok, e.Indexes()[at])
	}
	if pos, _ = e.Undo(); pos != (Pos{Indexes: true, Row: 2}) || len(e.Indexes()) != 3 { // the row the new index went below
		t.Fatalf("undo = %+v, %d indexes", pos, len(e.Indexes()))
	}
	if pos, _ = e.Undo(); pos != (Pos{Row: 1}) || e.Columns()[1].Col.Name != "name" {
		t.Fatalf("undo = %+v", pos)
	}
	if _, ok := e.Undo(); ok {
		t.Fatal("nothing left to undo")
	}
	must(t, e.ToggleNullable(1))
	e.Reset()
	if e.Changes() != 0 {
		t.Fatal("Reset drops every change")
	}
	if _, ok := e.Undo(); ok {
		t.Fatal("Reset empties the undo history")
	}
}

func TestRenamedColumnFollowsIntoIndexes(t *testing.T) {
	s := users()
	s.Columns[1].Name = "Odd Col"
	s.Indexes[1].Columns = []string{`"Odd Col"`}
	e := New(s, sqlx.Postgres)
	must(t, e.SetColumn(1, ColName, "full name"))
	r := e.Indexes()[1]
	if !slices.Equal(r.Index.Columns, []string{`"full name"`}) || r.State() != Unchanged {
		t.Fatalf("index = %+v, state %v", r.Index, r.State())
	}
	must(t, e.SetIndexColumns(1, "full name, qty"))
	if r := e.Indexes()[1]; !slices.Equal(r.Index.Columns, []string{`"full name"`, "qty"}) || !r.Changed(IdxColumns) {
		t.Fatalf("index = %+v", r.Index)
	}
	must(t, e.SetColumn(1, ColName, "fn"))
	if r := e.Indexes()[1]; !slices.Equal(r.Index.Columns, []string{`"full name"`, "qty"}) {
		t.Fatalf("columns typed for an index stay as typed: %+v", r.Index)
	}
}

func TestNewIndexNamesFollowColumnsAndKind(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	at := e.AddIndex(2)
	must(t, e.SetIndexColumns(at, "name, qty"))
	if got := e.Indexes()[at].Index.Name; got != "users_name_qty_idx" {
		t.Fatalf("name = %q", got)
	}
	must(t, e.CycleIndexKind(at)) // UNIQUE
	if r := e.Indexes()[at]; r.Index.Name != "users_name_qty_key" || !r.Index.Unique || r.Text(IdxKind) != "UNIQUE" {
		t.Fatalf("index = %+v", r.Index)
	}
	must(t, e.CycleIndexKind(at)) // PRIMARY is taken by users_pkey: back to INDEX
	if r := e.Indexes()[at]; r.Index.Unique || r.Index.Primary || r.Text(IdxKind) != "INDEX" {
		t.Fatalf("index = %+v", r.Index)
	}
	must(t, e.SetIndexName(at, "by_name"))
	must(t, e.SetIndexColumns(at, "qty"))
	if got := e.Indexes()[at].Index.Name; got != "by_name" {
		t.Fatalf("a typed name stays: %q", got)
	}
	must(t, e.DeleteIndex(0)) // users_pkey
	must(t, e.CycleIndexKind(at))
	must(t, e.CycleIndexKind(at))
	if r := e.Indexes()[at]; !r.Index.Primary || r.Text(IdxKind) != "PRIMARY" {
		t.Fatalf("with the primary key deleted, PRIMARY is free: %+v", r.Index)
	}
}

func TestSplitList(t *testing.T) {
	for in, want := range map[string][]string{
		"a, b":                        {"a", "b"},
		" a ,, b ":                    {"a", "b"},
		"lower(a), coalesce(b, c)":    {"lower(a)", "coalesce(b, c)"},
		`"x,y", z`:                    {`"x,y"`, "z"},
		"`a,b`, 'c,d'":                {"`a,b`", "'c,d'"},
		"":                            nil,
	} {
		if got := splitList(in); !slices.Equal(got, want) {
			t.Errorf("splitList(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/schemaedit`
Expected: FAIL（包不存在 / 未定义）

- [ ] **Step 3: 实现 `list.go`**

`internal/schemaedit/list.go`：

```go
package schemaedit

import (
	"strings"
	"unicode"
)

// splitList splits a typed column list at the commas outside parentheses
// and quotes, trimming the parts and dropping empty ones.
func splitList(s string) []string {
	var out []string
	depth, quote, start := 0, rune(0), 0
	add := func(end int) {
		if p := strings.TrimSpace(s[start:end]); p != "" {
			out = append(out, p)
		}
	}
	for i, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"' || r == '`':
			quote = r
		case r == '(':
			depth++
		case r == ')':
			depth = max(depth-1, 0)
		case r == ',' && depth == 0:
			add(i)
			start = i + 1
		}
	}
	add(len(s))
	return out
}

// unquote is the name an index column entry spells: "Odd Col" for
// `"Odd Col"`, the entry itself when it is not quoted.
func unquote(s string) string {
	if len(s) >= 2 {
		if q := s[0]; (q == '"' || q == '`') && s[len(s)-1] == q {
			return strings.ReplaceAll(s[1:len(s)-1], string(q)+string(q), string(q))
		}
	}
	return s
}

// isExpression reports whether an index entry is an expression, such as
// lower(email) or MySQL's "(expression)", rather than a column.
func isExpression(entry string) bool { return strings.ContainsRune(entry, '(') }

// nameWords is entries' column names, for making up an index name.
func nameWords(entries []string) []string {
	var out []string
	for _, e := range entries {
		if isExpression(e) {
			continue
		}
		w := strings.Map(func(r rune) rune {
			if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return '_'
		}, unquote(e))
		out = append(out, w)
	}
	return out
}
```

- [ ] **Step 4: 实现 `table.go`**

`internal/schemaedit/table.go`：

```go
// Package schemaedit stages changes to one table's columns and indexes, as
// the structure tab's Modify Table makes them, and plans them as
// db.SchemaChanges (with package schemadiff) for a db.DDLWriter to write.
// It knows nothing about terminals.
package schemaedit

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// ErrDeleted is an edit of a column or index marked for deletion.
var ErrDeleted = errors.New("marked for deletion · u undoes that")

// State is what a row's staged edits do to it.
type State int

const (
	Unchanged State = iota
	Changed
	Added
	Deleted
)

// ColumnField is one editable field of a column, in the order the
// structure view shows them.
type ColumnField int

const (
	ColName ColumnField = iota
	ColType
	ColNullable
	ColDefault
	ColComment
	ColumnFields // how many there are
)

// IndexField is one editable field of an index.
type IndexField int

const (
	IdxName IndexField = iota
	IdxKind
	IdxColumns
	IndexFields
)

// ColumnRow is a column with its staged edits.
type ColumnRow struct {
	Orig    *db.Column // as read; nil for a new column
	Col     db.Column  // as it should be
	Deleted bool
}

func sameDefault(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Changed reports whether f differs from the column as read; false for a
// new column, which is new as a whole.
func (r ColumnRow) Changed(f ColumnField) bool {
	if r.Orig == nil {
		return false
	}
	o, c := r.Orig, r.Col
	switch f {
	case ColName:
		return c.Name != o.Name
	case ColType:
		return c.Type != o.Type
	case ColNullable:
		return c.Nullable != o.Nullable
	case ColDefault:
		return !sameDefault(c.Default, o.Default)
	case ColComment:
		return c.Comment != o.Comment
	}
	return false
}

func (r ColumnRow) State() State {
	switch {
	case r.Orig == nil:
		return Added
	case r.Deleted:
		return Deleted
	}
	for f := range ColumnFields {
		if r.Changed(f) {
			return Changed
		}
	}
	return Unchanged
}

// Text is f as the structure view shows it.
func (r ColumnRow) Text(f ColumnField) string {
	switch f {
	case ColName:
		return r.Col.Name
	case ColType:
		return r.Col.Type
	case ColNullable:
		if r.Col.Nullable {
			return "NULL"
		}
		return "NOT NULL"
	case ColDefault:
		if r.Col.Default == nil {
			return ""
		}
		return *r.Col.Default
	case ColComment:
		return r.Col.Comment
	}
	return ""
}

// IndexRow is an index with its staged edits.
type IndexRow struct {
	Orig    *db.Index // as read; nil for a new index
	Index   db.Index  // as it should be
	Deleted bool

	origin   int      // position in Before().Indexes; -1 for a new index
	base     []string // Orig's columns under the staged column renames
	touched  bool     // the columns were typed, so renames leave them alone
	autoName bool     // the name was made up and follows columns and kind
}

func (r IndexRow) Changed(f IndexField) bool {
	if r.Orig == nil {
		return false
	}
	switch f {
	case IdxName:
		return r.Index.Name != r.Orig.Name
	case IdxKind:
		return r.Index.Unique != r.Orig.Unique || r.Index.Primary != r.Orig.Primary
	case IdxColumns:
		return !slices.Equal(r.Index.Columns, r.base)
	}
	return false
}

func (r IndexRow) State() State {
	switch {
	case r.Orig == nil:
		return Added
	case r.Deleted:
		return Deleted
	}
	for f := range IndexFields {
		if r.Changed(f) {
			return Changed
		}
	}
	return Unchanged
}

func (r IndexRow) Text(f IndexField) string {
	switch f {
	case IdxName:
		return r.Index.Name
	case IdxKind:
		switch {
		case r.Index.Primary:
			return "PRIMARY"
		case r.Index.Unique:
			return "UNIQUE"
		}
		return "INDEX"
	case IdxColumns:
		return strings.Join(r.Index.Columns, ", ")
	}
	return ""
}

// Pos is where an edit was made, for putting the cursor back on undo.
type Pos struct {
	Indexes bool // the Indexes section, else Columns
	Row     int
}

type snapshot struct {
	cols []ColumnRow
	idx  []IndexRow
	pos  Pos
}

// Table is one table being modified.
type Table struct {
	before  db.TableSchema
	quote   func(name string) string // a column name as index entries spell it
	cols    []ColumnRow
	idx     []IndexRow
	history []snapshot
}

// New starts modifying s, a table as MetaReader.TableSchema read it on an
// engine of dialect d.
func New(s db.TableSchema, d sqlx.Dialect) *Table {
	t := &Table{before: s, quote: func(n string) string { return n }}
	if d == sqlx.Postgres {
		// PostgreSQL's index entries are pg_get_indexdef text, quoted where needed.
		t.quote = func(n string) string { return sqlx.QuoteIfNeeded(n, sqlx.Postgres) }
	}
	t.Reset()
	return t
}

func (t *Table) Before() db.TableSchema { return t.before }
func (t *Table) Columns() []ColumnRow   { return t.cols }
func (t *Table) Indexes() []IndexRow    { return t.idx }

// Reset drops every staged edit and the undo history.
func (t *Table) Reset() {
	t.cols = make([]ColumnRow, len(t.before.Columns))
	for i := range t.before.Columns {
		t.cols[i] = ColumnRow{Orig: &t.before.Columns[i], Col: t.before.Columns[i]}
	}
	t.idx = make([]IndexRow, len(t.before.Indexes))
	for i := range t.before.Indexes {
		ix := t.before.Indexes[i]
		ix.Columns = slices.Clone(ix.Columns)
		t.idx[i] = IndexRow{Orig: &t.before.Indexes[i], Index: ix, origin: i, base: slices.Clone(ix.Columns)}
	}
	t.history = nil
}

// Changes counts the columns and indexes with staged edits.
func (t *Table) Changes() int {
	n := 0
	for _, r := range t.cols {
		if r.State() != Unchanged {
			n++
		}
	}
	for _, r := range t.idx {
		if r.State() != Unchanged {
			n++
		}
	}
	return n
}

// push saves the rows before an edit at pos.
func (t *Table) push(pos Pos) {
	idx := slices.Clone(t.idx)
	for i := range idx {
		idx[i].Index.Columns = slices.Clone(idx[i].Index.Columns)
		idx[i].base = slices.Clone(idx[i].base)
	}
	t.history = append(t.history, snapshot{cols: slices.Clone(t.cols), idx: idx, pos: pos})
}

// Undo takes back the last edit and says where it was made.
func (t *Table) Undo() (Pos, bool) {
	if len(t.history) == 0 {
		return Pos{}, false
	}
	s := t.history[len(t.history)-1]
	t.history = t.history[:len(t.history)-1]
	t.cols, t.idx = s.cols, s.idx
	return s.pos, true
}

func (t *Table) liveColumns() int {
	n := 0
	for _, r := range t.cols {
		if !r.Deleted {
			n++
		}
	}
	return n
}

// SetColumn sets the name, type, default or comment of column i from
// typed text. A type typed in another case than the one read is no change.
func (t *Table) SetColumn(i int, f ColumnField, text string) error {
	r := t.cols[i]
	if r.Deleted {
		return ErrDeleted
	}
	c := r.Col
	switch f {
	case ColName:
		name := strings.TrimSpace(text)
		if name == "" {
			return errors.New("a column needs a name")
		}
		c.Name = name
	case ColType:
		typ := strings.TrimSpace(text)
		switch {
		case typ == "":
			return errors.New("a column needs a type")
		case c.Generated != "":
			return fmt.Errorf("%s is a generated column: change it in a console", c.Name)
		case r.Orig != nil && strings.EqualFold(typ, r.Orig.Type):
			typ = r.Orig.Type
		}
		c.Type = typ
	case ColDefault:
		def := strings.TrimSpace(text)
		switch {
		case c.Generated != "":
			return fmt.Errorf("%s is a generated column: it has no default", c.Name)
		case c.AutoIncrement:
			return fmt.Errorf("%s is auto-increment: it has no default", c.Name)
		case def == "":
			c.Default = nil
		case r.Orig != nil && r.Orig.Default != nil && *r.Orig.Default == def:
			c.Default = r.Orig.Default
		default:
			c.Default = &def
		}
	case ColComment:
		c.Comment = text
	default:
		return fmt.Errorf("schemaedit: SetColumn cannot set field %d", f)
	}
	if (ColumnRow{Col: c}).Text(f) == r.Text(f) {
		return nil
	}
	t.push(Pos{Row: i})
	t.cols[i].Col = c
	t.syncIndexes()
	return nil
}

// ToggleNullable switches column i between NULL and NOT NULL.
func (t *Table) ToggleNullable(i int) error {
	if t.cols[i].Deleted {
		return ErrDeleted
	}
	t.push(Pos{Row: i})
	t.cols[i].Col.Nullable = !t.cols[i].Col.Nullable
	return nil
}

// AddColumn adds a nullable column with no name and type below row after
// (-1 puts it first) and returns its row.
func (t *Table) AddColumn(after int) int {
	at := after + 1
	t.push(Pos{Row: max(after, 0)})
	t.cols = slices.Insert(t.cols, at, ColumnRow{Col: db.Column{Nullable: true}})
	return at
}

// DeleteColumn drops a new column, or marks one read from the server.
func (t *Table) DeleteColumn(i int) error {
	r := t.cols[i]
	switch {
	case r.Deleted:
		return errors.New("already marked for deletion · u undoes that")
	case t.liveColumns() == 1:
		return errors.New("a table needs at least one column")
	}
	t.push(Pos{Row: i})
	if r.Orig == nil {
		t.cols = slices.Delete(t.cols, i, i+1)
	} else {
		t.cols[i].Deleted = true
	}
	t.syncIndexes()
	return nil
}

// renames maps the old names of the live columns read from the server to
// their staged names.
func (t *Table) renames() map[string]string {
	out := map[string]string{}
	for _, r := range t.cols {
		if r.Orig != nil && !r.Deleted && r.Col.Name != r.Orig.Name {
			out[r.Orig.Name] = r.Col.Name
		}
	}
	return out
}

// syncIndexes carries column renames into the indexes read from the
// server; the columns of an index whose columns were typed stay as typed.
func (t *Table) syncIndexes() {
	renames := t.renames()
	for i := range t.idx {
		r := &t.idx[i]
		if r.Orig == nil {
			continue
		}
		r.base = make([]string, len(r.Orig.Columns))
		for j, e := range r.Orig.Columns {
			r.base[j] = e
			if to, ok := renames[unquote(e)]; ok && !isExpression(e) {
				r.base[j] = t.quote(to)
			}
		}
		if !r.touched {
			r.Index.Columns = slices.Clone(r.base)
		}
	}
}

// SetIndexName names index i; the name no longer follows its columns.
func (t *Table) SetIndexName(i int, name string) error {
	if t.idx[i].Deleted {
		return ErrDeleted
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("an index needs a name")
	}
	t.push(Pos{Indexes: true, Row: i})
	t.idx[i].Index.Name, t.idx[i].autoName = name, false
	return nil
}

// SetIndexColumns sets index i's columns from a comma-separated list;
// column names are spelled the way the engine's index entries are.
func (t *Table) SetIndexColumns(i int, text string) error {
	if t.idx[i].Deleted {
		return ErrDeleted
	}
	entries := splitList(text)
	if len(entries) == 0 {
		return errors.New("an index needs at least one column")
	}
	for j, e := range entries {
		for _, r := range t.cols {
			if !r.Deleted && r.Col.Name == e {
				entries[j] = t.quote(e)
			}
		}
	}
	t.push(Pos{Indexes: true, Row: i})
	r := &t.idx[i]
	r.Index.Columns, r.touched = entries, true
	t.rename(r)
	return nil
}

// CycleIndexKind turns index i from INDEX to UNIQUE to PRIMARY (when the
// table has no other primary key) and back to INDEX.
func (t *Table) CycleIndexKind(i int) error {
	if t.idx[i].Deleted {
		return ErrDeleted
	}
	t.push(Pos{Indexes: true, Row: i})
	ix := &t.idx[i].Index
	switch {
	case ix.Primary:
		ix.Primary, ix.Unique = false, false
	case ix.Unique && !t.otherPrimary(i):
		ix.Primary = true
	case ix.Unique:
		ix.Unique = false
	default:
		ix.Unique = true
	}
	t.rename(&t.idx[i])
	return nil
}

func (t *Table) otherPrimary(i int) bool {
	for j, r := range t.idx {
		if j != i && !r.Deleted && r.Index.Primary {
			return true
		}
	}
	return false
}

// rename makes up r's name from the table, its columns and its kind, as
// long as the name was never typed: users_pkey, users_email_key,
// users_name_qty_idx.
func (t *Table) rename(r *IndexRow) {
	if !r.autoName {
		return
	}
	words := append([]string{t.before.Ref.Name}, nameWords(r.Index.Columns)...)
	switch {
	case r.Index.Primary:
		words = []string{t.before.Ref.Name, "pkey"}
	case r.Index.Unique:
		words = append(words, "key")
	default:
		words = append(words, "idx")
	}
	r.Index.Name = strings.Join(words, "_")
}

// AddIndex adds an index with no columns below row after (-1 puts it
// first) and returns its row.
func (t *Table) AddIndex(after int) int {
	at := after + 1
	t.push(Pos{Indexes: true, Row: max(after, 0)})
	t.idx = slices.Insert(t.idx, at, IndexRow{origin: -1, autoName: true})
	return at
}

// DeleteIndex drops a new index, or marks one read from the server.
func (t *Table) DeleteIndex(i int) error {
	r := t.idx[i]
	if r.Deleted {
		return errors.New("already marked for deletion · u undoes that")
	}
	t.push(Pos{Indexes: true, Row: i})
	if r.Orig == nil {
		t.idx = slices.Delete(t.idx, i, i+1)
	} else {
		t.idx[i].Deleted = true
	}
	return nil
}
```

- [ ] **Step 5: 运行，确认通过**

Run: `go test -race ./internal/schemaedit`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/schemaedit
git commit -m "feat(schemaedit): 暂存列与索引的编辑，支持撤销，列改名带进索引"
```

---

### Task 4: `schemaedit.Plan`——检查暂存的编辑并算出变更

**Files:**
- Create: `internal/schemaedit/plan.go`
- Create: `internal/schemaedit/plan_test.go`

**Interfaces:**
- Consumes: Task 2 `schemadiff.DiffIndexEdits`；第 2 阶段 `schemadiff.DiffTable`；Task 3 的 `Table`
- Produces（Task 5、9 使用）：

  ```go
  // Plan checks the staged edits and lists the changes that apply them,
  // ordered for DDLWriter.AlterTable: index drops, column renames, drops,
  // adds and alters, then index adds. normType is DDLWriter.NormalizeType.
  func (t *Table) Plan(normType func(string) string) ([]db.SchemaChange, error)
  ```

- [ ] **Step 1: 写测试**

`internal/schemaedit/plan_test.go`：

```go
package schemaedit

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// norm stands in for DDLWriter.NormalizeType: PostgreSQL spells INTEGER int.
func norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "integer" {
		return "int"
	}
	return s
}

// kinds is "Kind name" per change: the column's new (else old) name or the index's.
func kinds(cs []db.SchemaChange) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		name := ""
		switch {
		case c.New != nil:
			name = c.New.Name
		case c.Old != nil:
			name = c.Old.Name
		case c.NewIndex != nil:
			name = c.NewIndex.Name
		case c.OldIndex != nil:
			name = c.OldIndex.Name
		}
		out[i] = c.Kind.String() + " " + name
	}
	return out
}

func plan(t *testing.T, e *Table) []db.SchemaChange {
	t.Helper()
	cs, err := e.Plan(norm)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func wantKinds(t *testing.T, cs []db.SchemaChange, want ...string) {
	t.Helper()
	if got := kinds(cs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("changes = %q, want %q", got, want)
	}
}

func TestPlanNothing(t *testing.T) {
	wantKinds(t, plan(t, New(users(), sqlx.MySQL)))
}

func TestPlanRenameAndRetype(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, "full_name"))
	must(t, e.SetColumn(1, ColType, "VARCHAR(200)"))
	cs := plan(t, e)
	wantKinds(t, cs, "RenameColumn full_name", "AlterColumn full_name")
	if cs[1].New.Type != "varchar(200)" || cs[1].Attrs != db.AttrType {
		t.Fatalf("alter = %+v", cs[1])
	}
}

func TestPlanRenameCarriesIntoIndexes(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, "full_name"))
	wantKinds(t, plan(t, e), "RenameColumn full_name") // users_name follows on its own

	must(t, e.SetIndexColumns(1, "full_name, qty"))
	cs := plan(t, e)
	wantKinds(t, cs, "DropIndex users_name", "RenameColumn full_name", "AddIndex users_name")
	if got := strings.Join(cs[2].NewIndex.Columns, ","); got != "full_name,qty" {
		t.Fatalf("index columns = %q", got)
	}
}

func TestPlanNormalizesTypedTypes(t *testing.T) {
	e := New(users(), sqlx.Postgres)
	must(t, e.SetColumn(2, ColType, "integer"))
	wantKinds(t, plan(t, e)) // integer is int
	at := e.AddColumn(3)
	must(t, e.SetColumn(at, ColName, "n"))
	must(t, e.SetColumn(at, ColType, "INTEGER"))
	cs := plan(t, e)
	wantKinds(t, cs, "AddColumn n")
	if cs[0].New.Type != "int" || cs[0].After != "total" {
		t.Fatalf("add = %+v", cs[0])
	}
}

func TestPlanIndexRenameIsDropAndAdd(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetIndexName(1, "users_by_name"))
	wantKinds(t, plan(t, e), "DropIndex users_name", "AddIndex users_by_name")
	must(t, e.SetIndexName(1, "users_name"))
	wantKinds(t, plan(t, e))
}

func TestPlanRefusesDeletedColumnInIndex(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.DeleteColumn(1))
	_, err := e.Plan(norm)
	if err == nil || !strings.Contains(err.Error(), "users_name uses column name") {
		t.Fatalf("err = %v", err)
	}
	must(t, e.DeleteIndex(1))
	cs := plan(t, e)
	wantKinds(t, cs, "DropIndex users_name", "DropColumn name")
	if cs[1].Risk != db.Destructive {
		t.Fatalf("dropping a column is destructive: %+v", cs[1])
	}
}

func TestPlanAddColumnsAfter(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.SetColumn(1, ColName, "full_name"))
	a := e.AddColumn(1)
	must(t, e.SetColumn(a, ColName, "email"))
	must(t, e.SetColumn(a, ColType, "varchar(80)"))
	b := e.AddColumn(a)
	must(t, e.SetColumn(b, ColName, "phone"))
	must(t, e.SetColumn(b, ColType, "varchar(20)"))
	cs := plan(t, e)
	wantKinds(t, cs, "RenameColumn full_name", "AddColumn email", "AddColumn phone")
	if cs[1].After != "full_name" || cs[2].After != "email" {
		t.Fatalf("after = %q, %q", cs[1].After, cs[2].After)
	}
}

func TestPlanRisks(t *testing.T) {
	e := New(users(), sqlx.MySQL)
	must(t, e.ToggleNullable(1))                       // NULL → NOT NULL
	must(t, e.SetColumn(1, ColType, "varchar(50)"))   // narrower
	cs := plan(t, e)
	if len(cs) != 1 || cs[0].Risk != db.Lossy {
		t.Fatalf("changes = %+v", cs)
	}
}

func TestPlanChecks(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(e *Table) error
		want string
	}{
		{"new column without a name", func(e *Table) error { e.AddColumn(3); return nil }, "column 5 needs a name"},
		{"new column without a type", func(e *Table) error {
			return e.SetColumn(e.AddColumn(3), ColName, "x")
		}, "column x needs a type"},
		{"two columns with one name", func(e *Table) error { return e.SetColumn(2, ColName, "name") }, "two columns are called name"},
		{"new index without columns", func(e *Table) error { e.AddIndex(2); return nil }, "index 4 needs columns"},
		{"index on a missing column", func(e *Table) error { return e.SetIndexColumns(1, "nope") }, "index users_name: no column nope"},
		{"two indexes with one name", func(e *Table) error { return e.SetIndexName(2, "users_name") }, "two indexes are called users_name"},
		{"swapping names", func(e *Table) error {
			if err := e.SetColumn(1, ColName, "tmp"); err != nil {
				return err
			}
			return e.SetColumn(2, ColName, "name")
		}, "qty cannot take the name name of another column in the same submit"},
		{"reusing a deleted column's name", func(e *Table) error {
			if err := e.DeleteIndex(1); err != nil {
				return err
			}
			if err := e.DeleteColumn(1); err != nil {
				return err
			}
			return e.SetColumn(2, ColName, "name")
		}, "qty cannot take the name name of another column in the same submit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := New(users(), sqlx.MySQL)
			must(t, tc.edit(e))
			if _, err := e.Plan(norm); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/schemaedit -run TestPlan`
Expected: FAIL，`e.Plan undefined`

- [ ] **Step 3: 实现 `plan.go`**

`internal/schemaedit/plan.go`：

```go
package schemaedit

import (
	"fmt"
	"slices"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/schemadiff"
)

// check refuses edits no ALTER can apply as staged.
func (t *Table) check() error {
	gone := map[string]bool{} // old names of the columns renamed or dropped
	for _, r := range t.cols {
		if r.Orig != nil && (r.Deleted || r.Col.Name != r.Orig.Name) {
			gone[r.Orig.Name] = true
		}
	}
	live := map[string]bool{}
	for i, r := range t.cols {
		if r.Deleted {
			continue
		}
		name := r.Col.Name
		switch {
		case name == "":
			return fmt.Errorf("column %d needs a name", i+1)
		case r.Orig != nil && name != r.Orig.Name && gone[name]:
			// PostgreSQL renames, then drops, one statement at a time: the
			// old name is still taken when this rename runs.
			return fmt.Errorf("%s cannot take the name %s of another column in the same submit · submit in two steps", r.Orig.Name, name)
		case live[name]:
			return fmt.Errorf("two columns are called %s", name)
		case r.Col.Type == "":
			return fmt.Errorf("column %s needs a type", name)
		}
		live[name] = true
	}
	names, primaries := map[string]bool{}, 0
	for i, r := range t.idx {
		if r.Deleted {
			continue
		}
		ix := r.Index
		switch {
		case len(ix.Columns) == 0:
			return fmt.Errorf("index %d needs columns", i+1)
		case ix.Name == "":
			return fmt.Errorf("index %d needs a name", i+1)
		case names[ix.Name]:
			return fmt.Errorf("two indexes are called %s", ix.Name)
		}
		names[ix.Name] = true
		if ix.Primary {
			primaries++
		}
		for _, e := range ix.Columns {
			n := unquote(e)
			switch {
			case isExpression(e), live[n]:
			case t.dropped(n):
				return fmt.Errorf("index %s uses column %s, which is marked for deletion · delete or change the index too", ix.Name, n)
			default:
				return fmt.Errorf("index %s: no column %s", ix.Name, n)
			}
		}
	}
	if primaries > 1 {
		return fmt.Errorf("a table has one primary key")
	}
	return nil
}

// dropped reports whether the column read as name is marked for deletion.
func (t *Table) dropped(name string) bool {
	for _, r := range t.cols {
		if r.Orig != nil && r.Deleted && r.Orig.Name == name {
			return true
		}
	}
	return false
}

// Plan checks the staged edits and lists the changes that apply them,
// ordered for DDLWriter.AlterTable: index drops, column renames, drops,
// adds and alters, then index adds. Columns go through
// schemadiff.DiffTable; indexes pair by the row they were edited from
// (schemadiff.DiffIndexEdits). normType is DDLWriter.NormalizeType: it
// spells typed types the way TableSchema reads them.
func (t *Table) Plan(normType func(string) string) ([]db.SchemaChange, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	before := t.before
	before.Indexes, before.ForeignKeys = nil, nil
	after := before
	after.Columns = nil
	renames := t.renames()
	for _, r := range t.cols {
		if r.Deleted {
			continue
		}
		c := r.Col
		if r.Orig == nil || r.Changed(ColType) {
			c.Type = normType(c.Type)
		}
		after.Columns = append(after.Columns, c)
	}
	columns := schemadiff.DiffTable(before, after, renames)

	// The indexes as read, under the column renames: an index that only
	// follows a renamed column is unchanged.
	base := slices.Clone(t.before.Indexes)
	var edited []db.Index
	var origin []int
	for _, r := range t.idx {
		if r.origin >= 0 {
			base[r.origin].Columns = r.base
		}
		if !r.Deleted {
			edited = append(edited, r.Index)
			origin = append(origin, r.origin)
		}
	}
	drops, adds := schemadiff.DiffIndexEdits(base, edited, origin)
	return slices.Concat(drops, columns, adds), nil
}
```

`check` 里的 swap 规则（Review Focus 1）：PG 按 `RENAME COLUMN`、`DROP COLUMN`、`ADD COLUMN` 的顺序逐条执行，所以现有列不能在同一次提交里接过另一个被改名或被删除的列的旧名字；新增列不受限（加列在改名和删列之后）。这条判断放在重名判断之前，否则交换名字会先被报成"two columns are called …"。

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/schemaedit`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/schemaedit
git commit -m "feat(schemaedit): Plan 检查暂存的编辑并算出 ALTER 所需的变更"
```

---

### Task 5: contract 往返测试——Modify Table 在两个引擎上能执行

**Files:**
- Modify: `internal/db/dbtest/contract.go`

**Interfaces:**
- Consumes: Task 1 `edit.ApplyDDL`、`DDLWriter.NormalizeType`；Task 3/4 `schemaedit`；第 2 阶段 `DDLWriter.CreateTable` / `AlterTable`

- [ ] **Step 1: 写子测试**

`internal/db/dbtest/contract.go` 的 import 加 `"github.com/shepherd-yun/datavim/internal/schemaedit"`，在 Task 1 的两个子测试之后追加：

```go
	t.Run("Modify Table edits round-trip through schemaedit and AlterTable", func(t *testing.T) {
		w := d.DDL()
		dialect := sqlx.DialectOf(f.Driver)
		tbl := ref("mod_t")
		if _, err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+d.QualifiedName(tbl)); err != nil {
			t.Fatal(err)
		}
		create, _ := w.CreateTable(db.TableSchema{
			Ref: tbl,
			Columns: []db.Column{
				{Name: "id", Type: "bigint"},
				{Name: "name", Type: "varchar(50)", Nullable: true},
				{Name: "qty", Type: "int", Nullable: true},
				{Name: "legacy", Type: "int", Nullable: true},
			},
			Indexes: []db.Index{
				{Name: "mod_t_pkey", Columns: []string{"id"}, Unique: true, Primary: true},
				{Name: "mod_t_name", Columns: []string{"name"}},
				{Name: "mod_t_qty", Columns: []string{"qty"}},
			},
		})
		for _, q := range create {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		schema := func() db.TableSchema {
			t.Helper()
			s, err := conn.Meta().TableSchema(ctx, tbl)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		ed := schemaedit.New(schema(), dialect)
		col := func(name string) int {
			t.Helper()
			for i, r := range ed.Columns() {
				if r.Col.Name == name {
					return i
				}
			}
			t.Fatalf("no column %s", name)
			return -1
		}
		ix := func(name string) int {
			t.Helper()
			for i, r := range ed.Indexes() {
				if r.Index.Name == name {
					return i
				}
			}
			t.Fatalf("no index %s", name)
			return -1
		}
		must(ed.SetColumn(col("name"), schemaedit.ColName, "full_name"))
		must(ed.SetColumn(col("full_name"), schemaedit.ColType, "VARCHAR(100)"))
		at := ed.AddColumn(col("full_name")) // AFTER the renamed column, in the same ALTER
		must(ed.SetColumn(at, schemaedit.ColName, "email"))
		must(ed.SetColumn(at, schemaedit.ColType, "varchar(80)"))
		at = ed.AddColumn(at) // AFTER a column added by the same ALTER
		must(ed.SetColumn(at, schemaedit.ColName, "phone"))
		must(ed.SetColumn(at, schemaedit.ColType, "varchar(20)"))
		must(ed.DeleteColumn(col("legacy")))
		must(ed.ToggleNullable(col("qty")))
		must(ed.SetColumn(col("qty"), schemaedit.ColDefault, "0"))
		must(ed.SetColumn(col("qty"), schemaedit.ColComment, "how many"))
		must(ed.SetIndexName(ix("mod_t_qty"), "mod_t_quantity"))
		at = ed.AddIndex(len(ed.Indexes()) - 1)
		must(ed.SetIndexColumns(at, "email"))
		must(ed.CycleIndexKind(at)) // UNIQUE: mod_t_email_key

		changes, err := ed.Plan(w.NormalizeType)
		must(err)
		alter, manual := w.AlterTable(tbl, changes)
		if len(manual) != 0 {
			t.Fatalf("manual: %+v", manual)
		}
		s, err := conn.Session(ctx)
		must(err)
		defer s.Close()
		if err := edit.ApplyDDL(ctx, s, dialect, alter); err != nil {
			t.Fatalf("%s: %v", strings.Join(alter, ";\n"), err)
		}

		after := schema()
		var names []string
		for _, c := range after.Columns {
			names = append(names, c.Name)
		}
		want := []string{"id", "full_name", "email", "phone", "qty"}
		if f.Driver == "postgres" {
			want = []string{"id", "full_name", "qty", "email", "phone"} // PostgreSQL adds columns at the end
		}
		if !slices.Equal(names, want) {
			t.Fatalf("columns = %q, want %q", names, want)
		}
		for _, c := range after.Columns {
			switch c.Name {
			case "full_name":
				if c.Type != "varchar(100)" {
					t.Errorf("full_name: %+v", c)
				}
			case "qty":
				if c.Nullable || c.Default == nil || *c.Default != "0" || c.Comment != "how many" {
					t.Errorf("qty: %+v", c)
				}
			}
		}
		indexes := map[string]db.Index{}
		for _, x := range after.Indexes {
			indexes[x.Name] = x
		}
		if x := indexes["mod_t_name"]; !slices.Equal(x.Columns, []string{"full_name"}) {
			t.Errorf("mod_t_name follows the renamed column: %+v", after.Indexes)
		}
		if x, ok := indexes["mod_t_quantity"]; !ok || !slices.Equal(x.Columns, []string{"qty"}) {
			t.Errorf("mod_t_qty is renamed: %+v", after.Indexes)
		}
		if x := indexes["mod_t_email_key"]; !x.Unique || !slices.Equal(x.Columns, []string{"email"}) {
			t.Errorf("unique index on email: %+v", after.Indexes)
		}
		if _, ok := indexes["mod_t_qty"]; ok {
			t.Errorf("the old name is gone: %+v", after.Indexes)
		}
	})
```

- [ ] **Step 2: 跑集成测试**

Run: `make test-integration`
Expected: PASS（两个引擎）

如果 MySQL 拒绝 `ADD COLUMN email … AFTER full_name`（报 `Unknown column 'full_name'`），说明 MySQL 在同一条 ALTER 里按旧名解析 `AFTER`。不要改这个测试；在 `schemaedit.Plan` 的 `columns := schemadiff.DiffTable(...)` 之后加：

```go
	// MySQL resolves AFTER against the table before this ALTER.
	oldName := map[string]string{}
	for from, to := range renames {
		oldName[to] = from
	}
	for i := range columns {
		if c := &columns[i]; c.Kind == db.AddColumn {
			if from, ok := oldName[c.After]; ok {
				c.After = from
			}
		}
	}
```

`TestPlanAddColumnsAfter` 的期望相应改为 `cs[1].After != "name"`，并在 Task 10 的 spec §8.2 末尾加一条"新增列的 `AFTER` 引用改名列时写旧名（MySQL 按 ALTER 之前的表解析）"。

- [ ] **Step 3: 提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/db/dbtest/contract.go
git commit -m "test(db): Modify Table 往返测试，两个引擎上改名、加列、删列、改索引后结构如期"
```

---

### Task 6: keymap——`node.edit`、`table.modify` 与结构页的编辑键

**Files:**
- Modify: `internal/ui/keymap/keymap.go`、`custom.go`、`defaults.go`
- Modify: `internal/ui/keymap/keymap_test.go`、`custom_test.go`
- Modify: `internal/ui/app.go:487`、`internal/ui/nodemenu.go:34`（只改常量名，行为不变）

**Interfaces:**
- Produces（Task 8、9 使用）：

  ```go
  EditNode    Action = "node.edit"    // replaces EditConnection ("conn.edit" still loads)
  ModifyTable Action = "table.modify" // the structure tab's e
  ```

  Structure 上下文新增：`h`/`left` → `Left`，`l`/`right` → `Right`，`e` → `ModifyTable`，`i`/`enter` → `EditCell`，`o` → `InsertRow`，`dd` → `DeleteRow`，`u` → `UndoEdit`，`U` → `UndoAllEdits`，`<leader>w` → `SubmitEdits`。

- [ ] **Step 1: 改测试**

`internal/ui/keymap/keymap_test.go` 的 `TestConnectionEditBindings` 里 `EditConnection` 改成 `EditNode`。在文件末尾追加：

```go
func TestStructureEditBindings(t *testing.T) {
	k := Default()
	for keys, want := range map[string]Action{
		"e": ModifyTable, "i": EditCell, "enter": EditCell, "o": InsertRow, "u": UndoEdit, "U": UndoAllEdits,
		"h": Left, "l": Right, "[": PrevSection, "]": NextSection,
	} {
		if a, res := k.Resolve(Structure, []string{keys}); a != want || res != Matched {
			t.Errorf("%s = %v %v, want %v", keys, a, res, want)
		}
	}
	if a, res := k.Resolve(Structure, []string{"d", "d"}); a != DeleteRow || res != Matched {
		t.Errorf("dd = %v %v", a, res)
	}
	if a, res := k.Resolve(Structure, []string{"space", "w"}); a != SubmitEdits || res != Matched {
		t.Errorf("<space>w = %v %v", a, res)
	}
}
```

`internal/ui/keymap/custom_test.go` 末尾追加：

```go
func TestCustomAcceptsConnEdit(t *testing.T) {
	k, err := Custom("", map[Context]map[string]string{Tree: {"E": "conn.edit"}})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := k.Resolve(Tree, []string{"E"}); a != EditNode {
		t.Fatalf("E = %v", a)
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/ui/keymap`
Expected: FAIL（`undefined: EditNode`）

- [ ] **Step 3: 实现**

`keymap.go`：`EditConnection    Action = "conn.edit"` 改成 `EditNode          Action = "node.edit"`；在 `TruncateTable` 下加 `ModifyTable       Action = "table.modify"`。

`custom.go`：

```go
var renamedActions = map[Action]Action{"conn.delete": DeleteNode, "conn.edit": EditNode}
```

同一文件里 "unknown context (want one of …)" 的列表补上 `diff`（第 2 阶段漏了）：`want one of global, tree, grid, structure, console, results, diff, popup`。

`defaults.go`：Tree 的 `e` 改成

```go
		Binding{Tree, keys("e"), EditNode, "Edit connection / modify table"},
```

Structure 段改成：

```go
	b = append(b, vertical(Structure)...)
	b = append(b,
		Binding{Structure, keys("["), PrevSection, "Previous section"},
		Binding{Structure, keys("]"), NextSection, "Next section"},
		Binding{Structure, keys("h"), Left, "Previous field"},
		Binding{Structure, keys("left"), Left, "Previous field"},
		Binding{Structure, keys("l"), Right, "Next field"},
		Binding{Structure, keys("right"), Right, "Next field"},
		Binding{Structure, keys("e"), ModifyTable, "Modify the table / stop modifying"},
		Binding{Structure, keys("i"), EditCell, "Edit the field"},
		Binding{Structure, keys("enter"), EditCell, "Edit the field"},
		Binding{Structure, keys("o"), InsertRow, "Add a column or index below"},
		Binding{Structure, keys("d", "d"), DeleteRow, "Delete the column or index"},
		Binding{Structure, keys("u"), UndoEdit, "Undo the last change"},
		Binding{Structure, keys("U"), UndoAllEdits, "Discard every change"},
		Binding{Structure, keys(LeaderToken, "w"), SubmitEdits, "Submit changes (:w)"},
	)
```

把仓库里其余的 `keymap.EditConnection` 换成 `keymap.EditNode`：

Run: `grep -rn "EditConnection" internal` ，逐个替换（`app.go` 的 `treeAction`、`nodemenu.go` 的连接菜单）。

- [ ] **Step 4: 运行**

Run: `go test -race ./internal/ui/...`
Expected: PASS（`TestKnownActionsCoverDefaults` 会检查新动作都有描述）

- [ ] **Step 5: 提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/ui
git commit -m "feat(keymap): 树上 e 改为 node.edit 并兼容 conn.edit，结构页新增编辑键"
```

---

### Task 7: 结构页 pane——行 / 字段光标与编辑标记

**Files:**
- Modify: `internal/ui/panes/structure/structure.go`
- Create: `internal/ui/panes/structure/table.go`
- Modify: `internal/ui/panes/structure/structure_test.go`

**Interfaces:**
- Consumes: Task 3 的 `schemaedit.Table`、`ColumnRow`、`IndexRow`、`State`、`ColumnFields`、`IndexFields`
- Produces（Task 8 使用）：

  ```go
  func (m *Model) SetEdit(e *schemaedit.Table) // nil: back to the table as read
  func (m *Model) Editing() bool
  func (m *Model) Cursor() (row, field int)
  func (m *Model) SetCursor(s Section, row int) // clamps; resets the field only when the section changes
  func (m *Model) SetField(f int)
  func (m *Model) MoveField(delta int)          // only while editing, in Columns and Indexes
  // Scroll, Top, Bottom, HalfPage now move the row cursor in Columns,
  // Indexes and Foreign Keys, and scroll in DDL.
  ```

- [ ] **Step 1: 写测试**

`internal/ui/panes/structure/structure_test.go` 的 import 加 `"github.com/shepherd-yun/datavim/internal/schemaedit"`，末尾追加：

```go
func editable() db.TableSchema {
	d := data()
	return db.TableSchema{
		Ref:     db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"},
		Columns: d.Columns,
		Indexes: d.Indexes,
	}
}

func TestCursorMovesOverRows(t *testing.T) {
	m := New(sqlx.MySQL)
	m.SetSize(80, 20)
	m.SetData(data())
	m.Scroll(1)
	if row, _ := m.Cursor(); row != 1 {
		t.Fatalf("row = %d", row)
	}
	m.Scroll(5)
	if row, _ := m.Cursor(); row != 1 {
		t.Fatal("the cursor stops at the last row")
	}
	m.MoveField(1)
	if _, f := m.Cursor(); f != 0 {
		t.Fatal("fields move only while editing")
	}
	m.NextSection()
	if row, f := m.Cursor(); row != 0 || f != 0 || m.Section() != Indexes {
		t.Fatalf("a new section starts at the top: %d %d", row, f)
	}
}

func TestEditModeMarksFields(t *testing.T) {
	m := New(sqlx.MySQL)
	m.SetSize(80, 20)
	m.SetData(data())
	e := schemaedit.New(editable(), sqlx.MySQL)
	if err := e.SetColumn(1, schemaedit.ColName, "memo"); err != nil {
		t.Fatal(err)
	}
	at := e.AddColumn(1)
	if err := e.SetColumn(at, schemaedit.ColName, "neww"); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteColumn(0); err != nil {
		t.Fatal(err)
	}
	m.SetEdit(e)
	if !m.Editing() {
		t.Fatal("editing")
	}
	m.SetCursor(Columns, 2)
	m.MoveField(1) // the new column's type: the cursor is not on the cells checked below
	v := m.View()
	for name, style := range map[string]lipgloss.Style{
		"memo": theme.Changed, "neww": theme.Inserted, "id  ": theme.Deleted,
	} {
		if !strings.Contains(v, style.Render(name)) {
			t.Errorf("%q is not marked:\n%q", name, v)
		}
	}
	if _, f := m.Cursor(); f != 1 {
		t.Fatalf("field = %d", f)
	}
	m.MoveField(10)
	if _, f := m.Cursor(); f != int(schemaedit.ColumnFields)-1 {
		t.Fatalf("the field cursor stops at the last field: %d", f)
	}
	m.SetEdit(nil)
	if m.Editing() || strings.Contains(m.View(), "neww") {
		t.Fatal("SetEdit(nil) shows the table as read")
	}
}

func TestCursorKeepsItsRowVisible(t *testing.T) {
	m := New(sqlx.MySQL)
	m.SetSize(40, 5) // header line, blank, 3 body lines: table header, rule, one row
	d := data()
	for i := range 10 {
		d.Columns = append(d.Columns, db.Column{Name: fmt.Sprintf("c%d", i), Type: "int"})
	}
	m.SetData(d)
	m.Scroll(6)
	if v := ansi.Strip(m.View()); !strings.Contains(v, "c4") {
		t.Fatalf("row 6 (c4) is visible:\n%s", v)
	}
	m.Top()
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Name") {
		t.Fatalf("the top shows the header:\n%s", v)
	}
}
```

import 补 `"fmt"`、`"charm.land/lipgloss/v2"`。名字列宽：`Name` 4、`memo` 4、`neww` 4、`id` 2，所以 `id` 填充成 `"id  "`，`memo`、`neww` 不填充。

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/ui/panes/structure`
Expected: FAIL（`undefined: m.Cursor` 等）

- [ ] **Step 3: 实现 `table.go`**

`internal/ui/panes/structure/table.go`：

```go
package structure

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/textutil"
)

// table lays rows out under headers like textutil.Table and styles each
// padded cell with style(row, col); ok false leaves a cell plain. Cells
// are sanitized here, before styling.
func table(headers []string, rows [][]string, style func(row, col int) (lipgloss.Style, bool)) []string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = ansi.StringWidth(h)
	}
	clean := make([][]string, len(rows))
	for r, cells := range rows {
		clean[r] = make([]string, len(cells))
		for i, c := range cells {
			clean[r][i] = textutil.Sanitize(c)
			widths[i] = max(widths[i], ansi.StringWidth(clean[r][i]))
		}
	}
	line := func(cells []string, row int) string {
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = textutil.Fit(c, widths[i])
			if row >= 0 && style != nil {
				if st, ok := style(row, i); ok {
					parts[i] = st.Render(parts[i])
				}
			}
		}
		return strings.TrimRight(strings.Join(parts, "  "), " ")
	}
	rule := make([]string, len(widths))
	for i, w := range widths {
		rule[i] = strings.Repeat("─", w)
	}
	out := []string{line(headers, -1), strings.TrimRight(strings.Join(rule, "  "), " ")}
	if len(rows) == 0 {
		return append(out, "(none)")
	}
	for r, cells := range clean {
		out = append(out, line(cells, r))
	}
	return out
}
```

- [ ] **Step 4: 改 `structure.go`**

import 加 `"charm.land/lipgloss/v2"` 与 `"github.com/shepherd-yun/datavim/internal/schemaedit"`。`Model` 加字段：

```go
type Model struct {
	data          Data
	edit          *schemaedit.Table // the table being modified; nil shows data
	section       Section
	offset        int
	row, field    int // the cursor in Columns, Indexes and Foreign Keys; field only while editing
	width, height int
	dialect       sqlx.Dialect
	ddl           []string // highlighted DDL lines
}
```

替换 `SetData` 之后到 `lines` 之前的方法：

```go
// SetData replaces what the view shows.
func (m *Model) SetData(d Data) {
	m.data, m.offset = d, 0
	clean := textutil.SanitizeText(d.DDL)
	m.ddl = theme.HighlightLines(clean, highlight.SQL(clean, m.dialect))
	m.clampRow()
}

// SetEdit shows e's staged columns and indexes and lets the cursor pick a
// field; nil goes back to the table as read.
func (m *Model) SetEdit(e *schemaedit.Table) {
	m.edit, m.field = e, 0
	m.clampRow()
}

func (m *Model) Editing() bool           { return m.edit != nil }
func (m *Model) Cursor() (row, field int) { return m.row, m.field }
func (m *Model) SetSize(w, h int)         { m.width, m.height = max(w, 0), max(h, 0); m.follow() }
func (m *Model) Section() Section         { return m.section }
func (m *Model) NextSection()             { m.SetCursor((m.section+1)%sectionCount, 0) }
func (m *Model) PrevSection()             { m.SetCursor((m.section+sectionCount-1)%sectionCount, 0) }
func (m *Model) bodyHeight() int          { return max(m.height-2, 0) }
func (m *Model) maxOffset() int           { return max(len(m.lines())-m.bodyHeight(), 0) }

// SetCursor puts the cursor on row of section s.
func (m *Model) SetCursor(s Section, row int) {
	if s != m.section {
		m.section, m.field, m.offset = s, 0, 0
	}
	m.row = row
	m.clampRow()
}

// SetField puts the cursor on field f of its row.
func (m *Model) SetField(f int) { m.field = textutil.Clamp(f, 0, m.fieldCount()-1) }

// MoveField moves the cursor between a row's fields while editing.
func (m *Model) MoveField(delta int) { m.SetField(m.field + delta) }

func (m *Model) fieldCount() int {
	switch {
	case m.edit == nil:
		return 1
	case m.section == Columns:
		return int(schemaedit.ColumnFields)
	case m.section == Indexes:
		return int(schemaedit.IndexFields)
	}
	return 1
}

func (m *Model) rowCount() int {
	switch m.section {
	case Columns:
		if m.edit != nil {
			return len(m.edit.Columns())
		}
		return len(m.data.Columns)
	case Indexes:
		if m.edit != nil {
			return len(m.edit.Indexes())
		}
		return len(m.data.Indexes)
	case ForeignKeys:
		return len(m.data.ForeignKeys)
	}
	return 0
}

func (m *Model) clampRow() {
	m.row = textutil.Clamp(m.row, 0, m.rowCount()-1)
	m.field = textutil.Clamp(m.field, 0, m.fieldCount()-1)
	m.follow()
}

// follow scrolls so that the cursor's row shows; the top row brings the
// header back.
func (m *Model) follow() {
	if m.section == DDL {
		return
	}
	line := m.row + 2 // below the header and its rule
	switch h := m.bodyHeight(); {
	case m.row == 0:
		m.offset = 0
	case line < m.offset:
		m.offset = line
	case h > 0 && line >= m.offset+h:
		m.offset = line - h + 1
	}
}

// Scroll moves the cursor delta rows; the DDL scrolls instead.
func (m *Model) Scroll(delta int) {
	if m.section == DDL {
		m.offset = textutil.Clamp(m.offset+delta, 0, m.maxOffset())
		return
	}
	m.row += delta
	m.clampRow()
}

func (m *Model) Top()             { m.Scroll(-max(m.rowCount(), len(m.ddl))) }
func (m *Model) Bottom()          { m.Scroll(max(m.rowCount(), len(m.ddl))) }
func (m *Model) HalfPage(dir int) { m.Scroll(dir * max(m.bodyHeight()/2, 1)) }
```

把 `lines()` 里 Columns 与 Indexes 两个分支改成调用新函数，并让三张表都用 `table`：

```go
func (m *Model) lines() []string {
	switch m.section {
	case Columns:
		if m.edit != nil {
			return m.editColumns()
		}
		rows := make([][]string, len(m.data.Columns))
		for i, c := range m.data.Columns {
			rows[i] = []string{keyMark(c.PrimaryKey), c.Name, c.Type, nullText(c.Nullable), defaultText(c.Default), c.Comment}
		}
		return table(columnHeaders, rows, m.rowStyle)
	case Indexes:
		if m.edit != nil {
			return m.editIndexes()
		}
		rows := make([][]string, len(m.data.Indexes))
		for i, idx := range m.data.Indexes {
			rows[i] = []string{idx.Name, indexKind(idx), strings.Join(idx.Columns, ", ")}
		}
		return table(indexHeaders, rows, m.rowStyle)
	case ForeignKeys:
		rows := make([][]string, len(m.data.ForeignKeys))
		for i, fk := range m.data.ForeignKeys {
			ref := fk.RefTable.Name
			if ns := fk.RefTable.Schema; ns != "" {
				ref = ns + "." + ref
			} else if ns := fk.RefTable.Database; ns != "" {
				ref = ns + "." + ref
			}
			rows[i] = []string{fk.Name, strings.Join(fk.Columns, ", "), ref + "(" + strings.Join(fk.RefColumns, ", ") + ")"}
		}
		return table([]string{"Name", "Columns", "References"}, rows, m.rowStyle)
	default:
		return m.ddl
	}
}

var (
	columnHeaders = []string{"", "Name", "Type", "Null", "Default", "Comment"}
	indexHeaders  = []string{"Name", "Kind", "Columns"}
)

func keyMark(pk bool) string {
	if pk {
		return "PK"
	}
	return ""
}

func nullText(nullable bool) string {
	if nullable {
		return "NULL"
	}
	return "NOT NULL"
}

func defaultText(d *string) string {
	if d == nil {
		return ""
	}
	return *d
}

func indexKind(idx db.Index) string {
	switch {
	case idx.Primary:
		return "PRIMARY"
	case idx.Unique:
		return "UNIQUE"
	}
	return "INDEX"
}

// rowStyle highlights the cursor's row of the table as read.
func (m *Model) rowStyle(row, _ int) (lipgloss.Style, bool) {
	return theme.Selected, row == m.row
}

// cellStyle marks a field being modified: the cursor, then deleted, new
// and changed rows and fields, colored like staged data edits.
func cellStyle(state schemaedit.State, changed, cursor bool) (lipgloss.Style, bool) {
	switch {
	case cursor:
		return theme.Selected, true
	case state == schemaedit.Deleted:
		return theme.Deleted, true
	case state == schemaedit.Added:
		return theme.Inserted, true
	case changed:
		return theme.Changed, true
	}
	return lipgloss.Style{}, false
}

func (m *Model) editColumns() []string {
	cols := m.edit.Columns()
	rows := make([][]string, len(cols))
	for i, r := range cols {
		rows[i] = []string{keyMark(r.Col.PrimaryKey)}
		for f := range schemaedit.ColumnFields {
			rows[i] = append(rows[i], r.Text(f))
		}
	}
	return table(columnHeaders, rows, func(row, col int) (lipgloss.Style, bool) {
		r := cols[row]
		if col == 0 { // the key mark is not a field
			return cellStyle(r.State(), false, false)
		}
		f := schemaedit.ColumnField(col - 1)
		return cellStyle(r.State(), r.Changed(f), row == m.row && col-1 == m.field)
	})
}

func (m *Model) editIndexes() []string {
	idx := m.edit.Indexes()
	rows := make([][]string, len(idx))
	for i, r := range idx {
		for f := range schemaedit.IndexFields {
			rows[i] = append(rows[i], r.Text(f))
		}
	}
	return table(indexHeaders, rows, func(row, col int) (lipgloss.Style, bool) {
		r := idx[row]
		return cellStyle(r.State(), r.Changed(schemaedit.IndexField(col)), row == m.row && col == m.field)
	})
}
```

`View()` 里删掉逐行 `textutil.Sanitize` 的那三行（单元格已在 `table` 里清理过，DDL 在 `SetData` 里清理过），只保留 `out = append(out, textutil.Fit(l, m.width))`。

删除不再使用的 `textutil.Table` 调用后，若 `textutil.Table` 仍被其它包使用就保留它（`grep -rn "textutil.Table" internal`）。

- [ ] **Step 5: 运行**

Run: `go test -race ./internal/ui/...`
Expected: PASS（`TestSections`、`TestScrollClamps` 等原有测试也要过；`TestScrollClamps` 在 DDL 区，行为不变）

- [ ] **Step 6: 提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/ui/panes/structure
git commit -m "feat(ui): 结构页有行光标，编辑模式下按字段移动并标出改动"
```

---

### Task 8: 结构页的编辑模式

**Files:**
- Create: `internal/ui/structedit.go`
- Create: `internal/ui/modify.go`
- Create: `internal/ui/structedit_test.go`
- Modify: `internal/ui/tabs.go`（`structureTab` 字段、构造、`SetSize`、`Handle`、`Update`、`View`、`retryAfterPassword`、`retarget`）
- Modify: `internal/ui/app.go`（`openStructure` 设 `keys` 与 `store`；`treeAction` 的 `EditNode`；`requestClose`、`unsavedTables`、`canChange` 改用 `stager`）
- Modify: `internal/ui/tableops.go`（`tableOpBlocked` 改用 `tableBlocked`）
- Modify: `internal/ui/nodemenu.go`（表的菜单加 "Modify table"）
- Modify: `internal/ui/reconnect.go`（`schemaLoadedMsg.failure`）

**Interfaces:**
- Consumes: Task 3 `schemaedit`；Task 6 `keymap.EditNode`、`ModifyTable`；Task 7 pane 的 `SetEdit`、`Cursor`、`SetCursor`、`SetField`、`MoveField`
- Produces（Task 9 使用）：

  ```go
  // in modify.go
  type stager interface {
      tab
      changes() int
      unsavedHint(submit, discard string) string
      submit() tea.Cmd
  }
  func (m *Model) tableBlocked(conn string, ref db.TableRef, self tab) string
  func (m *Model) modifyTable(conn string, ref db.TableRef) tea.Cmd

  // in structedit.go
  const readOnlyHint = "read-only connection · :set noreadonly allows changes for this session"
  type schemaLoadedMsg struct { tabID int; conn string; schema db.TableSchema; ddl db.DDLWriter; err error }
  type structEditedMsg struct { tabID int; ed *schemaedit.Table; pos schemaedit.Pos; field int; text string }
  func (t *structureTab) startEdit() tea.Cmd
  func (t *structureTab) stopEdit()
  func (t *structureTab) changes() int
  func (t *structureTab) unsavedHint(submit, discard string) string
  // structureTab gains: keys *keymap.Keymap, store Store, dialect sqlx.Dialect,
  // ed *schemaedit.Table, ddl db.DDLWriter, editLoading, retryEdit bool,
  // status string, statusErr bool, height int — and for Task 9:
  // submitting, retrySubmit bool, gen int, pendingSQL []string, cancel context.CancelFunc
  ```

  本任务的 `stager` 接口不含 `submit()`；Task 9 实现 `structureTab.submit` 时再把 `submit() tea.Cmd` 加进接口。

- [ ] **Step 1: 写测试**

`internal/ui/structedit_test.go`：

```go
package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/schemaedit"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/structure"
)

func usersSchema() db.TableSchema {
	return db.TableSchema{
		Ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"},
		Columns: []db.Column{
			{Name: "id", Type: "bigint", PrimaryKey: true, Position: 1},
			{Name: "name", Type: "text", Nullable: true, Position: 2},
		},
		Indexes: []db.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
	}
}

// modifyUsers selects conn's users table in the tree and presses e.
func modifyUsers(t *testing.T, m *Model, b *fakeBackend, conn string) *structureTab {
	t.Helper()
	b.conn.setSchemas(usersSchema())
	selectNode(t, m, conn, "app", "users")
	press(t, m, "e")
	st, ok := m.activeTab().(*structureTab)
	if !ok || st.ed == nil {
		t.Fatalf("not modifying: %T\n%s", m.activeTab(), screen(m))
	}
	return st
}

func TestTreeEModifiesTheTable(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	if !strings.Contains(screen(m), "modifying users") {
		t.Fatalf("status:\n%s", screen(m))
	}
	if st.view.Section() != structure.Columns
		t.Fatal("editing starts in the columns")
	}
	press(t, m, "e")
	if st.ed != nil {
		t.Fatal("e with no changes stops modifying")
	}
}

func TestTreeEOnAConnectionStillEditsIt(t *testing.T) {
	m, _ := newTestModel(t)
	m.setFocus(focusTree)
	press(t, m, "g", "g", "e") // local, the first node
	if _, ok := m.overlay.(*overlay.ConnForm); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
}

func TestModifyRefusedOnReadOnlyAndViews(t *testing.T) {
	m, b := newTestModel(t)
	if err := b.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	selectNode(t, m, "local", "app", "users")
	press(t, m, "e")
	if !strings.Contains(screen(m), "read-only connection") {
		t.Fatalf("readonly:\n%s", screen(m))
	}
	if err := b.SetReadOnly("local", false); err != nil {
		t.Fatal(err)
	}
	selectNode(t, m, "local", "app", "active")
	press(t, m, "e")
	if !strings.Contains(screen(m), "views cannot be modified") {
		t.Fatalf("view:\n%s", screen(m))
	}
}

func TestEditKeysNeedEditMode(t *testing.T) {
	m, _ := newTestModel(t)
	drain(t, m, m.openStructure("local", usersRef))
	press(t, m, "i")
	if !strings.Contains(screen(m), "e modifies the table") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestStructureEditStagesAndUndoes(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "j", "i") // name's Name field
	if !strings.Contains(screen(m), "Column name:") {
		t.Fatalf("prompt:\n%s", screen(m))
	}
	typeText(t, m, "_x")
	press(t, m, "enter")
	if got := st.ed.Columns()[1].Col.Name; got != "name_x" {
		t.Fatalf("name = %q", got)
	}
	if !strings.Contains(screen(m), "● 1 change") {
		t.Fatalf("status:\n%s", screen(m))
	}
	press(t, m, "l", "l", "i") // Null toggles in place
	if st.ed.Columns()[1].Col.Nullable {
		t.Fatal("i on Null toggles it")
	}
	press(t, m, "u", "u")
	if st.ed.Changes() != 0 {
		t.Fatalf("changes after undo: %d", st.ed.Changes())
	}
	press(t, m, "u")
	if !strings.Contains(screen(m), "no changes to undo") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestNewColumnAsksNameThenType(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "j", "o")
	typeText(t, m, "email")
	press(t, m, "enter")
	if !strings.Contains(screen(m), "Type, e.g.") {
		t.Fatalf("type prompt:\n%s", screen(m))
	}
	typeText(t, m, "varchar(80)")
	press(t, m, "enter")
	r := st.ed.Columns()[2]
	if r.State() != schemaedit.Added || r.Col.Name != "email" || r.Col.Type != "varchar(80)" {
		t.Fatalf("row = %+v", r)
	}
	if row, _ := st.view.Cursor(); row != 2 {
		t.Fatalf("cursor row = %d", row)
	}
}

func TestNewIndexAsksColumnsAndNamesItself(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "]", "o")
	typeText(t, m, "name")
	press(t, m, "enter")
	r := st.ed.Indexes()[1]
	if r.Index.Name != "users_name_idx" || r.Text(schemaedit.IdxColumns) != "name" {
		t.Fatalf("index = %+v", r.Index)
	}
	press(t, m, "h", "i") // Kind
	if !st.ed.Indexes()[1].Index.Unique {
		t.Fatal("i on Kind cycles it")
	}
}

func TestForeignKeysAndDDLAreReadOnly(t *testing.T) {
	m, b := newTestModel(t)
	modifyUsers(t, m, b, "local")
	press(t, m, "]", "]", "o")
	if !strings.Contains(screen(m), "only columns and indexes") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestStagedStructureEditsGuardCloseQuitAndDrop(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "j", "d", "d")
	if st.changes() != 1 {
		t.Fatalf("changes = %d", st.changes())
	}
	press(t, m, "space", "r")
	if st.ed == nil || st.changes() != 1 || !strings.Contains(screen(m), "1 unsaved change") {
		t.Fatalf("refresh keeps the edits:\n%s", screen(m))
	}
	command(t, m, "q")
	if !strings.Contains(screen(m), "Unsaved changes") {
		t.Fatalf("close asks first:\n%s", screen(m))
	}
	press(t, m, "n")
	selectNode(t, m, "local", "app", "users")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "users:struct has 1 unsaved change") {
		t.Fatalf("drop is blocked:\n%s", screen(m))
	}
	m.setFocus(focusTree)
	press(t, m, "g", "g", "e") // local, the first node
	if m.overlay != nil {
		t.Fatal("the connection cannot be edited with staged structure edits")
	}
}
```

`usersRef` 在 `internal/ui/tabs_test.go:16`。`:q` 弹出的确认框没有 expect，`n` 取消。树上的状态写在最后一行，标签页的状态写在标签页里，所以断言都用 `screen(m)`。

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/ui -run 'TestTreeE|TestModifyRefused|TestEditKeys|TestStructureEdit|TestNewColumn|TestNewIndex|TestForeignKeysAndDDL|TestStagedStructure'`
Expected: FAIL（编译失败：`st.ed` 等未定义）

- [ ] **Step 3: `modify.go`——`stager`、`tableBlocked`、`modifyTable`**

`internal/ui/modify.go`：

```go
package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
)

// stager is a tab that stages changes until they are submitted: table
// data, and a table being modified.
type stager interface {
	tab
	changes() int
	unsavedHint(submit, discard string) string
}

// tableBlocked says why the table ref on conn cannot be changed now ("" when
// it can): a read-only connection, a console on conn in a transaction, or a
// tab other than self with staged changes of the table.
func (m *Model) tableBlocked(conn string, ref db.TableRef, self tab) string {
	if cfg, _ := m.backend.Connection(conn); cfg.IsReadOnly() {
		return readOnlyHint
	}
	for _, t := range m.tabs {
		if t == self {
			continue
		}
		switch t := t.(type) {
		case *consoleTab:
			if t.conn == conn && t.openTx() {
				return "a console on " + conn + " has an open transaction · :commit or :rollback first"
			}
		case stager:
			if c, r := t.Target(); c == conn && r == ref && t.changes() > 0 {
				return joinHints(t.Title()+" has "+plural(t.changes(), "unsaved change"), t.unsavedHint("submits", "discards"))
			}
		}
	}
	return ""
}

// modifyTable opens the structure of ref and starts modifying it.
func (m *Model) modifyTable(conn string, ref db.TableRef) tea.Cmd {
	if cfg, _ := m.backend.Connection(conn); cfg.IsReadOnly() {
		m.setStatus(readOnlyHint, true)
		return nil
	}
	open := m.openStructure(conn, ref)
	st, ok := m.activeTab().(*structureTab)
	if !ok {
		return open
	}
	return tea.Batch(open, st.startEdit())
}
```

`tableops.go` 的 `tableOpBlocked` 整个函数体换成：

```go
func (m *Model) tableOpBlocked(op tableOp) string { return m.tableBlocked(op.conn, op.ref, nil) }
```

（保留原注释；若 `config`、`keymap` 等 import 因此不再使用，删掉。）

`app.go`：

- `canChange` 的 `case *dataTab:` 改成 `case stager:`，`t.Title()+" on "+conn+" has unsaved changes"` 不变；
- `requestClose` 里 `if d, ok := t.(*dataTab); ok && d.changes() > 0 && !force` 改成 `if d, ok := t.(stager); ok && d.changes() > 0 && !force`；
- `unsavedTables` 里 `t.(*dataTab)` 改成 `t.(stager)`；
- `openStructure` 改成：

```go
func (m *Model) openStructure(conn string, ref db.TableRef) tea.Cmd {
	return tea.Batch(m.openTab(structureTabKey(conn, ref), history.Recent{Kind: history.RecentStructure, Conn: conn, Database: ref.Database, Schema: ref.Schema, Name: ref.Name}, func(id int) tab {
		t := newStructureTab(id, m.backend, m.store, conn, ref)
		t.keys = m.keys
		return t
	}), m.ensureMeta(conn, ref.Database))
}
```

- `treeAction` 的 `case keymap.EditNode:`（Task 6 改过名）换成：

```go
	case keymap.EditNode:
		n := t.Selected()
		switch {
		case n == nil:
		case n.Kind == tree.KindTable:
			return m.modifyTable(n.Conn, n.TableRef())
		case n.Kind == tree.KindView:
			m.setStatus(errViewNotModifiable.Error(), true)
		case m.canChange(n.Conn):
			if cfg, ok := m.backend.Connection(n.Conn); ok {
				m.overlay = overlay.NewEditConnForm(m.keys, cfg)
			}
		}
```

`nodemenu.go` 表与视图分支：在 `{keymap.OpenFullStructure, "Open structure"},` 之后，仅对表加：

```go
		entries = []entry{
			{keymap.Expand, "Open data"},
			{keymap.OpenStructure, "Columns"},
			{keymap.OpenFullStructure, "Open structure"},
		}
		if n.Kind == tree.KindTable {
			entries = append(entries, entry{keymap.EditNode, "Modify table"})
		}
		entries = append(entries,
			entry{keymap.CopyDDL, "Copy DDL"},
			entry{keymap.OpenConsole, "Open console"},
			entry{keymap.RenameTable, "Rename " + obj},
		)
```

（其后的 Mark / Compare / Truncate / Drop 不变。）

- [ ] **Step 4: `structedit.go`——编辑模式**

`internal/ui/structedit.go`：

```go
package ui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/schemaedit"
	"github.com/shepherd-yun/datavim/internal/session"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/structure"
)

const readOnlyHint = "read-only connection · :set noreadonly allows changes for this session"

var errViewNotModifiable = errors.New("views cannot be modified · change the view in a console")

type (
	// schemaLoadedMsg carries the table as Modify Table edits it, and the
	// connection's DDL writer.
	schemaLoadedMsg struct {
		tabID  int
		conn   string
		schema db.TableSchema
		ddl    db.DDLWriter
		err    error
	}
	// structEditedMsg is a field typed at a Modify Table prompt.
	structEditedMsg struct {
		tabID int
		ed    *schemaedit.Table // the edit the prompt was opened on
		pos   schemaedit.Pos
		field int // a schemaedit.ColumnField or IndexField
		text  string
	}
)

func (m schemaLoadedMsg) TabID() int { return m.tabID }
func (m structEditedMsg) TabID() int { return m.tabID }

func (t *structureTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

func (t *structureTab) changes() int {
	if t.ed == nil {
		return 0
	}
	return t.ed.Changes()
}

// unsavedHint is "<space>w submits · U discards" with the keymap's keys.
func (t *structureTab) unsavedHint(submit, discard string) string {
	return joinHints(keyHint(t.keys, keymap.Structure, keymap.SubmitEdits, submit), keyHint(t.keys, keymap.Structure, keymap.UndoAllEdits, discard))
}

// startEdit reads the table for Modify Table, unless it is being modified
// or read already.
func (t *structureTab) startEdit() tea.Cmd {
	if t.ed != nil || t.editLoading {
		return nil
	}
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil
	}
	t.editLoading = true
	id, b, conn, ref := t.id, t.backend, t.conn, t.ref
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := schemaLoadedMsg{tabID: id, conn: conn}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			tables, err := c.Meta().Tables(ctx, ref.Namespace)
			if err != nil {
				return err
			}
			for _, tb := range tables {
				if tb.Name == ref.Name && tb.Kind == db.TableKindView {
					return errViewNotModifiable
				}
			}
			msg.ddl = c.Dialect().DDL()
			msg.schema, err = c.Meta().TableSchema(ctx, ref)
			if errors.Is(err, db.ErrNotFound) {
				return fmt.Errorf("table %s not found", ref.Name)
			}
			return err
		})
		return msg
	}
}

func (t *structureTab) schemaLoaded(m schemaLoadedMsg) {
	t.editLoading = false
	if m.err != nil {
		t.retryEdit = errors.Is(m.err, session.ErrPasswordRequired)
		t.setStatus(errorText(m.err), true)
		return
	}
	t.ed, t.ddl = schemaedit.New(m.schema, t.dialect), m.ddl
	t.view.SetEdit(t.ed)
	if s := t.view.Section(); s != structure.Columns && s != structure.Indexes {
		t.view.SetCursor(structure.Columns, 0)
	}
	t.setStatus("", false)
}

// stopEdit goes back to the table as read, dropping any staged edits.
func (t *structureTab) stopEdit() {
	t.ed, t.ddl = nil, nil
	t.view.SetEdit(nil)
}

// toggleEdit is e in the structure tab.
func (t *structureTab) toggleEdit() tea.Cmd {
	switch n := t.changes(); {
	case t.ed == nil:
		return t.startEdit()
	case n > 0:
		t.setStatus(joinHints(plural(n, "unsaved change"), t.unsavedHint("submits", "discards")), true)
	default:
		t.stopEdit()
	}
	return nil
}

// refresh reloads the structure, and the table being modified, unless
// edits are staged.
func (t *structureTab) refresh() tea.Cmd {
	if n := t.changes(); n > 0 {
		t.setStatus(joinHints(plural(n, "unsaved change"), t.unsavedHint("submits", "discards")), true)
		return nil
	}
	if t.loading {
		return nil
	}
	cmds := []tea.Cmd{t.load()}
	if t.ed != nil {
		t.stopEdit()
		cmds = append(cmds, t.startEdit())
	}
	return tea.Batch(cmds...)
}

// editAction handles the keys that stage edits.
func (t *structureTab) editAction(a keymap.Action) tea.Cmd {
	if t.ed == nil {
		hint := keyHint(t.keys, keymap.Structure, keymap.ModifyTable, "modifies the table")
		if hint == "" {
			hint = "the table is not being modified"
		}
		t.setStatus(hint, true)
		return nil
	}
	if t.submitting {
		t.setStatus("submitting changes…", true)
		return nil
	}
	switch a {
	case keymap.UndoEdit:
		t.undo()
		return nil
	case keymap.UndoAllEdits:
		t.discard()
		return nil
	}
	sec := t.view.Section()
	if sec != structure.Columns && sec != structure.Indexes {
		t.setStatus("only columns and indexes can be changed here", true)
		return nil
	}
	row, field := t.view.Cursor()
	switch a {
	case keymap.EditCell:
		return t.editField(sec == structure.Indexes, row, field)
	case keymap.InsertRow:
		return t.insertRow(sec == structure.Indexes, row)
	case keymap.DeleteRow:
		t.deleteRow(sec == structure.Indexes, row)
	}
	return nil
}

func (t *structureTab) report(err error) {
	if err != nil {
		t.setStatus(err.Error(), true)
	}
}

// prompt asks for a field's new text.
func (t *structureTab) prompt(pos schemaedit.Pos, field int, label, initial string) tea.Cmd {
	id, ed := t.id, t.ed
	return openOverlay(overlay.NewPrompt("Modify "+t.ref.Name, label, initial, func(s string) tea.Msg {
		return structEditedMsg{tabID: id, ed: ed, pos: pos, field: field, text: s}
	}))
}

const (
	labelColumnName  = "Column name:"
	labelColumnType  = "Type, e.g. varchar(255), bigint, decimal(10,2):"
	labelDefault     = "Default as SQL ('text', 0, CURRENT_TIMESTAMP) · empty: no default:"
	labelComment     = "Comment:"
	labelIndexName   = "Index name:"
	labelIndexColumn = "Columns, comma-separated, in order:"
)

func (t *structureTab) editField(index bool, row, field int) tea.Cmd {
	add := keyHint(t.keys, keymap.Structure, keymap.InsertRow, "adds one")
	if index {
		rows := t.ed.Indexes()
		switch {
		case row >= len(rows):
			t.setStatus(joinHints("no index here", add), true)
			return nil
		case rows[row].Deleted:
			t.setStatus(schemaedit.ErrDeleted.Error(), true)
			return nil
		}
		pos := schemaedit.Pos{Indexes: true, Row: row}
		switch f := schemaedit.IndexField(field); f {
		case schemaedit.IdxKind:
			t.report(t.ed.CycleIndexKind(row))
		case schemaedit.IdxName:
			return t.prompt(pos, field, labelIndexName, rows[row].Text(f))
		case schemaedit.IdxColumns:
			return t.prompt(pos, field, labelIndexColumn, rows[row].Text(f))
		}
		return nil
	}
	rows := t.ed.Columns()
	switch {
	case row >= len(rows):
		t.setStatus(joinHints("no column here", add), true)
		return nil
	case rows[row].Deleted:
		t.setStatus(schemaedit.ErrDeleted.Error(), true)
		return nil
	}
	pos := schemaedit.Pos{Row: row}
	labels := map[schemaedit.ColumnField]string{
		schemaedit.ColName: labelColumnName, schemaedit.ColType: labelColumnType,
		schemaedit.ColDefault: labelDefault, schemaedit.ColComment: labelComment,
	}
	f := schemaedit.ColumnField(field)
	if f == schemaedit.ColNullable {
		t.report(t.ed.ToggleNullable(row))
		return nil
	}
	return t.prompt(pos, field, labels[f], rows[row].Text(f))
}

// structEdited applies a typed field. Naming a new column goes on to ask
// for its type.
func (t *structureTab) structEdited(m structEditedMsg) tea.Cmd {
	if m.ed != t.ed {
		return nil // stopped or reloaded meanwhile
	}
	if m.pos.Indexes {
		if m.pos.Row >= len(t.ed.Indexes()) {
			return nil
		}
		switch schemaedit.IndexField(m.field) {
		case schemaedit.IdxName:
			t.report(t.ed.SetIndexName(m.pos.Row, m.text))
		case schemaedit.IdxColumns:
			t.report(t.ed.SetIndexColumns(m.pos.Row, m.text))
		}
		return nil
	}
	if m.pos.Row >= len(t.ed.Columns()) {
		return nil
	}
	f := schemaedit.ColumnField(m.field)
	if err := t.ed.SetColumn(m.pos.Row, f, m.text); err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if r := t.ed.Columns()[m.pos.Row]; f == schemaedit.ColName && r.State() == schemaedit.Added && r.Col.Type == "" {
		t.view.SetField(int(schemaedit.ColType))
		return t.prompt(m.pos, int(schemaedit.ColType), labelColumnType, "")
	}
	return nil
}

// insertRow adds a column (asking its name) or an index (asking its
// columns) below row.
func (t *structureTab) insertRow(index bool, row int) tea.Cmd {
	if index {
		after := row
		if len(t.ed.Indexes()) == 0 {
			after = -1
		}
		at := t.ed.AddIndex(after)
		t.view.SetCursor(structure.Indexes, at)
		t.view.SetField(int(schemaedit.IdxColumns))
		return t.prompt(schemaedit.Pos{Indexes: true, Row: at}, int(schemaedit.IdxColumns), labelIndexColumn, "")
	}
	after := row
	if len(t.ed.Columns()) == 0 {
		after = -1
	}
	at := t.ed.AddColumn(after)
	t.view.SetCursor(structure.Columns, at)
	t.view.SetField(int(schemaedit.ColName))
	return t.prompt(schemaedit.Pos{Row: at}, int(schemaedit.ColName), labelColumnName, "")
}

func (t *structureTab) deleteRow(index bool, row int) {
	var err error
	switch {
	case index && row < len(t.ed.Indexes()):
		err = t.ed.DeleteIndex(row)
		t.view.SetCursor(structure.Indexes, row)
	case !index && row < len(t.ed.Columns()):
		err = t.ed.DeleteColumn(row)
		t.view.SetCursor(structure.Columns, row)
	default:
		err = errors.New("nothing here to delete")
	}
	t.report(err)
}

func (t *structureTab) undo() {
	pos, ok := t.ed.Undo()
	if !ok {
		t.setStatus("no changes to undo", false)
		return
	}
	sec := structure.Columns
	if pos.Indexes {
		sec = structure.Indexes
	}
	t.view.SetCursor(sec, pos.Row)
}

func (t *structureTab) discard() {
	n := t.changes()
	if n == 0 {
		t.setStatus("no changes to discard", false)
		return
	}
	t.ed.Reset()
	t.view.SetCursor(t.view.Section(), 0)
	t.setStatus("discarded "+plural(n, "change"), false)
}
```

`reconnect.go` 追加：

```go
func (m schemaLoadedMsg) failure() (string, error) { return m.conn, m.err }
```

- [ ] **Step 5: 改 `tabs.go` 里的 `structureTab`**

结构体与构造函数：

```go
type structureTab struct {
	id      int
	backend Backend
	store   Store
	keys    *keymap.Keymap
	conn    string
	ref     db.TableRef
	dialect sqlx.Dialect
	view    *structure.Model
	loading bool
	err     string
	height  int

	status    string // what the last key did; cleared by the next one
	statusErr bool

	// Modify Table (structedit.go)
	ed          *schemaedit.Table // nil unless the table is being modified
	ddl         db.DDLWriter      // the connection's, read with ed
	editLoading bool
	retryEdit   bool // reading the table for editing wanted a password
}

func newStructureTab(id int, b Backend, st Store, conn string, ref db.TableRef) *structureTab {
	cfg, _ := b.Connection(conn)
	d := sqlx.DialectOf(cfg.Driver)
	return &structureTab{id: id, backend: b, store: st, keys: keymap.Default(), conn: conn, ref: ref, dialect: d, view: structure.New(d)}
}
```

import 加 `"github.com/shepherd-yun/datavim/internal/schemaedit"`。把仓库里其它 `newStructureTab(` 调用（`grep -rn "newStructureTab(" internal/ui`，含测试）都补上 `store` 参数（测试里传 `newFakeStore()`）。

`SetSize`：

```go
func (t *structureTab) SetSize(w, h int) {
	t.height = max(h, 0)
	t.view.SetSize(w, max(h-1, 0))
}
```

`Update`：

```go
func (t *structureTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case structureLoadedMsg:
		t.loading = false
		if m.err != nil {
			t.err = m.err.Error()
			return nil
		}
		t.err = ""
		t.view.SetData(m.data)
	case schemaLoadedMsg:
		t.schemaLoaded(m)
	case structEditedMsg:
		return t.structEdited(m)
	}
	return nil
}
```

`Handle`：

```go
func (t *structureTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	v := t.view
	switch a {
	case keymap.Down:
		v.Scroll(1)
	case keymap.Up:
		v.Scroll(-1)
	case keymap.Left:
		v.MoveField(-1)
	case keymap.Right:
		v.MoveField(1)
	case keymap.Top:
		v.Top()
	case keymap.Bottom:
		v.Bottom()
	case keymap.HalfPageDown:
		v.HalfPage(1)
	case keymap.HalfPageUp:
		v.HalfPage(-1)
	case keymap.NextSection:
		v.NextSection()
	case keymap.PrevSection:
		v.PrevSection()
	case keymap.Refresh:
		return t.refresh()
	case keymap.ModifyTable:
		return t.toggleEdit()
	case keymap.EditCell, keymap.InsertRow, keymap.DeleteRow, keymap.UndoEdit, keymap.UndoAllEdits:
		return t.editAction(a)
	}
	return nil
}
```

`View` 与状态行：

```go
func (t *structureTab) View(bool) string {
	switch {
	case t.err != "":
		return theme.Error.Render("✗ " + textutil.Sanitize(t.err))
	case t.loading:
		return theme.Muted.Render("loading…")
	}
	return textutil.PadLines(t.view.View(), max(t.height-1, 0)) + "\n" + t.statusLine()
}

func (t *structureTab) statusLine() string {
	switch {
	case t.status != "":
		style := theme.Muted
		if t.statusErr {
			style = theme.Error
		}
		return style.Render(textutil.Sanitize(t.status))
	case t.editLoading:
		return theme.Muted.Render("reading " + t.ref.Name + " to modify it…")
	case t.ed == nil:
		return theme.Muted.Render(keyHint(t.keys, keymap.Structure, keymap.ModifyTable, "modify table"))
	}
	k := func(a keymap.Action, label string) string { return keyHint(t.keys, keymap.Structure, a, label) }
	hints := joinHints(k(keymap.EditCell, "edit"), k(keymap.InsertRow, "add"), k(keymap.DeleteRow, "delete"), k(keymap.ModifyTable, "done"))
	if n := t.changes(); n > 0 {
		return theme.Accent.Render("● "+plural(n, "change")) + theme.Muted.Render(" · "+joinHints(t.unsavedHint("submit", "discard"), hints))
	}
	return theme.Muted.Render(joinHints("modifying "+textutil.Sanitize(t.ref.Name), hints))
}
```

`retryAfterPassword` 与 `retarget`：

```go
func (t *structureTab) retryAfterPassword() tea.Cmd {
	if t.retryEdit {
		t.retryEdit = false
		return t.startEdit()
	}
	return t.refresh()
}

func (t *structureTab) retarget(ref db.TableRef) tea.Cmd {
	t.ref = ref
	return t.refresh()
}
```

（rename 在有暂存编辑时已被 `tableBlocked` 拦下，所以 `refresh` 在这里总会重读；编辑模式下会以新名字重读并继续编辑。）

- [ ] **Step 6: 运行**

Run: `go test -race ./internal/ui/...`
Expected: PASS（含 Step 1 的新测试与全部旧测试）

- [ ] **Step 7: 提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/ui
git commit -m "feat(ui): 树上 e 进入结构页编辑模式，暂存列与索引的修改"
```

---

### Task 9: 提交——预览、确认、执行

**Files:**
- Create: `internal/ui/overlay/danger.go`
- Modify: `internal/ui/overlay/preview.go`、`confirm.go`
- Modify: `internal/ui/overlay/overlay_test.go`
- Modify: `internal/ui/structedit.go`（`submit`、`apply`、`applied`、`cancelRun`）
- Modify: `internal/ui/modify.go`（`stager.submit`、`modifySubmit`、`modifyApplied`）
- Modify: `internal/ui/tabs.go`（`structureTab` 的提交字段，`Handle`/`Update` 分派）
- Modify: `internal/ui/app.go`（`Update` 的两个 case；`:w`）
- Modify: `internal/ui/reconnect.go`（`modifyAppliedMsg.failure`）
- Modify: `internal/ui/fakes_test.go`（manual 类型；`Exec` 尊重 ctx）
- Create: `internal/ui/modify_test.go`

**Interfaces:**
- Consumes: Task 1 `edit.ApplyDDL`、`DDLWriter.NormalizeType`；Task 4 `Plan`；Task 8 的 `stager`、`tableBlocked`、`structureTab` 字段
- Produces:

  ```go
  // in package overlay
  type BoxOption func(*boxOptions)
  func WithDanger(lines ...int) BoxOption // 0-based indexes into lines, before wrapping
  func NewPreview(title string, lines []string, ok tea.Msg, opts ...BoxOption) *Preview
  func NewConfirm(title string, lines []string, expect string, ok tea.Msg, opts ...BoxOption) *Confirm

  // in package ui
  type modifySubmitMsg struct { tabID, gen int; conn string; ref db.TableRef; stmts, warnings []string; changes int }
  type modifyConfirmedMsg struct{ tabID, gen int }                                          // tabMsg
  type modifyAppliedMsg struct { tabID int; conn string; ref db.TableRef; n int; err error } // tabMsg, connFailure
  func (t *structureTab) submit() tea.Cmd
  func (t *structureTab) cancelRun() tea.Cmd
  ```

- [ ] **Step 1: overlay 的 `WithDanger`——测试**

`internal/ui/overlay/overlay_test.go` 末尾追加：

```go
func TestPreviewAndConfirmMarkDangerLines(t *testing.T) {
	lines := []string{"⚠ destructive  - column legacy", "", "ALTER TABLE t DROP COLUMN legacy;"}
	p := NewPreview("Modify", lines, nil, WithDanger(0))
	v := p.View(100, 20)
	if !strings.Contains(v, theme.Error.Render(lines[0])) {
		t.Fatalf("the warning is red:\n%q", v)
	}
	if strings.Contains(v, theme.Error.Render(lines[2])) {
		t.Fatal("the statement is not")
	}
	c := NewConfirm("Modify", lines, "t", nil, WithDanger(0))
	if v := c.View(100, 20); !strings.Contains(v, theme.Error.Render(lines[0])) {
		t.Fatalf("confirm:\n%q", v)
	}
}
```

（import 补 `theme`、`strings`，若缺。）

Run: `go test ./internal/ui/overlay -run TestPreviewAndConfirmMarkDangerLines`
Expected: FAIL，`undefined: WithDanger`

- [ ] **Step 2: overlay 的 `WithDanger`——实现**

`internal/ui/overlay/danger.go`：

```go
package overlay

import (
	"strings"

	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// BoxOption changes a Preview or a Confirm.
type BoxOption func(*boxOptions)

type boxOptions struct{ danger map[int]bool }

// WithDanger shows lines (0-based, before wrapping) in the error color.
func WithDanger(lines ...int) BoxOption {
	return func(o *boxOptions) {
		if o.danger == nil {
			o.danger = map[int]bool{}
		}
		for _, l := range lines {
			o.danger[l] = true
		}
	}
}

func applyBoxOptions(opts []BoxOption) boxOptions {
	var o boxOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

// wrapLines sanitizes and wraps lines to width, coloring danger lines.
func (o boxOptions) wrapLines(lines []string, width int) []string {
	var out []string
	for i, l := range lines {
		for _, part := range strings.Split(l, "\n") {
			for _, w := range textutil.Wrap(textutil.Sanitize(part), width) {
				if o.danger[i] {
					w = theme.Error.Render(w)
				}
				out = append(out, w)
			}
		}
	}
	return out
}
```

`preview.go`：`Preview` 加字段 `opts boxOptions`；构造函数改为

```go
func NewPreview(title string, lines []string, ok tea.Msg, opts ...BoxOption) *Preview {
	return &Preview{title: title, lines: lines, ok: ok, opts: applyBoxOptions(opts)}
}
```

`View` 改为

```go
func (p *Preview) View(width, height int) string {
	inner := max(min(width-4, 120), 10) - 2
	return scrollBox(p.title+" · y submit · n cancel", p.opts.wrapLines(p.lines, inner), &p.offset, width, height, 120)
}
```

`confirm.go`：`Confirm` 加字段 `opts boxOptions`；`NewConfirm(title string, lines []string, expect string, ok tea.Msg, opts ...BoxOption)` 里 `opts: applyBoxOptions(opts)`；`View` 里构造 `body` 的循环换成 `body := c.opts.wrapLines(c.lines, w-2)`。

Run: `go test -race ./internal/ui/...`
Expected: PASS（已有调用不传 opts，行为不变）

- [ ] **Step 3: fake 的改动**

`internal/ui/fakes_test.go`：

`fakeConn.Exec` 的签名从 `(_ context.Context, …)` 改成 `(ctx context.Context, …)`，在加锁并记录 `q` 之后、查 `fail` 之前加：

```go
	if err := ctx.Err(); err != nil {
		return db.ExecResult{}, err
	}
```

`fakeDDL.AlterTable` 的 manual 条件补一种，注释同步：

```go
// AlterTable writes one line per change. Like the real writers it leaves
// generated-column changes and foreign keys to the user; a new type
// starting with "manual" stands for a change it cannot write (PostgreSQL's
// USING).
func (w fakeDDL) AlterTable(t db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	var stmts []string
	var manual []db.SchemaChange
	for _, c := range changes {
		if c.Kind == db.ForeignKeyDiff || c.Kind == db.AlterColumn && c.Attrs&db.AttrGenerated != 0 ||
			c.Kind == db.AlterColumn && c.Attrs&db.AttrType != 0 && strings.HasPrefix(c.New.Type, "manual") {
			manual = append(manual, c)
			continue
		}
		stmts = append(stmts, "ALTER TABLE "+w.d.QualifiedName(t)+" "+c.Kind.String()+" "+fakeChangeName(c))
	}
	return stmts, manual
}
```

- [ ] **Step 4: 写提交的测试**

`internal/ui/modify_test.go`：

```go
package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

const renameName = "ALTER TABLE `app`.`users` RenameColumn name_x"

// renameNameColumn modifies conn's users and renames name to name_x.
func renameNameColumn(t *testing.T, m *Model, b *fakeBackend, conn string) *structureTab {
	t.Helper()
	st := modifyUsers(t, m, b, conn)
	press(t, m, "j", "i")
	typeText(t, m, "_x")
	press(t, m, "enter")
	return st
}

func count(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

func TestModifySubmitPreviewsAndRuns(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	press(t, m, "space", "w")
	s := screen(m)
	for _, want := range []string{"Modify table users · 1 change", renameName + ";"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	press(t, m, "y")
	execs := b.conn.execList()
	if count(execs, renameName) != 1 || slices.Contains(execs, sqlx.BeginSQL(sqlx.MySQL)) {
		t.Fatalf("execs = %q (MySQL runs DDL outside a transaction)", execs)
	}
	if st.ed != nil {
		t.Fatal("a successful submit stops modifying")
	}
	if !strings.Contains(screen(m), "modified users") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestColonWSubmitsStructureEdits(t *testing.T) {
	m, b := newTestModel(t)
	renameNameColumn(t, m, b, "local")
	command(t, m, "w")
	if !strings.Contains(screen(m), renameName+";") {
		t.Fatalf(":w previews:\n%s", screen(m))
	}
}

func TestModifyOnProdNeedsTheNameAndOneTransaction(t *testing.T) {
	m, b := newTestModel(t)
	renameNameColumn(t, m, b, "prod")
	press(t, m, "space", "w")
	if !strings.Contains(screen(m), "PROD · Modify table users") || !strings.Contains(screen(m), "Type users to confirm") {
		t.Fatalf("prod asks for the name:\n%s", screen(m))
	}
	press(t, m, "y", "enter")
	if slices.Contains(b.conn.execList(), renameName) {
		t.Fatal("y alone ran it")
	}
	press(t, m, "ctrl+u") // the y went into the name field
	typeText(t, m, "users")
	press(t, m, "enter")
	execs := b.conn.execList()
	at := slices.Index(execs, renameName)
	if at < 1 || execs[at-1] != "BEGIN" || at+1 >= len(execs) || execs[at+1] != "COMMIT" {
		t.Fatalf("execs = %q (PostgreSQL runs DDL in a transaction)", execs)
	}
}

func TestModifyPreviewNamesRisks(t *testing.T) {
	m, b := newTestModel(t)
	modifyUsers(t, m, b, "local")
	press(t, m, "j", "d", "d", "space", "w")
	if !strings.Contains(screen(m), "⚠ destructive  - column name") {
		t.Fatalf("preview:\n%s", screen(m))
	}
}

func TestModifyRefusesManualChanges(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "j", "l", "i")
	press(t, m, "ctrl+u")
	typeText(t, m, "manual_type")
	press(t, m, "enter", "space", "w")
	if m.overlay != nil {
		t.Fatalf("nothing to confirm:\n%s", screen(m))
	}
	if !strings.Contains(screen(m), "datavim cannot write") || st.changes() != 1 {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestModifyFailureKeepsEdits(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	b.conn.setFail(renameName, errors.New("boom"))
	press(t, m, "space", "w", "y")
	if st.ed == nil || st.changes() != 1 {
		t.Fatal("the edits are kept")
	}
	if s := screen(m); !strings.Contains(s, "changes kept") || !strings.Contains(s, "boom") {
		t.Fatalf("status:\n%s", s)
	}
	if count(b.conn.execList(), renameName) != 1 {
		t.Fatal("no retry")
	}
}

func TestModifyConnLostIsNotRetried(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	b.conn.setFail(renameName, fmt.Errorf("read: %w", db.ErrConnLost))
	press(t, m, "space", "w", "y")
	if count(b.conn.execList(), renameName) != 1 || st.changes() != 1 {
		t.Fatalf("execs = %q", b.conn.execList())
	}
	if !slices.Contains(b.resetList(), "local") {
		t.Fatal("the pool is reset")
	}
}

func TestModifyConfirmRunsOnce(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	b.conn.setFail(renameName, errors.New("boom")) // keep the edits so a second run could happen
	press(t, m, "space", "w")
	drain(t, m, st.Update(modifyConfirmedMsg{tabID: st.id, gen: st.gen}))
	drain(t, m, st.Update(modifyConfirmedMsg{tabID: st.id, gen: st.gen}))
	if n := count(b.conn.execList(), renameName); n != 1 {
		t.Fatalf("ran %d times", n)
	}
}

func TestModifyCancelKeepsEdits(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	cmd := st.apply([]string{renameName})
	st.cancelRun()
	_, next := m.Update(cmd())
	drain(t, m, next)
	if st.changes() != 1 || st.submitting || !strings.Contains(screen(m), "cancelled · changes kept") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestModifyBlockedByUnsavedDataEdits(t *testing.T) {
	m, b := newTestModel(t)
	openUsers(t, m)
	press(t, m, "o") // stage a new row in the data tab
	st := renameNameColumn(t, m, b, "local")
	press(t, m, "space", "w")
	if m.overlay != nil || !strings.Contains(screen(m), "users has 1 unsaved change") {
		t.Fatalf("blocked:\n%s", screen(m))
	}
	if slices.Contains(b.conn.execList(), renameName) || st.changes() != 1 {
		t.Fatal("nothing ran")
	}
}
```

`TestModifyRefusesManualChanges` 里 `ctrl+u` 清空提示框原有的类型文本（v1.0 起文本框里 `Ctrl-u` 删到行首）；类型 `manual_type` 在 fake 里代表"写不出"。

- [ ] **Step 5: 运行，确认失败**

Run: `go test ./internal/ui -run TestModify`
Expected: FAIL（`undefined: modifyConfirmedMsg` 等）

- [ ] **Step 6: 实现提交**

`structureTab`（`tabs.go`）在 Modify Table 字段下追加：

```go
	submitting  bool
	retrySubmit bool               // the last submit wanted a password
	gen         int                // tells the current preview from stale ones
	pendingSQL  []string           // the previewed statements, until confirmed
	cancel      context.CancelFunc // stops a running submit
```

`Handle` 的编辑键 case 里加上 `keymap.SubmitEdits`：

```go
	case keymap.SubmitEdits:
		return t.submit()
```

`Update` 加：

```go
	case modifyConfirmedMsg:
		if m.gen == t.gen && t.pendingSQL != nil && !t.submitting {
			stmts := t.pendingSQL
			t.pendingSQL = nil
			return t.apply(stmts)
		}
	case modifyAppliedMsg:
		return t.applied(m)
```

`retryAfterPassword` 开头加：

```go
	if t.retrySubmit {
		t.retrySubmit = false
		return t.submit()
	}
```

`structedit.go` 追加（import 补 `"log/slog"`、`"strings"`、`"time"`、`edit`、`history`、`schemadiff`）：

```go
type (
	// modifySubmitMsg asks the root model to check a Modify Table submit
	// and preview it.
	modifySubmitMsg struct {
		tabID, gen int
		conn       string
		ref        db.TableRef
		stmts      []string
		warnings   []string // the destructive and lossy changes, one line each
		changes    int
	}
	// modifyConfirmedMsg is the go-ahead for the previewed statements.
	modifyConfirmedMsg struct{ tabID, gen int }
	// modifyAppliedMsg is the outcome of a Modify Table submit.
	modifyAppliedMsg struct {
		tabID int
		conn  string
		ref   db.TableRef
		n     int // statements
		err   error
	}
)

func (m modifyConfirmedMsg) TabID() int { return m.tabID }
func (m modifyAppliedMsg) TabID() int   { return m.tabID }

// submit plans the staged edits, has the connection's dialect write them
// and asks the root model to preview them. Edits the dialect cannot write
// stop it before anything runs.
func (t *structureTab) submit() tea.Cmd {
	switch {
	case t.ed == nil:
		t.setStatus("nothing to submit: the table is not being modified", true)
		return nil
	case t.submitting:
		t.setStatus("submitting changes…", true)
		return nil
	case t.changes() == 0:
		t.setStatus("no changes to submit", false)
		return nil
	}
	changes, err := t.ed.Plan(t.ddl.NormalizeType)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if len(changes) == 0 {
		t.setStatus("no changes to submit", false)
		return nil
	}
	stmts, manual := t.ddl.AlterTable(t.ref, changes)
	if len(manual) > 0 {
		t.setStatus("datavim cannot write "+schemadiff.Describe(manual[0])+" · undo it, or make that change in a console", true)
		return nil
	}
	var warnings []string
	for _, c := range changes {
		switch {
		case c.Risk&db.Destructive != 0:
			warnings = append(warnings, "⚠ destructive  "+schemadiff.Describe(c))
		case c.Risk&db.Lossy != 0:
			warnings = append(warnings, "⚠ lossy  "+schemadiff.Describe(c))
		}
	}
	t.gen++
	t.pendingSQL = stmts
	msg := modifySubmitMsg{tabID: t.id, gen: t.gen, conn: t.conn, ref: t.ref, stmts: stmts, warnings: warnings, changes: t.changes()}
	return func() tea.Msg { return msg }
}

// apply runs the confirmed statements on a session of their own (never a
// console's): PostgreSQL in one transaction, MySQL one by one. DDL on a
// big table can run for minutes, so there is no timeout; Ctrl-c cancels.
// It never retries: a dropped connection may or may not have run them.
func (t *structureTab) apply(stmts []string) tea.Cmd {
	t.submitting = true
	t.setStatus(joinHints("modifying "+t.ref.Name+"…", keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels")), false)
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	id, b, st, conn, ref, d := t.id, t.backend, t.store, t.conn, t.ref, t.dialect
	return func() tea.Msg {
		defer cancel()
		msg := modifyAppliedMsg{tabID: id, conn: conn, ref: ref, n: len(stmts)}
		c, err := b.Conn(ctx, conn, ref.Database)
		if err == nil {
			var s db.Session
			if s, err = c.Session(ctx); err == nil {
				start := time.Now()
				err = edit.ApplyDDL(ctx, s, d, stmts)
				_ = s.Close()
				entry := history.Entry{
					Time: start, Conn: conn, Database: ref.Database,
					SQL: strings.Join(stmts, ";\n") + ";", Duration: time.Since(start),
				}
				if err != nil {
					entry.Err = errorText(err)
				}
				if herr := st.AddHistory(entry); herr != nil {
					slog.Warn("save history", "err", herr)
				}
			}
		}
		if db.IsConnLost(err) {
			b.Reset(conn)
		}
		msg.err = err
		return msg
	}
}

// cancelRun is Ctrl-c: it stops a running submit.
func (t *structureTab) cancelRun() tea.Cmd {
	if t.submitting && t.cancel != nil {
		t.cancel()
		return nil
	}
	t.setStatus("nothing is running", false)
	return nil
}

// applied stops modifying and reloads after a submit, or keeps the edits
// staged when it failed.
func (t *structureTab) applied(m modifyAppliedMsg) tea.Cmd {
	t.submitting, t.cancel = false, nil
	switch {
	case errors.Is(m.err, context.Canceled):
		t.setStatus("cancelled · changes kept", true)
		return nil
	case m.err != nil:
		t.retrySubmit = errors.Is(m.err, session.ErrPasswordRequired)
		// "changes kept" first: the tab's status line is narrow.
		t.setStatus("✗ changes kept · "+errorText(m.err), true)
		return nil
	}
	t.stopEdit()
	t.setStatus("modified "+t.ref.Name+" · "+plural(m.n, "statement"), false)
	return t.load()
}
```

`structureTab.load()` 目前不清状态，`applied` 设的状态会留在状态行上直到下一个按键，这是想要的。

`reconnect.go` 追加：

```go
func (m modifyAppliedMsg) failure() (string, error) { return m.conn, m.err }
```

`modify.go`：`stager` 接口加一行 `submit() tea.Cmd`（`dataTab.submit` 已有），并追加（import 补 `slices`、`config`、`runner`、`sqlx`、`overlay`）：

```go
// modifySubmit checks a Modify Table submit and previews it: prod wants
// the table's name typed, other connections y. The destructive and lossy
// changes come first, in red.
func (m *Model) modifySubmit(msg modifySubmitMsg) {
	self := m.tabByID(msg.tabID)
	if self == nil || m.overlay != nil {
		return // closed, or the user opened something else meanwhile
	}
	if why := m.tableBlocked(msg.conn, msg.ref, self); why != "" {
		m.setStatus(why, true)
		return
	}
	cfg, _ := m.backend.Connection(msg.conn)
	policy := runner.Policy{ReadOnly: cfg.IsReadOnly(), Prod: cfg.Env == config.EnvProd}
	batch := make([]sqlx.Statement, len(msg.stmts))
	for i, s := range msg.stmts {
		batch[i] = sqlx.Statement{Text: s}
	}
	if chk := runner.Guard(batch, sqlx.DialectOf(cfg.Driver), policy); chk.Verdict == runner.Reject {
		m.setStatus(chk.Reason, true)
		return
	}
	title := "Modify table " + msg.ref.Name + " · " + plural(msg.changes, "change")
	if policy.Prod {
		title = "PROD · " + title
	}
	lines := slices.Clone(msg.warnings)
	danger := make([]int, len(lines))
	for i := range danger {
		danger[i] = i
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	for _, s := range msg.stmts {
		lines = append(lines, s+";")
	}
	ok := modifyConfirmedMsg{tabID: msg.tabID, gen: msg.gen}
	if !policy.Prod {
		m.overlay = overlay.NewPreview(title, lines, ok, overlay.WithDanger(danger...))
		return
	}
	lines = append(lines, "", "Type "+msg.ref.Name+" to confirm.")
	m.overlay = overlay.NewConfirm(title, lines, msg.ref.Name, ok, overlay.WithDanger(danger...))
}

// modifyApplied brings the catalog and the table's data tabs up to date
// after a successful submit, then lets the structure tab finish.
func (m *Model) modifyApplied(msg modifyAppliedMsg) tea.Cmd {
	var cmds []tea.Cmd
	if msg.err == nil {
		cmds = append(cmds,
			m.metaStale(metaStaleMsg{conn: msg.conn, database: msg.ref.Database}),
			m.refreshTabsOfTable(msg.conn, msg.ref))
	}
	if t := m.tabByID(msg.tabID); t != nil {
		cmds = append(cmds, t.Update(msg))
	}
	return tea.Batch(cmds...)
}
```

`app.go` 的 `Update`，在 `case tableOpDoneMsg:` 之后加（必须在通用的 `case tabMsg:` 之前）：

```go
	case modifySubmitMsg:
		m.modifySubmit(msg)
	case modifyAppliedMsg:
		return m, m.modifyApplied(msg)
```

`:w` 改成：

```go
	case "w", "write":
		if s, ok := m.activeTab().(stager); ok {
			return s.submit()
		}
		m.setStatus("nothing to write: the active tab is not table data or a table being modified", true)
```

- [ ] **Step 7: 运行**

Run: `go test -race ./internal/ui/...`
Expected: PASS


- [ ] **Step 8: 提交**

```bash
gofumpt -l . && go vet ./... && go test -race ./...
git add internal/ui
git commit -m "feat(ui): 结构页 <Space>w 预览并执行 ALTER，失败保留编辑，Ctrl-c 可取消"
```

---

### Task 10: 文档

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `README.md`、`README.zh-CN.md`
- Modify: `docs/superpowers/specs/2026-10-15-table-ops-compare-design.md`（新增 §8.2）

- [ ] **Step 1: CHANGELOG**

`[Unreleased]` 的 `### Added` 末尾加：

```markdown
- Modify Table: `e` on a table in the tree, or `e` in its structure tab, edits the columns
  (name, type, NULL, default, comment) and indexes (name, kind, columns) with the data grid's
  keys: `i` edits the field under the cursor (`h` / `l` move between fields), `o` adds a column
  or index, `dd` deletes, `u` / `U` undo. `<Space>w` (or `:w`) previews the ALTER with the
  destructive and lossy changes in red and runs it — PostgreSQL in one transaction — once you
  press `y`, or type the table's name on a prod connection. A failed or cancelled (`Ctrl-c`)
  submit keeps the edits. Foreign keys, column order, generated and auto-increment columns are
  left to a console.
```

`### Changed` 末尾加：

```markdown
- The action ID `conn.edit` is now `node.edit` (`e` in the tree edits a connection or modifies
  a table); `keymap.toml` files that use the old ID keep working.
- The structure tab has a cursor: `j` / `k` move over the rows of columns, indexes and foreign
  keys, and a hint line at the bottom.
```

- [ ] **Step 2: README（中英两份）**

`README.md` 的按键表里，把

```markdown
| `e` | Tree: edit the selected connection (saved to `connections.toml`) |
```

换成

```markdown
| `e` | Tree: edit the selected connection (saved to `connections.toml`), or modify the selected table |
| `e` | Structure: modify the table — `i` edits a field (`h` / `l` pick it), `o` adds a column or index, `dd` deletes, `u` / `U` undo, `<Space>w` previews and runs the ALTER |
```

`README.zh-CN.md` 对应的

```markdown
| `e` | 树：编辑选中的连接（保存到 `connections.toml`） |
```

换成

```markdown
| `e` | 树：编辑选中的连接（保存到 `connections.toml`），或修改选中的表 |
| `e` | 结构页：修改表——`i` 编辑字段（`h` / `l` 选择字段），`o` 新增列或索引，`dd` 删除，`u` / `U` 撤销，`<Space>w` 预览并执行 ALTER |
```

- [ ] **Step 3: spec §8.2**

在 spec 的 `## 8.1 第 2 阶段的实现调整` 一节之后、`## 9. 待定` 之前加：

```markdown
## 8.2 第 3 阶段的实现调整

第 3 阶段（v1.4）实现时对上文做了这些调整，后续阶段以此为准：

- 暂存的编辑放在新的纯函数包 `internal/schemaedit`（不在结构页里）：`Plan` 的列变更走 `DiffTable(编辑前, 编辑后, renames)`；索引按"从哪一行编辑而来"配对（`schemadiff.DiffIndexEdits`），只改索引名也生成 Drop + Add。
- `DDLWriter` 新增 `NormalizeType`（转调驱动已有的类型归一化），让键入的类型与读出的类型可比较；这是本阶段唯一的驱动改动。
- 执行用 `edit.ApplyDDL`，在一个新的 Session 上：PG 一个事务，MySQL 逐条执行（只有一条 ALTER）。不设超时，`Ctrl-c` 取消。
- 结构页自己也有 `e`（进入 / 退出编辑模式），树上的 `e` 动作 ID 改为 `node.edit`（兼容 `conn.edit`）。结构页有了行光标，编辑模式下 `h` / `l` 在字段间移动。
- 不能编辑：生成列的类型与默认值、自增列的默认值、排序规则、`ON UPDATE`（保留原值）；表注释与表选项不在编辑范围内。新索引的名字由表名、列名与种类生成（`users_email_key`），手动改过名后不再跟随。
- 提交前拒绝：删除仍被索引使用的列；同一次提交里让一个现有列接过另一个被改名或删除的列的旧名字；驱动写不出的变更（manual）。
```

如果 Task 5 Step 2 走了"`After` 换回旧名"的分支，在这一节末尾再加一条说明。

- [ ] **Step 4: 提交**

```bash
git add CHANGELOG.md README.md README.zh-CN.md docs/superpowers/specs/2026-10-15-table-ops-compare-design.md
git commit -m "docs: 记录 Modify Table（结构页编辑模式）"
```

---

## 完成标准

- `make test`、`make lint`、`make test-integration` 全绿。
- 手动验收（`make run`，MySQL 与 PG 各一次）：树上对表按 `e` → 改列名与类型、加一列、删一列、改一个索引 → `<Space>w` 预览里危险项标红 → `y` 执行 → 结构页回到浏览、数据页列名更新；prod 连接要输入表名；故意写错类型时报错且编辑保留。
