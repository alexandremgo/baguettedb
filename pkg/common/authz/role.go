// Package authz holds the authorization model the apps share, built on the standard library alone: global roles and
// the immutable Policy granting them permissions, the roles a member holds in a bdb with the bdb and database
// permissions each grants, and the helpers writing an authenticated identity into a context and reading it back.
package authz

import (
	"maps"
	"slices"
)

// Role represents a global user role.
type Role string

// The global roles.
const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Permission represents a fine-grained permission derived from roles.
type Permission string

// The permissions the global roles grant.
const (
	PermReadOwnProfile   Permission = "read:own_profile"
	PermUpdateOwnProfile Permission = "update:own_profile"
	PermManageUsers      Permission = "manage:users"
	PermManageRoles      Permission = "manage:roles"
)

// defaultGrants maps each built-in role to the permissions it grants. It is never modified; an extended policy holds
// a copy.
var defaultGrants = map[Role][]Permission{
	RoleUser: {
		PermReadOwnProfile,
		PermUpdateOwnProfile,
	},
	RoleAdmin: {
		PermReadOwnProfile,
		PermUpdateOwnProfile,
		PermManageUsers,
		PermManageRoles,
	},
}

// Policy maps each recognized global role to the permissions it grants. Its zero value is the built-in policy:
// RoleUser and RoleAdmin with their default permissions.
//
// A Policy is immutable: With returns a new one and leaves its receiver untouched, so a value can be shared by
// concurrent requests without locking.
type Policy struct {
	grants map[Role][]Permission
}

// With returns a policy that also grants perms to role, recognizing role when the receiver does not.
func (p Policy) With(role Role, perms ...Permission) Policy {
	grants := maps.Clone(p.roleGrants())
	grants[role] = slices.Concat(grants[role], perms)
	return Policy{grants: grants}
}

// IsValidRole reports whether the policy recognizes r.
func (p Policy) IsValidRole(r Role) bool {
	_, ok := p.roleGrants()[r]
	return ok
}

// PermissionsForRoles computes the deduplicated set of permissions the policy grants to roles.
func (p Policy) PermissionsForRoles(roles []Role) []Permission {
	grants := p.roleGrants()
	seen := make(map[Permission]struct{})
	var result []Permission

	for _, role := range roles {
		for _, perm := range grants[role] {
			if _, ok := seen[perm]; !ok {
				seen[perm] = struct{}{}
				result = append(result, perm)
			}
		}
	}

	return result
}

// HasPermission reports whether any of roles is granted perm by the policy.
func (p Policy) HasPermission(roles []Role, perm Permission) bool {
	grants := p.roleGrants()
	return slices.ContainsFunc(roles, func(role Role) bool {
		return slices.Contains(grants[role], perm)
	})
}

// roleGrants returns the policy's grants, the built-in ones for the zero value.
func (p Policy) roleGrants() map[Role][]Permission {
	if p.grants == nil {
		return defaultGrants
	}
	return p.grants
}
