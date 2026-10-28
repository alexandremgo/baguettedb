package api

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/rs/zerolog"
)

// OAuth2ProxyHandler proxies OAuth2 public endpoints to Hydra's public API.
type OAuth2ProxyHandler struct {
	proxy  *httputil.ReverseProxy
	logger zerolog.Logger
}

// NewOAuth2ProxyHandler creates a new OAuth2ProxyHandler that forwards requests
// to the given Hydra public URL.
func NewOAuth2ProxyHandler(hydraPublicURL string, logger zerolog.Logger) *OAuth2ProxyHandler {
	target, err := url.Parse(hydraPublicURL)
	if err != nil {
		logger.Fatal().Err(err).Str("url", hydraPublicURL).Msg("invalid Hydra public URL for OAuth2 proxy")
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error().Err(err).Str("path", r.URL.Path).Msg("OAuth2 proxy error")
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

	return &OAuth2ProxyHandler{
		proxy:  proxy,
		logger: logger,
	}
}

// ServeHTTP forwards the request to Hydra as-is.
func (h *OAuth2ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.logger.Info().
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Str("query", r.URL.RawQuery).
		Msg("OAuth2 proxy forwarding")
	h.proxy.ServeHTTP(w, r)
}

// ServeWithPathRewrite forwards the request to Hydra with the path rewritten.
// Used for compatibility aliases (e.g. /token → /oauth2/token).
func (h *OAuth2ProxyHandler) ServeWithPathRewrite(targetPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.logger.Info().
			Str("method", r.Method).
			Str("original_path", r.URL.Path).
			Str("rewritten_path", targetPath).
			Msg("OAuth2 proxy forwarding (path rewrite)")
		r.URL.Path = targetPath
		h.proxy.ServeHTTP(w, r)
	}
}
