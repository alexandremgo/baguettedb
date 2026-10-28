// Command databases runs the databases app as a service of its own: each bdb's data in its own encrypted SQLite file,
// the queries, change log, formulas and skills over it, and the REST routes and MCP tools serving them. It checks
// permissions over the bdbs service's internal HTTP endpoints, serves its MCP tools over SSE and on an internal
// endpoint gated on the shared internal API secret, and reads its configuration from BAGUETTEDB_ environment variables.
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

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/telemetry"
	dbmodule "github.com/alexandremgo/baguettedb/pkg/databases"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/databaseshttp"
)

const otelShutdownTimeout = 5 * time.Second

func main() {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).
		With().
		Str("service", "databases").
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

	otelShutdown, err := telemetry.Init(ctx, "baguettedb-databases", cfg)
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

	// In standalone mode, calls bdbs service over HTTP for permission checks.
	bdbsHTTP := databaseshttp.NewBdbsApp(cfg.BdbsURL, cfg.InternalAPISecret)

	dbResult, err := dbmodule.Setup(ctx, cfg, logger, bdbsHTTP, dbmodule.SetupOptions{
		IncludeMCPRoutes: true,
	})
	if err != nil {
		return fmt.Errorf("setting up databases module: %w", err)
	}
	defer dbResult.Cleanup()

	r := chi.NewRouter()
	r.Use(httputil.OtelHTTP("baguettedb-databases"))
	r.Use(httputil.RequestLogger(logger))
	r.Mount("/", dbResult.Router)

	// Internal Streamable HTTP MCP endpoint — service-to-service only, gated on the shared internal API secret.
	// The gateway forwards the caller's JWT; its signature, issuer, and validity are verified here before the
	// subject is trusted as the acting user.
	internalMCP := mcpserver.NewStreamableHTTPServer(dbResult.MCPServer,
		mcpserver.WithHTTPContextFunc(mcputil.NewInternalHTTPContextFunc(dbResult.TokenValidator)),
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
