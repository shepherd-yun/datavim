package rcmd

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Level is how much a command can do to the server.
type Level int

const (
	Read Level = iota
	Write
	Dangerous
	Unsupported
	Unknown
)

func (l Level) String() string {
	switch l {
	case Read:
		return "read"
	case Write:
		return "write"
	case Dangerous:
		return "dangerous"
	case Unsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// unsupported commands would put the connection out of step with sqlvim
// (pub/sub, MONITOR, protocol or reply changes, QUIT).
var unsupported = set("SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE", "MONITOR", "SYNC", "PSYNC",
	"CLIENT TRACKING", "CLIENT REPLY", "RESET", "HELLO", "QUIT")

// overrides are the levels that must be right whatever the table's flags
// say; "X *" covers every subcommand of X (and X alone).
var overrides = func() map[string]Level {
	m := map[string]Level{}
	for _, n := range []string{
		"EVAL_RO", "EVALSHA_RO", "FCALL_RO", "SELECT", "PING", "INFO", "CONFIG GET", "CLIENT LIST",
		"CLIENT INFO", "CLIENT GETNAME", "CLIENT ID", "SLOWLOG GET", "SLOWLOG LEN", "MEMORY USAGE",
		"OBJECT ENCODING", "MULTI", "EXEC", "DISCARD", "WATCH", "UNWATCH", "AUTH", "ECHO", "TIME",
		"DBSIZE", "ROLE", "LASTSAVE", "COMMAND *",
	} {
		m[n] = Read
	}
	for _, n := range []string{"EVAL", "EVALSHA", "FCALL", "SCRIPT LOAD", "FUNCTION LOAD"} {
		m[n] = Write
	}
	for _, n := range []string{
		"FLUSHDB", "FLUSHALL", "KEYS", "CONFIG SET", "CONFIG REWRITE", "CONFIG RESETSTAT", "SHUTDOWN",
		"DEBUG", "SCRIPT FLUSH", "SCRIPT KILL", "FUNCTION FLUSH", "FUNCTION DELETE", "FUNCTION RESTORE",
		"FUNCTION KILL", "CLIENT KILL", "CLIENT PAUSE", "CLIENT NO-EVICT", "REPLICAOF", "SLAVEOF",
		"MIGRATE", "ACL SETUSER", "ACL DELUSER", "ACL LOAD", "ACL SAVE", "SAVE", "BGSAVE",
		"BGREWRITEAOF", "SWAPDB", "MODULE LOAD", "MODULE LOADEX", "MODULE UNLOAD", "FAILOVER",
		"CLUSTER *", "SLOWLOG RESET", "LATENCY RESET", "MEMORY PURGE",
	} {
		m[n] = Dangerous
	}
	return m
}()

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Classify levels one command; name is what messages call it ("CONFIG
// SET", "FOO").
func Classify(args []string) (level Level, name string) {
	if len(args) == 0 {
		return Unknown, ""
	}
	c, ok := Lookup(args)
	top := strings.ToUpper(args[0])
	name = top
	switch {
	case ok:
		name = c.Name
	case c.Subcommands && len(args) > 1:
		name = top + " " + strings.ToUpper(args[1])
	}
	if unsupported[name] || unsupported[top] {
		return Unsupported, name
	}
	if l, hit := overrides[name]; hit {
		return l, name
	}
	if l, hit := overrides[top+" *"]; hit {
		return l, name
	}
	if !ok {
		return Unknown, name
	}
	if slices.Contains(c.Flags, "write") {
		return Write, name
	}
	if slices.Contains(c.Flags, "admin") || slices.Contains(c.ACL, "dangerous") {
		return Dangerous, name
	}
	return Read, name
}

// Verdict is what Guard decides for a batch.
type Verdict int

const (
	Allow Verdict = iota
	Reject
	Confirm
)

// Policy is what Guard checks a batch against.
type Policy struct {
	ReadOnly, Prod bool
	Conn           string // the name a dangerous command on prod asks for
	DBSize         int64  // looked up by the caller for a batch with KEYS; -1 when not
	InMulti        bool   // a MULTI was already open before the batch
}

// Check is Guard's decision. Line is the index in lines of the command
// that caused it; Expect is the text a Confirm asks for (empty: press y).
type Check struct {
	Verdict Verdict
	Line    int
	Reason  string
	Title   string
	Body    []string
	Expect  string
}

// KeysLimit is the DBSIZE above which KEYS is refused.
const KeysLimit = 10000

// Guard checks the whole batch before anything runs: one rejected line
// rejects all of it, and everything that needs confirming goes into one
// confirmation. Lines that do not split are rejected with the split error.
func Guard(lines []Line, p Policy) Check {
	inMulti := p.InMulti
	confirm := -1
	danger := false
	var items []string
	for i, l := range lines {
		args, err := Split(l.Text)
		if err != nil {
			reason := err.Error()
			var se *SplitError
			if errors.As(err, &se) {
				reason = fmt.Sprintf("line %d, col %d: %s", l.Num, se.Col, se.Msg)
			}
			return Check{Verdict: Reject, Line: i, Reason: reason}
		}
		level, name := Classify(args)
		reject := func(reason string) Check { return Check{Verdict: Reject, Line: i, Reason: reason} }
		switch {
		case level == Unsupported:
			return reject(name + " is not supported in sqlvim")
		case p.ReadOnly && level != Read:
			return reject(fmt.Sprintf("%s is a %s command and the connection is read-only · :set noreadonly to allow it", name, level))
		case name == "SELECT" && inMulti:
			return reject("SELECT inside MULTI is not supported · run it before MULTI")
		case name == "KEYS" && p.DBSize > KeysLimit:
			return reject(fmt.Sprintf("KEYS on %d keys is refused · use SCAN or the tree filter", p.DBSize))
		}
		switch name {
		case "MULTI":
			inMulti = true
		case "EXEC", "DISCARD":
			inMulti = false
		}
		if level == Dangerous || p.Prod && (level == Write || level == Unknown) {
			if confirm < 0 {
				confirm = i
			}
			danger = danger || level == Dangerous
			items = append(items, level.String()+": "+clip(Redact(args), 60))
		}
	}
	if confirm < 0 {
		return Check{Verdict: Allow}
	}
	c := Check{Verdict: Confirm, Line: confirm}
	switch {
	case danger:
		c.Title = "Dangerous command"
		c.Body = append([]string{fmt.Sprintf("%d command(s) need confirming:", len(items))}, items...)
		if p.Prod {
			c.Title = "PROD · " + c.Title
			c.Expect = p.Conn
		}
	default:
		c.Title = "Write to PROD"
		c.Body = append([]string{fmt.Sprintf("%d command(s) will run on PROD %s:", len(items), p.Conn)}, items...)
	}
	return c
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// NeedsDBSize reports whether the batch has a KEYS, so the caller must
// look up DBSIZE before Guard.
func NeedsDBSize(lines []Line) bool {
	for _, l := range lines {
		if args, err := Split(l.Text); err == nil && len(args) > 0 && strings.EqualFold(args[0], "KEYS") {
			return true
		}
	}
	return false
}

// GuardArgs is Guard over commands sqlvim made itself (each joined with
// Quote).
func GuardArgs(cmds [][]string, p Policy) Check {
	lines := make([]Line, len(cmds))
	for i, cmd := range cmds {
		parts := make([]string, len(cmd))
		for j, a := range cmd {
			parts[j] = Quote(a)
		}
		lines[i] = Line{Text: strings.Join(parts, " "), Num: i + 1}
	}
	return Guard(lines, p)
}
