package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/appconv"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// InternalHandler handles internal service-to-service endpoints.
type InternalHandler struct {
	role *service.RoleService
	user *service.UserService
}

// NewInternalHandler creates a new InternalHandler.
func NewInternalHandler(role *service.RoleService, user *service.UserService) *InternalHandler {
	return &InternalHandler{role: role, user: user}
}

type getUserRolesRequest struct {
	UserID string `json:"user_id"`
}

type getUserRolesResponse struct {
	Roles []string `json:"roles"`
}

// GetUserRoles handles POST /internal/api/auth/v1/user-roles.
// Returns the global roles assigned to the given user.
func (h *InternalHandler) GetUserRoles(w http.ResponseWriter, r *http.Request) {
	var req getUserRolesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user_id"})
		return
	}

	roles, err := h.role.GetUserRoles(r.Context(), userID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	roleStrings := make([]string, len(roles))
	for i, role := range roles {
		roleStrings[i] = string(role)
	}

	httputil.RespondJSON(w, http.StatusOK, getUserRolesResponse{Roles: roleStrings})
}

type getUserProfilesRequest struct {
	UserIDs []string `json:"user_ids"`
}

type getUserProfilesResponse struct {
	Profiles []apptypes.UserProfile `json:"profiles"`
}

// GetUserProfiles handles POST /internal/api/auth/v1/user-profiles.
// Returns the non-credential profile of each user matching the given IDs.
// Unknown IDs are silently omitted; an empty input returns an empty list.
func (h *InternalHandler) GetUserProfiles(w http.ResponseWriter, r *http.Request) {
	var req getUserProfilesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	ids := make([]uuid.UUID, 0, len(req.UserIDs))
	for _, raw := range req.UserIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user_id"})
			return
		}
		ids = append(ids, id)
	}

	profiles, err := h.user.GetProfiles(r.Context(), ids)
	if err != nil {
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	entries := make([]apptypes.UserProfile, len(profiles))
	for i, p := range profiles {
		entries[i] = appconv.UserProfile(p)
	}

	httputil.RespondJSON(w, http.StatusOK, getUserProfilesResponse{Profiles: entries})
}
