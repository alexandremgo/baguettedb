package authz

import (
	"testing"
)

// TestPolicy_IsValidRole checks that the zero policy recognizes the built-in roles only.
func TestPolicy_IsValidRole(t *testing.T) {
	tests := []struct {
		name string
		role Role
		want bool
	}{
		{"user is valid", RoleUser, true},
		{"admin is valid", RoleAdmin, true},
		{"empty is invalid", Role(""), false},
		{"unknown is invalid", Role("superadmin"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Policy{}).IsValidRole(tt.role); got != tt.want {
				t.Errorf("IsValidRole(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

// TestPolicy_PermissionsForRoles checks the permissions the zero policy grants to each built-in role, and that an
// unknown role or no role grants none.
func TestPolicy_PermissionsForRoles(t *testing.T) {
	tests := []struct {
		name     string
		roles    []Role
		wantPerm Permission
		wantLen  int
	}{
		{
			name:     "user gets basic permissions",
			roles:    []Role{RoleUser},
			wantPerm: PermReadOwnProfile,
			wantLen:  2,
		},
		{
			name:     "admin gets all permissions",
			roles:    []Role{RoleAdmin},
			wantPerm: PermManageUsers,
			wantLen:  4,
		},
		{
			name:    "empty roles returns nil",
			roles:   []Role{},
			wantLen: 0,
		},
		{
			name:    "unknown role returns nothing",
			roles:   []Role{Role("unknown")},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perms := (Policy{}).PermissionsForRoles(tt.roles)
			if len(perms) != tt.wantLen {
				t.Errorf("PermissionsForRoles() returned %d permissions, want %d", len(perms), tt.wantLen)
			}
			if tt.wantPerm != "" {
				found := false
				for _, p := range perms {
					if p == tt.wantPerm {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("PermissionsForRoles() missing expected permission %q", tt.wantPerm)
				}
			}
		})
	}
}

// TestPolicy_HasPermission checks the zero policy's answer for each built-in role and permission pair.
func TestPolicy_HasPermission(t *testing.T) {
	tests := []struct {
		name  string
		roles []Role
		perm  Permission
		want  bool
	}{
		{"user can read own profile", []Role{RoleUser}, PermReadOwnProfile, true},
		{"user cannot manage users", []Role{RoleUser}, PermManageUsers, false},
		{"admin can manage users", []Role{RoleAdmin}, PermManageUsers, true},
		{"admin can manage roles", []Role{RoleAdmin}, PermManageRoles, true},
		{"empty roles grants nothing", []Role{}, PermReadOwnProfile, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Policy{}).HasPermission(tt.roles, tt.perm); got != tt.want {
				t.Errorf("HasPermission(%v, %q) = %v, want %v", tt.roles, tt.perm, got, tt.want)
			}
		})
	}
}

// TestPolicy_With checks that With recognizes a new role with its permissions, adds permissions to an existing role
// on top of the ones it already grants, and leaves the policy it extends unchanged. The policy it returns holds its
// own copy of the permissions, so a caller reusing the slice it passed does not change it.
func TestPolicy_With(t *testing.T) {
	const roleBeta Role = "beta"
	const permBeta Permission = "access:beta_features"

	base := Policy{}
	extended := base.With(roleBeta, PermReadOwnProfile, permBeta).With(RoleAdmin, permBeta)

	if !extended.IsValidRole(roleBeta) {
		t.Errorf("extended.IsValidRole(%q) = false, want true", roleBeta)
	}
	if !extended.HasPermission([]Role{roleBeta}, permBeta) {
		t.Errorf("extended: beta lacks %q", permBeta)
	}
	admin := []Role{RoleAdmin}
	if !extended.HasPermission(admin, permBeta) || !extended.HasPermission(admin, PermManageUsers) {
		t.Errorf("extended: admin should hold both %q and its built-in %q", permBeta, PermManageUsers)
	}

	if base.IsValidRole(roleBeta) {
		t.Errorf("base.IsValidRole(%q) = true after With, want false", roleBeta)
	}
	if base.HasPermission([]Role{RoleAdmin}, permBeta) {
		t.Errorf("base: admin gained %q after With", permBeta)
	}

	perms := []Permission{permBeta}
	fromSlice := base.With(roleBeta, perms...)
	perms[0] = PermManageUsers
	if fromSlice.HasPermission([]Role{roleBeta}, PermManageUsers) {
		t.Errorf("fromSlice: beta gained %q when the caller reused its slice", PermManageUsers)
	}
}

func TestIsValidBdbRole(t *testing.T) {
	tests := []struct {
		name string
		role BdbRole
		want bool
	}{
		{"owner is valid", BdbRoleOwner, true},
		{"editor is valid", BdbRoleEditor, true},
		{"viewer is valid", BdbRoleViewer, true},
		{"empty is invalid", BdbRole(""), false},
		{"unknown is invalid", BdbRole("superadmin"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidBdbRole(tt.role); got != tt.want {
				t.Errorf("IsValidBdbRole(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

func TestBdbRoleHasPermission(t *testing.T) {
	tests := []struct {
		name string
		role BdbRole
		perm BdbPermission
		want bool
	}{
		{"owner can manage members", BdbRoleOwner, BdbPermManageMembers, true},
		{"owner can edit", BdbRoleOwner, BdbPermEdit, true},
		{"owner can view", BdbRoleOwner, BdbPermView, true},
		{"editor cannot manage members", BdbRoleEditor, BdbPermManageMembers, false},
		{"editor cannot edit", BdbRoleEditor, BdbPermEdit, false},
		{"editor can view", BdbRoleEditor, BdbPermView, true},
		{"viewer cannot manage members", BdbRoleViewer, BdbPermManageMembers, false},
		{"viewer cannot edit", BdbRoleViewer, BdbPermEdit, false},
		{"viewer can view", BdbRoleViewer, BdbPermView, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BdbRoleHasPermission(tt.role, tt.perm); got != tt.want {
				t.Errorf("BdbRoleHasPermission(%q, %q) = %v, want %v", tt.role, tt.perm, got, tt.want)
			}
		})
	}
}
