package session

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/shepherd-yun/datavim/internal/config"
	"github.com/shepherd-yun/datavim/internal/kafka"
)

// connectReadMethods are the kafka.Connect methods that never change a
// cluster. Every method must be here or in connectWriteMethods.
var connectReadMethods = []string{"Version", "Connectors", "Connector", "Validate"}

type fakeConnect struct {
	kafka.Connect
	mu      sync.Mutex
	calls   []string
	authErr bool
}

func (c *fakeConnect) record(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, name)
	if c.authErr {
		return kafka.ErrConnectAuth
	}
	return nil
}

func (c *fakeConnect) Connectors(context.Context) ([]kafka.Connector, error) {
	return nil, c.record("Connectors")
}
func (c *fakeConnect) Pause(context.Context, string) error  { return c.record("Pause") }
func (c *fakeConnect) Resume(context.Context, string) error { return c.record("Resume") }
func (c *fakeConnect) Restart(context.Context, string) error {
	return c.record("Restart")
}

func (c *fakeConnect) RestartTask(context.Context, string, int) error {
	return c.record("RestartTask")
}

func (c *fakeConnect) SetConfig(context.Context, string, map[string]string, map[string]string) error {
	return c.record("SetConfig")
}

func callConnectWrite(c kafka.Connect, name string) error {
	ctx := context.Background()
	switch name {
	case "Pause":
		return c.Pause(ctx, "sink")
	case "Resume":
		return c.Resume(ctx, "sink")
	case "Restart":
		return c.Restart(ctx, "sink")
	case "RestartTask":
		return c.RestartTask(ctx, "sink", 2)
	case "SetConfig":
		return c.SetConfig(ctx, "sink", map[string]string{"a": "1", "b": "2"}, map[string]string{"a": "9", "c": "3"})
	}
	panic("no call for " + name)
}

func setupConnect(c config.Connection) (*Manager, *fakeConnect, *[]kafka.ConnectConfig, *fakeSecrets) {
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	if _, ok := c.Params["connect_url"]; !ok {
		c.Params["connect_url"] = "main=http://c1:8083, cdc=http://c2:8083"
	}
	m, _, sec := setupKafka(c)
	fc := &fakeConnect{}
	var cfgs []kafka.ConnectConfig
	m.ConnectOpen = func(cfg kafka.ConnectConfig) (kafka.Connect, error) {
		cfgs = append(cfgs, cfg)
		return fc, nil
	}
	return m, fc, &cfgs, sec
}

func TestConnectMethodsListed(t *testing.T) {
	typ := reflect.TypeFor[kafka.Connect]()
	for i := range typ.NumMethod() {
		name := typ.Method(i).Name
		if !slices.Contains(connectReadMethods, name) && !slices.Contains(connectWriteMethods, name) {
			t.Errorf("kafka.Connect.%s is neither a read nor a write method: add it to connectWriteMethods (and guard it) or to connectReadMethods", name)
		}
	}
}

func TestConnectGuard(t *testing.T) {
	on := true
	m, fc, cfgs, _ := setupConnect(config.Connection{ReadOnly: &on})
	audit := recordAudit(m)
	c, err := m.KafkaConnect(context.Background(), "k", "cdc")
	if err != nil {
		t.Fatal(err)
	}
	if (*cfgs)[0].URL != "http://c2:8083" {
		t.Fatalf("cfg = %+v", (*cfgs)[0])
	}
	for _, name := range connectWriteMethods {
		if err := callConnectWrite(c, name); !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s on a read-only connection: err = %v", name, err)
		}
	}
	if len(fc.calls) != 0 || len(audit.entries()) != 0 {
		t.Fatalf("calls %v, audit %+v", fc.calls, audit.entries())
	}
	if err := m.SetReadOnly("k", false); err != nil {
		t.Fatal(err)
	}
	for _, name := range connectWriteMethods {
		if err := callConnectWrite(c, name); err != nil {
			t.Errorf("%s after :set noreadonly: %v", name, err)
		}
	}
	if !slices.Equal(fc.calls, connectWriteMethods) {
		t.Fatalf("calls = %v", fc.calls)
	}
	audit.checkOnePerWrite(t, connectWriteMethods)
	es := audit.entries()
	if es[0].Object != "cdc/sink" || es[3].Detail != "task 2" || es[4].Detail != "a, b, c" {
		t.Fatalf("audit = %+v", es)
	}
}

func TestConnectSecret(t *testing.T) {
	m, fc, cfgs, sec := setupConnect(config.Connection{Params: map[string]string{"connect_user": "cu"}})
	_, err := m.KafkaConnect(context.Background(), "k", "main")
	var se *SecretRequiredError
	if !errors.As(err, &se) || se.Kind != SecretConnect || se.Wrong {
		t.Fatalf("err = %v", err)
	}
	if err := m.SetSecret("k", SecretConnect, "pw"); err != nil {
		t.Fatal(err)
	}
	c, err := m.KafkaConnect(context.Background(), "k", "main")
	if err != nil {
		t.Fatal(err)
	}
	if got := (*cfgs)[0]; got.User != "cu" || got.Password != "pw" {
		t.Fatalf("cfg = %+v", got)
	}
	fc.authErr = true
	_, err = c.Connectors(context.Background())
	if !errors.As(err, &se) || se.Kind != SecretConnect || !se.Wrong {
		t.Fatalf("refused password: %v", err)
	}
	delete(sec.m, "k#connect")
	if _, err := m.KafkaConnect(context.Background(), "k", "main"); !errors.As(err, &se) {
		t.Fatalf("the refused password is still remembered: %v", err)
	}
}

func TestConnectURLPasswordNotAsked(t *testing.T) {
	m, fc, _, _ := setupConnect(config.Connection{Params: map[string]string{
		"connect_user": "cu", "connect_url": "http://u:pw@c1:8083",
	}})
	c, err := m.KafkaConnect(context.Background(), "k", "connect")
	if err != nil {
		t.Fatal(err)
	}
	fc.authErr = true
	_, err = c.Connectors(context.Background())
	var se *SecretRequiredError
	if errors.As(err, &se) || !errors.Is(err, kafka.ErrConnectAuth) {
		t.Fatalf("err = %v, want ErrConnectAuth without a prompt", err)
	}
}

func TestConnectUnknownCluster(t *testing.T) {
	m, _, _, _ := setupConnect(config.Connection{})
	if _, err := m.KafkaConnect(context.Background(), "k", "nope"); !errors.Is(err, ErrNoConnect) {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectCachedAndTunnel(t *testing.T) {
	m, _, cfgs, _ := setupConnect(config.Connection{SSH: "bastion"})
	m.Tunnels = &fakeTunnels{}
	for range 2 {
		if _, err := m.KafkaConnect(context.Background(), "k", "main"); err != nil {
			t.Fatal(err)
		}
	}
	if len(*cfgs) != 1 || (*cfgs)[0].Dial == nil {
		t.Fatalf("cfgs = %+v", *cfgs)
	}
	m.Reset("k")
	if _, err := m.KafkaConnect(context.Background(), "k", "main"); err != nil {
		t.Fatal(err)
	}
	if len(*cfgs) != 2 {
		t.Fatal("Reset keeps the connect client")
	}
}
