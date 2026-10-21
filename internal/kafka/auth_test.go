package kafka

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/sasl/aws"
)

func countingDial(n *atomic.Int32) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		n.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
}

func TestOAuthTokenSource(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		calls++
		n := calls
		bodies = append(bodies, r.PostForm.Encode())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = w.Write([]byte(`{"access_token":"t1","token_type":"bearer","expires_in":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"t2","token_type":"bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	var dials atomic.Int32
	cfg := Config{OAuth: &OAuthConfig{TokenURL: srv.URL, ClientID: "cid", ClientSecret: "csecret", Scope: "kafka"}}
	auth := oauthAuth(cfg, countingDial(&dials))
	a, err := auth(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if a.Token != "t1" {
		t.Fatalf("first token = %q", a.Token)
	}
	// oauth2 treats a token as expired some seconds early, so t1 (1s) is
	// stale at once; wait past it anyway.
	time.Sleep(1100 * time.Millisecond)
	a, err = auth(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if a.Token != "t2" {
		t.Fatalf("second token = %q", a.Token)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(bodies[0], "grant_type=client_credentials") || !strings.Contains(bodies[0], "scope=kafka") {
		t.Fatalf("token request = %q", bodies[0])
	}
	if dials.Load() < 1 {
		t.Fatal("the token request did not go through Config.Dial")
	}
}

func TestOAuthRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	defer srv.Close()
	var dials atomic.Int32
	cfg := Config{OAuth: &OAuthConfig{TokenURL: srv.URL, ClientID: "cid", ClientSecret: "csecret-xyz"}}
	_, err := oauthAuth(cfg, countingDial(&dials))(ctx(t))
	if !errors.Is(err, ErrOAuthRefused) {
		t.Fatalf("err = %v, want ErrOAuthRefused", err)
	}
	if strings.Contains(err.Error(), "csecret-xyz") {
		t.Fatalf("the client secret is in the error: %v", err)
	}
}

// TestOAuthErrorRedactsURL: a token URL's userinfo stays out of a network error.
func TestOAuthErrorRedactsURL(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()
	var dials atomic.Int32
	cfg := Config{OAuth: &OAuthConfig{TokenURL: "http://u:url-pw-123@" + dead + "/token", ClientID: "cid", ClientSecret: "s"}}
	_, err = oauthAuth(cfg, countingDial(&dials))(ctx(t))
	if err == nil || strings.Contains(err.Error(), "url-pw-123") {
		t.Fatalf("err = %v", err)
	}
}

func TestAWSAuthFromChain(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AK")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "SK")
	t.Setenv("AWS_SESSION_TOKEN", "ST")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/none")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/none")
	fn, err := awsAuth(ctx(t), AWSConfig{Region: "eu-west-1"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := fn(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	want := aws.Auth{AccessKey: "AK", SecretKey: "SK", SessionToken: "ST", UserAgent: "sqlvim"}
	if a != want {
		t.Fatalf("auth = %+v", a)
	}
}
