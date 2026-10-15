package overlay

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

func send(t *testing.T, o Overlay, keys ...string) (Overlay, tea.Msg) {
	t.Helper()
	var msg tea.Msg
	for _, k := range keys {
		var cmd tea.Cmd
		o, cmd = o.HandleKey(uitest.Key(k))
		if cmd != nil {
			msg = cmd()
		}
		if o == nil {
			break
		}
	}
	return o, msg
}

func TestPlaceAt(t *testing.T) {
	base := "aaaaaa\nbbbbbb\ncccccc"
	got := ansi.Strip(PlaceAt(base, "XY\nZW", 2, 1))
	if want := "aaaaaa\nbbXYbb\nccZWcc"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got = ansi.Strip(PlaceAt("ab", "XYZ", 5, 0))
	if want := "ab   XYZ"; got != want {
		t.Fatalf("past end: got %q, want %q", got, want)
	}
	if got := ansi.Strip(PlaceAt("ab", "X", 0, 3)); got != "ab" {
		t.Fatalf("rows outside base are dropped: got %q", got)
	}
}

func TestCenter(t *testing.T) {
	base := strings.Repeat(strings.Repeat(".", 10)+"\n", 4) + strings.Repeat(".", 10)
	got := strings.Split(ansi.Strip(Center(base, "##\n##", 10, 5)), "\n")
	if got[2] != "....##...." {
		t.Fatalf("got %q", got)
	}
}

func TestHelpScrollAndClose(t *testing.T) {
	h := NewHelp([]keymap.HelpEntry{
		{Context: keymap.Tree, Keys: "j, <down>", Desc: "Move down"},
		{Context: keymap.Global, Keys: "?", Desc: "Help"},
	})
	view := ansi.Strip(h.View(80, 20))
	for _, want := range []string{"tree", "j, <down>", "Move down", "global"} {
		if !strings.Contains(view, want) {
			t.Errorf("help view missing %q:\n%s", want, view)
		}
	}
	if o, _ := send(t, h, "esc"); o != nil {
		t.Fatal("esc must close help")
	}
}

func TestDetailPrettyPrintsJSONAndWraps(t *testing.T) {
	d := NewDetail("payload", `{"a":1,"b":[1,2]}`)
	view := ansi.Strip(d.View(80, 20))
	if !strings.Contains(view, `"a": 1`) {
		t.Fatalf("JSON not indented:\n%s", view)
	}
	long := NewDetail("note", strings.Repeat("x", 30))
	if !strings.Contains(ansi.Strip(long.View(20, 10)), "xxxxxxxxxx") {
		t.Fatal("long values must wrap, not vanish")
	}
	if o, _ := send(t, d, "q"); o != nil {
		t.Fatal("q must close detail")
	}
}

func TestDetailCopiesTheOriginalText(t *testing.T) {
	raw := `{"a":1}`
	d := NewDetail("payload", raw)
	if !strings.Contains(ansi.Strip(d.View(80, 20)), "y copy") {
		t.Fatalf("title:\n%s", ansi.Strip(d.View(80, 20)))
	}
	o, msg := send(t, d, "y")
	if o != d || msg != (CopyMsg{Text: raw, What: "payload"}) {
		t.Fatalf("o=%v msg=%#v", o, msg)
	}
}

func TestPasswordSubmit(t *testing.T) {
	p := NewPassword("prod")
	keys := []string{"s", "3", "c", "r", "x", "backspace", "t", "space", "tab", "enter"}
	o, msg := send(t, p, keys...)
	if o != nil {
		t.Fatal("enter must close the prompt")
	}
	got, ok := msg.(PasswordSubmittedMsg)
	if !ok {
		t.Fatalf("msg = %#v", msg)
	}
	if got != (PasswordSubmittedMsg{Conn: "prod", Password: "s3crt ", Save: false}) {
		t.Fatalf("got %+v", got)
	}
	if strings.Contains(ansi.Strip(NewPassword("x").View(60, 20)), "s3crt") {
		t.Fatal("password must be masked")
	}
}

func TestPasswordCancel(t *testing.T) {
	if o, msg := send(t, NewPassword("p"), "a", "esc"); o != nil || msg != nil {
		t.Fatalf("esc: o=%v msg=%v", o, msg)
	}
}

func TestCmdLine(t *testing.T) {
	o, msg := send(t, NewCmdLine(), "q", "a", "enter")
	if o != nil || msg != (CommandMsg{Text: "qa"}) {
		t.Fatalf("o=%v msg=%#v", o, msg)
	}
	if o, msg := send(t, NewCmdLine(), "backspace"); o != nil || msg != nil {
		t.Fatal("backspace on empty input closes the command line")
	}
	c := NewCmdLine()
	c.HandleKey(uitest.Key("w"))
	if !strings.Contains(ansi.Strip(c.View(20, 1)), ":w") {
		t.Fatal("view shows typed command")
	}
}

