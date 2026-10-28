package authz

import (
	"context"
	"slices"
	"testing"
)

// TestContextWithIdentityFrom checks that the subject, the granted scopes, and the roles all cross from the
// source context into the destination.
func TestContextWithIdentityFrom(t *testing.T) {
	src := context.WithValue(context.Background(), userIDKey, "user-123")
	src = context.WithValue(src, scopesKey, []string{"openid", "offline_access"})
	src = context.WithValue(src, rolesKey, []Role{RoleUser, RoleAdmin})

	ctx := ContextWithIdentityFrom(context.Background(), src)

	userID, ok := UserIDFromContext(ctx)
	if !ok || userID != "user-123" {
		t.Errorf("UserIDFromContext = (%q, %v), want (\"user-123\", true)", userID, ok)
	}
	if got := ScopesFromContext(ctx); !slices.Equal(got, []string{"openid", "offline_access"}) {
		t.Errorf("ScopesFromContext = %v, want [openid offline_access]", got)
	}
	if got := RolesFromContext(ctx); !slices.Equal(got, []Role{RoleUser, RoleAdmin}) {
		t.Errorf("RolesFromContext = %v, want [user admin]", got)
	}
}

// TestContextWithIdentityFrom_CopiesOnlyWhatSrcCarries checks that each value is copied only when the source
// holds it, so a partial or unauthenticated source leaves the remaining keys unset rather than empty.
func TestContextWithIdentityFrom_CopiesOnlyWhatSrcCarries(t *testing.T) {
	tests := []struct {
		name       string
		src        context.Context
		wantUserID string
		wantRoles  []Role
	}{
		{
			name: "unauthenticated source copies nothing",
			src:  context.Background(),
		},
		{
			name:      "roles without a subject",
			src:       context.WithValue(context.Background(), rolesKey, []Role{RoleUser}),
			wantRoles: []Role{RoleUser},
		},
		{
			name:       "subject without roles",
			src:        context.WithValue(context.Background(), userIDKey, "user-123"),
			wantUserID: "user-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ContextWithIdentityFrom(context.Background(), tt.src)

			userID, ok := UserIDFromContext(ctx)
			if tt.wantUserID == "" && ok {
				t.Errorf("UserIDFromContext = %q, want no value", userID)
			}
			if tt.wantUserID != "" && userID != tt.wantUserID {
				t.Errorf("UserIDFromContext = %q, want %q", userID, tt.wantUserID)
			}
			if got := RolesFromContext(ctx); !slices.Equal(got, tt.wantRoles) {
				t.Errorf("RolesFromContext = %v, want %v", got, tt.wantRoles)
			}
			if got := ScopesFromContext(ctx); got != nil {
				t.Errorf("ScopesFromContext = %v, want nil", got)
			}
		})
	}
}

// TestRolesFromContext checks that the roles stored under the roles key come back as written, and that a context
// without them, or holding a value of another type under that key, yields no roles.
func TestRolesFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want []Role
	}{
		{
			name: "roles present",
			ctx:  context.WithValue(context.Background(), rolesKey, []Role{RoleUser, RoleAdmin}),
			want: []Role{RoleUser, RoleAdmin},
		},
		{
			name: "no roles",
			ctx:  context.Background(),
		},
		{
			name: "another type under the roles key",
			ctx:  context.WithValue(context.Background(), rolesKey, []string{"admin"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RolesFromContext(tt.ctx); !slices.Equal(got, tt.want) {
				t.Errorf("RolesFromContext = %v, want %v", got, tt.want)
			}
		})
	}
}
