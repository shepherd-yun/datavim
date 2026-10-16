package rcmd

import (
	"strings"
	"testing"
)

func TestTxState(t *testing.T) {
	type step struct {
		cmd    string
		failed bool
		want   TxState
		badge  string
	}
	for name, steps := range map[string][]step{
		"multi exec": {
			{"MULTI", false, TxState{InMulti: true}, "TX 0"},
			{"SET a 1", false, TxState{InMulti: true, Queued: 1}, "TX 1"},
			{"EXEC", false, TxState{}, ""},
		},
		"watch unwatch": {
			{"WATCH k", false, TxState{Watching: true}, "WATCH"},
			{"UNWATCH", false, TxState{}, ""},
		},
		"multi failed": {
			{"MULTI", true, TxState{}, ""},
		},
		"discard": {
			{"multi", false, TxState{InMulti: true}, "TX 0"},
			{"discard", false, TxState{}, ""},
		},
		"execabort": {
			{"MULTI", false, TxState{InMulti: true}, "TX 0"},
			{"FOO", true, TxState{InMulti: true}, "TX 0"},
			{"EXEC", true, TxState{}, ""},
		},
		"watch multi exec": {
			{"WATCH k", false, TxState{Watching: true}, "WATCH"},
			{"MULTI", false, TxState{InMulti: true, Watching: true}, "TX 0"},
			{"EXEC", false, TxState{}, ""},
		},
		"watch failed": {
			{"WATCH k", true, TxState{}, ""},
		},
	} {
		var s TxState
		for _, st := range steps {
			s.Apply(strings.Fields(st.cmd), st.failed)
			if s != st.want || s.Badge() != st.badge || s.Active() != (st.want.InMulti || st.want.Watching) {
				t.Errorf("%s: after %q = %+v %q, want %+v %q", name, st.cmd, s, s.Badge(), st.want, st.badge)
			}
		}
	}
}
