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

const consoleUpdateUser1 = "UPDATE `app`.`users` SET `name` = 'x' WHERE `id` = '1'"

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
	want := []string{"START TRANSACTION", consoleUpdateUser1, "COMMIT"}
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
	want := []string{"SAVEPOINT datavim_edit", consoleUpdateUser1, "RELEASE SAVEPOINT datavim_edit"}
	if got := execs[len(execs)-3:]; !slices.Equal(got, want) {
		t.Fatalf("execs = %v", execs)
	}
	if slices.Contains(execs, "COMMIT") || c.TxStatus() != "TX 1" {
		t.Fatalf("the change stays in the transaction: %v, %q", execs, c.TxStatus())
	}
}

func TestConsoleEditFailureInTransactionKeepsChanges(t *testing.T) {
	b := newFakeBackend()
	b.conn.setAffected(consoleUpdateUser1, 0)
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
