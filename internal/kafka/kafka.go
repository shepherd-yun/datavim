// Package kafka is sqlvim's only link to Kafka clusters, what internal/db
// is for SQL servers and internal/rdb for Redis. The UI never imports
// franz-go. Keys, values and header values are raw bytes: nothing here
// converts them.
//
// No client here ever lets the cluster create a topic, and the Reader that
// browses messages never joins a consumer group.
package kafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"github.com/twmb/franz-go/pkg/sasl/aws"
	"github.com/twmb/franz-go/pkg/sasl/oauth"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/shepherd-yun/sqlvim/internal/db"
)

// Config is what Open and OpenReader need.
type Config struct {
	Brokers        []string // host:port
	SASL           string   // "", "plain", "scram-sha-256", "scram-sha-512", "oauth" or "aws-msk-iam"
	User, Password string
	TLS            bool
	TLSSkipVerify  bool
	// TLSCA, TLSCert and TLSKey are PEM file paths; ~/ is the home directory.
	TLSCA, TLSCert, TLSKey string
	ClientID               string
	// Dial opens every broker connection (an SSH tunnel); nil dials
	// directly. TLS runs on top of the connection it returns.
	Dial db.DialFunc
	// BrokerMap is the address to dial for an advertised host:port.
	BrokerMap map[string]string
	OAuth     *OAuthConfig // SASL "oauth"
	AWS       *AWSConfig   // SASL "aws-msk-iam"
}

var (
	// ErrAuthFailed is a SASL login the cluster refused.
	ErrAuthFailed = errors.New("kafka: authentication failed")
	// ErrUnsupported is a request this cluster does not know.
	ErrUnsupported = errors.New("not supported by this cluster")
	// ErrDenied is any *_AUTHORIZATION_FAILED: the user's ACLs do not
	// allow the request.
	ErrDenied = errors.New("kafka: permission denied")
)

// Client is one cluster connection, safe for concurrent use: metadata,
// admin requests and producing. Every method that changes the cluster
// goes through session's read-only check.
type Client interface {
	Cluster(ctx context.Context) (ClusterInfo, error)
	// Topics lists every topic, sorted by name.
	Topics(ctx context.Context) ([]Topic, error)
	TopicDetail(ctx context.Context, topic string) (TopicDetail, error)
	// TopicConfigs lists a topic's configs by name; a sensitive one has
	// Value "".
	TopicConfigs(ctx context.Context, topic string) ([]ConfigEntry, error)
	// Supports reports whether the cluster has a feature:
	// "consumer_groups" (KIP-848), "offsets_by_time" or "delete_records".
	Supports(feature string) bool

	// Groups lists every group by name; Protocol and Members are left
	// for GroupDetail.
	Groups(ctx context.Context) ([]Group, error)
	GroupDetail(ctx context.Context, group string) (GroupDetail, error)
	// GroupsLag sums each group's lag in one batch; groups without
	// offsets to fetch (share, unknown) are left out.
	GroupsLag(ctx context.Context, groups []string) (map[string]int64, error)
	// TopicGroups lists the groups that commit to topic, with lag on
	// topic only.
	TopicGroups(ctx context.Context, topic string) ([]GroupDetail, error)
	// OffsetsAt is each partition's first offset at or after ts, or its
	// end offset.
	OffsetsAt(ctx context.Context, topic string, ts time.Time) (map[int32]int64, error)

	// Produce sends recs and waits for each acknowledgement; a missing
	// topic is an error and is not created.
	Produce(ctx context.Context, topic string, recs []ProduceRecord) ([]ProduceResult, error)
	CreateTopic(ctx context.Context, t NewTopic) error
	DeleteTopic(ctx context.Context, name string) error
	// ResetOffsets commits only when the group is Empty and nothing moved
	// since the preview (ErrGroupNotEmpty, ErrGroupChanged).
	ResetOffsets(ctx context.Context, group string, commits []OffsetCommit) ([]CommitResult, error)

	Close()
}

