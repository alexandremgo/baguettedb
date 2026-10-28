// Command mcp-gateway runs the MCP gateway as a service of its own: one Streamable HTTP endpoint, /mcp, behind OAuth
// token authentication, serving the tools of the bdbs and databases services together. It reaches them over their
// internal MCP endpoints with the shared internal API secret, forwarding each caller's token, and waits a configured
// time for a backend unreachable at startup. It reads its configuration from BAGUETTEDB_ environment variables.
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
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/common/telemetry"
	"github.com/alexandremgo/baguettedb/pkg/mcpgateway"
)

const otelShutdownTimeout = 5 * time.Second

func main() {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).
		With().
		Str("service", "mcp-gateway").
		Timestamp().
		Logger()

	if err := run(logger); err != nil {
		logger.Fatal().Err(err).Msg("exiting")
	}
}

// run wires the gateway and serves until a shutdown signal, returning the first startup or serve error. Every
// cleanup is deferred inside it, so the process exits only after all of them have run.
func run(logger zerolog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	cfg.LogValues(logger)
	if cfg.InternalAPISecret == "" {
		return errors.New("BAGUETTEDB_INTERNAL_API_SECRET is required to call internal endpoints")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	otelShutdown, err := telemetry.Init(ctx, "baguettedb-mcp-gateway", cfg)
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

	// Connects to backend MCP servers via Streamable HTTP. Tool calls carry the shared internal API secret
	// and forward the caller's JWT so backends can identify the user.
	backendURLs := []string{
		cfg.BdbsMCPURL(),
		cfg.DatabasesMCPURL(),
	}

	result, err := mcpgateway.SetupHTTP(
		backendURLs, cfg.MCPGatewayBackendInitTimeout, cfg.InternalAPISecret, logger, mcpgateway.Options{},
	)
	if err != nil {
		return fmt.Errorf("setting up MCP gateway: %w", err)
	}
	defer result.Cleanup()

	// Creates token validator for external client authentication.
	jwksURL := cfg.HydraPublicURL + "/.well-known/jwks.json"
	resourceMetadataURL := cfg.HydraIssuerURL + "/.well-known/oauth-protected-resource"
	tokenValidator, err := authhttp.NewTokenValidator(ctx, jwksURL, cfg.HydraIssuerURL, resourceMetadataURL, logger)
	if err != nil {
		return fmt.Errorf("creating token validator: %w", err)
	}
	defer tokenValidator.Close()

	r := chi.NewRouter()
	r.Use(httputil.OtelHTTP("baguettedb-mcp-gateway"))
	r.Use(httputil.RequestLogger(logger))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "mcp-gateway"})
	})

	// External MCP endpoint (authenticated, Streamable HTTP transport).
	r.Group(func(r chi.Router) {
		r.Use(tokenValidator.RequireToken())
		r.Handle("/mcp", result.StreamableServer)
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}
	if err := httputil.ServeUntilSignal(ctx, srv, cfg.ShutdownTimeout, logger); err != nil {
		return fmt.Errorf("serving: %w", err)
	}
	return nil
}
