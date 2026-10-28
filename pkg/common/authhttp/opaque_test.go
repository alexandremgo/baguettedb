package authhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// testChecker accepts the token "bdbp_good" as the user u1 with the user role, refuses any other with
// ErrInvalidToken, and fails on "bdbp_broken" as a store outage would.
func testChecker(_ context.Context, token string) (string, []authz.Role, error) {
	switch token {
	case "bdbp_good":
		return "u1", []authz.Role{authz.RoleUser}, nil
	case "bdbp_broken":
		return "", nil, errors.New("store unreachable")
	default:
		return "", nil, ErrInvalidToken
	}
}

// serveWith runs one request through tv's RequireToken, with the bearer token in the Authorization header, or in the
// access token cookie when cookie is set, and returns the status and the user ID and roles the handler saw.
func serveWith(tv *TokenValidator, token string, cookie bool) (int, string, []authz.Role) {
	var userID string
	var roles []authz.Role
	handler := tv.RequireToken()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, _ = authz.UserIDFromContext(r.Context())
		roles = authz.RolesFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	switch {
	case token == "":
	case cookie:
		req.AddCookie(&http.Cookie{Name: "bdb_access_token", Value: token})
	default:
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, userID, roles
}

// TestRequireToken_OpaqueOnly checks a validator with no JWKS: a token its checker accepts authenticates the request
// with the user and roles it returns, from the header or the cookie; a refused one, a token without the prefix and no
// token at all are 401s; and a failing check is a 500.
func TestRequireToken_OpaqueOnly(t *testing.T) {
	tv := NewOpaqueOnlyValidator(zerolog.Nop(), testChecker, []string{"bdbp_"})
	defer tv.Close()

	for _, tt := range []struct {
		name   string
		token  string
		cookie bool
		code   int
		userID string
	}{
		{"an accepted token", "bdbp_good", false, http.StatusOK, "u1"},
		{"an accepted token in the cookie", "bdbp_good", true, http.StatusOK, "u1"},
		{"a refused token", "bdbp_nope", false, http.StatusUnauthorized, ""},
		{"a failing check", "bdbp_broken", false, http.StatusInternalServerError, ""},
		{"a token without the prefix", "eyJhbGciOiJSUzI1NiJ9.e30.sig", false, http.StatusUnauthorized, ""},
		{"no token", "", false, http.StatusUnauthorized, ""},
	} {
		code, userID, roles := serveWith(tv, tt.token, tt.cookie)
		if code != tt.code || userID != tt.userID {
			t.Errorf("%s = %d, user %q; want %d, user %q", tt.name, code, userID, tt.code, tt.userID)
		}
		if tt.userID != "" && !slices.Equal(roles, []authz.Role{authz.RoleUser}) {
			t.Errorf("%s roles = %v, want the checker's", tt.name, roles)
		}
	}
}

// TestRequireToken_OpaqueAlongsideJWKS checks that a validator with a JWKS and WithOpaqueTokens sends a prefixed token
// to its checker and validates any other as a JWT.
func TestRequireToken_OpaqueAlongsideJWKS(t *testing.T) {
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(w).Encode(jwk.NewSet()); err != nil {
			http.Error(w, "failed to encode JWKS", http.StatusInternalServerError)
		}
	}))
	defer jwksServer.Close()
	tv, err := NewTokenValidator(context.Background(), jwksServer.URL, "http://test-issuer", "", zerolog.Nop(),
		WithOpaqueTokens(testChecker, "bdbp_"))
	if err != nil {
		t.Fatalf("NewTokenValidator = %v", err)
	}
	defer tv.Close()

	if code, userID, _ := serveWith(tv, "bdbp_good", false); code != http.StatusOK || userID != "u1" {
		t.Errorf("an accepted opaque token = %d, user %q; want 200, user u1", code, userID)
	}
	if code, _, _ := serveWith(tv, "not-a-jwt", false); code != http.StatusUnauthorized {
		t.Errorf("a token without the prefix = %d, want 401 from the JWT path", code)
	}
}

// TestHasOpaquePrefix checks that a token matches when it starts with any of the prefixes, not only the first, and
// that an empty prefix or an empty list matches nothing.
func TestHasOpaquePrefix(t *testing.T) {
	for _, tt := range []struct {
		token    string
		prefixes []string
		want     bool
	}{
		{"bdbp_x", []string{"bdbp_"}, true},
		{"bdbk_x", []string{"bdbp_", "bdbk_"}, true},
		{"bdbl_x", []string{"bdbp_", "bdbk_"}, false},
		{"anything", []string{""}, false},
		{"bdbp_x", []string{"", "bdbp_"}, true},
		{"bdbp_x", nil, false},
	} {
		if got := hasOpaquePrefix(tt.token, tt.prefixes); got != tt.want {
			t.Errorf("hasOpaquePrefix(%q, %q) = %v, want %v", tt.token, tt.prefixes, got, tt.want)
		}
	}
}