type client struct {
	kcl      *kgo.Client
	adm      *kadm.Client
	versions kadm.BrokersApiVersions
	opts     []kgo.Opt

	mu           sync.Mutex
	noIdempotent bool        // the cluster refused an idempotent producer
	plain        *kgo.Client // Produce's client once noIdempotent
}

// Open connects and checks the connection with one ApiVersions round to
// every broker.
func Open(ctx context.Context, cfg Config) (Client, error) {
	opts, err := clientOpts(cfg)
	if err != nil {
		return nil, err
	}
	kcl, err := kgo.NewClient(append(opts, kgo.RecordPartitioner(newPartitioner()))...)
	if err != nil {
		return nil, err
	}
	c := &client{kcl: kcl, adm: kadm.NewClient(kcl), opts: opts}
	vs, err := c.adm.ApiVersions(ctx)
	if err == nil {
		err = firstVersionsErr(vs)
	}
	if err != nil {
		kcl.Close()
		if errors.Is(err, kerr.UnsupportedSaslMechanism) {
			// not a wrong password: asking for it again would never help
			return nil, fmt.Errorf("kafka sasl %q is not enabled on the brokers: %w", cfg.SASL, err)
		}
		return nil, wrap(ctx, err, "")
	}
	c.versions = vs
	return c, nil
}

// firstVersionsErr is the error of a broker's ApiVersions, when none
// answered.
func firstVersionsErr(vs kadm.BrokersApiVersions) error {
	var first error
	for _, v := range vs {
		if v.Err == nil {
			return nil
		}
		if first == nil {
			first = v.Err
		}
	}
	return first
}

// clientOpts are the kgo settings every client here shares: seeds, the
// dialer, SASL and no topic auto-creation (kgo's default, kept).
func clientOpts(cfg Config) ([]kgo.Opt, error) {
	clientID := cfg.ClientID
	if clientID == "" {
		clientID = "sqlvim"
	}
	dial, err := dialer(cfg)
	if err != nil {
		return nil, err
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ClientID(clientID),
		kgo.Dialer(dial),
	}
	switch cfg.SASL {
	case "":
	case "plain":
		opts = append(opts, kgo.SASL(plain.Auth{User: cfg.User, Pass: cfg.Password}.AsMechanism()))
	case "scram-sha-256":
		opts = append(opts, kgo.SASL(scram.Auth{User: cfg.User, Pass: cfg.Password}.AsSha256Mechanism()))
	case "scram-sha-512":
		opts = append(opts, kgo.SASL(scram.Auth{User: cfg.User, Pass: cfg.Password}.AsSha512Mechanism()))
	case "oauth":
		if cfg.OAuth == nil {
			return nil, errors.New("kafka sasl oauth: no token url / client id")
		}
		opts = append(opts, kgo.SASL(oauth.Oauth(oauthAuth(cfg, baseDial(cfg)))))
	case "aws-msk-iam":
		if cfg.AWS == nil {
			return nil, errors.New("kafka sasl aws-msk-iam: no aws settings")
		}
		fn, err := awsAuth(context.Background(), *cfg.AWS)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.SASL(aws.ManagedStreamingIAM(fn)))
	default:
		return nil, fmt.Errorf("kafka sasl %q is not supported (want plain, scram-sha-256, scram-sha-512, oauth or aws-msk-iam)", cfg.SASL)
	}
	return opts, nil
}

