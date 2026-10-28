package testutil

import (
	"net/http"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// IdentityMiddleware returns an authentication middleware that accepts every request as userID holding roles, with
// no token involved. It stands in for the JWT validator when a test injects the middleware a router or app setup
// guards its protected routes with.
func IdentityMiddleware(userID string, roles ...authz.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(authz.ContextWithIdentity(r.Context(), userID, roles)))
		})
	}
}
