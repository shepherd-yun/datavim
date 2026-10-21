# Import Data from File Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Import a CSV, TSV or XLSX file into an existing table, or into a new table inferred from the file, from an Import tab with column mapping and preview; all rows go in one transaction or none do.

**Architecture:** A new terminal-free package `internal/importer` reads files (`Source`), converts cells for a target column (`Target`, `Convert`), infers types for a new table (`Inferrer`), builds batched parameterised INSERTs (`Plan`), and runs the dry run (`Check`) and the import (`Run`) on a `db.Querier`, like `edit.Apply`. The UI adds an `importTab` (keymap context `Import`), opened from the tree with `I` or the `a` menu; it loads a sample in a `tea.Cmd`, lets the user map columns, then check → confirm → run, with progress ticks. The driver contract runs `importer.Run` against MySQL 8 and PostgreSQL 16.

**Tech Stack:** Go 1.26, Bubble Tea v2, `encoding/csv`, `golang.org/x/text/encoding/simplifiedchinese` (GBK), `github.com/xuri/excelize/v2` (already a dependency).

**Spec:** `docs/superpowers/specs/2026-10-16-import-data-design.md`

## Global Constraints

- Formats: `.csv` → CSV, `.tsv` / `.tab` → TSV, `.xlsx` → XLSX, anything else → CSV. Encodings: `UTF-8` (leading BOM dropped) and `GBK`.
- XLSX is opened with `excelize.Options{ShortDatePattern: "yyyy-mm-dd", LongTimePattern: "hh:mm:ss"}` and read as formatted text.
- Batch size: `max(1, min(500, 65535/columns))` rows per INSERT. Values are parameters (`?` on MySQL, `$n` on PostgreSQL); NULL and DEFAULT are written as keywords.
- Savepoint name `datavim_import`, taken before every batch.
- Preview: first 20 rows. Dry run reports at most 50 problems plus the total.
- One transaction: any failure, cancel or lost connection rolls back everything; on MySQL a table the import created is dropped again. Never reconnect and retry an import.
- Safety: read-only connections refuse; a console with an open transaction on the connection refuses; staged changes of the target table refuse (`tableBlocked`); prod asks to type the table name.
- Logs never contain cell values or server error text (which can quote values): only connection, table, row count, duration and whether it failed.
- UI text in English; key names in UI text only through `keyHint`. Checkboxes are `[✓]` / `[ ]`.
- Keys in the Import tab: `j` `k` `gg` `G` `Ctrl-d` `Ctrl-u` move; `[` `]` previous / next section; `i` / `Enter` edit; `x` import / skip a column; `a` / `A` all / none; `t` new table column type; `<Space>w` check and import; `<Space>r` re-read the file; `Ctrl-c` cancels. Tree: `I`.
- Conventional Commits with a scope, Chinese subject like recent history. **No `Co-Authored-By` line.**
- `CHANGELOG.md` `[Unreleased]`; README changes in both `README.md` and `README.zh-CN.md`.
- `make test` and `make lint` pass at the end of every task; Task 7 also needs `make test-integration` (Docker / `orb start`).

## Deviations from the spec

The spec was updated in the same change as this plan (keys, placeholder location, xlsx dates, names). Remaining choices the spec leaves open, fixed here:

- `Float` is its own `Kind` (float, double, real) so that only `decimal`/`numeric` check precision.
- `timetz` and `time with time zone` are `KindOther` (passed through).
- An inferred integer column whose values have leading zeros (`007`, zip codes, phone numbers) is inferred as `varchar`, so the zeros survive.
- MySQL `datetime` columns whose values have fractional seconds are inferred as `datetime(6)`.
- Mapping survives a reload when the file's header is unchanged (changing the NULL text or encoding keeps your picks); a different header resets it.
- The Source section also has a `file` row, so a mistyped path can be fixed in the tab.

## Review Focus

1. **Excel turned a long ID into `1.23456789012346E+17`** and it is imported into an integer column: it must be refused with a message that says to format the column as text, not silently rounded. → test in Task 3.
2. **A GBK CSV opened as UTF-8** (the default): the tab must say the file does not look like UTF-8 and suggest GBK, not show mojibake silently. → test in Task 6 (`BadUTF8`) and Task 9 (hint on screen).
3. **Rows ending with a trailing delimiter** (`1,a,` under a two-column header, common in exports) must import, not fail with "3 cells, the header has 2". → test in Task 4.
4. **Closing the Import tab or quitting while it runs** must cancel the import so it rolls back. → test in Task 11.
5. **A table with a generated column or an auto-increment key that the file does not have**: the generated column must not be offered as a target, and the auto-increment column must not be reported as missing. → test in Task 10.

---

## File Structure

| File | Change |
|---|---|
| `internal/importer/source.go` (new) | `Format`, `Encoding`, `Options`, `DefaultOptions`, `Source`, `Open`, CSV/TSV reader |
| `internal/importer/xlsx.go` (new) | XLSX reader, `Sheets` |
| `internal/importer/convert.go` (new) | `Kind`, `Target`, `TargetOf`, `Cell`, `Convert`, `RowError` |
| `internal/importer/plan.go` (new) | `Column`, `Plan`, `Plan.Row`, `BatchSize`, `Plan.Insert`, `Ident`, `MatchKey`, `UniqueIdents` |
| `internal/importer/infer.go` (new) | `Inferrer` |
| `internal/importer/run.go` (new) | `Sample`, `Read`, `CheckResult`, `Check`, `Job`, `Run`, `LineError`, `LeftoverError` |
| `internal/importer/*_test.go` (new) | unit tests |
| `internal/db/dbtest/import.go` (new), `internal/db/dbtest/contract.go` | driver contract for the importer |
| `internal/ui/keymap/keymap.go`, `defaults.go`, `custom.go`, `keymap_test.go` | `Import` context and actions, Tree `I` |
| `internal/ui/importtab.go` (new) | the Import tab: state, load, mapping, edits, view |
| `internal/ui/importrun.go` (new) | entry from the tree, check, confirm, run, progress, done |
| `internal/ui/app.go`, `internal/ui/nodemenu.go` | message routing, `treeAction`, menu entries |
| `internal/ui/fakes_test.go` | `fakeConn.columns` |
| `internal/ui/import_test.go` (new) | UI tests |
| `go.mod`, `go.sum` | `golang.org/x/text` becomes a direct dependency |
| `CHANGELOG.md`, `README.md`, `README.zh-CN.md`, the spec | docs |

---

### Task 1: CSV and TSV sources

**Files:**
- Create: `internal/importer/source.go`
- Test: `internal/importer/source_test.go`
- Modify: `go.mod`, `go.sum` (via `go mod tidy`)

**Interfaces:**
- Produces:
  - `type Format string` with `CSV`, `TSV`, `XLSX`; `type Encoding string` with `UTF8 = "UTF-8"`, `GBK = "GBK"`
  - `type Options struct { Format Format; Delimiter rune; Encoding Encoding; HeaderRow int; Sheet string }`
  - `func DefaultOptions(path string) Options`
  - `type Source interface { Header() []string; Next() ([]string, error); Line() int; Close() error }`
  - `func Open(path string, o Options) (Source, error)`
  - unexported `rowReader` interface (`Read() ([]string, error)`, `Line() int`, `Close() error`) that Task 2 implements for XLSX via `openXLSX(path, sheet string) (*xlsxReader, error)`

- [ ] **Step 1: Write the failing tests**

`internal/importer/source_test.go`:

```go
package importer

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func writeFile(t *testing.T, name, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type readOut struct {
	Header []string
	Rows   [][]string
	Lines  []int
}

func readAll(t *testing.T, path string, o Options) readOut {
	t.Helper()
	s, err := Open(path, o)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	out := readOut{Header: s.Header()}
	for {
		row, err := s.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out.Rows = append(out.Rows, row)
		out.Lines = append(out.Lines, s.Line())
	}
}

func TestDefaultOptions(t *testing.T) {
	for path, want := range map[string]Format{"a.csv": CSV, "a.TSV": TSV, "a.tab": TSV, "a.xlsx": XLSX, "a.txt": CSV, "a": CSV} {
		o := DefaultOptions(path)
		if o.Format != want || o.Delimiter != ',' || o.Encoding != UTF8 || o.HeaderRow != 1 {
			t.Errorf("DefaultOptions(%q) = %+v", path, o)
		}
	}
}

func TestOpenDelimited(t *testing.T) {
	gbk, err := simplifiedchinese.GBK.NewEncoder().String("名字,城市\n张三,北京\n")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		file string
		data string
		opts func(*Options)
		want readOut
	}{
		{"header", "a.csv", "id,name\n1,Alice\n2,Bob\n", nil,
			readOut{[]string{"id", "name"}, [][]string{{"1", "Alice"}, {"2", "Bob"}}, []int{2, 3}}},
		{"quotes and a newline in a field", "a.csv", "id,note\n1,\"a, \"\"b\"\"\nc\"\n2,x\n", nil,
			readOut{[]string{"id", "note"}, [][]string{{"1", "a, \"b\"\nc"}, {"2", "x"}}, []int{2, 4}}},
		{"utf-8 bom", "a.csv", "\ufeffid,name\n1,a\n", nil,
			readOut{[]string{"id", "name"}, [][]string{{"1", "a"}}, []int{2}}},
		{"semicolon", "a.csv", "id;name\n1;a\n", func(o *Options) { o.Delimiter = ';' },
			readOut{[]string{"id", "name"}, [][]string{{"1", "a"}}, []int{2}}},
		{"tsv", "a.tsv", "id\tname\n1\ta b\n", nil,
			readOut{[]string{"id", "name"}, [][]string{{"1", "a b"}}, []int{2}}},
		{"gbk", "a.csv", gbk, func(o *Options) { o.Encoding = GBK },
			readOut{[]string{"名字", "城市"}, [][]string{{"张三", "北京"}}, []int{2}}},
		{"no header", "a.csv", "1,a\n2,b\n", func(o *Options) { o.HeaderRow = 0 },
			readOut{[]string{"column_1", "column_2"}, [][]string{{"1", "a"}, {"2", "b"}}, []int{1, 2}}},
		{"header on row 2", "a.csv", "exported by x\nid,name\n1,a\n", func(o *Options) { o.HeaderRow = 2 },
			readOut{[]string{"id", "name"}, [][]string{{"1", "a"}}, []int{3}}},
		{"blank and padded header cells, ragged rows", "a.csv", " id ,,name\n1\n2,b,c,d\n", nil,
			readOut{[]string{"id", "column_2", "name"}, [][]string{{"1"}, {"2", "b", "c", "d"}}, []int{2, 3}}},
		{"empty file without a header", "a.csv", "", func(o *Options) { o.HeaderRow = 0 },
			readOut{Header: []string{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := writeFile(t, tt.file, tt.data)
			o := DefaultOptions(p)
			if tt.opts != nil {
				tt.opts(&o)
			}
			if diff := cmp.Diff(tt.want, readAll(t, p, o), cmpopts.EquateEmpty()); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestOpenErrors(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing.csv"), DefaultOptions("x.csv")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	p := writeFile(t, "a.csv", "id\n1\n")
	o := DefaultOptions(p)
	o.HeaderRow = 5
	if _, err := Open(p, o); err == nil || !strings.Contains(err.Error(), "fewer than 5 rows") {
		t.Fatalf("header row past the end: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/importer/`
Expected: FAIL to compile (`undefined: Open`, `DefaultOptions`, …).

- [ ] **Step 3: Implement**

`internal/importer/source.go`:

```go
// Package importer reads CSV, TSV and XLSX files and writes their rows into
// a table: cell conversion, type inference for a new table, batched INSERTs
// in one transaction. It does no terminal IO.
package importer

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// Format is a file format.
type Format string

const (
	CSV  Format = "csv"
	TSV  Format = "tsv"
	XLSX Format = "xlsx"
)

// Encoding is the text encoding of a CSV or TSV file.
type Encoding string

const (
	UTF8 Encoding = "UTF-8"
	GBK  Encoding = "GBK"
)

// Options say how to read a file.
type Options struct {
	Format    Format
	Delimiter rune     // CSV only; TSV always splits on tabs
	Encoding  Encoding // CSV and TSV
	HeaderRow int      // 1-based row holding the column names; 0: none
	Sheet     string   // XLSX; "" is the first sheet
}

// DefaultOptions guesses the options from the file name: comma, UTF-8,
// column names on the first row.
func DefaultOptions(path string) Options {
	o := Options{Format: CSV, Delimiter: ',', Encoding: UTF8, HeaderRow: 1}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsv", ".tab":
		o.Format = TSV
	case ".xlsx":
		o.Format = XLSX
	}
	return o
}

// Source reads the data rows of a file.
type Source interface {
	// Header is the column names: the header row, or column_1, column_2…
	// (as wide as the first row) without one.
	Header() []string
	// Next returns the next data row, or io.EOF.
	Next() ([]string, error)
	// Line is the 1-based line (CSV, TSV) or row (XLSX) where the row Next
	// returned last starts.
	Line() int
	Close() error
}

// Open opens path for reading as o says.
func Open(path string, o Options) (Source, error) {
	var r rowReader
	var err error
	if o.Format == XLSX {
		r, err = openXLSX(path, o.Sheet)
	} else {
		r, err = openDelimited(path, o)
	}
	if err != nil {
		return nil, err
	}
	s, err := newSource(r, o.HeaderRow)
	if err != nil {
		_ = r.Close()
		return nil, err
	}
	return s, nil
}

// rowReader is every row of a file, the header included.
type rowReader interface {
	Read() ([]string, error)
	Line() int
	Close() error
}

// source skips to the header and hands out the rows after it.
type source struct {
	r           rowReader
	header      []string
	pending     []string // the first data row when there is no header row
	pendingLine int
	line        int
}

func newSource(r rowReader, headerRow int) (*source, error) {
	s := &source{r: r}
	if headerRow == 0 {
		row, err := r.Read()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		s.pending, s.pendingLine = row, r.Line()
		s.header = columnNames(make([]string, len(row)))
		return s, nil
	}
	for i := 1; i <= headerRow; i++ {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the file has fewer than %d rows", headerRow)
		}
		if err != nil {
			return nil, err
		}
		if i == headerRow {
			s.header = columnNames(row)
		}
	}
	return s, nil
}

// columnNames trims the header cells and names the blank ones column_N.
func columnNames(row []string) []string {
	out := make([]string, len(row))
	for i, h := range row {
		out[i] = strings.TrimSpace(h)
		if out[i] == "" {
			out[i] = "column_" + strconv.Itoa(i+1)
		}
	}
	return out
}

func (s *source) Header() []string { return s.header }
func (s *source) Line() int        { return s.line }
func (s *source) Close() error     { return s.r.Close() }

func (s *source) Next() ([]string, error) {
	if s.pending != nil {
		row := s.pending
		s.pending, s.line = nil, s.pendingLine
		return row, nil
	}
	row, err := s.r.Read()
	if err != nil {
		return nil, err
	}
	s.line = s.r.Line()
	return row, nil
}

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// delimited reads CSV or TSV.
type delimited struct {
	f    *os.File
	r    *csv.Reader
	line int
}

func openDelimited(path string, o Options) (*delimited, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	br := bufio.NewReader(f)
	if b, _ := br.Peek(len(utf8BOM)); bytes.Equal(b, utf8BOM) {
		_, _ = br.Discard(len(utf8BOM))
	}
	var in io.Reader = br
	if o.Encoding == GBK {
		in = transform.NewReader(br, simplifiedchinese.GBK.NewDecoder())
	}
	r := csv.NewReader(in)
	r.Comma = o.Delimiter
	if o.Format == TSV {
		r.Comma = '\t'
	}
	if r.Comma == 0 {
		r.Comma = ','
	}
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	return &delimited{f: f, r: r}, nil
}

func (d *delimited) Read() ([]string, error) {
	row, err := d.r.Read()
	if err == nil {
		d.line, _ = d.r.FieldPos(0)
	}
	return row, err
}

func (d *delimited) Line() int    { return d.line }
func (d *delimited) Close() error { return d.f.Close() }
```

Add a temporary stub so the package compiles before Task 2 (Task 2 replaces it), in `internal/importer/xlsx.go`:

```go
package importer

import "errors"

type xlsxReader struct{ rowReader }

func openXLSX(string, string) (*xlsxReader, error) { return nil, errors.New("xlsx is not supported yet") }
```

Then make `golang.org/x/text` a direct dependency:

Run: `go mod tidy`
Expected: `go.mod` lists `golang.org/x/text` without `// indirect`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/importer/ && make lint`
Expected: PASS, no lint findings.

- [ ] **Step 5: Commit**

```bash
git add internal/importer go.mod go.sum
git commit -m "feat(importer): 读取 CSV 与 TSV 文件"
```

---

### Task 2: XLSX source

**Files:**
- Modify: `internal/importer/xlsx.go` (replace the stub)
- Test: `internal/importer/xlsx_test.go`

**Interfaces:**
- Consumes: `rowReader`, `Open`, `Options` from Task 1.
- Produces: `func Sheets(path string) ([]string, error)`; `Open` with `Format: XLSX` works.

- [ ] **Step 1: Write the failing tests**

`internal/importer/xlsx_test.go`:

```go
package importer

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/xuri/excelize/v2"
)

