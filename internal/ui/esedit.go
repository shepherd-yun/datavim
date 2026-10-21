package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/es"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/panes/grid"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
)

// changeKind is what a staged change does to a document.
type changeKind int

const (
	chEdit changeKind = iota
	chCreate
	chDelete
)

// fieldChange is one edited field, for the confirmation.
type fieldChange struct{ field, old, new string }

// esChange is a staged change of one document.
type esChange struct {
	hit    esx.Hit // the document as read (create: Index and ID only)
	kind   changeKind
	src    json.RawMessage // the new _source
	fields []fieldChange   // nil for a whole-document edit
	whole  bool
}

// esOp is one staging step, undone by putting prev back.
type esOp struct {
	key  string
	prev *esChange // nil: the key had no change
}

func docKey(index, id string) string { return index + "\x00" + id }

func hitKey(h esx.Hit) string { return docKey(h.Index, h.ID) }

// esCellMsg is a cell's new value from the cell editor.
type esCellMsg struct {
	tabID int
	key   string
	col   string
	v     db.Value
}

func (m esCellMsg) TabID() int { return m.tabID }

// esSubmitMsg sends the staged changes, confirmed.
type esSubmitMsg struct{ tabID int }

func (m esSubmitMsg) TabID() int { return m.tabID }

// esSubmittedMsg is what sending the changes did.
type esSubmittedMsg struct {
	tabID int
	conn  string
	done  []string         // the keys saved
	errs  map[string]error // the keys that failed
	err   error            // the connection failed
}

func (m esSubmittedMsg) TabID() int               { return m.tabID }
func (m esSubmittedMsg) esMsg()                   {}
func (m esSubmittedMsg) failure() (string, error) { return m.conn, m.err }

// view is h with its staged edit applied.
func (d *esDocs) view(h esx.Hit) esx.Hit {
	if c := d.changes[hitKey(h)]; c != nil && c.kind == chEdit {
		h.Source = c.src
	}
	return h
}

// mark draws staged edits and deletions.
func (d *esDocs) mark(row, col int) grid.Mark {
	if row >= len(d.hits) || col >= len(d.cols) {
		return grid.MarkNone
	}
	c := d.changes[hitKey(d.hits[row])]
	switch {
	case c == nil:
		return grid.MarkNone
	case c.kind == chDelete:
		return grid.MarkDeleted
	case c.whole || slices.ContainsFunc(c.fields, func(f fieldChange) bool { return f.field == d.cols[col] }):
		return grid.MarkChanged
	}
	return grid.MarkNone
}

// stage records c under key (nil removes it), undoably.
func (d *esDocs) stage(key string, c *esChange) {
	if d.changes == nil {
		d.changes = map[string]*esChange{}
	}
	d.ops = append(d.ops, esOp{key: key, prev: d.changes[key]})
	if c == nil {
		delete(d.changes, key)
	} else {
		if _, ok := d.changes[key]; !ok {
			d.order = append(d.order, key)
		}
		d.changes[key] = c
	}
	d.redraw()
}

func (d *esDocs) redraw() {
	row, col := d.grid.Position()
	d.grid.ReplaceResult(d.result())
	d.grid.SetCursor(row, col)
}

func (d *esDocs) undo() {
	if len(d.ops) == 0 {
		d.tab.setStatus("nothing to undo", false)
		return
	}
	op := d.ops[len(d.ops)-1]
	d.ops = d.ops[:len(d.ops)-1]
	if op.prev == nil {
		delete(d.changes, op.key)
	} else {
		d.changes[op.key] = op.prev
	}
	d.redraw()
}

func (d *esDocs) undoAll() {
	d.changes, d.ops, d.order = nil, nil, nil
	d.redraw()
}

// editRefusal is why h cannot be edited, or "".
func (d *esDocs) editRefusal(h esx.Hit) string {
	switch {
	case h.Truncated:
		return "this document is over 1 MB and shown in part: it cannot be edited"
	case strings.HasPrefix(h.Index, ".ds-"):
		return h.Index + " backs a data stream: change its documents with _update_by_query"
	}
	var m struct {
		Source *struct {
			Enabled  *bool    `json:"enabled"`
			Excludes []string `json:"excludes"`
			Includes []string `json:"includes"`
		} `json:"_source"`
	}
	if raw := d.tab.rawMappings[h.Index]; raw != nil && json.Unmarshal(raw, &m) == nil && m.Source != nil {
		if m.Source.Enabled != nil && !*m.Source.Enabled || len(m.Source.Excludes) > 0 || len(m.Source.Includes) > 0 {
			return "the _source of " + h.Index + " is disabled or filtered: rewriting it would lose fields"
		}
	}
	return ""
}

