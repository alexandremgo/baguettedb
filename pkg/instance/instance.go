// Package instance composes every app of a BaguetteDB instance in-process: auth, bdbs, databases, starters, the
// MCP gateway and the web frontend, mounted on a single router.
package instance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	authmodule "github.com/alexandremgo/baguettedb/pkg/auth"
	bdbsmodule "github.com/alexandremgo/baguettedb/pkg/bdbs"
	bdbsintra "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/intraprocess"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	dbmodule "github.com/alexandremgo/baguettedb/pkg/databases"
	"github.com/alexandremgo/baguettedb/pkg/mcpgateway"
	"github.com/alexandremgo/baguettedb/pkg/starters"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessbdbs"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessdatabases"
	startersservice "github.com/alexandremgo/baguettedb/pkg/starters/service"
	"github.com/alexandremgo/baguettedb/pkg/webui"
)

// Options configures an instance beyond what the config carries.
type Options struct {
	// Middlewares wrap every route of the instance router, outermost first.
	Middlewares []func(http.Handler) http.Handler

	// Auth, Bdbs and Databases are handed to the matching app setup unchanged, so each app's extension points
	// stay reachable from any entrypoint.
	Auth      authmodule.SetupOptions
	Bdbs      bdbsmodule.SetupOptions
	Databases dbmodule.SetupOptions

	// Authentication says how every app's protected routes and the /mcp endpoint authenticate requests;
	// nil is OAuth without personal access tokens.
	// New sets the RequireToken of Auth, Bdbs and Databases and Auth.PersonalAccessTokens from it, replacing
	// what they held.
	Authentication Authentication

	// Policy maps the global roles the instance recognizes to the permissions they grant; the zero value is the
	// built-in policy. New sets the Policy of Auth and Databases from it, replacing what they held, so every app
	// checks permissions against the same roles.
	Policy authz.Policy

	// ImplicitOwner makes New ensure the local owner account exists, in place of the admin bootstrap, and record it
	// in Instance.Owner: the account the instance records as its owner, or one created with OwnerEmail on first use.
	ImplicitOwner bool

	// Gateway sets how the MCP gateway presents itself to clients.
	Gateway mcpgateway.Options

	// GatewayServerOptions are applied to the MCP gateway's aggregated server after its own, so a caller can add
	// middleware or hooks around every tool call, whichever transport serves it.
	GatewayServerOptions []mcpserver.ServerOption
}

// Authentication is how an instance authenticates requests. OAuth, PersonalTokensOnly and InjectedAuth make one.
//
// Its method is unexported, so no other package can implement it: those constructors are the only values, and a
// combination New can't honour can't be written.
type Authentication interface {
	authentication()
}

// oauthAuth checks JWTs issued by the authorization server, and the auth app's personal access tokens alongside when
// personalTokens is set.
type oauthAuth struct {
	personalTokens bool
}

// personalTokensOnlyAuth checks the auth app's personal access tokens and browser sessions alone.
type personalTokensOnlyAuth struct{}

// injectedAuth guards every protected route and the /mcp endpoint with requireToken.
type injectedAuth struct {
	requireToken func(http.Handler) http.Handler
}

func (oauthAuth) authentication()              {}
func (personalTokensOnlyAuth) authentication() {}
func (injectedAuth) authentication()           {}

// OAuth authenticates requests with JWTs issued by the authorization server, checked against its JWKS. With
// personalTokens, the auth app's personal access tokens are accepted alongside and its routes managing them mounted.
func OAuth(personalTokens bool) Authentication {
	return oauthAuth{personalTokens: personalTokens}
}

// PersonalTokensOnly authenticates requests with the auth app's personal access tokens alone, sent as bearer tokens or
// held in a browser's session cookie: the auth app is set up without its OAuth flow and no JWKS is fetched.
func PersonalTokensOnly() Authentication {
	return personalTokensOnlyAuth{}
}

// InjectedAuth guards every app's protected routes and the /mcp endpoint with requireToken: the auth app is set up
// without its OAuth flow and no JWKS is fetched. New refuses a nil requireToken.
func InjectedAuth(requireToken func(http.Handler) http.Handler) Authentication {
	return injectedAuth{requireToken: requireToken}
}

