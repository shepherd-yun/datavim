package es

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// WriteFuncs names every write operation of this package; the session's
// read-only test calls each one through the guarded Doer.
var WriteFuncs = []string{
	"PutDoc", "CreateDoc", "DeleteDoc", "DeleteByQuery", "CreateIndex", "DeleteIndices", "OpenIndex",
	"CloseIndex", "Refresh", "Flush", "ClearCache", "ForceMerge", "UpdateAliases", "PutSettings",
	"CancelTask", "Reindex", "UpdateByQuery", "PutMapping", "PutClusterSettings",
	"PutTemplate", "DeleteTemplate",
	"CreateSnapshot", "DeleteSnapshot", "RestoreSnapshot", "PutRepository", "DeleteRepository",
	"PutPolicy", "DeletePolicy", "PutPipeline", "DeletePipeline",
}

// Version is the sequence number and primary term a document was read at.
type Version struct{ SeqNo, PrimaryTerm int64 }

func (v Version) query() url.Values {
	q := url.Values{}
	q.Set("if_seq_no", strconv.FormatInt(v.SeqNo, 10))
	q.Set("if_primary_term", strconv.FormatInt(v.PrimaryTerm, 10))
	return q
}

// encodeQuery keeps the parameters in a fixed, readable order.
func encodeQuery(q url.Values, keys ...string) string {
	var parts []string
	for _, k := range keys {
		if v := q.Get(k); v != "" {
			parts = append(parts, k+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func docPath(index, id string) string { return indexPath(index) + "/_doc/" + url.PathEscape(id) }

// PutDoc replaces a document only if it is still at v; a newer document
// is ErrConflict.
func PutDoc(ctx context.Context, d Doer, index, id, routing string, src json.RawMessage, v Version) (Version, error) {
	q := v.query()
	q.Set("refresh", "wait_for")
	q.Set("routing", routing)
	resp, err := do(ctx, d, Request{
		Method: "PUT", Path: docPath(index, id) + "?" + encodeQuery(q, "if_seq_no", "if_primary_term", "refresh", "routing"),
		Body: src,
	})
	if err != nil {
		return Version{}, err
	}
	var r struct {
		SeqNo       int64 `json:"_seq_no"`
		PrimaryTerm int64 `json:"_primary_term"`
	}
	err = json.Unmarshal(resp.Body, &r)
	return Version{r.SeqNo, r.PrimaryTerm}, err
}

// CreateDoc adds a document; an empty id lets the server make one. It
// returns the document's id.
func CreateDoc(ctx context.Context, d Doer, index, id string, src json.RawMessage) (string, error) {
	r := Request{Method: "POST", Path: indexPath(index) + "/_doc?refresh=wait_for", Body: src}
	if id != "" {
		r = Request{Method: "PUT", Path: indexPath(index) + "/_create/" + url.PathEscape(id) + "?refresh=wait_for", Body: src}
	}
	resp, err := do(ctx, d, r)
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"_id"`
	}
	err = json.Unmarshal(resp.Body, &out)
	return out.ID, err
}

// DeleteDoc deletes a document only if it is still at v.
func DeleteDoc(ctx context.Context, d Doer, index, id, routing string, v Version) error {
	q := v.query()
	q.Set("refresh", "wait_for")
	q.Set("routing", routing)
	_, err := do(ctx, d, Request{Method: "DELETE", Path: docPath(index, id) + "?" + encodeQuery(q, "if_seq_no", "if_primary_term", "refresh", "routing")})
	return err
}

// concrete checks names are indices, not patterns (spec §9.2).
func concrete(names []string) error {
	if len(names) == 0 {
		return errors.New("elasticsearch: no index named")
	}
	for _, n := range names {
		if n == "" || n == "_all" || strings.ContainsAny(n, "*?,") {
			return fmt.Errorf("elasticsearch: %q is not one index; name the indices one by one", n)
		}
	}
	return nil
}

// DeleteByQuery starts deleting what query matches in the named indices
// as a task and returns its id.
func DeleteByQuery(ctx context.Context, d Doer, indices []string, query json.RawMessage) (string, error) {
	if err := concrete(indices); err != nil {
		return "", err
	}
	if len(query) == 0 {
		query = json.RawMessage(`{"match_all":{}}`)
	}
	body, _ := json.Marshal(map[string]json.RawMessage{"query": query})
	resp, err := do(ctx, d, Request{
		Method: "POST", Path: strings.Join(indices, ",") + "/_delete_by_query?wait_for_completion=false&conflicts=proceed", Body: body,
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Task string `json:"task"`
	}
	err = json.Unmarshal(resp.Body, &r)
	return r.Task, err
}

// TaskStatus is a task's progress.
type TaskStatus struct {
	Completed                                  bool
	Total, Deleted, Updated, Created, Failures int64
	Err                                        string
}

// GetTask reads a task's progress.
func GetTask(ctx context.Context, d Doer, id string) (TaskStatus, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_tasks/" + url.PathEscape(id)})
	if err != nil {
		return TaskStatus{}, err
	}
	var r struct {
		Completed bool `json:"completed"`
		Task      struct {
			Status struct{ Total, Deleted, Updated, Created int64 } `json:"status"`
		} `json:"task"`
		Response struct {
			Failures []json.RawMessage `json:"failures"`
		} `json:"response"`
		Error *struct{ Type, Reason string } `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return TaskStatus{}, err
	}
	s := r.Task.Status
	out := TaskStatus{Completed: r.Completed, Total: s.Total, Deleted: s.Deleted, Updated: s.Updated, Created: s.Created, Failures: int64(len(r.Response.Failures))}
	if r.Error != nil {
		out.Err = r.Error.Type + ": " + r.Error.Reason
	}
	return out, nil
}

// NewIndex is a new index; Mappings and Settings may be empty.
type NewIndex struct {
	Name             string
	Shards, Replicas int
	Mappings         json.RawMessage
	Settings         json.RawMessage
}

// CreateIndex creates an index.
func CreateIndex(ctx context.Context, d Doer, n NewIndex) error {
	settings := map[string]any{}
	if len(n.Settings) > 0 {
		if err := json.Unmarshal(n.Settings, &settings); err != nil {
			return fmt.Errorf("settings: %w", err)
		}
	}
	if n.Shards > 0 {
		settings["number_of_shards"] = n.Shards
	}
	if n.Replicas >= 0 {
		settings["number_of_replicas"] = n.Replicas
	}
	body := map[string]any{"settings": settings}
	if len(n.Mappings) > 0 {
		body["mappings"] = n.Mappings
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = do(ctx, d, Request{Method: "PUT", Path: url.PathEscape(n.Name), Body: b})
	return err
}

// DeleteIndices deletes the named indices; patterns are refused.
func DeleteIndices(ctx context.Context, d Doer, names []string) error {
	if err := concrete(names); err != nil {
		return err
	}
	_, err := do(ctx, d, Request{Method: "DELETE", Path: strings.Join(names, ",")})
	return err
}

func indexOp(ctx context.Context, d Doer, target, op string) error {
	p := op
	if target != "" {
		p = indexPath(target) + "/" + op
	}
	_, err := do(ctx, d, Request{Method: "POST", Path: p})
	return err
}

// OpenIndex opens a closed index.
func OpenIndex(ctx context.Context, d Doer, name string) error { return indexOp(ctx, d, name, "_open") }

// CloseIndex closes an index.
func CloseIndex(ctx context.Context, d Doer, name string) error {
	return indexOp(ctx, d, name, "_close")
}

// Refresh refreshes target; empty is every index.
func Refresh(ctx context.Context, d Doer, target string) error {
	return indexOp(ctx, d, target, "_refresh")
}

// Flush flushes target.
func Flush(ctx context.Context, d Doer, target string) error {
	return indexOp(ctx, d, target, "_flush")
}

// ClearCache clears target's caches.
func ClearCache(ctx context.Context, d Doer, target string) error {
	return indexOp(ctx, d, target, "_cache/clear")
}

// ForceMerge merges target down to maxSegments segments per shard.
func ForceMerge(ctx context.Context, d Doer, target string, maxSegments int) error {
	return indexOp(ctx, d, target, "_forcemerge?max_num_segments="+strconv.Itoa(max(maxSegments, 1)))
}

// AliasAction adds or removes one alias.
type AliasAction struct {
	Add          bool
	Index, Alias string
	IsWrite      *bool
	Filter       json.RawMessage
	Routing      string
}

// UpdateAliases applies the actions in one atomic request.
func UpdateAliases(ctx context.Context, d Doer, acts []AliasAction) error {
	var actions []map[string]map[string]any
	for _, a := range acts {
		v := map[string]any{"index": a.Index, "alias": a.Alias}
		if !a.Add {
			actions = append(actions, map[string]map[string]any{"remove": v})
			continue
		}
		if a.IsWrite != nil {
			v["is_write_index"] = *a.IsWrite
		}
		if len(a.Filter) > 0 {
			v["filter"] = a.Filter
		}
		if a.Routing != "" {
			v["routing"] = a.Routing
		}
		actions = append(actions, map[string]map[string]any{"add": v})
	}
	b, _ := json.Marshal(map[string]any{"actions": actions})
	_, err := do(ctx, d, Request{Method: "POST", Path: "_aliases", Body: b})
	return err
}

// PutSettings sends the changed index settings only; a nil value puts
// the setting back to its default.
func PutSettings(ctx context.Context, d Doer, index string, changed map[string]*string) error {
	b, _ := json.Marshal(changed)
	_, err := do(ctx, d, Request{Method: "PUT", Path: indexPath(index) + "/_settings", Body: b})
	return err
}
