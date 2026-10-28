package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// RoleService manages user role operations.
type RoleService struct {
	repo   RoleRepository
	policy authz.Policy
}

// NewRoleService creates a new RoleService that accepts the global roles policy recognizes.
func NewRoleService(repo RoleRepository, policy authz.Policy) *RoleService {
	return &RoleService{repo: repo, policy: policy}
}

// GetUserRoles returns all roles assigned to the given user. A plain read, kept on the service so that role storage
// stays reachable only through the app's services.
func (s *RoleService) GetUserRoles(ctx context.Context, userID uuid.UUID) ([]authz.Role, error) {
	return s.repo.GetRoles(ctx, userID)
}

// AssignRole validates and assigns the given role to a user.
func (s *RoleService) AssignRole(ctx context.Context, userID uuid.UUID, role string) error {
	r := authz.Role(role)
	if !s.policy.IsValidRole(r) {
		return domain.ErrInvalidRole
	}
	return s.repo.AssignRole(ctx, userID, r)
}

// RemoveRole removes the given role from a user.
func (s *RoleService) RemoveRole(ctx context.Context, userID uuid.UUID, role string) error {
	r := authz.Role(role)
	if !s.policy.IsValidRole(r) {
		return domain.ErrInvalidRole
	}
	return s.repo.RemoveRole(ctx, userID, r)
}
