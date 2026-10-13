package vimedit

import "testing"

func TestVisualMode(t *testing.T) {
	runCases(t, []editCase{
		{"|select id", "v", "|select id", Visual},
		{"|select id", "vV", "|select id", VisualLine},
		{"|select id", "vv", "|select id", Normal},
		{"|select id", "v<esc>", "|select id", Normal},
		{"|select id", "ved", "| id", Normal},
		{"|select id", "vex", "| id", Normal},
		{"sele|ct id", "vbd", "|t id", Normal},
		{"|select id", "vecx<esc>", "|x id", Normal},
		{"|a\nb\nc", "Vjd", "|c", Normal},
		{"a\n|b\nc", "Vkd", "|c", Normal},
		{"|a\nb", "Vjcx<esc>", "|x", Normal},
		{"|a\nb", "Vj>", "    |a\n    b", Normal},
		{"|a\nb", "Vjyjp", "a\nb\n|a\nb", Normal},
		{"|ab cd", "veyP", "a|bab cd", Normal},
		{"ab |cd", "veo", "ab |cd", Visual},
		{"x f(a|, b) y", "vi(d", "x f(|) y", Normal},
		{"x f(a|, b) y", "va(d", "x f| y", Normal},
		{"select us|ers x", "viwd", "select | x", Normal},
		{"|ab cd", "ywwvep", "ab ab| ", Normal},
		{"|ab\ncd", "yyjVp", "ab\n|ab", Normal},
		{"|ab", "v3l", "a|b", Visual},
	})
}

func TestVisualSelection(t *testing.T) {
	e := newAt(t, "select 1;\nsele|ct 2;\nx")
	if _, _, ok := e.Selection(); ok {
		t.Fatal("no selection in NORMAL mode")
	}
	feed(e, "vb")
	from, to, ok := e.Selection()
	if !ok || e.Text()[from:to] != "selec" {
		t.Fatalf("charwise selection: %q", e.Text()[from:to])
	}
	feed(e, "V")
	from, to, _ = e.Selection()
	if got := e.Text()[from:to]; got != "select 2;" {
		t.Fatalf("linewise selection: %q", got)
	}
	feed(e, "k")
	from, to, _ = e.Selection()
	if got := e.Text()[from:to]; got != "select 1;\nselect 2;" {
		t.Fatalf("two lines: %q", got)
	}
	if r := e.Feed(Key{Name: "enter"}); r.Handled {
		t.Fatal("enter is left to the host (run the selection)")
	}
	if r := e.Feed(Key{Name: "esc"}); !r.Handled || e.Mode() != Normal {
		t.Fatal("esc leaves VISUAL mode")
	}
}

func TestVisualEmptyText(t *testing.T) {
	e := newAt(t, "|")
	feed(e, "vd")
	if e.Text() != "" || e.Mode() != Normal {
		t.Fatalf("%q %s", e.Text(), e.Mode())
	}
	feed(e, "Vd")
	if e.Text() != "" {
		t.Fatal(e.Text())
	}
}

func TestReplacingTheTextLeavesVisualMode(t *testing.T) {
	for _, replace := range []func(e *Editor, s string){(*Editor).ReplaceAll, (*Editor).SetText} {
		e := newAt(t, "a\nb\nc\n|d")
		feed(e, "vk")
		replace(e, "x")
		if e.Mode() != Normal {
			t.Fatalf("mode after replacing the text: %s", e.Mode())
		}
		if _, _, ok := e.Selection(); ok {
			t.Fatal("no selection after replacing the text")
		}
		feed(e, "vd")
		if e.Text() != "" {
			t.Fatalf("text = %q", e.Text())
		}
	}
}
