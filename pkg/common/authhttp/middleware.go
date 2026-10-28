// Package authhttp holds the HTTP authentication middleware the apps share. A TokenValidator authenticates a request
// by its bearer token or cookie, either as a JWT checked against the authorization server's JWKS or as an opaque token
// handed to a checker, and writes the identity the token carries into the request context. Two more middlewares check
// the internal service-to-service secret and that the identity's roles grant a permission.
package authhttp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

var tracer = otel.Tracer("baguettedb/auth")

const (
	jwksRefreshInterval = 5 * time.Minute
	jwksMaxRetries      = 5
	jwksInitialDelay    = 1 * time.Second
)

// errorBody is the JSON body of the errors the middleware answers with.
type errorBody struct {
	Error string `json:"error"`
}

// ErrInvalidToken reports a bearer token that authenticates no one. An OpaqueTokenChecker returns it for a token it
// refuses; any other error from a checker is a failure to check.
var ErrInvalidToken = errors.New("invalid token")

// OpaqueTokenChecker returns the user an opaque bearer token authenticates, with their roles: ErrInvalidToken for a
// token that authenticates no one, any other error when the check itself failed.
type OpaqueTokenChecker func(ctx context.Context, token string) (userID string, roles []authz.Role, err error)

// ValidatorOption configures a TokenValidator.
type ValidatorOption func(*TokenValidator)

// WithOpaqueTokens makes the validator pass each bearer token that starts with one of prefixes to check, instead of
// validating it as a JWT. An empty prefix matches no token.
func WithOpaqueTokens(check OpaqueTokenChecker, prefixes ...string) ValidatorOption {
	return func(tv *TokenValidator) {
		tv.checkOpaque = check
		tv.opaquePrefixes = prefixes
	}
}

// WithTokenHint makes the validator tell a client it refuses how to get a valid token, in hint: in the error of every
// 401's body, and in the error_description of the WWW-Authenticate challenge when a token was sent and refused.
// RFC 6750 keeps error details out of the challenge to a request that sent none. Characters a quoted header parameter
// can't hold are dropped from the challenge's copy.
func WithTokenHint(hint string) ValidatorOption {
	return func(tv *TokenValidator) {
		tv.hint = hint
	}
}

// WithCookieName makes the validator read the token a browser sends from the cookie name instead of DefaultCookieName,
// so instances sharing a host name keep separate sessions. Empty keeps the default.
func WithCookieName(name string) ValidatorOption {
	return func(tv *TokenValidator) {
		tv.cookieName = name
	}
}

// TokenValidator validates JWT access tokens using a cached JWKS, and opaque bearer tokens with a checker when one is
// set (WithOpaqueTokens).
type TokenValidator struct {
	jwksURL             string
	issuer              string
	resourceMetadataURL string
	logger              zerolog.Logger
	cache               jwk.Set
	mu                  sync.RWMutex
	// bgCancel cancels the background refresh ctx. Called from Close so the loop and any in-flight jwk.Fetch bail.
	bgCancel context.CancelFunc
	// wg waits for the background refresh goroutine to exit on Close.
	wg sync.WaitGroup

	// checkOpaque checks the bearer tokens that start with one of opaquePrefixes.
	// nil when the validator takes JWTs only.
	checkOpaque    OpaqueTokenChecker
	opaquePrefixes []string

	// opaqueOnly marks a validator with no JWKS, which refuses every token its checker doesn't take.
	opaqueOnly bool

	// hint tells a refused client how to get a valid token (WithTokenHint); empty adds nothing to a 401.
	hint string

	// cookieName is the cookie a browser's token is read from (WithCookieName); empty reads DefaultCookieName.
	cookieName string
}