// writeXLSX saves a workbook with sheets Data and Other. Data has a header,
// a date cell, a date-time cell, a formatted number, a gap row and a short row.
func writeXLSX(t *testing.T) string {
	t.Helper()
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()
	if err := f.SetSheetName("Sheet1", "Data"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.NewSheet("Other"); err != nil {
		t.Fatal(err)
	}
	date, _ := f.NewStyle(&excelize.Style{NumFmt: 14})
	stamp, _ := f.NewStyle(&excelize.Style{NumFmt: 22})
	thousands, _ := f.NewStyle(&excelize.Style{NumFmt: 3})
	set := func(cell string, v any, style int) {
		t.Helper()
		if err := f.SetCellValue("Data", cell, v); err != nil {
			t.Fatal(err)
		}
		if style != 0 {
			if err := f.SetCellStyle("Data", cell, cell, style); err != nil {
				t.Fatal(err)
			}
		}
	}
	set("A1", "id", 0)
	set("B1", "born", 0)
	set("C1", "seen", 0)
	set("D1", "amount", 0)
	set("A2", 1, 0)
	set("B2", 46024, date)
	set("C2", 46024.5, stamp)
	set("D2", 1234567, thousands)
	set("A4", 2, 0)
	if err := f.SetCellValue("Other", "A1", "x"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "book.xlsx")
	if err := f.SaveAs(p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenXLSX(t *testing.T) {
	p := writeXLSX(t)
	got := readAll(t, p, DefaultOptions(p))
	want := readOut{
		Header: []string{"id", "born", "seen", "amount"},
		Rows:   [][]string{{"1", "2026-01-02", "2026-01-02 12:00", "1,234,567"}, {}, {"2"}},
		Lines:  []int{2, 3, 4},
	}
	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
	o := DefaultOptions(p)
	o.Sheet = "Other"
	if h := readAll(t, p, o).Header; !cmp.Equal(h, []string{"x"}) {
		t.Fatalf("Other header = %v", h)
	}
	o.Sheet = "Nope"
	if _, err := Open(p, o); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("missing sheet: %v", err)
	}
}

func TestSheets(t *testing.T) {
	got, err := Sheets(writeXLSX(t))
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Equal(got, []string{"Data", "Other"}) {
		t.Fatalf("Sheets = %v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/importer/ -run 'XLSX|Sheets'`
Expected: FAIL (`undefined: Sheets`, then "xlsx is not supported yet").

- [ ] **Step 3: Implement**

Replace `internal/importer/xlsx.go`:

```go
package importer

import (
	"errors"
	"io"

	"github.com/xuri/excelize/v2"
)

// xlsxOptions make date cells read as ISO text (2026-01-02, 2026-01-02 12:00).
var xlsxOptions = excelize.Options{ShortDatePattern: "yyyy-mm-dd", LongTimePattern: "hh:mm:ss"}

// xlsxReader streams the formatted rows of one sheet. Rows missing from the
// sheet come back empty, so Line is the sheet's row number.
type xlsxReader struct {
	f    *excelize.File
	rows *excelize.Rows
	line int
}

func openXLSX(path, sheet string) (*xlsxReader, error) {
	f, err := excelize.OpenFile(path, xlsxOptions)
	if err != nil {
		return nil, err
	}
	if sheet == "" {
		list := f.GetSheetList()
		if len(list) == 0 {
			_ = f.Close()
			return nil, errors.New("the workbook has no sheets")
		}
		sheet = list[0]
	}
	rows, err := f.Rows(sheet)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &xlsxReader{f: f, rows: rows}, nil
}

func (x *xlsxReader) Read() ([]string, error) {
	if !x.rows.Next() {
		if err := x.rows.Error(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	x.line++
	return x.rows.Columns()
}

func (x *xlsxReader) Line() int { return x.line }

func (x *xlsxReader) Close() error {
	return errors.Join(x.rows.Close(), x.f.Close())
}

// Sheets lists the sheets of an XLSX file in workbook order.
func Sheets(path string) ([]string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.GetSheetList(), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/importer/ && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/importer
git commit -m "feat(importer): 读取 xlsx，日期单元格读成 ISO 文本"
```

---

### Task 3: Target columns and cell conversion

**Files:**
- Create: `internal/importer/convert.go`
- Test: `internal/importer/convert_test.go`

**Interfaces:**
- Produces:
  - `type Kind int` with `KindString, KindInteger, KindDecimal, KindFloat, KindBool, KindDate, KindDateTime, KindTime, KindOther`
  - `type Target struct { Name, Type string; Kind Kind; Nullable, Default, Unsigned bool; Bits, Precision, Scale, Length int; TZ, MySQL bool }`
  - `func TargetOf(driver string, c db.Column) Target`
  - `type Cell struct { Text string; Null, Default bool }`
  - `func Convert(t Target, cell, null string) (Cell, error)` — the error text is the reason only (`is not an integer`)
  - `type RowError struct { Line int; Column, Value, Reason string }` with `Error()`
  - unexported, used by Task 5: `stripThousands(s string) string`, `numberRe`, `parseDate(s string) (time.Time, bool)`, `parseDateTime(s string, zone bool) (time.Time, bool, bool)`

- [ ] **Step 1: Write the failing tests**

`internal/importer/convert_test.go`:

```go
package importer

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

func strp(s string) *string { return &s }

func TestTargetOf(t *testing.T) {
	tests := []struct {
		driver string
		col    db.Column
		want   Target
	}{
		{"mysql", db.Column{Name: "a", Type: "bigint unsigned"}, Target{Kind: KindInteger, Bits: 64, Unsigned: true}},
		{"mysql", db.Column{Name: "a", Type: "int(11)", Nullable: true}, Target{Kind: KindInteger, Bits: 32, Nullable: true}},
		{"mysql", db.Column{Name: "a", Type: "tinyint(1)"}, Target{Kind: KindBool}},
		{"mysql", db.Column{Name: "a", Type: "tinyint"}, Target{Kind: KindInteger, Bits: 8}},
		{"mysql", db.Column{Name: "a", Type: "mediumint", AutoIncrement: true}, Target{Kind: KindInteger, Bits: 24, Default: true}},
		{"postgres", db.Column{Name: "a", Type: "smallint"}, Target{Kind: KindInteger, Bits: 16}},
		{"postgres", db.Column{Name: "a", Type: "integer", Default: strp("0")}, Target{Kind: KindInteger, Bits: 32, Default: true}},
		{"mysql", db.Column{Name: "a", Type: "decimal(10,2)"}, Target{Kind: KindDecimal, Precision: 10, Scale: 2}},
		{"postgres", db.Column{Name: "a", Type: "numeric"}, Target{Kind: KindDecimal}},
		{"postgres", db.Column{Name: "a", Type: "double precision"}, Target{Kind: KindFloat}},
		{"postgres", db.Column{Name: "a", Type: "boolean"}, Target{Kind: KindBool}},
		{"mysql", db.Column{Name: "a", Type: "date"}, Target{Kind: KindDate}},
		{"mysql", db.Column{Name: "a", Type: "datetime(3)"}, Target{Kind: KindDateTime, Precision: 3}},
		{"postgres", db.Column{Name: "a", Type: "timestamp(3) with time zone"}, Target{Kind: KindDateTime, Precision: 3, TZ: true}},
		{"postgres", db.Column{Name: "a", Type: "timestamptz"}, Target{Kind: KindDateTime, TZ: true}},
		{"postgres", db.Column{Name: "a", Type: "time without time zone"}, Target{Kind: KindTime}},
		{"postgres", db.Column{Name: "a", Type: "timetz"}, Target{Kind: KindOther}},
		{"postgres", db.Column{Name: "a", Type: "character varying(64)"}, Target{Kind: KindString, Precision: 64, Length: 64}},
		{"mysql", db.Column{Name: "a", Type: "varchar(20)"}, Target{Kind: KindString, Precision: 20, Length: 20}},
		{"mysql", db.Column{Name: "a", Type: "enum('a','b')"}, Target{Kind: KindString}},
		{"mysql", db.Column{Name: "a", Type: "longtext"}, Target{Kind: KindString}},
		{"postgres", db.Column{Name: "a", Type: "jsonb"}, Target{Kind: KindOther}},
		{"postgres", db.Column{Name: "a", Type: "int[]"}, Target{Kind: KindOther}},
	}
	for _, tt := range tests {
		got := TargetOf(tt.driver, tt.col)
		tt.want.Name, tt.want.Type, tt.want.MySQL = tt.col.Name, tt.col.Type, tt.driver == "mysql"
		if diff := cmp.Diff(tt.want, got); diff != "" {
			t.Errorf("TargetOf(%s, %q) (-want +got):\n%s", tt.driver, tt.col.Type, diff)
		}
	}
}

func TestConvert(t *testing.T) {
	col := func(driver, typ string, nullable bool) Target {
		return TargetOf(driver, db.Column{Name: "c", Type: typ, Nullable: nullable})
	}
	withDefault := TargetOf("mysql", db.Column{Name: "c", Type: "int", Default: strp("7")})
	tests := []struct {
		target Target
		in     string
		null   string
		want   Cell
		err    string // substring; "" means no error
	}{
		{col("mysql", "int", true), " 42 ", "", Cell{Text: "42"}, ""},
		{col("mysql", "int", true), "1,234", "", Cell{Text: "1234"}, ""},
		{col("mysql", "int", true), "3.0", "", Cell{Text: "3"}, ""},
		{col("mysql", "int", true), "3.5", "", Cell{}, "is not an integer"},
		{col("mysql", "int", true), "abc", "", Cell{}, "is not an integer"},
		{col("mysql", "int", true), "1.23456789012346E+17", "", Cell{}, "format the column as text"},
		{col("mysql", "tinyint", true), "128", "", Cell{}, "is out of range for tinyint"},
		{col("mysql", "int unsigned", true), "-1", "", Cell{}, "is out of range for int unsigned"},
		{col("mysql", "bigint unsigned", true), "18446744073709551615", "", Cell{Text: "18446744073709551615"}, ""},
		{col("mysql", "decimal(7,2)", true), "1,234.5", "", Cell{Text: "1234.50"}, ""},
		{col("mysql", "decimal(5,2)", true), "1.234", "", Cell{}, "has more than 2 decimal places"},
		{col("mysql", "decimal(5,2)", true), "1234.5", "", Cell{}, "has more than 3 digits before the point"},
		{col("mysql", "decimal(5,2)", true), "-12.5", "", Cell{Text: "-12.50"}, ""},
		{col("postgres", "numeric", true), "1e3", "", Cell{Text: "1e3"}, ""},
		{col("postgres", "double precision", true), "x1", "", Cell{}, "is not a number"},
		{col("postgres", "boolean", true), "Yes", "", Cell{Text: "true"}, ""},
		{col("mysql", "tinyint(1)", true), "f", "", Cell{Text: "0"}, ""},
		{col("mysql", "tinyint(1)", true), "maybe", "", Cell{}, "is not a boolean"},
		{col("mysql", "date", true), "2026/1/2", "", Cell{Text: "2026-01-02"}, ""},
		{col("mysql", "date", true), "2026-01-02 00:00:00", "", Cell{Text: "2026-01-02"}, ""},
		{col("mysql", "date", true), "2026-01-02 10:00", "", Cell{}, "is not a date"},
		{col("mysql", "date", true), "2026/13/01", "", Cell{}, "is not a date"},
		{col("mysql", "datetime", true), "2026-01-02", "", Cell{Text: "2026-01-02 00:00:00"}, ""},
		{col("mysql", "datetime", true), "2026/1/2 9:05", "", Cell{Text: "2026-01-02 09:05:00"}, ""},
		{col("mysql", "datetime", true), "2026-01-02T09:05:07.25", "", Cell{Text: "2026-01-02 09:05:07.25"}, ""},
		{col("mysql", "datetime", true), "2026-01-02T09:05:07Z", "", Cell{}, "is not a date and time"},
		{col("postgres", "timestamptz", true), "2026-01-02T09:05:07+08:00", "", Cell{Text: "2026-01-02 09:05:07+08:00"}, ""},
		{col("postgres", "timestamptz", true), "2026-01-02 09:05", "", Cell{Text: "2026-01-02 09:05:00"}, ""},
		{col("postgres", "time", true), "9:05", "", Cell{Text: "09:05:00"}, ""},
		{col("postgres", "time", true), "25:00", "", Cell{}, "is not a time"},
		{col("mysql", "varchar(3)", true), "中文字", "", Cell{Text: "中文字"}, ""},
		{col("mysql", "varchar(3)", true), "abcd", "", Cell{}, "is 4 characters, the column allows 3"},
		{col("mysql", "text", true), "  keep spaces ", "", Cell{Text: "  keep spaces "}, ""},
		{col("postgres", "jsonb", true), ` {"a":1}`, "", Cell{Text: ` {"a":1}`}, ""},
		// empty cells
		{col("mysql", "text", true), "", "", Cell{Null: true}, ""},
		{col("mysql", "text", true), "NULL", "NULL", Cell{Null: true}, ""},
		{col("mysql", "text", true), "", "NULL", Cell{Text: ""}, ""},
		{col("mysql", "int", true), "  ", "NULL", Cell{Null: true}, ""},
		{withDefault, "", "", Cell{Default: true}, ""},
		{col("mysql", "int", false), "", "", Cell{}, "cannot be empty"},
	}
	for _, tt := range tests {
		got, err := Convert(tt.target, tt.in, tt.null)
		switch {
		case tt.err == "" && err != nil:
			t.Errorf("Convert(%s, %q): %v", tt.target.Type, tt.in, err)
		case tt.err != "" && (err == nil || !strings.Contains(err.Error(), tt.err)):
			t.Errorf("Convert(%s, %q) error = %v, want %q", tt.target.Type, tt.in, err, tt.err)
		case tt.err == "" && got != tt.want:
			t.Errorf("Convert(%s, %q) = %+v, want %+v", tt.target.Type, tt.in, got, tt.want)
		}
	}
}

func TestRowError(t *testing.T) {
	e := &RowError{Line: 12, Column: "created", Value: "2026/13/01", Reason: "is not a date"}
	if got := e.Error(); got != `line 12, created: "2026/13/01" is not a date` {
		t.Fatalf("Error() = %s", got)
	}
	if got := (&RowError{Line: 3, Reason: "3 cells, the header has 2"}).Error(); got != "line 3: 3 cells, the header has 2" {
		t.Fatalf("Error() = %s", got)
	}
	long := &RowError{Line: 1, Column: "c", Value: strings.Repeat("x", 100), Reason: "is bad"}
	if !strings.Contains(long.Error(), "…") {
		t.Fatalf("long values are not clipped: %s", long.Error())
	}
}
```

Note: `Precision` holds the first type argument for every kind (length for strings, fractional digits for `datetime(3)`); only `KindDecimal` reads it as precision. `Length` is set for `KindString` only.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/importer/ -run 'TargetOf|Convert|RowError'`
Expected: FAIL to compile (`undefined: TargetOf`).

- [ ] **Step 3: Implement**

`internal/importer/convert.go`:

```go
package importer

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shepherd-yun/datavim/internal/db"
)

// Kind groups column types by how a cell is converted for them.
type Kind int

const (
	KindString Kind = iota
	KindInteger
	KindDecimal // decimal, numeric: precision and scale are checked
	KindFloat
	KindBool
	KindDate
	KindDateTime
	KindTime
	KindOther // passed through for the server to check
)

// Target is a column rows are written to.
type Target struct {
	Name string
	Type string
	Kind Kind
	// Nullable: an empty cell is NULL. Default: otherwise it is DEFAULT
	// (the column has a default or is auto-increment).
	Nullable, Default bool
	Unsigned          bool
	Bits              int // integer width: 8, 16, 24, 32 or 64
	Precision, Scale  int // the type's arguments; decimal(p,s), 0 when absent
	Length            int // char and varchar: the limit in characters
	TZ                bool
	MySQL             bool // booleans are written as 1 and 0
}

var typeRe = regexp.MustCompile(`^([a-z][a-z0-9 ]*?)\s*(?:\(\s*(\d+)\s*(?:,\s*(\d+)\s*)?\))?(\s+unsigned)?(?:\s+zerofill)?$`)

// TargetOf describes column c of a table on driver ("mysql" or "postgres").
func TargetOf(driver string, c db.Column) Target {
	t := Target{
		Name: c.Name, Type: c.Type, Nullable: c.Nullable,
		Default: c.Default != nil || c.AutoIncrement, MySQL: driver == "mysql",
	}
	typ := strings.ToLower(strings.TrimSpace(c.Type))
	if strings.HasSuffix(typ, " with time zone") {
		t.TZ = true
	}
	typ = strings.NewReplacer(" without time zone", "", " with time zone", "").Replace(typ)
	if strings.HasPrefix(typ, "enum(") || strings.HasPrefix(typ, "set(") {
		return t // KindString
	}
	m := typeRe.FindStringSubmatch(typ)
	if m == nil {
		t.Kind = KindOther
		return t
	}
	base := m[1]
	t.Precision, _ = strconv.Atoi(m[2])
	t.Scale, _ = strconv.Atoi(m[3])
	t.Unsigned = m[4] != ""
	switch base {
	case "tinyint":
		if t.MySQL && m[2] == "1" {
			t.Kind, t.Precision = KindBool, 0
			return t
		}
		t.Kind, t.Bits = KindInteger, 8
	case "bool", "boolean":
		t.Kind = KindBool
	case "smallint", "int2", "smallserial", "serial2":
		t.Kind, t.Bits = KindInteger, 16
	case "mediumint":
		t.Kind, t.Bits = KindInteger, 24
	case "int", "integer", "int4", "serial", "serial4":
		t.Kind, t.Bits = KindInteger, 32
	case "bigint", "int8", "bigserial", "serial8":
		t.Kind, t.Bits = KindInteger, 64
	case "decimal", "numeric", "dec", "fixed":
		t.Kind = KindDecimal
	case "float", "double", "double precision", "real", "float4", "float8":
		t.Kind = KindFloat
	case "date":
		t.Kind = KindDate
	case "datetime", "timestamp", "timestamptz":
		t.Kind, t.TZ = KindDateTime, t.TZ || base == "timestamptz"
	case "time":
		t.Kind = KindTime
		if t.TZ {
			t.Kind, t.TZ = KindOther, false
		}
	case "char", "character", "varchar", "character varying", "nchar", "nvarchar", "bpchar":
		t.Kind, t.Length = KindString, t.Precision
	case "text", "tinytext", "mediumtext", "longtext", "citext":
		t.Kind = KindString
	default:
		t.Kind = KindOther
	}
	if t.Kind == KindInteger {
		t.Precision = 0 // int(11) is a display width
	}
	return t
}

// Cell is a converted value: text sent as a parameter, NULL or DEFAULT.
type Cell struct {
	Text    string
	Null    bool
	Default bool
}

const excelHint = "is not an integer (Excel keeps 15 digits and turns longer numbers into floats: format the column as text)"

// Convert turns the text of a cell into the value written to t; null is the
// text that stands for an empty cell. The error is the reason alone.
func Convert(t Target, cell, null string) (Cell, error) {
	text := cell
	if t.Kind != KindString && t.Kind != KindOther {
		text = strings.TrimSpace(cell)
	}
	if cell == null || text == null || text == "" && t.Kind != KindString {
		switch {
		case t.Nullable:
			return Cell{Null: true}, nil
		case t.Default:
			return Cell{Default: true}, nil
		}
		return Cell{}, errors.New("cannot be empty")
	}
	var out string
	var err error
	switch t.Kind {
	case KindInteger:
		out, err = convertInteger(t, text)
	case KindDecimal, KindFloat:
		out, err = convertNumber(t, text)
	case KindBool:
		out, err = convertBool(t, text)
	case KindDate:
		out, err = convertDate(text)
	case KindDateTime:
		out, err = convertDateTime(t, text)
	case KindTime:
		out, err = convertTime(text)
	case KindString:
		if n := utf8.RuneCountInString(text); t.Length > 0 && n > t.Length {
			err = fmt.Errorf("is %d characters, the column allows %d", n, t.Length)
		}
		out = text
	default:
		out = text
	}
	if err != nil {
		return Cell{}, err
	}
	return Cell{Text: out}, nil
}

var (
	thousandsRe = regexp.MustCompile(`^[+-]?\d{1,3}(,\d{3})+(\.\d*)?$`)
	numberRe    = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
)

// stripThousands drops the commas of 1,234,567.5.
func stripThousands(s string) string {
	if thousandsRe.MatchString(s) {
		return strings.ReplaceAll(s, ",", "")
	}
	return s
}

func convertInteger(t Target, s string) (string, error) {
	s = stripThousands(s)
	if i := strings.IndexByte(s, '.'); i > 0 && strings.Trim(s[i+1:], "0") == "" {
		s = s[:i]
	}
	if t.Unsigned {
		if strings.HasPrefix(s, "-") && numberRe.MatchString(s) {
			return "", fmt.Errorf("is out of range for %s", t.Type)
		}
		v, err := strconv.ParseUint(strings.TrimPrefix(s, "+"), 10, t.Bits)
		if err != nil {
			return "", integerError(t, s, err)
		}
		return strconv.FormatUint(v, 10), nil
	}
	v, err := strconv.ParseInt(s, 10, t.Bits)
	if err != nil {
		return "", integerError(t, s, err)
	}
	return strconv.FormatInt(v, 10), nil
}

func integerError(t Target, s string, err error) error {
	switch {
	case errors.Is(err, strconv.ErrRange):
		return fmt.Errorf("is out of range for %s", t.Type)
	case strings.ContainsAny(s, "eE") && numberRe.MatchString(s):
		return errors.New(excelHint)
	}
	return errors.New("is not an integer")
}

func convertNumber(t Target, s string) (string, error) {
	s = stripThousands(s)
	if !numberRe.MatchString(s) {
		return "", errors.New("is not a number")
	}
	if t.Kind == KindFloat || t.Precision == 0 {
		return s, nil
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "", errors.New("is not a number")
	}
	scale := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(t.Scale)), nil))
	if !new(big.Rat).Mul(r, scale).IsInt() {
		return "", fmt.Errorf("has more than %d decimal places", t.Scale)
	}
	out := r.FloatString(t.Scale)
	whole, _, _ := strings.Cut(strings.TrimPrefix(out, "-"), ".")
	if digits := len(strings.TrimLeft(whole, "0")); digits > t.Precision-t.Scale {
		return "", fmt.Errorf("has more than %d digits before the point", t.Precision-t.Scale)
	}
	return out, nil
}

func convertBool(t Target, s string) (string, error) {
	var v bool
	switch strings.ToLower(s) {
	case "true", "t", "yes", "y", "1":
		v = true
	case "false", "f", "no", "n", "0":
	default:
		return "", errors.New("is not a boolean")
	}
	switch {
	case t.MySQL && v:
		return "1", nil
	case t.MySQL:
		return "0", nil
	case v:
		return "true", nil
	}
	return "false", nil
}

const (
	dateFormat     = "2006-01-02"
	dateTimeFormat = "2006-01-02 15:04:05.999999"
	timeFormat     = "15:04:05.999999"
)

var (
	dateLayouts  = []string{"2006-1-2", "2006/1/2"}
	clockLayouts = []string{"15:4", "15:4:5"} // fractional seconds parse after :5
	zoneLayouts  = []string{"Z07:00", " Z07:00", "Z0700", "Z07"}
)

func parseDate(s string) (time.Time, bool) {
	for _, l := range dateLayouts {
		if v, err := time.Parse(l, s); err == nil {
			return v, true
		}
	}
	return time.Time{}, false
}

// parseDateTime parses a date, a space or T, and a time of day; with zone
// set it also accepts a trailing zone and reports whether there was one.
func parseDateTime(s string, zone bool) (v time.Time, zoned, ok bool) {
	for _, d := range dateLayouts {
		for _, sep := range []string{" ", "T"} {
			for _, c := range clockLayouts {
				l := d + sep + c
				if v, err := time.Parse(l, s); err == nil {
					return v, false, true
				}
				if !zone {
					continue
				}
				for _, z := range zoneLayouts {
					if v, err := time.Parse(l+z, s); err == nil {
						return v, true, true
					}
				}
			}
		}
	}
	return time.Time{}, false, false
}

func convertDate(s string) (string, error) {
	if v, ok := parseDate(s); ok {
		return v.Format(dateFormat), nil
	}
	if v, _, ok := parseDateTime(s, false); ok && v.Hour() == 0 && v.Minute() == 0 && v.Second() == 0 && v.Nanosecond() == 0 {
		return v.Format(dateFormat), nil
	}
	return "", errors.New("is not a date")
}

func convertDateTime(t Target, s string) (string, error) {
	if v, ok := parseDate(s); ok {
		return v.Format(dateTimeFormat), nil
	}
	v, zoned, ok := parseDateTime(s, t.TZ)
	switch {
	case !ok:
		return "", errors.New("is not a date and time")
	case zoned:
		return v.Format(dateTimeFormat + "-07:00"), nil
	}
	return v.Format(dateTimeFormat), nil
}

func convertTime(s string) (string, error) {
	for _, l := range clockLayouts {
		if v, err := time.Parse(l, s); err == nil {
			return v.Format(timeFormat), nil
		}
	}
	return "", errors.New("is not a time")
}

// RowError is a file row that cannot be written.
type RowError struct {
	Line   int
	Column string // the file's name for the column; "" for the whole row
	Value  string
	Reason string
}

func (e *RowError) Error() string {
	if e.Column == "" {
		return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
	}
	v := e.Value
	if r := []rune(v); len(r) > 40 {
		v = string(r[:40]) + "…"
	}
	return fmt.Sprintf("line %d, %s: %q %s", e.Line, e.Column, v, e.Reason)
}
```

Check against the tests: `"2026-01-02T09:05:07Z"` for a plain `datetime` fails because `zone` is false; `"2026-01-02T09:05:07.25"` formats as `…07.25` because `.999999` trims zeros.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/importer/ && make lint`
Expected: PASS. If `TestTargetOf` disagrees only in `Precision` for a type, fix the expectation to the rule in the note above, not the code.

- [ ] **Step 5: Commit**

```bash
git add internal/importer
git commit -m "feat(importer): 按目标列类型转换和校验单元格"
```

---

### Task 4: Plan, batched INSERT, names

**Files:**
- Create: `internal/importer/plan.go`
- Test: `internal/importer/plan_test.go`

**Interfaces:**
- Consumes: `Target`, `Cell`, `Convert`, `RowError` (Task 3); `sqlx.DialectOf`, `sqlx.QualifiedName`, `sqlx.QuoteIdent`, `sqlx.Postgres`.
- Produces:
  - `type Column struct { Source int; Header string; Target Target }`
  - `type Plan struct { Driver string; Table db.TableRef; Columns []Column; Width int; Null string }`
  - `func (p Plan) Row(line int, cells []string) ([]Cell, bool, error)` — `false` for a blank row; the error is a `*RowError`
  - `func BatchSize(cols int) int`
  - `func (p Plan) Insert(rows [][]Cell) (string, []any)`
  - `func Ident(s, fallback string) string`, `func MatchKey(s string) string`, `func UniqueIdents(names []string) []string`

- [ ] **Step 1: Write the failing tests**

`internal/importer/plan_test.go`:

```go
package importer

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
)

func testPlan(driver string) Plan {
	cols := []db.Column{
		{Name: "id", Type: "int"},
		{Name: "name", Type: "varchar(10)", Nullable: true},
		{Name: "n", Type: "int", Default: strp("7")},
	}
	p := Plan{Driver: driver, Table: db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "t"}, Width: 4}
	for i, c := range cols {
		src := i
		if i == 2 {
			src = 3 // the file's third column is skipped
		}
		p.Columns = append(p.Columns, Column{Source: src, Header: "h" + c.Name, Target: TargetOf(driver, c)})
	}
	return p
}

func TestPlanRow(t *testing.T) {
	p := testPlan("mysql")
	tests := []struct {
		name  string
		cells []string
		want  []Cell
		ok    bool
		err   string
	}{
		{"all columns", []string{"1", "a", "skipped", "5"}, []Cell{{Text: "1"}, {Text: "a"}, {Text: "5"}}, true, ""},
		{"short row: missing cells are empty", []string{"2"}, []Cell{{Text: "2"}, {Null: true}, {Default: true}}, true, ""},
		{"trailing empty cells are ignored", []string{"3", "b", "", "", "", " "}, []Cell{{Text: "3"}, {Text: "b"}, {Default: true}}, true, ""},
		{"blank row", []string{"", " ", ""}, nil, false, ""},
		{"extra cells with text", []string{"4", "c", "", "", "oops"}, nil, false, "line 9: 5 cells, the header has 4"},
		{"bad value", []string{"x", "c"}, nil, false, `line 9, hid: "x" is not an integer`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := p.Row(9, tt.cells)
			if tt.err != "" {
				var re *RowError
				if !errors.As(err, &re) || err.Error() != tt.err {
					t.Fatalf("err = %v, want %s", err, tt.err)
				}
				return
			}
			if err != nil || ok != tt.ok {
				t.Fatalf("ok = %v, err = %v", ok, err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Fatalf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestBatchSize(t *testing.T) {
	for cols, want := range map[int]int{0: 500, 1: 500, 131: 500, 200: 327, 70000: 1} {
		if got := BatchSize(cols); got != want {
			t.Errorf("BatchSize(%d) = %d, want %d", cols, got, want)
		}
	}
}

func TestInsert(t *testing.T) {
	rows := [][]Cell{{{Text: "1"}, {Null: true}, {Default: true}}, {{Text: "2"}, {Text: "b"}, {Text: "5"}}}
	q, args := testPlan("mysql").Insert(rows)
	if want := "INSERT INTO `app`.`t` (`id`, `name`, `n`) VALUES (?, NULL, DEFAULT), (?, ?, ?)"; q != want {
		t.Fatalf("mysql:\n%s\nwant\n%s", q, want)
	}
	if diff := cmp.Diff([]any{"1", "2", "b", "5"}, args); diff != "" {
		t.Fatalf("args (-want +got):\n%s", diff)
	}
	q, _ = testPlan("postgres").Insert(rows)
	if want := `INSERT INTO "public"."t" ("id", "name", "n") VALUES ($1, NULL, DEFAULT), ($2, $3, $4)`; q != want {
		t.Fatalf("postgres:\n%s\nwant\n%s", q, want)
	}
}

func TestNames(t *testing.T) {
	for in, want := range map[string]string{"order id": "order_id", " 订单号 ": "订单号", "a-b.c": "a_b_c", "": "fb", "%%": "fb", "_x": "_x"} {
		if got := Ident(in, "fb"); got != want {
			t.Errorf("Ident(%q) = %q, want %q", in, got, want)
		}
	}
	if MatchKey("Order ID") != MatchKey("order_id") || MatchKey("order-id") != MatchKey("ORDERID") || MatchKey("a") == MatchKey("b") {
		t.Error("MatchKey does not ignore case and separators")
	}
	got := UniqueIdents([]string{"id", "ID", "", "a b", "a_b", "a_b_2"})
	want := []string{"id", "ID_2", "column_3", "a_b", "a_b_2", "a_b_2_2"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("UniqueIdents (-want +got):\n%s", diff)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/importer/ -run 'PlanRow|BatchSize|Insert|Names'`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/importer/plan.go`:

```go
package importer

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

// Column is a file column picked for import and the column it goes to.
type Column struct {
	Source int    // index in the file's rows
	Header string // the file's name for it, for messages
	Target Target
}

// Plan says how file rows become table rows.
type Plan struct {
	Driver  string // "mysql" or "postgres"
	Table   db.TableRef
	Columns []Column
	Width   int    // the header's width
	Null    string // the text of an empty cell
}

// Row converts the file row at line. ok is false for a row whose cells are
// all blank, which is skipped. Cells past the header's width must be blank
// (a trailing delimiter); the error is a *RowError.
func (p Plan) Row(line int, cells []string) (out []Cell, ok bool, err error) {
	if blank(cells) {
		return nil, false, nil
	}
	if len(cells) > p.Width && !blank(cells[p.Width:]) {
		return nil, false, &RowError{Line: line, Reason: fmt.Sprintf("%d cells, the header has %d", len(cells), p.Width)}
	}
	out = make([]Cell, len(p.Columns))
	for i, c := range p.Columns {
		text := ""
		if c.Source < len(cells) {
			text = cells[c.Source]
		}
		v, err := Convert(c.Target, text, p.Null)
		if err != nil {
			return nil, false, &RowError{Line: line, Column: c.Header, Value: text, Reason: err.Error()}
		}
		out[i] = v
	}
	return out, true, nil
}

func blank(cells []string) bool {
	for _, c := range cells {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

const (
	maxBatch  = 500
	maxParams = 65535 // MySQL's and PostgreSQL's limit on placeholders
)

// BatchSize is how many rows of cols columns one INSERT carries.
func BatchSize(cols int) int { return max(1, min(maxBatch, maxParams/max(cols, 1))) }

// Insert writes one INSERT of rows: values are arguments, NULL and DEFAULT
// are keywords.
func (p Plan) Insert(rows [][]Cell) (string, []any) {
	d := sqlx.DialectOf(p.Driver)
	var b strings.Builder
	b.WriteString("INSERT INTO " + sqlx.QualifiedName(d, p.Table) + " (")
	for i, c := range p.Columns {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(sqlx.QuoteIdent(d, c.Target.Name))
	}
	b.WriteString(") VALUES ")
	var args []any
	for i, row := range rows {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for j, v := range row {
			if j > 0 {
				b.WriteString(", ")
			}
			switch {
			case v.Null:
				b.WriteString("NULL")
			case v.Default:
				b.WriteString("DEFAULT")
			default:
				args = append(args, v.Text)
				if d == sqlx.Postgres {
					b.WriteString("$" + strconv.Itoa(len(args)))
				} else {
					b.WriteByte('?')
				}
			}
		}
		b.WriteByte(')')
	}
	return b.String(), args
}

// Ident turns a file's column name or file name into a column or table
// name: characters other than letters (any script), digits and _ become _;
// a name with nothing else becomes fallback.
func Ident(s, fallback string) string {
	out := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return r
		}
		return '_'
	}, strings.TrimSpace(s))
	if strings.Trim(out, "_") == "" {
		return fallback
	}
	return out
}

