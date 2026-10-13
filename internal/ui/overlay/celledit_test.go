package overlay

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

type edited struct{ v db.Value }

func openCell(v db.Value) *CellEditor {
	return NewCellEditor("name · text", v, func(v db.Value) tea.Msg { return edited{v} })
}

func TestCellEditorAppendsAndSaves(t *testing.T) {
	o, msg := send(t, openCell(db.Value{Text: "ab"}), "c", "enter")
	if o != nil || msg != (edited{db.Value{Text: "abc"}}) {
		t.Fatalf("overlay %v msg %#v", o, msg)
	}
}

func TestCellEditorWithoutEditsSavesNothing(t *testing.T) {
	o, msg := send(t, openCell(db.Value{Null: true}), "enter")
	if o != nil || msg != nil {
		t.Fatalf("enter without an edit closes and stages nothing: %v %#v", o, msg)
	}
}

func TestCellEditorSetsNull(t *testing.T) {
	_, msg := send(t, openCell(db.Value{Text: "ab"}), "x", "ctrl+n")
	if msg != (edited{db.Value{Null: true}}) {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestCellEditorEscGoesToNormalThenCancels(t *testing.T) {
	o, _ := send(t, openCell(db.Value{Text: "ab"}), "x", "esc")
	if c, ok := o.(*CellEditor); !ok || c.Mode() != "NORMAL" {
		t.Fatalf("first esc leaves INSERT mode: %v", o)
	}
	o, _ = send(t, o, "d", "esc")
	if o == nil {
		t.Fatal("esc after d cancels the d, not the editor")
	}
	o, msg := send(t, o, "esc")
	if o != nil || msg != nil {
		t.Fatalf("esc in NORMAL mode cancels: %v %#v", o, msg)
	}
}

func TestCellEditorNewLinesWithVimKeys(t *testing.T) {
	_, msg := send(t, openCell(db.Value{Text: "one"}), "esc", "o", "t", "w", "o", "enter")
	if msg != (edited{db.Value{Text: "one\ntwo"}}) {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestCellEditorPasteAndView(t *testing.T) {
	c := openCell(db.Value{})
	c.Paste("pasted")
	v := ansi.Strip(c.View(80, 24))
	for _, want := range []string{"pasted", "name · text · INSERT", "ctrl+n NULL", "esc normal mode"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
}

type submitted struct{}

func TestPreview(t *testing.T) {
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("UPDATE t SET n = %d WHERE id = %d;", i, i))
	}
	p := NewPreview("Submit 30 changes to t", lines, submitted{})
	v := ansi.Strip(p.View(100, 12))
	if !strings.Contains(v, "n = 0 ") || strings.Contains(v, "n = 29 ") || !strings.Contains(v, "y submit · n cancel") {
		t.Fatalf("first screen:\n%s", v)
	}
	var o Overlay = p
	for range 40 {
		o, _ = send(t, o, "j")
	}
	if v := ansi.Strip(o.View(100, 12)); !strings.Contains(v, "n = 29 ") {
		t.Fatalf("scrolled to the end:\n%s", v)
	}
	if o, msg := send(t, p, "y"); o != nil || msg != (submitted{}) {
		t.Fatalf("y submits: %v %#v", o, msg)
	}
	if o, msg := send(t, p, "esc"); o != nil || msg != nil {
		t.Fatalf("esc cancels: %v %#v", o, msg)
	}
}
