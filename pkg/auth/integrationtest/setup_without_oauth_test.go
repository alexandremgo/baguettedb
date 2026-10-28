//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	authmodule "github.com/alexandremgo/baguettedb/pkg/auth"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// setupWithoutOAuth sets up the auth app against the shared test database with requireToken injected, and
// releases it when the test ends. The Hydra URLs point at a closed port, so any attempt to reach Hydra fails.
func setupWithoutOAuth(t *testing.T, requireToken func(http.Handler) http.Handler) *authmodule.SetupResult {
	t.Helper()
	cfg := config.Config{
		InternalAPISecret: testutil.InternalAPISecret,
		PublicURL:         "http://localhost:8080",
		HydraPublicURL:    "http://127.0.0.1:1",
		HydraAdminURL:     "http://127.0.0.1:1",
	}
	testDB.Configure(&cfg)
	res, err := authmodule.SetupWithoutOAuth(
		context.Background(), cfg, testLogger, authmodule.SetupOptions{RequireToken: requireToken},
	)
	if err != nil {
		t.Fatalf("setting up the auth app without OAuth: %v", err)
	}
	t.Cleanup(res.Cleanup)
	return res
}

// TestRouter_InjectedMiddlewareIdentity checks that the protected routes of an auth app set up with an injected
// middleware see the identity that middleware sets: /me resolves the caller, and the admin routes grant or refuse
// access from the injected roles.
func TestRouter_InjectedMiddlewareIdentity(t *testing.T) {
	truncateAll(t)
	adminID := testDB.SeedUser(t, "injected-admin@example.com", []string{"user", "admin"})
	userID := testDB.SeedUser(t, "injected-user@example.com", []string{"user"})

	admin := setupWithoutOAuth(t,
		testutil.IdentityMiddleware(adminID.String(), authz.RoleUser, authz.RoleAdmin)).Router
	user := setupWithoutOAuth(t, testutil.IdentityMiddleware(userID.String(), authz.RoleUser)).Router

	status, resp := testutil.DoRequest(t, admin, http.MethodGet, "/api/auth/v1/me", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /me status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	if resp["email"] != "injected-admin@example.com" {
		t.Errorf("GET /me email = %v, want injected-admin@example.com", resp["email"])
	}

	status, resp = testutil.DoRequest(t, admin, http.MethodGet, "/api/auth/v1/admin/users", "", nil)
	if status != http.StatusOK {
		t.Errorf("admin GET /admin/users status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	status, _ = testutil.DoRequest(t, user, http.MethodGet, "/api/auth/v1/admin/users", "", nil)
	if status != http.StatusForbidden {
		t.Errorf("user GET /admin/users status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestSetupWithoutOAuth_MountsNoOAuthRoutes checks that the auth app set up without OAuth mounts none of the login
// flow's routes — login and consent pages, OAuth2 endpoints and their aliases, discovery documents, cookie token
// endpoints — while its API routes are there.
func TestSetupWithoutOAuth_MountsNoOAuthRoutes(t *testing.T) {
	router := setupWithoutOAuth(t, testutil.IdentityMiddleware("unused")).Router

	mounted := map[string]bool{}
	err := chi.Walk(router, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mounted[route] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walking routes: %v", err)
	}

	for route := range mounted {
		for _, prefix := range []string{
			"/login", "/consent", "/oauth2/", "/.well-known/", "/authorize", "/token", "/register", "/userinfo",
			"/api/auth/v1/token", "/api/auth/v1/logout",
		} {
			if strings.HasPrefix(route, prefix) {
				t.Errorf("route %s is mounted, want no OAuth route", route)
			}
		}
	}
	for _, route := range []string{"/api/auth/v1/me", "/api/auth/v1/admin/users"} {
		if !mounted[route] {
			t.Errorf("route %s is not mounted, want the API routes", route)
		}
	}
}

// TestSetupWithoutOAuth_PersonalAccessTokensAlone checks that the auth app set up without OAuth and with no injected
// middleware authenticates with its personal access tokens alone: a token gets the caller's profile and is the
// result's RequireToken, while no token and a JWT are refused. Turning neither on is a setup error.
func TestSetupWithoutOAuth_PersonalAccessTokensAlone(t *testing.T) {
	truncateAll(t)
	cfg := config.Config{InternalAPISecret: testutil.InternalAPISecret, PublicURL: "http://localhost:8080"}
	testDB.Configure(&cfg)

	if _, err := authmodule.SetupWithoutOAuth(context.Background(), cfg, testLogger,
		authmodule.SetupOptions{}); err == nil {
		t.Fatal("SetupWithoutOAuth with neither RequireToken nor PersonalAccessTokens succeeded, want an error")
	}

	res, err := authmodule.SetupWithoutOAuth(context.Background(), cfg, testLogger,
		authmodule.SetupOptions{PersonalAccessTokens: true})
	if err != nil {
		t.Fatalf("setting up the auth app with personal access tokens alone: %v", err)
	}
	t.Cleanup(res.Cleanup)
	if res.RequireToken == nil {
		t.Fatal("SetupResult.RequireToken is nil, want the middleware the setup built")
	}

	ctx := context.Background()
	user, err := res.UserSvc.CreateUser(ctx, "alone@example.com", "Str0ng-Passw0rd!", "Alone", "Token")
	if err != nil {
		t.Fatalf("creating a user: %v", err)
	}
	pat, _, err := res.TokenSvc.Create(ctx, user.ID, "test", nil)
	if err != nil {
		t.Fatalf("creating a token: %v", err)
	}

	status, resp := testutil.DoRequest(t, res.Router, http.MethodGet, "/api/auth/v1/me", pat, nil)
	if status != http.StatusOK || resp["email"] != "alone@example.com" {
		t.Errorf("GET /me with the token = %d %v, want 200 with the caller's profile", status, resp)
	}
	for name, bearer := range map[string]string{"no token": "", "a JWT": userToken(t, user.ID.String())} {
		if status, _ := testutil.DoRequest(t, res.Router, http.MethodGet, "/api/auth/v1/me", bearer, nil); status !=
			http.StatusUnauthorized {
			t.Errorf("GET /me with %s = %d, want 401", name, status)
		}
	}
}