// UniqueIdents makes column names from a header: Ident each (column_N for
// an empty one) and add _2, _3… to names taken already, ignoring case.
func UniqueIdents(names []string) []string {
	out := make([]string, len(names))
	taken := map[string]bool{}
	for i, n := range names {
		base := Ident(n, "column_"+strconv.Itoa(i+1))
		name := base
		for k := 2; taken[strings.ToLower(name)]; k++ {
			name = base + "_" + strconv.Itoa(k)
		}
		taken[strings.ToLower(name)] = true
		out[i] = name
	}
	return out
}

// MatchKey is what file columns are matched to table columns by: case and
// the separators _, - and space do not count.
func MatchKey(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', ' ':
			return -1
		}
		return unicode.ToLower(r)
	}, strings.TrimSpace(s))
}
```

Check `Ident` against the test table: `"%%"` → `"__"` → fallback; `"_x"` → `"_x"`; `""` → fallback; `"a-b.c"` → `"a_b_c"`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/importer/ && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/importer
git commit -m "feat(importer): 映射计划与参数化批量 INSERT"
```

---

### Task 5: Type inference for a new table

**Files:**
- Create: `internal/importer/infer.go`
- Test: `internal/importer/infer_test.go`

**Interfaces:**
- Consumes: `stripThousands`, `numberRe`, `parseDate`, `parseDateTime` (Task 3).
- Produces:
  - `func NewInferrer(driver, null string) *Inferrer`
  - `func (in *Inferrer) Add(cells []string)`
  - `func (in *Inferrer) Schema(ref db.TableRef, names []string) db.TableSchema` — one column per name in order, `Type` inferred, `Nullable` false only when every row had a value, `Position` 1-based

- [ ] **Step 1: Write the failing tests**

`internal/importer/infer_test.go`:

```go
package importer

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

func TestInferTypes(t *testing.T) {
	tests := []struct {
		name          string
		values        []string
		mysql, pg     string
	}{
		{"integers", []string{"1", "-20", "1,234"}, "bigint", "bigint"},
		{"integers written as floats", []string{"1", "2.0"}, "bigint", "bigint"},
		{"decimals", []string{"1.5", "22.25", "3"}, "decimal(4,2)", "numeric(4,2)"},
		{"exponents", []string{"1e3", "2"}, "double", "double precision"},
		{"leading zeros stay text", []string{"007", "12"}, "varchar(16)", "varchar(16)"},
		{"dates", []string{"2026-01-02", "2026/1/3"}, "date", "date"},
		{"dates and times", []string{"2026-01-02", "2026-01-02 10:00"}, "datetime", "timestamp"},
		{"fractional seconds", []string{"2026-01-02 10:00:00.5"}, "datetime(6)", "timestamp"},
		{"text", []string{"abc", "1"}, "varchar(16)", "varchar(16)"},
		{"longer text", []string{strings.Repeat("x", 40)}, "varchar(48)", "varchar(48)"},
		{"up to 255", []string{strings.Repeat("x", 250)}, "varchar(255)", "varchar(255)"},
		{"long text", []string{strings.Repeat("x", 300)}, "text", "text"},
		{"empty", []string{"", " "}, "varchar(255)", "text"},
		{"huge integers", []string{"123456789012345678901"}, "decimal(21,0)", "numeric(21,0)"},
	}
	for _, tt := range tests {
		for _, driver := range []string{"mysql", "postgres"} {
			in := NewInferrer(driver, "")
			for _, v := range tt.values {
				in.Add([]string{"k", v})
			}
			got := in.Schema(db.TableRef{Name: "t"}, []string{"k", "c"}).Columns[1].Type
			want := tt.mysql
			if driver == "postgres" {
				want = tt.pg
			}
			if got != want {
				t.Errorf("%s on %s: %s, want %s", tt.name, driver, got, want)
			}
		}
	}
}

func TestInferNullability(t *testing.T) {
	in := NewInferrer("mysql", "NULL")
	in.Add([]string{"1", "a", "x"})
	in.Add([]string{"", "", ""}) // blank rows do not count
	in.Add([]string{"2", "NULL", "y"})
	in.Add([]string{"3"}) // a short row leaves the rest empty
	s := in.Schema(db.TableRef{Name: "t"}, []string{"id", "name", "note"})
	for i, want := range []bool{false, true, true} {
		if c := s.Columns[i]; c.Nullable != want || c.Position != i+1 {
			t.Errorf("column %s: nullable %v, position %d", c.Name, c.Nullable, c.Position)
		}
	}
	if s.Columns[0].Type != "bigint" || s.Ref.Name != "t" {
		t.Fatalf("schema %+v", s)
	}
	empty := NewInferrer("postgres", "").Schema(db.TableRef{Name: "t"}, []string{"a"})
	if c := empty.Columns[0]; !c.Nullable || c.Type != "text" {
		t.Fatalf("no rows: %+v", c)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/importer/ -run Infer`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/importer/infer.go`:

```go
package importer

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/shepherd-yun/datavim/internal/db"
)

// Inferrer guesses the column types of a new table from the file's rows.
type Inferrer struct {
	mysql bool
	null  string
	rows  int
	cols  []colStats
}

// colStats is what one column's values have in common.
type colStats struct {
	values    int
	notInt    bool
	notNum    bool
	exp       bool
	notDate   bool
	notStamp  bool // neither a date nor a date and time
	fracSec   bool
	intDigits int
	scale     int
	maxLen    int
}

// NewInferrer counts rows for a table on driver; null is the text of an
// empty cell.
func NewInferrer(driver, null string) *Inferrer {
	return &Inferrer{mysql: driver == "mysql", null: null}
}

// Add counts one data row; blank rows are skipped, as the import skips them.
func (in *Inferrer) Add(cells []string) {
	if blank(cells) {
		return
	}
	in.rows++
	for len(in.cols) < len(cells) {
		in.cols = append(in.cols, colStats{})
	}
	for i, c := range cells {
		in.cols[i].add(c, in.null)
	}
}

func (c *colStats) add(raw, null string) {
	s := strings.TrimSpace(raw)
	if s == "" || raw == null || s == null {
		return
	}
	c.values++
	c.maxLen = max(c.maxLen, utf8.RuneCountInString(raw))
	n := stripThousands(s)
	switch {
	case !numberRe.MatchString(n):
		c.notInt, c.notNum = true, true
	case strings.ContainsAny(n, "eE"):
		c.notInt, c.exp = true, true
	default:
		whole, frac, _ := strings.Cut(strings.TrimLeft(n, "+-"), ".")
		if len(whole) > 1 && whole[0] == '0' {
			c.notInt, c.notNum = true, true // 007: an identifier, not a number
			break
		}
		frac = strings.TrimRight(frac, "0")
		digits := len(strings.TrimLeft(whole, "0"))
		if frac != "" || digits > 18 {
			c.notInt = true
		}
		c.intDigits, c.scale = max(c.intDigits, digits), max(c.scale, len(frac))
	}
	_, isDate := parseDate(s)
	_, _, isStamp := parseDateTime(s, false)
	if !isDate {
		c.notDate = true
	}
	if !isDate && !isStamp {
		c.notStamp = true
	}
	if isStamp && strings.Contains(s, ".") {
		c.fracSec = true
	}
}

