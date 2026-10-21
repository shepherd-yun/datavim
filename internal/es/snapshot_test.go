package es

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRepositories(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{
		"GET _snapshot": ok(`{"s3b":{"type":"s3","settings":{"bucket":"b","compress":true}},"fsrepo":{"type":"fs","settings":{"location":"/mnt/backups"}}}`),
	}}
	got, err := Repositories(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	want := []Repository{
		{Name: "fsrepo", Type: "fs", Settings: map[string]string{"location": "/mnt/backups"}},
		{Name: "s3b", Type: "s3", Settings: map[string]string{"bucket": "b", "compress": "true"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
	got, err = Repositories(ctx, &fakeDoer{table: map[string]Response{"GET _snapshot": ok(`{}`)}})
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty: %#v %v", got, err)
	}
	if _, err := Repositories(ctx, &fakeDoer{}); !errors.Is(err, ErrUnsupported) {
		t.Errorf("404: %v", err)
	}
}

func TestSnapshots(t *testing.T) {
	path := "GET _cat/snapshots/repo%201?format=json&h=id,status,start_epoch,duration,indices,total_shards,failed_shards&s=start_epoch:desc"
	d := &fakeDoer{table: map[string]Response{path: ok(`[
		{"id":"s2","status":"SUCCESS","start_epoch":"1729600000","duration":"1.2m","indices":"8","total_shards":"16","failed_shards":"0"},
		{"id":"s1","status":"FAILED","start_epoch":"1729500000","duration":"500ms","indices":"1","total_shards":"2","failed_shards":"2"}
	]`)}}
	got, err := Snapshots(ctx, d, "repo 1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Snapshot{
		{Name: "s2", State: "SUCCESS", Start: time.Unix(1729600000, 0), Duration: 72 * time.Second, Indices: 8, Shards: 16},
		{Name: "s1", State: "FAILED", Start: time.Unix(1729500000, 0), Duration: 500 * time.Millisecond, Indices: 1, Shards: 2, FailedShards: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v", got)
	}
}

func TestParseCatDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"1.2m": 72 * time.Second, "500ms": 500 * time.Millisecond, "12s": 12 * time.Second, "1.5h": 90 * time.Minute,
		"2d": 48 * time.Hour, "": 0, "x": 0, "300micros": 300 * time.Microsecond,
	} {
		if got := parseCatDuration(in); got != want {
			t.Errorf("%q = %v, want %v", in, got, want)
		}
	}
}

func TestGetSnapshot(t *testing.T) {
	d := &fakeDoer{table: map[string]Response{
		"GET _snapshot/repo1/s1":         fixture(t, "snapshot_7.json"),
		"GET _snapshot/repo1/s1/_status": ok(`{"snapshots":[{"snapshot":"s1","state":"PARTIAL","stats":{"incremental":{"file_count":3,"size_in_bytes":100},"total":{"file_count":4,"size_in_bytes":6024},"start_time_in_millis":1,"time_in_millis":2}}]}`),
	}}
	got, err := GetSnapshot(ctx, d, "repo1", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "s1" || got.State != "PARTIAL" || got.Version != "7.10.2" || !got.IncludeGlobalState ||
		!reflect.DeepEqual(got.IndexNames, []string{"users", "orders"}) || got.Indices != 2 || got.Shards != 4 || got.FailedShards != 1 ||
		got.Duration != 12*time.Second || !got.Start.Equal(time.UnixMilli(1792683000000)) || !got.End.Equal(time.UnixMilli(1792683012000)) {
		t.Errorf("got %+v", got)
	}
	if !reflect.DeepEqual(got.Failures, []string{"orders[1] IndexShardSnapshotFailedException[boom]"}) {
		t.Errorf("failures %q", got.Failures)
	}
	if got.SizeBytes != 6024 || got.DoneBytes != 6024 {
		t.Errorf("size %d / %d", got.SizeBytes, got.DoneBytes)
	}

	d = &fakeDoer{table: map[string]Response{
		"GET _snapshot/repo1/s1":         fixture(t, "snapshot_8.json"),
		"GET _snapshot/repo1/s1/_status": ok(`{"snapshots":[{"snapshot":"s1","state":"STARTED","stats":{"incremental":{"size_in_bytes":4000},"processed":{"file_count":1,"size_in_bytes":1200},"total":{"file_count":9,"size_in_bytes":4000}}}]}`),
	}}
	got, err = GetSnapshot(ctx, d, "repo1", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "IN_PROGRESS" || !reflect.DeepEqual(got.DataStreams, []string{"logs-app"}) || got.SizeBytes != 4000 || got.DoneBytes != 1200 || !got.End.IsZero() {
		t.Errorf("8.x: %+v", got)
	}

	d = &fakeDoer{table: map[string]Response{
		"GET _snapshot/repo1/s1":         fixture(t, "snapshot_7.json"),
		"GET _snapshot/repo1/s1/_status": {Status: 500, Body: []byte(`{"error":{"type":"repository_exception","reason":"x"},"status":500}`)},
	}}
	got, err = GetSnapshot(ctx, d, "repo1", "s1")
	if err != nil || got.SizeBytes != -1 {
		t.Errorf("status error: %+v %v", got, err)
	}
}

func TestCreateSnapshot(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{"accepted":true}`), nil }}
	if err := CreateSnapshot(ctx, d, NewSnapshot{Repo: "repo1", Name: "snap-x", Indices: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := CreateSnapshot(ctx, d, NewSnapshot{Repo: "repo1", Name: "snap-y"}); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	if r[0].Method != "PUT" || r[0].Path != "_snapshot/repo1/snap-x?wait_for_completion=false" || !sameJSON(r[0].Body, []byte(`{"indices":"a,b","include_global_state":false}`)) {
		t.Errorf("req %s %s %s", r[0].Method, r[0].Path, r[0].Body)
	}
	if !sameJSON(r[1].Body, []byte(`{"include_global_state":false}`)) {
		t.Errorf("all indices: %s", r[1].Body)
	}
}

func TestDeleteSnapshotAndRepository(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{"acknowledged":true}`), nil }}
	if err := DeleteSnapshot(ctx, d, "repo1", "s/1"); err != nil {
		t.Fatal(err)
	}
	if err := PutRepository(ctx, d, "backups", json.RawMessage(`{"type":"fs","settings":{"location":"/x"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := DeleteRepository(ctx, d, "backups"); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	got := []string{r[0].Method + " " + r[0].Path, r[1].Method + " " + r[1].Path, r[2].Method + " " + r[2].Path}
	if !reflect.DeepEqual(got, []string{"DELETE _snapshot/repo1/s%2F1", "PUT _snapshot/backups", "DELETE _snapshot/backups"}) || string(r[1].Body) != `{"type":"fs","settings":{"location":"/x"}}` {
		t.Errorf("got %q", got)
	}
}

func TestRestoreSnapshot(t *testing.T) {
	d := &fakeDoer{fn: func(Request) (Response, error) { return ok(`{"accepted":true}`), nil }}
	if err := RestoreSnapshot(ctx, d, RestoreSpec{Repo: "r", Name: "s", Indices: []string{"orders", "users"}}); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSnapshot(ctx, d, RestoreSpec{Repo: "r", Name: "s", RenamePattern: "(.+)", RenameReplacement: "restored-$1", IncludeAliases: true}); err != nil {
		t.Fatal(err)
	}
	r := d.requests()
	if r[0].Method != "POST" || r[0].Path != "_snapshot/r/s/_restore?wait_for_completion=false" ||
		!sameJSON(r[0].Body, []byte(`{"indices":"orders,users","include_global_state":false,"include_aliases":false}`)) {
		t.Errorf("plain: %s %s %s", r[0].Method, r[0].Path, r[0].Body)
	}
	if !sameJSON(r[1].Body, []byte(`{"include_global_state":false,"include_aliases":true,"rename_pattern":"(.+)","rename_replacement":"restored-$1"}`)) {
		t.Errorf("rename: %s", r[1].Body)
	}
}
