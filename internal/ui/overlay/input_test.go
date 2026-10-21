package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/uitest"
)

func keysInto(f *field, keys ...string) {
	for _, k := range keys {
		f.handle(uitest.Key(k))
	}
}

// show is the field's text with the cursor drawn as |.
func show(f *field) string {
	r := []rune(f.String())
	return string(r[:f.Pos()]) + "|" + string(r[f.Pos():])
}

func TestFieldCursorMoves(t *testing.T) {
	f := newField("select abc_1 from t")
	for _, c := range []struct {
		key, want string
	}{
		{"home", "|select abc_1 from t"},
		{"right", "s|elect abc_1 from t"},
		{"ctrl+f", "se|lect abc_1 from t"},
		{"left", "s|elect abc_1 from t"},
		{"ctrl+b", "|select abc_1 from t"},
		{"left", "|select abc_1 from t"}, // stops at the start
		{"alt+right", "select |abc_1 from t"},
		{"alt+f", "select abc_1 |from t"},
		{"alt+left", "select |abc_1 from t"},
		{"alt+b", "|select abc_1 from t"},
		{"end", "select abc_1 from t|"},
		{"right", "select abc_1 from t|"}, // stops at the end
		{"ctrl+a", "|select abc_1 from t"},
		{"ctrl+e", "select abc_1 from t|"},
	} {
		keysInto(&f, c.key)
		if got := show(&f); got != c.want {
			t.Fatalf("after %s: %q, want %q", c.key, got, c.want)
		}
	}
}

func TestFieldEditsAtCursor(t *testing.T) {
	f := newField("ac")
	keysInto(&f, "left", "b")
	if got := show(&f); got != "ab|c" {
		t.Fatalf("insert: %q", got)
	}
	keysInto(&f, "backspace")
	if got := show(&f); got != "a|c" {
		t.Fatalf("backspace: %q", got)
	}
	keysInto(&f, "delete")
	if got := show(&f); got != "a|" {
		t.Fatalf("delete: %q", got)
	}
	keysInto(&f, "delete", "home", "backspace")
	if got := show(&f); got != "|a" {
		t.Fatalf("no-ops at the ends: %q", got)
	}

	f = newField("name = 'x' and")
	keysInto(&f, "ctrl+w")
	if got := show(&f); got != "name = 'x' |" {
		t.Fatalf("ctrl+w: %q", got)
	}
	keysInto(&f, "ctrl+w")
	if got := show(&f); got != "name = '|" {
		t.Fatalf("ctrl+w skips the punctuation before the word: %q", got)
	}

	f = newField("abcdef")
	keysInto(&f, "left", "left", "ctrl+k")
	if got := show(&f); got != "abcd|" {
		t.Fatalf("ctrl+k: %q", got)
	}
	keysInto(&f, "left", "ctrl+u")
	if got := show(&f); got != "|d" {
		t.Fatalf("ctrl+u: %q", got)
	}
}

func TestFieldPasteAndReplace(t *testing.T) {
	f := newField("ad")
	keysInto(&f, "left")
	f.paste("b\nc")
	if got := show(&f); got != "abc|d" {
		t.Fatalf("paste inserts at the cursor without control runes: %q", got)
	}
	f = newField("where na")
	f.replace(6, "name")
	if got := show(&f); got != "where name|" {
		t.Fatalf("replace: %q", got)
	}
	f = newField("中文")
	keysInto(&f, "left", "x")
	if got := show(&f); got != "中x|文" {
		t.Fatalf("runes, not bytes: %q", got)
	}
}

func TestFieldIgnoresAltText(t *testing.T) {
	f := newField("a")
	f.handle(tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt, Text: "b"})
	f.handle(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl, Text: "x"})
	if f.String() != "a" {
		t.Fatalf("alt / ctrl keys are not typed: %q", f.String())
	}
}

func TestFieldView(t *testing.T) {
	f := newField("abcdefghij")
	if got := ansi.Strip(f.View(20)); got != "abcdefghij " {
		t.Fatalf("fits: %q", got)
	}
	if got := ansi.Strip(f.View(5)); got != "…hij " {
		t.Fatalf("end: %q", got)
	}
	keysInto(&f, "home")
	if got := ansi.Strip(f.View(5)); got != "abcd…" {
		t.Fatalf("start: %q", got)
	}
	for i := 0; i < 5; i++ {
		keysInto(&f, "right")
	}
	got := ansi.Strip(f.View(6))
	if !strings.Contains(got, "f") || ansi.StringWidth(got) > 6 {
		t.Fatalf("middle keeps the cursor's rune in view: %q", got)
	}
}

func TestFieldViewWideRunes(t *testing.T) {
	f := newField("中文输入法测试")
	for _, w := range []int{4, 6, 9} { // 4 is the least that fits "…试" and the cursor
		got := ansi.Strip(f.View(w))
		if ansi.StringWidth(got) > w || !strings.Contains(got, "试") {
			t.Fatalf("width %d: %q", w, got)
		}
	}
}

func TestFieldViewMasked(t *testing.T) {
	f := newField("hunter2")
	for i := 0; i <= f.Len(); i++ {
		v := ansi.Strip(f.ViewMasked(40, '•'))
		if strings.ContainsAny(v, "hunter2") {
			t.Fatalf("cursor at %d leaks the secret: %q", f.Pos(), v)
		}
		if strings.Count(v, "•") != 7 {
			t.Fatalf("one bullet per rune: %q", v)
		}
		keysInto(&f, "left")
	}
}

func TestFilterPopupsDrawTheCursorWhereItIs(t *testing.T) {
	nop := func(int) tea.Msg { return nil }
	k := keymap.Default()
	for name, o := range map[string]Overlay{
		"picker":  NewPicker("p", []string{"abc"}, nop),
		"search":  NewSearch(k, "", "c"),
		"columns": NewColumns(k, "t", []db.Column{{Name: "abc"}}, "", func(string, bool) tea.Msg { return nil }),
	} {
		o, _ = send(t, o, "a", "b", "c", "ctrl+a")
		var f field
		keysInto(&f, "a", "b", "c", "ctrl+a")
		v := o.View(100, 40)
		if strings.Contains(v, "█") || !strings.Contains(v, "> "+f.View(10)) {
			t.Errorf("%s: the cursor is not on the a:\n%s", name, v)
		}
	}
}
