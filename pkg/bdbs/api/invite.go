package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/appconv"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

const defaultInviteExpirationHours = 72

// InviteHandler holds dependencies for bdb invite HTTP handlers.
type InviteHandler struct {
	inviteSvc *service.InviteService
	bdbSvc    *service.BdbService
	baseURL   string
	logger    zerolog.Logger
}

// NewInviteHandler creates a new InviteHandler.
func NewInviteHandler(
	inviteSvc *service.InviteService, bdbSvc *service.BdbService, baseURL string, logger zerolog.Logger,
) *InviteHandler {
	return &InviteHandler{
		inviteSvc: inviteSvc,
		bdbSvc:    bdbSvc,
		baseURL:   baseURL,
		logger:    logger,
	}
}

// CreateInvite handles POST /api/bdbs/v1/bdbs/{bdbID}/invites.
func (h *InviteHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	var req createInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	hours := req.ExpiresInHours
	if hours <= 0 {
		hours = defaultInviteExpirationHours
	}
	expiresAt := time.Now().UTC().Add(time.Duration(hours) * time.Hour)

	role := authz.BdbRole(req.Role)
	invite, err := h.inviteSvc.CreateInvite(r.Context(), userID, bdbID, role, expiresAt)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusCreated, toInviteResponse(invite, h.baseURL))
}

// GetInvite handles GET /api/bdbs/v1/invites/{token}.
func (h *InviteHandler) GetInvite(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "missing invite token"})
		return
	}

	result, err := h.inviteSvc.GetInviteByToken(r.Context(), token)
	if err != nil {
		h.respondError(w, uuid.Nil, err)
		return
	}

	invite := result.Invite

	// Return specific status codes for non-usable invites.
	if invite.Status == domain.InviteStatusAccepted {
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "invite has already been used"})
		return
	}
	if invite.Status == domain.InviteStatusRevoked {
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "invite has been revoked"})
		return
	}
	if !invite.ExpiresAt.After(time.Now().UTC()) {
		httputil.RespondJSON(w, http.StatusGone, errorResponse{Error: "invite has expired"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toInviteDetailsResponse(invite, result.Bdb, result.CreatorEmail))
}

// AcceptInvite handles POST /api/bdbs/v1/invites/{token}/accept.
func (h *InviteHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.parseUserID(w, r)
	if !ok {
		return
	}

	token := chi.URLParam(r, "token")
	if token == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "missing invite token"})
		return
	}

	bdb, err := h.inviteSvc.AcceptInvite(r.Context(), token, userID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	profiles := h.bdbSvc.ResolveMemberProfiles(r.Context(), bdb)
	httputil.RespondJSON(w, http.StatusOK, appconv.Bdb(bdb, userID, profiles))
}

// RevokeInvite handles DELETE /api/bdbs/v1/bdbs/{bdbID}/invites/{inviteID}.
func (h *InviteHandler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	inviteID, err := uuid.Parse(chi.URLParam(r, "inviteID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid invite ID"})
		return
	}

	if err := h.inviteSvc.RevokeInvite(r.Context(), userID, inviteID); err != nil {
		h.respondError(w, userID, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListBdbInvites handles GET /api/bdbs/v1/bdbs/{bdbID}/invites.
func (h *InviteHandler) ListBdbInvites(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := h.parseIDs(w, r)
	if !ok {
		return
	}

	invites, err := h.inviteSvc.ListBdbInvites(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	resp := make([]inviteResponse, len(invites))
	for i, inv := range invites {
		resp[i] = toInviteResponse(inv, h.baseURL)
	}

	httputil.RespondJSON(w, http.StatusOK, resp)
}

// parseUserID extracts and parses the user ID from JWT context.
func (h *InviteHandler) parseUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
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
func (h *InviteHandler) parseIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
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
func (h *InviteHandler) respondError(w http.ResponseWriter, userID uuid.UUID, err error) {
	if limitErr, ok := errors.AsType[domain.MemberLimitReachedError](err); ok {
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{
			Error: fmt.Sprintf("this bdb has reached its limit of %d members", limitErr.Max),
		})
		return
	}

	switch {
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
	case errors.Is(err, domain.ErrBdbNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "bdb not found"})
	case errors.Is(err, domain.ErrNotBdbMember):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: "access denied"})
	case errors.Is(err, domain.ErrAlreadyMember):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "user is already a member"})
	case errors.Is(err, domain.ErrCannotChangeOwnerRole):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "cannot assign the owner role"})
	case errors.Is(err, domain.ErrInvalidRole):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	default:
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("invite operation failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}
