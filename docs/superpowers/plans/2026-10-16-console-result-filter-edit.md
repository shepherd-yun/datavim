# 控制台结果的过滤与编辑 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 控制台 SELECT 结果支持表数据 tab 同款的 `f` / `F` 过滤（服务端重新执行），以及单表结果上的 `i` `o` `dd` `u` `U` `:w` 编辑，提交跟随控制台的事务状态。

**Architecture:** 纯逻辑放在 `sqlx`（`ResultSource` 判断结果能否对应回单表，`Filtered` 改写 SQL）和 `edit`（`Target.Columns` 列对应，`ApplyInTx` 用 SAVEPOINT 在已打开的事务里执行）。UI 侧把 `dataTab` 的过滤和编辑逻辑抽成 `editableResult`，通过 `resultHost` 接口拿到两处不同的行为（如何重新加载、如何执行修改）；`dataTab` 内嵌它，`consoleTab` 以 `res` 字段持有它。

**Tech Stack:** Go，Bubble Tea v2，`internal/sqlx` 词法分析器，`internal/edit`，testcontainers（集成测试）。

**Spec:** `docs/superpowers/specs/2026-10-16-console-result-filter-edit-design.md`

## Global Constraints

- UI 文本用英文；不在 UI 文本里写死按键名，用 `keyHint` / `joinHints`。
- Conventional Commits 带 scope：`feat(sqlx): …`、`feat(edit): …`、`refactor(ui): …`、`feat(ui): …`、`docs: …`。commit message 里**不加** `Co-Authored-By` 行。
- 安全规则不变：只读连接、生产环境确认（`runner.Guard`）、每条修改语句必须正好影响 1 行、不在事务中悄悄重连。
- 依赖方向只能向下：`ui` → `sqlx` / `edit` → `db`。`sqlx` 不能 import `edit`。
- 每个任务结束时 `make test` 与 `make lint` 都通过。
- README 改动要同时改 `README.zh-CN.md`；用户可见的变化在 `CHANGELOG.md` 的 `[Unreleased]` 下记一条。

## Review Focus

1. **结果列名重复的 JOIN 被包装成子查询**（`SELECT * FROM a JOIN b …`，两边都有 `id`）：MySQL 会报 "Duplicate column name"。期望：服务端错误显示在状态栏，条件记进 `failedWhere`，原结果不动。由 Task 7 的 `TestConsoleFilterFailureKeepsRows` 覆盖。
2. **列名大小写与目录不一致**（MySQL 上 `SELECT ID, Name FROM users`）：期望能编辑，生成的语句用目录里的列名。由 Task 6 的 `TestResolveMapping` 覆盖。
3. **同一列选了两次**（`SELECT id, name, name FROM users`）：期望只读并说明原因，不能生成重复的 SET。由 Task 1 的 `duplicate column` 用例覆盖。
4. **原语句的 WHERE 以行注释结尾**（`… WHERE a = 1 -- note`）：期望合并后的条件不被注释吞掉。由 Task 2 的 `where ends in a comment` 用例覆盖。
5. **有暂存修改时执行新语句或过滤**：期望被拦下并提示，修改不丢。由 Task 6 的 `TestConsoleUnsavedChangesBlockRuns` 和 Task 7 的 `TestConsoleUnsavedChangesBlockFilter` 覆盖。

---

### Task 1: `sqlx.ResultSource` — 判断结果能否对应回单表

**Files:**
- Create: `internal/sqlx/source.go`
- Test: `internal/sqlx/source_test.go`

**Interfaces:**
- Consumes: `codeTokens`、`tableUse`、`isWord`、`isPunct`、`isName`、`depthDelta`、`upper`（都在 `sqlx` 包内）。
- Produces:
  ```go
  type Source struct {
      Qualifier string   // database (MySQL) or schema (PostgreSQL) written before the name
      Table     string
      Columns   []string // the table column behind each result column, as written
  }
  func ResultSource(d Dialect, stmt string, cols []string) (Source, string)
  ```
  第二个返回值是不可编辑的简短原因（"" 表示可编辑），例如 `"JOIN"`、`"GROUP BY"`、`"column 2 is an expression"`。UI 负责加前缀 `read-only result: `。

- [ ] **Step 1: 写失败的测试**

```go
package sqlx

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestResultSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Dialect
		stmt string
		cols []string
		want Source
		why  string
	}{
		{name: "star", stmt: "SELECT * FROM users", cols: []string{"id", "name"},
			want: Source{Table: "users", Columns: []string{"id", "name"}}},
		{name: "qualified table and alias star", stmt: "select u.* from app.users u where u.id > 3 order by name limit 10",
			cols: []string{"id", "name"}, want: Source{Qualifier: "app", Table: "users", Columns: []string{"id", "name"}}},
		{name: "column list with aliases", stmt: "SELECT id, u.name AS n, note x FROM users AS u",
			cols: []string{"id", "n", "x"}, want: Source{Table: "users", Columns: []string{"id", "name", "note"}}},
		{name: "quoted names", d: Postgres, stmt: `SELECT "Id" FROM "public"."Users"`,
			cols: []string{"Id"}, want: Source{Qualifier: "public", Table: "Users", Columns: []string{"Id"}}},
		{name: "star with extra column", stmt: "SELECT *, name AS n2 FROM users",
			cols: []string{"id", "name", "n2"}, why: "column name appears twice"},
		{name: "subquery in WHERE is fine", stmt: "SELECT * FROM users WHERE id IN (SELECT uid FROM orders)",
			cols: []string{"id"}, want: Source{Table: "users", Columns: []string{"id"}}},
		{name: "for update", stmt: "SELECT * FROM users FOR UPDATE", cols: []string{"id"},
			want: Source{Table: "users", Columns: []string{"id"}}},
		{name: "duplicate column", stmt: "SELECT id, name, name FROM users", cols: []string{"id", "name", "name"},
			why: "column name appears twice"},
		{name: "join", stmt: "SELECT * FROM users u JOIN orders o ON o.uid = u.id", cols: []string{"id"}, why: "JOIN"},
		{name: "comma join", stmt: "SELECT * FROM users, orders", cols: []string{"id"}, why: "JOIN"},
		{name: "union", stmt: "SELECT id FROM a UNION SELECT id FROM b", cols: []string{"id"}, why: "UNION"},
		{name: "group by", stmt: "SELECT name FROM users GROUP BY name", cols: []string{"name"}, why: "GROUP BY"},
		{name: "having", stmt: "SELECT name FROM users HAVING name > 'a'", cols: []string{"name"}, why: "HAVING"},
		{name: "distinct", stmt: "SELECT DISTINCT name FROM users", cols: []string{"name"}, why: "DISTINCT"},
		{name: "subquery in FROM", stmt: "SELECT * FROM (SELECT * FROM users) q", cols: []string{"id"}, why: "a subquery in FROM"},
		{name: "expression", stmt: "SELECT id, upper(name) FROM users", cols: []string{"id", "upper(name)"},
			why: "column 2 is an expression"},
		{name: "constant", stmt: "SELECT id, 1 FROM users", cols: []string{"id", "1"}, why: "column 2 is an expression"},
		{name: "window", stmt: "SELECT id, row_number() OVER () FROM users", cols: []string{"id", "row_number"},
			why: "column 2 is an expression"},
		{name: "with", stmt: "WITH x AS (SELECT 1) SELECT * FROM x", cols: []string{"1"}, why: "WITH"},
		{name: "no from", stmt: "SELECT 1", cols: []string{"1"}, why: "no FROM"},
		{name: "not a select", stmt: "SHOW TABLES", cols: []string{"Tables_in_app"}, why: "not a SELECT"},
		{name: "other table's column", stmt: "SELECT o.id FROM users u", cols: []string{"id"},
			why: "column 1 names another table"},
		{name: "column count differs", stmt: "SELECT id FROM users", cols: []string{"id", "name"},
			why: "the result does not match the select list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, why := ResultSource(tc.d, tc.stmt, tc.cols)
			if why != tc.why {
				t.Fatalf("why = %q, want %q", why, tc.why)
			}
			if tc.why == "" {
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Fatalf("source (-want +got):\n%s", diff)
				}
			}
		})
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/sqlx -run TestResultSource`
Expected: FAIL，`undefined: ResultSource`

- [ ] **Step 3: 实现**

```go
package sqlx

import (
	"fmt"
	"strings"
)

// Source is the table a SELECT reads its rows from, when each result
// column is one of its columns.
type Source struct {
	Qualifier string   // database (MySQL) or schema (PostgreSQL) written before the name
	Table     string
	Columns   []string // the table column behind each result column, as written
}

var afterTable = map[string]string{
	"JOIN": "JOIN", "STRAIGHT_JOIN": "JOIN", "UNION": "UNION", "INTERSECT": "INTERSECT",
	"EXCEPT": "EXCEPT", "GROUP": "GROUP BY", "HAVING": "HAVING", "WINDOW": "a window function",
}

// selectItem is one entry of a select list: * or t.*, or [t.]col [[AS] alias].
type selectItem struct {
	star      bool
	qualifier string
	column    string
	output    string // the name the result gives it
}

// ResultSource reports the table behind the rows stmt returns, cols being
// the result's column names, or why the rows cannot be traced to one table
// ("" when they can): only a single-table SELECT of plain columns can.
func ResultSource(d Dialect, stmt string, cols []string) (Source, string) {
	toks := codeTokens(stmt, d)
	switch {
	case len(toks) > 0 && isWord(toks[0], "WITH"):
		return Source{}, "WITH"
	case len(toks) == 0 || !isWord(toks[0], "SELECT"):
		return Source{}, "not a SELECT"
	}
	start := 1
	if start < len(toks) && (isWord(toks[start], "DISTINCT") || isWord(toks[start], "DISTINCTROW")) {
		return Source{}, "DISTINCT"
	}
	if start < len(toks) && isWord(toks[start], "ALL") {
		start++
	}
	from := depth0Index(toks, start, "FROM")
	if from < 0 {
		return Source{}, "no FROM"
	}
	if from+1 < len(toks) && isPunct(toks[from+1], "(") {
		return Source{}, "a subquery in FROM"
	}
	use, next, ok := tableUse(toks, from+1)
	if !ok {
		return Source{}, "not a single-table SELECT"
	}
	depth := 0
	for j := next; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth != 0 {
			continue
		}
		if isPunct(toks[j], ",") {
			return Source{}, "JOIN"
		}
		if toks[j].kind == tokWord {
			if why, bad := afterTable[upper(toks[j])]; bad {
				return Source{}, why
			}
		}
	}
	items, why := selectItems(toks[start:from])
	if why != "" {
		return Source{}, why
	}
	src := Source{Qualifier: use.Qualifier, Table: use.Name}
	stars := false
	for i, it := range items {
		if it.qualifier != "" && !strings.EqualFold(it.qualifier, use.Name) && !strings.EqualFold(it.qualifier, use.Alias) {
			return Source{}, fmt.Sprintf("column %d names another table", i+1)
		}
		stars = stars || it.star
	}
	if !stars {
		if len(items) != len(cols) {
			return Source{}, "the result does not match the select list"
		}
		for _, it := range items {
			src.Columns = append(src.Columns, it.column)
		}
	} else {
		for _, c := range cols {
			col := c
			for _, it := range items {
				if !it.star && strings.EqualFold(it.output, c) {
					col = it.column
				}
			}
			src.Columns = append(src.Columns, col)
		}
	}
	seen := map[string]bool{}
	for _, c := range src.Columns {
		k := strings.ToLower(c)
		if seen[k] {
			return Source{}, "column " + c + " appears twice"
		}
		seen[k] = true
	}
	return src, ""
}

// selectItems reads a select list made only of plain columns.
func selectItems(toks []token) ([]selectItem, string) {
	var items []selectItem
	var cur []token
	depth := 0
	flush := func() string {
		it, ok := readItem(cur)
		if !ok {
			return fmt.Sprintf("column %d is an expression", len(items)+1)
		}
		items = append(items, it)
		cur = nil
		return ""
	}
	for _, t := range toks {
		depth += depthDelta(t)
		if depth == 0 && isPunct(t, ",") {
			if why := flush(); why != "" {
				return nil, why
			}
			continue
		}
		cur = append(cur, t)
	}
	if why := flush(); why != "" {
		return nil, why
	}
	return items, ""
}

// readItem reads "*", "t.*", "col", "t.col", each but the stars optionally
// followed by [AS] alias.
func readItem(toks []token) (selectItem, bool) {
	var it selectItem
	j := 0
	switch {
	case len(toks) == 1 && isPunct(toks[0], "*"):
		return selectItem{star: true}, true
	case len(toks) == 3 && isName(toks[0]) && isPunct(toks[1], ".") && isPunct(toks[2], "*"):
		return selectItem{star: true, qualifier: toks[0].text}, true
	case len(toks) >= 3 && isName(toks[0]) && isPunct(toks[1], ".") && isName(toks[2]):
		it.qualifier, it.column, j = toks[0].text, toks[2].text, 3
	case len(toks) >= 1 && isName(toks[0]) && !(toks[0].kind == tokWord && IsNumber(toks[0].text)):
		it.column, j = toks[0].text, 1
	default:
		return it, false
	}
	it.output = it.column
	if j < len(toks) && isWord(toks[j], "AS") {
		j++
	}
	if j < len(toks) && isName(toks[j]) {
		it.output = toks[j].text
		j++
	}
	return it, j == len(toks)
}
```

