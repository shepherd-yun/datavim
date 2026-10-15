# 数据对比 第 4 阶段（v1.5）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Diff 标签页按 `D` 按键列对比两张表的数据，列出 `+` / `-` / `~` 差异行，可勾选、交换两边、用 `F` 设两边共用的 WHERE，`<Space>w` 把 INSERT / UPDATE / DELETE 脚本放进目标连接的新 console。

**Architecture:** 新增纯函数包 `internal/datadiff`：`Spec` 决定比较哪些列、用哪组键列配对（主键，否则 unique NOT NULL 索引，两边必须有同一组键），并写出两边共用的 SELECT；`Source` 把源端的行按键的文本建哈希表，`Matcher` 流式读目标端逐行比对，最后剩下的就是仅源有的行；`Spec.Script` 把勾选的差异行交给 `edit.Table` + `edit.Plan` 生成 DML（复用数据编辑的 WHERE 定位与字面量）。UI 侧 `diffTab` 新增 `dataView`（`difftab_data.go`），`D` 在结构视图与数据视图之间切换；读取走 `withConn`，每次尝试都新建 `Source` / `Matcher`，断线重试不会重复计数。生成的脚本走第 2 阶段已有的 `scriptConsoleMsg`，readonly / prod / 事务检查都由 console 负责。

**Tech Stack:** Go、Bubble Tea v2（`charm.land/bubbletea/v2`）、Lip Gloss v2、pgx v5、go-sql-driver/mysql、testcontainers（集成测试）、go-cmp（测试）；不新增依赖。

**Spec:** `docs/superpowers/specs/2026-10-15-table-ops-compare-design.md`（本 plan 实现 §3.2 的 `internal/datadiff`、§5.3 的 `D`、§5.4、§6 中对比的条目、§7 的 `datadiff` 测试、§8 第 4 阶段；第 2、3 阶段的调整见 spec §8.1、§8.2，本阶段的调整在 Task 8 写进 spec §8.3）

## Global Constraints

- **UI 文案用英文。** 提示里的按键一律用 `keyHint` 生成，不写死键名；新按键全部加进 `keymap/defaults.go`。
- UI 层不做 IO：所有数据库访问都在 `tea.Cmd` 里，带可取消的 `context.Context`（复用 `compareTimeout`），`Ctrl-c` 能取消。
- 方言差异只在 `internal/db/*` 与 `internal/sqlx` 里。本阶段**不改驱动**；`datadiff` 只用 `sqlx.QuoteIdent` / `sqlx.QualifiedName` 拼 SELECT，DML 由 `edit.Plan` 写。
- 依赖方向：`ui` → `datadiff` → `edit` → `sqlx` → `db`。`datadiff` 不 import 任何 `ui` 包。
- 只做同引擎、表对表的数据对比；视图不比较。没有主键、也没有 unique NOT NULL 索引的表直接拒绝（spec §2）。
- 不用服务端 ORDER BY 后归并（两边排序规则可能不同）；源端全读进哈希表，目标端流式比对。每边最多读 `compare_max_rows` 行（`config.toml`，默认 100000），超过就中止并提示用 `F` 缩小范围，**不做只覆盖部分数据的对比**。
- 值的比较用 `db.Value`：`Null` 与空串不同；文本按服务端返回的文本逐字节比较。
- `-`（仅目标有，生成 DELETE）默认不勾选，`+` 与 `~` 默认勾选。每条 UPDATE / DELETE 都带键列的 WHERE。
- 脚本只放进 console，不直接执行。
- 不新增依赖；`go.mod` 不变。
- 每个任务结束时：`gofumpt -l .` 无输出、`go vet ./...` 通过、`go test -race ./...` 全绿；改到 `internal/db` 或 `dbtest` 的任务还要跑 `make test-integration`（需要 Docker；OrbStack 先 `orb start`）。
- Conventional Commits，带 scope，描述用中文（与仓库历史一致）。**commit message 不加 `Co-Authored-By` 行。**
- 每个用户可见的改动在 `CHANGELOG.md` 的 `[Unreleased]` 下记一行；README 的改动同时改 `README.zh-CN.md`。

## Review Focus

1. **读到一半连接断了**：`withConn` 在 `ErrConnLost` 后会重连并把整个回调再跑一遍。如果 `Source` / `Matcher` 建在回调外面，第二遍会把已读的行再加一次，报"键出现两次"或算错差异。每次尝试必须从头开始。→ Task 2 `TestMatchStartsOverEachPass`，Task 6 `TestDataViewStartsASideOverAfterReconnect`（fake 在流到第 2 行时断线）。
2. **NULL 与空串、驱动复用行切片**：`''` 与 `NULL` 是不同的值，生成的 UPDATE 要写出 `NULL` 或 `''`；驱动的 `onRow` 可能复用同一个切片，存进哈希表前必须复制。→ Task 2 `TestCompare`（id 5：`''` 对 `NULL`）、`TestAddCopiesTheRow`，Task 3 `TestScript`，Task 4 的 contract 往返测试。
3. **行数上限的边界与两边**：恰好等于上限要能比较，多一行就中止；源端和目标端都要检查；错误要说清是哪一边、指向 `F`。→ Task 2 `TestRowLimit`，Task 6 `TestDataViewRowLimit`，Task 4 contract 里的上限断言。
4. **`F` 里输入的条件**：`1=1; DELETE FROM …` 这种把 SELECT 变成多条语句或写语句的输入，必须在发出任何查询之前拒绝；尾部的 `-- 注释` 不能吞掉后面的 SQL（条件单独占一行再闭合括号）。→ Task 1 `TestQuery` 的注释用例，Task 7 `TestDataViewFilterRejectsASecondStatement`。
5. **键的判定**：只看列名不够——源端的键在目标端也必须是主键或 unique NOT NULL 索引；可空列上的唯一索引、PG 的部分唯一索引（`Where != ""`）都不能当键；生成列不比较也不写。→ Task 1 `TestNewSpec` 的全部用例，Task 6 `TestDataViewNeedsAKey`。

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/datadiff/spec.go`（新） | `Spec`、`NewSpec`、`ErrNoKey`、`Query`、`KeyIndexes`、`Spec.Swap` | 1 |
| `internal/datadiff/compare.go`（新） | `Status`、`Row`、`Source`、`Matcher`、`TooManyRowsError`、`ErrDuplicateKey`、`Count`、`SwapRows`、按键排序 | 2 |
| `internal/datadiff/script.go`（新） | `Spec.Script`：差异行 → `edit.Table` → `edit.Plan` | 3 |
| `internal/db/dbtest/contract.go` | `Fixture.DataCompareSQL`；数据同步往返子测试 | 4 |
| `internal/db/mysql/integration_test.go`、`internal/db/postgres/integration_test.go` | `DataCompareSQL` fixture | 4 |
| `internal/ui/keymap/keymap.go`、`defaults.go`、`keymap_test.go` | `diff.data`（`D`）、Diff 上下文的 `F` | 5 |
| `internal/config/config.go`、`config_test.go`、`docs/examples/config.toml` | `compare_max_rows` | 6 |
| `internal/ui/difftab_data.go`（新） | `dataView`：读取、渲染、移动、列滚动、勾选、交换、过滤、脚本 | 6、7 |
| `internal/ui/difftab.go` | `maxRows`、`data` 字段；`Handle` / `Update` / `View` / `cancelRun` / `statusLine` 分派到数据视图 | 6、7 |
| `internal/ui/app.go` | `openDiff` 传入 `compare_max_rows` | 6 |
| `internal/ui/tablequery.go`、`internal/ui/tabs.go` | 抽出 `checkSelect`、`completeCondition` 供两处共用 | 7 |
| `internal/ui/fakes_test.go` | fake 的表数据、阻塞、流中断线 | 6 |
| `internal/ui/difftab_data_test.go`（新） | 数据视图的 UI 测试 | 6、7 |
| `CHANGELOG.md`、`README.md`、`README.zh-CN.md`、spec §8.3 | 文档 | 8 |

---

### Task 1: `datadiff.Spec`——比较哪些列、用哪组键

**Files:**
- Create: `internal/datadiff/spec.go`
- Test: `internal/datadiff/spec_test.go`

**Interfaces:**
- Consumes: `db.TableSchema`、`db.Column`（`Nullable`、`PrimaryKey`、`Generated`）、`db.Index`（`Unique`、`Primary`、`Where`）、`sqlx.QuoteIdent`、`sqlx.QualifiedName`。
- Produces（Task 2、3、4、6、7 使用）：

  ```go
  package datadiff

  var ErrNoKey = errors.New("rows cannot be paired")

  type Spec struct {
      Columns    []string // compared, in the source's order; the key is among them
      Key        []string
      SourceOnly []string // columns only the source has
      TargetOnly []string // columns only the target has
  }

  func NewSpec(src, tgt db.TableSchema) (Spec, error)       // errors wrap ErrNoKey
  func (s Spec) Query(d sqlx.Dialect, t db.TableRef, where string) string
  func (s Spec) KeyIndexes() []int                          // positions of Key in Columns
  func (s Spec) Swap() Spec                                 // SourceOnly <-> TargetOnly
  ```

- [ ] **Step 1: 写失败的测试**

`internal/datadiff/spec_test.go`：

```go
package datadiff

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

func col(name string, nullable bool) db.Column {
	return db.Column{Name: name, Type: "varchar(50)", Nullable: nullable}
}

func pk(name string) db.Column { return db.Column{Name: name, Type: "bigint", PrimaryKey: true} }

func table(name string, idx []db.Index, cols ...db.Column) db.TableSchema {
	return db.TableSchema{Ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: name}, Columns: cols, Indexes: idx}
}

var primary = []db.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}}

func unique(name string, cols ...string) db.Index { return db.Index{Name: name, Columns: cols, Unique: true} }

