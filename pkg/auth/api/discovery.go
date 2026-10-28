package api

import (
	"net/http"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// DiscoveryHandler serves the OAuth Protected Resource Metadata (RFC 9728).
// MCP clients fetch this to discover the authorization server.
type DiscoveryHandler struct {
	publicURL      string
	hydraPublicURL string
	logger         zerolog.Logger
}

// NewDiscoveryHandler creates a new DiscoveryHandler.
func NewDiscoveryHandler(publicURL, hydraPublicURL string, logger zerolog.Logger) *DiscoveryHandler {
	return &DiscoveryHandler{publicURL: publicURL, hydraPublicURL: hydraPublicURL, logger: logger}
}

// ServeHTTP handles GET /.well-known/oauth-protected-resource.
func (h *DiscoveryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.logger.Info().
		Str("path", r.URL.Path).
		Str("user_agent", r.UserAgent()).
		Msg("OAuth protected resource discovery")

	httputil.RespondJSON(w, http.StatusOK, map[string]any{
		"resource":                 h.publicURL,
		"authorization_servers":    []string{h.publicURL},
		"scopes_supported":         []string{"openid", "offline_access"},
		"bearer_methods_supported": []string{"header"},
	})
}
