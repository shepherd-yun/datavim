package ui

import (
	"fmt"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/history"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
)

// recentLimit bounds the <space>f list.
const recentLimit = 100

type (
	recentLoadedMsg struct {
		items []history.Recent
		err   error
	}
	recentPickedMsg struct{ r history.Recent }
)

// recordRecent saves that r was opened now.
func recordRecent(st Store, r history.Recent) tea.Cmd {
	r.Time = time.Now()
	return func() tea.Msg {
		if err := st.AddRecent(r); err != nil {
			slog.Warn("save recent", "err", err)
		}
		return nil
	}
}

func loadRecent(st Store) tea.Cmd {
	return func() tea.Msg {
		items, err := st.Recent(recentLimit)
		return recentLoadedMsg{items: items, err: err}
	}
}

var recentKinds = map[string]string{
	history.RecentData:      "data",
	history.RecentStructure: "struct",
	history.RecentConsole:   "console",
}

func recentLabel(r history.Recent) string {
	path := r.Conn
	for _, p := range []string{r.Database, r.Schema, r.Name} {
		if p != "" {
			path += "/" + p
		}
	}
	return fmt.Sprintf("%-8s %s  %s", recentKinds[r.Kind], path, r.Time.Local().Format("01-02 15:04"))
}

func (m *Model) recentLoaded(msg recentLoadedMsg) {
	switch {
	case msg.err != nil:
		m.setStatus("recent: "+msg.err.Error(), true)
	case len(msg.items) == 0:
		m.setStatus("nothing opened yet", false)
	default:
		labels := make([]string, len(msg.items))
		for i, r := range msg.items {
			labels[i] = recentLabel(r)
		}
		items := msg.items
		m.overlay = overlay.NewPicker("Recent", labels, func(i int) tea.Msg { return recentPickedMsg{r: items[i]} })
	}
}

// openRecent reopens (or switches to) a recent tab.
func (m *Model) openRecent(r history.Recent) tea.Cmd {
	if _, ok := m.backend.Connection(r.Conn); !ok {
		m.setStatus("connection "+r.Conn+" no longer exists", true)
		return nil
	}
	ns := db.Namespace{Database: r.Database, Schema: r.Schema}
	switch r.Kind {
	case history.RecentData:
		return m.openData(r.Conn, db.TableRef{Namespace: ns, Name: r.Name})
	case history.RecentStructure:
		return m.openStructure(r.Conn, db.TableRef{Namespace: ns, Name: r.Name})
	case history.RecentConsole:
		return m.openConsole(r.Conn, ns)
	}
	return nil
}

// touchActive records the active tab as just used.
func (m *Model) touchActive() tea.Cmd {
	t := m.activeTab()
	if t == nil {
		return nil
	}
	conn, ref := t.Target()
	r := history.Recent{Conn: conn, Database: ref.Database, Schema: ref.Schema, Name: ref.Name}
	switch t.(type) {
	case *dataTab:
		r.Kind = history.RecentData
	case *structureTab:
		r.Kind = history.RecentStructure
	case *consoleTab:
		r.Kind, r.Name = history.RecentConsole, ""
	default:
		return nil
	}
	return recordRecent(m.store, r)
}
