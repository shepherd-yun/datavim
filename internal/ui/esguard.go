package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/esx"
	"github.com/shepherd-yun/datavim/internal/ui/overlay"
)

// esProdRefusal is a critical request on a prod connection (spec §9.2).
const esProdRefusal = "refused on a prod connection: name the indices one by one"

// esPlan is a request (or a batch of them) about to be sent, for its
// confirmation.
type esPlan struct {
	Class esx.Class
	// Resolved are the concrete indices a wide target expands to; the
	// request is sent with them, not with the pattern.
	Resolved []string
	Count    int64    // documents a by-query request matches (-1: unknown)
	Lines    []string // the confirmation's body
	// Expect is the name a dangerous request is confirmed with; empty is
	// its target.
	Expect string
	// Method, Path and Body are shown on a prod connection's write.
	Method, Path string
	Body         []byte
}

// esDecision is what a plan needs before it may run.
type esDecision struct {
	refusal string          // refused: why
	confirm overlay.Overlay // asks first
	run     bool            // goes without asking
}

// decideES applies spec §9.2 to a plan on cfg's connection: read-only
// refuses anything but reads; a write asks y (refresh / flush / cache
// clear off prod go at once), on prod showing the request; a dangerous
// request asks for its object's name; a critical one is refused on prod
// and otherwise lists the indices it expands to and asks for the
// expression typed as given. ok is sent once confirmed.
func decideES(cfg config.Connection, p esPlan, ok tea.Msg) esDecision {
	c := p.Class
	prod := cfg.Env == config.EnvProd
	if c.Level == esx.LevelRead {
		return esDecision{run: true}
	}
	if cfg.IsReadOnly() {
		return esDecision{refusal: readOnlyHint}
	}
	title := "Confirm " + c.Op
	lines := append([]string{}, p.Lines...)
	switch c.Level {
	case esx.LevelWrite:
		if c.Quiet && !prod {
			return esDecision{run: true}
		}
		if prod && p.Method != "" {
			lines = append(lines, "", p.Method+" "+p.Path)
			lines = append(lines, firstLines(string(p.Body), 5)...)
		}
		lines = append(lines, "", "y confirms · n / esc cancels")
		return esDecision{confirm: overlay.NewConfirm(title, lines, "", ok)}
	case esx.LevelDanger:
		expect := p.Expect
		if expect == "" {
			expect = strings.Join(c.Targets, ",")
		}
		if expect == "" {
			expect = c.Op
		}
		lines = append(lines, "", "type "+expect+" to confirm")
		return esDecision{confirm: overlay.NewConfirm(title, lines, expect, ok)}
	}
	if prod {
		return esDecision{refusal: esProdRefusal}
	}
	expr := p.Expect
	if expr == "" {
		expr = strings.Join(c.Targets, ",")
	}
	lines = append(lines, "", "matches "+indexCount(len(p.Resolved))+":")
	for _, i := range p.Resolved[:min(len(p.Resolved), 20)] {
		lines = append(lines, "  "+i)
	}
	if n := len(p.Resolved) - 20; n > 0 {
		lines = append(lines, "  … and "+groupDigits(int64(n))+" more")
	}
	lines = append(lines, "", "only these are sent · type "+expr+" to confirm")
	return esDecision{confirm: overlay.NewConfirm(title, lines, expr, ok)}
}

func firstLines(s string, n int) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return lines
}

// confirmES decides a plan for conn: refused plans set the status,
// confirmed ones open the confirmation, the rest send ok at once.
func (m *Model) confirmES(conn string, p esPlan, ok tea.Msg) tea.Cmd {
	cfg, _ := m.backend.Connection(conn)
	d := decideES(cfg, p, ok)
	switch {
	case d.refusal != "":
		m.setStatus(d.refusal, true)
		return nil
	case d.confirm != nil:
		m.overlay = d.confirm
		return nil
	}
	return func() tea.Msg { return ok }
}

// confirmESCmd is confirmES for a tab: setStatus reports a refusal.
func confirmESCmd(cfg config.Connection, p esPlan, ok tea.Msg, setStatus func(string, bool)) tea.Cmd {
	d := decideES(cfg, p, ok)
	switch {
	case d.refusal != "":
		setStatus(d.refusal, true)
		return nil
	case d.confirm != nil:
		return openOverlay(d.confirm)
	}
	return func() tea.Msg { return ok }
}

func indexCount(n int) string {
	if n == 1 {
		return "1 index"
	}
	return groupDigits(int64(n)) + " indices"
}
