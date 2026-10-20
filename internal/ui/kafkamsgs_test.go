package ui

import (
	"encoding/base64"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/uitest"

	tea "charm.land/bubbletea/v2"
)

func rec(p int32, o int64, sec int) kafka.Record {
	return kafka.Record{Partition: p, Offset: o, Time: fakeKafkaBase.Add(time.Duration(sec) * time.Second)}
}

func ids(recs []kafka.Record) []string {
	var out []string
	for _, r := range recs {
		out = append(out, "p"+string(rune('0'+r.Partition))+":"+itoa64(r.Offset))
	}
	return out
}

func itoa64(n int64) string { return groupDigits(n) }

func TestMergeRecords(t *testing.T) {
	old := []kafka.Record{rec(0, 10, 10), rec(1, 5, 11), rec(0, 11, 12)}
	for _, tc := range []struct {
		name        string
		old, add    []kafka.Record
		dir         int
		single      bool
		limit       int
		cursor      int
		want        []string
		wantCursor  int
		wantDropped int
	}{
		{"older by time", old, []kafka.Record{rec(0, 9, 9), rec(1, 4, 8)}, -1, false, 100, 0,
			[]string{"p1:4", "p0:9", "p0:10", "p1:5", "p0:11"}, 2, 0},
		{"newer by time", old, []kafka.Record{rec(1, 6, 13)}, 1, false, 100, 2,
			[]string{"p0:10", "p1:5", "p0:11", "p1:6"}, 2, 0},
		{"single by offset", []kafka.Record{rec(0, 10, 50), rec(0, 11, 40)}, []kafka.Record{rec(0, 9, 60)}, -1, true, 100, 0,
			[]string{"p0:9", "p0:10", "p0:11"}, 1, 0},
		{"replace", old, []kafka.Record{rec(2, 1, 1)}, 0, false, 100, 2,
			[]string{"p2:1"}, 0, 0},
		{"limit drops the far end (tail)", old, []kafka.Record{rec(0, 9, 9), rec(1, 4, 8)}, -1, false, 3, 0,
			[]string{"p1:4", "p0:9", "p0:10"}, 2, 2},
		{"limit drops the far end (head)", old, []kafka.Record{rec(1, 6, 13), rec(1, 7, 14)}, 1, false, 3, 2,
			[]string{"p0:11", "p1:6", "p1:7"}, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, cur, dropped := mergeRecords(slices.Clone(tc.old), tc.add, tc.dir, tc.single, tc.limit, tc.cursor)
			if !slices.Equal(ids(got), tc.want) || cur != tc.wantCursor || dropped != tc.wantDropped {
				t.Fatalf("got %v cursor %d dropped %d; want %v %d %d", ids(got), cur, dropped, tc.want, tc.wantCursor, tc.wantDropped)
			}
		})
	}
}

// openMessages opens the topic tab on Messages and returns it.
func openMessages(t *testing.T, m *Model, topic string) *topicTab {
	t.Helper()
	tt := openTopicTab(t, m, topic)
	if tt.section != secMessages {
		t.Fatal("a topic tab opens on Messages")
	}
	return tt
}

func cursorRec(t *testing.T, tt *topicTab) kafka.Record {
	t.Helper()
	r, ok := tt.msgs.current()
	if !ok {
		t.Fatal("no message under the cursor")
	}
	return r
}

func TestMessagesLatest(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 300)
	tt := openMessages(t, m, "orders")
	r := k.lastReader()
	want := map[int32]kafka.Range{0: {From: 100, To: 300}, 1: {From: 100, To: 300}, 2: {From: 100, To: 300}}
	if got := r.rangeList(); len(got) != 1 || !maps.Equal(got[0], want) {
		t.Fatalf("ranges = %v", got)
	}
	recs := tt.msgs.recs
	if len(recs) != 200 || !slices.IsSortedFunc(recs, func(a, b kafka.Record) int { return a.Time.Compare(b.Time) }) {
		t.Fatalf("%d records, sorted %v", len(recs), slices.IsSortedFunc(recs, func(a, b kafka.Record) int { return a.Time.Compare(b.Time) }))
	}
	if last := recs[len(recs)-1]; last.Partition != 2 || last.Offset != 299 {
		t.Fatalf("newest = %+v", last)
	}
	if c := cursorRec(t, tt); c.Offset != 299 || c.Partition != 2 {
		t.Fatalf("cursor on %+v, want the newest", c)
	}
	s := screen(m)
	for _, w := range []string{"PART", "OFFSET", "TIME", "KEY", "SIZE", "VALUE", "k-299", `{"p":2,"o":299}`, "latest"} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
}

