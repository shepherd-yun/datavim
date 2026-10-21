package es

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
)

// SecurityAPI is which user and role API a cluster has.
type SecurityAPI int

const (
	SecurityNone       SecurityAPI = iota
	SecurityES                     // _security
	SecurityOpenSearch             // _plugins/_security
)

const osSecurity = "_plugins/_security/api/"

// Security finds out whether the cluster has its security features on.
func Security(ctx context.Context, d Doer, info Info) (SecurityAPI, error) {
	if info.OpenSearch() {
		_, err := do(ctx, d, Request{Method: "GET", Path: osSecurity + "account"})
		switch {
		case err == nil, errors.Is(err, ErrAuthFailed), errors.Is(err, ErrForbidden):
			return SecurityOpenSearch, nil
		case missingAPI(err):
			return SecurityNone, nil
		}
		return SecurityNone, err
	}
	resp, err := do(ctx, d, Request{Method: "GET", Path: "_xpack?categories=features"})
	if missingAPI(err) {
		return SecurityNone, nil
	}
	if err != nil {
		return SecurityNone, err
	}
	var r struct {
		Features struct {
			Security struct {
				Enabled bool `json:"enabled"`
			} `json:"security"`
		} `json:"features"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return SecurityNone, err
	}
	if r.Features.Security.Enabled {
		return SecurityES, nil
	}
	return SecurityNone, nil
}

// User is a native (ES) or internal (OpenSearch) user.
type User struct {
	Name         string
	Roles        []string // ES roles; OpenSearch opendistro_security_roles
	BackendRoles []string // OpenSearch only
	FullName     string   // ES only
	Email        string   // ES only
	Enabled      bool     // always true on OpenSearch
	Reserved     bool
	Metadata     json.RawMessage // ES; sent back as it is on update
}

// UserPath is the REST path of a user.
func UserPath(api SecurityAPI, name string) string {
	if api == SecurityOpenSearch {
		return osSecurity + "internalusers/" + url.PathEscape(name)
	}
	return "_security/user/" + url.PathEscape(name)
}

// RolePath is the REST path of a role.
func RolePath(api SecurityAPI, name string) string {
	if api == SecurityOpenSearch {
		return osSecurity + "roles/" + url.PathEscape(name)
	}
	return "_security/role/" + url.PathEscape(name)
}

// Users lists the users by name; OpenSearch's hidden ones are left out.
func Users(ctx context.Context, d Doer, api SecurityAPI) ([]User, error) {
	path := "_security/user"
	if api == SecurityOpenSearch {
		path = osSecurity + "internalusers"
	}
	resp, err := do(ctx, d, Request{Method: "GET", Path: path})
	if err != nil {
		return nil, err
	}
	var r map[string]struct {
		Roles        []string        `json:"roles"`
		FullName     *string         `json:"full_name"`
		Email        *string         `json:"email"`
		Enabled      bool            `json:"enabled"`
		Metadata     json.RawMessage `json:"metadata"`
		OSRoles      []string        `json:"opendistro_security_roles"`
		BackendRoles []string        `json:"backend_roles"`
		Reserved     bool            `json:"reserved"`
		Hidden       bool            `json:"hidden"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := []User{}
	for name, v := range r {
		u := User{Name: name}
		if api == SecurityOpenSearch {
			if v.Hidden {
				continue
			}
			u.Roles, u.BackendRoles, u.Enabled, u.Reserved = v.OSRoles, v.BackendRoles, true, v.Reserved
		} else {
			u.Roles, u.Enabled, u.Metadata, u.Reserved = v.Roles, v.Enabled, v.Metadata, reserved(v.Metadata)
			if v.FullName != nil {
				u.FullName = *v.FullName
			}
			if v.Email != nil {
				u.Email = *v.Email
			}
		}
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b User) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func reserved(metadata json.RawMessage) bool {
	var m struct {
		Reserved bool `json:"_reserved"`
	}
	return json.Unmarshal(metadata, &m) == nil && m.Reserved
}

func orEmpty(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func put(ctx context.Context, d Doer, method, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = do(ctx, d, Request{Method: method, Path: path, Body: b})
	return err
}

type patchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// CreateUser creates a user with a password.
func CreateUser(ctx context.Context, d Doer, api SecurityAPI, u User, password string) error {
	if api == SecurityOpenSearch {
		return put(ctx, d, "PUT", UserPath(api, u.Name), map[string]any{
			"password": password, "opendistro_security_roles": orEmpty(u.Roles), "backend_roles": orEmpty(u.BackendRoles),
		})
	}
	return put(ctx, d, "PUT", UserPath(api, u.Name), map[string]any{
		"password": password, "roles": orEmpty(u.Roles), "full_name": u.FullName, "email": u.Email,
	})
}

// UpdateUser changes a user's roles (and on ES its name and email),
// keeping the password.
func UpdateUser(ctx context.Context, d Doer, api SecurityAPI, u User) error {
	if api == SecurityOpenSearch {
		return put(ctx, d, "PATCH", UserPath(api, u.Name), []patchOp{
			{"add", "/opendistro_security_roles", orEmpty(u.Roles)},
			{"add", "/backend_roles", orEmpty(u.BackendRoles)},
		})
	}
	body := map[string]any{"roles": orEmpty(u.Roles), "full_name": u.FullName, "email": u.Email}
	if len(u.Metadata) > 0 {
		body["metadata"] = u.Metadata
	}
	return put(ctx, d, "PUT", UserPath(api, u.Name), body)
}

// SetUserEnabled enables or disables an ES user; OpenSearch has no such
// thing.
func SetUserEnabled(ctx context.Context, d Doer, api SecurityAPI, name string, enabled bool) error {
	if api == SecurityOpenSearch {
		return ErrUnsupported
	}
	action := "/_disable"
	if enabled {
		action = "/_enable"
	}
	_, err := do(ctx, d, Request{Method: "PUT", Path: UserPath(api, name) + action})
	return err
}

// ChangePassword sets a user's password.
func ChangePassword(ctx context.Context, d Doer, api SecurityAPI, name, password string) error {
	if api == SecurityOpenSearch {
		return put(ctx, d, "PATCH", UserPath(api, name), []patchOp{{"add", "/password", password}})
	}
	return put(ctx, d, "POST", UserPath(api, name)+"/_password", map[string]string{"password": password})
}

// DeleteUser deletes a user.
func DeleteUser(ctx context.Context, d Doer, api SecurityAPI, name string) error {
	_, err := do(ctx, d, Request{Method: "DELETE", Path: UserPath(api, name)})
	return err
}

// Role is a security role.
type Role struct {
	Name     string
	Cluster  []string // ES cluster; OpenSearch cluster_permissions
	Indices  int      // index privilege entries
	Reserved bool
	// Body is what PUT takes back: without the fields the cluster sets.
	Body json.RawMessage
}

// Roles lists the roles by name; OpenSearch's hidden ones are left out.
func Roles(ctx context.Context, d Doer, api SecurityAPI) ([]Role, error) {
	path := "_security/role"
	if api == SecurityOpenSearch {
		path = osSecurity + "roles"
	}
	resp, err := do(ctx, d, Request{Method: "GET", Path: path})
	if err != nil {
		return nil, err
	}
	var r map[string]map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, err
	}
	out := []Role{}
	for name, body := range r {
		role := Role{Name: name}
		var list []json.RawMessage
		if api == SecurityOpenSearch {
			var hidden bool
			_ = json.Unmarshal(body["hidden"], &hidden)
			if hidden {
				continue
			}
			_ = json.Unmarshal(body["reserved"], &role.Reserved)
			_ = json.Unmarshal(body["cluster_permissions"], &role.Cluster)
			_ = json.Unmarshal(body["index_permissions"], &list)
			delete(body, "reserved")
			delete(body, "hidden")
			delete(body, "static")
		} else {
			role.Reserved = reserved(body["metadata"])
			_ = json.Unmarshal(body["cluster"], &role.Cluster)
			_ = json.Unmarshal(body["indices"], &list)
			delete(body, "transient_metadata")
			var meta map[string]json.RawMessage
			if json.Unmarshal(body["metadata"], &meta) == nil {
				delete(meta, "_reserved")
				body["metadata"], _ = json.Marshal(meta)
			}
		}
		role.Indices = len(list)
		role.Body, _ = json.Marshal(body)
		out = append(out, role)
	}
	slices.SortFunc(out, func(a, b Role) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// PutRole creates or replaces a role.
func PutRole(ctx context.Context, d Doer, api SecurityAPI, name string, body json.RawMessage) error {
	_, err := do(ctx, d, Request{Method: "PUT", Path: RolePath(api, name), Body: body})
	return err
}

// DeleteRole deletes a role.
func DeleteRole(ctx context.Context, d Doer, api SecurityAPI, name string) error {
	_, err := do(ctx, d, Request{Method: "DELETE", Path: RolePath(api, name)})
	return err
}
