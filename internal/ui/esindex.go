package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/history"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// indexSection is one part of an Index tab.
type indexSection int

const (
	idxDocuments indexSection = iota
	idxMapping
	idxSettings
	idxAliases
	idxStats
	idxSections
)

var idxSectionNames = [idxSections]string{"Documents", "Mapping", "Settings", "Aliases", "Stats"}

func esIndexTabKey(conn, target string) string { return "esindex\x00" + conn + "\x00" + target }

// esMsg marks the async results of the elasticsearch tabs: a missing
// password asks for one and the tab tries again.
type esMsg interface {
	tabMsg
	esMsg()
}

// esHeadMsg is the _cat/indices rows of the tab's indices.
type esHeadMsg struct {
	tabID   int
	conn    string
	indices []es.IndexInfo
	err     error
}

func (m esHeadMsg) TabID() int               { return m.tabID }
func (m esHeadMsg) esMsg()                   {}
func (m esHeadMsg) failure() (string, error) { return m.conn, m.err }

// esMappingMsg is the mappings of the tab's indices.
type esMappingMsg struct {
	tabID  int
	conn   string
	raw    map[string]json.RawMessage
	fields map[string][]esx.Field
	err    error
}

func (m esMappingMsg) TabID() int               { return m.tabID }
func (m esMappingMsg) esMsg()                   {}
func (m esMappingMsg) failure() (string, error) { return m.conn, m.err }

// esSectionMsg is the Settings, Aliases or Stats section read.
type esSectionMsg struct {
	tabID    int
	conn     string
	section  indexSection
	settings []es.Setting
	aliases  []es.Alias
	stats    []es.IndexStats
	ilm      map[string]es.ILMState // with stats; nil when ILM can't be read
	err      error
}

func (m esSectionMsg) TabID() int               { return m.tabID }
func (m esSectionMsg) esMsg()                   {}
func (m esSectionMsg) failure() (string, error) { return m.conn, m.err }

// esPart is a section's load state.
type esPart struct {
	loaded, loading bool
	err             string
}

// esIndexTab is an index, alias, pattern or data stream: its documents,
// mapping, settings, aliases and stats.
type esIndexTab struct {
	id       int
	backend  Backend
	store    Store // the filter history
	keys     *keymap.Keymap
	settings config.Settings
	conn     string
	target   string
	kind     targetKind
	section  indexSection

	indices []es.IndexInfo // the tab's indices, from _cat/indices
	head    esPart

	rawMappings map[string]json.RawMessage
	mappings    map[string][]esx.Field // per index
	fields      []esx.Field            // merged
	mapping     esPart

	settingList []es.Setting
	setEdits    []settingEdit // staged in the Settings section, in order
	aliases     []es.Alias
	stats       []es.IndexStats
	ilm         map[string]es.ILMState // by index; nil: no ilm column
	parts       [idxSections]esPart

	docs    *esDocs
	editFor esEditFor   // what the $EDITOR session under way is for
	masker  *esx.Masker // the [[mask]] rules of the connection; nil: none

	mapGrid, setGrid, aliasGrid, statsGrid *grid.Model

	status  string
	statusE bool

	width, height int

	ctx    context.Context // every load's; shutdown cancels it
	cancel context.CancelFunc
}

