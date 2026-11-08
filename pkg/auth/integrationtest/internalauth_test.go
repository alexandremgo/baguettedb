//go:build integration

package integrationtest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
)

// TestInternalRoutes_RequireSecret verifies the internal service-to-service surface rejects requests missing the
// shared internal API secret, on every internal route. Raw requests are built here on purpose: testutil.DoRequest
// attaches the well-known test secret to /internal/ paths automatically.
func TestInternalRoutes_RequireSecret(t *testing.T) {
	router := setupRouter(t)

	tests := []struct {
		name      string
		setHeader func(h http.Header)
	}{
		{name: "missing secret", setHeader: func(http.Header) {}},
		{name: "wrong secret", setHeader: func(h http.Header) { authhttp.SetInternalSecretHeader(h, "nope") }},
	}
	paths := []string{
		"/internal/api/auth/v1/user-roles",
		"/internal/api/auth/v1/user-profiles",
		"/internal/api/auth/v1/data-api-keys",
		"/internal/api/auth/v1/data-api-keys/create",
		"/internal/api/auth/v1/data-api-keys/check",
		"/internal/api/auth/v1/data-api-keys/revoke",
		"/internal/api/auth/v1/data-api-keys/revoke-all",
		"/internal/api/auth/v1/data-api-keys/revoke-member",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, path := range paths {
				req := httptest.NewRequest(http.MethodPost, path, nil)
				tt.setHeader(req.Header)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != http.StatusUnauthorized {
					t.Errorf("POST %s: status = %d, want %d", path, rec.Code, http.StatusUnauthorized)
				}
			}
		})
	}
}
