package authhttp

import (
	"crypto/subtle"
	"net/http"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// InternalSecretHeader is the request header carrying the shared secret for internal service-to-service endpoints.
const InternalSecretHeader = "X-Internal-Api-Secret"

// SetInternalSecretHeader sets the internal service-to-service secret on outgoing request headers, keeping the
// header name in one place for all callers.
func SetInternalSecretHeader(h http.Header, secret string) {
	h.Set(InternalSecretHeader, secret)
}

// RequireInternalSecret returns a chi middleware that rejects with a 401 any request whose InternalSecretHeader
// does not match secret. The comparison is constant-time, and an empty secret rejects every request: enforcement
// fails closed when the deployment leaves the secret unconfigured.
func RequireInternalSecret(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := r.Header.Get(InternalSecretHeader)
			if secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(provided)) != 1 {
				httputil.RespondJSON(w, http.StatusUnauthorized, errorBody{Error: "invalid internal secret"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