// personalTokensOn reports whether an instance authenticating with a accepts the auth app's personal access tokens:
// as asked under OAuth, always with personal tokens alone, never with an injected middleware or a nil Authentication.
func personalTokensOn(a Authentication) bool {
	switch a := a.(type) {
	case oauthAuth:
		return a.personalTokens
	case personalTokensOnlyAuth:
		return true
	case injectedAuth, nil:
		return false
	default:
		return false
	}
}

// Identity Options.ImplicitOwner gives the local owner account when it creates it, which the owner may change since.
const (
	OwnerEmail     = "baker@bdb.localhost"
	OwnerFirstName = "Baker"
)

// Owner identifies the implicit local owner account, with its email as it stands.
type Owner struct {
	ID    uuid.UUID
	Email string
	Roles []authz.Role
}

// Instance is a running composition of every app. Close releases what New acquired.
type Instance struct {
	// Router serves every public route: the apps' APIs, the authenticated /mcp endpoint, the web frontend's
	// configuration document and, when its bundle is compiled in, the web frontend itself. Internal
	// service-to-service routes are left out, since the apps talk in-process. Routes added to it after New are
	// wrapped by Options.Middlewares like the others.
	Router *chi.Mux

	// WebAppEmbedded reports that the web frontend's bundle is compiled in, so Router serves the web app.
	WebAppEmbedded bool

	Auth      *authmodule.SetupResult
	Bdbs      *bdbsmodule.SetupResult
	Databases *dbmodule.SetupResult
	Starters  *startersservice.StarterService
	Gateway   *mcpgateway.SetupResult

	// Owner is the implicit local owner account; nil unless Options.ImplicitOwner is set.
	Owner *Owner

	cleanups []func()
}