// Schema is a table called ref with one column per name, typed from the
// rows added. A column is NOT NULL only when every row had a value for it.
func (in *Inferrer) Schema(ref db.TableRef, names []string) db.TableSchema {
	s := db.TableSchema{Ref: ref}
	for i, name := range names {
		var c colStats
		if i < len(in.cols) {
			c = in.cols[i]
		}
		s.Columns = append(s.Columns, db.Column{
			Name: name, Type: in.typeOf(c), Nullable: c.values == 0 || c.values < in.rows, Position: i + 1,
		})
	}
	return s
}

func (in *Inferrer) typeOf(c colStats) string {
	maxPrecision, maxScale, numeric, double, stamp, text := 1000, 1000, "numeric", "double precision", "timestamp", "text"
	if in.mysql {
		maxPrecision, maxScale, numeric, double, stamp, text = 65, 30, "decimal", "double", "datetime", "varchar(255)"
		if c.fracSec {
			stamp = "datetime(6)"
		}
	}
	switch {
	case c.values == 0:
		return text
	case !c.notInt:
		return "bigint"
	case !c.notNum && !c.exp && c.intDigits+c.scale <= maxPrecision && c.scale <= maxScale:
		return fmt.Sprintf("%s(%d,%d)", numeric, max(c.intDigits+c.scale, 1), c.scale)
	case !c.notNum:
		return double
	case !c.notDate:
		return "date"
	case !c.notStamp:
		return stamp
	case c.maxLen <= 255:
		return fmt.Sprintf("varchar(%d)", min(255, max(16, (c.maxLen+15)/16*16)))
	}
	return "text"
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/importer/ && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/importer
git commit -m "feat(importer): 从文件推断新表的列类型"
```

---

### Task 6: Sample, dry run and import run

**Files:**
- Create: `internal/importer/run.go`
- Test: `internal/importer/run_test.go`

**Interfaces:**
- Consumes: `Open`, `Plan`, `RowError`, `BatchSize` (Tasks 1–4); `db.Querier`, `db.IsConnLost`, `db.ErrConnLost`; `sqlx.BeginSQL`, `sqlx.DialectOf`, `sqlx.MySQL`, `sqlx.Postgres`.
- Produces:
  - `const PreviewRows = 20`, `const MaxErrors = 50`, `const Savepoint = "datavim_import"`
  - `type Sample struct { Header []string; Rows [][]string; Lines []int; BadUTF8 bool }`
  - `func Read(ctx context.Context, path string, o Options, n int, scan func([]string)) (Sample, error)`
  - `type CheckResult struct { Rows int; Errors []*RowError; Failed int }`
  - `func Check(ctx context.Context, path string, o Options, p Plan, done *atomic.Int64) (CheckResult, error)`
  - `type Job struct { Path string; Options Options; Plan Plan; Create []string; Drop string }`
  - `func Run(ctx context.Context, s db.Querier, job Job, done *atomic.Int64) (int, error)`
  - `type LineError struct { Line, First, Last int; Err error }` (`Unwrap`)
  - `type LeftoverError struct { Table string; Err, DropErr error }` (`Unwrap` returns `Err`)

- [ ] **Step 1: Write the failing tests**

`internal/importer/run_test.go`:

```go
package importer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
)

type call struct {
	sql  string
	args []any
}

// fakeQuerier records Exec calls; fail decides which ones fail.
type fakeQuerier struct {
	calls []call
	fail  func(sql string, args []any) error
}

func (f *fakeQuerier) Exec(ctx context.Context, q string, args ...any) (db.ExecResult, error) {
	f.calls = append(f.calls, call{q, args})
	if err := ctx.Err(); err != nil {
		return db.ExecResult{}, err
	}
	if f.fail != nil {
		if err := f.fail(q, args); err != nil {
			return db.ExecResult{}, err
		}
	}
	return db.ExecResult{RowsAffected: 1}, nil
}

func (f *fakeQuerier) Query(context.Context, string, ...any) (*db.ResultSet, error) {
	return &db.ResultSet{}, nil
}

func (f *fakeQuerier) Stream(context.Context, string, func([]db.ColumnInfo), func([]db.Value) error) error {
	return nil
}

// statements lists the SQL run, inserts shortened to "INSERT n rows".
func (f *fakeQuerier) statements() []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c.sql, "INSERT") {
			out = append(out, "INSERT "+strconv.Itoa(strings.Count(c.sql, "("))-1)+" rows")
			continue
		}
		out = append(out, c.sql)
	}
	return out
}

// idsPlan maps a one-column file "id" to an int column of app.t.
func idsPlan(driver string) Plan {
	return Plan{
		Driver: driver, Width: 1,
		Table:   db.TableRef{Namespace: db.Namespace{Database: "app", Schema: "public"}, Name: "t"},
		Columns: []Column{{Source: 0, Header: "id", Target: TargetOf(driver, db.Column{Name: "id", Type: "int"})}},
	}
}

func idsFile(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("id\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	return writeFile(t, "ids.csv", b.String())
}

func TestRead(t *testing.T) {
	p := writeFile(t, "a.csv", "id,name\n1,a\n2,b\n3,c\n")
	s, err := Read(context.Background(), p, DefaultOptions(p), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Rows) != 2 || !slices.Equal(s.Lines, []int{2, 3}) || s.Header[1] != "name" || s.BadUTF8 {
		t.Fatalf("sample %+v", s)
	}
	var scanned [][]string
	s, err = Read(context.Background(), p, DefaultOptions(p), 2, func(r []string) { scanned = append(scanned, r) })
	if err != nil || len(s.Rows) != 2 || len(scanned) != 3 {
		t.Fatalf("scan: %d rows, sample %d, err %v", len(scanned), len(s.Rows), err)
	}
	gbk := writeFile(t, "g.csv", "\xc3\xfb\xd7\xd6\n\xd5\xc5\xc8\xfd\n") // 名字 / 张三 in GBK
	if s, err := Read(context.Background(), gbk, DefaultOptions(gbk), 20, nil); err != nil || !s.BadUTF8 {
		t.Fatalf("GBK read as UTF-8: BadUTF8 = %v, err %v", s.BadUTF8, err)
	}
}

func TestCheck(t *testing.T) {
	var b strings.Builder
	b.WriteString("id\n1\n\n2\n")
	for range 60 {
		b.WriteString("x\n")
	}
	p := writeFile(t, "a.csv", b.String())
	var done atomic.Int64
	r, err := Check(context.Background(), p, DefaultOptions(p), idsPlan("mysql"), &done)
	if err != nil {
		t.Fatal(err)
	}
	// encoding/csv skips the empty line 3, so the first x is on line 5.
	if r.Rows != 2 || r.Failed != 60 || len(r.Errors) != MaxErrors || r.Errors[0].Line != 5 || done.Load() != 62 {
		t.Fatalf("result %+v, first %v, done %d", r, r.Errors[0], done.Load())
	}
}

func TestRunCommitsInBatches(t *testing.T) {
	q := &fakeQuerier{}
	var done atomic.Int64
	n, err := Run(context.Background(), q, Job{Path: idsFile(t, 1001), Options: DefaultOptions("ids.csv"), Plan: idsPlan("postgres")}, &done)
	if err != nil || n != 1001 || done.Load() != 1001 {
		t.Fatalf("n = %d, done = %d, err = %v", n, done.Load(), err)
	}
	batch := []string{"SAVEPOINT datavim_import", "", "RELEASE SAVEPOINT datavim_import"}
	want := []string{"BEGIN"}
	for _, rows := range []int{500, 500, 1} {
		batch[1] = "INSERT " + strconv.Itoa(rows) + " rows"
		want = append(want, batch...)
	}
	want = append(want, "COMMIT")
	if got := q.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements:\n%v\nwant\n%v", got, want)
	}
	if q.calls[2].args[0] != "1" || !strings.Contains(q.calls[2].sql, "$500") {
		t.Fatalf("first insert %q %v", q.calls[2].sql, q.calls[2].args[:1])
	}
}

// failOn fails multi-row inserts and the single-row insert of id.
func failOn(id string) func(string, []any) error {
	return func(q string, args []any) error {
		if !strings.HasPrefix(q, "INSERT") {
			return nil
		}
		if len(args) > 1 || slices.Contains(args, any(id)) {
			return errors.New("duplicate key")
		}
		return nil
	}
}

func TestRunFindsTheRejectedRow(t *testing.T) {
	q := &fakeQuerier{fail: failOn("2")}
	var done atomic.Int64
	_, err := Run(context.Background(), q, Job{Path: idsFile(t, 3), Options: DefaultOptions("ids.csv"), Plan: idsPlan("mysql")}, &done)
	var le *LineError
	if !errors.As(err, &le) || le.Line != 3 || err.Error() != "line 3: duplicate key" {
		t.Fatalf("err = %v", err)
	}
	want := []string{"START TRANSACTION", "SAVEPOINT datavim_import", "INSERT 3 rows", "ROLLBACK TO SAVEPOINT datavim_import", "INSERT 1 rows", "INSERT 1 rows", "ROLLBACK"}
	if got := q.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements:\n%v\nwant\n%v", got, want)
	}
	if done.Load() != 0 {
		t.Fatalf("done = %d", done.Load())
	}
}

func TestRunReportsTheBatchWhenNoRowFails(t *testing.T) {
	q := &fakeQuerier{fail: failOn("none")}
	_, err := Run(context.Background(), q, Job{Path: idsFile(t, 3), Options: DefaultOptions("ids.csv"), Plan: idsPlan("mysql")}, new(atomic.Int64))
	var le *LineError
	if !errors.As(err, &le) || le.Line != 0 || err.Error() != "lines 2–4: duplicate key" {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsOnABadCell(t *testing.T) {
	q := &fakeQuerier{}
	p := writeFile(t, "a.csv", "id\n1\nx\n")
	_, err := Run(context.Background(), q, Job{Path: p, Options: DefaultOptions(p), Plan: idsPlan("mysql")}, new(atomic.Int64))
	var re *RowError
	if !errors.As(err, &re) || re.Line != 3 {
		t.Fatalf("err = %v", err)
	}
	if got := q.statements(); got[len(got)-1] != "ROLLBACK" {
		t.Fatalf("statements %v", got)
	}
}

func TestRunCreatesAndDropsOnMySQL(t *testing.T) {
	job := func(t *testing.T, driver string) Job {
		return Job{Path: idsFile(t, 1), Options: DefaultOptions("ids.csv"), Plan: idsPlan(driver), Create: []string{"CREATE TABLE t"}, Drop: "DROP TABLE t"}
	}
	q := &fakeQuerier{fail: failOn("1")}
	_, err := Run(context.Background(), q, job(t, "mysql"), new(atomic.Int64))
	want := []string{"CREATE TABLE t", "START TRANSACTION", "SAVEPOINT datavim_import", "INSERT 1 rows", "ROLLBACK TO SAVEPOINT datavim_import", "INSERT 1 rows", "ROLLBACK", "DROP TABLE t"}
	if got := q.statements(); err == nil || !slices.Equal(got, want) {
		t.Fatalf("mysql: err %v, statements\n%v\nwant\n%v", err, got, want)
	}

	q = &fakeQuerier{fail: func(s string, args []any) error {
		if s == "DROP TABLE t" {
			return errors.New("locked")
		}
		return failOn("1")(s, args)
	}}
	_, err = Run(context.Background(), q, job(t, "mysql"), new(atomic.Int64))
	var left *LeftoverError
	if !errors.As(err, &left) || left.Table != "app.t" || !strings.Contains(err.Error(), "was created but is empty") {
		t.Fatalf("drop fails: %v", err)
	}

	q = &fakeQuerier{fail: failOn("1")}
	_, err = Run(context.Background(), q, job(t, "postgres"), new(atomic.Int64))
	want = []string{"BEGIN", "CREATE TABLE t", "SAVEPOINT datavim_import", "INSERT 1 rows", "ROLLBACK TO SAVEPOINT datavim_import", "INSERT 1 rows", "ROLLBACK"}
	if got := q.statements(); err == nil || !slices.Equal(got, want) {
		t.Fatalf("postgres: err %v, statements\n%v\nwant\n%v", err, got, want)
	}
}

func TestRunCancelAndConnLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	q := &fakeQuerier{fail: func(s string, _ []any) error {
		if strings.HasPrefix(s, "INSERT") {
			cancel()
			return context.Canceled
		}
		return nil
	}}
	_, err := Run(ctx, q, Job{Path: idsFile(t, 2), Options: DefaultOptions("ids.csv"), Plan: idsPlan("mysql")}, new(atomic.Int64))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if got := q.statements(); got[len(got)-1] != "ROLLBACK" || slices.Contains(got, "ROLLBACK TO SAVEPOINT datavim_import") {
		t.Fatalf("cancel statements %v", got)
	}

	q = &fakeQuerier{fail: func(s string, _ []any) error {
		if strings.HasPrefix(s, "INSERT") {
			return fmt.Errorf("%w: reset by peer", db.ErrConnLost)
		}
		return nil
	}}
	_, err = Run(context.Background(), q, Job{Path: idsFile(t, 2), Options: DefaultOptions("ids.csv"), Plan: idsPlan("mysql")}, new(atomic.Int64))
	if !db.IsConnLost(err) || slices.Contains(q.statements(), "ROLLBACK TO SAVEPOINT datavim_import") {
		t.Fatalf("conn lost: %v, %v", err, q.statements())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/importer/ -run 'Read|Check|Run'`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`internal/importer/run.go`:

```go
package importer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/sqlx"
)

const (
	// PreviewRows is how many rows the import tab shows.
	PreviewRows = 20
	// MaxErrors is how many problems Check reports.
	MaxErrors = 50
	// Savepoint is taken before every batch, so that a rejected batch can
	// be replayed row by row to find the row the server rejects.
	Savepoint = "datavim_import"

	cleanupTimeout = 5 * time.Second
)

// Sample is the start of a file.
type Sample struct {
	Header []string
	Rows   [][]string
	Lines  []int
	// BadUTF8: some sampled text is not UTF-8, so the file is likely in
	// another encoding.
	BadUTF8 bool
}

// Read returns the header and the first n rows of path. With scan set it
// reads to the end, passing every row (the first n too) to scan.
func Read(ctx context.Context, path string, o Options, n int, scan func([]string)) (Sample, error) {
	src, err := Open(path, o)
	if err != nil {
		return Sample{}, err
	}
	defer func() { _ = src.Close() }()
	s := Sample{Header: src.Header(), BadUTF8: !validUTF8(src.Header())}
	for len(s.Rows) < n || scan != nil {
		if err := ctx.Err(); err != nil {
			return Sample{}, err
		}
		row, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Sample{}, err
		}
		if len(s.Rows) < n {
			s.Rows, s.Lines = append(s.Rows, row), append(s.Lines, src.Line())
			s.BadUTF8 = s.BadUTF8 || !validUTF8(row)
		}
		if scan != nil {
			scan(row)
		}
	}
	return s, nil
}

func validUTF8(cells []string) bool {
	for _, c := range cells {
		if !utf8.ValidString(c) {
			return false
		}
	}
	return true
}

// CheckResult is what a dry run found.
type CheckResult struct {
	Rows   int         // rows that would be inserted
	Errors []*RowError // the first MaxErrors problems
	Failed int         // rows with a problem
}

// Check converts every row of path as p says, without a database. done
// counts the rows read.
func Check(ctx context.Context, path string, o Options, p Plan, done *atomic.Int64) (CheckResult, error) {
	src, err := Open(path, o)
	if err != nil {
		return CheckResult{}, err
	}
	defer func() { _ = src.Close() }()
	var r CheckResult
	for {
		if err := ctx.Err(); err != nil {
			return CheckResult{}, err
		}
		row, err := src.Next()
		if errors.Is(err, io.EOF) {
			return r, nil
		}
		if err != nil {
			return CheckResult{}, err
		}
		done.Add(1)
		_, ok, err := p.Row(src.Line(), row)
		var re *RowError
		switch {
		case errors.As(err, &re):
			r.Failed++
			if len(r.Errors) < MaxErrors {
				r.Errors = append(r.Errors, re)
			}
		case err != nil:
			return CheckResult{}, err
		case ok:
			r.Rows++
		}
	}
}

// Job is one import.
type Job struct {
	Path    string
	Options Options
	Plan    Plan
	// Create holds the statements that create the table; none when it exists.
	Create []string
	// Drop removes the created table again after a failure on MySQL, whose
	// CREATE TABLE commits at once.
	Drop string
}

// LineError is a batch the server rejected: at Line when replaying the
// batch row by row found the row, else somewhere in lines First…Last.
type LineError struct {
	Line, First, Last int
	Err               error
}

func (e *LineError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %v", e.Line, e.Err)
	}
	return fmt.Sprintf("lines %d–%d: %v", e.First, e.Last, e.Err)
}

func (e *LineError) Unwrap() error { return e.Err }

// LeftoverError is a failed import on MySQL whose new table could not be
// dropped again.
type LeftoverError struct {
	Table   string // as people write it: app.orders
	Err     error  // why the import failed
	DropErr error
}

func (e *LeftoverError) Error() string {
	return fmt.Sprintf("%v · table %s was created but is empty; drop it manually", e.Err, e.Table)
}

func (e *LeftoverError) Unwrap() error { return e.Err }

// Run imports job on s (a db.Session) in one transaction and returns the
// rows inserted; done counts them as batches land. On any failure nothing
// stays: the transaction is rolled back and, on MySQL, a table the job
// created is dropped.
func Run(ctx context.Context, s db.Querier, job Job, done *atomic.Int64) (n int, err error) {
	d := sqlx.DialectOf(job.Plan.Driver)
	created := false
	if d == sqlx.MySQL {
		for _, q := range job.Create {
			if _, err := s.Exec(ctx, q); err != nil {
				return 0, err
			}
			created = true
		}
		defer func() {
			if err == nil || !created || job.Drop == "" {
				return
			}
			if derr := detached(s, job.Drop); derr != nil {
				err = &LeftoverError{Table: displayName(d, job.Plan.Table), Err: err, DropErr: derr}
			}
		}()
	}
	if _, err := s.Exec(ctx, sqlx.BeginSQL(d)); err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	n, err = insertAll(ctx, s, d, job, done)
	if err == nil {
		if _, err = s.Exec(ctx, "COMMIT"); err != nil {
			err = fmt.Errorf("commit: %w", err)
		}
	}
	if err != nil {
		_ = detached(s, "ROLLBACK")
		return 0, err
	}
	return n, nil
}

