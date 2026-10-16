package rcmd

import (
	"slices"
	"strconv"
	"strings"
)

// Command is one entry of the built-in command table (commands_gen.go).
type Command struct {
	Name    string // upper case; a subcommand is "CONFIG SET"
	Args    string // argument hint, such as "key field value [field value ...]"
	Summary string
	Group   string   // "string", "hash", "server" …
	Flags   []string // COMMAND INFO flags: write, readonly, admin, blocking …
	ACL     []string // ACL categories without @: dangerous, keyspace …
	// FirstKey, LastKey and Step are COMMAND INFO's key positions, counted
	// from the command name (also for subcommands). FirstKey 0 means no
	// keys; LastKey -1 is the last argument; Step 0 counts as 1.
	FirstKey, LastKey, Step int
	Subcommands             bool // a container such as CONFIG or CLIENT
}

var (
	byName   = map[string]*Command{}
	topNames []string
	subNames = map[string][]string{}
)

func init() {
	for i := range commandTable {
		c := &commandTable[i]
		byName[c.Name] = c
		if parent, sub, ok := strings.Cut(c.Name, " "); ok {
			subNames[parent] = append(subNames[parent], sub)
		} else {
			topNames = append(topNames, c.Name)
		}
	}
	slices.Sort(topNames)
	for _, s := range subNames {
		slices.Sort(s)
	}
}

// Lookup finds the command for args, case-insensitively; for a container
// it looks at args[1] for the subcommand. An unknown subcommand returns the
// container itself with ok false.
func Lookup(args []string) (Command, bool) {
	if len(args) == 0 {
		return Command{}, false
	}
	c, ok := byName[strings.ToUpper(args[0])]
	if !ok {
		return Command{}, false
	}
	if !c.Subcommands {
		return *c, true
	}
	if len(args) > 1 {
		if s, ok := byName[c.Name+" "+strings.ToUpper(args[1])]; ok {
			return *s, true
		}
	}
	return *c, false
}

// Names is every top-level command name, upper case and sorted.
func Names() []string { return topNames }

// SubNames is a container's subcommand names without the container,
// upper case and sorted.
func SubNames(container string) []string { return subNames[strings.ToUpper(container)] }

// numkeys are the commands whose keys are counted by args[2].
var numkeys = map[string]bool{
	"EVAL": true, "EVALSHA": true, "EVAL_RO": true, "EVALSHA_RO": true, "FCALL": true, "FCALL_RO": true,
}

// KeyArg reports whether args[i] (0 is the command name) is a key of c.
func KeyArg(c Command, args []string, i int) bool {
	if numkeys[c.Name] {
		if len(args) < 3 {
			return false
		}
		n, err := strconv.Atoi(args[2])
		return err == nil && i >= 3 && i < 3+n
	}
	if c.FirstKey <= 0 || i < c.FirstKey {
		return false
	}
	last := c.LastKey
	if last < 0 {
		last += len(args)
	}
	step := max(c.Step, 1)
	return i <= last && (i-c.FirstKey)%step == 0
}

var withScores = map[string]bool{
	"ZRANGE": true, "ZRANGEBYSCORE": true, "ZREVRANGE": true, "ZREVRANGEBYSCORE": true,
	"ZRANDMEMBER": true, "ZUNION": true, "ZINTER": true, "ZDIFF": true,
}

// PairReply reports whether the command's RESP2 reply is a flat array of
// pairs (field value … or member score …).
func PairReply(args []string) bool {
	if len(args) == 0 {
		return false
	}
	name := strings.ToUpper(args[0])
	has := func(word string) bool {
		return slices.ContainsFunc(args[1:], func(a string) bool { return strings.EqualFold(a, word) })
	}
	switch {
	case name == "HGETALL":
		return true
	case name == "CONFIG":
		return len(args) > 1 && strings.EqualFold(args[1], "GET")
	case name == "HRANDFIELD":
		return has("WITHVALUES")
	case withScores[name]:
		return has("WITHSCORES")
	case name == "ZPOPMIN" || name == "ZPOPMAX":
		return len(args) > 2
	}
	return false
}
