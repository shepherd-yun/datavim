package overlay

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/datavim/internal/ui/panes/complete"
)

// columns completes the word before the cursor from a fixed list.
func columns(text string, cursor int) (int, []complete.Candidate) {
	rs := []rune(text)
	start := cursor
	for start > 0 && rs[start-1] != ' ' {
		start--
	}
	var out []complete.Candidate
	for _, c := range []string{"name", "nickname", "id"} {
		if strings.HasPrefix(c, string(rs[start:cursor])) {
			out = append(out, complete.Candidate{Text: c, Label: c, Kind: complete.Column})
		}
	}
	return start, out
}

func textMsg(s string) tea.Msg { return CommandMsg{Text: s} }

func TestPromptCompletesASingleCandidate(t *testing.T) {
	p := NewPrompt("Filter", "WHERE", "x = 1 and i", textMsg, WithCompleter(columns))
	if o, msg := send(t, p, "tab", "enter"); o != nil || msg != (CommandMsg{Text: "x = 1 and id"}) {
		t.Fatalf("o=%v msg=%#v", o, msg)
	}
}

func TestPromptCompletionPopup(t *testing.T) {
	p := NewPrompt("Filter", "WHERE", "n", textMsg, WithCompleter(columns))
	send(t, p, "tab")
	if v := ansi.Strip(p.View(80, 24)); !strings.Contains(v, "nickname") || !strings.Contains(v, "name") {
		t.Fatalf("popup lists the candidates:\n%s", v)
	}
	o, msg := send(t, p, "tab", "enter")
	if o == nil || msg != nil || p.input.String() != "nickname" || p.popup != nil {
		t.Fatalf("tab moves on, enter accepts and keeps the prompt open: %q", p.input.String())
	}
	if _, msg := send(t, p, "enter"); msg != (CommandMsg{Text: "nickname"}) {
		t.Fatalf("msg=%#v", msg)
	}
}

func TestPromptPopupKeys(t *testing.T) {
	p := NewPrompt("Filter", "WHERE", "n", textMsg, WithCompleter(columns))
	if o, _ := send(t, p, "tab", "esc"); o == nil || p.popup != nil {
		t.Fatal("esc closes only the popup")
	}
	send(t, p, "tab", "a")
	if p.popup != nil || p.input.String() != "na" {
		t.Fatalf("typing closes the popup and types: %q", p.input.String())
	}
	send(t, p, "backspace", "tab", "down", "up", "enter")
	if p.input.String() != "name" {
		t.Fatalf("up / down move in the popup: %q", p.input.String())
	}
	send(t, p, "ctrl+u", "z", "tab")
	if p.popup != nil || p.input.String() != "z" {
		t.Fatal("no candidates: nothing happens")
	}
}

func TestPromptWithoutCompleterIgnoresTab(t *testing.T) {
	p := NewPrompt("Export", "File:", "a", textMsg)
	if _, msg := send(t, p, "tab", "enter"); msg != (CommandMsg{Text: "a"}) {
		t.Fatalf("msg=%#v", msg)
	}
}

func TestPromptValidate(t *testing.T) {
	check := func(s string) error {
		if strings.Contains(s, ";") {
			return errors.New("one condition only")
		}
		return nil
	}
	p := NewPrompt("Filter", "WHERE", "a; b", textMsg, WithValidate(check))
	o, msg := send(t, p, "enter")
	if o == nil || msg != nil || !strings.Contains(ansi.Strip(p.View(80, 24)), "one condition only") {
		t.Fatalf("an invalid value keeps the prompt open with the error:\n%s", ansi.Strip(p.View(80, 24)))
	}
	send(t, p, "ctrl+u")
	if strings.Contains(ansi.Strip(p.View(80, 24)), "one condition only") {
		t.Fatal("editing clears the error")
	}
	if _, msg := send(t, p, "a", "enter"); msg != (CommandMsg{Text: "a"}) {
		t.Fatalf("msg=%#v", msg)
	}
}

func TestPromptHistory(t *testing.T) {
	p := NewPrompt("Filter", "q", "typed", textMsg, WithHistory([]string{"newest", "older"}))
	send(t, p, "up")
	if p.input.String() != "newest" {
		t.Fatalf("up = %q", p.input.String())
	}
	send(t, p, "up", "up")
	if p.input.String() != "older" {
		t.Fatalf("up up = %q", p.input.String())
	}
	send(t, p, "down", "down")
	if p.input.String() != "typed" {
		t.Fatalf("back down = %q", p.input.String())
	}
}

func TestPromptWithError(t *testing.T) {
	p := NewPrompt("Filter", "q", "a:(", textMsg, WithError("parse_exception: bad"))
	if v := ansi.Strip(p.View(80, 20)); !strings.Contains(v, "parse_exception: bad") || !strings.Contains(v, "a:(") {
		t.Fatalf("view:\n%s", v)
	}
	send(t, p, "x")
	if v := ansi.Strip(p.View(80, 20)); strings.Contains(v, "parse_exception") {
		t.Fatalf("error kept after an edit:\n%s", v)
	}
}