func insertAll(ctx context.Context, s db.Querier, d sqlx.Dialect, job Job, done *atomic.Int64) (int, error) {
	if d == sqlx.Postgres {
		for _, q := range job.Create {
			if _, err := s.Exec(ctx, q); err != nil {
				return 0, err
			}
		}
	}
	src, err := Open(job.Path, job.Options)
	if err != nil {
		return 0, err
	}
	defer func() { _ = src.Close() }()
	size := BatchSize(len(job.Plan.Columns))
	var rows [][]Cell
	var lines []int
	n := 0
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		if err := insertBatch(ctx, s, job.Plan, rows, lines); err != nil {
			return err
		}
		n += len(rows)
		done.Add(int64(len(rows)))
		rows, lines = rows[:0], lines[:0]
		return nil
	}
	for {
		row, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		cells, ok, err := job.Plan.Row(src.Line(), row)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		rows, lines = append(rows, cells), append(lines, src.Line())
		if len(rows) == size {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	return n, flush()
}

// insertBatch inserts rows behind a savepoint. When the server rejects
// them it rolls back to the savepoint and replays them one by one to name
// the row it rejects.
func insertBatch(ctx context.Context, s db.Querier, p Plan, rows [][]Cell, lines []int) error {
	if _, err := s.Exec(ctx, "SAVEPOINT "+Savepoint); err != nil {
		return err
	}
	q, args := p.Insert(rows)
	_, err := s.Exec(ctx, q, args...)
	if err == nil {
		_, err = s.Exec(ctx, "RELEASE SAVEPOINT "+Savepoint)
		return err
	}
	if ctx.Err() != nil || db.IsConnLost(err) {
		return err
	}
	whole := &LineError{First: lines[0], Last: lines[len(lines)-1], Err: err}
	if _, rerr := s.Exec(ctx, "ROLLBACK TO SAVEPOINT "+Savepoint); rerr != nil {
		return whole
	}
	for i, row := range rows {
		q, args := p.Insert([][]Cell{row})
		if _, rowErr := s.Exec(ctx, q, args...); rowErr != nil {
			if ctx.Err() != nil || db.IsConnLost(rowErr) {
				return rowErr
			}
			return &LineError{Line: lines[i], Err: rowErr}
		}
	}
	return whole
}

// detached runs q even when the import's context was cancelled.
func detached(s db.Querier, q string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := s.Exec(ctx, q)
	return err
}

// displayName writes ref as people do: app.orders.
func displayName(d sqlx.Dialect, ref db.TableRef) string {
	ns := ref.Database
	if d == sqlx.Postgres {
		ns = ref.Schema
	}
	return strings.TrimPrefix(ns+"."+ref.Name, ".")
}
```

Check against `TestRunFindsTheRejectedRow`: three rows go in one batch (`INSERT 3 rows` fails), the replay inserts id 1 (ok) then id 2 (fails) → `line 3` (header is line 1). `done` stays 0 because no batch landed.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/importer/ && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/importer
git commit -m "feat(importer): dry-run 校验与单事务导入，失败定位到行"
```

---

### Task 7: Driver contract

**Files:**
- Create: `internal/db/dbtest/import.go`
- Modify: `internal/db/dbtest/contract.go` (call it at the end of `RunContract`)

**Interfaces:**
- Consumes: `importer.Open`/`DefaultOptions`/`TargetOf`/`Plan`/`Column`/`Run`/`Job`/`LineError`/`NewInferrer`/`Read`/`UniqueIdents`/`PreviewRows`; `db.Conn`; `Fixture.NS`, `Fixture.Driver`.
- Produces: `func runImportContract(t *testing.T, conn db.Conn, f Fixture)`.

- [ ] **Step 1: Write the contract**

`internal/db/dbtest/import.go`:

```go
package dbtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/importer"
)

// runImportContract checks that importer.Run writes through the driver:
// string parameters for every column type, NULL, DEFAULT, rollback, the
// rejected row, and new tables.
func runImportContract(t *testing.T, conn db.Conn, f Fixture) {
	ctx := context.Background()
	d := conn.Dialect()
	ref := func(name string) db.TableRef { return db.TableRef{Namespace: f.NS, Name: name} }
	exec := func(t *testing.T, q string) {
		t.Helper()
		if _, err := conn.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	file := func(t *testing.T, data string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "in.csv")
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(t *testing.T, job importer.Job) (int, error) {
		t.Helper()
		s, err := conn.Session(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		return importer.Run(ctx, s, job, new(atomic.Int64))
	}
	planFor := func(t *testing.T, table db.TableRef) importer.Plan {
		t.Helper()
		cols, err := conn.Meta().Columns(ctx, table)
		if err != nil {
			t.Fatal(err)
		}
		p := importer.Plan{Driver: f.Driver, Table: table, Width: len(cols)}
		for i, c := range cols {
			p.Columns = append(p.Columns, importer.Column{Source: i, Header: c.Name, Target: importer.TargetOf(f.Driver, c)})
		}
		return p
	}
	count := func(t *testing.T, q string) string {
		t.Helper()
		rs, err := conn.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return rs.Rows[0][0].Text
	}

	t.Run("importer writes text parameters into typed columns in one transaction", func(t *testing.T) {
		table := ref("imp_into")
		name := d.QualifiedName(table)
		exec(t, "DROP TABLE IF EXISTS "+name)
		exec(t, "CREATE TABLE "+name+" (id int PRIMARY KEY, name varchar(10) NOT NULL, born date, flag boolean, note text, n int NOT NULL DEFAULT 7)")
		p := planFor(t, table)
		src := file(t, "id,name,born,flag,note,n\n"+
			"1,张三,2026/1/2,yes,\"a, \"\"q\"\"\nline\",\n"+
			"2,Bob,2026-01-03 00:00:00,0,,5\n")
		job := importer.Job{Path: src, Options: importer.DefaultOptions(src), Plan: p}
		if n, err := run(t, job); err != nil || n != 2 {
			t.Fatalf("n = %d, err = %v", n, err)
		}
		rs, err := conn.Query(ctx, "SELECT id, name, note, n, CASE WHEN flag THEN 'y' ELSE 'n' END FROM "+name+" ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		want := [][]db.Value{
			{{Text: "1"}, {Text: "张三"}, {Text: "a, \"q\"\nline"}, {Text: "7"}, {Text: "y"}},
			{{Text: "2"}, {Text: "Bob"}, {Null: true}, {Text: "5"}, {Text: "n"}},
		}
		if diff := cmp.Diff(want, rs.Rows); diff != "" {
			t.Fatalf("rows (-want +got):\n%s", diff)
		}
		if got := count(t, "SELECT COUNT(*) FROM "+name+" WHERE (id = 1 AND born = DATE '2026-01-02') OR (id = 2 AND born = DATE '2026-01-03')"); got != "2" {
			t.Fatalf("dates: %s rows match", got)
		}

		// id 1 again on line 3: the server rejects it, the replay names it, nothing stays.
		dup := file(t, "id,name,born,flag,note,n\n3,c,,,,\n1,d,,,,\n")
		_, err = run(t, importer.Job{Path: dup, Options: importer.DefaultOptions(dup), Plan: p})
		var le *importer.LineError
		if !errors.As(err, &le) || le.Line != 3 {
			t.Fatalf("duplicate: %v", err)
		}
		if got := count(t, "SELECT COUNT(*) FROM "+name); got != "2" {
			t.Fatalf("%s rows after the rollback, want 2", got)
		}
	})

	t.Run("importer creates a table from the file, and leaves none when it fails", func(t *testing.T) {
		for _, tc := range []struct {
			table, data string
			ok          bool
		}{
			{"imp_new", "id,name,amount\n1,Alice,1.50\n2,Bob,22.25\n", true},
			{"imp_bad", "id,name\n1,a\nx,b\n", false},
		} {
			table := ref(tc.table)
			exec(t, "DROP TABLE IF EXISTS "+d.QualifiedName(table))
			src := file(t, tc.data)
			o := importer.DefaultOptions(src)
			in := importer.NewInferrer(f.Driver, "")
			sample, err := importer.Read(ctx, src, o, importer.PreviewRows, in.Add)
			if err != nil {
				t.Fatal(err)
			}
			schema := in.Schema(table, importer.UniqueIdents(sample.Header))
			p := importer.Plan{Driver: f.Driver, Table: table, Width: len(sample.Header)}
			for i, c := range schema.Columns {
				p.Columns = append(p.Columns, importer.Column{Source: i, Header: c.Name, Target: importer.TargetOf(f.Driver, c)})
			}
			create, _ := d.DDL().CreateTable(schema)
			job := importer.Job{Path: src, Options: o, Plan: p, Create: create, Drop: d.DDL().DropTable(table, false)}
			if tc.ok {
				if n, err := run(t, job); err != nil || n != 2 {
					t.Fatalf("%s: n = %d, err = %v", tc.table, n, err)
				}
				if got := count(t, "SELECT COUNT(*) FROM "+d.QualifiedName(table)+" WHERE amount = 22.25"); got != "1" {
					t.Fatalf("amount: %s", got)
				}
				continue
			}
			// imp_bad infers id as varchar (x is not a number); force bigint so row 3 fails.
			job.Plan.Columns[0].Target = importer.TargetOf(f.Driver, db.Column{Name: "id", Type: "bigint"})
			schema.Columns[0].Type = "bigint"
			job.Create, _ = d.DDL().CreateTable(schema)
			if _, err := run(t, job); err == nil {
				t.Fatalf("%s imported", tc.table)
			}
			tables, err := conn.Meta().Tables(ctx, f.NS)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(tables, func(x db.Table) bool { return x.Name == tc.table }) {
				t.Fatalf("%s is left behind on %s", tc.table, f.Driver)
			}
		}
	})
}
```

At the end of `RunContract` in `internal/db/dbtest/contract.go` (after the last `t.Run`), add:

```go
	runImportContract(t, conn, f)
```

- [ ] **Step 2: Run the integration tests**

Run: `orb start` (if Docker is not running), then `go test -race -tags integration ./internal/db/... -run 'TestContract/importer'`
Expected: PASS on MySQL and PostgreSQL. If MySQL reports `boolean` columns as `tinyint(1)` the plan maps them to `KindBool` already; if a driver rejects a text parameter for a typed column, fix it in `importer` (not in the test) and add a unit test there.

- [ ] **Step 3: Run the whole suite**

Run: `make test && make test-integration && make lint`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/db/dbtest
git commit -m "test(db): 导入的驱动契约测试"
```

---

### Task 8: Keymap context and actions

**Files:**
- Modify: `internal/ui/keymap/keymap.go`, `internal/ui/keymap/defaults.go`, `internal/ui/keymap/custom.go`
- Test: `internal/ui/keymap/keymap_test.go`

**Interfaces:**
- Produces: `keymap.Import` (`"import"`); actions `ImportData = "import.open"`, `ImportToggle = "import.toggle"`, `ImportAll = "import.all"`, `ImportNone = "import.none"`, `ImportType = "import.type"`. The Import context also binds the existing `Down`, `Up`, `Top`, `Bottom`, `HalfPageDown`, `HalfPageUp`, `PrevSection`, `NextSection`, `EditCell`, `SubmitEdits`.

- [ ] **Step 1: Write the failing test**

Append to `internal/ui/keymap/keymap_test.go`:

```go
func TestImportBindings(t *testing.T) {
	k := Default()
	tests := []struct {
		ctx    Context
		seq    []string
		action Action
	}{
		{Tree, []string{"I"}, ImportData},
		{Import, []string{"j"}, Down},
		{Import, []string{"]"}, NextSection},
		{Import, []string{"["}, PrevSection},
		{Import, []string{"i"}, EditCell},
		{Import, []string{"enter"}, EditCell},
		{Import, []string{"x"}, ImportToggle},
		{Import, []string{"a"}, ImportAll},
		{Import, []string{"A"}, ImportNone},
		{Import, []string{"t"}, ImportType},
		{Import, []string{"space", "w"}, SubmitEdits},
		{Import, []string{"space", "r"}, Refresh},
	}
	for _, tt := range tests {
		if a, r := k.Resolve(tt.ctx, tt.seq); a != tt.action || r != Matched {
			t.Errorf("Resolve(%s, %v) = %q, %v; want %q", tt.ctx, tt.seq, a, r, tt.action)
		}
	}
	if !slices.Contains(Contexts(), Import) {
		t.Fatalf("Contexts() = %v", Contexts())
	}
}
```

(Add `"slices"` to the test file's imports if it is not there.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -race ./internal/ui/keymap/ -run TestImportBindings`
Expected: FAIL to compile (`undefined: ImportData`).

- [ ] **Step 3: Implement**

In `internal/ui/keymap/keymap.go`, add to the `Context` constants after `Diff`:

```go
	Import    Context = "import"
```

and to the `Action` constants after `DataDiff`:

```go
	ImportData        Action = "import.open"
	ImportToggle      Action = "import.toggle"
	ImportAll         Action = "import.all"
	ImportNone        Action = "import.none"
	ImportType        Action = "import.type"
```

In `internal/ui/keymap/custom.go`:

```go
	return []Context{Global, Tree, Grid, Structure, Console, Results, Diff, Import, Popup}
```

In `internal/ui/keymap/defaults.go`, add to the Tree bindings (after the `C` Compare line):

```go
		Binding{Tree, keys("I"), ImportData, "Import data from a file (database or schema: into a new table)"},
```

and after the Diff block (before the Popup block):

```go
	b = append(b, vertical(Import)...)
	b = append(b,
		Binding{Import, keys("["), PrevSection, "Previous section"},
		Binding{Import, keys("]"), NextSection, "Next section"},
		Binding{Import, keys("i"), EditCell, "Edit the setting, the target column or the new column's name"},
		Binding{Import, keys("enter"), EditCell, "Edit the setting, the target column or the new column's name"},
		Binding{Import, keys("x"), ImportToggle, "Import / skip the column"},
		Binding{Import, keys("a"), ImportAll, "Import every column"},
		Binding{Import, keys("A"), ImportNone, "Skip every column"},
		Binding{Import, keys("t"), ImportType, "New table: the column's type"},
		Binding{Import, keys(LeaderToken, "w"), SubmitEdits, "Check the file and import it"},
	)
```

`<Space>r` (Refresh) is a Global binding already, so `{Import, space r}` resolves through Global.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/ui/... && make lint`
Expected: PASS (including `TestDefaultIsValid`, which rejects prefix conflicts).

- [ ] **Step 5: Commit**

```bash
git add internal/ui/keymap
git commit -m "feat(keymap): Import 上下文与树上的 I"
```

---

### Task 9: Open the Import tab from the tree

**Files:**
- Create: `internal/ui/importtab.go`, `internal/ui/importrun.go`, `internal/ui/import_test.go`
- Modify: `internal/ui/app.go` (Update routing, `treeAction`), `internal/ui/nodemenu.go`, `internal/ui/fakes_test.go`

**Interfaces:**
- Consumes: `importer.*` (Tasks 1–6); `keymap.Import`, `keymap.ImportData` (Task 8); `Model.tableBlocked`, `readOnlyHint`, `expandHome`, `tableParts`, `tableKey`, `withConn`, `keyHint`, `joinHints`, `plural`, `openOverlay`, `openOverlayMsg`, `overlay.NewPrompt`, `overlay.WithValidate`, `textutil.*`, `theme.*`.
- Produces (used by Tasks 10–11):
  - `type importTab struct` with the fields below, `newImportTab(id int, b Backend, home, conn, driver string, ref db.TableRef, create bool, path string) *importTab`
  - `importLoadedMsg`, `importEditMsg{tabID int; apply func(*importTab) tea.Cmd}`, `importFileMsg`
  - `(*importTab).load() tea.Cmd`, `columns() []importer.Column`, `refName() string`
  - `(*Model).importFromNode() tea.Cmd`, `(*Model).openImport(importFileMsg) tea.Cmd`, `(*Model).importBlocked(conn string, ref db.TableRef, create bool) string`
  - test helpers `importFixture(t *testing.T, m *Model, name, data string)`, `openImportOn(t *testing.T, m *Model, file, conn string, path ...string) *importTab`, `ordersCSV`

- [ ] **Step 1: Write the failing tests**

In `internal/ui/fakes_test.go`, add a field to `fakeConn` (after `schemas`):

```go
	columns    []db.Column                    // returned by Columns when set
```

and make `fakeMeta.Columns` honour it, at its top:

```go
	m.c.mu.Lock()
	cols := slices.Clone(m.c.columns)
	m.c.mu.Unlock()
	if cols != nil {
		return cols, nil
	}
```

plus a setter next to `setTables`:

```go
func (c *fakeConn) setColumns(cols []db.Column) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.columns = cols
}
```

`internal/ui/import_test.go`:

```go
package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// importFixture writes name into a temp directory that becomes the export
// directory, so the file prompt starts there.
func importFixture(t *testing.T, m *Model, name, data string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m.settings.ExportDir = dir
}

// openImportOn selects conn/path…, presses I and opens file from the export directory.
func openImportOn(t *testing.T, m *Model, file, conn string, path ...string) *importTab {
	t.Helper()
	selectNode(t, m, conn, path...)
	press(t, m, "I")
	if !strings.Contains(screen(m), "File (CSV, TSV or XLSX):") {
		t.Fatalf("no file prompt:\n%s", screen(m))
	}
	typeText(t, m, file)
	press(t, m, "enter")
	it, ok := m.activeTab().(*importTab)
	if !ok {
		t.Fatalf("active tab %T, status %s", m.activeTab(), statusLine(m))
	}
	return it
}

const ordersCSV = "ID,Name,note\n1,Alice,x\n2,Bob,y\n"

func TestImportOpensOnATable(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "local", "app", "users")
	if it.create || it.ref.Name != "users" || it.Title() != "import:users" {
		t.Fatalf("tab %+v", it)
	}
	s := screen(m)
	for _, want := range []string{"Import → app.users", "orders.csv", "format", "CSV", "[✓] ID", "→ id", "[✓] Name", "→ name", "[ ] note", "(skip)", "Alice"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
}

func TestImportOpensANewTableOnADatabase(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "order-lines.csv", ordersCSV)
	it := openImportOn(t, m, "order-lines.csv", "local", "app")
	if !it.create || it.ref.Name != "order_lines" || it.ref.Database != "app" {
		t.Fatalf("tab %+v", it.ref)
	}
	s := screen(m)
	for _, want := range []string{"New table → app.order_lines", "[✓] ID", "bigint", "[✓] note", "varchar(16)"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
}

func TestImportRefusals(t *testing.T) {
	m, b := newTestModel(t)
	selectNode(t, m, "local", "app", "active")
	press(t, m, "I")
	if !strings.Contains(statusLine(m), "select a table, database or schema") {
		t.Fatalf("view: %s", statusLine(m))
	}
	selectNode(t, m, "prod", "app")
	press(t, m, "I")
	if !strings.Contains(statusLine(m), "select a schema") {
		t.Fatalf("PostgreSQL database: %s", statusLine(m))
	}
	if err := b.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	selectNode(t, m, "local", "app", "users")
	press(t, m, "I")
	if !strings.Contains(statusLine(m), "read-only") {
		t.Fatalf("read-only: %s", statusLine(m))
	}
	if err := b.SetReadOnly("local", false); err != nil {
		t.Fatal(err)
	}
	openTxConsole(t, m)
	selectNode(t, m, "local", "app")
	press(t, m, "I")
	if !strings.Contains(statusLine(m), "open transaction") {
		t.Fatalf("console tx: %s", statusLine(m))
	}
	if m.overlay != nil {
		t.Fatal("a refusal opened the prompt")
	}
}

func TestImportMissingFileStaysInTheTab(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "nope.csv", "local", "app", "users")
	if it.loadErr == "" || !strings.Contains(screen(m), "nope.csv") {
		t.Fatalf("loadErr %q:\n%s", it.loadErr, screen(m))
	}
}

func TestImportHintsAtGBK(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "g.csv", "\xc3\xfb\xd7\xd6\n\xd5\xc5\xc8\xfd\n")
	openImportOn(t, m, "g.csv", "local", "app", "users")
	if !strings.Contains(screen(m), "not UTF-8") {
		t.Fatalf("no encoding hint:\n%s", screen(m))
	}
}