// editCell opens the cell editor on the field under the cursor.
func (d *esDocs) editCell() tea.Cmd {
	t := d.tab
	h, ok := d.selected()
	col := d.column()
	if !ok {
		return nil
	}
	if r := d.editRefusal(h); r != "" {
		t.setStatus(r, true)
		return nil
	}
	if col == "" || col == "_id" || col == "_index" {
		t.setStatus("only _source fields can be edited", true)
		return nil
	}
	if t.masker.Covers(h.Index, col) {
		t.setStatus("masked fields cannot be edited", true)
		return nil
	}
	if c := d.changes[hitKey(h)]; c != nil && c.kind == chDelete {
		t.setStatus("the document is marked for deletion; undo it first", true)
		return nil
	}
	v := cell(d.view(h), col)
	id, key := t.id, hitKey(h)
	return openOverlay(overlay.NewCellEditor(h.ID+" · "+col, v, func(nv db.Value) tea.Msg {
		return esCellMsg{tabID: id, key: key, col: col, v: nv}
	}))
}

func (d *esDocs) hitByKey(key string) (esx.Hit, bool) {
	for _, h := range d.hits {
		if hitKey(h) == key {
			return h, true
		}
	}
	return esx.Hit{}, false
}

// fieldType is col's type in h's index, "object" for an object or array
// value: the input is parsed by it (spec §5.6).
func (d *esDocs) fieldType(h esx.Hit, col string) string {
	if v, ok := esx.GetPath(h.Source, col); ok {
		if b := bytes.TrimSpace(v); len(b) > 0 && (b[0] == '{' || b[0] == '[') {
			return "object"
		}
	}
	if fs, ok := d.tab.mappings[h.Index]; ok {
		return esx.TypeFor(fs, col)
	}
	return esx.TypeFor(d.tab.fields, col)
}

func (d *esDocs) cellEdited(m esCellMsg) {
	t := d.tab
	h, ok := d.hitByKey(m.key)
	if !ok {
		return
	}
	text := m.v.Text
	if m.v.Null {
		text = "null"
	}
	raw, err := esx.ParseCell(d.fieldType(h, m.col), text)
	if err != nil {
		t.setStatus(err.Error(), true)
		return
	}
	cur := d.view(h)
	src, err := esx.SetPath(cur.Source, m.col, raw)
	if err != nil {
		t.setStatus(err.Error(), true)
		return
	}
	c := &esChange{hit: h, kind: chEdit, src: src}
	if prev := d.changes[m.key]; prev != nil && prev.kind == chEdit {
		c.whole, c.fields = prev.whole, slices.Clone(prev.fields)
	}
	old, _ := esx.CellText(esx.GetPath(h.Source, m.col))
	nw, _ := esx.CellText(raw, true)
	if i := slices.IndexFunc(c.fields, func(f fieldChange) bool { return f.field == m.col }); i >= 0 {
		c.fields[i].new = nw
	} else {
		c.fields = append(c.fields, fieldChange{field: m.col, old: old, new: nw})
	}
	d.stage(m.key, c)
}

// deleteRow marks the document under the cursor for deletion, or takes
// the mark back.
func (d *esDocs) deleteRow() {
	h, ok := d.selected()
	if !ok {
		return
	}
	key := hitKey(h)
	if c := d.changes[key]; c != nil && c.kind == chDelete {
		d.stage(key, nil)
		return
	}
	d.stage(key, &esChange{hit: h, kind: chDelete})
}

// externalEdit opens the document's _source, or a new document, in $EDITOR.
func (d *esDocs) externalEdit(create bool) tea.Cmd {
	t := d.tab
	if create {
		if t.kind == targetPattern {
			t.setStatus("open one index (or an alias with a write index) to add a document", true)
			return nil
		}
		d.editing = ""
		return writeTempEdit(t.id, d.newTemplate(), "datavim-*.json")
	}
	h, ok := d.selected()
	if !ok {
		return nil
	}
	if r := d.editRefusal(h); r != "" {
		t.setStatus(r, true)
		return nil
	}
	if d.masked[hitKey(h)] {
		t.setStatus("this document has masked fields: edit single fields instead", true)
		return nil
	}
	d.editing = hitKey(h)
	return writeTempEdit(t.id, textutil.PrettyJSON(string(d.view(h).Source))+"\n", "datavim-*.json")
}

