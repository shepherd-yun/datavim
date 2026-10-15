# SQL 文件 console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `:e <path>` opens a `.sql` file in a console on the current connection; it runs like any console, `<Space>R` runs from the cursor to the end, `:w` / `:w <path>` / `:w!` / `:wq` save it, and unsaved files are never dropped silently.

**Architecture:** A file console is a `consoleTab` with a `file *consoleFile` (path, mtime, size, saved text). `internal/ui/consolefile.go` holds the pure file IO (`loadSQLFile`, `writeSQLFile`, path helpers), always run inside a `tea.Cmd`. The root model parses `:e` / `:w…` in `runCommand`, inherits the connection from `contextNamespace()`, and creates the tab once `fileReadMsg` arrives. Saving goes `consoleTab.save` → `fileSavedMsg` (or a confirm overlay → `saveConfirmedMsg` → forced save).

**Tech Stack:** Go 1.26, Bubble Tea v2 (`charm.land/bubbletea/v2`), stdlib `os` / `path/filepath` / `io/fs`.

**Spec:** `docs/superpowers/specs/2026-10-16-sql-file-console-design.md`

## Global Constraints

- Max file size for `:e`: 2 MB (`2 << 20` bytes). Error: `<name> is <size>: too large for the editor`.
- Non-UTF-8 or NUL bytes: `<name> is not a UTF-8 text file`. Directory: `<name> is a directory`.
- No context: `open a console or select a connection first`. Empty `:e`: `:e needs a file name`.
- Tab key: `"file:" + path + "|" + consoleTabKey(conn, ns)`; path is absolute and clean (`filepath.Abs`, which cleans), `~` expanded. File consoles never read or save drafts.
- Title: `base`, plus ` [new]` when not on disk yet, else ` [+]` when the text differs from the saved text.
- Writes are atomic (temp file in the same directory → `Sync` → `Chmod` to the old mode or 0644 → `Rename`), follow symlinks, and strip nothing; a leading UTF-8 BOM is dropped on read.
- File consoles: `:w` saves the file and never submits staged result edits (`<Space>w` does). Plain consoles: `:w` unchanged.
- Safety rules (readonly, prod confirm, missing WHERE, tx, no silent reconnect in a tx) go through the unchanged `consoleTab.run`.
- UI text in English; key names in hints only via `keyHint`.
- Conventional Commits with a scope, Chinese subject like recent history. **No `Co-Authored-By` line.**
- `CHANGELOG.md` `[Unreleased]`; README changes in both `README.md` and `README.zh-CN.md`.
- `make test` and `make lint` pass at the end of every task.

## Deviations from the spec (Task 6 updates the spec to match)

- "Modified" is `editor.Value() != file.saved`, as the spec says; no version counter.
- `:w!` with no path on a plain console behaves like `:w` (there is no file to force).
- Editing or deleting a connection closes its tabs today; a file console with unsaved changes is **kept open** instead (its session is closed and a note is shown), so the text is not lost. The spec did not cover this.
- Run-all failures also move the cursor to the start of the failed statement when the server gives no error position.

## Review Focus

1. **A path with spaces** (`:e ~/my scripts/a b.sql`) must open that file: the argument is everything after the verb, not the first field. → test in Task 2.
2. **The same file spelled differently** (`a.sql`, `./a.sql`, the absolute path) on one connection must switch to the one tab. → test in Task 2.
3. **A symlinked `.sql` file** (dotfiles setups): `:w` must write the target and leave the link a link. → test in Task 3.
4. **Typing while a save is in flight**: the tab must stay `[+]` once the save lands, because the new text was not written. → test in Task 3.
5. **Editing a connection while its file console has unsaved changes** must not discard the text. → test in Task 4.

---

## File Structure

| File | Change |
|---|---|
| `internal/sqlx/split.go` | `From(stmts, offset)` |
| `internal/sqlx/split_test.go` | `From` table test |
| `internal/ui/keymap/keymap.go` | `RunFromCursor` action |
| `internal/ui/keymap/defaults.go` | `<Space>R` in Console |
| `internal/ui/console.go` | `RunFromCursor`, failure N/M, `file` field, key/title/draft, `save`, `fileSavedMsg` handling |
| `internal/ui/consolefile.go` (new) | `consoleFile`, `fileTabKey`, `absPath`, `tildePath`, `loadSQLFile`, `writeSQLFile`, messages |
| `internal/ui/consolefile_test.go` (new) | UI tests for open / save / close |
| `internal/ui/app.go` | `runCommand` (`e`, `w…`, `wq`), `editFile`, `openFile`, `fileRead`, `writeFile`, `location`, close/quit confirmations, `closeTabsOf` |
| `internal/ui/recent.go`, `internal/history/history.go` | `RecentFile` |
| `internal/ui/console_test.go`, `internal/ui/recent_test.go` | tests |
| `CHANGELOG.md`, `README.md`, `README.zh-CN.md`, `docs/superpowers/specs/2026-10-13-sqlvim-design.md`, `docs/superpowers/specs/2026-10-16-sql-file-console-design.md` | docs |

---

### Task 1: Run from cursor and "statement N/M failed"

**Files:**
- Modify: `internal/sqlx/split.go` (after `At`, ~line 86)
- Test: `internal/sqlx/split_test.go`
- Modify: `internal/ui/keymap/keymap.go:86`, `internal/ui/keymap/defaults.go:105`
- Modify: `internal/ui/console.go` (`runDoneMsg` ~67, `editorAction` ~353, `execute` ~521, `done` ~722, `statusLine` ~1074, `locateError` ~1100)
- Test: `internal/ui/console_test.go`

**Interfaces:**
- Produces: `func sqlx.From(stmts []Statement, offset int) []Statement`; `keymap.RunFromCursor Action = "sql.runfrom"`; `runDoneMsg.total int`.

- [ ] **Step 1: Write the failing sqlx test** (append to `internal/sqlx/split_test.go`)

