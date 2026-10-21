package kafka

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/sr"
	"github.com/twmb/franz-go/pkg/sr/srfake"
)

const orderAvro = `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"}]}`

func openRegistry(t *testing.T, reg *srfake.Registry, cfg RegistryConfig) Registry {
	t.Helper()
	if cfg.URL == "" {
		cfg.URL = reg.URL()
	}
	r, err := OpenRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegistryReadWrite(t *testing.T) {
	reg := srfake.New(srfake.WithGlobalCompat(sr.CompatBackward))
	defer reg.Close()
	reg.SeedSchema("orders-value", 1, 1, sr.Schema{Schema: orderAvro, Type: sr.TypeAvro})
	r := openRegistry(t, reg, RegistryConfig{})
	subs, err := r.Subjects(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(subs, []Subject{{Name: "orders-value", Latest: 1, Type: "AVRO"}}) {
		t.Fatalf("subjects = %+v", subs)
	}
	v2 := Schema{Type: "AVRO", Text: `{"type":"record","name":"Order","fields":[{"name":"id","type":"long"},{"name":"note","type":["null","string"],"default":null}]}`}
	got, err := r.Register(ctx(t), "orders-value", v2)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || got.ID == 0 || got.Subject != "orders-value" {
		t.Fatalf("registered = %+v", got)
	}
	if vs, _ := r.Versions(ctx(t), "orders-value"); !slices.Equal(vs, []int{1, 2}) {
		t.Fatalf("versions = %v", vs)
	}
	if s, err := r.Schema(ctx(t), "orders-value", -1); err != nil || s.Version != 2 || s.Type != "AVRO" {
		t.Fatalf("latest = %+v, %v", s, err)
	}
	if err := r.DeleteVersion(ctx(t), "orders-value", 1); err != nil {
		t.Fatal(err)
	}
	if vs, _ := r.Versions(ctx(t), "orders-value"); !slices.Equal(vs, []int{2}) {
		t.Fatalf("versions after the delete = %v", vs)
	}
	if lvl, global, err := r.Compatibility(ctx(t), "orders-value"); err != nil || lvl != "BACKWARD" || !global {
		t.Fatalf("compat = %s %v %v", lvl, global, err)
	}
	if err := r.SetCompatibility(ctx(t), "orders-value", "FULL"); err != nil {
		t.Fatal(err)
	}
	if lvl, global, err := r.Compatibility(ctx(t), "orders-value"); err != nil || lvl != "FULL" || global {
		t.Fatalf("compat = %s %v %v", lvl, global, err)
	}
}

func TestRegistryResolve(t *testing.T) {
	reg := srfake.New()
	defer reg.Close()
	reg.SeedSchema("c", 1, 3, sr.Schema{Schema: `{"type":"record","name":"C","fields":[]}`})
	reg.SeedSchema("b", 1, 2, sr.Schema{
		Schema:     `{"type":"record","name":"B","fields":[{"name":"c","type":"C"}]}`,
		References: []sr.SchemaReference{{Name: "C", Subject: "c", Version: 1}},
	})
	reg.SeedSchema("a", 1, 1, sr.Schema{
		Schema:     `{"type":"record","name":"A","fields":[{"name":"b","type":"B"}]}`,
		References: []sr.SchemaReference{{Name: "B", Subject: "b", Version: 1}},
	})
	r := openRegistry(t, reg, RegistryConfig{})
	a, err := r.Schema(ctx(t), "a", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.References) != 1 || a.References[0] != (SchemaRef{Name: "B", Subject: "b", Version: 1}) {
		t.Fatalf("refs = %+v", a.References)
	}
	refs, err := r.Resolve(ctx(t), a)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs["B"].Subject != "b" || refs["C"].Subject != "c" {
		t.Fatalf("resolved = %+v", refs)
	}
}

func TestRegistryDialsThroughTunnel(t *testing.T) {
	reg := srfake.New()
	defer reg.Close()
	var dials atomic.Int32
	r := openRegistry(t, reg, RegistryConfig{Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}})
	if _, err := r.Subjects(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if dials.Load() < 1 {
		t.Fatal("the registry's HTTP requests did not go through Dial")
	}
}

func TestRegistryRedactsCredentials(t *testing.T) {
	reg := srfake.New(srfake.WithAuth("Basic other"))
	defer reg.Close()
	host := strings.TrimPrefix(reg.URL(), "http://")
	r := openRegistry(t, reg, RegistryConfig{URL: "http://u:secret-pw@" + host})
	_, err := r.Subjects(ctx(t))
	if !errors.Is(err, ErrRegistryAuth) {
		t.Fatalf("err = %v, want ErrRegistryAuth", err)
	}
	if strings.Contains(err.Error(), "secret-pw") {
		t.Fatalf("the password is in the error: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()
	r = openRegistry(t, reg, RegistryConfig{URL: "http://u:secret-pw@" + dead})
	_, err = r.Subjects(ctx(t))
	if err == nil || strings.Contains(err.Error(), "secret-pw") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegistryCachesByID(t *testing.T) {
	reg := srfake.New()
	defer reg.Close()
	reg.SeedSchema("orders-value", 3, 12, sr.Schema{Schema: orderAvro})
	var byID atomic.Int32
	reg.Intercept(func(_ http.ResponseWriter, req *http.Request) bool {
		if strings.HasPrefix(req.URL.Path, "/schemas/ids/") {
			byID.Add(1)
		}
		return false
	})
	r := openRegistry(t, reg, RegistryConfig{})
	s, err := r.SchemaByID(ctx(t), 12)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != 12 || s.Subject != "orders-value" || s.Version != 3 || s.Type != "AVRO" {
		t.Fatalf("schema = %+v", s)
	}
	first := byID.Load()
	if _, err := r.SchemaByID(ctx(t), 12); err != nil {
		t.Fatal(err)
	}
	if byID.Load() != first {
		t.Fatalf("SchemaByID asked again: %d requests, then %d", first, byID.Load())
	}
	if _, err := r.SchemaByID(ctx(t), 7); err == nil || err.Error() != "schema 7: not found" {
		t.Fatalf("missing id err = %v", err)
	}
}

// TestRegistrySubjectsOneRequest: subjects with their latest version come
// from one GET /schemas?latestOnly; a registry without it is asked per subject.
func TestRegistrySubjectsOneRequest(t *testing.T) {
	reg := srfake.New()
	defer reg.Close()
	reg.SeedSchema("orders-value", 1, 1, sr.Schema{Schema: orderAvro})
	reg.SeedSchema("orders-value", 2, 2, sr.Schema{Schema: orderAvro})
	reg.SeedSchema("payments-value", 1, 3, sr.Schema{Schema: `{"type":"object"}`, Type: sr.TypeJSON})
	var reqs atomic.Int32
	var noAll atomic.Bool
	reg.Intercept(func(w http.ResponseWriter, req *http.Request) bool {
		reqs.Add(1)
		if noAll.Load() && req.URL.Path == "/schemas" {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return false
	})
	want := []Subject{{Name: "orders-value", Latest: 2, Type: "AVRO"}, {Name: "payments-value", Latest: 1, Type: "JSON"}}
	r := openRegistry(t, reg, RegistryConfig{})
	subs, err := r.Subjects(ctx(t))
	if err != nil || !slices.Equal(subs, want) {
		t.Fatalf("subjects = %+v, %v", subs, err)
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
	noAll.Store(true)
	subs, err = r.Subjects(ctx(t))
	if err != nil || !slices.Equal(subs, want) {
		t.Fatalf("fallback subjects = %+v, %v", subs, err)
	}
}

// TestRegistryClientCert: tls_cert / tls_key are the registry's client
// certificate too, for a registry behind mTLS.
func TestRegistryClientCert(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	srv.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, key := clientCert(t, dir)

	r := openRegistry(t, nil, RegistryConfig{URL: srv.URL, TLSCA: ca})
	if _, err := r.Subjects(ctx(t)); err == nil {
		t.Fatal("the server wants a client certificate")
	}
	r = openRegistry(t, nil, RegistryConfig{URL: srv.URL, TLSCA: ca, TLSCert: cert, TLSKey: key})
	if subs, err := r.Subjects(ctx(t)); err != nil || len(subs) != 0 {
		t.Fatalf("subjects = %+v, %v", subs, err)
	}
}

// clientCert writes a self-signed client certificate and its key to dir.
func clientCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sqlvim"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "client.pem"), filepath.Join(dir, "client.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
