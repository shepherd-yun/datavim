package rcmd

import "strings"

// CompKind is what the word at the cursor is.
type CompKind int

const (
	CompNone CompKind = iota
	CompCommand
	CompSubcommand
	CompKey
)

// Completion is the completion context at a cursor in one command line.
type Completion struct {
	Kind    CompKind
	Prefix  string  // what is typed of the word (raw bytes, without its quote)
	Start   int     // byte offset in the line of the word to replace
	Command Command // the command for CompSubcommand and CompKey
	Hint    string  // the argument hint: Command.Name + " " + Command.Args
}

// CompleteAt says what to complete at byte offset in line: the command
// name, a container's subcommand, or a key where the command takes one.
func CompleteAt(line string, offset int) Completion {
	toks := Tokens(line[:offset])
	var cur Token
	if n := len(toks); n > 0 && toks[n-1].End == offset && (offset == 0 || !isSpace(line[offset-1]) || toks[n-1].Open) {
		cur, toks = toks[n-1], toks[:n-1]
	} else {
		cur = Token{Start: offset, End: offset}
	}
	c := Completion{Prefix: cur.Text, Start: cur.Start}
	if len(toks) == 0 {
		c.Kind = CompCommand
		return c
	}
	args := make([]string, 0, len(toks)+1)
	for _, t := range toks {
		args = append(args, t.Text)
	}
	args = append(args, cur.Text)
	i := len(args) - 1
	cmd, ok := Lookup(args)
	if cmd.Name != "" {
		c.Command = cmd
		c.Hint = strings.TrimSpace(cmd.Name + " " + cmd.Args)
	}
	switch {
	case cmd.Subcommands && i == 1:
		c.Kind = CompSubcommand
	case ok && KeyArg(cmd, args, i):
		c.Kind = CompKey
	}
	return c
}
