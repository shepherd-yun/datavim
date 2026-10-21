package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/keymap"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
	"github.com/shepherd-yun/datavim/internal/ui/textutil"
	"github.com/shepherd-yun/datavim/internal/ui/theme"
)

// esDocDetail is K on a document: its metadata and its _source as
// indented JSON whose objects and arrays fold like the tree (the Tree's
// fold keys); n / N show the next / previous document.
type esDocDetail struct {
	keys    *keymap.Keymap
	docs    *esDocs // the view it shows a row of; nil for one document
	doc     esx.Hit // the document when docs is nil
	row     int     // the row of docs it shows
	lines   []string
	folded  map[int]bool // the folded blocks by their first line
	cur     int          // the cursor, a line of lines
	offset  int
	pending []string
}

func newESDocDetail(t *esIndexTab, row int) *esDocDetail {
	d := &esDocDetail{keys: t.keys, docs: t.docs, row: row}
	d.reset()
	return d
}

// newESDocDetailOf shows one document that is not a row of a view.
func newESDocDetailOf(keys *keymap.Keymap, h esx.Hit) *esDocDetail {
	d := &esDocDetail{keys: keys, row: -1, doc: h}
	d.reset()
	return d
}

func (d *esDocDetail) hit() esx.Hit {
	if d.docs == nil || d.row < 0 || d.row >= len(d.docs.hits) {
		return d.doc
	}
	return d.docs.hits[d.row]
}

func (d *esDocDetail) reset() {
	d.lines = strings.Split(textutil.PrettyJSON(string(d.hit().Source)), "\n")
	d.folded, d.cur, d.offset = map[int]bool{}, 0, 0
}

// blockEnd is the line closing the object or array line i opens, or -1.
func (d *esDocDetail) blockEnd(i int) int {
	l := strings.TrimRight(d.lines[i], " ")
	if !strings.HasSuffix(l, "{") && !strings.HasSuffix(l, "[") {
		return -1
	}
	indent := len(l) - len(strings.TrimLeft(l, " "))
	for j := i + 1; j < len(d.lines); j++ {
		lj := d.lines[j]
		if len(lj)-len(strings.TrimLeft(lj, " ")) == indent {
			return j
		}
	}
	return -1
}

// visible are the shown line numbers: a folded block shows its first line.
func (d *esDocDetail) visible() []int {
	var out []int
	for i := 0; i < len(d.lines); i++ {
		out = append(out, i)
		if d.folded[i] {
			if e := d.blockEnd(i); e > 0 {
				i = e
			}
		}
	}
	return out
}

// blockAt is the block the cursor's line opens, or the one around it.
func (d *esDocDetail) blockAt(line int) int {
	for i := line; i >= 0; i-- {
		if e := d.blockEnd(i); e >= line && (i == line || e > line) {
			return i
		}
	}
	return -1
}

func (d *esDocDetail) trigger(ctx keymap.Context, a keymap.Action, key string) bool {
	return d.keys.Triggers(ctx, a, key)
}

// fold reads the fold key sequences of the tree (zo, zc, za by default).
func (d *esDocDetail) fold(key string) (handled bool) {
	d.pending = append(d.pending, key)
	prefix := false
	for _, a := range []keymap.Action{keymap.FoldOpen, keymap.FoldClose, keymap.FoldToggle} {
		seq := d.keys.SeqFor(keymap.Tree, a)
		if len(seq) < 2 {
			continue
		}
		if slices.Equal(seq, d.pending) {
			d.pending = nil
			vis := d.visible()
			line := vis[min(d.cur, len(vis)-1)]
			b := d.blockAt(line)
			if b < 0 {
				return true
			}
			switch a {
			case keymap.FoldOpen:
				delete(d.folded, b)
			case keymap.FoldClose:
				d.folded[b] = true
			default:
				d.folded[b] = !d.folded[b]
			}
			d.cur = slices.Index(d.visible(), b)
			return true
		}
		if len(d.pending) < len(seq) && slices.Equal(seq[:len(d.pending)], d.pending) {
			prefix = true
		}
	}
	if !prefix {
		d.pending = nil
	}
	return prefix
}