func TestNewSpec(t *testing.T) {
	tests := []struct {
		name     string
		src, tgt db.TableSchema
		want     Spec
		err      string
	}{
		{
			"same columns",
			table("a", primary, pk("id"), col("name", false)),
			table("b", primary, pk("id"), col("name", false)),
			Spec{Columns: []string{"id", "name"}, Key: []string{"id"}}, "",
		},
		{
			"columns only one side has are left out, in the source's order",
			table("a", primary, pk("id"), col("email", true), col("name", false)),
			table("b", primary, col("name", false), pk("id"), col("legacy", true)),
			Spec{Columns: []string{"id", "name"}, Key: []string{"id"}, SourceOnly: []string{"email"}, TargetOnly: []string{"legacy"}}, "",
		},
		{
			"generated columns are neither compared nor written",
			table("a", primary, pk("id"), db.Column{Name: "total", Type: "int", Generated: "a + b"}),
			table("b", primary, pk("id"), db.Column{Name: "total", Type: "int", Nullable: true}),
			Spec{Columns: []string{"id"}, Key: []string{"id"}}, "",
		},
		{
			"a unique NOT NULL index stands in for the primary key",
			table("a", []db.Index{unique("a_code", "code")}, col("code", false), col("name", true)),
			table("b", []db.Index{unique("b_code", "code")}, col("code", false), col("name", true)),
			Spec{Columns: []string{"code", "name"}, Key: []string{"code"}}, "",
		},
		{
			"the target may have the key as another of its keys",
			table("a", []db.Index{unique("a_code", "code")}, col("code", false)),
			table("b", append([]db.Index{unique("b_code", "code")}, primary...), pk("id"), col("code", false)),
			Spec{Columns: []string{"code"}, Key: []string{"code"}, TargetOnly: []string{"id"}}, "",
		},
		{
			"no key",
			table("a", nil, col("id", false)), table("b", primary, pk("id")),
			Spec{}, "a has no primary key or unique NOT NULL index",
		},
		{
			"a unique index over a nullable column is no key",
			table("a", []db.Index{unique("a_email", "email")}, col("email", true)),
			table("b", []db.Index{unique("b_email", "email")}, col("email", true)),
			Spec{}, "a has no primary key or unique NOT NULL index",
		},
		{
			"a partial unique index is no key",
			table("a", []db.Index{{Name: "a_code", Columns: []string{"code"}, Unique: true, Where: "code > 0"}}, col("code", false)),
			table("b", []db.Index{unique("b_code", "code")}, col("code", false)),
			Spec{}, "a has no primary key or unique NOT NULL index",
		},
		{
			"the target lacks the source's key",
			table("a", primary, pk("id")),
			table("b", []db.Index{unique("b_code", "code")}, col("id", false), col("code", false)),
			Spec{}, "b has no primary key or unique NOT NULL index on (id), the key of a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewSpec(tt.src, tt.tgt)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) || !errors.Is(err, ErrNoKey) {
					t.Fatalf("err = %v, want %q wrapping ErrNoKey", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestQuery(t *testing.T) {
	s := Spec{Columns: []string{"id", "Odd Col"}, Key: []string{"id"}}
	my := db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}
	pg := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}
	for _, tt := range []struct {
		d     sqlx.Dialect
		ref   db.TableRef
		where string
		want  string
	}{
		{sqlx.MySQL, my, "", "SELECT `id`, `Odd Col` FROM `app`.`users`"},
		{sqlx.Postgres, pg, "", `SELECT "id", "Odd Col" FROM "public"."users"`},
		// The condition is closed on its own line: a trailing comment cannot
		// swallow the parenthesis.
		{sqlx.MySQL, my, "id < 3 -- small", "SELECT `id`, `Odd Col` FROM `app`.`users` WHERE (id < 3 -- small\n)"},
	} {
		if got := s.Query(tt.d, tt.ref, tt.where); got != tt.want {
			t.Errorf("Query(%q) = %q, want %q", tt.where, got, tt.want)
		}
	}
}

func TestKeyIndexesAndSwap(t *testing.T) {
	s := Spec{Columns: []string{"a", "id", "b", "code"}, Key: []string{"code", "id"}, SourceOnly: []string{"x"}, TargetOnly: []string{"y"}}
	if diff := cmp.Diff([]int{3, 1}, s.KeyIndexes()); diff != "" {
		t.Fatalf("KeyIndexes (-want +got):\n%s", diff)
	}
	w := s.Swap()
	if diff := cmp.Diff(Spec{Columns: s.Columns, Key: s.Key, SourceOnly: []string{"y"}, TargetOnly: []string{"x"}}, w); diff != "" {
		t.Fatalf("Swap (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/datadiff/`
Expected: 编译失败，`undefined: NewSpec`、`undefined: Spec` 等。

- [ ] **Step 3: 实现**

`internal/datadiff/spec.go`：

```go
// Package datadiff compares the rows of two tables by their key: the rows
// only the source has, the rows only the target has and the rows whose
// values differ, and writes the statements that make the target's rows
// like the source's. It has no terminal or server code.
package datadiff

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// ErrNoKey means the rows of the two tables cannot be paired.
var ErrNoKey = errors.New("rows cannot be paired")

// Spec is what a comparison reads: the columns both tables have and the
// key that pairs their rows.
type Spec struct {
	Columns    []string // compared, in the source's order; the key is among them
	Key        []string
	SourceOnly []string // columns only the source has
	TargetOnly []string // columns only the target has
}

// NewSpec decides how src and tgt compare. The key is the source's primary
// key, else its first unique index over NOT NULL columns (the rule of
// edit.ChooseKey), and the target must have the same columns as one of its
// keys. Generated columns are left out: they follow from the others and
// cannot be written.
func NewSpec(src, tgt db.TableSchema) (Spec, error) {
	sk := keys(src)
	if len(sk) == 0 {
		return Spec{}, fmt.Errorf("%s has no primary key or unique NOT NULL index: %w", src.Ref.Name, ErrNoKey)
	}
	key := sk[0]
	if !slices.ContainsFunc(keys(tgt), func(k []string) bool { return slices.Equal(k, key) }) {
		return Spec{}, fmt.Errorf("%s has no primary key or unique NOT NULL index on (%s), the key of %s: %w",
			tgt.Ref.Name, strings.Join(key, ", "), src.Ref.Name, ErrNoKey)
	}
	s := Spec{Key: slices.Clone(key)}
	tcols := make(map[string]db.Column, len(tgt.Columns))
	for _, c := range tgt.Columns {
		tcols[c.Name] = c
	}
	inSource := make(map[string]bool, len(src.Columns))
	for _, c := range src.Columns {
		inSource[c.Name] = true
		tc, ok := tcols[c.Name]
		switch {
		case !ok:
			s.SourceOnly = append(s.SourceOnly, c.Name)
		case (c.Generated != "" || tc.Generated != "") && !slices.Contains(key, c.Name):
			// follows from the other columns and cannot be written
		default:
			s.Columns = append(s.Columns, c.Name)
		}
	}
	for _, c := range tgt.Columns {
		if !inSource[c.Name] {
			s.TargetOnly = append(s.TargetOnly, c.Name)
		}
	}
	return s, nil
}

// keys lists the column lists that locate one row of s: its primary key,
// then each unique, non-partial index over NOT NULL columns.
func keys(s db.TableSchema) [][]string {
	nullable := make(map[string]bool, len(s.Columns))
	var pk []string
	for _, c := range s.Columns {
		nullable[c.Name] = c.Nullable
		if c.PrimaryKey {
			pk = append(pk, c.Name)
		}
	}
	for _, ix := range s.Indexes {
		if ix.Primary && len(ix.Columns) > 0 {
			pk = ix.Columns
		}
	}
	var out [][]string
	if len(pk) > 0 {
		out = append(out, pk)
	}
	for _, ix := range s.Indexes {
		if ix.Primary || !ix.Unique || ix.Where != "" || !notNull(ix.Columns, nullable) {
			continue
		}
		out = append(out, ix.Columns)
	}
	return out
}

// notNull reports names that are all known NOT NULL columns.
func notNull(names []string, nullable map[string]bool) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if null, known := nullable[n]; !known || null {
			return false
		}
	}
	return true
}

// Query selects the compared columns of t, narrowed by where (a condition
// without WHERE; "" reads every row). The condition is closed on its own
// line, so a trailing -- comment cannot swallow the parenthesis.
func (s Spec) Query(d sqlx.Dialect, t db.TableRef, where string) string {
	cols := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		cols[i] = sqlx.QuoteIdent(d, c)
	}
	q := "SELECT " + strings.Join(cols, ", ") + " FROM " + sqlx.QualifiedName(d, t)
	if where != "" {
		q += " WHERE (" + where + "\n)"
	}
	return q
}

// KeyIndexes are the positions of the key's columns in Columns.
func (s Spec) KeyIndexes() []int {
	out := make([]int, len(s.Key))
	for i, k := range s.Key {
		out[i] = slices.Index(s.Columns, k)
	}
	return out
}

// Swap is the spec with the source and the target exchanged.
func (s Spec) Swap() Spec {
	s.SourceOnly, s.TargetOnly = s.TargetOnly, s.SourceOnly
	return s
}
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/datadiff/ && gofumpt -l . && go vet ./...`
Expected: PASS，无格式输出。

- [ ] **Step 5: 提交**

```bash
git add internal/datadiff/spec.go internal/datadiff/spec_test.go
git commit -m "feat(datadiff): 决定对比的列与配对行的键，写出两边共用的 SELECT"
```

---

### Task 2: `datadiff.Source` / `Matcher`——哈希配对、行数上限、按键排序

**Files:**
- Create: `internal/datadiff/compare.go`
- Test: `internal/datadiff/compare_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Spec`、`Spec.KeyIndexes`。
- Produces（Task 3、4、6、7 使用）：

  ```go
  type Status int
  const (
      Added   Status = iota // only the source has the row
      Removed               // only the target has the row
      Changed               // both have it, with different values
  )

  type Row struct {
      Status Status
      Source []db.Value // in Spec.Columns order; nil for Removed
      Target []db.Value // nil for Added
  }
  func (r Row) Differs(i int) bool

  type TooManyRowsError struct{ Limit int } // Error(): "more than N rows (compare_max_rows)"
  var ErrDuplicateKey = errors.New("the key is not unique")

  func (s Spec) NewSource(limit int) *Source
  func (s *Source) Add(row []db.Value) error // fits db.Querier.Stream's onRow
  func (s *Source) Match() *Matcher          // a fresh pass over the target
  func (m *Matcher) Add(row []db.Value) error
  func (m *Matcher) Len() int                // target rows read
  func (m *Matcher) Rows() []Row             // sorted by key
  func Count(rows []Row) (added, removed, changed int)
  func SwapRows(rows []Row) []Row
  ```

- [ ] **Step 1: 写失败的测试**

`internal/datadiff/compare_test.go`：

```go
package datadiff

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

var users = Spec{Columns: []string{"id", "name"}, Key: []string{"id"}}

func v(s string) db.Value { return db.Value{Text: s} }

var null = db.Value{Null: true}

func r(vals ...db.Value) []db.Value { return vals }

// compare runs one comparison: the source's rows, then the target's.
func compare(s Spec, limit int, src, tgt [][]db.Value) ([]Row, error) {
	so := s.NewSource(limit)
	for _, row := range src {
		if err := so.Add(row); err != nil {
			return nil, err
		}
	}
	m := so.Match()
	for _, row := range tgt {
		if err := m.Add(row); err != nil {
			return nil, err
		}
	}
	return m.Rows(), nil
}

func TestCompare(t *testing.T) {
	src := [][]db.Value{r(v("1"), v("alice")), r(v("2"), v("bob")), r(v("4"), v("dave")), r(v("5"), v(""))}
	tgt := [][]db.Value{r(v("5"), null), r(v("3"), v("carol")), r(v("2"), v("robert")), r(v("1"), v("alice"))}
	got, err := compare(users, 10, src, tgt)
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{Status: Changed, Source: r(v("2"), v("bob")), Target: r(v("2"), v("robert"))},
		{Status: Removed, Target: r(v("3"), v("carol"))},
		{Status: Added, Source: r(v("4"), v("dave"))},
		{Status: Changed, Source: r(v("5"), v("")), Target: r(v("5"), null)}, // '' is not NULL
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[0].Differs(0) || !got[0].Differs(1) || got[1].Differs(1) {
		t.Errorf("Differs: %+v", got[:2])
	}
	if a, rm, c := Count(got); a != 1 || rm != 1 || c != 2 {
		t.Errorf("Count = %d %d %d", a, rm, c)
	}
}

func TestRowsSortByKey(t *testing.T) {
	ints := [][]db.Value{r(v("10"), v("")), r(v("9"), v("")), r(v("-1"), v("")), r(v("-20"), v(""))}
	got, err := compare(users, 10, ints, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, row := range got {
		ids = append(ids, row.Source[0].Text)
	}
	if diff := cmp.Diff([]string{"-20", "-1", "9", "10"}, ids); diff != "" {
		t.Fatalf("integers by value (-want +got):\n%s", diff)
	}

	pair := Spec{Columns: []string{"a", "b"}, Key: []string{"a", "b"}}
	got, err = compare(pair, 10, nil, [][]db.Value{r(v("1"), v("y")), r(v("1"), v("x")), r(v("0"), v("z")), r(v("1"), v("B"))})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, row := range got {
		keys = append(keys, row.Target[0].Text+row.Target[1].Text)
	}
	if diff := cmp.Diff([]string{"0z", "1B", "1x", "1y"}, keys); diff != "" {
		t.Fatalf("text byte by byte, column by column (-want +got):\n%s", diff)
	}
}

func TestRowLimit(t *testing.T) {
	two := [][]db.Value{r(v("1"), v("a")), r(v("2"), v("b"))}
	three := append(two[:2:2], r(v("3"), v("c")))
	if _, err := compare(users, 2, two, two); err != nil {
		t.Fatalf("exactly the limit: %v", err)
	}
	var tm TooManyRowsError
	if _, err := compare(users, 2, three, nil); !errors.As(err, &tm) || tm.Limit != 2 {
		t.Fatalf("source over the limit: %v", err)
	}
	if _, err := compare(users, 2, two, three); !errors.As(err, &tm) {
		t.Fatalf("target over the limit: %v", err)
	}
	if tm.Error() != "more than 2 rows (compare_max_rows)" {
		t.Errorf("Error() = %q", tm.Error())
	}
}

func TestDuplicateKey(t *testing.T) {
	dup := [][]db.Value{r(v("1"), v("a")), r(v("1"), v("b"))}
	for name, sides := range map[string][2][][]db.Value{"source": {dup, nil}, "target": {nil, dup}} {
		_, err := compare(users, 10, sides[0], sides[1])
		if !errors.Is(err, ErrDuplicateKey) || !strings.Contains(err.Error(), "(1)") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// A pass over the target that broke off (the connection was lost) is
// read again from the start with a new Matcher: nothing counts twice.
func TestMatchStartsOverEachPass(t *testing.T) {
	so := users.NewSource(10)
	for _, row := range [][]db.Value{r(v("1"), v("a")), r(v("2"), v("b"))} {
		if err := so.Add(row); err != nil {
			t.Fatal(err)
		}
	}
	broken := so.Match()
	if err := broken.Add(r(v("1"), v("a"))); err != nil {
		t.Fatal(err)
	}
	m := so.Match()
	for _, row := range [][]db.Value{r(v("1"), v("a")), r(v("2"), v("b"))} {
		if err := m.Add(row); err != nil {
			t.Fatalf("second pass: %v", err)
		}
	}
	if rows := m.Rows(); len(rows) != 0 || m.Len() != 2 {
		t.Fatalf("rows = %+v, Len = %d", rows, m.Len())
	}
}

// Drivers may hand every row in one reused slice.
func TestAddCopiesTheRow(t *testing.T) {
	so := users.NewSource(10)
	row := r(v("1"), v("a"))
	if err := so.Add(row); err != nil {
		t.Fatal(err)
	}
	row[1] = v("z")
	m := so.Match()
	tgt := r(v("1"), v("a"))
	if err := m.Add(tgt); err != nil {
		t.Fatal(err)
	}
	tgt[1] = v("q")
	if rows := m.Rows(); len(rows) != 0 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestSwapRows(t *testing.T) {
	in := []Row{
		{Status: Added, Source: r(v("4"), v("dave"))},
		{Status: Removed, Target: r(v("3"), v("carol"))},
		{Status: Changed, Source: r(v("2"), v("bob")), Target: r(v("2"), v("robert"))},
	}
	want := []Row{
		{Status: Removed, Target: r(v("4"), v("dave"))},
		{Status: Added, Source: r(v("3"), v("carol"))},
		{Status: Changed, Source: r(v("2"), v("robert")), Target: r(v("2"), v("bob"))},
	}
	if diff := cmp.Diff(want, SwapRows(in)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if in[0].Status != Added {
		t.Fatal("SwapRows changed its input")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/datadiff/`
Expected: 编译失败，`undefined: Row`、`undefined: Changed` 等。

- [ ] **Step 3: 实现**

`internal/datadiff/compare.go`：

```go
package datadiff

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Status says how a row differs.
type Status int

const (
	Added   Status = iota // only the source has the row
	Removed               // only the target has the row
	Changed               // both have it, with different values
)

// Row is one differing row. Source and Target hold the compared columns in
// Spec.Columns order; Source is nil for Removed and Target for Added.
type Row struct {
	Status Status
	Source []db.Value
	Target []db.Value
}

// Differs reports whether column i differs between the sides.
func (r Row) Differs(i int) bool { return r.Status == Changed && r.Source[i] != r.Target[i] }

// values are the row's values on the side that has it (the source first).
func (r Row) values() []db.Value {
	if r.Source != nil {
		return r.Source
	}
	return r.Target
}

// TooManyRowsError means a side has more rows than a comparison reads.
type TooManyRowsError struct{ Limit int }

func (e TooManyRowsError) Error() string {
	return fmt.Sprintf("more than %d rows (compare_max_rows)", e.Limit)
}

// ErrDuplicateKey means a side returned one key twice.
var ErrDuplicateKey = errors.New("the key is not unique")

// Source holds the source's rows by key. Add each row, then pair the
// target's rows with Match.
type Source struct {
	key   []int
	limit int
	rows  map[string][]db.Value
}

// NewSource starts reading the source; it takes at most limit rows.
func (s Spec) NewSource(limit int) *Source {
	return &Source{key: s.KeyIndexes(), limit: limit, rows: map[string][]db.Value{}}
}

// Add stores a row of the source (a copy: drivers may reuse the slice).
func (s *Source) Add(row []db.Value) error {
	if len(s.rows) >= s.limit {
		return TooManyRowsError{Limit: s.limit}
	}
	k := keyString(row, s.key)
	if _, dup := s.rows[k]; dup {
		return duplicate(row, s.key)
	}
	s.rows[k] = slices.Clone(row)
	return nil
}

// Matcher is one pass over the target's rows. A pass that broke off is
// read again with a new Matcher, so no row counts twice.
type Matcher struct {
	src   *Source
	seen  map[string]bool
	diffs []Row
}

// Match starts a pass over the target.
func (s *Source) Match() *Matcher { return &Matcher{src: s, seen: map[string]bool{}} }

// Add pairs a row of the target with the source's row of the same key.
func (m *Matcher) Add(row []db.Value) error {
	if len(m.seen) >= m.src.limit {
		return TooManyRowsError{Limit: m.src.limit}
	}
	k := keyString(row, m.src.key)
	if m.seen[k] {
		return duplicate(row, m.src.key)
	}
	m.seen[k] = true
	src, ok := m.src.rows[k]
	switch {
	case !ok:
		m.diffs = append(m.diffs, Row{Status: Removed, Target: slices.Clone(row)})
	case !slices.Equal(src, row):
		m.diffs = append(m.diffs, Row{Status: Changed, Source: src, Target: slices.Clone(row)})
	}
	return nil
}

// Len counts the target rows read.
func (m *Matcher) Len() int { return len(m.seen) }

// Rows are the differing rows, the source's unmatched rows included,
// sorted by key.
func (m *Matcher) Rows() []Row {
	out := slices.Clone(m.diffs)
	for k, row := range m.src.rows {
		if !m.seen[k] {
			out = append(out, Row{Status: Added, Source: row})
		}
	}
	key := m.src.key
	slices.SortFunc(out, func(a, b Row) int {
		av, bv := a.values(), b.values()
		for _, i := range key {
			if c := compareValues(av[i], bv[i]); c != 0 {
				return c
			}
		}
		return 0
	})
	return out
}

// Count counts the rows of each status.
func Count(rows []Row) (added, removed, changed int) {
	for _, r := range rows {
		switch r.Status {
		case Added:
			added++
		case Removed:
			removed++
		case Changed:
			changed++
		}
	}
	return added, removed, changed
}

// SwapRows turns the rows of source → target into those of target → source.
func SwapRows(rows []Row) []Row {
	out := make([]Row, len(rows))
	for i, r := range rows {
		r.Source, r.Target = r.Target, r.Source
		switch r.Status {
		case Added:
			r.Status = Removed
		case Removed:
			r.Status = Added
		}
		out[i] = r
	}
	return out
}

// keyString encodes the key's values so that different keys never encode
// alike: each value is N; for NULL, else its length, a colon and its text.
func keyString(row []db.Value, key []int) string {
	var b strings.Builder
	for _, i := range key {
		if row[i].Null {
			b.WriteString("N;")
			continue
		}
		b.WriteString(strconv.Itoa(len(row[i].Text)))
		b.WriteByte(':')
		b.WriteString(row[i].Text)
	}
	return b.String()
}

func duplicate(row []db.Value, key []int) error {
	parts := make([]string, len(key))
	for j, i := range key {
		parts[j] = row[i].Text
		if row[i].Null {
			parts[j] = "NULL"
		}
	}
	return fmt.Errorf("the key (%s) was read twice: %w", strings.Join(parts, ", "), ErrDuplicateKey)
}

var intRe = regexp.MustCompile(`^-?[0-9]+$`)

// compareValues orders key values: NULL first, integers by value, other
// text byte by byte.
func compareValues(a, b db.Value) int {
	switch {
	case a.Null && b.Null:
		return 0
	case a.Null:
		return -1
	case b.Null:
		return 1
	case intRe.MatchString(a.Text) && intRe.MatchString(b.Text):
		return compareInts(a.Text, b.Text)
	}
	return strings.Compare(a.Text, b.Text)
}

// compareInts compares decimal integers of any length.
func compareInts(a, b string) int {
	na, nb := strings.HasPrefix(a, "-"), strings.HasPrefix(b, "-")
	if na != nb {
		if na {
			return -1
		}
		return 1
	}
	a = strings.TrimLeft(strings.TrimPrefix(a, "-"), "0")
	b = strings.TrimLeft(strings.TrimPrefix(b, "-"), "0")
	c := cmp.Compare(len(a), len(b))
	if c == 0 {
		c = strings.Compare(a, b)
	}
	if na {
		return -c
	}
	return c
}
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/datadiff/ && gofumpt -l . && go vet ./...`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/datadiff/compare.go internal/datadiff/compare_test.go
git commit -m "feat(datadiff): 源端按键建哈希表、目标端流式比对，超过行数上限即中止"
```

---

### Task 3: `Spec.Script`——差异行写成 INSERT / UPDATE / DELETE

**Files:**
- Create: `internal/datadiff/script.go`
- Test: `internal/datadiff/script_test.go`

**Interfaces:**
- Consumes: Task 1、2；`edit.New`、`edit.Table.SetCell` / `Delete` / `InsertAfter` / `Len`、`edit.Plan`、`edit.Target`、`edit.Key{Kind: edit.PrimaryKey}`。
- Produces（Task 4、7 使用）：

  ```go
  // cols: the target query's result columns (their DatabaseType picks the
  // literal syntax); they must be Spec.Columns in order.
  func (s Spec) Script(d sqlx.Dialect, target db.TableRef, cols []db.ColumnInfo, rows []Row, picked func(i int) bool) ([]string, error)
  ```

- [ ] **Step 1: 写失败的测试**

`internal/datadiff/script_test.go`：

```go
package datadiff

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

var (
	bak     = db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users_bak"}
	noteful = Spec{Columns: []string{"id", "name", "note"}, Key: []string{"id"}}
	bakCols = []db.ColumnInfo{{Name: "id", DatabaseType: "BIGINT"}, {Name: "name", DatabaseType: "VARCHAR"}, {Name: "note", DatabaseType: "TEXT"}}
	diffs   = []Row{
		{Status: Changed, Source: r(v("2"), v("bob"), v("")), Target: r(v("2"), v("robert"), null)},
		{Status: Removed, Target: r(v("3"), v("it's"), null)},
		{Status: Added, Source: r(v("4"), v("dave"), null)},
	}
)

func all(int) bool { return true }

func TestScript(t *testing.T) {
	got, err := noteful.Script(sqlx.MySQL, bak, bakCols, diffs, all)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"DELETE FROM `app`.`users_bak` WHERE `id` = 3",
		"UPDATE `app`.`users_bak` SET `name` = 'bob', `note` = '' WHERE `id` = 2",
		"INSERT INTO `app`.`users_bak` (`id`, `name`, `note`) VALUES (4, 'dave', NULL)",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestScriptOnlyPicked(t *testing.T) {
	got, err := noteful.Script(sqlx.Postgres, db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users_bak"},
		bakCols, diffs, func(i int) bool { return i == 2 })
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{`INSERT INTO "public"."users_bak" ("id", "name", "note") VALUES (4, 'dave', NULL)`}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got, err := noteful.Script(sqlx.MySQL, bak, bakCols, diffs, func(int) bool { return false }); err != nil || len(got) != 0 {
		t.Fatalf("nothing picked: %v, %v", got, err)
	}
}

func TestScriptNeedsTheComparedColumns(t *testing.T) {
	if _, err := noteful.Script(sqlx.MySQL, bak, bakCols[:2], diffs, all); err == nil {
		t.Fatal("want an error for result columns that are not the compared ones")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/datadiff/ -run Script`
Expected: 编译失败，`noteful.Script undefined`。

- [ ] **Step 3: 实现**

`internal/datadiff/script.go`：

```go
package datadiff

import (
	"fmt"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

// Script writes the statements that make the target's picked rows like the
// source's: DELETE for Removed, UPDATE of the differing columns for
// Changed, INSERT of every compared column for Added. Package edit writes
// them, deletes first, each UPDATE and DELETE located by the key. cols are
// the target query's result columns, Spec.Columns in order.
func (s Spec) Script(d sqlx.Dialect, target db.TableRef, cols []db.ColumnInfo, rows []Row, picked func(i int) bool) ([]string, error) {
	if len(cols) != len(s.Columns) {
		return nil, fmt.Errorf("the target returned %d columns, %d are compared", len(cols), len(s.Columns))
	}
	for i, c := range cols {
		if c.Name != s.Columns[i] {
			return nil, fmt.Errorf("the target returned column %s where %s was expected", c.Name, s.Columns[i])
		}
	}
	var orig [][]db.Value
	var at []int // the rows behind orig
	for i, r := range rows {
		if picked(i) && r.Status != Added {
			orig = append(orig, r.Target)
			at = append(at, i)
		}
	}
	t := edit.New(cols, orig, nil)
	for j, i := range at {
		r := rows[i]
		if r.Status == Removed {
			t.Delete(j)
			continue
		}
		for c := range s.Columns {
			if r.Differs(c) {
				if err := t.SetCell(j, c, r.Source[c]); err != nil {
					return nil, err
				}
			}
		}
	}
	for i, r := range rows {
		if !picked(i) || r.Status != Added {
			continue
		}
		j := t.InsertAfter(t.Len() - 1)
		for c, val := range r.Source {
			if err := t.SetCell(j, c, val); err != nil {
				return nil, err
			}
		}
	}
	stmts, _, err := edit.Plan(t, edit.Target{Dialect: d, Table: target, Key: edit.Key{Kind: edit.PrimaryKey, Columns: s.Key}})
	return stmts, err
}
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/datadiff/ && gofumpt -l . && go vet ./...`
Expected: PASS。若 `TestScript` 的 UPDATE 里 SET 的顺序不同，以 `edit.Plan` 的实际顺序（列顺序）为准——它就是按 `t.cols` 的顺序写的，`name` 在 `note` 之前。

- [ ] **Step 5: 提交**

```bash
git add internal/datadiff/script.go internal/datadiff/script_test.go
git commit -m "feat(datadiff): 勾选的差异行经 edit.Plan 写成 INSERT、UPDATE、DELETE"
```

---

### Task 4: contract 往返测试——数据同步在两个引擎上可执行

**Files:**
- Modify: `internal/db/dbtest/contract.go`（`Fixture.DataCompareSQL`；新子测试，放在 `Modify Table edits round-trip …` 之后）
- Modify: `internal/db/mysql/integration_test.go`、`internal/db/postgres/integration_test.go`（填 `DataCompareSQL`）

**Interfaces:**
- Consumes: Task 1–3 的 `datadiff` 全部 API；`conn.Stream`、`conn.Meta().TableSchema`。
- Produces: 无新 API。验证 `Spec.Query` 在两个引擎上能执行、驱动返回的文本能经 `sqlx.Literal` 原样写回（引号、反斜杠、中文、空串、NULL、decimal、datetime）、`TooManyRowsError` 能穿过驱动的 `Stream` 被 `errors.As` 认出。

- [ ] **Step 1: 在 `Fixture` 里加字段**

`internal/db/dbtest/contract.go` 的 `Fixture` 末尾（`CompareSQL` 之后）加：

```go
	// DataCompareSQL creates dat_src and dat_dst, two tables of the same
	// structure (id primary key, name NOT NULL, note, price, seen_at) and
	// fills them: id 1 is alike; 2 has note '' in dat_src and NULL in
	// dat_dst; 3 has name it's and note back\slash in dat_src, name its and
	// another seen_at in dat_dst; 4 is only in dat_dst; 5 only in dat_src.
	DataCompareSQL []string
```

- [ ] **Step 2: 写子测试**

在 `t.Run("Modify Table edits round-trip through schemaedit and AlterTable", …)` 之后加（import 里补上 `"errors"`（如果还没有）和 `"github.com/shepherd-yun/sqlvim/internal/datadiff"`）：

```go
	t.Run("data sync round-trips through datadiff and edit", func(t *testing.T) {
		for _, q := range f.DataCompareSQL {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		schema := func(name string) db.TableSchema {
			t.Helper()
			s, err := conn.Meta().TableSchema(ctx, ref(name))
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
		spec, err := datadiff.NewSpec(schema("dat_src"), schema("dat_dst"))
		if err != nil {
			t.Fatal(err)
		}
		dialect := sqlx.DialectOf(f.Driver)
		compare := func(where string) ([]datadiff.Row, []db.ColumnInfo) {
			t.Helper()
			src := spec.NewSource(100)
			if err := conn.Stream(ctx, spec.Query(dialect, ref("dat_src"), where), func([]db.ColumnInfo) {}, src.Add); err != nil {
				t.Fatal(err)
			}
			m := src.Match()
			var cols []db.ColumnInfo
			if err := conn.Stream(ctx, spec.Query(dialect, ref("dat_dst"), where),
				func(c []db.ColumnInfo) { cols = c }, m.Add); err != nil {
				t.Fatal(err)
			}
			return m.Rows(), cols
		}

		rows, cols := compare("")
		wantStatus := []datadiff.Status{datadiff.Changed, datadiff.Changed, datadiff.Removed, datadiff.Added}
		var status []datadiff.Status
		for _, r := range rows {
			status = append(status, r.Status)
		}
		if diff := cmp.Diff(wantStatus, status); diff != "" {
			t.Fatalf("ids 2, 3, 4, 5 (-want +got):\n%s\n%+v", diff, rows)
		}

		if narrowed, _ := compare(d.QuoteIdent("id") + " < 3"); len(narrowed) != 1 {
			t.Fatalf("WHERE id < 3 read on both sides: %+v", narrowed)
		}

		small := spec.NewSource(3)
		err = conn.Stream(ctx, spec.Query(dialect, ref("dat_src"), ""), func([]db.ColumnInfo) {}, small.Add)
		var tm datadiff.TooManyRowsError
		if !errors.As(err, &tm) {
			t.Fatalf("4 rows past a limit of 3: %v", err)
		}

		stmts, err := spec.Script(dialect, ref("dat_dst"), cols, rows, func(int) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range stmts {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		if left, _ := compare(""); len(left) != 0 {
			t.Fatalf("after\n%s\nstill differ: %+v", strings.Join(stmts, ";\n"), left)
		}
	})
```

（`d` 是 `RunContract` 开头的 `conn.Dialect()`，`cmp` 是已 import 的 `github.com/google/go-cmp/cmp`。）

- [ ] **Step 3: 写两个引擎的 fixture**

`internal/db/mysql/integration_test.go` 的 `dbtest.Fixture{…}` 里，`CompareSQL` 之后加：

```go
		DataCompareSQL: []string{
			"CREATE TABLE dat_src (id BIGINT PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT, price DECIMAL(10,2), seen_at DATETIME)",
			"CREATE TABLE dat_dst (id BIGINT PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT, price DECIMAL(10,2), seen_at DATETIME)",
			`INSERT INTO dat_src VALUES (1, 'same', 'x', 1.50, '2026-01-02 03:04:05'), (2, '李雷', '', NULL, NULL),
				(3, 'it''s', 'back\\slash', 2.00, '2026-01-02 03:04:05'), (5, 'new', NULL, 9.99, NULL)`,
			`INSERT INTO dat_dst VALUES (1, 'same', 'x', 1.50, '2026-01-02 03:04:05'), (2, '李雷', NULL, NULL, NULL),
				(3, 'its', 'back\\slash', 2.00, '2026-01-02 03:04:06'), (4, 'gone', NULL, NULL, NULL)`,
		},
```

（MySQL 的字符串里 `\\` 是一个反斜杠；Go 的反引号字符串原样传过去。）

`internal/db/postgres/integration_test.go` 的 `dbtest.Fixture{…}` 里加：

```go
		DataCompareSQL: []string{
			"CREATE TABLE app.dat_src (id BIGINT PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT, price NUMERIC(10,2), seen_at TIMESTAMP)",
			"CREATE TABLE app.dat_dst (id BIGINT PRIMARY KEY, name VARCHAR(50) NOT NULL, note TEXT, price NUMERIC(10,2), seen_at TIMESTAMP)",
			`INSERT INTO app.dat_src VALUES (1, 'same', 'x', 1.50, '2026-01-02 03:04:05'), (2, '李雷', '', NULL, NULL),
				(3, 'it''s', 'back\slash', 2.00, '2026-01-02 03:04:05'), (5, 'new', NULL, 9.99, NULL)`,
			`INSERT INTO app.dat_dst VALUES (1, 'same', 'x', 1.50, '2026-01-02 03:04:05'), (2, '李雷', NULL, NULL, NULL),
				(3, 'its', 'back\slash', 2.00, '2026-01-02 03:04:06'), (4, 'gone', NULL, NULL, NULL)`,
		},
```

（PG 默认 `standard_conforming_strings = on`，`\` 是普通字符。）

- [ ] **Step 4: 运行集成测试**

Run: `make test-integration`
Expected: 两个引擎的 `data sync round-trips through datadiff and edit` 都 PASS。

如果失败在"after … still differ"：看是哪一列——
- `seen_at` / `price` 的文本与写回后读出的不同：说明驱动返回的文本格式与字面量不对称，**不要**在 `datadiff` 里做归一化；把问题和差异原样报告给我（这会影响 spec §5.4 的"逐字节比较"）。
- `note` 的 `back\slash`：MySQL 的 `sqlx.Literal` 会把 `\` 写成 `\\`，PG 不会；如果不对，问题在 `sqlx.Literal`，同样报告。

如果 `errors.As(err, &tm)` 失败：看驱动的 `Stream` 是否把 `onRow` 的错误包了一层不带 `%w` 的 `fmt.Errorf`；MySQL 的 `wrap` 与 PG 的 `stream` 目前都原样返回，应当通过。

- [ ] **Step 5: 单元测试与格式**

Run: `go test -race ./... && gofumpt -l . && go vet ./...`
Expected: 全绿。

- [ ] **Step 6: 提交**

```bash
git add internal/db/dbtest/contract.go internal/db/mysql/integration_test.go internal/db/postgres/integration_test.go
git commit -m "test(db): 数据同步往返测试，两个引擎上对比、写脚本、执行后无差异"
```

---

### Task 5: keymap——`diff.data`（`D`）与 Diff 上下文的 `F`

**Files:**
- Modify: `internal/ui/keymap/keymap.go`（新 Action）
- Modify: `internal/ui/keymap/defaults.go`（Diff 上下文的绑定）
- Test: `internal/ui/keymap/keymap_test.go`

**Interfaces:**
- Produces（Task 6、7 使用）：

  ```go
  DataDiff Action = "diff.data"
  // Diff context: "D" → DataDiff, "F" → FilterPrompt (existing action)
  ```

- [ ] **Step 1: 写失败的测试**

`internal/ui/keymap/keymap_test.go` 末尾加：

```go
func TestDataDiffBindings(t *testing.T) {
	k := Default()
	for _, c := range []struct {
		keys []string
		want Action
	}{
		{[]string{"D"}, DataDiff},
		{[]string{"F"}, FilterPrompt},
	} {
		if a, res := k.Resolve(Diff, c.keys); a != c.want || res != Matched {
			t.Errorf("diff %v = %q %v, want %q", c.keys, a, res, c.want)
		}
	}
	if KnownActions()[DataDiff] == "" {
		t.Error("diff.data cannot be bound in keymap.toml")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/ui/keymap/ -run TestDataDiffBindings`
Expected: 编译失败，`undefined: DataDiff`。

- [ ] **Step 3: 实现**

`internal/ui/keymap/keymap.go`，在 `WriteScript Action = "diff.script"` 之后加一行：

```go
	DataDiff          Action = "diff.data"
```

`internal/ui/keymap/defaults.go`，Diff 段改成（只动 `l`/`h` 等的描述，并在末尾加两行）：

```go
	b = append(b, vertical(Diff)...)
	b = append(b,
		Binding{Diff, keys("l"), Expand, "Expand the table · data: scroll columns right"},
		Binding{Diff, keys("enter"), Expand, "Expand the table · data: scroll columns right"},
		Binding{Diff, keys("right"), Expand, "Expand the table · data: scroll columns right"},
		Binding{Diff, keys("h"), Collapse, "Collapse the table · data: scroll columns left"},
		Binding{Diff, keys("left"), Collapse, "Collapse the table · data: scroll columns left"},
		Binding{Diff, keys("x"), ToggleChange, "Include / skip in the script"},
		Binding{Diff, keys("~"), SwapSides, "Swap source and target"},
		Binding{Diff, keys("]", "c"), NextChange, "Next change"},
		Binding{Diff, keys("[", "c"), PrevChange, "Previous change"},
		Binding{Diff, keys(LeaderToken, "w"), WriteScript, "Write the sync script to a new console"},
		Binding{Diff, keys("D"), DataDiff, "Compare the data / back to the structure"},
		Binding{Diff, keys("F"), FilterPrompt, "Filter the compared rows (WHERE on both sides)"},
	)
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/ui/... && gofumpt -l . && go vet ./...`
Expected: PASS（`TestCompareBindings`、`TestDefaultIsValid`、`TestHelpMergesAliases` 照旧通过）。

- [ ] **Step 5: 提交**

```bash
git add internal/ui/keymap/keymap.go internal/ui/keymap/defaults.go internal/ui/keymap/keymap_test.go
git commit -m "feat(keymap): Diff 上下文新增 D 切换数据对比、F 设置两边共用的过滤条件"
```

---

### Task 6: 数据视图——读取、显示、上限、取消、重连

**Files:**
- Modify: `internal/config/config.go`、`internal/config/config_test.go`、`docs/examples/config.toml`
- Create: `internal/ui/difftab_data.go`
- Modify: `internal/ui/difftab.go`
- Modify: `internal/ui/app.go`（`openDiff`）
- Modify: `internal/ui/fakes_test.go`
- Create: `internal/ui/difftab_data_test.go`

**Interfaces:**
- Consumes: Task 1、2 的 `datadiff`；Task 5 的 `keymap.DataDiff`；已有的 `withConn`、`sideError`、`compareTimeout`、`compareSide`、`keyHint`、`joinHints`、`plural`。
- Produces（Task 7 使用）：

  ```go
  // config
  Settings.CompareMaxRows int `toml:"compare_max_rows"` // default 100000, must be positive

  // ui
  type dataView struct { src, tgt compareSide; spec datadiff.Spec; columns []db.Column; where string;
      loading bool; cancel context.CancelFunc; srcErr, tgtErr string; srcCols, tgtCols []db.ColumnInfo;
      rows []datadiff.Row; compared int; picked []bool; widths []int; cursor, offset, colOffset int }
  func (t *diffTab) openData() tea.Cmd
  func (t *diffTab) closeData()
  func (t *diffTab) loadData() tea.Cmd
  func (t *diffTab) handleData(a keymap.Action) tea.Cmd
  func (v *dataView) reset()         // default picks, cursor and column widths
  diffTab.maxRows int; diffTab.data *dataView

  // fakes
  func (c *fakeConn) setData(table string, cols []db.ColumnInfo, rows ...[]db.Value)
  func (c *fakeConn) setBlockData(v bool)
  func (c *fakeConn) setStreamDrop(n int)
  ```

- [ ] **Step 1: `compare_max_rows` 的失败测试**

`internal/config/config_test.go` 末尾加：

```go
func TestCompareMaxRows(t *testing.T) {
	if got := DefaultSettings().CompareMaxRows; got != 100000 {
		t.Fatalf("CompareMaxRows default = %d", got)
	}
	dir := t.TempDir()
	writeFile(t, dir, "config.toml", "compare_max_rows = 5000\n")
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.CompareMaxRows != 5000 {
		t.Fatalf("settings = %+v", cfg.Settings)
	}
	writeFile(t, dir, "config.toml", "compare_max_rows = 0\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "compare_max_rows") {
		t.Fatalf("err = %v", err)
	}
}
```

Run: `go test -race ./internal/config/ -run TestCompareMaxRows`
Expected: 编译失败，`CompareMaxRows undefined`。

- [ ] **Step 2: 实现 `compare_max_rows`**

`internal/config/config.go`：

```go
// Settings is config.toml.
type Settings struct {
	PageSize       int    `toml:"page_size"`
	MaxColumnWidth int    `toml:"max_column_width"`
	MaxRows        int    `toml:"max_rows"`
	CompareMaxRows int    `toml:"compare_max_rows"`
	Editor         string `toml:"editor"`
	ExportDir      string `toml:"export_dir"`
}

// DefaultSettings returns the settings used when config.toml omits a key.
func DefaultSettings() Settings {
	return Settings{PageSize: 200, MaxColumnWidth: 40, MaxRows: 1000, CompareMaxRows: 100000}
}
```

`Load` 里 `max_rows` 的检查之后加：

```go
	if cfg.Settings.CompareMaxRows <= 0 {
		return nil, fmt.Errorf("%s: compare_max_rows must be positive", settingsPath)
	}
```

`docs/examples/config.toml` 的 `max_rows` 一行之后加：

```toml
compare_max_rows = 100000         # rows a data compare (D in a diff tab) reads per side
```

Run: `go test -race ./internal/config/`
Expected: PASS。

- [ ] **Step 3: fake 支持表数据、阻塞与流中断线**

`internal/ui/fakes_test.go`：

`fakeConn` 结构体末尾加三个字段：

```go
	data      map[string]*db.ResultSet // table name -> what a query FROM `app`.`name` returns
	blockData bool                     // data queries wait until their context ends
	dropAt    int                      // the next Stream fails with ErrConnLost after this many rows
```

加三个 setter（放在 `setSchemas` 之后）：

```go
func (c *fakeConn) setData(table string, cols []db.ColumnInfo, rows ...[]db.Value) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.data == nil {
		c.data = map[string]*db.ResultSet{}
	}
	c.data[table] = &db.ResultSet{Columns: cols, Rows: rows}
}

func (c *fakeConn) setBlockData(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blockData = v
}

func (c *fakeConn) setStreamDrop(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropAt = n
}

// tableData is what setData stored for the table q reads from; the caller
// holds mu.
func (c *fakeConn) tableData(q string) *db.ResultSet {
	for name, rs := range c.data {
		if strings.Contains(q, " FROM `app`.`"+name+"`") {
			return rs
		}
	}
	return nil
}
```

`Query` 改成（只在开头多取两个值，并在 `err != nil` 之后返回表数据）：

```go
func (c *fakeConn) Query(ctx context.Context, q string, _ ...any) (*db.ResultSet, error) {
	c.mu.Lock()
	c.queries = append(c.queries, q)
	err, rows := c.fail[q], c.rows
	data, block := c.tableData(q), c.blockData
	c.mu.Unlock()
	if q == slowQuery {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if data != nil {
		if block {
			<-ctx.Done()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return data, nil
	}
	rs := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "id"}, {Name: "name"}}}
	// … the rest as before
```

`Stream` 改成：

```go
func (c *fakeConn) Stream(ctx context.Context, q string, onColumns func([]db.ColumnInfo), onRow func([]db.Value) error) error {
	rs, err := c.Query(ctx, q)
	if err != nil {
		return err
	}
	c.mu.Lock()
	drop := c.dropAt
	c.dropAt = 0
	c.mu.Unlock()
	onColumns(rs.Columns)
	for i, r := range rs.Rows {
		if drop > 0 && i == drop {
			return fmt.Errorf("%w: connection reset by peer", db.ErrConnLost)
		}
		if err := onRow(r); err != nil {
			if errors.Is(err, db.ErrStop) {
				return nil
			}
			return err
		}
	}
	return nil
}
```

Run: `go test -race ./internal/ui/`
Expected: PASS（现有测试不受影响）。

- [ ] **Step 4: 写数据视图的失败测试**

`internal/ui/difftab_data_test.go`：

```go
package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

var userCols = []db.ColumnInfo{{Name: "id", DatabaseType: "BIGINT"}, {Name: "name", DatabaseType: "VARCHAR"}}

func userRow(id, name string) []db.Value { return []db.Value{{Text: id}, {Text: name}} }

// usersData fills app.users (source) and app.users_bak (target): 1 is
// alike, 2 is bob in users and robert in users_bak, 3 is only in
// users_bak and 4 only in users.
func usersData(c *fakeConn) {
	c.setData("users", userCols, userRow("1", "alice"), userRow("2", "bob"), userRow("4", "dave"))
	c.setData("users_bak", userCols, userRow("1", "alice"), userRow("2", "robert"), userRow("3", "carol"))
}

// openUsersData compares the data of app.users with app.users_bak.
func openUsersData(t *testing.T, m *Model, b *fakeBackend) *diffTab {
	t.Helper()
	d := openUsersDiff(t, m, b)
	usersData(b.conn)
	press(t, m, "D")
	if d.data == nil {
		t.Fatalf("no data view:\n%s", screen(m))
	}
	return d
}

func queried(c *fakeConn, s string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.queries, func(q string) bool { return strings.Contains(q, s) })
}

func TestDataViewListsDifferingRows(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	wantScreen(t, m,
		"local · app.users ──▶ local · app.users_bak",
		"key: id", "3 differ (1 + · 1 - · 1 ~)", "1 delete skipped",
		"the structures differ · comparing the 2 columns both have",
		"[x] ~ 2   robert → bob",
		"[ ] - 3   carol",
		"[x] + 4   dave",
	)
	if strings.Contains(screen(m), "alice") {
		t.Fatalf("a row that is alike is listed:\n%s", screen(m))
	}
	for _, q := range []string{"SELECT `id`, `name` FROM `app`.`users`", "SELECT `id`, `name` FROM `app`.`users_bak`"} {
		if !queried(b.conn, q) {
			t.Errorf("no query %q", q)
		}
	}
}

func TestDataViewBackToStructure(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersData(t, m, b)
	press(t, m, "D")
	if d.data != nil {
		t.Fatal("D did not go back")
	}
	wantScreen(t, m, "column email")
}

func TestDataViewSameRows(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	b.conn.setData("users", userCols, userRow("1", "alice"))
	b.conn.setData("users_bak", userCols, userRow("1", "alice"))
	press(t, m, "D")
	wantScreen(t, m, "the rows are the same · 1 row compared")
}

func TestDataViewNeedsAKey(t *testing.T) {
	m, b := newTestModel(t)
	wide(m)
	keyless := func(name string) db.TableSchema {
		return db.TableSchema{Ref: db.TableRef{Namespace: appNS, Name: name}, Columns: []db.Column{{Name: "id", Type: "bigint"}}}
	}
	b.conn.setSchemas(keyless("users"), keyless("users_bak"))
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	d := m.activeTab().(*diffTab)
	press(t, m, "D")
	if d.data != nil {
		t.Fatal("opened a data view without a key")
	}
	wantScreen(t, m, "users has no primary key or unique NOT NULL index")
}

func TestDataViewNamespacePicksTheTableUnderCursor(t *testing.T) {
	m, b := newTestModel(t)
	wide(m)
	staging := b.addConn(config.Connection{Name: "staging", Driver: "mysql", Env: config.EnvDev})
	b.conn.setTables([]db.Table{{Name: "users"}, {Name: "audit"}})
	b.conn.setSchemas(schemaOf("users", idColumn(), db.Column{Name: "name", Type: "varchar(100)"}), schemaOf("audit", idColumn()))
	staging.setTables([]db.Table{{Name: "users"}})
	staging.setSchemas(schemaOf("users", idColumn(), db.Column{Name: "name", Type: "varchar(50)"}))
	src := compareSide{conn: "local", driver: "mysql", ref: db.TableRef{Namespace: appNS}}
	tgt := compareSide{conn: "staging", driver: "mysql", ref: db.TableRef{Namespace: appNS}}
	drain(t, m, m.openDiff(src, tgt))
	d := m.activeTab().(*diffTab)

	press(t, m, "D") // on "+ audit": only the source has it
	if d.data != nil {
		t.Fatal("compared the data of a table the target lacks")
	}
	wantScreen(t, m, "move to a ~ table")

	b.conn.setData("users", userCols, userRow("1", "alice"))
	staging.setData("users", userCols, userRow("1", "alicia"))
	press(t, m, "j", "D") // on "~ users"
	wantScreen(t, m, "local · app.users ──▶ staging · app.users", "[x] ~ 1   alicia → alice")
}

func TestDataViewRowLimit(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersDiff(t, m, b)
	usersData(b.conn)
	d.maxRows = 2
	press(t, m, "D")
	wantScreen(t, m, "✗ source local · app.users: more than 2 rows (compare_max_rows)", "narrows the rows compared")

	d.maxRows = 3 // exactly the rows each side has
	press(t, m, "space", "r")
	wantScreen(t, m, "3 differ")
}

func TestCompareMaxRowsComesFromSettings(t *testing.T) {
	b := newFakeBackend()
	m := New(b, newFakeStore(), config.Settings{PageSize: 2, MaxColumnWidth: 40, MaxRows: 10, CompareMaxRows: 7})
	b.conn.setSchemas(usersSchemas()...)
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	if d := m.activeTab().(*diffTab); d.maxRows != 7 {
		t.Fatalf("maxRows = %d", d.maxRows)
	}
	m2, b2 := newTestModel(t) // settings without compare_max_rows
	if d := openUsersDiff(t, m2, b2); d.maxRows != config.DefaultSettings().CompareMaxRows {
		t.Fatalf("maxRows = %d", d.maxRows)
	}
}

func TestDataViewLoadCanBeCancelled(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	usersData(b.conn)
	b.conn.setBlockData(true)
	_, load := m.Update(uitest.Key("D"))
	wantScreen(t, m, "reading both sides")
	press(t, m, "ctrl+c")
	drain(t, m, load)
	wantScreen(t, m, "✗ source local · app.users: canceled")
}

func TestDataViewReloads(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	b.conn.setData("users_bak", userCols, userRow("1", "alice"), userRow("2", "bob"), userRow("4", "dave"))
	press(t, m, "space", "r")
	wantScreen(t, m, "the rows are the same")
}

// The source's stream breaks after two rows; withConn reconnects and reads
// it again, which must start the side over (not report a key read twice).
func TestDataViewStartsASideOverAfterReconnect(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersDiff(t, m, b)
	usersData(b.conn)
	b.conn.setStreamDrop(2)
	press(t, m, "D")
	wantScreen(t, m, "3 differ (1 + · 1 - · 1 ~)")
	if d.data.srcErr != "" || d.data.tgtErr != "" {
		t.Fatalf("errors: %q %q", d.data.srcErr, d.data.tgtErr)
	}
}

func TestDataViewScrollsColumns(t *testing.T) {
	m, b := newTestModel(t)
	wide(m)
	three := func(name string) db.TableSchema {
		return schemaOf(name, idColumn(), db.Column{Name: "name", Type: "text"}, db.Column{Name: "note", Type: "text"})
	}
	b.conn.setSchemas(three("users"), three("users_bak"))
	cols := append(slices.Clone(userCols), db.ColumnInfo{Name: "note", DatabaseType: "TEXT"})
	b.conn.setData("users", cols, []db.Value{{Text: "1"}, {Text: "bob"}, {Text: "hi"}})
	b.conn.setData("users_bak", cols, []db.Value{{Text: "1"}, {Text: "bob"}, {Text: "ho"}})
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	press(t, m, "D")
	wantScreen(t, m, "bob", "ho → hi")
	press(t, m, "l")
	if strings.Contains(screen(m), "bob") {
		t.Fatalf("l did not scroll past name:\n%s", screen(m))
	}
	wantScreen(t, m, "1   ho → hi") // the key stays
	press(t, m, "l", "h")
	wantScreen(t, m, "bob")
}
```

Run: `go test -race ./internal/ui/ -run 'DataView|CompareMaxRows'`
Expected: 编译失败（`d.data`、`d.maxRows` 未定义）。

- [ ] **Step 5: 实现 `difftab_data.go`**

`internal/ui/difftab_data.go`：

```go
package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/datadiff"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/schemadiff"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// dataCellWidth caps a column of the data view; l / h scroll the columns
// that do not fit.
const dataCellWidth = 30

// dataPrefix is the width of " [x] ~ " before a row's cells.
const dataPrefix = 7

// dataView compares the rows of one pair of tables (spec §5.4). The diff
// tab shows it instead of the structure while it is set.
type dataView struct {
	src, tgt compareSide
	spec     datadiff.Spec
	columns  []db.Column // the compared columns, for F's completion
	where    string      // the condition both sides are read with; "" for every row

	loading        bool
	cancel         context.CancelFunc
	srcErr, tgtErr string
	srcCols        []db.ColumnInfo // as the source's query returned them
	tgtCols        []db.ColumnInfo
	rows           []datadiff.Row
	compared       int // the target's rows read
	picked         []bool
	widths         []int // per compared column

	cursor, offset, colOffset int
}

type dataLoadedMsg struct {
	tabID            int
	gen              int
	srcConn, tgtConn string
	rows             []datadiff.Row
	compared         int
	srcCols, tgtCols []db.ColumnInfo
	srcErr, tgtErr   error
}

func (m dataLoadedMsg) TabID() int { return m.tabID }

func (m dataLoadedMsg) failure() (string, error) {
	if m.srcErr != nil {
		return m.srcConn, m.srcErr
	}
	return m.tgtConn, m.tgtErr
}

// readData reads the source's rows, then pairs the target's with them as
// they stream in. Each attempt at a side starts it over, so a retry after
// a reconnect counts no row twice.
func readData(ctx context.Context, b Backend, src, tgt compareSide, spec datadiff.Spec, where string, limit int) dataLoadedMsg {
	msg := dataLoadedMsg{srcConn: src.conn, tgtConn: tgt.conn}
	d := sqlx.DialectOf(src.driver)
	var rows *datadiff.Source
	msg.srcErr = withConn(ctx, b, src.conn, src.ref.Database, func(c db.Conn) error {
		rows = spec.NewSource(limit)
		return c.Stream(ctx, spec.Query(d, src.ref, where), func(cols []db.ColumnInfo) { msg.srcCols = cols }, rows.Add)
	})
	if msg.srcErr != nil {
		return msg
	}
	var match *datadiff.Matcher
	msg.tgtErr = withConn(ctx, b, tgt.conn, tgt.ref.Database, func(c db.Conn) error {
		match = rows.Match()
		return c.Stream(ctx, spec.Query(d, tgt.ref, where), func(cols []db.ColumnInfo) { msg.tgtCols = cols }, match.Add)
	})
	if msg.tgtErr == nil {
		msg.rows, msg.compared = match.Rows(), match.Len()
	}
	return msg
}

// openData shows the data view of the tables compared: the two tables, or
// the ~ table under the cursor in a namespace comparison.
func (t *diffTab) openData() tea.Cmd {
	if t.loading || t.srcErr != "" || t.tgtErr != "" || t.writer == nil {
		t.setStatus("both sides have to be loaded first", true)
		return nil
	}
	var src, tgt *db.TableSchema
	if t.src.isTable() {
		src, tgt = &t.srcTables[0], &t.tgtTables[0]
	} else if r, ok := t.current(); ok && t.objects[r.obj].Kind == schemadiff.AlterTable {
		src, tgt = t.objects[r.obj].Source, t.objects[r.obj].Target
	}
	if src == nil {
		t.setStatus("only tables both sides have compare their data · move to a ~ table", true)
		return nil
	}
	spec, err := datadiff.NewSpec(*src, *tgt)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	var cols []db.Column
	for _, c := range src.Columns {
		if slices.Contains(spec.Columns, c.Name) {
			cols = append(cols, c)
		}
	}
	t.data = &dataView{
		src:  compareSide{conn: t.src.conn, driver: t.src.driver, ref: src.Ref},
		tgt:  compareSide{conn: t.tgt.conn, driver: t.tgt.driver, ref: tgt.Ref},
		spec: spec, columns: cols,
	}
	return t.loadData()
}

// closeData goes back to the structure, abandoning a load.
func (t *diffTab) closeData() {
	if t.data.cancel != nil {
		t.data.cancel()
	}
	t.data = nil
}

// loadData reads both sides' rows again; a load still running is cancelled.
func (t *diffTab) loadData() tea.Cmd {
	v := t.data
	if v.cancel != nil {
		v.cancel()
	}
	t.gen++
	v.loading, v.srcErr, v.tgtErr = true, "", ""
	ctx, cancel := context.WithTimeout(context.Background(), compareTimeout)
	v.cancel = cancel
	id, gen, b, limit := t.id, t.gen, t.backend, t.maxRows
	src, tgt, spec, where := v.src, v.tgt, v.spec, v.where
	return func() tea.Msg {
		defer cancel()
		msg := readData(ctx, b, src, tgt, spec, where, limit)
		msg.tabID, msg.gen = id, gen
		return msg
	}
}

func (t *diffTab) dataLoaded(m dataLoadedMsg) {
	v := t.data
	if v == nil || m.gen != t.gen {
		return
	}
	v.loading, v.cancel = false, nil
	v.srcErr, v.tgtErr = t.dataError(m.srcErr), t.dataError(m.tgtErr)
	if v.srcErr != "" || v.tgtErr != "" {
		v.rows = nil
		return
	}
	v.srcCols, v.tgtCols, v.rows, v.compared = m.srcCols, m.tgtCols, m.rows, m.compared
	v.reset()
}

// dataError is sideError, pointing a side over the row limit at F.
func (t *diffTab) dataError(err error) string {
	s := sideError(err)
	var tm datadiff.TooManyRowsError
	if errors.As(err, &tm) {
		s = joinHints(s, keyHint(t.keys, keymap.Diff, keymap.FilterPrompt, "narrows the rows compared"))
	}
	return s
}

// reset picks every row but the deletions, puts the cursor on the first
// row and measures the columns.
func (v *dataView) reset() {
	v.picked = make([]bool, len(v.rows))
	for i, r := range v.rows {
		v.picked[i] = r.Status != datadiff.Removed
	}
	v.cursor, v.offset, v.colOffset = 0, 0, 0
	v.widths = make([]int, len(v.spec.Columns))
	for c, name := range v.spec.Columns {
		v.widths[c] = ansi.StringWidth(textutil.Sanitize(name))
	}
	for _, r := range v.rows {
		for c := range v.widths {
			text, _ := cellText(r, c)
			v.widths[c] = max(v.widths[c], ansi.StringWidth(text))
		}
	}
	for c := range v.widths {
		v.widths[c] = min(v.widths[c], dataCellWidth)
	}
}

// cellText is column c of r as shown: the source's value of a new row, the
// target's of a deleted one, "target → source" where they differ.
func cellText(r datadiff.Row, c int) (text string, differs bool) {
	switch {
	case r.Status == datadiff.Removed:
		return valueText(r.Target[c]), false
	case r.Differs(c):
		return valueText(r.Target[c]) + " → " + valueText(r.Source[c]), true
	}
	return valueText(r.Source[c]), false
}

func valueText(v db.Value) string {
	if v.Null {
		return "NULL"
	}
	return textutil.Sanitize(v.Text)
}

// others are the columns that are not the key, in compared order.
func (v *dataView) others() []int {
	key := v.spec.KeyIndexes()
	var out []int
	for c := range v.spec.Columns {
		if !slices.Contains(key, c) {
			out = append(out, c)
		}
	}
	return out
}

// shown are the columns drawn: the key, then the others from colOffset.
func (v *dataView) shown() []int {
	others := v.others()
	return append(v.spec.KeyIndexes(), others[min(v.colOffset, len(others)):]...)
}

func (v *dataView) scrollColumns(delta int) {
	v.colOffset = textutil.Clamp(v.colOffset+delta, 0, max(len(v.others())-1, 0))
}

func (v *dataView) move(delta int) {
	v.cursor = textutil.Clamp(v.cursor+delta, 0, max(len(v.rows)-1, 0))
}

func (t *diffTab) dataListHeight() int { return max(t.height-5, 1) }

// dataJump moves to the next (dir 1) or previous (dir -1) row: every row
// of the data view is a change.
func (t *diffTab) dataJump(dir int) {
	v := t.data
	if i := v.cursor + dir; i >= 0 && i < len(v.rows) {
		v.cursor = i
		return
	}
	t.setStatus("no more changes", false)
}

// handleData runs an action in the data view.
func (t *diffTab) handleData(a keymap.Action) tea.Cmd {
	v := t.data
	switch a {
	case keymap.Down:
		v.move(1)
	case keymap.Up:
		v.move(-1)
	case keymap.Top:
		v.cursor = 0
	case keymap.Bottom:
		v.move(len(v.rows))
	case keymap.HalfPageDown:
		v.move(max(t.dataListHeight()/2, 1))
	case keymap.HalfPageUp:
		v.move(-max(t.dataListHeight()/2, 1))
	case keymap.NextChange:
		t.dataJump(1)
	case keymap.PrevChange:
		t.dataJump(-1)
	case keymap.Expand:
		v.scrollColumns(1)
	case keymap.Collapse:
		v.scrollColumns(-1)
	case keymap.DataDiff:
		t.closeData()
	case keymap.Refresh:
		return t.loadData()
	}
	return nil
}

func (v *dataView) skippedDeletes() int {
	n := 0
	for i, r := range v.rows {
		if r.Status == datadiff.Removed && !v.picked[i] {
			n++
		}
	}
	return n
}

func (t *diffTab) dataHeader() string {
	v := t.data
	head := theme.Title.Render(textutil.Sanitize(v.src.label() + " ──▶ " + v.tgt.label()))
	if cfg, _ := t.backend.Connection(v.tgt.conn); cfg.Env == config.EnvProd {
		head += " " + theme.Prod.Render("[PROD]")
	}
	parts := []string{"key: " + strings.Join(v.spec.Key, ", ")}
	if !v.loading && v.srcErr == "" && v.tgtErr == "" && len(v.rows) > 0 {
		added, removed, changed := datadiff.Count(v.rows)
		verb := "differ"
		if len(v.rows) == 1 {
			verb = "differs"
		}
		parts = append(parts, fmt.Sprintf("%d %s (%d + · %d - · %d ~)", len(v.rows), verb, added, removed, changed))
		if n := v.skippedDeletes(); n > 0 {
			parts = append(parts, plural(n, "delete")+" skipped")
		}
	}
	return head + "   " + theme.Muted.Render(textutil.Sanitize(strings.Join(parts, "   ")))
}

// note says what narrows the comparison: the filter, and columns only one
// side has.
func (v *dataView) note() string {
	var parts []string
	if v.where != "" {
		parts = append(parts, "WHERE "+textutil.Sanitize(v.where))
	}
	if len(v.spec.SourceOnly)+len(v.spec.TargetOnly) > 0 {
		parts = append(parts, "the structures differ · comparing the "+plural(len(v.spec.Columns), "column")+" both have")
	}
	return strings.Join(parts, "   ")
}

// dataLines are the data view's lines above the status line.
func (t *diffTab) dataLines() []string {
	v := t.data
	out := []string{textutil.Fit(t.dataHeader(), t.width)}
	if n := v.note(); n != "" {
		out = append(out, textutil.Fit(theme.Muted.Render(n), t.width))
	}
	switch {
	case v.srcErr != "" || v.tgtErr != "":
		if v.srcErr != "" {
			out = append(out, theme.Error.Render(textutil.Sanitize("✗ source "+v.src.label()+": "+v.srcErr)))
		}
		if v.tgtErr != "" {
			out = append(out, theme.Error.Render(textutil.Sanitize("✗ target "+v.tgt.label()+": "+v.tgtErr)))
		}
		if h := keyHint(t.keys, keymap.Global, keymap.Refresh, "reloads"); h != "" {
			out = append(out, theme.Muted.Render(h))
		}
	case v.loading:
		out = append(out, theme.Muted.Render(joinHints("reading both sides…", keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels"))))
	case len(v.rows) == 0:
		out = append(out, theme.Muted.Render("the rows are the same · "+plural(v.compared, "row")+" compared"))
	default:
		out = append(out, t.dataTable(t.height-1-len(out))...)
	}
	return out
}

// dataTable is the column header and the rows that fit in h lines.
func (t *diffTab) dataTable(h int) []string {
	v := t.data
	cols := v.shown()
	head := make([]string, len(cols))
	for j, c := range cols {
		head[j] = textutil.Fit(textutil.Sanitize(v.spec.Columns[c]), v.widths[c])
	}
	out := []string{textutil.Fit(theme.Header.Render(strings.Repeat(" ", dataPrefix)+strings.Join(head, "  ")), t.width)}
	h = max(h-1, 1)
	v.cursor = textutil.Clamp(v.cursor, 0, len(v.rows)-1)
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	if v.cursor >= v.offset+h {
		v.offset = v.cursor - h + 1
	}
	v.offset = textutil.Clamp(v.offset, 0, max(len(v.rows)-h, 0))
	for i := v.offset; i < min(v.offset+h, len(v.rows)); i++ {
		plain, styled := v.line(i, cols)
		if i == v.cursor {
			out = append(out, theme.Selected.Render(textutil.Fit(plain, t.width)))
		} else {
			out = append(out, textutil.Fit(styled, t.width))
		}
	}
	return out
}

// line is row i, plain and styled: its checkbox, its sign and the cells of
// cols, the differing ones highlighted.
func (v *dataView) line(i int, cols []int) (plain, styled string) {
	r := v.rows[i]
	box := "[ ]"
	if v.picked[i] {
		box = "[x]"
	}
	sign, style := "~", theme.Changed.Render
	switch r.Status {
	case datadiff.Added:
		sign, style = "+", theme.Inserted.Render
	case datadiff.Removed:
		sign, style = "-", theme.Error.Render
	}
	prefix := " " + box + " " + sign + " "
	ps, ss := make([]string, len(cols)), make([]string, len(cols))
	for j, c := range cols {
		text, differs := cellText(r, c)
		ps[j] = textutil.Fit(text, v.widths[c])
		ss[j] = ps[j]
		if differs {
			ss[j] = theme.Changed.Render(ps[j])
		}
	}
	return prefix + strings.Join(ps, "  "), style(prefix) + strings.Join(ss, "  ")
}

func (t *diffTab) dataHints() string {
	return theme.Muted.Render(joinHints(
		keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "include/skip"),
		keyHint(t.keys, keymap.Diff, keymap.FilterPrompt, "filter"),
		keyHint(t.keys, keymap.Diff, keymap.SwapSides, "swap"),
		keyHint(t.keys, keymap.Diff, keymap.WriteScript, "script to console"),
		keyHint(t.keys, keymap.Diff, keymap.DataDiff, "structure"),
	))
}
```

- [ ] **Step 6: 把 `diffTab` 接到数据视图上**

`internal/ui/difftab.go`：

1. `diffTab` 结构体在 `now func() time.Time` 之后加：

   ```go
	maxRows int       // compare_max_rows: rows the data view reads per side
	data    *dataView // the data view, while it is shown
   ```

2. `newDiffTab` 里补默认值：

   ```go
	return &diffTab{
		id: id, key: diffTabKey(src, tgt), backend: b, src: src, tgt: tgt,
		now: time.Now, keys: keymap.Default(), maxRows: config.DefaultSettings().CompareMaxRows,
	}
   ```

3. `retryAfterPassword` 改成：

   ```go
func (t *diffTab) retryAfterPassword() tea.Cmd {
	if t.data != nil {
		return t.loadData()
	}
	return t.load()
}
   ```

4. `cancelRun` 改成：

   ```go
func (t *diffTab) cancelRun() tea.Cmd {
	switch {
	case t.data != nil && t.data.loading && t.data.cancel != nil:
		t.data.cancel()
	case t.data == nil && t.loading && t.cancel != nil:
		t.cancel()
	default:
		t.setStatus("nothing is loading", false)
	}
	return nil
}
   ```

5. `Update` 拆成分派 + `structureLoaded`（原函数体原样搬进 `structureLoaded`）：

   ```go
func (t *diffTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case diffLoadedMsg:
		t.structureLoaded(m)
	case dataLoadedMsg:
		t.dataLoaded(m)
	}
	return nil
}

func (t *diffTab) structureLoaded(m diffLoadedMsg) {
	if m.gen != t.gen {
		return
	}
	t.loading, t.cancel = false, nil
	t.srcErr, t.tgtErr = sideError(m.srcErr), sideError(m.tgtErr)
	if t.srcErr != "" || t.tgtErr != "" {
		t.objects = nil
		return
	}
	t.srcTables, t.tgtTables, t.writer = m.src, m.tgt, m.writer
	t.compute()
}
   ```

6. `Handle` 开头、`t.setStatus("", false)` 之后加分派，并在 switch 里加两个 case：

   ```go
func (t *diffTab) Handle(a keymap.Action) tea.Cmd {
	t.setStatus("", false)
	if t.data != nil {
		return t.handleData(a)
	}
	switch a {
	// … the existing cases …
	case keymap.DataDiff:
		return t.openData()
	case keymap.FilterPrompt:
		t.setStatus(joinHints("the filter narrows a data compare", keyHint(t.keys, keymap.Diff, keymap.DataDiff, "compares the data")), false)
	}
	return nil
}
   ```

7. `statusLine` 改成先看状态、再按视图给提示，结构视图的提示加上 `D`：

   ```go
func (t *diffTab) statusLine() string {
	if t.status != "" {
		if t.statusErr {
			return theme.Error.Render(t.status)
		}
		return t.status
	}
	if t.data != nil {
		return t.dataHints()
	}
	return theme.Muted.Render(joinHints(
		keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "include/skip"),
		keyHint(t.keys, keymap.Diff, keymap.NextChange, "next change"),
		keyHint(t.keys, keymap.Diff, keymap.SwapSides, "swap"),
		keyHint(t.keys, keymap.Diff, keymap.WriteScript, "script to console"),
		keyHint(t.keys, keymap.Diff, keymap.DataDiff, "data"),
		keyHint(t.keys, keymap.Global, keymap.Refresh, "reload"),
	))
}
   ```

8. `View` 改成：

   ```go
func (t *diffTab) View(bool) string {
	var lines []string
	if t.data != nil {
		lines = t.dataLines()
	} else {
		lines = append([]string{textutil.Fit(t.header(), t.width)}, t.body()...)
	}
	for len(lines) < t.height-1 {
		lines = append(lines, "")
	}
	lines = lines[:max(t.height-1, 1)]
	return strings.Join(append(lines, textutil.Fit(t.statusLine(), t.width)), "\n")
}
   ```

`internal/ui/app.go` 的 `openDiff`：

```go
func (m *Model) openDiff(src, tgt compareSide) tea.Cmd {
	return m.showTab(diffTabKey(src, tgt), func(id int) tab {
		t := newDiffTab(id, m.backend, src, tgt)
		t.keys = m.keys
		if m.settings.CompareMaxRows > 0 {
			t.maxRows = m.settings.CompareMaxRows
		}
		return t
	})
}
```

- [ ] **Step 7: 运行测试，确认通过**

Run: `go test -race ./internal/ui/ -run 'DataView|CompareMaxRows|DiffTab|WriteScript' -v 2>&1 | tail -40`
Expected: 全部 PASS。

常见出入：
- `wantScreen` 的行内空格：`"[x] ~ 2   robert → bob"` 来自 `id` 列宽 2（`Fit("2", 2)` = `"2 "`）+ 两个空格分隔。如果某个断言因为列宽差一格而失败，先打印 `screen(m)` 看实际排版，**按实际排版修测试字符串**，不要为了迎合测试改列宽规则。
- `TestDataViewNamespacePicksTheTableUnderCursor` 依赖 `DiffNamespace` 按表名排序（`audit` 在 `users` 之前），所以 `j` 之后光标在 `~ users` 的对象行上。

然后跑全量：`go test -race ./... && gofumpt -l . && go vet ./...`
Expected: 全绿。

- [ ] **Step 8: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go docs/examples/config.toml \
	internal/ui/difftab_data.go internal/ui/difftab.go internal/ui/app.go \
	internal/ui/fakes_test.go internal/ui/difftab_data_test.go
git commit -m "feat(ui): Diff 标签页按 D 对比两张表的数据，列出差异行，超过 compare_max_rows 即中止"
```

---

### Task 7: 数据视图——勾选、交换、`F` 过滤、`<Space>w` 写脚本

**Files:**
- Modify: `internal/ui/tablequery.go`（抽出 `checkSelect`）
- Modify: `internal/ui/tabs.go`（抽出 `completeCondition`；`dataTab.completeFilter` 转调）
- Modify: `internal/ui/difftab_data.go`（`toggleData`、`swapData`、`dataFilterPrompt`、`dataFilterMsg`、`writeDataScript`；`handleData` 加 case）
- Modify: `internal/ui/difftab.go`（`Update` 处理 `dataFilterMsg`）
- Test: `internal/ui/difftab_data_test.go`

**Interfaces:**
- Consumes: Task 3 的 `Spec.Script`；Task 2 的 `SwapRows`；Task 1 的 `Spec.Swap`；Task 6 的 `dataView` 与 `handleData`；已有的 `scriptConsoleMsg`、`overlay.NewPrompt`、`overlay.WithCompleter`、`overlay.WithValidate`、`normFilter`、`errBadFilter`。
- Produces：

  ```go
  func checkSelect(d sqlx.Dialect, q string) error // errBadFilter unless q is one read statement
  func completeCondition(d sqlx.Dialect, cols []db.Column, text string, cursor int) (int, []complete.Candidate)
  type dataFilterMsg struct{ tabID int; text string }
  ```

- [ ] **Step 1: 写失败的测试**

`internal/ui/difftab_data_test.go` 末尾加（需要的 import：`"github.com/shepherd-yun/sqlvim/internal/ui/overlay"`）：

```go
func TestDataViewToggle(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersData(t, m, b)
	press(t, m, "x") // the ~ row
	wantScreen(t, m, "[ ] ~ 2")
	press(t, m, "j", "x") // the - row
	wantScreen(t, m, "[x] - 3")
	if strings.Contains(screen(m), "delete skipped") {
		t.Fatalf("the delete is picked:\n%s", screen(m))
	}
	if d.data.picked[0] || !d.data.picked[1] || !d.data.picked[2] {
		t.Fatalf("picked = %v", d.data.picked)
	}
}

func TestDataViewJump(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersData(t, m, b)
	press(t, m, "]", "c", "]", "c")
	if d.data.cursor != 2 {
		t.Fatalf("cursor = %d", d.data.cursor)
	}
	press(t, m, "]", "c")
	wantScreen(t, m, "no more changes")
	press(t, m, "[", "c")
	if d.data.cursor != 1 {
		t.Fatalf("cursor = %d", d.data.cursor)
	}
}

func TestDataViewSwap(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "~")
	wantScreen(t, m,
		"local · app.users_bak ──▶ local · app.users",
		"[x] ~ 2   bob → robert",
		"[x] + 3   carol",
		"[ ] - 4   dave",
	)
}

func TestDataViewFilter(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "F")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, "< 3") // after the key column the prompt starts with
	press(t, m, "enter")
	for _, q := range []string{
		"SELECT `id`, `name` FROM `app`.`users` WHERE (id < 3\n)",
		"SELECT `id`, `name` FROM `app`.`users_bak` WHERE (id < 3\n)",
	} {
		if !queried(b.conn, q) {
			t.Errorf("no query %q", q)
		}
	}
	wantScreen(t, m, "WHERE id < 3")
}

func TestDataViewFilterRejectsASecondStatement(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "F", "ctrl+u")
	typeText(t, m, "1=1; DELETE FROM users")
	press(t, m, "enter")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("the prompt closed: overlay = %T", m.overlay)
	}
	wantScreen(t, m, "the filter must be a condition")
	if queried(b.conn, "DELETE") {
		t.Fatal("the bad filter reached the server")
	}
}

func TestDataScriptOpensConsoleOnTarget(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "space", "w")
	c := scriptConsole(t, m)
	if c.conn != "local" || c.ns != appNS {
		t.Fatalf("console on %s %+v", c.conn, c.ns)
	}
	text := c.editor.Value()
	for _, want := range []string{
		"-- sqlvim data sync\n",
		"-- source: local · app.users\n",
		"-- target: local · app.users_bak\n",
		"-- key: id\n",
		"UPDATE `app`.`users_bak` SET `name` = 'bob' WHERE `id` = 2;",
		"INSERT INTO `app`.`users_bak` (`id`, `name`) VALUES (4, 'dave');",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "DELETE") {
		t.Fatalf("the unpicked delete is in the script:\n%s", text)
	}
}

func TestDataScriptNothingPicked(t *testing.T) {
	m, b := newTestModel(t)
	openUsersData(t, m, b)
	press(t, m, "x", "j", "j", "x") // unpick the ~ and the + row
	press(t, m, "space", "w")
	if _, ok := m.activeTab().(*diffTab); !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	wantScreen(t, m, "nothing picked")
}
```

Run: `go test -race ./internal/ui/ -run 'DataView|DataScript'`
Expected: 新测试 FAIL（`x`、`~`、`F`、`<Space>w` 在数据视图里还没有作用）。

- [ ] **Step 2: 抽出 `checkSelect` 与 `completeCondition`**

`internal/ui/tablequery.go` 的 `check` 改成：

```go
// check rejects a filter that turns the query into more than one SELECT.
func (q tableQuery) check(d sqlx.Dialect, ref db.TableRef) error {
	return checkSelect(d, q.sql(d, ref))
}

// checkSelect rejects a query that a typed filter turned into more than
// one statement, or into something other than a read.
func checkSelect(d sqlx.Dialect, q string) error {
	if len(sqlx.Split(q, d)) != 1 || sqlx.Analyze(q, d).Kind != sqlx.Read {
		return errBadFilter
	}
	return nil
}
```

`internal/ui/tabs.go` 的 `completeFilter` 改成转调一个不依赖 `dataTab` 的函数：

```go
// completeFilter offers the table's columns and condition keywords for the
// word before the cursor in the F prompt.
func (t *dataTab) completeFilter(text string, cursor int) (int, []complete.Candidate) {
	return completeCondition(t.dialect, t.cols, text, cursor)
}

// completeCondition offers cols and condition keywords for the word before
// the cursor in a WHERE prompt.
func completeCondition(d sqlx.Dialect, cols []db.Column, text string, cursor int) (int, []complete.Candidate) {
	rs := []rune(text)
	cursor = min(max(cursor, 0), len(rs))
	start := cursor
	for start > 0 && isFilterWordRune(rs[start-1]) {
		start--
	}
	prefix := strings.Trim(string(rs[start:cursor]), "\"`")
	var cands []complete.Candidate
	for _, c := range cols {
		cands = append(cands, complete.Candidate{
			Text: sqlx.QuoteIfNeeded(c.Name, d), Label: c.Name, Kind: complete.Column, Detail: c.Type,
		})
	}
	kws := filterKeywords
	if d == sqlx.Postgres {
		kws = append(slices.Clone(kws), "ILIKE")
	}
	lower := strings.ToUpper(prefix) != prefix
	for _, kw := range kws {
		if lower {
			kw = strings.ToLower(kw)
		}
		cands = append(cands, complete.Candidate{Text: kw, Label: kw, Kind: complete.Keyword})
	}
	return start, complete.Rank(prefix, cands, maxCandidates)
}
```

（函数体与原 `completeFilter` 相同，只把 `t.dialect` / `t.cols` 换成参数。如果 `t.cols` 的类型不是 `[]db.Column`，以实际类型为准调整参数类型，并在数据视图里传同样类型的列。）

Run: `go test -race ./internal/ui/ -run 'Filter'`
Expected: 现有的过滤测试照旧 PASS。

- [ ] **Step 3: 实现勾选、交换、过滤与脚本**

`internal/ui/difftab_data.go` 末尾加（import 里补上 `"github.com/shepherd-yun/sqlvim/internal/ui/overlay"` 与 `"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"`；生成时间用 `diffTab` 已有的 `t.now`）：

```go
// dataFilterMsg is the condition typed at F in the data view.
type dataFilterMsg struct {
	tabID int
	text  string
}

func (m dataFilterMsg) TabID() int { return m.tabID }

// loaded reports rows to act on: the load finished without an error.
func (v *dataView) loaded() bool { return !v.loading && v.srcErr == "" && v.tgtErr == "" }

func (t *diffTab) toggleData() {
	v := t.data
	if !v.loaded() || len(v.rows) == 0 {
		return
	}
	v.picked[v.cursor] = !v.picked[v.cursor]
}

// swapData makes the target the source; the rows read are reused.
func (t *diffTab) swapData() {
	v := t.data
	if !v.loaded() {
		t.setStatus("both sides have to be loaded first", true)
		return
	}
	v.src, v.tgt = v.tgt, v.src
	v.srcCols, v.tgtCols = v.tgtCols, v.srcCols
	v.spec = v.spec.Swap()
	v.rows = datadiff.SwapRows(v.rows)
	v.reset()
	t.setStatus("now "+v.src.label()+" ──▶ "+v.tgt.label(), false)
}

// dataFilterPrompt edits the condition both sides are read with, starting
// from the current one, else the first key column.
func (t *diffTab) dataFilterPrompt() tea.Cmd {
	v := t.data
	d := sqlx.DialectOf(v.src.driver)
	initial := v.where
	if initial == "" && len(v.spec.Key) > 0 {
		initial = sqlx.QuoteIfNeeded(v.spec.Key[0], d) + " "
	}
	id, cols, spec, ref := t.id, v.columns, v.spec, v.src.ref
	return openOverlay(overlay.NewPrompt("Filter both sides",
		"WHERE …  read on the source and the target · empty compares every row", initial,
		func(s string) tea.Msg { return dataFilterMsg{tabID: id, text: s} },
		overlay.WithCompleter(func(text string, cursor int) (int, []complete.Candidate) {
			return completeCondition(d, cols, text, cursor)
		}),
		overlay.WithValidate(func(s string) error { return checkSelect(d, spec.Query(d, ref, normFilter(s))) })))
}

// filterData reads both sides again with the typed condition.
func (t *diffTab) filterData(text string) tea.Cmd {
	if t.data == nil {
		return nil
	}
	t.data.where = normFilter(text)
	return t.loadData()
}

// writeDataScript asks for a console on the target holding the INSERT,
// UPDATE and DELETE statements of the picked rows.
func (t *diffTab) writeDataScript() tea.Cmd {
	v := t.data
	if !v.loaded() || len(v.rows) == 0 {
		t.setStatus("nothing to sync", true)
		return nil
	}
	stmts, err := v.spec.Script(sqlx.DialectOf(v.tgt.driver), v.tgt.ref, v.tgtCols, v.rows, func(i int) bool { return v.picked[i] })
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	if len(stmts) == 0 {
		t.setStatus(joinHints("nothing picked", keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "picks a row")), true)
		return nil
	}
	conn, ns, text := v.tgt.conn, v.tgt.ref.Namespace, t.dataScriptText(stmts)
	return func() tea.Msg { return scriptConsoleMsg{conn: conn, ns: ns, text: text} }
}

func (t *diffTab) dataScriptText(stmts []string) string {
	v := t.data
	var b strings.Builder
	b.WriteString("-- sqlvim data sync\n")
	fmt.Fprintf(&b, "-- source: %s\n-- target: %s\n-- key: %s\n", v.src.label(), v.tgt.label(), strings.Join(v.spec.Key, ", "))
	if v.where != "" {
		fmt.Fprintf(&b, "-- where: %s\n", strings.ReplaceAll(v.where, "\n", " "))
	}
	fmt.Fprintf(&b, "-- generated: %s\n", t.now().Format("2006-01-02 15:04:05"))
	b.WriteString("-- These statements change the target's rows to match the source. Review them before running.\n\n")
	for _, s := range stmts {
		b.WriteString(s + ";\n")
	}
	return b.String()
}
```

`handleData` 的 switch 里加四个 case：

```go
	case keymap.ToggleChange:
		t.toggleData()
	case keymap.SwapSides:
		t.swapData()
	case keymap.FilterPrompt:
		return t.dataFilterPrompt()
	case keymap.WriteScript:
		return t.writeDataScript()
```

`internal/ui/difftab.go` 的 `Update` 加一个 case：

```go
	case dataFilterMsg:
		return t.filterData(m.text)
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/ui/ -run 'DataView|DataScript|Filter|DiffTab|WriteScript'`
Expected: PASS。

如果 `TestDataViewFilter` 的查询里条件是 `` `id` < 3 ``：说明 `QuoteIfNeeded("id", MySQL)` 认为 `id` 需要引号；按实际输出改测试里的期望字符串（两条查询与 `WHERE …` 那一行都改），不改 `QuoteIfNeeded`。

然后：`go test -race ./... && gofumpt -l . && go vet ./... && make lint`
Expected: 全绿。

- [ ] **Step 5: 提交**

```bash
git add internal/ui/tablequery.go internal/ui/tabs.go internal/ui/difftab_data.go internal/ui/difftab.go internal/ui/difftab_data_test.go
git commit -m "feat(ui): 数据视图可勾选、交换两边、F 设置共用的 WHERE，<Space>w 把脚本放进目标 console"
```

---

### Task 8: 文档

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `README.md`、`README.zh-CN.md`
- Modify: `docs/superpowers/specs/2026-10-15-table-ops-compare-design.md`（新增 §8.3）

- [ ] **Step 1: CHANGELOG**

`[Unreleased]` 的 `### Added` 末尾加：

```markdown
- Data compare: `D` in a diff tab compares the rows of the two tables (or of the `~` table under
  the cursor) by their primary key, or a unique NOT NULL index both have. It lists the rows only
  the source has (`+`), only the target has (`-`) and those whose values differ (`~`, shown as
  target → source). `F` sets a WHERE both sides are read with, `x` picks rows (deletions start
  unpicked), `~` swaps the sides, `h` / `l` scroll the columns, and `<Space>w` writes the
  INSERT / UPDATE / DELETE script into a new console on the target. Each side reads at most
  `compare_max_rows` rows (`config.toml`, default 100000); `D` goes back to the structure.
```

- [ ] **Step 2: README（中英两份）**

`README.md` 的 `## Configure` 里的 TOML 块，在 `max_rows` 一行之后加：

```toml
compare_max_rows = 100000        # rows a data compare reads per side
```

按键表里 `<Space>w | Diff tab: …` 那一行之后加：

```markdown
| `D` / `F` | Diff tab: compare the rows of the tables by their key, or go back to the structure / in the data view, a WHERE both sides are read with |
```

`README.zh-CN.md` 对应的 TOML 块加：

```toml
compare_max_rows = 100000        # 数据对比每边最多读取的行数
```

（如果中文 README 的 TOML 注释是英文，就照英文版写。）按键表里 `<Space>w | Diff 标签页：…` 之后加：

```markdown
| `D` / `F` | Diff 标签页：按键列对比两张表的数据，或回到结构 / 数据视图里设置两边共用的 WHERE |
```

- [ ] **Step 3: spec §8.3**

在 spec 的 `## 8.2 第 3 阶段的实现调整` 一节之后、`## 9. 待定` 之前加：

```markdown
## 8.3 第 4 阶段的实现调整

第 4 阶段（v1.5）实现时对上文做了这些调整：

- `datadiff.NewSpec` 决定对比的列与键：键取源表的主键，否则第一个非部分的 unique NOT NULL 索引；目标表只要有同一组列作为主键或 unique NOT NULL 索引即可（不要求是"第一个"）。生成列不比较、不写。
- 读取在 `withConn` 里进行，每次尝试都新建 `Source` / `Matcher`，断线重连后从头读这一边。同一边读到重复的键时报错，不猜测。
- 差异行按键排序：NULL 在前，整数按数值，其余按字节。
- 脚本由 `edit.Table` + `edit.Plan` 生成：先 DELETE，再 UPDATE（只 SET 不同的列），最后 INSERT（写出全部对比列）。
- 数据视图从结构视图的 `D` 进入：表对表时直接对比；命名空间对比时对光标下的 `~` 表。两边结构相同的表在命名空间对比里不列出，要对比它们的数据，直接对两张表 `C`。
- `~` 在数据视图里复用已读的行，不重新读取；`<Space>r` 只重新读数据，不重读结构。
- `h` / `l` 在数据视图里左右滚动非键列，键列固定在左侧；单元格最宽 30 列。
```

§9 的待定项保持不变（是否分段校验和仍等实际使用后决定）。

- [ ] **Step 4: 提交**

```bash
git add CHANGELOG.md README.md README.zh-CN.md docs/superpowers/specs/2026-10-15-table-ops-compare-design.md
git commit -m "docs: 记录数据对比与 compare_max_rows"
```

---

## 完成标准

- `make test`、`make lint`、`make test-integration` 全绿。
- 手动验收（`make run`，MySQL 与 PG 各一次）：对两张有主键的表 `m` / `C` → `D` 进入数据视图，看到 `+` / `-` / `~` 行和 `target → source` 的高亮；`F` 输入 `id < 100` 两边一起缩小；`x` 勾上一条 DELETE；`~` 交换后方向与勾选默认值随之变化；`<Space>w` 打开目标连接的 console，脚本里每条 UPDATE / DELETE 都带键列的 WHERE；在 prod 目标上运行时出现 prod 确认；把 `compare_max_rows` 设成 10 时，超过 10 行的一边报错并提示 `F`。
