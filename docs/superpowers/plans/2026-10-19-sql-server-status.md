# SQL Server Status Tab Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A server status tab for MySQL, PostgreSQL, SQL Server and Oracle connections — Status (facts + sparklines), Sessions (cancel / disconnect), Locks, Slow queries, Variables, Replication — like the Redis one.

**Architecture:** `db.Conn` gains `Server() ServerReader`; each driver implements it with its own catalog queries and a shared contract in `dbtest`. The UI adds `sqlServerTab` (one per connection, same patterns as `serverTab`: `shower` sampling, one shared grid for the table sections) and a pure guard for the session actions. The UI never branches on driver name.

**Tech Stack:** Go, Bubble Tea v2, Lip Gloss v2, testcontainers (mysql:8.4, mysql:5.7, postgres:16-alpine, mssql 2022, gvenzl/oracle-free:23-slim).

**Spec:** `docs/superpowers/specs/2026-10-19-sql-server-status-design.md`

## Global Constraints

- Dependencies point downward: drivers must not import `ui`; the UI writes no dialect SQL and does not switch on `cfg.Driver` except Redis vs SQL.
- Versions: MySQL 5.7 and 8.0+ (version branches at **8.0.22**), PostgreSQL 12+, SQL Server 2014+ (must work on 2014; no Query Store), Oracle 12c+ (`CANCEL SQL` only on 18c+).
- Every SQL text shown (Sessions, Locks, TopStatements) passes through `sqlx.RedactSQL`.
- Sessions' SQL is truncated to 1024 characters in the driver; TopStatements default n = 50.
- Duration columns in table sections read `850 ms`, `41s`, `4m12s`, `3h05m` (`db.FormatElapsed`).
- Session actions: read-only (incl. default-read-only prod) → refused; others → confirm; prod with `readonly = false` → type the connection name. Every kill/cancel checks `SessionID.Since` first and returns `db.ErrSessionChanged` on mismatch.
- Action IDs `redis.server` / `redis.killclient` stay as they are (users' `keymap.toml`). New context `DBServer = "db_server"`.
- Key names in UI text only via `keyHint`. UI text in English.
- Refresh on the status tab must not reload the catalog.
- One `Sample` is at most two or three round trips and reads no per-statement or per-row data; it runs every `m.timings.server` (default 2s) only while the Status section is on screen.
- Every driver sets application name `datavim` unless the user's `Params` set one.
- Conventional Commits with scope; never add a Co-Authored-By line. CHANGELOG `[Unreleased]` entry; README and README.zh-CN both.

## Review Focus

- A session row with NULL user / db / SQL (PG background-ish rows, MySQL `Sleep` rows, SQL Server sessions without a request) → blank cells, never a panic or a scan error. Pinned in Task 3's contract (idle session listed with `includeIdle=true`) and Task 10's fake rows.
- SQL text with newlines or tabs (multi-line queries are the norm) → shown on one line in the grid, full text under `K`. Pinned in Task 10.
- Switching sections faster than they load → a late `sqlServerLoadedMsg` for the previous section is dropped, never shown under the wrong title. Pinned in Task 10.
- A terminal narrower than even `‹ Replication 6/6 ›` (e.g. 12 columns) → the bar is cut by `textutil.Fit`, no panic. Pinned in Task 9.
- The connection drops while the tab is open (server restart) → the section shows the error, the next Refresh reconnects through `withConn` and works; sampling restarts on Refresh. Pinned in Task 9.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/db/server.go` (new) | `ServerReader` and its types, `ErrUnavailable`, `ErrSessionChanged`, `Unavailable(reason)`, `FormatElapsed` |
| `internal/db/driver.go` | `Conn` gains `Server() ServerReader` |
| `internal/sqlx/redact.go` (new) | `RedactSQL` |
| `internal/db/dbtest/server.go` (new) | `RunServerContract`, `ServerFixture` |
| `internal/db/<driver>/server.go` (new, ×4) | the engine's `ServerReader` |
| `internal/db/<driver>/<driver>.go` | `Server()` method, application name |
| `internal/db/oracle/oracle.go` | `wrap` also maps ORA-00028 / ORA-03113 / ORA-03114 to `ErrConnLost` |
| `internal/ui/keymap/{keymap,defaults,kind,custom}.go` | context, actions, bindings |
| `internal/ui/sqlservertab.go` (new) | the tab: sections, sampling, grid sections |
| `internal/ui/sqlserverstatus.go` (new) | pure: fact formatting, metric rates, section bar |
| `internal/ui/sessionguard.go` (new) | pure: guard for cancel / disconnect |
| `internal/ui/app.go`, `nodemenu.go` | entry points, Refresh without catalog reload |
| `internal/ui/fakes_test.go` | `fakeServer` behind `fakeConn.Server()` |

Keeping `sqlServerTab` separate from the Redis `serverTab` (the spec leaves this open): their data and actions differ and a shared source interface would touch every Redis test. Shared helpers (`padRight`, `humanBytes`, `groupDigits`, `uptime`, sparkline) are reused as they are.

---

### Task 1: `db` server types, `Conn.Server()`, stubs

**Files:**
- Create: `internal/db/server.go`, `internal/db/server_test.go`
- Modify: `internal/db/driver.go` (Conn), `internal/db/{mysql,postgres,sqlserver,oracle}/*.go` (stub `Server()`), `internal/ui/fakes_test.go`, `internal/session/manager_test.go` (embedded `db.Conn` already satisfies it — check it compiles)

**Interfaces:**
- Produces (exact):

```go
var ErrUnavailable = errors.New("unavailable")
var ErrSessionChanged = errors.New("session changed")

// Unavailable is ErrUnavailable with the reason the user sees.
func Unavailable(reason string) error // errors.Is(err, ErrUnavailable); err.Error() == reason

type ServerReader interface {
	Info(ctx context.Context) (ServerInfo, error)
	Sample(ctx context.Context) (ServerSample, error)
	Sessions(ctx context.Context, includeIdle bool) (SessionList, error)
	Locks(ctx context.Context) (*ResultSet, error)
	TopStatements(ctx context.Context, n int) (*ResultSet, error)
	Settings(ctx context.Context) (*ResultSet, error)
	Replication(ctx context.Context) (*ResultSet, error)
	CancelQuery(ctx context.Context, id SessionID) (ok bool, err error)
	KillSession(ctx context.Context, id SessionID) (ok bool, err error)
}

type ServerInfo struct {
	Version       string
	SettingsTitle string // "Variables", "Settings", "Configuration", "Parameters"
	CanCancel     bool
}

type FactKind int
const ( FactText FactKind = iota; FactCount; FactBytes; FactPercent; FactDuration ) // Duration in seconds

type Fact struct {
	Label string
	Kind  FactKind
	Text  string
	Num   float64
	Of    float64 // 0: none
}

type MetricUnit int
const ( UnitNone MetricUnit = iota; UnitBytes )

type Metric struct {
	Name    string
	Counter bool
	Value   float64
	Unit    MetricUnit
}

type ServerSample struct {
	Facts   []Fact
	Metrics []Metric
	At      time.Time
}

type SessionID struct{ ID, Since string }

// SessionSummary is what the confirm dialog shows of a session; SQL is
// redacted and at most 1024 characters.
type SessionSummary struct{ User, Host, DB, State, Elapsed, SQL string }

type SessionList struct {
	Rows          *ResultSet
	IDs           []SessionID      // row by row
	Info          []SessionSummary // row by row
	Self          []bool           // row by row: application name is datavim
	Total, Active int
}

// FormatElapsed is 850 ms, 41s, 4m12s, 3h05m, 2d03h.
func FormatElapsed(d time.Duration) string
```

- [ ] **Step 1: Write the failing test** `TestFormatElapsed` (table) in `internal/db/server_test.go`:

```go
cases := []struct{ d time.Duration; want string }{
	{850 * time.Millisecond, "850 ms"}, {0, "0 ms"}, {time.Second, "1s"}, {41 * time.Second, "41s"},
	{4*time.Minute + 12*time.Second, "4m12s"}, {3*time.Hour + 5*time.Minute + 59*time.Second, "3h05m"},
	{51*time.Hour + 10*time.Minute, "2d03h"}, {-time.Second, "0 ms"},
}
```
and `TestUnavailable`: `err := Unavailable("needs PROCESS")`; `errors.Is(err, ErrUnavailable)` and `err.Error() == "needs PROCESS"`.

- [ ] **Step 2: Run** `go test ./internal/db -run 'TestFormatElapsed|TestUnavailable'` — Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `internal/db/server.go`; add `Server() ServerReader` to `Conn` in `driver.go` with the doc comment "Server reads server-wide status for the server tab". In each driver add `func (c *conn) Server() db.ServerReader { return server{c: c} }` and an empty `server` type in a new `server.go` whose methods all return `db.Unavailable("not implemented")` (replaced in Tasks 4–7). In `fakes_test.go` add `func (c *fakeConn) Server() db.ServerReader` returning `c.server` (a `*fakeServer`, defined in Task 9; for now a nil-safe stub type with the same methods returning `db.Unavailable("fake")`).
- [ ] **Step 4: Run** `go build ./... && go test -race ./internal/db/... ./internal/ui/... ./internal/session/...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(db): server status reader interface`

---

### Task 2: `sqlx.RedactSQL`

**Files:**
- Create: `internal/sqlx/redact.go`, `internal/sqlx/redact_test.go`

**Interfaces:**
- Produces: `func RedactSQL(sql string) string` — replaces the secret after `IDENTIFIED BY`, `IDENTIFIED WITH <plugin> BY`, `PASSWORD`, `PASSWORD =` (SQL Server `WITH PASSWORD = '…'`) in `CREATE|ALTER USER|ROLE|LOGIN` and `SET PASSWORD` with `'***'`; quoted literals and Oracle's bare identifier both. Other text unchanged. Uses the existing lexer (`lex.go`) so that a `password` inside a string or comment elsewhere is not touched, and works for any dialect (lex with `sqlx.MySQL` rules is fine for the keyword scan; tokens of the four engines' quotes must not break it).

- [ ] **Step 1: Write the failing test** `TestRedactSQL` (table):

```go
{"CREATE USER 'a'@'%' IDENTIFIED BY 's3cr3t'", "CREATE USER 'a'@'%' IDENTIFIED BY '***'"},
{"alter user a identified with mysql_native_password by 'x'", "alter user a identified with mysql_native_password by '***'"},
{"ALTER ROLE app WITH PASSWORD 'it''s'", "ALTER ROLE app WITH PASSWORD '***'"},
{"CREATE LOGIN app WITH PASSWORD = N'P@ss', CHECK_POLICY = OFF", "CREATE LOGIN app WITH PASSWORD = '***', CHECK_POLICY = OFF"},
{"ALTER USER scott IDENTIFIED BY tiger", "ALTER USER scott IDENTIFIED BY '***'"},
{"SET PASSWORD FOR 'a'@'%' = 'x'", "SET PASSWORD FOR 'a'@'%' = '***'"},
{"SELECT password FROM users WHERE name = 'IDENTIFIED BY x'", "SELECT password FROM users WHERE name = 'IDENTIFIED BY x'"},
{"UPDATE t SET note = 'PASSWORD x'", "UPDATE t SET note = 'PASSWORD x'"},
{"", ""},
{"ALTER USER a IDENTIFIED BY", "ALTER USER a IDENTIFIED BY"}, // truncated text: nothing to hide, no panic
```

- [ ] **Step 2: Run** `go test ./internal/sqlx -run TestRedactSQL` — Expected: FAIL.
- [ ] **Step 3: Implement** `RedactSQL`. Only statements whose first keywords are `CREATE|ALTER (USER|ROLE|LOGIN)` or `SET PASSWORD` are scanned.
- [ ] **Step 4: Run** the test — Expected: PASS.
- [ ] **Step 5: Commit** `feat(sqlx): redact passwords in shown SQL`

---

### Task 3: Server contract + MySQL reader

**Files:**
- Create: `internal/db/dbtest/server.go`, `internal/db/mysql/server.go` (replace stub)
- Modify: `internal/db/mysql/mysql.go` (program_name attribute), `internal/db/mysql/integration_test.go`

**Interfaces:**
- Consumes: Task 1 types, `sqlx.RedactSQL`, `db.FormatElapsed`.
- Produces:

```go
type ServerFixture struct {
	// SleepSQL runs for at least 30 seconds; it contains the marker
	// "datavim_contract_sleep" (in a comment or alias) so Sessions can find it.
	SleepSQL string
	// LockSQL updates one row of the standard fixture table (users, id 1),
	// so two sessions running it inside a transaction block each other.
	LockSQL string
	// Begin starts a transaction on a Session ("BEGIN", "START TRANSACTION",
	// "BEGIN TRANSACTION"; "" when the engine opens one implicitly: Oracle).
	Begin string
	// Setting is a parameter Settings must list (first column, case-insensitive).
	Setting string
}

func RunServerContract(t *testing.T, conn db.Conn, f ServerFixture)
```

Contract subtests (each `t.Run`, names exact):
- `"info"`: `Info` ok, `Version != ""`, `SettingsTitle != ""`.
- `"sample"`: two `Sample` calls 1s apart; same metric names in the same order; for `Counter` metrics value2 ≥ value1; `len(Facts) > 0`.
- `"sessions"`: session S runs `SleepSQL` in a goroutine; poll `Sessions(ctx, false)` up to 5s until a row's `Info[i].SQL` contains `datavim_contract_sleep`; `len(IDs) == len(Info) == len(Self) == len(Rows.Rows)`; that row's `Self` is true (S is a datavim connection); `Total >= Active >= 1`. Also `Sessions(ctx, true)` lists at least as many rows, and includes an idle `Session` opened just for this (its row must scan even with NULL SQL).
- `"cancel"`: if `Info.CanCancel`, `CancelQuery(id)` → ok; the sleep goroutine returns within 10s (error or not — MySQL `SLEEP` returns 1); `S.Ping` ok. Else `CancelQuery` → `errors.Is(err, db.ErrUnavailable)`.
- `"kill"`: start `SleepSQL` again on S, `KillSession(id)` → ok; within 10s S's call returns and the next `S.Ping` error `errors.Is(err, db.ErrConnLost)`; `KillSession(id)` again → `ok == false, err == nil`.
- `"session changed"`: open session S2 (idle), find its row, call `KillSession(SessionID{ID: id.ID, Since: "1999-01-01 00:00:00"})` when `id.Since != ""` → `errors.Is(err, db.ErrSessionChanged)` and `S2.Ping` still ok. Skip with `t.Skip` when `id.Since == ""` (MySQL, Oracle).
- `"locks"`: sessions A, B; A: `Begin` + `LockSQL`; B: `Begin` + `LockSQL` in a goroutine; poll `Locks` up to 5s for one row; the row contains A's and B's IDs (as text, in any column); roll back A, B's statement then finishes, roll back B.
- `"settings"`: some row's first column equals `Setting` (case-insensitive).
- `"top statements and replication"`: each returns a non-nil ResultSet or `ErrUnavailable`; nothing else.

MySQL reader, per spec §6.1 (queries left to the implementer): `Info` reads `VERSION()` once and caches `>= 8.0.22`; `SettingsTitle "Variables"`, `CanCancel true`. Facts labels: `version`, `uptime` (Duration), `role`, `connections` (Count, Of = max_connections), `running`, `peak`, `buffer pool` (Bytes), `hit rate` (Percent), `aborted` (Text "clients 3 · connects 0"). Metrics in order: `QPS` (Questions), `commits/s` (Handler_commit), `running` (gauge), `slow/s` (Slow_queries), `net in` / `net out` (Bytes). `Since` is `""`. Kill: `KILL QUERY ?`/`KILL ?` with the id parsed as uint64; "Unknown thread id" (error 1094) → `ok=false, nil`. Permission errors 1227 / 1142 → `db.Unavailable("needs the PROCESS privilege")` etc. Set the connection attribute `program_name=datavim` unless `Params["connectionAttributes"]` is set (check go-sql-driver docs with Context7 for the DSN key).

- [ ] **Step 1: Write the contract** `internal/db/dbtest/server.go` and call it from `mysql/integration_test.go` after `RunContract`:

```go
dbtest.RunServerContract(t, conn, dbtest.ServerFixture{
	SleepSQL: "SELECT SLEEP(30) AS datavim_contract_sleep",
	LockSQL:  "UPDATE app.users SET name = name WHERE id = 1",
	Begin:    "START TRANSACTION",
	Setting:  "max_connections",
})
```
Add `TestServerMySQL57` in the same file: start `mysql:5.7`, run `RunServerContract` with the same fixture (create `app.users` with one row first).

- [ ] **Step 2: Run** `go test -race -tags integration ./internal/db/mysql -run 'TestContract|TestServerMySQL57'` — Expected: FAIL (`not implemented`).
- [ ] **Step 3: Implement** `internal/db/mysql/server.go`.
- [ ] **Step 4: Run** the same command — Expected: PASS on 8.4 and 5.7.
- [ ] **Step 5: Commit** `feat(db): MySQL server status`

---

### Task 4: PostgreSQL reader

**Files:**
- Create: `internal/db/postgres/server.go` (replace stub)
- Modify: `internal/db/postgres/postgres.go` (`application_name`), `internal/db/postgres/integration_test.go`

**Interfaces:**
- Consumes: Task 3's `RunServerContract`.
- Per spec §6.2. `SettingsTitle "Settings"`, `CanCancel true`. Metrics: `TPS`, `active` (gauge), `rows read/s`, `rows written/s`. Facts: `version`, `uptime`, `role`, `connections` (Of max_connections), `active`, `idle in tx`, `cache hit` (Percent), `db size` (Bytes), `deadlocks`, `temp files` (Text), `oldest tx` (Text "00:04:12 (pid 48211)" or `—`). `SessionID{ID: pid, Since: backend_start in RFC3339Nano}`; kill/cancel run `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid = $1 AND backend_start = $2` — no row → check whether the pid exists at all: exists → `ErrSessionChanged`, not → `ok=false`. `pg_stat_statements` missing in this database → `db.Unavailable(fmt.Sprintf("pg_stat_statements is not installed in database %q", c.database))`; support `total_exec_time` (13+) and `total_time`. `<insufficient privilege>` query text is passed through as is. `application_name=datavim` via pgx `RuntimeParams` unless `Params["application_name"]` is set.

- [ ] **Step 1:** In `postgres/integration_test.go` call `RunServerContract(t, conn, dbtest.ServerFixture{SleepSQL: "SELECT pg_sleep(30) AS datavim_contract_sleep", LockSQL: "UPDATE public.users SET name = name WHERE id = 1", Begin: "BEGIN", Setting: "max_connections"})` (adjust the table name to the fixture's).
- [ ] **Step 2: Run** `go test -race -tags integration ./internal/db/postgres -run TestContract` — Expected: FAIL.
- [ ] **Step 3: Implement** `internal/db/postgres/server.go`.
- [ ] **Step 4: Run** — Expected: PASS, including `"session changed"`.
- [ ] **Step 5: Commit** `feat(db): PostgreSQL server status`

---

### Task 5: SQL Server reader

**Files:**
- Create: `internal/db/sqlserver/server.go` (replace stub)
- Modify: `internal/db/sqlserver/integration_test.go`

**Interfaces:**
- Per spec §6.3. `SettingsTitle "Configuration"`, `CanCancel false` → `CancelQuery` returns `db.Unavailable("cancelling a query is not supported on SQL Server")`. Metrics: `batch req/s`, `tx/s` (counters from `dm_os_performance_counters`), `running` (gauge), `connections` (gauge). Facts include `buffer hit` computed as ratio/base. `SessionID{ID: session_id, Since: login_time (RFC3339Nano)}`; kill: check `login_time` in `sys.dm_exec_sessions` then `KILL <n>` (the id is an int you format yourself; `KILL` takes no parameter). Error 6106 (process ID not active) → `ok=false`. Missing `VIEW SERVER STATE` (error 300 / 297) → `db.Unavailable("needs VIEW SERVER STATE")`. Every query must run on 2014: no `STRING_AGG`, no `dm_exec_input_buffer` without a fallback, no `sys.dm_exec_session_wait_stats`. No AG → Replication returns an empty ResultSet with its columns.
- App name is already `datavim` (`sqlserver.go:79`).

- [ ] **Step 1:** Call `RunServerContract` with `SleepSQL: "WAITFOR DELAY '00:00:30'; SELECT 1 AS datavim_contract_sleep"`, `LockSQL: "UPDATE dbo.users SET name = name WHERE id = 1"`, `Begin: "BEGIN TRANSACTION"`, `Setting: "max server memory (MB)"`. If the marker is not visible in `dm_exec_sql_text` for a batch, put it in a leading comment: `"/* datavim_contract_sleep */ WAITFOR DELAY '00:00:30'"`.
- [ ] **Step 2: Run** `go test -race -tags integration ./internal/db/sqlserver -run TestContract` — Expected: FAIL.
- [ ] **Step 3: Implement** `internal/db/sqlserver/server.go`.
- [ ] **Step 4: Run** — Expected: PASS (`"cancel"` takes the `CanCancel=false` branch).
- [ ] **Step 5: Commit** `feat(db): SQL Server server status`

---

### Task 6: Oracle reader

**Files:**
- Create: `internal/db/oracle/server.go` (replace stub)
- Modify: `internal/db/oracle/oracle.go` (`wrap`, program name), `internal/db/oracle/oracle_test.go`, `internal/db/oracle/integration_test.go`

**Interfaces:**
- Per spec §6.4. `SettingsTitle "Parameters"`, `CanCancel` = major version ≥ 18. Metrics: `user calls/s`, `executions/s`, `commits/s`, `active` (gauge). `SessionID{ID: "sid,serial#", Since: ""}`; the ID string is validated as `^\d+,\d+$` before it is put into `ALTER SYSTEM KILL SESSION '<id>' IMMEDIATE` / `ALTER SYSTEM CANCEL SQL '<id>'` (no bind variables there). ORA-00030 (session does not exist) → `ok=false`. ORA-00942 / ORA-01031 → `db.Unavailable("needs SELECT_CATALOG_ROLE")` / `("needs ALTER SYSTEM")`.
- `wrap`: also `ErrConnLost` for ORA-00028, ORA-03113, ORA-03114, ORA-01012 (match go-ora's error code, not the message).
- Program name `datavim` via go-ora's connection option (look it up with Context7) unless set in `Params`.

- [ ] **Step 1: Write the failing unit test** `TestWrapKilled` in `oracle_test.go`: for each of the four codes, an error carrying it → `errors.Is(wrap(err), db.ErrConnLost)`; ORA-00942 → not.
- [ ] **Step 2: Run** `go test ./internal/db/oracle -run TestWrapKilled` — Expected: FAIL.
- [ ] **Step 3: Implement** the `wrap` change.
- [ ] **Step 4:** In `integration_test.go`, before `RunServerContract`, connect as `system` / `Sys0racle` to `FREEPDB1` and run `GRANT SELECT_CATALOG_ROLE TO datavim`, `GRANT ALTER SYSTEM TO datavim`; then reopen the `datavim` connection (roles apply to new sessions). Call `RunServerContract` with `SleepSQL: "SELECT /* datavim_contract_sleep */ COUNT(*) FROM all_objects a, all_objects b, all_objects c"`, `LockSQL: "UPDATE \"users\" SET \"name\" = \"name\" WHERE id = 1"` (match the fixture's quoting), `Begin: ""`, `Setting: "processes"`.
- [ ] **Step 5: Run** `go test -race -tags integration ./internal/db/oracle -run TestContract` — Expected: FAIL.
- [ ] **Step 6: Implement** `internal/db/oracle/server.go`.
- [ ] **Step 7: Run** unit + integration — Expected: PASS.
- [ ] **Step 8: Commit** `feat(db): Oracle server status`

---

### Task 7: Keymap

**Files:**
- Modify: `internal/ui/keymap/keymap.go`, `defaults.go`, `kind.go`, `custom.go`, `keymap_test.go`

**Interfaces:**
- Produces: `DBServer Context = "db_server"` (doc: "a SQL connection's server tab"); actions `CancelSessionQuery Action = "server.cancelquery"`, `ToggleIdle Action = "server.toggleidle"`.
- Bindings in `DBServer`: `gridBindings(DBServer)`; `[` PrevSection "Previous section"; `]` NextSection "Next section"; `d d` KillClient "Disconnect the session (sessions list)"; `c` CancelSessionQuery "Cancel the session's query (sessions list)"; `a` ToggleIdle "Show / hide idle sessions"; `F` FilterPrompt "Filter by name (variables)"; `f x` FilterClear "Clear the filter".
- `ServerInfo` description → `"Server status: load, sessions, locks, …"`; remove `ServerInfo: KindRedis` from `kinds[Global]`; add `DBServer` to the list in `custom.go`.

- [ ] **Step 1: Write the failing tests** in `keymap_test.go`: `TestDBServerBindings` resolves `c`, `a`, `d d`, `[`, `]`, `F`, `f x` in `DBServer` to the actions above; `TestServerInfoBothKinds`: the help listing for `KindSQL` and for `KindRedis` both contain `ServerInfo` (use the same helper the existing kind tests use).
- [ ] **Step 2: Run** `go test ./internal/ui/keymap` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./internal/ui/...` — Expected: PASS (the `app.go` ServerInfo case still refuses SQL connections until Task 9, so `TestServerTabOpens` is unchanged).
- [ ] **Step 5: Commit** `feat(keymap): server tab keys for SQL connections`

---

### Task 8: Session action guard (pure)

**Files:**
- Create: `internal/ui/sessionguard.go`, `internal/ui/sessionguard_test.go`

**Interfaces:**
- Produces:

```go
type sessionAction int
const ( actCancel sessionAction = iota; actDisconnect )

// sessionTarget is the row the action is for.
type sessionTarget struct {
	ID, User, Host, DB, State, Elapsed, SQL string
	Self bool
}

type sessionCheck struct {
	Refuse string   // non-empty: refused, the status line
	Title  string
	Body   []string
	Expect string   // the text to type; "" means press y
}

func guardSession(cfg config.Connection, a sessionAction, s sessionTarget, width int) sessionCheck
```

Exact copy: refuse `"read-only connection: cannot cancel or disconnect sessions"`; titles `"Cancel the query of session <ID>?"` / `"Disconnect session <ID>?"`; body lines `"user <User> · host <Host> · db <DB>"`, `"<State> · <Elapsed>"`, the SQL clipped to `width` (one line, newlines → spaces), then for disconnect `"Its open transaction, if any, is rolled back."`, and for `Self` `"This is a datavim connection (maybe this one)."`. `Expect = cfg.Name` when `cfg.Env == config.EnvProd` (and not read-only, which already refused).

- [ ] **Step 1: Write the failing test** `TestGuardSession` (table): read-only dev → Refuse; prod default (read-only) → Refuse; prod with `ReadOnly: &no` (`no := false`; the field is `*bool`) → Expect `"prod-db"`; dev → Expect `""`, Title for each action; Self adds the line; SQL `"SELECT\n  1"` shows as `"SELECT 1"`, and a 200-char SQL is clipped to `width`.
- [ ] **Step 2: Run** `go test ./internal/ui -run TestGuardSession` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): guard for cancelling and disconnecting sessions`

---

### Task 9: `sqlServerTab` — opening, section bar, Status sampling

**Files:**
- Create: `internal/ui/sqlservertab.go`, `internal/ui/sqlserverstatus.go`, `internal/ui/sqlservertab_test.go`
- Modify: `internal/ui/app.go` (ServerInfo case at `app.go:575`, Refresh case, retry list near `app.go:837`), `internal/ui/nodemenu.go`, `internal/ui/fakes_test.go`, `internal/ui/servertab_test.go` (`TestServerTabOpens` last block)

**Interfaces:**
- Consumes: Task 1 types, Task 7 keymap, `withConn` (`reconnect.go:12`), `serverTabKey`, `shower`, `padRight`, `humanBytes`, `groupDigits`, `uptime`, `missing`, `sparkline.Render`.
- Produces:

```go
func (m *Model) openSQLServer(conn string) tea.Cmd // showTab(serverTabKey(conn), …)
func newSQLServerTab(id int, b Backend, keys *keymap.Keymap, conn string, interval time.Duration, s config.Settings) *sqlServerTab

type sqlSection int
const ( sqlStatus sqlSection = iota; sqlSessions; sqlLocks; sqlSlow; sqlSettings; sqlReplication; sqlSections )

type sqlServerInfoMsg   struct{ tabID int; conn string; info db.ServerInfo; err error }
type sqlServerSampleMsg struct{ tabID, gen int; conn string; sample db.ServerSample; err error }
type sqlServerTickMsg   struct{ tabID, gen int }
// all three have TabID(); the two with conn/err have failure()

// pure helpers in sqlserverstatus.go
func formatFact(f db.Fact) string
func metricPoint(prev *db.ServerSample, cur db.ServerSample, i int) float64 // NaN: no value
func sectionBar(names []string, cur, width int) string
```

Behavior: `Context()` is `keymap.DBServer`; `Title()` `"<conn> · server"`; `Target()` returns `("", db.TableRef{})` so `app.go`'s Refresh does not call `reloadMeta` (verify the Refresh branch takes the conn from `Target()`; if it uses `contextNamespace()` instead, add an explicit `if _, ok := t.(*sqlServerTab)` skip there). The database for `withConn` is `cfg.Database`, else `sqlx.DialectOf(cfg.Driver).Features().FallbackDatabase`. `Init` reads `Info`; `Shown`/`Hidden` start/stop sampling like `serverTab`. Section names: `Status, Sessions, Locks, Slow queries, <info.SettingsTitle or "Variables">, Replication`. `formatFact`: Text as is; Count `groupDigits`, with `Of` → `"87 / 500"`; Bytes `humanBytes`; Percent one decimal + `%`; Duration `uptime(int64(Num))`. `metricPoint`: gauge → value; counter → `(cur-prev)/seconds(cur.At-prev.At)`, NaN when `prev == nil`, dt ≤ 0, cur < prev, or the metric names at index i differ. `sectionBar`: full `A · B · C` (current in `theme.Title`, rest `theme.Muted`) when it fits in `width`, else `"‹ <name> <cur+1>/<n> ›"`, then `textutil.Fit`. A sample error stops sampling and shows the error (`ErrUnavailable` in `theme.Muted`); Refresh restarts. ServerInfo action in `app.go`: Redis → `openServer`, SQL → `openSQLServer`, no conn → `"select a connection"`. `nodemenu.go`: add `{keymap.ServerInfo, "Server status"}` after "Open console" for SQL `KindConn`, `KindDatabase`, `KindSchema`.

`fakeServer` in `fakes_test.go`: fields `info db.ServerInfo; infoErr error; samples []db.ServerSample` (returned in turn, last repeats); `sampleErr error; sessions db.SessionList; sets map[string]*db.ResultSet` (keys `"locks"`, `"top"`, `"settings"`, `"replication"`); `errs map[string]error` (same keys + `"sessions"`, `"cancel"`, `"kill"`); `calls []string` (method name, plus `"sessions idle"` when `includeIdle`); `cancelled, killed []db.SessionID`. Mutex-guarded like `fakeConn`.

- [ ] **Step 1: Write the failing tests** in `sqlservertab_test.go`:
  - `TestFormatFact` (table): `{Kind: FactCount, Num: 87, Of: 500}` → `"87 / 500"`; `{FactBytes, Num: 12 << 30}` → `humanBytes(12<<30)`; `{FactPercent, Num: 99.84}` → `"99.8%"`; `{FactDuration, Num: 273600}` → `"3d 4h"`; `{FactText, Text: "primary"}` → `"primary"`.
  - `TestMetricPoint` (table): no prev → NaN; counter 100→160 over 2s → 30; counter decreased → NaN; gauge → value; name mismatch → NaN.
  - `TestSectionBar`: width 80 → contains all six names; width 30 with cur 2 → `"‹ Locks 3/6 ›"` (strip ANSI); width 12 → no panic, `ansi.StringWidth ≤ 12`.
  - `TestSQLServerTabOpens`: SQL model (`newTestModel`), `press "space","i"` from the tree → active tab title `"<conn> · server"`, screen contains `"Status"`, `"Sessions"`, the fake's version, a fact label and a metric name; `fakeServer.calls` starts `["Info","Sample"]`; held ticks = 1 (reuse `drainHold`, extended to hold `sqlServerTickMsg` too).
  - `TestSQLServerSamplingStopsWhenHidden`: switch to another tab → the tick is ignored (no new `Sample` call); switch back → sampling resumes with a new gen.
  - `TestSQLServerRefreshNoCatalog`: on the tab, `<leader> r` → `fakeConn.catalogCount()` unchanged; `Sample` called again and samples reset.
  - `TestSQLServerSampleErrorThenRefresh`: `sampleErr = db.ErrConnLost`-wrapped error → error shown, sampling stopped; clear the error, Refresh → sampling again (Review Focus: connection drop).
  - `TestSQLServerNodeMenu`: `KindConn`, `KindDatabase` menus of a SQL connection contain "Server status".
  - Change `TestServerTabOpens`'s last block: a SQL connection now opens the SQL tab (title `" · server"` suffix), no "select a Redis connection".
- [ ] **Step 2: Run** `go test -race ./internal/ui -run 'TestFormatFact|TestMetricPoint|TestSectionBar|TestSQLServer|TestServerTabOpens'` — Expected: FAIL.
- [ ] **Step 3: Implement** `sqlserverstatus.go`, the Status part of `sqlservertab.go`, `fakeServer`, `app.go` and `nodemenu.go` changes. Status view layout = `serverTab.statusLines` (facts `padRight(label, 12)`; blank line; one sparkline row per metric: `padRight(name, 12) + padRight(value, 12) + sparkline`), value of a counter shown per second; Bytes metrics as `humanBytes(v) + "/s"`.
- [ ] **Step 4: Run** the same tests, then `go test -race ./internal/ui/...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): server status tab for SQL connections`

---

### Task 10: Table sections

**Files:**
- Modify: `internal/ui/sqlservertab.go`, `internal/ui/sqlservertab_test.go`

**Interfaces:**
- Consumes: Task 9 tab, `fakeServer`.
- Produces: `type sqlServerLoadedMsg struct{ tabID int; conn string; sec sqlSection; rs *db.ResultSet; sessions db.SessionList; err error }` (Sessions fills `sessions` and sets `rs = sessions.Rows`) with `TabID()`/`failure()`; `func (t *sqlServerTab) load() tea.Cmd`.

Behavior: entering a non-Status section stops sampling and loads it; a `sqlServerLoadedMsg` whose `sec` is not the current section is dropped. Same grid reuse as `serverTab` (`ReplaceResult` on reload of the same section). Empty texts: Sessions `"no sessions"`, Locks `"no lock waits"`, Slow `"no statements recorded"`, Settings `"no settings"` (or `"no match for <filter>"`), Replication `"no replication configured"`. `ErrUnavailable` → the reason in `theme.Muted`; other errors `theme.Error`. Sessions: summary line `"<Total> sessions · <Active> running · idle hidden (<keyHint a>)"` / `"idle shown (<keyHint a>)"`; `a` toggles `showIdle` and reloads; rows with `Self` get `"*"` prefixed to the first cell and the summary ends `" · * datavim"` when any. SQL cells: the grid already shows newlines as one line — assert it. Settings filter: `F` opens the same prompt keytab uses for its filter (`keytab.go:520`), case-insensitive substring on the first column, applied client-side to the last loaded ResultSet; `f x` clears; the summary shows `"filter: <text>"`. Export works (`exportSource` like `serverTab`).

- [ ] **Step 1: Write the failing tests**:
  - `TestSQLServerSections`: `]` four times walks Sessions→Locks→Slow→Settings with calls `"Sessions"`, `"Locks"`, `"TopStatements"`, `"Settings"`; the bar shows the fake's `SettingsTitle` (`"Parameters"`); `]` past the last wraps to Status and sampling restarts.
  - `TestSQLServerStaleSectionDropped`: from Sessions press `]` before running the Sessions load cmd, then deliver the Sessions msg → the grid stays empty / loading under "Locks" (Review Focus).
  - `TestSQLServerUnavailable`: `errs["top"] = db.Unavailable("pg_stat_statements is not installed in database \"shop\"")` → that text on screen; `]` to Settings works.
  - `TestSQLServerSessions`: fake list with 2 rows, one `Self`, one with empty user and a SQL `"SELECT\n  1"` → summary `"2 sessions · 1 running · idle hidden"`, `"*"` on the self row, `"SELECT"` and `"1"` on one screen line; `a` → `calls` has `"sessions idle"` and the summary says `"idle shown"`.
  - `TestSQLServerSettingsFilter`: settings rows `max_connections`, `Max_Allowed_Packet`, `wait_timeout`; `F` + `"max"` + enter → 2 rows; `f x` → 3.
- [ ] **Step 2: Run** `go test -race ./internal/ui -run TestSQLServer` — Expected: new tests FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/ui/...` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): sessions, locks, statements, settings and replication sections`

---

### Task 11: Cancel and disconnect

**Files:**
- Modify: `internal/ui/sqlservertab.go`, `internal/ui/sqlservertab_test.go`

**Interfaces:**
- Consumes: Task 8 `guardSession`, Task 10 sessions state (`IDs`, `Self`, rows), `overlay.NewConfirm(title, lines, expect, okMsg)`.
- Produces: `sessionConfirmedMsg{tabID int; act sessionAction; id db.SessionID}`, `sessionActedMsg{tabID int; conn string; act sessionAction; id db.SessionID; ok bool; err error}`.

Behavior: `KillClient` → disconnect, `CancelSessionQuery` → cancel. Outside Sessions: `"works in the sessions list"`; no row: `"no session selected"`; cancel with `!info.CanCancel`: `"cancelling a query is not supported here; use <keyHint dd> to disconnect"`. The `sessionTarget` comes from `SessionList.IDs[r]`, `Info[r]` and `Self[r]` of the cursor row (never from grid columns, which differ per engine). Results: ok → `"cancelled the query of session <ID>"` / `"disconnected session <ID>"`; `!ok` → `"session <ID> is already gone"`; `ErrSessionChanged` → `"session <ID> changed since the list was read; refreshed"`; other errors → the error text. Every outcome reloads Sessions.

- [ ] **Step 1: Write the failing tests**:
  - `TestSQLServerKillReadOnly`: read-only connection, Sessions with one row, `d d` → status `"read-only connection: cannot cancel or disconnect sessions"`, no overlay, `fakeServer.killed` empty.
  - `TestSQLServerKillConfirm`: dev connection, `d d` → confirm overlay with title `"Disconnect session 48211?"`; `y` → `killed == [{"48211", "t0"}]`, status `"disconnected session 48211"`, `calls` ends with `"Sessions"`.
  - `TestSQLServerKillProdTypesName`: prod with read-only off → the overlay expects `"prod-db"`; typing it + enter kills.
  - `TestSQLServerCancelUnsupported`: `info.CanCancel = false`, `c` → status contains `"not supported"` and the `d d` hint.
  - `TestSQLServerKillChanged`: `errs["kill"] = db.ErrSessionChanged` → status `"session 48211 changed since the list was read; refreshed"`, Sessions reloaded.
  - `TestSQLServerKillGone`: fake returns `ok=false` → `"session 48211 is already gone"`.
  - `TestSQLServerKillOutsideSessions`: on Locks, `d d` → `"works in the sessions list"`.
- [ ] **Step 2: Run** `go test -race ./internal/ui -run 'TestSQLServerKill|TestSQLServerCancel'` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test` — Expected: PASS.
- [ ] **Step 5: Commit** `feat(ui): cancel and disconnect sessions from the server tab`

---

### Task 12: Docs, lint, full verification

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]` → `### Added`), `README.md`, `README.zh-CN.md`

- [ ] **Step 1:** CHANGELOG line: `- Server status for MySQL, PostgreSQL, SQL Server and Oracle (\`<leader> i\` or "Server status" in the tree menu): load and sparklines, sessions (cancel / disconnect, guarded like Redis's client kill), lock waits, top statements, settings and replication.` README feature list: one line in each language.
- [ ] **Step 2: Run** `make lint && make test && make test-integration` — Expected: all pass (OrbStack: `orb start` first).
- [ ] **Step 3: Manual check** `./bin/datavim --config <dir> --debug` against a local MySQL and PG: open the tab, create a lock wait from two consoles, see it under Locks, disconnect the blocker from Sessions, the waiter proceeds. SQL Server 2014 prod is checked by the author (spec §9).
- [ ] **Step 4: Commit** `docs: server status for SQL connections`
