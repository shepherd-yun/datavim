package ui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"
)

const usersPage = " ORDER BY `id` LIMIT 3 OFFSET 0"

func TestFilterMenuInstantConditions(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right) // name of user1
	for _, c := range []struct {
		action keymap.Action
		where  string
	}{
		{keymap.FilterNotEqual, "`name` <> 'user1'"},
		{keymap.FilterNotNull, "(`name` <> 'user1') AND `name` IS NOT NULL"},
	} {
		tab.Update(run(tab.Handle(c.action)))
		if got, want := b.conn.lastQuery(), "SELECT * FROM `app`.`users` WHERE ("+c.where+"\n)"+usersPage; got != want {
			t.Fatalf("%s: query = %q, want %q", c.action, got, want)
		}
	}
	tab.Update(run(tab.Handle(keymap.FilterClear)))
	if got := b.conn.lastQuery(); got != "SELECT * FROM `app`.`users`"+usersPage {
		t.Fatalf("fx clears the filter: %q", got)
	}
	if tab.Handle(keymap.FilterClear) != nil {
		t.Fatal("fx without a filter does not reload")
	}
	tab.Update(run(tab.Handle(keymap.FilterNull)))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE (`name` IS NULL\n)") {
		t.Fatalf("fn: %q", got)
	}
}

// promptFor runs action and returns the value prompt it opens.
func promptFor(t *testing.T, tab *dataTab, action keymap.Action) *overlay.Prompt {
	t.Helper()
	msg, ok := run(tab.Handle(action)).(openOverlayMsg)
	p, isPrompt := msg.overlay.(*overlay.Prompt)
	if !ok || !isPrompt {
		t.Fatalf("%s opens a prompt: %#v", action, msg)
	}
	return p
}

func TestFilterMenuContains(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right)
	p := promptFor(t, tab, keymap.FilterContains)
	if v := ansi.Strip(p.View(80, 24)); !strings.Contains(v, "name contains") || !strings.Contains(v, "user1") {
		t.Fatalf("the prompt names the column and starts from the cell:\n%s", v)
	}
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterContains, col: colNamed("name"), text: `50%_'`})))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE (`name` LIKE '%50\\\\%\\\\_''%'\n)") {
		t.Fatalf("contains escapes the value: %q", got)
	}
}

func TestFilterMenuComparisons(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	promptFor(t, tab, keymap.FilterGreater) // cursor on id
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterGreater, col: colNamed("id"), text: "1"})))
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterLess, col: colNamed("id"), text: "9"})))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE ((`id` > '1') AND `id` < '9'\n)") {
		t.Fatalf("comparisons add up: %q", got)
	}
	before := b.conn.lastQuery()
	if tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterLess, col: colNamed("id"), text: ""}) != nil || b.conn.lastQuery() != before {
		t.Fatal("an empty value cancels")
	}
}

// colNamed is the column called name as the fake reports it (no type, so
// values are quoted).
func colNamed(name string) db.ColumnInfo { return db.ColumnInfo{Name: name} }

func TestFilterMenuNeedsACell(t *testing.T) {
	b := newFakeBackend()
	b.conn.rows = 0
	tab := loadedDataTab(t, b)
	before := b.conn.lastQuery()
	tab.Handle(keymap.FilterEqual)
	if b.conn.lastQuery() != before || !strings.Contains(ansi.Strip(tab.View(true)), "no cell to filter by") {
		t.Fatalf("ff on an empty page:\n%s", ansi.Strip(tab.View(true)))
	}
}

func TestFilterMenuPrefixCancels(t *testing.T) {
	m, b, _ := openUsersTab(t)
	before := b.conn.lastQuery()
	press(t, m, "f", "esc")
	press(t, m, "f", "q")
	if b.conn.lastQuery() != before || m.overlay != nil {
		t.Fatal("f then esc or an unbound key does nothing")
	}
	press(t, m, "l", "f", "f")
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE (`name` = 'user1'\n)") {
		t.Fatalf("ff filters by the cell: %q", got)
	}
}

func TestBadFilterValueKeepsThePage(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	b.conn.setFail("SELECT * FROM `app`.`users` WHERE (`id` > 'x'\n)"+usersPage, errors.New("bad value"))
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterGreater, col: colNamed("id"), text: "x"})))
	if v := ansi.Strip(tab.View(true)); !strings.Contains(v, "bad value") || !strings.Contains(v, "user1") {
		t.Fatalf("view:\n%s", v)
	}
}

