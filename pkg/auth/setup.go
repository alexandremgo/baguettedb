// Package auth wires the auth app: user accounts and their global roles, personal access tokens and sessions, and
// sign-in through the OAuth2 login and consent flow the Hydra authorization server delegates to it, external identity
// providers included. Setup mounts the app in front of Hydra, SetupWithoutOAuth without it; both open the repositories
// on the instance store the config selects, Postgres or SQLite, and take the extension points of SetupOptions.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/adapters/hydrahttp"
	authapi "github.com/alexandremgo/baguettedb/pkg/auth/api"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	authrunner "github.com/alexandremgo/baguettedb/pkg/auth/runner"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
)

// oidcStatePurgeInterval is how often expired sign-in flow rows are purged. Rows are normally consumed by the
// callback; the purger only collects flows the user abandoned at the IdP, so the interval is not latency-sensitive.
const oidcStatePurgeInterval = 5 * time.Minute

// ExtDeps holds dependencies made available to extension routes registered via
// SetupOptions.ExtraRoutes. Extensions can use these to build authenticated
// routes and transaction-scoped adapters against the auth database.
type ExtDeps struct {
	// RequireToken is the middleware guarding the app's protected routes: SetupOptions.RequireToken when
	// injected, otherwise TokenValidator's.
	RequireToken func(http.Handler) http.Handler
	// TokenValidator validates the JWTs Hydra issues. Nil when SetupOptions.RequireToken was injected.
	TokenValidator *authhttp.TokenValidator
	// Pool is the auth database's Postgres pool. Nil on the SQLite instance store.
	Pool *pgxpool.Pool
	// NewCompletionAPI constructs a CompletionAPI bound to the given login_challenge. Lets the asynchronous
	// signup-completion path (the POST handler that runs after the interstitial form submit) reconstruct a
	// completion handle without re-importing the transaction-manager and Hydra adapter internals. Nil under
	// SetupWithoutOAuth, which has no login flow.
	NewCompletionAPI func(challenge string) authapi.CompletionAPI
}

// SetupOptions configures optional behaviors of the auth module setup.
type SetupOptions struct {
	// RequireToken authenticates the app's protected routes: it rejects an unauthenticated request or puts the
	// caller's identity in the request context. When nil, Setup validates the JWTs Hydra issues against its JWKS, and
	// SetupWithoutOAuth accepts the app's personal access tokens alone, which takes PersonalAccessTokens on.
	RequireToken func(http.Handler) http.Handler

	// Policy maps the global roles the app recognizes to the permissions they grant. The zero value is the built-in
	// policy; a distribution extends it with authz.Policy.With.
	Policy authz.Policy

	// ExtraRoutes registers additional routes on the auth router after all
	// built-in routes are mounted. Receives the router and shared dependencies
	// so extensions can mount both public and authenticated routes.
	ExtraRoutes func(r chi.Router, deps ExtDeps)

	// UserResponseEnricher adds extra fields to the /api/auth/v1/me response.
	// If nil, the response contains only the standard user fields.
	UserResponseEnricher authapi.UserResponseEnricher

	// OIDCProviders is the list of external identity providers that users can sign in with. When non-empty, the auth
	// router mounts /login/oidc/{provider}/{start,callback} routes. Ignored by SetupWithoutOAuth.
	//
	// A provider also implementing DisplayName() string, ButtonTemplate() *template.Template and Stylesheet() []byte
	// gets a button on the login page, its template executing with [authapi.ButtonData]; any other is reachable through
	// its start route only.
	OIDCProviders []service.OIDCProvider

	// SignupInterstitial is the optional extension point invoked when an OIDC callback resolves to no local user.
	// When nil, the auth handler renders a generic "sign-up unavailable" page on that branch. A registered handler
	// can run any UI / validation flow (invite-code form, terms-of-service prompt, …) and ultimately call
	// CompletionAPI.CompleteSignup to materialize the user inside a single database transaction. Ignored by
	// SetupWithoutOAuth.
	SignupInterstitial authapi.SignupInterstitialHandler

	// PageFooterLinks are links the server-rendered auth pages show in their footer next to the copyright notice —
	// typically a deployment's privacy policy and terms of service. Empty renders the copyright notice alone.
	// Ignored by SetupWithoutOAuth, which renders no pages.
	PageFooterLinks []authapi.FooterLink

	// PersonalAccessTokens makes the app accept its personal access tokens as bearer tokens, and mounts the routes
	// managing them. Off, a personal access token authenticates no one and the routes are not mounted.
	//
	// With RequireToken injected, that middleware alone decides which tokens authenticate; only the routes follow.
	PersonalAccessTokens bool

	// SessionCookieName is the cookie a browser's session travels in, so instances sharing a host name keep separate
	// sessions: cookies ignore ports. Used by SetupWithoutOAuth when it builds the middleware itself, from RequireToken
	// left nil; empty keeps authhttp.DefaultCookieName.
	SessionCookieName string

	// TokenHint tells a client the app refuses how to get a personal access token, on its 401s
	// (authhttp.WithTokenHint). Used by SetupWithoutOAuth when it builds the middleware itself, from RequireToken
	// left nil; empty adds nothing.
	TokenHint string
}

