package ui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Message view limits (design §5.2).
const (
	msgBatch      = 200     // per partition, per load
	msgLimit      = 10_000  // messages one view keeps
	maxValueBytes = 1 << 20 // longer values keep their first 1 MB
	previewWidth  = 200     // cells of a key / value preview
)

// msgView is the Messages section of a topic tab: the messages it keeps,
// in display order (by offset when it shows one partition, else by time).
type msgView struct {
	grid    *grid.Model
	recs    []kafka.Record
	bounds  kmsgx.Bounds
	pos     kmsgx.Position
	single  bool
	gen     int // bumped by every jump; late results of an older one are dropped
	reader  kafka.Reader
	loading bool
	loaded  bool
	err     string
	headers bool        // the HEADERS column is shown
	format  valueFormat // how K and yl show keys and values
	from    string      // the group this view was opened from
}

// current is the message under the cursor.
func (v *msgView) current() (kafka.Record, bool) {
	r, _ := v.grid.Position()
	if r < 0 || r >= len(v.recs) {
		return kafka.Record{}, false
	}
	return v.recs[r], true
}

// compareRecs orders messages by offset (single) or by time.
func compareRecs(single bool) func(a, b kafka.Record) int {
	return func(a, b kafka.Record) int {
		if single {
			return cmp.Or(cmp.Compare(a.Partition, b.Partition), cmp.Compare(a.Offset, b.Offset))
		}
		return cmp.Or(a.Time.Compare(b.Time), cmp.Compare(a.Partition, b.Partition), cmp.Compare(a.Offset, b.Offset))
	}
}

// mergeRecords merges add into old: dir 0 replaces, -1 adds older, +1
// newer messages. Over limit, the end away from where the load went is
// dropped (the end farther from the cursor when replacing). The cursor
// stays on its message.
func mergeRecords(old, add []kafka.Record, dir int, single bool, limit, cursor int) (recs []kafka.Record, newCursor, dropped int) {
	type id struct {
		p int32
		o int64
	}
	var at *id
	if dir != 0 && cursor >= 0 && cursor < len(old) {
		at = &id{old[cursor].Partition, old[cursor].Offset}
	}
	if dir == 0 {
		recs = slices.Clone(add)
	} else {
		seen := make(map[id]bool, len(old))
		recs = slices.Clone(old)
		for _, r := range old {
			seen[id{r.Partition, r.Offset}] = true
		}
		for _, r := range add {
			if !seen[id{r.Partition, r.Offset}] {
				recs = append(recs, r)
			}
		}
	}
	slices.SortStableFunc(recs, compareRecs(single))
	newCursor = 0
	if at != nil {
		newCursor = max(slices.IndexFunc(recs, func(r kafka.Record) bool { return r.Partition == at.p && r.Offset == at.o }), 0)
	}
	if over := len(recs) - limit; over > 0 {
		dropTail := dir < 0 || dir == 0 && newCursor <= len(recs)/2
		if dropTail {
			recs = recs[:limit]
		} else {
			recs = recs[over:]
			newCursor -= over
		}
		dropped = over
		newCursor = textutil.Clamp(newCursor, 0, len(recs)-1)
	}
	return slices.Clip(recs), newCursor, dropped
}

// msgsLoadedMsg is a load of messages: dir 0 replaces the view, -1 adds
// older ones, +1 newer ones.
type msgsLoadedMsg struct {
	tabID, gen int
	conn       string
	dir        int
	pos        kmsgx.Position
	recs       []kafka.Record
	bounds     kmsgx.Bounds
	reader     kafka.Reader // opened by this load
	gone       *kmsgx.ErrOffsetGone
	at         time.Time // PosTime: put the cursor on the first message at or after it
	err        error
}

func (m msgsLoadedMsg) TabID() int               { return m.tabID }
func (m msgsLoadedMsg) kafkaMsg()                {}
func (m msgsLoadedMsg) failure() (string, error) { return m.conn, m.err }

// seekMsg is a position typed at the jump prompt.
type seekMsg struct {
	tabID int
	text  string
}

func (m seekMsg) TabID() int { return m.tabID }

