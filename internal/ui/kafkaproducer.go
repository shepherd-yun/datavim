package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/kmsgx"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/editor"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
	"github.com/shepherd-yun/sqlvim/internal/ui/vimedit"
)

func producerTabKey(conn, topic string) string   { return "kprod\x00" + conn + "\x00" + topic }
func producerDraftKey(conn, topic string) string { return "kafka:" + conn + "/" + topic }

// producerTemplate is a new draft: one message to fill in.
func producerTemplate(topic string) string { return "# topic: " + topic + "\nkey: \n\n{}" }

// openProducer opens, or switches to, the producer console of topic.
func (m *Model) openProducer(conn, topic string) tea.Cmd {
	return m.showTab(producerTabKey(conn, topic), func(id int) tab {
		t := newProducerTab(id, m.backend, m.store, conn, topic, m.settings, m.timings)
		t.keys = m.keys
		return t
	})
}

// kafkaContextTopic is the topic <space>c opens a producer console for:
// the active topic tab's, or the selected topic node's.
func (m *Model) kafkaContextTopic() (conn, topic string) {
	if m.focus == focusTabs {
		switch t := m.activeTab().(type) {
		case *topicTab:
			return t.conn, t.topic
		case *producerTab:
			return t.conn, t.topic
		}
		return "", ""
	}
	if n := m.tree.Selected(); n != nil && n.Kind == tree.KindTopic {
		return n.Conn, n.Key
	}
	return "", ""
}

type (
	// producedMsg is the outcome of a send.
	producedMsg struct {
		tabID, gen int
		conn       string
		recs       []kmsgx.DraftRecord
		results    []kafka.ProduceResult
		err        error
	}
	// produceConfirmMsg sends the messages waiting for a confirmation.
	produceConfirmMsg struct{ tabID, gen, stage int }
)

func (m producedMsg) TabID() int               { return m.tabID }
func (m producedMsg) kafkaMsg()                {}
func (m producedMsg) failure() (string, error) { return m.conn, m.err }
func (m produceConfirmMsg) TabID() int         { return m.tabID }

// The confirmation stages of a send.
const (
	stageJSON    = iota // a JSON-looking body that is not JSON
	stageConfirm        // ConfirmFor(OpProduce)
	stageSend
)

// producerTab is a topic's producer console: a draft of messages
// (kmsgx.ParseDraft) and the result of each send.
type producerTab struct {
	id        int
	keys      *keymap.Keymap
	backend   Backend
	store     Store
	conn      string
	topic     string
	timings   timings
	editorCmd []string
	draftGen  int
	clipPaste *vimedit.ClipboardPaste

	editor       *editor.Model
	grid         *grid.Model
	onResults    bool
	showMessages bool
	messages     []string

	pending []kmsgx.DraftRecord // checked, waiting for a confirmation
	gen     int
	running bool
	sent    int // messages sent by this console, for the result numbers

	status    string
	statusErr bool

	width, height, editorH, resultsH int
	full                             bool
	fitOnResults                     bool
}

func newProducerTab(id int, b Backend, st Store, conn, topic string, s config.Settings, tm timings) *producerTab {
	t := &producerTab{
		id: id, backend: b, store: st, conn: conn, topic: topic, timings: tm, editorCmd: s.EditorCommand(os.Getenv),
		editor: editor.New(), grid: grid.New(nil, s.MaxColumnWidth), keys: keymap.Default(),
	}
	t.editor.EnterInsert()
	return t
}

func (t *producerTab) ID() int                          { return t.id }
func (t *producerTab) UseRegisters(r vimedit.Registers) { t.editor.UseRegisters(r) }
func (t *producerTab) Key() string                      { return producerTabKey(t.conn, t.topic) }
func (t *producerTab) Title() string                    { return "produce:" + t.topic }
func (t *producerTab) Target() (string, db.TableRef)    { return t.conn, db.TableRef{} }
func (t *producerTab) CapturesInput() bool              { return !t.onResults && t.editor.Insert() }
func (t *producerTab) activeGrid() *grid.Model          { return t.grid }
func (t *producerTab) setStatus(s string, isErr bool)   { t.status, t.statusErr = s, isErr }
func (t *producerTab) retryAfterPassword() tea.Cmd      { return nil }

func (t *producerTab) Context() keymap.Context {
	if t.onResults {
		return keymap.KafkaProduceResults
	}
	return keymap.Console
}

func (t *producerTab) Mode() string {
	if t.onResults {
		return "NORMAL"
	}
	return t.editor.Mode().String()
}