func newESIndexTab(id int, b Backend, keys *keymap.Keymap, s config.Settings, conn, target string, kind targetKind) *esIndexTab {
	t := &esIndexTab{
		id: id, backend: b, keys: keys, settings: s, conn: conn, target: target, kind: kind,
		mapGrid: grid.New(nil, s.MaxColumnWidth), setGrid: grid.New(nil, s.MaxColumnWidth),
		aliasGrid: grid.New(nil, s.MaxColumnWidth), statsGrid: grid.New(nil, s.MaxColumnWidth),
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.setGrid.SetMarks(t.settingsMark)
	t.statsGrid.SetMarks(t.statsMark)
	t.docs = newESDocs(t)
	return t
}

var targetKindNames = map[targetKind]string{targetIndex: "index", targetAlias: "alias", targetPattern: "pattern", targetStream: "stream"}

// recentIndex is an Index tab among the recent tabs; Database names what
// its target is.
func recentIndex(conn, target string, kind targetKind) history.Recent {
	return history.Recent{Kind: history.RecentESIndex, Conn: conn, Name: target, Database: targetKindNames[kind]}
}

func targetKindNamed(s string) targetKind {
	for k, n := range targetKindNames {
		if n == s {
			return k
		}
	}
	return targetIndex
}

// openIndex opens (or shows) the Index tab of target on conn.
func (m *Model) openIndex(conn, target string, kind targetKind) tea.Cmd {
	rec := recentIndex(conn, target, kind)
	return m.openTab(esIndexTabKey(conn, target), rec, func(id int) tab {
		t := newESIndexTab(id, m.backend, m.keys, m.settings, conn, target, kind)
		t.store = m.store
		t.masker = m.esMaskerFor(conn)
		return t
	})
}

func (t *esIndexTab) ID() int       { return t.id }
func (t *esIndexTab) Title() string { return t.target }
func (t *esIndexTab) Key() string   { return esIndexTabKey(t.conn, t.target) }
func (t *esIndexTab) Context() keymap.Context {
	switch t.section {
	case idxDocuments:
		return keymap.ESDocs
	case idxSettings:
		return keymap.ESSettings
	}
	return keymap.ESIndex
}
func (t *esIndexTab) Target() (string, db.TableRef) { return t.conn, db.TableRef{} }
func (t *esIndexTab) SetSize(w, h int)              { t.width, t.height = max(w, 0), max(h, 0) }
func (t *esIndexTab) Init() tea.Cmd                 { return tea.Batch(t.loadHead(), t.loadMapping()) }

// shutdown cancels the loads and releases the Documents cursor when the
// tab closes or datavim quits.
func (t *esIndexTab) shutdown() func() {
	t.cancel()
	return t.docs.close()
}

func (t *esIndexTab) setStatus(s string, isErr bool) { t.status, t.statusE = s, isErr }

func (t *esIndexTab) retryAfterPassword() tea.Cmd {
	cmds := []tea.Cmd{t.loadHead(), t.loadMapping()}
	if !t.docs.loaded && t.mapping.loaded {
		cmds = append(cmds, t.docs.reload())
	}
	if t.section >= idxSettings {
		cmds = append(cmds, t.loadSection(t.section))
	}
	return tea.Batch(cmds...)
}

func (t *esIndexTab) activeGrid() *grid.Model {
	switch t.section {
	case idxMapping:
		return t.mapGrid
	case idxSettings:
		return t.setGrid
	case idxAliases:
		return t.aliasGrid
	case idxStats:
		return t.statsGrid
	}
	return t.docs.grid
}

func (t *esIndexTab) Find(q string, forward bool) bool {
	g := t.activeGrid()
	return g != nil && g.Find(q, forward)
}

func (t *esIndexTab) exportSource() exportSrc {
	if t.section == idxDocuments {
		return exportSrc{rs: t.docs.grid.Result(), jsonl: t.docs.ndjson(), name: t.target, conn: t.conn}
	}
	return exportSrc{rs: t.activeGrid().Result(), name: t.target + "-" + strings.ToLower(idxSectionNames[t.section]), conn: t.conn}
}

// yankText copies a document on the Documents section, the whole
// mapping as JSON on the Mapping section.
func (t *esIndexTab) yankText(a keymap.Action) (text, what string, ok bool) {
	if t.section == idxDocuments {
		return t.docs.yankText(a)
	}
	if a != keymap.YankRow || t.section != idxMapping || t.rawMappings == nil {
		return "", "", false
	}
	var v any = t.rawMappings
	if len(t.rawMappings) == 1 {
		for _, m := range t.rawMappings {
			v = m
		}
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", "", false
	}
	return string(b), "the mapping", true
}

// load runs fn on the connection with the tab's context.
func (t *esIndexTab) load(fn func(ctx context.Context, d es.Doer) tea.Msg, fail func(error) tea.Msg) tea.Cmd {
	tabCtx, b, conn := t.ctx, t.backend, t.conn
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		var out tea.Msg
		err := withES(ctx, b, conn, func(d es.Doer) error {
			out = fn(ctx, d)
			if f, ok := out.(connFailure); ok {
				_, err := f.failure()
				return err
			}
			return nil
		})
		if out == nil {
			return fail(err)
		}
		return out
	}
}

