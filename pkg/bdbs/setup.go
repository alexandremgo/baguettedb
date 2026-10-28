// Package bdbs wires the bdbs app: the services managing bdbs, their members and invites on the instance store the
// config selects (Postgres or SQLite, migrated first), the REST routes and MCP tools serving them, the purge runner and
// usage gauges. The auth app is a Setup parameter; the databases app, set up later, arrives through
// SetupResult.SetDatabasesApp. SetupOptions adds routes and MCP tools and replaces authentication and quotas.
package bdbs

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"

	bdbapi "github.com/alexandremgo/baguettedb/pkg/bdbs/api"
	bdbsmcp "github.com/alexandremgo/baguettedb/pkg/bdbs/mcp"
	bdbrunner "github.com/alexandremgo/baguettedb/pkg/bdbs/runner"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

// SetupResult holds the outputs of the bdbs module setup.
type SetupResult struct {
	Router      chi.Router
	AuthChecker *service.BdbAuthChecker
	BdbSvc      *service.BdbService
	MCPServer   *mcpserver.MCPServer
	// RequireToken is the middleware guarding the app's protected routes: SetupOptions.RequireToken when
	// injected, otherwise TokenValidator's.
	RequireToken func(http.Handler) http.Handler
	// TokenValidator validates JWTs against the platform's JWKS (signature, issuer, validity window). Nil when
	// SetupOptions.RequireToken was injected.
	TokenValidator *authhttp.TokenValidator
	Cleanup        func()

	// SetDatabasesApp injects the databases module dependency after it is set up (enabling schema enrichment in
	// list_bdbs) and starts the soft-delete purge runner, which needs it. Idempotent: later calls are no-ops.
	SetDatabasesApp func(service.DatabasesApp)
}

// ExtDeps holds dependencies made available to extension routes registered via
// SetupOptions.ExtraRoutes. Extensions can use these to build authenticated
// routes and transaction-scoped adapters against the bdbs database.
type ExtDeps struct {
	// RequireToken is the middleware guarding the app's protected routes.
	RequireToken func(http.Handler) http.Handler
	// TokenValidator validates JWTs against the platform's JWKS. Nil when SetupOptions.RequireToken was injected.
	TokenValidator *authhttp.TokenValidator
	// Pool is the bdbs database's Postgres pool. Nil on the SQLite instance store.
	Pool *pgxpool.Pool
}

// SetupOptions configures optional behaviors of the bdbs module setup.
type SetupOptions struct {
	// RequireToken authenticates the app's protected routes: it rejects an unauthenticated request or puts the
	// caller's identity in the request context. When nil, the app validates JWTs against the platform's JWKS.
	RequireToken func(http.Handler) http.Handler

	// TokenValidatorOptions configure the JWT validator the app builds when RequireToken is nil, e.g. to accept
	// opaque tokens alongside (authhttp.WithOpaqueTokens).
	TokenValidatorOptions []authhttp.ValidatorOption

	// QuotaResolver overrides the default quota resolver built from config.
	// If nil, the module uses quota.Fixed over the config values.
	QuotaResolver quota.Resolver

	// ExtraRoutes registers additional routes on the bdbs router after all
	// built-in routes are mounted. Receives the router and shared dependencies
	// so extensions can mount both public and authenticated routes.
	ExtraRoutes func(r chi.Router, deps ExtDeps)

	// ExtraMCPTools registers additional MCP tools on the bdbs MCP server
	// after all built-in tools are registered. Receives the same shared
	// dependencies as ExtraRoutes so extensions can create their own adapters.
	ExtraMCPTools func(s *mcpserver.MCPServer, deps ExtDeps)
}

