// Package intraprocess implements the bdbs service's ports to the auth and databases apps with direct calls on their
// services, for deployments running the modules in one process. The adapters convert the services' results into the
// cross-app transfer shapes through each providing app's appconv package.
package intraprocess

import (
	"context"

	"github.com/google/uuid"

	authappconv "github.com/alexandremgo/baguettedb/pkg/auth/appconv"
	authservice "github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// AuthApp wraps the auth module's services for in-process use.
type AuthApp struct {
	roleSvc *authservice.RoleService
	userSvc *authservice.UserService
}

// NewAuthApp creates a new AuthApp.
func NewAuthApp(roleSvc *authservice.RoleService, userSvc *authservice.UserService) *AuthApp {
	return &AuthApp{roleSvc: roleSvc, userSvc: userSvc}
}

// GetUserRoles delegates to the auth module's role service.
func (a *AuthApp) GetUserRoles(ctx context.Context, userID uuid.UUID) ([]authz.Role, error) {
	return a.roleSvc.GetUserRoles(ctx, userID)
}

// GetUserProfiles delegates to the auth module's user service and converts the result to the cross-app transfer
// shape, keyed by user ID. Unknown IDs are omitted.
func (a *AuthApp) GetUserProfiles(
	ctx context.Context, userIDs []uuid.UUID,
) (map[uuid.UUID]*apptypes.UserProfile, error) {
	profiles, err := a.userSvc.GetProfiles(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*apptypes.UserProfile, len(profiles))
	for _, p := range profiles {
		profile := authappconv.UserProfile(p)
		out[p.ID] = &profile
	}
	return out, nil
}