// NewTokenValidator creates a token validator that fetches the JWKS (with retries and backoff) and starts a background
// refresh. The resourceMetadataURL is included in WWW-Authenticate headers on 401 responses per RFC 9728.
//
// The background refresh listens to a ctx derived from the one passed in: cancelling that ctx OR calling Close stops
// the refresh loop. Close also waits for the goroutine to exit, so any in-flight jwk.Fetch is drained before return.
func NewTokenValidator(
	ctx context.Context, jwksURL, issuer, resourceMetadataURL string, logger zerolog.Logger, opts ...ValidatorOption,
) (*TokenValidator, error) {
	bgCtx, bgCancel := context.WithCancel(ctx)
	tv := &TokenValidator{
		jwksURL:             jwksURL,
		issuer:              issuer,
		resourceMetadataURL: resourceMetadataURL,
		logger:              logger,
		bgCancel:            bgCancel,
	}
	// Applies each option, in order, before the first JWKS fetch: an option only sets fields of the validator.
	for _, opt := range opts {
		opt(tv)
	}

	var err error
	delay := jwksInitialDelay

	for attempt := 1; attempt <= jwksMaxRetries; attempt++ {
		if err = tv.refresh(ctx); err == nil {
			break
		}

		if attempt == jwksMaxRetries {
			bgCancel()
			return nil, fmt.Errorf("initial JWKS fetch after %d attempts: %w", jwksMaxRetries, err)
		}

		logger.Warn().
			Err(err).
			Int("attempt", attempt).
			Dur("retry_in", delay).
			Msg("JWKS fetch failed, retrying")

		select {
		case <-ctx.Done():
			bgCancel()
			return nil, fmt.Errorf("initial JWKS fetch cancelled: %w", ctx.Err())
		case <-time.After(delay):
		}

		delay *= 2
	}

	tv.wg.Go(func() { tv.backgroundRefresh(bgCtx) })

	return tv, nil
}

// NewOpaqueOnlyValidator creates a token validator with no JWKS, for an instance with no authorization server: it sends
// each bearer token that starts with one of prefixes to check, and refuses every other token. opts apply after, e.g.
// WithTokenHint.
func NewOpaqueOnlyValidator(
	logger zerolog.Logger, check OpaqueTokenChecker, prefixes []string, opts ...ValidatorOption,
) *TokenValidator {
	tv := &TokenValidator{logger: logger, bgCancel: func() {}, opaqueOnly: true}
	WithOpaqueTokens(check, prefixes...)(tv)
	for _, opt := range opts {
		opt(tv)
	}
	return tv
}

// refuseMissingToken answers 401 to a request that sent no bearer token. The challenge names where OAuth clients
// discover the authorization server, per RFC 9728, when the validator knows it, and carries no error, per RFC 6750.
func (tv *TokenValidator) refuseMissingToken(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", tv.challenge(false))
	httputil.RespondJSON(w, http.StatusUnauthorized,
		errorBody{Error: tv.withHint("missing or invalid authorization header")})
}

// refuseInvalidToken answers 401 to a request whose bearer token authenticates no one. With a hint, the challenge
// carries the invalid_token error and the hint as its description.
func (tv *TokenValidator) refuseInvalidToken(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", tv.challenge(true))
	httputil.RespondJSON(w, http.StatusUnauthorized, errorBody{Error: tv.withHint("invalid token")})
}

// challenge returns the WWW-Authenticate value of a 401: Bearer, with the resource metadata URL when the validator
// has one, and, for a refused token when the validator has a hint, the invalid_token error described by the hint.
func (tv *TokenValidator) challenge(tokenRefused bool) string {
	var params []string
	if tv.resourceMetadataURL != "" {
		params = append(params, fmt.Sprintf(`resource_metadata="%s"`, tv.resourceMetadataURL))
	}
	if tokenRefused && tv.hint != "" {
		params = append(params, `error="invalid_token"`, fmt.Sprintf(`error_description="%s"`, quotable(tv.hint)))
	}
	if len(params) == 0 {
		return "Bearer"
	}
	return "Bearer " + strings.Join(params, ", ")
}

// withHint returns msg followed by the validator's hint, or msg alone when it has none.
func (tv *TokenValidator) withHint(msg string) string {
	if tv.hint == "" {
		return msg
	}
	return msg + ": " + tv.hint
}

// quotable returns s without the characters RFC 6750's quoted parameters can't hold: double quotes, backslashes, and
// anything outside printable ASCII.
func quotable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, s)
}