// New sets up every app against cfg, wires them to each other in-process and mounts their routes on one router.
// ctx bounds the setup and the lifetime of the background runners the apps start.
//
// On error, everything acquired so far is released before returning.
func New(ctx context.Context, cfg config.Config, logger zerolog.Logger, opts Options) (_ *Instance, err error) {
	inst := &Instance{}
	defer func() {
		if err != nil {
			inst.Close()
		}
	}()

	injected, isInjected := opts.Authentication.(injectedAuth)
	_, tokensOnly := opts.Authentication.(personalTokensOnlyAuth)
	if isInjected && injected.requireToken == nil {
		return nil, errors.New("instance: InjectedAuth needs a middleware, got nil")
	}

	// The auth app turns personal access tokens on when the authentication accepts them, and the validators built
	// below for the others accept them then.
	personalTokens := personalTokensOn(opts.Authentication)
	opts.Auth.PersonalAccessTokens = personalTokens
	opts.Auth.Policy = opts.Policy
	opts.Databases.Policy = opts.Policy
	opts.Auth.RequireToken = nil
	setupAuth := authmodule.Setup
	if isInjected {
		opts.Auth.RequireToken = injected.requireToken
		setupAuth = authmodule.SetupWithoutOAuth
	}
	if tokensOnly {
		setupAuth = authmodule.SetupWithoutOAuth
	}

	// Auth comes first: bdbs resolves users and roles through it.
	inst.Auth, err = setupAuth(ctx, cfg, logger, opts.Auth)
	if err != nil {
		return nil, fmt.Errorf("setting up auth app: %w", err)
	}
	inst.cleanups = append(inst.cleanups, inst.Auth.Cleanup)

	// Without OAuth, the middleware guarding auth's routes guards every app's: the injected one, or the one
	// accepting personal access tokens alone that auth built.
	var requireToken func(http.Handler) http.Handler
	switch {
	case isInjected:
		requireToken = injected.requireToken
	case tokensOnly:
		requireToken = inst.Auth.RequireToken
	}
	opts.Bdbs.RequireToken = requireToken
	opts.Databases.RequireToken = requireToken

	// With personal tokens on, every JWT validator built below accepts them too, checked in-process.
	var validatorOpts []authhttp.ValidatorOption
	if personalTokens {
		validatorOpts = append(validatorOpts,
			authhttp.WithOpaqueTokens(inst.Auth.OpaqueTokenChecker, inst.Auth.OpaqueTokenPrefixes...))
	}
	opts.Bdbs.TokenValidatorOptions = append(opts.Bdbs.TokenValidatorOptions, validatorOpts...)
	opts.Databases.TokenValidatorOptions = append(opts.Databases.TokenValidatorOptions, validatorOpts...)

	authApp := bdbsintra.NewAuthApp(inst.Auth.RoleSvc, inst.Auth.UserSvc)
	inst.Bdbs, err = bdbsmodule.Setup(ctx, cfg, logger, authApp, opts.Bdbs)
	if err != nil {
		return nil, fmt.Errorf("setting up bdbs app: %w", err)
	}
	inst.cleanups = append(inst.cleanups, inst.Bdbs.Cleanup)

	// Ensures the implicit owner, or bootstraps the first admin account (a no-op once any user exists).
	if opts.ImplicitOwner {
		owner, roles, err := inst.Auth.OwnerSvc.EnsureOwner(ctx, OwnerEmail, OwnerFirstName)
		if err != nil {
			return nil, fmt.Errorf("ensuring the local owner account: %w", err)
		}
		inst.Owner = &Owner{ID: owner.ID, Email: owner.Email, Roles: roles}
	} else if err := inst.Auth.UserSvc.BootstrapAdmin(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
		return nil, fmt.Errorf("bootstrapping admin: %w", err)
	}

	inst.Databases, err = dbmodule.Setup(ctx, cfg, logger, inst.Bdbs.AuthChecker, opts.Databases)
	if err != nil {
		return nil, fmt.Errorf("setting up databases app: %w", err)
	}
	inst.cleanups = append(inst.cleanups, inst.Databases.Cleanup)

	// Wires databases into bdbs for schema enrichment in list_bdbs (this also starts the purge runner).
	inst.Bdbs.SetDatabasesApp(bdbsintra.NewDatabasesApp(
		inst.Databases.QuerySvc, inst.Databases.SkillSvc, inst.Databases.FormulaSvc, inst.Databases.DatabaseSvc))

	if err := inst.setupStarters(cfg, logger); err != nil {
		return nil, err
	}

	// Built after the starters tools are registered: the gateway reads each backend's tool list once, at
	// construction.
	inst.Gateway, err = mcpgateway.SetupInProcess(
		[]*mcpserver.MCPServer{inst.Bdbs.MCPServer, inst.Databases.MCPServer},
		logger,
		opts.Gateway,
		opts.GatewayServerOptions...,
	)
	if err != nil {
		return nil, fmt.Errorf("setting up MCP gateway: %w", err)
	}
	inst.cleanups = append(inst.cleanups, inst.Gateway.Cleanup)

	requireMCPToken := requireToken
	if requireMCPToken == nil {
		jwksURL := cfg.HydraPublicURL + "/.well-known/jwks.json"
		resourceMetadataURL := cfg.HydraIssuerURL + "/.well-known/oauth-protected-resource"
		mcpTokenValidator, err := authhttp.NewTokenValidator(
			ctx, jwksURL, cfg.HydraIssuerURL, resourceMetadataURL, logger, validatorOpts...,
		)
		if err != nil {
			return nil, fmt.Errorf("creating MCP token validator: %w", err)
		}
		inst.cleanups = append(inst.cleanups, mcpTokenValidator.Close)
		requireMCPToken = mcpTokenValidator.RequireToken()
	}

	// The web frontend, when its bundle is compiled in. The configuration document is served either way, since
	// the frontend dev server proxies it here.
	authMode := webui.AuthModeOAuth
	if tokensOnly {
		authMode = webui.AuthModeToken
	}
	var ownerEmail string
	if inst.Owner != nil {
		ownerEmail = inst.Owner.Email
	}
	ui, err := webui.New(webui.Config{
		AuthMode:           authMode,
		OwnerEmail:         ownerEmail,
		OAuthClientID:      inst.Auth.WebClientID,
		OAuthRedirectURI:   inst.Auth.WebRedirectURI,
		MCPURL:             strings.TrimRight(cfg.PublicURL, "/") + "/mcp",
		CSPScriptSrcExtra:  cfg.WebUICSPScriptSrcExtra,
		CSPConnectSrcExtra: cfg.WebUICSPConnectSrcExtra,
		CSPImgSrcExtra:     cfg.WebUICSPImgSrcExtra,
	})
	if err != nil {
		return nil, fmt.Errorf("setting up the web frontend: %w", err)
	}
	logger.Info().Bool("embedded", ui.Embedded()).Msg("web frontend")

	r := chi.NewRouter()
	r.Use(opts.Middlewares...)

	// An embedded frontend owns the site root, so the auth app's API-root page is left out in that case.
	mountAll(r, inst.Auth.Router, ui.Embedded())
	mountAll(r, inst.Bdbs.Router, false)
	mountAll(r, inst.Databases.Router, false)

	r.Group(func(r chi.Router) {
		r.Use(requireMCPToken)
		r.Handle("/mcp", inst.Gateway.StreamableServer)
	})

	r.Get("/config.json", ui.ServeConfig)
	if ui.Embedded() {
		r.NotFound(ui.ServeHTTP)
	}
	inst.Router = r
	inst.WebAppEmbedded = ui.Embedded()

	return inst, nil
}

