package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// Me handles GET /api/auth/v1/me — returns the current user's profile.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(w, r)
	if !ok {
		return
	}

	user, err := h.user.GetProfile(r.Context(), userID)
	if err != nil {
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to get user profile")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	roles := authz.RolesFromContext(r.Context())

	// If no enricher is set, returns the standard response.
	if h.userResponseEnricher == nil {
		httputil.RespondJSON(w, http.StatusOK, toUserResponseWithRoles(user, roles))
		return
	}

	// Builds a map response so the enricher can add extra fields.
	resp := toUserResponseMap(user, roles)
	if err := h.userResponseEnricher(r.Context(), userID, resp); err != nil {
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to enrich user response")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// ChangePassword handles POST /api/auth/v1/password — changes the current user's password and, unless end_sessions is
// false, ends every session of theirs, the one making the request included. Personal access tokens stay.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	userID, ok := callerID(w, r)
	if !ok {
		return
	}

	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.OldPassword == "" || req.NewPassword == "" {
		httputil.RespondJSON(w, http.StatusBadRequest,
			errorResponse{Error: "old_password and new_password are required"})
		return
	}

	endSessions := req.EndSessions == nil || *req.EndSessions
	err := h.user.ChangePassword(r.Context(), userID, req.OldPassword, req.NewPassword, endSessions)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidCredentials):
			// A 400, not a 401: the caller is signed in, and a client reads a 401 as a session that ended.
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "incorrect current password"})
		case errors.Is(err, domain.ErrPasswordTooWeak):
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		default:
			h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to change password")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"message": "password changed"})
}

// callerID returns the authenticated caller's user ID, or answers 401 and reports false when the request carries
// none.
func callerID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	sub, ok := authz.UserIDFromContext(r.Context())
	if !ok {
		httputil.RespondJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing user identity"})
		return uuid.Nil, false
	}
	userID, err := uuid.Parse(sub)
	if err != nil {
		httputil.RespondJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid user identity"})
		return uuid.Nil, false
	}
	return userID, true
}
