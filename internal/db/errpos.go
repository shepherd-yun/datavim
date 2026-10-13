package db

import "errors"

// PositionError is a server error that points into the statement that
// caused it, such as a syntax error.
type PositionError struct {
	Err    error
	Offset int // byte offset into the statement text
}

func (e *PositionError) Error() string { return e.Err.Error() }
func (e *PositionError) Unwrap() error { return e.Err }

// ErrorOffset returns where in its statement err points, if it does.
func ErrorOffset(err error) (int, bool) {
	var pe *PositionError
	if errors.As(err, &pe) {
		return pe.Offset, true
	}
	return 0, false
}

// RuneOffset is the byte offset of the n-th rune (0-based) of s, clamped
// to [0, len(s)].
func RuneOffset(s string, n int) int {
	if n <= 0 {
		return 0
	}
	i := 0
	for off := range s {
		if i == n {
			return off
		}
		i++
	}
	return len(s)
}