func TestMessagesAutoLoadOlder(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1000)
	tt := openMessages(t, m, "orders")
	pressAll(t, m, "g", "g", "k")
	if got := k.lastReader().rangeList(); len(got) != 2 || got[1][0] != (kafka.Range{From: 600, To: 800}) {
		t.Fatalf("ranges = %v", got)
	}
	if n := len(tt.msgs.recs); n != 400 {
		t.Fatalf("%d records", n)
	}
	if c := cursorRec(t, tt); c.Offset != 800 {
		t.Fatalf("the cursor stays on its message: %d", c.Offset)
	}
	pressAll(t, m, "G", "j")
	if !strings.Contains(m.status+tt.status, "end of partition") {
		t.Fatalf("status = %q / %q", m.status, tt.status)
	}
}

func TestMessagesBeginning(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 50)
	tt := openMessages(t, m, "orders")
	pressAll(t, m, "g", "g", "k")
	if !strings.Contains(tt.status, "beginning of partition") || len(k.lastReader().rangeList()) != 1 {
		t.Fatalf("status %q, ranges %v", tt.status, k.lastReader().rangeList())
	}
}

func seek(t *testing.T, m *Model, text string) {
	t.Helper()
	pressAll(t, m, "t")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	typeText(t, m, text)
	pressAll(t, m, "enter")
}

func TestMessagesSeekOffset(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 3, 0, 300)
	tt := openMessages(t, m, "orders")
	pressAll(t, m, "t")
	typeText(t, m, "p1:150")
	if s := screen(m); !strings.Contains(s, "p1 @ 150 (start 0 · end 300)") {
		t.Fatalf("live preview:\n%s", s)
	}
	pressAll(t, m, "enter")
	if got := k.lastReader().rangeList(); !maps.Equal(got[len(got)-1], map[int32]kafka.Range{1: {From: 147, To: 347 - 47}}) {
		t.Fatalf("ranges = %v", got[len(got)-1])
	}
	c := cursorRec(t, tt)
	row, _ := tt.msgGrid.Position()
	if c.Partition != 1 || c.Offset != 150 || row != 3 || !tt.msgs.single {
		t.Fatalf("cursor %+v at row %d, single %v", c, row, tt.msgs.single)
	}
	pressAll(t, m, "t")
	typeText(t, m, "p7:1")
	pressAll(t, m, "enter")
	if !strings.Contains(screen(m), "partition 7 doesn't exist") {
		t.Fatalf("a bad jump keeps the prompt with its error:\n%s", screen(m))
	}
}

func TestMessagesSeekGone(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 100, 200)
	openMessages(t, m, "orders")
	before := len(k.lastReader().rangeList())
	seek(t, m, "p0:5")
	if _, ok := m.overlay.(*overlay.Confirm); !ok || !strings.Contains(screen(m), "offset 5 is gone; earliest is 100 · jump to earliest?") {
		t.Fatalf("overlay %T:\n%s", m.overlay, screen(m))
	}
	if n := len(k.lastReader().rangeList()); n != before {
		t.Fatal("nothing is read before the answer")
	}
	pressAll(t, m, "n")
	if n := len(k.lastReader().rangeList()); n != before {
		t.Fatal("no means no read")
	}
	seek(t, m, "p0:5")
	pressAll(t, m, "y")
	got := k.lastReader().rangeList()
	if last := got[len(got)-1]; last[0].From != 100 {
		t.Fatalf("range = %v", last)
	}
}

func TestMessagesSeekTime(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 2, 0, 100)
	tt := openMessages(t, m, "orders")
	seek(t, m, "@09:00:30")
	at := time.Date(2026, 10, 20, 9, 0, 30, 0, time.Local)
	c := cursorRec(t, tt)
	if c.Time.Before(at) || k.countCalls("OffsetsAt") != 1 {
		t.Fatalf("cursor %+v", c)
	}
	row, _ := tt.msgGrid.Position()
	if row == 0 || !tt.msgs.recs[row-1].Time.Before(at) {
		t.Fatalf("the row above the cursor is before the time (row %d)", row)
	}
}

