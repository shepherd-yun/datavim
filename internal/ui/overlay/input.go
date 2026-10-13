package overlay

import (
	"slices"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// field is a single-line text input with a cursor, edited with readline
// keys. Positions are rune indexes.
type field struct {
	r   []rune
	pos int // 0..len(r)
}

func newField(s string) field {
	r := []rune(s)
	return field{r: r, pos: len(r)}
}

func (f *field) handle(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "left", "ctrl+b":
		f.pos = max(f.pos-1, 0)
	case "right", "ctrl+f":
		f.pos = min(f.pos+1, len(f.r))
	case "home", "ctrl+a":
		f.pos = 0
	case "end", "ctrl+e":
		f.pos = len(f.r)
	case "alt+left", "alt+b":
		f.pos = f.wordLeft()
	case "alt+right", "alt+f":
		f.pos = f.wordRight()
	case "backspace":
		if f.pos > 0 {
			f.cut(f.pos-1, f.pos)
		}
	case "delete":
		if f.pos < len(f.r) {
			f.cut(f.pos, f.pos+1)
		}
	case "ctrl+w":
		f.cut(f.wordLeft(), f.pos)
	case "ctrl+u":
		f.cut(0, f.pos)
	case "ctrl+k":
		f.cut(f.pos, len(f.r))
	default:
		if msg.Text != "" && !msg.Mod.Contains(tea.ModCtrl) && !msg.Mod.Contains(tea.ModAlt) {
			f.insert([]rune(msg.Text))
		}
	}
}

// paste inserts a terminal paste at the cursor. The field is single-line,
// so newlines and other control characters are dropped (password managers
// often copy a trailing newline).
func (f *field) paste(s string) {
	var rs []rune
	for _, r := range s {
		if !unicode.IsControl(r) {
			rs = append(rs, r)
		}
	}
	f.insert(rs)
}

// replace swaps the runes from start up to the cursor for text.
func (f *field) replace(start int, text string) {
	f.cut(min(max(start, 0), f.pos), f.pos)
	f.insert([]rune(text))
}

func (f *field) String() string { return string(f.r) }
func (f *field) Len() int       { return len(f.r) }
func (f *field) Pos() int       { return f.pos }

func (f *field) insert(rs []rune) {
	f.r = slices.Insert(f.r, f.pos, rs...)
	f.pos += len(rs)
}

func (f *field) cut(from, to int) {
	f.r = slices.Delete(f.r, from, to)
	f.pos = from
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordLeft is where the word before the cursor starts, past any non-word
// runes in between.
func (f *field) wordLeft() int {
	i := f.pos
	for i > 0 && !isWordRune(f.r[i-1]) {
		i--
	}
	for i > 0 && isWordRune(f.r[i-1]) {
		i--
	}
	return i
}

// wordRight is where the next word after the cursor starts.
func (f *field) wordRight() int {
	i := f.pos
	for i < len(f.r) && isWordRune(f.r[i]) {
		i++
	}
	for i < len(f.r) && !isWordRune(f.r[i]) {
		i++
	}
	return i
}

// View draws the text with the cursor as a reversed cell, scrolled so the
// cursor stays within width columns; … marks text cut off at either side.
func (f *field) View(width int) string { return f.view(width, 0) }

// ViewMasked is View with every rune drawn as mask.
func (f *field) ViewMasked(width int, mask rune) string { return f.view(width, mask) }

func (f *field) view(width int, mask rune) string {
	width = max(width, 1)
	n := len(f.r)
	cells := make([]string, n+1) // the last cell is the cursor past the end
	ws := make([]int, n+1)
	for i, r := range f.r {
		if mask != 0 {
			r = mask
		}
		cells[i] = textutil.Sanitize(string(r))
		ws[i] = ansi.StringWidth(cells[i])
	}
	cells[n], ws[n] = " ", 1

	// The window [from, to) holds the cursor; a little of the text after it
	// stays in view, then the text before it, then the rest after it.
	from, to, used := f.pos, f.pos+1, ws[f.pos]
	grow := func(limit int) {
		for to <= n && used+ws[to] <= limit {
			used += ws[to]
			to++
		}
	}
	grow(min(width, used+width/4))
	for from > 0 && used+ws[from-1] <= width {
		from--
		used += ws[from]
	}
	grow(width)

	// Make room for a … at each cut-off side, never dropping the cursor.
	left := from > 0
	if left {
		for used+1 > width && from < f.pos {
			used -= ws[from]
			from++
		}
		if left = used+1 <= width; left {
			used++
		}
	}
	right := to <= n
	if right {
		for used+1 > width && to-1 > f.pos {
			to--
			used -= ws[to]
		}
		right = used+1 <= width
	}

	out := slices.Clone(cells[from:to])
	c := f.pos - from
	out[c] = theme.Selected.Render(out[c])
	s := strings.Join(out, "")
	if left {
		s = "…" + s
	}
	if right {
		s += "…"
	}
	return s
}