// newTemplate is a new document: an empty _id (the server makes one)
// and the mapped fields set to null.
func (d *esDocs) newTemplate() string {
	src := json.RawMessage(`{"_id":""}`)
	for _, f := range d.tab.fields {
		if f.Object {
			continue
		}
		if next, err := esx.SetPath(src, f.Path, json.RawMessage("null")); err == nil {
			src = next
		}
	}
	return textutil.PrettyJSON(string(src)) + "\n"
}

// edited is $EDITOR's text: the whole new _source of d.editing, or a new
// document when d.editing is empty.
func (d *esDocs) edited(text string) {
	t := d.tab
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &obj); err != nil || obj == nil {
		t.setStatus("the document must be a JSON object", true)
		return
	}
	if d.editing == "" {
		var id string
		if raw, ok := obj["_id"]; ok && json.Unmarshal(raw, &id) != nil {
			t.setStatus("_id must be a string", true)
			return
		}
		src, err := esx.RemoveKey([]byte(text), "_id")
		if err != nil {
			t.setStatus(err.Error(), true)
			return
		}
		d.creates++
		key := fmt.Sprintf("new\x00%d", d.creates)
		d.stage(key, &esChange{hit: esx.Hit{Index: t.target, ID: id}, kind: chCreate, src: src})
		return
	}
	h, ok := d.hitByKey(d.editing)
	if !ok {
		return
	}
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(text)); err != nil {
		t.setStatus("the document must be a JSON object", true)
		return
	}
	d.stage(d.editing, &esChange{hit: h, kind: chEdit, src: b.Bytes(), whole: true})
}

// changeLines are the confirmation's lines for the staged changes.
func (d *esDocs) changeLines() []string {
	var lines []string
	for _, key := range d.order {
		c := d.changes[key]
		if c == nil {
			continue
		}
		where := c.hit.Index + "/" + c.hit.ID
		switch {
		case c.kind == chDelete:
			lines = append(lines, "delete "+where)
		case c.kind == chCreate:
			id := c.hit.ID
			if id == "" {
				id = "a new _id"
			}
			lines = append(lines, "new document in "+c.hit.Index+" ("+id+")")
		case c.whole:
			lines = append(lines, where+": the whole _source")
		default:
			lines = append(lines, where+":")
			for _, f := range c.fields {
				lines = append(lines, fmt.Sprintf("  %s: %s → %s", f.field, f.old, f.new))
			}
		}
	}
	return lines
}

func (d *esDocs) pending() []*esChange {
	var out []*esChange
	for _, key := range d.order {
		if c := d.changes[key]; c != nil {
			out = append(out, c)
		}
	}
	return out
}

// submit confirms the staged changes (spec §9.2) and sends them.
func (d *esDocs) submit() tea.Cmd {
	t := d.tab
	cs := d.pending()
	if len(cs) == 0 {
		t.setStatus("nothing to submit", false)
		return nil
	}
	n := len(cs)
	lines := append(d.changeLines(), "", requestCount(n)+" · each only if the document is unchanged since it was read")
	p := esPlan{Class: esx.Class{Level: esx.LevelWrite, Op: "document changes"}, Lines: lines}
	first := cs[0]
	switch first.kind {
	case chDelete:
		p.Method, p.Path = "DELETE", first.hit.Index+"/_doc/"+first.hit.ID
	case chCreate:
		p.Method, p.Path, p.Body = "POST", first.hit.Index+"/_doc", []byte(textutil.PrettyJSON(string(first.src)))
	default:
		p.Method, p.Path, p.Body = "PUT", first.hit.Index+"/_doc/"+first.hit.ID, []byte(textutil.PrettyJSON(string(first.src)))
		if d.masked[hitKey(first.hit)] {
			partial, _ := d.partial(first)
			p.Method, p.Path, p.Body = "POST", first.hit.Index+"/_update/"+first.hit.ID, []byte(textutil.PrettyJSON(`{"doc":`+string(partial)+`}`))
		}
	}
	cfg, _ := t.backend.Connection(t.conn)
	return confirmESCmd(cfg, p, esSubmitMsg{tabID: t.id}, t.setStatus)
}

