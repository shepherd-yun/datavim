//go:build integration

package kafka

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestIntegrationACLs(t *testing.T) {
	for _, image := range []string{kafka3, kafka4} {
		t.Run(image, func(t *testing.T) {
			aclSuite(t, Config{Brokers: []string{startApache(t, image, withAuthorizer)}})
		})
	}
	t.Run(redpanda, func(t *testing.T) {
		aclSuite(t, Config{Brokers: []string{startRedpanda(t)}})
	})
}

func TestIntegrationNoAuthorizer(t *testing.T) {
	cl, err := Open(ictx(t), Config{Brokers: []string{startApache(t, kafka4)}})
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if _, err := cl.ACLs(ictx(t)); !errors.Is(err, ErrNoAuthorizer) {
		t.Fatalf("ACLs without an authorizer: %v", err)
	}
}

// aclSuite: a literal, a prefixed and a wildcard-principal ACL on the same
// topic are three ACLs, and deleting one leaves the other two.
func aclSuite(t *testing.T, cfg Config) {
	cl, err := Open(ictx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	lit := ACL{Principal: "User:alice", Host: "*", ResourceType: "topic", ResourceName: "orders", Pattern: "literal", Operation: "read", Permission: "allow"}
	pre, wild := lit, lit
	pre.Pattern, wild.Principal = "prefixed", "User:*"
	group := ACL{Principal: "User:alice", Host: "*", ResourceType: "group", ResourceName: "billing", Pattern: "literal", Operation: "read", Permission: "allow"}
	if err := cl.CreateACLs(ictx(t), []ACL{lit, pre, wild, group}); err != nil {
		if unsupported(t, err, "CreateACLs") || errors.Is(err, ErrNoAuthorizer) {
			t.Logf("ACLs: %v", err)
			return
		}
		t.Fatal(err)
	}
	all := waitACLs(t, cl, 4)
	for _, a := range []ACL{lit, pre, wild, group} {
		if !slices.Contains(all, a) {
			t.Fatalf("%s missing from %+v", a, all)
		}
	}
	deleted, err := cl.DeleteACLs(ictx(t), []ACL{lit})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(deleted, []ACL{lit}) {
		t.Fatalf("deleted %+v", deleted)
	}
	left := waitACLs(t, cl, 3)
	if slices.Contains(left, lit) || !slices.Contains(left, pre) || !slices.Contains(left, wild) || !slices.Contains(left, group) {
		t.Fatalf("left %+v", left)
	}
}

// waitACLs reads the ACLs until there are n (KRaft applies them a moment
// after the answer).
func waitACLs(t *testing.T, cl Client, n int) []ACL {
	t.Helper()
	var got []ACL
	for range 50 {
		var err error
		if got, err = cl.ACLs(ictx(t)); err != nil {
			t.Fatal(err)
		}
		if len(got) == n {
			return got
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("want %d ACLs, have %+v", n, got)
	return nil
}
