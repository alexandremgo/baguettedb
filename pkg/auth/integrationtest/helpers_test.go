//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/api"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// testUserSvc is a package-level UserService used by test helpers to create users without going through the HTTP layer.
var testUserSvc *service.UserService

// setupRouter wires up the full auth HTTP stack backed by the shared test pool, its validator accepting the suite's
// JWTs and the personal access tokens of its token service.
func setupRouter(t *testing.T) chi.Router {
	t.Helper()

	r := repos()
	authenticationSvc := service.NewAuthenticationService(r.users)
	userSvc := service.NewUserService(r.users, r.userTx, testLogger)
	roleSvc := service.NewRoleService(r.roles, authz.Policy{})

	testUserSvc = userSvc

	handler := api.NewHandler(authenticationSvc, userSvc, roleSvc, nil, "", "", testLogger)
	internalHandler := api.NewInternalHandler(roleSvc, userSvc)
	publicURL := "http://localhost:8080"
	discovery := api.NewDiscoveryHandler(publicURL, testJWKS.Issuer(), testLogger)
	oidcProxy := api.NewOIDCProxyHandler(nil, publicURL, testLogger)

	tokens := newTokenService()
	tv, err := authhttp.NewTokenValidator(context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger,
		authhttp.WithOpaqueTokens(checkTestToken(tokens), domain.AccessTokenPersonal.Prefix()))
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	router := api.NewRouter(handler, internalHandler, tv.RequireToken(), authz.Policy{}, testutil.InternalAPISecret)
	api.RegisterOAuthRoutes(router, handler, discovery, oidcProxy, nil, nil, nil)
	api.RegisterTokenRoutes(router, api.NewTokenHandler(tokens, testLogger), tv.RequireToken())
	return router
}

// checkTestToken adapts tokens' check to the middleware's, as the auth app's setup does: a refused token is
// authhttp.ErrInvalidToken.
func checkTestToken(tokens *service.TokenService) authhttp.OpaqueTokenChecker {
	return func(ctx context.Context, token string) (string, []authz.Role, error) {
		userID, roles, err := tokens.Check(ctx, token)
		if errors.Is(err, domain.ErrInvalidAccessToken) {
			return "", nil, authhttp.ErrInvalidToken
		}
		if err != nil {
			return "", nil, err
		}
		return userID.String(), roles, nil
	}
}

// createUser creates a user via the UserService and returns the user ID string.
func createUser(t *testing.T, email, password string) string {
	t.Helper()
	user, err := testUserSvc.CreateUser(context.Background(), email, password, "Test", "User")
	if err != nil {
		t.Fatalf("creating user %s: %v", email, err)
	}
	return user.ID.String()
}

// seedAdmin creates a user and promotes them to admin. Returns the user ID string.
func seedAdmin(t *testing.T, email, password string) string {
	t.Helper()
	userID := createUser(t, email, password)

	uid, _ := uuid.Parse(userID)
	if err := repos().roles.AssignRole(context.Background(), uid, authz.RoleAdmin); err != nil {
		t.Fatalf("assigning admin role: %v", err)
	}
	return userID
}

// adminToken builds a JWT with admin role for the given user ID.
func adminToken(t *testing.T, userID string) string {
	t.Helper()
	tok, err := testJWKS.BuildToken(userID, []string{"admin"})
	if err != nil {
		t.Fatalf("building admin token: %v", err)
	}
	return tok
}

// userToken builds a JWT with user role for the given user ID.
func userToken(t *testing.T, userID string) string {
	t.Helper()
	tok, err := testJWKS.BuildToken(userID, []string{"user"})
	if err != nil {
		t.Fatalf("building user token: %v", err)
	}
	return tok
}
