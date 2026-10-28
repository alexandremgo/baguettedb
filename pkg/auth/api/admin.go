package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// AdminCreateUser handles POST /api/auth/v1/admin/users — creates a new user account.
func (h *Handler) AdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req adminCreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Email == "" || req.Password == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "email and password are required"})
		return
	}

	user, err := h.user.CreateUser(r.Context(), req.Email, req.Password, req.FirstName, req.LastName)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidEmail):
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid email format"})
		case errors.Is(err, domain.ErrEmailAlreadyExists):
			httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "email already exists"})
		case errors.Is(err, domain.ErrPasswordTooWeak):
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		default:
			h.logger.Error().Err(err).Msg("failed to create user")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		}
		return
	}

	httputil.RespondJSON(w, http.StatusCreated, toUserResponse(user))
}

// ListUsers handles GET /api/auth/v1/admin/users.
func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	search := r.URL.Query().Get("search")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	users, total, err := h.user.ListUsers(r.Context(), search, limit, offset)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to list users")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	resp := userListResponse{
		Users: make([]userResponse, len(users)),
		Total: total,
	}
	for i, u := range users {
		roles, err := h.role.GetUserRoles(r.Context(), u.ID)
		if err != nil {
			h.logger.Error().Err(err).Str("user_id", u.ID.String()).Msg("failed to get roles for user")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
			return
		}
		resp.Users[i] = toUserResponseWithRoles(u, roles)
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// GetUserRoles handles GET /api/auth/v1/admin/users/{userID}/roles.
func (h *Handler) GetUserRoles(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user ID"})
		return
	}

	roles, err := h.role.GetUserRoles(r.Context(), userID)
	if err != nil {
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to get user roles")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	roleStrings := make([]string, len(roles))
	for i, role := range roles {
		roleStrings[i] = string(role)
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]any{"roles": roleStrings})
}

// AssignRole handles POST /api/auth/v1/admin/users/{userID}/roles.
func (h *Handler) AssignRole(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user ID"})
		return
	}

	var req assignRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.Role == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "role is required"})
		return
	}

	err = h.role.AssignRole(r.Context(), userID, req.Role)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidRole):
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid role"})
		default:
			h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to assign role")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"message": "role assigned"})
}

// RemoveRole handles DELETE /api/auth/v1/admin/users/{userID}/roles/{role}.
func (h *Handler) RemoveRole(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user ID"})
		return
	}

	role := chi.URLParam(r, "role")
	if role == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "role is required"})
		return
	}

	err = h.role.RemoveRole(r.Context(), userID, role)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrInvalidRole):
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid role"})
		default:
			h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to remove role")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		}
		return
	}

	httputil.RespondJSON(w, http.StatusOK, map[string]string{"message": "role removed"})
}
