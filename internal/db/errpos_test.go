package db

import (
	"errors"
	"fmt"
	"testing"
)

func TestPositionError(t *testing.T) {
	base := errors.New("syntax error at or near \"FORM\"")
	err := fmt.Errorf("run: %w", &PositionError{Err: base, Offset: 9})
	off, ok := ErrorOffset(err)
	if !ok || off != 9 {
		t.Fatalf("ErrorOffset = %d, %v", off, ok)
	}
	if !errors.Is(err, base) {
		t.Fatal("the server error stays reachable")
	}
	if (&PositionError{Err: base}).Error() != base.Error() {
		t.Fatal("the message is unchanged")
	}
	if _, ok := ErrorOffset(base); ok {
		t.Fatal("plain errors have no offset")
	}
}

func TestRuneOffset(t *testing.T) {
	s := "a李b"
	for n, want := range map[int]int{-1: 0, 0: 0, 1: 1, 2: 4, 3: 5, 9: 5} {
		if got := RuneOffset(s, n); got != want {
			t.Errorf("RuneOffset(%d) = %d, want %d", n, got, want)
		}
	}
}
