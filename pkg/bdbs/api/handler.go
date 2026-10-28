// Package api serves the bdbs REST surface: bdb lifecycle and ordering, members, and invites, behind token
// authentication. It also serves the service-to-service endpoints resolving membership permissions and bdb ownership,
// guarded by the shared internal API secret. Request and response DTOs carry the JSON tags the domain types leave out.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/appconv"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// Handler holds dependencies for all bdbs HTTP handlers.
type Handler struct {
	bdbSvc *service.BdbService
	dbApp  service.DatabasesApp
	logger zerolog.Logger
}

// NewHandler creates a new bdbs Handler.
func NewHandler(bdbSvc *service.BdbService, logger zerolog.Logger) *Handler {
	return &Handler{
		bdbSvc: bdbSvc,
		logger: logger,
	}
}

// SetDatabasesApp injects the databases module dependency after it is set up.
func (h *Handler) SetDatabasesApp(app service.DatabasesApp) {
	h.dbApp = app
}

// CreateBdb handles POST /api/bdbs/v1/bdbs.
func (h *Handler) CreateBdb(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.parseUserID(w, r)
	if !ok {
		return
	}

	var req createBdbRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	roles := authz.RolesFromContext(r.Context())
	bdb, err := h.bdbSvc.CreateBdb(r.Context(), userID, roles, req.Name, req.Description)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	profiles := h.bdbSvc.ResolveMemberProfiles(r.Context(), bdb)
	httputil.RespondJSON(w, http.StatusCreated, appconv.Bdb(bdb, userID, profiles))
}

