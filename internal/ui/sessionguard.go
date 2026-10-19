package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/shepherd-yun/sqlvim/internal/config"
)

// sessionAction is what the server tab does to a session.
type sessionAction int

const (
	actCancel sessionAction = iota
	actDisconnect
)

// sessionTarget is the session row an action is for.
type sessionTarget struct {
	ID, User, Host, DB, State, Elapsed, SQL string
	Self                                    bool
}

// sessionCheck is guardSession's decision: refused (Refuse is the status
// line), or confirmed with Title and Body, typing Expect ("" is press y).
type sessionCheck struct {
	Refuse string
	Title  string
	Body   []string
	Expect string
}

// guardSession checks cancelling or disconnecting s, as Redis's client
// kill is checked: refused on read-only, confirmed everywhere, with the
// connection's name typed on prod. Lines fit in width.
func guardSession(cfg config.Connection, a sessionAction, s sessionTarget, width int) sessionCheck {
	if cfg.IsReadOnly() {
		return sessionCheck{Refuse: "read-only connection: cannot cancel or disconnect sessions"}
	}
	c := sessionCheck{Title: "Cancel the query of session " + s.ID + "?"}
	if a == actDisconnect {
		c.Title = "Disconnect session " + s.ID + "?"
	}
	fit := func(l string) string { return ansi.Truncate(l, width, "…") }
	c.Body = []string{
		fit("user " + s.User + " · host " + s.Host + " · db " + s.DB),
		fit(s.State + " · " + s.Elapsed),
	}
	if sql := strings.Join(strings.Fields(s.SQL), " "); sql != "" {
		c.Body = append(c.Body, fit(sql))
	}
	if a == actDisconnect {
		c.Body = append(c.Body, "Its open transaction, if any, is rolled back.")
	}
	if s.Self {
		c.Body = append(c.Body, "This is a sqlvim connection (maybe this one).")
	}
	if cfg.Env == config.EnvProd {
		c.Expect = cfg.Name
	}
	return c
}
