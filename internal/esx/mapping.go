package esx

import (
	"encoding/json"
	"slices"
	"strings"
)

// Field is one field of an index mapping.
type Field struct {
	Path string // "user.name"
	// Type is the mapping type; "keyword|long" when the indices of an
	// alias or pattern disagree.
	Type string
	// Keyword is the full path of a keyword sub-field ("title.keyword"),
	// for sorting a text field.
	Keyword            string
	Indexed, DocValues bool
	Analyzer           string
	Sub                []string // full paths of the "fields" sub-fields
	CopyTo             []string
	Object             bool // an object or nested parent
}

type mappingProp struct {
	Type       string                     `json:"type"`
	Properties map[string]json.RawMessage `json:"properties"`
	Fields     map[string]struct {
		Type string `json:"type"`
	} `json:"fields"`
	Index     *bool           `json:"index"`
	DocValues *bool           `json:"doc_values"`
	Analyzer  string          `json:"analyzer"`
	CopyTo    json.RawMessage `json:"copy_to"`
}

// FlattenMapping turns one index's "mappings" object (7.x and later: no
// type level) into fields sorted by path.
func FlattenMapping(raw json.RawMessage) ([]Field, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return []Field{}, nil
	}
	var top mappingProp
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	out := []Field{}
	if err := flattenProps("", top.Properties, &out); err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b Field) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

func flattenProps(prefix string, props map[string]json.RawMessage, out *[]Field) error {
	for name, raw := range props {
		var p mappingProp
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		f := Field{Path: prefix + name, Type: p.Type, Indexed: p.Index == nil || *p.Index, Analyzer: p.Analyzer}
		if p.Type == "" || p.Type == "object" || p.Type == "nested" {
			if f.Type == "" {
				f.Type = "object"
			}
			f.Object = true
			*out = append(*out, f)
			if err := flattenProps(f.Path+".", p.Properties, out); err != nil {
				return err
			}
			continue
		}
		f.DocValues = p.Type != "text" && (p.DocValues == nil || *p.DocValues)
		subs := make([]string, 0, len(p.Fields))
		for sub := range p.Fields {
			subs = append(subs, sub)
		}
		slices.Sort(subs)
		for _, sub := range subs {
			full := f.Path + "." + sub
			f.Sub = append(f.Sub, full)
			if f.Keyword == "" && p.Fields[sub].Type == "keyword" {
				f.Keyword = full
			}
		}
		f.CopyTo = stringOrList(p.CopyTo)
		*out = append(*out, f)
	}
	return nil
}

func stringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var l []string
	_ = json.Unmarshal(raw, &l)
	return l
}

// MergeMappings unions the fields of several indices, sorted by path; a
// field whose type differs between them gets the types joined by "|".
func MergeMappings(per map[string][]Field) []Field {
	names := make([]string, 0, len(per))
	for n := range per {
		names = append(names, n)
	}
	slices.Sort(names)
	byPath := map[string]*Field{}
	types := map[string][]string{}
	var order []string
	for _, n := range names {
		for _, f := range per[n] {
			if _, ok := byPath[f.Path]; !ok {
				byPath[f.Path] = &f
				order = append(order, f.Path)
			}
			if !slices.Contains(types[f.Path], f.Type) {
				types[f.Path] = append(types[f.Path], f.Type)
			}
		}
	}
	out := make([]Field, 0, len(order))
	for _, p := range order {
		f := *byPath[p]
		ts := slices.Clone(types[p])
		slices.Sort(ts)
		f.Type = strings.Join(ts, "|")
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Field) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// TypeFor is path's type in fields, "" when the field is not mapped.
func TypeFor(fields []Field, path string) string {
	for _, f := range fields {
		if f.Path == path {
			return f.Type
		}
	}
	return ""
}

var (
	shortTypes = map[string]bool{
		"keyword": true, "constant_keyword": true, "wildcard": true, "ip": true, "version": true,
		"long": true, "integer": true, "short": true, "byte": true, "double": true, "float": true,
		"half_float": true, "scaled_float": true, "unsigned_long": true,
		"date": true, "date_nanos": true, "boolean": true,
	}
	skippedTypes = map[string]bool{
		"geo_point": true, "geo_shape": true, "shape": true, "point": true, "dense_vector": true,
		"sparse_vector": true, "rank_features": true, "binary": true, "match_only_text": true,
		"search_as_you_type": true, "completion": true, "join": true, "percolator": true,
	}
)

// DefaultColumns picks the columns a Documents view starts with (spec
// §5.2): keyword, number, date and boolean fields first, then text fields
// with a keyword sub-field, then the rest; never long text, object
// parents, geo, vectors or binary. At most max.
func DefaultColumns(fields []Field, maxCols int) []string {
	rank := func(f Field) int {
		if f.Object {
			return -1
		}
		short, skip := true, false
		for t := range strings.SplitSeq(f.Type, "|") {
			short = short && shortTypes[t]
			skip = skip || skippedTypes[t] || t == "text" && f.Keyword == ""
		}
		switch {
		case skip:
			return -1
		case short:
			return 0
		case f.Type == "text":
			return 1
		}
		return 2
	}
	var out []string
	for r := range 3 {
		for _, f := range fields {
			if len(out) == maxCols {
				return out
			}
			if rank(f) == r {
				out = append(out, f.Path)
			}
		}
	}
	return out
}