注意：测试 `star with extra column` 期望 `"column name appears twice"`——`*` 展开出 `name`，`name AS n2` 又把 `n2` 对应到 `name`。

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/sqlx -run TestResultSource`
Expected: PASS。如果某个关键字（如 `FROM` 后面的 `WHERE`）被 `tableUse` 误读成别名，检查 `aliasStop` 是否包含它，不要在 `ResultSource` 里绕开。

- [ ] **Step 5: 提交**

```bash
git add internal/sqlx/source.go internal/sqlx/source_test.go
git commit -m "feat(sqlx): ResultSource 判断查询结果能否对应回单表"
```

---

### Task 2: `sqlx.Filtered` — 过滤条件的 SQL 改写

**Files:**
- Create: `internal/sqlx/filtered.go`
- Test: `internal/sqlx/filtered_test.go`

**Interfaces:**
- Consumes: `codeTokens`、`depth0Index`、`depthDelta`、`isWord`、`isPunct`、`isName`、`upper`。
- Produces:
  ```go
  var ErrNotSelect = errors.New("filter works on SELECT results")
  func Filtered(d Dialect, stmt, cond string, merge bool, cols []string) (sql string, ordered bool, err error)
  ```
  `merge` 为 true 时（`ResultSource` 接受了 stmt）把条件合并进顶层 WHERE；否则包成子查询。`cond == ""` 返回去掉末尾分号的 stmt。`ordered` 为 false 表示原 ORDER BY 无法保留。

- [ ] **Step 1: 写失败的测试**

```go
package sqlx

import (
	"errors"
	"testing"
)

func TestFiltered(t *testing.T) {
	cols := []string{"id", "name"}
	for _, tc := range []struct {
		name    string
		stmt    string
		merge   bool
		want    string
		ordered bool
	}{
		{name: "merge without WHERE", stmt: "select * from users", merge: true,
			want: "select * from users\nWHERE (a = 1\n)", ordered: true},
		{name: "merge before ORDER BY", stmt: "select * from users order by name limit 5", merge: true,
			want: "select * from users WHERE (a = 1\n) order by name limit 5", ordered: true},
		{name: "merge into WHERE", stmt: "select * from users where id > 3 or name = 'x' order by name", merge: true,
			want: "select * from users where (id > 3 or name = 'x'\n) AND (a = 1\n) order by name", ordered: true},
		{name: "where ends in a comment", stmt: "select * from users where id > 3 -- note\norder by id", merge: true,
			want: "select * from users where (id > 3 -- note\n) AND (a = 1\n) order by id", ordered: true},
		{name: "trailing semicolon", stmt: "select * from users;", merge: true,
			want: "select * from users\nWHERE (a = 1\n)", ordered: true},
		{name: "wrap", stmt: "select count(*) n from users",
			want: "SELECT * FROM (\nselect count(*) n from users\n) q WHERE (a = 1\n)", ordered: true},
		{name: "wrap moves ORDER BY out", stmt: "select u.id, o.name from users u join o on o.uid = u.id order by name desc, 1",
			want: "SELECT * FROM (\nselect u.id, o.name from users u join o on o.uid = u.id\n) q WHERE (a = 1\n)\norder by name desc, 1", ordered: true},
		{name: "wrap keeps ORDER BY with LIMIT inside", stmt: "select * from a join b order by a.x limit 3",
			want: "SELECT * FROM (\nselect * from a join b order by a.x limit 3\n) q WHERE (a = 1\n)", ordered: true},
		{name: "wrap cannot move a qualified ORDER BY", stmt: "select * from a join b order by a.x",
			want: "SELECT * FROM (\nselect * from a join b order by a.x\n) q WHERE (a = 1\n)", ordered: false},
		{name: "wrap a WITH query", stmt: "with x as (select 1 id) select * from x",
			want: "SELECT * FROM (\nwith x as (select 1 id) select * from x\n) q WHERE (a = 1\n)", ordered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ordered, err := Filtered(MySQL, tc.stmt, "a = 1", tc.merge, cols)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || ordered != tc.ordered {
				t.Fatalf("got %q ordered=%v\nwant %q ordered=%v", got, ordered, tc.want, tc.ordered)
			}
		})
	}
}

