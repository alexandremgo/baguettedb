package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// TokenHandler serves the caller's own personal access tokens: listing, creating and revoking them.
type TokenHandler struct {
	tokens *service.TokenService
	logger zerolog.Logger
}

// NewTokenHandler creates a TokenHandler managing tokens through tokens.
func NewTokenHandler(tokens *service.TokenService, logger zerolog.Logger) *TokenHandler {
	return &TokenHandler{tokens: tokens, logger: logger}
}

// List handles GET /api/auth/v1/personal-tokens: the caller's tokens that are not revoked, newest first.
func (h *TokenHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(w, r)
	if !ok {
		return
	}
	tokens, err := h.tokens.List(r.Context(), userID)
	if err != nil {
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to list access tokens")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	resp := accessTokenListResponse{Tokens: make([]accessTokenResponse, len(tokens))}
	for i, t := range tokens {
		resp.Tokens[i] = toAccessTokenResponse(t)
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// Create handles POST /api/auth/v1/personal-tokens: creates a token for the caller and returns it with its plain
// value, the only time the value is shown.
func (h *TokenHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(w, r)
	if !ok {
		return
	}
	var req createAccessTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	plain, token, err := h.tokens.Create(r.Context(), userID, req.Name, req.ExpiresAt)
	if errors.Is(err, domain.ErrInvalidAccessTokenName) || errors.Is(err, domain.ErrInvalidAccessTokenExpiry) {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to create an access token")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	httputil.RespondJSON(w, http.StatusCreated, createdAccessTokenResponse{
		accessTokenResponse: toAccessTokenResponse(token),
		Token:               plain,
	})
}

// Revoke handles DELETE /api/auth/v1/personal-tokens/{tokenID}: revokes one of the caller's tokens, which stops
// authenticating at once.
func (h *TokenHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(w, r)
	if !ok {
		return
	}
	tokenID, err := uuid.Parse(chi.URLParam(r, "tokenID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid token ID"})
		return
	}
	err = h.tokens.Revoke(r.Context(), userID, tokenID)
	if errors.Is(err, domain.ErrAccessTokenNotFound) {
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}
	if err != nil {
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to revoke an access token")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