func TestImportInNodeMenu(t *testing.T) {
	m, _ := newTestModel(t)
	selectNode(t, m, "local", "app", "users")
	press(t, m, "a")
	if !strings.Contains(screen(m), "Import data from file") {
		t.Fatalf("table menu:\n%s", screen(m))
	}
	press(t, m, "esc")
	selectNode(t, m, "local", "app")
	press(t, m, "a")
	if !strings.Contains(screen(m), "Create table from file") {
		t.Fatalf("database menu:\n%s", screen(m))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/ui/ -run TestImport`
Expected: FAIL to compile (`undefined: importTab`).

- [ ] **Step 3: Implement the tab**

`internal/ui/importtab.go`:

```go
package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/importer"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// importTimeout bounds reading, checking or importing one file.
const importTimeout = 30 * time.Minute

// importTick is how often the progress of a check or an import is redrawn.
const importTick = 200 * time.Millisecond

type importPhase int

const (
	importIdle importPhase = iota
	importChecking
	importRunning
)

// The Import tab's sections.
const (
	secSource = iota
	secMapping
	secPreview
	sectionCount
)

// mapRow is the mapping of one file column.
type mapRow struct {
	picked bool
	target int    // existing table: index in cols, -1 for none
	name   string // new table: the column's name
	typ    string // new table: the column's type
}

// importTab imports a CSV, TSV or XLSX file into a table, or into a new
// table of a database or schema (create).
type importTab struct {
	id      int
	key     string
	keys    *keymap.Keymap
	backend Backend
	home    string
	conn    string
	driver  string
	ref     db.TableRef // the table; when create, ref.Name is the new table's name
	create  bool
	path    string
	opts    importer.Options
	null    string
	tick    time.Duration

	gen     int
	loading bool
	cancel  context.CancelFunc
	loadErr string
	sample  importer.Sample
	cols    []db.Column        // the table's columns
	infer   *importer.Inferrer // create: what the file's values look like
	rows    []mapRow

	phase    importPhase
	progress *atomic.Int64
	total    int
	problems []*importer.RowError
	failed   int
	job      *importer.Job // checked, waiting for the confirmation

	section       int
	cursor        [sectionCount]int
	offset        [sectionCount]int
	width, height int
	status        string
	statusErr     bool
}

func importTabKey(conn string, ref db.TableRef, path string) string {
	return "import\x00" + tableKey(conn, ref) + "\x00" + path
}

func newImportTab(id int, b Backend, home, conn, driver string, ref db.TableRef, create bool, path string) *importTab {
	return &importTab{
		id: id, key: importTabKey(conn, ref, path), keys: keymap.Default(), backend: b, home: home,
		conn: conn, driver: driver, ref: ref, create: create, path: path,
		opts: importer.DefaultOptions(path), tick: importTick, section: secMapping,
	}
}

func (t *importTab) ID() int                       { return t.id }
func (t *importTab) Title() string                 { return "import:" + t.ref.Name }
func (t *importTab) Key() string                   { return t.key }
func (t *importTab) Context() keymap.Context       { return keymap.Import }
func (t *importTab) Target() (string, db.TableRef) { return t.conn, t.ref }
func (t *importTab) Init() tea.Cmd                 { return t.load() }
func (t *importTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }

func (t *importTab) setStatus(s string, isErr bool) { t.status, t.statusErr = s, isErr }

// refName is the table as people write it: app.users.
func (t *importTab) refName() string { return strings.Join(tableParts(t.driver, t.ref), ".") }

type importLoadedMsg struct {
	tabID, gen int
	sample     importer.Sample
	cols       []db.Column
	infer      *importer.Inferrer
	err        error
}

func (m importLoadedMsg) TabID() int { return m.tabID }

// importEditMsg carries an edit made in a popup back to the tab.
type importEditMsg struct {
	tabID int
	apply func(t *importTab) tea.Cmd
}

func (m importEditMsg) TabID() int { return m.tabID }

// load reads the file's sample (and, for a new table, every row for type
// inference) and the table's columns; a load still running is cancelled.
func (t *importTab) load() tea.Cmd {
	if t.cancel != nil {
		t.cancel()
	}
	t.gen++
	t.loading, t.loadErr, t.problems, t.failed = true, "", nil, 0
	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	t.cancel = cancel
	id, gen, b, conn, ref, path, o := t.id, t.gen, t.backend, t.conn, t.ref, t.path, t.opts
	create, driver, null := t.create, t.driver, t.null
	return func() tea.Msg {
		defer cancel()
		msg := importLoadedMsg{tabID: id, gen: gen}
		var scan func([]string)
		if create {
			msg.infer = importer.NewInferrer(driver, null)
			scan = msg.infer.Add
		}
		msg.sample, msg.err = importer.Read(ctx, path, o, importer.PreviewRows, scan)
		if msg.err == nil && !create {
			msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
				var err error
				msg.cols, err = c.Meta().Columns(ctx, ref)
				return err
			})
		}
		return msg
	}
}

func (t *importTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case importLoadedMsg:
		t.loaded(m)
	case importEditMsg:
		return m.apply(t)
	}
	return nil
}

func (t *importTab) loaded(m importLoadedMsg) {
	if m.gen != t.gen {
		return
	}
	t.loading, t.cancel = false, nil
	if m.err != nil {
		t.loadErr, t.sample, t.rows = errorText(m.err), importer.Sample{}, nil
		return
	}
	sameHeader := slices.Equal(t.sample.Header, m.sample.Header) && len(t.rows) == len(m.sample.Header)
	t.sample, t.cols, t.infer = m.sample, m.cols, m.infer
	if sameHeader {
		return
	}
	if t.create {
		t.mapNew()
	} else {
		t.mapByName()
	}
	// The Source cursor stays on the setting just changed.
	for _, s := range []int{secMapping, secPreview} {
		t.cursor[s], t.offset[s] = 0, 0
	}
}

// mapByName maps file columns to table columns of the same name (ignoring
// case and _ - space); the rest are skipped. Generated columns are never
// targets.
func (t *importTab) mapByName() {
	t.rows = make([]mapRow, len(t.sample.Header))
	used := map[int]bool{}
	for i, h := range t.sample.Header {
		t.rows[i].target = -1
		for j, c := range t.cols {
			if c.Generated == "" && !used[j] && importer.MatchKey(c.Name) == importer.MatchKey(h) {
				t.rows[i], used[j] = mapRow{picked: true, target: j}, true
				break
			}
		}
	}
}

// mapNew picks every file column for the new table, named after the header
// and typed by inference.
func (t *importTab) mapNew() {
	names := importer.UniqueIdents(t.sample.Header)
	schema := t.infer.Schema(t.ref, names)
	t.rows = make([]mapRow, len(names))
	for i, n := range names {
		t.rows[i] = mapRow{picked: true, target: -1, name: n, typ: schema.Columns[i].Type}
	}
}

// columns is the picked columns with their targets, in file order.
func (t *importTab) columns() []importer.Column {
	var out []importer.Column
	for i, r := range t.rows {
		if !r.picked {
			continue
		}
		var c db.Column
		switch {
		case t.create:
			c = db.Column{Name: r.name, Type: r.typ, Nullable: t.newNullable(i)}
		case r.target >= 0 && r.target < len(t.cols):
			c = t.cols[r.target]
		default:
			continue
		}
		out = append(out, importer.Column{Source: i, Header: t.sample.Header[i], Target: importer.TargetOf(t.driver, c)})
	}
	return out
}

// newNullable says whether column i of a new table allows NULL: when some
// row had no value for it.
func (t *importTab) newNullable(i int) bool {
	if t.infer == nil {
		return true
	}
	return t.infer.Schema(t.ref, make([]string, len(t.rows))).Columns[i].Nullable
}

// missing lists the table's columns that need a value and get none: NOT
// NULL, no default, not auto-increment, not generated, not mapped.
func (t *importTab) missing() []string {
	if t.create {
		return nil
	}
	used := map[int]bool{}
	for _, r := range t.rows {
		if r.picked && r.target >= 0 {
			used[r.target] = true
		}
	}
	var out []string
	for j, c := range t.cols {
		if !used[j] && !c.Nullable && c.Default == nil && !c.AutoIncrement && c.Generated == "" {
			out = append(out, c.Name)
		}
	}
	return out
}

// importSetting is a row of the Source section.
type importSetting int

const (
	setFile importSetting = iota
	setFormat
	setDelimiter
	setEncoding
	setSheet
	setHeader
	setNull
	setTable
)

func (t *importTab) settings() []importSetting {
	out := []importSetting{setFile, setFormat}
	switch t.opts.Format {
	case importer.CSV:
		out = append(out, setDelimiter, setEncoding)
	case importer.TSV:
		out = append(out, setEncoding)
	case importer.XLSX:
		out = append(out, setSheet)
	}
	out = append(out, setHeader, setNull)
	if t.create {
		out = append(out, setTable)
	}
	return out
}

func (t *importTab) settingText(s importSetting) string {
	var name, value string
	switch s {
	case setFile:
		name, value = "file", tildePath(t.path)
	case setFormat:
		name, value = "format", strings.ToUpper(string(t.opts.Format))
	case setDelimiter:
		name, value = "delimiter", string(t.opts.Delimiter)
	case setEncoding:
		name, value = "encoding", string(t.opts.Encoding)
	case setSheet:
		name, value = "sheet", t.opts.Sheet
		if value == "" {
			value = "(first)"
		}
	case setHeader:
		name, value = "header", fmt.Sprintf("row %d", t.opts.HeaderRow)
		if t.opts.HeaderRow == 0 {
			value = "none"
		}
	case setNull:
		name, value = "null", fmt.Sprintf("%q", t.null)
	case setTable:
		name, value = "table", t.refName()
	}
	return fmt.Sprintf("%-10s %s", name, value)
}

// sectionLen is how many rows the cursor can be on in section s.
func (t *importTab) sectionLen(s int) int {
	switch s {
	case secSource:
		return len(t.settings())
	case secMapping:
		return len(t.rows)
	}
	if t.problems != nil {
		return len(t.problems)
	}
	return len(t.sample.Rows)
}

func (t *importTab) header() string {
	title := "Import → " + t.refName()
	if t.create {
		title = "New table → " + t.refName()
	}
	head := theme.Title.Render(textutil.Sanitize(title))
	if cfg, _ := t.backend.Connection(t.conn); cfg.Env == config.EnvProd {
		head += " " + theme.Prod.Render("[PROD]")
	}
	return head + "   " + theme.Muted.Render(textutil.Sanitize(filepath.Base(t.path)))
}

func (t *importTab) sectionTitle(s int, title string) string {
	if s == t.section {
		return theme.Accent.Render("▌" + title)
	}
	return theme.Muted.Render(" " + title)
}

// window cuts lines to h, scrolling section s so its cursor shows.
func (t *importTab) window(s int, lines []string, h int) []string {
	if h <= 0 || len(lines) == 0 {
		return nil
	}
	cur := textutil.Clamp(t.cursor[s], 0, len(lines)-1)
	t.cursor[s] = cur
	off := t.offset[s]
	if cur < off {
		off = cur
	}
	if cur >= off+h {
		off = cur - h + 1
	}
	off = textutil.Clamp(off, 0, max(len(lines)-h, 0))
	t.offset[s] = off
	out := slices.Clone(lines[off:min(off+h, len(lines))])
	for i := range out {
		line := textutil.Fit(out[i], t.width)
		if s == t.section && off+i == cur {
			line = theme.Selected.Render(textutil.Fit(stripStyle(out[i]), t.width))
		}
		out[i] = line
	}
	return out
}

func (t *importTab) sourceLines() []string {
	var out []string
	for _, s := range t.settings() {
		out = append(out, "  "+textutil.Sanitize(t.settingText(s)))
	}
	return out
}

func (t *importTab) mappingLines() []string {
	width := 0
	for _, h := range t.sample.Header {
		width = max(width, min(24, len([]rune(h))))
	}
	var out []string
	for i, r := range t.rows {
		box := "[ ]"
		if r.picked {
			box = "[✓]"
		}
		target := "(skip)"
		switch {
		case t.create:
			target = r.name + "  " + r.typ
		case r.target >= 0:
			c := t.cols[r.target]
			target = c.Name + "  " + c.Type
		}
		h := []rune(t.sample.Header[i])
		if len(h) > 24 {
			h = append(h[:23], '…')
		}
		out = append(out, textutil.Sanitize(fmt.Sprintf("  %s %-*s  → %s", box, width, string(h), target)))
	}
	return out
}

// previewLines is the first rows as they will be inserted, under a header
// and a rule; a row with a cell that cannot be converted shows ✗ and the
// cell's text, in red.
func (t *importTab) previewLines() (head, rows []string) {
	cols := t.columns()
	if len(cols) == 0 {
		return []string{theme.Muted.Render("  nothing picked")}, nil
	}
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = clipCell(c.Target.Name)
	}
	cells := make([][]string, len(t.sample.Rows))
	bad := make([]bool, len(t.sample.Rows))
	for r, row := range t.sample.Rows {
		cells[r] = make([]string, len(cols))
		for i, c := range cols {
			text := ""
			if c.Source < len(row) {
				text = row[c.Source]
			}
			v, err := importer.Convert(c.Target, text, t.null)
			switch {
			case err != nil:
				cells[r][i], bad[r] = "✗ "+clipCell(text), true
			case v.Null:
				cells[r][i] = "NULL"
			case v.Default:
				cells[r][i] = "DEFAULT"
			default:
				cells[r][i] = clipCell(v.Text)
			}
		}
	}
	lines := textutil.Table(headers, cells)
	head = []string{"  " + theme.Header.Render(lines[0]), "  " + theme.Muted.Render(lines[1])}
	if len(t.sample.Rows) == 0 {
		return append(head, theme.Muted.Render("  the file has no rows")), nil
	}
	for r, l := range lines[2:] {
		if bad[r] {
			l = theme.Error.Render(l)
		}
		rows = append(rows, "  "+l)
	}
	return head, rows
}

// previewError is why preview row i cannot be imported ("" when it can).
func (t *importTab) previewError(i int) string {
	if i < 0 || i >= len(t.sample.Rows) {
		return ""
	}
	p := importer.Plan{Columns: t.columns(), Width: len(t.sample.Header), Null: t.null}
	if _, _, err := p.Row(t.sample.Lines[i], t.sample.Rows[i]); err != nil {
		return err.Error()
	}
	return ""
}

func clipCell(s string) string {
	s = strings.ReplaceAll(s, "\n", "⏎")
	if r := []rune(s); len(r) > 24 {
		return string(r[:23]) + "…"
	}
	return s
}

func (t *importTab) problemLines() []string {
	out := make([]string, 0, len(t.problems)+1)
	for _, p := range t.problems {
		out = append(out, "  "+theme.Error.Render(textutil.Sanitize(p.Error())))
	}
	if more := t.failed - len(t.problems); more > 0 {
		out = append(out, theme.Muted.Render(fmt.Sprintf("  … and %s more", plural(more, "row"))))
	}
	return out
}

func (t *importTab) View(bool) string {
	lines := []string{textutil.Fit(t.header(), t.width)}
	avail := max(t.height-2, 3)
	src := t.sourceLines()
	lines = append(lines, t.sectionTitle(secSource, "Source"))
	lines = append(lines, t.window(secSource, src, len(src))...)
	if t.loadErr != "" {
		lines = append(lines, theme.Error.Render(textutil.Fit("  ✗ "+textutil.Sanitize(t.loadErr), t.width)))
	} else if t.sample.BadUTF8 && t.opts.Encoding == importer.UTF8 {
		lines = append(lines, theme.Error.Render(textutil.Fit("  the file is not UTF-8 · try encoding GBK", t.width)))
	}
	rest := avail - len(lines)
	mapH := max(min(len(t.rows), (rest-4)/2), 1)
	lines = append(lines, t.sectionTitle(secMapping, "Mapping"))
	switch {
	case t.loading:
		lines = append(lines, theme.Muted.Render("  reading the file…"))
	default:
		lines = append(lines, t.window(secMapping, t.mappingLines(), mapH)...)
	}
	if miss := t.missing(); len(miss) > 0 {
		lines = append(lines, theme.Error.Render(textutil.Fit("  "+t.refName()+" needs values for "+strings.Join(miss, ", "), t.width)))
	}
	title, head, body := "Preview", []string(nil), []string(nil)
	if t.problems != nil {
		title, body = fmt.Sprintf("Problems (%s)", plural(t.failed, "row")), t.problemLines()
	} else {
		head, body = t.previewLines()
	}
	lines = append(lines, t.sectionTitle(secPreview, title))
	if !t.loading && t.loadErr == "" {
		for _, h := range head {
			lines = append(lines, textutil.Fit(h, t.width))
		}
		lines = append(lines, t.window(secPreview, body, max(avail-len(lines), 1))...)
	}
	for len(lines) < t.height-1 {
		lines = append(lines, "")
	}
	lines = lines[:max(t.height-1, 1)]
	return strings.Join(append(lines, textutil.Fit(t.statusLine(), t.width)), "\n")
}

func (t *importTab) statusLine() string {
	switch {
	case t.status != "" && t.statusErr:
		return theme.Error.Render(t.status)
	case t.status != "":
		return t.status
	case t.phase != importIdle:
		return theme.Muted.Render(joinHints(t.progressText(), keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels")))
	case t.section == secPreview && t.problems == nil:
		if e := t.previewError(t.cursor[secPreview]); e != "" {
			return theme.Error.Render(textutil.Sanitize(e))
		}
	}
	return theme.Muted.Render(joinHints(
		keyHint(t.keys, keymap.Import, keymap.NextSection, "next section"),
		keyHint(t.keys, keymap.Import, keymap.ImportToggle, "import/skip"),
		keyHint(t.keys, keymap.Import, keymap.EditCell, "edit"),
		keyHint(t.keys, keymap.Import, keymap.SubmitEdits, "import"),
	))
}

// progressText is set up in Task 11; until then it is empty.
func (t *importTab) progressText() string { return "" }
```

`tildePath(p string) string` already exists in `consolefile.go`. Add `stripStyle` to `importtab.go`:

```go
// stripStyle drops ANSI styling, so a selected line is drawn in one style.
func stripStyle(s string) string { return ansi.Strip(s) }
```

with `"github.com/charmbracelet/x/ansi"` imported (the package `textutil` uses).

Until Task 10 adds key handling, the tab needs a `Handle` to satisfy the `tab` interface; add it to `importtab.go` (Task 10 replaces it):

```go
func (t *importTab) Handle(keymap.Action) tea.Cmd { return nil }
```

`internal/ui/importrun.go` (entry; Task 11 adds the run):

```go
package ui

import (
	"errors"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/importer"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/tree"
)

// importFileMsg is the file typed at the prompt that starts an import.
type importFileMsg struct {
	conn   string
	ref    db.TableRef // the table; for a new table only the namespace
	create bool
	path   string
}

// importBlocked says why an import on conn cannot start now ("" when it
// can): read-only, a console in a transaction on conn, staged changes of the
// table.
func (m *Model) importBlocked(conn string, ref db.TableRef, create bool) string {
	if !create {
		return m.tableBlocked(conn, ref, nil)
	}
	if cfg, _ := m.backend.Connection(conn); cfg.IsReadOnly() {
		return readOnlyHint
	}
	for _, t := range m.tabs {
		if c, ok := t.(*consoleTab); ok && c.conn == conn && c.openTx() {
			return "a console on " + conn + " has an open transaction · :commit or :rollback first"
		}
	}
	return ""
}

// importFromNode asks for the file to import into the selected table, or
// into a new table of the selected database or schema.
func (m *Model) importFromNode() tea.Cmd {
	n := m.tree.Selected()
	if n == nil {
		return nil
	}
	cfg, _ := m.backend.Connection(n.Conn)
	var ref db.TableRef
	create := false
	switch {
	case n.Kind == tree.KindTable:
		ref = n.TableRef()
	case n.Kind == tree.KindSchema, n.Kind == tree.KindDatabase && cfg.Driver != "postgres":
		ref, create = db.TableRef{Namespace: n.NS}, true
	case n.Kind == tree.KindDatabase:
		m.setStatus("PostgreSQL creates tables in a schema · select a schema", true)
		return nil
	default:
		m.setStatus("select a table, database or schema to import into", true)
		return nil
	}
	if why := m.importBlocked(n.Conn, ref, create); why != "" {
		m.setStatus(why, true)
		return nil
	}
	// A namespace-only ref joins as "app.", hence the TrimSuffix.
	where := strings.TrimSuffix(strings.Join(tableParts(cfg.Driver, ref), "."), ".")
	title := "Import into " + where
	if create {
		title = "Create a table in " + where
	}
	conn := n.Conn
	start := m.settings.ExportDirectory(m.home) + string(filepath.Separator)
	m.overlay = overlay.NewPrompt(title, "File (CSV, TSV or XLSX):", start, func(s string) tea.Msg {
		return importFileMsg{conn: conn, ref: ref, create: create, path: s}
	}, overlay.WithValidate(func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("enter a file")
		}
		return nil
	}))
	return nil
}