func (t *producerTab) Find(q string, forward bool) bool {
	found := t.grid.Find(q, forward)
	if found {
		t.onResults = true
	}
	return found
}

func (t *producerTab) exportSource() exportSrc {
	return exportSrc{rs: t.grid.Result(), name: "produce-" + t.topic, conn: t.conn}
}

func (t *producerTab) SetSize(w, h int) {
	t.width, t.height = max(w, 0), max(h, 0)
	t.fit()
}

func (t *producerTab) SetFull(full bool) {
	if full != t.full || full && t.fitOnResults != t.onResults {
		t.full = full
		t.fit()
	}
}

func (t *producerTab) fit() {
	t.editorH, t.resultsH = splitHeights(t.height, t.full, t.onResults)
	t.fitOnResults = t.onResults
	t.editor.SetSize(t.width, t.editorH)
	t.grid.SetSize(t.width, t.resultsH)
}

func (t *producerTab) log(line string) {
	t.messages = append(t.messages, line)
	if over := len(t.messages) - maxMessages; over > 0 {
		t.messages = t.messages[over:]
	}
}

func (t *producerTab) Init() tea.Cmd {
	id, st, key := t.id, t.store, producerDraftKey(t.conn, t.topic)
	return func() tea.Msg {
		text, err := st.Draft(key)
		if err != nil {
			slog.Warn("load draft", "err", err)
		}
		return draftLoadedMsg{tabID: id, text: text}
	}
}

func (t *producerTab) shutdown() func() { return t.saveDraft() }

func (t *producerTab) scheduleDraft() tea.Cmd {
	t.draftGen++
	id, gen := t.id, t.draftGen
	return tea.Tick(t.timings.draft, func(time.Time) tea.Msg { return draftTickMsg{tabID: id, gen: gen} })
}

func (t *producerTab) saveDraft() func() {
	st, key, text := t.store, producerDraftKey(t.conn, t.topic), t.editor.Value()
	return func() {
		if err := st.SaveDraft(key, text); err != nil {
			slog.Warn("save draft", "err", err)
		}
	}
}

// ---- input ----

func (t *producerTab) Input(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		case "ctrl+enter", "ctrl+j":
			return t.runAtCursor()
		}
	}
	before := t.editor.Version()
	cmd := editorEffects(t.id, t.editor.Update(msg), &t.clipPaste)
	if t.editor.Version() == before {
		return cmd
	}
	return tea.Batch(cmd, t.scheduleDraft())
}

func (t *producerTab) Paste(msg tea.PasteMsg) tea.Cmd {
	if t.onResults {
		return nil
	}
	before := t.editor.Version()
	t.editor.Update(msg)
	if t.editor.Version() == before {
		return nil
	}
	return t.scheduleDraft()
}

func (t *producerTab) TakeKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if t.onResults {
		return nil, false
	}
	if msg.String() == t.keys.Leader() && !t.editor.Insert() && t.editor.Pending() == "" {
		return nil, false
	}
	before := t.editor.Version()
	res := t.editor.Update(msg)
	if !res.Handled {
		return nil, false
	}
	cmd := editorEffects(t.id, res, &t.clipPaste)
	if t.editor.Version() != before {
		cmd = tea.Batch(cmd, t.scheduleDraft())
	}
	return cmd, true
}

func (t *producerTab) Handle(a keymap.Action) tea.Cmd {
	switch a {
	case keymap.FocusResults:
		t.onResults = true
		return nil
	case keymap.FocusEditor:
		t.onResults = false
		return nil
	case keymap.ToggleMessages:
		t.showMessages = !t.showMessages
		return nil
	}
	if t.onResults {
		cmd, _ := gridAction(t.grid, a)
		return cmd
	}
	switch a {
	case keymap.ExternalEdit:
		return writeTempEdit(t.id, t.editor.Value(), "sqlvim-*.kafka")
	case keymap.RunStatement:
		return t.runAtCursor()
	case keymap.RunAll:
		return t.send(kmsgx.ParseDraft(t.editor.Value()))
	}
	return nil
}

// runAtCursor sends the message under the cursor, or the messages the
// selection covers.
func (t *producerTab) runAtCursor() tea.Cmd {
	text := t.editor.Value()
	if from, to, ok := t.editor.Selection(); ok {
		t.editor.Escape()
		first := strings.Count(text[:from], "\n") + 1
		last := strings.Count(text[:to], "\n") + 1
		var recs []kmsgx.DraftRecord
		for line := first; line <= last; line++ {
			r, err := kmsgx.DraftAt(text, line)
			if err != nil {
				if _, isDraftErr := err.(kmsgx.DraftError); isDraftErr {
					return t.send(nil, err)
				}
				continue // a separator or comment line
			}
			if len(recs) == 0 || recs[len(recs)-1].Line != r.Line {
				recs = append(recs, r)
			}
		}
		return t.send(recs, nil)
	}
	line, _ := t.editor.Cursor()
	r, err := kmsgx.DraftAt(text, line+1)
	if err != nil {
		return t.send(nil, err)
	}
	return t.send([]kmsgx.DraftRecord{r}, nil)
}

