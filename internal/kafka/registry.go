package kafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/sr"
	"golang.org/x/sync/errgroup"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Schema is one version of a subject in a Schema Registry.
type Schema struct {
	ID         int
	Subject    string
	Version    int
	Type       string // "AVRO", "PROTOBUF" or "JSON"
	Text       string
	References []SchemaRef
}

// SchemaRef is a schema another one imports, by the name it uses for it.
type SchemaRef struct {
	Name, Subject string
	Version       int
}

// Subject is a registry subject with its latest version.
type Subject struct {
	Name   string
	Latest int
	Type   string
}

// RegistryConfig is what OpenRegistry needs. User and Password may also
// come in URL's userinfo; they never stay in the URL.
type RegistryConfig struct {
	URL, User, Password string
	TLSCA               string
	TLSSkipVerify       bool
	// Dial opens every HTTP connection (an SSH tunnel); nil dials directly.
	Dial db.DialFunc
}

// ErrRegistryAuth is a registry that refused the user and password.
var ErrRegistryAuth = errors.New("schema registry: authentication failed")

// Registry is one connection's Schema Registry (the Confluent API).
// Schemas by id are cached for the Registry's life.
type Registry interface {
	Subjects(ctx context.Context) ([]Subject, error) // by name, soft-deleted left out
	Versions(ctx context.Context, subject string) ([]int, error)
	Schema(ctx context.Context, subject string, version int) (Schema, error) // version -1: latest
	SchemaByID(ctx context.Context, id int) (Schema, error)
	// Resolve returns every schema s references, directly or not, by reference name.
	Resolve(ctx context.Context, s Schema) (map[string]Schema, error)
	Compatibility(ctx context.Context, subject string) (level string, global bool, err error)
	// Check asks whether s may be registered under subject; msgs say why not.
	Check(ctx context.Context, subject string, s Schema) (ok bool, msgs []string, err error)
	Register(ctx context.Context, subject string, s Schema) (Schema, error)
	SetCompatibility(ctx context.Context, subject, level string) error
	DeleteVersion(ctx context.Context, subject string, version int) error // soft
}

type registry struct {
	cl *sr.Client

	mu   sync.Mutex
	byID map[int]Schema
}

type noGlobalKey struct{}

// OpenRegistry makes a registry client; it does not connect yet.
func OpenRegistry(cfg RegistryConfig) (Registry, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("schema registry: schema_registry_url must be http(s)://host[:port]")
	}
	user, pw := cfg.User, cfg.Password
	if u.User != nil {
		if user == "" {
			user = u.User.Username()
		}
		if p, ok := u.User.Password(); ok && pw == "" {
			pw = p
		}
		u.User = nil
	}
	tc := &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in via tls_skip_verify
	if cfg.TLSCA != "" {
		pem, err := os.ReadFile(expandHome(cfg.TLSCA))
		if err != nil {
			return nil, fmt.Errorf("schema registry tls_ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("schema registry tls_ca: no certificates in %s", cfg.TLSCA)
		}
		tc.RootCAs = pool
	}
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext:     baseDial(Config{Dial: cfg.Dial}),
		TLSClientConfig: tc,
	}}
	opts := []sr.ClientOpt{
		sr.URLs(u.String()), sr.HTTPClient(hc), sr.UserAgent("sqlvim"),
		// GET /config/{subject} falls back to the global level on some
		// registries unless told not to; Compatibility wants to know which.
		sr.PreReq(func(req *http.Request) error {
			if req.Context().Value(noGlobalKey{}) != nil {
				q := req.URL.Query()
				q.Set("defaultToGlobal", "false")
				req.URL.RawQuery = q.Encode()
			}
			return nil
		}),
	}
	if user != "" {
		opts = append(opts, sr.BasicAuth(user, pw))
	}
	cl, err := sr.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("schema registry: %w", err)
	}
	return &registry{cl: cl, byID: map[int]Schema{}}, nil
}

// regErr is err without anything the registry put in it but its message:
// 401 and 403 are ErrRegistryAuth.
func regErr(err error) error {
	if err == nil {
		return nil
	}
	var re *sr.ResponseError
	if errors.As(err, &re) {
		if re.StatusCode == http.StatusUnauthorized || re.StatusCode == http.StatusForbidden {
			return ErrRegistryAuth
		}
		return fmt.Errorf("schema registry: %s", strings.TrimSpace(re.Error()))
	}
	return fmt.Errorf("schema registry: %w", err)
}

// notFound reports a 404, or the registry error code when code is not 0.
func notFound(err error, code int) bool {
	var re *sr.ResponseError
	if !errors.As(err, &re) {
		return false
	}
	if code == 0 {
		return re.StatusCode == http.StatusNotFound
	}
	return re.ErrorCode == code
}

func fromSR(ss sr.SubjectSchema) Schema {
	s := Schema{ID: ss.ID, Subject: ss.Subject, Version: ss.Version, Type: ss.Type.String(), Text: ss.Schema.Schema}
	for _, r := range ss.References {
		s.References = append(s.References, SchemaRef{Name: r.Name, Subject: r.Subject, Version: r.Version})
	}
	return s
}

