package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

const (
	accessTokenCookie  = authhttp.DefaultCookieName
	refreshTokenCookie = "bdb_refresh_token"

	// refreshTokenMaxAge is the max age for the refresh token cookie (30 days).
	refreshTokenMaxAge = 30 * 24 * time.Hour
)

// CookieTokenHandler exchanges OAuth authorization codes for tokens and stores
// them in httpOnly cookies, keeping tokens invisible to client-side JavaScript.
//
// The webClientID field holds the OAuth2 client ID of the SPA. The BFF serves a
// single SPA, so the client ID is fixed at startup. MCP and REST API clients
// provide their own client_id via the standard /oauth2/token proxy path instead.
type CookieTokenHandler struct {
	tokens        tokenEndpoint
	webClientID   string
	secureCookies bool
	logger        zerolog.Logger
}

// tokenEndpoint is the authorization server's token and revocation endpoints.
type tokenEndpoint interface {
	// ExchangeToken sends req to the token endpoint and returns the tokens it issues.
	ExchangeToken(ctx context.Context, req domain.TokenRequest) (*domain.TokenSet, error)

	// RevokeToken invalidates token.
	RevokeToken(ctx context.Context, token string) error
}

// NewCookieTokenHandler creates a handler that proxies token exchanges to Hydra
// and returns tokens as httpOnly cookies instead of in the response body.
func NewCookieTokenHandler(
	tokens tokenEndpoint, webClientID string, secureCookies bool, logger zerolog.Logger,
) *CookieTokenHandler {
	return &CookieTokenHandler{
		tokens:        tokens,
		webClientID:   webClientID,
		secureCookies: secureCookies,
		logger:        logger,
	}
}

type exchangeTokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	ClientID     string `json:"client_id"`
	RedirectURI  string `json:"redirect_uri"`
}

type exchangeTokenResponse struct {
	ExpiresIn int `json:"expires_in"`
}

// ExchangeToken handles POST /api/auth/v1/token.
// Receives an authorization code + PKCE verifier from the SPA, exchanges them
// with Hydra, and sets the resulting tokens as httpOnly cookies.
func (h *CookieTokenHandler) ExchangeToken(w http.ResponseWriter, r *http.Request) {
	var req exchangeTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if req.Code == "" || req.CodeVerifier == "" || req.ClientID == "" || req.RedirectURI == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "code, code_verifier, client_id, and redirect_uri are required",
		})
		return
	}

	tokens, err := h.tokens.ExchangeToken(r.Context(), domain.TokenRequest{
		GrantType:    "authorization_code",
		Code:         req.Code,
		CodeVerifier: req.CodeVerifier,
		ClientID:     req.ClientID,
		RedirectURI:  req.RedirectURI,
	})
	if err != nil {
		h.logger.Error().Err(err).Msg("token exchange with Hydra failed")
		httputil.RespondJSON(w, http.StatusBadGateway, map[string]string{"error": "token exchange failed"})
		return
	}

	h.setTokenCookies(w, tokens)
	httputil.RespondJSON(w, http.StatusOK, exchangeTokenResponse{ExpiresIn: tokens.ExpiresIn})
}

// RefreshToken handles POST /api/auth/v1/token/refresh.
// Reads the refresh token from the cookie, obtains new tokens from Hydra,
// and sets updated cookies.
func (h *CookieTokenHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshTokenCookie)
	if err != nil {
		httputil.RespondJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing refresh token"})
		return
	}

	tokens, err := h.tokens.ExchangeToken(r.Context(), domain.TokenRequest{
		GrantType:    "refresh_token",
		RefreshToken: cookie.Value,
		ClientID:     h.webClientID,
	})
	if err != nil {
		h.logger.Debug().Err(err).Msg("token refresh with Hydra failed")
		h.clearTokenCookies(w)
		httputil.RespondJSON(w, http.StatusUnauthorized, map[string]string{"error": "token refresh failed"})
		return
	}

	h.setTokenCookies(w, tokens)
	httputil.RespondJSON(w, http.StatusOK, exchangeTokenResponse{ExpiresIn: tokens.ExpiresIn})
}

// Logout handles POST /api/auth/v1/logout.
// Revokes the refresh token with Hydra and clears the token cookies.
func (h *CookieTokenHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(refreshTokenCookie); err == nil {
		if revokeErr := h.tokens.RevokeToken(r.Context(), cookie.Value); revokeErr != nil {
			h.logger.Warn().Err(revokeErr).Msg("token revocation with Hydra failed (continuing with cookie clear)")
		}
	}

	h.clearTokenCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

// setTokenCookies sets httpOnly cookies for the access and refresh tokens.
func (h *CookieTokenHandler) setTokenCookies(w http.ResponseWriter, tokens *domain.TokenSet) {
	http.SetCookie(w, &http.Cookie{
		Name:     accessTokenCookie,
		Value:    tokens.AccessToken,
		Path:     "/",
		MaxAge:   tokens.ExpiresIn,
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})

	if tokens.RefreshToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     refreshTokenCookie,
			Value:    tokens.RefreshToken,
			Path:     "/",
			MaxAge:   int(refreshTokenMaxAge.Seconds()),
			HttpOnly: true,
			Secure:   h.secureCookies,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

// clearTokenCookies expires both token cookies.
func (h *CookieTokenHandler) clearTokenCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     accessTokenCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     refreshTokenCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}
