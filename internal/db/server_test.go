package db

import (
	"errors"
	"testing"
	"time"
)

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{850 * time.Millisecond, "850 ms"},
		{0, "0 ms"},
		{time.Second, "1s"},
		{41 * time.Second, "41s"},
		{4*time.Minute + 12*time.Second, "4m12s"},
		{3*time.Hour + 5*time.Minute + 59*time.Second, "3h05m"},
		{51*time.Hour + 10*time.Minute, "2d03h"},
		{-time.Second, "0 ms"},
	}
	for _, c := range cases {
		if got := FormatElapsed(c.d); got != c.want {
			t.Errorf("FormatElapsed(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestUnavailable(t *testing.T) {
	err := Unavailable("needs PROCESS")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v is not ErrUnavailable", err)
	}
	if err.Error() != "needs PROCESS" {
		t.Fatalf("Error() = %q", err.Error())
	}
}
