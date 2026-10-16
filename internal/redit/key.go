package redit

import (
	"errors"
	"strconv"
	"strings"
)

// Caps is what the server's version allows.
type Caps struct{ KeepTTL bool }

// CapsFor reads redis_version ("7.2.4", "5.0.14"); KeepTTL from 6.0. ""
// or unparsable → no caps.
func CapsFor(version string) Caps {
	parts := strings.SplitN(version, ".", 3)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return Caps{}
	}
	if len(parts) > 1 {
		if _, err := strconv.Atoi(parts[1]); err != nil {
			return Caps{}
		}
	}
	return Caps{KeepTTL: major >= 6}
}

// String plans SET of a whole string value (only when it was read in full).
func String(key, old, value string, c Caps) Plan {
	p := Plan{
		Key:    key,
		Checks: []Check{{Args: []string{"GET", key}, Expect: ExpectValue, Value: old, What: "the value changed"}},
		N:      1,
	}
	if c.KeepTTL {
		p.Cmds = [][]string{{"SET", key, value, "KEEPTTL"}}
	} else {
		p.Cmds = [][]string{{"SET", key, value}}
		p.RestoreTTL = true
	}
	return p
}

// Types are the types NewKey makes, in the order the picker lists them.
var Types = []string{"string", "hash", "list", "set", "zset", "stream"}

// Usage is what NewKey wants after the key for typ, for the prompt's label.
func Usage(typ string) string {
	switch typ {
	case "string":
		return "value"
	case "hash", "stream":
		return "field value [field value …]"
	case "list":
		return "element [element …]"
	case "set":
		return "member [member …]"
	case "zset":
		return "score member [score member …]"
	}
	return ""
}

// NewKey plans creating key of typ with args (as rcmd.Split cut the
// prompt's text).
func NewKey(typ, key string, args []string) (Plan, error) {
	bad := errors.New("expected " + Usage(typ))
	pairs := len(args) > 0 && len(args)%2 == 0
	var cmd []string
	switch typ {
	case "string":
		if len(args) != 1 {
			return Plan{}, bad
		}
		cmd = []string{"SET", key}
	case "hash":
		if !pairs {
			return Plan{}, bad
		}
		cmd = []string{"HSET", key}
	case "list":
		if len(args) == 0 {
			return Plan{}, bad
		}
		cmd = []string{"RPUSH", key}
	case "set":
		if len(args) == 0 {
			return Plan{}, bad
		}
		cmd = []string{"SADD", key}
	case "zset":
		if !pairs {
			return Plan{}, bad
		}
		for i := 0; i < len(args); i += 2 {
			if _, err := strconv.ParseFloat(args[i], 64); err != nil {
				return Plan{}, bad
			}
		}
		cmd = []string{"ZADD", key}
	case "stream":
		if !pairs {
			return Plan{}, bad
		}
		cmd = []string{"XADD", key, "*"}
	default:
		return Plan{}, errors.New("cannot create " + typ + " keys")
	}
	return Plan{
		Key:    key,
		Checks: []Check{{Args: []string{"EXISTS", key}, Expect: ExpectInt, Int: 0, What: "key already exists"}},
		Cmds:   [][]string{append(cmd, args...)},
		N:      1,
	}, nil
}
