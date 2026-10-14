package config

// KeymapFile is keymap.toml: an optional leader and, per context, key
// sequence -> action ID ("" unbinds). ui/keymap validates the contents.
type KeymapFile struct {
	Leader    string            `toml:"leader"`
	Global    map[string]string `toml:"global"`
	Tree      map[string]string `toml:"tree"`
	Grid      map[string]string `toml:"grid"`
	Structure map[string]string `toml:"structure"`
	Console   map[string]string `toml:"console"`
	Results   map[string]string `toml:"results"`
	Popup     map[string]string `toml:"popup"`
}

// Overrides returns the tables present in the file, keyed by context name.
func (f KeymapFile) Overrides() map[string]map[string]string {
	out := map[string]map[string]string{}
	for name, m := range map[string]map[string]string{
		"global": f.Global, "tree": f.Tree, "grid": f.Grid,
		"structure": f.Structure, "console": f.Console, "results": f.Results, "popup": f.Popup,
	} {
		if m != nil {
			out[name] = m
		}
	}
	return out
}
