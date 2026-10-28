package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// SkillHandler holds dependencies for the databases skill HTTP handlers.
type SkillHandler struct {
	skillSvc *service.SkillService
	logger   zerolog.Logger
}

// NewSkillHandler creates a new SkillHandler.
func NewSkillHandler(skillSvc *service.SkillService, logger zerolog.Logger) *SkillHandler {
	return &SkillHandler{
		skillSvc: skillSvc,
		logger:   logger,
	}
}

// ListSkills handles GET /api/databases/v1/bdbs/{bdbID}/skills.
// Returns every skill (with target tables and full body) for the bdb.
// Visible to viewer+ roles; permission is enforced inside SkillService.
func (h *SkillHandler) ListSkills(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	skills, err := h.skillSvc.ListSkills(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toSkillResponses(skills))
}

// GetSkill handles GET /api/databases/v1/bdbs/{bdbID}/skills/{ref}.
// {ref} accepts either a UUID or the human-readable skill name.
func (h *SkillHandler) GetSkill(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	if ref == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "skill ref is required"})
		return
	}

	skill, err := h.skillSvc.GetSkill(r.Context(), userID, bdbID, ref)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toSkillResponse(skill))
}

// CreateSkill handles POST /api/databases/v1/bdbs/{bdbID}/skills.
// Editor+ only; permission enforced inside SkillService.
func (h *SkillHandler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	var req createSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	skill, err := h.skillSvc.CreateSkill(r.Context(), userID, bdbID, service.SkillInput{
		Name:         req.Name,
		Description:  req.Description,
		Body:         req.Body,
		TargetTables: req.TargetTables,
	})
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusCreated, toSkillResponse(skill))
}

// UpdateSkill handles PUT /api/databases/v1/bdbs/{bdbID}/skills/{ref}.
// Partial update — only supplied fields are changed. To replace target_tables,
// pass the full new list (an empty array converts the skill to bdb-level).
func (h *SkillHandler) UpdateSkill(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	if ref == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "skill ref is required"})
		return
	}

	var req updateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	patch := service.SkillPatch{
		Name:         req.Name,
		Description:  req.Description,
		Body:         req.Body,
		TargetTables: req.TargetTables,
	}

	skill, err := h.skillSvc.UpdateSkill(r.Context(), userID, bdbID, ref, patch)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toSkillResponse(skill))
}

// DeleteSkill handles DELETE /api/databases/v1/bdbs/{bdbID}/skills/{ref}.
// Editor+ only. Removes the skill and its target rows (FK cascade).
func (h *SkillHandler) DeleteSkill(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	ref := chi.URLParam(r, "ref")
	if ref == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "skill ref is required"})
		return
	}

	if err := h.skillSvc.DeleteSkill(r.Context(), userID, bdbID, ref); err != nil {
		h.respondError(w, userID, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// respondError maps skill-related domain errors to HTTP status codes and writes the response.
func (h *SkillHandler) respondError(w http.ResponseWriter, userID uuid.UUID, err error) {
	switch {
	case errors.Is(err, domain.ErrNotMember):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrAccessDenied):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: "access denied"})
	case errors.Is(err, domain.ErrDatabaseReadOnly):
		w.Header().Set("Retry-After", "5")
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrDatabaseNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
	case errors.Is(err, domain.ErrSkillNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrSkillNameTaken):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrSkillNameInvalid),
		errors.Is(err, domain.ErrSkillDescriptionInvalid),
		errors.Is(err, domain.ErrSkillBodyInvalid),
		errors.Is(err, domain.ErrSkillTargetNotFound):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrSkillBodyTooLarge):
		httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrQueryExecution):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrNewerPlatformSchema):
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
	default:
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("skill operation failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}
