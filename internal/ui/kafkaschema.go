package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/ui/highlight"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/grid"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// schemaSection is one part of a Schema tab.
type schemaSection int

const (
	schSchema schemaSection = iota
	schVersions
	schDiff
	schemaSections
)

var schemaSectionNames = [schemaSections]string{"Schema", "Versions", "Diff"}

func schemaTabKey(conn, subject string) string { return "kschema\x00" + conn + "\x00" + subject }

// subjectLoadedMsg is a subject read again: every version, and its level.
type subjectLoadedMsg struct {
	tabID    int
	conn     string
	versions []kafka.Schema // by version
	compat   string
	global   bool
	err      error
}

func (m subjectLoadedMsg) TabID() int               { return m.tabID }
func (m subjectLoadedMsg) kafkaMsg()                {}
func (m subjectLoadedMsg) failure() (string, error) { return m.conn, m.err }

// schemaTab is a Schema Registry subject: one version's text, the list
// of versions and a diff between two of them.
type schemaTab struct {
	id       int
	backend  Backend
	keys     *keymap.Keymap
	settings config.Settings
	conn     string
	subject  string
	version  int // the version shown; -1 the latest
	section  schemaSection

	versions []kafka.Schema
	compat   string
	global   bool
	err      string
	loading  bool

	verGrid  *grid.Model
	mark     int // the version m marked, 0 none
	diff     []string
	diffName string
	scroll   [schemaSections]int // the first line shown of Schema and Diff

	status  string
	statusE bool

	width, height int

	ctx    context.Context
	cancel context.CancelFunc
}

func newSchemaTab(id int, b Backend, keys *keymap.Keymap, s config.Settings, conn, subject string, version int) *schemaTab {
	t := &schemaTab{id: id, backend: b, keys: keys, settings: s, conn: conn, subject: subject, version: version,
		verGrid: grid.New(nil, s.MaxColumnWidth)}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	return t
}

// openSchema opens (or shows) subject's tab at version (-1: the latest);
// an open tab moves to version.
func (m *Model) openSchema(conn, subject string, version int) tea.Cmd {
	key := schemaTabKey(conn, subject)
	if i := slices.IndexFunc(m.tabs, func(t tab) bool { return t.Key() == key }); i >= 0 {
		m.gotoTab(i)
		st := m.tabs[i].(*schemaTab)
		st.version, st.section = version, schSchema
		return st.load()
	}
	rec := history.Recent{Kind: history.RecentKafkaSubject, Conn: conn, Name: subject}
	return m.openTab(key, rec, func(id int) tab {
		return newSchemaTab(id, m.backend, m.keys, m.settings, conn, subject, version)
	})
}

func (t *schemaTab) ID() int                        { return t.id }
func (t *schemaTab) Title() string                  { return t.subject }
func (t *schemaTab) Key() string                    { return schemaTabKey(t.conn, t.subject) }
func (t *schemaTab) Context() keymap.Context        { return keymap.KafkaSchema }
func (t *schemaTab) Target() (string, db.TableRef)  { return t.conn, db.TableRef{} }
func (t *schemaTab) SetSize(w, h int)               { t.width, t.height = max(w, 0), max(h, 0) }
func (t *schemaTab) Init() tea.Cmd                  { return t.load() }
func (t *schemaTab) retryAfterPassword() tea.Cmd    { return t.load() }
func (t *schemaTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }
func (t *schemaTab) shutdown() func()               { t.cancel(); return func() {} }

func (t *schemaTab) Find(q string, forward bool) bool {
	if t.section == schVersions {
		return t.verGrid.Find(q, forward)
	}
	lines := t.textLines()
	q = strings.ToLower(q)
	n := len(lines)
	for i := 1; i <= n; i++ {
		j := (t.scroll[t.section] + i) % n
		if !forward {
			j = (t.scroll[t.section] - i + n*2) % n
		}
		if strings.Contains(strings.ToLower(ansi.Strip(lines[j])), q) {
			t.scroll[t.section] = j
			return true
		}
	}
	return false
}

func (t *schemaTab) load() tea.Cmd {
	t.loading = true
	tabCtx, id, b, conn, subject := t.ctx, t.id, t.backend, t.conn, t.subject
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := subjectLoadedMsg{tabID: id, conn: conn}
		reg, err := b.SchemaRegistry(ctx, conn)
		if err != nil {
			msg.err = err
			return msg
		}
		vs, err := reg.Versions(ctx, subject)
		if err != nil {
			msg.err = err
			return msg
		}
		for _, v := range vs {
			s, err := reg.Schema(ctx, subject, v)
			if err != nil {
				msg.err = err
				return msg
			}
			msg.versions = append(msg.versions, s)
		}
		msg.compat, msg.global, msg.err = reg.Compatibility(ctx, subject)
		return msg
	}
}

