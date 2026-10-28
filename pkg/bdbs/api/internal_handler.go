package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// checkPermissionRequest holds the JSON body for an internal permission check.
type checkPermissionRequest struct {
	UserID     string `json:"user_id"`
	BdbID      string `json:"bdb_id"`
	Permission string `json:"permission"`
}

// checkPermissionResponse holds the result of an internal permission check.
type checkPermissionResponse struct {
	Allowed bool `json:"allowed"`
}

// bdbOwnerRequest holds the JSON body for an internal bdb owner lookup.
type bdbOwnerRequest struct {
	BdbID string `json:"bdb_id"`
}

// bdbOwnerResponse holds the result of an internal bdb owner lookup.
type bdbOwnerResponse struct {
	OwnerUserID string   `json:"owner_user_id"`
	Roles       []string `json:"roles"`
}

// ownedBdbIDsRequest holds the JSON body for an internal owned-bdb-ids lookup.
type ownedBdbIDsRequest struct {
	UserID string `json:"user_id"`
}

// ownedBdbIDsResponse holds the result of an internal owned-bdb-ids lookup.
type ownedBdbIDsResponse struct {
	BdbIDs []string `json:"bdb_ids"`
}

// InternalHandler handles internal service-to-service endpoints.
type InternalHandler struct {
	authChecker *service.BdbAuthChecker
	logger      zerolog.Logger
}

// NewInternalHandler creates a new InternalHandler.
func NewInternalHandler(authChecker *service.BdbAuthChecker, logger zerolog.Logger) *InternalHandler {
	return &InternalHandler{authChecker: authChecker, logger: logger}
}

// CheckPermission handles POST /api/bdbs/v1/internal/check-permission.
func (h *InternalHandler) CheckPermission(w http.ResponseWriter, r *http.Request) {
	var req checkPermissionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user_id"})
		return
	}

	bdbID, err := uuid.Parse(req.BdbID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid bdb_id"})
		return
	}

	perm := authz.DatabasePermission(req.Permission)
	if !authz.IsValidDatabasePermission(perm) {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid permission"})
		return
	}

	allowed, err := h.authChecker.CheckMemberPermission(r.Context(), userID, bdbID, perm)
	if err != nil {
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, checkPermissionResponse{Allowed: allowed})
}

// GetBdbOwner handles POST /internal/api/bdbs/v1/bdb-owner.
// Returns the bdb owner's user ID and global roles.
func (h *InternalHandler) GetBdbOwner(w http.ResponseWriter, r *http.Request) {
	var req bdbOwnerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	bdbID, err := uuid.Parse(req.BdbID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid bdb_id"})
		return
	}

	ownerID, roles, err := h.authChecker.GetBdbOwner(r.Context(), bdbID)
	if err != nil {
		h.logger.Error().Err(err).Str("bdb_id", bdbID.String()).Msg("failed to get bdb owner")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	roleStrings := make([]string, len(roles))
	for i, role := range roles {
		roleStrings[i] = string(role)
	}

	httputil.RespondJSON(w, http.StatusOK, bdbOwnerResponse{OwnerUserID: ownerID.String(), Roles: roleStrings})
}

// GetOwnedBdbIDs handles POST /internal/api/bdbs/v1/owned-bdb-ids.
// Returns the IDs of every bdb the user owns, including soft-deleted bdbs still awaiting purge.
func (h *InternalHandler) GetOwnedBdbIDs(w http.ResponseWriter, r *http.Request) {
	var req ownedBdbIDsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user_id"})
		return
	}

	ids, err := h.authChecker.GetOwnedBdbIDs(r.Context(), userID)
	if err != nil {
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("failed to get owned bdb ids")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	idStrings := make([]string, len(ids))
	for i, id := range ids {
		idStrings[i] = id.String()
	}

	httputil.RespondJSON(w, http.StatusOK, ownedBdbIDsResponse{BdbIDs: idStrings})
}
