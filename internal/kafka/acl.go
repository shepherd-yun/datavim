package kafka

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// ACL is one access control entry. Every field but Principal, Host and
// ResourceName is lower case.
type ACL struct {
	Principal    string // "User:alice"
	Host         string // "*" or an address
	ResourceType string // "topic", "group", "cluster", "transactional_id", "delegation_token", "user"
	ResourceName string
	Pattern      string // "literal" or "prefixed"
	Operation    string // "all", "read", "write", "create", "delete", "alter", "describe", "cluster_action", "describe_configs", "alter_configs", "idempotent_write"
	Permission   string // "allow" or "deny"
}

// String is "User:alice ALLOW READ topic:orders (literal) from *".
func (a ACL) String() string {
	return fmt.Sprintf("%s %s %s %s:%s (%s) from %s", a.Principal, strings.ToUpper(a.Permission),
		strings.ToUpper(a.Operation), a.ResourceType, a.ResourceName, a.Pattern, a.Host)
}

var (
	// ErrNoAuthorizer is a cluster without an authorizer (SECURITY_DISABLED).
	ErrNoAuthorizer = errors.New("this cluster has no authorizer: ACLs are off")
	// ErrACLMismatch is a delete that removed ACLs nobody selected.
	ErrACLMismatch = errors.New("the cluster deleted ACLs that were not selected")
)

var (
	aclResourceTypes = []string{"topic", "group", "cluster", "transactional_id", "delegation_token", "user"}
	aclPatterns      = []string{"literal", "prefixed"}
	aclOperations    = []string{
		"all", "read", "write", "create", "delete", "alter", "describe", "cluster_action",
		"describe_configs", "alter_configs", "idempotent_write",
	}
	aclPermissions = []string{"allow", "deny"}
)

// wireACL is an ACL in the protocol's enums.
type wireACL struct {
	typ  kmsg.ACLResourceType
	pat  kmsg.ACLResourcePatternType
	op   kmsg.ACLOperation
	perm kmsg.ACLPermissionType
}

func oneOf(field, v string, allowed []string) error {
	if !slices.Contains(allowed, v) {
		return fmt.Errorf("acl %s %q: want one of %s", field, v, strings.Join(allowed, ", "))
	}
	return nil
}

// Validate reports a field outside the known values, as CreateACLs would.
func (a ACL) Validate() error {
	_, err := a.toWire()
	return err
}

// toWire checks a and maps it to the protocol's enums.
func (a ACL) toWire() (wireACL, error) {
	var w wireACL
	if p, name, ok := strings.Cut(a.Principal, ":"); !ok || p == "" || name == "" {
		return w, fmt.Errorf("acl principal %q: want type:name, such as User:alice", a.Principal)
	}
	if a.Host == "" {
		return w, errors.New("acl host is empty (* is every host)")
	}
	if a.ResourceName == "" {
		return w, errors.New("acl resource name is empty")
	}
	for _, c := range []struct {
		field, v string
		allowed  []string
	}{
		{"resource type", a.ResourceType, aclResourceTypes},
		{"pattern", a.Pattern, aclPatterns},
		{"operation", a.Operation, aclOperations},
		{"permission", a.Permission, aclPermissions},
	} {
		if err := oneOf(c.field, c.v, c.allowed); err != nil {
			return w, err
		}
	}
	var err error
	if w.typ, err = kmsg.ParseACLResourceType(a.ResourceType); err != nil {
		return w, err
	}
	if w.pat, err = kmsg.ParseACLResourcePatternType(a.Pattern); err != nil {
		return w, err
	}
	if w.op, err = kmsg.ParseACLOperation(a.Operation); err != nil {
		return w, err
	}
	w.perm, err = kmsg.ParseACLPermissionType(a.Permission)
	return w, err
}

func lower(s fmt.Stringer) string { return strings.ToLower(s.String()) }

func fromWire(principal, host string, typ kmsg.ACLResourceType, name string, pat kmsg.ACLResourcePatternType,
	op kmsg.ACLOperation, perm kmsg.ACLPermissionType,
) ACL {
	return ACL{
		Principal: principal, Host: host, ResourceType: lower(typ), ResourceName: name,
		Pattern: lower(pat), Operation: lower(op), Permission: lower(perm),
	}
}

