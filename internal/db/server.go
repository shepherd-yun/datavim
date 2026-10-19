package db

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrUnavailable marks a server status part the server cannot give: a
// missing privilege, extension or version. Its message is the reason.
var ErrUnavailable = errors.New("unavailable")

// ErrSessionChanged is returned by a cancel or kill whose session id now
// belongs to another session.
var ErrSessionChanged = errors.New("session changed")

type unavailable string

func (e unavailable) Error() string   { return string(e) }
func (unavailable) Is(err error) bool { return err == ErrUnavailable }

// Unavailable is ErrUnavailable with the reason the user sees.
func Unavailable(reason string) error { return unavailable(reason) }

// ServerReader reads server-wide status for the server tab. Each method
// is independent: one failing (permission, missing extension, old
// version) must not affect the others.
type ServerReader interface {
	// Info is what does not change while the tab is open.
	Info(ctx context.Context) (ServerInfo, error)
	// Sample is one Status sample: facts plus metrics.
	Sample(ctx context.Context) (ServerSample, error)
	Sessions(ctx context.Context, includeIdle bool) (SessionList, error)
	// Locks lists the sessions waiting for a lock and who blocks them.
	Locks(ctx context.Context) (*ResultSet, error)
	// TopStatements is the statement summary, ordered by total time, at
	// most n rows.
	TopStatements(ctx context.Context, n int) (*ResultSet, error)
	Settings(ctx context.Context) (*ResultSet, error)
	Replication(ctx context.Context) (*ResultSet, error)
	// CancelQuery stops what the session runs; KillSession ends the
	// session. ok is false when it was already gone. Both check first
	// that the session still matches id.Since, and return
	// ErrSessionChanged if not.
	CancelQuery(ctx context.Context, id SessionID) (ok bool, err error)
	KillSession(ctx context.Context, id SessionID) (ok bool, err error)
}

// ServerInfo describes the server and what its status tab can do.
type ServerInfo struct {
	Version       string
	SettingsTitle string // "Variables", "Settings", "Configuration", "Parameters"
	CanCancel     bool   // CancelQuery works on this server
}

// FactKind tells the UI how to format a Fact.
type FactKind int

const (
	FactText FactKind = iota
	FactCount
	FactBytes
	FactPercent
	FactDuration // Num in seconds
)

// Fact is one line of the Status summary.
type Fact struct {
	Label string
	Kind  FactKind
	Text  string  // for FactText
	Num   float64 // for the other kinds
	Of    float64 // an upper bound, as in "87 / 500"; 0 for none
}

// MetricUnit is the unit of a Metric's value.
type MetricUnit int

const (
	UnitNone MetricUnit = iota
	UnitBytes
)

// Metric is one sparkline. Counter metrics are cumulative and the UI turns
// two samples into a per-second rate; gauges are shown as read.
type Metric struct {
	Name    string
	Counter bool
	Value   float64
	Unit    MetricUnit
}

// ServerSample is one Status sample.
type ServerSample struct {
	Facts   []Fact
	Metrics []Metric
	At      time.Time
}

// SessionID names one session and what tells it from a later session with
// the same ID: PostgreSQL's backend_start, SQL Server's login_time; empty
// where IDs are not reused (MySQL until a restart, Oracle's sid,serial#).
type SessionID struct{ ID, Since string }

// SessionSummary is what a confirmation shows of a session; SQL is
// redacted and at most 1024 characters.
type SessionSummary struct{ User, Host, DB, State, Elapsed, SQL string }

// SessionList is the Sessions section. IDs, Info and Self go row by row.
type SessionList struct {
	Rows          *ResultSet
	IDs           []SessionID
	Info          []SessionSummary
	Self          []bool // the session's application name is sqlvim
	Total, Active int
}

// FormatElapsed is 850 ms, 41s, 4m12s, 3h05m or 2d03h.
func FormatElapsed(d time.Duration) string {
	switch {
	case d < 0:
		return "0 ms"
	case d < time.Second:
		return fmt.Sprintf("%d ms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}
