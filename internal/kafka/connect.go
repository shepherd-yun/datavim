package kafka

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shepherd-yun/datavim/internal/db"
)

// ConnectConfig is what OpenConnect needs, as RegistryConfig. User and
// Password may also come in URL's userinfo; they never stay in the URL.
type ConnectConfig struct {
	URL, User, Password    string
	TLSCA, TLSCert, TLSKey string
	TLSSkipVerify          bool
	// Dial opens every HTTP connection (an SSH tunnel); nil dials directly.
	Dial db.DialFunc
}

// Connector is one connector of a Connect cluster, with its tasks.
type Connector struct {
	Name, Type, Class string // Type "source" or "sink"
	State, Worker     string // RUNNING, PAUSED, STOPPED, FAILED, UNASSIGNED, RESTARTING
	Trace             string
	Tasks             []ConnectorTask
	Config            map[string]string
}

// ConnectorTask is one task of a connector.
type ConnectorTask struct {
	ID                   int
	State, Worker, Trace string
}

// ConfigProblem is one config key the cluster's validation rejected.
type ConfigProblem struct {
	Name   string
	Errors []string
}

var (
	// ErrConnectAuth is a Connect cluster that refused the user and password.
	ErrConnectAuth = errors.New("kafka connect: authentication failed")
	// ErrConnectorChanged is a config edit whose connector changed since it was read.
	ErrConnectorChanged = errors.New("connector config changed since you opened it; review again")
	// ErrConnectorNotFound is a connector the cluster does not have.
	ErrConnectorNotFound = errors.New("kafka connect: no such connector")
)

// Connect is one Kafka Connect cluster (its REST API).
type Connect interface {
	Version(ctx context.Context) (string, error)
	// Connectors lists every connector with status and config, by name.
	Connectors(ctx context.Context) ([]Connector, error)
	Connector(ctx context.Context, name string) (Connector, error)
	// Validate asks the connector class's plugin about cfg.
	Validate(ctx context.Context, class string, cfg map[string]string) ([]ConfigProblem, error)
	Pause(ctx context.Context, name string) error
	Resume(ctx context.Context, name string) error
	// Restart restarts the connector and its failed tasks (a cluster
	// before 3.0 ignores the parameters and restarts the connector only).
	Restart(ctx context.Context, name string) error
	RestartTask(ctx context.Context, name string, task int) error
	// SetConfig re-reads the config, gives up with ErrConnectorChanged when
	// it is no longer old, and replaces it with cfg.
	SetConfig(ctx context.Context, name string, old, cfg map[string]string) error
}

type connect struct {
	hc         *http.Client
	base       string // scheme://host[:port][/path], no userinfo, no trailing /
	user, pass string
}

// OpenConnect makes a Connect client; it does not connect yet.
func OpenConnect(cfg ConnectConfig) (Connect, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("kafka connect: connect_url must be http(s)://host[:port]")
	}
	c := &connect{user: cfg.User, pass: cfg.Password}
	if u.User != nil {
		if c.user == "" {
			c.user = u.User.Username()
		}
		if p, ok := u.User.Password(); ok && c.pass == "" {
			c.pass = p
		}
		u.User = nil
	}
	u.RawQuery, u.Fragment = "", ""
	c.base = strings.TrimSuffix(u.String(), "/")
	tc, err := tlsConfig(Config{TLSCA: cfg.TLSCA, TLSCert: cfg.TLSCert, TLSKey: cfg.TLSKey, TLSSkipVerify: cfg.TLSSkipVerify})
	if err != nil {
		return nil, fmt.Errorf("kafka connect: %w", err)
	}
	c.hc = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext:     baseDial(Config{Dial: cfg.Dial}),
		TLSClientConfig: tc,
	}}
	return c, nil
}

// do sends one request; in (if not nil) is the JSON body, out (if not nil)
// what the JSON answer decodes into. Errors carry the cluster's message,
// never the URL.
func (c *connect) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return fmt.Errorf("kafka connect: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "datavim")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.user != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // its URL is ours: keep only why
		}
		return wrap(ctx, fmt.Errorf("kafka connect: %w", err), "")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return wrap(ctx, fmt.Errorf("kafka connect: %w", err), "")
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrConnectAuth
	case resp.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/connectors/"):
		return fmt.Errorf("%w: %s", ErrConnectorNotFound, connectMessage(data, resp.StatusCode))
	case resp.StatusCode >= 300:
		return fmt.Errorf("kafka connect: %s", connectMessage(data, resp.StatusCode))
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("kafka connect: unexpected answer: %w", err)
	}
	return nil
}