func requestCount(n int) string {
	if n == 1 {
		return "1 request"
	}
	return fmt.Sprintf("%d requests", n)
}

// partial is the fields an edit of a masked document changed: only they
// are sent, so the masked values are never written back.
func (d *esDocs) partial(c *esChange) (json.RawMessage, error) {
	fields := make([]string, len(c.fields))
	for i, f := range c.fields {
		fields[i] = f.field
	}
	return esx.PartialDoc(c.src, fields)
}

// send writes the changes one by one: each edit and delete only if the
// document is still at the version read, to the hit's own index.
func (d *esDocs) send() tea.Cmd {
	t := d.tab
	keys := slices.Clone(d.order)
	changes := map[string]*esChange{}
	partials := map[string]json.RawMessage{} // the edits of masked documents
	for _, k := range keys {
		c := d.changes[k]
		if c == nil {
			continue
		}
		changes[k] = c
		if c.kind == chEdit && d.masked[k] {
			p, err := d.partial(c)
			if err != nil {
				t.setStatus(err.Error(), true)
				return nil
			}
			partials[k] = p
		}
	}
	b, conn, id, tabCtx := t.backend, t.conn, t.id, t.ctx
	t.setStatus("saving…", false)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(tabCtx, loadTimeout)
		defer cancel()
		msg := esSubmittedMsg{tabID: id, conn: conn, errs: map[string]error{}}
		d, err := b.ES(ctx, conn)
		if err != nil {
			msg.err = err
			return msg
		}
		for _, k := range keys {
			c := changes[k]
			if c == nil {
				continue
			}
			h := c.hit
			v := es.Version{SeqNo: h.SeqNo, PrimaryTerm: h.PrimaryTerm}
			switch c.kind {
			case chEdit:
				if p, ok := partials[k]; ok {
					_, err = es.UpdateDoc(ctx, d, h.Index, h.ID, h.Routing, p, v)
				} else {
					_, err = es.PutDoc(ctx, d, h.Index, h.ID, h.Routing, c.src, v)
				}
			case chCreate:
				_, err = es.CreateDoc(ctx, d, h.Index, h.ID, c.src)
			case chDelete:
				err = es.DeleteDoc(ctx, d, h.Index, h.ID, h.Routing, v)
			}
			if err != nil {
				msg.errs[k] = err
			} else {
				msg.done = append(msg.done, k)
			}
		}
		return msg
	}
}

func (d *esDocs) submitted(m esSubmittedMsg) tea.Cmd {
	t := d.tab
	if m.err != nil {
		t.setStatus(esReason(m.err), true)
		return nil
	}
	for _, k := range m.done {
		delete(d.changes, k)
	}
	d.order = slices.DeleteFunc(d.order, func(k string) bool { return d.changes[k] == nil })
	d.ops = nil
	var problems []string
	for _, k := range d.order {
		err := m.errs[k]
		if err == nil {
			continue
		}
		c := d.changes[k]
		if errors.Is(err, es.ErrConflict) {
			problems = append(problems, c.hit.ID+" changed since you loaded it; reload to see it")
		} else {
			problems = append(problems, c.hit.Index+"/"+c.hit.ID+": "+esReason(err))
		}
	}
	saved := fmt.Sprintf("saved %s", plural(len(m.done), "document"))
	if len(problems) > 0 {
		t.setStatus(saved+" · "+strings.Join(problems, " · "), true)
	} else {
		t.setStatus(saved, false)
	}
	if len(m.done) == 0 {
		d.redraw()
		return nil
	}
	return d.reload()
}

// editorFile runs $EDITOR on the temp file the tab wrote.
func (t *esIndexTab) editorFile(m editFileMsg) tea.Cmd {
	if m.err != nil {
		t.setStatus("cannot create temp file: "+m.err.Error(), true)
		return nil
	}
	t.docs.editPath = m.path
	argv := append(slices.Clone(t.settings.EditorCommand(os.Getenv)), m.path)
	id, path := t.id, m.path
	return tea.ExecProcess(exec.Command(argv[0], argv[1:]...), func(err error) tea.Msg { //nolint:gosec // the user's own editor
		return editDoneMsg{tabID: id, path: path, err: err}
	})
}