// Close cancels the background JWKS refresh ctx and waits for the goroutine to exit. Idempotent.
func (tv *TokenValidator) Close() {
	tv.bgCancel()
	tv.wg.Wait()
}

// RequireToken returns a chi middleware that authenticates a request by its bearer token: an opaque token through the
// validator's checker when it starts with one of the validator's prefixes, any other as a JWT. A refused token is a
// 401, and a failure to check an opaque one a 500.
func (tv *TokenValidator) RequireToken() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), "auth.RequireToken")
			defer span.End()

			tokenStr := extractToken(r, cmp.Or(tv.cookieName, DefaultCookieName))
			if tokenStr == "" {
				tv.refuseMissingToken(w)
				return
			}

			if tv.checkOpaque != nil && hasOpaquePrefix(tokenStr, tv.opaquePrefixes) {
				userID, roles, err := tv.checkOpaque(ctx, tokenStr)
				switch {
				case errors.Is(err, ErrInvalidToken):
					tv.refuseInvalidToken(w)
					return
				case err != nil:
					tv.logger.Error().Err(err).Msg("checking an opaque token failed")
					httputil.RespondJSON(w, http.StatusInternalServerError,
						errorBody{Error: "internal server error"})
					return
				}
				span.SetAttributes(attribute.String("user.id", userID))
				next.ServeHTTP(w, r.WithContext(authz.ContextWithIdentity(ctx, userID, roles)))
				return
			}
			if tv.opaqueOnly {
				tv.refuseInvalidToken(w)
				return
			}

			token, err := tv.parseToken(ctx, tokenStr)
			if err != nil {
				tv.logger.Debug().Err(err).Msg("token validation failed")
				tv.refuseInvalidToken(w)
				return
			}

			sub := token.Subject()
			if sub == "" {
				httputil.RespondJSON(w, http.StatusUnauthorized, errorBody{Error: "token missing subject"})
				return
			}

			span.SetAttributes(attribute.String("user.id", sub))

			scopes := extractScopes(token)
			roles := extractRoles(token)

			ctx = authz.ContextWithScopedIdentity(ctx, sub, scopes, roles)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// hasOpaquePrefix reports whether token starts with one of prefixes, an empty prefix matching no token.
func hasOpaquePrefix(token string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(p string) bool {
		return p != "" && strings.HasPrefix(token, p)
	})
}

// ContextWithTokenIdentity writes the identity carried by token — subject, granted scopes, and roles — into ctx.
//
// The token must already be validated: its signature, issuer, and validity window are not checked here, and its
// claims are trusted as written.
func ContextWithTokenIdentity(ctx context.Context, token jwt.Token) context.Context {
	return authz.ContextWithScopedIdentity(ctx, token.Subject(), extractScopes(token), extractRoles(token))
}