func TestWhichKey(t *testing.T) {
	box := ansi.Strip(WhichKey([]keymap.Hint{{Keys: "s", Desc: "Open structure"}, {Keys: "b", Desc: "Toggle tree"}}))
	if !strings.Contains(box, "s  Open structure") || !strings.Contains(box, "b  Toggle tree") {
		t.Fatalf("box:\n%s", box)
	}
}

type okMsg struct{ n int }

func TestSearchLine(t *testing.T) {
	o, msg := send(t, NewSearchLine(), "u", "s", "enter")
	if o != nil || msg != (SearchMsg{Text: "us"}) {
		t.Fatalf("o=%v msg=%#v", o, msg)
	}
	s := NewSearchLine()
	s.HandleKey(uitest.Key("x"))
	if !strings.HasPrefix(ansi.Strip(s.View(20, 1)), "/x") {
		t.Fatal("search line shows /")
	}
}

func TestConfirmYesNo(t *testing.T) {
	c := NewConfirm("Run?", []string{"DROP TABLE t"}, "", okMsg{1})
	if v := ansi.Strip(c.View(80, 20)); !strings.Contains(v, "DROP TABLE t") || !strings.Contains(v, "y confirm") {
		t.Fatalf("view:\n%s", v)
	}
	if o, msg := send(t, c, "x"); o == nil || msg != nil {
		t.Fatal("other keys are ignored")
	}
	if o, msg := send(t, c, "y"); o != nil || msg != (okMsg{1}) {
		t.Fatalf("y: o=%v msg=%v", o, msg)
	}
	if o, msg := send(t, NewConfirm("Run?", nil, "", okMsg{1}), "n"); o != nil || msg != nil {
		t.Fatal("n cancels")
	}
}

func TestConfirmTypedText(t *testing.T) {
	c := NewConfirm("No WHERE", []string{"delete from users"}, "users", okMsg{2})
	o, msg := send(t, c, "y")
	if o == nil || msg != nil {
		t.Fatal("y alone does not confirm when a word is expected")
	}
	o, msg = send(t, c, "backspace", "u", "s", "e", "r", "enter")
	if o == nil || msg != nil || !strings.Contains(ansi.Strip(o.View(80, 20)), "does not match") {
		t.Fatal("a wrong word keeps the box open with an error")
	}
	o, msg = send(t, o, "s", "enter")
	if o != nil || msg != (okMsg{2}) {
		t.Fatalf("o=%v msg=%v", o, msg)
	}
	if o, msg := send(t, NewConfirm("x", nil, "t", okMsg{}), "esc"); o != nil || msg != nil {
		t.Fatal("esc cancels")
	}
}

func TestPrompt(t *testing.T) {
	p := NewPrompt("Export", "Save to:", "/tmp/a.csv", func(s string) tea.Msg { return CommandMsg{Text: s} })
	o, msg := send(t, p, "backspace", "backspace", "backspace", "t", "s", "v", "enter")
	if o != nil || msg != (CommandMsg{Text: "/tmp/a.tsv"}) {
		t.Fatalf("o=%v msg=%#v", o, msg)
	}
	long := NewPrompt("Export", "Save to:", "/very/long/path/"+strings.Repeat("d", 80)+"/end.csv", nil)
	if v := ansi.Strip(long.View(40, 10)); !strings.Contains(v, "end.csv") {
		t.Fatalf("long values show their end:\n%s", v)
	}
	if o, _ := send(t, long, "esc"); o != nil {
		t.Fatal("esc cancels")
	}
}

func TestChoice(t *testing.T) {
	pick := func(i int) tea.Msg { return okMsg{i} }
	c := NewChoice("Export", []string{"Current page", "Whole table"}, pick)
	if v := ansi.Strip(c.View(60, 10)); !strings.Contains(v, "> Current page") {
		t.Fatalf("view:\n%s", v)
	}
	if o, msg := send(t, c, "j", "enter"); o != nil || msg != (okMsg{1}) {
		t.Fatalf("o=%v msg=%v", o, msg)
	}
	if o, msg := send(t, NewChoice("x", []string{"a", "b"}, pick), "k", "k", "enter"); o != nil || msg != (okMsg{0}) {
		t.Fatalf("clamped: %v", msg)
	}
	if o, msg := send(t, NewChoice("x", []string{"a", "b"}, pick), "2"); o != nil || msg != (okMsg{1}) {
		t.Fatalf("digits pick directly: %v", msg)
	}
}

func TestPreviewAndConfirmMarkDangerLines(t *testing.T) {
	lines := []string{"⚠ destructive  - column legacy", "", "ALTER TABLE t DROP COLUMN legacy;"}
	p := NewPreview("Modify", lines, nil, WithDanger(0))
	v := p.View(100, 20)
	if !strings.Contains(v, theme.Error.Render(lines[0])) {
		t.Fatalf("the warning is red:\n%q", v)
	}
	if strings.Contains(v, theme.Error.Render(lines[2])) {
		t.Fatal("the statement is not")
	}
	c := NewConfirm("Modify", lines, "t", nil, WithDanger(0))
	if v := c.View(100, 20); !strings.Contains(v, theme.Error.Render(lines[0])) {
		t.Fatalf("confirm:\n%q", v)
	}
}