func compareACLs(a, b ACL) int {
	return cmp.Or(cmp.Compare(a.Principal, b.Principal), cmp.Compare(a.ResourceType, b.ResourceType),
		cmp.Compare(a.ResourceName, b.ResourceName), cmp.Compare(a.Operation, b.Operation),
		cmp.Compare(a.Pattern, b.Pattern), cmp.Compare(a.Host, b.Host), cmp.Compare(a.Permission, b.Permission))
}

// aclErr maps an ACL request's error code.
func aclErr(err error) error {
	if errors.Is(err, kerr.SecurityDisabled) {
		return fmt.Errorf("%w: %w", ErrNoAuthorizer, err)
	}
	return wrap(context.Background(), err, "")
}

func (c *client) ACLs(ctx context.Context) ([]ACL, error) {
	req := kmsg.NewPtrDescribeACLsRequest()
	req.ResourceType = kmsg.ACLResourceTypeAny
	req.ResourcePatternType = kmsg.ACLResourcePatternTypeAny
	req.Operation = kmsg.ACLOperationAny
	req.PermissionType = kmsg.ACLPermissionTypeAny
	resp, err := req.RequestWith(ctx, c.kcl)
	if err != nil {
		return nil, wrap(ctx, err, "acls")
	}
	if err := kerr.ErrorForCode(resp.ErrorCode); err != nil {
		return nil, aclErr(err)
	}
	var out []ACL
	for _, r := range resp.Resources {
		for _, a := range r.ACLs {
			out = append(out, fromWire(a.Principal, a.Host, r.ResourceType, r.ResourceName, r.ResourcePatternType, a.Operation, a.PermissionType))
		}
	}
	slices.SortFunc(out, compareACLs)
	return out, nil
}

func (c *client) CreateACLs(ctx context.Context, acls []ACL) error {
	req := kmsg.NewPtrCreateACLsRequest()
	for _, a := range acls {
		w, err := a.toWire()
		if err != nil {
			return err
		}
		cr := kmsg.NewCreateACLsRequestCreation()
		cr.ResourceType, cr.ResourceName, cr.ResourcePatternType = w.typ, a.ResourceName, w.pat
		cr.Principal, cr.Host, cr.Operation, cr.PermissionType = a.Principal, a.Host, w.op, w.perm
		req.Creations = append(req.Creations, cr)
	}
	resp, err := req.RequestWith(ctx, c.kcl)
	if err != nil {
		return wrap(ctx, err, "acls")
	}
	var errs []error
	for i, r := range resp.Results {
		if err := kerr.ErrorForCode(r.ErrorCode); err != nil && i < len(acls) {
			errs = append(errs, fmt.Errorf("%s: %w", acls[i], aclErr(err)))
		}
	}
	return errors.Join(errs...)
}

// DeleteACLs sends one filter per ACL with every field set, so each
// matches only an ACL equal to it.
func (c *client) DeleteACLs(ctx context.Context, acls []ACL) ([]ACL, error) {
	req := kmsg.NewPtrDeleteACLsRequest()
	for _, a := range acls {
		w, err := a.toWire()
		if err != nil {
			return nil, err
		}
		f := kmsg.NewDeleteACLsRequestFilter()
		name, principal, host := a.ResourceName, a.Principal, a.Host
		f.ResourceType, f.ResourceName, f.ResourcePatternType = w.typ, &name, w.pat
		f.Principal, f.Host, f.Operation, f.PermissionType = &principal, &host, w.op, w.perm
		req.Filters = append(req.Filters, f)
	}
	resp, err := req.RequestWith(ctx, c.kcl)
	if err != nil {
		return nil, wrap(ctx, err, "acls")
	}
	var deleted []ACL
	var errs []error
	for i, r := range resp.Results {
		if err := kerr.ErrorForCode(r.ErrorCode); err != nil {
			errs = append(errs, aclErr(err))
			continue
		}
		for _, m := range r.MatchingACLs {
			if err := kerr.ErrorForCode(m.ErrorCode); err != nil {
				errs = append(errs, aclErr(err))
				continue
			}
			d := fromWire(m.Principal, m.Host, m.ResourceType, m.ResourceName, m.ResourcePatternType, m.Operation, m.PermissionType)
			deleted = append(deleted, d)
			if i >= len(acls) || d != acls[i] {
				errs = append(errs, fmt.Errorf("%w: %s", ErrACLMismatch, d))
			}
		}
	}
	slices.SortFunc(deleted, compareACLs)
	return deleted, errors.Join(errs...)
}