// RequirePermission returns a middleware that refuses a request whose roles, read from the request context, are
// not granted perm by policy. It expects the identity a RequireToken middleware puts in the context.
func RequirePermission(policy authz.Policy, perm authz.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, span := tracer.Start(r.Context(), "auth.RequirePermission",
				trace.WithAttributes(attribute.String("permission", string(perm))),
			)
			defer span.End()

			roles := authz.RolesFromContext(ctx)
			if !policy.HasPermission(roles, perm) {
				httputil.RespondJSON(w, http.StatusForbidden, errorBody{Error: "insufficient permissions"})
				return
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DefaultCookieName is the cookie the web app's access token travels in, unless WithCookieName names another.
const DefaultCookieName = "bdb_access_token"

// extractToken pulls the access token from either the Authorization header (for MCP clients and external API consumers)
// or the cookie cookieName (for the SPA). The header takes precedence.
func extractToken(r *http.Request, cookieName string) string {
	if token := extractBearerToken(r); token != "" {
		return token
	}
	if cookie, err := r.Cookie(cookieName); err == nil {
		return cookie.Value
	}
	return ""
}

// extractBearerToken pulls the token from the Authorization header.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// extractScopes reads the "scp" claim (Hydra's default) as a string slice.
func extractScopes(token jwt.Token) []string {
	raw, ok := token.Get("scp")
	if !ok {
		return nil
	}

	switch v := raw.(type) {
	case []any:
		scopes := make([]string, 0, len(v))
		for _, s := range v {
			if str, ok := s.(string); ok {
				scopes = append(scopes, str)
			}
		}
		return scopes
	case []string:
		return v
	default:
		return nil
	}
}

// extractRoles reads the "ext.roles" claim from the JWT.
// Hydra places AccessToken session data under the "ext" claim in JWT access tokens.
func extractRoles(token jwt.Token) []authz.Role {
	raw, ok := token.Get("ext")
	if !ok {
		return nil
	}

	ext, ok := raw.(map[string]any)
	if !ok {
		return nil
	}

	rolesRaw, ok := ext["roles"]
	if !ok {
		return nil
	}

	switch v := rolesRaw.(type) {
	case []any:
		roles := make([]authz.Role, 0, len(v))
		for _, r := range v {
			if str, ok := r.(string); ok {
				roles = append(roles, authz.Role(str))
			}
		}
		return roles
	case []string:
		roles := make([]authz.Role, len(v))
		for i, s := range v {
			roles[i] = authz.Role(s)
		}
		return roles
	default:
		return nil
	}
}

// ValidateToken verifies a raw JWT — signature against the cached JWKS, issuer, and validity window — and
// returns the parsed token. The key set is refreshed once when the signing key is unknown (key rotation).
func (tv *TokenValidator) ValidateToken(ctx context.Context, tokenStr string) (jwt.Token, error) {
	return tv.parseToken(ctx, tokenStr)
}

// parseToken validates the token against the cached JWKS.
// If validation fails because the signing key is not in the cache (e.g. after key rotation),
// it refreshes the JWKS once and retries.
func (tv *TokenValidator) parseToken(ctx context.Context, tokenStr string) (jwt.Token, error) {
	tv.mu.RLock()
	keySet := tv.cache
	tv.mu.RUnlock()

	token, err := jwt.Parse([]byte(tokenStr),
		jwt.WithKeySet(keySet),
		jwt.WithIssuer(tv.issuer),
		jwt.WithValidate(true),
	)
	if err == nil {
		return token, nil
	}

	if !strings.Contains(err.Error(), "failed to find key") {
		return nil, err
	}

	tv.logger.Debug().Msg("key not found in JWKS cache, refreshing")
	if refreshErr := tv.refresh(ctx); refreshErr != nil {
		tv.logger.Error().Err(refreshErr).Msg("JWKS refresh after key miss failed")
		return nil, err
	}

	tv.mu.RLock()
	keySet = tv.cache
	tv.mu.RUnlock()

	return jwt.Parse([]byte(tokenStr),
		jwt.WithKeySet(keySet),
		jwt.WithIssuer(tv.issuer),
		jwt.WithValidate(true),
	)
}

// refresh fetches the JWKS from the configured URL.
func (tv *TokenValidator) refresh(ctx context.Context) error {
	set, err := jwk.Fetch(ctx, tv.jwksURL)
	if err != nil {
		return fmt.Errorf("fetching JWKS from %s: %w", tv.jwksURL, err)
	}

	tv.mu.Lock()
	tv.cache = set
	tv.mu.Unlock()

	tv.logger.Debug().Str("url", tv.jwksURL).Msg("JWKS refreshed")
	return nil
}

// backgroundRefresh periodically refreshes the JWKS cache. Exits when ctx is cancelled (either by the caller of
// NewTokenValidator or by Close). The per-refresh ctx is derived from this one so an in-flight jwk.Fetch also bails on
// cancellation.
func (tv *TokenValidator) backgroundRefresh(ctx context.Context) {
	ticker := time.NewTicker(jwksRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			tv.logger.Debug().Msg("JWKS background refresh stopped")
			return
		case <-ticker.C:
			refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := tv.refresh(refreshCtx); err != nil {
				tv.logger.Error().Err(err).Msg("failed to refresh JWKS")
			}
			cancel()
		}
	}
}
