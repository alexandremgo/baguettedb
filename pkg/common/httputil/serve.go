package httputil

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"
)

// defaultReadHeaderTimeout blocks slowloris-style clients that trickle header bytes to keep a connection alive
// indefinitely. Legitimate clients finish headers in milliseconds; 10s is comfortably above any real-world latency.
const defaultReadHeaderTimeout = 10 * time.Second

// defaultIdleTimeout closes keep-alive connections that sit idle between requests so abandoned client connections do
// not accumulate. A connection is not idle while a handler is writing, so long-lived responses are unaffected.
const defaultIdleTimeout = 120 * time.Second

// applyServerDefaults fills the timeouts net/http leaves at "no timeout": ReadHeaderTimeout against slowloris headers
// and IdleTimeout against abandoned keep-alive connections. ReadTimeout and WriteTimeout stay unset on purpose:
// net/http keeps the whole-request read deadline armed while the handler runs and cancels the request context when
// it expires, so either would cut streaming responses (MCP SSE sessions, database exports) at the timeout. Slow
// request bodies are bounded by the body size limit instead.
func applyServerDefaults(srv *http.Server) {
	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if srv.IdleTimeout == 0 {
		srv.IdleTimeout = defaultIdleTimeout
	}
}

// ServeUntilSignal starts srv in a goroutine and blocks until either ctx is cancelled (signal received) or the server
// exits on its own with an error. On ctx cancellation it calls srv.Shutdown with shutdownTimeout to drain in-flight
// requests; the shutdown context is fresh (derived from context.Background) so the drain still has a budget even though
// the caller's ctx is already done.
//
// Returns nil on the graceful path, the server error otherwise. http.ErrServerClosed is suppressed since it's the
// expected return after Shutdown is called.
//
// Zero-valued ReadHeaderTimeout and IdleTimeout are filled in before serving (see applyServerDefaults) — net/http's
// zero value is "no timeout".
func ServeUntilSignal(
	ctx context.Context,
	srv *http.Server,
	shutdownTimeout time.Duration,
	logger zerolog.Logger,
) error {
	applyServerDefaults(srv)

	serverErr := make(chan error, 1)
	go func() {
		logger.Info().Str("addr", srv.Addr).Msg("HTTP server listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case <-ctx.Done():
		logger.Info().Dur("timeout", shutdownTimeout).Msg("shutdown signal received, draining HTTP server")
	case err := <-serverErr:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("HTTP server shutdown: %w", err)
	}
	logger.Info().Msg("HTTP server stopped cleanly")
	return nil
}
