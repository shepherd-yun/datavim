package redit

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/shepherd-yun/sqlvim/internal/edit"
	"github.com/shepherd-yun/sqlvim/internal/rcmd"
)

// Target is the key a table of staged rows belongs to.
type Target struct {
	Key  string
	Type string // "hash", "list", "set", "zset", "stream"
	Len  int64  // LLEN when the list was loaded
	Tomb string // a list's placeholder for deleted elements; the caller makes it unique
}

// ErrNull is a cell left NULL: Redis has no NULL.
var ErrNull = errors.New("redis has no NULL · type a value (empty is fine)")

func quoted(s string) string { return `"` + rcmd.Escape(s) + `"` }

// Build plans the staged rows of t. Columns by type (keyTab builds them):
// hash field,value · list index,value · set member · zset rank,member,score · stream id,fields.
func Build(t *edit.Table, tg Target) (Plan, error) {
	p := Plan{Key: tg.Key}
	var err error
	switch tg.Type {
	case "hash", "set", "zset":
		err = buildMembers(t, tg, &p)
	default:
		return Plan{}, fmt.Errorf("%s keys cannot be changed here", tg.Type)
	}
	if err != nil {
		return Plan{}, err
	}
	for i := range t.Len() {
		if t.Row(i).State != edit.Unchanged {
			p.N++
		}
	}
	return p, nil
}

// members describes how a hash, a set and a zset name and write their
// members: name is the column of the member's name.
type members struct {
	noun       string
	name       int
	fixed      int // a column that cannot be edited (zset rank), or -1
	value      func(r *edit.Row) ([]string, error)
	current    func(key, name string, r *edit.Row) Check // the original row as it must still be
	missing    func(key, name string) Check
	remove     string
	add        func(key, name string, vals []string, existing bool) []string
	addChanged func(r *edit.Row) bool // whether an unrenamed modified row needs a write
}

func spec(tg Target) members {
	switch tg.Type {
	case "hash":
		return members{
			noun: "field", name: 0, fixed: -1,
			value: func(r *edit.Row) ([]string, error) { return []string{r.Values[1].Text}, nil },
			current: func(key, f string, r *edit.Row) Check {
				return Check{Args: []string{"HGET", key, f}, Expect: ExpectValue, Value: r.Orig[1].Text, What: "field " + quoted(f) + " changed"}
			},
			missing: func(key, f string) Check {
				return Check{Args: []string{"HEXISTS", key, f}, Expect: ExpectInt, Int: 0, What: "field " + quoted(f) + " already exists"}
			},
			remove: "HDEL",
			add: func(key, f string, vals []string, _ bool) []string {
				return []string{"HSET", key, f, vals[0]}
			},
			addChanged: func(r *edit.Row) bool { return r.Set[1] },
		}
	case "set":
		return members{
			noun: "member", name: 0, fixed: -1,
			value: func(*edit.Row) ([]string, error) { return nil, nil },
			current: func(key, m string, _ *edit.Row) Check {
				return Check{Args: []string{"SISMEMBER", key, m}, Expect: ExpectInt, Int: 1, What: "member " + quoted(m) + " is gone"}
			},
			missing: func(key, m string) Check {
				return Check{Args: []string{"SISMEMBER", key, m}, Expect: ExpectInt, Int: 0, What: "member " + quoted(m) + " already exists"}
			},
			remove: "SREM",
			add: func(key, m string, _ []string, _ bool) []string {
				return []string{"SADD", key, m}
			},
			addChanged: func(*edit.Row) bool { return false },
		}
	default: // zset
		return members{
			noun: "member", name: 1, fixed: 0,
			value: func(r *edit.Row) ([]string, error) {
				s := r.Values[2].Text
				f, err := strconv.ParseFloat(s, 64)
				if err != nil {
					return nil, fmt.Errorf("score %s is not a number", quoted(s))
				}
				return []string{strconv.FormatFloat(f, 'g', -1, 64)}, nil
			},
			current: func(key, m string, r *edit.Row) Check {
				f, _ := strconv.ParseFloat(r.Orig[2].Text, 64)
				return Check{Args: []string{"ZSCORE", key, m}, Expect: ExpectScore, Score: f, What: "member " + quoted(m) + " changed"}
			},
			missing: func(key, m string) Check {
				return Check{Args: []string{"ZSCORE", key, m}, Expect: ExpectNil, What: "member " + quoted(m) + " already exists"}
			},
			remove: "ZREM",
			add: func(key, m string, vals []string, existing bool) []string {
				mode := "NX"
				if existing {
					mode = "XX"
				}
				return []string{"ZADD", key, mode, vals[0], m}
			},
			addChanged: func(r *edit.Row) bool { return r.Set[2] },
		}
	}
}

// buildMembers plans a hash, a set or a zset: removals first, then writes.
func buildMembers(t *edit.Table, tg Target, p *Plan) error {
	sp := spec(tg)
	key := tg.Key
	moved := map[string]bool{} // original names this plan deletes or renames away
	seen := map[string]bool{}
	for i := range t.Len() {
		r := t.Row(i)
		if r.State == edit.Deleted {
			moved[r.Orig[sp.name].Text] = true
			continue
		}
		if err := nulls(r, sp.fixed); err != nil {
			return err
		}
		if r.State == edit.Modified && sp.fixed >= 0 && r.Set[sp.fixed] {
			return fmt.Errorf("the %s column cannot be edited", t.Columns()[sp.fixed].Name)
		}
		if r.State == edit.Modified && r.Set[sp.name] {
			moved[r.Orig[sp.name].Text] = true
		}
		name := r.Values[sp.name].Text
		if seen[name] {
			return fmt.Errorf("%s %s appears twice", sp.noun, quoted(name))
		}
		seen[name] = true
	}
	var removes, adds [][]string
	for i := range t.Len() {
		r := t.Row(i)
		switch r.State {
		case edit.Deleted:
			old := r.Orig[sp.name].Text
			p.Checks = append(p.Checks, sp.current(key, old, r))
			removes = append(removes, []string{sp.remove, key, old})
		case edit.Inserted:
			name := r.Values[sp.name].Text
			vals, err := sp.value(r)
			if err != nil {
				return err
			}
			if !moved[name] {
				p.Checks = append(p.Checks, sp.missing(key, name))
			}
			adds = append(adds, sp.add(key, name, vals, false))
		case edit.Modified:
			old, name := r.Orig[sp.name].Text, r.Values[sp.name].Text
			vals, err := sp.value(r)
			if err != nil {
				return err
			}
			p.Checks = append(p.Checks, sp.current(key, old, r))
			if name == old {
				if sp.addChanged(r) {
					adds = append(adds, sp.add(key, name, vals, true))
				}
				continue
			}
			if !moved[name] {
				p.Checks = append(p.Checks, sp.missing(key, name))
			}
			removes = append(removes, []string{sp.remove, key, old})
			adds = append(adds, sp.add(key, name, vals, false))
		}
	}
	p.Cmds = slices.Concat(removes, adds)
	return nil
}

// nulls is ErrNull when a cell other than skip is NULL.
func nulls(r *edit.Row, skip int) error {
	for i, c := range r.Values {
		if c.Null && i != skip {
			return ErrNull
		}
	}
	return nil
}