// send checks recs and asks what has to be asked before sending them.
func (t *producerTab) send(recs []kmsgx.DraftRecord, err error) tea.Cmd {
	if t.running {
		t.setStatus("a send is already running", true)
		return nil
	}
	if err != nil {
		if de, ok := err.(kmsgx.DraftError); ok {
			t.onResults = false
			t.editor.SetCursor(de.Line-1, 0)
		}
		t.setStatus(err.Error(), true)
		return nil
	}
	if len(recs) == 0 {
		t.setStatus("nothing to send", true)
		return nil
	}
	if cfg, _ := t.backend.Connection(t.conn); cfg.IsReadOnly() {
		t.setStatus(readOnlyHint, true)
		return nil
	}
	t.gen++
	t.pending = recs
	return t.advance(stageJSON)
}

// advance runs the confirmation stages from stage on.
func (t *producerTab) advance(stage int) tea.Cmd {
	recs, id, gen := t.pending, t.id, t.gen
	if stage <= stageJSON {
		for _, r := range recs {
			v := strings.TrimSpace(string(r.Value))
			if !r.ValueNull && (strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[")) && !json.Valid([]byte(v)) {
				return openOverlay(overlay.NewConfirm("Not JSON", []string{
					fmt.Sprintf("message at line %d is not valid JSON · send as text?", r.Line),
				}, "", produceConfirmMsg{tabID: id, gen: gen, stage: stageConfirm}))
			}
		}
	}
	if stage <= stageConfirm {
		cfg, _ := t.backend.Connection(t.conn)
		if kmsgx.ConfirmFor(kmsgx.OpProduce, cfg.Env == config.EnvProd) != kmsgx.ConfirmNone {
			return openOverlay(overlay.NewConfirm("Send messages", []string{
				fmt.Sprintf("Send %s to %s on %s?", plural(len(recs), "message"), t.topic, t.conn),
			}, "", produceConfirmMsg{tabID: id, gen: gen, stage: stageSend}))
		}
	}
	return t.produce()
}

func (t *producerTab) produce() tea.Cmd {
	recs := t.pending
	t.pending, t.running = nil, true
	t.setStatus("sending "+plural(len(recs), "message")+"…", false)
	id, gen, b, conn, topic := t.id, t.gen, t.backend, t.conn, t.topic
	out := make([]kafka.ProduceRecord, len(recs))
	for i, r := range recs {
		out[i] = kafka.ProduceRecord{Key: r.Key, Value: r.Value, KeyNull: r.KeyNull, ValueNull: r.ValueNull, Partition: r.Partition, Headers: r.Headers}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		msg := producedMsg{tabID: id, gen: gen, conn: conn, recs: recs}
		c, err := b.Kafka(ctx, conn)
		if err == nil {
			msg.results, err = c.Produce(ctx, topic, out)
		}
		msg.err = err
		return msg
	}
}

func (t *producerTab) produced(m producedMsg) {
	t.running = false
	rows := make([][]string, len(m.recs))
	failed := 0
	for i, r := range m.recs {
		t.sent++
		res := ""
		switch {
		case m.err != nil:
			res = "✗ " + kafkaErr(m.err)
		case i >= len(m.results):
			res = "✗ no answer"
		case m.results[i].Err != nil:
			res = "✗ " + kafkaErr(m.results[i].Err)
		default:
			pr := m.results[i]
			res = fmt.Sprintf("→ p%d · offset %s · %s", pr.Partition, groupDigits(pr.Offset), pr.Time.In(time.Local).Format("15:04:05.000"))
		}
		if strings.HasPrefix(res, "✗") {
			failed++
		}
		line := "#" + strconv.Itoa(t.sent) + " line " + strconv.Itoa(r.Line) + " " + res
		rows[i] = []string{line}
		t.log(line)
	}
	t.grid.SetResult(textResult([]string{"RESULT"}, rows))
	if failed > 0 {
		t.setStatus(fmt.Sprintf("%d of %d not sent", failed, len(m.recs)), true)
	} else {
		t.setStatus("sent "+plural(len(m.recs), "message")+" to "+t.topic, false)
	}
}

func (t *producerTab) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case produceConfirmMsg:
		if m.gen == t.gen && t.pending != nil {
			return t.advance(m.stage)
		}
	case producedMsg:
		if m.gen == t.gen {
			t.produced(m)
		}
	case draftLoadedMsg:
		switch cur := t.editor.Value(); {
		case m.text == "" && cur == "":
			t.editor.SetValue(producerTemplate(t.topic))
		case m.text == "" || m.text == cur:
		case cur == "":
			t.editor.SetValue(m.text)
		default:
			t.editor.SetValue(m.text + "\n" + cur)
		}
	case draftTickMsg:
		if m.gen == t.draftGen {
			save := t.saveDraft()
			return func() tea.Msg { save(); return nil }
		}
	case clipboardTextMsg:
		req := t.clipPaste
		t.clipPaste = nil
		if req != nil && m.text != "" && t.editor.PasteClipboard(*req, m.text) {
			return t.scheduleDraft()
		}
	case editFileMsg:
		if m.err != nil {
			t.setStatus("cannot create temp file: "+m.err.Error(), true)
			return nil
		}
		argv := append(slices.Clone(t.editorCmd), m.path)
		id, path := t.id, m.path
		return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { //nolint:gosec // the user's own editor
			return editDoneMsg{tabID: id, path: path, err: err}
		})
	case editDoneMsg:
		return readEditedFile(t.id, m)
	case editTextMsg:
		if m.err != nil {
			t.setStatus("editor: "+m.err.Error()+" · text unchanged", true)
			return nil
		}
		t.editor.ReplaceAll(strings.TrimRight(m.text, "\n"))
		return t.scheduleDraft()
	}
	return nil
}