// Setup sets up the bdbs module: runs migrations, creates all adapters, services,
// and handlers. The authApp parameter provides user role lookups for quota enforcement.
func Setup(
	ctx context.Context, cfg config.Config, logger zerolog.Logger, authApp service.AuthApp, opts SetupOptions,
) (*SetupResult, error) {
	var qr quota.Resolver
	if opts.QuotaResolver != nil {
		qr = opts.QuotaResolver
	} else {
		qr = quota.Fixed(quota.Limits{
			MaxBdbs:          cfg.QuotaMaxBdbs,
			MaxMembersPerBdb: cfg.QuotaMaxMembersPerBdb,
		})
	}
	st, err := openStore(ctx, cfg, logger, authApp, qr)
	if err != nil {
		return nil, err
	}
	bdbSvc, inviteSvc, authChecker := st.bdbSvc, st.inviteSvc, st.authChecker

	var tokenValidator *authhttp.TokenValidator
	requireToken := opts.RequireToken
	if requireToken == nil {
		jwksURL := cfg.HydraPublicURL + "/.well-known/jwks.json"
		tokenValidator, err = authhttp.NewTokenValidator(ctx, jwksURL, cfg.HydraIssuerURL, "", logger,
			opts.TokenValidatorOptions...)
		if err != nil {
			st.close()
			return nil, fmt.Errorf("bdbs: creating token validator: %w", err)
		}
		requireToken = tokenValidator.RequireToken()
	}

	// Creates handlers and router.
	handler := bdbapi.NewHandler(bdbSvc, logger)
	inviteHandler := bdbapi.NewInviteHandler(inviteSvc, bdbSvc, cfg.AppBaseURL, logger)
	internalHandler := bdbapi.NewInternalHandler(authChecker, logger)
	router := bdbapi.NewRouter(
		handler, inviteHandler, internalHandler, requireToken, cfg.MaxRequestBodySize, cfg.InternalAPISecret)

	// Registers extension-provided extra routes if any.
	extDeps := ExtDeps{
		RequireToken:   requireToken,
		TokenValidator: tokenValidator,
		Pool:           st.pool,
	}
	if opts.ExtraRoutes != nil {
		opts.ExtraRoutes(router, extDeps)
	}

	// Creates MCP server with deferred databases app injection.
	dbRef := &bdbsmcp.DatabasesAppRef{}
	mcpSrv := bdbsmcp.NewServer(bdbSvc, dbRef, bdbsmcp.Options{
		SkillBodyBudgetBytes: cfg.MCPDescribeBdbSkillBodyBudgetBytes,
		SchemaTableLimit:     cfg.MCPDescribeBdbSchemaTableLimit,
	}, logger)

	// Prepares the soft-delete purge runner; SetDatabasesApp starts it once the databases dependency it sweeps
	// through is wired, so the runner's goroutine never reads a field the wiring is still writing. The runner
	// uses a Setup-internal cancellable ctx so cleanup() can both signal shutdown AND wait for the in-flight
	// sweep to drain before the postgres pool below is closed.
	purger := bdbrunner.NewPurger(bdbSvc, cfg.BdbsPurgeInterval, logger)
	purgerCtx, cancelPurger := context.WithCancel(ctx)
	var purgerWG sync.WaitGroup
	var startPurger sync.Once

	// Registers extension-provided extra MCP tools if any.
	if opts.ExtraMCPTools != nil {
		opts.ExtraMCPTools(mcpSrv, extDeps)
	}

	// Usage gauges read the active-bdb count at every metric collection. Instruments are no-ops until
	// OpenTelemetry is initialized with a real meter provider; a registration failure disables them, never startup.
	unregisterUsageGauges := func() {}
	usageGauges, err := bdbrunner.RegisterUsageGauges(
		otel.Meter("github.com/alexandremgo/baguettedb/pkg/bdbs/runner"), bdbSvc, logger)
	if err != nil {
		logger.Error().Err(err).Msg("bdbs: registering usage gauges; usage gauges disabled")
	} else {
		unregisterUsageGauges = func() {
			if err := usageGauges.Unregister(); err != nil {
				logger.Warn().Err(err).Msg("bdbs: unregistering usage gauges")
			}
		}
	}

	cleanup := func() {
		cancelPurger()
		purgerWG.Wait()
		unregisterUsageGauges()
		if tokenValidator != nil {
			tokenValidator.Close()
		}
		st.close()
	}

	return &SetupResult{
		Router:         router,
		AuthChecker:    authChecker,
		BdbSvc:         bdbSvc,
		MCPServer:      mcpSrv,
		RequireToken:   requireToken,
		TokenValidator: tokenValidator,
		Cleanup:        cleanup,
		SetDatabasesApp: func(app service.DatabasesApp) {
			dbRef.App = app
			handler.SetDatabasesApp(app)
			bdbSvc.SetDatabasesApp(app)
			startPurger.Do(func() {
				purgerWG.Go(func() {
					purger.Run(purgerCtx)
				})
			})
		},
	}, nil
}
