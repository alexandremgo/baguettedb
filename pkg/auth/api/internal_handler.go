package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/appconv"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// InternalHandler handles internal service-to-service endpoints.
type InternalHandler struct {
	role   *service.RoleService
	user   *service.UserService
	tokens *service.TokenService
}

// NewInternalHandler creates a new InternalHandler, reading roles and profiles through role and user, and managing
// Data API keys through tokens.
func NewInternalHandler(
	role *service.RoleService, user *service.UserService, tokens *service.TokenService,
) *InternalHandler {
	return &InternalHandler{role: role, user: user, tokens: tokens}
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

type createDataAPIKeyRequest struct {
	UserID string `json:"user_id"`
	BdbID  string `json:"bdb_id"`
	Name   string `json:"name"`
}

// CreateDataAPIKey handles POST /internal/api/auth/v1/data-api-keys/create: creates a Data API key for a user, bound to
// a bdb, and returns it with its plain value. Whether the user may create a key for the bdb is not checked here.
//
// An invalid name is a 422 carrying what is wrong with it, an unknown user a 404.
func (h *InternalHandler) CreateDataAPIKey(w http.ResponseWriter, r *http.Request) {
	var req createDataAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	userID, ok := parseInternalID(w, req.UserID, "user_id")
	if !ok {
		return
	}
	bdbID, ok := parseInternalID(w, req.BdbID, "bdb_id")
	if !ok {
		return
	}

	plain, token, err := h.tokens.CreateDataAPIKey(r.Context(), userID, bdbID, req.Name)
	if nameErr, ok := errors.AsType[domain.InvalidAccessTokenNameError](err); ok {
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: nameErr.Problem})
		return
	}
	switch {
	case errors.Is(err, domain.ErrUserNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "user not found"})
		return
	case err != nil:
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusCreated,
		apptypes.CreatedDataAPIKey{Key: appconv.DataAPIKey(token), Value: plain})
}

type listDataAPIKeysRequest struct {
	BdbID string `json:"bdb_id"`
}

type listDataAPIKeysResponse struct {
	Keys []apptypes.DataAPIKey `json:"keys"`
}

// ListDataAPIKeys handles POST /internal/api/auth/v1/data-api-keys: the Data API keys bound to a bdb that are not
// revoked, newest first, whoever created them.
func (h *InternalHandler) ListDataAPIKeys(w http.ResponseWriter, r *http.Request) {
	var req listDataAPIKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	bdbID, ok := parseInternalID(w, req.BdbID, "bdb_id")
	if !ok {
		return
	}

	tokens, err := h.tokens.ListDataAPIKeys(r.Context(), bdbID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	resp := listDataAPIKeysResponse{Keys: make([]apptypes.DataAPIKey, len(tokens))}
	for i, t := range tokens {
		resp.Keys[i] = appconv.DataAPIKey(t)
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

type revokeDataAPIKeyRequest struct {
	BdbID string `json:"bdb_id"`
	KeyID string `json:"key_id"`
}

// RevokeDataAPIKey handles POST /internal/api/auth/v1/data-api-keys/revoke: revokes one Data API key of a bdb, which
// stops authenticating at once. A key the bdb doesn't have, or one already revoked, is a 404.
func (h *InternalHandler) RevokeDataAPIKey(w http.ResponseWriter, r *http.Request) {
	var req revokeDataAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	bdbID, ok := parseInternalID(w, req.BdbID, "bdb_id")
	if !ok {
		return
	}
	keyID, ok := parseInternalID(w, req.KeyID, "key_id")
	if !ok {
		return
	}

	err := h.tokens.RevokeDataAPIKey(r.Context(), bdbID, keyID)
	if errors.Is(err, domain.ErrAccessTokenNotFound) {
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "Data API key not found"})
		return
	}
	if err != nil {
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type revokeDataAPIKeysRequest struct {
	BdbID string `json:"bdb_id"`
}

// RevokeAllDataAPIKeys handles POST /internal/api/auth/v1/data-api-keys/revoke-all: revokes every Data API key bound to
// a bdb. A bdb without keys is not an error.
func (h *InternalHandler) RevokeAllDataAPIKeys(w http.ResponseWriter, r *http.Request) {
	var req revokeDataAPIKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	bdbID, ok := parseInternalID(w, req.BdbID, "bdb_id")
	if !ok {
		return
	}

	if err := h.tokens.RevokeDataAPIKeys(r.Context(), bdbID); err != nil {
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type revokeMemberDataAPIKeysRequest struct {
	BdbID  string `json:"bdb_id"`
	UserID string `json:"user_id"`
}

// RevokeMemberDataAPIKeys handles POST /internal/api/auth/v1/data-api-keys/revoke-member: revokes the Data API keys a
// user created for a bdb. A member without keys is not an error.
func (h *InternalHandler) RevokeMemberDataAPIKeys(w http.ResponseWriter, r *http.Request) {
	var req revokeMemberDataAPIKeysRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	bdbID, ok := parseInternalID(w, req.BdbID, "bdb_id")
	if !ok {
		return
	}
	userID, ok := parseInternalID(w, req.UserID, "user_id")
	if !ok {
		return
	}

	if err := h.tokens.RevokeMemberDataAPIKeys(r.Context(), bdbID, userID); err != nil {
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseInternalID parses raw, the field field of an internal request, as a UUID. A malformed one is answered with a
// 400 naming the field, and reported as not ok.
func parseInternalID(w http.ResponseWriter, raw, field string) (uuid.UUID, bool) {
	id, err := uuid.Parse(raw)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid " + field})
		return uuid.Nil, false
	}
	return id, true
}
