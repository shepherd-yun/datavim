package es

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Task is one long-running task (spec §7.1 Tasks).
type Task struct {
	ID, Node, Action, Description string
	Running                       time.Duration
	Cancellable                   bool
	// Done counts created + updated + deleted + noops + version conflicts;
	// both are -1 when the task reports no status.
	Total, Done int64
}

// ErrTaskGone is a cancel of a task that has already finished.
var ErrTaskGone = errors.New("task already finished")

// Tasks lists the top-level reindex, by-query, force merge, snapshot and
// resize tasks, the longest running first.
func Tasks(ctx context.Context, d Doer) ([]Task, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_tasks?actions=*reindex,*byquery,*forcemerge,cluster:admin/snapshot/*,indices:admin/resize*&detailed=true&group_by=none"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Tasks []struct {
			Node        string `json:"node"`
			ID          int64  `json:"id"`
			Action      string `json:"action"`
			Description string `json:"description"`
			Running     int64  `json:"running_time_in_nanos"`
			Cancellable bool   `json:"cancellable"`
			Parent      string `json:"parent_task_id"`
			Status      *struct {
				Total, Created, Updated, Deleted, Noops int64
				Conflicts                               int64 `json:"version_conflicts"`
			} `json:"status"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	var out []Task
	for _, t := range r.Tasks {
		if t.Parent != "" {
			continue
		}
		k := Task{
			ID: t.Node + ":" + strconv.FormatInt(t.ID, 10), Node: t.Node, Action: t.Action, Description: t.Description,
			Running: time.Duration(t.Running), Cancellable: t.Cancellable, Total: -1, Done: -1,
		}
		if s := t.Status; s != nil {
			k.Total, k.Done = s.Total, s.Created+s.Updated+s.Deleted+s.Noops+s.Conflicts
		}
		out = append(out, k)
	}
	slices.SortStableFunc(out, func(a, b Task) int { return cmp.Compare(b.Running, a.Running) })
	return out, nil
}

// CancelTask cancels a task; one that has already finished is
// ErrTaskGone.
func CancelTask(ctx context.Context, d Doer, id string) error {
	resp, err := do(ctx, d, Request{Method: "POST", Path: "_tasks/" + url.PathEscape(id) + "/_cancel"})
	if errors.Is(err, ErrNotFound) {
		return ErrTaskGone
	}
	if err != nil {
		return err
	}
	// some versions answer 200 with the not-found in node_failures
	var r struct {
		Failures []json.RawMessage `json:"node_failures"`
	}
	if json.Unmarshal(resp.Body, &r) == nil {
		for _, f := range r.Failures {
			if bytes.Contains(f, []byte("resource_not_found_exception")) {
				return ErrTaskGone
			}
		}
	}
	return nil
}

func taskID(resp Response) (string, error) {
	var r struct {
		Task string `json:"task"`
	}
	err := json.Unmarshal(resp.Body, &r)
	return r.Task, err
}

// ReindexSpec is a reindex (spec §7.1).
type ReindexSpec struct {
	Source, Dest string
	Query        json.RawMessage // nil: every document
	OpType       string          // "index" or "create"
	Conflicts    string          // "abort" or "proceed"
}

// Reindex starts a reindex as a task and returns its id.
func Reindex(ctx context.Context, d Doer, r ReindexSpec) (string, error) {
	type source struct {
		Index string          `json:"index"`
		Query json.RawMessage `json:"query,omitempty"`
	}
	type dest struct {
		Index  string `json:"index"`
		OpType string `json:"op_type,omitempty"`
	}
	b, _ := json.Marshal(struct {
		Source    source `json:"source"`
		Dest      dest   `json:"dest"`
		Conflicts string `json:"conflicts,omitempty"`
	}{source{r.Source, r.Query}, dest{r.Dest, r.OpType}, r.Conflicts})
	resp, err := do(ctx, d, Request{Method: "POST", Path: "_reindex?wait_for_completion=false", Body: b})
	if err != nil {
		return "", err
	}
	return taskID(resp)
}

// UpdateByQuery starts running a painless script on what query matches in
// the named indices as a task and returns its id.
func UpdateByQuery(ctx context.Context, d Doer, indices []string, query json.RawMessage, script string) (string, error) {
	if err := concrete(indices); err != nil {
		return "", err
	}
	if len(query) == 0 {
		query = json.RawMessage(`{"match_all":{}}`)
	}
	type painless struct {
		Source string `json:"source"`
		Lang   string `json:"lang"`
	}
	b, _ := json.Marshal(struct {
		Query  json.RawMessage `json:"query"`
		Script painless        `json:"script"`
	}{query, painless{script, "painless"}})
	resp, err := do(ctx, d, Request{
		Method: "POST", Path: strings.Join(indices, ",") + "/_update_by_query?wait_for_completion=false", Body: b,
	})
	if err != nil {
		return "", err
	}
	return taskID(resp)
}

// PutMapping adds fields to an index's mapping.
func PutMapping(ctx context.Context, d Doer, index string, properties json.RawMessage) error {
	b, _ := json.Marshal(map[string]json.RawMessage{"properties": properties})
	_, err := do(ctx, d, Request{Method: "PUT", Path: indexPath(index) + "/_mapping", Body: b})
	return err
}

// settingValue is a cluster setting value: null for nil, JSON for a value
// that is a JSON array or object, a string otherwise.
func settingValue(v *string) json.RawMessage {
	if v == nil {
		return json.RawMessage("null")
	}
	s := strings.TrimSpace(*v)
	if (strings.HasPrefix(s, "[") || strings.HasPrefix(s, "{")) && json.Valid([]byte(s)) {
		var b bytes.Buffer
		_ = json.Compact(&b, []byte(s))
		return b.Bytes()
	}
	b, _ := json.Marshal(*v)
	return b
}

// PutClusterSettings changes cluster settings; a nil value puts one back
// to its default. Nothing is sent when both maps are empty.
func PutClusterSettings(ctx context.Context, d Doer, persistent, transient map[string]*string) error {
	if len(persistent) == 0 && len(transient) == 0 {
		return nil
	}
	body := map[string]map[string]json.RawMessage{}
	for scope, m := range map[string]map[string]*string{"persistent": persistent, "transient": transient} {
		if len(m) == 0 {
			continue
		}
		body[scope] = map[string]json.RawMessage{}
		for k, v := range m {
			body[scope][k] = settingValue(v)
		}
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = do(ctx, d, Request{Method: "PUT", Path: "_cluster/settings", Body: b})
	return err
}

// IndexExists reports whether name is an index, alias or data stream.
func IndexExists(ctx context.Context, d Doer, name string) (bool, error) {
	_, err := do(ctx, d, Request{Method: "HEAD", Path: url.PathEscape(name)})
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// RawSettings are an index's own settings, flat.
func RawSettings(ctx context.Context, d Doer, index string) (map[string]json.RawMessage, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: indexPath(index) + "/_settings?flat_settings=true"})
	if err != nil {
		return nil, err
	}
	var r map[string]struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	if v, ok := r[index]; ok {
		return v.Settings, nil
	}
	for _, v := range r {
		return v.Settings, nil
	}
	return nil, ErrNotFound
}