```go
func TestFrom(t *testing.T) {
	src := "select 1;\nselect 2;\nselect 3;" // statements end at 8, 18, 28
	stmts := Split(src, MySQL)
	for _, c := range []struct {
		offset int
		first  string
		n      int
	}{
		{0, "select 1", 3},
		{8, "select 1", 3},  // on the delimiter
		{9, "select 2", 2},  // between statements: the next one
		{15, "select 2", 2}, // inside
		{28, "select 3", 1},
		{29, "", 0}, // after the last
	} {
		got := From(stmts, c.offset)
		if len(got) != c.n || (c.n > 0 && got[0].Text != c.first) {
			t.Errorf("From(%d) = %+v; want %d from %q", c.offset, got, c.n, c.first)
		}
	}
}
```

- [ ] **Step 2: Run it** — `go test ./internal/sqlx -run TestFrom` → FAIL: `undefined: From`.

- [ ] **Step 3: Implement** (after `At` in `internal/sqlx/split.go`)

```go
// From returns the statements from the one under offset to the end; an
// offset between two statements starts at the next one.
func From(stmts []Statement, offset int) []Statement {
	for i, s := range stmts {
		if offset <= s.End {
			return stmts[i:]
		}
	}
	return nil
}
```

- [ ] **Step 4: Run** `go test ./internal/sqlx -run TestFrom` → PASS.

- [ ] **Step 5: Write the failing UI tests** (append to `internal/ui/console_test.go`; add imports `errors`, `slices` if missing)

```go
const threeInserts = "insert into a values (1);\ninsert into b values (2);\ninsert into c values (3);"

func TestRunFromCursor(t *testing.T) {
	m, b := newTestModel(t)
	c := openLocalConsole(t, m)
	c.editor.SetValue(threeInserts)
	c.editor.SetCursor(1, 3)
	press(t, m, "space", "R")
	execs := b.conn.execList()
	want := []string{"insert into b values (2)", "insert into c values (3)"}
	if len(execs) < 2 || !slices.Equal(execs[len(execs)-2:], want) || slices.Contains(execs, "insert into a values (1)") {
		t.Fatalf("execs = %v", execs)
	}
}

func TestRunAllNamesTheFailedStatement(t *testing.T) {
	m, b := newTestModel(t)
	c := openLocalConsole(t, m)
	b.conn.setFail("insert into b values (2)", errors.New("boom"))
	c.editor.SetValue(threeInserts)
	press(t, m, "R")
	if !strings.Contains(c.status, "statement 2/3 failed") || !strings.Contains(c.status, "boom") {
		t.Fatalf("status = %q", c.status)
	}
	if got, want := c.editor.Offset(), strings.Index(threeInserts, "insert into b"); got != want {
		t.Fatalf("cursor at %d, want %d (the failed statement)", got, want)
	}
}

func TestConsoleHintsRunFromCursor(t *testing.T) {
	m, _ := newTestModel(t)
	c := openLocalConsole(t, m)
	if !strings.Contains(c.statusLine(), "run from here") {
		t.Fatalf("hints: %s", c.statusLine())
	}
}
```

(If the hint line is cut at width 100, call `c.SetSize(200, 20)` before asserting.)

- [ ] **Step 6: Run** `go test ./internal/ui -run 'TestRunFromCursor|TestRunAllNamesTheFailedStatement|TestConsoleHintsRunFromCursor'` → FAIL: `<Space>R` does nothing, the status has no `statement 2/3`, the hint is missing.

- [ ] **Step 7: Implement**

`internal/ui/keymap/keymap.go`, next to `RunAll`:

```go
	RunFromCursor     Action = "sql.runfrom"
```

`internal/ui/keymap/defaults.go`, after the `R` binding:

```go
		Binding{Console, keys(LeaderToken, "R"), RunFromCursor, "Run from the statement under the cursor to the end"},
```

`internal/ui/console.go` — `editorAction`:

```go
	case keymap.RunFromCursor:
		return t.run(sqlx.From(sqlx.Split(t.editor.Value(), t.dialect), t.editor.Offset()))
```

`runDoneMsg`: add `total int // statements asked for, without an injected BEGIN`. In `execute`, before the BEGIN injection: `total := len(stmts)`, and set `total: total` in the `runDoneMsg` literal.

In `done`, replace `t.setStatus(t.locateError(m.text, last), true)` with:

```go
		status := t.locateError(m.text, last)
		n := len(m.results)
		if m.began {
			n--
		}
		if m.total > 1 && n > 0 {
			status = fmt.Sprintf("statement %d/%d failed: %s", n, m.total, status)
		}
		t.setStatus(status, true)
```

`locateError` — jump to the statement when the server gives no position:

```go
func (t *consoleTab) locateError(text string, r runner.Result) string {
	msg := errorText(r.Err)
	if r.Start < 0 || text == "" || text != t.editor.Value() {
		return msg
	}
	t.onResults = false
	off, ok := db.ErrorOffset(r.Err)
	if !ok {
		t.editor.SetCursorOffset(r.Start)
		return msg
	}
	pos := min(r.Start+off, len(text))
	t.editor.SetCursorOffset(pos)
	before := text[:pos]
	line := strings.Count(before, "\n") + 1
	col := utf8.RuneCountInString(before[strings.LastIndexByte(before, '\n')+1:]) + 1
	return fmt.Sprintf("line %d, col %d: %s", line, col, msg)
}
```

`statusLine` hints, after the run-all hint:

```go
		keyHint(t.keys, keymap.Console, keymap.RunFromCursor, "run from here"),
```

- [ ] **Step 8: Run** `go test -race ./internal/ui ./internal/sqlx ./internal/ui/keymap` → PASS (fix any golden/hint test that lists the console hints exactly).

- [ ] **Step 9: Commit**

```bash
git add internal/sqlx internal/ui/keymap internal/ui/console.go internal/ui/console_test.go
git commit -m "feat(ui): <Space>R 从光标执行到末尾，失败时显示第几条"
```

---

### Task 2: `:e` opens a file console

**Files:**
- Create: `internal/ui/consolefile.go`, `internal/ui/consolefile_test.go`
- Modify: `internal/history/history.go:20-24` (`RecentFile`)
- Modify: `internal/ui/console.go` (`consoleTab` struct, `Key`, `Title`, `Init`, `saveDraft`)
- Modify: `internal/ui/app.go` (`runCommand` ~657, `Update` near `case overlay.CommandMsg`, `location` ~1379)

