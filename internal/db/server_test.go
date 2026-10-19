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

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		v                   string
		major, minor, patch int
		want                bool
	}{
		{"8.0.22", 8, 0, 22, true},
		{"8.0.21-log", 8, 0, 22, false},
		{"8.4.2", 8, 0, 22, true},
		{"5.7.44-log", 8, 0, 0, false},
		{"10.11.6-MariaDB", 8, 0, 22, true},
		{"16.4", 13, 0, 0, true},
		{"12.0.6024.0", 13, 0, 0, false},
		{"23.0.0.0.0", 18, 0, 0, true},
		{"", 1, 0, 0, false},
		{"garbage", 1, 0, 0, false},
	}
	for _, c := range cases {
		if got := VersionAtLeast(c.v, c.major, c.minor, c.patch); got != c.want {
			t.Errorf("VersionAtLeast(%q, %d, %d, %d) = %v", c.v, c.major, c.minor, c.patch, got)
		}
	}
}
