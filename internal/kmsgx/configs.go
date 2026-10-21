package kmsgx

import (
	"errors"
	"fmt"

	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

// ConfigEdit is one staged change: a new value, or back to the default.
type ConfigEdit struct {
	Name, Value string
	Delete      bool
}

// ErrSensitiveConfig is an edit of a config whose value is hidden.
var ErrSensitiveConfig = errors.New("sensitive configs can't be edited here")

// PlanConfigs is the changes to send for edits on top of current: the
// last edit of a config counts, an edit back to the current value is
// dropped, a delete of a default config too. Unknown and sensitive configs
// are errors. Changes keep the order the configs were first edited in.
func PlanConfigs(current []kafka.ConfigEntry, edits []ConfigEdit) ([]kafka.ConfigChange, error) {
	byName := make(map[string]kafka.ConfigEntry, len(current))
	for _, e := range current {
		byName[e.Name] = e
	}
	last := map[string]ConfigEdit{}
	var order []string
	for _, ed := range edits {
		e, ok := byName[ed.Name]
		if !ok {
			return nil, fmt.Errorf("unknown config %s", ed.Name)
		}
		if e.Sensitive {
			return nil, ErrSensitiveConfig
		}
		if _, seen := last[ed.Name]; !seen {
			order = append(order, ed.Name)
		}
		last[ed.Name] = ed
	}
	var out []kafka.ConfigChange
	for _, name := range order {
		ed, e := last[name], byName[name]
		switch {
		case ed.Delete && e.Default:
		case !ed.Delete && ed.Value == e.Value:
		default:
			out = append(out, kafka.ConfigChange{Name: name, Value: ed.Value, Delete: ed.Delete, Old: e.Value, OldDefault: e.Default})
		}
	}
	return out, nil
}