**Interfaces:**
- Consumes: `consoleTabKey`, `newConsoleTab`, `m.openTab`, `m.ensureMeta`, `m.contextNamespace`.
- Produces:
  - `type consoleFile struct { path string; modTime time.Time; size int64; saved string; isNew bool }`
  - `func fileTabKey(path, conn string, ns db.Namespace) string`
  - `func absPath(p string) (string, error)`, `func tildePath(p string) string`
  - `func loadSQLFile(path string) (consoleFile, error)`
  - `type fileReadMsg struct { conn string; ns db.Namespace; file consoleFile; err error }`
  - `func (t *consoleTab) dirty() bool`
  - `func (m *Model) openFile(conn string, ns db.Namespace, path string) tea.Cmd`
  - `history.RecentFile = "file"`

- [ ] **Step 1: Write the failing tests** (`internal/ui/consolefile_test.go`)

```go
package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// ex runs an ex command as if typed on the command line.
func ex(t *testing.T, m *Model, text string) {
	t.Helper()
	drain(t, m, func() tea.Msg { return overlay.CommandMsg{Text: text} })
}

// writeSQL writes text to name in a new temp dir and returns its path.
func writeSQL(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func fileConsole(t *testing.T, m *Model) *consoleTab {
	t.Helper()
	c, ok := m.activeTab().(*consoleTab)
	if !ok || c.file == nil {
		t.Fatalf("no file console: %T\n%s", m.activeTab(), screen(m))
	}
	return c
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEditOpensAFileConsoleOnTheTreeConnection(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;\n")
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	if c.conn != "local" || c.editor.Value() != "select 1;\n" || c.Title() != "a.sql" {
		t.Fatalf("conn %s title %q text %q", c.conn, c.Title(), c.editor.Value())
	}
	if !strings.Contains(statusLine(m), "a.sql") {
		t.Fatalf("status bar shows the path: %s", statusLine(m))
	}
}

func TestEditInheritsTheTabNamespace(t *testing.T) {
	m, _ := newTestModel(t)
	openUsers(t, m)
	ex(t, m, "e "+writeSQL(t, "a.sql", ""))
	if c := fileConsole(t, m); c.conn != "local" || c.ns != appNS {
		t.Fatalf("on %s %+v", c.conn, c.ns)
	}
}

func TestEditNeedsAConnectionAndAName(t *testing.T) {
	b := newFakeBackend()
	b.conns = nil
	m := New(b, newFakeStore(), config.Settings{PageSize: 2, MaxColumnWidth: 40, MaxRows: 10})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	ex(t, m, "e "+writeSQL(t, "a.sql", ""))
	if !strings.Contains(m.status, "open a console or select a connection first") {
		t.Fatalf("status = %q", m.status)
	}
	m2, _ := newTestModel(t)
	ex(t, m2, "e")
	if !strings.Contains(m2.status, ":e needs a file name") {
		t.Fatalf("status = %q", m2.status)
	}
}

func TestEditSameFileSwitchesToItsTab(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;")
	t.Chdir(filepath.Dir(p))
	for _, spelling := range []string{p, "a.sql", "./a.sql"} {
		ex(t, m, "e "+spelling)
	}
	if len(m.tabs) != 1 {
		t.Fatalf("tabs = %d, want 1", len(m.tabs))
	}
}

func TestEditSameFileOnAnotherConnection(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	press(t, m, "ctrl+h", "g", "g", "j") // tree: prod
	ex(t, m, "e "+p)
	if len(m.tabs) != 2 || fileConsole(t, m).conn != "prod" {
		t.Fatalf("tabs = %d", len(m.tabs))
	}
}

func TestEditMissingFileIsNew(t *testing.T) {
	m, _ := newTestModel(t)
	ex(t, m, "e "+filepath.Join(t.TempDir(), "b.sql"))
	if c := fileConsole(t, m); c.Title() != "b.sql [new]" || c.editor.Value() != "" {
		t.Fatalf("title %q", c.Title())
	}
}

func TestEditPathWithSpaces(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "my file.sql", "select 1;")
	ex(t, m, "e "+p)
	if c := fileConsole(t, m); c.editor.Value() != "select 1;" {
		t.Fatalf("text %q", c.editor.Value())
	}
}

func TestEditRejectsWhatTheEditorCannotHold(t *testing.T) {
	for name, c := range map[string]struct {
		content []byte
		dir     bool
		want    string
	}{
		"big.sql":  {content: []byte(strings.Repeat("x", 2<<20+1)), want: "big.sql is 2.0 MB: too large for the editor"},
		"bin.sql":  {content: []byte{0xff, 0xfe, 'a'}, want: "bin.sql is not a UTF-8 text file"},
		"nul.sql":  {content: []byte("a\x00b"), want: "nul.sql is not a UTF-8 text file"},
		"dir.sql":  {dir: true, want: "dir.sql is a directory"},
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := newTestModel(t)
			p := filepath.Join(t.TempDir(), name)
			var err error
			if c.dir {
				err = os.Mkdir(p, 0o755)
			} else {
				err = os.WriteFile(p, c.content, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			ex(t, m, "e "+p)
			if len(m.tabs) != 0 || !strings.Contains(m.status, c.want) {
				t.Fatalf("tabs %d status %q", len(m.tabs), m.status)
			}
		})
	}
}

func TestEditDropsTheBOM(t *testing.T) {
	m, _ := newTestModel(t)
	ex(t, m, "e "+writeSQL(t, "a.sql", "﻿select 1;"))
	if c := fileConsole(t, m); c.editor.Value() != "select 1;" || c.dirty() {
		t.Fatalf("text %q dirty %v", c.editor.Value(), c.dirty())
	}
}

func TestFileConsoleMarksChangesAndKeepsNoDraft(t *testing.T) {
	m, _ := newTestModel(t)
	ex(t, m, "e "+writeSQL(t, "a.sql", "select 1;"))
	c := fileConsole(t, m)
	press(t, m, "A", "x", "esc")
	if c.Title() != "a.sql [+]" {
		t.Fatalf("title %q", c.Title())
	}
	st := m.store.(*fakeStore)
	st.mu.Lock()
	defer st.mu.Unlock()
	for k := range st.drafts {
		if strings.HasPrefix(k, "file:") {
			t.Fatalf("draft saved for %s", k)
		}
	}
}
```

Add `"github.com/shepherd-yun/sqlvim/internal/config"` to the imports (used by `TestEditNeedsAConnectionAndAName`).

