package ui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/session"
)

// fakeRegistry is an in-memory Schema Registry.
type fakeRegistry struct {
	mu        sync.Mutex
	schemas   []kafka.Schema // every live version
	compat    map[string]string
	global    string
	calls     []string // "Method arg"
	errs      map[string]error
	checkOK   bool
	checkMsgs []string
	ro        bool
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{compat: map[string]string{}, global: "BACKWARD", errs: map[string]error{}, checkOK: true}
}

// add registers a version of subject with id.
func (r *fakeRegistry) add(subject string, version, id int, typ, text string, refs ...kafka.SchemaRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.schemas = append(r.schemas, kafka.Schema{ID: id, Subject: subject, Version: version, Type: typ, Text: text, References: refs})
}

func (r *fakeRegistry) call(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *fakeRegistry) callsOf(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, c := range r.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			out = append(out, c)
		}
	}
	return out
}

func (r *fakeRegistry) Subjects(context.Context) ([]kafka.Subject, error) {
	r.call("Subjects")
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errs["Subjects"]; err != nil {
		return nil, err
	}
	latest := map[string]kafka.Schema{}
	for _, s := range r.schemas {
		if s.Version > latest[s.Subject].Version {
			latest[s.Subject] = s
		}
	}
	var out []kafka.Subject
	for name, s := range latest {
		out = append(out, kafka.Subject{Name: name, Latest: s.Version, Type: s.Type})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (r *fakeRegistry) Versions(_ context.Context, subject string) ([]int, error) {
	r.call("Versions " + subject)
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int
	for _, s := range r.schemas {
		if s.Subject == subject {
			out = append(out, s.Version)
		}
	}
	slices.Sort(out)
	return out, nil
}

func (r *fakeRegistry) Schema(_ context.Context, subject string, version int) (kafka.Schema, error) {
	r.call(fmt.Sprintf("Schema %s %d", subject, version))
	r.mu.Lock()
	defer r.mu.Unlock()
	var best kafka.Schema
	for _, s := range r.schemas {
		if s.Subject == subject && (s.Version == version || (version == -1 && s.Version > best.Version)) {
			best = s
		}
	}
	if best.Subject == "" {
		return kafka.Schema{}, fmt.Errorf("schema registry: %s v%d not found", subject, version)
	}
	return best, nil
}

func (r *fakeRegistry) SchemaByID(_ context.Context, id int) (kafka.Schema, error) {
	r.call(fmt.Sprintf("SchemaByID %d", id))
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.schemas {
		if s.ID == id {
			return s, nil
		}
	}
	return kafka.Schema{}, fmt.Errorf("schema %d: not found", id)
}

func (r *fakeRegistry) Resolve(ctx context.Context, s kafka.Schema) (map[string]kafka.Schema, error) {
	out := map[string]kafka.Schema{}
	for _, ref := range s.References {
		dep, err := r.Schema(ctx, ref.Subject, ref.Version)
		if err != nil {
			return nil, err
		}
		out[ref.Name] = dep
	}
	return out, nil
}

func (r *fakeRegistry) Compatibility(_ context.Context, subject string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.compat[subject]; ok {
		return l, false, nil
	}
	return r.global, true, nil
}

func (r *fakeRegistry) Check(_ context.Context, subject string, _ kafka.Schema) (bool, []string, error) {
	r.call("Check " + subject)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checkOK, r.checkMsgs, r.errs["Check"]
}

func (r *fakeRegistry) Register(_ context.Context, subject string, s kafka.Schema) (kafka.Schema, error) {
	if r.ro {
		return kafka.Schema{}, session.ErrReadOnly
	}
	r.call("Register " + subject)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.errs["Register"]; err != nil {
		return kafka.Schema{}, err
	}
	maxID, maxV := 0, 0
	for _, x := range r.schemas {
		maxID = max(maxID, x.ID)
		if x.Subject == subject {
			maxV = max(maxV, x.Version)
		}
	}
	s.Subject, s.Version, s.ID = subject, maxV+1, maxID+1
	r.schemas = append(r.schemas, s)
	return s, nil
}

func (r *fakeRegistry) SetCompatibility(_ context.Context, subject, level string) error {
	if r.ro {
		return session.ErrReadOnly
	}
	r.call("SetCompatibility " + subject + " " + level)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.compat[subject] = level
	return nil
}

func (r *fakeRegistry) DeleteVersion(_ context.Context, subject string, version int) error {
	if r.ro {
		return session.ErrReadOnly
	}
	r.call(fmt.Sprintf("DeleteVersion %s %d", subject, version))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.schemas = slices.DeleteFunc(r.schemas, func(s kafka.Schema) bool { return s.Subject == subject && s.Version == version })
	return nil
}

func (f *fakeBackend) SchemaRegistry(_ context.Context, name string) (kafka.Registry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registryOpens++
	cfg, err := f.kafkaConn(name)
	if err != nil {
		return nil, err
	}
	if cfg.Params["schema_registry_url"] == "" {
		return nil, session.ErrNoRegistry
	}
	if f.registryErr != nil {
		return nil, f.registryErr
	}
	if f.registry == nil {
		f.registry = newFakeRegistry()
	}
	f.registry.ro = cfg.IsReadOnly()
	return f.registry, nil
}

// withRegistry is cfg with a schema_registry_url.
func withRegistry(cfg config.Connection) config.Connection {
	p := map[string]string{"schema_registry_url": "http://sr:8081"}
	for k, v := range cfg.Params {
		p[k] = v
	}
	cfg.Params = p
	return cfg
}
