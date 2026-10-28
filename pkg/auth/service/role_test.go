package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

func TestRoleService_AssignRole(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		repoErr error
		wantErr error
	}{
		{"assigns valid role", "user", nil, nil},
		{"rejects invalid role", "superadmin", nil, domain.ErrInvalidRole},
		{"idempotent re-assign", "user", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockRoleRepo{assignErr: tt.repoErr}
			svc := NewRoleService(repo, authz.Policy{})

			err := svc.AssignRole(context.Background(), uuid.New(), tt.role)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("AssignRole() error = %v, wantErr = %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("AssignRole() unexpected error: %v", err)
			}
		})
	}
}

func TestRoleService_RemoveRole(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		wantErr error
	}{
		{"removes valid role", "user", nil},
		{"rejects invalid role", "superadmin", domain.ErrInvalidRole},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &mockRoleRepo{}
			svc := NewRoleService(repo, authz.Policy{})

			err := svc.RemoveRole(context.Background(), uuid.New(), tt.role)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("RemoveRole() error = %v, wantErr = %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RemoveRole() unexpected error: %v", err)
			}
		})
	}
}

func TestRoleService_GetUserRoles(t *testing.T) {
	roles := []authz.Role{authz.RoleUser, authz.RoleAdmin}
	repo := &mockRoleRepo{roles: roles}
	svc := NewRoleService(repo, authz.Policy{})

	result, err := svc.GetUserRoles(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetUserRoles() unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("GetUserRoles() returned %d roles, want 2", len(result))
	}
	if result[0] != authz.RoleUser {
		t.Errorf("GetUserRoles()[0] = %q, want %q", result[0], authz.RoleUser)
	}
	if result[1] != authz.RoleAdmin {
		t.Errorf("GetUserRoles()[1] = %q, want %q", result[1], authz.RoleAdmin)
	}
}

func TestUserService_ListUsers(t *testing.T) {
	users := []*domain.User{{Email: "a@b.com"}, {Email: "c@d.com"}}
	repo := &mockUserRepo{users: users, totalUsers: 2}
	svc := NewUserService(repo, nil, zerolog.Nop())

	result, total, err := svc.ListUsers(context.Background(), "", 10, 0)
	if err != nil {
		t.Fatalf("ListUsers() unexpected error: %v", err)
	}
	if total != 2 {
		t.Errorf("ListUsers() total = %d, want 2", total)
	}
	if len(result) != 2 {
		t.Fatalf("ListUsers() returned %d users, want 2", len(result))
	}
	if result[0].Email != "a@b.com" {
		t.Errorf("ListUsers()[0].Email = %q, want %q", result[0].Email, "a@b.com")
	}
	if result[1].Email != "c@d.com" {
		t.Errorf("ListUsers()[1].Email = %q, want %q", result[1].Email, "c@d.com")
	}
}
