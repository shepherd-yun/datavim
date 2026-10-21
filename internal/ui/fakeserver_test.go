package ui

import (
	"context"
	"sync"
	"time"

	"github.com/shepherd-yun/datavim/internal/db"
)

// fakeServer is a db.ServerReader for the server tab tests. A zero
// fakeServer reports every part unavailable.
type fakeServer struct {
	mu        sync.Mutex
	info      db.ServerInfo
	infoErr   error
	samples   []db.ServerSample // returned in turn; the last one repeats
	sampleErr error
	sessions  db.SessionList
	sets      map[string]*db.ResultSet // "locks", "top", "settings", "replication"
	errs      map[string]error         // the same keys, plus "sessions", "cancel", "kill"
	gone      bool                     // cancel and kill report the session already gone
	calls     []string
	cancelled []db.SessionID
	killed    []db.SessionID
}

var errFakeServer = db.Unavailable("fake")

func (f *fakeServer) call(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeServer) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeServer) Info(context.Context) (db.ServerInfo, error) {
	f.call("Info")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.info.Version == "" && f.infoErr == nil {
		return db.ServerInfo{}, errFakeServer
	}
	return f.info, f.infoErr
}

func (f *fakeServer) Sample(context.Context) (db.ServerSample, error) {
	f.call("Sample")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sampleErr != nil {
		return db.ServerSample{}, f.sampleErr
	}
	if len(f.samples) == 0 {
		return db.ServerSample{}, errFakeServer
	}
	s := f.samples[0]
	if len(f.samples) > 1 {
		f.samples = f.samples[1:]
	}
	return s, nil
}

func (f *fakeServer) Sessions(_ context.Context, includeIdle bool) (db.SessionList, error) {
	if includeIdle {
		f.call("Sessions idle")
	} else {
		f.call("Sessions")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["sessions"]; err != nil {
		return db.SessionList{}, err
	}
	if f.sessions.Rows == nil {
		return db.SessionList{}, errFakeServer
	}
	return f.sessions, nil
}

func (f *fakeServer) set(name, key string) (*db.ResultSet, error) {
	f.call(name)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs[key]; err != nil {
		return nil, err
	}
	if rs := f.sets[key]; rs != nil {
		return rs, nil
	}
	return nil, errFakeServer
}

func (f *fakeServer) Locks(context.Context) (*db.ResultSet, error) { return f.set("Locks", "locks") }

func (f *fakeServer) TopStatements(context.Context, int) (*db.ResultSet, error) {
	return f.set("TopStatements", "top")
}

func (f *fakeServer) Settings(context.Context) (*db.ResultSet, error) {
	return f.set("Settings", "settings")
}

func (f *fakeServer) Replication(context.Context) (*db.ResultSet, error) {
	return f.set("Replication", "replication")
}

func (f *fakeServer) CancelQuery(_ context.Context, id db.SessionID) (bool, error) {
	f.call("CancelQuery")
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["cancel"]; err != nil {
		return false, err
	}
	f.cancelled = append(f.cancelled, id)
	return !f.gone, nil
}

func (f *fakeServer) KillSession(_ context.Context, id db.SessionID) (bool, error) {
	f.call("KillSession")
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errs["kill"]; err != nil {
		return false, err
	}
	f.killed = append(f.killed, id)
	return !f.gone, nil
}

// mysqlServer is a fakeServer with an 8.4 server's info and two samples
// 2 seconds apart.
func mysqlServer() *fakeServer {
	at := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	sample := func(questions float64, at time.Time) db.ServerSample {
		return db.ServerSample{
			Facts: []db.Fact{
				{Label: "version", Text: "8.4.2"},
				{Label: "uptime", Kind: db.FactDuration, Num: 273600},
				{Label: "connections", Kind: db.FactCount, Num: 87, Of: 500},
			},
			Metrics: []db.Metric{
				{Name: "QPS", Counter: true, Value: questions},
				{Name: "running", Value: 12},
				{Name: "net out", Counter: true, Value: questions * 1024, Unit: db.UnitBytes},
			},
			At: at,
		}
	}
	return &fakeServer{
		info:    db.ServerInfo{Version: "8.4.2", SettingsTitle: "Variables", CanCancel: true},
		samples: []db.ServerSample{sample(100, at), sample(160, at.Add(2*time.Second))},
	}
}