// SetupResult holds the outputs of the auth module setup.
type SetupResult struct {
	Router chi.Router

	// RequireToken is the middleware guarding the app's protected routes: SetupOptions.RequireToken when injected,
	// otherwise the one the setup built.
	RequireToken func(http.Handler) http.Handler

	RoleSvc *service.RoleService
	UserSvc *service.UserService
	// TokenSvc manages personal access tokens and checks bearer ones.
	TokenSvc *service.TokenService
	// OpaqueTokenChecker checks a personal access token for an auth middleware (authhttp.WithOpaqueTokens,
	// authhttp.NewOpaqueOnlyValidator), and OpaqueTokenPrefixes lists the prefixes of the tokens it takes. Both are
	// nil when SetupOptions.PersonalAccessTokens is off.
	OpaqueTokenChecker  authhttp.OpaqueTokenChecker
	OpaqueTokenPrefixes []string
	// Pool exposes the underlying connection pool so extensions can build
	// transaction-scoped adapters against the same database. Nil on the SQLite instance store.
	Pool *pgxpool.Pool
	// OwnerSvc resolves the account owning a local instance.
	OwnerSvc *service.OwnerService
	Cleanup  func()
	// WebClientID is the OAuth2 client id the web frontend authenticates with: the configured one, or the
	// first-party client the app registered at startup. Empty under SetupWithoutOAuth.
	WebClientID string
	// WebRedirectURI is the redirect URI registered for that client, derived from the web frontend's base URL.
	// Empty under SetupWithoutOAuth.
	WebRedirectURI string
}

// core holds what every auth setup builds: the store, the services, the handler and the router carrying the auth API
// routes. personalTokens reports that personal access tokens are on.
type core struct {
	store          *store
	userSvc        *service.UserService
	roleSvc        *service.RoleService
	handler        *authapi.Handler
	router         chi.Router
	requireToken   func(http.Handler) http.Handler
	personalTokens bool
}

// setupCore builds the services over st, the handler and the router carrying the auth API routes behind requireToken,
// the personal access token routes included when opts turns them on.
//
// loginFlow and webClientID back the handler's login and consent pages; they are nil and empty when those pages are
// not mounted.
func setupCore(
	cfg config.Config, logger zerolog.Logger, opts SetupOptions, st *store,
	requireToken func(http.Handler) http.Handler, loginFlow *service.LoginFlowService, webClientID string,
) *core {
	authenticationSvc := service.NewAuthenticationService(st.users)
	userSvc := service.NewUserService(st.users, st.userTx, logger)
	roleSvc := service.NewRoleService(st.roles, opts.Policy)

	handler := authapi.NewHandler(authenticationSvc, userSvc, roleSvc, loginFlow, webClientID, cfg.PublicURL, logger)
	if opts.UserResponseEnricher != nil {
		handler.SetUserResponseEnricher(opts.UserResponseEnricher)
	}
	internalHandler := authapi.NewInternalHandler(roleSvc, userSvc)
	router := authapi.NewRouter(handler, internalHandler, requireToken, opts.Policy, cfg.InternalAPISecret)
	if opts.PersonalAccessTokens {
		authapi.RegisterTokenRoutes(router, authapi.NewTokenHandler(st.tokenSvc, logger), requireToken)
	}

	return &core{
		store:          st,
		userSvc:        userSvc,
		roleSvc:        roleSvc,
		handler:        handler,
		router:         router,
		requireToken:   requireToken,
		personalTokens: opts.PersonalAccessTokens,
	}
}

// opaqueTokenPrefixes lists the prefixes of the opaque token kinds a request may carry as its bearer token: personal
// tokens and sessions.
var opaqueTokenPrefixes = []string{domain.AccessTokenPersonal.Prefix(), domain.AccessTokenSession.Prefix()}

