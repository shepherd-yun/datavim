// Package keymap maps key sequences to actions per UI context.
package keymap

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Context names the part of the UI that has focus.
type Context string

const (
	Global    Context = "global"
	Tree      Context = "tree"
	Grid      Context = "grid"
	Structure Context = "structure"
	Console   Context = "console"
	Results   Context = "results"
)

// Action is what a key sequence does.
type Action string

const (
	Quit           Action = "app.quit"
	CommandLine    Action = "app.cmdline"
	Help           Action = "app.help"
	FocusNext      Action = "focus.next"
	FocusPrev      Action = "focus.prev"
	FocusLeft      Action = "focus.left"
	FocusRight     Action = "focus.right"
	ToggleTree     Action = "tree.toggle"
	OpenStructure  Action = "structure.open"
	Refresh        Action = "refresh"
	Down           Action = "move.down"
	Up             Action = "move.up"
	Left           Action = "move.left"
	Right          Action = "move.right"
	Top            Action = "move.top"
	Bottom         Action = "move.bottom"
	HalfPageDown   Action = "move.halfpage.down"
	HalfPageUp     Action = "move.halfpage.up"
	LineStart      Action = "move.linestart"
	LineEnd        Action = "move.lineend"
	Expand         Action = "tree.expand"
	Collapse       Action = "tree.collapse"
	Password       Action = "conn.password"
	PrevPage       Action = "page.prev"
	NextPage       Action = "page.next"
	CellDetail     Action = "cell.detail"
	PrevSection    Action = "section.prev"
	NextSection    Action = "section.next"
	Cancel         Action = "query.cancel"
	OpenConsole    Action = "console.open"
	NewConnection  Action = "conn.new"
	History        Action = "history.open"
	ExternalEdit   Action = "editor.external"
	InsertMode     Action = "editor.insert"
	Append         Action = "editor.append"
	RunStatement   Action = "sql.run"
	RunAll         Action = "sql.runall"
	FocusResults   Action = "console.results"
	FocusEditor    Action = "console.editor"
	ToggleMessages Action = "console.messages"
	LoadMore       Action = "results.more"
	Search         Action = "search.start"
	SearchNext     Action = "search.next"
	SearchPrev     Action = "search.prev"
	ScrollLeft     Action = "scroll.left"
	ScrollRight    Action = "scroll.right"
	YankCell       Action = "yank.cell"
	YankRow        Action = "yank.row"
	ToggleTx       Action = "tx.toggle"
)

const (
	copyPrefix   = "copy."
	exportPrefix = "export."
)

// CopyAs is the action that copies the result in format (e.g. "csv").
func CopyAs(format string) Action { return Action(copyPrefix + format) }

// CopyFormat extracts the format from a CopyAs action.
func CopyFormat(a Action) (string, bool) { return strings.CutPrefix(string(a), copyPrefix) }

// ExportAs is the action that exports the result in format (e.g. "xlsx").
func ExportAs(format string) Action { return Action(exportPrefix + format) }

// ExportFormat extracts the format from an ExportAs action.
func ExportFormat(a Action) (string, bool) { return strings.CutPrefix(string(a), exportPrefix) }

const tabPrefix = "tab.goto."

// GotoTab is the action that focuses tab n (1-based).
func GotoTab(n int) Action { return Action(tabPrefix + strconv.Itoa(n)) }

