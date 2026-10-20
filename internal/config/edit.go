package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// UpdateConnection rewrites the [[connection]] block named name in
// dir/connections.toml so it describes c. Only the keys the connection form
// edits are touched, and only when their value changes: other keys,
// comments and the other blocks stay byte for byte. Changed params are
// written as one inline table.
func UpdateConnection(dir, name string, c Connection) (Connection, error) {
	c, err := Normalize(c)
	if err != nil {
		return c, err
	}
	path := filepath.Join(dir, "connections.toml")
	lines, blocks, err := readBlocks(path)
	if err != nil {
		return c, err
	}
	idx := -1
	for i, b := range blocks {
		switch b.conn.Name {
		case name:
			idx = i
		case c.Name:
			return c, fmt.Errorf("connection %q already exists", c.Name)
		}
	}
	if idx < 0 {
		return c, fmt.Errorf("connection %q is not in %s", name, path)
	}
	b := blocks[idx]
	body, err := editBlock(lines[b.start:b.end], b.conn, c)
	if err != nil {
		return c, err
	}
	out := append(append(append([]string{}, lines[:b.start]...), body...), lines[b.end:]...)
	err = writeChecked(path, out, func(conns []Connection) error {
		if len(conns) != len(blocks) || !sameEditable(conns[idx], c) {
			return errors.New("the rewritten block does not match the form")
		}
		return nil
	})
	return c, err
}

// RemoveConnection deletes the [[connection]] block named name. Comments
// above the following block stay with it.
func RemoveConnection(dir, name string) error {
	path := filepath.Join(dir, "connections.toml")
	lines, blocks, err := readBlocks(path)
	if err != nil {
		return err
	}
	for _, b := range blocks {
		if b.conn.Name != name {
			continue
		}
		out := append(append([]string{}, lines[:b.start]...), lines[b.end:]...)
		// Do not leave two blank lines where the block was.
		for s := b.start; s < len(out) && blank(out[s]) && (s == 0 || blank(out[s-1])); {
			out = append(out[:s], out[s+1:]...)
		}
		return writeChecked(path, out, func(conns []Connection) error {
			if len(conns) != len(blocks)-1 {
				return errors.New("removing the block changed other connections")
			}
			return nil
		})
	}
	return fmt.Errorf("connection %q is not in %s", name, path)
}

// block is one [[connection]] table: lines[start:end], from its header to
// its last key (blank lines and comments after it belong to what follows).
type block struct {
	start, end int
	conn       Connection // as written, normalized
}

func readBlocks(path string) ([]string, []block, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(string(data), "\n")
	var blocks []block
	for i := 0; i < len(lines); i++ {
		if strings.ReplaceAll(strings.TrimSpace(lines[i]), " ", "") != "[[connection]]" {
			continue
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
			end++
		}
		for end > i+1 && (blank(lines[end-1]) || comment(lines[end-1])) {
			end--
		}
		var cf connectionsFile
		if err := toml.Unmarshal([]byte(strings.Join(lines[i:end], "\n")), &cf); err != nil {
			return nil, nil, fmt.Errorf("%s:%d: cannot read this [[connection]] block: %w", path, i+1, err)
		}
		if len(cf.Connections) != 1 {
			return nil, nil, fmt.Errorf("%s:%d: cannot read this [[connection]] block", path, i+1)
		}
		c, err := Normalize(cf.Connections[0])
		if err != nil {
			return nil, nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		blocks = append(blocks, block{start: i, end: end, conn: c})
		i = end - 1
	}
	return lines, blocks, nil
}

// editBlock rewrites the form's keys whose value changed. A changed line is
// replaced whole (an end-of-line comment on it is lost); new keys go after
// the block's last line; emptied optional keys are removed.
func editBlock(body []string, old, c Connection) ([]string, error) {
	out := append([]string{}, body...)
	for _, f := range []struct {
		key      string
		old, new any
		optional bool
	}{
		{"name", old.Name, c.Name, false},
		{"driver", old.Driver, c.Driver, false},
		{"host", old.Host, c.Host, false},
		{"port", old.Port, c.Port, false},
		{"user", old.User, c.User, true},
		{"database", old.Database, c.Database, true},
		{"ssh", old.SSH, c.SSH, true},
		{"env", string(old.Env), string(c.Env), false},
		{"readonly", boolOrNil(old.ReadOnly), boolOrNil(c.ReadOnly), true},
		{"params", paramsLine(old.Params), paramsLine(c.Params), true},
	} {
		if f.old == f.new {
			continue
		}
		line := ""
		switch {
		case f.key == "params" && f.new != "":
			line = f.new.(string)
		case f.key == "params":
		case !f.optional || (f.new != "" && f.new != nil):
			enc, err := toml.Marshal(map[string]any{f.key: f.new})
			if err != nil {
				return nil, err
			}
			line = strings.TrimSuffix(string(enc), "\n")
		}
		at := -1
		for i, l := range out {
			if keyOf(l) == f.key {
				at = i
				break
			}
		}
		switch {
		case at >= 0 && line == "":
			out = append(out[:at], out[at+1:]...)
		case at >= 0:
			indent := out[at][:len(out[at])-len(strings.TrimLeft(out[at], " \t"))]
			out[at] = indent + line
		case line != "":
			out = append(out, line)
		}
	}
	return out, nil
}

// paramsLine is the "params = { … }" line for p, or "" when p is empty.
func paramsLine(p map[string]string) string {
	if len(p) == 0 {
		return ""
	}
	enc, err := toml.Marshal(struct {
		Params map[string]string `toml:"params,inline"`
	}{p})
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(string(enc), "\n")
}

// boolOrNil is *b, or nil for an unset key.
func boolOrNil(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// keyOf returns the key a "key = value" line sets, or "".
func keyOf(line string) string {
	t := strings.TrimSpace(line)
	if t == "" || comment(t) {
		return ""
	}
	k, _, ok := strings.Cut(t, "=")
	if !ok {
		return ""
	}
	return strings.Trim(strings.TrimSpace(k), `"'`)
}

func blank(l string) bool   { return strings.TrimSpace(l) == "" }
func comment(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "#") }

func sameEditable(a, b Connection) bool {
	return a.Name == b.Name && a.Driver == b.Driver && a.Host == b.Host && a.Port == b.Port &&
		a.User == b.User && a.Database == b.Database && a.SSH == b.SSH && a.Env == b.Env &&
		boolOrNil(a.ReadOnly) == boolOrNil(b.ReadOnly) && paramsLine(a.Params) == paramsLine(b.Params)
}

// writeChecked parses the new content the way Load does and lets check vet
// it before the file is replaced (atomically, mode 0600). A mistake in the
// rewrite never reaches the disk.
func writeChecked(path string, lines []string, check func([]Connection) error) error {
	data := []byte(strings.Join(lines, "\n"))
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cf connectionsFile
	if err := dec.Decode(&cf); err != nil {
		return fmt.Errorf("%s: the edit would break the file, nothing was written: %w", path, err)
	}
	conns, err := normalize(cf.Connections)
	if err == nil {
		err = check(conns)
	}
	if err != nil {
		return fmt.Errorf("%s: nothing was written: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".connections-*.toml")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