// opaqueTokenChecker adapts the token service's check to an auth middleware's: a token the service refuses is
// authhttp.ErrInvalidToken, and the user is given by their ID's string form.
func opaqueTokenChecker(tokens *service.TokenService) authhttp.OpaqueTokenChecker {
	return func(ctx context.Context, token string) (string, []authz.Role, error) {
		userID, roles, err := tokens.Check(ctx, token)
		if errors.Is(err, domain.ErrInvalidAccessToken) {
			return "", nil, authhttp.ErrInvalidToken
		}
		if err != nil {
			return "", nil, err
		}
		return userID.String(), roles, nil
	}
}

// result assembles the setup result around c, with cleanup releasing everything the setup acquired.
func (c *core) result(cleanup func()) *SetupResult {
	res := &SetupResult{
		Router:       c.router,
		RequireToken: c.requireToken,
		RoleSvc:      c.roleSvc,
		UserSvc:      c.userSvc,
		TokenSvc:     c.store.tokenSvc,
		Pool:         c.store.pool,
		OwnerSvc:     service.NewOwnerService(c.store.ownerTx),
		Cleanup:      cleanup,
	}
	if c.personalTokens {
		res.OpaqueTokenChecker = opaqueTokenChecker(c.store.tokenSvc)
		res.OpaqueTokenPrefixes = opaqueTokenPrefixes
	}
	return res
}

// Setup sets up the auth app with its OAuth login flow in front of Hydra: provisions the web frontend's OAuth2
// client, runs migrations, creates all adapters, services and handlers, and mounts the API routes, the login and
// consent pages, the discovery documents and the proxied Hydra endpoints.
func Setup(ctx context.Context, cfg config.Config, logger zerolog.Logger, opts SetupOptions) (*SetupResult, error) {
	hydraAdmin := hydrahttp.NewAdminClient(cfg.HydraAdminURL)

	// Resolves the web frontend's OAuth2 client: an explicit id is used as-is, otherwise the module registers (or
	// tops up) its own first-party client so a fresh instance needs no manual client setup.
	webRedirectURI := strings.TrimRight(cfg.AppBaseURL, "/") + "/callback"
	webClientID := cfg.WebClientID
	if webClientID == "" {
		var err error
		webClientID, err = service.EnsureWebClient(ctx, hydraAdmin, webRedirectURI, logger)
		if err != nil {
			return nil, fmt.Errorf("auth: provisioning the web client: %w", err)
		}
	}

	st, err := openStore(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}

	// The JWKS is fetched via the internal URL; the issuer must match what Hydra stamps in tokens. The app's own
	// personal access tokens are accepted alongside when they are on, checked against its store.
	var tokenValidator *authhttp.TokenValidator
	requireToken := opts.RequireToken
	if requireToken == nil {
		var validatorOpts []authhttp.ValidatorOption
		if opts.PersonalAccessTokens {
			validatorOpts = append(validatorOpts,
				authhttp.WithOpaqueTokens(opaqueTokenChecker(st.tokenSvc), opaqueTokenPrefixes...))
		}
		jwksURL := cfg.HydraPublicURL + "/.well-known/jwks.json"
		resourceMetadataURL := cfg.PublicURL + "/.well-known/oauth-protected-resource"
		tokenValidator, err = authhttp.NewTokenValidator(
			ctx, jwksURL, cfg.HydraIssuerURL, resourceMetadataURL, logger, validatorOpts...,
		)
		if err != nil {
			st.close()
			return nil, fmt.Errorf("auth: creating token validator: %w", err)
		}
		requireToken = tokenValidator.RequireToken()
	}
	closeTokenValidator := func() {
		if tokenValidator != nil {
			tokenValidator.Close()
		}
	}

	loginFlow := service.NewLoginFlowService(hydraAdmin, st.roles)
	c := setupCore(cfg, logger, opts, st, requireToken, loginFlow, webClientID)

	if len(opts.OIDCProviders) > 0 {
		c.handler.SetOIDCProviders(opts.OIDCProviders)
	}
	if len(opts.PageFooterLinks) > 0 {
		c.handler.SetFooterLinks(opts.PageFooterLinks)
	}

	// Creates Hydra public API client (shared by cookie token handler and OIDC proxy).
	hydraPublic := hydrahttp.NewPublicClient(cfg.HydraPublicURL)
	discovery := authapi.NewDiscoveryHandler(cfg.PublicURL, cfg.HydraIssuerURL, logger)
	oidcProxy := authapi.NewOIDCProxyHandler(hydraPublic, cfg.PublicURL, logger)
	oauth2Proxy := authapi.NewOAuth2ProxyHandler(cfg.HydraPublicURL, logger)
	secureCookies := cfg.SecureCookies != nil && *cfg.SecureCookies
	cookieToken := authapi.NewCookieTokenHandler(hydraPublic, webClientID, secureCookies, logger)

	// Wires the OIDC login handler when at least one provider is registered. A nil handler makes
	// RegisterOAuthRoutes skip mounting the routes. The flow-state purge runner only runs alongside the handler,
	// since nothing else writes to the table. It uses a Setup-internal cancellable ctx so cleanup() can both signal
	// shutdown AND wait for an in-flight sweep to drain before the postgres pool below is closed.
	var oidcLogin *authapi.OIDCLoginHandler
	stopOIDCStatePurger := func() {}
	if len(opts.OIDCProviders) > 0 {
		extIdentitySvc := service.NewExternalIdentityService(c.store.identityTx)
		oidcLoginSvc := service.NewOIDCLoginService(extIdentitySvc, c.store.oidcStates)
		oidcLogin = authapi.NewOIDCLoginHandler(
			opts.OIDCProviders, oidcLoginSvc, loginFlow, opts.SignupInterstitial, c.handler, logger,
		)

		purger := authrunner.NewOIDCStatePurger(oidcLoginSvc, oidcStatePurgeInterval, logger)
		purgerCtx, cancelPurger := context.WithCancel(ctx)
		var purgerWG sync.WaitGroup
		purgerWG.Go(func() {
			purger.Run(purgerCtx)
		})
		stopOIDCStatePurger = func() {
			cancelPurger()
			purgerWG.Wait()
		}
	}

	authapi.RegisterOAuthRoutes(c.router, c.handler, discovery, oidcProxy, oauth2Proxy, cookieToken, oidcLogin)

	// Registers extension-provided extra routes if any.
	if opts.ExtraRoutes != nil {
		opts.ExtraRoutes(c.router, ExtDeps{
			RequireToken:   requireToken,
			TokenValidator: tokenValidator,
			Pool:           c.store.pool,
			NewCompletionAPI: func(challenge string) authapi.CompletionAPI {
				return authapi.NewCompletionAPI(loginFlow, challenge)
			},
		})
	}

	res := c.result(func() {
		stopOIDCStatePurger()
		closeTokenValidator()
		c.store.close()
	})
	res.WebClientID = webClientID
	res.WebRedirectURI = webRedirectURI
	return res, nil
}