- [ ] **Step 2: Run** `go test ./internal/ui -run 'TestEdit|TestFileConsole'` → FAIL to compile (`c.file`, `c.dirty` undefined).

- [ ] **Step 3: Implement `internal/ui/consolefile.go`**

```go
package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// maxFileSize bounds the files :e opens into a console.
const maxFileSize = 2 << 20

// consoleFile is the .sql file a console edits.
type consoleFile struct {
	path    string // absolute and clean
	modTime time.Time
	size    int64
	saved   string // the text on disk when read or last written
	isNew   bool   // there is no such file on disk yet
}

func (f *consoleFile) name() string { return filepath.Base(f.path) }

func fileTabKey(path, conn string, ns db.Namespace) string {
	return "file:" + path + "|" + consoleTabKey(conn, ns)
}

// absPath expands a leading ~ and makes p absolute and clean.
func absPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	return filepath.Abs(p)
}

// tildePath shortens a path under the home directory to ~/….
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.Join("~", rel)
}

func humanSize(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// fileReadMsg carries a file :e read for a new console on conn.
type fileReadMsg struct {
	conn string
	ns   db.Namespace
	file consoleFile
	err  error
}

func readSQLFile(conn string, ns db.Namespace, path string) tea.Cmd {
	return func() tea.Msg {
		f, err := loadSQLFile(path)
		return fileReadMsg{conn: conn, ns: ns, file: f, err: err}
	}
}

// loadSQLFile reads path; a missing file is a new, empty one.
func loadSQLFile(path string) (consoleFile, error) {
	name := filepath.Base(path)
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return consoleFile{path: path, isNew: true}, nil
	case err != nil:
		return consoleFile{}, err
	case info.IsDir():
		return consoleFile{}, fmt.Errorf("%s is a directory", name)
	case info.Size() > maxFileSize:
		return consoleFile{}, fmt.Errorf("%s is %s: too large for the editor", name, humanSize(info.Size()))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return consoleFile{}, err
	}
	text := string(b)
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return consoleFile{}, fmt.Errorf("%s is not a UTF-8 text file", name)
	}
	text = strings.TrimPrefix(text, "﻿")
	return consoleFile{path: path, modTime: info.ModTime(), size: info.Size(), saved: text}, nil
}
```

`internal/history/history.go`, in the kinds block:

```go
	RecentFile      = "file" // Name holds the file's absolute path
```

`internal/ui/console.go`:

- struct field after `scratchKey`: `file *consoleFile // the .sql file this console edits; nil for a plain console`
- `Key()`: first line `if t.file != nil { return fileTabKey(t.file.path, t.conn, t.ns) }`
- `Title()`: first lines

```go
	if t.file != nil {
		switch {
		case t.file.isNew:
			return t.file.name() + " [new]"
		case t.dirty():
			return t.file.name() + " [+]"
		}
		return t.file.name()
	}
```

- new method:

```go
// dirty reports a file console whose text differs from the file's.
func (t *consoleTab) dirty() bool { return t.file != nil && t.editor.Value() != t.file.saved }
```

- `Init()` and `saveDraft()`: change `if t.scratchKey != ""` to `if t.scratchKey != "" || t.file != nil`.

`internal/ui/app.go`:

In `runCommand`, right after the `set ` prefix check:

```go
	verb, arg, _ := strings.Cut(cmd, " ")
	arg = strings.TrimSpace(arg)
	if verb == "e" || verb == "edit" {
		return m.editFile(arg)
	}
```

New methods (place after `openScriptConsole`):

```go
// editFile carries out :e path on the connection in context.
func (m *Model) editFile(arg string) tea.Cmd {
	if arg == "" {
		m.setStatus(":e needs a file name", true)
		return nil
	}
	path, err := absPath(arg)
	if err != nil {
		m.setStatus(err.Error(), true)
		return nil
	}
	conn, ns := m.contextNamespace()
	if conn == "" {
		m.setStatus("open a console or select a connection first", true)
		return nil
	}
	return m.openFile(conn, ns, path)
}

// openFile switches to path's console on conn, or reads path for a new one.
func (m *Model) openFile(conn string, ns db.Namespace, path string) tea.Cmd {
	key := fileTabKey(path, conn, ns)
	for i, t := range m.tabs {
		if t.Key() == key {
			m.gotoTab(i)
			return nil
		}
	}
	return readSQLFile(conn, ns, path)
}

// fileRead opens the console for a file openFile read.
func (m *Model) fileRead(msg fileReadMsg) tea.Cmd {
	if msg.err != nil {
		m.setStatus(msg.err.Error(), true)
		return nil
	}
	f := msg.file
	rec := history.Recent{Kind: history.RecentFile, Conn: msg.conn, Database: msg.ns.Database, Schema: msg.ns.Schema, Name: f.path}
	return tea.Batch(m.openTab(fileTabKey(f.path, msg.conn, msg.ns), rec, func(id int) tab {
		c := newConsoleTab(id, m.backend, m.store, m.catalogs, msg.conn, msg.ns, m.settings, m.timings)
		c.keys, c.res.keys = m.keys, m.keys
		c.file = &f
		c.editor.SetValue(f.saved)
		return c
	}), m.ensureMeta(msg.conn, msg.ns.Database))
}
```

In `Update`, next to `case overlay.CommandMsg:`:

```go
	case fileReadMsg:
		return m, m.fileRead(msg)
```

In `location()`, replace the final `return` with:

```go
	loc := strings.Join(parts, "/")
	if c, ok := m.activeTab().(*consoleTab); ok && m.focus == focusTabs && c.file != nil {
		loc += "  " + tildePath(c.file.path)
	}
	return loc
```

- [ ] **Step 4: Run** `go test -race ./internal/ui -run 'TestEdit|TestFileConsole'` → PASS. Then `make test`.

- [ ] **Step 5: Commit**

```bash
git add internal/history/history.go internal/ui/consolefile.go internal/ui/consolefile_test.go internal/ui/console.go internal/ui/app.go
git commit -m "feat(ui): :e 在当前连接上打开 SQL 文件"
```

---

### Task 3: `:w`, `:w <path>`, `:w!`, `:wq`

**Files:**
- Modify: `internal/ui/consolefile.go` (`writeSQLFile`, messages)
- Modify: `internal/ui/console.go` (`save`, `saved`, `Update` cases)
- Modify: `internal/ui/app.go` (`runCommand`, `writeFile`)
- Test: `internal/ui/consolefile_test.go`