// filterPrompt presses F and returns the prompt.
func filterPrompt(t *testing.T, tab *dataTab) *overlay.Prompt {
	t.Helper()
	return promptFor(t, tab, keymap.FilterPrompt)
}

func promptText(t *testing.T, p *overlay.Prompt) string {
	t.Helper()
	_, cmd := p.HandleKey(uitest.Key("enter"))
	if cmd == nil {
		t.Fatal("enter did not submit")
	}
	m, ok := cmd().(filterMsg)
	if !ok {
		t.Fatalf("submit: %#v", cmd())
	}
	return m.text
}

func candTexts(cands []complete.Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.Text
	}
	return out
}

func TestFilterPromptStartsFromTheColumn(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right)
	if got := promptText(t, filterPrompt(t, tab)); got != "name " {
		t.Fatalf("an empty filter starts with the column: %q", got)
	}
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: "id > 1"})))
	if got := promptText(t, filterPrompt(t, tab)); got != "id > 1" {
		t.Fatalf("an existing filter is kept: %q", got)
	}
}

func TestFilterPromptCompletes(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	start, cands := tab.completeFilter("id > 1 and na", 13)
	if start != 11 || len(cands) == 0 || cands[0].Text != "name" || cands[0].Detail != "text" {
		t.Fatalf("start=%d cands=%+v", start, cands)
	}
	_, cands = tab.completeFilter("name li", 7)
	if len(cands) == 0 || cands[0].Text != "like" {
		t.Fatalf("keywords follow the typed case: %+v", cands)
	}
	_, cands = tab.completeFilter("name IS N", 9)
	if texts := candTexts(cands); !slices.Contains(texts, "NULL") || !slices.Contains(texts, "NOT") {
		t.Fatalf("upper case: %v", texts)
	}
	if _, cands = tab.completeFilter("", 0); len(cands) < 2 {
		t.Fatalf("tab on nothing offers everything: %+v", cands)
	}
}

func TestFilterPromptChecksLocally(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	p := filterPrompt(t, tab)
	p.HandleKey(uitest.Key("ctrl+u"))
	for _, k := range uitest.Type("1=1; DELETE FROM users") {
		p.HandleKey(k)
	}
	o, cmd := p.HandleKey(uitest.Key("enter"))
	if o == nil || cmd != nil || !strings.Contains(ansi.Strip(p.View(80, 24)), "the filter must be a condition") {
		t.Fatalf("a bad filter keeps the prompt open:\n%s", ansi.Strip(p.View(80, 24)))
	}
}

func TestFilterPromptBringsBackAFailedFilter(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	b.conn.setFail("SELECT * FROM `app`.`users` WHERE (nmae = 'x'\n)"+usersPage, errors.New("unknown column nmae"))
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: "nmae = 'x'"})))
	if got := promptText(t, filterPrompt(t, tab)); got != "nmae = 'x'" {
		t.Fatalf("F brings back the failed filter: %q", got)
	}
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: "name = 'x'"})))
	tab.Update(run(tab.Update(filterMsg{tabID: 1, text: ""})))
	if got := promptText(t, filterPrompt(t, tab)); got != "id " {
		t.Fatalf("a later success forgets it: %q", got)
	}
}

func TestFilterMenuLike(t *testing.T) {
	b := newFakeBackend()
	tab := loadedDataTab(t, b)
	tab.Handle(keymap.Right)
	p := promptFor(t, tab, keymap.FilterLike)
	if v := ansi.Strip(p.View(80, 24)); !strings.Contains(v, "name LIKE") || !strings.Contains(v, "%user1%") {
		t.Fatalf("the prompt names the column and starts from %%cell%%:\n%s", v)
	}
	tab.Update(run(tab.Update(filterValueMsg{tabID: 1, action: keymap.FilterLike, col: colNamed("name"), text: "user_%"})))
	if got := b.conn.lastQuery(); !strings.Contains(got, "WHERE (`name` LIKE 'user_%'\n)") {
		t.Fatalf("like keeps the wildcards: %q", got)
	}
}

func TestFilterMenuShowsTheChoices(t *testing.T) {
	m, _, _ := openUsersTab(t)
	press(t, m, "f")
	if s := screen(m); !strings.Contains(s, "LIKE pattern") || !strings.Contains(s, "contains") {
		t.Fatalf("f lists the filters:\n%s", s)
	}
}
