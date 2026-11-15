package dataapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// keyContextKey is the request context key holding the Data API key a request was authenticated with.
type keyContextKey struct{}

// requireKey authenticates a request with the Data API key of its Authorization header, sent as a bearer token, and
// puts what the key authenticates in the request context: the key itself, and its creator as the caller. A cookie is
// never read, so a browser's session authenticates nothing here.
//
// A request without a key, or with one that authenticates nothing, is a 401; a key bound to another bdb than the
// path's a 403.
func requireKey(check authhttp.DataAPIKeyChecker, logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plain, ok := bearerKey(r)
			if !ok {
				respondError(w, http.StatusUnauthorized, codeMissingKey,
					"a data API key is required, as a bearer token in the Authorization header")
				return
			}
			key, err := check(r.Context(), plain)
			if errors.Is(err, authhttp.ErrInvalidToken) {
				respondError(w, http.StatusUnauthorized, codeInvalidKey, "the data API key is unknown or revoked")
				return
			}
			if err != nil {
				logger.Error().Err(err).Msg("failed to check a data API key")
				respondError(w, http.StatusInternalServerError, codeInternalError, "internal server error")
				return
			}
			if !keyBoundToPath(key, r) {
				respondError(w, http.StatusForbidden, codeKeyNotForBdb, "the data API key is bound to another bdb")
				return
			}

			ctx := authz.ContextWithIdentity(r.Context(), key.UserID.String(), key.Roles)
			ctx = context.WithValue(ctx, keyContextKey{}, key)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// bearerKey returns the value of r's Authorization header when it is a bearer token, whatever the scheme's case.
func bearerKey(r *http.Request) (string, bool) {
	scheme, value, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || value == "" {
		return "", false
	}
	return value, true
}

// keyBoundToPath reports whether key is bound to the bdb of r's path.
func keyBoundToPath(key authhttp.DataAPIKeyIdentity, r *http.Request) bool {
	bdbID, err := uuid.Parse(chi.URLParam(r, "bdbID"))
	return err == nil && key.BdbID == bdbID
}

// keyFromContext returns the Data API key ctx's request was authenticated with.
func keyFromContext(ctx context.Context) (authhttp.DataAPIKeyIdentity, bool) {
	key, ok := ctx.Value(keyContextKey{}).(authhttp.DataAPIKeyIdentity)
	return key, ok
}