// dialer opens broker connections with cfg.Dial (or directly) and runs
// TLS on top when cfg.TLS: kgo cannot combine its own TLS with a custom
// dialer.
func dialer(cfg Config) (func(ctx context.Context, network, host string) (net.Conn, error), error) {
	tunnel := baseDial(cfg)
	base := tunnel
	if len(cfg.BrokerMap) > 0 {
		base = func(ctx context.Context, network, host string) (net.Conn, error) {
			if to, ok := cfg.BrokerMap[host]; ok {
				host = to
			}
			return tunnel(ctx, network, host)
		}
	}
	if !cfg.TLS {
		return base, nil
	}
	tc, err := tlsConfig(cfg)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, network, host string) (net.Conn, error) {
		conn, err := base(ctx, network, host)
		if err != nil {
			return nil, err
		}
		c := tc.Clone()
		if c.ServerName == "" {
			if h, _, err := net.SplitHostPort(host); err == nil {
				c.ServerName = h
			}
		}
		t := tls.Client(conn, c)
		if err := t.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return t, nil
	}, nil
}

// baseDial is cfg.Dial, or a direct dial: every connection, brokers'
// and HTTP ones, goes through it.
func baseDial(cfg Config) db.DialFunc {
	if cfg.Dial != nil {
		return cfg.Dial
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext
}

func tlsConfig(cfg Config) (*tls.Config, error) {
	tc := &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify, MinVersion: tls.VersionTLS12} //nolint:gosec // opt-in via tls_skip_verify
	if cfg.TLSCA != "" {
		pem, err := os.ReadFile(expandHome(cfg.TLSCA))
		if err != nil {
			return nil, fmt.Errorf("kafka tls_ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("kafka tls_ca: no certificates in %s", cfg.TLSCA)
		}
		tc.RootCAs = pool
	}
	if cfg.TLSCert != "" || cfg.TLSKey != "" {
		cert, err := tls.LoadX509KeyPair(expandHome(cfg.TLSCert), expandHome(cfg.TLSKey))
		if err != nil {
			return nil, fmt.Errorf("kafka tls_cert / tls_key: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

func (c *client) Close() {
	c.kcl.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.plain != nil {
		c.plain.Close()
	}
}

// Supports checks the ApiVersions every reachable broker answered.
func (c *client) Supports(feature string) bool {
	var key kmsg.Key
	minVersion := int16(0)
	switch feature {
	case "consumer_groups":
		key = kmsg.ConsumerGroupDescribe
	case "offsets_by_time":
		key, minVersion = kmsg.ListOffsets, 1
	case "delete_records":
		key = kmsg.DeleteRecords
	default:
		return false
	}
	ok := false
	for _, v := range c.versions {
		if v.Err != nil {
			continue
		}
		maxV, exists := v.KeyMaxVersion(int16(key))
		if !exists || maxV < minVersion {
			return false
		}
		ok = true
	}
	return ok
}

// wrap turns franz-go errors into sqlvim's: cancellation is ctx.Err(), a
// refused login ErrAuthFailed, missing ACLs ErrDenied (naming what, e.g.
// "topic orders"), an unknown request ErrUnsupported and network failures
// db.ErrConnLost. Other broker errors stay as they are.
func wrap(ctx context.Context, err error, what string) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case errors.Is(err, kerr.SaslAuthenticationFailed):
		return fmt.Errorf("%w: %w", ErrAuthFailed, err)
	case errors.Is(err, kerr.TopicAuthorizationFailed), errors.Is(err, kerr.GroupAuthorizationFailed),
		errors.Is(err, kerr.ClusterAuthorizationFailed), errors.Is(err, kerr.TransactionalIDAuthorizationFailed),
		errors.Is(err, kerr.DelegationTokenAuthorizationFailed):
		if what != "" {
			return fmt.Errorf("%w (%s): %w", ErrDenied, what, err)
		}
		return fmt.Errorf("%w: %w", ErrDenied, err)
	case errors.Is(err, kerr.UnsupportedVersion), strings.Contains(err.Error(), "broker is too old"):
		return fmt.Errorf("%w: %w", ErrUnsupported, err)
	}
	var ne net.Error
	var ke *kerr.Error
	if !errors.As(err, &ke) && (errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, kgo.ErrClientClosed) || errors.Is(err, net.ErrClosed)) {
		return fmt.Errorf("%w: %w", db.ErrConnLost, err)
	}
	return err
}
