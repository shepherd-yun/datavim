package ui

import (
	"strings"
	"testing"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/schemaedit"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/structure"
)

func usersSchema() db.TableSchema {
	return db.TableSchema{
		Ref: db.TableRef{Namespace: db.Namespace{Database: "app"}, Name: "users"},
		Columns: []db.Column{
			{Name: "id", Type: "bigint", PrimaryKey: true, Position: 1},
			{Name: "name", Type: "text", Nullable: true, Position: 2},
		},
		Indexes: []db.Index{{Name: "PRIMARY", Columns: []string{"id"}, Unique: true, Primary: true}},
	}
}

// modifyUsers selects conn's users table in the tree and presses e.
func modifyUsers(t *testing.T, m *Model, b *fakeBackend, conn string) *structureTab {
	t.Helper()
	b.conn.setSchemas(usersSchema())
	selectNode(t, m, conn, "app", "users")
	press(t, m, "e")
	st, ok := m.activeTab().(*structureTab)
	if !ok || st.ed == nil {
		t.Fatalf("not modifying: %T\n%s", m.activeTab(), screen(m))
	}
	return st
}

func TestTreeEModifiesTheTable(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	if !strings.Contains(screen(m), "modifying users") {
		t.Fatalf("status:\n%s", screen(m))
	}
	if st.view.Section() != structure.Columns {
		t.Fatal("editing starts in the columns")
	}
	press(t, m, "e")
	if st.ed != nil {
		t.Fatal("e with no changes stops modifying")
	}
}

func TestTreeEOnAConnectionStillEditsIt(t *testing.T) {
	m, _ := newTestModel(t)
	m.setFocus(focusTree)
	press(t, m, "g", "g", "e") // local, the first node
	if _, ok := m.overlay.(*overlay.ConnForm); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
}

func TestModifyRefusedOnReadOnlyAndViews(t *testing.T) {
	m, b := newTestModel(t)
	if err := b.SetReadOnly("local", true); err != nil {
		t.Fatal(err)
	}
	selectNode(t, m, "local", "app", "users")
	press(t, m, "e")
	if !strings.Contains(screen(m), "read-only connection") {
		t.Fatalf("readonly:\n%s", screen(m))
	}
	if err := b.SetReadOnly("local", false); err != nil {
		t.Fatal(err)
	}
	selectNode(t, m, "local", "app", "active")
	press(t, m, "e")
	if !strings.Contains(screen(m), "views cannot be modified") {
		t.Fatalf("view:\n%s", screen(m))
	}
}

func TestEditKeysNeedEditMode(t *testing.T) {
	m, _ := newTestModel(t)
	drain(t, m, m.openStructure("local", usersRef))
	press(t, m, "i")
	if !strings.Contains(screen(m), "e modifies the table") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestStructureEditStagesAndUndoes(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "j", "i") // name's Name field
	if !strings.Contains(screen(m), "Column name:") {
		t.Fatalf("prompt:\n%s", screen(m))
	}
	typeText(t, m, "_x")
	press(t, m, "enter")
	if got := st.ed.Columns()[1].Col.Name; got != "name_x" {
		t.Fatalf("name = %q", got)
	}
	if !strings.Contains(screen(m), "● 1 change") {
		t.Fatalf("status:\n%s", screen(m))
	}
	press(t, m, "l", "l", "i") // Null toggles in place
	if st.ed.Columns()[1].Col.Nullable {
		t.Fatal("i on Null toggles it")
	}
	press(t, m, "u", "u")
	if st.ed.Changes() != 0 {
		t.Fatalf("changes after undo: %d", st.ed.Changes())
	}
	press(t, m, "u")
	if !strings.Contains(screen(m), "no changes to undo") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestNewColumnAsksNameThenType(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "j", "o")
	typeText(t, m, "email")
	press(t, m, "enter")
	if !strings.Contains(screen(m), "Type, e.g.") {
		t.Fatalf("type prompt:\n%s", screen(m))
	}
	typeText(t, m, "varchar(80)")
	press(t, m, "enter")
	r := st.ed.Columns()[2]
	if r.State() != schemaedit.Added || r.Col.Name != "email" || r.Col.Type != "varchar(80)" {
		t.Fatalf("row = %+v", r)
	}
	if row, _ := st.view.Cursor(); row != 2 {
		t.Fatalf("cursor row = %d", row)
	}
}

func TestNewIndexAsksColumnsAndNamesItself(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "]", "o")
	typeText(t, m, "name")
	press(t, m, "enter")
	r := st.ed.Indexes()[1]
	if r.Index.Name != "users_name_idx" || r.Text(schemaedit.IdxColumns) != "name" {
		t.Fatalf("index = %+v", r.Index)
	}
	press(t, m, "h", "i") // Kind
	if !st.ed.Indexes()[1].Index.Unique {
		t.Fatal("i on Kind cycles it")
	}
}

func TestForeignKeysAndDDLAreReadOnly(t *testing.T) {
	m, b := newTestModel(t)
	modifyUsers(t, m, b, "local")
	press(t, m, "]", "]", "o")
	if !strings.Contains(screen(m), "only columns and indexes") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestStagedStructureEditsGuardCloseQuitAndDrop(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "j", "d", "d")
	if st.changes() != 1 {
		t.Fatalf("changes = %d", st.changes())
	}
	press(t, m, "space", "r")
	if st.ed == nil || st.changes() != 1 || !strings.Contains(screen(m), "1 unsaved change") {
		t.Fatalf("refresh keeps the edits:\n%s", screen(m))
	}
	command(t, m, "q")
	if !strings.Contains(screen(m), "Unsaved changes") {
		t.Fatalf("close asks first:\n%s", screen(m))
	}
	press(t, m, "n")
	selectNode(t, m, "local", "app", "users")
	press(t, m, "d", "d")
	if !strings.Contains(screen(m), "users:struct has 1 unsaved change") {
		t.Fatalf("drop is blocked:\n%s", screen(m))
	}
	m.setFocus(focusTree)
	press(t, m, "g", "g", "e") // local, the first node
	if m.overlay != nil {
		t.Fatal("the connection cannot be edited with staged structure edits")
	}
}
