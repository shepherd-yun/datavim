# 结构对比第 2 阶段（v1.3）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在树上用 `m` 标记一张表、一个 MySQL 库或一个 PG schema 作为对比源，在另一个同类节点上按 `C`，打开 Diff 标签页列出结构差异；勾选后用 `<Space>w` 把同步脚本写进目标连接的新 console。

**Architecture:** 驱动新增 `MetaReader.TableSchema`，读出完整的表结构并把类型、默认值归一化；新的纯函数包 `internal/schemadiff` 只算差异（`db.SchemaChange`），不写 SQL；驱动的 `DDLWriter` 新增 `CreateTable` / `AlterTable` 把差异写成方言 SQL，写不出的退回 `manual`。往返行为（读 → 算差异 → 写 ALTER → 执行 → 再读，差异为空）由 `dbtest` contract 在 MySQL 8 与 PG 16 上验证。UI 新增 `diffTab`（`Diff` 键位上下文）与树上的 `m` / `C`；脚本只进 console，执行与安全检查全部交给 console 现有的路径。

**Tech Stack:** Go、Bubble Tea v2（`charm.land/bubbletea/v2`）、Lip Gloss v2、pgx v5、go-sql-driver/mysql、testcontainers（集成测试）；不新增依赖。

**Spec:** `docs/superpowers/specs/2026-10-15-table-ops-compare-design.md`（本 plan 实现 §3.2、§3.3 的 Compare 部分、§4、§5.1 中 `m` / `C` 两行、§5.3、§6 与结构对比有关的条目、§8 第 2 阶段。§5.3 里的 `D` 属于第 4 阶段的数据视图，本阶段不做）

## Global Constraints

- **UI 文案用英文。** 提示里的按键一律用 `keyHint` 或 `Keymap.KeysFor` 生成，不写死键名。
- UI 层不做 IO：所有数据库访问都在 `tea.Cmd` 里，带可取消的 `context.Context`。
- 方言差异只在 `internal/db/mysql`、`internal/db/postgres` 里；`internal/schemadiff` 不 import 任何驱动，也不按方言分支。
- 依赖方向：`ui` → `schemadiff` → `db`；`db` 不 import `schemadiff`（变更类型定义在 `db` 里）。
- 不跨引擎对比：两边驱动不同时直接拒绝。
- 外键差异只显示，不生成语句；视图、函数、触发器、分区不参与对比。
- 生成的脚本**只放进 console，不直接执行**：readonly、prod 确认、无 WHERE 拦截、事务检测都由 console 负责。
- `Destructive` 的变更默认不勾选；`manual` 的变更不能勾选，脚本里只留一行注释。
- 不新增依赖；`go.mod` 不变。
- 每个任务结束时：`gofumpt -l .` 无输出、`go vet ./...` 通过、`go test -race ./...` 全绿；改到 `internal/db` 的任务还要跑 `make test-integration`（需要 Docker；OrbStack 先 `orb start`）。
- Conventional Commits，带 scope，描述用中文（与仓库历史一致）。**commit message 不加 `Co-Authored-By` 行。**
- 每个用户可见的改动在 `CHANGELOG.md` 的 `[Unreleased]` 下记一行；README 的改动同时改 `README.zh-CN.md`。

## Review Focus

