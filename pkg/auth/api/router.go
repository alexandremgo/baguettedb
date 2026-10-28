package api

import (
	"bytes"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// staticFile returns a handler that serves a single embedded file.
func staticFile(name string) http.HandlerFunc {
	data, err := staticFS.ReadFile(name)
	if err != nil {
		panic("missing embedded file: " + name)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	}
}

// staticFileImmutable serves a single embedded file with an aggressive cache policy, for assets that only change
// with a binary release (fonts).
func staticFileImmutable(name string) http.HandlerFunc {
	serve := staticFile(name)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
		serve(w, r)
	}
}

// NewRouter creates a chi router with the auth API routes mounted: the profile and admin routes behind requireToken,
// which rejects an unauthenticated request or puts the caller's identity in the request context, and the internal
// routes behind the shared internal API secret. The admin routes also need a permission that policy grants to the
// caller's roles. The OAuth login flow is mounted separately by RegisterOAuthRoutes.
func NewRouter(
	handler *Handler,
	internalHandler *InternalHandler,
	requireToken func(http.Handler) http.Handler,
	policy authz.Policy,
	internalAPISecret string,
) chi.Router {
	r := chi.NewRouter()

	r.Get("/api/auth/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "auth"})
	})

	// API fallback — shown when a user navigates to the API root in a browser.
	r.Get("/", handler.GetAPIFallback)

	// Favicons — served from embedded static assets at the domain root.
	r.Get("/favicon.ico", staticFile("static/favicon.ico"))
	r.Get("/favicon-16x16.png", staticFile("static/favicon-16x16.png"))
	r.Get("/favicon-32x32.png", staticFile("static/favicon-32x32.png"))
	r.Get("/favicon-48x48.png", staticFile("static/favicon-48x48.png"))
	r.Get("/favicon-180x180.png", staticFile("static/favicon-180x180.png"))

	// Protected API routes.
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Get("/api/auth/v1/me", handler.Me)
		r.Post("/api/auth/v1/password", handler.ChangePassword)
	})

	// Admin: user management — requires manage:users permission.
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Use(authhttp.RequirePermission(policy, authz.PermManageUsers))
		r.Post("/api/auth/v1/admin/users", handler.AdminCreateUser)
		r.Get("/api/auth/v1/admin/users", handler.ListUsers)
		r.Get("/api/auth/v1/admin/users/{userID}/roles", handler.GetUserRoles)
	})

	// Admin: role management — requires manage:roles permission.
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Use(authhttp.RequirePermission(policy, authz.PermManageRoles))
		r.Post("/api/auth/v1/admin/users/{userID}/roles", handler.AssignRole)
		r.Delete("/api/auth/v1/admin/users/{userID}/roles/{role}", handler.RemoveRole)
	})

	// Internal service-to-service routes — no JWT, but callers must present the shared internal API secret.
	r.Group(func(r chi.Router) {
		r.Use(authhttp.RequireInternalSecret(internalAPISecret))
		r.Post("/internal/api/auth/v1/user-roles", internalHandler.GetUserRoles)
		r.Post("/internal/api/auth/v1/user-profiles", internalHandler.GetUserProfiles)
	})

	return r
}

