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