func (t *esIndexTab) loadHead() tea.Cmd {
	t.head.loading = true
	id, conn, target := t.id, t.conn, t.target
	return t.load(func(ctx context.Context, d es.Doer) tea.Msg {
		msg := esHeadMsg{tabID: id, conn: conn}
		r, err := es.ResolveIndex(ctx, d, target)
		if err != nil {
			msg.err = err
			return msg
		}
		all, err := es.Indices(ctx, d)
		for _, i := range all {
			if slices.Contains(r.Indices, i.Name) {
				msg.indices = append(msg.indices, i)
			}
		}
		msg.err = err
		return msg
	}, func(err error) tea.Msg { return esHeadMsg{tabID: id, conn: conn, err: err} })
}

func (t *esIndexTab) loadMapping() tea.Cmd {
	t.mapping.loading = true
	id, conn, target := t.id, t.conn, t.target
	return t.load(func(ctx context.Context, d es.Doer) tea.Msg {
		msg := esMappingMsg{tabID: id, conn: conn}
		if msg.raw, msg.err = es.RawMappings(ctx, d, target); msg.err != nil {
			return msg
		}
		msg.fields = map[string][]esx.Field{}
		for k, raw := range msg.raw {
			if msg.fields[k], msg.err = esx.FlattenMapping(raw); msg.err != nil {
				return msg
			}
		}
		return msg
	}, func(err error) tea.Msg { return esMappingMsg{tabID: id, conn: conn, err: err} })
}

func (t *esIndexTab) loadSection(s indexSection) tea.Cmd {
	t.parts[s].loading = true
	id, conn, target, b := t.id, t.conn, t.target, t.backend
	return t.load(func(ctx context.Context, d es.Doer) tea.Msg {
		msg := esSectionMsg{tabID: id, conn: conn, section: s}
		switch s {
		case idxSettings:
			msg.settings, msg.err = es.Settings(ctx, d, target)
		case idxAliases:
			msg.aliases, msg.err = es.IndexAliases(ctx, d, target)
		case idxStats:
			msg.stats, msg.err = es.Stats(ctx, d, target)
			// the ILM state is a column of its own; no ILM (OpenSearch, a
			// basic license, no permission) leaves it out
			if info, _ := b.ESInfo(conn); msg.err == nil && !info.OpenSearch() {
				msg.ilm, _ = es.ILMExplain(ctx, d, target)
			}
		}
		return msg
	}, func(err error) tea.Msg { return esSectionMsg{tabID: id, conn: conn, section: s, err: err} })
}