// ListBdbs handles GET /api/bdbs/v1/bdbs.
// Supports ?expand=schemas to include table schemas for each bdb.
func (h *Handler) ListBdbs(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.parseUserID(w, r)
	if !ok {
		return
	}

	bdbs, err := h.bdbSvc.ListUserBdbs(r.Context(), userID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	expandSchemas := r.URL.Query().Get("expand") == "schemas"

	resp := make([]apptypes.Bdb, len(bdbs))
	for i, b := range bdbs {
		profiles := h.bdbSvc.ResolveMemberProfiles(r.Context(), b)
		resp[i] = appconv.Bdb(b, userID, profiles)

		if expandSchemas {
			tables, err := h.dbApp.ListTablesWithSchemas(r.Context(), userID, b.ID)
			if err != nil {
				h.logger.Warn().Err(err).Str("bdb_id", b.ID.String()).Msg("failed to fetch tables for bdb")
				tables = []apptypes.TableSchema{}
			}
			resp[i].Tables = &tables
		}
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// ReorderBdbs handles PATCH /api/bdbs/v1/bdbs/order.
func (h *Handler) ReorderBdbs(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.parseUserID(w, r)
	if !ok {
		return
	}

	var req reorderBdbsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if err := h.bdbSvc.ReorderBdbs(r.Context(), userID, req.BdbIDs); err != nil {
		h.respondError(w, userID, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetBdb handles GET /api/bdbs/v1/bdbs/{bdbID}.
func (h *Handler) GetBdb(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	bdb, err := h.bdbSvc.GetBdb(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.Bdb(bdb, userID, h.bdbSvc.ResolveMemberProfiles(r.Context(), bdb)))
}

// UpdateBdb handles PATCH /api/bdbs/v1/bdbs/{bdbID}.
func (h *Handler) UpdateBdb(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	var req updateBdbRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	bdb, err := h.bdbSvc.UpdateBdb(r.Context(), userID, bdbID, req.Name, req.Description)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.Bdb(bdb, userID, h.bdbSvc.ResolveMemberProfiles(r.Context(), bdb)))
}

// DeleteBdb handles DELETE /api/bdbs/v1/bdbs/{bdbID}.
// Marks the bdb for permanent deletion after the grace period. The owner
// can call RestoreBdb to cancel within that window.
func (h *Handler) DeleteBdb(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	if err := h.bdbSvc.DeleteBdb(r.Context(), userID, bdbID); err != nil {
		h.respondError(w, userID, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RestoreBdb handles POST /api/bdbs/v1/bdbs/{bdbID}/restore.
// Cancels a pending soft-delete and returns the bdb in its restored state.
func (h *Handler) RestoreBdb(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	bdb, err := h.bdbSvc.RestoreBdb(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	profiles := h.bdbSvc.ResolveMemberProfiles(r.Context(), bdb)
	httputil.RespondJSON(w, http.StatusOK, appconv.Bdb(bdb, userID, profiles))
}

// ListMembers handles GET /api/bdbs/v1/bdbs/{bdbID}/members.
func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	bdb, err := h.bdbSvc.GetBdb(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	profiles := h.bdbSvc.ResolveMemberProfiles(r.Context(), bdb)
	members := make([]apptypes.BdbMember, len(bdb.Members))
	for i, m := range bdb.Members {
		members[i] = appconv.BdbMember(&m, profiles[m.UserID])
	}

	httputil.RespondJSON(w, http.StatusOK, members)
}

// UpdateMemberRole handles PATCH /api/bdbs/v1/bdbs/{bdbID}/members/{userID}.
func (h *Handler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	targetUserID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid member user ID"})
		return
	}

	var req updateMemberRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	role := authz.BdbRole(req.Role)

	bdb, err := h.bdbSvc.UpdateMemberRole(r.Context(), userID, bdbID, targetUserID, role)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.Bdb(bdb, userID, h.bdbSvc.ResolveMemberProfiles(r.Context(), bdb)))
}

// RemoveMember handles DELETE /api/bdbs/v1/bdbs/{bdbID}/members/{userID}.
func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	targetUserID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid member user ID"})
		return
	}

	bdb, err := h.bdbSvc.RemoveMember(r.Context(), userID, bdbID, targetUserID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.Bdb(bdb, userID, h.bdbSvc.ResolveMemberProfiles(r.Context(), bdb)))
}

// parseUserID extracts and parses the user ID from JWT context.
func (h *Handler) parseUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
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

// parseIDs extracts and parses the user ID from JWT context and bdb ID from URL.
func (h *Handler) parseIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	userID, ok := h.parseUserID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}

	bdbID, err := uuid.Parse(chi.URLParam(r, "bdbID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid bdb ID"})
		return uuid.Nil, uuid.Nil, false
	}

	return userID, bdbID, true
}

// respondError maps domain errors to HTTP status codes and writes the response.
func (h *Handler) respondError(w http.ResponseWriter, userID uuid.UUID, err error) {
	if limitErr, ok := errors.AsType[domain.BdbLimitReachedError](err); ok {
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{
			Error: fmt.Sprintf("you've reached your limit of %d bdbs", limitErr.Max),
		})
		return
	}
	if limitErr, ok := errors.AsType[domain.MemberLimitReachedError](err); ok {
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{
			Error: fmt.Sprintf("this bdb has reached its limit of %d members", limitErr.Max),
		})
		return
	}

	switch {
	case errors.Is(err, domain.ErrBdbNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "bdb not found"})
	case errors.Is(err, domain.ErrBdbAlreadyDeleted):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "bdb is already deleted"})
	case errors.Is(err, domain.ErrMemberNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "member not found"})
	case errors.Is(err, domain.ErrNotBdbMember):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: "access denied"})
	case errors.Is(err, domain.ErrAlreadyMember):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "user is already a member"})
	case errors.Is(err, domain.ErrCannotRemoveOwner):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "cannot remove the bdb owner"})
	case errors.Is(err, domain.ErrCannotChangeOwnerRole):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: "cannot change the owner's role"})
	case errors.Is(err, domain.ErrBdbNameEmpty):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "bdb name is required"})
	case errors.Is(err, domain.ErrBdbNameTooLong):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "bdb name exceeds maximum length"})
	case errors.Is(err, domain.ErrBdbNameTaken):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrInvalidRole):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrInvalidBdbOrder):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrInviteNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "invite not found"})
	case errors.Is(err, domain.ErrInviteExpired):
		httputil.RespondJSON(w, http.StatusGone, errorResponse{Error: "invite has expired"})
	case errors.Is(err, domain.ErrInviteBdbDeleted):
		httputil.RespondJSON(w, http.StatusGone, errorResponse{Error: "bdb has been deleted"})
	case errors.Is(err, domain.ErrInviteAlreadyUsed):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "invite has already been used"})
	case errors.Is(err, domain.ErrInviteRevoked):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "invite has been revoked"})
	default:
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("bdb operation failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}