// seekEarliestMsg answers yes to jumping to the start of a partition
// whose asked offset retention deleted.
type seekEarliestMsg struct {
	tabID     int
	partition int32
	earliest  int64
}

func (m seekEarliestMsg) TabID() int { return m.tabID }

// msgEditDoneMsg is $EDITOR closing on a value written to path.
type msgEditDoneMsg struct {
	tabID int
	path  string
	err   error
}

func (m msgEditDoneMsg) TabID() int { return m.tabID }

// boundsOf reads each partition's start and end from a topic's detail.
func boundsOf(d kafka.TopicDetail) kmsgx.Bounds {
	b := kmsgx.Bounds{}
	for _, p := range d.Parts {
		b[p.ID] = kmsgx.Range{From: p.Start, To: p.End}
	}
	return b
}

// singlePartition reports whether pos shows one partition only.
func singlePartition(pos kmsgx.Position) bool { return len(pos.Partitions) == 1 }

// loadMsgs reads messages: dir 0 at pos, -1 / +1 the batch before / after
// edges (each partition's lowest / highest offset kept). The topic's
// bounds are read again first.
func (t *topicTab) loadMsgs(dir int, pos kmsgx.Position, edges map[int32]int64) tea.Cmd {
	v := t.msgs
	v.loading = true
	id, gen, b, conn, topic, reader := t.id, v.gen, t.backend, t.conn, t.topic, v.reader
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := msgsLoadedMsg{tabID: id, gen: gen, conn: conn, dir: dir, pos: pos}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			d, err := c.TopicDetail(ctx, topic)
			if err != nil {
				return err
			}
			msg.bounds = boundsOf(d)
			var ranges map[int32]kafka.Range
			switch dir {
			case -1:
				ranges = kmsgx.Older(edges, msg.bounds, msgBatch)
			case 1:
				ranges = kmsgx.Newer(edges, msg.bounds, msgBatch)
			default:
				var byTime map[int32]int64
				if pos.Kind == kmsgx.PosTime {
					if byTime, err = c.OffsetsAt(ctx, topic, pos.Time); err != nil {
						return err
					}
					msg.at = pos.Time
				}
				ranges, err = pos.Ranges(msg.bounds, msgBatch, byTime)
				var gone kmsgx.ErrOffsetGone
				if errors.As(err, &gone) {
					msg.gone = &gone
					return nil
				}
				if err != nil {
					return err
				}
			}
			if len(ranges) == 0 {
				return nil
			}
			if reader == nil {
				if reader, err = b.KafkaReader(ctx, conn, kafka.ReaderConfig{MaxValueBytes: maxValueBytes}); err != nil {
					return err
				}
				msg.reader = reader
			}
			msg.recs, err = reader.Range(ctx, topic, ranges)
			return err
		})
		if dir == 0 && pos.Kind == kmsgx.PosLatest && !singlePartition(pos) && len(msg.recs) > msgBatch {
			slices.SortStableFunc(msg.recs, compareRecs(false))
			msg.recs = msg.recs[len(msg.recs)-msgBatch:]
		}
		return msg
	}
}

// edges are the lowest (low) or highest offset kept of each partition;
// partitions with none kept count from their end.
func (v *msgView) edges(low bool) map[int32]int64 {
	out := map[int32]int64{}
	for _, r := range v.recs {
		o, ok := out[r.Partition]
		switch {
		case !ok, low && r.Offset < o, !low && r.Offset > o:
			out[r.Partition] = r.Offset
		}
	}
	if low && !v.single {
		for p, b := range v.bounds {
			if _, ok := out[p]; !ok && (v.pos.Partitions == nil || slices.Contains(v.pos.Partitions, p)) {
				out[p] = b.To
			}
		}
	}
	return out
}