**Interfaces:**
- Consumes: Task 2's `consoleFile`, `absPath`, `tildePath`, `fileTabKey`, `dirty`; existing `closeTabConfirmedMsg`, `openOverlay`, `overlay.NewConfirm(title, lines, expect, okMsg)`, `plural`.
- Produces:
  - `func writeSQLFile(path, text string, known *consoleFile, force bool) (f consoleFile, conflict string, err error)`
  - `type fileSavedMsg struct { tabID int; file consoleFile; conflict string; quit bool; err error }`
  - `type saveConfirmedMsg struct { tabID int; path string; quit bool }`
  - `func (t *consoleTab) save(path string, force, quit bool) tea.Cmd`
  - `func (m *Model) writeFile(arg string, force, quit bool) tea.Cmd`

- [ ] **Step 1: Write the failing tests** (append to `consolefile_test.go`; add imports `io/fs`, `time`)

```go
func TestWriteSavesTheFileKeepingItsMode(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	c.editor.SetValue("select 2;\n")
	ex(t, m, "w")
	if got := readFile(t, p); got != "select 2;\n" {
		t.Fatalf("disk %q", got)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if c.Title() != "a.sql" || !strings.Contains(c.status, "written, 1 line") {
		t.Fatalf("title %q status %q", c.Title(), c.status)
	}
}

func TestWriteInAFileConsoleLeavesStagedEdits(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	stageName(t, c)
	ex(t, m, "w")
	if c.changes() != 1 || c.res.pendingSQL != nil {
		t.Fatalf("changes %d, preview %v: :w only saves the file", c.changes(), c.res.pendingSQL)
	}
	if readFile(t, p) != c.editor.Value() {
		t.Fatal("file not written")
	}
}

func TestWriteInAPlainConsoleIsUnchanged(t *testing.T) {
	m, _ := newTestModel(t)
	openLocalConsole(t, m)
	ex(t, m, "w")
	if !strings.Contains(m.status, "nothing to write") {
		t.Fatalf("status %q", m.status)
	}
	ex(t, m, "w!")
	if !strings.Contains(m.status, "nothing to write") {
		t.Fatalf(":w! without a file: status %q", m.status)
	}
}

// touch rewrites p with text and a later mtime.
func touch(t *testing.T, p, text string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
}

func TestWriteAsksWhenTheFileChangedOnDisk(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;")
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	c.editor.SetValue("mine")
	touch(t, p, "theirs")
	ex(t, m, "w")
	if !strings.Contains(screen(m), "changed on disk") || readFile(t, p) != "theirs" {
		t.Fatalf("no confirm:\n%s", screen(m))
	}
	press(t, m, "n")
	if readFile(t, p) != "theirs" {
		t.Fatal("n keeps the disk file")
	}
	ex(t, m, "w")
	press(t, m, "y")
	if readFile(t, p) != "mine" {
		t.Fatal("y overwrites")
	}
	touch(t, p, "theirs again")
	ex(t, m, "w!")
	if readFile(t, p) != "mine" {
		t.Fatal(":w! writes without asking")
	}
}

func TestWriteAsTurnsAPlainConsoleIntoAFileConsole(t *testing.T) {
	m, _ := newTestModel(t)
	c := openLocalConsole(t, m)
	c.editor.SetValue("select 1;")
	p := filepath.Join(t.TempDir(), "b.sql")
	ex(t, m, "w "+p)
	if readFile(t, p) != "select 1;" || c.file == nil || c.file.path != p || c.Title() != "b.sql" {
		t.Fatalf("file %+v title %q", c.file, c.Title())
	}
}

func TestWriteAsAPathOpenElsewhereFails(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	openLocalConsole(t, m)
	ex(t, m, "w "+p)
	if !strings.Contains(m.status, "is open in another tab") {
		t.Fatalf("status %q", m.status)
	}
}

func TestWriteAsAnExistingFileAsks(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "b.sql", "keep")
	c := openLocalConsole(t, m)
	c.editor.SetValue("new")
	ex(t, m, "w "+p)
	if !strings.Contains(screen(m), "overwrite") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "n")
	if readFile(t, p) != "keep" || c.file != nil {
		t.Fatal("n keeps the file and the console plain")
	}
}

func TestWriteCreatesANewFileButNotItsDirectory(t *testing.T) {
	m, _ := newTestModel(t)
	p := filepath.Join(t.TempDir(), "b.sql")
	ex(t, m, "e "+p)
	c := fileConsole(t, m)
	c.editor.SetValue("x")
	ex(t, m, "w")
	if readFile(t, p) != "x" || c.Title() != "b.sql" {
		t.Fatalf("title %q", c.Title())
	}
	ex(t, m, "w "+filepath.Join(t.TempDir(), "missing", "c.sql"))
	if !strings.Contains(c.status, "write c.sql") {
		t.Fatalf("status %q", c.status)
	}
}

func TestWriteQuitClosesOnceWritten(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	fileConsole(t, m).editor.SetValue("x")
	ex(t, m, "wq")
	if len(m.tabs) != 0 || readFile(t, p) != "x" {
		t.Fatalf("tabs %d", len(m.tabs))
	}
	openLocalConsole(t, m)
	ex(t, m, "wq")
	if len(m.tabs) != 1 || !strings.Contains(m.status, ":wq needs a file") {
		t.Fatalf("plain console: tabs %d status %q", len(m.tabs), m.status)
	}
}

func TestWriteFollowsSymlinks(t *testing.T) {
	m, _ := newTestModel(t)
	real := writeSQL(t, "real.sql", "a")
	link := filepath.Join(t.TempDir(), "link.sql")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	ex(t, m, "e "+link)
	fileConsole(t, m).editor.SetValue("b")
	ex(t, m, "w")
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&fs.ModeSymlink == 0 || readFile(t, real) != "b" {
		t.Fatalf("link %v err %v real %q", info.Mode(), err, readFile(t, real))
	}
}

func TestEditsDuringASaveStayMarked(t *testing.T) {
	m, _ := newTestModel(t)
	ex(t, m, "e "+writeSQL(t, "a.sql", ""))
	c := fileConsole(t, m)
	c.editor.SetValue("first")
	cmd := c.save("", false, false)
	c.editor.SetValue("second")
	drain(t, m, cmd)
	if c.Title() != "a.sql [+]" {
		t.Fatalf("title %q", c.Title())
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/ui -run 'TestWrite|TestEditsDuringASave'` → FAIL (`c.save` undefined).