// openImport opens the Import tab for the file typed at the prompt.
func (m *Model) openImport(msg importFileMsg) tea.Cmd {
	path := expandHome(strings.TrimSpace(msg.path), m.home)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	ref := msg.ref
	if msg.create {
		ref.Name = importer.Ident(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), "imported")
	}
	cfg, _ := m.backend.Connection(msg.conn)
	return m.showTab(importTabKey(msg.conn, msg.ref, path), func(id int) tab {
		t := newImportTab(id, m.backend, m.home, msg.conn, cfg.Driver, ref, msg.create, path)
		t.keys = m.keys
		return t
	})
}
```

In `internal/ui/app.go`:
- In `Update`, before `case tabMsg:` (e.g. after `case tableOpDoneMsg:`), add:

```go
	case importFileMsg:
		return m, m.openImport(msg)
```

- In `treeAction`, add:

```go
	case keymap.ImportData:
		return m.importFromNode()
```

In `internal/ui/nodemenu.go`:
- `case tree.KindDatabase, tree.KindSchema:` — after the Refresh entry, add (the PostgreSQL database node has no import):

```go
		if cfg, _ := m.backend.Connection(n.Conn); n.Kind == tree.KindSchema || cfg.Driver != "postgres" {
			entries = append(entries, entry{keymap.ImportData, "Create table from file…"})
		}
```

- `case tree.KindTable, tree.KindView:` — inside the `if n.Kind == tree.KindTable` block that adds Modify table, add after it:

```go
			entries = append(entries, entry{keymap.ImportData, "Import data from file…"})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/ui/ -run 'TestImport|TestActionMenu' && make test && make lint`
Expected: PASS. If `TestImportOpensANewTableOnADatabase` shows `varchar(16)` missing for `note`, check the sample has the values `x`, `y` (text) — the type comes from `Inferrer` (Task 5).

- [ ] **Step 5: Commit**

```bash
git add internal/ui
git commit -m "feat(ui): 从树上打开 Import tab，读取文件并按列名映射"
```

---

### Task 10: Editing settings and the mapping

**Files:**
- Modify: `internal/ui/importtab.go`
- Test: `internal/ui/import_test.go`

**Interfaces:**
- Consumes: Task 9's tab; `overlay.NewPrompt`, `overlay.NewPicker`, `overlay.WithValidate`, `openOverlay`, `openOverlayMsg`.
- Produces: `(*importTab).Handle(a keymap.Action) tea.Cmd`, `edit`, `editSetting`, `editMapping`, `editType`, `toggle`, `pickAll`, `setTarget`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/ui/import_test.go` (and add `"github.com/shepherd-yun/datavim/internal/db"` to its imports). The tab draws its own status as its last line, above the root status bar, so tab messages are checked with `screen(m)`; `statusLine(m)` is the root's.

```go
func TestImportMappingKeys(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "local", "app", "users")
	// cursor starts on the first mapping row (ID → id)
	press(t, m, "x")
	if it.rows[0].picked {
		t.Fatal("x did not skip ID")
	}
	if !strings.Contains(screen(m), "app.users needs values for id") {
		t.Fatalf("missing id not shown:\n%s", screen(m))
	}
	press(t, m, "x")
	press(t, m, "j", "j", "x") // note has no target
	if it.rows[2].picked || !strings.Contains(screen(m), "no target column") {
		t.Fatalf("note: %+v\n%s", it.rows[2], screen(m))
	}
	press(t, m, "A")
	if it.rows[0].picked || it.rows[1].picked {
		t.Fatal("A left columns picked")
	}
	press(t, m, "a")
	if !it.rows[0].picked || !it.rows[1].picked || it.rows[2].picked {
		t.Fatalf("a: %+v", it.rows)
	}
	// i on note: pick name → refused while Name holds it
	press(t, m, "i")
	typeText(t, m, "name")
	press(t, m, "enter")
	if it.rows[2].target != -1 || !strings.Contains(screen(m), "name already takes Name") {
		t.Fatalf("conflict: %+v\n%s", it.rows[2], screen(m))
	}
	// skip Name, then note can take name
	press(t, m, "k", "x", "j", "i")
	typeText(t, m, "name")
	press(t, m, "enter")
	if it.rows[2].target != 1 || !it.rows[2].picked || it.rows[1].target != -1 {
		t.Fatalf("steal: %+v", it.rows)
	}
}

func TestImportSourceSettings(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "orders.csv", "ID;Name\n1;Alice\n")
	it := openImportOn(t, m, "orders.csv", "local", "app", "users")
	if len(it.sample.Header) != 1 {
		t.Fatalf("header %v", it.sample.Header)
	}
	press(t, m, "[") // to Source; rows: file, format, delimiter, encoding, header, null
	press(t, m, "j", "j", "enter")
	if it.opts.Delimiter != ';' || len(it.sample.Header) != 2 || !it.rows[1].picked {
		t.Fatalf("delimiter %q, header %v, rows %+v", it.opts.Delimiter, it.sample.Header, it.rows)
	}
	press(t, m, "j", "j", "enter") // header row prompt
	press(t, m, "ctrl+u")
	typeText(t, m, "0")
	press(t, m, "enter")
	if it.opts.HeaderRow != 0 || it.sample.Header[0] != "column_1" {
		t.Fatalf("no header: %+v", it.sample.Header)
	}
}

func TestImportNewTableEdits(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "local", "app")
	press(t, m, "i", "ctrl+u")
	typeText(t, m, "order_id")
	press(t, m, "enter", "t", "ctrl+u")
	typeText(t, m, "int")
	press(t, m, "enter")
	if it.rows[0].name != "order_id" || it.rows[0].typ != "int" {
		t.Fatalf("row %+v", it.rows[0])
	}
	if !strings.Contains(screen(m), "order_id  int") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestImportPreviewNamesTheBadCell(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "orders.csv", "ID,Name\n1,a\nabc,b\n")
	openImportOn(t, m, "orders.csv", "local", "app", "users")
	press(t, m, "]", "j") // the preview, second row
	if !strings.Contains(screen(m), `line 3, ID: "abc" is not an integer`) || !strings.Contains(screen(m), "✗ abc") {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestImportSkipsGeneratedAndAutoIncrement(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setColumns([]db.Column{
		{Name: "id", Type: "bigint", PrimaryKey: true, AutoIncrement: true},
		{Name: "name", Type: "text", Nullable: true},
		{Name: "upper_name", Type: "text", Nullable: true, Generated: "upper(name)"},
	})
	importFixture(t, m, "p.csv", "name,upper_name\na,A\n")
	it := openImportOn(t, m, "p.csv", "local", "app", "users")
	if it.rows[1].picked || it.rows[1].target != -1 {
		t.Fatalf("generated column mapped: %+v", it.rows[1])
	}
	if strings.Contains(screen(m), "needs values for") {
		t.Fatalf("auto-increment reported missing:\n%s", screen(m))
	}
	press(t, m, "j", "i")
	if strings.Contains(screen(m), "upper_name  text") {
		t.Fatalf("generated column offered:\n%s", screen(m))
	}
}
```

`ctrl+u` clears a prompt (`overlay/input.go`); typing in a picker filters and `enter` picks the first match.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/ui/ -run 'TestImport(Mapping|Source|NewTable|Skips)'`
Expected: FAIL (keys do nothing: `importTab` has no `Handle`).

- [ ] **Step 3: Implement**

Add to `internal/ui/importtab.go`:

```go
func (t *importTab) Handle(a keymap.Action) tea.Cmd {
	t.setStatus("", false)
	if t.phase != importIdle {
		t.setStatus(joinHints("importing…", keyHint(t.keys, keymap.Global, keymap.Cancel, "cancels")), false)
		return nil
	}
	switch a {
	case keymap.Down:
		t.move(1)
	case keymap.Up:
		t.move(-1)
	case keymap.Top:
		t.cursor[t.section] = 0
	case keymap.Bottom:
		t.move(t.sectionLen(t.section))
	case keymap.HalfPageDown:
		t.move(max(t.height/4, 1))
	case keymap.HalfPageUp:
		t.move(-max(t.height/4, 1))
	case keymap.NextSection:
		t.section = (t.section + 1) % sectionCount
	case keymap.PrevSection:
		t.section = (t.section + sectionCount - 1) % sectionCount
	case keymap.EditCell:
		return t.edit()
	case keymap.ImportToggle:
		t.toggle()
	case keymap.ImportAll:
		t.pickAll(true)
	case keymap.ImportNone:
		t.pickAll(false)
	case keymap.ImportType:
		return t.editType()
	case keymap.Refresh:
		return t.load()
	case keymap.SubmitEdits:
		return t.submit()
	}
	return nil
}

func (t *importTab) move(delta int) {
	t.cursor[t.section] = textutil.Clamp(t.cursor[t.section]+delta, 0, max(t.sectionLen(t.section)-1, 0))
}

// mapCursor is the mapping row under the cursor; outside the mapping it
// says where the mapping keys work.
func (t *importTab) mapCursor() (int, bool) {
	if t.section != secMapping {
		t.setStatus(joinHints("this works in the mapping", keyHint(t.keys, keymap.Import, keymap.NextSection, "moves between sections")), true)
		return 0, false
	}
	if len(t.rows) == 0 {
		return 0, false
	}
	return textutil.Clamp(t.cursor[secMapping], 0, len(t.rows)-1), true
}

func (t *importTab) toggle() {
	i, ok := t.mapCursor()
	if !ok {
		return
	}
	if !t.create && t.rows[i].target < 0 {
		t.setStatus(joinHints("no target column", keyHint(t.keys, keymap.Import, keymap.EditCell, "picks one")), true)
		return
	}
	t.rows[i].picked = !t.rows[i].picked
}

func (t *importTab) pickAll(on bool) {
	if _, ok := t.mapCursor(); !ok {
		return
	}
	for i := range t.rows {
		if t.create || t.rows[i].target >= 0 {
			t.rows[i].picked = on
		}
	}
}

// setTarget maps file column i to table column target (-1: skip). A target
// held by a skipped row moves over; one held by a picked row is refused.
func (t *importTab) setTarget(i, target int) {
	if target < 0 {
		t.rows[i] = mapRow{target: -1}
		return
	}
	for j, r := range t.rows {
		if j == i || r.target != target {
			continue
		}
		if r.picked {
			t.setStatus(t.cols[target].Name+" already takes "+t.sample.Header[j], true)
			return
		}
		t.rows[j].target = -1
	}
	t.rows[i] = mapRow{picked: true, target: target}
}

func (t *importTab) edit() tea.Cmd {
	switch t.section {
	case secSource:
		s := t.settings()
		return t.editSetting(s[textutil.Clamp(t.cursor[secSource], 0, len(s)-1)])
	case secMapping:
		return t.editMapping()
	}
	return nil
}

func nonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("enter a value")
	}
	return nil
}

// ask opens a prompt whose answer set applies to the tab.
func (t *importTab) ask(title, label, initial string, validate func(string) error, set func(t *importTab, v string) tea.Cmd) tea.Cmd {
	id := t.id
	var opts []overlay.PromptOption
	if validate != nil {
		opts = append(opts, overlay.WithValidate(validate))
	}
	return openOverlay(overlay.NewPrompt(title, label, initial, func(v string) tea.Msg {
		return importEditMsg{tabID: id, apply: func(t *importTab) tea.Cmd { return set(t, v) }}
	}, opts...))
}

func next[T comparable](list []T, cur T) T {
	return list[(slices.Index(list, cur)+1)%len(list)]
}

func (t *importTab) editSetting(s importSetting) tea.Cmd {
	switch s {
	case setFile:
		return t.ask("Import", "File:", t.path, nonEmpty, func(t *importTab, v string) tea.Cmd {
			p := expandHome(strings.TrimSpace(v), t.home)
			if abs, err := filepath.Abs(p); err == nil {
				p = abs
			}
			t.path, t.opts.Format, t.opts.Sheet = p, importer.DefaultOptions(p).Format, ""
			return t.load()
		})
	case setFormat:
		t.opts.Format, t.opts.Sheet = next([]importer.Format{importer.CSV, importer.TSV, importer.XLSX}, t.opts.Format), ""
		return t.load()
	case setDelimiter:
		t.opts.Delimiter = next([]rune{',', ';', '|'}, t.opts.Delimiter)
		return t.load()
	case setEncoding:
		t.opts.Encoding = next([]importer.Encoding{importer.UTF8, importer.GBK}, t.opts.Encoding)
		return t.load()
	case setHeader:
		return t.ask("Header", "Row with the column names (0: none):", strconv.Itoa(t.opts.HeaderRow), func(v string) error {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || n < 0 {
				return errors.New("enter a row number, 0 for none")
			}
			return nil
		}, func(t *importTab, v string) tea.Cmd {
			t.opts.HeaderRow, _ = strconv.Atoi(strings.TrimSpace(v))
			return t.load()
		})
	case setSheet:
		id, path := t.id, t.path
		return func() tea.Msg {
			names, err := importer.Sheets(path)
			if err != nil {
				return importEditMsg{tabID: id, apply: func(t *importTab) tea.Cmd { t.setStatus(errorText(err), true); return nil }}
			}
			return openOverlayMsg{overlay: overlay.NewPicker("Sheet", names, func(i int) tea.Msg {
				return importEditMsg{tabID: id, apply: func(t *importTab) tea.Cmd { t.opts.Sheet = names[i]; return t.load() }}
			})}
		}
	case setNull:
		return t.ask("Empty cells", "Text that means NULL:", t.null, nil, func(t *importTab, v string) tea.Cmd {
			t.null = v
			return t.load()
		})
	case setTable:
		return t.ask("New table", "Name:", t.ref.Name, nonEmpty, func(t *importTab, v string) tea.Cmd {
			t.ref.Name = strings.TrimSpace(v)
			return nil
		})
	}
	return nil
}

func (t *importTab) editMapping() tea.Cmd {
	i, ok := t.mapCursor()
	if !ok {
		return nil
	}
	id, header := t.id, t.sample.Header[i]
	if t.create {
		return t.ask("Column "+header, "Name:", t.rows[i].name, nonEmpty, func(t *importTab, v string) tea.Cmd {
			t.rows[i].name = strings.TrimSpace(v)
			return nil
		})
	}
	labels, idx := []string{"(skip)"}, []int{-1}
	for j, c := range t.cols {
		if c.Generated == "" {
			labels, idx = append(labels, c.Name+"  "+c.Type), append(idx, j)
		}
	}
	return openOverlay(overlay.NewPicker(header+" →", labels, func(k int) tea.Msg {
		return importEditMsg{tabID: id, apply: func(t *importTab) tea.Cmd { t.setTarget(i, idx[k]); return nil }}
	}))
}

func (t *importTab) editType() tea.Cmd {
	if !t.create {
		t.setStatus("the table's types are fixed · t changes types of a new table", true)
		return nil
	}
	i, ok := t.mapCursor()
	if !ok {
		return nil
	}
	return t.ask("Column "+t.rows[i].name, "Type:", t.rows[i].typ, nonEmpty, func(t *importTab, v string) tea.Cmd {
		t.rows[i].typ = strings.TrimSpace(v)
		return nil
	})
}

// submit comes in Task 11; until then it does nothing.
func (t *importTab) submit() tea.Cmd { return nil }
```

The `"t changes types"` text names a key: replace it with `keyHint(t.keys, keymap.Import, keymap.ImportType, "changes types of a new table")` joined by `joinHints`, per the Global Constraints. Add imports `errors`, `strconv`, `github.com/shepherd-yun/datavim/internal/ui/overlay`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/ui/ -run TestImport && make test && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui
git commit -m "feat(ui): Import tab 里修改文件设置与列映射"
```

---

### Task 11: Check, confirm, run, progress

**Files:**
- Modify: `internal/ui/importtab.go` (replace `submit` and `progressText`), `internal/ui/importrun.go`, `internal/ui/app.go`
- Test: `internal/ui/import_test.go`

**Interfaces:**
- Consumes: `importer.Check`, `importer.Run`, `importer.Job`, `importer.LineError`, `importer.LeftoverError`; `overlay.NewPreview`, `overlay.NewConfirm`, `overlay.WithDanger`; `Model.metaStale`, `metaStaleMsg`, `Model.reloadTables`, `m.tree.SetBroken`, `m.tabByID`.
- Produces: `importCheckedMsg`, `importConfirmedMsg{tabID int}`, `importDoneMsg`, `importTickMsg`; `(*importTab).plan() (importer.Plan, string)`, `submit`, `checked`, `start`, `done`, `cancelRun`, `shutdown`; `(*Model).importConfirmed`, `(*Model).importDone`; `importErrorText(err error) string`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/ui/import_test.go`:

```go
// importTab test speed: progress ticks fire at once.
func fastTicks(it *importTab) { it.tick = 0 }

