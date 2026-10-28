// Package api serves the databases REST surface: a bdb's database and tables, query, data and schema execution, the
// change log with undo, redo and revert, skills, formulas, and exports, plus the internal service-to-service endpoints
// for cross-app reads and writes, snapshots, and moves between pods. Its ShardRouter forwards a request for a bdb
// another pod owns to that pod, so callers never need to know the topology.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/appconv"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// Handler holds dependencies for all databases HTTP handlers.
type Handler struct {
	dbSvc    *service.DatabaseService
	querySvc *service.QueryService
	logger   zerolog.Logger
}

// NewHandler creates a new databases Handler.
func NewHandler(dbSvc *service.DatabaseService, querySvc *service.QueryService, logger zerolog.Logger) *Handler {
	return &Handler{
		dbSvc:    dbSvc,
		querySvc: querySvc,
		logger:   logger,
	}
}

// GetDatabase handles GET /api/databases/v1/bdbs/{bdbID}/database.
func (h *Handler) GetDatabase(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	db, err := h.dbSvc.AuthorizeAndResolveDatabase(r.Context(), userID, bdbID, authz.DBPermRead)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toDatabaseResponse(db))
}

// GetDatabaseStats handles GET /api/databases/v1/bdbs/{bdbID}/database/stats.
func (h *Handler) GetDatabaseStats(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	// Verifies the user has read access to the bdb's database.
	if _, err := h.dbSvc.AuthorizeAndResolveDatabase(r.Context(), userID, bdbID, authz.DBPermRead); err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	used, limit, err := h.dbSvc.GetDatabaseStats(r.Context(), bdbID)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toDatabaseStatsResponse(used, limit))
}

// GetDatabaseStatsAdmin handles GET /api/databases/v1/admin/bdbs/{bdbID}/database/stats.
// Requires manage:users permission.
func (h *Handler) GetDatabaseStatsAdmin(w http.ResponseWriter, r *http.Request) {
	bdbID, err := uuid.Parse(chi.URLParam(r, "bdbID"))
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid bdb ID"})
		return
	}

	used, limit, err := h.dbSvc.GetDatabaseStats(r.Context(), bdbID)
	if err != nil {
		h.logger.Error().Err(err).Str("bdb_id", bdbID.String()).Msg("admin stats lookup failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toDatabaseStatsResponse(used, limit))
}

// GetAccountStorage handles GET /api/databases/v1/account/storage.
// Returns the calling user's account-wide storage usage: the tracked total across every bdb they own
// against their aggregate cap.
func (h *Handler) GetAccountStorage(w http.ResponseWriter, r *http.Request) {
	sub, ok := authz.UserIDFromContext(r.Context())
	if !ok {
		httputil.RespondJSON(w, http.StatusUnauthorized, errorResponse{Error: "missing user identity"})
		return
	}
	userID, err := uuid.Parse(sub)
	if err != nil {
		httputil.RespondJSON(w, http.StatusUnauthorized, errorResponse{Error: "invalid user identity"})
		return
	}

	used, limit, err := h.dbSvc.AccountStorage(r.Context(), userID, authz.RolesFromContext(r.Context()))
	if err != nil {
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("account storage lookup failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, accountStorageResponse{
		TotalStorageBytesUsed:  used,
		TotalStorageBytesLimit: limit,
	})
}

// ListTables handles GET /api/databases/v1/bdbs/{bdbID}/database/tables.
func (h *Handler) ListTables(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	result, err := h.querySvc.ListTables(r.Context(), userID, bdbID)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toQueryResultResponse(result))
}

// DescribeTable handles GET /api/databases/v1/bdbs/{bdbID}/database/tables/{tableName}.
func (h *Handler) DescribeTable(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	tableName := chi.URLParam(r, "tableName")
	if tableName == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "table name is required"})
		return
	}

	result, err := h.querySvc.DescribeTable(r.Context(), userID, bdbID, tableName)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.TableDescription(result))
}

// ExecuteQuery handles POST /api/databases/v1/bdbs/{bdbID}/database/query.
// Accepts DQL (SELECT) only; rejects DML and DDL.
func (h *Handler) ExecuteQuery(w http.ResponseWriter, r *http.Request) {
	h.executeSQL(w, r, domain.IntentQuery)
}

// ExecuteData handles POST /api/databases/v1/bdbs/{bdbID}/database/data.
// Accepts DML (INSERT/UPDATE/DELETE) only; rejects DQL and DDL.
func (h *Handler) ExecuteData(w http.ResponseWriter, r *http.Request) {
	h.executeSQL(w, r, domain.IntentExecute)
}