func toSR(s Schema) (sr.Schema, error) {
	var t sr.SchemaType
	if err := t.UnmarshalText([]byte(s.Type)); err != nil {
		return sr.Schema{}, fmt.Errorf("unsupported schema type %q", s.Type)
	}
	out := sr.Schema{Schema: s.Text, Type: t}
	for _, r := range s.References {
		out.References = append(out.References, sr.SchemaReference{Name: r.Name, Subject: r.Subject, Version: r.Version})
	}
	return out, nil
}

func (r *registry) remember(s Schema) {
	if s.ID == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[s.ID] = s
}

func (r *registry) Subjects(ctx context.Context) ([]Subject, error) {
	names, err := r.cl.Subjects(ctx)
	if err != nil {
		return nil, regErr(err)
	}
	slices.Sort(names)
	out := make([]Subject, len(names))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for i, name := range names {
		g.Go(func() error {
			s, err := r.Schema(gctx, name, -1)
			if err != nil {
				return err
			}
			out[i] = Subject{Name: name, Latest: s.Version, Type: s.Type}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *registry) Versions(ctx context.Context, subject string) ([]int, error) {
	vs, err := r.cl.SubjectVersions(ctx, subject)
	if err != nil {
		return nil, regErr(err)
	}
	slices.Sort(vs)
	return vs, nil
}

func (r *registry) Schema(ctx context.Context, subject string, version int) (Schema, error) {
	ss, err := r.cl.SchemaByVersion(ctx, subject, version)
	if err != nil {
		return Schema{}, regErr(err)
	}
	s := fromSR(ss)
	r.remember(s)
	return s, nil
}

func (r *registry) SchemaByID(ctx context.Context, id int) (Schema, error) {
	r.mu.Lock()
	s, ok := r.byID[id]
	r.mu.Unlock()
	if ok {
		return s, nil
	}
	sch, err := r.cl.SchemaByID(ctx, id)
	if notFound(err, 0) {
		return Schema{}, fmt.Errorf("schema %d: not found", id)
	}
	if err != nil {
		return Schema{}, regErr(err)
	}
	s = fromSR(sr.SubjectSchema{ID: id, Schema: sch})
	if vs, err := r.cl.SchemaVersionsByID(ctx, id); err == nil && len(vs) > 0 {
		s.Subject, s.Version = vs[0].Subject, vs[0].Version
	}
	r.remember(s)
	return s, nil
}

func (r *registry) Resolve(ctx context.Context, s Schema) (map[string]Schema, error) {
	out := map[string]Schema{}
	queue := slices.Clone(s.References)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if _, done := out[ref.Name]; done {
			continue
		}
		dep, err := r.Schema(ctx, ref.Subject, ref.Version)
		if err != nil {
			return nil, fmt.Errorf("reference %s (%s v%d): %w", ref.Name, ref.Subject, ref.Version, err)
		}
		out[ref.Name] = dep
		queue = append(queue, dep.References...)
	}
	return out, nil
}

func (r *registry) Compatibility(ctx context.Context, subject string) (string, bool, error) {
	res := r.cl.Compatibility(context.WithValue(ctx, noGlobalKey{}, true), subject)
	if c := res[0]; c.Err == nil && c.Level != 0 {
		return c.Level.String(), false, nil
	} else if c.Err != nil && !notFound(c.Err, 0) {
		return "", false, regErr(c.Err)
	}
	res = r.cl.Compatibility(ctx)
	if c := res[0]; c.Err != nil {
		return "", true, regErr(c.Err)
	}
	return res[0].Level.String(), true, nil
}

func (r *registry) Check(ctx context.Context, subject string, s Schema) (bool, []string, error) {
	sch, err := toSR(s)
	if err != nil {
		return false, nil, err
	}
	res, err := r.cl.CheckCompatibility(sr.WithParams(ctx, sr.Verbose), subject, -1, sch)
	if notFound(err, sr.ErrSubjectNotFound.Code) || notFound(err, sr.ErrVersionNotFound.Code) {
		return true, nil, nil // nothing to be compatible with yet
	}
	if err != nil {
		return false, nil, regErr(err)
	}
	return res.Is, res.Messages, nil
}

func (r *registry) Register(ctx context.Context, subject string, s Schema) (Schema, error) {
	sch, err := toSR(s)
	if err != nil {
		return Schema{}, err
	}
	ss, err := r.cl.CreateSchema(ctx, subject, sch)
	if err != nil {
		return Schema{}, regErr(err)
	}
	out := fromSR(ss)
	r.remember(out)
	return out, nil
}

func (r *registry) SetCompatibility(ctx context.Context, subject, level string) error {
	var l sr.CompatibilityLevel
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return fmt.Errorf("unknown compatibility level %q", level)
	}
	return regErr(r.cl.SetCompatibility(ctx, sr.SetCompatibility{Level: l}, subject)[0].Err)
}

func (r *registry) DeleteVersion(ctx context.Context, subject string, version int) error {
	return regErr(r.cl.DeleteSchema(ctx, subject, version, sr.SoftDelete))
}