func (d *esDocDetail) HandleKey(msg tea.KeyPressMsg) (overlay.Overlay, tea.Cmd) {
	key := msg.String()
	if d.fold(key) {
		return d, nil
	}
	docs := d.docs
	switch {
	case key == "esc" || key == "q" || d.trigger(keymap.ESDocs, keymap.CellDetail, key):
		return nil, nil
	case key == "j" || key == "down":
		d.cur = min(d.cur+1, len(d.visible())-1)
	case key == "k" || key == "up":
		d.cur = max(d.cur-1, 0)
	case docs != nil && (d.trigger(keymap.ESDocs, keymap.SearchNext, key) || d.trigger(keymap.ESDocs, keymap.SearchPrev, key)):
		step := 1
		if d.trigger(keymap.ESDocs, keymap.SearchPrev, key) {
			step = -1
		}
		if r := d.row + step; r >= 0 && r < len(docs.hits) {
			d.row = r
			_, col := docs.grid.Position()
			docs.grid.SetCursor(r, col)
			d.reset()
		}
	case key == "y":
		h := d.hit()
		what := "_source"
		if h.Truncated {
			what += " (a preview: the document is over 1 MB)"
		}
		return d, func() tea.Msg { return overlay.CopyMsg{Text: textutil.PrettyJSON(string(h.Source)), What: what} }
	}
	return d, nil
}

// head is "orders · ord-1   _seq_no 1022 · _primary_term 3 · _version 4 · _score 7.31".
func (d *esDocDetail) head() string {
	h := d.hit()
	meta := []string{"_seq_no " + strconv.FormatInt(h.SeqNo, 10), "_primary_term " + strconv.FormatInt(h.PrimaryTerm, 10)}
	if h.Version > 0 {
		meta = append(meta, "_version "+strconv.FormatInt(h.Version, 10))
	}
	if h.Score != nil {
		meta = append(meta, "_score "+strconv.FormatFloat(*h.Score, 'f', 2, 64))
	}
	if h.Routing != "" {
		meta = append(meta, "_routing "+h.Routing)
	}
	return fmt.Sprintf("%s · %s   %s", h.Index, h.ID, strings.Join(meta, " · "))
}

// styled is one JSON line with its key in the accent color and the hit's
// highlighted words marked.
func styled(line string, words []string) string {
	text := textutil.Sanitize(line)
	key, rest := "", text
	if t := strings.TrimLeft(text, " "); strings.HasPrefix(t, `"`) {
		if end := strings.Index(t[1:], `":`); end >= 0 {
			indent := text[:len(text)-len(t)]
			key, rest = indent+t[:end+3], t[end+3:]
		}
	}
	return theme.Accent.Render(key) + markWords(rest, words)
}

func (d *esDocDetail) View(width, height int) string {
	w := max(min(width-4, 120), 20)
	h := d.hit()
	lines := []string{d.head()}
	if h.Truncated {
		lines = append(lines, theme.Error.Render("showing first 1 MB of "+humanBytes(int64(h.SourceSize))))
	}
	lines = append(lines, theme.Muted.Render(strings.Repeat("─", max(w-4, 10))))
	top := len(lines)
	words := highlightWords(h)
	vis := d.visible()
	for n, i := range vis {
		l := d.lines[i]
		if d.folded[i] {
			if e := d.blockEnd(i); e > 0 {
				closer := strings.TrimSpace(d.lines[e])
				l = strings.TrimRight(l, " ") + "…" + closer
			}
		}
		s := styled(l, words)
		if n == d.cur {
			s = theme.Selected.Render(textutil.Sanitize(l))
		}
		lines = append(lines, s)
	}
	bh := max(min(height-2, len(lines)+2), 3)
	rows := bh - 2 - top
	d.cur = textutil.Clamp(d.cur, 0, max(len(vis)-1, 0))
	if d.cur < d.offset {
		d.offset = d.cur
	}
	if rows > 0 && d.cur >= d.offset+rows {
		d.offset = d.cur - rows + 1
	}
	body := append([]string{}, lines[:top]...)
	end := min(top+d.offset+max(rows, 0), len(lines))
	body = append(body, lines[top+d.offset:end]...)
	for i, l := range body {
		body[i] = textutil.Fit(l, w-2)
	}
	k := d.keys
	own := func(ctx keymap.Context, a keymap.Action, label string) string {
		if seq := k.SeqFor(ctx, a); seq != nil {
			return k.Display(seq) + " " + label
		}
		return ""
	}
	hints := []string{"document", own(keymap.Tree, keymap.FoldToggle, "fold")}
	if d.docs != nil {
		hints = append(hints, own(keymap.ESDocs, keymap.SearchNext, "next"), own(keymap.ESDocs, keymap.SearchPrev, "prev"))
	}
	title := joinHints(append(hints, "y copy", "esc close")...)
	return theme.Box(title, strings.Join(body, "\n"), w, bh, true)
}
