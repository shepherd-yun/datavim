package keymap

import (
	"fmt"
	"maps"
	"slices"
)

// Contexts lists the tables keymap.toml may contain.
func Contexts() []Context {
	return []Context{Global, Tree, Grid, Structure, Console, Results, Diff, Import, Popup}
}

// KnownActions maps every bindable action to its description.
func KnownActions() map[Action]string {
	out := map[Action]string{Quit: "Quit"}
	for _, b := range DefaultBindings() {
		if out[b.Action] == "" {
			out[b.Action] = b.Desc
		}
	}
	return out
}

// renamedActions maps action IDs of earlier versions to their current
// names, so old keymap.toml files keep loading.
var renamedActions = map[Action]Action{"conn.delete": DeleteNode, "conn.edit": EditNode}

// Custom is the default keymap with another leader and keymap.toml's
// overrides: per context, key sequence -> action, "" unbinds the keys.
func Custom(leader string, overrides map[Context]map[string]string) (*Keymap, error) {
	leader, err := parseLeader(leader)
	if err != nil {
		return nil, err
	}
	known := KnownActions()
	expand := func(keys []string) []string {
		out := slices.Clone(keys)
		for i, k := range out {
			if k == LeaderToken {
				out[i] = leader
			}
		}
		return out
	}
	bindings := DefaultBindings()
	for _, ctx := range slices.Sorted(maps.Keys(overrides)) {
		if !slices.Contains(Contexts(), ctx) {
			return nil, fmt.Errorf("[%s]: unknown context (want one of global, tree, grid, structure, console, results, diff, popup)", ctx)
		}
		for _, ks := range slices.Sorted(maps.Keys(overrides[ctx])) {
			keys, err := ParseKeys(ks)
			if err != nil {
				return nil, fmt.Errorf("[%s] %q: %w", ctx, ks, err)
			}
			action := Action(overrides[ctx][ks])
			if a, ok := renamedActions[action]; ok {
				action = a
			}
			if action != "" && known[action] == "" {
				return nil, fmt.Errorf("[%s] %q: unknown action %q", ctx, ks, action)
			}
			want := expand(keys)
			bindings = slices.DeleteFunc(bindings, func(b Binding) bool {
				return b.Context == ctx && slices.Equal(expand(b.Keys), want)
			})
			if action != "" {
				bindings = append(bindings, Binding{ctx, keys, action, known[action]})
			}
		}
	}
	return New(leader, bindings)
}
