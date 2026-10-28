package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// ExportKeyHeader carries the one-time passphrase that opens a database export. It is returned once on the
// download response and never stored server-side — clients must capture it before discarding the response.
const ExportKeyHeader = "X-Baguettedb-Export-Key"

// ExportHandler holds dependencies for the user-facing data-export HTTP handlers.
type ExportHandler struct {
	exportSvc *service.ExportService
	logger    zerolog.Logger
}

// NewExportHandler creates a new ExportHandler.
func NewExportHandler(exportSvc *service.ExportService, logger zerolog.Logger) *ExportHandler {
	return &ExportHandler{exportSvc: exportSvc, logger: logger}
}

// ExportDatabase handles GET /api/databases/v1/bdbs/{bdbID}/database/export.
// Streams the bdb's full database as a SQLCipher file keyed with a one-time passphrase returned in the
// ExportKeyHeader response header. Owner-only (DBPermExport), enforced inside ExportService.
func (h *ExportHandler) ExportDatabase(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	artifact, err := h.exportSvc.ExportDatabase(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	w.Header().Set(ExportKeyHeader, artifact.Passphrase)
	h.streamArtifact(w, userID, artifact, "application/octet-stream")
}

// GetExportStatus handles GET /api/databases/v1/bdbs/{bdbID}/database/export/status.
// Reports the caller's export allowances: the full-database export quota state and the formula CSV row
// cap. Requires read permission only, so every member can see the caps that apply to them.
func (h *ExportHandler) GetExportStatus(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	status, err := h.exportSvc.GetExportStatus(r.Context(), userID, bdbID)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}
	httputil.RespondJSON(w, http.StatusOK, toExportStatusResponse(status))
}

// ExportFormulaCSV handles GET /api/databases/v1/bdbs/{bdbID}/formulas/{ref}/export.csv.
// Runs the view formula with parameter values taken from the query string (first value per key) and streams
// its full result set as a UTF-8 CSV attachment. Read permission suffices — the CSV shows nothing a member
// couldn't already read through the view.
func (h *ExportHandler) ExportFormulaCSV(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}
	ref := chi.URLParam(r, "ref")

	query := r.URL.Query()
	rawParams := make(map[string]string, len(query))
	for name, values := range query {
		if len(values) > 0 {
			rawParams[name] = values[0]
		}
	}

	artifact, err := h.exportSvc.ExportFormulaCSV(r.Context(), userID, bdbID, ref, rawParams)
	if err != nil {
		h.respondError(w, userID, err)
		return
	}

	h.streamArtifact(w, userID, artifact, "text/csv; charset=utf-8")
}

// streamArtifact streams a spooled export artifact as a download with truthful Content-Length, then
// finalizes it (audit outcome + spool removal) with the streaming result. Headers already set by the
// caller (e.g. the export key) survive — nothing is written to the response before this point.
func (h *ExportHandler) streamArtifact(
	w http.ResponseWriter, userID uuid.UUID, artifact *service.ExportArtifact, contentType string,
) {
	f, err := os.Open(artifact.Path)
	if err != nil {
		artifact.Finish(err, 0)
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("opening export spool")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			h.logger.Warn().Err(cerr).Msg("closing export spool")
		}
	}()

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+artifact.Filename+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(artifact.SizeBytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	n, copyErr := io.Copy(w, f)
	artifact.Finish(copyErr, n)
}

// respondError maps export domain errors to HTTP status codes.
func (h *ExportHandler) respondError(w http.ResponseWriter, userID uuid.UUID, err error) {
	if limited, ok := errors.AsType[*domain.ExportRateLimitedError](err); ok {
		retryAfterSecs := int64((limited.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSecs, 10))
		httputil.RespondJSON(w, http.StatusTooManyRequests, errorResponse{Error: limited.Error()})
		return
	}

	switch {
	case errors.Is(err, domain.ErrNotMember):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrAccessDenied):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: "access denied"})
	case errors.Is(err, domain.ErrDatabaseNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
	case errors.Is(err, domain.ErrFormulaNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrExportInFlight):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrExportLowSpace):
		w.Header().Set("Retry-After", "300")
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrFormulaExportTooLarge):
		httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{
			Error: err.Error() + ". Narrow the view with filters, or export the full database instead.",
		})
	case errors.Is(err, domain.ErrFormulaExportTimeout):
		httputil.RespondJSON(w, http.StatusGatewayTimeout, errorResponse{
			Error: "This view took too long to export. Narrow it with filters and retry.",
		})
	case errors.Is(err, domain.ErrFormulaExportNotView),
		errors.Is(err, domain.ErrFormulaParamInvalid),
		errors.Is(err, domain.ErrFormulaParamValue),
		errors.Is(err, domain.ErrFormulaSQLInvalid):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrQueryExecution):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrNewerPlatformSchema):
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
	default:
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("export failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}
