package overlay

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
)

type resetSubmitted struct{ commits []kafka.OffsetCommit }

var resetNow = time.Date(2026, 10, 20, 14, 0, 0, 0, time.Local)

func resetLags() []kafka.PartitionLag {
	return []kafka.PartitionLag{
		{TP: kafka.TP{Topic: "orders", Partition: 1}, Committed: 1180002, Start: 1002113, End: 1228190, Lag: 48188},
		{TP: kafka.TP{Topic: "orders", Partition: 0}, Committed: 10, Start: 0, End: 20, Lag: 10},
		{TP: kafka.TP{Topic: "refunds", Partition: 0}, Committed: -1, Start: 0, End: 5, Lag: 5},
	}
}

func newReset(sel kafka.TP, members int, prod bool) *ResetOffsets {
	return NewResetOffsets(keymap.Default(), "order-svc", resetLags(), []kafka.TP{sel}, members, prod, resetNow,
		func(c []kafka.OffsetCommit) tea.Msg { return resetSubmitted{c} }, nil)
}

func view(o Overlay) string { return ansi.Strip(o.View(120, 40)) }

func TestResetDialogDefaultNext(t *testing.T) {
	r := newReset(kafka.TP{Topic: "orders", Partition: 1}, 0, false)
	if v := view(r); !strings.Contains(v, "orders p1  1,180,002 → 1,180,003 · lag 48,188 → 48,187") || !strings.Contains(v, "next") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestResetDialogScopeTopic(t *testing.T) {
	r := newReset(kafka.TP{Topic: "orders", Partition: 1}, 0, false)
	send(t, r, "right") // scope: all of orders
	v := view(r)
	if !strings.Contains(v, "orders p0  10 → 11") || !strings.Contains(v, "orders p1") || strings.Contains(v, "refunds p0") {
		t.Fatalf("view:\n%s", v)
	}
	send(t, r, "right") // all topics: refunds p0 never committed, so next fails
	if v := view(r); !strings.Contains(v, "refunds p0 has no committed offset") {
		t.Fatalf("view:\n%s", v)
	}
	_, msg := send(t, r, "tab", "ctrl+u", "e", "a", "r", "l", "i", "e", "s", "t", "enter", "y")
	got, ok := msg.(resetSubmitted)
	if !ok || len(got.commits) != 3 {
		t.Fatalf("msg = %#v", msg)
	}
	for _, c := range got.commits {
		if c.TP == (kafka.TP{Topic: "refunds"}) && (c.Expect != -1 || c.To != 0) {
			t.Fatalf("refunds commit = %+v", c)
		}
	}
}

func TestResetDialogActiveMembers(t *testing.T) {
	r := newReset(kafka.TP{Topic: "orders", Partition: 1}, 3, false)
	if v := view(r); !strings.Contains(v, "group has 3 active members — stop the consumers first") {
		t.Fatalf("view:\n%s", v)
	}
	if o, msg := send(t, r, "enter", "y"); o == nil || msg != nil {
		t.Fatal("no reset while members are active")
	}
}

func TestResetDialogProdTypeName(t *testing.T) {
	r := newReset(kafka.TP{Topic: "orders", Partition: 1}, 0, true)
	o, msg := send(t, r, "enter", "y", "enter")
	if o == nil || msg != nil {
		t.Fatal("prod wants the group name")
	}
	if v := view(o); !strings.Contains(v, "order-svc") || !strings.Contains(v, "does not match") {
		t.Fatalf("view:\n%s", v)
	}
	send(t, o, "backspace")
	for _, k := range "order-svc" {
		send(t, o, string(k))
	}
	_, msg = send(t, o, "enter")
	got, ok := msg.(resetSubmitted)
	if !ok || len(got.commits) != 1 || got.commits[0] != (kafka.OffsetCommit{TP: kafka.TP{Topic: "orders", Partition: 1}, Expect: 1180002, To: 1180003}) {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestResetDialogNonProdY(t *testing.T) {
	r := newReset(kafka.TP{Topic: "orders", Partition: 0}, 0, false)
	_, msg := send(t, r, "tab", "ctrl+u", "+", "5", "enter", "y")
	got, ok := msg.(resetSubmitted)
	if !ok || got.commits[0] != (kafka.OffsetCommit{TP: kafka.TP{Topic: "orders"}, Expect: 10, To: 15}) {
		t.Fatalf("msg = %#v", msg)
	}
}

func TestResetDialogClamped(t *testing.T) {
	r := newReset(kafka.TP{Topic: "orders", Partition: 0}, 0, false)
	send(t, r, "tab", "ctrl+u", "9", "9", "9")
	if v := view(r); !strings.Contains(v, "10 → 20") || !strings.Contains(v, "clamped") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestResetDialogTimeTarget(t *testing.T) {
	var asked time.Time
	r := NewResetOffsets(keymap.Default(), "order-svc", resetLags(), []kafka.TP{{Topic: "orders"}}, 0, false, resetNow,
		func(c []kafka.OffsetCommit) tea.Msg { return resetSubmitted{c} },
		func(ts time.Time) tea.Cmd { asked = ts; return nil })
	send(t, r, "tab", "ctrl+u", "@", "0", "9", ":", "1", "2")
	if asked.IsZero() || !strings.Contains(view(r), "…") {
		t.Fatalf("asked %v:\n%s", asked, view(r))
	}
	r.SetTimes(asked, map[kafka.TP]int64{{Topic: "orders"}: 12})
	if v := view(r); !strings.Contains(v, "10 → 12") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestResetNeverCommittedNext(t *testing.T) {
	r := newReset(kafka.TP{Topic: "refunds", Partition: 0}, 0, false)
	if v := view(r); !strings.Contains(v, "has no committed offset; pick earliest, latest or an offset") {
		t.Fatalf("view:\n%s", v)
	}
	if o, msg := send(t, r, "enter", "y"); o == nil || msg != nil {
		t.Fatal("an error keeps the dialog")
	}
}

func TestResetDialogConfirmWording(t *testing.T) {
	r := newReset(kafka.TP{Topic: "orders", Partition: 1}, 0, true)
	send(t, r, "enter")
	if v := view(r); !strings.Contains(v, "Reset 1 partition? Type order-svc") {
		t.Fatalf("view:\n%s", v)
	}
}