// TabNumber extracts n from a GotoTab action.
func TabNumber(a Action) (int, bool) {
	s, ok := strings.CutPrefix(string(a), tabPrefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// LeaderToken in Binding.Keys is replaced by the configured leader key.
const LeaderToken = "<leader>"

// Binding maps a key sequence to an action within a context.
type Binding struct {
	Context Context
	Keys    []string
	Action  Action
	Desc    string
}

// Result is the outcome of resolving a key sequence.
type Result int

const (
	NoMatch Result = iota
	Pending
	Matched
)

// Hint is one which-key line: the remaining keys and what they do.
type Hint struct {
	Keys string
	Desc string
}

// HelpEntry is one help line; aliases for the same action are merged.
type HelpEntry struct {
	Context Context
	Keys    string
	Desc    string
}

// Keymap resolves key sequences.
type Keymap struct {
	leader   string
	bindings []Binding
}

// New expands the leader token and validates bindings.
func New(leader string, bindings []Binding) (*Keymap, error) {
	k := &Keymap{leader: leader}
	for _, b := range bindings {
		keys := make([]string, len(b.Keys))
		for i, key := range b.Keys {
			if key == LeaderToken {
				key = leader
			}
			keys[i] = key
		}
		b.Keys = keys
		k.bindings = append(k.bindings, b)
	}
	if err := k.validate(); err != nil {
		return nil, err
	}
	return k, nil
}

// Default returns the built-in keymap with <space> as leader.
func Default() *Keymap {
	k, err := New("space", DefaultBindings())
	if err != nil {
		panic(err) // covered by TestDefaultIsValid
	}
	return k
}

// Leader returns the leader key.
func (k *Keymap) Leader() string { return k.leader }

func (k *Keymap) validate() error {
	contexts := map[Context]bool{Global: true}
	seen := map[string]bool{}
	for _, b := range k.bindings {
		contexts[b.Context] = true
		id := string(b.Context) + "\x00" + strings.Join(b.Keys, "\x00")
		if seen[id] {
			return fmt.Errorf("keymap: %s bound twice in context %s", k.Display(b.Keys), b.Context)
		}
		seen[id] = true
	}
	for ctx := range contexts {
		scope := k.scope(ctx)
		for _, a := range scope {
			for _, b := range scope {
				if len(a.Keys) < len(b.Keys) && hasPrefix(b.Keys, a.Keys) {
					return fmt.Errorf("keymap: %s (%s) is a prefix of %s (%s) in context %s",
						k.Display(a.Keys), a.Action, k.Display(b.Keys), b.Action, ctx)
				}
			}
		}
	}
	return nil
}

// scope returns the bindings visible in ctx: ctx first, then unshadowed globals.
func (k *Keymap) scope(ctx Context) []Binding {
	var out []Binding
	seen := map[string]bool{}
	for _, c := range []Context{ctx, Global} {
		for _, b := range k.bindings {
			if b.Context != c {
				continue
			}
			id := strings.Join(b.Keys, "\x00")
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, b)
		}
		if ctx == Global {
			break
		}
	}
	return out
}

// Resolve looks up seq in ctx.
func (k *Keymap) Resolve(ctx Context, seq []string) (Action, Result) {
	pending := false
	for _, b := range k.scope(ctx) {
		if slices.Equal(b.Keys, seq) {
			return b.Action, Matched
		}
		if hasPrefix(b.Keys, seq) {
			pending = true
		}
	}
	if pending {
		return "", Pending
	}
	return "", NoMatch
}

// Continuations lists what can follow prefix in ctx, sorted by key.
func (k *Keymap) Continuations(ctx Context, prefix []string) []Hint {
	var out []Hint
	for _, b := range k.scope(ctx) {
		if len(b.Keys) > len(prefix) && hasPrefix(b.Keys, prefix) {
			out = append(out, Hint{Keys: k.Display(b.Keys[len(prefix):]), Desc: b.Desc})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Keys < out[j].Keys })
	return out
}

// Help lists bindings visible in ctx, merging aliases of the same action.
func (k *Keymap) Help(ctx Context) []HelpEntry {
	var out []HelpEntry
	index := map[string]int{}
	for _, b := range k.scope(ctx) {
		id := string(b.Context) + "\x00" + string(b.Action)
		if i, ok := index[id]; ok {
			out[i].Keys += ", " + k.Display(b.Keys)
			continue
		}
		index[id] = len(out)
		out = append(out, HelpEntry{Context: b.Context, Keys: k.Display(b.Keys), Desc: b.Desc})
	}
	return out
}

// Display renders keys vim-style: "gg", "<space>s", "<ctrl+d>".
func (k *Keymap) Display(keys []string) string {
	var b strings.Builder
	for _, key := range keys {
		if len([]rune(key)) == 1 {
			b.WriteString(key)
		} else {
			b.WriteString("<" + key + ">")
		}
	}
	return b.String()
}

func hasPrefix(keys, prefix []string) bool {
	return len(keys) >= len(prefix) && slices.Equal(keys[:len(prefix)], prefix)
}
