//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	authmodule "github.com/alexandremgo/baguettedb/pkg/auth"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestREST_Tokens checks the personal access token routes: a user creates a token, shown once with its prefix, lists
// it, manages tokens with the token itself, and revokes it, after which it stops authenticating; a second revoke,
// another user's token, a malformed ID, an empty name and an expiry in the past are refused, and so is a request with
// no identity. A token created with an expiry lists it.
func TestREST_Tokens(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := createUser(t, "tokens-api@example.com", "Str0ng-Passw0rd!")
	jwt := userToken(t, userID)

	status, created := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/personal-tokens", jwt,
		map[string]string{"name": "laptop"})
	pat, _ := created["token"].(string)
	prefix, _ := created["token_prefix"].(string)
	if status != http.StatusCreated || !strings.HasPrefix(pat, "bdbp_"+prefix) || created["name"] != "laptop" ||
		created["last_used_at"] != nil {
		t.Fatalf("POST /personal-tokens = %d %v; want 201 with a bdbp_ token, its prefix and no use yet", status,
			created)
	}
	tokenID, _ := created["id"].(string)

	status, listed := testutil.DoRequest(t, router, http.MethodGet, "/api/auth/v1/personal-tokens", pat, nil)
	tokens, _ := listed["tokens"].([]any)
	if status != http.StatusOK || len(tokens) != 1 || tokens[0].(map[string]any)["id"] != tokenID ||
		tokens[0].(map[string]any)["token"] != nil {
		t.Fatalf("GET /personal-tokens with the token = %d %v; want the token alone, without its value", status,
			listed)
	}

	otherJWT := userToken(t, createUser(t, "tokens-api-other@example.com", "Str0ng-Passw0rd!"))
	for _, tt := range []struct {
		name, method, path, bearer string
		body                       any
		status                     int
	}{
		{"another user's token", http.MethodDelete, "/api/auth/v1/personal-tokens/" + tokenID, otherJWT, nil,
			http.StatusNotFound},
		{"a malformed ID", http.MethodDelete, "/api/auth/v1/personal-tokens/nope", jwt, nil,
			http.StatusBadRequest},
		{"an empty name", http.MethodPost, "/api/auth/v1/personal-tokens", jwt, map[string]string{"name": " "},
			http.StatusBadRequest},
		{"an expiry in the past", http.MethodPost, "/api/auth/v1/personal-tokens", jwt,
			map[string]string{"name": "old", "expires_at": "2020-01-01T00:00:00Z"}, http.StatusBadRequest},
		{"no identity", http.MethodGet, "/api/auth/v1/personal-tokens", "", nil, http.StatusUnauthorized},
	} {
		if status, resp := testutil.DoRequest(t, router, tt.method, tt.path, tt.bearer, tt.body); status != tt.status {
			t.Errorf("%s: %s %s = %d %v, want %d", tt.name, tt.method, tt.path, status, resp, tt.status)
		}
	}

	expiresAt := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second).Format(time.RFC3339)
	status, expiring := testutil.DoRequest(t, router, http.MethodPost, "/api/auth/v1/personal-tokens", jwt,
		map[string]string{"name": "monthly", "expires_at": expiresAt})
	if got, _ := expiring["expires_at"].(string); status != http.StatusCreated || !sameInstant(got, expiresAt) {
		t.Errorf("POST /personal-tokens with an expiry = %d %v; want 201 and expires_at %s", status, expiring,
			expiresAt)
	}

	revoke := "/api/auth/v1/personal-tokens/" + tokenID
	if status, resp := testutil.DoRequest(t, router, http.MethodDelete, revoke, jwt, nil); status !=
		http.StatusNoContent {
		t.Fatalf("DELETE /personal-tokens/{id} = %d %v, want 204", status, resp)
	}
	if status, _ := testutil.DoRequest(t, router, http.MethodGet, "/api/auth/v1/me", pat, nil); status !=
		http.StatusUnauthorized {
		t.Errorf("GET /me with the revoked token = %d, want 401", status)
	}
	if status, _ := testutil.DoRequest(t, router, http.MethodDelete, revoke, jwt, nil); status != http.StatusNotFound {
		t.Errorf("a second DELETE = %d, want 404", status)
	}
}

// TestSetup_PersonalAccessTokens checks that the auth app accepts personal access tokens and mounts the routes
// managing them only when SetupOptions.PersonalAccessTokens is on. Off, a valid token is refused with 401 and the
// routes answer 404 to a JWT, and the result carries no opaque token checker.
func TestSetup_PersonalAccessTokens(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "on", false: "off"}[on], func(t *testing.T) {
			truncateAll(t)
			res := setupWithOAuth(t, authmodule.SetupOptions{PersonalAccessTokens: on})

			ctx := context.Background()
			user, err := res.UserSvc.CreateUser(ctx, "setup-pat-"+uuid.NewString()+"@example.com",
				"Str0ng-Passw0rd!", "Setup", "Token")
			if err != nil {
				t.Fatalf("creating a user: %v", err)
			}
			pat, _, err := res.TokenSvc.Create(ctx, user.ID, "test", nil)
			if err != nil {
				t.Fatalf("creating a token: %v", err)
			}
			jwt := userToken(t, user.ID.String())

			wantMe, wantRoutes := http.StatusOK, http.StatusOK
			if !on {
				wantMe, wantRoutes = http.StatusUnauthorized, http.StatusNotFound
			}
			if status, _ := testutil.DoRequest(t, res.Router, http.MethodGet, "/api/auth/v1/me", pat, nil); status !=
				wantMe {
				t.Errorf("GET /me with the token = %d, want %d", status, wantMe)
			}
			if status, _ := testutil.DoRequest(t, res.Router, http.MethodGet, "/api/auth/v1/personal-tokens", jwt,
				nil); status != wantRoutes {
				t.Errorf("GET /personal-tokens with a JWT = %d, want %d", status, wantRoutes)
			}
			if (res.OpaqueTokenChecker != nil) != on {
				t.Errorf("OpaqueTokenChecker set = %t, want %t", res.OpaqueTokenChecker != nil, on)
			}
		})
	}
}

// setupWithOAuth sets up the auth app with its OAuth login flow against the shared test database and opts, and
// releases it when the test ends. JWTs are checked against the suite's JWKS standing in for Hydra's, and a preset
// web client spares the Hydra admin call.
func setupWithOAuth(t *testing.T, opts authmodule.SetupOptions) *authmodule.SetupResult {
	t.Helper()
	cfg := config.Config{
		InternalAPISecret: testutil.InternalAPISecret,
		PublicURL:         "http://localhost:8080",
		HydraPublicURL:    testJWKS.JWKSURL(),
		HydraIssuerURL:    testJWKS.Issuer(),
		HydraAdminURL:     "http://127.0.0.1:1",
		WebClientID:       "test-web-client",
	}
	testDB.Configure(&cfg)
	res, err := authmodule.Setup(context.Background(), cfg, testLogger, opts)
	if err != nil {
		t.Fatalf("setting up the auth app: %v", err)
	}
	t.Cleanup(res.Cleanup)
	return res
}

// sameInstant reports whether the RFC 3339 times a and b are the same instant, whatever their zones.
func sameInstant(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	return errA == nil && errB == nil && ta.Equal(tb)
}
