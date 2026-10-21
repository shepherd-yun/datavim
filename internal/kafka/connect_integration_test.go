//go:build integration

package kafka

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// connectProps run a one-worker distributed Connect cluster on the
// network's Kafka with the file connectors that ship with apache/kafka.
const connectProps = `bootstrap.servers=kafka:19092
group.id=sqlvim-connect
key.converter=org.apache.kafka.connect.storage.StringConverter
value.converter=org.apache.kafka.connect.storage.StringConverter
offset.storage.topic=connect-offsets
config.storage.topic=connect-configs
status.storage.topic=connect-status
offset.storage.replication.factor=1
config.storage.replication.factor=1
status.storage.replication.factor=1
listeners=http://0.0.0.0:8083
`

// startConnect starts Kafka and a Connect worker on one network and
// returns the worker's REST URL.
func startConnect(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	nw, err := network.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	testcontainers.CleanupNetwork(t, nw)
	startApache(t, kafka4, apacheOpts{nw: nw})
	ctr, err := testcontainers.Run(ctx, kafka4,
		network.WithNetwork([]string{"connect"}, nw),
		testcontainers.WithExposedPorts("8083/tcp"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(connectProps), ContainerFilePath: "/tmp/connect.properties", FileMode: 0o644,
		}),
		testcontainers.WithEntrypoint("sh"),
		// the copied file is root's: the worker (appuser) writes its own
		testcontainers.WithCmd("-c", `{ cat /tmp/connect.properties; echo "plugin.path=$(ls /opt/kafka/libs/connect-file-*.jar)"; } > /tmp/worker.properties && `+
			`exec /opt/kafka/bin/connect-distributed.sh /tmp/worker.properties`),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/connectors").WithPort("8083/tcp").WithStartupTimeout(3*time.Minute)),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "8083/tcp", "http")
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

// createConnector adds a connector with the REST API (sqlvim does not).
func createConnector(t *testing.T, url, name string, cfg map[string]string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "config": cfg})
	resp, err := http.Post(url+"/connectors", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create connector: %s", resp.Status)
	}
}

// waitState waits until name is in state (the worker gets there async).
func waitState(t *testing.T, c Connect, name, state string) Connector {
	t.Helper()
	var cn Connector
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		var err error
		if cn, err = c.Connector(ictx(t), name); err == nil && cn.State == state && len(cn.Tasks) > 0 {
			return cn
		}
	}
	t.Fatalf("%s: state %q, want %s (%+v)", name, cn.State, state, cn)
	return cn
}

func TestIntegrationConnect(t *testing.T) {
	url := startConnect(t)
	createConnector(t, url, "orders-sink", map[string]string{
		"connector.class": "org.apache.kafka.connect.file.FileStreamSinkConnector",
		"tasks.max":       "1", "topics": "orders", "file": "/tmp/orders.txt",
	})
	c, err := OpenConnect(ConnectConfig{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := c.Version(ictx(t)); err != nil || v == "" {
		t.Fatalf("Version = %q, %v", v, err)
	}
	cn := waitState(t, c, "orders-sink", "RUNNING")
	if cn.Type != "sink" || !strings.HasSuffix(cn.Class, "FileStreamSinkConnector") {
		t.Fatalf("connector = %+v", cn)
	}
	all, err := c.Connectors(ictx(t))
	if err != nil || len(all) != 1 || all[0].Name != "orders-sink" || all[0].State != "RUNNING" {
		t.Fatalf("Connectors = %+v, %v", all, err)
	}
	if err := c.Pause(ictx(t), "orders-sink"); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "orders-sink", "PAUSED")
	if err := c.Resume(ictx(t), "orders-sink"); err != nil {
		t.Fatal(err)
	}
	waitState(t, c, "orders-sink", "RUNNING")
	if err := c.Restart(ictx(t), "orders-sink"); err != nil {
		t.Fatal(err)
	}
	if err := c.RestartTask(ictx(t), "orders-sink", 0); err != nil {
		t.Fatal(err)
	}
	bad := maps.Clone(cn.Config)
	bad["tasks.max"] = "abc"
	probs, err := c.Validate(ictx(t), cn.Class, bad)
	if err != nil || len(probs) == 0 || probs[0].Name != "tasks.max" {
		t.Fatalf("Validate = %+v, %v", probs, err)
	}
	old := cn.Config
	cfg := maps.Clone(old)
	cfg["tasks.max"] = "2"
	if err := c.SetConfig(ictx(t), "orders-sink", old, cfg); err != nil {
		t.Fatal(err)
	}
	// the worker rebalances after a config change: reads fail for a moment
	var got Connector
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if got, err = c.Connector(ictx(t), "orders-sink"); err == nil && got.Config["tasks.max"] == "2" {
			break
		}
	}
	if err != nil || got.Config["tasks.max"] != "2" {
		t.Fatalf("after SetConfig: %+v, %v", got.Config, err)
	}
	cfg["tasks.max"] = "3"
	if err := c.SetConfig(ictx(t), "orders-sink", old, cfg); !errors.Is(err, ErrConnectorChanged) {
		t.Fatalf("SetConfig with a stale old: %v", err)
	}
	if _, err := c.Connector(ictx(t), "nope"); !errors.Is(err, ErrConnectorNotFound) {
		t.Fatalf("missing connector: %v", err)
	}
}
