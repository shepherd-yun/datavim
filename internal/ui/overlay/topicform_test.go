package overlay

import (
	"maps"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

func typeIn(t *testing.T, o Overlay, s string) {
	t.Helper()
	for _, r := range s {
		send(t, o, string(r))
	}
}

func TestTopicForm(t *testing.T) {
	f := NewTopicForm(keymap.Default())
	typeIn(t, f, "audit")
	send(t, f, "tab", "backspace")
	typeIn(t, f, "6")
	send(t, f, "tab")
	typeIn(t, f, "3")
	send(t, f, "tab")
	typeIn(t, f, "86400000")
	send(t, f, "tab", "right", "right") // cleanup: default -> delete -> compact
	send(t, f, "tab")
	typeIn(t, f, "2")
	send(t, f, "tab")
	typeIn(t, f, "segment.ms=1000, max.message.bytes = 2048")
	_, msg := send(t, f, "ctrl+s")
	got, ok := msg.(TopicFormMsg)
	if !ok {
		t.Fatalf("msg = %#v\n%s", msg, ansi.Strip(f.View(100, 40)))
	}
	want := map[string]string{
		"retention.ms": "86400000", "cleanup.policy": "compact", "min.insync.replicas": "2",
		"segment.ms": "1000", "max.message.bytes": "2048",
	}
	if got.Topic.Name != "audit" || got.Topic.Partitions != 6 || got.Topic.Replication != 3 || !maps.Equal(got.Topic.Configs, want) {
		t.Fatalf("topic = %+v", got.Topic)
	}
	_, msg = send(t, NewTopicForm(keymap.Default()), "x", "ctrl+s")
	if got := msg.(TopicFormMsg).Topic; got.Partitions != 1 || got.Replication != -1 || got.Configs != nil {
		t.Fatalf("defaults = %+v", got)
	}
}

func TestTopicFormValidation(t *testing.T) {
	for _, tc := range []struct{ keys, want string }{
		{"", "name is required"},
		{"a b", "letters, digits, . _ and -"},
		{".", `"." and ".." are not topic names`},
		{strings.Repeat("a", 250), "249 characters at most"},
	} {
		f := NewTopicForm(keymap.Default())
		typeIn(t, f, tc.keys)
		if o, msg := send(t, f, "ctrl+s"); o == nil || msg != nil || !strings.Contains(ansi.Strip(f.View(300, 40)), tc.want) {
			t.Errorf("%q: want %q:\n%s", tc.keys, tc.want, ansi.Strip(f.View(300, 40)))
		}
	}
	f := NewTopicForm(keymap.Default())
	typeIn(t, f, "ok")
	send(t, f, "tab", "backspace", "0")
	if o, _ := send(t, f, "ctrl+s"); o == nil || !strings.Contains(ansi.Strip(f.View(100, 40)), "partitions must be a number ≥ 1") {
		t.Fatal("partitions ≥ 1")
	}
	f = NewTopicForm(keymap.Default())
	typeIn(t, f, "ok")
	send(t, f, "tab", "tab", "tab", "tab", "tab", "tab")
	typeIn(t, f, "novalue")
	if o, _ := send(t, f, "ctrl+s"); o == nil || !strings.Contains(ansi.Strip(f.View(100, 40)), "other configs: write name=value") {
		t.Fatalf("configs:\n%s", ansi.Strip(f.View(100, 40)))
	}
}