func (t *esIndexTab) Update(msg tea.Msg) tea.Cmd {
	if cmd, ok := t.editUpdate(msg); ok {
		return cmd
	}
	switch m := msg.(type) {
	case esFilterMsg:
		return t.docs.filtered(m.text)
	case esPipelinesMsg:
		return t.pickPipeline(m)
	case esSimulatePickMsg:
		return t.runSimulate(m)
	case esSimulatedMsg:
		return t.simulated(m)
	case esCellMsg:
		t.docs.cellEdited(m)
	case esDBQPlanMsg:
		return t.docs.dbqPlanned(m)
	case esDBQRunMsg:
		return t.docs.runDBQ(m)
	case esTaskMsg:
		return t.docs.tasked(m)
	case esUBQPlanMsg:
		return t.docs.ubqPlanned(m)
	case esUBQRunMsg:
		return t.docs.runUBQ(m)
	case esSubmitMsg:
		return t.docs.send()
	case esSubmittedMsg:
		return t.docs.submitted(m)
	case editFileMsg:
		return t.editorFile(m)
	case editDoneMsg:
		return readEditedFile(t.id, m)
	case editTextMsg:
		edit := t.editFor
		t.editFor = editDocs
		if m.err != nil {
			t.setStatus("editor: "+m.err.Error(), true)
			return nil
		}
		switch edit {
		case editMapping:
			return t.mappingEdited(m.text)
		case editUBQ:
			return t.docs.ubqEdited(m.text)
		}
		t.docs.edited(m.text)
	case esPageMsg:
		return t.docs.paged(m)
	case esHeadMsg:
		t.head = esPart{loaded: m.err == nil}
		if m.err != nil {
			t.head.err = esReason(m.err)
			return nil
		}
		t.indices = m.indices
		if t.parts[idxStats].loaded {
			t.statsGrid.ReplaceResult(t.statsResult())
		}
	case esMappingMsg:
		t.mapping = esPart{loaded: m.err == nil}
		if m.err != nil {
			t.mapping.err = esReason(m.err)
			if !t.docs.started {
				return t.docs.reload() // documents without the mapping's columns
			}
			return nil
		}
		t.rawMappings, t.mappings = m.raw, m.fields
		t.fields = esx.MergeMappings(m.fields)
		t.mapGrid.ReplaceResult(t.mappingResult())
		if !t.docs.started {
			return t.docs.reload() // the default columns and sort come from the mapping
		}
	case esSectionMsg:
		p := &t.parts[m.section]
		*p = esPart{loaded: m.err == nil}
		if m.err != nil {
			p.err = esReason(m.err)
			return nil
		}
		switch m.section {
		case idxSettings:
			t.settingList = m.settings
			t.setGrid.ReplaceResult(t.settingsResult())
		case idxAliases:
			t.aliases = m.aliases
			t.aliasGrid.ReplaceResult(t.aliasesResult())
		case idxStats:
			t.stats, t.ilm = m.stats, m.ilm
			t.statsGrid.ReplaceResult(t.statsResult())
		}
	}
	return nil
}

func (t *esIndexTab) Handle(a keymap.Action) tea.Cmd {
	t.status = ""
	var cmd tea.Cmd
	ok := false
	switch t.section {
	case idxDocuments:
		cmd, ok = t.docs.handle(a)
	case idxAliases:
		cmd, ok = t.handleAliases(a)
	case idxSettings:
		cmd, ok = t.handleSettings(a)
	case idxMapping:
		cmd, ok = t.handleMapping(a)
	case idxStats:
		cmd, ok = t.handleStats(a)
	}
	if ok {
		return cmd
	}
	switch a {
	case keymap.NextSection:
		return t.setSection(min(t.section+1, idxSections-1))
	case keymap.PrevSection:
		return t.setSection(max(t.section-1, 0))
	case keymap.Refresh:
		switch t.section {
		case idxMapping:
			return tea.Batch(t.loadHead(), t.loadMapping())
		case idxSettings, idxAliases, idxStats:
			return tea.Batch(t.loadHead(), t.loadSection(t.section))
		}
		return t.loadHead()
	}
	if g := t.activeGrid(); g != nil {
		if cmd, ok := gridAction(g, a); ok {
			return cmd
		}
	}
	return nil
}

func (t *esIndexTab) setSection(s indexSection) tea.Cmd {
	if s == t.section {
		return nil
	}
	t.section = s
	if s >= idxSettings && !t.parts[s].loaded && !t.parts[s].loading {
		return t.loadSection(s)
	}
	return nil
}