// setupStarters sets up the starters app over the bdbs and databases services, mounted on the bdbs router and MCP
// server.
func (inst *Instance) setupStarters(cfg config.Config, logger zerolog.Logger) error {
	db := inst.Databases
	res, err := starters.Setup(cfg, logger,
		intraprocessbdbs.New(inst.Bdbs.BdbSvc),
		intraprocessdatabases.New(db.QuerySvc, db.FormulaSvc, db.SkillSvc, db.DatabaseSvc),
		starters.Host{Router: inst.Bdbs.Router, RequireToken: inst.Bdbs.RequireToken, MCPServer: inst.Bdbs.MCPServer},
		starters.SetupOptions{},
	)
	if err != nil {
		return err
	}
	inst.Starters = res.StarterSvc
	return nil
}

// ServeStdio serves the MCP gateway over stdin and stdout, every call made as the implicit owner, as ServeStdioAs
// does. It requires Options.ImplicitOwner.
func (inst *Instance) ServeStdio(ctx context.Context, stdin io.Reader, stdout io.Writer, logger zerolog.Logger) error {
	if inst.Owner == nil {
		return errors.New("instance: ServeStdio requires Options.ImplicitOwner")
	}
	return inst.ServeStdioAs(ctx, inst.Owner.ID, inst.Owner.Roles, stdin, stdout, logger)
}

// ServeStdioAs serves the MCP gateway over stdin and stdout, every call made as the account userID holding roles,
// until stdin closes (nil) or ctx ends (ctx.Err()). Errors the transport cannot send back to the client go to logger.
func (inst *Instance) ServeStdioAs(
	ctx context.Context, userID uuid.UUID, roles []authz.Role, stdin io.Reader, stdout io.Writer,
	logger zerolog.Logger,
) error {
	srv := mcpserver.NewStdioServer(inst.Gateway.MCPServer)
	srv.SetErrorLogger(log.New(logger, "", 0))
	srv.SetContextFunc(func(ctx context.Context) context.Context {
		return mcputil.ContextWithIdentity(ctx, userID.String(), roles)
	})
	return srv.Listen(ctx, stdin, stdout)
}

// Close runs every cleanup in the reverse order of acquisition. It is safe to call more than once.
func (inst *Instance) Close() {
	for _, cleanup := range slices.Backward(inst.cleanups) {
		cleanup()
	}
	inst.cleanups = nil
}

// mountAll walks all routes in a sub-router and registers them on the parent, skipping internal service-to-service
// routes (/internal/*): the apps are wired in-process and must not expose them on the single public listener.
// With skipRoot set, GET / is left out too, for a parent whose site root belongs to the embedded web frontend
// rather than to an app's API-root page. chi.Walk returns the raw handler and middleware separately, so the
// middleware is re-applied manually.
func mountAll(parent *chi.Mux, sub chi.Router, skipRoot bool) {
	if err := chi.Walk(sub, func(
		method, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler,
	) error {
		if strings.HasPrefix(route, `/internal/`) {
			return nil
		}
		if skipRoot && method == http.MethodGet && route == "/" {
			return nil
		}

		h := handler
		for _, mw := range slices.Backward(middlewares) {
			h = mw(h)
		}
		parent.Method(method, route, h)
		return nil
	}); err != nil {
		panic(fmt.Sprintf("walking routes: %v", err))
	}
}