// connectMessage is the message of a Connect error body, or the status.
func connectMessage(data []byte, code int) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &e) == nil && e.Message != "" {
		return fmt.Sprintf("%s (%d)", e.Message, code)
	}
	return fmt.Sprintf("HTTP %d %s", code, http.StatusText(code))
}

func connectorPath(name string, rest ...string) string {
	return "/connectors/" + url.PathEscape(name) + strings.Join(append([]string{""}, rest...), "/")
}

type connectState struct {
	State    string `json:"state"`
	WorkerID string `json:"worker_id"`
	Trace    string `json:"trace"`
}

type connectStatus struct {
	Connector connectState `json:"connector"`
	Tasks     []struct {
		ID int `json:"id"`
		connectState
	} `json:"tasks"`
	Type string `json:"type"`
}

func (s connectStatus) fill(c *Connector) {
	c.State, c.Worker, c.Trace = s.Connector.State, s.Connector.WorkerID, s.Connector.Trace
	if s.Type != "" {
		c.Type = s.Type
	}
	c.Tasks = c.Tasks[:0]
	for _, t := range s.Tasks {
		c.Tasks = append(c.Tasks, ConnectorTask{ID: t.ID, State: t.State, Worker: t.WorkerID, Trace: t.Trace})
	}
	slices.SortFunc(c.Tasks, func(a, b ConnectorTask) int { return a.ID - b.ID })
}

func (c *connect) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	err := c.do(ctx, http.MethodGet, "/", nil, &out)
	return out.Version, err
}

func (c *connect) Connectors(ctx context.Context) ([]Connector, error) {
	var all map[string]struct {
		Info struct {
			Config map[string]string `json:"config"`
			Type   string            `json:"type"`
		} `json:"info"`
		Status connectStatus `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, "/connectors?expand=status&expand=info", nil, &all); err != nil {
		return nil, err
	}
	out := make([]Connector, 0, len(all))
	for _, name := range slices.Sorted(maps.Keys(all)) {
		e := all[name]
		cn := Connector{Name: name, Type: e.Info.Type, Class: e.Info.Config["connector.class"], Config: e.Info.Config}
		e.Status.fill(&cn)
		out = append(out, cn)
	}
	return out, nil
}

func (c *connect) Connector(ctx context.Context, name string) (Connector, error) {
	cn := Connector{Name: name}
	var st connectStatus
	if err := c.do(ctx, http.MethodGet, connectorPath(name, "status"), nil, &st); err != nil {
		return cn, err
	}
	if err := c.do(ctx, http.MethodGet, connectorPath(name, "config"), nil, &cn.Config); err != nil {
		return cn, err
	}
	st.fill(&cn)
	cn.Class = cn.Config["connector.class"]
	return cn, nil
}

func (c *connect) Validate(ctx context.Context, class string, cfg map[string]string) ([]ConfigProblem, error) {
	var out struct {
		ErrorCount int `json:"error_count"`
		Configs    []struct {
			Value struct {
				Name   string   `json:"name"`
				Errors []string `json:"errors"`
			} `json:"value"`
		} `json:"configs"`
	}
	path := "/connector-plugins/" + url.PathEscape(class) + "/config/validate"
	if err := c.do(ctx, http.MethodPut, path, cfg, &out); err != nil {
		return nil, err
	}
	var probs []ConfigProblem
	for _, v := range out.Configs {
		if len(v.Value.Errors) > 0 {
			probs = append(probs, ConfigProblem{Name: v.Value.Name, Errors: v.Value.Errors})
		}
	}
	return probs, nil
}

func (c *connect) Pause(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPut, connectorPath(name, "pause"), nil, nil)
}

func (c *connect) Resume(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPut, connectorPath(name, "resume"), nil, nil)
}

func (c *connect) Restart(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, connectorPath(name, "restart")+"?includeTasks=true&onlyFailed=true", nil, nil)
}

func (c *connect) RestartTask(ctx context.Context, name string, task int) error {
	return c.do(ctx, http.MethodPost, connectorPath(name, "tasks", strconv.Itoa(task), "restart"), nil, nil)
}

func (c *connect) SetConfig(ctx context.Context, name string, old, cfg map[string]string) error {
	var now map[string]string
	if err := c.do(ctx, http.MethodGet, connectorPath(name, "config"), nil, &now); err != nil {
		return err
	}
	if !maps.Equal(now, old) {
		return ErrConnectorChanged
	}
	return c.do(ctx, http.MethodPut, connectorPath(name, "config"), cfg, nil)
}
