package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/importer"
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
	// id is a plain integer key: the import offers to number it
	if !strings.Contains(screen(m), "[✓] (generate)") || strings.Contains(screen(m), "needs values for") {
		t.Fatalf("generated id not offered:\n%s", screen(m))
	}
	press(t, m, "j", "j", "j", "x") // skip the generated id too
	if !strings.Contains(screen(m), "app.users needs values for id") {
		t.Fatalf("missing id not shown:\n%s", screen(m))
	}
	press(t, m, "g", "g", "x")
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
	press(t, m, "x", "j", "j", "j", "x", "space", "w") // skip ID and the generated id
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
		context.Canceled:                                               "import cancelled, nothing was written",
		fmt.Errorf("%w: reset", db.ErrConnLost):                        "connection lost: the transaction is gone and nothing was committed",
		&importer.LeftoverError{Table: "app.t", Err: context.Canceled}: "import cancelled, nothing was written · table app.t was created but is empty; drop it manually",
		errors.New("x"):                                                "import failed, nothing was written: x",
	}
	for err, want := range tests {
		if got := importErrorText(err); got != want {
			t.Errorf("importErrorText(%v) = %q, want %q", err, got, want)
		}
	}
}

// keyedUsers gives app.users a plain int primary key: no default, not
// auto-increment, so a file without ids needs them generated.
func keyedUsers(b *fakeBackend) {
	b.conn.setColumns([]db.Column{
		{Name: "id", Type: "int", PrimaryKey: true},
		{Name: "name", Type: "text", Nullable: true},
	})
	b.conn.setData("users", []db.ColumnInfo{{Name: "max"}}, []db.Value{{Text: "41"}})
}

func TestImportGeneratesTheKey(t *testing.T) {
	m, b := newTestModel(t)
	keyedUsers(b)
	importFixture(t, m, "names.csv", "Name\na\nb\n")
	it := openImportOn(t, m, "names.csv", "local", "app", "users")
	fastTicks(it)
	s := screen(m)
	for _, want := range []string{"[✓] (generate)", "→ id  int  MAX(id)+1…", "auto"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "needs values for") {
		t.Fatalf("id reported missing:\n%s", s)
	}
	press(t, m, "space", "w")
	if !strings.Contains(screen(m), "id = MAX(id)+1, +2… in file order") {
		t.Fatalf("confirmation:\n%s", screen(m))
	}
	press(t, m, "y")
	if !slices.Contains(b.conn.execList(), "INSERT INTO `app`.`users` (`name`, `id`) VALUES (?, ?), (?, ?)") {
		t.Fatalf("execs %v", b.conn.execList())
	}
	if !strings.Contains(screen(m), "imported 2 rows into app.users") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestImportGeneratedKeyCanBeSkipped(t *testing.T) {
	m, b := newTestModel(t)
	keyedUsers(b)
	importFixture(t, m, "names.csv", "Name\na\n")
	it := openImportOn(t, m, "names.csv", "local", "app", "users")
	press(t, m, "j", "x") // the (generate) row
	if it.autoID || !strings.Contains(screen(m), "needs values for id") {
		t.Fatalf("x did not skip it:\n%s", screen(m))
	}
	press(t, m, "a")
	if !it.autoID {
		t.Fatal("a did not pick it again")
	}
	// mapping a file column to id drops the generated row
	importFixture(t, m, "ids.csv", "ID,Name\n7,a\n")
	it = openImportOn(t, m, "ids.csv", "local", "app", "users")
	if strings.Contains(screen(m), "(generate)") || len(it.columns()) != 2 {
		t.Fatalf("generate offered with id in the file:\n%s", screen(m))
	}
}

func TestImportGeneratesOnlyIntegerKeys(t *testing.T) {
	m, b := newTestModel(t)
	b.conn.setColumns([]db.Column{
		{Name: "code", Type: "varchar(10)", PrimaryKey: true},
		{Name: "name", Type: "text", Nullable: true},
	})
	importFixture(t, m, "names.csv", "Name\na\n")
	openImportOn(t, m, "names.csv", "local", "app", "users")
	if s := screen(m); strings.Contains(s, "(generate)") || !strings.Contains(s, "needs values for code") {
		t.Fatalf("screen:\n%s", s)
	}
}
