// Command bdbs runs the bdbs app as a service of its own, the starters app mounted on it: bdbs with their members and
// invites, the starter catalog, and the REST routes and MCP tools serving them. It reaches the auth and databases
// services over their internal HTTP endpoints, serves its MCP tools over SSE and on an internal endpoint gated on the
// shared internal API secret, and reads its configuration from BAGUETTEDB_ environment variables.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	bdbsmodule "github.com/alexandremgo/baguettedb/pkg/bdbs"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/bdbshttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/telemetry"
	"github.com/alexandremgo/baguettedb/pkg/starters"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessbdbs"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/startershttp"
)

const otelShutdownTimeout = 5 * time.Second

func main() {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).
		With().
		Str("service", "bdbs").
		Timestamp().
		Logger()

	if err := run(logger); err != nil {
		logger.Fatal().Err(err).Msg("exiting")
	}
}

// run wires the service and serves until a shutdown signal, returning the first startup or serve error. Every
// cleanup is deferred inside it, so the process exits only after all of them have run.
func run(logger zerolog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	cfg.LogValues(logger)
	if cfg.InternalAPISecret == "" {
		return errors.New("BAGUETTEDB_INTERNAL_API_SECRET is required to serve internal endpoints")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	otelShutdown, err := telemetry.Init(ctx, "baguettedb-bdbs", cfg)
	if err != nil {
		return fmt.Errorf("initializing telemetry: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), otelShutdownTimeout)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("telemetry shutdown error")
		}
	}()

	// In standalone mode, calls auth service over HTTP for user role lookups.
	authHTTP := bdbshttp.NewAuthApp(cfg.AuthURL, cfg.InternalAPISecret)
	bdbsResult, err := bdbsmodule.Setup(ctx, cfg, logger, authHTTP, bdbsmodule.SetupOptions{})
	if err != nil {
		return fmt.Errorf("setting up bdbs module: %w", err)
	}
	defer bdbsResult.Cleanup()

	// In standalone mode, calls databases service over HTTP for schema enrichment (this also starts the purge
	// runner).
	dbHTTP := bdbshttp.NewDatabasesApp(cfg.DatabasesURL, cfg.InternalAPISecret)
	bdbsResult.SetDatabasesApp(dbHTTP)

	// Starters: catalog + create-from-starter, seeding the databases service over its internal HTTP endpoints.
	if _, err := starters.Setup(cfg, logger,
		intraprocessbdbs.New(bdbsResult.BdbSvc),
		startershttp.NewDatabasesApp(cfg.DatabasesURL, cfg.InternalAPISecret),
		starters.Host{
			Router: bdbsResult.Router, RequireToken: bdbsResult.RequireToken, MCPServer: bdbsResult.MCPServer,
		},
		starters.SetupOptions{},
	); err != nil {
		return err
	}

	// Creates MCP SSE transport for standalone mode.
	sseServer := mcpserver.NewSSEServer(bdbsResult.MCPServer,
		mcpserver.WithStaticBasePath("/mcp"),
		mcpserver.WithSSEContextFunc(mcputil.SSEContextFunc),
	)

	// Creates token validator for MCP SSE endpoints.
	jwksURL := cfg.HydraPublicURL + "/.well-known/jwks.json"
	resourceMetadataURL := cfg.HydraIssuerURL + "/.well-known/oauth-protected-resource"
	mcpTokenValidator, err := authhttp.NewTokenValidator(
		ctx, jwksURL, cfg.HydraIssuerURL, resourceMetadataURL, logger,
	)
	if err != nil {
		return fmt.Errorf("creating MCP token validator: %w", err)
	}
	defer mcpTokenValidator.Close()

	r := chi.NewRouter()
	r.Use(httputil.OtelHTTP("baguettedb-bdbs"))
	r.Use(httputil.RequestLogger(logger))
	r.Mount("/", bdbsResult.Router)

	// MCP SSE endpoints (authenticated, for direct external access).
	r.Group(func(r chi.Router) {
		r.Use(mcpTokenValidator.RequireToken())
		r.Handle("/mcp/sse", sseServer.SSEHandler())
		r.Handle("/mcp/message", sseServer.MessageHandler())
	})

	// Internal Streamable HTTP MCP endpoint — service-to-service only, gated on the shared internal API secret.
	// The gateway forwards the caller's JWT; its signature, issuer, and validity are verified here before the
	// subject is trusted as the acting user.
	internalMCP := mcpserver.NewStreamableHTTPServer(bdbsResult.MCPServer,
		mcpserver.WithHTTPContextFunc(mcputil.NewInternalHTTPContextFunc(bdbsResult.TokenValidator)),
		mcpserver.WithStateLess(true),
	)
	r.Handle("/internal/mcp", authhttp.RequireInternalSecret(cfg.InternalAPISecret)(internalMCP))

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}
	if err := httputil.ServeUntilSignal(ctx, srv, cfg.ShutdownTimeout, logger); err != nil {
		return fmt.Errorf("serving: %w", err)
	}
	return nil
}