func TestImportIntoATable(t *testing.T) {
	m, b := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "local", "app", "users")
	fastTicks(it)
	press(t, m, "space", "w")
	if !strings.Contains(screen(m), "Insert 2 rows into app.users") {
		t.Fatalf("no confirmation:\n%s", screen(m))
	}
	press(t, m, "y")
	want := []string{
		"START TRANSACTION",
		"SAVEPOINT datavim_import",
		"INSERT INTO `app`.`users` (`id`, `name`) VALUES (?, ?), (?, ?)",
		"RELEASE SAVEPOINT datavim_import",
		"COMMIT",
	}
	if got := b.conn.execList(); !slices.Equal(got[len(got)-len(want):], want) {
		t.Fatalf("execs %v", got)
	}
	if !strings.Contains(screen(m), "imported 2 rows into app.users") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestImportDryRunStopsOnProblems(t *testing.T) {
	m, b := newTestModel(t)
	importFixture(t, m, "orders.csv", "ID,Name\n1,a\nabc,b\n")
	it := openImportOn(t, m, "orders.csv", "local", "app", "users")
	fastTicks(it)
	press(t, m, "space", "w")
	s := screen(m)
	for _, want := range []string{"Problems (1 row)", `line 3, ID: "abc" is not an integer`, "1 row cannot be imported"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	if m.overlay != nil || len(b.conn.execList()) != 0 {
		t.Fatalf("overlay %v, execs %v", m.overlay, b.conn.execList())
	}
}

func TestImportRefusesWithoutRequiredColumns(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	openImportOn(t, m, "orders.csv", "local", "app", "users")
	press(t, m, "x", "space", "w") // skip ID
	if !strings.Contains(screen(m), "needs values for id") || m.overlay != nil {
		t.Fatalf("screen:\n%s", screen(m))
	}
}

func TestImportOnProdAsksForTheName(t *testing.T) {
	m, b := newTestModel(t)
	if err := b.SetReadOnly("prod", false); err != nil { // prod is read-only by default
		t.Fatal(err)
	}
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "prod", "app", "users")
	fastTicks(it)
	press(t, m, "space", "w")
	if !strings.Contains(screen(m), "PROD · Import into users") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	press(t, m, "y", "enter")
	if slices.Contains(b.conn.execList(), "BEGIN") {
		t.Fatal("y alone imported on prod")
	}
	press(t, m, "ctrl+u")
	typeText(t, m, "users")
	press(t, m, "enter")
	if !slices.Contains(b.conn.execList(), `INSERT INTO "users" ("id", "name") VALUES ($1, $2), ($3, $4)`) {
		t.Fatalf("execs %v", b.conn.execList())
	}
}

func TestImportFailureRollsBack(t *testing.T) {
	m, b := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "local", "app", "users")
	fastTicks(it)
	b.conn.setFail("INSERT INTO `app`.`users` (`id`, `name`) VALUES (?, ?), (?, ?)", errors.New("boom"))
	press(t, m, "space", "w", "y")
	if got := b.conn.execList(); got[len(got)-1] != "ROLLBACK" {
		t.Fatalf("execs %v", got)
	}
	if !strings.Contains(screen(m), "import failed, nothing was written: lines 2–3: boom") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestImportCreatesATable(t *testing.T) {
	m, b := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "local", "app")
	fastTicks(it)
	press(t, m, "space", "w")
	if !strings.Contains(screen(m), "CREATE TABLE `app`.`orders`;") {
		t.Fatalf("no DDL:\n%s", screen(m))
	}
	press(t, m, "y")
	got := b.conn.execList()
	if i := slices.Index(got, "CREATE TABLE `app`.`orders`"); i < 0 || got[i+1] != "START TRANSACTION" || got[len(got)-1] != "COMMIT" {
		t.Fatalf("execs %v", got)
	}
	if !strings.Contains(screen(m), "imported 2 rows into app.orders") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestImportRefusesAnExistingNewTable(t *testing.T) {
	m, b := newTestModel(t)
	importFixture(t, m, "users.csv", ordersCSV)
	it := openImportOn(t, m, "users.csv", "local", "app")
	fastTicks(it)
	press(t, m, "space", "w")
	if !strings.Contains(screen(m), "table app.users already exists") || len(b.conn.execList()) != 0 {
		t.Fatalf("execs %v\n%s", b.conn.execList(), screen(m))
	}
}

func TestImportRechecksOnConfirm(t *testing.T) {
	m, b := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "local", "app", "users")
	fastTicks(it)
	press(t, m, "space", "w")
	if err := b.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	press(t, m, "y")
	if len(b.conn.execList()) != 0 || !strings.Contains(statusLine(m), "read-only") {
		t.Fatalf("status %s, execs %v", statusLine(m), b.conn.execList())
	}
}

func TestImportCancelAndClose(t *testing.T) {
	m, _ := newTestModel(t)
	importFixture(t, m, "orders.csv", ordersCSV)
	it := openImportOn(t, m, "orders.csv", "local", "app", "users")
	ctx, cancel := context.WithCancel(context.Background())
	it.phase, it.cancel = importRunning, cancel
	press(t, m, "ctrl+c")
	if ctx.Err() == nil {
		t.Fatal("ctrl+c did not cancel the import")
	}
	drain(t, m, func() tea.Msg { return importDoneMsg{tabID: it.id, conn: "local", ref: it.ref, err: context.Canceled} })
	if !strings.Contains(screen(m), "import cancelled, nothing was written") {
		t.Fatalf("status:\n%s", screen(m))
	}

	ctx, cancel = context.WithCancel(context.Background())
	it.phase, it.cancel = importRunning, cancel
	it.shutdown()()
	if ctx.Err() == nil {
		t.Fatal("closing the tab did not cancel the import")
	}
}

func TestImportErrorText(t *testing.T) {
	tests := map[error]string{
		context.Canceled: "import cancelled, nothing was written",
		fmt.Errorf("%w: reset", db.ErrConnLost): "connection lost: the transaction is gone and nothing was committed",
		&importer.LeftoverError{Table: "app.t", Err: context.Canceled}: "import cancelled, nothing was written · table app.t was created but is empty; drop it manually",
		errors.New("x"): "import failed, nothing was written: x",
	}
	for err, want := range tests {
		if got := importErrorText(err); got != want {
			t.Errorf("importErrorText(%v) = %q, want %q", err, got, want)
		}
	}
}
```

Add imports `context`, `errors`, `fmt`, `slices`, `tea "charm.land/bubbletea/v2"`, `github.com/shepherd-yun/datavim/internal/importer`.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/ui/ -run TestImport`
Expected: FAIL (`undefined: importDoneMsg`, …).

- [ ] **Step 3: Implement**

In `internal/ui/importtab.go`, delete the placeholder `submit` and `progressText`, and add:

```go
type (
	importCheckedMsg struct {
		tabID, gen int
		job        importer.Job
		result     importer.CheckResult
		exists     bool
		err        error
	}
	// importConfirmedMsg is the user's yes; the root model checks again
	// that the import may run, then starts it.
	importConfirmedMsg struct{ tabID int }
	importDoneMsg      struct {
		tabID  int
		conn   string
		ref    db.TableRef
		create bool
		rows   int
		took   time.Duration
		err    error
	}
	importTickMsg struct{ tabID, gen int }
)

func (m importCheckedMsg) TabID() int { return m.tabID }
func (m importDoneMsg) TabID() int    { return m.tabID }
func (m importTickMsg) TabID() int    { return m.tabID }

// plan is the import as mapped, or why it cannot run.
func (t *importTab) plan() (importer.Plan, string) {
	p := importer.Plan{Driver: t.driver, Table: t.ref, Columns: t.columns(), Width: len(t.sample.Header), Null: t.null}
	switch {
	case t.loading:
		return p, "the file is still loading"
	case t.loadErr != "":
		return p, t.loadErr
	case len(p.Columns) == 0:
		return p, "pick at least one column"
	case len(t.missing()) > 0:
		return p, t.refName() + " needs values for " + strings.Join(t.missing(), ", ")
	case t.create && strings.TrimSpace(t.ref.Name) == "":
		return p, "name the new table"
	}
	if t.create {
		seen := map[string]bool{}
		for _, c := range p.Columns {
			k := strings.ToLower(c.Target.Name)
			if k == "" || seen[k] {
				return p, "two columns are called " + c.Target.Name
			}
			seen[k] = true
		}
	}
	return p, ""
}

// newSchema is the new table: the picked columns, named and typed as mapped.
func (t *importTab) newSchema() db.TableSchema {
	s := db.TableSchema{Ref: t.ref}
	for _, c := range t.columns() {
		s.Columns = append(s.Columns, db.Column{
			Name: c.Target.Name, Type: c.Target.Type, Nullable: c.Target.Nullable, Position: len(s.Columns) + 1,
		})
	}
	return s
}

func (t *importTab) tickCmd() tea.Cmd {
	id, gen := t.id, t.gen
	return tea.Tick(t.tick, func(time.Time) tea.Msg { return importTickMsg{tabID: id, gen: gen} })
}

// submit checks the whole file (and, for a new table, that the name is
// free), then asks for confirmation.
func (t *importTab) submit() tea.Cmd {
	p, problem := t.plan()
	if problem != "" {
		t.setStatus(problem, true)
		return nil
	}
	if t.cancel != nil {
		t.cancel()
	}
	t.gen++
	t.phase, t.problems, t.failed, t.job = importChecking, nil, 0, nil
	t.progress = new(atomic.Int64)
	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	t.cancel = cancel
	id, gen, b, conn, ref, path, o, progress := t.id, t.gen, t.backend, t.conn, t.ref, t.path, t.opts, t.progress
	create, schema := t.create, t.newSchema()
	check := func() tea.Msg {
		defer cancel()
		msg := importCheckedMsg{tabID: id, gen: gen, job: importer.Job{Path: path, Options: o, Plan: p}}
		if create {
			msg.err = withConn(ctx, b, conn, ref.Database, func(c db.Conn) error {
				tables, err := c.Meta().Tables(ctx, ref.Namespace)
				if err != nil {
					return err
				}
				msg.exists = slices.ContainsFunc(tables, func(x db.Table) bool { return strings.EqualFold(x.Name, ref.Name) })
				w := c.Dialect().DDL()
				msg.job.Create, _ = w.CreateTable(schema)
				msg.job.Drop = w.DropTable(ref, false)
				return nil
			})
			if msg.err != nil || msg.exists {
				return msg
			}
		}
		msg.result, msg.err = importer.Check(ctx, path, o, p, progress)
		return msg
	}
	return tea.Batch(check, t.tickCmd())
}

func (t *importTab) checked(m importCheckedMsg) tea.Cmd {
	if m.gen != t.gen {
		return nil
	}
	t.phase, t.cancel = importIdle, nil
	switch {
	case errors.Is(m.err, context.Canceled):
		t.setStatus("check cancelled", false)
	case m.err != nil:
		t.setStatus("check failed: "+errorText(m.err), true)
	case m.exists:
		t.setStatus(joinHints("table "+t.refName()+" already exists", "rename it in the Source section"), true)
	case m.result.Failed > 0:
		t.problems, t.failed = m.result.Errors, m.result.Failed
		t.section, t.cursor[secPreview], t.offset[secPreview] = secPreview, 0, 0
		t.setStatus(plural(m.result.Failed, "row")+" cannot be imported · nothing was written", true)
	case m.result.Rows == 0:
		t.setStatus("the file has no rows to import", true)
	default:
		t.total = m.result.Rows
		job := m.job
		t.job = &job
		return openOverlay(t.confirmation())
	}
	return nil
}

func (t *importTab) confirmation() overlay.Overlay {
	cfg, _ := t.backend.Connection(t.conn)
	title := "Import into " + t.refName()
	if t.create {
		title = "Create " + t.refName()
	}
	var lines []string
	for _, q := range t.job.Create {
		lines = append(lines, q+";", "")
	}
	insert := len(lines)
	lines = append(lines, fmt.Sprintf("Insert %s into %s in one transaction.", plural(t.total, "row"), t.refName()))
	ok := importConfirmedMsg{tabID: t.id}
	if cfg.Env != config.EnvProd {
		return overlay.NewPreview(title, lines, ok)
	}
	lines = append(lines, "", "Type "+t.ref.Name+" to confirm.")
	return overlay.NewConfirm("PROD · "+title, lines, t.ref.Name, ok, overlay.WithDanger(insert))
}

// start runs the confirmed import on its own session.
func (t *importTab) start() tea.Cmd {
	if t.job == nil {
		return nil
	}
	job := *t.job
	t.job = nil
	t.gen++
	t.phase, t.progress = importRunning, new(atomic.Int64)
	ctx, cancel := context.WithTimeout(context.Background(), importTimeout)
	t.cancel = cancel
	id, b, conn, ref, create, progress := t.id, t.backend, t.conn, t.ref, t.create, t.progress
	run := func() tea.Msg {
		defer cancel()
		begin := time.Now()
		msg := importDoneMsg{tabID: id, conn: conn, ref: ref, create: create}
		c, err := b.Conn(ctx, conn, ref.Database)
		if err == nil {
			var s db.Session
			if s, err = c.Session(ctx); err == nil {
				msg.rows, err = importer.Run(ctx, s, job, progress)
				_ = s.Close()
			}
		}
		if db.IsConnLost(err) {
			b.Reset(conn)
		}
		msg.err, msg.took = err, time.Since(begin)
		slog.Info("import", "conn", conn, "table", ref.Name, "rows", msg.rows, "took", msg.took, "failed", err != nil)
		return msg
	}
	return tea.Batch(run, t.tickCmd())
}

func (t *importTab) done(m importDoneMsg) {
	t.phase, t.cancel = importIdle, nil
	if m.err != nil {
		t.setStatus(importErrorText(m.err), true)
		return
	}
	t.setStatus(fmt.Sprintf("imported %s into %s in %s", plural(m.rows, "row"), t.refName(), m.took.Round(time.Millisecond)), false)
}

// importErrorText says what a failed import did: never anything.
func importErrorText(err error) string {
	suffix := ""
	var left *importer.LeftoverError
	if errors.As(err, &left) {
		suffix, err = " · table "+left.Table+" was created but is empty; drop it manually", left.Err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "import cancelled, nothing was written" + suffix
	case errors.Is(err, context.DeadlineExceeded):
		return "import timed out, nothing was written" + suffix
	case db.IsConnLost(err):
		return "connection lost: the transaction is gone and nothing was committed" + suffix
	}
	return "import failed, nothing was written: " + err.Error() + suffix
}

func (t *importTab) progressText() string {
	if t.progress == nil {
		return ""
	}
	n := int(t.progress.Load())
	if t.phase == importChecking {
		return fmt.Sprintf("checking… %s read", plural(n, "row"))
	}
	return fmt.Sprintf("inserting %d / %s", n, plural(t.total, "row"))
}

func (t *importTab) cancelRun() tea.Cmd {
	if t.phase == importIdle || t.cancel == nil {
		t.setStatus("nothing is running", false)
		return nil
	}
	t.cancel()
	return nil
}

// shutdown cancels a running check or import when the tab closes; the
// import then rolls back.
func (t *importTab) shutdown() func() {
	c := t.cancel
	return func() {
		if c != nil {
			c()
		}
	}
}
```

Extend `Update`:

```go
	case importCheckedMsg:
		return t.checked(m)
	case importDoneMsg:
		t.done(m)
	case importTickMsg:
		if m.gen == t.gen && t.phase != importIdle {
			return t.tickCmd()
		}
```

Add imports `errors`, `log/slog`, `github.com/shepherd-yun/datavim/internal/ui/overlay` (if not there yet).

In `internal/ui/importrun.go` add:

```go
// importConfirmed starts the confirmed import unless something changed
// meanwhile (read-only, a console transaction, staged changes).
func (m *Model) importConfirmed(msg importConfirmedMsg) tea.Cmd {
	t, ok := m.tabByID(msg.tabID).(*importTab)
	if !ok {
		return nil
	}
	if why := m.importBlocked(t.conn, t.ref, t.create); why != "" {
		t.job = nil
		t.setStatus(why, true)
		m.setStatus(why, true)
		return nil
	}
	return t.start()
}

// importDone updates the tab, then the tree and the catalog: a new table
// shows up, and a failed one is looked up again in case it was left over.
func (m *Model) importDone(msg importDoneMsg) tea.Cmd {
	m.tree.SetBroken(msg.conn, db.IsConnLost(msg.err))
	var cmd tea.Cmd
	if t := m.tabByID(msg.tabID); t != nil {
		cmd = t.Update(msg)
	}
	if msg.err != nil && !msg.create {
		return cmd
	}
	cmds := []tea.Cmd{cmd, m.metaStale(metaStaleMsg{conn: msg.conn, database: msg.ref.Database})}
	if msg.create {
		cmds = append(cmds, m.reloadTables(msg.conn, msg.ref.Namespace, ""))
	}
	return tea.Batch(cmds...)
}
```

In `internal/ui/app.go` `Update`, next to `case importFileMsg:` (before `case tabMsg:`):

```go
	case importConfirmedMsg:
		return m, m.importConfirmed(msg)
	case importDoneMsg:
		return m, m.importDone(msg)
```

`importDoneMsg` must be matched before `tabMsg` (it has `TabID()`); the switch takes the first matching case.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/ui/ -run TestImport && make test && make lint`
Expected: PASS. If `TestImportIntoATable` shows the INSERT columns as `(`id`, `name`)` but `execList` has other statements after COMMIT (e.g. a catalog refresh), compare the slice ending at COMMIT instead of the tail.

- [ ] **Step 5: Commit**

```bash
git add internal/ui
git commit -m "feat(ui): 导入前校验、确认，单事务写入并显示进度"
```

---

### Task 12: Docs

**Files:**
- Modify: `CHANGELOG.md`, `README.md`, `README.zh-CN.md`, `docs/superpowers/specs/2026-10-16-import-data-design.md`

- [ ] **Step 1: CHANGELOG**

Under `## [Unreleased]` → `### Added`, first entry:

```markdown
- Import data from CSV, TSV or XLSX files, like DataGrip's Import Data from File: `I` on a
  table (or "Import data from file…" in its `a` menu) opens an Import tab with the file's
  settings (delimiter, UTF-8 or GBK, header row, sheet, NULL text), the column mapping (`x`
  imports or skips a column, `i` picks the target column) and a preview of the converted
  rows. `<Space>w` checks the whole file first and lists the rows that cannot be imported,
  then inserts everything in one transaction: any failure, `Ctrl-c` or a lost connection
  writes nothing, and a rejected row is named by its line. On a database or schema `I`
  creates a new table with column types inferred from the file (`t` changes a type).
  Read-only connections refuse; prod asks for the table name.
```

- [ ] **Step 2: README (both languages)**

`README.md`, features list (line ~14), after the export bullet:

```markdown
- Import CSV, TSV and xlsx files into a table, or into a new table inferred from the file.
```

Keys table, after the `<Space>x c/j/s/x` row:

```markdown
| `I` | Tree: import a CSV / TSV / xlsx file into the table; on a database or schema, into a new table |
| `x` / `a` `A` / `i` / `t` | Import tab: import or skip a column / all / none / pick the target column (new table: rename) / new table: column type; `[` `]` switch sections, `<Space>w` checks and imports |
```

`README.zh-CN.md`, the same places:

```markdown
- 把 CSV、TSV、xlsx 文件导入到表，或从文件推断结构新建表。
```

```markdown
| `I` | 树：把 CSV / TSV / xlsx 文件导入到这张表；在库或 schema 上则新建表 |
| `x` / `a` `A` / `i` / `t` | Import tab：导入或跳过一列 / 全选 / 全不选 / 选目标列（新表：改列名）/ 新表：改类型；`[` `]` 切换区域，`<Space>w` 校验并导入 |
```

Also add one line to the Safety section of both READMEs:

```markdown
- Imports run in one transaction and write nothing unless every row goes in; on MySQL a table the import created is dropped again.
```

```markdown
- 导入在一个事务里完成，只要有一行失败就什么都不写；MySQL 上导入新建的表会被删掉。
```

- [ ] **Step 3: Spec status**

In `docs/superpowers/specs/2026-10-16-import-data-design.md` change `- 状态：设计中` to `- 状态：已实现`, and add the plan's "Deviations from the spec" bullets as a short `## 10. 实现差异` section in Chinese.

- [ ] **Step 4: Verify**

Run: `make test && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add CHANGELOG.md README.md README.zh-CN.md docs/superpowers/specs/2026-10-16-import-data-design.md
git commit -m "docs: 从文件导入数据"
```
