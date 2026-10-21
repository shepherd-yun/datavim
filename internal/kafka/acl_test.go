package kafka

import (
	"errors"
	"slices"
	"testing"

	"github.com/twmb/franz-go/pkg/kerr"
)

func aclOn(principal, typ, name, pattern string) ACL {
	return ACL{Principal: principal, Host: "*", ResourceType: typ, ResourceName: name, Pattern: pattern, Operation: "read", Permission: "allow"}
}

func TestACLsRoundTrip(t *testing.T) {
	cl := open(t, newCluster(t))
	want := []ACL{
		aclOn("User:alice", "group", "billing", "literal"),
		aclOn("User:alice", "topic", "orders", "literal"),
		{Principal: "User:bob", Host: "10.0.0.1", ResourceType: "topic", ResourceName: "pay.", Pattern: "prefixed", Operation: "write", Permission: "deny"},
	}
	if err := cl.CreateACLs(ctx(t), []ACL{want[2], want[0], want[1]}); err != nil {
		t.Fatal(err)
	}
	got, err := cl.ACLs(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ACLs = %+v\nwant %+v", got, want)
	}
}

// TestDeleteACLsExact: a literal and a prefixed ACL on the same name, and
// one of User:*, are three ACLs; deleting one leaves the other two.
func TestDeleteACLsExact(t *testing.T) {
	cl := open(t, newCluster(t))
	lit := aclOn("User:alice", "topic", "orders", "literal")
	pre := aclOn("User:alice", "topic", "orders", "prefixed")
	any := aclOn("User:*", "topic", "orders", "literal")
	if err := cl.CreateACLs(ctx(t), []ACL{lit, pre, wild}); err != nil {
		t.Fatal(err)
	}
	deleted, err := cl.DeleteACLs(ctx(t), []ACL{lit})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(deleted, []ACL{lit}) {
		t.Fatalf("deleted = %+v", deleted)
	}
	left, err := cl.ACLs(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(left, []ACL{wild, pre}) {
		t.Fatalf("left = %+v", left)
	}
}

func TestCreateACLInvalid(t *testing.T) {
	cl := open(t, newCluster(t))
	ok := aclOn("User:alice", "topic", "orders", "literal")
	for _, tc := range []func(*ACL){
		func(a *ACL) { a.Pattern = "match" },
		func(a *ACL) { a.Operation = "fly" },
		func(a *ACL) { a.Principal = "alice" },
		func(a *ACL) { a.Permission = "" },
		func(a *ACL) { a.ResourceType = "any" },
		func(a *ACL) { a.ResourceName = "" },
	} {
		a := ok
		tc(&a)
		if err := cl.CreateACLs(ctx(t), []ACL{ok, a}); err == nil {
			t.Errorf("CreateACLs(%+v) is not an error", a)
		}
	}
	if got, err := cl.ACLs(ctx(t)); err != nil || len(got) != 0 {
		t.Fatalf("an invalid batch created %+v (%v)", got, err)
	}
}

func TestACLErrors(t *testing.T) {
	if err := aclErr(kerr.SecurityDisabled); !errors.Is(err, ErrNoAuthorizer) {
		t.Fatalf("SECURITY_DISABLED = %v", err)
	}
	if err := aclErr(kerr.ClusterAuthorizationFailed); !errors.Is(err, ErrDenied) {
		t.Fatalf("CLUSTER_AUTHORIZATION_FAILED = %v", err)
	}
}

func TestACLString(t *testing.T) {
	if got := aclOn("User:alice", "topic", "orders", "literal").String(); got != "User:alice ALLOW READ topic:orders (literal) from *" {
		t.Fatalf("String = %q", got)
	}
}
