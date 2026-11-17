package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
)

// DataAPIKeyHandler serves a bdb's Data API keys to its owner: listing, creating and revoking them, and adding and
// removing the formulas each may run.
type DataAPIKeyHandler struct {
	keys   *service.DataAPIKeyService
	logger zerolog.Logger
}

// NewDataAPIKeyHandler creates a DataAPIKeyHandler managing keys through keys.
func NewDataAPIKeyHandler(keys *service.DataAPIKeyService, logger zerolog.Logger) *DataAPIKeyHandler {
	return &DataAPIKeyHandler{keys: keys, logger: logger}
}

// RegisterDataAPIKeyRoutes mounts the routes managing a bdb's Data API keys on r, behind requireToken and a request
// body limit of maxRequestBodySize.
func RegisterDataAPIKeyRoutes(
	r chi.Router, h *DataAPIKeyHandler, requireToken func(http.Handler) http.Handler, maxRequestBodySize int64,
) {
	r.Group(func(r chi.Router) {
		r.Use(requireToken)
		r.Use(httputil.MaxBodySize(maxRequestBodySize))
		r.Get("/api/bdbs/v1/bdbs/{bdbID}/data-api-keys", h.List)
		r.Post("/api/bdbs/v1/bdbs/{bdbID}/data-api-keys", h.Create)
		r.Delete("/api/bdbs/v1/bdbs/{bdbID}/data-api-keys/{keyID}", h.Revoke)
		r.Put("/api/bdbs/v1/bdbs/{bdbID}/data-api-keys/{keyID}/formulas/{formulaID}", h.AddFormula)
		r.Delete("/api/bdbs/v1/bdbs/{bdbID}/data-api-keys/{keyID}/formulas/{formulaID}", h.RemoveFormula)
	})
}

// List handles GET /api/bdbs/v1/bdbs/{bdbID}/data-api-keys: the bdb's keys that are not revoked, newest first, each
// with the formulas it may run.
func (h *DataAPIKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := callerAndBdb(w, r)
	if !ok {
		return
	}
	keys, err := h.keys.ListKeys(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}
	resp := dataAPIKeyListResponse{Keys: make([]dataAPIKeyResponse, len(keys))}
	for i, k := range keys {
		resp.Keys[i] = toDataAPIKeyResponse(k)
	}
	httputil.RespondJSON(w, http.StatusOK, resp)
}

// Create handles POST /api/bdbs/v1/bdbs/{bdbID}/data-api-keys: creates a key allowed to run the given formulas and
// returns it with its plain value, the only time the value is shown.
func (h *DataAPIKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := callerAndBdb(w, r)
	if !ok {
		return
	}
	var req createDataAPIKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	key, plain, err := h.keys.CreateKey(r.Context(), userID, bdbID, req.Name, req.FormulaIDs)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}
	httputil.RespondJSON(w, http.StatusCreated, createdDataAPIKeyResponse{
		dataAPIKeyResponse: toDataAPIKeyResponse(key),
		Token:              plain,
	})
}

// Revoke handles DELETE /api/bdbs/v1/bdbs/{bdbID}/data-api-keys/{keyID}: revokes the key, which stops authenticating
// at once.
func (h *DataAPIKeyHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := callerAndBdb(w, r)
	if !ok {
		return
	}
	keyID, ok := pathKeyID(w, r)
	if !ok {
		return
	}
	if err := h.keys.RevokeKey(r.Context(), userID, bdbID, keyID); err != nil {
		h.respondError(w, userID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// AddFormula handles PUT /api/bdbs/v1/bdbs/{bdbID}/data-api-keys/{keyID}/formulas/{formulaID}: lets the key run the
// formula too. The key keeps its value.
func (h *DataAPIKeyHandler) AddFormula(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := callerAndBdb(w, r)
	if !ok {
		return
	}
	keyID, ok := pathKeyID(w, r)
	if !ok {
		return
	}
	if err := h.keys.AddFormula(r.Context(), userID, bdbID, keyID, chi.URLParam(r, "formulaID")); err != nil {
		h.respondError(w, userID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveFormula handles DELETE /api/bdbs/v1/bdbs/{bdbID}/data-api-keys/{keyID}/formulas/{formulaID}: stops the key from
// running the formula.
func (h *DataAPIKeyHandler) RemoveFormula(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := callerAndBdb(w, r)
	if !ok {
		return
	}
	keyID, ok := pathKeyID(w, r)
	if !ok {
		return
	}
	if err := h.keys.RemoveFormula(r.Context(), userID, bdbID, keyID, chi.URLParam(r, "formulaID")); err != nil {
		h.respondError(w, userID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// respondError maps Data API key errors to HTTP status codes and writes the response.
func (h *DataAPIKeyHandler) respondError(w http.ResponseWriter, userID uuid.UUID, err error) {
	switch {
	case errors.Is(err, domain.ErrNotBdbMember):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: "access denied"})
	case errors.Is(err, domain.ErrBdbNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "bdb not found"})
	case errors.Is(err, domain.ErrBdbAlreadyDeleted):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "bdb is already deleted"})
	case errors.Is(err, domain.ErrDataAPIKeyNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "data API key not found"})
	case errors.Is(err, domain.ErrInvalidDataAPIKeyName):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrFormulaNotInBdb):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
	default:
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("data API key operation failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}

// callerAndBdb returns the caller's user ID and the bdb ID in the path. A missing or malformed identity is answered
// with a 401, a malformed bdb ID with a 400, and both reported as not ok.
func callerAndBdb(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	sub, ok := authz.UserIDFromContext(r.Context())
	if !ok {
		httputil.RespondJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing user identity"})
		return uuid.Nil, uuid.Nil, false
	}
	userID, err := uuid.Parse(sub)
	if err != nil {
		httputil.RespondJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid user identity"})
		return uuid.Nil, uuid.Nil, false
	}
	bdbID, err := uuid.Parse(chi.URLParam(r, "bdbID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid bdb ID"})
		return uuid.Nil, uuid.Nil, false
	}
	return userID, bdbID, true
}

// pathKeyID returns the Data API key ID in the path, answering a malformed one with a 400 and reporting it as not ok.
func pathKeyID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	keyID, err := uuid.Parse(chi.URLParam(r, "keyID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid data API key ID"})
		return uuid.Nil, false
	}
	return keyID, true
}