// health is the worst health of the tab's indices.
func (t *esIndexTab) health() string {
	rank := map[string]int{"green": 1, "yellow": 2, "red": 3}
	h := ""
	for _, i := range t.indices {
		if rank[i.Health] > rank[h] {
			h = i.Health
		}
	}
	return h
}

// header is "● green · 5p / 1r · 8,812,301 docs · 4.1 GB · created
// 2026-03-02"; an alias, pattern or data stream starts with how many
// indices it names.
func (t *esIndexTab) header() string {
	var docs, size int64
	for _, i := range t.indices {
		docs += i.Docs
		size += i.StoreBytes
	}
	var parts []string
	if t.kind == targetIndex && len(t.indices) == 1 {
		i := t.indices[0]
		parts = append(parts, healthLabel(i.Health), fmt.Sprintf("%dp / %dr", i.Primaries, i.Replicas))
		if i.Closed() {
			parts = append(parts, "[closed]")
		}
	} else {
		what := map[targetKind]string{targetAlias: "alias", targetPattern: "pattern", targetStream: "data stream", targetIndex: "index"}[t.kind]
		parts = append(parts, fmt.Sprintf("%s → %d indices", what, len(t.indices)))
		if h := t.health(); h != "" {
			parts = append(parts, healthLabel(h))
		}
	}
	parts = append(parts, groupDigits(docs)+" docs", humanBytes(size))
	if len(t.indices) == 1 && !t.indices[0].Created.IsZero() {
		parts = append(parts, "created "+t.indices[0].Created.Format("2006-01-02"))
	}
	return strings.Join(parts, " · ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func (t *esIndexTab) mappingResult() *db.ResultSet {
	rows := make([][]string, 0, len(t.fields))
	for _, f := range t.fields {
		index, dv := yesNo(f.Indexed), yesNo(f.DocValues)
		if f.Object {
			index, dv = "", ""
		}
		rows = append(rows, []string{f.Path, f.Type, index, dv, f.Analyzer, strings.Join(f.Sub, ", "), strings.Join(f.CopyTo, ", ")})
	}
	return textResult([]string{"field", "type", "index", "doc_values", "analyzer", "fields", "copy_to"}, rows)
}

func (t *esIndexTab) settingsResult() *db.ResultSet {
	rows := make([][]string, len(t.settingList))
	for i, s := range t.settingList {
		kind := "static"
		if s.Dynamic {
			kind = "dynamic"
		}
		def := ""
		if s.Default {
			def = "default"
		}
		rows[i] = []string{s.Key, t.settingValue(s), def, kind}
	}
	return textResult([]string{"setting", "value", "", "kind"}, rows)
}

// settingMark marks the settings set on the index (not defaults).
func (t *esIndexTab) settingMark(row, _ int) grid.Mark {
	if row < len(t.settingList) && !t.settingList[row].Default {
		return grid.MarkSet
	}
	return grid.MarkNone
}

func (t *esIndexTab) aliasesResult() *db.ResultSet {
	rows := make([][]string, len(t.aliases))
	for i, a := range t.aliases {
		write := ""
		if a.IsWrite {
			write = "write"
		}
		filter, _ := esx.CellText(a.Filter, len(a.Filter) > 0)
		rows[i] = []string{a.Name, a.Index, write, filter, a.Routing}
	}
	return textResult([]string{"alias", "index", "write", "filter", "routing"}, rows)
}

func (t *esIndexTab) statsResult() *db.ResultSet {
	shards := map[string]string{}
	for _, i := range t.indices {
		shards[i.Name] = fmt.Sprintf("%dp / %dr", i.Primaries, i.Replicas)
	}
	n := groupDigits
	rows := make([][]string, len(t.stats))
	for i, s := range t.stats {
		rows[i] = []string{s.Index, n(s.Docs), n(s.Deleted), humanBytes(s.PriStoreBytes), humanBytes(s.StoreBytes), shards[s.Index]}
		if t.ilm != nil {
			rows[i] = append(rows[i], ilmText(t.ilm[s.Index]))
		}
		rows[i] = append(rows[i], n(s.Segments), n(s.Refreshes), n(s.Flushes), n(s.Merges), n(s.Queries), n(s.Indexed))
	}
	cols := []string{"index", "docs", "deleted", "pri size", "size", "shards"}
	if t.ilm != nil {
		cols = append(cols, "ilm")
	}
	return textResult(append(cols, "segments", "refreshes", "flushes", "merges", "queries", "indexed"), rows)
}

// ilmText is an index's ILM state in a cell: "—" when ILM does not manage it.
func ilmText(s es.ILMState) string {
	switch {
	case s.Policy == "":
		return "—"
	case s.Failed != "":
		return s.Policy + " · ERROR: " + s.Failed
	}
	return s.Policy + " · " + s.Phase + "/" + s.Action
}

// statsMark shows the indices whose ILM step failed.
func (t *esIndexTab) statsMark(row, _ int) grid.Mark {
	if row < len(t.stats) && t.ilm[t.stats[row].Index].Failed != "" {
		return grid.MarkError
	}
	return grid.MarkNone
}

func (t *esIndexTab) sectionLine() string {
	names := make([]string, idxSections)
	for i, n := range idxSectionNames {
		if indexSection(i) == t.section {
			names[i] = theme.Title.Render(n)
		} else {
			names[i] = theme.Muted.Render(n)
		}
	}
	return strings.Join(names, theme.Muted.Render(" · "))
}

func (t *esIndexTab) View(focused bool) string {
	lines := []string{t.sectionLine()}
	switch {
	case t.head.err != "":
		lines = append(lines, theme.Error.Render(textutil.Sanitize(t.head.err)))
	case t.head.loaded:
		lines = append(lines, theme.Muted.Render(t.header()))
	default:
		lines = append(lines, theme.Muted.Render("loading…"))
	}
	if t.status != "" {
		st := theme.Muted
		if t.statusE {
			st = theme.Error
		}
		lines = append(lines, st.Render(textutil.Sanitize(t.status)))
	}
	var g *grid.Model
	note := func(p esPart, empty bool, emptyText string, gr *grid.Model) {
		switch {
		case p.err != "":
			lines = append(lines, theme.Error.Render(textutil.Sanitize(p.err)))
		case !p.loaded:
			lines = append(lines, theme.Muted.Render("loading…"))
		case empty:
			lines = append(lines, theme.Muted.Render(emptyText))
		default:
			g = gr
		}
	}
	switch t.section {
	case idxDocuments:
		d := t.docs
		switch {
		case d.err != "":
			lines = append(lines, theme.Error.Render(textutil.Sanitize(d.err)))
		case !d.loaded:
			lines = append(lines, theme.Muted.Render("loading…"))
		default:
			lines = append(lines, theme.Muted.Render(textutil.Sanitize(d.header())))
			if len(d.hits) == 0 {
				lines = append(lines, theme.Muted.Render("no documents"))
			} else {
				g = d.grid
			}
		}
	case idxMapping:
		note(t.mapping, len(t.fields) == 0, "no fields", t.mapGrid)
	case idxSettings:
		note(t.parts[idxSettings], len(t.settingList) == 0, "no settings", t.setGrid)
	case idxAliases:
		note(t.parts[idxAliases], len(t.aliases) == 0, "no aliases", t.aliasGrid)
	case idxStats:
		note(t.parts[idxStats], len(t.stats) == 0, "no stats", t.statsGrid)
	}
	for i, l := range lines {
		lines[i] = textutil.Fit(l, t.width)
	}
	if g != nil {
		g.SetSize(t.width, max(t.height-len(lines), 0))
		lines = append(lines, g.View(focused))
	}
	return strings.Join(lines, "\n")
}

// healthLabel is "● green"; unknown health is "○".
func healthLabel(h string) string {
	if h == "" {
		return "○"
	}
	return "● " + h
}