// ExecuteSchema handles POST /api/databases/v1/bdbs/{bdbID}/database/schema.
// Accepts DDL (CREATE/DROP/ALTER) only; rejects DQL and DML.
func (h *Handler) ExecuteSchema(w http.ResponseWriter, r *http.Request) {
	h.executeSQL(w, r, domain.IntentSchema)
}

// ListChanges handles GET /api/databases/v1/bdbs/{bdbID}/database/changes.
//
// Query params:
//   - kind (optional): "DML" or "DDL"; omit to return both
//   - table_name (optional): scope to one table (matches DML.table_name or DDL.object_name)
//   - user_id (optional): scope to changes by this user; omit to return the full audit view
//   - limit (optional, default 20, max 100)
//
// Note: virtual tables (e.g. FTS5) are captured only at the DDL level — direct row writes bypass the change log,
// and undo of DROP returns an empty vtable to be rebuilt from the source.
func (h *Handler) ListChanges(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	kindStr := r.URL.Query().Get("kind")
	var kind domain.ChangeKind
	switch kindStr {
	case "", "DML", "DDL":
		kind = domain.ChangeKind(kindStr)
	default:
		httputil.RespondJSON(w, http.StatusBadRequest,
			errorResponse{Error: "kind must be 'DML', 'DDL', or omitted"})
		return
	}

	tableName := r.URL.Query().Get("table_name")

	var filterUserID *uuid.UUID
	if raw := r.URL.Query().Get("user_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user_id query parameter"})
			return
		}
		filterUserID = &parsed
	}

	limit := 20
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil {
			limit = parsed
		}
	}

	result, err := h.querySvc.ListChanges(r.Context(), userID, bdbID, kind, tableName, filterUserID, limit)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toListChangesResponse(result))
}

// UndoLastChange handles POST /api/databases/v1/bdbs/{bdbID}/database/changes/undo.
//
// Note: virtual tables (e.g. FTS5) are captured only at the DDL level — direct row writes bypass the change log,
// and undo of DROP returns an empty vtable to be rebuilt from the source.
func (h *Handler) UndoLastChange(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	result, err := h.querySvc.UndoLastChange(r.Context(), userID, bdbID)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toQueryResultResponse(result))
}

// RedoLastChange handles POST /api/databases/v1/bdbs/{bdbID}/database/changes/redo.
//
// Note: virtual tables (e.g. FTS5) are captured only at the DDL level — direct row writes bypass the change log,
// and undo of DROP returns an empty vtable to be rebuilt from the source.
func (h *Handler) RedoLastChange(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	result, err := h.querySvc.RedoLastChange(r.Context(), userID, bdbID)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toQueryResultResponse(result))
}

// UndoLastChangeAnyone handles POST /api/databases/v1/bdbs/{bdbID}/database/changes/undo/anyone.
// Owner-only: reverses the most recent change in the bdb regardless of who made it.
//
// Note: virtual tables (e.g. FTS5) are captured only at the DDL level — direct row writes bypass the change log,
// and undo of DROP returns an empty vtable to be rebuilt from the source.
func (h *Handler) UndoLastChangeAnyone(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	result, err := h.querySvc.UndoLastChangeAnyone(r.Context(), userID, bdbID)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toQueryResultResponse(result))
}

// RedoLastChangeAnyone handles POST /api/databases/v1/bdbs/{bdbID}/database/changes/redo/anyone.
// Owner-only: re-applies the most recently undone change in the bdb regardless of who undid it.
//
// Note: virtual tables (e.g. FTS5) are captured only at the DDL level — direct row writes bypass the change log,
// and undo of DROP returns an empty vtable to be rebuilt from the source.
func (h *Handler) RedoLastChangeAnyone(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	result, err := h.querySvc.RedoLastChangeAnyone(r.Context(), userID, bdbID)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toQueryResultResponse(result))
}

// RevertChange handles POST /api/databases/v1/bdbs/{bdbID}/database/changes/{changeLogID}/revert.
//
// Note: virtual tables (e.g. FTS5) are captured only at the DDL level — direct row writes bypass the change log,
// and reverting a DROP returns an empty vtable to be rebuilt from the source.
func (h *Handler) RevertChange(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	changeLogIDStr := chi.URLParam(r, "changeLogID")
	changeLogID, err := strconv.ParseInt(changeLogIDStr, 10, 64)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid change log ID"})
		return
	}

	result, err := h.querySvc.RevertChange(r.Context(), userID, bdbID, changeLogID)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toQueryResultResponse(result))
}

