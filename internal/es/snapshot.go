package es

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shepherd-yun/datavim/internal/esx"
)

// Repository is a registered snapshot repository.
type Repository struct {
	Name, Type string
	Settings   map[string]string
}

// Repositories lists the snapshot repositories by name.
func Repositories(ctx context.Context, d Doer) ([]Repository, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_snapshot"})
	if missingAPI(err) {
		return nil, ErrUnsupported
	}
	if err != nil {
		return nil, err
	}
	var r map[string]struct {
		Type     string                     `json:"type"`
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := []Repository{}
	for name, v := range r {
		repo := Repository{Name: name, Type: v.Type, Settings: map[string]string{}}
		for k, raw := range v.Settings {
			repo.Settings[k], _ = esx.CellText(raw, true)
		}
		out = append(out, repo)
	}
	slices.SortFunc(out, func(a, b Repository) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Snapshot is one snapshot of a repository, as _cat/snapshots lists it.
type Snapshot struct {
	Name, State          string // SUCCESS / IN_PROGRESS / PARTIAL / FAILED / INCOMPATIBLE
	Start                time.Time
	Duration             time.Duration
	Indices              int
	Shards, FailedShards int
}

func snapshotPath(repo, name string) string {
	return "_snapshot/" + url.PathEscape(repo) + "/" + url.PathEscape(name)
}

// Snapshots lists a repository's snapshots, newest first. It does not read
// their sizes: that takes a _status per snapshot.
func Snapshots(ctx context.Context, d Doer, repo string) ([]Snapshot, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_cat/snapshots/" + url.PathEscape(repo) +
		"?format=json&h=id,status,start_epoch,duration,indices,total_shards,failed_shards&s=start_epoch:desc"})
	if err != nil {
		return nil, err
	}
	var rows []map[string]string
	if err := json.Unmarshal(resp.Body, &rows); err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(rows))
	for _, r := range rows {
		s := Snapshot{
			Name: r["id"], State: r["status"], Duration: parseCatDuration(r["duration"]),
			Indices: atoi(r["indices"]), Shards: atoi(r["total_shards"]), FailedShards: atoi(r["failed_shards"]),
		}
		if sec, err := strconv.ParseInt(r["start_epoch"], 10, 64); err == nil {
			s.Start = time.Unix(sec, 0)
		}
		out = append(out, s)
	}
	return out, nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// parseCatDuration reads a _cat duration ("1.2m", "500ms", "2d"); what it
// cannot read is 0.
func parseCatDuration(s string) time.Duration {
	units := []struct {
		suffix string
		unit   time.Duration
	}{
		{"nanos", time.Nanosecond}, {"micros", time.Microsecond}, {"ms", time.Millisecond},
		{"s", time.Second}, {"m", time.Minute}, {"h", time.Hour}, {"d", 24 * time.Hour},
	}
	for _, u := range units {
		if num, ok := strings.CutSuffix(s, u.suffix); ok {
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0
			}
			return time.Duration(f * float64(u.unit))
		}
	}
	return 0
}

// SnapshotDetail is one snapshot with its indices, failures and size.
type SnapshotDetail struct {
	Snapshot
	End                time.Time // zero while running
	IndexNames         []string
	DataStreams        []string
	IncludeGlobalState bool
	Version            string
	Failures           []string // "<index>[<shard>] <reason>"
	SizeBytes          int64    // -1: unknown
	DoneBytes          int64    // bytes copied so far; SizeBytes once done
}

