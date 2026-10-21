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
	contextRows   = 3       // messages shown before a jump's or a search's target
	previewWidth  = 200     // cells of a key / value preview
)

// msgView is the Messages section of a topic tab: the messages it keeps,
// in display order (by offset when it shows one partition, else by time).
type msgView struct {
	grid    *grid.Model
	recs    []kafka.Record
	bounds  kmsgx.Bounds
	read    map[int32]int64 // the highest offset read of each partition
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
	// uncommitted reads read_uncommitted: aborted and open transactions'
	// messages too. Off (read_committed) in every new view.
	uncommitted bool
	// decoded holds the Schema Registry decodes of wire-format messages.
	decoded map[recID]decodedRec
	// masker hides what config.toml's [[mask]] rules name (nil: nothing);
	// masked are the messages it changed, pending their wire-format parts
	// (key, value) waiting for a decode.
	masker  *kmsgx.Masker
	masked  map[recID]bool
	pending map[recID][2]bool

	filter    kmsgx.Filter
	shown     []int      // the indexes of the recs that pass the filter, one per grid row
	search    *msgSearch // the running search past what is kept
	searchGen int        // bumped when a search starts or stops

	tail  *liveTail // live tail; nil when off
	limit int       // messages kept, msgLimit but in tests
}

// readerConfig is what the view's Reader is opened with.
func (v *msgView) readerConfig() kafka.ReaderConfig {
	return kafka.ReaderConfig{MaxValueBytes: maxValueBytes, ReadUncommitted: v.uncommitted}
}

// current is the message under the cursor.
func (v *msgView) current() (kafka.Record, bool) {
	if i := v.recIndex(); i >= 0 {
		return v.recs[i], true
	}
	return kafka.Record{}, false
}

// recIndex is the index in recs of the message under the cursor, or -1.
func (v *msgView) recIndex() int {
	r, _ := v.grid.Position()
	if r < 0 || r >= len(v.shown) {
		return -1
	}
	return v.shown[r]
}

// refilter recomputes which recs the grid shows.
func (v *msgView) refilter() {
	v.shown = v.shown[:0]
	for i, r := range v.recs {
		if v.filter.Match(v.decodedRecord(r)) {
			v.shown = append(v.shown, i)
		}
	}
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
	ranges     map[int32]kafka.Range
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
	tabCtx, id, gen, b, conn, topic, reader, rc := t.ctx, t.id, v.gen, t.backend, t.conn, t.topic, v.reader, v.readerConfig()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
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
				if reader, err = b.KafkaReader(ctx, conn, rc); err != nil {
					return err
				}
				msg.reader = reader
			}
			msg.ranges = ranges
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

// noteRead records that ranges were read.
func (v *msgView) noteRead(ranges map[int32]kafka.Range) {
	for p, rg := range ranges {
		if o, ok := v.read[p]; !ok || rg.To-1 > o {
			v.read[p] = rg.To - 1
		}
	}
}

