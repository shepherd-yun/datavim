package ui

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"sync"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
	"github.com/shepherd-yun/sqlvim/internal/session"
)

// fakeConnect is one Kafka Connect cluster of the fake backend.
type fakeConnect struct {
	mu          sync.Mutex
	version     string
	connectors  []kafka.Connector
	calls       []string // "Method name …"
	errs        map[string]error
	problems    []kafka.ConfigProblem // what Validate answers
	setOld      map[string]string     // SetConfig's last old and cfg
	setCfg      map[string]string
	ro          bool
	connectorsN int // Connectors calls
}

func (c *fakeConnect) call(s string) { c.calls = append(c.calls, s) }

func (c *fakeConnect) callsOf(prefix string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, s := range c.calls {
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			out = append(out, s)
		}
	}
	return out
}

func cloneConnector(cn kafka.Connector) kafka.Connector {
	cn.Tasks = slices.Clone(cn.Tasks)
	cn.Config = maps.Clone(cn.Config)
	return cn
}

func (c *fakeConnect) Version(context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version, c.errs["Version"]
}

func (c *fakeConnect) Connectors(context.Context) ([]kafka.Connector, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectorsN++
	out := make([]kafka.Connector, len(c.connectors))
	for i, cn := range c.connectors {
		out[i] = cloneConnector(cn)
	}
	return out, c.errs["Connectors"]
}

func (c *fakeConnect) Connector(_ context.Context, name string) (kafka.Connector, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call("Connector " + name)
	if err := c.errs["Connector"]; err != nil {
		return kafka.Connector{}, err
	}
	for _, cn := range c.connectors {
		if cn.Name == name {
			return cloneConnector(cn), nil
		}
	}
	return kafka.Connector{}, kafka.ErrConnectorNotFound
}

func (c *fakeConnect) Validate(_ context.Context, class string, _ map[string]string) ([]kafka.ConfigProblem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call("Validate " + class)
	return c.problems, c.errs["Validate"]
}

// write records a write and sets the connector's state.
func (c *fakeConnect) write(call, name, state string) error {
	if c.ro {
		return session.ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call(call)
	if err := c.errs[call[:len(call)-len(name)-1]]; err != nil {
		return err
	}
	for i := range c.connectors {
		if c.connectors[i].Name == name && state != "" {
			c.connectors[i].State = state
		}
	}
	return nil
}

func (c *fakeConnect) Pause(_ context.Context, name string) error {
	return c.write("Pause "+name, name, "PAUSED")
}

func (c *fakeConnect) Resume(_ context.Context, name string) error {
	return c.write("Resume "+name, name, "RUNNING")
}

func (c *fakeConnect) Restart(_ context.Context, name string) error {
	return c.write("Restart "+name, name, "")
}

func (c *fakeConnect) RestartTask(_ context.Context, name string, task int) error {
	if c.ro {
		return session.ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call("RestartTask " + name + " " + strconv.Itoa(task))
	return c.errs["RestartTask"]
}

func (c *fakeConnect) SetConfig(_ context.Context, name string, old, cfg map[string]string) error {
	if c.ro {
		return session.ErrReadOnly
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.call("SetConfig " + name)
	c.setOld, c.setCfg = maps.Clone(old), maps.Clone(cfg)
	if err := c.errs["SetConfig"]; err != nil {
		return err
	}
	for i := range c.connectors {
		if c.connectors[i].Name == name {
			c.connectors[i].Config = maps.Clone(cfg)
		}
	}
	return nil
}

// addConnect gives the kafka connection conn the Connect cluster name.
func (f *fakeBackend) addConnect(name string) *fakeConnect {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.connects == nil {
		f.connects = map[string]*fakeConnect{}
	}
	c := &fakeConnect{version: "3.7.0", errs: map[string]error{}}
	f.connects[name] = c
	return c
}

func (f *fakeBackend) KafkaConnect(_ context.Context, conn, cluster string) (kafka.Connect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cfg, err := f.kafkaConn(conn)
	if err != nil {
		return nil, err
	}
	cls, err := config.KafkaConnectClusters(cfg)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(cls, func(cl config.ConnectCluster) bool { return cl.Name == cluster }) {
		return nil, session.ErrNoConnect
	}
	if err := f.connectErr[cluster]; err != nil {
		return nil, err
	}
	c := f.connects[cluster]
	if c == nil {
		return nil, session.ErrNoConnect
	}
	c.ro = cfg.IsReadOnly()
	return c, nil
}