func TestFilteredEmptyAndNotSelect(t *testing.T) {
	if got, _, _ := Filtered(MySQL, "select * from users;", "", true, nil); got != "select * from users" {
		t.Fatalf("no condition: %q", got)
	}
	for _, s := range []string{"show tables", "explain select 1", "update t set a = 1"} {
		if _, _, err := Filtered(MySQL, s, "a = 1", false, nil); !errors.Is(err, ErrNotSelect) {
			t.Errorf("%s: err = %v", s, err)
		}
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/sqlx -run TestFiltered`
Expected: FAIL，`undefined: Filtered`

- [ ] **Step 3: 实现**

```go
package sqlx

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

// ErrNotSelect is returned for a statement that a filter cannot narrow.
var ErrNotSelect = errors.New("filter works on SELECT results")

// whereEnd ends a WHERE clause, or marks where a missing one goes.
var whereEnd = wordSet("GROUP", "HAVING", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH", "FOR", "LOCK", "UNION", "INTERSECT", "EXCEPT")

// Filtered narrows stmt to the rows matching cond, a condition without
// WHERE. With merge stmt is a single-table SELECT (ResultSource accepted
// it) and cond is ANDed into its WHERE. Otherwise stmt is wrapped:
// SELECT * FROM (stmt) q WHERE cond, a trailing ORDER BY that names only
// result columns (cols) or positions moving outside, since MySQL drops an
// ORDER BY without LIMIT inside a derived table; ordered is false when one
// could not be kept. Each condition closes on its own line so a trailing
// -- comment cannot swallow what follows.
func Filtered(d Dialect, stmt, cond string, merge bool, cols []string) (sql string, ordered bool, err error) {
	stmt = strings.TrimRight(strings.TrimSpace(stmt), "; \t\r\n")
	toks := codeTokens(stmt, d)
	if len(toks) == 0 || !isWord(toks[0], "SELECT") && !isWord(toks[0], "WITH") && !isPunct(toks[0], "(") {
		return "", false, ErrNotSelect
	}
	if cond == "" {
		return stmt, true, nil
	}
	if merge {
		return mergeWhere(toks, stmt, cond), true, nil
	}
	return wrapWhere(toks, stmt, cond, cols)
}

func mergeWhere(toks []token, stmt, cond string) string {
	from := depth0Index(toks, 0, "FROM")
	where := depth0Index(toks, from, "WHERE")
	if where < 0 {
		at := clauseAt(toks, from+1)
		if at < 0 {
			return stmt + "\nWHERE (" + cond + "\n)"
		}
		return stmt[:at] + "WHERE (" + cond + "\n) " + stmt[at:]
	}
	open := toks[where].end
	end := clauseAt(toks, where+1)
	rest := ""
	if end < 0 {
		end = len(stmt)
	} else {
		rest = " " + stmt[end:]
	}
	return stmt[:open] + " (" + strings.TrimSpace(stmt[open:end]) + "\n) AND (" + cond + "\n)" + rest
}

// clauseAt is the byte offset of the first top-level clause that ends a
// WHERE, from toks[from] on; -1 when there is none.
func clauseAt(toks []token, from int) int {
	depth := 0
	for j := from; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth == 0 && toks[j].kind == tokWord && whereEnd[upper(toks[j])] {
			return toks[j].pos
		}
	}
	return -1
}

func wrapWhere(toks []token, stmt, cond string, cols []string) (string, bool, error) {
	wrap := func(inner string) string { return "SELECT * FROM (\n" + inner + "\n) q WHERE (" + cond + "\n)" }
	order := lastOrderBy(toks)
	if order < 0 {
		return wrap(stmt), true, nil
	}
	depth := 0
	for j := order + 2; j < len(toks); j++ {
		depth += depthDelta(toks[j])
		if depth == 0 && toks[j].kind == tokWord && (isWord(toks[j], "LIMIT") || isWord(toks[j], "OFFSET") || isWord(toks[j], "FETCH")) {
			return wrap(stmt), true, nil // LIMIT keeps the ORDER BY inside meaningful
		}
	}
	if !plainOrder(toks[order+2:], cols) {
		return wrap(stmt), false, nil
	}
	at := toks[order].pos
	return wrap(strings.TrimSpace(stmt[:at])) + "\n" + stmt[at:], true, nil
}

// lastOrderBy is the index of the top-level ORDER of a trailing ORDER BY.
func lastOrderBy(toks []token) int {
	depth, at := 0, -1
	for j := range toks {
		depth += depthDelta(toks[j])
		if depth == 0 && isWord(toks[j], "ORDER") && j+1 < len(toks) && isWord(toks[j+1], "BY") {
			at = j
		}
	}
	return at
}

// plainOrder reports ORDER BY terms that are each a result column or a
// position, optionally with ASC/DESC and NULLS FIRST/LAST.
func plainOrder(toks []token, cols []string) bool {
	var term []token
	ok := func() bool {
		if len(term) == 0 {
			return false
		}
		name := term[0]
		named := isName(name) && slices.ContainsFunc(cols, func(c string) bool { return strings.EqualFold(c, name.text) })
		if _, err := strconv.Atoi(name.text); err != nil && !named {
			return false
		}
		for _, t := range term[1:] {
			if !isWord(t, "ASC") && !isWord(t, "DESC") && !isWord(t, "NULLS") && !isWord(t, "FIRST") && !isWord(t, "LAST") {
				return false
			}
		}
		return true
	}
	for _, t := range toks {
		if isPunct(t, ",") {
			if !ok() {
				return false
			}
			term = nil
			continue
		}
		term = append(term, t)
	}
	return ok()
}
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/sqlx -run 'TestFiltered'`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/sqlx/filtered.go internal/sqlx/filtered_test.go
git commit -m "feat(sqlx): Filtered 把过滤条件合并进 WHERE 或包成子查询"
```

---

### Task 3: `edit` — 结果列到表列的对应，以及 `ApplyInTx`

**Files:**
- Modify: `internal/edit/plan.go`（`Target` 和用到列名的地方）
- Modify: `internal/edit/apply.go`（新增 `ApplyInTx`、`StatementError.InTx`）
- Test: `internal/edit/plan_test.go`、`internal/edit/apply_test.go`
- Modify: `internal/db/dbtest/contract.go`（集成测试）

**Interfaces:**
- Produces:
  ```go
  type Target struct {
      Dialect sqlx.Dialect
      Table   db.TableRef
      Key     Key
      Columns []string // table column behind each result column; nil: the same names
  }
  func ApplyInTx(ctx context.Context, s db.Querier, stmts []string) error
  const Savepoint = "datavim_edit"
  // StatementError gains: InTx bool
  ```

- [ ] **Step 1: 写失败的测试**

`internal/edit/plan_test.go` 末尾追加：

```go
func TestPlanMapsResultColumnsToTableColumns(t *testing.T) {
	cols := []db.ColumnInfo{{Name: "uid", DatabaseType: "INT"}, {Name: "n", DatabaseType: "VARCHAR"}}
	tb := New(cols, [][]db.Value{{{Text: "1"}, {Text: "a"}}}, nil)
	_ = tb.SetCell(0, 1, db.Value{Text: "b"})
	at := tb.InsertAfter(0)
	_ = tb.SetCell(at, 1, db.Value{Text: "c"})
	tg := Target{
		Dialect: sqlx.MySQL, Table: db.TableRef{Name: "users"},
		Key: Key{Kind: PrimaryKey, Columns: []string{"id"}}, Columns: []string{"id", "name"},
	}
	stmts, _, err := Plan(tb, tg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"UPDATE `users` SET `name` = 'b' WHERE `id` = 1",
		"INSERT INTO `users` (`name`) VALUES ('c')",
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Fatalf("stmts (-want +got):\n%s", diff)
	}
}
```

`internal/edit/apply_test.go` 末尾追加：

```go
func TestApplyInTxReleasesItsSavepoint(t *testing.T) {
	f := &fakeQuerier{}
	if err := ApplyInTx(context.Background(), f, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"SAVEPOINT datavim_edit", "a", "b", "RELEASE SAVEPOINT datavim_edit"}
	if diff := cmp.Diff(want, f.execs); diff != "" {
		t.Fatalf("execs (-want +got):\n%s", diff)
	}
}

func TestApplyInTxRollsBackToItsSavepoint(t *testing.T) {
	f := &fakeQuerier{affected: map[string]int64{"b": 2}}
	err := ApplyInTx(context.Background(), f, []string{"a", "b", "c"})
	want := []string{"SAVEPOINT datavim_edit", "a", "b", "ROLLBACK TO SAVEPOINT datavim_edit"}
	if diff := cmp.Diff(want, f.execs); diff != "" {
		t.Fatalf("execs (-want +got):\n%s", diff)
	}
	var se *StatementError
	if !errors.As(err, &se) || se.Index != 1 || !se.InTx {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "the transaction is still open") {
		t.Fatalf("message: %v", err)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/edit -run 'TestPlanMaps|TestApplyInTx'`
Expected: FAIL，`unknown field Columns` 和 `undefined: ApplyInTx`

- [ ] **Step 3: 实现 `Target.Columns`**

在 `plan.go` 的 `Target` 里加字段，并加一个辅助方法：

```go
// Target is the table staged changes are written to.
type Target struct {
	Dialect sqlx.Dialect
	Table   db.TableRef
	Key     Key
	// Columns names the table column behind each result column (a console
	// result may rename them); nil when they are the same.
	Columns []string
}

// column is result column i under its table column's name.
func (tg Target) column(cols []db.ColumnInfo, i int) db.ColumnInfo {
	c := cols[i]
	if tg.Columns != nil {
		c.Name = tg.Columns[i]
	}
	return c
}
```

再把三处直接用 `c.Name` / `cols[i]` 的地方换成 `tg.column(cols, i)`：

- `Plan` 的 `Modified` 分支：`for i := range t.cols { if r.Set[i] { c := tg.column(t.cols, i); sets = append(sets, sqlx.QuoteIdent(d, c.Name)+" = "+sqlx.Literal(d, c.DatabaseType, r.Values[i])) } }`
- `where` 的 `PrimaryKey, UniqueKey` 分支：
  ```go
  for _, name := range tg.Key.Columns {
      i := slices.IndexFunc(cols, func(c db.ColumnInfo) bool { return c.Name == name })
      if tg.Columns != nil {
          i = slices.Index(tg.Columns, name)
      }
      if i < 0 {
          return "", fmt.Errorf("key column %s is not in the result", name)
      }
      parts = append(parts, match(d, tg.column(cols, i), r.Orig[i]))
  }
  ```
- `where` 的 `AllColumns` 分支：`for i := range cols { c := tg.column(cols, i); if !incomparable[...] { parts = append(parts, match(d, c, r.Orig[i])) } }`
- `insert`：`for i := range cols { if r.Set[i] { c := tg.column(cols, i); names = append(names, sqlx.QuoteIdent(d, c.Name)); vals = append(vals, sqlx.Literal(d, c.DatabaseType, r.Values[i])) } }`

- [ ] **Step 4: 实现 `ApplyInTx`**

在 `apply.go` 里：

```go
// StatementError is a statement that failed or did not change exactly one
// row; the changes were rolled back.
type StatementError struct {
	Index int // 0-based
	SQL   string
	Err   error
	InTx  bool // rolled back to the savepoint: the transaction is still open
}

func (e *StatementError) Error() string {
	if e.InTx {
		return fmt.Sprintf("statement %d: %v · these changes were rolled back, the transaction is still open", e.Index+1, e.Err)
	}
	return fmt.Sprintf("statement %d: %v · rolled back, nothing was changed", e.Index+1, e.Err)
}

// Savepoint is where ApplyInTx rolls back to.
const Savepoint = "datavim_edit"

// ApplyInTx runs stmts in the transaction already open on s (a db.Session)
// and leaves it open. Like Apply, every statement must change exactly one
// row; otherwise the transaction is rolled back to a savepoint taken
// first, keeping what ran in it before, and a *StatementError names the
// statement.
func ApplyInTx(ctx context.Context, s db.Querier, stmts []string) error {
	if _, err := s.Exec(ctx, "SAVEPOINT "+Savepoint); err != nil {
		return fmt.Errorf("savepoint: %w", err)
	}
	for i, q := range stmts {
		res, err := s.Exec(ctx, q)
		if err == nil && res.RowsAffected != 1 {
			err = &CountError{Affected: res.RowsAffected}
		}
		if err != nil {
			rollbackTo(s)
			return &StatementError{Index: i, SQL: q, Err: err, InTx: true}
		}
	}
	if _, err := s.Exec(ctx, "RELEASE SAVEPOINT "+Savepoint); err != nil {
		rollbackTo(s)
		return fmt.Errorf("release savepoint: %w", err)
	}
	return nil
}

// rollbackTo undoes ApplyInTx's statements even when ctx was cancelled.
// On PostgreSQL it also clears the failed state the error left.
func rollbackTo(s db.Querier) {
	ctx, cancel := context.WithTimeout(context.Background(), rollbackTimeout)
	defer cancel()
	_, _ = s.Exec(ctx, "ROLLBACK TO SAVEPOINT "+Savepoint)
}
```

- [ ] **Step 5: 运行单元测试，确认通过**

Run: `go test -race ./internal/edit`
Expected: PASS（包括原有的 `Plan` / `Apply` 测试）

- [ ] **Step 6: 契约测试**

在 `internal/db/dbtest/contract.go` 中 `"staged edits apply in one transaction"` 子测试之后加：

```go
	t.Run("ApplyInTx rolls back to its savepoint and keeps the transaction", func(t *testing.T) {
		table := ref("edit_tx")
		name := d.QualifiedName(table)
		for _, q := range []string{
			"CREATE TABLE " + name + " (id int PRIMARY KEY, name varchar(50))",
			"INSERT INTO " + name + " VALUES (1, 'a'), (2, 'b')",
		} {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		exec := func(q string) {
			t.Helper()
			if _, err := s.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		exec(sqlx.BeginSQL(sd))
		exec("UPDATE " + name + " SET name = 'before' WHERE id = 1")
		bad := []string{
			"UPDATE " + name + " SET name = 'x' WHERE id = 2",
			"UPDATE " + name + " SET name = 'y' WHERE id = 99",
		}
		var se *edit.StatementError
		if err := edit.ApplyInTx(ctx, s, bad); !errors.As(err, &se) || se.Index != 1 {
			t.Fatalf("err = %v", err)
		}
		if err := edit.ApplyInTx(ctx, s, []string{"UPDATE " + name + " SET name = 'after' WHERE id = 2"}); err != nil {
			t.Fatalf("the transaction is usable after the rollback: %v", err)
		}
		exec("COMMIT")
		want := [][]db.Value{{{Text: "1"}, {Text: "before"}}, {{Text: "2"}, {Text: "after"}}}
		if diff := cmp.Diff(want, read(t, "SELECT * FROM "+name+" ORDER BY id").Rows); diff != "" {
			t.Fatalf("rows (-want +got):\n%s", diff)
		}
	})
```

Run: `make test-integration`（先 `orb start`）
Expected: PASS on MySQL 8 and PostgreSQL 16

- [ ] **Step 7: 提交**

```bash
git add internal/edit internal/db/dbtest/contract.go
git commit -m "feat(edit): Target.Columns 对应结果列，ApplyInTx 在已打开的事务里用 SAVEPOINT 提交"
```

---

### Task 4: keymap — 结果区（Results）加上过滤与编辑键

**Files:**
- Modify: `internal/ui/keymap/defaults.go:77-106`
- Test: `internal/ui/keymap/keymap_test.go`

**Interfaces:**
- Produces: Results 上下文里 `ff f! fs f~ fl f> f< fn fN fx F i enter o dd u U <leader>w` 绑定到和 Grid 相同的 Action。Grid 仍然独有 `[ ] s`。

- [ ] **Step 1: 写失败的测试**

```go
func TestResultsShareFilterAndEditKeys(t *testing.T) {
	k := Default()
	for _, tc := range []struct {
		keys []string
		want Action
	}{
		{[]string{"f", "f"}, FilterEqual},
		{[]string{"F"}, FilterPrompt},
		{[]string{"i"}, EditCell},
		{[]string{"o"}, InsertRow},
		{[]string{"d", "d"}, DeleteRow},
		{[]string{"U"}, UndoAllEdits},
	} {
		for _, ctx := range []Context{Grid, Results} {
			if a, r := k.Resolve(ctx, tc.keys); r != Matched || a != tc.want {
				t.Errorf("%s %v = %v %v, want %v", ctx, tc.keys, a, r, tc.want)
			}
		}
	}
	if a, r := k.Resolve(Results, []string{"s"}); r == Matched && a == SortColumn {
		t.Error("results do not sort")
	}
	if a, _ := k.Resolve(Results, []string{"]"}); a != LoadMore {
		t.Errorf("] in results = %v", a)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/ui/keymap -run TestResultsShare`
Expected: FAIL（Results 里 `f f` 没有绑定）

- [ ] **Step 3: 实现**

在 `defaults.go` 把 Grid 里过滤和编辑的绑定移到一个函数里，Grid 和 Results 都用它：

```go
// filterEditBindings are the filter and edit keys shared by table data and
// console results.
func filterEditBindings(ctx Context) []Binding {
	return []Binding{
		{ctx, keys("f", "f"), FilterEqual, "Filter: equals this value"},
		{ctx, keys("f", "!"), FilterNotEqual, "Filter: not this value"},
		{ctx, keys("f", "s"), FilterContains, "Filter: contains… (LIKE %…%)"},
		{ctx, keys("f", "~"), FilterContains, "Filter: contains… (LIKE %…%)"},
		{ctx, keys("f", "l"), FilterLike, "Filter: LIKE pattern… (% any, _ one char)"},
		{ctx, keys("f", ">"), FilterGreater, "Filter: greater than…"},
		{ctx, keys("f", "<"), FilterLess, "Filter: less than…"},
		{ctx, keys("f", "n"), FilterNull, "Filter: is NULL"},
		{ctx, keys("f", "N"), FilterNotNull, "Filter: is not NULL"},
		{ctx, keys("f", "x"), FilterClear, "Clear the filter"},
		{ctx, keys("F"), FilterPrompt, "Edit the filter (WHERE)"},
		{ctx, keys("i"), EditCell, "Edit cell"},
		{ctx, keys("enter"), EditCell, "Edit cell"},
		{ctx, keys("o"), InsertRow, "Add a row below"},
		{ctx, keys("d", "d"), DeleteRow, "Delete row"},
		{ctx, keys("u"), UndoEdit, "Undo the last change"},
		{ctx, keys("U"), UndoAllEdits, "Discard every change"},
		{ctx, keys(LeaderToken, "w"), SubmitEdits, "Submit changes (:w)"},
	}
}
```

Grid 段落变成：

```go
	b = append(b, gridBindings(Grid)...)
	b = append(b,
		Binding{Grid, keys("["), PrevPage, "Previous page"},
		Binding{Grid, keys("]"), NextPage, "Next page"},
		Binding{Grid, keys("s"), SortColumn, "Sort by column: asc, desc, off"},
	)
	b = append(b, filterEditBindings(Grid)...)

	b = append(b, gridBindings(Results)...)
	b = append(b, filterEditBindings(Results)...)
	b = append(b,
		Binding{Results, keys("]"), LoadMore, "Load more rows"},
		// … the rest of Results unchanged
	)
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/ui/...`
Expected: PASS。如果 help / which-key 的快照测试因为 Results 多了条目而失败，确认新条目正确后更新期望值。

- [ ] **Step 5: 提交**

```bash
git add internal/ui/keymap
git commit -m "feat(keymap): 控制台结果区加上过滤与编辑键"
```

---

### Task 5: 重构 — 从 `dataTab` 抽出 `editableResult`

纯重构，行为不变。现有测试一行不改，全部通过。

**Files:**
- Create: `internal/ui/editable.go`
- Delete: `internal/ui/datatab_edit.go` 的大部分内容（只留 `dataTab` 的 `applyEdits` / `applied` 和消息类型）
- Modify: `internal/ui/tabs.go`（`dataTab` 结构体、`newDataTab`、`Handle`、`Update`、`pageLoaded`、过滤函数）
- Test: 现有的 `datatab_edit_test.go`、`filter_test.go`、`submit_test.go`、`tabs_test.go`

**Interfaces:**
- Produces（Task 6、7 依赖）：
  ```go
  type resultHost interface {
      setStatus(s string, isErr bool)
      busy() string                 // why the rows cannot change now ("" when they can)
      filter() string               // the filter shown, without WHERE; "" for none
      filterTitle() string          // "Filter users"
      checkFilter(s string) error   // validates text typed at the F prompt
      refilter(where string) tea.Cmd
      applyEdits(stmts []string) tea.Cmd
  }

  type editableResult struct {
      id      int
      backend Backend
      conn    string
      dialect sqlx.Dialect
      keys    *keymap.Keymap
      keyCtx  keymap.Context
      grid    *grid.Model
      host    resultHost

      ref      db.TableRef
      cols     []db.Column
      key      edit.Key
      mapping  []string
      table    *edit.Table
      readOnly string
      addOnly  string

      submitting  bool
      previewGen  int
      pendingSQL  []string
      retrySubmit bool
      failedWhere string
  }

  func (r *editableResult) changes() int
  func (r *editableResult) mark(row, col int) grid.Mark
  func (r *editableResult) showChanges()
  func (r *editableResult) unsavedHint(submit, discard string) string
  func (r *editableResult) unsavedNote() string
  func (r *editableResult) submit() tea.Cmd
  func (r *editableResult) handle(a keymap.Action) (tea.Cmd, bool)
  func (r *editableResult) update(msg tea.Msg) (tea.Cmd, bool)
  ```
  `submitConfirmedMsg.gen` 现在与 `previewGen` 比较。

- [ ] **Step 1: 确认基线**

Run: `go test -race ./internal/ui`
Expected: PASS（记下测试数量，重构后要一致）

- [ ] **Step 2: 新建 `internal/ui/editable.go`**

```go
package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/edit"
	"github.com/shepherd-yun/datavim/internal/runner"
	"github.com/shepherd-yun/datavim/internal/sqlx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/complete"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
)

// resultHost is the tab an editableResult belongs to: it reloads the rows
// with another filter and runs confirmed changes.
type resultHost interface {
	setStatus(s string, isErr bool)
	// busy says why the rows cannot change now ("" when they can).
	busy() string
	// filter is the filter shown, without WHERE; "" for none.
	filter() string
	// filterTitle titles the filter prompts.
	filterTitle() string
	// checkFilter validates text typed at the F prompt.
	checkFilter(s string) error
	refilter(where string) tea.Cmd
	applyEdits(stmts []string) tea.Cmd
}

// editableResult is a grid of rows that can be filtered and edited: a
// table data tab's page, or a console's result set (spec §5.8).
type editableResult struct {
	id      int // the owning tab
	backend Backend
	conn    string
	dialect sqlx.Dialect
	keys    *keymap.Keymap
	keyCtx  keymap.Context // where hints look up keys
	grid    *grid.Model
	host    resultHost

	ref      db.TableRef // the table changes are written to
	cols     []db.Column // its columns, for types in the cell editor and F completion
	key      edit.Key    // how rows are located
	mapping  []string    // the table column behind each result column; nil: the same names
	table    *edit.Table // the rows with their staged changes
	readOnly string      // why no change can be staged ("" when one can)
	addOnly  string      // why rows read from the server cannot change ("" when they can)

	submitting  bool     // a submit is running
	previewGen  int      // tells the current preview from stale ones
	pendingSQL  []string // the previewed statements, until confirmed
	retrySubmit bool     // the last submit wanted a password
	failedWhere string   // a filter the server rejected, offered again by F
}
```

然后把下面这些从 `datatab_edit.go` / `tabs.go` 搬过来，接收者改成 `r *editableResult`，`t.setStatus` 改成 `r.host.setStatus`，`t.gen` 改成 `r.previewGen`：

- `changes`、`mark`、`showChanges`、`editCell`、`columnTitle`、`cellEdited`、`insertRow`、`deleteRow`、`undoEdit`、`undoAll`：函数体不变。
- `unsavedHint`：`keymap.Grid` 改成 `r.keyCtx`。
- `editBlocked`：

  ```go
  // editBlocked says why the rows cannot be changed now ("" when they can);
  // existing is set for changes to rows read from the server.
  func (r *editableResult) editBlocked(existing bool) string {
  	cfg, _ := r.backend.Connection(r.conn)
  	switch {
  	case r.table == nil:
  		return "wait for the rows to load"
  	case r.host.busy() != "":
  		return r.host.busy()
  	case r.submitting:
  		return "submitting changes…"
  	case r.readOnly != "":
  		return r.readOnly
  	case cfg.IsReadOnly():
  		return readOnlyHint
  	case existing && r.addOnly != "":
  		return r.addOnly
  	}
  	return ""
  }
  ```

- `submit`：函数体不变，只有 `edit.Plan` 的 Target 改成 `edit.Target{Dialect: r.dialect, Table: r.ref, Key: r.key, Columns: r.mapping}`，`t.gen++` 改成 `r.previewGen++`，`submitConfirmedMsg{tabID: r.id, gen: r.previewGen}`。开头再加一个分支：`case r.host.busy() != "": r.host.setStatus(r.host.busy(), true); return nil`。
- `filterValuePrompt`、`filterPrompt`：`t.ref.Name` 的标题改成 `r.host.filterTitle()`，`t.query.where` 改成 `r.host.filter()`，`WithValidate(t.checkFilter)` 改成 `WithValidate(r.host.checkFilter)`；列通过 `r.filterColumn` 取（见下）。
- `completeFilter`：

  ```go
  // completeFilter offers the table's columns, or the result's when the
  // table is unknown, and condition keywords.
  func (r *editableResult) completeFilter(text string, cursor int) (int, []complete.Candidate) {
  	cols := r.cols
  	if len(cols) == 0 && r.table != nil {
  		for _, c := range r.table.Columns() {
  			cols = append(cols, db.Column{Name: c.Name, Type: strings.ToLower(c.DatabaseType)})
  		}
  	}
  	return completeCondition(r.dialect, cols, text, cursor)
  }
  ```

新增：

```go
// filterColumn is the column under the cursor as a filter names it: its
// table column when the filter goes into the table's WHERE.
func (r *editableResult) filterColumn() (db.ColumnInfo, bool) {
	col, ok := r.grid.Column()
	if ok && r.mapping != nil {
		_, i := r.grid.Position()
		col.Name = r.mapping[i]
	}
	return col, ok
}

// unsavedNote is "2 unsaved changes · <space>w submits · U discards".
func (r *editableResult) unsavedNote() string {
	return joinHints(plural(r.changes(), "unsaved change"), r.unsavedHint("submits", "discards"))
}

func (r *editableResult) addFilter(cond string) tea.Cmd {
	return r.host.refilter(andFilter(r.host.filter(), cond))
}

// handle runs the filter and edit actions; ok is false for other actions.
func (r *editableResult) handle(a keymap.Action) (cmd tea.Cmd, ok bool) {
	switch a {
	case keymap.FilterPrompt, keymap.FilterEqual, keymap.FilterNotEqual, keymap.FilterContains, keymap.FilterLike,
		keymap.FilterGreater, keymap.FilterLess, keymap.FilterNull, keymap.FilterNotNull, keymap.FilterClear:
		if r.changes() > 0 {
			r.host.setStatus(r.unsavedNote(), true)
			return nil, true
		}
	}
	switch a {
	case keymap.FilterEqual, keymap.FilterNotEqual:
		col, ok := r.filterColumn()
		_, v, _ := r.grid.Cell()
		if !ok {
			r.host.setStatus("no cell to filter by", true)
			return nil, true
		}
		op := "="
		if a == keymap.FilterNotEqual {
			op = "<>"
		}
		return r.addFilter(compareFilter(r.dialect, col, op, v)), true
	case keymap.FilterNull, keymap.FilterNotNull:
		col, ok := r.filterColumn()
		if !ok {
			r.host.setStatus("no column to filter by", true)
			return nil, true
		}
		return r.addFilter(nullFilter(r.dialect, col.Name, a == keymap.FilterNotNull)), true
	case keymap.FilterContains, keymap.FilterLike, keymap.FilterGreater, keymap.FilterLess:
		return r.filterValuePrompt(a), true
	case keymap.FilterClear:
		if r.host.filter() == "" {
			return nil, true
		}
		return r.host.refilter(""), true
	case keymap.FilterPrompt:
		return r.filterPrompt(), true
	case keymap.EditCell:
		return r.editCell(), true
	case keymap.InsertRow:
		r.insertRow()
	case keymap.DeleteRow:
		r.deleteRow()
	case keymap.UndoEdit:
		r.undoEdit()
	case keymap.UndoAllEdits:
		r.undoAll()
	case keymap.SubmitEdits:
		return r.submit(), true
	default:
		return nil, false
	}
	return nil, true
}

// update handles the messages of the cell editor, the submit preview and
// the filter prompts; ok is false for other messages.
func (r *editableResult) update(msg tea.Msg) (cmd tea.Cmd, ok bool) {
	switch m := msg.(type) {
	case cellEditedMsg:
		r.cellEdited(m)
	case submitConfirmedMsg:
		if m.gen == r.previewGen && r.pendingSQL != nil && !r.submitting {
			stmts := r.pendingSQL
			r.pendingSQL = nil
			return r.host.applyEdits(stmts), true
		}
	case filterMsg:
		return r.host.refilter(normFilter(m.text)), true
	case filterValueMsg:
		if m.text == "" {
			return nil, true
		}
		switch m.action {
		case keymap.FilterContains:
			return r.addFilter(containsFilter(r.dialect, m.col.Name, m.text)), true
		case keymap.FilterLike:
			return r.addFilter(likeFilter(r.dialect, m.col.Name, m.text)), true
		case keymap.FilterGreater:
			return r.addFilter(compareFilter(r.dialect, m.col, ">", db.Value{Text: m.text})), true
		case keymap.FilterLess:
			return r.addFilter(compareFilter(r.dialect, m.col, "<", db.Value{Text: m.text})), true
		}
	default:
		return nil, false
	}
	return nil, true
}
```

`filterValuePrompt` 开头的 `col, ok := t.grid.Column()` 改成 `col, ok := r.filterColumn()`；初始值仍然取 `r.grid.Cell()` 的值。`filterPrompt` 里默认列名同样用 `r.filterColumn()`。

- [ ] **Step 3: 改 `dataTab`**

`tabs.go` 的结构体，挪走的字段删掉，内嵌 `editableResult`：

```go
type dataTab struct {
	editableResult // id, backend, conn, ref, dialect, keys, grid and the staged changes

	pageSize   int
	page       int
	hasMore    bool
	loading    bool
	err        string
	status     string // what the last key did; cleared by the next one
	statusErr  bool
	query      tableQuery
	known      bool // meta (key, columns) has been read
	store      Store
	height     int
	wantColumn string // column to put the cursor on once data is shown
}

func newDataTab(id int, b Backend, st Store, conn string, ref db.TableRef, s config.Settings) *dataTab {
	cfg, _ := b.Connection(conn)
	t := &dataTab{
		editableResult: editableResult{
			id: id, backend: b, conn: conn, ref: ref, dialect: sqlx.DialectOf(cfg.Driver),
			grid: grid.New(nil, s.MaxColumnWidth), keys: keymap.Default(), keyCtx: keymap.Grid,
		},
		store: st, pageSize: s.PageSize,
	}
	t.host = t
	t.grid.SetMarks(t.mark)
	return t
}
```

注意：如果别处有 `t.keys = …` 的赋值（例如加载用户 keymap 时），因为字段被提升，写法不变。

`dataTab` 实现 `resultHost`（放在 `datatab_edit.go`）：

```go
func (t *dataTab) busy() string {
	if t.loading {
		return "wait for the rows to load"
	}
	return ""
}

func (t *dataTab) filter() string      { return t.query.where }
func (t *dataTab) filterTitle() string { return "Filter " + t.ref.Name }

func (t *dataTab) refilter(where string) tea.Cmd {
	return t.applyQuery(func(q *tableQuery) { q.where = where })
}
```

`checkFilter` 已经是 `func (t *dataTab) checkFilter(s string) error`，保留。原来的 `apply` 改名为 `applyEdits`，函数体不变（第一行仍然是 `t.submitting = true`）。删掉 `tabs.go` 里的 `addFilter`、`filterValuePrompt`、`filterPrompt`、`completeFilter`（已经搬走）。

`pageLoaded` 里设置 key 的地方：

```go
	if m.meta != nil {
		t.cols, t.key = m.meta.cols, m.meta.key
		t.addOnly = ""
		if t.key.Kind == edit.NoKey {
			t.addOnly = t.ref.Name + " has no primary or unique key: rows can only be added"
		}
	}
```

`Handle`：

```go
func (t *dataTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	if cmd, ok := gridAction(t.grid, a); ok {
		return cmd
	}
	switch a {
	case keymap.NextPage, keymap.PrevPage, keymap.Refresh, keymap.SortColumn:
		if t.changes() > 0 {
			t.setStatus(t.unsavedNote(), true)
			return nil
		}
	}
	switch a {
	case keymap.NextPage:
		if t.hasMore && !t.loading {
			return t.load(t.page+1, t.query)
		}
		return nil
	case keymap.PrevPage:
		if t.page > 0 && !t.loading {
			return t.load(t.page-1, t.query)
		}
		return nil
	case keymap.Refresh:
		if !t.loading {
			t.known = false
			return t.load(t.page, t.query)
		}
		return nil
	case keymap.SortColumn:
		// unchanged body
	}
	cmd, _ := t.handle(a)
	return cmd
}
```

`Update`：

```go
func (t *dataTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case pageLoadedMsg:
		t.pageLoaded(m)
		return nil
	case editsAppliedMsg:
		return t.applied(m)
	}
	cmd, _ := t.update(msg)
	return cmd
}
```

`pageLoaded` 和 `applyQuery` 里 `t.failedWhere` 的用法不变（字段被提升）。

- [ ] **Step 4: 运行全部测试**

Run: `go test -race ./internal/ui/... && make lint`
Expected: PASS，测试数量和 Step 1 一致。有测试引用了 `d.gen`（submit 测试可能构造 `submitConfirmedMsg{gen: d.gen}`）时，只把字段名换成 `d.previewGen`，不改断言。

- [ ] **Step 5: 提交**

```bash
git add internal/ui
git commit -m "refactor(ui): 从 dataTab 抽出 editableResult，供控制台结果复用"
```

---

### Task 6: 控制台结果的编辑与提交

**Files:**
- Create: `internal/ui/console_edit.go`
- Modify: `internal/ui/console.go`（结构体、`newConsoleTab`、`Handle`、`Update`、`run`、`loadMore`、`execute`、`done`、`staleCatalog`、`statusLine`、`retryAfterPassword`、`runAttempt`、`runDoneMsg`）
- Test: `internal/ui/console_edit_test.go`

**Interfaces:**
- Consumes: Task 1 `sqlx.ResultSource`；Task 3 `edit.ApplyInTx`、`edit.Target.Columns`；Task 5 `editableResult`、`resultHost`；已有的 `readTableMeta`、`tableMeta`、`withConn`、`ensureSession`、`consoleTab.qualified`。
- Produces（Task 7 依赖）：
  ```go
  type resultFilter struct{ base, where string } // the statement a result came from, and its filter
  // consoleTab gains:
  //   res         *editableResult
  //   shown       resultFilter  // of the result on screen
  //   runFilter   *resultFilter // set before execute: the run re-reads the shown result
  //   runNote     string        // set before execute: status shown when the run succeeds
  //   metas       map[db.TableRef]*tableMeta
  // runAttempt and runDoneMsg gain: filter *resultFilter; note string
  func (t *consoleTab) resetResult(rs *db.ResultSet) tea.Cmd
  ```

- [ ] **Step 1: 写失败的测试**

`internal/ui/console_edit_test.go`：

```go
package ui

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/edit"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

var _ stager = (*consoleTab)(nil)

const updateUser1 = "UPDATE `app`.`users` SET `name` = 'x' WHERE `id` = '1'"

// stageName runs a single-table SELECT and stages name = 'x' on row 1.
func stageName(t *testing.T, c *consoleTab) {
	t.Helper()
	runSQL(t, c, "select * from users")
	if c.res.readOnly != "" {
		t.Fatalf("editable: %q", c.res.readOnly)
	}
	c.onResults = true
	c.Update(cellEditedMsg{tabID: c.id, table: c.res.table, row: 0, col: 1, value: db.Value{Text: "x"}})
	if c.changes() != 1 {
		t.Fatalf("changes = %d", c.changes())
	}
}

// confirmSubmit previews the staged changes and confirms them.
func confirmSubmit(t *testing.T, c *consoleTab) {
	t.Helper()
	runTab(t, c, c.submit())
	if c.res.pendingSQL == nil {
		t.Fatalf("no preview: %q", c.status)
	}
	gen := c.res.previewGen
	runTab(t, c, func() tea.Msg { return submitConfirmedMsg{tabID: c.id, gen: gen} })
}

func TestConsoleEditsSingleTableResult(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	stageName(t, c)
	if !strings.Contains(consoleView(c), "● 1 change") {
		t.Fatalf("view:\n%s", consoleView(c))
	}
	confirmSubmit(t, c)
	execs := b.conn.execList()
	want := []string{"START TRANSACTION", updateUser1, "COMMIT"}
	if got := execs[len(execs)-3:]; !slices.Equal(got, want) {
		t.Fatalf("execs = %v", execs)
	}
	if c.changes() != 0 || b.conn.lastQuery() != "select * from users" {
		t.Fatalf("changes %d, last query %q: the result is read again", c.changes(), b.conn.lastQuery())
	}
	if !strings.Contains(c.status, "submitted 1 change") {
		t.Fatalf("status = %q", c.status)
	}
}

func TestConsoleEditInOpenTransactionUsesSavepoint(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	c.toggleTx()
	stageName(t, c) // manual commit: the SELECT opened the transaction
	confirmSubmit(t, c)
	execs := b.conn.execList()
	want := []string{"SAVEPOINT datavim_edit", updateUser1, "RELEASE SAVEPOINT datavim_edit"}
	if got := execs[len(execs)-3:]; !slices.Equal(got, want) {
		t.Fatalf("execs = %v", execs)
	}
	if slices.Contains(execs, "COMMIT") || c.TxStatus() != "TX 1" {
		t.Fatalf("the change stays in the transaction: %v, %q", execs, c.TxStatus())
	}
}

func TestConsoleEditFailureInTransactionKeepsChanges(t *testing.T) {
	b := newFakeBackend()
	b.conn.setAffected(updateUser1, 0)
	c := newConsole(t, b, "local")
	c.toggleTx()
	stageName(t, c)
	confirmSubmit(t, c)
	if execs := b.conn.execList(); execs[len(execs)-1] != "ROLLBACK TO SAVEPOINT datavim_edit" {
		t.Fatalf("execs = %v", execs)
	}
	if c.changes() != 1 || !strings.Contains(c.status, "changes kept") || c.TxStatus() != "TX 0" {
		t.Fatalf("changes %d, status %q, tx %q", c.changes(), c.status, c.TxStatus())
	}
}

func TestConsoleReadOnlyResults(t *testing.T) {
	b := newFakeBackend()
	b.conn.setData("people", []db.ColumnInfo{{Name: "name"}}, []db.Value{{Text: "a"}})
	for _, tc := range []struct{ sql, want string }{
		{"select * from users u join orders o on o.uid = u.id", "read-only result: JOIN"},
		{"select count(*) from users", "read-only result: column 1 is an expression"},
		{"select name FROM `app`.`people`", "id (primary key) not in the result: rows can only be added"},
	} {
		c := newConsole(t, b, "local")
		runSQL(t, c, tc.sql)
		c.onResults = true
		c.Handle(keymap.EditCell)
		if !strings.Contains(c.status, tc.want) {
			t.Errorf("%s: status %q, want %q", tc.sql, c.status, tc.want)
		}
	}
	c := newConsole(t, b, "local")
	runSQL(t, c, "select name FROM `app`.`people`")
	c.onResults = true
	c.Handle(keymap.InsertRow)
	if c.res.table.Len() != 2 || c.res.table.Row(1).State != edit.Inserted {
		t.Fatal("rows can still be added without the key")
	}
}

func TestConsoleUnsavedChangesBlockRuns(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	stageName(t, c)
	c.onResults = false // RunStatement is an editor action
	runSQL(t, c, "select 2")
	if b.conn.lastQuery() == "select 2" || !strings.Contains(c.status, "1 unsaved change") {
		t.Fatalf("last %q, status %q", b.conn.lastQuery(), c.status)
	}
	c.onResults = true
	c.Handle(keymap.LoadMore)
	if !strings.Contains(c.status, "1 unsaved change") {
		t.Fatalf("load more: %q", c.status)
	}
}

func TestConsoleFailedTransactionBlocksEdits(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	runSQL(t, c, "select * from users")
	c.inTx, c.txAborted, c.onResults = true, true, true
	c.Handle(keymap.EditCell)
	if !strings.Contains(c.status, ":rollback") {
		t.Fatalf("status = %q", c.status)
	}
}

func TestResolveMapping(t *testing.T) {
	cols := []db.Column{{Name: "id"}, {Name: "Name"}}
	got, why := resolveMapping([]string{"ID", "name"}, cols)
	if why != "" || !slices.Equal(got, []string{"id", "Name"}) {
		t.Fatalf("got %v %q", got, why)
	}
	if _, why := resolveMapping([]string{"nope"}, cols); !strings.Contains(why, "nope") {
		t.Fatalf("why = %q", why)
	}
}

func TestKeyBlocked(t *testing.T) {
	cols := []db.Column{{Name: "id"}, {Name: "name"}}
	for _, tc := range []struct {
		key     edit.Key
		mapping []string
		want    string
	}{
		{edit.Key{Kind: edit.PrimaryKey, Columns: []string{"id"}}, []string{"id", "name"}, ""},
		{edit.Key{Kind: edit.PrimaryKey, Columns: []string{"id"}}, []string{"name"}, "id (primary key) not in the result: rows can only be added"},
		{edit.Key{Kind: edit.UniqueKey, Columns: []string{"id"}}, []string{"name"}, "id (unique key) not in the result: rows can only be added"},
		{edit.Key{Kind: edit.AllColumns}, []string{"id", "name"}, ""},
		{edit.Key{Kind: edit.AllColumns}, []string{"id"}, "users has no key and name is not in the result: rows can only be added"},
		{edit.Key{Kind: edit.RowID}, []string{"id", "name"}, "users has no primary or unique key: rows can only be added"},
	} {
		if got := keyBlocked("users", tc.key, cols, tc.mapping); got != tc.want {
			t.Errorf("%+v %v: %q, want %q", tc.key, tc.mapping, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/ui -run 'TestConsoleEdit|TestConsoleReadOnlyResults|TestConsoleUnsaved|TestConsoleFailedTransaction|TestResolveMapping|TestKeyBlocked'`
Expected: 编译失败，`c.res undefined`、`resolveMapping undefined` 等

- [ ] **Step 3: 新建 `internal/ui/console_edit.go`**

```go
package ui

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/edit"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/session"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// resultFilter is the statement a console result came from and the filter
// narrowing it ("" for none).
type resultFilter struct{ base, where string }

type (
	// consoleMetaMsg is the key and columns of the table behind a result.
	consoleMetaMsg struct {
		tabID int
		ref   db.TableRef
		meta  *tableMeta
		err   error
	}
	// consoleEditsMsg is the outcome of submitting a result's changes.
	consoleEditsMsg struct {
		tabID       int
		gen         int
		session     db.Session
		readOnly    bool
		reopened    bool
		reconnected bool
		began       bool // a transaction was started for manual commit
		n           int
		sql         string
		took        time.Duration
		err         error
	}
)

func (m consoleMetaMsg) TabID() int  { return m.tabID }
func (m consoleEditsMsg) TabID() int { return m.tabID }

// stager: a console stages changes to its result.
func (t *consoleTab) changes() int { return t.res.changes() }
func (t *consoleTab) unsavedHint(submit, discard string) string {
	return t.res.unsavedHint(submit, discard)
}
func (t *consoleTab) submit() tea.Cmd { return t.res.submit() }

// ---- resultHost ----

func (t *consoleTab) busy() string {
	switch {
	case t.running:
		return "a query is running · ctrl+c to cancel"
	case t.txAborted:
		return "the transaction failed · :rollback first"
	}
	return ""
}

func (t *consoleTab) filter() string      { return t.shown.where }
func (t *consoleTab) filterTitle() string { return "Filter result" }

// Task 7 replaces these two.
func (t *consoleTab) checkFilter(string) error  { return sqlx.ErrNotSelect }
func (t *consoleTab) refilter(string) tea.Cmd {
	t.setStatus(sqlx.ErrNotSelect.Error(), true)
	return nil
}

// resetResult makes rs the result to edit and finds the table behind it.
func (t *consoleTab) resetResult(rs *db.ResultSet) tea.Cmd {
	r := t.res
	r.table = edit.New(rs.Columns, rs.Rows, nil)
	r.ref, r.cols, r.key, r.mapping, r.addOnly = db.TableRef{}, nil, edit.Key{}, nil, ""
	names := make([]string, len(rs.Columns))
	for i, c := range rs.Columns {
		names[i] = c.Name
	}
	src, why := sqlx.ResultSource(t.dialect, t.shown.base, names)
	if why != "" {
		r.readOnly = "read-only result: " + why + " · edit single-table SELECTs"
		return nil
	}
	r.ref = db.TableRef{Namespace: t.qualified(src.Qualifier), Name: src.Table}
	r.mapping = src.Columns
	r.readOnly = "reading the keys of " + src.Table + "…"
	if tm, ok := t.metas[r.ref]; ok {
		t.applyMeta(tm)
		return nil
	}
	return t.loadMeta(r.ref)
}

func (t *consoleTab) loadMeta(ref db.TableRef) tea.Cmd {
	id, b, conn, d := t.id, t.backend, t.conn, t.dialect
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := consoleMetaMsg{tabID: id, ref: ref}
		msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
			var err error
			msg.meta, err = readTableMeta(ctx, c.Meta(), d, ref)
			return err
		})
		return msg
	}
}

func (t *consoleTab) metaLoaded(m consoleMetaMsg) {
	if m.err == nil {
		if t.metas == nil {
			t.metas = map[db.TableRef]*tableMeta{}
		}
		t.metas[m.ref] = m.meta
	}
	if m.ref != t.res.ref || t.res.cols != nil {
		return // another result is shown now
	}
	if m.err != nil {
		t.res.readOnly = "read-only result: cannot read the keys of " + m.ref.Name + ": " + errorText(m.err)
		return
	}
	t.applyMeta(m.meta)
}

func (t *consoleTab) applyMeta(tm *tableMeta) {
	r := t.res
	mapping, why := resolveMapping(r.mapping, tm.cols)
	if why != "" {
		r.readOnly = why
		return
	}
	r.readOnly, r.cols, r.key, r.mapping = "", tm.cols, tm.key, mapping
	r.addOnly = keyBlocked(r.ref.Name, tm.key, tm.cols, mapping)
}

// resolveMapping spells each result's table column as the catalog does.
func resolveMapping(names []string, cols []db.Column) ([]string, string) {
	out := make([]string, len(names))
	for i, n := range names {
		j := slices.IndexFunc(cols, func(c db.Column) bool { return strings.EqualFold(c.Name, n) })
		if j < 0 {
			return nil, "read-only result: " + n + " is not a column of the table"
		}
		out[i] = cols[j].Name
	}
	return out, ""
}

// keyBlocked says why rows read through a result whose table columns are
// mapping cannot be changed ("" when they can): every column that locates
// a row must be in the result, and a console result has no ctid.
func keyBlocked(table string, key edit.Key, cols []db.Column, mapping []string) string {
	switch key.Kind {
	case edit.PrimaryKey, edit.UniqueKey:
		kind := "primary key"
		if key.Kind == edit.UniqueKey {
			kind = "unique key"
		}
		for _, k := range key.Columns {
			if !slices.Contains(mapping, k) {
				return k + " (" + kind + ") not in the result: rows can only be added"
			}
		}
	case edit.AllColumns:
		for _, c := range cols {
			if !slices.Contains(mapping, c.Name) {
				return table + " has no key and " + c.Name + " is not in the result: rows can only be added"
			}
		}
	default:
		return table + " has no primary or unique key: rows can only be added"
	}
	return ""
}

// applyEdits runs confirmed changes on the console's session: in a
// transaction of their own under autocommit, else inside the console's
// transaction behind a savepoint (starting it under manual commit).
func (t *consoleTab) applyEdits(stmts []string) tea.Cmd {
	if t.running {
		t.setStatus("a query is already running · ctrl+c to cancel", true)
		return nil
	}
	cfg, _ := t.backend.Connection(t.conn)
	ro := cfg.IsReadOnly()
	ctx, cancel := context.WithCancel(context.Background())
	t.gen++
	t.running, t.cancel, t.started = true, cancel, time.Now()
	t.res.submitting = true
	t.setStatus("", false)
	gen, id, b, conn, ns, d := t.gen, t.id, t.backend, t.conn, t.ns, t.dialect
	s, sessRO, inTx, manual := t.session, t.sessionRO, t.inTx, t.manualTx
	t.session = nil // owned by the command until consoleEditsMsg hands it back
	run := func() tea.Msg {
		defer cancel()
		msg := consoleEditsMsg{tabID: id, gen: gen, readOnly: ro, n: len(stmts), sql: strings.Join(stmts, ";\n") + ";"}
		if s != nil && sessRO != ro {
			_ = s.Close()
			s = nil
			msg.reopened = true
		}
		sess, reconnected, err := ensureSession(ctx, b, conn, ns, s, inTx)
		msg.reconnected = reconnected
		if err != nil {
			msg.err = err
			return msg
		}
		start := time.Now()
		switch {
		case inTx:
			msg.err = edit.ApplyInTx(ctx, sess, stmts)
		case manual:
			if _, err := sess.Exec(ctx, sqlx.BeginSQL(d)); err != nil {
				msg.err = err
				break
			}
			msg.began = true
			msg.err = edit.ApplyInTx(ctx, sess, stmts)
		default:
			msg.err = edit.Apply(ctx, sess, d, stmts)
		}
		msg.took = time.Since(start)
		if db.IsConnLost(msg.err) {
			_ = sess.Close()
			sess = nil
		}
		msg.session = sess
		return msg
	}
	return tea.Batch(run, t.tick(gen))
}

// editsDone takes the session back and reads the result again after a
// submit, or keeps the changes staged when it failed.
func (t *consoleTab) editsDone(m consoleEditsMsg) tea.Cmd {
	t.running, t.cancel = false, nil
	t.session, t.sessionRO = m.session, m.readOnly
	t.res.submitting = false
	t.sessionNotes(m.reconnected, m.reopened, m.readOnly)
	if errors.Is(m.err, errTxLost) {
		t.clearTx()
	}
	if m.began {
		t.inTx, t.txWrites, t.txAborted = true, 0, false
	}
	if m.session == nil && t.inTx && m.err != nil && !errors.Is(m.err, errTxLost) {
		t.log("✗ " + errTxLost.Error())
		t.clearTx()
	}
	record := t.recordEdit(m)
	if m.err != nil {
		t.res.retrySubmit = errors.Is(m.err, session.ErrPasswordRequired)
		t.log("✗ " + oneLine(m.sql) + " — " + errorText(m.err))
		t.setStatus("✗ changes kept · "+errorText(m.err), true)
		return record
	}
	if t.inTx {
		t.txWrites += m.n
	}
	t.log("✓ submitted " + plural(m.n, "change"))
	t.res.table.Reset()
	t.res.showChanges()
	f := t.shown
	t.runText, t.runFilter, t.runNote = "", &f, "submitted "+plural(m.n, "change")
	return tea.Batch(record, t.execute([]sqlx.Statement{{Text: t.last.SQL}}, t.lastLimit, true))
}

func (t *consoleTab) recordEdit(m consoleEditsMsg) tea.Cmd {
	e := history.Entry{Time: time.Now(), Conn: t.conn, Database: t.ns.Database, SQL: m.sql, Duration: m.took}
	if m.err != nil {
		e.Err = errorText(m.err)
	}
	st := t.store
	return func() tea.Msg {
		if err := st.AddHistory(e); err != nil {
			slog.Warn("save history", "err", err)
		}
		return nil
	}
}
```

- [ ] **Step 4: 接到 `console.go`**

1. 结构体加字段（放在 `grid` 附近）：

   ```go
   	res       *editableResult            // the result on screen, with its staged changes
   	shown     resultFilter               // the statement and filter of the result on screen
   	runFilter *resultFilter              // set before execute: the run reads the shown result again
   	runNote   string                     // set before execute: the status once the run succeeds
   	metas     map[db.TableRef]*tableMeta // keys of tables behind results; dropped when DDL runs
   ```

2. `newConsoleTab` 末尾：

   ```go
   	t.res = &editableResult{
   		id: id, backend: b, conn: conn, dialect: d, keys: t.keys, keyCtx: keymap.Results,
   		grid: t.grid, host: t, readOnly: "run a query first",
   	}
   	t.grid.SetMarks(t.res.mark)
   ```
   `app.go` 在创建控制台后会替换 keymap：`openConsole`（`t.keys = m.keys`）和 `openScriptConsole`（`c.keys, c.scratchKey = m.keys, key`）。两处都紧接着加上 `t.res.keys = m.keys` / `c.res.keys = m.keys`，否则用户在 `keymap.toml` 里改的键不会出现在提示里。

3. 把 `done` 里记录重连 / 重开会话的两段抽成方法，`done` 和 `editsDone` 共用：

   ```go
   func (t *consoleTab) sessionNotes(reconnected, reopened, readOnly bool) {
   	if reconnected {
   		t.log("reconnected to " + t.conn)
   	}
   	if reopened {
   		mode := "read-write"
   		if readOnly {
   			mode = "read-only"
   		}
   		t.log("session reopened " + mode + " (session state such as temporary tables was reset)")
   	}
   }
   ```

4. `runAttempt` 和 `runDoneMsg` 各加 `filter *resultFilter` 和 `note string`。`execute` 开头：

   ```go
   	filter, note := t.runFilter, t.runNote
   	t.runFilter, t.runNote = nil, ""
   	t.attempt = &runAttempt{stmts: stmts, limit: limit, more: more, text: t.runText, filter: filter, note: note}
   ```
   构造 `runDoneMsg` 时带上 `filter: filter, note: note`。`retryAfterPassword` 改成：

   ```go
   func (t *consoleTab) retryAfterPassword() tea.Cmd {
   	if t.res.retrySubmit {
   		t.res.retrySubmit = false
   		return t.res.submit()
   	}
   	a := t.attempt
   	if a == nil || t.running {
   		return nil
   	}
   	t.runText, t.runFilter, t.runNote = a.text, a.filter, a.note
   	return t.execute(a.stmts, a.limit, a.more)
   }
   ```

5. `done` 里显示结果的那段改成：

   ```go
   	var meta tea.Cmd
   	if shown != nil {
   		t.shown = resultFilter{base: shown.SQL}
   		if m.filter != nil {
   			t.shown = *m.filter
   		}
   		t.res.failedWhere = ""
   		meta = t.resetResult(shown.Rows)
   		if m.more {
   			t.grid.ReplaceResult(t.res.table.Result())
   		} else {
   			t.grid.SetResult(t.res.table.Result())
   		}
   		t.last, t.lastLimit = shown, m.limit
   	} else if m.filter != nil && last.Err != nil {
   		t.res.failedWhere = m.filter.where
   	}
   ```
   `switch` 的 `default` 分支加上 `if m.note != "" { t.setStatus(m.note, false) }`，最后 `return tea.Batch(t.record(recorded), t.staleCatalog(m.results), meta)`。

6. `staleCatalog` 发现 DDL 时先 `t.metas = nil`（表结构可能变了），再返回原来的消息。

7. `Update` 开头：

   ```go
   	if cmd, ok := t.res.update(msg); ok {
   		return cmd
   	}
   	switch m := msg.(type) {
   	case consoleMetaMsg:
   		t.metaLoaded(m)
   		return nil
   	case consoleEditsMsg:
   		if m.gen == t.gen {
   			return t.editsDone(m)
   		}
   		return nil
   	// … existing cases
   ```
   `runTickMsg` 原来的判断 `t.running && m.gen == t.gen` 对提交同样适用，不用改。

8. `Handle` 的结果区分支：

   ```go
   	if t.onResults {
   		if cmd, ok := gridAction(t.grid, a); ok {
   			return cmd
   		}
   		if cmd, ok := t.res.handle(a); ok {
   			return cmd
   		}
   		if a == keymap.LoadMore {
   			return t.loadMore()
   		}
   		return nil
   	}
   ```

9. `run` 和 `loadMore` 开头都加：

   ```go
   	if t.res.changes() > 0 {
   		t.setStatus(t.res.unsavedNote(), true)
   		return nil
   	}
   ```
   `loadMore` 的 `default` 分支在 `execute` 之前加 `f := t.shown; t.runFilter = &f`，保留当前过滤条件。

10. `statusLine`：在 `t.status != ""` 两个分支之后、`hint :=` 之前加：

    ```go
    	if n := t.res.changes(); n > 0 {
    		return theme.Accent.Render("● "+plural(n, "change")) +
    			theme.Muted.Render(textutil.Fit(" · "+t.res.unsavedHint("submit", "discard"), t.width))
    	}
    ```
    结果区的提示加上编辑和过滤键：

    ```go
    		hint = joinHints("hjkl move · yy copy row · K show cell · / search",
    			keyHint(t.keys, keymap.Results, keymap.EditCell, "edit"),
    			keyHint(t.keys, keymap.Results, keymap.FilterPrompt, "filter"),
    			keyHint(t.keys, keymap.Results, keymap.ToggleMessages, "messages"),
    			keyHint(t.keys, keymap.Results, keymap.FocusEditor, "editor"))
    ```

11. `app.go` 的 `runCommand` 里 `:w` 的错误提示改成 `"nothing to write: the active tab has no staged changes to submit"`，因为控制台现在也是 `stager`。

- [ ] **Step 5: 运行测试，确认通过**

Run: `go test -race ./internal/ui/... && make lint`
Expected: PASS。原有控制台测试里检查 `execList` 或状态行的断言如果因为多了 meta 读取而失败，先确认 meta 读取走的是 `Meta()`（`fakeMeta` 不记录查询），不要改断言来迁就。

- [ ] **Step 6: 提交**

```bash
git add internal/ui
git commit -m "feat(ui): 控制台单表查询结果可直接编辑，提交跟随控制台事务"
```

---

### Task 7: 控制台结果的过滤

**Files:**
- Modify: `internal/ui/console_edit.go`（替换 Task 6 里占位的 `checkFilter` / `refilter`）
- Modify: `internal/ui/console.go`（`resultsTitle`）
- Test: `internal/ui/console_filter_test.go`

**Interfaces:**
- Consumes: Task 2 `sqlx.Filtered`、`sqlx.ErrNotSelect`；Task 6 的 `shown`、`runFilter`、`runNote`；已有的 `checkSelect`、`normFilter`。

- [ ] **Step 1: 写失败的测试**

```go
package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/ui/keymap"
)

// filterFirstID runs sql and filters its result by the first row's id.
func filterFirstID(t *testing.T, c *consoleTab, sql string) {
	t.Helper()
	c.onResults = false
	runSQL(t, c, sql)
	c.onResults = true
	c.grid.SetCursor(0, 0)
	runTab(t, c, c.Handle(keymap.FilterEqual))
}

func TestConsoleFilterMergesIntoWhere(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	filterFirstID(t, c, "select * from users")
	want := "select * from users\nWHERE (`id` = '1'\n)"
	if got := b.conn.lastQuery(); got != want {
		t.Fatalf("ran %q, want %q", got, want)
	}
	if c.shown.where != "`id` = '1'" || !strings.Contains(consoleView(c), "where `id` = '1'") {
		t.Fatalf("where %q:\n%s", c.shown.where, consoleView(c))
	}
	runTab(t, c, c.Handle(keymap.FilterClear))
	if got := b.conn.lastQuery(); got != "select * from users" || c.shown.where != "" {
		t.Fatalf("fx: ran %q, where %q", got, c.shown.where)
	}
}

func TestConsoleFilterWrapsOtherQueries(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	filterFirstID(t, c, "select id, name from users u join t on t.id = u.id order by name")
	want := "SELECT * FROM (\nselect id, name from users u join t on t.id = u.id\n) q WHERE (`id` = '1'\n)\norder by name"
	if got := b.conn.lastQuery(); got != want {
		t.Fatalf("ran %q", got)
	}

	c = newConsole(t, b, "local")
	filterFirstID(t, c, "select * from a join b order by a.x")
	if !strings.Contains(c.status, "order may not be kept") {
		t.Fatalf("status = %q", c.status)
	}
}

func TestConsoleFilterKeptByLoadMoreDroppedByNewRun(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	filterFirstID(t, c, "select * from users")
	filtered := b.conn.lastQuery()
	runTab(t, c, c.Handle(keymap.LoadMore)) // 3 rows, max_rows 2: truncated
	if b.conn.lastQuery() != filtered || c.shown.where == "" {
		t.Fatalf("load more ran %q, where %q", b.conn.lastQuery(), c.shown.where)
	}
	c.onResults = false // RunStatement is an editor action
	runSQL(t, c, "select * from users")
	if c.shown.where != "" || c.shown.base != "select * from users" {
		t.Fatalf("shown = %+v", c.shown)
	}
}

func TestConsoleFilterFailureKeepsRows(t *testing.T) {
	b := newFakeBackend()
	b.conn.setFail("select * from users\nWHERE (`id` = '1'\n)", errors.New("Duplicate column name 'id'"))
	c := newConsole(t, b, "local")
	filterFirstID(t, c, "select * from users")
	if c.res.failedWhere != "`id` = '1'" || c.shown.where != "" {
		t.Fatalf("failed %q, where %q", c.res.failedWhere, c.shown.where)
	}
	if !strings.Contains(consoleView(c), "user1") || !strings.Contains(c.status, "Duplicate column") {
		t.Fatalf("status %q:\n%s", c.status, consoleView(c))
	}
}

func TestConsoleUnsavedChangesBlockFilter(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	stageName(t, c)
	before := b.conn.lastQuery()
	runTab(t, c, c.Handle(keymap.FilterEqual))
	if b.conn.lastQuery() != before || !strings.Contains(c.status, "1 unsaved change") {
		t.Fatalf("ran %q, status %q", b.conn.lastQuery(), c.status)
	}
}

func TestConsoleFilterNeedsASelect(t *testing.T) {
	b := newFakeBackend()
	c := newConsole(t, b, "local")
	runSQL(t, c, "show tables")
	c.onResults = true
	runTab(t, c, c.Handle(keymap.FilterEqual))
	if !strings.Contains(c.status, "filter works on SELECT results") {
		t.Fatalf("status = %q", c.status)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `go test -race ./internal/ui -run 'TestConsoleFilter|TestConsoleUnsavedChangesBlockFilter'`
Expected: FAIL（`refilter` 还是占位）

- [ ] **Step 3: 实现**

替换 `console_edit.go` 里的两个占位：

```go
// filtered is the shown result's statement narrowed by where.
func (t *consoleTab) filtered(where string) (string, bool, error) {
	if t.last == nil || t.last.Rows == nil || len(t.last.Rows.Columns) == 0 {
		return "", false, sqlx.ErrNotSelect
	}
	names := make([]string, len(t.last.Rows.Columns))
	for i, c := range t.last.Rows.Columns {
		names[i] = c.Name
	}
	sql, ordered, err := sqlx.Filtered(t.dialect, t.shown.base, where, t.res.mapping != nil, names)
	if err != nil {
		return "", false, err
	}
	if err := checkSelect(t.dialect, sql); err != nil {
		return "", false, err
	}
	return sql, ordered, nil
}

func (t *consoleTab) checkFilter(s string) error {
	_, _, err := t.filtered(normFilter(s))
	return err
}

// refilter reads the shown result again narrowed by where.
func (t *consoleTab) refilter(where string) tea.Cmd {
	if t.running {
		t.setStatus("a query is already running · ctrl+c to cancel", true)
		return nil
	}
	sql, ordered, err := t.filtered(where)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	t.runText = ""
	t.runFilter = &resultFilter{base: t.shown.base, where: where}
	if !ordered {
		t.runNote = "order may not be kept: the ORDER BY names more than result columns"
	}
	return t.execute([]sqlx.Statement{{Text: sql}}, t.maxRows, false)
}
```

`console.go` 的 `resultsTitle` 在行数后面加上过滤条件：

```go
	if rs := t.grid.Result(); rs != nil {
		label += fmt.Sprintf(" · %d rows", len(rs.Rows))
		if t.shown.where != "" {
			label += " · where " + oneLine(t.shown.where)
		}
		if t.last != nil && t.last.Truncated {
			label += " · truncated · ] more"
		}
	}
```

`done` 在 `last.Err != nil` 时已经把错误放进状态栏；`failedWhere` 由 Task 6 的 `done` 设置，`F` 会把它带回来（`filterPrompt` 已经读 `r.failedWhere`）。

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/ui/... && make lint`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/ui
git commit -m "feat(ui): 控制台查询结果支持 f / F 过滤，在服务端重新执行"
```

---

### Task 8: 文档

**Files:**
- Modify: `docs/superpowers/specs/2026-10-13-datavim-design.md`（§5.7、§5.8）
- Modify: `docs/superpowers/specs/2026-10-16-console-result-filter-edit-design.md`（状态，以及 meta 加载时机）
- Modify: `CHANGELOG.md`
- Modify: `README.md`、`README.zh-CN.md`

- [ ] **Step 1: 主设计**

§5.7 最后一条改成：

```markdown
- 表数据 tab：`[` `]` 翻页，`f` 按当前单元格的值过滤，`F` 输入 WHERE 条件，`s` 按当前列切换排序（升序 → 降序 → 无）。控制台的 SELECT 结果同样可以 `f` / `F` 过滤，见 `2026-10-16-console-result-filter-edit-design.md`。
```

§5.8 第一条改成：

```markdown
- 表数据 tab 中可用；控制台里能对应回单张表的 SELECT 结果也可以编辑，条件和事务规则见 `2026-10-16-console-result-filter-edit-design.md` 第 3、6 节。
```

- [ ] **Step 2: 本设计文档**

状态改成 `已实现`。5.2 第 2 条改成实际做法："结果显示时，如果能对应回单表，就异步加载该表的列和索引（按表缓存在控制台里，执行 DDL 后清空）；加载完成前按编辑键提示 `reading the keys of <table>…`。"

- [ ] **Step 3: CHANGELOG**

在 `## [Unreleased]` 的 `### Added` 下面加：

```markdown
- Console results can be filtered and edited like table data. `f` and `F` narrow any SELECT
  result by running it again on the server: a single-table query gets the condition in its
  WHERE, any other query is wrapped in a subquery. A single-table SELECT of plain columns
  that includes the table's key can be edited with `i`, `o`, `dd`, `u`, `U` and submitted
  with `:w`. Changes join the console's open transaction behind a savepoint, so a failed
  submit keeps the transaction and `:rollback` undoes them.
```

- [ ] **Step 4: README**

在两个 README 介绍数据编辑的段落里各加一句（找到 "edit" / "编辑" 小节）：

- `README.md`："The same filter and edit keys work on console results of a single-table SELECT."
- `README.zh-CN.md`："控制台里单表 SELECT 的结果也能用同样的按键过滤和编辑。"

- [ ] **Step 5: 最终检查**

Run: `make test && make lint && make test-integration`
Expected: 全部 PASS

手动验证（`make build && ./bin/datavim --config <dir> --debug`）：在 MySQL 和 PostgreSQL 各跑一次 `select * from <表> where …`，`ff` 过滤、`i` 改一个单元格、`:w` 提交；再用 `<Space>t` 打开手动事务重复一遍，然后 `:rollback` 确认修改被撤销。

- [ ] **Step 6: 提交**

```bash
git add docs CHANGELOG.md README.md README.zh-CN.md
git commit -m "docs: 控制台结果的过滤与编辑"
```