1. **同一引擎、不同版本或写法带来的假差异**：`int(11)` 与 `int`、`character varying(20)` 与 `varchar(20)`、`'x'::character varying` 与 `'x'`、PG 的 `serial` 与 identity 都不应算作差异。→ Task 1 的 `TestNormType` / `TestNormDefault`（两个驱动）与 contract 子测试 `TableSchema reads the table with normalized types and defaults`。
2. **同一个 PG schema 里对比 `users` 和 `users_bak`**：两张表的主键名不同（`users_pkey` / `users_bak_pkey`）不算差异；新增的索引不能沿用源表的索引名（同一 schema 里会重名）。→ Task 2 `TestDiffTablePairsIndexes`、`TestDiffTableRetargetsIndexNames`，Task 4 的往返测试（`cmp_src` / `cmp_dst` 在同一个 schema 里）。
3. **需要加引号的名字和带引号、反斜杠的注释**（`Odd Col`、`we"ird`、`it's \ here`）：两种方言写出的 ALTER / CREATE 在真实数据库上能执行。→ Task 3 的 golden 测试，Task 4 往返测试里的 `Odd Col` 列。
4. **加载失败或被取消**（一边要密码、表在对比前被删、`Ctrl-c`）：标签页说清楚是哪一边、什么错误，`<Space>r` 能重试，不显示过期的差异。→ Task 7 `TestDiffTabShowsWhichSideFailed`、`TestDiffTabLoadCanBeCancelled`、`TestDiffTabAsksForPassword`。
5. **什么都没勾、只剩 manual、连按两次 `<Space>w`**：没有可执行语句时不开 console 并说明原因；每按一次开一个新 console，不覆盖用户平时用的 console 与它的草稿。→ Task 9 `TestWriteScriptNothingPicked`、`TestManualChangesStayInTheScriptAsComments`、`TestWriteScriptOpensANewConsoleEachTime`、`TestScriptConsoleKeepsNoDraft`。

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/db/types.go` | `Column`、`Index` 新字段；`TableSchema` | 1 |
| `internal/db/driver.go` | `MetaReader.TableSchema`；`DDLWriter.CreateTable` / `AlterTable` | 1、3 |
| `internal/db/schemachange.go`（新） | `SchemaChange`、`ChangeKind`、`ColumnAttr`、`Risk` | 2 |
| `internal/db/mysql/schema.go`（新） | MySQL `TableSchema` | 1 |
| `internal/db/mysql/normalize.go`（新） | MySQL 类型、默认值归一化，字符串字面量 | 1 |
| `internal/db/mysql/ddlwriter.go` | MySQL `CreateTable` / `AlterTable` | 3 |
| `internal/db/postgres/schema.go`（新） | PG `TableSchema` | 1 |
| `internal/db/postgres/normalize.go`（新） | PG 类型、默认值归一化，字符串字面量 | 1 |
| `internal/db/postgres/ddlwriter.go` | PG `CreateTable` / `AlterTable` | 3 |
| `internal/db/dbtest/contract.go` | `TableSchema` 与往返测试 | 1、4 |
| `internal/schemadiff/diff.go`（新） | `DiffTable` | 2 |
| `internal/schemadiff/types.go`（新） | `LossyType` | 2 |
| `internal/schemadiff/namespace.go`（新） | `DiffNamespace`、`ObjectDiff` | 5 |
| `internal/schemadiff/describe.go`（新） | `Describe`、`DescribeObject` | 5 |
| `internal/schemadiff/script.go`（新） | `Writable`、`Script` | 5 |
| `internal/ui/keymap/*` | `Diff` 上下文与新动作 | 6 |
| `internal/ui/difftab.go`（新） | Diff 标签页 | 7、9 |
| `internal/ui/compare.go`（新） | `m` / `C`、选源 picker | 8 |
| `internal/ui/panes/tree/tree.go` | 对比标记 `◆` | 8 |
| `internal/ui/nodemenu.go` | 动作菜单加对比项 | 8 |
| `internal/ui/console.go` | 脚本 console（不存草稿） | 9 |
| `internal/ui/app.go` | `showTab`、消息分发、`Cancel` | 7、8、9 |
| `internal/ui/fakes_test.go` | fake 的 `TableSchema`、`CreateTable` / `AlterTable`、多连接 | 1、3、7 |

---

### Task 1: `TableSchema`——读取完整的表结构并归一化

**Files:**
- Modify: `internal/db/types.go`（`Column`、`Index` 新字段，新增 `TableSchema`）
- Modify: `internal/db/driver.go`（`MetaReader` 增加 `TableSchema`）
- Create: `internal/db/mysql/normalize.go`、`internal/db/mysql/schema.go`
- Create: `internal/db/postgres/normalize.go`、`internal/db/postgres/schema.go`
- Modify: `internal/db/mysql/mysql_test.go`、`internal/db/postgres/postgres_test.go`（单元测试）
- Modify: `internal/db/dbtest/contract.go`（新字段与一个子测试）
- Modify: `internal/db/mysql/integration_test.go`、`internal/db/postgres/integration_test.go`（`norm_types` fixture）
- Modify: `internal/ui/fakes_test.go`（`fakeMeta.TableSchema`）

**Interfaces:**
- Produces（Task 2–9 使用）：

  ```go
  // in package db
  type Column struct {
      Name, Type string
      Nullable   bool
      Default    *string
      Comment    string
      PrimaryKey bool
      AutoIncrement bool   // TableSchema only
      Collation     string // TableSchema only; set when not the default
      Generated     string // TableSchema only
      Position      int    // TableSchema only; 1-based, not compared
  }
  type Index struct {
      Name       string
      Columns    []string
      Unique     bool
      Primary    bool
      Method     string // TableSchema only; lower case
      Where      string // TableSchema only; PostgreSQL partial index
      Constraint bool   // TableSchema only; PostgreSQL PRIMARY KEY / UNIQUE constraint
  }
  type TableSchema struct {
      Ref         TableRef
      Columns     []Column
      Indexes     []Index
      ForeignKeys []ForeignKey
      Comment     string
      Options     map[string]string // MySQL: "engine", "collation"
  }
  // MetaReader gains:
  TableSchema(ctx context.Context, t TableRef) (TableSchema, error) // ErrNotFound when t is missing
  ```

  归一化后的约定：`Type` 小写（PG 用短名：`varchar`、`char`、`int`、`bool`、`timestamp`、`timestamptz`；MySQL 去掉整数显示宽度，保留 `tinyint(1)`）；`Default` 是能直接跟在 `DEFAULT` 后面的 SQL 文本（字符串带引号，PG 去掉字面量后的 `::type`）；自增列（MySQL `auto_increment`、PG identity 与 `serial`）`AutoIncrement` 为真、`Default` 为 nil；生成列 `Generated` 是表达式、`Default` 为 nil。

  归一化**只在 `TableSchema` 里做**：`Columns()` 等方法照旧返回服务端的原文，结构页、补全的显示不变。

- [ ] **Step 1: 写 MySQL 归一化的单元测试**

在 `internal/db/mysql/mysql_test.go` 末尾追加：

```go
func TestNormType(t *testing.T) {
	for in, want := range map[string]string{
		"int(11)":             "int",
		"INT":                 "int",
		"bigint(20) unsigned": "bigint unsigned",
		"tinyint(1)":          "tinyint(1)",
		"tinyint(4)":          "tinyint",
		"varchar(100)":        "varchar(100)",
		"decimal(10,2)":       "decimal(10,2)",
	} {
		if got := normType(in); got != want {
			t.Errorf("normType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormDefault(t *testing.T) {
	s := func(v string) *string { return &v }
	show := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	for _, c := range []struct {
		def        *string
		typ, extra string
		want       *string
	}{
		{nil, "int", "", nil},
		{s("0"), "int", "", s("0")},
		{s("1.5"), "decimal(10,2)", "", s("1.5")},
		{s("hi"), "varchar(10)", "", s("'hi'")},
		{s(`it's \`), "varchar(10)", "", s(`'it''s \\'`)},
		{s("1"), "varchar(10)", "", s("'1'")},
		{s("CURRENT_TIMESTAMP"), "timestamp", "default_generated", s("CURRENT_TIMESTAMP")},
		{s("CURRENT_TIMESTAMP(3)"), "datetime(3)", "default_generated on update current_timestamp(3)", s("CURRENT_TIMESTAMP(3)")},
		{s("uuid()"), "varchar(36)", "default_generated", s("(uuid())")},
		{s("b'1'"), "bit(1)", "", s("b'1'")},
	} {
		if got := normDefault(c.def, c.typ, c.extra); show(got) != show(c.want) {
			t.Errorf("normDefault(%s, %q, %q) = %s, want %s", show(c.def), c.typ, c.extra, show(got), show(c.want))
		}
	}
}
```

- [ ] **Step 2: 写 PG 归一化的单元测试**

在 `internal/db/postgres/postgres_test.go` 末尾追加：

```go
func TestNormType(t *testing.T) {
	for in, want := range map[string]string{
		"character varying(100)":         "varchar(100)",
		"character varying":              "varchar",
		"character varying(10)[]":        "varchar(10)[]",
		"character(1)":                   "char(1)",
		"integer":                        "int",
		"integer[]":                      "int[]",
		"boolean":                        "bool",
		"timestamp without time zone":    "timestamp",
		"timestamp(3) without time zone": "timestamp(3)",
		"timestamp with time zone":       "timestamptz",
		"time with time zone":            "timetz",
		"bigint":                         "bigint",
		"numeric(10,2)":                  "numeric(10,2)",
		"double precision":               "double precision",
		"text":                           "text",
	} {
		if got := normType(in); got != want {
			t.Errorf("normType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormDefault(t *testing.T) {
	s := func(v string) *string { return &v }
	show := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	for _, c := range []struct{ in, want *string }{
		{nil, nil},
		{s("0"), s("0")},
		{s("'x'::character varying"), s("'x'")},
		{s("'it''s'::text"), s("'it''s'")},
		{s("'{}'::integer[]"), s("'{}'")},
		{s("'2024-01-01'::date"), s("'2024-01-01'")},
		{s("1.5::numeric(10,2)"), s("1.5")},
		{s("NULL::character varying"), nil},
		{s("CURRENT_TIMESTAMP"), s("CURRENT_TIMESTAMP")},
		{s("nextval('users_id_seq'::regclass)"), s("nextval('users_id_seq'::regclass)")},
		{s("(now() + '1 day'::interval)"), s("(now() + '1 day'::interval)")},
	} {
		if got := normDefault(c.in); show(got) != show(c.want) {
			t.Errorf("normDefault(%s) = %s, want %s", show(c.in), show(got), show(c.want))
		}
	}
}
```

- [ ] **Step 3: 运行，确认编译失败**

Run: `go test ./internal/db/mysql ./internal/db/postgres -run 'TestNorm'`
Expected: FAIL，`undefined: normType`、`undefined: normDefault`

- [ ] **Step 4: MySQL 的归一化**

Create `internal/db/mysql/normalize.go`:

```go
package mysql

import (
	"regexp"
	"strings"
)

var (
	intDisplayWidth = regexp.MustCompile(`^(tinyint|smallint|mediumint|int|bigint)\(\d+\)`)
	numericColumn   = regexp.MustCompile(`^(tinyint|smallint|mediumint|int|bigint|decimal|numeric|float|double|real|year)\b`)
	plainNumber     = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][-+]?\d+)?$`)
	currentTime     = regexp.MustCompile(`(?i)^(current_timestamp|now|localtime|localtimestamp)(\(\d*\))?$`)
)

// normType lower-cases a column type and drops integer display widths,
// which MySQL 8 ignores, except tinyint(1), the conventional boolean.
func normType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if strings.HasPrefix(t, "tinyint(1)") {
		return t
	}
	return intDisplayWidth.ReplaceAllString(t, "$1")
}

// normDefault turns COLUMN_DEFAULT, which shows strings without quotes,
// into SQL text: expressions (extra, the lower-case EXTRA, says
// default_generated) as MySQL keeps them, in parentheses unless they are
// CURRENT_TIMESTAMP and the like; numbers and bit values bare; everything
// else quoted.
func normDefault(def *string, typ, extra string) *string {
	if def == nil {
		return nil
	}
	d := *def
	switch {
	case strings.Contains(extra, "default_generated"):
		if !currentTime.MatchString(d) {
			d = "(" + d + ")"
		}
	case numericColumn.MatchString(typ) && plainNumber.MatchString(d):
	case strings.HasPrefix(typ, "bit") && (strings.HasPrefix(d, "b'") || strings.HasPrefix(d, "0x")):
	default:
		d = quoteString(d)
	}
	return &d
}

// quoteString writes s as a string literal. The server reads backslash
// escapes in literals, so backslashes are doubled as well as quotes.
func quoteString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(s) + "'"
}
```

- [ ] **Step 5: PG 的归一化**

Create `internal/db/postgres/normalize.go`:

```go
package postgres

import (
	"regexp"
	"strings"
)

var (
	timeZoneType = regexp.MustCompile(`^(timestamp|time)(\(\d+\))? with(out)? time zone(\[\])?$`)
	castLiteral  = regexp.MustCompile(`^('(?:[^']|'')*'|-?\d+(?:\.\d+)?)::[a-z][a-z0-9_ ]*(?:\(\d+(?:,\s*\d+)?\))?(?:\[\])?$`)
	typeAliases  = []struct{ long, short string }{
		{"character varying", "varchar"},
		{"character", "char"},
		{"integer", "int"},
		{"boolean", "bool"},
	}
)

// normType writes format_type's long names the short way people type them:
// varchar, char, int, bool, timestamp, timestamptz.
func normType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if m := timeZoneType.FindStringSubmatch(t); m != nil {
		base := m[1]
		if m[3] == "" { // "with time zone"
			base += "tz"
		}
		return base + m[2] + m[4]
	}
	for _, a := range typeAliases {
		if rest, ok := strings.CutPrefix(t, a.long); ok && (rest == "" || rest[0] == '(' || rest[0] == '[') {
			return a.short + rest
		}
	}
	return t
}

// normDefault drops the cast PostgreSQL puts after literal defaults
// ('x'::character varying → 'x') and reads an explicit NULL default as none.
func normDefault(d *string) *string {
	if d == nil {
		return nil
	}
	s := *d
	if m := castLiteral.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	if l := strings.ToLower(s); l == "null" || strings.HasPrefix(l, "null::") {
		return nil
	}
	return &s
}

// quoteString writes s as a string literal (standard_conforming_strings is on).
func quoteString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
```

- [ ] **Step 6: 运行归一化的单元测试，确认通过**

Run: `go test ./internal/db/mysql ./internal/db/postgres -run 'TestNorm'`
Expected: PASS

- [ ] **Step 7: 扩展 `db` 的类型与接口**

把 `internal/db/types.go` 里的 `Column` 与 `Index` 换成：

```go
// Column describes one table column. The fields after PrimaryKey are only
// filled by MetaReader.TableSchema.
type Column struct {
	Name       string
	Type       string
	Nullable   bool
	Default    *string
	Comment    string
	PrimaryKey bool

	AutoIncrement bool   // MySQL auto_increment; PostgreSQL identity or serial
	Collation     string // only when it is not the default (the table's on MySQL, the type's on PostgreSQL)
	Generated     string // the expression of a generated column
	Position      int    // 1-based; shown, never compared
}

// Index describes one index; Primary implies Unique. The fields after
// Primary are only filled by MetaReader.TableSchema.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Primary bool

	Method     string // btree, hash, gin, fulltext, … in lower case
	Where      string // the predicate of a PostgreSQL partial index
	Constraint bool   // PostgreSQL: the index backs a PRIMARY KEY or UNIQUE constraint
}
```

在 `ForeignKey` 后面新增：

```go
// TableSchema is a table as schema comparison sees it. MetaReader.TableSchema
// normalizes it so that equal definitions compare equal: Type is lower case
// with the engine's short names, Default is SQL text that can follow
// DEFAULT, and auto-increment and generated columns have no Default.
type TableSchema struct {
	Ref         TableRef
	Columns     []Column
	Indexes     []Index
	ForeignKeys []ForeignKey
	Comment     string
	Options     map[string]string // MySQL: "engine" and "collation"; PostgreSQL: none
}
```

在 `internal/db/driver.go` 的 `MetaReader` 里，`DDL` 那一行后面加：

```go
	// TableSchema reads t for schema comparison (see TableSchema), or
	// returns ErrNotFound.
	TableSchema(ctx context.Context, t TableRef) (TableSchema, error)
```

改了接口以后，两个驱动和 `internal/ui` 的 fake 暂时编译不过，Step 8–10 补上。

- [ ] **Step 8: MySQL 的 `TableSchema`**

Create `internal/db/mysql/schema.go`:

```go
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// TableSchema reads t for schema comparison: what Columns, Indexes and
// ForeignKeys return, plus what only comparison needs, normalized.
func (m meta) TableSchema(ctx context.Context, t db.TableRef) (db.TableSchema, error) {
	s := db.TableSchema{Ref: t, Options: map[string]string{}}
	var engine, collation, comment sql.NullString
	err := m.c.db.QueryRowContext(ctx, `
		SELECT ENGINE, TABLE_COLLATION, TABLE_COMMENT FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, t.Database, t.Name).Scan(&engine, &collation, &comment)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("%s: %w", dialect{}.QualifiedName(t), db.ErrNotFound)
	}
	if err != nil {
		return s, err
	}
	s.Comment = comment.String
	if engine.Valid {
		s.Options["engine"] = engine.String
	}
	if collation.Valid {
		s.Options["collation"] = collation.String
	}
	if s.Columns, err = m.Columns(ctx, t); err != nil {
		return s, err
	}
	if err := m.columnDetails(ctx, &s); err != nil {
		return s, err
	}
	if s.Indexes, err = m.Indexes(ctx, t); err != nil {
		return s, err
	}
	if err := m.indexMethods(ctx, t, s.Indexes); err != nil {
		return s, err
	}
	s.ForeignKeys, err = m.ForeignKeys(ctx, t)
	return s, err
}

// columnDetails fills in what Columns leaves out and normalizes types and
// defaults.
func (m meta) columnDetails(ctx context.Context, s *db.TableSchema) error {
	rows, err := m.c.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, EXTRA, COALESCE(COLLATION_NAME, ''), COALESCE(GENERATION_EXPRESSION, '')
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, s.Ref.Database, s.Ref.Name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	type detail struct{ extra, collation, generated string }
	details := map[string]detail{}
	for rows.Next() {
		var name string
		var d detail
		if err := rows.Scan(&name, &d.extra, &d.collation, &d.generated); err != nil {
			return err
		}
		d.extra = strings.ToLower(d.extra)
		details[name] = d
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range s.Columns {
		c := &s.Columns[i]
		d := details[c.Name]
		c.Position = i + 1
		c.Type = normType(c.Type)
		c.AutoIncrement = strings.Contains(d.extra, "auto_increment")
		// "virtual generated" / "stored generated"; "default_generated"
		// marks an expression default instead.
		if strings.HasSuffix(d.extra, "generated") && !strings.Contains(d.extra, "default_generated") {
			c.Generated, c.Default = d.generated, nil
		}
		if d.collation != "" && d.collation != s.Options["collation"] {
			c.Collation = d.collation
		}
		c.Default = normDefault(c.Default, c.Type, d.extra)
	}
	return nil
}

// indexMethods fills in Method (btree, fulltext, spatial, hash).
func (m meta) indexMethods(ctx context.Context, t db.TableRef, idx []db.Index) error {
	rows, err := m.c.db.QueryContext(ctx, `
		SELECT DISTINCT INDEX_NAME, INDEX_TYPE FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`, t.Database, t.Name)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	methods := map[string]string{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return err
		}
		methods[name] = strings.ToLower(typ)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range idx {
		idx[i].Method = methods[idx[i].Name]
	}
	return nil
}
```

- [ ] **Step 9: PG 的 `TableSchema`**

Create `internal/db/postgres/schema.go`:

```go
package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// serialDefault is the default a serial column gets from its sequence.
var serialDefault = regexp.MustCompile(`^nextval\('.*'::regclass\)$`)

// TableSchema reads t for schema comparison: what Columns, Indexes and
// ForeignKeys return, plus what only comparison needs, normalized.
func (m meta) TableSchema(ctx context.Context, t db.TableRef) (db.TableSchema, error) {
	s := db.TableSchema{Ref: t}
	if err := m.checkDatabase(t.Database); err != nil {
		return s, err
	}
	name := regclass(t)
	err := m.c.pool.QueryRow(ctx, `
		SELECT COALESCE(obj_description(c.oid, 'pg_class'), '') FROM pg_class c
		WHERE c.oid = to_regclass($1)`, name).Scan(&s.Comment)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, fmt.Errorf("%s: %w", name, db.ErrNotFound)
	}
	if err != nil {
		return s, err
	}
	if s.Columns, err = m.Columns(ctx, t); err != nil {
		return s, err
	}
	if err := m.columnDetails(ctx, name, s.Columns); err != nil {
		return s, err
	}
	if s.Indexes, err = m.Indexes(ctx, t); err != nil {
		return s, err
	}
	if err := m.indexDetails(ctx, name, s.Indexes); err != nil {
		return s, err
	}
	s.ForeignKeys, err = m.ForeignKeys(ctx, t)
	return s, err
}

// columnDetails fills in what Columns leaves out and normalizes types and
// defaults. name is the table's regclass text.
func (m meta) columnDetails(ctx context.Context, name string, cols []db.Column) error {
	rows, err := m.c.pool.Query(ctx, `
		SELECT a.attname::text, a.attidentity::text, a.attgenerated::text,
		       CASE WHEN a.attcollation <> 0 AND a.attcollation <> ty.typcollation
		            THEN co.collname::text ELSE '' END
		FROM pg_attribute a
		JOIN pg_type ty ON ty.oid = a.atttypid
		LEFT JOIN pg_collation co ON co.oid = a.attcollation
		WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped`, name)
	if err != nil {
		return err
	}
	type detail struct{ identity, generated, collation string }
	details := map[string]detail{}
	var col string
	var d detail
	if _, err := pgx.ForEachRow(rows, []any{&col, &d.identity, &d.generated, &d.collation}, func() error {
		details[col] = d
		return nil
	}); err != nil {
		return err
	}
	for i := range cols {
		c := &cols[i]
		d := details[c.Name]
		c.Position = i + 1
		c.Type = normType(c.Type)
		c.Collation = d.collation
		switch {
		case d.generated == "s": // the generation expression is stored as the default
			if c.Default != nil {
				c.Generated = *c.Default
			}
			c.Default = nil
		case d.identity != "":
			c.AutoIncrement = true
		case c.Default != nil && serialDefault.MatchString(*c.Default):
			c.AutoIncrement, c.Default = true, nil
		}
		c.Default = normDefault(c.Default)
	}
	return nil
}

// indexDetails fills in Method, Where and Constraint.
func (m meta) indexDetails(ctx context.Context, name string, idx []db.Index) error {
	rows, err := m.c.pool.Query(ctx, `
		SELECT i.relname::text, am.amname::text, COALESCE(pg_get_expr(ix.indpred, ix.indrelid, true), ''),
		       EXISTS (SELECT 1 FROM pg_constraint con
		               WHERE con.conrelid = ix.indrelid AND con.conindid = ix.indexrelid
		                 AND con.contype IN ('p', 'u'))
		FROM pg_index ix
		JOIN pg_class i ON i.oid = ix.indexrelid
		JOIN pg_am am ON am.oid = i.relam
		WHERE ix.indrelid = to_regclass($1)`, name)
	if err != nil {
		return err
	}
	type detail struct {
		method, where string
		constraint    bool
	}
	details := map[string]detail{}
	var ixName string
	var d detail
	if _, err := pgx.ForEachRow(rows, []any{&ixName, &d.method, &d.where, &d.constraint}, func() error {
		details[ixName] = d
		return nil
	}); err != nil {
		return err
	}
	for i := range idx {
		d := details[idx[i].Name]
		idx[i].Method, idx[i].Where, idx[i].Constraint = d.method, d.where, d.constraint
	}
	return nil
}
```

- [ ] **Step 10: fake 实现 `TableSchema`**

在 `internal/ui/fakes_test.go` 的 `fakeConn` 结构体里，`tables` 字段后面加一行：

```go
	schemas    map[db.TableRef]db.TableSchema // returned by TableSchema
```

在 `setTables` 后面新增：

```go
func (c *fakeConn) setSchemas(ss ...db.TableSchema) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.schemas == nil {
		c.schemas = map[db.TableRef]db.TableSchema{}
	}
	for _, s := range ss {
		c.schemas[s.Ref] = s
	}
}
```

在 `fakeMeta.DDL` 后面新增：

```go
// TableSchema returns what setSchemas stored; it fails once ctx is done,
// like a real query would.
func (m fakeMeta) TableSchema(ctx context.Context, t db.TableRef) (db.TableSchema, error) {
	if err := ctx.Err(); err != nil {
		return db.TableSchema{}, err
	}
	m.c.mu.Lock()
	defer m.c.mu.Unlock()
	if s, ok := m.c.schemas[t]; ok {
		return s, nil
	}
	return db.TableSchema{}, fmt.Errorf("%s: %w", t.Name, db.ErrNotFound)
}
```

- [ ] **Step 11: 运行单元测试，确认通过**

Run: `go test -race ./internal/db/... ./internal/ui/...`
Expected: PASS（含 `TestNormType`、`TestNormDefault`）

- [ ] **Step 12: contract 子测试**

在 `internal/db/dbtest/contract.go` 的 `Fixture` 结构体末尾加：

```go
	// NormTypesSQL creates the table norm_types: columns id (auto-increment
	// primary key), n (DEFAULT 0), flag, name (DEFAULT 'x'), created_at
	// (DEFAULT CURRENT_TIMESTAMP), big, price, body, ci (its own collation),
	// and the table comment "normalized".
	NormTypesSQL []string
	// NormTypes is the type TableSchema must report for each column of norm_types.
	NormTypes map[string]string
	// AutoIncrement lists the auto-incrementing columns of norm_types.
	AutoIncrement []string
	// CICollation is the collation of norm_types.ci.
	CICollation string
```

在 `RunContract` 里，`t.Run("DDL", …)` 子测试之后插入：

```go
	t.Run("TableSchema reads the table with normalized types and defaults", func(t *testing.T) {
		s, err := conn.Meta().TableSchema(ctx, ref("users"))
		if err != nil {
			t.Fatal(err)
		}
		if s.Ref != ref("users") {
			t.Errorf("Ref = %+v", s.Ref)
		}
		var names []string
		for i, c := range s.Columns {
			names = append(names, c.Name)
			if c.Position != i+1 {
				t.Errorf("%s: Position = %d, want %d", c.Name, c.Position, i+1)
			}
		}
		if diff := cmp.Diff([]string{"id", "name", "email", "note"}, names); diff != "" {
			t.Fatalf("columns (-want +got):\n%s", diff)
		}
		if c := s.Columns[1]; c.Comment != "display name" || c.Nullable {
			t.Errorf("name = %+v", c)
		}
		if d := s.Columns[3].Default; d == nil || *d != "'hi'" {
			t.Errorf("note default = %v, want 'hi' quoted", d)
		}
		var email *db.Index
		for i := range s.Indexes {
			if s.Indexes[i].Name == "users_email_key" {
				email = &s.Indexes[i]
			}
		}
		if email == nil || !email.Unique || email.Primary || email.Method != "btree" {
			t.Errorf("users_email_key = %+v", email)
		}
		orders, err := conn.Meta().TableSchema(ctx, ref("orders"))
		if err != nil || len(orders.ForeignKeys) != 1 {
			t.Errorf("orders foreign keys = %+v, %v", orders.ForeignKeys, err)
		}
		if _, err := conn.Meta().TableSchema(ctx, ref("no_such_table_xyz")); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("missing table err = %v, want ErrNotFound", err)
		}

		for _, q := range f.NormTypesSQL {
			if _, err := conn.Exec(ctx, q); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		n, err := conn.Meta().TableSchema(ctx, ref("norm_types"))
		if err != nil {
			t.Fatal(err)
		}
		types := map[string]string{}
		cols := map[string]db.Column{}
		for _, c := range n.Columns {
			types[c.Name], cols[c.Name] = c.Type, c
		}
		if diff := cmp.Diff(f.NormTypes, types); diff != "" {
			t.Errorf("types (-want +got):\n%s", diff)
		}
		for name, want := range map[string]string{"n": "0", "name": "'x'", "created_at": "CURRENT_TIMESTAMP"} {
			if d := cols[name].Default; d == nil || *d != want {
				t.Errorf("%s default = %v, want %s", name, d, want)
			}
		}
		for _, name := range f.AutoIncrement {
			if c := cols[name]; !c.AutoIncrement || c.Default != nil {
				t.Errorf("%s: AutoIncrement = %v, Default = %v; want true, nil", name, c.AutoIncrement, c.Default)
			}
		}
		for name, c := range cols {
			want := ""
			if name == "ci" {
				want = f.CICollation
			}
			if c.Collation != want {
				t.Errorf("%s collation = %q, want %q", name, c.Collation, want)
			}
		}
		if n.Comment != "normalized" {
			t.Errorf("table comment = %q", n.Comment)
		}
	})
```

- [ ] **Step 13: 两个集成测试提供 fixture**

在 `internal/db/mysql/integration_test.go` 的 `dbtest.RunContract(t, conn, dbtest.Fixture{` 里，`OpenReadOnly` 字段之后加：

```go
		NormTypesSQL: []string{
			"CREATE TABLE norm_types (id INT AUTO_INCREMENT PRIMARY KEY, n INT(11) NOT NULL DEFAULT 0, " +
				"flag TINYINT(1), name VARCHAR(20) DEFAULT 'x', created_at TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP, " +
				"big BIGINT UNSIGNED, price DECIMAL(10,2), body TEXT, ci VARCHAR(10) COLLATE utf8mb4_bin) " +
				"COMMENT 'normalized'",
		},
		NormTypes: map[string]string{
			"id": "int", "n": "int", "flag": "tinyint(1)", "name": "varchar(20)", "created_at": "timestamp",
			"big": "bigint unsigned", "price": "decimal(10,2)", "body": "text", "ci": "varchar(10)",
		},
		AutoIncrement: []string{"id"},
		CICollation:   "utf8mb4_bin",
```

在 `internal/db/postgres/integration_test.go` 的 `dbtest.Fixture{` 里，`OpenReadOnly` 字段之后加：

```go
		NormTypesSQL: []string{
			`CREATE TABLE app.norm_types (id serial PRIMARY KEY, n integer NOT NULL DEFAULT 0, flag boolean,
			   name character varying(20) DEFAULT 'x', created_at timestamp with time zone DEFAULT CURRENT_TIMESTAMP,
			   big bigint, price numeric(10,2), body text, ci varchar(10) COLLATE "C",
			   ident int GENERATED BY DEFAULT AS IDENTITY)`,
			`COMMENT ON TABLE app.norm_types IS 'normalized'`,
		},
		NormTypes: map[string]string{
			"id": "int", "n": "int", "flag": "bool", "name": "varchar(20)", "created_at": "timestamptz",
			"big": "bigint", "price": "numeric(10,2)", "body": "text", "ci": "varchar(10)", "ident": "int",
		},
		AutoIncrement: []string{"id", "ident"},
		CICollation:   "C",
```

- [ ] **Step 14: 跑集成测试**

Run: `make test-integration`
Expected: PASS（两个驱动的 `TestContract/TableSchema_reads_the_table_with_normalized_types_and_defaults`）

- [ ] **Step 15: 全量检查并提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./...`
Expected: 无输出、通过

```bash
git add internal/db internal/ui/fakes_test.go
git commit -m "feat(db): 读取表的完整结构（TableSchema），归一化类型与默认值"
```

---
### Task 2: `db.SchemaChange` 与 `schemadiff.DiffTable`

**Files:**
- Create: `internal/db/schemachange.go`
- Create: `internal/schemadiff/diff.go`、`internal/schemadiff/types.go`
- Test: `internal/schemadiff/diff_test.go`、`internal/schemadiff/types_test.go`

**Interfaces:**
- Consumes: Task 1 的 `db.TableSchema`、`db.Column`、`db.Index` 新字段。
- Produces（Task 3–9 使用）：

  ```go
  // in package db
  type ChangeKind int // AddColumn, DropColumn, AlterColumn, RenameColumn, AddIndex, DropIndex,
                      // AlterTableComment, AlterTableOption, ForeignKeyDiff; String() gives the name
  type ColumnAttr uint8 // AttrType, AttrNullable, AttrDefault, AttrComment, AttrCollation, AttrAutoIncrement, AttrGenerated
  type Risk uint8       // Destructive, Lossy
  type SchemaChange struct {
      Kind               ChangeKind
      Old, New           *Column      // AddColumn: New; DropColumn: Old; AlterColumn, RenameColumn: both
      Attrs              ColumnAttr   // AlterColumn
      After              string       // AddColumn: the source column before it; "" = first
      OldIndex, NewIndex *Index       // DropIndex: OldIndex; AddIndex: NewIndex
      OldFK, NewFK       *ForeignKey  // ForeignKeyDiff: either may be nil
      Option             string       // AlterTableOption: the key in TableSchema.Options
      OldValue, NewValue string       // AlterTableComment, AlterTableOption
      Risk               Risk
  }

  // in package schemadiff
  func DiffTable(target, source db.TableSchema, renames map[string]string) []db.SchemaChange
  func LossyType(old, nw string) bool
  ```

  `DiffTable` 的输出顺序就是执行顺序：删索引 → 改列名 → 删列 → 加列 → 改列 → 加索引 → 表注释 → 表选项 → 外键差异（只显示）。变更里的指针指向传入的 `target` / `source` 的切片元素；新增索引是改过名的副本，不改动 `source`。

  风险标记：
  - `Destructive`：删列；删除唯一索引且没有唯一的替代（删表由 Task 5 的 `ObjectDiff` 标记）。
  - `Lossy`：`LossyType` 为真的改类型；可空改为 NOT NULL；新增 NOT NULL、无默认值、非自增、非生成列；新增唯一索引（已有数据可能重复）。

  索引配对：先把两边的主键配成一对，再按名字配对，剩下的按定义（列、唯一性、主键、方法、条件）配对。只是名字不同的索引不算差异。配对但定义不同的索引表示为 DropIndex + AddIndex，新索引沿用目标的名字。没配上的源索引若以源表名加 `_` 开头，新名字换成目标表名开头（`users_email_key` → `users_bak_email_key`）。

  外键配对：先按名字，再按定义。`RefTable` 与自己的表在同一命名空间时只比较表名，这样跨库、跨 schema 对比不会因为命名空间不同报差异。

- [ ] **Step 1: 写 `LossyType` 的测试**

Create `internal/schemadiff/types_test.go`:

```go
package schemadiff

import "testing"

func TestLossyType(t *testing.T) {
	for _, c := range []struct {
		old, nw string
		want    bool
	}{
		{"int", "int", false},
		{"int", "bigint", false},
		{"bigint", "int", true},
		{"tinyint(1)", "int", false},
		{"int unsigned", "bigint", false},
		{"int unsigned", "int", true},
		{"int", "int unsigned", true},
		{"varchar(100)", "varchar(255)", false},
		{"varchar(255)", "varchar(100)", true},
		{"varchar(100)", "text", false},
		{"text", "varchar(100)", true},
		{"char(10)", "varchar(10)", false},
		{"varchar(10)", "char(10)", true},
		{"decimal(10,2)", "decimal(12,2)", false},
		{"decimal(10,2)", "decimal(12,4)", false},
		{"decimal(10,2)", "decimal(10,3)", true},
		{"numeric(10,2)", "numeric", false},
		{"numeric", "numeric(10,2)", true},
		{"float", "double", false},
		{"double", "float", true},
		{"timestamp(3)", "timestamp(6)", false},
		{"timestamp(6)", "timestamp(3)", true},
		{"int[]", "bigint[]", false},
		{"int", "int[]", true},
		{"varchar(10)", "int", true},
		{"enum('a','b')", "enum('a','b','c')", true},
	} {
		if got := LossyType(c.old, c.nw); got != c.want {
			t.Errorf("LossyType(%q, %q) = %v, want %v", c.old, c.nw, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 写 `DiffTable` 的测试**

Create `internal/schemadiff/diff_test.go`:

```go
package schemadiff

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

var app = db.Namespace{Database: "app"}

func table(name string, cols []db.Column, idx ...db.Index) db.TableSchema {
	return db.TableSchema{Ref: db.TableRef{Namespace: app, Name: name}, Columns: cols, Indexes: idx}
}

func str(s string) *string { return &s }

// summary is one "Kind name risk" line per change.
func summary(changes []db.SchemaChange) []string {
	var out []string
	for _, c := range changes {
		name := c.Option
		switch {
		case c.New != nil:
			name = c.New.Name
		case c.Old != nil:
			name = c.Old.Name
		case c.NewIndex != nil:
			name = c.NewIndex.Name
		case c.OldIndex != nil:
			name = c.OldIndex.Name
		case c.NewFK != nil:
			name = c.NewFK.Name
		case c.OldFK != nil:
			name = c.OldFK.Name
		}
		out = append(out, fmt.Sprintf("%s %s %d", c.Kind, name, c.Risk))
	}
	return out
}

var pk = db.Index{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true, Method: "btree"}

func TestDiffTableIdentical(t *testing.T) {
	a := table("users", []db.Column{{Name: "id", Type: "bigint"}}, pk)
	if got := DiffTable(a, a, nil); len(got) != 0 {
		t.Fatalf("changes = %v", summary(got))
	}
}

func TestDiffTableColumns(t *testing.T) {
	target := table("users", []db.Column{
		{Name: "id", Type: "bigint"},
		{Name: "name", Type: "varchar(50)", Nullable: true},
		{Name: "legacy", Type: "int", Nullable: true},
	})
	source := table("users", []db.Column{
		{Name: "id", Type: "bigint"},
		{Name: "name", Type: "varchar(100)", Comment: "who"},
		{Name: "email", Type: "varchar(255)", Nullable: true},
	})
	got := DiffTable(target, source, nil)
	want := []string{
		"DropColumn legacy 1",  // Destructive
		"AddColumn email 0",
		"AlterColumn name 2",   // Lossy: NOT NULL now
	}
	if diff := cmp.Diff(want, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[1].After != "name" {
		t.Errorf("email goes after %q, want name", got[1].After)
	}
	if a := got[2].Attrs; a != db.AttrType|db.AttrNullable|db.AttrComment {
		t.Errorf("attrs = %b", a)
	}
	if got[2].Old != &target.Columns[1] || got[2].New != &source.Columns[1] {
		t.Error("changes point into target and source")
	}
}

func TestDiffTableRenames(t *testing.T) {
	target := table("users", []db.Column{{Name: "id", Type: "int"}, {Name: "nm", Type: "varchar(10)"}})
	source := table("users", []db.Column{{Name: "id", Type: "int"}, {Name: "name", Type: "varchar(20)"}})
	got := DiffTable(target, source, map[string]string{"nm": "name"})
	if diff := cmp.Diff([]string{"RenameColumn name 0", "AlterColumn name 0"}, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[0].Old.Name != "nm" || got[1].Old.Name != "nm" || got[1].Attrs != db.AttrType {
		t.Errorf("rename %+v, alter %+v", got[0], got[1])
	}
	plain := DiffTable(target, source, nil)
	// name is NOT NULL without a default: adding it fails on a table with rows
	if diff := cmp.Diff([]string{"DropColumn nm 1", "AddColumn name 2"}, summary(plain)); diff != "" {
		t.Fatalf("without renames (-want +got):\n%s", diff)
	}
}

func TestDiffTableColumnRisks(t *testing.T) {
	base := []db.Column{{Name: "id", Type: "int"}}
	for _, c := range []struct {
		name     string
		old, nw  *db.Column
		wantRisk db.Risk
	}{
		{"add NOT NULL without default", nil, &db.Column{Name: "c", Type: "int"}, db.Lossy},
		{"add NOT NULL with default", nil, &db.Column{Name: "c", Type: "int", Default: str("0")}, 0},
		{"add nullable", nil, &db.Column{Name: "c", Type: "int", Nullable: true}, 0},
		{"add auto increment", nil, &db.Column{Name: "c", Type: "int", AutoIncrement: true}, 0},
		{"add generated", nil, &db.Column{Name: "c", Type: "int", Generated: "id + 1"}, 0},
		{"narrow", &db.Column{Name: "c", Type: "varchar(100)"}, &db.Column{Name: "c", Type: "varchar(50)"}, db.Lossy},
		{"widen", &db.Column{Name: "c", Type: "varchar(50)"}, &db.Column{Name: "c", Type: "varchar(100)"}, 0},
		{"comment only", &db.Column{Name: "c", Type: "int", Comment: "a"}, &db.Column{Name: "c", Type: "int", Comment: "b"}, 0},
		{"drop NOT NULL", &db.Column{Name: "c", Type: "int"}, &db.Column{Name: "c", Type: "int", Nullable: true}, 0},
		{"drop", &db.Column{Name: "c", Type: "int"}, nil, db.Destructive},
	} {
		t.Run(c.name, func(t *testing.T) {
			target, source := table("t", slices.Clone(base)), table("t", slices.Clone(base))
			if c.old != nil {
				target.Columns = append(target.Columns, *c.old)
			}
			if c.nw != nil {
				source.Columns = append(source.Columns, *c.nw)
			}
			got := DiffTable(target, source, nil)
			if len(got) != 1 || got[0].Risk != c.wantRisk {
				t.Fatalf("changes = %v, want one with risk %d", summary(got), c.wantRisk)
			}
		})
	}
}

func TestDiffTablePairsIndexes(t *testing.T) {
	target := table("users", []db.Column{{Name: "id", Type: "int"}}, pk,
		db.Index{Name: "idx_a", Columns: []string{"a"}, Method: "btree"},
		db.Index{Name: "u_b", Columns: []string{"b"}, Unique: true, Method: "btree"},
		db.Index{Name: "old_x", Columns: []string{"x"}, Method: "btree"},
		db.Index{Name: "u_c", Columns: []string{"c"}, Unique: true, Method: "btree"},
		db.Index{Name: "z_old_name", Columns: []string{"z"}, Method: "btree"},
	)
	source := table("users", []db.Column{{Name: "id", Type: "int"}},
		db.Index{Name: "users_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Method: "btree"},
		db.Index{Name: "idx_a", Columns: []string{"a", "b"}, Method: "btree"},
		db.Index{Name: "u_b", Columns: []string{"b"}, Unique: true, Method: "btree"},
		db.Index{Name: "new_y", Columns: []string{"y"}, Unique: true, Method: "btree"},
		db.Index{Name: "z_new_name", Columns: []string{"z"}, Method: "btree"},
	)
	want := []string{
		"DropIndex idx_a 0",
		"DropIndex old_x 0",
		"DropIndex u_c 1", // a unique index goes and nothing replaces it
		"AddIndex idx_a 0",
		"AddIndex new_y 2", // existing rows may hold duplicates
	}
	if diff := cmp.Diff(want, summary(DiffTable(target, source, nil))); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestDiffTableRetargetsIndexNames(t *testing.T) {
	target := table("users_bak", []db.Column{{Name: "id", Type: "int"}},
		db.Index{Name: "users_bak_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Constraint: true})
	source := table("users", []db.Column{{Name: "id", Type: "int"}},
		db.Index{Name: "users_pkey", Columns: []string{"id", "tenant"}, Unique: true, Primary: true, Constraint: true},
		db.Index{Name: "users_email_key", Columns: []string{"email"}, Unique: true, Constraint: true})
	got := DiffTable(target, source, nil)
	want := []string{"DropIndex users_bak_pkey 0", "AddIndex users_bak_pkey 2", "AddIndex users_bak_email_key 2"}
	if diff := cmp.Diff(want, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if source.Indexes[1].Name != "users_email_key" {
		t.Error("DiffTable renamed the source's index")
	}
}

func TestDiffTableCommentAndOptions(t *testing.T) {
	target := table("t", nil)
	target.Options = map[string]string{"engine": "MyISAM", "collation": "utf8mb4_bin"}
	source := table("t", nil)
	source.Comment = "people"
	source.Options = map[string]string{"engine": "InnoDB", "collation": "utf8mb4_bin"}
	got := DiffTable(target, source, nil)
	if diff := cmp.Diff([]string{"AlterTableComment  0", "AlterTableOption engine 0"}, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[0].NewValue != "people" || got[1].OldValue != "MyISAM" || got[1].NewValue != "InnoDB" {
		t.Errorf("changes = %+v", got)
	}
	source.Options = nil // a side without options (PostgreSQL) changes none
	if got := DiffTable(target, source, nil); len(got) != 1 {
		t.Errorf("changes = %v", summary(got))
	}
}

func TestDiffTableForeignKeys(t *testing.T) {
	ref := func(database, name string) db.TableRef {
		return db.TableRef{Namespace: db.Namespace{Database: database}, Name: name}
	}
	target := table("orders", nil)
	target.ForeignKeys = []db.ForeignKey{
		{Name: "fk_user", Columns: []string{"user_id"}, RefTable: ref("app", "users"), RefColumns: []string{"id"}},
		{Name: "fk_gone", Columns: []string{"x"}, RefTable: ref("app", "x"), RefColumns: []string{"id"}},
	}
	source := table("orders", nil)
	source.Ref.Database = "app2" // another namespace: its local references point into app2
	source.ForeignKeys = []db.ForeignKey{
		{Name: "fk_user", Columns: []string{"user_id"}, RefTable: ref("app2", "users"), RefColumns: []string{"id"}},
		{Name: "fk_new", Columns: []string{"y"}, RefTable: ref("app2", "y"), RefColumns: []string{"id"}},
	}
	got := DiffTable(target, source, nil)
	if diff := cmp.Diff([]string{"ForeignKeyDiff fk_new 0", "ForeignKeyDiff fk_gone 0"}, summary(got)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if got[0].OldFK != nil || got[1].NewFK != nil {
		t.Errorf("changes = %+v", got)
	}
}
```

注意 `TestDiffTableForeignKeys` 里外键的顺序：先列出只有源有的（`fk_new`），再列只有目标有的（`fk_gone`）——实现里按这个顺序写。

- [ ] **Step 3: 运行，确认编译失败**

Run: `go test ./internal/schemadiff`
Expected: FAIL，`undefined: LossyType`、`undefined: DiffTable`、`db.AddColumn undefined`

- [ ] **Step 4: 定义变更类型**

Create `internal/db/schemachange.go`:

```go
package db

// ChangeKind is the kind of one SchemaChange.
type ChangeKind int

const (
	AddColumn ChangeKind = iota
	DropColumn
	AlterColumn
	RenameColumn
	AddIndex
	DropIndex
	AlterTableComment
	AlterTableOption
	// ForeignKeyDiff is shown, never synced.
	ForeignKeyDiff
)

var changeKindNames = [...]string{
	AddColumn: "AddColumn", DropColumn: "DropColumn", AlterColumn: "AlterColumn", RenameColumn: "RenameColumn",
	AddIndex: "AddIndex", DropIndex: "DropIndex", AlterTableComment: "AlterTableComment",
	AlterTableOption: "AlterTableOption", ForeignKeyDiff: "ForeignKeyDiff",
}

func (k ChangeKind) String() string { return changeKindNames[k] }

// ColumnAttr flags what an AlterColumn changes.
type ColumnAttr uint8

const (
	AttrType ColumnAttr = 1 << iota
	AttrNullable
	AttrDefault
	AttrComment
	AttrCollation
	AttrAutoIncrement
	AttrGenerated
)

// Risk flags changes that can lose data.
type Risk uint8

const (
	// Destructive changes delete something: a table, a column, a unique index.
	Destructive Risk = 1 << iota
	// Lossy changes can fail on, or cut, the data a table holds: a narrower
	// type, NOT NULL, a new unique index.
	Lossy
)

// SchemaChange is one change to a table, computed by package schemadiff and
// written as SQL by a DDLWriter. Old is what the table has, New what it
// should have.
type SchemaChange struct {
	Kind     ChangeKind
	Old, New *Column    // AddColumn: New; DropColumn: Old; AlterColumn and RenameColumn: both
	Attrs    ColumnAttr // AlterColumn: what differs
	// After is, for AddColumn, the column the new one follows in the
	// source; "" puts it first.
	After              string
	OldIndex, NewIndex *Index      // DropIndex: OldIndex; AddIndex: NewIndex
	OldFK, NewFK       *ForeignKey // ForeignKeyDiff: nil on the side that lacks it
	Option             string      // AlterTableOption: the key in TableSchema.Options
	OldValue, NewValue string      // AlterTableComment and AlterTableOption
	Risk               Risk
}
```

- [ ] **Step 5: `LossyType`**

Create `internal/schemadiff/types.go`:

```go
package schemadiff

import (
	"math"
	"strconv"
	"strings"
)

// parsedType is a normalized column type split up: varchar(100) is base
// varchar with args [100].
type parsedType struct {
	base     string
	args     []int
	unsigned bool
	array    bool
}

func parseType(t string) parsedType {
	var p parsedType
	t, p.array = strings.CutSuffix(t, "[]")
	t, p.unsigned = strings.CutSuffix(t, " unsigned")
	p.base = t
	open := strings.IndexByte(t, '(')
	if open <= 0 || !strings.HasSuffix(t, ")") {
		return p
	}
	var args []int
	for _, a := range strings.Split(t[open+1:len(t)-1], ",") {
		n, err := strconv.Atoi(strings.TrimSpace(a))
		if err != nil {
			return p // enum('a','b') and the like stay whole
		}
		args = append(args, n)
	}
	p.base, p.args = t[:open], args
	return p
}

var (
	intRank      = map[string]int{"tinyint": 1, "smallint": 2, "mediumint": 3, "int": 4, "bigint": 5}
	textCapacity = map[string]int{"tinytext": 255, "text": 65535, "mediumtext": 16777215, "longtext": math.MaxInt}
	wider        = map[[2]string]bool{{"float", "double"}: true, {"real", "double precision"}: true, {"real", "double"}: true}
)

func isText(base string) bool { return base == "char" || base == "varchar" || textCapacity[base] > 0 }

func isDecimal(base string) bool { return base == "decimal" || base == "numeric" }

// capacity is how many characters a text type holds.
func capacity(p parsedType) int {
	switch {
	case p.base == "char" && len(p.args) > 0, p.base == "varchar" && len(p.args) > 0:
		return p.args[0]
	case p.base == "char":
		return 1
	case p.base == "varchar":
		return math.MaxInt
	}
	return textCapacity[p.base]
}

// LossyType reports whether changing a column from type old to type nw (both
// normalized) can reject or cut values the column holds. Changes it does not
// understand count as lossy.
func LossyType(old, nw string) bool {
	if old == nw {
		return false
	}
	o, n := parseType(old), parseType(nw)
	if o.array != n.array {
		return true
	}
	if ro, rn := intRank[o.base], intRank[n.base]; ro > 0 && rn > 0 {
		switch {
		case o.unsigned == n.unsigned:
			return rn < ro
		case o.unsigned: // unsigned → signed needs a wider type
			return rn <= ro
		default: // signed → unsigned loses negative values
			return true
		}
	}
	if o.unsigned != n.unsigned {
		return true
	}
	switch {
	case isText(o.base) && isText(n.base):
		return n.base == "char" && o.base != "char" || capacity(n) < capacity(o)
	case isDecimal(o.base) && isDecimal(n.base):
		if len(n.args) == 0 {
			return false // unbounded
		}
		if len(o.args) == 0 {
			return true
		}
		scale := func(p parsedType) int {
			if len(p.args) > 1 {
				return p.args[1]
			}
			return 0
		}
		return n.args[0]-scale(n) < o.args[0]-scale(o) || scale(n) < scale(o)
	case o.base == n.base:
		if len(o.args) != len(n.args) {
			return true
		}
		for i := range o.args {
			if n.args[i] < o.args[i] {
				return true
			}
		}
		return false
	}
	return !wider[[2]string{o.base, n.base}]
}
```

- [ ] **Step 6: `DiffTable`**

Create `internal/schemadiff/diff.go`:

```go
// Package schemadiff computes the changes that make one table, or one
// namespace, look like another. It writes no SQL: drivers do that
// (db.DDLWriter), so this package knows nothing about dialects.
package schemadiff

import (
	"slices"
	"sort"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// DiffTable lists the changes that make target look like source, in the
// order they can be applied: index drops, column renames, column drops,
// column adds, column alters, index adds, the table's comment and options,
// then foreign key differences (shown, never synced). Columns pair by name;
// renames (old name → new name) pairs a target column with a renamed
// source column, which only Modify Table passes. The changes point into
// target and source.
func DiffTable(target, source db.TableSchema, renames map[string]string) []db.SchemaChange {
	cols := diffColumns(target, source, renames)
	indexDrops, indexAdds := diffIndexes(target, source)
	out := slices.Concat(indexDrops, cols, indexAdds, diffTable(target, source))
	return append(out, diffForeignKeys(target, source)...)
}

// diffColumns returns renames, drops, adds and alters, in that order.
func diffColumns(target, source db.TableSchema, renames map[string]string) []db.SchemaChange {
	srcByName := map[string]*db.Column{}
	for i := range source.Columns {
		srcByName[source.Columns[i].Name] = &source.Columns[i]
	}
	matched := map[string]bool{}
	var renamed, drops, adds, alters []db.SchemaChange
	for i := range target.Columns {
		old := &target.Columns[i]
		name := old.Name
		if to, ok := renames[name]; ok {
			name = to
		}
		nw, ok := srcByName[name]
		if !ok {
			drops = append(drops, db.SchemaChange{Kind: db.DropColumn, Old: old, Risk: db.Destructive})
			continue
		}
		matched[name] = true
		if name != old.Name {
			renamed = append(renamed, db.SchemaChange{Kind: db.RenameColumn, Old: old, New: nw})
		}
		if attrs := columnAttrs(*old, *nw); attrs != 0 {
			alters = append(alters, db.SchemaChange{Kind: db.AlterColumn, Old: old, New: nw, Attrs: attrs, Risk: alterRisk(*old, *nw, attrs)})
		}
	}
	for i := range source.Columns {
		c := &source.Columns[i]
		if matched[c.Name] {
			continue
		}
		ch := db.SchemaChange{Kind: db.AddColumn, New: c}
		if i > 0 {
			ch.After = source.Columns[i-1].Name
		}
		if !c.Nullable && c.Default == nil && !c.AutoIncrement && c.Generated == "" {
			ch.Risk = db.Lossy // fails on a table with rows
		}
		adds = append(adds, ch)
	}
	return slices.Concat(renamed, drops, adds, alters)
}

func columnAttrs(old, nw db.Column) db.ColumnAttr {
	var a db.ColumnAttr
	if old.Type != nw.Type {
		a |= db.AttrType
	}
	if old.Nullable != nw.Nullable {
		a |= db.AttrNullable
	}
	if (old.Default == nil) != (nw.Default == nil) || old.Default != nil && *old.Default != *nw.Default {
		a |= db.AttrDefault
	}
	if old.Comment != nw.Comment {
		a |= db.AttrComment
	}
	if old.Collation != nw.Collation {
		a |= db.AttrCollation
	}
	if old.AutoIncrement != nw.AutoIncrement {
		a |= db.AttrAutoIncrement
	}
	if old.Generated != nw.Generated {
		a |= db.AttrGenerated
	}
	return a
}

func alterRisk(old, nw db.Column, a db.ColumnAttr) db.Risk {
	if a&db.AttrType != 0 && LossyType(old.Type, nw.Type) || a&db.AttrNullable != 0 && !nw.Nullable {
		return db.Lossy
	}
	return 0
}

func sameIndex(a, b db.Index) bool {
	return slices.Equal(a.Columns, b.Columns) && a.Unique == b.Unique && a.Primary == b.Primary &&
		a.Method == b.Method && a.Where == b.Where
}

// diffIndexes pairs the primary keys, then indexes by name, then the rest
// by definition, so that an index named differently on the other side is
// no change. A changed index is dropped and added again under the
// target's name.
func diffIndexes(target, source db.TableSchema) (drops, adds []db.SchemaChange) {
	tgt := make([]*db.Index, len(target.Indexes))
	for i := range target.Indexes {
		tgt[i] = &target.Indexes[i]
	}
	src := make([]*db.Index, len(source.Indexes))
	for i := range source.Indexes {
		src[i] = &source.Indexes[i]
	}
	var pairs [][2]*db.Index
	take := func(match func(t, s *db.Index) bool) {
		for i := range tgt {
			for j := range src {
				if tgt[i] != nil && src[j] != nil && match(tgt[i], src[j]) {
					pairs = append(pairs, [2]*db.Index{tgt[i], src[j]})
					tgt[i], src[j] = nil, nil
					break
				}
			}
		}
	}
	take(func(t, s *db.Index) bool { return t.Primary && s.Primary })
	take(func(t, s *db.Index) bool { return t.Name == s.Name })
	take(func(t, s *db.Index) bool { return sameIndex(*t, *s) })

	drop := func(old, replacement *db.Index) {
		ch := db.SchemaChange{Kind: db.DropIndex, OldIndex: old}
		if old.Unique && (replacement == nil || !replacement.Unique) {
			ch.Risk = db.Destructive
		}
		drops = append(drops, ch)
	}
	add := func(ix *db.Index, name string) {
		cp := *ix
		cp.Name = name
		ch := db.SchemaChange{Kind: db.AddIndex, NewIndex: &cp}
		if cp.Unique {
			ch.Risk = db.Lossy
		}
		adds = append(adds, ch)
	}
	for _, p := range pairs {
		if !sameIndex(*p[0], *p[1]) {
			drop(p[0], p[1])
			add(p[1], p[0].Name)
		}
	}
	for _, t := range tgt {
		if t != nil {
			drop(t, nil)
		}
	}
	for _, s := range src {
		if s != nil {
			add(s, indexName(s.Name, source.Ref.Name, target.Ref.Name))
		}
	}
	return drops, adds
}

// indexName is the name source's index gets on target's table. Names that
// start with the source table's name follow the target's, so comparing
// users with users_bak in one PostgreSQL schema, where index names are
// unique, does not try to reuse users's names.
func indexName(name, srcTable, tgtTable string) string {
	if srcTable != tgtTable {
		if rest, ok := strings.CutPrefix(name, srcTable+"_"); ok {
			return tgtTable + "_" + rest
		}
	}
	return name
}

// diffTable compares the table's own comment and options. Options the
// source does not have (PostgreSQL has none) are left alone.
func diffTable(target, source db.TableSchema) []db.SchemaChange {
	var out []db.SchemaChange
	if target.Comment != source.Comment {
		out = append(out, db.SchemaChange{Kind: db.AlterTableComment, OldValue: target.Comment, NewValue: source.Comment})
	}
	keys := make([]string, 0, len(source.Options))
	for k := range source.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := source.Options[k]; v != target.Options[k] {
			out = append(out, db.SchemaChange{Kind: db.AlterTableOption, Option: k, OldValue: target.Options[k], NewValue: v})
		}
	}
	return out
}

// sameForeignKey compares two foreign keys of tables in possibly different
// namespaces: a reference into the table's own namespace matches one into
// the other table's own namespace.
func sameForeignKey(a db.ForeignKey, aNS db.Namespace, b db.ForeignKey, bNS db.Namespace) bool {
	aLocal, bLocal := a.RefTable.Namespace == aNS, b.RefTable.Namespace == bNS
	sameRef := aLocal && bLocal && a.RefTable.Name == b.RefTable.Name || !aLocal && !bLocal && a.RefTable == b.RefTable
	return sameRef && slices.Equal(a.Columns, b.Columns) && slices.Equal(a.RefColumns, b.RefColumns)
}

// diffForeignKeys pairs foreign keys by name, then by definition, and
// reports the differences: the source's extra keys first, then changed
// ones, then the target's extra keys.
func diffForeignKeys(target, source db.TableSchema) []db.SchemaChange {
	tNS, sNS := target.Ref.Namespace, source.Ref.Namespace
	tgt := make([]*db.ForeignKey, len(target.ForeignKeys))
	for i := range target.ForeignKeys {
		tgt[i] = &target.ForeignKeys[i]
	}
	var added, changed []db.SchemaChange
	for i := range source.ForeignKeys {
		s := &source.ForeignKeys[i]
		match := -1
		for j, t := range tgt {
			if t != nil && t.Name == s.Name {
				match = j
				break
			}
		}
		if match < 0 {
			for j, t := range tgt {
				if t != nil && sameForeignKey(*t, tNS, *s, sNS) {
					match = j
					break
				}
			}
		}
		if match < 0 {
			added = append(added, db.SchemaChange{Kind: db.ForeignKeyDiff, NewFK: s})
			continue
		}
		if t := tgt[match]; !sameForeignKey(*t, tNS, *s, sNS) {
			changed = append(changed, db.SchemaChange{Kind: db.ForeignKeyDiff, OldFK: t, NewFK: s})
		}
		tgt[match] = nil
	}
	var removed []db.SchemaChange
	for _, t := range tgt {
		if t != nil {
			removed = append(removed, db.SchemaChange{Kind: db.ForeignKeyDiff, OldFK: t})
		}
	}
	return slices.Concat(added, changed, removed)
}
```

- [ ] **Step 7: 运行测试，确认通过**

Run: `go test -race ./internal/schemadiff ./internal/db`
Expected: PASS

如果 `TestDiffTablePairsIndexes` 不过，先核对配对顺序：主键配对 → `idx_a`、`u_b`、`old_x`（不在源里）按名字 → `z_old_name` 与 `z_new_name` 按定义配上（不产生变更）。

- [ ] **Step 8: 全量检查并提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./...`
Expected: 无输出、通过

```bash
git add internal/db/schemachange.go internal/schemadiff
git commit -m "feat(schemadiff): 计算把目标表改成与源表一致的变更"
```

---
### Task 3: `DDLWriter.CreateTable` / `AlterTable`（两个驱动）

**Files:**
- Modify: `internal/db/driver.go`（`DDLWriter` 新增两个方法）
- Modify: `internal/db/mysql/ddlwriter.go`、`internal/db/postgres/ddlwriter.go`
- Modify: `internal/db/mysql/mysql_test.go`、`internal/db/postgres/postgres_test.go`（golden 测试）
- Modify: `internal/ui/fakes_test.go`（`fakeDDL` 实现新方法）

**Interfaces:**
- Consumes: Task 1 的 `db.TableSchema`，Task 2 的 `db.SchemaChange` 及其常量。
- Produces（Task 4、5、7、9 使用）：

  ```go
  // DDLWriter gains:
  CreateTable(s TableSchema) (stmts []string, manual []SchemaChange)
  AlterTable(t TableRef, changes []SchemaChange) (stmts []string, manual []SchemaChange)
  ```

  语句不带结尾分号。`CreateTable` 不建外键；写不出的索引（MySQL 的表达式索引，`information_schema` 只给出 `(expression)`）以 `AddIndex` 的形式放进 `manual`。`AlterTable` 按传入顺序写：MySQL 合并成**一条** `ALTER TABLE`（改名并修改同一列时用 `CHANGE COLUMN`），PG 每项变更一条语句（同一列的多个属性合在一条里），注释用 `COMMENT ON`。写不出的放进 `manual`：

  | | MySQL | PG |
  |---|---|---|
  | `AlterColumn` 改了 `Generated` | manual | manual |
  | `AlterColumn` 改了 `AutoIncrement` | `MODIFY` 写得出 | manual |
  | `AlterColumn` 改类型且没有隐式 / 赋值转换（需要 `USING`） | `MODIFY` 写得出 | manual |
  | 表达式索引 | manual | 写得出（列是 `pg_get_indexdef` 的原文） |
  | `AlterTableOption` | `ENGINE =` / `COLLATE =` | manual（PG 没有） |
  | `ForeignKeyDiff` | manual | manual |

  生成列一律写成 `STORED`（PG 只支持 `STORED`；MySQL 的 `VIRTUAL` / `STORED` 不在 `TableSchema` 里）。

- [ ] **Step 1: 写 MySQL 的 golden 测试**

在 `internal/db/mysql/mysql_test.go` 末尾追加：

```go
func TestCreateTable(t *testing.T) {
	w := dialect{}.DDL()
	zero := "0"
	s := db.TableSchema{
		Ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "audit log"},
		Columns: []db.Column{
			{Name: "id", Type: "bigint", AutoIncrement: true},
			{Name: "n", Type: "int", Default: &zero, Comment: `it's \ here`},
			{Name: "code", Type: "varchar(10)", Nullable: true, Collation: "utf8mb4_bin"},
			{Name: "total", Type: "int", Nullable: true, Generated: "`n` * 2"},
		},
		Indexes: []db.Index{
			{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true, Method: "btree"},
			{Name: "code_u", Columns: []string{"code"}, Unique: true, Method: "btree"},
			{Name: "n_idx", Columns: []string{"n", "code"}, Method: "btree"},
			{Name: "fx", Columns: []string{"(expression)"}, Method: "btree"},
		},
		Comment: "who did what",
		Options: map[string]string{"engine": "InnoDB", "collation": "utf8mb4_0900_ai_ci"},
	}
	stmts, manual := w.CreateTable(s)
	want := "CREATE TABLE `app`.`audit log` (\n" +
		"  `id` bigint NOT NULL AUTO_INCREMENT,\n" +
		"  `n` int NOT NULL DEFAULT 0 COMMENT 'it''s \\\\ here',\n" +
		"  `code` varchar(10) COLLATE utf8mb4_bin NULL,\n" +
		"  `total` int GENERATED ALWAYS AS (`n` * 2) STORED NULL,\n" +
		"  PRIMARY KEY (`id`),\n" +
		"  UNIQUE INDEX `code_u` (`code`),\n" +
		"  INDEX `n_idx` (`n`, `code`)\n" +
		") ENGINE=InnoDB COLLATE=utf8mb4_0900_ai_ci COMMENT='who did what'"
	if len(stmts) != 1 || stmts[0] != want {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(stmts, ";\n"), want)
	}
	if len(manual) != 1 || manual[0].Kind != db.AddIndex || manual[0].NewIndex.Name != "fx" {
		t.Errorf("manual = %+v", manual)
	}
}

func TestAlterTable(t *testing.T) {
	w := dialect{}.DDL()
	users := db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"}
	changes := []db.SchemaChange{
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "idx_old", Columns: []string{"x"}}},
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
		{Kind: db.RenameColumn, Old: &db.Column{Name: "nm"}, New: &db.Column{Name: "nick"}},
		{Kind: db.RenameColumn, Old: &db.Column{Name: "a"}, New: &db.Column{Name: "b", Type: "bigint"}},
		{Kind: db.DropColumn, Old: &db.Column{Name: "legacy"}},
		{Kind: db.AddColumn, New: &db.Column{Name: "email", Type: "varchar(255)", Nullable: true}, After: "name"},
		{Kind: db.AddColumn, New: &db.Column{Name: "first", Type: "int", Nullable: true}},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "name", Type: "varchar(50)"},
			New: &db.Column{Name: "name", Type: "varchar(100)", Comment: "who"}, Attrs: db.AttrType | db.AttrComment},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "a", Type: "int"}, New: &db.Column{Name: "b", Type: "bigint"}, Attrs: db.AttrType},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "g", Type: "int"}, New: &db.Column{Name: "g", Type: "int", Generated: "1"}, Attrs: db.AttrGenerated},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "PRIMARY", Columns: []string{"id", "tenant"}, Unique: true, Primary: true}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "email_u", Columns: []string{"email"}, Unique: true}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "body_ft", Columns: []string{"body"}, Method: "fulltext"}},
		{Kind: db.AlterTableComment, NewValue: "people"},
		{Kind: db.AlterTableOption, Option: "engine", NewValue: "InnoDB"},
		{Kind: db.AlterTableOption, Option: "collation", NewValue: "utf8mb4_bin"},
		{Kind: db.ForeignKeyDiff, NewFK: &db.ForeignKey{Name: "fk"}},
	}
	stmts, manual := w.AlterTable(users, changes)
	want := "ALTER TABLE `app`.`users`\n" +
		"  DROP INDEX `idx_old`,\n" +
		"  DROP PRIMARY KEY,\n" +
		"  RENAME COLUMN `nm` TO `nick`,\n" +
		"  DROP COLUMN `legacy`,\n" +
		"  ADD COLUMN `email` varchar(255) NULL AFTER `name`,\n" +
		"  ADD COLUMN `first` int NULL FIRST,\n" +
		"  MODIFY COLUMN `name` varchar(100) NOT NULL COMMENT 'who',\n" +
		"  CHANGE COLUMN `a` `b` bigint NOT NULL,\n" +
		"  ADD PRIMARY KEY (`id`, `tenant`),\n" +
		"  ADD UNIQUE INDEX `email_u` (`email`),\n" +
		"  ADD FULLTEXT INDEX `body_ft` (`body`),\n" +
		"  COMMENT = 'people',\n" +
		"  ENGINE = InnoDB,\n" +
		"  COLLATE = utf8mb4_bin"
	if len(stmts) != 1 || stmts[0] != want {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(stmts, ";\n"), want)
	}
	if len(manual) != 2 || manual[0].Kind != db.AlterColumn || manual[1].Kind != db.ForeignKeyDiff {
		t.Errorf("manual = %+v", manual)
	}
	if stmts, manual := w.AlterTable(users, nil); stmts != nil || manual != nil {
		t.Errorf("no changes: %v %v", stmts, manual)
	}
}
```

- [ ] **Step 2: 写 PG 的 golden 测试**

在 `internal/db/postgres/postgres_test.go` 末尾追加（若文件还没 import `strings`，加上）：

```go
func TestCreateTable(t *testing.T) {
	w := dialect{}.DDL()
	x := "'x'"
	s := db.TableSchema{
		Ref: db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "Audit"},
		Columns: []db.Column{
			{Name: "id", Type: "bigint", AutoIncrement: true},
			{Name: "code", Type: "varchar(10)", Nullable: true, Default: &x, Collation: "C", Comment: "it's"},
			{Name: "total", Type: "int", Nullable: true, Generated: "id * 2"},
		},
		Indexes: []db.Index{
			{Name: "audit_pkey", Columns: []string{"id"}, Unique: true, Primary: true, Constraint: true, Method: "btree"},
			{Name: "audit_code_key", Columns: []string{"code"}, Unique: true, Constraint: true, Method: "btree"},
			{Name: "audit_lower", Columns: []string{"lower((code)::text)"}, Method: "btree", Where: "id > 0"},
			{Name: "audit_gin", Columns: []string{"code"}, Method: "gin"},
		},
		Comment: "who did what",
	}
	stmts, manual := w.CreateTable(s)
	want := []string{
		`CREATE TABLE "public"."Audit" (` + "\n" +
			`  "id" bigint GENERATED BY DEFAULT AS IDENTITY NOT NULL,` + "\n" +
			`  "code" varchar(10) COLLATE "C" DEFAULT 'x',` + "\n" +
			`  "total" int GENERATED ALWAYS AS (id * 2) STORED,` + "\n" +
			`  CONSTRAINT "audit_pkey" PRIMARY KEY (id),` + "\n" +
			`  CONSTRAINT "audit_code_key" UNIQUE (code)` + "\n" +
			`)`,
		`CREATE INDEX "audit_lower" ON "public"."Audit" (lower((code)::text)) WHERE id > 0`,
		`CREATE INDEX "audit_gin" ON "public"."Audit" USING gin (code)`,
		`COMMENT ON TABLE "public"."Audit" IS 'who did what'`,
		`COMMENT ON COLUMN "public"."Audit"."code" IS 'it''s'`,
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	if len(manual) != 0 {
		t.Errorf("manual = %+v", manual)
	}
}

func TestAlterTable(t *testing.T) {
	w := dialect{}.DDL()
	users := db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}
	x := "'x'"
	changes := []db.SchemaChange{
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "users_pkey", Unique: true, Primary: true, Constraint: true}},
		{Kind: db.DropIndex, OldIndex: &db.Index{Name: "users_name_idx"}},
		{Kind: db.RenameColumn, Old: &db.Column{Name: "nm"}, New: &db.Column{Name: "Nick"}},
		{Kind: db.DropColumn, Old: &db.Column{Name: "legacy"}},
		{Kind: db.AddColumn, New: &db.Column{Name: "email", Type: "varchar(255)", Nullable: true, Comment: "mail"}, After: "name"},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "name", Type: "varchar(50)", Nullable: true},
			New: &db.Column{Name: "name", Type: "varchar(100)", Default: &x}, Attrs: db.AttrType | db.AttrNullable | db.AttrDefault},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "note", Type: "text", Comment: "c", Default: &x},
			New: &db.Column{Name: "note", Type: "text", Collation: "C"}, Attrs: db.AttrComment | db.AttrCollation | db.AttrDefault},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "price", Type: "text"}, New: &db.Column{Name: "price", Type: "int"}, Attrs: db.AttrType},
		{Kind: db.AlterColumn, Old: &db.Column{Name: "n", Type: "int"}, New: &db.Column{Name: "n", Type: "int", AutoIncrement: true}, Attrs: db.AttrAutoIncrement},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "users_pkey", Columns: []string{"id", "tenant"}, Unique: true, Primary: true, Constraint: true}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "users_email_key", Columns: []string{"email"}, Unique: true, Constraint: true}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "users_lower", Columns: []string{"lower(email)"}, Unique: true, Method: "btree", Where: "email IS NOT NULL"}},
		{Kind: db.AddIndex, NewIndex: &db.Index{Name: "users_tags", Columns: []string{"tags"}, Method: "gin"}},
		{Kind: db.AlterTableComment, OldValue: "people", NewValue: ""},
		{Kind: db.AlterTableOption, Option: "engine", NewValue: "InnoDB"},
	}
	stmts, manual := w.AlterTable(users, changes)
	want := []string{
		`ALTER TABLE "public"."users" DROP CONSTRAINT "users_pkey"`,
		`DROP INDEX "public"."users_name_idx"`,
		`ALTER TABLE "public"."users" RENAME COLUMN "nm" TO "Nick"`,
		`ALTER TABLE "public"."users" DROP COLUMN "legacy"`,
		`ALTER TABLE "public"."users" ADD COLUMN "email" varchar(255)`,
		`COMMENT ON COLUMN "public"."users"."email" IS 'mail'`,
		`ALTER TABLE "public"."users" ALTER COLUMN "name" TYPE varchar(100), ALTER COLUMN "name" SET NOT NULL, ALTER COLUMN "name" SET DEFAULT 'x'`,
		`ALTER TABLE "public"."users" ALTER COLUMN "note" TYPE text COLLATE "C", ALTER COLUMN "note" DROP DEFAULT`,
		`COMMENT ON COLUMN "public"."users"."note" IS NULL`,
		`ALTER TABLE "public"."users" ADD CONSTRAINT "users_pkey" PRIMARY KEY (id, tenant)`,
		`ALTER TABLE "public"."users" ADD CONSTRAINT "users_email_key" UNIQUE (email)`,
		`CREATE UNIQUE INDEX "users_lower" ON "public"."users" (lower(email)) WHERE email IS NOT NULL`,
		`CREATE INDEX "users_tags" ON "public"."users" USING gin (tags)`,
		`COMMENT ON TABLE "public"."users" IS NULL`,
	}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	var kinds []string
	for _, c := range manual {
		kinds = append(kinds, c.Kind.String()+" "+c.Option)
	}
	if diff := cmp.Diff([]string{"AlterColumn ", "AlterColumn ", "AlterTableOption engine"}, kinds); diff != "" {
		t.Errorf("manual (-want +got):\n%s", diff)
	}
}

func TestCastable(t *testing.T) {
	for _, c := range []struct {
		old, nw string
		want    bool
	}{
		{"varchar(50)", "varchar(100)", true},
		{"int", "bigint", true},
		{"bigint", "int", true},
		{"int", "numeric(10,2)", true},
		{"int", "text", true},
		{"uuid", "text", true},
		{"int[]", "text", true},
		{"int[]", "bigint[]", true},
		{"date", "timestamptz", true},
		{"text", "int", false},
		{"text", "uuid", false},
		{"bool", "int", false},
		{"int", "int[]", false},
	} {
		if got := castable(c.old, c.nw); got != c.want {
			t.Errorf("castable(%q, %q) = %v, want %v", c.old, c.nw, got, c.want)
		}
	}
}
```

如果 `postgres_test.go` 还没 import `github.com/google/go-cmp/cmp`，加上（`go.mod` 里已有这个依赖）。

- [ ] **Step 3: 运行，确认编译失败**

Run: `go test ./internal/db/mysql ./internal/db/postgres -run 'TestCreateTable|TestAlterTable|TestCastable'`
Expected: FAIL，`w.CreateTable undefined`、`undefined: castable`

- [ ] **Step 4: 扩展接口**

在 `internal/db/driver.go` 的 `DDLWriter` 里，`TruncateTable` 后面加：

```go
	// CreateTable writes the statements that create s, named by s.Ref,
	// without its foreign keys. Indexes the engine cannot spell out come
	// back as AddIndex changes in manual.
	CreateTable(s TableSchema) (stmts []string, manual []SchemaChange)
	// AlterTable writes changes, in the order given (package schemadiff
	// orders them), to table t: MySQL as one ALTER TABLE, PostgreSQL as one
	// statement per change plus COMMENT ON. Changes it cannot write, and
	// foreign key differences, come back in manual.
	AlterTable(t TableRef, changes []SchemaChange) (stmts []string, manual []SchemaChange)
```

- [ ] **Step 5: MySQL 实现**

在 `internal/db/mysql/ddlwriter.go` 里，把 import 改成：

```go
import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)
```

并在文件末尾追加：

```go
// columnDef is a column as CREATE TABLE and ALTER TABLE write it. NULL is
// spelled out: a TIMESTAMP column is NOT NULL unless told otherwise.
func (w ddlWriter) columnDef(c db.Column) string {
	var b strings.Builder
	b.WriteString(w.d.QuoteIdent(c.Name) + " " + c.Type)
	if c.Collation != "" {
		b.WriteString(" COLLATE " + c.Collation)
	}
	if c.Generated != "" {
		b.WriteString(" GENERATED ALWAYS AS (" + c.Generated + ") STORED")
	}
	if c.Nullable {
		b.WriteString(" NULL")
	} else {
		b.WriteString(" NOT NULL")
	}
	if c.Default != nil && c.Generated == "" {
		b.WriteString(" DEFAULT " + *c.Default)
	}
	if c.AutoIncrement {
		b.WriteString(" AUTO_INCREMENT")
	}
	if c.Comment != "" {
		b.WriteString(" COMMENT " + quoteString(c.Comment))
	}
	return b.String()
}

// indexDef is ix as CREATE TABLE lists it and ALTER TABLE ADD takes it; ok
// is false for expression indexes, whose expression information_schema
// does not give.
func (w ddlWriter) indexDef(ix db.Index) (def string, ok bool) {
	cols := make([]string, len(ix.Columns))
	for i, c := range ix.Columns {
		if c == "(expression)" {
			return "", false
		}
		cols[i] = w.d.QuoteIdent(c)
	}
	list := "(" + strings.Join(cols, ", ") + ")"
	name := w.d.QuoteIdent(ix.Name)
	switch {
	case ix.Primary:
		return "PRIMARY KEY " + list, true
	case ix.Method == "fulltext":
		return "FULLTEXT INDEX " + name + " " + list, true
	case ix.Method == "spatial":
		return "SPATIAL INDEX " + name + " " + list, true
	case ix.Unique:
		return "UNIQUE INDEX " + name + " " + list, true
	}
	return "INDEX " + name + " " + list, true
}

func (w ddlWriter) CreateTable(s db.TableSchema) ([]string, []db.SchemaChange) {
	lines := make([]string, 0, len(s.Columns)+len(s.Indexes))
	for _, c := range s.Columns {
		lines = append(lines, "  "+w.columnDef(c))
	}
	var manual []db.SchemaChange
	for i := range s.Indexes {
		def, ok := w.indexDef(s.Indexes[i])
		if !ok {
			manual = append(manual, db.SchemaChange{Kind: db.AddIndex, NewIndex: &s.Indexes[i]})
			continue
		}
		lines = append(lines, "  "+def)
	}
	stmt := "CREATE TABLE " + w.d.QualifiedName(s.Ref) + " (\n" + strings.Join(lines, ",\n") + "\n)"
	if e := s.Options["engine"]; e != "" {
		stmt += " ENGINE=" + e
	}
	if c := s.Options["collation"]; c != "" {
		stmt += " COLLATE=" + c
	}
	if s.Comment != "" {
		stmt += " COMMENT=" + quoteString(s.Comment)
	}
	return []string{stmt}, manual
}

func (w ddlWriter) AlterTable(t db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	// A column renamed and altered at once is one CHANGE COLUMN.
	altered := map[string]bool{}
	for _, c := range changes {
		if c.Kind == db.AlterColumn {
			altered[c.Old.Name] = true
		}
	}
	var clauses []string
	var manual []db.SchemaChange
	for _, c := range changes {
		if c.Kind == db.RenameColumn && altered[c.Old.Name] {
			continue
		}
		clause, ok := w.alterClause(c)
		if !ok {
			manual = append(manual, c)
			continue
		}
		clauses = append(clauses, clause)
	}
	if len(clauses) == 0 {
		return nil, manual
	}
	return []string{"ALTER TABLE " + w.d.QualifiedName(t) + "\n  " + strings.Join(clauses, ",\n  ")}, manual
}

func (w ddlWriter) alterClause(c db.SchemaChange) (string, bool) {
	q := w.d.QuoteIdent
	switch c.Kind {
	case db.AddColumn:
		pos := " FIRST"
		if c.After != "" {
			pos = " AFTER " + q(c.After)
		}
		return "ADD COLUMN " + w.columnDef(*c.New) + pos, true
	case db.DropColumn:
		return "DROP COLUMN " + q(c.Old.Name), true
	case db.RenameColumn:
		return "RENAME COLUMN " + q(c.Old.Name) + " TO " + q(c.New.Name), true
	case db.AlterColumn:
		switch {
		case c.Attrs&db.AttrGenerated != 0:
			return "", false
		case c.Old.Name != c.New.Name:
			return "CHANGE COLUMN " + q(c.Old.Name) + " " + w.columnDef(*c.New), true
		}
		return "MODIFY COLUMN " + w.columnDef(*c.New), true
	case db.AddIndex:
		def, ok := w.indexDef(*c.NewIndex)
		return "ADD " + def, ok
	case db.DropIndex:
		if c.OldIndex.Primary {
			return "DROP PRIMARY KEY", true
		}
		return "DROP INDEX " + q(c.OldIndex.Name), true
	case db.AlterTableComment:
		return "COMMENT = " + quoteString(c.NewValue), true
	case db.AlterTableOption:
		switch c.Option {
		case "engine":
			return "ENGINE = " + c.NewValue, true
		case "collation":
			return "COLLATE = " + c.NewValue, true
		}
	}
	return "", false
}
```

- [ ] **Step 6: PG 实现**

在 `internal/db/postgres/ddlwriter.go` 里，把 import 改成：

```go
import (
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)
```

并在文件末尾追加：

```go
// columnDef is a column as CREATE TABLE and ADD COLUMN write it.
func (w ddlWriter) columnDef(c db.Column) string {
	s := w.d.QuoteIdent(c.Name) + " " + c.Type
	if c.Collation != "" {
		s += " COLLATE " + w.d.QuoteIdent(c.Collation)
	}
	switch {
	case c.Generated != "":
		s += " GENERATED ALWAYS AS (" + c.Generated + ") STORED"
	case c.AutoIncrement:
		s += " GENERATED BY DEFAULT AS IDENTITY"
	}
	if !c.Nullable {
		s += " NOT NULL"
	}
	if c.Default != nil && c.Generated == "" && !c.AutoIncrement {
		s += " DEFAULT " + *c.Default
	}
	return s
}

// indexColumns lists ix's columns; they are pg_get_indexdef's text, quoted
// where needed already.
func indexColumns(ix db.Index) string { return "(" + strings.Join(ix.Columns, ", ") + ")" }

// constraintDef is the table constraint behind ix, or "" for a plain index.
func (w ddlWriter) constraintDef(ix db.Index) string {
	switch {
	case ix.Primary:
		return "CONSTRAINT " + w.d.QuoteIdent(ix.Name) + " PRIMARY KEY " + indexColumns(ix)
	case ix.Unique && ix.Constraint:
		return "CONSTRAINT " + w.d.QuoteIdent(ix.Name) + " UNIQUE " + indexColumns(ix)
	}
	return ""
}

func (w ddlWriter) createIndex(t db.TableRef, ix db.Index) string {
	s := "CREATE "
	if ix.Unique {
		s += "UNIQUE "
	}
	s += "INDEX " + w.d.QuoteIdent(ix.Name) + " ON " + w.d.QualifiedName(t)
	if ix.Method != "" && ix.Method != "btree" {
		s += " USING " + ix.Method
	}
	s += " " + indexColumns(ix)
	if ix.Where != "" {
		s += " WHERE " + ix.Where
	}
	return s
}

// comment is COMMENT ON what; an empty text removes the comment.
func comment(what, text string) string {
	v := "NULL"
	if text != "" {
		v = quoteString(text)
	}
	return "COMMENT ON " + what + " IS " + v
}

func (w ddlWriter) columnComment(t db.TableRef, col, text string) string {
	return comment("COLUMN "+w.d.QualifiedName(t)+"."+w.d.QuoteIdent(col), text)
}

func (w ddlWriter) CreateTable(s db.TableSchema) ([]string, []db.SchemaChange) {
	lines := make([]string, 0, len(s.Columns)+len(s.Indexes))
	for _, c := range s.Columns {
		lines = append(lines, w.columnDef(c))
	}
	var indexes []string
	for _, ix := range s.Indexes {
		if def := w.constraintDef(ix); def != "" {
			lines = append(lines, def)
		} else {
			indexes = append(indexes, w.createIndex(s.Ref, ix))
		}
	}
	stmts := []string{"CREATE TABLE " + w.d.QualifiedName(s.Ref) + " (\n  " + strings.Join(lines, ",\n  ") + "\n)"}
	stmts = append(stmts, indexes...)
	if s.Comment != "" {
		stmts = append(stmts, comment("TABLE "+w.d.QualifiedName(s.Ref), s.Comment))
	}
	for _, c := range s.Columns {
		if c.Comment != "" {
			stmts = append(stmts, w.columnComment(s.Ref, c.Name, c.Comment))
		}
	}
	return stmts, nil
}

func (w ddlWriter) AlterTable(t db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	q := w.d.QuoteIdent
	var stmts []string
	var manual []db.SchemaChange
	alter := func(clause string) { stmts = append(stmts, "ALTER TABLE "+w.d.QualifiedName(t)+" "+clause) }
	for _, c := range changes {
		switch c.Kind {
		case db.AddColumn:
			alter("ADD COLUMN " + w.columnDef(*c.New))
			if c.New.Comment != "" {
				stmts = append(stmts, w.columnComment(t, c.New.Name, c.New.Comment))
			}
		case db.DropColumn:
			alter("DROP COLUMN " + q(c.Old.Name))
		case db.RenameColumn:
			alter("RENAME COLUMN " + q(c.Old.Name) + " TO " + q(c.New.Name))
		case db.AlterColumn:
			st, ok := w.alterColumn(t, c)
			if !ok {
				manual = append(manual, c)
				continue
			}
			stmts = append(stmts, st...)
		case db.AddIndex:
			if def := w.constraintDef(*c.NewIndex); def != "" {
				alter("ADD " + def)
			} else {
				stmts = append(stmts, w.createIndex(t, *c.NewIndex))
			}
		case db.DropIndex:
			if c.OldIndex.Primary || c.OldIndex.Constraint {
				alter("DROP CONSTRAINT " + q(c.OldIndex.Name))
			} else {
				stmts = append(stmts, "DROP INDEX "+w.d.QualifiedName(db.TableRef{Namespace: t.Namespace, Name: c.OldIndex.Name}))
			}
		case db.AlterTableComment:
			stmts = append(stmts, comment("TABLE "+w.d.QualifiedName(t), c.NewValue))
		default: // options (PostgreSQL has none) and foreign keys
			manual = append(manual, c)
		}
	}
	return stmts, manual
}

// alterColumn writes one AlterColumn as one ALTER TABLE (plus COMMENT ON);
// ok is false for what needs a hand: identity and generated columns, and
// type changes that need USING.
func (w ddlWriter) alterColumn(t db.TableRef, c db.SchemaChange) (stmts []string, ok bool) {
	if c.Attrs&(db.AttrAutoIncrement|db.AttrGenerated) != 0 || c.Attrs&db.AttrType != 0 && !castable(c.Old.Type, c.New.Type) {
		return nil, false
	}
	col := "ALTER COLUMN " + w.d.QuoteIdent(c.New.Name)
	var parts []string
	if c.Attrs&(db.AttrType|db.AttrCollation) != 0 {
		p := col + " TYPE " + c.New.Type
		// TYPE alone would reset the collation to the type's default.
		if c.New.Collation != "" || c.Attrs&db.AttrCollation != 0 {
			coll := c.New.Collation
			if coll == "" {
				coll = "default"
			}
			p += " COLLATE " + w.d.QuoteIdent(coll)
		}
		parts = append(parts, p)
	}
	if c.Attrs&db.AttrNullable != 0 {
		if c.New.Nullable {
			parts = append(parts, col+" DROP NOT NULL")
		} else {
			parts = append(parts, col+" SET NOT NULL")
		}
	}
	if c.Attrs&db.AttrDefault != 0 {
		if c.New.Default == nil {
			parts = append(parts, col+" DROP DEFAULT")
		} else {
			parts = append(parts, col+" SET DEFAULT "+*c.New.Default)
		}
	}
	if len(parts) > 0 {
		stmts = append(stmts, "ALTER TABLE "+w.d.QualifiedName(t)+" "+strings.Join(parts, ", "))
	}
	if c.Attrs&db.AttrComment != 0 {
		stmts = append(stmts, w.columnComment(t, c.New.Name, c.New.Comment))
	}
	return stmts, true
}

// castable reports whether ALTER COLUMN … TYPE works without USING, that
// is whether the old type has an implicit or assignment cast to the new
// one: anything casts to a string type, and numbers, strings and dates
// cast within their family.
func castable(old, nw string) bool {
	oArr, nArr := strings.HasSuffix(old, "[]"), strings.HasSuffix(nw, "[]")
	o, n := typeFamily(strings.TrimSuffix(old, "[]")), typeFamily(strings.TrimSuffix(nw, "[]"))
	if n == "string" && !nArr {
		return true
	}
	return oArr == nArr && o != "" && o == n
}

func typeFamily(t string) string {
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = t[:i]
	}
	switch t {
	case "char", "varchar", "text":
		return "string"
	case "smallint", "int", "bigint", "numeric", "decimal", "real", "double precision":
		return "number"
	case "date", "timestamp", "timestamptz":
		return "time"
	}
	return ""
}
```

- [ ] **Step 7: fake 实现新方法**

在 `internal/ui/fakes_test.go` 的 `fakeDDL.TruncateTable` 后面追加：

```go
func (w fakeDDL) CreateTable(s db.TableSchema) ([]string, []db.SchemaChange) {
	return []string{"CREATE TABLE " + w.d.QualifiedName(s.Ref)}, nil
}

// AlterTable writes one line per change. Like the real writers it leaves
// generated-column changes and foreign keys to the user.
func (w fakeDDL) AlterTable(t db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	var stmts []string
	var manual []db.SchemaChange
	for _, c := range changes {
		if c.Kind == db.ForeignKeyDiff || c.Kind == db.AlterColumn && c.Attrs&db.AttrGenerated != 0 {
			manual = append(manual, c)
			continue
		}
		stmts = append(stmts, "ALTER TABLE "+w.d.QualifiedName(t)+" "+c.Kind.String()+" "+fakeChangeName(c))
	}
	return stmts, manual
}

// fakeChangeName is the column, index or option a change is about.
func fakeChangeName(c db.SchemaChange) string {
	switch {
	case c.New != nil:
		return c.New.Name
	case c.Old != nil:
		return c.Old.Name
	case c.NewIndex != nil:
		return c.NewIndex.Name
	case c.OldIndex != nil:
		return c.OldIndex.Name
	}
	return c.Option
}
```

- [ ] **Step 8: 运行测试，确认通过**

Run: `go test -race ./internal/db/... ./internal/ui/...`
Expected: PASS

- [ ] **Step 9: 全量检查并提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./...`
Expected: 无输出、通过

```bash
git add internal/db internal/ui/fakes_test.go
git commit -m "feat(db): DDLWriter 写出建表与 ALTER 语句，写不出的退回 manual"
```

---

### Task 4: contract 往返测试（MySQL 8 与 PG 16）

**Files:**
- Modify: `internal/db/dbtest/contract.go`（`Fixture.CompareSQL` 与一个子测试）
- Modify: `internal/db/mysql/integration_test.go`、`internal/db/postgres/integration_test.go`（`cmp_src` / `cmp_dst`）

**Interfaces:**
- Consumes: Task 1 的 `MetaReader.TableSchema`，Task 2 的 `schemadiff.DiffTable`，Task 3 的 `DDLWriter.CreateTable` / `AlterTable`。
- Produces: 无新接口；这是 spec §8 第 2 阶段的验收测试。

两张表放在同一个命名空间里（PG 是同一个 schema），顺带覆盖 Review Focus 第 2、3 条：索引名的改写、需要引号的列名 `Odd Col`。

- [ ] **Step 1: 写 contract 子测试**

在 `internal/db/dbtest/contract.go` 的 import 里加上 `"github.com/shepherd-yun/sqlvim/internal/schemadiff"`。

在 `Fixture` 末尾加：

```go
	// CompareSQL creates cmp_src and cmp_dst, two tables whose differences
	// the round-trip test syncs: cmp_dst lacks columns email, "Odd Col" and
	// created, has a narrower name (nullable, no comment), a narrower qty with
	// another default, an extra column legacy with an index, an index
	// cmp_dst_name on (name, qty) where cmp_src has cmp_src_name on (name),
	// no unique index cmp_src_email and no table comment.
	CompareSQL []string
```

在 `RunContract` 末尾（`DDLWriter renames, truncates and drops tables and views` 之后）追加：

```go
	t.Run("schema sync round-trips through DiffTable, AlterTable and CreateTable", func(t *testing.T) {
		exec := func(stmts []string) {
			t.Helper()
			for _, q := range stmts {
				if _, err := conn.Exec(ctx, q); err != nil {
					t.Fatalf("%s: %v", q, err)
				}
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
		w := d.DDL()
		exec(f.CompareSQL)
		src := schema("cmp_src")

		changes := schemadiff.DiffTable(schema("cmp_dst"), src, nil)
		if len(changes) < 10 {
			t.Fatalf("the fixture yields only %d changes: %+v", len(changes), changes)
		}
		stmts, manual := w.AlterTable(ref("cmp_dst"), changes)
		if len(manual) != 0 {
			t.Fatalf("manual: %+v", manual)
		}
		exec(stmts)
		if left := schemadiff.DiffTable(schema("cmp_dst"), src, nil); len(left) != 0 {
			t.Fatalf("after\n%s\nstill differ: %+v", strings.Join(stmts, ";\n"), left)
		}

		created := src
		created.Ref = ref("cmp_new")
		created.Indexes = slices.Clone(src.Indexes)
		for i := range created.Indexes {
			// PostgreSQL index names are unique per schema: cmp_src has these.
			created.Indexes[i].Name = strings.Replace(created.Indexes[i].Name, "cmp_src", "cmp_new", 1)
		}
		stmts, manual = w.CreateTable(created)
		if len(manual) != 0 {
			t.Fatalf("manual: %+v", manual)
		}
		exec(stmts)
		if left := schemadiff.DiffTable(schema("cmp_new"), src, nil); len(left) != 0 {
			t.Fatalf("after\n%s\nstill differ: %+v", strings.Join(stmts, ";\n"), left)
		}
	})
```

- [ ] **Step 2: MySQL 的 fixture**

在 `internal/db/mysql/integration_test.go` 的 `dbtest.Fixture{` 里，`CICollation` 之后加：

```go
		CompareSQL: []string{
			"CREATE TABLE cmp_src (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100) NOT NULL COMMENT 'who', " +
				"email VARCHAR(255), qty BIGINT NOT NULL DEFAULT 1, `Odd Col` VARCHAR(10), " +
				"created TIMESTAMP NULL DEFAULT CURRENT_TIMESTAMP, KEY cmp_src_name (name), " +
				"UNIQUE KEY cmp_src_email (email)) COMMENT 'it''s the source'",
			"CREATE TABLE cmp_dst (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(50) NULL, " +
				"qty INT NOT NULL DEFAULT 0, legacy INT, KEY cmp_legacy (legacy), KEY cmp_dst_name (name, qty))",
		},
```

- [ ] **Step 3: PG 的 fixture**

在 `internal/db/postgres/integration_test.go` 的 `dbtest.Fixture{` 里，`CICollation` 之后加：

```go
		CompareSQL: []string{
			`CREATE TABLE app.cmp_src (id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			   name varchar(100) NOT NULL, email varchar(255), qty bigint NOT NULL DEFAULT 1,
			   "Odd Col" varchar(10), created timestamptz DEFAULT CURRENT_TIMESTAMP)`,
			`CREATE INDEX cmp_src_name ON app.cmp_src (name)`,
			`CREATE UNIQUE INDEX cmp_src_email ON app.cmp_src (email)`,
			`COMMENT ON TABLE app.cmp_src IS 'it''s the source'`,
			`COMMENT ON COLUMN app.cmp_src.name IS 'who'`,
			`CREATE TABLE app.cmp_dst (id bigint GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			   name varchar(50), qty int NOT NULL DEFAULT 0, legacy int)`,
			`CREATE INDEX cmp_legacy ON app.cmp_dst (legacy)`,
			`CREATE INDEX cmp_dst_name ON app.cmp_dst (name, qty)`,
		},
```

两边的变更都是：删 `cmp_legacy`、删 `cmp_dst_name`、删列 `legacy`、加 `email` / `Odd Col` / `created`、改 `name`、改 `qty`、加 `cmp_dst_name`（由 `cmp_src_name` 改名而来）、加 `cmp_dst_email`、表注释——共 11 项。PG 的 `qty` 从 `int` 到 `bigint` 不需要 `USING`，所以没有 manual。

- [ ] **Step 4: 跑集成测试**

Run: `make test-integration`
Expected: PASS（两个驱动的 `TestContract/schema_sync_round-trips_through_DiffTable,_AlterTable_and_CreateTable`）

失败时先看错误里打印的语句：
- 在 MySQL 上报 `Unknown column 'email'`（`AFTER` 引用同一条 ALTER 里新加的列）：说明 `AFTER` 只能引用已有的列，把 `alterClause` 里的 `AddColumn` 改成：当 `c.After` 是同一批 `changes` 里另一个 `AddColumn` 的列时省略 `AFTER`，并在 `TestAlterTable` 里补一个这样的用例。
- 再读出的差异不为空：打印 `left` 看是哪一项，多半是归一化（Task 1）漏了某种写法，回到 `normType` / `normDefault` 补单元测试和实现。

- [ ] **Step 5: 全量检查并提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./... && make test-integration`
Expected: 无输出、通过

```bash
git add internal/db/dbtest/contract.go internal/db/mysql/integration_test.go internal/db/postgres/integration_test.go
git commit -m "test(db): 结构同步往返测试，两个引擎上对比、ALTER、重读后无差异"
```

---
### Task 5: `schemadiff`：命名空间对比、描述与脚本

**Files:**
- Create: `internal/schemadiff/namespace.go`、`internal/schemadiff/describe.go`、`internal/schemadiff/script.go`
- Test: `internal/schemadiff/namespace_test.go`、`internal/schemadiff/describe_test.go`、`internal/schemadiff/script_test.go`

**Interfaces:**
- Consumes: Task 2 的 `DiffTable`，Task 3 的 `db.DDLWriter`。
- Produces（Task 7、9 使用）：

  ```go
  type ObjectKind int // CreateTable (only the source has it), DropTable (only the target), AlterTable
  type ObjectDiff struct {
      Kind    ObjectKind
      Name    string
      Source  *db.TableSchema   // nil for DropTable
      Target  *db.TableSchema   // nil for CreateTable
      Changes []db.SchemaChange // AlterTable only
  }
  func DiffNamespace(target, source []db.TableSchema) (objs []ObjectDiff, identical int)
  func Describe(c db.SchemaChange) string
  func DescribeObject(o ObjectDiff) string
  func Writable(w db.DDLWriter, t db.TableRef, c db.SchemaChange) bool
  type Picked func(obj, change int) bool // change -1: the whole table (CreateTable, DropTable)
  func Script(w db.DDLWriter, ns db.Namespace, objs []ObjectDiff, picked Picked) (stmts, manual []string)
  ```

  `Script` 的语句顺序：建表 → 各表的 ALTER → 删表。`manual` 每行一项（`users: ~ column g  …`），列出写不出的变更，不论是否勾选；外键差异不列出。`ns` 是目标命名空间，新表建在这里。

- [ ] **Step 1: 写测试**

Create `internal/schemadiff/describe_test.go`:

```go
package schemadiff

import (
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestDescribe(t *testing.T) {
	x := "'x'"
	users := db.TableRef{Namespace: app, Name: "users"}
	people := db.TableRef{Namespace: app, Name: "people"}
	for _, c := range []struct {
		change db.SchemaChange
		want   string
	}{
		{db.SchemaChange{Kind: db.AddColumn, New: &db.Column{Name: "email", Type: "varchar(255)", Nullable: true}},
			"+ column email  varchar(255) NULL"},
		{db.SchemaChange{Kind: db.DropColumn, Old: &db.Column{Name: "legacy", Type: "int", Default: &x}},
			"- column legacy  int NOT NULL DEFAULT 'x'"},
		{db.SchemaChange{Kind: db.AddColumn, New: &db.Column{Name: "id", Type: "bigint", AutoIncrement: true}},
			"+ column id  bigint NOT NULL auto increment"},
		{db.SchemaChange{Kind: db.AlterColumn, Old: &db.Column{Name: "name", Type: "varchar(50)", Nullable: true},
			New: &db.Column{Name: "name", Type: "varchar(100)"}, Attrs: db.AttrType | db.AttrNullable},
			"~ column name  varchar(50) NULL → varchar(100) NOT NULL"},
		{db.SchemaChange{Kind: db.AlterColumn, Old: &db.Column{Name: "n", Type: "int"},
			New: &db.Column{Name: "n", Type: "int", Default: &x, Comment: "who"}, Attrs: db.AttrDefault | db.AttrComment},
			"~ column n  no default comment '' → DEFAULT 'x' comment 'who'"},
		{db.SchemaChange{Kind: db.AlterColumn, Old: &db.Column{Name: "c", Type: "int"},
			New:   &db.Column{Name: "c", Type: "int", Collation: "C", AutoIncrement: true, Generated: "a + 1"},
			Attrs: db.AttrCollation | db.AttrAutoIncrement | db.AttrGenerated},
			"~ column c  default collation no auto increment not generated → COLLATE C auto increment AS (a + 1)"},
		{db.SchemaChange{Kind: db.RenameColumn, Old: &db.Column{Name: "nm"}, New: &db.Column{Name: "nick"}},
			"~ column nm → nick  (rename)"},
		{db.SchemaChange{Kind: db.AddIndex, NewIndex: &db.Index{Name: "u_email", Columns: []string{"email"}, Unique: true, Method: "btree"}},
			"+ index u_email (email) unique"},
		{db.SchemaChange{Kind: db.AddIndex, NewIndex: &db.Index{Name: "tags", Columns: []string{"tags"}, Method: "gin", Where: "x > 0"}},
			"+ index tags (tags) gin WHERE x > 0"},
		{db.SchemaChange{Kind: db.DropIndex, OldIndex: &db.Index{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
			"- primary key (id)"},
		{db.SchemaChange{Kind: db.AlterTableComment, NewValue: "people"}, "~ comment '' → 'people'"},
		{db.SchemaChange{Kind: db.AlterTableOption, Option: "engine", OldValue: "MyISAM", NewValue: "InnoDB"},
			"~ engine MyISAM → InnoDB"},
		{db.SchemaChange{Kind: db.ForeignKeyDiff, NewFK: &db.ForeignKey{Name: "fk", Columns: []string{"user_id"}, RefTable: users, RefColumns: []string{"id"}}},
			"+ foreign key fk (user_id) REFERENCES users (id)"},
		{db.SchemaChange{Kind: db.ForeignKeyDiff,
			OldFK: &db.ForeignKey{Name: "fk", Columns: []string{"user_id"}, RefTable: users, RefColumns: []string{"id"}},
			NewFK: &db.ForeignKey{Name: "fk", Columns: []string{"user_id"}, RefTable: people, RefColumns: []string{"id"}}},
			"~ foreign key fk (user_id) REFERENCES users (id) → (user_id) REFERENCES people (id)"},
		{db.SchemaChange{Kind: db.ForeignKeyDiff, OldFK: &db.ForeignKey{Name: "fk", Columns: []string{"a"}, RefTable: users, RefColumns: []string{"id"}}},
			"- foreign key fk (a) REFERENCES users (id)"},
	} {
		if got := Describe(c.change); got != c.want {
			t.Errorf("got  %s\nwant %s", got, c.want)
		}
	}
}

func TestDescribeObject(t *testing.T) {
	two := table("audit", []db.Column{{Name: "id"}, {Name: "at"}})
	one := table("log", []db.Column{{Name: "id"}})
	for _, c := range []struct {
		o    ObjectDiff
		want string
	}{
		{ObjectDiff{Kind: CreateTable, Name: "audit", Source: &two}, "+ audit  (new table, 2 columns)"},
		{ObjectDiff{Kind: CreateTable, Name: "log", Source: &one}, "+ log  (new table, 1 column)"},
		{ObjectDiff{Kind: DropTable, Name: "tmp"}, "- tmp"},
		{ObjectDiff{Kind: AlterTable, Name: "users"}, "~ users"},
	} {
		if got := DescribeObject(c.o); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
```

Create `internal/schemadiff/namespace_test.go`:

```go
package schemadiff

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func TestDiffNamespace(t *testing.T) {
	id := []db.Column{{Name: "id", Type: "int"}}
	target := []db.TableSchema{table("users", id), table("tmp", id), table("same", id)}
	source := []db.TableSchema{
		table("users", []db.Column{{Name: "id", Type: "int"}, {Name: "name", Type: "text", Nullable: true}}),
		table("audit", id),
		table("same", id),
	}
	objs, identical := DiffNamespace(target, source)
	var got []string
	for _, o := range objs {
		got = append(got, DescribeObject(o))
	}
	want := []string{"+ audit  (new table, 1 column)", "- tmp", "~ users"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	if identical != 1 {
		t.Errorf("identical = %d", identical)
	}
	if objs[0].Source != &source[1] || objs[1].Target != &target[1] || len(objs[2].Changes) != 1 {
		t.Errorf("objects = %+v", objs)
	}
}
```

Create `internal/schemadiff/script_test.go`:

```go
package schemadiff

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// fakeWriter writes one short line per statement and, like the real ones,
// cannot write generated-column changes or foreign keys.
type fakeWriter struct{}

func (fakeWriter) DropTable(t db.TableRef, _ bool) string      { return "DROP " + t.Name }
func (fakeWriter) RenameTable(db.TableRef, string, bool) string { return "" }
func (fakeWriter) TruncateTable(db.TableRef) string             { return "" }

func (fakeWriter) CreateTable(s db.TableSchema) ([]string, []db.SchemaChange) {
	return []string{"CREATE " + s.Ref.Database + "." + s.Ref.Name}, nil
}

func (fakeWriter) AlterTable(t db.TableRef, changes []db.SchemaChange) ([]string, []db.SchemaChange) {
	var stmts []string
	var manual []db.SchemaChange
	for _, c := range changes {
		if c.Kind == db.ForeignKeyDiff || c.Kind == db.AlterColumn && c.Attrs&db.AttrGenerated != 0 {
			manual = append(manual, c)
			continue
		}
		stmts = append(stmts, "ALTER "+t.Name+" "+Describe(c))
	}
	return stmts, manual
}

func scriptFixture() []ObjectDiff {
	id := db.Column{Name: "id", Type: "int"}
	target := []db.TableSchema{table("users", []db.Column{id, {Name: "g", Type: "int"}}), table("tmp", []db.Column{id})}
	src := table("users", []db.Column{id, {Name: "g", Type: "int", Generated: "id + 1"}, {Name: "name", Type: "text", Nullable: true}})
	src.ForeignKeys = []db.ForeignKey{{Name: "fk", Columns: []string{"id"}, RefTable: src.Ref, RefColumns: []string{"id"}}}
	audit := table("audit", []db.Column{id})
	audit.Ref.Database = "app2" // created in the target's namespace, not the source's
	objs, _ := DiffNamespace(target, []db.TableSchema{src, audit})
	return objs
}

func TestWritable(t *testing.T) {
	objs := scriptFixture()
	users := objs[2]
	var got []bool
	for _, c := range users.Changes {
		got = append(got, Writable(fakeWriter{}, users.Target.Ref, c))
	}
	// AddColumn name, AlterColumn g (generated), ForeignKeyDiff fk
	if diff := cmp.Diff([]bool{true, false, false}, got); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

func TestScript(t *testing.T) {
	objs := scriptFixture()
	stmts, manual := Script(fakeWriter{}, app, objs, func(int, int) bool { return true })
	want := []string{"CREATE app.audit", "ALTER users + column name  text NULL", "DROP tmp"}
	if diff := cmp.Diff(want, stmts); diff != "" {
		t.Errorf("stmts (-want +got):\n%s", diff)
	}
	wantManual := []string{"users: ~ column g  not generated → AS (id + 1)"}
	if diff := cmp.Diff(wantManual, manual); diff != "" {
		t.Errorf("manual (-want +got):\n%s", diff)
	}

	stmts, manual = Script(fakeWriter{}, app, objs, func(int, int) bool { return false })
	if len(stmts) != 0 {
		t.Errorf("nothing picked, stmts = %v", stmts)
	}
	if diff := cmp.Diff(wantManual, manual); diff != "" {
		t.Errorf("manual changes are listed even when nothing is picked (-want +got):\n%s", diff)
	}

	// only the users table's AddColumn
	stmts, _ = Script(fakeWriter{}, app, objs, func(o, c int) bool { return o == 2 && c == 0 })
	if diff := cmp.Diff([]string{"ALTER users + column name  text NULL"}, stmts); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
}
```

`scriptFixture` 的对象按名字排序：`audit`（新建）、`tmp`（删除）、`users`（修改）。`users` 的变更依次是 `AddColumn name`、`AlterColumn g`、`ForeignKeyDiff fk`（`DiffTable` 的顺序：加列在改列之前，外键最后）。

- [ ] **Step 2: 运行，确认编译失败**

Run: `go test ./internal/schemadiff`
Expected: FAIL，`undefined: Describe`、`undefined: DiffNamespace`、`undefined: Script`

- [ ] **Step 3: `DiffNamespace`**

Create `internal/schemadiff/namespace.go`:

```go
package schemadiff

import (
	"sort"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// ObjectKind says how a table differs between the two sides.
type ObjectKind int

const (
	CreateTable ObjectKind = iota // only the source has the table
	DropTable                     // only the target has it
	AlterTable                    // both have it, with Changes
)

// ObjectDiff is one table that differs between two namespaces.
type ObjectDiff struct {
	Kind    ObjectKind
	Name    string
	Source  *db.TableSchema   // nil for DropTable
	Target  *db.TableSchema   // nil for CreateTable
	Changes []db.SchemaChange // AlterTable only
}

// DiffNamespace pairs the tables of two namespaces by name and lists those
// that differ, sorted by name; identical counts the tables both sides
// have alike. The objects point into target and source.
func DiffNamespace(target, source []db.TableSchema) (objs []ObjectDiff, identical int) {
	byName := map[string]*db.TableSchema{}
	for i := range target {
		byName[target[i].Ref.Name] = &target[i]
	}
	inSource := map[string]bool{}
	for i := range source {
		s := &source[i]
		inSource[s.Ref.Name] = true
		t, ok := byName[s.Ref.Name]
		if !ok {
			objs = append(objs, ObjectDiff{Kind: CreateTable, Name: s.Ref.Name, Source: s})
			continue
		}
		changes := DiffTable(*t, *s, nil)
		if len(changes) == 0 {
			identical++
			continue
		}
		objs = append(objs, ObjectDiff{Kind: AlterTable, Name: s.Ref.Name, Source: s, Target: t, Changes: changes})
	}
	for i := range target {
		if !inSource[target[i].Ref.Name] {
			objs = append(objs, ObjectDiff{Kind: DropTable, Name: target[i].Ref.Name, Target: &target[i]})
		}
	}
	sort.SliceStable(objs, func(i, j int) bool { return objs[i].Name < objs[j].Name })
	return objs, identical
}
```

- [ ] **Step 4: `Describe`**

Create `internal/schemadiff/describe.go`:

```go
package schemadiff

import (
	"fmt"
	"strings"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Describe is c on one line, as the diff tab lists it: a sign (+ added,
// - removed, ~ changed), what it is about, and the target's value → the
// source's.
func Describe(c db.SchemaChange) string {
	switch c.Kind {
	case db.AddColumn:
		return "+ column " + c.New.Name + "  " + columnText(*c.New)
	case db.DropColumn:
		return "- column " + c.Old.Name + "  " + columnText(*c.Old)
	case db.RenameColumn:
		return "~ column " + c.Old.Name + " → " + c.New.Name + "  (rename)"
	case db.AlterColumn:
		return "~ column " + c.New.Name + "  " + attrText(*c.Old, c.Attrs) + " → " + attrText(*c.New, c.Attrs)
	case db.AddIndex:
		return "+ " + indexText(*c.NewIndex)
	case db.DropIndex:
		return "- " + indexText(*c.OldIndex)
	case db.AlterTableComment:
		return "~ comment " + quoted(c.OldValue) + " → " + quoted(c.NewValue)
	case db.AlterTableOption:
		return "~ " + c.Option + " " + c.OldValue + " → " + c.NewValue
	case db.ForeignKeyDiff:
		switch {
		case c.OldFK == nil:
			return "+ foreign key " + c.NewFK.Name + " " + refText(*c.NewFK)
		case c.NewFK == nil:
			return "- foreign key " + c.OldFK.Name + " " + refText(*c.OldFK)
		}
		return "~ foreign key " + c.NewFK.Name + " " + refText(*c.OldFK) + " → " + refText(*c.NewFK)
	}
	return ""
}

// DescribeObject is the line of a whole table.
func DescribeObject(o ObjectDiff) string {
	switch o.Kind {
	case CreateTable:
		n := len(o.Source.Columns)
		noun := "columns"
		if n == 1 {
			noun = "column"
		}
		return fmt.Sprintf("+ %s  (new table, %d %s)", o.Name, n, noun)
	case DropTable:
		return "- " + o.Name
	}
	return "~ " + o.Name
}

func columnText(c db.Column) string {
	parts := []string{c.Type, nullText(c.Nullable)}
	if c.Default != nil {
		parts = append(parts, "DEFAULT "+*c.Default)
	}
	if c.AutoIncrement {
		parts = append(parts, "auto increment")
	}
	if c.Generated != "" {
		parts = append(parts, "AS ("+c.Generated+")")
	}
	return strings.Join(parts, " ")
}

// attrText shows the attributes of c that a flags.
func attrText(c db.Column, a db.ColumnAttr) string {
	var parts []string
	if a&db.AttrType != 0 {
		parts = append(parts, c.Type)
	}
	if a&db.AttrNullable != 0 {
		parts = append(parts, nullText(c.Nullable))
	}
	if a&db.AttrDefault != 0 {
		if c.Default == nil {
			parts = append(parts, "no default")
		} else {
			parts = append(parts, "DEFAULT "+*c.Default)
		}
	}
	if a&db.AttrCollation != 0 {
		if c.Collation == "" {
			parts = append(parts, "default collation")
		} else {
			parts = append(parts, "COLLATE "+c.Collation)
		}
	}
	if a&db.AttrAutoIncrement != 0 {
		if c.AutoIncrement {
			parts = append(parts, "auto increment")
		} else {
			parts = append(parts, "no auto increment")
		}
	}
	if a&db.AttrGenerated != 0 {
		if c.Generated == "" {
			parts = append(parts, "not generated")
		} else {
			parts = append(parts, "AS ("+c.Generated+")")
		}
	}
	if a&db.AttrComment != 0 {
		parts = append(parts, "comment "+quoted(c.Comment))
	}
	return strings.Join(parts, " ")
}

func nullText(nullable bool) string {
	if nullable {
		return "NULL"
	}
	return "NOT NULL"
}

func indexText(ix db.Index) string {
	cols := "(" + strings.Join(ix.Columns, ", ") + ")"
	if ix.Primary {
		return "primary key " + cols
	}
	s := "index " + ix.Name + " " + cols
	if ix.Unique {
		s += " unique"
	}
	if ix.Method != "" && ix.Method != "btree" {
		s += " " + ix.Method
	}
	if ix.Where != "" {
		s += " WHERE " + ix.Where
	}
	return s
}

func refText(fk db.ForeignKey) string {
	return "(" + strings.Join(fk.Columns, ", ") + ") REFERENCES " + fk.RefTable.Name + " (" + strings.Join(fk.RefColumns, ", ") + ")"
}

// quoted shows a comment; for people, not SQL.
func quoted(s string) string { return "'" + s + "'" }
```

- [ ] **Step 5: `Writable` 与 `Script`**

Create `internal/schemadiff/script.go`:

```go
package schemadiff

import (
	"slices"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Writable reports whether w can write c as SQL on table t. What it cannot
// write is left to the user; foreign keys are never synced.
func Writable(w db.DDLWriter, t db.TableRef, c db.SchemaChange) bool {
	if c.Kind == db.ForeignKeyDiff {
		return false
	}
	_, manual := w.AlterTable(t, []db.SchemaChange{c})
	return len(manual) == 0
}

// Picked says whether the script includes change of object obj; change is
// -1 for a whole table to create or drop.
type Picked func(obj, change int) bool

// Script writes the picked parts of objs as statements that make the target
// like the source: table creates first (in ns, the target's namespace),
// then each table's changes, then drops. manual lists, one line each, the
// changes w cannot write, whether picked or not; foreign key differences
// are left out.
func Script(w db.DDLWriter, ns db.Namespace, objs []ObjectDiff, picked Picked) (stmts, manual []string) {
	var creates, alters, drops []string
	note := func(table string, changes []db.SchemaChange) {
		for _, c := range changes {
			manual = append(manual, table+": "+Describe(c))
		}
	}
	for oi, o := range objs {
		switch o.Kind {
		case CreateTable:
			if !picked(oi, -1) {
				continue
			}
			s := *o.Source
			s.Ref = db.TableRef{Namespace: ns, Name: o.Name}
			st, m := w.CreateTable(s)
			creates = append(creates, st...)
			note(o.Name, m)
		case DropTable:
			if picked(oi, -1) {
				drops = append(drops, w.DropTable(o.Target.Ref, false))
			}
		case AlterTable:
			var chosen, unwritable []db.SchemaChange
			for ci, c := range o.Changes {
				switch {
				case c.Kind == db.ForeignKeyDiff:
				case !Writable(w, o.Target.Ref, c):
					unwritable = append(unwritable, c)
				case picked(oi, ci):
					chosen = append(chosen, c)
				}
			}
			st, m := w.AlterTable(o.Target.Ref, chosen)
			alters = append(alters, st...)
			note(o.Name, slices.Concat(unwritable, m))
		}
	}
	return slices.Concat(creates, alters, drops), manual
}
```

- [ ] **Step 6: 运行测试，确认通过**

Run: `go test -race ./internal/schemadiff`
Expected: PASS

- [ ] **Step 7: 全量检查并提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./...`
Expected: 无输出、通过

```bash
git add internal/schemadiff
git commit -m "feat(schemadiff): 命名空间对比、变更描述与同步脚本"
```

---
### Task 6: keymap：`Diff` 上下文与对比动作

**Files:**
- Modify: `internal/ui/keymap/keymap.go`（`Diff` 上下文、7 个动作）
- Modify: `internal/ui/keymap/custom.go`（`Contexts()` 加 `Diff`）
- Modify: `internal/ui/keymap/defaults.go`（默认绑定）
- Test: `internal/ui/keymap/keymap_test.go`、`internal/ui/keymap/custom_test.go`

**Interfaces:**
- Produces（Task 7–9 使用）：

  ```go
  const Diff Context = "diff"
  const (
      MarkCompare  Action = "compare.mark"  // tree: m
      Compare      Action = "compare.run"   // tree: C
      ToggleChange Action = "diff.toggle"   // diff: x
      SwapSides    Action = "diff.swap"     // diff: ~
      NextChange   Action = "diff.next"     // diff: ]c
      PrevChange   Action = "diff.prev"     // diff: [c
      WriteScript  Action = "diff.script"   // diff: <leader>w
  )
  ```

  `Diff` 里还绑定 `vertical(Diff)` 的移动键，`l` / `enter` / `right` → `Expand`，`h` / `left` → `Collapse`。`<leader>r`（`Refresh`）是 Global 绑定，Diff 标签页直接收到。

- [ ] **Step 1: 写测试**

在 `internal/ui/keymap/keymap_test.go` 末尾追加：

```go
func TestCompareBindings(t *testing.T) {
	k := Default()
	for _, c := range []struct {
		ctx  Context
		keys []string
		want Action
	}{
		{Tree, []string{"m"}, MarkCompare},
		{Tree, []string{"C"}, Compare},
		{Diff, []string{"x"}, ToggleChange},
		{Diff, []string{"~"}, SwapSides},
		{Diff, []string{"]", "c"}, NextChange},
		{Diff, []string{"[", "c"}, PrevChange},
		{Diff, []string{"l"}, Expand},
		{Diff, []string{"enter"}, Expand},
		{Diff, []string{"h"}, Collapse},
		{Diff, []string{"j"}, Down},
		{Diff, []string{"g", "g"}, Top},
		{Diff, []string{"space", "w"}, WriteScript},
		{Diff, []string{"space", "r"}, Refresh},
	} {
		if a, res := k.Resolve(c.ctx, c.keys); a != c.want || res != Matched {
			t.Errorf("%s %v = %q %v, want %q", c.ctx, c.keys, a, res, c.want)
		}
	}
	if keys := k.KeysFor(Diff, WriteScript); keys == "" {
		t.Error("WriteScript has no keys in Diff")
	}
}
```

在 `internal/ui/keymap/custom_test.go` 末尾追加：

```go
func TestCustomDiffTable(t *testing.T) {
	if !slices.Contains(Contexts(), Diff) {
		t.Fatalf("Contexts() = %v, want it to contain diff", Contexts())
	}
	k, err := Custom("space", map[Context]map[string]string{Diff: {"t": string(ToggleChange)}})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := k.Resolve(Diff, []string{"t"}); a != ToggleChange {
		t.Errorf("t = %q", a)
	}
}
```

（若 `custom_test.go` 还没 import `slices`，加上。）

- [ ] **Step 2: 运行，确认编译失败**

Run: `go test ./internal/ui/keymap`
Expected: FAIL，`undefined: MarkCompare`、`undefined: Diff`

- [ ] **Step 3: 实现**

在 `internal/ui/keymap/keymap.go` 的 Context 常量里，`Results` 之后加：

```go
	Diff      Context = "diff"
```

在 Action 常量块末尾（`PrevTab` 之后）加：

```go
	MarkCompare       Action = "compare.mark"
	Compare           Action = "compare.run"
	ToggleChange      Action = "diff.toggle"
	SwapSides         Action = "diff.swap"
	NextChange        Action = "diff.next"
	PrevChange        Action = "diff.prev"
	WriteScript       Action = "diff.script"
```

`internal/ui/keymap/custom.go`：

```go
func Contexts() []Context {
	return []Context{Global, Tree, Grid, Structure, Console, Results, Diff, Popup}
}
```

`internal/ui/keymap/defaults.go`：Tree 那组绑定里，`NodeActions` 之后加：

```go
		Binding{Tree, keys("m"), MarkCompare, "Mark as the compare source"},
		Binding{Tree, keys("C"), Compare, "Compare with the marked node"},
```

Structure 那组之后、Popup 那组之前加：

```go
	b = append(b, vertical(Diff)...)
	b = append(b,
		Binding{Diff, keys("l"), Expand, "Expand the table"},
		Binding{Diff, keys("enter"), Expand, "Expand the table"},
		Binding{Diff, keys("right"), Expand, "Expand the table"},
		Binding{Diff, keys("h"), Collapse, "Collapse the table"},
		Binding{Diff, keys("left"), Collapse, "Collapse the table"},
		Binding{Diff, keys("x"), ToggleChange, "Include / skip in the script"},
		Binding{Diff, keys("~"), SwapSides, "Swap source and target"},
		Binding{Diff, keys("]", "c"), NextChange, "Next change"},
		Binding{Diff, keys("[", "c"), PrevChange, "Previous change"},
		Binding{Diff, keys(LeaderToken, "w"), WriteScript, "Write the sync script to a new console"},
	)
```

- [ ] **Step 4: 运行测试，确认通过**

Run: `go test -race ./internal/ui/...`
Expected: PASS。如果某个已有测试列举了全部上下文（`grep -rn '"results"' internal/ui` 能找到），把 `diff` 加进它的期望里。

- [ ] **Step 5: 提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./...`

```bash
git add internal/ui/keymap
git commit -m "feat(keymap): Diff 上下文与对比相关的按键"
```

---

### Task 7: Diff 标签页（结构视图）

**Files:**
- Create: `internal/ui/difftab.go`
- Modify: `internal/ui/app.go`（`showTab`、`openDiff`、`askPassword`、`passwordSubmitted`）
- Modify: `internal/ui/fakes_test.go`（按名字区分的连接）
- Test: `internal/ui/difftab_test.go`

**Interfaces:**
- Consumes: Task 1 `MetaReader.TableSchema`，Task 3 `DDLWriter`，Task 5 `schemadiff.DiffTable` / `DiffNamespace` / `Describe` / `DescribeObject` / `Writable`，Task 6 的动作。
- Produces（Task 8、9 使用）：

  ```go
  // compareSide is one side: a table, or a whole namespace when ref.Name is "".
  type compareSide struct {
      conn   string
      driver string
      ref    db.TableRef
  }
  func (s compareSide) isTable() bool
  func (s compareSide) label() string // "local · app.users"
  func (m *Model) openDiff(src, tgt compareSide) tea.Cmd
  func (m *Model) showTab(key string, create func(id int) tab) tea.Cmd // openTab without recording a recent entry
  type diffTab struct{ … } // fields used by Task 9: src, tgt, writer, objects, picked, keys, now
  type pick struct{ obj, change int } // change -1: the object's own row
  ```

行为：
- 打开时读两边：表对表读两张表；命名空间读每张**表**（视图跳过）。一边失败时只显示是哪一边、什么错误，`<Space>r` 重试；`Ctrl-c` 取消正在进行的读取。
- 读到后计算差异：表对表只有一个 `AlterTable` 对象（名字用目标表名），没有差异时显示 "the tables have the same structure"；命名空间用 `DiffNamespace`，没有差异时显示 "no differences · N tables alike"。
- 默认勾选：写得出且不是 `Destructive` 的变更；新建表勾选，删表不勾选。写不出的（含外键）没有复选框。
- 行的样式：`+` 用 `theme.Inserted`，`-` 用 `theme.Error`，`~` 用 `theme.Changed`，当前行 `theme.Selected`。
- 一边要密码时弹出密码框，输入后重读——无论要密码的是源还是目标。

- [ ] **Step 1: fake 支持多个连接**

在 `internal/ui/fakes_test.go` 的 `fakeBackend` 结构体里加字段：

```go
	others map[string]*fakeConn // connections with their own fakeConn; the rest share conn
```

把 `fakeBackend.Conn` 换成：

```go
func (f *fakeBackend) Conn(_ context.Context, name, _ string) (db.Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lostConns > 0 {
		f.lostConns--
		return nil, fmt.Errorf("%w: connection reset by peer", db.ErrConnLost)
	}
	if f.connErr != nil {
		return nil, f.connErr
	}
	if f.needPassword {
		return nil, session.ErrPasswordRequired
	}
	if c, ok := f.others[name]; ok {
		return c, nil
	}
	return f.conn, nil
}

// addConn adds a connection with a fakeConn of its own.
func (f *fakeBackend) addConn(cfg config.Connection) *fakeConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := &fakeConn{rows: 3}
	if f.others == nil {
		f.others = map[string]*fakeConn{}
	}
	f.others[cfg.Name] = c
	f.conns = append(f.conns, cfg)
	return c
}
```

- [ ] **Step 2: 写测试**

Create `internal/ui/difftab_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

var appNS = db.Namespace{Database: "app"}

func idColumn() db.Column { return db.Column{Name: "id", Type: "bigint", PrimaryKey: true} }

func schemaOf(name string, cols ...db.Column) db.TableSchema {
	return db.TableSchema{
		Ref:     db.TableRef{Namespace: appNS, Name: name},
		Columns: cols,
		Indexes: []db.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
	}
}

func tableSide(conn, table string) compareSide {
	return compareSide{conn: conn, driver: "mysql", ref: db.TableRef{Namespace: appNS, Name: table}}
}

// usersSchemas are app.users (source) and app.users_bak (target): users_bak
// lacks email, has a narrower name and an extra column legacy.
func usersSchemas() []db.TableSchema {
	return []db.TableSchema{
		schemaOf("users", idColumn(), db.Column{Name: "name", Type: "varchar(100)"},
			db.Column{Name: "email", Type: "varchar(255)", Nullable: true}),
		schemaOf("users_bak", idColumn(), db.Column{Name: "name", Type: "varchar(50)"},
			db.Column{Name: "legacy", Type: "int", Nullable: true}),
	}
}

// openUsersDiff compares local app.users (source) with app.users_bak (target).
func openUsersDiff(t *testing.T, m *Model, b *fakeBackend) *diffTab {
	t.Helper()
	b.conn.setSchemas(usersSchemas()...)
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	d, ok := m.activeTab().(*diffTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return d
}

func wantScreen(t *testing.T, m *Model, want ...string) {
	t.Helper()
	s := screen(m)
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
}

func TestDiffTabListsChangesWithDefaults(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	wantScreen(t, m,
		"local · app.users ──▶ local · app.users_bak",
		"1 table differs", "1 destructive skipped",
		"▾ [-] ~ users_bak",
		"[ ] - column legacy  int NULL  ⚠ destructive",
		"[x] + column email  varchar(255) NULL",
		"[x] ~ column name  varchar(50) → varchar(100)",
	)
	if !strings.Contains(screen(m), "[1]diff:users_bak") {
		t.Errorf("tab title:\n%s", screen(m))
	}
}

func TestDiffTabToggle(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersDiff(t, m, b)
	press(t, m, "x") // on the table: everything
	wantScreen(t, m, "[x] - column legacy", "▾ [x] ~ users_bak")
	press(t, m, "x") // again: nothing
	wantScreen(t, m, "[ ] - column legacy", "[ ] + column email", "▾ [ ] ~ users_bak")
	press(t, m, "j", "j", "x") // email alone
	wantScreen(t, m, "[x] + column email", "▾ [-] ~ users_bak")
	if !d.picked[pick{0, 1}] || d.picked[pick{0, 0}] {
		t.Errorf("picked = %v", d.picked)
	}
}

func TestDiffTabFoldAndJump(t *testing.T) {
	m, b := newTestModel(t)
	d := openUsersDiff(t, m, b)
	press(t, m, "h")
	if strings.Contains(screen(m), "column email") {
		t.Fatalf("collapsed table still lists its changes:\n%s", screen(m))
	}
	wantScreen(t, m, "▸ [-] ~ users_bak")
	press(t, m, "l")
	wantScreen(t, m, "column email")

	press(t, m, "]", "c")
	if d.cursor != 1 {
		t.Fatalf("]c: cursor = %d, want 1", d.cursor)
	}
	press(t, m, "]", "c", "]", "c", "]", "c")
	if d.cursor != 3 || !strings.Contains(screen(m), "no more changes") {
		t.Fatalf("cursor = %d:\n%s", d.cursor, screen(m))
	}
	press(t, m, "[", "c")
	if d.cursor != 2 {
		t.Fatalf("[c: cursor = %d, want 2", d.cursor)
	}
	press(t, m, "h") // on a change: folds its table and goes up to it
	if d.cursor != 0 || !d.collapsed[0] {
		t.Fatalf("cursor = %d, collapsed = %v", d.cursor, d.collapsed)
	}
}

func TestDiffTabSwap(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "~")
	wantScreen(t, m,
		"local · app.users_bak ──▶ local · app.users",
		"[ ] - column email", "[x] + column legacy",
		"~ column name  varchar(100) → varchar(50)  ⚠ lossy",
	)
}

func TestDiffTabIdentical(t *testing.T) {
	m, b := newTestModel(t)
	same := schemaOf("users", idColumn())
	twin := schemaOf("users_bak", idColumn())
	b.conn.setSchemas(same, twin)
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	wantScreen(t, m, "the tables have the same structure")
}

func TestDiffTabShowsWhichSideFailed(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchemas()[1]) // users is missing
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	wantScreen(t, m, "✗ source local · app.users: users: not found")
	if strings.Contains(screen(m), "✗ target") {
		t.Fatalf("the target loaded fine:\n%s", screen(m))
	}
	b.conn.setSchemas(usersSchemas()...)
	press(t, m, "space", "r")
	wantScreen(t, m, "column email")
}

func TestDiffTabLoadCanBeCancelled(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchemas()...)
	load := m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak"))
	wantScreen(t, m, "loading both sides")
	press(t, m, "ctrl+c")
	drain(t, m, load) // the reads see their context cancelled
	wantScreen(t, m, "✗ source local · app.users: canceled")
}

func TestDiffTabNamespaces(t *testing.T) {
	m, b := newTestModel(t)
	staging := b.addConn(config.Connection{Name: "staging", Driver: "mysql", Env: config.EnvDev})
	b.conn.setTables([]db.Table{{Name: "users"}, {Name: "audit"}, {Name: "active", Kind: db.TableKindView}})
	b.conn.setSchemas(schemaOf("users", idColumn()), schemaOf("audit", idColumn()))
	staging.setTables([]db.Table{{Name: "users"}, {Name: "tmp"}})
	staging.setSchemas(schemaOf("users", idColumn()), schemaOf("tmp", idColumn()))
	src := compareSide{conn: "local", driver: "mysql", ref: db.TableRef{Namespace: appNS}}
	tgt := compareSide{conn: "staging", driver: "mysql", ref: db.TableRef{Namespace: appNS}}
	drain(t, m, m.openDiff(src, tgt))
	wantScreen(t, m,
		"local · app ──▶ staging · app",
		"2 tables differ · 1 identical · 1 destructive skipped",
		"[x] + audit  (new table, 1 column)",
		"[ ] - tmp  ⚠ destructive",
	)
}

func TestDiffTabAsksForPassword(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(usersSchemas()...)
	b.mu.Lock()
	b.needPassword = true
	b.mu.Unlock()
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	if _, ok := m.overlay.(*overlay.Password); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	b.mu.Lock()
	b.needPassword = false
	b.mu.Unlock()
	typeText(t, m, "secret")
	press(t, m, "enter")
	wantScreen(t, m, "column email")
}
```

说明：`typeText` 与 `selectNode` 在 `tableops_test.go` 里。标签页自己的状态行（`no more changes` 等）画在标签区底部，不是整个屏幕的最后一行，所以用 `screen(m)` 断言。`TestDiffTabAsksForPassword` 里如果密码框需要别的按键提交（看 `overlay/password.go`），照它的方式改。

- [ ] **Step 3: 运行，确认编译失败**

Run: `go test ./internal/ui -run 'TestDiffTab'`
Expected: FAIL，`m.openDiff undefined`、`undefined: compareSide`

- [ ] **Step 4: `showTab`**

在 `internal/ui/app.go` 里把 `openTab` 换成：

```go
func (m *Model) openTab(key string, rec history.Recent, create func(id int) tab) tea.Cmd {
	return tea.Batch(m.showTab(key, create), recordRecent(m.store, rec))
}

// showTab switches to the tab with key, creating it when there is none,
// without recording it among the recent tabs.
func (m *Model) showTab(key string, create func(id int) tab) tea.Cmd {
	for i, t := range m.tabs {
		if t.Key() == key {
			m.gotoTab(i)
			return nil
		}
	}
	m.nextTabID++
	t := create(m.nextTabID)
	m.tabs = append(m.tabs, t)
	m.active = len(m.tabs) - 1
	m.focus = focusTabs
	m.layout()
	return t.Init()
}

// openDiff opens, or switches to, the tab comparing src with tgt.
func (m *Model) openDiff(src, tgt compareSide) tea.Cmd {
	return m.showTab(diffTabKey(src, tgt), func(id int) tab {
		t := newDiffTab(id, m.backend, src, tgt)
		t.keys = m.keys
		return t
	})
}
```

- [ ] **Step 5: 密码重试认得 Diff 标签页的两边**

在 `internal/ui/app.go` 的 `askPassword` 的 switch 里加一个分支：

```go
	case diffLoadedMsg:
		m.retryNode, m.retryTab = nil, msg.tabID
```

在 `passwordSubmitted` 里，把

```go
		if conn, _ := m.tabByID(tabID).Target(); conn == msg.Conn {
```

换成

```go
		if usesConn(m.tabByID(tabID), msg.Conn) {
```

并在 `passwordSubmitted` 后面新增：

```go
// connUser is a tab that works on more connections than its Target.
type connUser interface{ usesConn(conn string) bool }

// usesConn reports whether t works on conn.
func usesConn(t tab, conn string) bool {
	if u, ok := t.(connUser); ok {
		return u.usesConn(conn)
	}
	c, _ := t.Target()
	return c == conn
}
```

- [ ] **Step 6: Diff 标签页**

Create `internal/ui/difftab.go`:

```go
package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/schemadiff"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// compareTimeout bounds reading both sides of a comparison; a namespace
// takes a few queries per table.
const compareTimeout = 5 * time.Minute

// compareSide is one side of a comparison: a table, or a whole namespace
// when ref.Name is empty.
type compareSide struct {
	conn   string
	driver string
	ref    db.TableRef
}

func (s compareSide) isTable() bool { return s.ref.Name != "" }

// label names the side for people: "local · app.users".
func (s compareSide) label() string {
	var parts []string
	for _, p := range []string{s.ref.Database, s.ref.Schema, s.ref.Name} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return s.conn + " · " + strings.Join(parts, ".")
}

// name is the side's last name: the table, the schema or the database.
func (s compareSide) name() string {
	for _, p := range []string{s.ref.Name, s.ref.Schema, s.ref.Database} {
		if p != "" {
			return p
		}
	}
	return s.conn
}

func diffTabKey(src, tgt compareSide) string {
	return "diff\x00" + tableKey(src.conn, src.ref) + "\x00" + tableKey(tgt.conn, tgt.ref)
}

type diffLoadedMsg struct {
	tabID            int
	gen              int
	srcConn, tgtConn string
	src, tgt         []db.TableSchema
	writer           db.DDLWriter // the target's
	srcErr, tgtErr   error
}

func (m diffLoadedMsg) TabID() int { return m.tabID }

func (m diffLoadedMsg) failure() (string, error) {
	if m.srcErr != nil {
		return m.srcConn, m.srcErr
	}
	return m.tgtConn, m.tgtErr
}

// readSide reads the schema of the side's table, or of every table (not
// view) of its namespace, and the DDL writer of its connection.
func readSide(ctx context.Context, b Backend, s compareSide) ([]db.TableSchema, db.DDLWriter, error) {
	var out []db.TableSchema
	var w db.DDLWriter
	err := withConn(ctx, b, s.conn, s.ref.Database, func(c db.Conn) error {
		out, w = nil, c.Dialect().DDL()
		m := c.Meta()
		if s.isTable() {
			ts, err := m.TableSchema(ctx, s.ref)
			out = append(out, ts)
			return err
		}
		tables, err := m.Tables(ctx, s.ref.Namespace)
		if err != nil {
			return err
		}
		for _, t := range tables {
			if t.Kind != db.TableKindTable {
				continue
			}
			ts, err := m.TableSchema(ctx, db.TableRef{Namespace: s.ref.Namespace, Name: t.Name})
			if err != nil {
				return err
			}
			out = append(out, ts)
		}
		return nil
	})
	return out, w, err
}

// pick addresses a checkbox: change -1 is the object's own row.
type pick struct{ obj, change int }

// diffRow is one line of the list; change -1 is the object's own row.
type diffRow struct{ obj, change int }

// diffTab compares the structure of two tables or two namespaces and
// writes the script that makes the target like the source.
type diffTab struct {
	id      int
	key     string
	keys    *keymap.Keymap
	backend Backend
	src     compareSide
	tgt     compareSide
	now     func() time.Time

	loading   bool
	cancel    context.CancelFunc
	gen       int
	srcErr    string
	tgtErr    string
	srcTables []db.TableSchema
	tgtTables []db.TableSchema
	writer    db.DDLWriter

	objects   []schemadiff.ObjectDiff
	identical int
	writable  map[pick]bool
	picked    map[pick]bool
	collapsed map[int]bool

	cursor, offset int
	width, height  int
	status         string
	statusErr      bool
}

func newDiffTab(id int, b Backend, src, tgt compareSide) *diffTab {
	return &diffTab{
		id: id, key: diffTabKey(src, tgt), backend: b, src: src, tgt: tgt,
		now: time.Now, keys: keymap.Default(),
	}
}

func (t *diffTab) ID() int                       { return t.id }
func (t *diffTab) Title() string                 { return "diff:" + t.tgt.name() }
func (t *diffTab) Key() string                   { return t.key }
func (t *diffTab) Context() keymap.Context       { return keymap.Diff }
func (t *diffTab) Target() (string, db.TableRef) { return t.tgt.conn, t.tgt.ref }
func (t *diffTab) Init() tea.Cmd                 { return t.load() }
func (t *diffTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }

func (t *diffTab) usesConn(conn string) bool { return conn == t.src.conn || conn == t.tgt.conn }

func (t *diffTab) retryAfterPassword() tea.Cmd { return t.load() }

func (t *diffTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

// load reads both sides again; a load still running is cancelled.
func (t *diffTab) load() tea.Cmd {
	if t.cancel != nil {
		t.cancel()
	}
	t.gen++
	t.loading, t.srcErr, t.tgtErr = true, "", ""
	ctx, cancel := context.WithTimeout(context.Background(), compareTimeout)
	t.cancel = cancel
	id, gen, b, src, tgt := t.id, t.gen, t.backend, t.src, t.tgt
	return func() tea.Msg {
		defer cancel()
		msg := diffLoadedMsg{tabID: id, gen: gen, srcConn: src.conn, tgtConn: tgt.conn}
		msg.src, _, msg.srcErr = readSide(ctx, b, src)
		msg.tgt, msg.writer, msg.tgtErr = readSide(ctx, b, tgt)
		return msg
	}
}

func (t *diffTab) cancelRun() tea.Cmd {
	if t.loading && t.cancel != nil {
		t.cancel()
		return nil
	}
	t.setStatus("nothing is loading", false)
	return nil
}

func sideError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return err.Error()
}

func (t *diffTab) Update(msg tea.Msg) tea.Cmd {
	m, ok := msg.(diffLoadedMsg)
	if !ok || m.gen != t.gen {
		return nil
	}
	t.loading, t.cancel = false, nil
	t.srcErr, t.tgtErr = sideError(m.srcErr), sideError(m.tgtErr)
	if t.srcErr != "" || t.tgtErr != "" {
		t.objects = nil
		return nil
	}
	t.srcTables, t.tgtTables, t.writer = m.src, m.tgt, m.writer
	t.compute()
	return nil
}

// compute diffs the loaded sides and resets the choices to their defaults:
// what can be scripted and deletes nothing is picked.
func (t *diffTab) compute() {
	t.objects, t.identical = nil, 0
	if t.src.isTable() {
		src, tgt := &t.srcTables[0], &t.tgtTables[0]
		if ch := schemadiff.DiffTable(*tgt, *src, nil); len(ch) > 0 {
			t.objects = []schemadiff.ObjectDiff{{Kind: schemadiff.AlterTable, Name: tgt.Ref.Name, Source: src, Target: tgt, Changes: ch}}
		} else {
			t.identical = 1
		}
	} else {
		t.objects, t.identical = schemadiff.DiffNamespace(t.tgtTables, t.srcTables)
	}
	t.writable, t.picked, t.collapsed = map[pick]bool{}, map[pick]bool{}, map[int]bool{}
	for oi, o := range t.objects {
		if o.Kind != schemadiff.AlterTable {
			t.writable[pick{oi, -1}] = true
			t.picked[pick{oi, -1}] = o.Kind == schemadiff.CreateTable
			continue
		}
		for ci, c := range o.Changes {
			w := schemadiff.Writable(t.writer, o.Target.Ref, c)
			t.writable[pick{oi, ci}] = w
			t.picked[pick{oi, ci}] = w && c.Risk&db.Destructive == 0
		}
	}
	t.cursor, t.offset = 0, 0
}

func (t *diffTab) rows() []diffRow {
	var out []diffRow
	for oi, o := range t.objects {
		out = append(out, diffRow{oi, -1})
		if o.Kind == schemadiff.AlterTable && !t.collapsed[oi] {
			for ci := range o.Changes {
				out = append(out, diffRow{oi, ci})
			}
		}
	}
	return out
}

func (t *diffTab) current() (diffRow, bool) {
	rows := t.rows()
	if len(rows) == 0 {
		return diffRow{}, false
	}
	return rows[textutil.Clamp(t.cursor, 0, len(rows)-1)], true
}

func (t *diffTab) listHeight() int { return max(t.height-2, 1) }

func (t *diffTab) move(delta int) {
	t.cursor = textutil.Clamp(t.cursor+delta, 0, max(len(t.rows())-1, 0))
}

func (t *diffTab) Handle(a keymap.Action) tea.Cmd {
	t.setStatus("", false)
	switch a {
	case keymap.Down:
		t.move(1)
	case keymap.Up:
		t.move(-1)
	case keymap.Top:
		t.cursor = 0
	case keymap.Bottom:
		t.move(len(t.rows()))
	case keymap.HalfPageDown:
		t.move(max(t.listHeight()/2, 1))
	case keymap.HalfPageUp:
		t.move(-max(t.listHeight()/2, 1))
	case keymap.Expand:
		t.setCollapsed(false)
	case keymap.Collapse:
		t.setCollapsed(true)
	case keymap.NextChange:
		t.jump(1)
	case keymap.PrevChange:
		t.jump(-1)
	case keymap.ToggleChange:
		t.toggle()
	case keymap.SwapSides:
		t.swap()
	case keymap.Refresh:
		return t.load()
	}
	return nil
}

// setCollapsed folds or unfolds the table under the cursor; folding from a
// change moves the cursor up to its table.
func (t *diffTab) setCollapsed(collapse bool) {
	r, ok := t.current()
	if !ok || t.objects[r.obj].Kind != schemadiff.AlterTable {
		return
	}
	t.collapsed[r.obj] = collapse
	if collapse {
		t.cursor = slices.Index(t.rows(), diffRow{r.obj, -1})
	}
}

// jump moves to the next (dir 1) or previous (dir -1) row that is a change:
// a change, a table to create or drop, or a folded table.
func (t *diffTab) jump(dir int) {
	rows := t.rows()
	for i := t.cursor + dir; i >= 0 && i < len(rows); i += dir {
		r := rows[i]
		if r.change >= 0 || t.objects[r.obj].Kind != schemadiff.AlterTable || t.collapsed[r.obj] {
			t.cursor = i
			return
		}
	}
	t.setStatus("no more changes", false)
}

// toggle flips the checkbox under the cursor. On an altered table's row it
// picks all of its changes, or none when all are picked already.
func (t *diffTab) toggle() {
	r, ok := t.current()
	if !ok {
		return
	}
	o := t.objects[r.obj]
	if r.change >= 0 || o.Kind != schemadiff.AlterTable {
		p := pick(r)
		if !t.writable[p] {
			if o.Changes[r.change].Kind == db.ForeignKeyDiff {
				t.setStatus("foreign keys are compared, not synced", true)
			} else {
				t.setStatus("this change cannot be scripted · make it by hand", true)
			}
			return
		}
		t.picked[p] = !t.picked[p]
		return
	}
	all := true
	for ci := range o.Changes {
		if p := (pick{r.obj, ci}); t.writable[p] && !t.picked[p] {
			all = false
		}
	}
	for ci := range o.Changes {
		if p := (pick{r.obj, ci}); t.writable[p] {
			t.picked[p] = !all
		}
	}
}

// swap makes the target the source and computes the diff again.
func (t *diffTab) swap() {
	if t.loading || t.srcErr != "" || t.tgtErr != "" || t.writer == nil {
		t.setStatus("both sides have to be loaded first", true)
		return
	}
	t.src, t.tgt = t.tgt, t.src
	t.srcTables, t.tgtTables = t.tgtTables, t.srcTables
	t.compute()
	t.setStatus("now "+t.src.label()+" ──▶ "+t.tgt.label(), false)
}

// skippedDestructive counts the deletions left out of the script.
func (t *diffTab) skippedDestructive() int {
	n := 0
	for oi, o := range t.objects {
		if o.Kind == schemadiff.DropTable && !t.picked[pick{oi, -1}] {
			n++
		}
		for ci, c := range o.Changes {
			if p := (pick{oi, ci}); c.Risk&db.Destructive != 0 && t.writable[p] && !t.picked[p] {
				n++
			}
		}
	}
	return n
}

// box is a row's checkbox: [x], [ ], [-] for a partly picked table, or
// blank when nothing on the row can be scripted.
func (t *diffTab) box(r diffRow) string {
	o := t.objects[r.obj]
	if r.change >= 0 || o.Kind != schemadiff.AlterTable {
		switch p := pick(r); {
		case !t.writable[p]:
			return "   "
		case t.picked[p]:
			return "[x]"
		}
		return "[ ]"
	}
	var writable, picked int
	for ci := range o.Changes {
		if p := (pick{r.obj, ci}); t.writable[p] {
			writable++
			if t.picked[p] {
				picked++
			}
		}
	}
	switch {
	case writable == 0:
		return "   "
	case picked == writable:
		return "[x]"
	case picked == 0:
		return "[ ]"
	}
	return "[-]"
}

// rowText is a row's text and the style its sign (+, -, ~) takes.
func (t *diffTab) rowText(r diffRow) (string, func(...string) string) {
	o := t.objects[r.obj]
	var prefix, desc, note string
	if r.change < 0 {
		fold := " "
		if o.Kind == schemadiff.AlterTable {
			fold = "▾"
			if t.collapsed[r.obj] {
				fold = "▸"
			}
		}
		prefix, desc = " "+fold+" "+t.box(r)+" ", schemadiff.DescribeObject(o)
		if o.Kind == schemadiff.DropTable {
			note = "  ⚠ destructive"
		}
	} else {
		c := o.Changes[r.change]
		prefix, desc = "     "+t.box(r)+" ", schemadiff.Describe(c)
		switch {
		case c.Kind == db.ForeignKeyDiff:
			note = "  (not synced)"
		case !t.writable[pick(r)]:
			note = "  ⚠ manual"
		case c.Risk&db.Destructive != 0:
			note = "  ⚠ destructive"
		case c.Risk&db.Lossy != 0:
			note = "  ⚠ lossy"
		}
	}
	style := theme.Changed.Render
	switch {
	case strings.HasPrefix(desc, "+"):
		style = theme.Inserted.Render
	case strings.HasPrefix(desc, "-"):
		style = theme.Error.Render
	}
	return textutil.Sanitize(prefix + desc + note), style
}

func (t *diffTab) header() string {
	head := theme.Title.Render(textutil.Sanitize(t.src.label() + " ──▶ " + t.tgt.label()))
	if cfg, _ := t.backend.Connection(t.tgt.conn); cfg.Env == config.EnvProd {
		head += " " + theme.Prod.Render("[PROD]")
	}
	if t.loading || t.srcErr != "" || t.tgtErr != "" || len(t.objects) == 0 {
		return head
	}
	parts := []string{fmt.Sprintf("%d tables differ", len(t.objects))}
	if len(t.objects) == 1 {
		parts[0] = "1 table differs"
	}
	if t.identical > 0 {
		parts = append(parts, fmt.Sprintf("%d identical", t.identical))
	}
	if n := t.skippedDestructive(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d destructive skipped", n))
	}
	return head + "   " + theme.Muted.Render(strings.Join(parts, " · "))
}

func (t *diffTab) body() []string {
	var out []string
	switch {
	case t.srcErr != "" || t.tgtErr != "":
		if t.srcErr != "" {
			out = append(out, theme.Error.Render(textutil.Sanitize("✗ source "+t.src.label()+": "+t.srcErr)))
		}
		if t.tgtErr != "" {
			out = append(out, theme.Error.Render(textutil.Sanitize("✗ target "+t.tgt.label()+": "+t.tgtErr)))
		}
		if h := keyHint(t.keys, keymap.Global, keymap.Refresh, "reloads"); h != "" {
			out = append(out, theme.Muted.Render(h))
		}
	case t.loading:
		out = append(out, theme.Muted.Render(joinHints("loading both sides…", keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels"))))
	case len(t.objects) == 0 && t.src.isTable():
		out = append(out, theme.Muted.Render("the tables have the same structure"))
	case len(t.objects) == 0:
		out = append(out, theme.Muted.Render(fmt.Sprintf("no differences · %d tables alike", t.identical)))
	default:
		out = t.listLines()
	}
	return out
}

func (t *diffTab) listLines() []string {
	rows := t.rows()
	h := t.listHeight()
	t.cursor = textutil.Clamp(t.cursor, 0, len(rows)-1)
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = textutil.Clamp(t.offset, 0, max(len(rows)-h, 0))
	var out []string
	for i := t.offset; i < min(t.offset+h, len(rows)); i++ {
		text, style := t.rowText(rows[i])
		text = textutil.Fit(text, t.width)
		if i == t.cursor {
			out = append(out, theme.Selected.Render(text))
		} else {
			out = append(out, style(text))
		}
	}
	return out
}

func (t *diffTab) statusLine() string {
	if t.status != "" {
		if t.statusErr {
			return theme.Error.Render(t.status)
		}
		return t.status
	}
	return theme.Muted.Render(joinHints(
		keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "include/skip"),
		keyHint(t.keys, keymap.Diff, keymap.NextChange, "next change"),
		keyHint(t.keys, keymap.Diff, keymap.SwapSides, "swap"),
		keyHint(t.keys, keymap.Diff, keymap.WriteScript, "script to console"),
		keyHint(t.keys, keymap.Global, keymap.Refresh, "reload"),
	))
}

func (t *diffTab) View(bool) string {
	lines := append([]string{textutil.Fit(t.header(), t.width)}, t.body()...)
	for len(lines) < t.height-1 {
		lines = append(lines, "")
	}
	lines = lines[:max(t.height-1, 1)]
	return strings.Join(append(lines, textutil.Fit(t.statusLine(), t.width)), "\n")
}
```

注意：
- `pick(r)` 把 `diffRow` 转成 `pick`，两个类型的字段完全相同，Go 允许直接转换。
- 状态栏的 `t.status` 由 `Handle` 每次清空；`swap` 等写入的提示保留到下一次按键。
- 如果 `textutil.Fit` 对带样式的文本截断不正确（header 里有 ANSI），换成对纯文本先 `Fit` 再套样式。

- [ ] **Step 7: 运行测试，确认通过**

Run: `go test -race ./internal/ui -run 'TestDiffTab'`
Expected: PASS

常见问题：
- `TestDiffTabLoadCanBeCancelled` 里 `ctrl+c` 没有生效：确认 `openDiff` 之后焦点在标签区（`showTab` 会设置 `focusTabs`），`dispatch` 的 `Cancel` 分支用 `canceler` 接口调到 `diffTab.cancelRun`。
- 屏幕里找不到 `local · app.users ──▶ …`：标签区宽度是 100 列减去树的宽度，若被截断，测试改为在 `newTestModel` 之后发一个更宽的 `tea.WindowSizeMsg{Width: 160, Height: 30}`。

- [ ] **Step 8: 全量检查并提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./...`
Expected: 无输出、通过

```bash
git add internal/ui
git commit -m "feat(ui): Diff 标签页列出结构差异，可勾选、折叠、交换两边"
```

---
### Task 8: 树上的 `m` / `C` 与选源 picker

**Files:**
- Create: `internal/ui/compare.go`
- Modify: `internal/ui/panes/tree/tree.go`（`MarkKey`、`SetMark`、`◆`）
- Modify: `internal/ui/app.go`（`compareMark` 字段、`treeAction` 两个分支、`compareSourceMsg`）
- Modify: `internal/ui/nodemenu.go`（菜单加两项）
- Test: `internal/ui/compare_test.go`、`internal/ui/nodemenu_test.go`

**Interfaces:**
- Consumes: Task 6 的 `MarkCompare` / `Compare`，Task 7 的 `compareSide`、`openDiff`。
- Produces:

  ```go
  // package tree
  type MarkKey struct {
      Conn string
      Kind Kind
      NS   db.Namespace
      Name string
  }
  func (n *Node) MarkKey() MarkKey
  func (m *Model) SetMark(k *MarkKey) // nil clears

  // package ui
  func (m *Model) compareSideOf(n *tree.Node) (compareSide, string) // the string says why n cannot be compared
  func compareProblem(src, tgt compareSide) string                 // "" when src and tgt can be compared
  ```

规则（spec §5.1）：
- 能做对比的节点：表；MySQL 的库；PG 的 schema。视图、连接、PG 的库都不行，状态栏说明原因。
- `m` 标记当前节点为对比源，节点前显示 `◆`；在已标记的节点上再按一次取消。
- `C`：有标记时，标记为源、当前节点为目标；两边必须同类（表对表、命名空间对命名空间）、同引擎、不是同一个。开始对比后清除标记。没有标记时弹出 picker，候选来自已加载的元数据（`m.catalogs.Snapshots("")`）里同引擎连接上的同类对象，排除目标自己；没有候选时状态栏提示用 `m` 标记源，并在后台加载目标连接的元数据，下一次就有候选了。

- [ ] **Step 1: 写测试**

Create `internal/ui/compare_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// withBakTable makes the fake app database list users, users_bak and the view active.
func withBakTable(b *fakeBackend) {
	b.conn.setTables([]db.Table{{Name: "users"}, {Name: "users_bak"}, {Name: "active", Kind: db.TableKindView}})
	b.conn.setSchemas(usersSchemas()...)
}

func TestMarkThenCompareOpensDiff(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "m")
	wantScreen(t, m, "◆ users")
	if !strings.Contains(statusLine(m), "compare source: local · app.users") {
		t.Fatalf("status: %s", statusLine(m))
	}
	selectNode(t, m, "local", "app", "users_bak")
	press(t, m, "C")
	d, ok := m.activeTab().(*diffTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	if d.src.ref.Name != "users" || d.tgt.ref.Name != "users_bak" {
		t.Fatalf("src %+v, tgt %+v", d.src, d.tgt)
	}
	if strings.Contains(screen(m), "◆") || m.compareMark != nil {
		t.Fatal("the mark stays after comparing")
	}
	wantScreen(t, m, "column email")
}

func TestMarkTwiceClears(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "m", "m")
	if strings.Contains(screen(m), "◆") || m.compareMark != nil {
		t.Fatalf("mark kept:\n%s", screen(m))
	}
	if !strings.Contains(statusLine(m), "compare mark cleared") {
		t.Fatalf("status: %s", statusLine(m))
	}
}

func TestCompareRejects(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	selectNode(t, m, "local", "app", "active")
	press(t, m, "m")
	if !strings.Contains(statusLine(m), "views are not compared") {
		t.Fatalf("view: %s", statusLine(m))
	}
	selectNode(t, m, "local", "app", "users")
	press(t, m, "m", "C")
	if !strings.Contains(statusLine(m), "the source and the target are the same") {
		t.Fatalf("same: %s", statusLine(m))
	}
	selectNode(t, m, "local", "app")
	press(t, m, "C")
	if !strings.Contains(statusLine(m), "compare a table with a table") {
		t.Fatalf("kinds: %s", statusLine(m))
	}
	if len(m.tabs) != 0 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
	selectNode(t, m, "prod", "app")
	press(t, m, "m")
	if !strings.Contains(statusLine(m), "select a schema") {
		t.Fatalf("PostgreSQL database: %s", statusLine(m))
	}
}

func TestCompareProblemEngines(t *testing.T) {
	my := compareSide{conn: "local", driver: "mysql", ref: db.TableRef{Namespace: appNS, Name: "users"}}
	pg := compareSide{conn: "prod", driver: "postgres", ref: db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "users"}}
	if p := compareProblem(my, pg); p != "cannot compare mysql with postgres" {
		t.Fatalf("problem = %q", p)
	}
	other := my
	other.conn = "staging"
	if p := compareProblem(my, other); p != "" {
		t.Fatalf("same table on two connections: %q", p)
	}
}

func TestComparePicksSourceWithoutMark(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	drain(t, m, m.ensureMeta("local", "app")) // the catalog lists active (view), order and users
	selectNode(t, m, "local", "app", "users_bak")
	press(t, m, "C")
	if _, ok := m.overlay.(*overlay.Picker); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	s := screen(m)
	if !strings.Contains(s, "local · app.order") || !strings.Contains(s, "local · app.users") || strings.Contains(s, "app.active") {
		t.Fatalf("picker:\n%s", s)
	}
	typeText(t, m, "app.users")
	press(t, m, "enter")
	d, ok := m.activeTab().(*diffTab)
	if !ok || d.src.ref.Name != "users" || d.tgt.ref.Name != "users_bak" {
		t.Fatalf("active tab = %+v", m.activeTab())
	}
}

func TestCompareWithoutCandidates(t *testing.T) {
	m, b := newTestModel(t)
	withBakTable(b)
	selectNode(t, m, "local", "app", "users_bak")
	if n := len(m.catalogs.Snapshots("")); n != 0 {
		t.Fatalf("a catalog is loaded already (%d); the test needs none", n)
	}
	press(t, m, "C")
	if m.overlay != nil || !strings.Contains(statusLine(m), "nothing to compare with") {
		t.Fatalf("overlay %T, status %s", m.overlay, statusLine(m))
	}
}
```

在 `internal/ui/nodemenu_test.go` 末尾追加：

```go
func TestActionMenuOffersCompare(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	wantScreen(t, m, "Mark for compare", "Compare with the marked one")
	press(t, m, "esc")
	selectNode(t, m, "local", "app")
	press(t, m, "a")
	wantScreen(t, m, "Mark for compare")
	press(t, m, "esc")
	selectNode(t, m, "prod", "app") // a PostgreSQL database is not a namespace
	press(t, m, "a")
	if strings.Contains(screen(m), "Mark for compare") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/ui -run 'TestMark|TestCompare|TestActionMenuOffersCompare'`
Expected: FAIL（`m.compareMark undefined`、`undefined: compareProblem`）

- [ ] **Step 3: 树上的标记**

在 `internal/ui/panes/tree/tree.go` 里：

`Node` 的方法后面新增：

```go
// MarkKey identifies a node across reloads, for the compare mark.
type MarkKey struct {
	Conn string
	Kind Kind
	NS   db.Namespace
	Name string
}

func (n *Node) MarkKey() MarkKey { return MarkKey{Conn: n.Conn, Kind: n.Kind, NS: n.NS, Name: n.Name} }
```

`Model` 结构体加字段：

```go
	mark     *MarkKey // the compare source, drawn with ◆
```

`SetBroken` 后面新增：

```go
// SetMark draws ◆ before the node k names; nil clears the mark.
func (m *Model) SetMark(k *MarkKey) { m.mark = k }
```

`View` 里，把

```go
		label := strings.Repeat("  ", rows[i].depth) + icon(n) + " " + textutil.Sanitize(n.Name)
```

换成

```go
		name := textutil.Sanitize(n.Name)
		if m.mark != nil && n.MarkKey() == *m.mark {
			name = "◆ " + name
		}
		label := strings.Repeat("  ", rows[i].depth) + icon(n) + " " + name
```

- [ ] **Step 4: `compare.go`**

Create `internal/ui/compare.go`:

```go
package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// compareSourceMsg is the source picked for a comparison.
type compareSourceMsg struct{ src, tgt compareSide }

// compareSideOf is node n as a side of a comparison, or why it cannot be
// one: tables, MySQL databases and PostgreSQL schemas compare.
func (m *Model) compareSideOf(n *tree.Node) (compareSide, string) {
	cfg, _ := m.backend.Connection(n.Conn)
	s := compareSide{conn: n.Conn, driver: cfg.Driver}
	switch {
	case n.Kind == tree.KindTable:
		s.ref = n.TableRef()
	case n.Kind == tree.KindView:
		return s, "views are not compared"
	case n.Kind == tree.KindSchema, n.Kind == tree.KindDatabase && cfg.Driver != "postgres":
		s.ref = db.TableRef{Namespace: n.NS}
	case n.Kind == tree.KindDatabase:
		return s, "PostgreSQL compares schemas · select a schema"
	default:
		return s, "select a table, a schema or a database to compare"
	}
	return s, ""
}

// compareProblem says why src cannot be compared with tgt ("" when it can).
func compareProblem(src, tgt compareSide) string {
	switch {
	case src.isTable() != tgt.isTable():
		return "compare a table with a table, a schema or a database with another"
	case src.driver != tgt.driver:
		return "cannot compare " + src.driver + " with " + tgt.driver
	case src.conn == tgt.conn && src.ref == tgt.ref:
		return "the source and the target are the same"
	}
	return ""
}

func (m *Model) clearCompareMark() {
	m.compareMark = nil
	m.tree.SetMark(nil)
}

// markCompare marks the selected node as the compare source, or clears the
// mark when it is on that node already.
func (m *Model) markCompare() {
	n := m.tree.Selected()
	if n == nil {
		return
	}
	s, problem := m.compareSideOf(n)
	if problem != "" {
		m.setStatus(problem, true)
		return
	}
	if m.compareMark != nil && *m.compareMark == s {
		m.clearCompareMark()
		m.setStatus("compare mark cleared", false)
		return
	}
	m.compareMark = &s
	k := n.MarkKey()
	m.tree.SetMark(&k)
	m.setStatus(joinHints("compare source: "+s.label(), keyHint(m.keys, keymap.Tree, keymap.Compare, "on the target compares")), false)
}

// compare compares the marked node with the selected one; without a mark
// it asks for the source.
func (m *Model) compare() tea.Cmd {
	n := m.tree.Selected()
	if n == nil {
		return nil
	}
	tgt, problem := m.compareSideOf(n)
	if problem != "" {
		m.setStatus(problem, true)
		return nil
	}
	if m.compareMark == nil {
		return m.pickCompareSource(tgt)
	}
	return m.startCompare(*m.compareMark, tgt)
}

func (m *Model) startCompare(src, tgt compareSide) tea.Cmd {
	if p := compareProblem(src, tgt); p != "" {
		m.setStatus(p, true)
		return nil
	}
	m.clearCompareMark()
	return m.openDiff(src, tgt)
}

// pickCompareSource offers the sides the loaded catalogs know. It loads
// the target's catalog meanwhile, so that the next try has more to offer.
func (m *Model) pickCompareSource(tgt compareSide) tea.Cmd {
	load := m.ensureMeta(tgt.conn, tgt.ref.Database)
	cands := m.compareCandidates(tgt)
	if len(cands) == 0 {
		m.setStatus(joinHints("nothing to compare with", keyHint(m.keys, keymap.Tree, keymap.MarkCompare, "marks the source")), true)
		return load
	}
	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = c.label()
	}
	m.overlay = overlay.NewPicker("Compare "+tgt.label()+" with", labels, func(i int) tea.Msg {
		return compareSourceMsg{src: cands[i], tgt: tgt}
	})
	return load
}

// compareCandidates lists what tgt can be compared with: the tables (for a
// table) or namespaces (for a namespace) of the loaded catalogs, on
// connections of tgt's engine.
func (m *Model) compareCandidates(tgt compareSide) []compareSide {
	var out []compareSide
	for _, snap := range m.catalogs.Snapshots("") {
		cfg, ok := m.backend.Connection(snap.Key.Conn)
		if !ok || cfg.Driver != tgt.driver {
			continue
		}
		add := func(ref db.TableRef) {
			if s := (compareSide{conn: snap.Key.Conn, driver: cfg.Driver, ref: ref}); compareProblem(s, tgt) == "" {
				out = append(out, s)
			}
		}
		if tgt.isTable() {
			for _, t := range snap.Catalog.Tables {
				if t.Kind == db.TableKindTable {
					add(db.TableRef{Namespace: t.Namespace, Name: t.Name})
				}
			}
			continue
		}
		for _, ns := range snap.Catalog.Namespaces {
			add(db.TableRef{Namespace: ns})
		}
	}
	return out
}
```

- [ ] **Step 5: 接到 `Model`**

在 `internal/ui/app.go` 的 `Model` 结构体里（`tree` 字段附近）加：

```go
	compareMark *compareSide // the compare source marked with m
```

`treeAction` 的 switch 里，`case keymap.NodeActions:` 之前加：

```go
	case keymap.MarkCompare:
		m.markCompare()
	case keymap.Compare:
		return m.compare()
```

`Update` 的 switch 里，`case menuPickedMsg:` 之前加：

```go
	case compareSourceMsg:
		return m, m.startCompare(msg.src, msg.tgt)
```

- [ ] **Step 6: 动作菜单**

在 `internal/ui/nodemenu.go` 的 `nodeMenu` 里：

把 `case tree.KindDatabase, tree.KindSchema:` 分支换成：

```go
	case tree.KindDatabase, tree.KindSchema:
		entries = []entry{
			{keymap.OpenConsole, "Open console"},
			{keymap.Refresh, "Refresh"},
		}
		if _, problem := m.compareSideOf(n); problem == "" {
			entries = append(entries, entry{keymap.MarkCompare, "Mark for compare"}, entry{keymap.Compare, "Compare with the marked one"})
		}
```

表 / 视图分支里，把

```go
		if n.Kind == tree.KindTable {
			entries = append(entries, entry{keymap.TruncateTable, "Truncate table"})
		}
```

换成

```go
		if n.Kind == tree.KindTable {
			entries = append(entries,
				entry{keymap.MarkCompare, "Mark for compare"},
				entry{keymap.Compare, "Compare with the marked one"},
				entry{keymap.TruncateTable, "Truncate table"},
			)
		}
```

- [ ] **Step 7: 运行测试，确认通过**

Run: `go test -race ./internal/ui/...`
Expected: PASS

若 `TestActionMenuOnTable` 因为 Truncate 前多了两项而选错条目：它用 `slices.IndexFunc` 找下标，不受影响；若有别的测试按固定行数移动光标，改成同样的写法。

- [ ] **Step 8: 全量检查并提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./...`

```bash
git add internal/ui
git commit -m "feat(ui): 树上 m 标记对比源、C 与当前节点对比"
```

---

### Task 9: `<Space>w` 把同步脚本写进新的 console

**Files:**
- Modify: `internal/ui/difftab.go`（`WriteScript`）
- Modify: `internal/ui/console.go`（脚本 console：自己的 key、不存草稿）
- Modify: `internal/ui/app.go`（`scriptConsoleMsg`、`openScriptConsole`）
- Test: `internal/ui/script_test.go`

**Interfaces:**
- Consumes: Task 5 `schemadiff.Script`，Task 7 `diffTab`、`showTab`。
- Produces:

  ```go
  type scriptConsoleMsg struct {
      conn string
      ns   db.Namespace
      text string
  }
  func (m *Model) openScriptConsole(conn string, ns db.Namespace, text string) tea.Cmd
  // consoleTab gains scratchKey string: a script console's own tab key; it keeps no draft.
  ```

脚本格式：

```
-- sqlvim schema sync
-- source: local · app.users
-- target: local · app.users_bak
-- generated: 2026-10-15 14:03:00
-- These statements change the target to match the source. Review them before running.

ALTER TABLE …;

-- Not scripted, change by hand:
--   users_bak: ~ column total  not generated → AS (id * 2)
```

每按一次 `<Space>w` 开一个新的 console（标题 `sync:<database>`），不复用、不覆盖该连接平时的 console，也不保存草稿。没有可执行的语句时不开 console，状态行说明原因。

- [ ] **Step 1: 写测试**

Create `internal/ui/script_test.go`:

```go
package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

func scriptConsole(t *testing.T, m *Model) *consoleTab {
	t.Helper()
	c, ok := m.activeTab().(*consoleTab)
	if !ok {
		t.Fatalf("active tab = %T", m.activeTab())
	}
	return c
}

func TestWriteScriptOpensConsoleOnTarget(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "space", "w")
	c := scriptConsole(t, m)
	if c.conn != "local" || c.ns != appNS || c.Title() != "sync:app" {
		t.Fatalf("console %s on %s %+v", c.Title(), c.conn, c.ns)
	}
	text := c.editor.Value()
	for _, want := range []string{
		"-- source: local · app.users\n",
		"-- target: local · app.users_bak\n",
		"ALTER TABLE `app`.`users_bak` AddColumn email;",
		"ALTER TABLE `app`.`users_bak` AlterColumn name;",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "DropColumn legacy") {
		t.Fatalf("the unpicked drop is in the script:\n%s", text)
	}
	if c.Key() == consoleTabKey("local", appNS) {
		t.Fatal("the script console took the connection's usual console key")
	}
}

func TestWriteScriptOpensANewConsoleEachTime(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "space", "w")
	m.gotoTab(0)
	press(t, m, "space", "w")
	if len(m.tabs) != 3 {
		t.Fatalf("tabs = %d, want the diff and two consoles", len(m.tabs))
	}
	if m.tabs[1].Key() == m.tabs[2].Key() {
		t.Fatal("both script consoles have one key")
	}
}

func TestWriteScriptNothingPicked(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "x", "x") // all, then none
	press(t, m, "space", "w")
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
	wantScreen(t, m, "nothing picked")
}

func TestManualChangesStayInTheScriptAsComments(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setSchemas(
		schemaOf("users", idColumn(), db.Column{Name: "email", Type: "text", Nullable: true},
			db.Column{Name: "total", Type: "int", Nullable: true, Generated: "id * 2"}),
		schemaOf("users_bak", idColumn(), db.Column{Name: "total", Type: "int", Nullable: true}),
	)
	drain(t, m, m.openDiff(tableSide("local", "users"), tableSide("local", "users_bak")))
	wantScreen(t, m, "~ column total  not generated → AS (id * 2)  ⚠ manual")
	press(t, m, "j", "j", "x") // on total
	wantScreen(t, m, "this change cannot be scripted")
	press(t, m, "space", "w")
	text := scriptConsole(t, m).editor.Value()
	for _, want := range []string{
		"AddColumn email;",
		"-- Not scripted, change by hand:\n--   users_bak: ~ column total  not generated → AS (id * 2)\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
}

func TestScriptConsoleKeepsNoDraft(t *testing.T) {
	m, b := newTestModel(t)
	openUsersDiff(t, m, b)
	press(t, m, "space", "w")
	c := scriptConsole(t, m)
	drain(t, m, m.closeTab(m.active))
	if d := m.store.(*fakeStore).draft(c.Key()); d != "" {
		t.Fatalf("draft saved: %q", d)
	}
}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/ui -run 'TestWriteScript|TestManualChanges|TestScriptConsole'`
Expected: FAIL（`<Space>w` 在 Diff 标签页里没有效果，`active tab = *ui.diffTab`）

- [ ] **Step 3: 脚本 console**

在 `internal/ui/console.go` 的 `consoleTab` 结构体里，`id` 字段后面加：

```go
	scratchKey string // a script console's own tab key; such a console keeps no draft
```

把 `Key`、`Title` 换成：

```go
func (t *consoleTab) Key() string {
	if t.scratchKey != "" {
		return t.scratchKey
	}
	return consoleTabKey(t.conn, t.ns)
}

func (t *consoleTab) Title() string {
	prefix := "console:"
	if t.scratchKey != "" {
		prefix = "sync:"
	}
	if t.ns.Database != "" {
		return prefix + t.ns.Database
	}
	return prefix + t.conn
}
```

在 `Init` 和 `saveDraft` 的开头分别加：

```go
	if t.scratchKey != "" {
		return nil
	}
```

```go
	if t.scratchKey != "" {
		return func() {}
	}
```

- [ ] **Step 4: `Model` 打开脚本 console**

在 `internal/ui/app.go` 的 `openConsole` 后面新增（需要 import `strconv`，若还没有）：

```go
// scriptConsoleMsg asks for a new console on conn holding text.
type scriptConsoleMsg struct {
	conn string
	ns   db.Namespace
	text string
}

// openScriptConsole opens a new console on conn with text, apart from the
// connection's usual console; it keeps no draft.
func (m *Model) openScriptConsole(conn string, ns db.Namespace, text string) tea.Cmd {
	key := consoleTabKey(conn, ns) + "\x00script\x00" + strconv.Itoa(m.nextTabID+1)
	var c *consoleTab
	open := m.showTab(key, func(id int) tab {
		c = newConsoleTab(id, m.backend, m.store, m.catalogs, conn, ns, m.settings, m.timings)
		c.keys, c.scratchKey = m.keys, key
		return c
	})
	return tea.Batch(open, c.insertText(text), m.ensureMeta(conn, ns.Database))
}
```

`Update` 的 switch 里加：

```go
	case scriptConsoleMsg:
		return m, m.openScriptConsole(msg.conn, msg.ns, msg.text)
```

- [ ] **Step 5: Diff 标签页写脚本**

在 `internal/ui/difftab.go` 的 `Handle` 的 switch 里加：

```go
	case keymap.WriteScript:
		return t.writeScript()
```

并在文件末尾追加：

```go
// writeScript asks for a console on the target holding the script of the
// picked changes.
func (t *diffTab) writeScript() tea.Cmd {
	if t.loading || len(t.objects) == 0 {
		t.setStatus("nothing to sync", true)
		return nil
	}
	stmts, manual := schemadiff.Script(t.writer, t.tgt.ref.Namespace, t.objects, func(obj, change int) bool {
		return t.picked[pick{obj, change}]
	})
	if len(stmts) == 0 {
		msg := "nothing picked"
		if len(manual) > 0 {
			msg += " · " + plural(len(manual), "change") + " to make by hand"
		}
		t.setStatus(joinHints(msg, keyHint(t.keys, keymap.Diff, keymap.ToggleChange, "picks a change")), true)
		return nil
	}
	conn, ns, text := t.tgt.conn, t.tgt.ref.Namespace, t.scriptText(stmts, manual)
	return func() tea.Msg { return scriptConsoleMsg{conn: conn, ns: ns, text: text} }
}

func (t *diffTab) scriptText(stmts, manual []string) string {
	var b strings.Builder
	b.WriteString("-- sqlvim schema sync\n")
	fmt.Fprintf(&b, "-- source: %s\n-- target: %s\n-- generated: %s\n",
		t.src.label(), t.tgt.label(), t.now().Format("2006-01-02 15:04:05"))
	b.WriteString("-- These statements change the target to match the source. Review them before running.\n")
	for _, s := range stmts {
		b.WriteString("\n" + s + ";\n")
	}
	if len(manual) > 0 {
		b.WriteString("\n-- Not scripted, change by hand:\n")
		for _, l := range manual {
			b.WriteString("--   " + strings.ReplaceAll(l, "\n", " ") + "\n")
		}
	}
	return b.String()
}
```

- [ ] **Step 6: 运行测试，确认通过**

Run: `go test -race ./internal/ui/...`
Expected: PASS

- [ ] **Step 7: 全量检查并提交**

Run: `gofumpt -l . && go vet ./... && go test -race ./...`

```bash
git add internal/ui
git commit -m "feat(ui): <Space>w 把勾选的变更写成脚本，放进目标连接的新 console"
```

---

### Task 10: 文档与收尾

**Files:**
- Modify: `README.md`、`README.zh-CN.md`（按键表）
- Modify: `CHANGELOG.md`（`[Unreleased]`）
- Modify: `docs/superpowers/specs/2026-10-15-table-ops-compare-design.md`（追加第 2 阶段的实现调整）

- [ ] **Step 1: README 按键表**

在 `README.md` 的按键表里，`| `a` | Tree: actions on the selected node, including truncating a table |` 之后加：

```
| `m` / `C` | Tree: mark a table, MySQL database or PostgreSQL schema as the compare source / compare it with the selected one (without a mark, pick the source from the loaded metadata) |
| `x` / `]c` `[c` / `~` | Diff tab: include or skip a change in the script / next, previous change / swap source and target |
| `<Space>w` | Diff tab: write the picked changes as a script into a new console on the target (nothing runs until you run it there) |
```

在 `README.zh-CN.md` 的对应位置（`a` 那一行之后）加：

```
| `m` / `C` | 树：把表、MySQL 库或 PostgreSQL schema 标记为对比源 / 与当前节点对比（没有标记时从已加载的元数据里选源） |
| `x` / `]c` `[c` / `~` | Diff 标签页：变更纳入或不纳入脚本 / 下一处、上一处变更 / 交换源与目标 |
| `<Space>w` | Diff 标签页：把勾选的变更写成脚本，放进目标连接的新 console（在那里运行才会执行） |
```

- [ ] **Step 2: CHANGELOG**

在 `CHANGELOG.md` 的 `## [Unreleased]` 的 `### Added` 下追加：

```
- Schema compare: `m` in the tree marks a table, a MySQL database or a PostgreSQL schema as the
  source and `C` compares it with the selected node of the same kind, on the same connection or
  another one of the same engine. The diff tab lists the differing columns, indexes, comments
  and options (foreign keys are shown, not synced); `x` picks what to sync (deletions start
  unpicked), `~` swaps the sides and `<Space>w` writes the script into a new console on the
  target, where the usual read-only and prod checks apply. Changes sqlvim cannot script stay in
  it as comments.
```

- [ ] **Step 3: spec 跟上实现**

在 `docs/superpowers/specs/2026-10-15-table-ops-compare-design.md` 的 §9 之前插入：

```markdown
## 8.1 第 2 阶段的实现调整

第 2 阶段（v1.3）实现时对上文做了这些调整，后续阶段以此为准：

- `DDLWriter.CreateTable` 返回 `(stmts []string, manual []SchemaChange)`：PG 的索引与注释是单独的语句；MySQL 写不出的表达式索引退回 `manual`。
- `DiffNamespace` 另外返回两边相同的表的数量（`identical`），供标题行使用。
- `Index` 新增 `Constraint bool`（PG：索引背后是 PRIMARY KEY / UNIQUE 约束），决定用 `DROP CONSTRAINT` / `ADD CONSTRAINT` 还是 `DROP INDEX` / `CREATE INDEX`；不参与比较。
- 归一化只在 `TableSchema` 里做，`Columns()` 等方法照旧返回服务端原文。`Default` 归一化为能直接跟在 `DEFAULT` 后面的 SQL 文本；自增与生成列的 `Default` 为空。`Position` 取列的顺序号。
- 索引配对：主键与主键配对，其余先按名字、再按定义；只是名字不同不算差异。新增索引的名字若以源表名开头，换成目标表名开头，避免同一个 PG schema 里重名。
- `Lossy` 还包括：新增 NOT NULL、无默认值的列；新增唯一索引。
- 写不出、标为 `manual` 的：两边的生成列变更；PG 的自增变更与需要 `USING` 的改类型；MySQL 的表达式索引；PG 的表选项。生成列一律写成 `STORED`。
- `C` 没有标记时，picker 的候选来自已加载的元数据（同引擎连接上的同类对象）。PG 的库节点不是命名空间，要选 schema。
- §5.3 的 `D`（切到数据视图）随第 4 阶段实现。
```

- [ ] **Step 4: 全量检查**

Run: `make lint && make test && make test-integration`
Expected: 全部通过

- [ ] **Step 5: 手动跑一遍**

Run: `make build && ./bin/sqlvim --config <测试配置目录> --debug`

在一个本地 MySQL 和一个本地 PG 上各建两个命名空间（或同一命名空间的两张表），制造列、索引、注释的差异：
1. 树上 `m` 标记源，`◆` 出现；在目标上 `C`，Diff 标签页列出差异，删除项默认未勾选。
2. `x`、`h` / `l`、`]c` / `[c`、`~` 各试一次；`?` 显示 Diff 的按键。
3. `<Space>w` 打开 `sync:<db>` console，运行脚本；回到 Diff 标签页 `<Space>r`，差异消失。
4. 在 prod 连接上做目标：console 运行时要求确认。
5. 不标记直接 `C`：picker 列出已加载元数据里的候选。

- [ ] **Step 6: Commit**

```bash
git add README.md README.zh-CN.md CHANGELOG.md docs/superpowers/specs/2026-10-15-table-ops-compare-design.md
git commit -m "docs: 记录结构对比与同步脚本"
```
