package rdb

import (
	"errors"
	"testing"
)

func TestChangedErrorText(t *testing.T) {
	if got := (&ChangedError{}).Error(); got != "key changed since loaded; refresh and retry" {
		t.Fatalf("got %q", got)
	}
	if got := (&ChangedError{What: `field "a" changed`}).Error(); got != `key changed since loaded; refresh and retry · field "a" changed` {
		t.Fatalf("got %q", got)
	}
}

func TestExecErrorText(t *testing.T) {
	inner := errors.New("WRONGTYPE x")
	e := &ExecError{Index: 1, Total: 3, Err: inner}
	if got := e.Error(); got != "command 2 of 3 failed: WRONGTYPE x · the others were applied" {
		t.Fatalf("got %q", got)
	}
	if !errors.Is(e, inner) {
		t.Fatal("ExecError unwraps to the command's error")
	}
}