- [ ] **Step 3: Implement**

Append to `internal/ui/consolefile.go` (imports unchanged):

```go
type (
	// fileSavedMsg reports a save: written (file), refused pending a
	// question (conflict), or failed (err).
	fileSavedMsg struct {
		tabID    int
		file     consoleFile
		conflict string
		quit     bool // :wq: close the tab once written
		err      error
	}
	// saveConfirmedMsg saves again, overwriting, after the user agreed.
	saveConfirmedMsg struct {
		tabID int
		path  string
		quit  bool
	}
)

func (m fileSavedMsg) TabID() int     { return m.tabID }
func (m saveConfirmedMsg) TabID() int { return m.tabID }

// writeSQLFile writes text to path atomically, through a symlink to its
// target. Unless force, it writes nothing and returns a question instead when
// path changed on disk since known was read, or when path is some other
// existing file (known == nil).
func writeSQLFile(path, text string, known *consoleFile, force bool) (consoleFile, string, error) {
	name := filepath.Base(path)
	fail := func(err error) (consoleFile, string, error) {
		return consoleFile{}, "", fmt.Errorf("write %s: %w", name, err)
	}
	target := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		target = r
	}
	perm := fs.FileMode(0o644)
	info, err := os.Stat(target)
	switch {
	case err == nil:
		if info.IsDir() {
			return fail(errors.New("is a directory"))
		}
		perm = info.Mode().Perm()
		if !force {
			asked := consoleFile{path: path}
			if known == nil {
				return asked, "overwrite " + tildePath(path) + "?", nil
			}
			if known.isNew || !info.ModTime().Equal(known.modTime) || info.Size() != known.size {
				return asked, name + " changed on disk since it was opened — overwrite?", nil
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fail(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // fails harmlessly once renamed
	if _, err := tmp.WriteString(text); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fail(err)
	}
	if info, err = os.Stat(target); err != nil {
		return fail(err)
	}
	return consoleFile{path: path, modTime: info.ModTime(), size: info.Size(), saved: text}, "", nil
}

// lineCount counts the lines of text the way editors do.
func lineCount(text string) int {
	n := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}
```

`internal/ui/console.go` — new methods (after `dirty`):

```go
// save writes the editor text to path, or to the console's file when path
// is "". quit closes the tab once written.
func (t *consoleTab) save(path string, force, quit bool) tea.Cmd {
	var known *consoleFile
	if t.file != nil && (path == "" || path == t.file.path) {
		f := *t.file
		known, path = &f, f.path
	}
	if path == "" {
		t.setStatus("no file name · :w <path>", true)
		return nil
	}
	id, text := t.id, t.editor.Value()
	return func() tea.Msg {
		f, conflict, err := writeSQLFile(path, text, known, force)
		return fileSavedMsg{tabID: id, file: f, conflict: conflict, quit: quit, err: err}
	}
}

func (t *consoleTab) saved(m fileSavedMsg) tea.Cmd {
	switch {
	case m.err != nil:
		t.setStatus(m.err.Error(), true)
		return nil
	case m.conflict != "":
		ok := saveConfirmedMsg{tabID: t.id, path: m.file.path, quit: m.quit}
		return openOverlay(overlay.NewConfirm("Write file", []string{m.conflict}, "", ok))
	}
	f := m.file
	t.file = &f
	t.setStatus(fmt.Sprintf("%q written, %s", tildePath(f.path), plural(lineCount(f.saved), "line")), false)
	if m.quit {
		id := t.id
		return func() tea.Msg { return closeTabConfirmedMsg{tabID: id} }
	}
	return nil
}
```

(Check `plural(1, "line")` returns `1 line`; it is in `datatab_edit.go:30`.)

In `consoleTab.Update`'s switch:

```go
	case fileSavedMsg:
		return t.saved(m)
	case saveConfirmedMsg:
		return t.save(m.path, true, m.quit)
```

`internal/ui/app.go` — in `runCommand`, after the `e`/`edit` check from Task 2:

```go
	switch verb {
	case "w", "write", "w!", "write!", "wq":
		c, _ := m.activeTab().(*consoleTab)
		if arg != "" || verb != "w" && verb != "write" || c != nil && c.file != nil {
			return m.writeFile(arg, strings.HasSuffix(verb, "!"), verb == "wq")
		}
	}
```

New method (after `fileRead`):

```go
// writeFile carries out :w [path], :w! [path] and :wq in a console.
func (m *Model) writeFile(arg string, force, quit bool) tea.Cmd {
	c, ok := m.activeTab().(*consoleTab)
	if !ok {
		m.setStatus("only a console can be written to a file", true)
		return nil
	}
	if arg == "" && c.file == nil {
		if quit {
			m.setStatus(":wq needs a file · :w <path> first", true)
			return nil
		}
		return m.runCommand("w") // :w! in a plain console: nothing to force
	}
	path := ""
	if arg != "" {
		p, err := absPath(arg)
		if err != nil {
			m.setStatus(err.Error(), true)
			return nil
		}
		if key := fileTabKey(p, c.conn, c.ns); key != c.Key() {
			for _, o := range m.tabs {
				if o.Key() == key {
					m.setStatus(tildePath(p)+" is open in another tab", true)
					return nil
				}
			}
		}
		path = p
	}
	return c.save(path, force, quit)
}
```

- [ ] **Step 4: Run** `go test -race ./internal/ui -run 'TestWrite|TestEditsDuringASave|TestCommandLine'` → PASS; then `make test` (existing `:w` tests in `modify_test.go` must still pass).

- [ ] **Step 5: Commit**

```bash
git add internal/ui/consolefile.go internal/ui/consolefile_test.go internal/ui/console.go internal/ui/app.go
git commit -m "feat(ui): 文件 console 的 :w、:w <path>、:w! 与 :wq"
```

---

### Task 4: Unsaved files are never dropped silently