// RegisterOAuthRoutes mounts the OAuth login flow on r: the login and consent pages Hydra delegates to, the OAuth
// discovery documents, the proxied Hydra endpoints and their compatibility aliases, and the SPA's cookie token
// endpoints. oidcLogin and cookieToken are optional — when nil, their routes are not mounted (the login page then
// renders no external-provider buttons).
func RegisterOAuthRoutes(
	r chi.Router,
	handler *Handler,
	discovery *DiscoveryHandler,
	oidcProxy *OIDCProxyHandler,
	oauth2Proxy *OAuth2ProxyHandler,
	cookieToken *CookieTokenHandler,
	oidcLogin *OIDCLoginHandler,
) {
	// Display font for the server-rendered pages, embedded so they need nothing from the SPA bundle.
	r.Get("/static/fonts/schibsted-grotesk.woff2", staticFileImmutable("static/schibsted-grotesk.woff2"))

	// Public routes — login/consent provider for Hydra.
	r.Get("/login", handler.GetLogin)
	r.Post("/login", handler.PostLogin)
	r.Get("/consent", handler.GetConsent)
	r.Post("/consent", handler.PostConsent)

	// External identity provider sign-in routes — mounted only when providers are registered. The stylesheet route
	// serves the CSS of each provider's login button; the button HTML itself is inlined into the login page.
	if oidcLogin != nil {
		r.Get("/login/oidc/{provider}/start", oidcLogin.Start)
		r.Get("/login/oidc/{provider}/callback", oidcLogin.Callback)
		r.Get("/static/oidc/{provider}.css", oidcLogin.ServeStylesheet)
	}

	// OAuth discovery and dynamic client registration endpoints.
	r.Get("/.well-known/oauth-protected-resource", discovery.ServeHTTP)
	r.Get("/.well-known/oauth-protected-resource/*", discovery.ServeHTTP)
	r.Get("/.well-known/openid-configuration", oidcProxy.ServeDiscovery)
	r.Get("/.well-known/oauth-authorization-server", oidcProxy.ServeDiscovery)
	r.Post("/oauth2/register", oidcProxy.ServeRegister)

	// OAuth2 public endpoints — proxied to Hydra so it stays fully internal.
	r.Get("/oauth2/auth", oauth2Proxy.ServeHTTP)
	r.Post("/oauth2/token", oauth2Proxy.ServeHTTP)
	r.Post("/oauth2/revoke", oauth2Proxy.ServeHTTP)
	r.Get("/oauth2/sessions/logout", oauth2Proxy.ServeHTTP)
	r.Post("/oauth2/sessions/logout", oauth2Proxy.ServeHTTP)
	r.Get("/.well-known/jwks.json", oauth2Proxy.ServeHTTP)
	r.Get("/userinfo", oauth2Proxy.ServeHTTP)
	r.Post("/userinfo", oauth2Proxy.ServeHTTP)

	// Compatibility aliases — some MCP clients (e.g. Claude.ai) ignore the OAuth
	// metadata endpoints and hardcode /authorize, /token, /register at the base URL.
	// See https://github.com/anthropics/claude-ai-mcp/issues/82
	r.Get("/authorize", oauth2Proxy.ServeWithPathRewrite("/oauth2/auth"))
	r.Post("/token", oauth2Proxy.ServeWithPathRewrite("/oauth2/token"))
	r.Post("/register", oidcProxy.ServeRegister)

	// Cookie-based token endpoints for the SPA (tokens stored in httpOnly cookies).
	if cookieToken != nil {
		r.Post("/api/auth/v1/token", cookieToken.ExchangeToken)
		r.Post("/api/auth/v1/token/refresh", cookieToken.RefreshToken)
		r.Post("/api/auth/v1/logout", cookieToken.Logout)
	}
}

// RegisterTokenRoutes mounts the personal access token routes on r, behind requireToken.
func RegisterTokenRoutes(r chi.Router, handler *TokenHandler, requireToken func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Get("/api/auth/v1/personal-tokens", handler.List)
		r.Post("/api/auth/v1/personal-tokens", handler.Create)
		r.Delete("/api/auth/v1/personal-tokens/{tokenID}", handler.Revoke)
	})
}

// RegisterBrowserSessionRoutes mounts on r the routes signing browsers in and out of an instance with no OAuth flow:
// signing in with an email and a password, and logging out. They take no authentication, since each is entitled by
// the credential its request carries.
func RegisterBrowserSessionRoutes(r chi.Router, handler *BrowserSessionHandler) {
	r.Post("/api/auth/v1/session", handler.SignIn)
	r.Post("/api/auth/v1/logout", handler.Logout)
}
