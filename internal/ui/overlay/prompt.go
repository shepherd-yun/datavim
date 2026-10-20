package overlay

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shepherd-yun/sqlvim/internal/ui/panes/complete"
	"github.com/shepherd-yun/sqlvim/internal/ui/textutil"
	"github.com/shepherd-yun/sqlvim/internal/ui/theme"
)

// Completer offers completions for the word before cursor in text; start
// is where that word begins. Both are rune indexes.
type Completer func(text string, cursor int) (start int, cands []complete.Candidate)

// PromptOption configures a Prompt.
type PromptOption func(*Prompt)

// WithCompleter makes tab complete the word before the cursor.
func WithCompleter(c Completer) PromptOption { return func(p *Prompt) { p.complete = c } }

// WithValidate checks the text on enter; an error keeps the prompt open.
func WithValidate(v func(string) error) PromptOption { return func(p *Prompt) { p.validate = v } }

// WithHint shows hint(text) under the input, updated as it is typed; a
// hint flagged bad is drawn as an error.
func WithHint(hint func(text string) (line string, bad bool)) PromptOption {
	return func(p *Prompt) { p.hint = hint }
}

// Prompt asks for one line of text with an editable initial value.
type Prompt struct {
	title, label string
	input        field
	submit       func(string) tea.Msg
	complete     Completer
	validate     func(string) error
	hint         func(string) (string, bool)
	popup        *complete.Model
	popStart     int    // where the completed word starts
	err          string // the last validation error, until the next edit
}

// NewPrompt builds a prompt; submit turns the entered text into a message.
func NewPrompt(title, label, initial string, submit func(string) tea.Msg, opts ...PromptOption) *Prompt {
	p := &Prompt{title: title, label: label, input: newField(initial), submit: submit}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Prompt) HandleKey(msg tea.KeyPressMsg) (Overlay, tea.Cmd) {
	key := msg.String()
	if p.popup != nil {
		switch key {
		case "tab", "down", "ctrl+n", "ctrl+j":
			p.popup.Next()
			return p, nil
		case "shift+tab", "up", "ctrl+p", "ctrl+k":
			p.popup.Prev()
			return p, nil
		case "enter":
			p.input.replace(p.popStart, p.popup.Chosen().Text)
			p.popup = nil
			return p, nil
		case "esc":
			p.popup = nil
			return p, nil
		}
		p.popup = nil
	}
	switch key {
	case "esc":
		return nil, nil
	case "enter":
		text := p.input.String()
		if p.validate != nil {
			if err := p.validate(text); err != nil {
				p.err = err.Error()
				return p, nil
			}
		}
		return nil, emit(p.submit(text))
	case "tab":
		if p.complete != nil {
			p.suggest()
			return p, nil
		}
	}
	p.err = ""
	p.input.handle(msg)
	return p, nil
}

// suggest completes a lone candidate at once, or opens the popup.
func (p *Prompt) suggest() {
	start, cands := p.complete(p.input.String(), p.input.Pos())
	switch len(cands) {
	case 0:
	case 1:
		p.input.replace(start, cands[0].Text)
	default:
		p.popup, p.popStart = complete.New(cands), start
	}
}

func (p *Prompt) View(width, height int) string {
	w := max(min(width-4, 80), 20)
	lines := []string{p.label, "> " + p.input.View(w-4)}
	if p.hint != nil && p.err == "" {
		if h, bad := p.hint(p.input.String()); h != "" {
			st := theme.Muted
			if bad {
				st = theme.Error
			}
			for _, l := range textutil.Wrap(textutil.Sanitize(h), w-2) {
				lines = append(lines, st.Render(l))
			}
		}
	}
	if p.err != "" {
		for _, l := range textutil.Wrap(textutil.Sanitize(p.err), w-2) {
			lines = append(lines, theme.Error.Render(l))
		}
	}
	footer := "enter ok · esc cancel"
	if p.complete != nil {
		footer = "tab complete · " + footer
	}
	lines = append(lines, "", theme.Muted.Render(footer))
	box := theme.Box(p.title, strings.Join(lines, "\n"), w, len(lines)+2, true)
	if p.popup == nil {
		return box
	}
	// The popup hangs below the box, indented past its border; its lines
	// are not padded, so the screen beside it stays visible.
	pop := p.popup.View(min(w-2, 60), max(height-lipgloss.Height(box), 3))
	popLines := strings.Split(pop, "\n")
	for i, l := range popLines {
		popLines[i] = "  " + l
	}
	return box + "\n" + strings.Join(popLines, "\n")
}

// Paste inserts text at the cursor.
func (p *Prompt) Paste(text string) {
	p.popup, p.err = nil, ""
	p.input.paste(text)
}