**Files:**
- Modify: `internal/ui/app.go` (`requestClose` ~697, `unsavedTables` ~740, `closeTabsOf` ~1230; add `path/filepath` import if missing)
- Test: `internal/ui/consolefile_test.go`

**Interfaces:**
- Consumes: `consoleTab.dirty()`, `consoleFile.name()`, `shutdownCmd`, `consoleTab.clearTx`, `connRemovedMsg{name, err}`, `connUpdatedMsg`.

- [ ] **Step 1: Write the failing tests**

```go
func openDirtyFile(t *testing.T, m *Model) *consoleTab {
	t.Helper()
	ex(t, m, "e "+writeSQL(t, "a.sql", ""))
	c := fileConsole(t, m)
	c.editor.SetValue("unsaved")
	return c
}

func TestClosingAnUnsavedFileAsks(t *testing.T) {
	m, _ := newTestModel(t)
	openDirtyFile(t, m)
	ex(t, m, "q")
	if len(m.tabs) != 1 || !strings.Contains(screen(m), "a.sql has unsaved changes") {
		t.Fatalf("tabs %d\n%s", len(m.tabs), screen(m))
	}
	press(t, m, "y")
	if len(m.tabs) != 0 {
		t.Fatal("y closes")
	}
	openDirtyFile(t, m)
	ex(t, m, "q!")
	if len(m.tabs) != 0 {
		t.Fatal(":q! closes without asking")
	}
	ex(t, m, "e "+writeSQL(t, "clean.sql", ""))
	ex(t, m, "q")
	if len(m.tabs) != 0 {
		t.Fatal("a saved file closes without asking")
	}
}

func TestQuittingWithAnUnsavedFileAsks(t *testing.T) {
	m, _ := newTestModel(t)
	openDirtyFile(t, m)
	ex(t, m, "qa")
	if !strings.Contains(screen(m), "Unsaved changes in: a.sql") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	forced, _ := newTestModel(t)
	openDirtyFile(t, forced)
	if !drainQuit(t, forced, "qa!") {
		t.Fatal(":qa! quits")
	}
}

func TestRemovingTheConnectionKeepsAnUnsavedFile(t *testing.T) {
	m, _ := newTestModel(t)
	c := openDirtyFile(t, m)
	ex(t, m, "e "+writeSQL(t, "clean.sql", ""))
	drain(t, m, func() tea.Msg { return connRemovedMsg{name: "local"} })
	if len(m.tabs) != 1 || m.tabs[0] != c || c.editor.Value() != "unsaved" {
		t.Fatalf("tabs %d", len(m.tabs))
	}
	if !strings.Contains(c.status, "the file stays open") {
		t.Fatalf("status %q", c.status)
	}
}

// drainQuit runs an ex command and reports whether it quit.
func drainQuit(t *testing.T, m *Model, text string) bool {
	t.Helper()
	return drain(t, m, func() tea.Msg { return overlay.CommandMsg{Text: text} })
}
```

- [ ] **Step 2: Run** `go test ./internal/ui -run 'TestClosingAnUnsavedFile|TestQuittingWithAnUnsavedFile|TestRemovingTheConnectionKeeps'` → FAIL.

- [ ] **Step 3: Implement** in `internal/ui/app.go`

`requestClose`, before the open-transaction check:

```go
	if c, ok := t.(*consoleTab); ok && c.dirty() && !force {
		lines := []string{c.file.name() + " has unsaved changes."}
		if c.openTx() {
			lines = append(lines, "It also has an open transaction ("+c.TxStatus()+"), which closing rolls back.")
		}
		m.overlay = overlay.NewConfirm("Unsaved changes", append(lines, "Closing the tab discards them."), "", closeTabConfirmedMsg{tabID: t.ID()})
		return nil
	}
```

`unsavedTables`, inside the loop after the stager check:

```go
		if c, ok := t.(*consoleTab); ok && c.dirty() {
			out = append(out, c.file.name())
		}
```

Update its comment: `// unsavedTables names the tabs with staged changes ("users (3)") and the unsaved files.`

`closeTabsOf`:

```go
// closeTabsOf closes every tab on conn, except file consoles with unsaved
// changes: those only lose their session, so the text is not lost.
func (m *Model) closeTabsOf(conn string) tea.Cmd {
	var cmds []tea.Cmd
	for i := len(m.tabs) - 1; i >= 0; i-- {
		if c, _ := m.tabs[i].Target(); c != conn {
			continue
		}
		if fc, ok := m.tabs[i].(*consoleTab); ok && fc.dirty() {
			cmds = append(cmds, shutdownCmd(fc))
			fc.clearTx()
			fc.setStatus("connection "+conn+" changed: its session is closed, the file stays open · :w to save", true)
			continue
		}
		cmds = append(cmds, m.closeTab(i))
	}
	return tea.Batch(cmds...)
}
```

- [ ] **Step 4: Run** `go test -race ./internal/ui` → PASS (including the existing `TestQuitAsksWithOpenTransaction` / `TestCloseTabAsksWithOpenTransaction`).

- [ ] **Step 5: Commit**

```bash
git add internal/ui/app.go internal/ui/consolefile_test.go
git commit -m "feat(ui): 关闭、退出与修改连接时保护未保存的文件"
```

---

### Task 5: Files in `<Space>f`

**Files:**
- Modify: `internal/ui/recent.go` (`recentKinds`, `recentLabel`, `openRecent`, `touchActive`)
- Test: `internal/ui/recent_test.go`

**Interfaces:**
- Consumes: `history.RecentFile`, `m.openFile`, `tildePath`, `consoleTab.file`.

- [ ] **Step 1: Write the failing test** (append to `recent_test.go`; imports `os`, `path/filepath` as needed)

