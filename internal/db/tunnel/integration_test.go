//go:build integration

package tunnel

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredpanda "github.com/testcontainers/testcontainers-go/modules/redpanda"

	"github.com/shepherd-yun/datavim/internal/db"
	"github.com/shepherd-yun/datavim/internal/db/mysql"
	"github.com/shepherd-yun/datavim/internal/db/postgres"
	"github.com/shepherd-yun/datavim/internal/kafka"
)

// tunnelDial starts an SSH server and returns a dial function through it.
func tunnelDial(t *testing.T) db.DialFunc {
	t.Helper()
	_, h, _ := direct(t)
	dial, err := h.pool("").Connect(context.Background(), "bastion")
	if err != nil {
		t.Fatal(err)
	}
	return dial
}

func hostPort(t *testing.T, endpoint string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}

// cancelThroughTunnel cancels a long statement on a session and checks that
// the cancel reached the server (it returns quickly) and that the session,
// with its temporary table, survived.
func cancelThroughTunnel(t *testing.T, conn db.Conn, sleepSQL string) {
	t.Helper()
	ctx := context.Background()
	s, err := conn.Session(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.Exec(ctx, "CREATE TEMPORARY TABLE keep_me (id int)"); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	start := time.Now()
	_, err = s.Query(cctx, sleepSQL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Fatalf("cancel took %s: it did not reach the server through the tunnel", took)
	}
	if _, err := s.Query(ctx, "SELECT * FROM keep_me"); err != nil {
		t.Fatalf("the session survives the cancel: %v", err)
	}
}

func TestPostgresThroughTunnel(t *testing.T) {
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("app"),
		tcpostgres.WithUsername("datavim"),
		tcpostgres.WithPassword("secret"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "5432/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	host, port := hostPort(t, endpoint)
	conn, err := postgres.Driver{}.Open(ctx, db.ConnConfig{
		Host: host, Port: port, User: "datavim", Password: "secret", Database: "app",
		Params: map[string]string{"sslmode": "disable"}, Dial: tunnelDial(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	rs, err := conn.Query(ctx, "SELECT 1 AS one")
	if err != nil || len(rs.Rows) != 1 || rs.Rows[0][0].Text != "1" {
		t.Fatalf("rs = %+v, err = %v", rs, err)
	}
	cancelThroughTunnel(t, conn, "SELECT pg_sleep(30)")
}

func TestMySQLThroughTunnel(t *testing.T) {
	ctx := context.Background()
	ctr, err := tcmysql.Run(ctx, "mysql:8.4",
		tcmysql.WithDatabase("app"),
		tcmysql.WithUsername("root"),
		tcmysql.WithPassword("secret"),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := ctr.PortEndpoint(ctx, "3306/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	host, port := hostPort(t, endpoint)
	conn, err := mysql.Driver{}.Open(ctx, db.ConnConfig{
		Host: host, Port: port, User: "root", Password: "secret", Database: "app", Dial: tunnelDial(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	rs, err := conn.Query(ctx, "SELECT 1 AS one")
	if err != nil || len(rs.Rows) != 1 || rs.Rows[0][0].Text != "1" {
		t.Fatalf("rs = %+v, err = %v", rs, err)
	}
	cancelThroughTunnel(t, conn, "SELECT SLEEP(30)") // KILL QUERY also goes through the tunnel
}

// TestKafkaThroughTunnel opens a Kafka client and a Reader through the
// tunnel: the bootstrap and the advertised broker address both go through
// the SSH server.
func TestKafkaThroughTunnel(t *testing.T) {
	ctx := context.Background()
	ctr, err := tcredpanda.Run(ctx, "redpandadata/redpanda:v26.2.1")
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := ctr.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s, h, _ := direct(t)
	dial, err := h.pool("").Connect(ctx, "bastion")
	if err != nil {
		t.Fatal(err)
	}
	cfg := kafka.Config{Brokers: []string{broker}, Dial: dial}
	cl, err := kafka.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if err := cl.CreateTopic(ctx, kafka.NewTopic{Name: "orders", Partitions: 1, Replication: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Produce(ctx, "orders", []kafka.ProduceRecord{{Value: []byte("hello"), Partition: 0}}); err != nil {
		t.Fatal(err)
	}
	r, err := kafka.OpenReader(ctx, cfg, kafka.ReaderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	recs, err := r.Range(ctx, "orders", map[int32]kafka.Range{0: {From: 0, To: 1}})
	if err != nil || len(recs) != 1 || string(recs[0].Value) != "hello" {
		t.Fatalf("recs = %+v, %v", recs, err)
	}
	if !slices.Contains(s.dialed(), broker) {
		t.Fatalf("the SSH server dialed %v, not %s", s.dialed(), broker)
	}
}
