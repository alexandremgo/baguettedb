// Package httputil holds the HTTP plumbing the apps' servers share: JSON responses, the middleware capping request
// bodies, tracing and logging requests and checking the Host a browser sends, and ServeUntilSignal, which serves until
// a shutdown signal and then drains in-flight requests.
package httputil

import (
	"net/http"
	"strings"
	"time"

	"github.com/felixge/httpsnoop"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/alexandremgo/baguettedb/pkg/common/telemetry"
)

// isHealthCheck reports whether the request path is a health-check endpoint (any path ending in /healthz).
func isHealthCheck(path string) bool {
	return strings.HasSuffix(path, "/healthz")
}

// MaxBodySize returns middleware that limits the request body size.
// If the body exceeds maxBytes, reading it returns an error and the handler
// responds with 413 Request Entity Too Large.
func MaxBodySize(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// OtelHTTP returns middleware that creates spans and records HTTP metrics for each request using otelhttp.
// Health-check requests produce no telemetry at all: kubelet probes fire every few seconds, and their spans
// and metric samples would otherwise dominate low-traffic services.
func OtelHTTP(serviceName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return otelhttp.NewMiddleware(serviceName, otelhttp.WithFilter(func(r *http.Request) bool {
			return !isHealthCheck(r.URL.Path)
		}))(next)
	}
}

// RequestLogger returns middleware that logs each HTTP request with trace and span IDs when available.
// Successful health-check requests are not logged (probe noise); failed ones (status >= 400) still are, since
// a failing probe is a real signal.
func RequestLogger(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			status := http.StatusOK

			hooks := httpsnoop.Hooks{
				WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
					return func(code int) {
						status = code
						next(code)
					}
				},
			}

			wrapped := httpsnoop.Wrap(w, hooks)
			next.ServeHTTP(wrapped, r)

			if isHealthCheck(r.URL.Path) && status < http.StatusBadRequest {
				return
			}

			evt := logger.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", status).
				Dur("duration", time.Since(start))

			telemetry.WithTrace(r.Context(), evt).Msg("request")
		})
	}
}
