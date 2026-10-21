package esx

import (
	"encoding/json"
	"errors"
)

// Hit is one document of a search response.
type Hit struct {
	Index, ID, Routing string
	SeqNo, PrimaryTerm int64
	Version            int64 // 0 when the response leaves it out
	Score              *float64
	Source             json.RawMessage
	// Truncated: Source went over the size limit and holds a preview only
	// (set by the es package).
	Truncated  bool
	SourceSize int // the full _source's size in bytes
	Highlight map[string][]string
	Sort      []json.RawMessage // for search_after
}

// Table is a response shown as rows of text.
type Table struct {
	Columns []string
	Rows    [][]string
}

func tableCell(v json.RawMessage, ok bool) string {
	text, null := CellText(v, ok)
	if null {
		return "null"
	}
	return text
}

// flatten lists the leaf paths of a _source object in document order;
// arrays and empty objects are leaves.
func flatten(prefix string, src json.RawMessage, seen map[string]bool, cols *[]string) {
	obj, err := decodeObject(src)
	if err != nil {
		return
	}
	for _, m := range obj {
		p := prefix + m.Key
		if sub, err := decodeObject(m.Val); err == nil && len(sub) > 0 {
			flatten(p+".", m.Val, seen, cols)
			continue
		}
		if !seen[p] {
			seen[p] = true
			*cols = append(*cols, p)
		}
	}
}

var errNoTable = errors.New("no table view for this response")

// HitsTable is a _search response's hits: _index, _id and the _source
// leaf paths in the order they first show up.
func HitsTable(resp []byte) (Table, error) {
	var r struct {
		Hits *struct {
			Hits []struct {
				Index  string          `json:"_index"`
				ID     string          `json:"_id"`
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(resp, &r); err != nil || r.Hits == nil || r.Hits.Hits == nil {
		return Table{}, errNoTable
	}
	cols := []string{"_index", "_id"}
	seen := map[string]bool{"_index": true, "_id": true}
	for _, h := range r.Hits.Hits {
		flatten("", h.Source, seen, &cols)
	}
	t := Table{Columns: cols}
	for _, h := range r.Hits.Hits {
		row := []string{h.Index, h.ID}
		for _, c := range cols[2:] {
			v, ok := GetPath(h.Source, c)
			row = append(row, tableCell(v, ok))
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

// CatTable is a _cat API's format=json array of objects.
func CatTable(resp []byte) (Table, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(resp, &items); err != nil {
		return Table{}, errNoTable
	}
	var t Table
	seen := map[string]bool{}
	objs := make([]object, 0, len(items))
	for _, it := range items {
		obj, err := decodeObject(it)
		if err != nil {
			return Table{}, errNoTable
		}
		objs = append(objs, obj)
		for _, m := range obj {
			if !seen[m.Key] {
				seen[m.Key] = true
				t.Columns = append(t.Columns, m.Key)
			}
		}
	}
	for _, obj := range objs {
		row := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			v, ok := obj.get(c)
			row[i] = tableCell(v, ok)
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

// bucketKeys are a bucket's own keys, not sub-aggregations.
var bucketKeys = map[string]bool{
	"key": true, "key_as_string": true, "doc_count": true, "from": true, "to": true,
	"from_as_string": true, "to_as_string": true, "doc_count_error_upper_bound": true,
	"sum_other_doc_count": true, "score": true, "bg_count": true,
}

// AggTable is a search response's first aggregation; nested bucket
// aggregations flatten to key1 · key2 · doc_count · metrics….
func AggTable(resp []byte) (Table, error) {
	top, err := decodeObject(resp)
	if err != nil {
		return Table{}, errNoTable
	}
	raw, ok := top.get("aggregations")
	if !ok {
		return Table{}, errNoTable
	}
	aggs, err := decodeObject(raw)
	if err != nil || len(aggs) == 0 {
		return Table{}, errNoTable
	}
	first := aggs[0]
	agg, err := decodeObject(first.Val)
	if err != nil {
		return Table{}, errNoTable
	}
	var t Table
	if _, ok := agg.get("buckets"); !ok {
		t.Columns = []string{first.Key}
		t.Rows = [][]string{{metricText(agg)}}
		return t, nil
	}
	walkBuckets([]string{first.Key}, nil, agg, &t)
	return t, nil
}

// walkBuckets adds a row per leaf bucket of agg, below the bucket keys
// in prefix; names are the bucket aggregations' names so far.
func walkBuckets(names, prefix []string, agg object, t *Table) {
	raw, _ := agg.get("buckets")
	var buckets []object
	if arr, err := decodeArray(raw); err == nil {
		for _, b := range arr {
			if obj, err := decodeObject(b); err == nil {
				buckets = append(buckets, obj)
			}
		}
	} else if keyed, err := decodeObject(raw); err == nil {
		for _, m := range keyed {
			obj, err := decodeObject(m.Val)
			if err != nil {
				continue
			}
			buckets = append(buckets, append(object{{"key", json.RawMessage(jsonString(m.Key))}}, obj...))
		}
	}
	for _, b := range buckets {
		key, ok := b.get("key_as_string")
		if !ok {
			key, _ = b.get("key")
		}
		row := append(append([]string{}, prefix...), tableCell(key, true))
		nested := false
		for _, m := range b {
			if bucketKeys[m.Key] {
				continue
			}
			if sub, err := decodeObject(m.Val); err == nil {
				if _, ok := sub.get("buckets"); ok {
					walkBuckets(append(append([]string{}, names...), m.Key), row, sub, t)
					nested = true
					break
				}
			}
		}
		if nested {
			continue
		}
		cols := append(append([]string{}, names...), "doc_count")
		count, _ := b.get("doc_count")
		row = append(row, tableCell(count, true))
		for _, m := range b {
			if bucketKeys[m.Key] {
				continue
			}
			cols = append(cols, m.Key)
			sub, _ := decodeObject(m.Val)
			row = append(row, metricText(sub))
		}
		if t.Columns == nil {
			t.Columns = cols
		}
		t.Rows = append(t.Rows, row)
	}
}

// metricText is a metric aggregation's value, or its JSON when it has
// several.
func metricText(agg object) string {
	if v, ok := agg.get("value_as_string"); ok {
		return tableCell(v, true)
	}
	if v, ok := agg.get("value"); ok {
		return tableCell(v, true)
	}
	return tableCell(agg.encode(), true)
}

func decodeArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if kindOf(raw) != '[' {
		return nil, errors.New("not an array")
	}
	var arr []json.RawMessage
	err := json.Unmarshal(raw, &arr)
	return arr, err
}