// current is the version shown, false before the first load.
func (t *schemaTab) current() (kafka.Schema, bool) {
	if len(t.versions) == 0 {
		return kafka.Schema{}, false
	}
	if t.version > 0 {
		if i := slices.IndexFunc(t.versions, func(s kafka.Schema) bool { return s.Version == t.version }); i >= 0 {
			return t.versions[i], true
		}
	}
	return t.versions[len(t.versions)-1], true
}

func (t *schemaTab) latest() kafka.Schema { return t.versions[len(t.versions)-1] }

// cursorVersion is the Versions row under the cursor.
func (t *schemaTab) cursorVersion() (kafka.Schema, bool) {
	r, _ := t.verGrid.Position()
	if r < 0 || r >= len(t.versions) {
		return kafka.Schema{}, false
	}
	return t.versions[r], true
}

func (t *schemaTab) Update(msg tea.Msg) tea.Cmd {
	if m, ok := msg.(subjectLoadedMsg); ok {
		t.loading = false
		if m.err != nil {
			t.err = kafkaErr(m.err)
			return nil
		}
		t.err = ""
		t.versions, t.compat, t.global = m.versions, m.compat, m.global
		t.verGrid.ReplaceResult(t.versionsResult())
		if cur, ok := t.current(); ok {
			t.verGrid.SetCursor(slices.IndexFunc(t.versions, func(s kafka.Schema) bool { return s.Version == cur.Version }), 0)
		}
		t.scroll[schSchema] = 0
	}
	return nil
}

func (t *schemaTab) versionsResult() *db.ResultSet {
	rows := make([][]string, len(t.versions))
	for i, s := range t.versions {
		v := "v" + strconv.Itoa(s.Version)
		if s.Version == t.mark {
			v += " ◆"
		}
		rows[i] = []string{v, strconv.Itoa(s.ID), s.Type}
	}
	return textResult([]string{"VERSION", "ID", "TYPE"}, rows)
}

func (t *schemaTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	switch a {
	case keymap.NextSection:
		t.section = min(t.section+1, schemaSections-1)
		return nil
	case keymap.PrevSection:
		t.section = max(t.section-1, 0)
		return nil
	case keymap.Refresh:
		if !t.loading {
			return t.load()
		}
		return nil
	case keymap.Open:
		if s, ok := t.cursorVersion(); ok && t.section == schVersions {
			t.version, t.section, t.scroll[schSchema] = s.Version, schSchema, 0
		}
		return nil
	case keymap.MarkCompare:
		if s, ok := t.cursorVersion(); ok && t.section == schVersions {
			t.mark = s.Version
			t.verGrid.ReplaceResult(t.versionsResult())
			t.setStatus(fmt.Sprintf("marked v%d · %s", s.Version, keyHint(t.keys, keymap.KafkaSchema, keymap.Compare, "diffs")), false)
		}
		return nil
	case keymap.Compare:
		t.compare()
		return nil
	}
	if t.section == schVersions {
		if cmd, ok := gridAction(t.verGrid, a); ok {
			return cmd
		}
		return nil
	}
	t.scrollText(a)
	return nil
}

// compare diffs the marked version (or, with none, the latest) with the
// cursor's (or the shown one outside Versions).
func (t *schemaTab) compare() {
	if len(t.versions) == 0 {
		return
	}
	b, ok := t.cursorVersion()
	if t.section != schVersions || !ok {
		b, _ = t.current()
	}
	a := t.latest()
	if t.mark > 0 {
		if i := slices.IndexFunc(t.versions, func(s kafka.Schema) bool { return s.Version == t.mark }); i >= 0 {
			a = t.versions[i]
		}
	}
	if a.Version > b.Version {
		a, b = b, a
	}
	t.diff = lineDiff(strings.Split(schemaText(a), "\n"), strings.Split(schemaText(b), "\n"))
	t.diffName = fmt.Sprintf("v%d → v%d", a.Version, b.Version)
	t.mark = 0
	t.verGrid.ReplaceResult(t.versionsResult())
	t.section, t.scroll[schDiff] = schDiff, 0
}

func (t *schemaTab) scrollText(a keymap.Action) {
	n := len(t.textLines())
	page := max(t.bodyHeight()/2, 1)
	s := &t.scroll[t.section]
	switch a {
	case keymap.Down:
		*s++
	case keymap.Up:
		*s--
	case keymap.HalfPageDown:
		*s += page
	case keymap.HalfPageUp:
		*s -= page
	case keymap.Top:
		*s = 0
	case keymap.Bottom:
		*s = n
	}
	*s = textutil.Clamp(*s, 0, max(n-t.bodyHeight(), 0))
}

