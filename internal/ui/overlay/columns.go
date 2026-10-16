package overlay

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Columns is the <space>s popup: a table's columns, filtered by typing.
// enter picks the column under the cursor, the popup's structure key
// (ctrl+o) asks for the full structure (with an empty name when nothing
// matches).
type Columns struct {
	keys    *keymap.Keymap
	title   string
	cols    []db.Column
	header  []string // column titles and their rule
	rows    []string // one aligned line per column
	filter  field
	matches []int
	cur     int
	offset  int
	pick    func(i int, structure bool) tea.Msg // i is -1 when nothing matches
}

// NewColumns builds the popup with the cursor on the column called current,
// if there is one.
func NewColumns(keys *keymap.Keymap, title string, cols []db.Column, current string, pick func(name string, structure bool) tea.Msg) *Columns {
	c := newColumns(keys, title, cols, nil, func(i int, structure bool) tea.Msg {
		if pick == nil {
			return nil
		}
		if i < 0 {
			return pick("", structure)
		}
		return pick(cols[i].Name, structure)
	})
	for i, col := range cols {
		if col.Name == current {
			c.cur = i
		}
	}
	return c
}

// NewResultColumns builds the popup for the columns of a query result,
// which may repeat a name: pick gets the index of the column. known tells
// the columns read from the catalog from those only the result describes
// (no nullability); nil means all are known.
func NewResultColumns(keys *keymap.Keymap, title string, cols []db.Column, known []bool, current int, pick func(i int, structure bool) tea.Msg) *Columns {
	c := newColumns(keys, title, cols, known, pick)
	if current >= 0 && current < len(cols) {
		c.cur = current
	}
	return c
}

func newColumns(keys *keymap.Keymap, title string, cols []db.Column, known []bool, pick func(i int, structure bool) tea.Msg) *Columns {
	rows := make([][]string, len(cols))
	for i, col := range cols {
		key, null, def := "", "NOT NULL", ""
		if col.PrimaryKey {
			key = "PK"
		}
		if col.Nullable {
			null = "NULL"
		}
		if known != nil && !known[i] {
			null = ""
		}
		if col.Default != nil {
			def = *col.Default
		}
		rows[i] = []string{key, col.Name, col.Type, null, def, col.Comment}
	}
	lines := textutil.Table([]string{"", "Name", "Type", "Null", "Default", "Comment"}, rows)
	c := &Columns{keys: keys, title: title, cols: cols, header: lines[:2], rows: lines[2 : 2+len(cols)], pick: pick}
	c.refilter()
	return c
}

func (c *Columns) refilter() {
	q := strings.ToLower(c.filter.String())
	c.matches = c.matches[:0]
	for i, col := range c.cols {
		if strings.Contains(strings.ToLower(col.Name), q) || strings.Contains(strings.ToLower(col.Comment), q) {
			c.matches = append(c.matches, i)
		}
	}
	c.cur, c.offset = 0, 0
}

func (c *Columns) selected() int {
	if len(c.matches) == 0 {
		return -1
	}
	return c.matches[c.cur]
}

func (c *Columns) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	if c.keys.Triggers(keymap.Popup, keymap.OpenFullStructure, msg.String()) {
		return nil, emit(c.pick(c.selected(), true))
	}
	switch msg.String() {
	case "esc":
		return nil, nil
	case "enter":
		if len(c.matches) == 0 {
			return c, nil
		}
		return nil, emit(c.pick(c.selected(), false))
	case "down", "ctrl+n", "ctrl+j":
		c.cur = textutil.Clamp(c.cur+1, 0, len(c.matches)-1)
	case "up", "ctrl+p", "ctrl+k":
		c.cur = textutil.Clamp(c.cur-1, 0, len(c.matches)-1)
	default:
		before := c.filter.String()
		c.filter.handle(msg)
		if c.filter.String() != before {
			c.refilter()
		}
	}
	return c, nil
}

func (c *Columns) View(width, height int) string {
	w := max(min(width-4, 110), 20)
	h := max(min(height-2, 24, len(c.cols)+7), 8) // fits the columns, never shrinks while filtering
	listH := h - 7                                // border, filter, status, header, rule, footer
	if c.cur < c.offset {
		c.offset = c.cur
	}
	if c.cur >= c.offset+listH {
		c.offset = c.cur - listH + 1
	}
	status := fmt.Sprintf("%d columns", len(c.cols))
	switch {
	case len(c.matches) == 0:
		status = "(no matches)"
	case len(c.matches) < len(c.cols):
		status = fmt.Sprintf("%d of %d columns", len(c.matches), len(c.cols))
	}
	lines := []string{
		"> " + textutil.Sanitize(c.filter.String()) + "█",
		theme.Muted.Render(status),
	}
	for _, l := range c.header {
		lines = append(lines, theme.Muted.Render(textutil.Fit(l, w-2)))
	}
	for i := c.offset; i < len(c.matches) && i < c.offset+listH; i++ {
		row := textutil.Fit(c.rows[c.matches[i]], w-2)
		if i == c.cur {
			row = theme.Selected.Render(row)
		}
		lines = append(lines, row)
	}
	body := textutil.PadLines(strings.Join(lines, "\n"), h-3) + "\n" +
		theme.Muted.Render(textutil.Fit(footer("enter go to column", popupHint(c.keys, keymap.OpenFullStructure, "full structure"), "↑/↓ move · esc close"), w-2))
	return theme.Box(c.title, body, w, h, true)
}

// Paste appends text to the filter.
func (c *Columns) Paste(text string) {
	c.filter.paste(text)
	c.refilter()
}
