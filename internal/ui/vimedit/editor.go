package vimedit

import (
	"strings"
)

// Mode is the editor's vim mode.
type Mode int

const (
	Normal Mode = iota
	Insert
	Visual
	VisualLine
)

func (m Mode) String() string {
	switch m {
	case Insert:
		return "INSERT"
	case Visual:
		return "VISUAL"
	case VisualLine:
		return "V-LINE"
	}
	return "NORMAL"
}

// Key is one key press: Name as Bubble Tea prints it ("j", "G", "ctrl+r",
// "esc", "enter", "space") and Text for keys that type text.
type Key struct{ Name, Text string }

// TabJump is gt / gT for the host: To is a 1-based tab number ({n}gt),
// otherwise By is a relative move (gt +1, {n}gT -n).
type TabJump struct{ To, By int }

// ClipboardPaste asks the host for the system clipboard ("+p, "+P). The
// host reads it and calls Editor.PasteClipboard.
type ClipboardPaste struct {
	Before  bool
	Count   int
	version int // the text the request was made on
}

// Result tells the host what a key did.
type Result struct {
	Handled   bool            // false: the key means nothing to the editor here
	Clipboard *string         // text yanked or deleted into "+: copy it to the system clipboard
	Paste     *ClipboardPaste // "+p / "+P: read the system clipboard
	Tab       *TabJump        // gt / gT
}

const (
	maxCount   = 9999     // bounds count prefixes
	maxPut     = 16 << 20 // bytes a counted p may insert
	shiftWidth = 4        // > < indent and INSERT-mode tab insert four spaces
	wantEnd    = -1       // after $: j and k stay at line ends
)

type register struct {
	text     string
	linewise bool
}

// cmd is one parsed NORMAL / VISUAL command: ["x][count]op(motion).
type cmd struct {
	reg    rune   // register; 0 is the unnamed one
	count  int    // 0: no count typed
	op     string // d c y > <, a command key (x p o …), or "" for a motion
	motion string // motion or text object: w, gg, $, iw, a(; the operator again for dd
	char   rune   // the argument of f t F T
}

func (c cmd) n() int { return max(c.count, 1) }

// pending is the command being typed.
type pending struct {
	keys   []string // as typed, for Pending
	reg    rune
	count  int    // before the operator
	op     string // operator typed so far
	count2 int    // after the operator
	await  string // the next key completes: `"` a register, g, f t F T a rune, i a an object
}

// Editor is the vim state machine over a text.
type Editor struct {
	buf    buffer
	cur    Pos
	want   int // the column j and k aim for, or wantEnd
	mode   Mode
	anchor Pos // where the visual selection started
	height int // lines on screen; ctrl+d and ctrl+u move half of it

	regs map[rune]register
	hist history
	pend pending
	res  Result

	dot    *change // the last change, for .
	rec    *change // the change in progress until its INSERT session ends
	replay bool    // . is running: do not record
}

// New returns an empty editor in NORMAL mode.
func New() *Editor {
	return &Editor{buf: newBuffer(""), regs: map[rune]register{}, height: 20}
}

func (e *Editor) Text() string { return e.buf.Text() }

// Version changes whenever the text does.
func (e *Editor) Version() int { return e.buf.version }

func (e *Editor) Mode() Mode { return e.mode }

func (e *Editor) Cursor() Pos { return e.cur }

// SetHeight tells the editor how many lines are on screen.
func (e *Editor) SetHeight(h int) { e.height = max(h, 1) }

// SetCursor moves the cursor, clamped to the text and the mode.
func (e *Editor) SetCursor(p Pos) {
	e.cur = e.buf.clamp(p)
	e.fit()
	e.want = e.cur.Col
}

// Offset is the cursor as a byte offset into Text().
func (e *Editor) Offset() int { return e.buf.offset(e.cur) }

// SetOffset moves the cursor to a byte offset into Text().
func (e *Editor) SetOffset(off int) { e.SetCursor(e.buf.pos(off)) }

// SetText replaces the text as a newly loaded one: the undo history is
// cleared and the cursor goes to the end.
func (e *Editor) SetText(s string) {
	v := e.buf.version
	e.buf = newBuffer(s)
	e.buf.version = v + 1
	e.hist.reset()
	e.pend, e.rec = pending{}, nil
	e.SetCursor(e.buf.end())
}

// ReplaceAll replaces the text as one change that u undoes; the cursor
// goes to the end.
func (e *Editor) ReplaceAll(s string) {
	e.hist.commit()
	e.change(Pos{}, e.buf.end(), s)
	e.hist.commit()
	e.SetCursor(e.buf.end())
}

// InsertText inserts s at the cursor: typed text in INSERT mode, a change
// of its own otherwise.
func (e *Editor) InsertText(s string) {
	if e.mode == Insert {
		e.typeText(s)
		e.record(typed{text: s})
		return
	}
	end := e.change(e.cur, e.cur, s)
	e.hist.commit()
	e.SetCursor(Pos{end.Line, end.Col - 1})
}

// ReplaceBeforeCursor replaces the text from byte offset start to the
// cursor with s, leaving the cursor after s (completion).
func (e *Editor) ReplaceBeforeCursor(start int, s string) {
	off := e.Offset()
	start = min(max(start, 0), off)
	e.replaceBefore(off-start, s)
	if e.mode == Insert {
		e.record(typed{text: s, back: off - start})
	} else {
		e.hist.commit()
		e.fit()
	}
}

func (e *Editor) replaceBefore(back int, s string) {
	from := e.buf.pos(e.Offset() - back)
	e.cur = e.change(from, e.cur, s)
}

// Pending is the command typed so far, like vim's showcmd.
func (e *Editor) Pending() string { return strings.Join(e.pend.keys, "") }

// PasteClipboard finishes a "+p / "+P with the clipboard's text. It does
// nothing (false) if the text or the mode changed since the request.
func (e *Editor) PasteClipboard(p ClipboardPaste, text string) bool {
	if p.version != e.buf.version || e.mode != Normal || text == "" {
		return false
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	e.putText(register{text: text, linewise: strings.HasSuffix(text, "\n")}, p.Before, p.Count)
	e.hist.commit()
	e.fit()
	return true
}

// Feed handles one key.
func (e *Editor) Feed(k Key) Result {
	e.res = Result{Handled: true}
	if e.mode == Insert {
		e.insertKey(k)
	} else {
		e.normalKey(k)
	}
	return e.res
}
