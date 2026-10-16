package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
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
