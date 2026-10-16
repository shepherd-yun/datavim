package rcmd

import (
	"strconv"
	"strings"
)

// TxState follows MULTI / EXEC / DISCARD / WATCH from the commands that ran.
type TxState struct {
	InMulti  bool
	Watching bool
	Queued   int
}

// Apply records one command that ran; failed means the server replied
// with an error.
func (s *TxState) Apply(args []string, failed bool) {
	if len(args) == 0 {
		return
	}
	switch strings.ToUpper(args[0]) {
	case "MULTI":
		if !failed {
			s.InMulti, s.Queued = true, 0
		}
	case "EXEC", "DISCARD":
		// Both end the transaction and drop WATCH, also on EXECABORT.
		*s = TxState{}
	case "WATCH":
		if !failed && !s.InMulti {
			s.Watching = true
		}
	case "UNWATCH":
		if !s.InMulti {
			s.Watching = false
			return
		}
		fallthrough
	default:
		if s.InMulti && !failed {
			s.Queued++
		}
	}
}

// Active reports whether the connection holds a MULTI or a WATCH.
func (s TxState) Active() bool { return s.InMulti || s.Watching }

// Badge is the status bar mark: "TX 3" in MULTI with 3 queued, "WATCH", "".
func (s TxState) Badge() string {
	switch {
	case s.InMulti:
		return "TX " + strconv.Itoa(s.Queued)
	case s.Watching:
		return "WATCH"
	}
	return ""
}
