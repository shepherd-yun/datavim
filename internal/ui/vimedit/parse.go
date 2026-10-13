package vimedit

import "unicode/utf8"

func keyRune(k Key) (rune, bool) {
	s := k.Text
	if s == "" {
		s = k.Name
	}
	r, size := utf8.DecodeRuneInString(s)
	return r, size > 0 && size == len(s)
}

func isMotion(name string) bool {
	switch name {
	case "h", "j", "k", "l", "left", "right", "up", "down", "w", "b", "e", "W", "B", "E",
		"0", "^", "$", "G", "%", "{", "}":
		return true
	}
	return false
}

// isFind reports f t F T, which take a rune.
func isFind(name string) bool {
	return name == "f" || name == "t" || name == "F" || name == "T"
}

func isOperator(name string) bool {
	switch name {
	case "d", "c", "y", ">", "<":
		return true
	}
	return false
}

func (e *Editor) normalKey(k Key) {
	p := &e.pend
	name := k.Name
	idle := len(p.keys) == 0
	visual := e.mode == Visual || e.mode == VisualLine
	if name == "esc" {
		if idle && visual {
			e.leaveVisual()
		}
		e.pend = pending{}
		return
	}
	if p.await != "" {
		e.complete(k)
		return
	}
	count := &p.count
	if p.op != "" {
		count = &p.count2
	}
	if len(name) == 1 && (name >= "1" && name <= "9" || name == "0" && *count > 0) {
		*count = min(*count*10+int(name[0]-'0'), maxCount)
		p.keys = append(p.keys, name)
		return
	}
	c := cmd{reg: p.reg, count: p.total()}
	switch {
	case name == `"` && p.op == "":
		p.await = `"`
	case name == "g" || isFind(name):
		p.await = name
	case (name == "i" || name == "a") && (p.op != "" || visual):
		p.await = name
	case isMotion(name):
		c.op, c.motion = p.op, name
		e.pend = pending{}
		e.exec(c)
		return
	case isOperator(name) && visual:
		c.op = name
		e.pend = pending{}
		e.visualOp(c)
		return
	case isOperator(name) && p.op == "":
		p.op = name
	case isOperator(name) && p.op == name:
		c.op, c.motion = name, name
		e.pend = pending{}
		e.exec(c)
		return
	case p.op == "" && e.command(name, visual):
		c.op = name
		e.pend = pending{}
		if visual {
			e.visualOp(c)
		} else {
			e.exec(c)
		}
		return
	default:
		e.res.Handled = !idle
		e.pend = pending{}
		return
	}
	p.keys = append(p.keys, name)
}

// command reports whether name is a command (not a motion or operator)
// in the current mode.
func (e *Editor) command(name string, visual bool) bool {
	if visual {
		switch name {
		case "v", "V", "o", "x", "p", "P", "ctrl+d", "ctrl+u":
			return true
		}
		return false
	}
	switch name {
	case "x", "p", "P", "D", "C", "o", "O", "i", "a", "I", "A", "u", "ctrl+r", ".",
		"v", "V", "ctrl+d", "ctrl+u":
		return true
	}
	return false
}

func (p *pending) total() int {
	switch {
	case p.count == 0:
		return p.count2
	case p.count2 == 0:
		return p.count
	}
	return min(p.count*p.count2, maxCount)
}

// complete finishes a command waiting for one more key.
func (e *Editor) complete(k Key) {
	p := &e.pend
	r, ok := keyRune(k)
	if k.Name == "space" {
		r, ok = ' ', true
	}
	c := cmd{reg: p.reg, count: p.total(), op: p.op}
	await := p.await
	p.await = ""
	visual := e.mode == Visual || e.mode == VisualLine
	switch {
	case !ok:
		e.pend = pending{}
	case await == `"`:
		if r == '"' || r == '+' || r >= 'a' && r <= 'z' {
			p.reg = r
			p.keys = append(p.keys, string(r))
			return
		}
		e.pend = pending{}
	case await == "g" && r == 'g':
		c.motion = "gg"
		e.pend = pending{}
		e.exec(c)
	case await == "g" && (r == 't' || r == 'T') && p.op == "" && !visual:
		e.pend = pending{}
		switch {
		case r == 't' && c.count > 0:
			e.res.Tab = &TabJump{To: c.count}
		case r == 't':
			e.res.Tab = &TabJump{By: 1}
		default:
			e.res.Tab = &TabJump{By: -c.n()}
		}
	case isFind(await):
		c.motion, c.char = await, r
		e.pend = pending{}
		e.exec(c)
	case await == "i" || await == "a":
		c.motion = await + string(r)
		e.pend = pending{}
		if visual {
			e.selectObject(c)
		} else {
			e.exec(c)
		}
	default:
		e.pend = pending{}
	}
}