func (t *topicTab) msgsLoaded(m msgsLoadedMsg) tea.Cmd {
	v := t.msgs
	if m.reader != nil {
		if v.reader == nil {
			v.reader = m.reader
		} else if m.reader != v.reader {
			_ = m.reader.Close()
		}
	}
	if m.gen != v.gen {
		return nil
	}
	v.loading = false
	if m.err != nil {
		v.err = kafkaErr(m.err)
		return nil
	}
	v.err = ""
	if m.gone != nil {
		g := *m.gone
		id := t.id
		return openOverlay(overlay.NewConfirm("Offset gone", []string{g.Error() + " · jump to earliest?"}, "",
			seekEarliestMsg{tabID: id, partition: g.Partition, earliest: g.Earliest}))
	}
	v.bounds = m.bounds
	if m.dir == 0 {
		v.pos, v.single, v.loaded = m.pos, singlePartition(m.pos), true
	}
	if m.dir != 0 && len(m.recs) == 0 {
		if m.dir < 0 {
			t.setStatus("beginning of partition", false)
		} else {
			t.setStatus("end of partition", false)
		}
		return nil
	}
	cursor, _ := v.grid.Position()
	recs, cur, dropped := mergeRecords(v.recs, m.recs, m.dir, v.single, msgLimit, cursor)
	v.recs = recs
	if m.dir == 0 {
		cur = t.jumpCursor(m)
	}
	t.renderMsgs(cur)
	if dropped > 0 {
		t.setStatus("dropped "+groupDigits(int64(dropped))+" messages far from the cursor", false)
	}
	if slices.ContainsFunc(m.recs, func(r kafka.Record) bool { return r.Truncated }) && t.status == "" {
		t.setStatus("some values were cut to their first 1 MB", false)
	}
	return nil
}

// jumpCursor is the row a jump lands on: the asked offset, the first
// message at or after the asked time, the newest for latest, else the
// first.
func (t *topicTab) jumpCursor(m msgsLoadedMsg) int {
	recs := t.msgs.recs
	switch m.pos.Kind {
	case kmsgx.PosLatest:
		return max(len(recs)-1, 0)
	case kmsgx.PosOffset:
		for _, p := range m.pos.Partitions {
			want := m.pos.Offsets[p]
			if i := slices.IndexFunc(recs, func(r kafka.Record) bool { return r.Partition == p && r.Offset >= want }); i >= 0 {
				return i
			}
		}
		return max(len(recs)-1, 0)
	case kmsgx.PosTime:
		if i := slices.IndexFunc(recs, func(r kafka.Record) bool { return !r.Time.Before(m.at) }); i >= 0 {
			return i
		}
		return max(len(recs)-1, 0)
	}
	return 0
}

// renderMsgs shows the kept messages with the cursor on row cursor.
func (t *topicTab) renderMsgs(cursor int) {
	g := t.msgs.grid
	_, col := g.Position()
	g.SetResult(t.msgsResult())
	if len(t.msgs.recs) > 0 {
		g.SetCursor(cursor, col)
	}
}

// msgColumns are the message table's columns.
func (v *msgView) msgColumns() []string {
	if v.headers {
		return []string{"PART", "OFFSET", "TIME", "KEY", "HEADERS", "SIZE", "VALUE"}
	}
	return []string{"PART", "OFFSET", "TIME", "KEY", "SIZE", "VALUE"}
}

func (t *topicTab) msgsResult() *db.ResultSet {
	v := t.msgs
	cols := v.msgColumns()
	rows := make([][]string, len(v.recs))
	for i, r := range v.recs {
		size := msgSize(r.Size)
		if r.Truncated {
			size += "+"
		}
		row := []string{
			strconv.Itoa(int(r.Partition)), groupDigits(r.Offset), r.Time.In(time.Local).Format("01-02 15:04:05.000"),
			kmsgx.Preview(r.Key, r.KeyNull, previewWidth),
		}
		if v.headers {
			row = append(row, headersPreview(r.Headers))
		}
		rows[i] = append(row, size, kmsgx.Preview(r.Value, r.ValueNull, previewWidth))
	}
	return textResult(cols, rows)
}

// headersPreview is "k=v k=v" with escaped values.
func headersPreview(hs []kafka.Header) string {
	parts := make([]string, len(hs))
	for i, h := range hs {
		parts[i] = kmsgx.Preview([]byte(h.Key), false, 40) + "=" + kmsgx.Preview(h.Value, false, 60)
	}
	return strings.Join(parts, " ")
}