// schemaText is s formatted: Avro and JSON Schema indented, Protobuf as written.
func schemaText(s kafka.Schema) string {
	if s.Type == "PROTOBUF" {
		return strings.TrimRight(s.Text, "\n")
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(s.Text), "", "  "); err != nil {
		return s.Text
	}
	return out.String()
}

// highlighted is s's text in colors.
func highlighted(s kafka.Schema) []string {
	src := schemaText(s)
	lang := "json"
	if s.Type == "PROTOBUF" {
		lang = "protobuf"
	}
	var b strings.Builder
	for _, sp := range highlight.Code(src, lang) {
		b.WriteString(theme.Syntax(sp.Class).Render(src[sp.Start:sp.End]))
	}
	return strings.Split(b.String(), "\n")
}

// lineDiff is a's and b's lines as a unified diff without hunks: " "
// kept, "-" only in a, "+" only in b.
func lineDiff(a, b []string) []string {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			out = append(out, "  "+a[i])
			i, j = i+1, j+1
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, "+ "+b[j])
			j++
		default:
			out = append(out, "- "+a[i])
			i++
		}
	}
	return out
}

// textLines are the lines of the Schema or Diff section.
func (t *schemaTab) textLines() []string {
	switch t.section {
	case schSchema:
		if s, ok := t.current(); ok {
			return highlighted(s)
		}
	case schDiff:
		if t.diff == nil {
			return []string{theme.Muted.Render(keyHint(t.keys, keymap.KafkaSchema, keymap.MarkCompare, "marks a version") +
				" · " + keyHint(t.keys, keymap.KafkaSchema, keymap.Compare, "diffs it with the cursor's"))}
		}
		if !slices.ContainsFunc(t.diff, func(l string) bool { return l[0] != ' ' }) {
			return []string{theme.Muted.Render(t.diffName + " · no differences")}
		}
		out := make([]string, len(t.diff))
		for i, l := range t.diff {
			switch l[0] {
			case '+':
				out[i] = theme.Inserted.Render(l)
			case '-':
				out[i] = theme.Deleted.UnsetStrikethrough().Render(l)
			default:
				out[i] = l
			}
		}
		return out
	}
	return nil
}

// header is "orders-value · v3 of 5 · id 12 · AVRO · compat BACKWARD (global)".
func (t *schemaTab) header() string {
	s, ok := t.current()
	if !ok {
		return t.subject
	}
	h := fmt.Sprintf("%s · v%d of %d · id %d · %s · compat %s", t.subject, s.Version, t.latest().Version, s.ID, s.Type, t.compat)
	if t.global {
		h += " (global)"
	}
	return h
}

func (t *schemaTab) refsLine() string {
	s, ok := t.current()
	if !ok || len(s.References) == 0 {
		return ""
	}
	parts := make([]string, len(s.References))
	for i, r := range s.References {
		parts[i] = fmt.Sprintf("%s → %s v%d", r.Name, r.Subject, r.Version)
	}
	return "refs: " + strings.Join(parts, ", ")
}

func (t *schemaTab) headLines() []string {
	names := make([]string, schemaSections)
	for i, n := range schemaSectionNames {
		if schemaSection(i) == t.section {
			names[i] = theme.Title.Render(n)
		} else {
			names[i] = theme.Muted.Render(n)
		}
	}
	lines := []string{strings.Join(names, theme.Muted.Render(" · "))}
	switch {
	case t.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.err)))
	case len(t.versions) == 0:
		lines = append(lines, theme.Muted.Render("loading…"))
	default:
		lines = append(lines, theme.Muted.Render(textutil.Sanitize(t.header())))
		if r := t.refsLine(); r != "" {
			lines = append(lines, theme.Muted.Render(textutil.Sanitize(r)))
		}
	}
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	return lines
}

func (t *schemaTab) bodyHeight() int { return max(t.height-len(t.headLines()), 1) }

func (t *schemaTab) View(focused bool) string {
	lines := t.headLines()
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if len(t.versions) == 0 {
		return strings.Join(lines, "\n")
	}
	h := max(t.height-len(lines), 0)
	if t.section == schVersions {
		t.verGrid.SetSize(t.width, h)
		return strings.Join(append(lines, t.verGrid.View(focused)), "\n")
	}
	body := t.textLines()
	start := min(t.scroll[t.section], max(len(body)-1, 0))
	for _, l := range body[start:min(start+h, len(body))] {
		lines = append(lines, textutil.Fit(l, t.width))
	}
	return strings.Join(lines, "\n")
}
