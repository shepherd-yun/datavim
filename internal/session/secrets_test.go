package session

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/shepherd-yun/sqlvim/internal/config"
	"github.com/shepherd-yun/sqlvim/internal/kafka"
)

func oauthConn() config.Connection {
	return config.Connection{Params: map[string]string{"sasl": "oauth", "oauth_token_url": "https://idp/token", "oauth_client_id": "cid", "oauth_scope": "kafka"}}
}

func TestOAuthSecretAsked(t *testing.T) {
	m, op, sec := setupKafka(oauthConn())
	_, err := m.Kafka(context.Background(), "k")
	var se *SecretRequiredError
	if !errors.As(err, &se) || se.Conn != "k" || se.Kind != SecretOAuth || se.Wrong {
		t.Fatalf("err = %v, want SecretRequiredError{k, oauth}", err)
	}
	if len(op.cfgs) != 0 {
		t.Fatal("opened without the secret")
	}
	if err := m.SetSecret("k", SecretOAuth, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Kafka(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	o := op.cfgs[0].OAuth
	if o == nil || o.ClientSecret != "s" || o.TokenURL != "https://idp/token" || o.ClientID != "cid" || o.Scope != "kafka" {
		t.Fatalf("OAuth = %+v", o)
	}
	if sec.m["k#oauth"] != "s" {
		t.Fatalf("keychain = %v", sec.m)
	}
}

func TestOAuthRefusedAsksAgain(t *testing.T) {
	m, op, sec := setupKafka(oauthConn())
	sec.m["k#oauth"] = "old"
	op.err = kafka.ErrOAuthRefused
	_, err := m.Kafka(context.Background(), "k")
	var se *SecretRequiredError
	if !errors.As(err, &se) || se.Kind != SecretOAuth || !se.Wrong {
		t.Fatalf("err = %v, want a wrong oauth secret", err)
	}
	op.err = nil
	delete(sec.m, "k#oauth")
	// the refused secret is not remembered for this run either
	if _, err := m.Kafka(context.Background(), "k"); !errors.As(err, &se) {
		t.Fatalf("err = %v, want the secret asked for again", err)
	}
}

func TestSecretRequiredErrorText(t *testing.T) {
	if got := (&SecretRequiredError{Kind: SecretSchemaRegistry}).Error(); got != "schema registry password required" {
		t.Errorf("schema registry: %q", got)
	}
	if got := (&SecretRequiredError{Kind: SecretOAuth}).Error(); got != "oauth client secret required" {
		t.Errorf("oauth: %q", got)
	}
}

func TestRemoveConnectionDeletesSecrets(t *testing.T) {
	m, _, sec := setupKafka(config.Connection{})
	sec.m["k"], sec.m["k#oauth"], sec.m["k#schema-registry"] = "a", "b", "c"
	if err := m.RemoveConnection("k"); err != nil {
		t.Fatal(err)
	}
	if len(sec.m) != 0 {
		t.Fatalf("left in the keychain: %v", sec.m)
	}
}

func TestRenameMovesSecrets(t *testing.T) {
	m, _, sec := setupKafka(config.Connection{})
	sec.m["k"], sec.m["k#oauth"], sec.m["k#schema-registry"] = "a", "b", "c"
	c, _ := m.Connection("k")
	c.Name = "k2"
	if err := m.UpdateConnection("k", c, "", false); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"k2": "a", "k2#oauth": "b", "k2#schema-registry": "c"}
	if !maps.Equal(sec.m, want) {
		t.Fatalf("keychain = %v, want %v", sec.m, want)
	}
}

func TestKafkaConfigK2(t *testing.T) {
	m, op, _ := setupKafka(config.Connection{Params: map[string]string{
		"sasl": "aws-msk-iam", "aws_region": "eu-west-1", "aws_profile": "prod",
		"broker_map": "b1:9092=127.0.0.1:19092",
	}})
	if _, err := m.Kafka(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	kc := op.cfgs[0]
	if kc.AWS == nil || *kc.AWS != (kafka.AWSConfig{Region: "eu-west-1", Profile: "prod"}) {
		t.Fatalf("AWS = %+v", kc.AWS)
	}
	if kc.BrokerMap["b1:9092"] != "127.0.0.1:19092" {
		t.Fatalf("BrokerMap = %v", kc.BrokerMap)
	}
	if !kc.TLS {
		t.Fatal("aws-msk-iam should default to TLS")
	}
}