// msgSize is 412B, 1.2K, 2.1M.
func msgSize(n int) string {
	switch {
	case n < 1024:
		return strconv.Itoa(n) + "B"
	case n < 1<<20:
		return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + "K"
	}
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + "M"
}

// msgsHeader is "latest · all partitions · 200 messages · format auto".
func (t *topicTab) msgsHeader() string {
	v := t.msgs
	parts := []string{v.pos.Describe(v.bounds), plural(len(v.recs), "message")}
	if v.format != fmtAuto {
		parts = append(parts, "format "+v.format.String())
	}
	if v.from != "" {
		parts = append(parts, "from group "+v.from)
	}
	return strings.Join(parts, " · ")
}

// handleMsgs is a key on the Messages section; ok is false for keys it
// leaves to the caller.
func (t *topicTab) handleMsgs(a keymap.Action) (tea.Cmd, bool) {
	v := t.msgs
	row, _ := v.grid.Position()
	last := len(v.recs) - 1
	switch a {
	case keymap.Down, keymap.HalfPageDown:
		if row >= last && len(v.recs) > 0 {
			return t.loadMore(1), true
		}
	case keymap.Up, keymap.HalfPageUp:
		if row == 0 && len(v.recs) > 0 {
			return t.loadMore(-1), true
		}
	case keymap.Seek:
		return t.seekPrompt(), true
	case keymap.ToggleHeaders:
		v.headers = !v.headers
		t.renderMsgs(row)
		return nil, true
	case keymap.ValueFormat:
		v.format = (v.format + 1) % (fmtBase64 + 1)
		t.setStatus("format "+v.format.String(), false)
		return nil, true
	case keymap.CellDetail:
		if _, ok := v.current(); ok {
			return openOverlay(newMsgDetail(t, row)), true
		}
		return nil, true
	case keymap.ExternalEdit:
		return t.openValueInEditor(), true
	case keymap.Refresh:
		if v.loading {
			return nil, true
		}
		// one partition: read again around the message under the cursor
		pos := v.pos
		if r, ok := v.current(); ok && v.single && pos.Kind != kmsgx.PosLatest {
			pos = kmsgx.Position{Kind: kmsgx.PosOffset, Partitions: []int32{r.Partition}, Offsets: map[int32]int64{r.Partition: r.Offset}}
		}
		v.gen++
		return t.loadMsgs(0, pos, nil), true
	}
	return nil, false
}

// loadMore loads the batch before (dir -1) or after (+1) what is kept.
func (t *topicTab) loadMore(dir int) tea.Cmd {
	v := t.msgs
	if v.loading || !v.loaded {
		return nil
	}
	if dir < 0 {
		t.setStatus("loading older messages…", false)
	} else {
		t.setStatus("loading newer messages…", false)
	}
	return t.loadMsgs(dir, v.pos, v.edges(dir < 0))
}

// seekPrompt asks where to jump, previewing the position as it is typed.
func (t *topicTab) seekPrompt() tea.Cmd {
	v := t.msgs
	current := int32(-1)
	if r, ok := v.current(); ok {
		current = r.Partition
	}
	bounds, id := maps.Clone(v.bounds), t.id
	check := func(text string) (kmsgx.Position, error) {
		pos, err := kmsgx.ParsePosition(text, time.Now(), current)
		if err != nil {
			return pos, err
		}
		_, err = pos.Ranges(bounds, msgBatch, nil)
		var gone kmsgx.ErrOffsetGone
		if errors.As(err, &gone) {
			err = nil // asked about once submitted
		}
		return pos, err
	}
	return openOverlay(overlay.NewPrompt("Jump to", "offset · p1:123 · @09:12 · -500 · earliest", "",
		func(text string) tea.Msg { return seekMsg{tabID: id, text: text} },
		overlay.WithValidate(func(text string) error { _, err := check(text); return err }),
		overlay.WithHint(func(text string) (string, bool) {
			pos, err := check(text)
			if err != nil {
				return err.Error(), true
			}
			return pos.Describe(bounds), false
		})))
}

