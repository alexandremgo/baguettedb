// Package api serves the starters REST surface: the catalog and bdb-from-starter creation. It is mounted on
// the bdbs service's router, behind the same authentication as bdb creation.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
	"github.com/alexandremgo/baguettedb/pkg/starters/service"
)

// Handler handles the starters REST endpoints.
type Handler struct {
	starterSvc *service.StarterService
	logger     zerolog.Logger
}

// NewHandler creates a new Handler.
func NewHandler(starterSvc *service.StarterService, logger zerolog.Logger) *Handler {
	return &Handler{starterSvc: starterSvc, logger: logger}
}

// ListStarters handles GET /api/bdbs/v1/starters. Returns the full starter catalog for the gallery.
func (h *Handler) ListStarters(w http.ResponseWriter, r *http.Request) {
	manifests, err := h.starterSvc.ListStarters(r.Context())
	if err != nil {
		h.logger.Error().Err(err).Msg("listing starters failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	out := make([]starterResponse, len(manifests))
	for i, m := range manifests {
		out[i] = toStarterResponse(m)
	}
	httputil.RespondJSON(w, http.StatusOK, out)
}

// CreateFromStarter handles POST /api/bdbs/v1/bdbs/from-starter. Creates a bdb seeded from the given starter
// and returns it with the same shape as POST /bdbs.
func (h *Handler) CreateFromStarter(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.parseUserID(w, r)
	if !ok {
		return
	}

	var req createFromStarterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	if req.StarterKey == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "starter_key is required"})
		return
	}

	roles := authz.RolesFromContext(r.Context())
	bdbID, err := h.starterSvc.CreateFromStarter(r.Context(), userID, roles, req.StarterKey, req.Name, req.Description)
	if err != nil {
		h.respondCreateError(w, userID, req.StarterKey, err)
		return
	}

	bdb, err := h.starterSvc.GetBdb(r.Context(), userID, bdbID)
	if err != nil {
		h.logger.Error().Err(err).
			Str("bdb_id", bdbID.String()).
			Msg("Loading freshly created bdb failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	httputil.RespondJSON(w, http.StatusCreated, bdb)
}

// respondCreateError maps a create-from-starter failure to its HTTP response: starters errors first, then the
// bdb-creation errors that surface through the engine (quota, name validation), then a logged 500. An apply
// failure responds with a clean retryable message — the real cause is server-side and goes to the log.
func (h *Handler) respondCreateError(w http.ResponseWriter, userID uuid.UUID, starterKey string, err error) {
	if limitErr, ok := errors.AsType[domain.BdbLimitReachedError](err); ok {
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{
			Error: fmt.Sprintf("you've reached your limit of %d bdbs", limitErr.Max),
		})
		return
	}

	switch {
	case errors.Is(err, domain.ErrStarterNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "starter not found"})
	case errors.Is(err, domain.ErrBdbNameEmpty):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "bdb name is required"})
	case errors.Is(err, domain.ErrBdbNameTooLong):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "bdb name exceeds maximum length"})
	case errors.Is(err, domain.ErrBdbNameTaken):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrStarterApplyFailed):
		h.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("starter_key", starterKey).
			Msg("Starter apply failed")
		httputil.RespondJSON(w, http.StatusInternalServerError,
			errorResponse{Error: "seeding the new bdb failed, please try again"})
	default:
		h.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("starter_key", starterKey).
			Msg("Create from starter failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
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
