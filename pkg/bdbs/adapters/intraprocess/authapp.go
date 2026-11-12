// Package intraprocess implements the bdbs service's ports to the auth and databases apps with direct calls on their
// services, for deployments running the modules in one process. The adapters convert the services' results into the
// cross-app transfer shapes through each providing app's appconv package.
package intraprocess

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	authappconv "github.com/alexandremgo/baguettedb/pkg/auth/appconv"
	authdomain "github.com/alexandremgo/baguettedb/pkg/auth/domain"
	authservice "github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// AuthApp wraps the auth module's services for in-process use.
type AuthApp struct {
	roleSvc  *authservice.RoleService
	userSvc  *authservice.UserService
	tokenSvc *authservice.TokenService
}

// NewAuthApp creates a new AuthApp reading roles and profiles through roleSvc and userSvc, and managing Data API keys
// through tokenSvc.
func NewAuthApp(
	roleSvc *authservice.RoleService, userSvc *authservice.UserService, tokenSvc *authservice.TokenService,
) *AuthApp {
	return &AuthApp{roleSvc: roleSvc, userSvc: userSvc, tokenSvc: tokenSvc}
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

// CreateDataAPIKey delegates to the auth module's token service and converts the key to the cross-app transfer shape.
func (a *AuthApp) CreateDataAPIKey(
	ctx context.Context, userID, bdbID uuid.UUID, name string,
) (apptypes.CreatedDataAPIKey, error) {
	plain, token, err := a.tokenSvc.CreateDataAPIKey(ctx, userID, bdbID, name)
	if err != nil {
		return apptypes.CreatedDataAPIKey{}, dataAPIKeyError(err)
	}
	return apptypes.CreatedDataAPIKey{Key: authappconv.DataAPIKey(token), Value: plain}, nil
}

// ListDataAPIKeys delegates to the auth module's token service and converts the keys to the cross-app transfer shape.
func (a *AuthApp) ListDataAPIKeys(ctx context.Context, bdbID uuid.UUID) ([]apptypes.DataAPIKey, error) {
	tokens, err := a.tokenSvc.ListDataAPIKeys(ctx, bdbID)
	if err != nil {
		return nil, err
	}
	keys := make([]apptypes.DataAPIKey, len(tokens))
	for i, t := range tokens {
		keys[i] = authappconv.DataAPIKey(t)
	}
	return keys, nil
}

// RevokeDataAPIKey delegates to the auth module's token service.
func (a *AuthApp) RevokeDataAPIKey(ctx context.Context, bdbID, keyID uuid.UUID) error {
	return dataAPIKeyError(a.tokenSvc.RevokeDataAPIKey(ctx, bdbID, keyID))
}

// RevokeDataAPIKeys delegates to the auth module's token service.
func (a *AuthApp) RevokeDataAPIKeys(ctx context.Context, bdbID uuid.UUID) error {
	return a.tokenSvc.RevokeDataAPIKeys(ctx, bdbID)
}

// RevokeMemberDataAPIKeys delegates to the auth module's token service.
func (a *AuthApp) RevokeMemberDataAPIKeys(ctx context.Context, bdbID, userID uuid.UUID) error {
	return a.tokenSvc.RevokeMemberDataAPIKeys(ctx, bdbID, userID)
}

// dataAPIKeyError translates a Data API key failure of the auth app into the bdbs domain's error for it, wherever it
// sits in err's chain, so the bdbs app matches on its own errors: an invalid name keeps what is wrong with it. Any
// other error, nil included, is returned unchanged.
func dataAPIKeyError(err error) error {
	if nameErr, ok := errors.AsType[authdomain.InvalidAccessTokenNameError](err); ok {
		return fmt.Errorf("%w: %s", domain.ErrInvalidDataAPIKeyName, nameErr.Problem)
	}
	if errors.Is(err, authdomain.ErrAccessTokenNotFound) {
		return domain.ErrDataAPIKeyNotFound
	}
	return err
}