```go
func TestRecentReopensAFile(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "select 1;")
	ex(t, m, "e "+p)
	ex(t, m, "q")
	press(t, m, "space", "f")
	if s := screen(m); !strings.Contains(s, "file") || !strings.Contains(s, "a.sql") || !strings.Contains(s, "local") {
		t.Fatalf("screen:\n%s", s)
	}
	press(t, m, "enter")
	if c := fileConsole(t, m); c.editor.Value() != "select 1;" || c.conn != "local" {
		t.Fatalf("conn %s text %q", c.conn, c.editor.Value())
	}
}

func TestRecentFileLabel(t *testing.T) {
	r := history.Recent{Kind: history.RecentFile, Conn: "local", Database: "app", Name: "/tmp/x/a.sql"}
	if got := recentLabel(r); !strings.HasPrefix(got, "file     /tmp/x/a.sql  local/app") {
		t.Fatalf("label %q", got)
	}
}

func TestSwitchingToAFileConsoleRecordsTheFile(t *testing.T) {
	m, _ := newTestModel(t)
	p := writeSQL(t, "a.sql", "")
	ex(t, m, "e "+p)
	openLocalConsole(t, m)
	press(t, m, "H") // back to the file console
	st := m.store.(*fakeStore)
	st.mu.Lock()
	last := st.recents[len(st.recents)-1]
	st.mu.Unlock()
	if last.Kind != history.RecentFile || last.Name != p {
		t.Fatalf("last recent %+v", last)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/ui -run 'TestRecent|TestSwitchingToAFileConsole'` → FAIL.

- [ ] **Step 3: Implement** in `internal/ui/recent.go`

```go
var recentKinds = map[string]string{
	history.RecentData:      "data",
	history.RecentStructure: "struct",
	history.RecentConsole:   "console",
	history.RecentFile:      "file",
}

func recentLabel(r history.Recent) string {
	parts := []string{r.Database, r.Schema, r.Name}
	if r.Kind == history.RecentFile {
		parts = parts[:2] // Name is the file, shown first
	}
	path := r.Conn
	for _, p := range parts {
		if p != "" {
			path += "/" + p
		}
	}
	if r.Kind == history.RecentFile {
		path = tildePath(r.Name) + "  " + path
	}
	return fmt.Sprintf("%-8s %s  %s", recentKinds[r.Kind], path, r.Time.Local().Format("01-02 15:04"))
}
```

`openRecent`, new case:

```go
	case history.RecentFile:
		return m.openFile(r.Conn, ns, r.Name)
```

`touchActive`, replace the `*consoleTab` case:

```go
	case *consoleTab:
		r.Kind, r.Name = history.RecentConsole, ""
		if c := t.(*consoleTab); c.file != nil {
			r.Kind, r.Name = history.RecentFile, c.file.path
		}
```

(Or switch on `x := t.(type)` and use `x.file`; keep whichever reads cleaner with the other cases.)

Also update the README/help wording of `<Space>f` in Task 6 ("tables, structures, consoles and files").

- [ ] **Step 4: Run** `go test -race ./internal/ui` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/recent.go internal/ui/recent_test.go
git commit -m "feat(ui): 最近列表记录并重新打开 SQL 文件"
```

---

### Task 6: Docs

**Files:**
- Modify: `CHANGELOG.md`, `README.md`, `README.zh-CN.md`, `docs/superpowers/specs/2026-10-13-sqlvim-design.md`, `docs/superpowers/specs/2026-10-16-sql-file-console-design.md`, `internal/ui/keymap/defaults.go` (`Recent` description)

- [ ] **Step 1: CHANGELOG** — under `## [Unreleased]` → `### Added`, at the top:

```markdown
- Open `.sql` files in a console: `:e <file>` opens one on the connection and database in
  context (`~` and relative paths work; files up to 2 MB). It runs like any console, the tab
  shows `[+]` while it has unsaved changes, and `:w` saves it (`:w <file>` saves as, `:wq`
  saves and closes, `:w!` overwrites a file changed on disk). In a file console `:w` only
  saves the file; staged result edits are submitted with `<Space>w`. `:w <file>` in a plain
  console turns it into a file console. Closing, quitting or editing the connection never
  drops unsaved text silently. Files appear in `<Space>f`. A leading UTF-8 BOM is dropped.
- `<Space>R` in the console runs from the statement under the cursor to the end. When one of
  several statements fails, the status names it (`statement 2/3 failed`) and the cursor
  moves to it.
```

- [ ] **Step 2: README.md** — after the `Enter` / `R` console row (line ~151) add:

```markdown
| `<Space>R` | Console: run from the statement under the cursor to the end |
```

After the `:q` / `:qa` row (~175) add:

```markdown
| `:e <file>` | Open a `.sql` file in a console on the connection / database in context |
| `:w` / `:w <file>` / `:wq` | File console: save / save as (a plain console becomes a file console) / save and close; `:w!` overwrites a file changed on disk |
```

Change the `<Space>f` row text to `Recently opened tables, structures, consoles and files`.

- [ ] **Step 3: README.zh-CN.md** — the same rows in Chinese, at the matching places:

```markdown
| `<Space>R` | 控制台：从光标所在语句执行到末尾 |
```

```markdown
| `:e <file>` | 在当前上下文的连接 / 数据库上，用控制台打开 `.sql` 文件 |
| `:w` / `:w <file>` / `:wq` | 文件控制台：保存 / 另存为（普通控制台会变成文件控制台）/ 保存并关闭；`:w!` 覆盖磁盘上已被修改的文件 |
```

and `<Space>f` → `最近打开的表、结构、控制台和文件`.

- [ ] **Step 4: `keymap/defaults.go`** — `Recent` description: `"Recent tabs and files"`. Run `go test ./internal/ui/...` (help/which-key tests may assert the text; update them).

- [ ] **Step 5: Main spec** `docs/superpowers/specs/2026-10-13-sqlvim-design.md` — after the `:q` / `:qa` line (~299) add:

```markdown
- `:e <file>` 用控制台打开 SQL 文件；文件控制台里 `:w` 保存文件而不是提交暂存修改（见 `2026-10-16-sql-file-console-design.md`）
```

- [ ] **Step 6: Feature spec** — set `状态：已实现（A）`, and add a section before §9:

```markdown
## 8.1 实现时的调整

- `:w!` 不带路径时在普通 console 里等同于 `:w`（没有文件可以强制写）。
- 修改或删除连接会关闭它的所有 tab；有未保存修改的文件 console 例外：只关闭它的会话，tab 保留并提示，避免丢失文本。
- 多条执行失败而服务器没有给出错误位置时，光标移到失败语句的开头。
```

- [ ] **Step 7: Verify** — `make test && make lint` → both pass.

- [ ] **Step 8: Commit**

```bash
git add CHANGELOG.md README.md README.zh-CN.md docs/superpowers/specs internal/ui/keymap/defaults.go internal/ui
git commit -m "docs: SQL 文件 console"
```