// RevertChangeBatch handles POST /api/databases/v1/bdbs/{bdbID}/database/changes/batch/{batchID}/revert.
//
// Note: virtual tables (e.g. FTS5) are captured only at the DDL level — direct row writes bypass the change log,
// and reverting a DROP returns an empty vtable to be rebuilt from the source.
func (h *Handler) RevertChangeBatch(w http.ResponseWriter, r *http.Request) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	batchID := chi.URLParam(r, "batchID")
	if batchID == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "batch ID is required"})
		return
	}

	result, err := h.querySvc.RevertChangeBatch(r.Context(), userID, bdbID, batchID)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toQueryResultResponse(result))
}

// executeSQL is the shared implementation for ExecuteQuery, ExecuteData, and ExecuteSchema.
func (h *Handler) executeSQL(w http.ResponseWriter, r *http.Request, intent domain.OperationIntent) {
	userID, bdbID, ok := parseIDs(w, r)
	if !ok {
		return
	}

	var req executeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if httputil.IsMaxBytesError(err) {
			httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: "request body too large"})
			return
		}
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	if req.SQL == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "sql is required"})
		return
	}

	args, err := toTypedValues(req.Args)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	result, err := h.querySvc.Execute(r.Context(), userID, bdbID, req.SQL, args, intent)
	if err != nil {
		h.respondQueryError(w, userID, err)
		return
	}

	httputil.RespondJSON(w, http.StatusOK, toQueryResultResponse(result))
}

// parseIDs extracts and parses the user ID from JWT context and bdb ID from URL.
func parseIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
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

	bdbIDStr := chi.URLParam(r, "bdbID")
	bdbID, err := uuid.Parse(bdbIDStr)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid bdb ID"})
		return uuid.Nil, uuid.Nil, false
	}

	return userID, bdbID, true
}

// statementKindAdvice returns which endpoint runs the statement refused, e.g. "send it to /data".
func statementKindAdvice(refused *domain.StatementKindError) string {
	if refused.Count > 1 {
		return "send one statement per /query request"
	}
	switch refused.Kind {
	case domain.StatementDQL:
		return "send it to /query"
	case domain.StatementDML:
		return "send it to /data"
	case domain.StatementDDL:
		return "send it to /schema"
	default:
		return "no endpoint runs it"
	}
}

// respondQueryError maps domain errors to HTTP status codes and writes the response.
func (h *Handler) respondQueryError(w http.ResponseWriter, userID uuid.UUID, err error) {
	if conflictErr, ok := errors.AsType[*service.ConflictError](err); ok {
		httputil.RespondJSON(w, http.StatusConflict, toChangeConflictResponse(conflictErr.Conflict))
		return
	}
	if limited, ok := errors.AsType[*domain.QueryRateLimitedError](err); ok {
		respondRateLimited(w, limited)
		return
	}
	if refused, ok := errors.AsType[*domain.StatementKindError](err); ok {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("%v: %v; %s",
			domain.ErrStatementNotAllowedOnEndpoint, refused, statementKindAdvice(refused))})
		return
	}
	switch {
	case errors.Is(err, domain.ErrInvalidSQL):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrMissingParameters):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error() + "; pass them in args"})
	case errors.Is(err, domain.ErrDatabaseNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
	case errors.Is(err, domain.ErrNotMember):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrAccessDenied):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: "access denied"})
	case errors.Is(err, domain.ErrDatabaseReadOnly):
		// Transient: the database is mid-rebalance drain. Signal retry-later rather than a hard failure.
		w.Header().Set("Retry-After", "5")
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrUnsupportedStatement):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrStatementNotAllowedOnEndpoint):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrTooManyStatements):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrTooManyRows):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrChangeLogEntryNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrNothingToUndo):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrNothingToRedo):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrSchemaConflict):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrReservedTable):
		httputil.RespondJSON(w, http.StatusForbidden, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrWithoutRowIDNotSupported):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrStorageQuotaExceeded):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrBlobUnsupported):
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrDatabaseFull):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrQueryExecution):
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrQueryTimeout):
		httputil.RespondJSON(w, http.StatusGatewayTimeout, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrQueryResultTooLarge):
		httputil.RespondJSON(w, http.StatusRequestEntityTooLarge, errorResponse{Error: err.Error()})
	case errors.Is(err, domain.ErrNewerPlatformSchema):
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
	default:
		h.logger.Error().Err(err).Str("user_id", userID.String()).Msg("query execution failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
	}
}

// respondRateLimited answers a refused request with 429 and a Retry-After header rounded up to whole seconds.
func respondRateLimited(w http.ResponseWriter, limited *domain.QueryRateLimitedError) {
	retryAfterSecs := int64((limited.RetryAfter + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSecs, 10))
	httputil.RespondJSON(w, http.StatusTooManyRequests, errorResponse{Error: limited.Error()})
}