func TestMessagesDetailFormatCycle(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 3)
	openMessages(t, m, "orders")
	pressAll(t, m, "K")
	d, ok := m.overlay.(*msgDetail)
	if !ok {
		t.Fatalf("overlay = %T", m.overlay)
	}
	s := screen(m)
	for _, w := range []string{"orders · p0 · offset 2", "CreateTime", "key", "k-2", `"o": 2`, "auto: json"} {
		if !strings.Contains(s, w) {
			t.Fatalf("missing %q:\n%s", w, s)
		}
	}
	for _, want := range []string{"text", "json", "hex", "base64", "auto"} {
		pressAll(t, m, "v")
		if !strings.Contains(screen(m), want) {
			t.Fatalf("format %s:\n%s", want, screen(m))
		}
		if want == "base64" && !strings.Contains(screen(m), base64.StdEncoding.EncodeToString([]byte(`{"p":0,"o":2}`))) {
			t.Fatalf("base64:\n%s", screen(m))
		}
	}
	pressAll(t, m, "N")
	if d.rec.Offset != 1 {
		t.Fatalf("N shows the previous message: %d", d.rec.Offset)
	}
}

func TestMessagesYankRowJSON(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 3)
	tt := openMessages(t, m, "orders")
	clip := captureClipboard(t)
	pressAll(t, m, "y", "y")
	if want := string(kmsgx.JSON(cursorRec(t, tt))); *clip != want {
		t.Fatalf("clipboard = %q, want %q", *clip, want)
	}
	pressAll(t, m, "$", "y", "l") // VALUE
	if *clip != `{"p":0,"o":2}` {
		t.Fatalf("value = %q", *clip)
	}
}

func TestMessagesHeadersColumn(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	r := fakeRecord("orders", 0, 0)
	r.Headers = []kafka.Header{{Key: "trace", Value: []byte("abc")}, {Key: "bin", Value: []byte{0xff}}}
	k.setRecord(r)
	tt := openMessages(t, m, "orders")
	if strings.Contains(screen(m), "HEADERS") {
		t.Fatal("headers are hidden at first")
	}
	pressAll(t, m, "c")
	if s := screen(m); !strings.Contains(s, "HEADERS") || !strings.Contains(s, "trace=abc") {
		t.Fatalf("screen:\n%s", s)
	}
	clip := captureClipboard(t)
	tt.msgGrid.SelectColumn("HEADERS")
	pressAll(t, m, "y", "l")
	if *clip != `[{"key":"trace","value":"abc"},{"key":"bin","value":"/w==","encoding":"base64"}]` {
		t.Fatalf("headers = %q", *clip)
	}
}

func TestMessagesBinaryPreview(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 1)
	r := fakeRecord("orders", 0, 0)
	r.Value = []byte{0x00, 0xff}
	k.setRecord(r)
	openMessages(t, m, "orders")
	if s := screen(m); !strings.Contains(s, `\x00\xFF`) {
		t.Fatalf("screen:\n%s", s)
	}
	pressAll(t, m, "K")
	if s := screen(m); !strings.Contains(s, "auto: hex") || !strings.Contains(s, "00 ff") {
		t.Fatalf("detail:\n%s", s)
	}
}

func TestMessagesExportJSONL(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 3)
	tt := openMessages(t, m, "orders")
	dir := t.TempDir()
	m.settings.ExportDir = dir
	pressAll(t, m, "space", "x", "l", "enter")
	got := exportedFile(t, filepath.Join(dir, "orders-*.jsonl"))
	var want []string
	for _, r := range tt.msgs.recs {
		want = append(want, string(kmsgx.JSON(r)))
	}
	if got != strings.Join(want, "\n")+"\n" {
		t.Fatalf("file =\n%s", got)
	}
}

func TestMessagesCloseClosesReader(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 3)
	openMessages(t, m, "orders")
	r := k.lastReader()
	pressAll(t, m, ":")
	typeText(t, m, "q")
	pressAll(t, m, "enter")
	if len(m.tabs) != 0 || !r.isClosed() {
		t.Fatalf("tabs %d, reader closed %v", len(m.tabs), r.isClosed())
	}
}

func TestRenderValueBase64(t *testing.T) {
	lines, used := renderValue("\x00\xff", fmtBase64)
	if used != fmtBase64 || strings.Join(lines, "") != "AP8=" {
		t.Fatalf("lines %q used %v", lines, used)
	}
	if fmtBase64.String() != "base64" {
		t.Fatal("name")
	}
}

