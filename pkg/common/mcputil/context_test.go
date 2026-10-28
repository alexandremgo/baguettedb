package mcputil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// newTestValidator builds a real TokenValidator pointed at a local test JWKS server.
func newTestValidator(t *testing.T, jwks *testutil.TestJWKS) *authhttp.TokenValidator {
	t.Helper()
	tv, err := authhttp.NewTokenValidator(context.Background(), jwks.JWKSURL(), jwks.Issuer(), "", zerolog.Nop())
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)
	return tv
}

func internalMCPRequest(t *testing.T, bearer string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/mcp", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

func TestNewInternalHTTPContextFunc_ValidTokenSetsUser(t *testing.T) {
	jwks, err := testutil.NewTestJWKS()
	if err != nil {
		t.Fatalf("creating test JWKS: %v", err)
	}
	t.Cleanup(jwks.Close)
	ctxFunc := NewInternalHTTPContextFunc(newTestValidator(t, jwks))

	token, err := jwks.BuildToken("user-123", []string{"user"})
	if err != nil {
		t.Fatalf("building token: %v", err)
	}

	ctx := ctxFunc(context.Background(), internalMCPRequest(t, token))

	userID, ok := UserIDFromContext(ctx)
	if !ok || userID != "user-123" {
		t.Fatalf("UserIDFromContext = (%q, %v), want (\"user-123\", true)", userID, ok)
	}
}

func TestNewInternalHTTPContextFunc_RejectsUnverifiedTokens(t *testing.T) {
	jwks, err := testutil.NewTestJWKS()
	if err != nil {
		t.Fatalf("creating test JWKS: %v", err)
	}
	t.Cleanup(jwks.Close)
	ctxFunc := NewInternalHTTPContextFunc(newTestValidator(t, jwks))

	// A token signed by a different key: parseable, valid-looking claims, but its signature does not verify
	// against the validator's JWKS — the exact forgery the unverified parse used to accept.
	forger, err := testutil.NewTestJWKS()
	if err != nil {
		t.Fatalf("creating forger JWKS: %v", err)
	}
	t.Cleanup(forger.Close)
	forged, err := forger.BuildToken("victim-user", []string{"user"})
	if err != nil {
		t.Fatalf("building forged token: %v", err)
	}

	tests := []struct {
		name   string
		bearer string
	}{
		{name: "no authorization header", bearer: ""},
		{name: "garbage token", bearer: "not-a-jwt"},
		{name: "forged token signed by another key", bearer: forged},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ctxFunc(context.Background(), internalMCPRequest(t, tt.bearer))
			if userID, ok := UserIDFromContext(ctx); ok {
				t.Fatalf("expected no user in context, got %q", userID)
			}
			if roles := authz.RolesFromContext(ctx); roles != nil {
				t.Fatalf("expected no roles in context, got %v", roles)
			}
		})
	}
}

// authenticatedRequest returns the request as RequireToken hands it to the next handler: its context carries the
// subject, scopes, and roles read from the verified token.
func authenticatedRequest(t *testing.T, tv *authhttp.TokenValidator, bearer string) *http.Request {
	t.Helper()

	var authed *http.Request
	handler := tv.RequireToken()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		authed = r
	}))

	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if authed == nil {
		t.Fatal("RequireToken rejected the request")
	}
	return authed
}

// TestSSEContextFunc_CarriesRolesFromRequest checks that the roles of a signed token reach the MCP handler
// context, driving the real RequireToken chain rather than a hand-built request context.
func TestSSEContextFunc_CarriesRolesFromRequest(t *testing.T) {
	jwks, err := testutil.NewTestJWKS()
	if err != nil {
		t.Fatalf("creating test JWKS: %v", err)
	}
	t.Cleanup(jwks.Close)

	token, err := jwks.BuildToken("user-123", []string{"user", "admin"})
	if err != nil {
		t.Fatalf("building token: %v", err)
	}
	req := authenticatedRequest(t, newTestValidator(t, jwks), token)

	ctx := SSEContextFunc(context.Background(), req)

	userID, ok := UserIDFromContext(ctx)
	if !ok || userID != "user-123" {
		t.Fatalf("UserIDFromContext = (%q, %v), want (\"user-123\", true)", userID, ok)
	}

	// Quota resolution reads roles off the context deep in the services, so an MCP call that drops them
	// resolves the caller's limits as if they held no role.
	want := []authz.Role{authz.RoleUser, authz.RoleAdmin}
	if got := authz.RolesFromContext(ctx); !slices.Equal(got, want) {
		t.Fatalf("RolesFromContext = %v, want %v", got, want)
	}
}

// TestNewInternalHTTPContextFunc_CarriesRolesFromToken checks that the roles are read off the verified token,
// the only identity source on a path with no authenticated request context to copy from.
func TestNewInternalHTTPContextFunc_CarriesRolesFromToken(t *testing.T) {
	jwks, err := testutil.NewTestJWKS()
	if err != nil {
		t.Fatalf("creating test JWKS: %v", err)
	}
	t.Cleanup(jwks.Close)
	ctxFunc := NewInternalHTTPContextFunc(newTestValidator(t, jwks))

	token, err := jwks.BuildToken("user-123", []string{"user", "admin"})
	if err != nil {
		t.Fatalf("building token: %v", err)
	}

	ctx := ctxFunc(context.Background(), internalMCPRequest(t, token))

	want := []authz.Role{authz.RoleUser, authz.RoleAdmin}
	if got := authz.RolesFromContext(ctx); !slices.Equal(got, want) {
		t.Fatalf("RolesFromContext = %v, want %v", got, want)
	}
}

// TestContextWithIdentity checks that one call makes the identity visible to both readers: the MCP user ID that tool
// handlers resolve, and the subject and roles the auth package reads for permission checks.
func TestContextWithIdentity(t *testing.T) {
	ctx := ContextWithIdentity(context.Background(), "user-123", []authz.Role{authz.RoleUser, authz.RoleAdmin})

	if userID, ok := UserIDFromContext(ctx); !ok || userID != "user-123" {
		t.Errorf("UserIDFromContext = (%q, %v), want (\"user-123\", true)", userID, ok)
	}
	if sub, ok := authz.UserIDFromContext(ctx); !ok || sub != "user-123" {
		t.Errorf("authz.UserIDFromContext = (%q, %v), want (\"user-123\", true)", sub, ok)
	}
	if got := authz.RolesFromContext(ctx); !slices.Equal(got, []authz.Role{authz.RoleUser, authz.RoleAdmin}) {
		t.Errorf("authz.RolesFromContext = %v, want [user admin]", got)
	}
	if got := authz.ScopesFromContext(ctx); got != nil {
		t.Errorf("authz.ScopesFromContext = %v, want nil", got)
	}
}