// seek jumps to a typed position.
func (t *topicTab) seek(text string) tea.Cmd {
	current := int32(-1)
	if r, ok := t.msgs.current(); ok {
		current = r.Partition
	}
	pos, err := kmsgx.ParsePosition(text, time.Now(), current)
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	return t.jump(pos)
}

// jump replaces the view with the messages at pos.
func (t *topicTab) jump(pos kmsgx.Position) tea.Cmd {
	t.msgs.gen++
	return t.loadMsgs(0, pos, nil)
}

// msgsUpdate handles the Messages section's messages.
func (t *topicTab) msgsUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case msgsLoadedMsg:
		return t.msgsLoaded(m), true
	case seekMsg:
		return t.seek(m.text), true
	case seekEarliestMsg:
		return t.jump(kmsgx.Position{Kind: kmsgx.PosOffset, Partitions: []int32{m.partition},
			Offsets: map[int32]int64{m.partition: m.earliest}}), true
	case msgEditDoneMsg:
		_ = os.Remove(m.path)
		if m.err != nil {
			t.setStatus("editor: "+m.err.Error(), true)
		}
		return nil, true
	}
	return nil, false
}

// openValueInEditor writes the value under the cursor to a temp file and
// opens it in $EDITOR; nothing is read back.
func (t *topicTab) openValueInEditor() tea.Cmd {
	r, ok := t.msgs.current()
	if !ok {
		return nil
	}
	ext := ".txt"
	if isJSON(string(r.Value)) {
		ext = ".json"
	}
	f, err := os.CreateTemp("", fmt.Sprintf("sqlvim-%s-p%d-%d-*%s", safeFileName(t.topic), r.Partition, r.Offset, ext))
	if err != nil {
		t.setStatus("cannot create temp file: "+err.Error(), true)
		return nil
	}
	body := r.Value
	if isJSON(string(body)) {
		body = []byte(textutil.PrettyJSON(string(body)))
	}
	_, err = f.Write(body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		t.setStatus("cannot write temp file: "+err.Error(), true)
		return nil
	}
	argv := append(slices.Clone(t.settings.EditorCommand(os.Getenv)), f.Name())
	id, path := t.id, f.Name()
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { //nolint:gosec // the user's own editor
		return msgEditDoneMsg{tabID: id, path: path, err: err}
	})
}

// safeFileName keeps letters, digits, . _ - of s.
func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, s)
}

// renderBytes is b in format f (auto picks), as lines, and the format used.
func renderBytes(b []byte, null bool, f valueFormat) ([]string, valueFormat) {
	if null {
		return []string{"(null)"}, f
	}
	return renderValue(string(b), f)
}

// yankText is what yy / yl copy on the Messages section: the message as
// JSON, or the full key, value or headers under the cursor.
func (t *topicTab) yankText(a keymap.Action) (text, what string, ok bool) {
	if t.section != secMessages {
		return "", "", false
	}
	r, ok := t.msgs.current()
	if !ok {
		return "", "", false
	}
	if a == keymap.YankRow {
		return string(kmsgx.JSON(r)), fmt.Sprintf("message p%d:%d", r.Partition, r.Offset), true
	}
	col, _ := t.msgs.grid.Column()
	switch col.Name {
	case "KEY":
		lines, _ := renderBytes(r.Key, r.KeyNull, t.msgs.format)
		return strings.Join(lines, "\n"), "key", true
	case "VALUE":
		if t.msgs.format == fmtAuto && !r.ValueNull && utf8.Valid(r.Value) {
			return string(r.Value), "value", true
		}
		lines, _ := renderBytes(r.Value, r.ValueNull, t.msgs.format)
		return strings.Join(lines, "\n"), "value", true
	case "HEADERS":
		return headersJSON(r), "headers", true
	}
	return "", "", false
}

// headersJSON is the headers array of kmsgx.JSON(r).
func headersJSON(r kafka.Record) string {
	full := string(kmsgx.JSON(kafka.Record{Headers: r.Headers, KeyNull: true, ValueNull: true}))
	i := strings.Index(full, `"headers":`)
	j := strings.LastIndex(full, `,"value"`)
	if i < 0 || j < i {
		return "[]"
	}
	return full[i+len(`"headers":`) : j]
}

