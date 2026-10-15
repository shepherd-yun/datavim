package tree

// The vim fold keys: zc, zO, zC, zR, zM, zx. Folding only flips Expanded;
// loaded children stay, so opening again needs no query.

// scope returns n alone, or every root for nil.
func (m *Model) scope(n *Node) []*Node {
	if n == nil {
		return m.roots
	}
	return []*Node{n}
}

// Fold is zc: it closes the selected node, or, on a leaf or a closed
// node, closes its parent and selects it.
func (m *Model) Fold() {
	n := m.Selected()
	if n == nil {
		return
	}
	if n.Expanded && !n.Loading {
		n.Expanded = false
		return
	}
	if p := n.parent; p != nil {
		p.Expanded = false
		m.selected = p
	}
}

// OpenLoaded is zO: it opens n and every loaded node under it (every
// root's for nil) without loading anything. Failed and loading nodes stay
// as they are.
func (m *Model) OpenLoaded(n *Node) {
	var walk func(x *Node)
	walk = func(x *Node) {
		if !x.Expandable() || !x.Loaded || x.Loading || x.Err != "" {
			return
		}
		x.Expanded = true
		for _, c := range x.Children {
			walk(c)
		}
	}
	for _, x := range m.scope(n) {
		walk(x)
	}
}

// CloseAll is zC: it closes n and everything under it (every root for
// nil). A loading node stays open so its "loading…" shows.
func (m *Model) CloseAll(n *Node) {
	var walk func(x *Node)
	walk = func(x *Node) {
		if !x.Loading {
			x.Expanded = false
		}
		for _, c := range x.Children {
			walk(c)
		}
	}
	for _, x := range m.scope(n) {
		walk(x)
	}
	m.cursor(m.visible())
}

// OpenTree is zR: every loaded node opens.
func (m *Model) OpenTree() { m.OpenLoaded(nil) }

// CloseTree is zM: everything closes down to the connections, and the
// cursor goes to its connection.
func (m *Model) CloseTree() {
	sel := m.Selected()
	m.CloseAll(nil)
	for sel != nil && sel.parent != nil {
		sel = sel.parent
	}
	if sel != nil {
		m.selected = sel
	}
}

// FocusPath is zx: everything closes but the path to the cursor.
func (m *Model) FocusPath() {
	sel := m.Selected()
	if sel == nil {
		return
	}
	m.CloseAll(nil)
	for p := sel.parent; p != nil; p = p.parent {
		p.Expanded = true
	}
	m.selected = sel
}