// SetupWithoutOAuth sets up the auth app without its OAuth login flow, so it needs no Hydra: it runs migrations,
// creates the adapters and services, and mounts the API routes only — no login or consent page, no discovery
// document, no /oauth2 endpoint.
//
// A request gets an identity from opts.RequireToken or, when it is nil, from one of the app's personal access tokens
// or sessions, as its bearer token or in the session cookie; one of the two is required, so a nil RequireToken takes
// opts.PersonalAccessTokens on.
// Tokens alone also mount the routes signing browsers in and out: signing in with an email and a password, and
// logging out.
func SetupWithoutOAuth(
	ctx context.Context, cfg config.Config, logger zerolog.Logger, opts SetupOptions,
) (*SetupResult, error) {
	if opts.RequireToken == nil && !opts.PersonalAccessTokens {
		return nil, errors.New("auth: SetupWithoutOAuth requires SetupOptions.RequireToken or PersonalAccessTokens")
	}

	st, err := openStore(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	requireToken := opts.RequireToken
	if requireToken == nil {
		requireToken = authhttp.NewOpaqueOnlyValidator(
			logger, opaqueTokenChecker(st.tokenSvc), opaqueTokenPrefixes,
			authhttp.WithTokenHint(opts.TokenHint), authhttp.WithCookieName(opts.SessionCookieName),
		).RequireToken()
	}
	c := setupCore(cfg, logger, opts, st, requireToken, nil, "")
	if opts.RequireToken == nil {
		secureCookies := cfg.SecureCookies != nil && *cfg.SecureCookies
		authapi.RegisterBrowserSessionRoutes(c.router,
			authapi.NewBrowserSessionHandler(st.tokenSvc, opts.SessionCookieName, secureCookies, logger))
	}

	// Registers extension-provided extra routes if any.
	if opts.ExtraRoutes != nil {
		opts.ExtraRoutes(c.router, ExtDeps{RequireToken: requireToken, Pool: c.store.pool})
	}

	return c.result(c.store.close), nil
}