// msgsExport is the kept messages for export: the table, and one JSON
// object per message for JSON Lines.
func (t *topicTab) msgsExport() exportSrc {
	jsonl := &db.ResultSet{Columns: []db.ColumnInfo{{Name: "message", DatabaseType: "JSON"}}}
	for _, r := range t.msgs.recs {
		jsonl.Rows = append(jsonl.Rows, []db.Value{{Text: string(kmsgx.JSON(r))}})
	}
	return exportSrc{rs: t.msgs.grid.Result(), jsonl: jsonl, name: t.topic, conn: t.conn}
}

// msgDetail is K on a message: its key, headers and value, in a format v
// cycles through; n / N show the next / previous message.
type msgDetail struct {
	tab    *topicTab
	row    int
	rec    kafka.Record
	offset int
}

func newMsgDetail(t *topicTab, row int) *msgDetail {
	return &msgDetail{tab: t, row: row, rec: t.msgs.recs[row]}
}

func (d *msgDetail) HandleKey(msg tea.KeyPressMsg) (overlay.Overlay, tea.Cmd) {
	v := d.tab.msgs
	switch msg.String() {
	case "esc", "q", "K":
		return nil, nil
	case "v":
		v.format = (v.format + 1) % (fmtBase64 + 1)
	case "j", "down":
		d.offset++
	case "k", "up":
		d.offset--
	case "n", "N":
		step := 1
		if msg.String() == "N" {
			step = -1
		}
		if r := d.row + step; r >= 0 && r < len(v.recs) {
			d.row, d.rec, d.offset = r, v.recs[r], 0
			_, col := v.grid.Position()
			v.grid.SetCursor(r, col)
		}
	case "y":
		lines, _ := renderBytes(d.rec.Value, d.rec.ValueNull, v.format)
		note := ""
		if d.rec.Truncated {
			note = " (the first 1 MB only)"
		}
		return d, func() tea.Msg { return overlay.CopyMsg{Text: strings.Join(lines, "\n"), What: "value" + note} }
	}
	return d, nil
}

func (d *msgDetail) lines(width int) []string {
	r, v := d.rec, d.tab.msgs
	lines := []string{
		fmt.Sprintf("%s · p%d · offset %s   ts %s (%s)   size %s", d.tab.topic, r.Partition, groupDigits(r.Offset),
			r.Time.In(time.Local).Format("2006-01-02 15:04:05.000"), r.TimeType, msgSize(r.Size)),
	}
	key, _ := renderBytes(r.Key, r.KeyNull, fmtAuto)
	lines = append(lines, "key      "+strings.Join(key, " "))
	if len(r.Headers) > 0 {
		lines = append(lines, fmt.Sprintf("headers  %s   (%d)", headersPreview(r.Headers), len(r.Headers)))
	}
	if r.Truncated {
		lines = append(lines, theme.Error.Render("showing first 1 MB of "+msgSize(r.Size)))
	}
	val, used := renderBytes(r.Value, r.ValueNull, v.format)
	f := v.format.String()
	if v.format == fmtAuto {
		f += ": " + used.String()
	}
	lines = append(lines, theme.Muted.Render(strings.Repeat("─", max(width-14, 10))+" "+f))
	for _, l := range val {
		lines = append(lines, textutil.Wrap(textutil.Sanitize(l), width)...)
	}
	return lines
}

func (d *msgDetail) View(width, height int) string {
	w := max(min(width-4, 120), 20)
	lines := d.lines(w - 2)
	h := max(min(height-2, len(lines)+2), 3)
	d.offset = textutil.Clamp(d.offset, 0, max(len(lines)-(h-2), 0))
	end := min(d.offset+h-2, len(lines))
	body := make([]string, 0, end-d.offset)
	for _, l := range lines[d.offset:end] {
		body = append(body, textutil.Fit(l, w-2))
	}
	return theme.Box("message · v format · n/N next/prev · y copy value · esc close", strings.Join(body, "\n"), w, h, true)
}
