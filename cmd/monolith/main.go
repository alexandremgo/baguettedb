// Command monolith runs a whole BaguetteDB instance in one process: the auth, bdbs, databases and starters apps wired
// in-process, their REST routes, the MCP gateway at /mcp and, when its bundle is compiled in, the web frontend, all on
// one port. Clients authenticate with OAuth tokens from the Hydra authorization server. It reads its configuration from
// BAGUETTEDB_ environment variables, bootstrapping an admin account from it when none exists.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/common/telemetry"
	"github.com/alexandremgo/baguettedb/pkg/instance"
	"github.com/alexandremgo/baguettedb/pkg/mcpgateway"
)

const otelShutdownTimeout = 5 * time.Second

// version identifies the build, stamped at link time with -X main.version=<tag>; "dev" for a plain go build.
var version = "dev"

func main() {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).
		With().
		Timestamp().
		Logger()

	if err := run(logger); err != nil {
		logger.Fatal().Err(err).Msg("exiting")
	}
}

// run composes the instance and serves it until a shutdown signal, returning the first startup or serve error.
// Every cleanup is deferred inside it, so the process exits only after all of them have run.
func run(logger zerolog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	cfg.LogValues(logger)
	logger.Info().Str("version", version).Msg("baguettedb monolith")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	otelShutdown, err := telemetry.Init(ctx, "baguettedb-monolith", cfg)
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

	inst, err := instance.New(ctx, cfg, logger, instance.Options{
		Middlewares: []func(http.Handler) http.Handler{
			httputil.OtelHTTP("baguettedb-monolith"),
			httputil.RequestLogger(logger),
		},
		Gateway: mcpgateway.Options{ServerVersion: version},
	})
	if err != nil {
		return err
	}
	defer inst.Close()

	// Monolith healthcheck.
	inst.Router.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httputil.RespondJSON(w, http.StatusOK, map[string]string{
			"status": "ok", "service": "monolith", "version": version,
		})
	})

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: inst.Router}
	if err := httputil.ServeUntilSignal(ctx, srv, cfg.ShutdownTimeout, logger); err != nil {
		return fmt.Errorf("serving: %w", err)
	}
	return nil
}
