package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/appinput"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// FormulaHandler holds dependencies for the databases formula HTTP handlers.
type FormulaHandler struct {
	formulaSvc *service.FormulaService
	logger     zerolog.Logger
}

// NewFormulaHandler creates a new FormulaHandler.
func NewFormulaHandler(formulaSvc *service.FormulaService, logger zerolog.Logger) *FormulaHandler {
	return &FormulaHandler{formulaSvc: formulaSvc, logger: logger}
}

// ListFormulas handles GET /api/databases/v1/bdbs/{bdbID}/formulas.
// Visible to viewer+ roles; permission is enforced inside FormulaService.
func (h *FormulaHandler) ListFormulas(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	formulas, err := h.formulaSvc.ListFormulas(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toFormulaResponses(formulas))
}

// GetFormula handles GET /api/databases/v1/bdbs/{bdbID}/formulas/{ref}.
// {ref} accepts either a UUID or the human-readable formula name.
func (h *FormulaHandler) GetFormula(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	if ref == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "formula ref is required"})
		return
	}

	formula, err := h.formulaSvc.GetFormula(r.Context(), userID, bdbID, ref)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toFormulaResponse(formula))
}

// CreateFormula handles POST /api/databases/v1/bdbs/{bdbID}/formulas.
// Editor+ only; permission enforced inside FormulaService.
func (h *FormulaHandler) CreateFormula(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	var req appinput.CreateFormula
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	input, err := req.Input()
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	formula, err := h.formulaSvc.CreateFormula(r.Context(), userID, bdbID, input)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusCreated, toFormulaResponse(formula))
}

// UpdateFormula handles PUT /api/databases/v1/bdbs/{bdbID}/formulas/{ref}.
// Partial update — only supplied fields are changed.
func (h *FormulaHandler) UpdateFormula(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	if ref == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "formula ref is required"})
		return
	}

	var req appinput.UpdateFormula
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	patch, err := req.Patch()
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	formula, err := h.formulaSvc.UpdateFormula(r.Context(), userID, bdbID, ref, patch)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toFormulaResponse(formula))
}

// DeleteFormula handles DELETE /api/databases/v1/bdbs/{bdbID}/formulas/{ref}.
// Editor+ only. Removes the formula and any attached actions (FK cascade).
func (h *FormulaHandler) DeleteFormula(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	if ref == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "formula ref is required"})
		return
	}

	if err := h.formulaSvc.DeleteFormula(r.Context(), userID, bdbID, ref); err != nil {
		h.respondError(w, userID, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RunFormula handles POST /api/databases/v1/bdbs/{bdbID}/formulas/{ref}/run.
// Runs the formula with the supplied parameter values; permission is enforced by the
// execute path according to the statement class (a view needs read, an action write).
func (h *FormulaHandler) RunFormula(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	if ref == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "formula ref is required"})
		return
	}

	var req runFormulaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	runResult, err := h.formulaSvc.RunFormula(r.Context(), userID, bdbID, ref, service.FormulaRunInput{
		Params: req.Params,
	})
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toFormulaRunResponse(runResult))
}

// respondError maps formula-related domain errors to HTTP status codes.
func (h *FormulaHandler) respondError(w http.ResponseWriter, userID uuid.UUID, err error) {
	if limited, ok := errors.AsType[*domain.QueryRateLimitedError](err); ok {
		respondRateLimited(w, limited)
		return
	}
	switch {
	case errors.Is(err, domain.ErrNotMember):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrAccessDenied), errors.Is(err, domain.ErrReservedTable):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: "access denied"})
	case errors.Is(err, domain.ErrDatabaseReadOnly):
		w.Header().Set("Retry-After", "5")
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrDatabaseNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
	case errors.Is(err, domain.ErrFormulaRunTimeout):
		httputil.RespondJSON(w, http.StatusGatewayTimeout, errorResponse{
			Error: "This formula is too big to run in the app. Ask your AI assistant to run it " +
				"for you — or to help make it faster.",
		})
	case errors.Is(err, domain.ErrFormulaNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrFormulaNameTaken):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrFormulaNameInvalid),
		errors.Is(err, domain.ErrFormulaLabelInvalid),
		errors.Is(err, domain.ErrFormulaDescriptionInvalid),
		errors.Is(err, domain.ErrFormulaSQLInvalid),
		errors.Is(err, domain.ErrFormulaParamInvalid),
		errors.Is(err, domain.ErrFormulaParamValue),
		errors.Is(err, domain.ErrFormulaKindUndeterminable),
		errors.Is(err, domain.ErrFormulaAttachTargetNotFound),
		errors.Is(err, domain.ErrFormulaAttachConflict),
		errors.Is(err, domain.ErrFormulaDisplayInvalid),
		errors.Is(err, domain.ErrFormulaViewNotPageable),
		errors.Is(err, domain.ErrUnsupportedStatement),
		errors.Is(err, domain.ErrInvalidSQL),
		errors.Is(err, domain.ErrTooManyRows),
		errors.Is(err, domain.ErrStatementNotAllowedOnEndpoint):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrQueryExecution):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrQueryTimeout):
		httputil.RespondJSON(w, http.StatusGatewayTimeout, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrQueryResultTooLarge):
		httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrNewerPlatformSchema):
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
	default:
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("formula operation failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}