func TestMessagesFilterPrompt(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 10)
	tt := openMessages(t, m, "orders")
	pressAll(t, m, "F")
	typeText(t, m, "key = k-5")
	pressAll(t, m, "enter")
	if n := tt.msgGrid.RowCount(); n != 1 || cursorRec(t, tt).Offset != 5 {
		t.Fatalf("%d rows", n)
	}
	if !strings.Contains(screen(m), "filter: key = k-5 · 1 of 10") {
		t.Fatalf("screen:\n%s", screen(m))
	}
	pressAll(t, m, "F")
	typeText(t, m, " ==")
	pressAll(t, m, "enter")
	if _, ok := m.overlay.(*overlay.Prompt); !ok {
		t.Fatal("a bad filter keeps the prompt open")
	}
	pressAll(t, m, "esc", "f", "x")
	if n := tt.msgGrid.RowCount(); n != 10 {
		t.Fatalf("cleared: %d rows", n)
	}
}

func TestMessagesCellFilter(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 10)
	tt := openMessages(t, m, "orders")
	tt.msgGrid.SelectColumn("KEY")
	pressAll(t, m, "f", "f")
	if got := tt.msgs.filter.String(); got != "key = k-9" {
		t.Fatalf("filter = %q", got)
	}
	tt.msgGrid.SelectColumn("PART")
	pressAll(t, m, "f", "!")
	if got := tt.msgs.filter.String(); got != "key = k-9 and partition != 0" || tt.msgGrid.RowCount() != 0 {
		t.Fatalf("filter = %q, %d rows", got, tt.msgGrid.RowCount())
	}
}

func TestMessagesFilterBadColumn(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 10)
	tt := openMessages(t, m, "orders")
	tt.msgGrid.SelectColumn("TIME")
	pressAll(t, m, "f", "f")
	if !strings.Contains(tt.status, "can't filter on this column") || !tt.msgs.filter.IsZero() {
		t.Fatalf("status %q", tt.status)
	}
}

func TestMessagesSearchForward(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 10_000)
	r := fakeRecord("orders", 0, 5300)
	r.Key = []byte("special")
	k.setRecord(r)
	tt := openMessages(t, m, "orders")
	seek(t, m, "earliest")
	pressAll(t, m, "F")
	typeText(t, m, "key = special")
	pressAll(t, m, "enter")
	if tt.msgGrid.RowCount() != 0 {
		t.Fatal("no match loaded yet")
	}
	before := len(k.lastReader().rangeList())
	pressAll(t, m, "n")
	if c := cursorRec(t, tt); c.Offset != 5300 {
		t.Fatalf("cursor on %d", c.Offset)
	}
	if n := len(k.lastReader().rangeList()) - before; n < 10 {
		t.Fatalf("%d reads", n)
	}
	if len(tt.msgs.recs) > 200+3+1 {
		t.Fatalf("the scan keeps only matches and their context: %d kept", len(tt.msgs.recs))
	}
	pressAll(t, m, "f", "x")
	row, _ := tt.msgGrid.Position()
	if row == 0 || tt.msgs.recs[tt.msgs.shown[row-1]].Offset != 5299 {
		t.Fatalf("3 messages of context before the match (row %d)", row)
	}
}

func TestMessagesSearchCap(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 120_000)
	tt := openMessages(t, m, "orders")
	seek(t, m, "earliest")
	pressAll(t, m, "F")
	typeText(t, m, "key = nothing")
	pressAll(t, m, "enter", "n")
	if !strings.Contains(tt.status, "no match in the next 100,000 messages") {
		t.Fatalf("status = %q", tt.status)
	}
	if last := k.lastReader().rangeList(); last[len(last)-1][0].To > 100_400 {
		t.Fatalf("read past the cap: %v", last[len(last)-1])
	}
}

func TestMessagesSearchCancel(t *testing.T) {
	m, _, k := newKafkaModel(t, config.Connection{})
	k.addTopic("orders", 1, 0, 5000)
	tt := openMessages(t, m, "orders")
	seek(t, m, "earliest")
	pressAll(t, m, "F")
	typeText(t, m, "key = nothing")
	pressAll(t, m, "enter")
	_, cmd := m.Update(uitestKey("n"))
	if !strings.Contains(tt.status, "searching") {
		t.Fatalf("status = %q", tt.status)
	}
	pressAll(t, m, "ctrl+c")
	if !strings.Contains(tt.status, "search stopped") {
		t.Fatalf("status = %q", tt.status)
	}
	n := len(k.lastReader().rangeList())
	drainAll(t, m, cmd)
	if got := len(k.lastReader().rangeList()); got > n+1 {
		t.Fatalf("the search went on: %d reads after the stop", got-n)
	}
}

func uitestKey(k string) tea.Msg { return uitest.Key(k) }
