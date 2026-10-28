package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// OIDCProxyHandler proxies Hydra's OpenID Connect discovery metadata and dynamic
// client registration, patching fields that Hydra omits or that clients send incorrectly.
type OIDCProxyHandler struct {
	clients   clientRegistrar
	publicURL string
	logger    zerolog.Logger
}

// clientRegistrar is the authorization server's dynamic client registration endpoint.
type clientRegistrar interface {
	// RegisterClient registers the client clientReq describes and returns the server's answer and its status code.
	RegisterClient(ctx context.Context, clientReq map[string]any) (map[string]any, int, error)
}

// NewOIDCProxyHandler creates a new OIDCProxyHandler.
func NewOIDCProxyHandler(clients clientRegistrar, publicURL string, logger zerolog.Logger) *OIDCProxyHandler {
	return &OIDCProxyHandler{
		clients:   clients,
		publicURL: publicURL,
		logger:    logger,
	}
}

// ServeDiscovery handles GET /.well-known/openid-configuration and /.well-known/oauth-authorization-server.
// Returns a minimal metadata response matching the MCP OAuth spec, rather than proxying
// Hydra's full OIDC metadata which includes many fields MCP clients don't expect.
func (h *OIDCProxyHandler) ServeDiscovery(w http.ResponseWriter, _ *http.Request) {
	httputil.RespondJSON(w, http.StatusOK, map[string]any{
		"issuer":                                h.publicURL,
		"authorization_endpoint":                h.publicURL + "/authorize",
		"token_endpoint":                        h.publicURL + "/token",
		"registration_endpoint":                 h.publicURL + "/register",
		"revocation_endpoint":                   h.publicURL + "/oauth2/revoke",
		"jwks_uri":                              h.publicURL + "/.well-known/jwks.json",
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"code_challenge_methods_supported":      []string{"plain", "S256"},
		"scopes_supported":                      []string{"openid", "offline_access"},
	})
}

// ServeRegister proxies POST /oauth2/register to Hydra, normalizing the request
// to fix fields that some MCP clients send incorrectly.
func (h *OIDCProxyHandler) ServeRegister(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "failed to read request body",
		})
		return
	}

	var clientReq map[string]any
	if err := json.Unmarshal(body, &clientReq); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "failed to parse request body",
		})
		return
	}

	h.logger.Info().
		Str("client_name", fmt.Sprintf("%v", clientReq["client_name"])).
		Str("grant_types", fmt.Sprintf("%v", clientReq["grant_types"])).
		Str("response_types", fmt.Sprintf("%v", clientReq["response_types"])).
		Str("redirect_uris", fmt.Sprintf("%v", clientReq["redirect_uris"])).
		Str("scope", fmt.Sprintf("%v", clientReq["scope"])).
		Str("token_endpoint_auth_method", fmt.Sprintf("%v", clientReq["token_endpoint_auth_method"])).
		Msg("OAuth2 dynamic client registration request")

	// Normalizes fields that Hydra requires but some clients omit or send incorrectly.
	if uri, ok := clientReq["client_uri"].(string); !ok || uri == "" {
		clientReq["client_uri"] = h.publicURL
	}
	if contacts, ok := clientReq["contacts"]; !ok || contacts == nil {
		clientReq["contacts"] = []string{}
	}
	// Preserves the client's requested auth method. If not specified, Hydra defaults
	// to client_secret_post (confidential). MCP clients that want public (PKCE-only)
	// should explicitly request token_endpoint_auth_method: none.

	respData, statusCode, err := h.clients.RegisterClient(r.Context(), clientReq)
	if err != nil {
		h.logger.Error().Err(err).Msg("dynamic client registration failed")
		httputil.RespondJSON(w, http.StatusBadGateway, map[string]string{
			"error": "failed to register client upstream",
		})
		return
	}

	h.logger.Info().
		Int("status", statusCode).
		Str("client_id", fmt.Sprintf("%v", respData["client_id"])).
		Str("client_name", fmt.Sprintf("%v", respData["client_name"])).
		Str("grant_types", fmt.Sprintf("%v", respData["grant_types"])).
		Str("token_endpoint_auth_method", fmt.Sprintf("%v", respData["token_endpoint_auth_method"])).
		Str("redirect_uris", fmt.Sprintf("%v", respData["redirect_uris"])).
		Msg("OAuth2 dynamic client registration response")

	// Returns only standard RFC 7591 fields. Hydra's response includes ~35 fields
	// with many Hydra-specific nulls that can confuse MCP clients.
	clean := map[string]any{
		"client_id":                  respData["client_id"],
		"redirect_uris":              respData["redirect_uris"],
		"client_name":                respData["client_name"],
		"grant_types":                respData["grant_types"],
		"response_types":             respData["response_types"],
		"token_endpoint_auth_method": respData["token_endpoint_auth_method"],
		"client_secret_expires_at":   respData["client_secret_expires_at"],
	}
	if secret, ok := respData["client_secret"]; ok && secret != nil && secret != "" {
		clean["client_secret"] = secret
	}
	if uri, ok := respData["registration_client_uri"].(string); ok {
		// Rewrites /oauth2/register/... to /register/... for MCP compatibility.
		clean["registration_client_uri"] = strings.Replace(uri, "/oauth2/register/", "/register/", 1)
	}

	httputil.RespondJSON(w, statusCode, clean)
}