// edges are the lowest (low) or highest offset kept of each partition;
// partitions with none kept count from their end (low) or from what was
// read of them (high).
func (v *msgView) edges(low bool) map[int32]int64 {
	out := map[int32]int64{}
	for _, r := range v.recs {
		o, ok := out[r.Partition]
		switch {
		case !ok, low && r.Offset < o, !low && r.Offset > o:
			out[r.Partition] = r.Offset
		}
	}
	for p, b := range v.bounds {
		if _, ok := out[p]; ok || v.single || v.pos.Partitions != nil && !slices.Contains(v.pos.Partitions, p) {
			continue
		}
		if low {
			out[p] = b.To
		} else if o, ok := v.read[p]; ok {
			out[p] = o
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
		var resume tea.Cmd
		if v.tail != nil && v.tail.resume {
			v.tail.paused, v.tail.resume = false, false
			resume = t.startTail(v.edges(false))
		}
		return tea.Batch(resume, openOverlay(overlay.NewConfirm("Offset gone", []string{g.Error() + " · jump to earliest?"}, "",
			seekEarliestMsg{tabID: id, partition: g.Partition, earliest: g.Earliest})))
	}
	v.bounds = m.bounds
	if m.dir == 0 {
		v.tail = nil
		v.decoded, v.masked, v.pending = nil, nil, nil
		v.pos, v.single, v.loaded = m.pos, singlePartition(m.pos), true
		v.read = map[int32]int64{}
		for p, b := range m.bounds {
			if _, ok := m.ranges[p]; !ok {
				v.read[p] = b.To - 1 // nothing to read there: up to its end
			}
		}
	}
	v.noteRead(m.ranges)
	if m.dir != 0 && len(m.recs) == 0 {
		if m.dir < 0 {
			t.setStatus("beginning of partition", false)
		} else {
			t.setStatus("end of partition", false)
		}
		return nil
	}
	recs, cur, dropped := mergeRecords(v.recs, t.maskIn(m.recs, nil), m.dir, v.single, v.limit, v.recIndex())
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
	return t.decodeCmd(m.recs)
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

// renderMsgs shows the kept messages that pass the filter, with the
// cursor on recs[cursor] (or the next one shown).
func (t *topicTab) renderMsgs(cursor int) {
	v, g := t.msgs, t.msgs.grid
	_, col := g.Position()
	v.refilter()
	g.SetResult(t.msgsResult())
	if len(v.shown) > 0 {
		row, _ := slices.BinarySearch(v.shown, cursor)
		g.SetCursor(min(row, len(v.shown)-1), col)
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
	rows := make([][]string, len(v.shown))
	for i, ri := range v.shown {
		r := v.recs[ri]
		size := msgSize(r.Size)
		if r.Truncated {
			size += "+"
		}
		if v.failed(r) {
			size = "!" + size // the error is in K's detail
		}
		row := []string{
			strconv.Itoa(int(r.Partition)), groupDigits(r.Offset), r.Time.In(time.Local).Format("01-02 15:04:05.000"),
			v.partPreview(r, true),
		}
		if v.headers {
			row = append(row, headersPreview(r.Headers))
		}
		rows[i] = append(row, size, v.partPreview(r, false))
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
	parts := []string{v.pos.Describe(v.bounds)}
	if v.uncommitted {
		parts = append(parts, "read_uncommitted")
	}
	parts = append(parts, plural(len(v.recs), "message"))
	switch {
	case v.tail == nil:
	case v.tail.paused:
		parts = append(parts, "live · paused")
	default:
		parts = append(parts, "live · +"+groupDigits(int64(v.tail.added))+" new")
	}
	if !v.filter.IsZero() {
		parts = append(parts, "filter: "+v.filter.String(), groupDigits(int64(len(v.shown)))+" of "+groupDigits(int64(len(v.recs))))
	}
	if v.format != fmtAuto {
		parts = append(parts, "format "+v.format.String())
	}
	if v.from != "" {
		parts = append(parts, "from group "+v.from)
	}
	if v.masker != nil {
		parts = append(parts, "masked")
	}
	return strings.Join(parts, " · ")
}

// handleMsgs is a key on the Messages section; ok is false for keys it
// leaves to the caller.
func (t *topicTab) handleMsgs(a keymap.Action) (tea.Cmd, bool) {
	v := t.msgs
	row, _ := v.grid.Position()
	last := len(v.shown) - 1
	switch a {
	case keymap.Down, keymap.HalfPageDown:
		if row >= last && len(v.shown) > 0 {
			return t.loadMore(1), true
		}
	case keymap.Up, keymap.HalfPageUp:
		if row == 0 && len(v.shown) > 0 {
			return t.loadMore(-1), true
		}
	case keymap.Seek:
		return t.seekPrompt(), true
	case keymap.DeleteRecords:
		return t.deleteFromMessage(), true
	case keymap.ToggleIsolation:
		return t.toggleIsolation(), true
	case keymap.ResendMessage, keymap.ResendShown:
		return t.resend(a == keymap.ResendShown), true
	case keymap.LiveTail:
		return t.toggleTail(), true
	case keymap.OpenCellKey:
		if v.from == "" {
			t.setStatus("this view didn't come from a group", true)
			return nil, true
		}
		return openGroupCmd(t.conn, v.from), true
	case keymap.ToggleHeaders:
		v.headers = !v.headers
		t.renderMsgs(v.recIndex())
		return nil, true
	case keymap.FilterPrompt:
		return t.filterPrompt(), true
	case keymap.FilterEqual, keymap.FilterNotEqual, keymap.FilterContains:
		return t.cellFilter(a), true
	case keymap.FilterClear:
		t.setFilter(kmsgx.Filter{})
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
		return t.jump(pos), true
	}
	return nil, false
}

// loadMore loads the batch before (dir -1) or after (+1) what is kept.
// Reading pauses live tail.
func (t *topicTab) loadMore(dir int) tea.Cmd {
	v := t.msgs
	if v.loading || !v.loaded {
		return nil
	}
	if v.tail != nil {
		if dir > 0 {
			return nil // live tail brings the newer ones
		}
		v.tail.paused = true
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

// jump replaces the view with the messages at pos; once it lands it ends
// live tail, which is paused meanwhile (a deleted offset may call it off).
func (t *topicTab) jump(pos kmsgx.Position) tea.Cmd {
	v := t.msgs
	v.gen++ // the poll out is dropped
	if v.tail != nil {
		v.tail.resume = v.tail.resume || !v.tail.paused
		v.tail.paused, v.tail.polling = true, false
	}
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
		return t.jump(kmsgx.Position{
			Kind: kmsgx.PosOffset, Partitions: []int32{m.partition},
			Offsets: map[int32]int64{m.partition: m.earliest},
		}), true
	case msgFilterMsg:
		t.setFilter(m.filter)
		return nil, true
	case tailStartedMsg:
		return t.tailStarted(m), true
	case tailPollMsg:
		return t.tailPolled(m), true
	case tailEstimateMsg:
		t.tailEstimated(m)
		return nil, true
	case searchStepMsg:
		return t.searchStepped(m), true
	case msgsDecodedMsg:
		return t.decodedUpdate(m), true
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
	cut := ""
	if r.Truncated {
		cut = " (the first 1 MB only)"
	}
	if a == keymap.YankRow {
		return string(kmsgx.JSON(r)), fmt.Sprintf("message p%d:%d", r.Partition, r.Offset) + cut, true
	}
	col, _ := t.msgs.grid.Column()
	switch col.Name {
	case "KEY":
		lines, _ := renderBytes(r.Key, r.KeyNull, t.msgs.format)
		return strings.Join(lines, "\n"), "key", true
	case "VALUE":
		if d := t.msgs.decodedOf(r, false); d != nil && d.Err == nil {
			return prettyJSON(d.JSON), "value" + cut, true
		}
		if t.msgs.format == fmtAuto && !r.ValueNull && utf8.Valid(r.Value) {
			return string(r.Value), "value" + cut, true
		}
		lines, _ := renderBytes(r.Value, r.ValueNull, t.msgs.format)
		return strings.Join(lines, "\n"), "value" + cut, true
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
	for _, i := range t.msgs.shown {
		r := t.msgs.recs[i]
		jsonl.Rows = append(jsonl.Rows, []db.Value{{Text: string(kmsgx.JSON(r))}})
	}
	return exportSrc{rs: t.msgs.grid.Result(), jsonl: jsonl, name: t.topic, conn: t.conn}
}

// msgDetail is K on a message: its key, headers and value, in a format v
// cycles through; n / N show the next / previous message.
type msgDetail struct {
	tab    *topicTab
	rec    kafka.Record // found again by partition and offset: loads move its row
	offset int
}

func newMsgDetail(t *topicTab, row int) *msgDetail {
	return &msgDetail{tab: t, rec: t.msgs.recs[t.msgs.shown[row]]}
}

// trigger reports whether key is the Messages section's own key for a,
// so the detail follows keymap.toml.
func (d *msgDetail) trigger(a keymap.Action, key string) bool {
	return d.tab.keys.Triggers(keymap.KafkaMessages, a, key)
}

func (d *msgDetail) HandleKey(msg tea.KeyPressMsg) (overlay.Overlay, tea.Cmd) {
	v := d.tab.msgs
	key := msg.String()
	switch {
	case key == "esc" || key == "q" || d.trigger(keymap.CellDetail, key):
		return nil, nil
	case d.trigger(keymap.ValueFormat, key):
		v.format = (v.format + 1) % (fmtBase64 + 1)
	case key == "j" || key == "down":
		d.offset++
	case key == "k" || key == "up":
		d.offset--
	case d.trigger(keymap.SearchNext, key) || d.trigger(keymap.SearchPrev, key):
		step := 1
		if d.trigger(keymap.SearchPrev, key) {
			step = -1
		}
		row := slices.IndexFunc(v.shown, func(i int) bool {
			return v.recs[i].Partition == d.rec.Partition && v.recs[i].Offset == d.rec.Offset
		})
		if r := row + step; row >= 0 && r >= 0 && r < len(v.shown) {
			d.rec, d.offset = v.recs[v.shown[r]], 0
			_, col := v.grid.Position()
			v.grid.SetCursor(r, col)
		}
	case key == "y":
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
	if d := v.decodedOf(r, true); d != nil && d.Err == nil {
		key = []string{string(d.JSON)}
	}
	lines = append(lines, "key      "+strings.Join(key, " "))
	if len(r.Headers) > 0 {
		lines = append(lines, fmt.Sprintf("headers  %s   (%d)", headersPreview(r.Headers), len(r.Headers)))
	}
	if r.Truncated {
		lines = append(lines, theme.Error.Render("showing first 1 MB of "+msgSize(r.Size)))
	}
	val, used := renderBytes(r.Value, r.ValueNull, v.format)
	head, dval, dused, decoded := v.decodedValueLines(r)
	if decoded {
		val, used = dval, dused
	}
	f := v.format.String()
	if v.format == fmtAuto {
		f += ": " + used.String()
	}
	lines = append(lines, theme.Muted.Render(strings.Repeat("─", max(width-14, 10))+" "+f))
	if decoded {
		lines = append(lines, head)
	}
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
	k := d.tab.keys
	own := func(a keymap.Action, label string) string {
		if key := k.OwnKey(keymap.KafkaMessages, a); key != "" {
			return key + " " + label
		}
		return ""
	}
	title := joinHints("message", own(keymap.ValueFormat, "format"), own(keymap.SearchNext, "next"), own(keymap.SearchPrev, "prev"),
		"y copy value", "esc close")
	return theme.Box(title, strings.Join(body, "\n"), w, h, true)
}

// msgFilterMsg is a filter typed at the F prompt.
type msgFilterMsg struct {
	tabID  int
	filter kmsgx.Filter
}

func (m msgFilterMsg) TabID() int { return m.tabID }

// filterPrompt asks for a filter expression.
func (t *topicTab) filterPrompt() tea.Cmd {
	id := t.id
	parse := func(text string) (kmsgx.Filter, error) { return kmsgx.ParseFilter(text, time.Now()) }
	return openOverlay(overlay.NewPrompt("Filter messages", "key = x · value ~ y · header.h = v · value.a.b = 1", t.msgs.filter.String(),
		func(text string) tea.Msg {
			f, _ := parse(text) // validated
			return msgFilterMsg{tabID: id, filter: f}
		},
		overlay.WithValidate(func(text string) error { _, err := parse(text); return err })))
}

// cellFilter is ff / f! / fs: a condition on the cell under the cursor,
// joined to the filter.
func (t *topicTab) cellFilter(a keymap.Action) tea.Cmd {
	r, ok := t.msgs.current()
	if !ok {
		return nil
	}
	col, _ := t.msgs.grid.Column()
	var value string
	switch col.Name {
	case "KEY":
		value = string(r.Key)
	case "VALUE":
		value = string(r.Value)
	case "PART":
		value = strconv.Itoa(int(r.Partition))
	case "OFFSET":
		value = strconv.FormatInt(r.Offset, 10)
	default:
		t.setStatus("can't filter on this column", true)
		return nil
	}
	op := map[keymap.Action]string{keymap.FilterEqual: "=", keymap.FilterNotEqual: "!=", keymap.FilterContains: "~"}[a]
	f, err := kmsgx.ParseFilter(kmsgx.CellFilter(t.msgs.filter.String(), col.Name, op, value), time.Now())
	if err != nil {
		t.setStatus(err.Error(), true)
		return nil
	}
	t.setFilter(f)
	return nil
}

// setFilter shows only the kept messages f matches.
func (t *topicTab) setFilter(f kmsgx.Filter) {
	v := t.msgs
	at := v.recIndex()
	v.filter = f
	t.renderMsgs(max(at, 0))
	if !f.IsZero() {
		t.setStatus("filter: "+f.String()+" · "+groupDigits(int64(len(v.shown)))+" of "+groupDigits(int64(len(v.recs))), false)
	}
}

// Search limits (design §5.6): a search stops at the first match, or
// after searchCap messages.
const searchCap = 100_000

// msgSearch is n / N looking past the kept messages for the next match.
type msgSearch struct {
	dir     int
	scanned int
}

// searchStepMsg is one batch a search read.
type searchStepMsg struct {
	tabID, gen int
	conn       string
	ranges     map[int32]kafka.Range
	recs       []kafka.Record
	dec        map[recID]decodedRec // the recs decoded, for the filter
	err        error
}

func (m searchStepMsg) TabID() int { return m.tabID }
func (m searchStepMsg) kafkaMsg()  {}

// searchFilter is n / N while a filter is set: the next match kept, else
// a search through the next messages; ok is false without a filter.
func (t *topicTab) searchFilter(forward bool) (tea.Cmd, bool) {
	v := t.msgs
	if t.section != secMessages || v.filter.IsZero() {
		return nil, false
	}
	if v.search != nil {
		return nil, true
	}
	row, col := v.grid.Position()
	switch {
	case forward && row+1 < len(v.shown):
		v.grid.SetCursor(row+1, col)
		return nil, true
	case !forward && len(v.shown) > 0 && row > 0:
		v.grid.SetCursor(row-1, col)
		return nil, true
	case v.reader == nil || !v.loaded:
		return nil, true
	}
	dir := 1
	if !forward {
		dir = -1
	}
	if v.tail != nil {
		v.tail.paused = true // reading ends the reader's tail; T goes on from what is kept
	}
	v.searchGen++
	v.search = &msgSearch{dir: dir}
	t.setStatus("searching… scanned 0", false)
	return t.searchStep(v.edges(dir < 0)), true
}

// searchStep reads the batch past edges in the search's direction.
func (t *topicTab) searchStep(edges map[int32]int64) tea.Cmd {
	v := t.msgs
	tabCtx, id, gen, b, conn, topic, reader, dir := t.ctx, t.id, v.searchGen, t.backend, t.conn, t.topic, v.reader, v.search.dir
	decode := t.hasRegistry()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := searchStepMsg{tabID: id, gen: gen, conn: conn}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			d, err := c.TopicDetail(ctx, topic)
			if err != nil {
				return err
			}
			if dir > 0 {
				msg.ranges = kmsgx.Newer(edges, boundsOf(d), msgBatch)
			} else {
				msg.ranges = kmsgx.Older(edges, boundsOf(d), msgBatch)
			}
			if len(msg.ranges) == 0 {
				return nil
			}
			if msg.recs, err = reader.Range(ctx, topic, msg.ranges); err != nil || !decode {
				return err
			}
			var wire []kafka.Record
			for _, r := range msg.recs {
				if inWire(r.Key, r.KeyNull) || inWire(r.Value, r.ValueNull) {
					wire = append(wire, r)
				}
			}
			msg.dec, err = decodeRecs(ctx, b, conn, wire)
			return err
		})
		return msg
	}
}

func (t *topicTab) searchStepped(m searchStepMsg) tea.Cmd {
	v := t.msgs
	s := v.search
	if s == nil || m.gen != v.searchGen {
		return nil
	}
	stop := func(status string, isErr bool) {
		v.search = nil
		v.searchGen++
		t.setStatus(status, isErr)
	}
	if m.err != nil {
		stop("search: "+kafkaErr(m.err), true)
		return nil
	}
	if len(m.ranges) == 0 {
		if s.dir > 0 {
			stop("no match before the end", false)
		} else {
			stop("no match after the beginning", false)
		}
		return nil
	}
	if v.decoded == nil && len(m.dec) > 0 {
		v.decoded = map[recID]decodedRec{}
	}
	recs := t.maskIn(m.recs, m.dec) // masks m.dec too
	for id, d := range m.dec {
		v.decoded[id] = d
	}
	match := func(r kafka.Record) bool { return v.filter.Match(v.decodedRecord(r)) }
	recs = slices.Clone(recs)
	slices.SortStableFunc(recs, compareRecs(v.single))
	found := -1
	if s.dir > 0 {
		found = slices.IndexFunc(recs, match)
	} else {
		for i := len(recs) - 1; i >= 0 && found < 0; i-- {
			if match(recs[i]) {
				found = i
			}
		}
	}
	if found >= 0 {
		keep := slices.Clone(recs[max(found-contextRows, 0):found])
		for _, r := range recs {
			if match(r) {
				keep = append(keep, r)
			}
		}
		target := recs[found]
		merged, _, dropped := mergeRecords(v.recs, keep, s.dir, v.single, v.limit, v.recIndex())
		v.recs = merged
		at := slices.IndexFunc(merged, func(r kafka.Record) bool { return r.Partition == target.Partition && r.Offset == target.Offset })
		t.renderMsgs(max(at, 0))
		stop(fmt.Sprintf("found p%d:%s after %s messages", target.Partition, groupDigits(target.Offset), groupDigits(int64(s.scanned+found+1))), false)
		if dropped > 0 {
			t.setStatus(t.status+" · dropped "+groupDigits(int64(dropped))+" messages far from the cursor", false)
		}
		return nil
	}
	s.scanned += rangeSize(m.ranges)
	if s.scanned >= searchCap {
		if s.dir > 0 {
			stop("no match in the next "+groupDigits(searchCap)+" messages", false)
		} else {
			stop("no match in the previous "+groupDigits(searchCap)+" messages", false)
		}
		return nil
	}
	t.setStatus("searching… scanned "+groupDigits(int64(s.scanned)), false)
	next := map[int32]int64{}
	for p, rg := range m.ranges {
		if s.dir > 0 {
			next[p] = rg.To - 1
		} else {
			next[p] = rg.From
		}
	}
	return t.searchStep(next)
}

// rangeSize is how many offsets ranges cover.
func rangeSize(ranges map[int32]kafka.Range) int {
	n := 0
	for _, r := range ranges {
		n += int(r.To - r.From)
	}
	return n
}

// cancelRun is ctrl+c: it stops a search.
func (t *topicTab) cancelRun() tea.Cmd {
	if t.msgs.search != nil {
		t.msgs.search = nil
		t.msgs.searchGen++
		t.setStatus("search stopped", false)
		return nil
	}
	t.setStatus("nothing is running", false)
	return nil
}

// Live tail (design §5.4): each Poll waits up to tailPollWait for at most
// tailPollMax messages.
const (
	tailPollWait = time.Second
	tailPollMax  = 500
)

// liveTail is the state of live tail on a message view.
type liveTail struct {
	paused  bool
	resume  bool // paused by a jump: goes on if the jump does not happen
	hidden  bool // the tab is not on screen: no polls
	polling bool // a Poll is out
	added   int  // messages it brought
}

// tailStartedMsg is the reader following the partitions from their end
// (or from what is kept, after a pause).
type tailStartedMsg struct {
	tabID, gen int
	conn       string
	reader     kafka.Reader
	bounds     kmsgx.Bounds
	err        error
}

func (m tailStartedMsg) TabID() int               { return m.tabID }
func (m tailStartedMsg) kafkaMsg()                {}
func (m tailStartedMsg) failure() (string, error) { return m.conn, m.err }

// tailPollMsg is what one Poll returned.
type tailPollMsg struct {
	tabID, gen int
	recs       []kafka.Record
	err        error
}

func (m tailPollMsg) TabID() int { return m.tabID }

// tailEstimateMsg is the partitions' ends when the tab comes back on
// screen.
type tailEstimateMsg struct {
	tabID, gen int
	bounds     kmsgx.Bounds
}

func (m tailEstimateMsg) TabID() int { return m.tabID }

// toggleTail is T: live tail on, paused, on again.
// toggleIsolation switches between read_committed and read_uncommitted:
// the reader (and live tail) stops, and a new one reads the same place.
func (t *topicTab) toggleIsolation() tea.Cmd {
	v := t.msgs
	v.uncommitted = !v.uncommitted
	level := "read_committed"
	if v.uncommitted {
		level = "read_uncommitted"
	}
	t.setStatus("isolation "+level, false)
	r := v.reader
	v.reader, v.tail = nil, nil
	v.gen++
	load := t.loadMsgs(0, v.pos, nil)
	if r == nil {
		return load
	}
	return tea.Batch(func() tea.Msg { _ = r.Close(); return nil }, load)
}

func (t *topicTab) toggleTail() tea.Cmd {
	v := t.msgs
	switch {
	case !v.loaded:
		return nil
	case v.tail == nil:
		v.tail = &liveTail{}
		t.setStatus("live", false)
		return t.startTail(nil)
	case !v.tail.paused:
		v.tail.paused = true
		t.setStatus("live · paused", false)
		return nil
	}
	v.tail.paused = false
	t.setStatus("live", false)
	return t.startTail(v.edges(false))
}

// startTail follows the view's partitions from after kept (each partition's
// highest offset kept), or from their end when kept is nil.
func (t *topicTab) startTail(kept map[int32]int64) tea.Cmd {
	v := t.msgs
	tabCtx, id, gen, b, conn, topic, reader, parts, rc := t.ctx, t.id, v.gen, t.backend, t.conn, t.topic, v.reader, v.pos.Partitions, v.readerConfig()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := tailStartedMsg{tabID: id, gen: gen, conn: conn}
		msg.err = withKafka(ctx, b, conn, func(c kafka.Client) error {
			d, err := c.TopicDetail(ctx, topic)
			if err != nil {
				return err
			}
			msg.bounds = boundsOf(d)
			from := map[int32]int64{}
			for p, bd := range msg.bounds {
				if parts != nil && !slices.Contains(parts, p) {
					continue
				}
				from[p] = bd.To
				if o, ok := kept[p]; ok {
					from[p] = min(o+1, bd.To)
				}
			}
			if reader == nil {
				if reader, err = b.KafkaReader(ctx, conn, rc); err != nil {
					return err
				}
				msg.reader = reader
			}
			return reader.Tail(ctx, topic, from)
		})
		return msg
	}
}

func (t *topicTab) tailStarted(m tailStartedMsg) tea.Cmd {
	v := t.msgs
	if m.reader != nil {
		if v.reader == nil {
			v.reader = m.reader
		} else if m.reader != v.reader {
			_ = m.reader.Close()
		}
	}
	if m.gen != v.gen || v.tail == nil {
		return nil
	}
	if m.err != nil {
		v.tail = nil
		t.setStatus("live tail: "+kafkaErr(m.err), true)
		return nil
	}
	v.bounds = m.bounds
	return t.poll()
}

// poll asks the reader for what arrived, unless paused, hidden or asking
// already.
func (t *topicTab) poll() tea.Cmd {
	v := t.msgs
	if v.tail == nil || v.tail.paused || v.tail.hidden || v.tail.polling || v.reader == nil {
		return nil
	}
	v.tail.polling = true
	tabCtx, id, gen, reader := t.ctx, t.id, v.gen, v.reader
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, tailPollWait)
		defer cancel()
		recs, err := reader.Poll(ctx, tailPollMax)
		return tailPollMsg{tabID: id, gen: gen, recs: recs, err: err}
	}
}

func (t *topicTab) tailPolled(m tailPollMsg) tea.Cmd {
	v := t.msgs
	if m.gen != v.gen || v.tail == nil {
		return nil
	}
	v.tail.polling = false
	if m.err != nil && v.tail.paused {
		return nil // a read made while paused ended the reader's tail; T starts it again
	}
	if m.err != nil {
		v.tail = nil
		t.setStatus("live tail: "+kafkaErr(m.err), true)
		return nil
	}
	if len(m.recs) > 0 {
		row, _ := v.grid.Position()
		follow := len(v.shown) == 0 || row >= len(v.shown)-1
		recs, cur, _ := mergeRecords(v.recs, t.maskIn(m.recs, nil), 1, v.single, v.limit, v.recIndex())
		v.recs = recs
		v.tail.added += len(m.recs)
		if follow {
			cur = len(recs) - 1
		}
		t.renderMsgs(cur)
		t.setStatus("live · +"+groupDigits(int64(v.tail.added))+" new", false)
		return tea.Batch(t.poll(), t.decodeCmd(m.recs))
	}
	return t.poll()
}

// Shown goes on with live tail, saying about how many messages arrived
// while the tab was hidden.
func (t *topicTab) Shown() tea.Cmd {
	v := t.msgs
	if v.tail == nil || !v.tail.hidden {
		return nil
	}
	v.tail.hidden = false
	tabCtx, id, gen, b, conn, topic := t.ctx, t.id, v.gen, t.backend, t.conn, t.topic
	estimate := func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := tailEstimateMsg{tabID: id, gen: gen}
		_ = withKafka(ctx, b, conn, func(c kafka.Client) error {
			d, err := c.TopicDetail(ctx, topic)
			msg.bounds = boundsOf(d)
			return err
		})
		return msg
	}
	return tea.Batch(estimate, t.poll())
}

// Hidden stops the polls of live tail.
func (t *topicTab) Hidden() {
	if v := t.msgs; v.tail != nil {
		v.tail.hidden = true
	}
}

func (t *topicTab) tailEstimated(m tailEstimateMsg) {
	v := t.msgs
	if m.gen != v.gen || v.tail == nil || m.bounds == nil {
		return
	}
	var missed int64
	for p, high := range v.edges(false) {
		if bd, ok := m.bounds[p]; ok {
			missed += max(bd.To-high-1, 0)
		}
	}
	if missed > 0 {
		t.setStatus("≈"+groupDigits(missed)+" new while hidden", false)
	}
}