// ---- view ----

func (t *producerTab) View(focused bool) string { return t.Framed("", focused, false) }

func (t *producerTab) Framed(title string, focused, danger bool) string {
	if t.height == 0 || t.width == 0 {
		return ""
	}
	onEditor, onResults := focused && !t.onResults, focused && t.onResults
	var results string
	if t.showMessages {
		start := max(len(t.messages)-t.resultsH, 0)
		lines := make([]string, 0, t.resultsH)
		for _, l := range t.messages[start:] {
			lines = append(lines, textutil.Fit(textutil.Sanitize(l), t.width))
		}
		results = textutil.PadLines(strings.Join(lines, "\n"), t.resultsH)
	} else {
		results = textutil.PadLines(t.grid.View(onResults), t.resultsH)
	}
	label := "Sent"
	if t.showMessages {
		label = "Messages"
	}
	t.editor.SetFocused(onEditor)
	switch {
	case t.full && t.onResults:
		return framedFull(title, label, results, t.statusLine(), t.width, t.height, onResults, danger)
	case t.full:
		return framedFull(title, "", t.editor.View(), t.statusLine(), t.width, t.height, onEditor, danger)
	}
	w := t.width + 2
	top := theme.Frame(title, t.editor.View(), w, t.editorH+2, onEditor, danger)
	bottom := theme.Frame(label, results+"\n"+t.statusLine(), w, t.resultsH+3, onResults, danger)
	return top + "\n" + bottom
}

func (t *producerTab) statusLine() string {
	switch {
	case t.running:
		return theme.Muted.Render(textutil.Fit("sending…", t.width))
	case t.editor.Pending() != "" && !t.onResults:
		return theme.Muted.Render(textutil.Fit(textutil.Sanitize(t.editor.Pending()), t.width))
	case t.status != "" && t.statusErr:
		return theme.Error.Render(textutil.Fit("✗ "+textutil.Sanitize(t.status), t.width))
	case t.status != "":
		return theme.Muted.Render(textutil.Fit(textutil.Sanitize(t.status), t.width))
	}
	hint := joinHints(
		keyHint(t.keys, keymap.Console, keymap.RunStatement, "send"),
		keyHint(t.keys, keymap.Console, keymap.RunAll, "send all"),
		keyHint(t.keys, keymap.Console, keymap.FocusResults, "results"),
		keyHint(t.keys, keymap.Console, keymap.ExternalEdit, "$EDITOR"),
		"key: · partition: · header: k=v · blank line · body · === between messages",
	)
	if t.onResults {
		hint = joinHints(
			keyHint(t.keys, keymap.KafkaProduceResults, keymap.ToggleMessages, "messages"),
			keyHint(t.keys, keymap.KafkaProduceResults, keymap.FocusEditor, "editor"),
		)
	}
	return theme.Muted.Render(textutil.Fit(hint, t.width))
}
