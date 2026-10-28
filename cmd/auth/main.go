// Command auth runs the auth app as a service of its own: user accounts and their global roles, personal access tokens,
// the OAuth2 sign-in the Hydra authorization server delegates to it, and the internal service-to-service endpoints
// resolving users and roles, gated on the shared internal API secret. It reads its configuration from BAGUETTEDB_
// environment variables and bootstraps an admin account from it when no account exists yet.
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

	authmodule "github.com/alexandremgo/baguettedb/pkg/auth"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/common/telemetry"
)

// otelShutdownTimeout caps the time given to flush buffered telemetry on shutdown. Fresh ctx (not the cancelled signal
// ctx) so the exporter can still drain even though the main ctx is already Done.
const otelShutdownTimeout = 5 * time.Second

func main() {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout}).
		With().
		Str("service", "auth").
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

	otelShutdown, err := telemetry.Init(ctx, "baguettedb-auth", cfg)
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

	authResult, err := authmodule.Setup(ctx, cfg, logger, authmodule.SetupOptions{})
	if err != nil {
		return fmt.Errorf("setting up auth module: %w", err)
	}
	defer authResult.Cleanup()

	// Bootstraps the first admin account (no-op if users already exist).
	if err := authResult.UserSvc.BootstrapAdmin(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
		return fmt.Errorf("bootstrapping admin: %w", err)
	}

	r := chi.NewRouter()
	r.Use(httputil.OtelHTTP("baguettedb-auth"))
	r.Use(httputil.RequestLogger(logger))

	r.Mount("/", authResult.Router)

	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}
	if err := httputil.ServeUntilSignal(ctx, srv, cfg.ShutdownTimeout, logger); err != nil {
		return fmt.Errorf("serving: %w", err)
	}
	return nil
}
