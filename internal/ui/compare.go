package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/shepherd-yun/sqlvim/internal/db"
	"github.com/shepherd-yun/sqlvim/internal/ui/keymap"
	"github.com/shepherd-yun/sqlvim/internal/ui/overlay"
	"github.com/shepherd-yun/sqlvim/internal/ui/panes/tree"
)

// compareSourceMsg is the source picked for a comparison.
type compareSourceMsg struct{ src, tgt compareSide }

// compareSideOf is node n as a side of a comparison, or why it cannot be
// one: tables, MySQL databases and PostgreSQL schemas compare.
func (m *Model) compareSideOf(n *tree.Node) (compareSide, string) {
	cfg, _ := m.backend.Connection(n.Conn)
	s := compareSide{conn: n.Conn, driver: cfg.Driver}
	switch {
	case n.Kind == tree.KindTable:
		s.ref = n.TableRef()
	case n.Kind == tree.KindView:
		return s, "views are not compared"
	case n.Kind == tree.KindSchema, n.Kind == tree.KindDatabase && cfg.Driver != "postgres":
		s.ref = db.TableRef{Namespace: n.NS}
	case n.Kind == tree.KindDatabase:
		return s, "PostgreSQL compares schemas · select a schema"
	default:
		return s, "select a table, a schema or a database to compare"
	}
	return s, ""
}

// compareProblem says why src cannot be compared with tgt ("" when it can).
func compareProblem(src, tgt compareSide) string {
	switch {
	case src.isTable() != tgt.isTable():
		return "compare a table with a table, a schema or a database with another"
	case src.driver != tgt.driver:
		return "cannot compare " + src.driver + " with " + tgt.driver
	case src.conn == tgt.conn && src.ref == tgt.ref:
		return "the source and the target are the same"
	}
	return ""
}

func (m *Model) clearCompareMark() {
	m.compareMark = nil
	m.tree.SetMark(nil)
}

// markCompare marks the selected node as the compare source, or clears the
// mark when it is on that node already.
func (m *Model) markCompare() {
	n := m.tree.Selected()
	if n == nil {
		return
	}
	s, problem := m.compareSideOf(n)
	if problem != "" {
		m.setStatus(problem, true)
		return
	}
	if m.compareMark != nil && *m.compareMark == s {
		m.clearCompareMark()
		m.setStatus("compare mark cleared", false)
		return
	}
	m.compareMark = &s
	k := n.MarkKey()
	m.tree.SetMark(&k)
	m.setStatus(joinHints("compare source: "+s.label(), keyHint(m.keys, keymap.Tree, keymap.Compare, "on the target compares")), false)
}

// compare compares the marked node with the selected one; without a mark
// it asks for the source.
func (m *Model) compare() tea.Cmd {
	n := m.tree.Selected()
	if n == nil {
		return nil
	}
	tgt, problem := m.compareSideOf(n)
	if problem != "" {
		m.setStatus(problem, true)
		return nil
	}
	if m.compareMark == nil {
		return m.pickCompareSource(tgt)
	}
	return m.startCompare(*m.compareMark, tgt)
}

func (m *Model) startCompare(src, tgt compareSide) tea.Cmd {
	if p := compareProblem(src, tgt); p != "" {
		m.setStatus(p, true)
		return nil
	}
	m.clearCompareMark()
	return m.openDiff(src, tgt)
}

// pickCompareSource offers the sides the loaded catalogs know. It loads
// the target's catalog meanwhile, so that the next try has more to offer.
func (m *Model) pickCompareSource(tgt compareSide) tea.Cmd {
	load := m.ensureMeta(tgt.conn, tgt.ref.Database)
	cands := m.compareCandidates(tgt)
	if len(cands) == 0 {
		m.setStatus(joinHints("nothing to compare with", keyHint(m.keys, keymap.Tree, keymap.MarkCompare, "marks the source")), true)
		return load
	}
	labels := make([]string, len(cands))
	for i, c := range cands {
		labels[i] = c.label()
	}
	m.overlay = overlay.NewPicker("Compare "+tgt.label()+" with", labels, func(i int) tea.Msg {
		return compareSourceMsg{src: cands[i], tgt: tgt}
	})
	return load
}

// compareCandidates lists what tgt can be compared with: the tables (for a
// table) or namespaces (for a namespace) of the loaded catalogs, on
// connections of tgt's engine.
func (m *Model) compareCandidates(tgt compareSide) []compareSide {
	var out []compareSide
	for _, snap := range m.catalogs.Snapshots("") {
		cfg, ok := m.backend.Connection(snap.Key.Conn)
		if !ok || cfg.Driver != tgt.driver {
			continue
		}
		add := func(ref db.TableRef) {
			if s := (compareSide{conn: snap.Key.Conn, driver: cfg.Driver, ref: ref}); compareProblem(s, tgt) == "" {
				out = append(out, s)
			}
		}
		if tgt.isTable() {
			for _, t := range snap.Catalog.Tables {
				if t.Kind == db.TableKindTable {
					add(db.TableRef{Namespace: t.Namespace, Name: t.Name})
				}
			}
			continue
		}
		for _, ns := range snap.Catalog.Namespaces {
			add(db.TableRef{Namespace: ns})
		}
	}
	return out
}