// GetSnapshot reads a snapshot and, from its _status, its size; a _status
// that fails leaves the size unknown.
func GetSnapshot(ctx context.Context, d Doer, repo, name string) (SnapshotDetail, error) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: snapshotPath(repo, name)})
	if err != nil {
		return SnapshotDetail{}, err
	}
	var r struct {
		Snapshots []struct {
			Snapshot           string   `json:"snapshot"`
			Version            string   `json:"version"`
			Indices            []string `json:"indices"`
			DataStreams        []string `json:"data_streams"`
			IncludeGlobalState bool     `json:"include_global_state"`
			State              string   `json:"state"`
			Start              int64    `json:"start_time_in_millis"`
			End                int64    `json:"end_time_in_millis"`
			Duration           int64    `json:"duration_in_millis"`
			Failures           []struct {
				Index   string `json:"index"`
				ShardID int    `json:"shard_id"`
				Reason  string `json:"reason"`
			} `json:"failures"`
			Shards struct{ Total, Failed int } `json:"shards"`
		} `json:"snapshots"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return SnapshotDetail{}, err
	}
	if len(r.Snapshots) == 0 {
		return SnapshotDetail{}, fmt.Errorf("%w: snapshot %s", ErrNotFound, name)
	}
	s := r.Snapshots[0]
	out := SnapshotDetail{
		Snapshot: Snapshot{
			Name: s.Snapshot, State: s.State, Duration: time.Duration(s.Duration) * time.Millisecond,
			Indices: len(s.Indices), Shards: s.Shards.Total, FailedShards: s.Shards.Failed,
		},
		IndexNames: s.Indices, DataStreams: s.DataStreams, IncludeGlobalState: s.IncludeGlobalState,
		Version: s.Version, SizeBytes: -1, DoneBytes: -1,
	}
	if s.Start > 0 {
		out.Start = time.UnixMilli(s.Start)
	}
	if s.End > 0 {
		out.End = time.UnixMilli(s.End)
	}
	for _, f := range s.Failures {
		out.Failures = append(out.Failures, fmt.Sprintf("%s[%d] %s", f.Index, f.ShardID, f.Reason))
	}
	out.SizeBytes, out.DoneBytes = snapshotSize(ctx, d, repo, name)
	return out, nil
}

func snapshotSize(ctx context.Context, d Doer, repo, name string) (size, done int64) {
	resp, err := do(ctx, d, Request{Method: "GET", Path: snapshotPath(repo, name) + "/_status"})
	if err != nil {
		return -1, -1
	}
	type bytes struct {
		Size *int64 `json:"size_in_bytes"`
	}
	var r struct {
		Snapshots []struct {
			Stats struct {
				Processed bytes `json:"processed"`
				Total     bytes `json:"total"`
			} `json:"stats"`
		} `json:"snapshots"`
	}
	if json.Unmarshal(resp.Body, &r) != nil || len(r.Snapshots) == 0 || r.Snapshots[0].Stats.Total.Size == nil {
		return -1, -1
	}
	st := r.Snapshots[0].Stats
	size, done = *st.Total.Size, *st.Total.Size
	if st.Processed.Size != nil {
		done = *st.Processed.Size
	}
	return size, done
}

// NewSnapshot is a snapshot to take; no Indices means all of them.
type NewSnapshot struct {
	Repo, Name         string
	Indices            []string
	IncludeGlobalState bool
}

// CreateSnapshot starts a snapshot and returns without waiting for it.
func CreateSnapshot(ctx context.Context, d Doer, s NewSnapshot) error {
	body := map[string]any{"include_global_state": s.IncludeGlobalState}
	if len(s.Indices) > 0 {
		body["indices"] = strings.Join(s.Indices, ",")
	}
	b, _ := json.Marshal(body)
	_, err := do(ctx, d, Request{Method: "PUT", Path: snapshotPath(s.Repo, s.Name) + "?wait_for_completion=false", Body: b})
	return err
}

// DeleteSnapshot deletes a snapshot.
func DeleteSnapshot(ctx context.Context, d Doer, repo, name string) error {
	_, err := do(ctx, d, Request{Method: "DELETE", Path: snapshotPath(repo, name)})
	return err
}

// RestoreSpec is a restore; no Indices means all of the snapshot's. The
// cluster's global state is never restored.
type RestoreSpec struct {
	Repo, Name                       string
	Indices                          []string
	RenamePattern, RenameReplacement string
	IncludeAliases                   bool
}

// RestoreSnapshot starts restoring a snapshot and returns without waiting.
func RestoreSnapshot(ctx context.Context, d Doer, r RestoreSpec) error {
	body := map[string]any{"include_global_state": false, "include_aliases": r.IncludeAliases}
	if len(r.Indices) > 0 {
		body["indices"] = strings.Join(r.Indices, ",")
	}
	if r.RenamePattern != "" {
		body["rename_pattern"] = r.RenamePattern
		body["rename_replacement"] = r.RenameReplacement
	}
	b, _ := json.Marshal(body)
	_, err := do(ctx, d, Request{Method: "POST", Path: snapshotPath(r.Repo, r.Name) + "/_restore?wait_for_completion=false", Body: b})
	return err
}

// PutRepository registers (or replaces) a snapshot repository.
func PutRepository(ctx context.Context, d Doer, name string, body json.RawMessage) error {
	_, err := do(ctx, d, Request{Method: "PUT", Path: "_snapshot/" + url.PathEscape(name), Body: body})
	return err
}

// DeleteRepository unregisters a repository; its files stay in the storage.
func DeleteRepository(ctx context.Context, d Doer, name string) error {
	_, err := do(ctx, d, Request{Method: "DELETE", Path: "_snapshot/" + url.PathEscape(name)})
	return err
}
