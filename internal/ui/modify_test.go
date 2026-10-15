package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/sqlx"
)

const renameName = "ALTER TABLE `app`.`users` RenameColumn name_x"

// renameNameColumn modifies conn's users and renames name to name_x.
func renameNameColumn(t *testing.T, m *Model, b *fakeBackend, conn string) *structureTab {
	t.Helper()
	st := modifyUsers(t, m, b, conn)
	press(t, m, "j", "i")
	typeText(t, m, "_x")
	press(t, m, "enter")
	return st
}

func count(list []string, s string) int {
	n := 0
	for _, x := range list {
		if x == s {
			n++
		}
	}
	return n
}

func TestModifySubmitPreviewsAndRuns(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	press(t, m, "space", "w")
	s := screen(m)
	for _, want := range []string{"Modify table users · 1 change", renameName + ";"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q:\n%s", want, s)
		}
	}
	press(t, m, "y")
	execs := b.conn.execList()
	if count(execs, renameName) != 1 || slices.Contains(execs, sqlx.BeginSQL(sqlx.MySQL)) {
		t.Fatalf("execs = %q (MySQL runs DDL outside a transaction)", execs)
	}
	if st.ed != nil {
		t.Fatal("a successful submit stops modifying")
	}
	if !strings.Contains(screen(m), "modified users") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestColonWSubmitsStructureEdits(t *testing.T) {
	m, b := newTestModel(t)
	renameNameColumn(t, m, b, "local")
	command(t, m, "w")
	if !strings.Contains(screen(m), renameName+";") {
		t.Fatalf(":w previews:\n%s", screen(m))
	}
}

func TestModifyOnProdNeedsTheNameAndOneTransaction(t *testing.T) {
	m, b := newTestModel(t)
	if err := b.SetReadOnly("prod", false); err != nil { // prod is read-only by default
		t.Fatal(err)
	}
	renameNameColumn(t, m, b, "prod")
	press(t, m, "space", "w")
	if !strings.Contains(screen(m), "PROD · Modify table users") || !strings.Contains(screen(m), "Type users to confirm") {
		t.Fatalf("prod asks for the name:\n%s", screen(m))
	}
	press(t, m, "y", "enter")
	if slices.Contains(b.conn.execList(), renameName) {
		t.Fatal("y alone ran it")
	}
	press(t, m, "ctrl+u") // the y went into the name field
	typeText(t, m, "users")
	press(t, m, "enter")
	execs := b.conn.execList()
	at := slices.Index(execs, renameName)
	if at < 1 || execs[at-1] != "BEGIN" || at+1 >= len(execs) || execs[at+1] != "COMMIT" {
		t.Fatalf("execs = %q (PostgreSQL runs DDL in a transaction)", execs)
	}
}

func TestModifyPreviewNamesRisks(t *testing.T) {
	m, b := newTestModel(t)
	modifyUsers(t, m, b, "local")
	press(t, m, "j", "d", "d", "space", "w")
	if !strings.Contains(screen(m), "⚠ destructive  - column name") {
		t.Fatalf("preview:\n%s", screen(m))
	}
}

func TestModifyRefusesManualChanges(t *testing.T) {
	m, b := newTestModel(t)
	st := modifyUsers(t, m, b, "local")
	press(t, m, "j", "l", "i")
	press(t, m, "ctrl+u")
	typeText(t, m, "manual_type")
	press(t, m, "enter", "space", "w")
	if m.overlay != nil {
		t.Fatalf("nothing to confirm:\n%s", screen(m))
	}
	if !strings.Contains(screen(m), "sqlvim cannot write") || st.changes() != 1 {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestModifyFailureKeepsEdits(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	b.conn.setFail(renameName, errors.New("boom"))
	press(t, m, "space", "w", "y")
	if st.ed == nil || st.changes() != 1 {
		t.Fatal("the edits are kept")
	}
	if s := screen(m); !strings.Contains(s, "changes kept") || !strings.Contains(s, "boom") {
		t.Fatalf("status:\n%s", s)
	}
	if count(b.conn.execList(), renameName) != 1 {
		t.Fatal("no retry")
	}
}

func TestModifyConnLostIsNotRetried(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	b.conn.setFail(renameName, fmt.Errorf("read: %w", db.ErrConnLost))
	press(t, m, "space", "w", "y")
	if count(b.conn.execList(), renameName) != 1 || st.changes() != 1 {
		t.Fatalf("execs = %q", b.conn.execList())
	}
	if !slices.Contains(b.resetList(), "local") {
		t.Fatal("the pool is reset")
	}
}

func TestModifyConfirmRunsOnce(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	b.conn.setFail(renameName, errors.New("boom")) // keep the edits so a second run could happen
	press(t, m, "space", "w")
	drain(t, m, st.Update(modifyConfirmedMsg{tabID: st.id, gen: st.gen}))
	drain(t, m, st.Update(modifyConfirmedMsg{tabID: st.id, gen: st.gen}))
	if n := count(b.conn.execList(), renameName); n != 1 {
		t.Fatalf("ran %d times", n)
	}
}

func TestModifyCancelKeepsEdits(t *testing.T) {
	m, b := newTestModel(t)
	st := renameNameColumn(t, m, b, "local")
	cmd := st.apply([]string{renameName})
	st.cancelRun()
	_, next := m.Update(cmd())
	drain(t, m, next)
	if st.changes() != 1 || st.submitting || !strings.Contains(screen(m), "cancelled · changes kept") {
		t.Fatalf("status:\n%s", screen(m))
	}
}

func TestModifyBlockedByUnsavedDataEdits(t *testing.T) {
	m, b := newTestModel(t)
	openUsers(t, m)
	press(t, m, "o") // stage a new row in the data tab
	st := renameNameColumn(t, m, b, "local")
	press(t, m, "space", "w")
	if m.overlay != nil || !strings.Contains(screen(m), "users has 1 unsaved change") {
		t.Fatalf("blocked:\n%s", screen(m))
	}
	if slices.Contains(b.conn.execList(), renameName) || st.changes() != 1 {
		t.Fatal("nothing ran")
	}
}
