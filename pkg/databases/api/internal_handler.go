package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/httputil"
	"github.com/alexandremgo/baguettedb/pkg/databases/appconv"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// tablesWithSchemasRequest holds the JSON body for an internal tables-with-schemas request.
type tablesWithSchemasRequest struct {
	UserID string `json:"user_id"`
	BdbID  string `json:"bdb_id"`
}

// bdbDatabaseRequest holds the JSON body for internal endpoints scoped to a single bdb's database.
type bdbDatabaseRequest struct {
	BdbID string `json:"bdb_id"`
}

// executeSchemaInternalRequest holds the JSON body for an internal schema-execution request.
type executeSchemaInternalRequest struct {
	UserID     string   `json:"user_id"`
	BdbID      string   `json:"bdb_id"`
	Statements []string `json:"statements"`
}

// executeDataInternalRequest holds the JSON body for an internal data-execution request: one statement with
// its positional bound values, typed as the public execute endpoint types them.
type executeDataInternalRequest struct {
	UserID string         `json:"user_id"`
	BdbID  string         `json:"bdb_id"`
	SQL    string         `json:"sql"`
	Args   []valueRequest `json:"args,omitempty"`
}

// createFormulaInternalRequest holds the JSON body for an internal create-formula request. Params reuses the
// public create-formula parameter payload; AttachedToFormulaID is nil for a top-level formula.
type createFormulaInternalRequest struct {
	UserID              string                `json:"user_id"`
	BdbID               string                `json:"bdb_id"`
	Name                string                `json:"name"`
	Label               string                `json:"label,omitempty"`
	Description         string                `json:"description"`
	SQL                 string                `json:"sql"`
	Params              []formulaParamRequest `json:"params,omitempty"`
	AttachedToFormulaID *string               `json:"attached_to_formula_id,omitempty"`
	Hidden              bool                  `json:"hidden,omitempty"`
}

// createFormulaInternalResponse carries the created formula's ID back to the internal caller.
type createFormulaInternalResponse struct {
	ID string `json:"id"`
}

// updateFormulaInternalRequest holds the JSON body for an internal update-formula request: the display config
// to store, replacing any existing one. Slot refs may be formula names — the service resolves them against the
// view's attached formulas and stores UUIDs, exactly as the public PUT does.
type updateFormulaInternalRequest struct {
	UserID  string             `json:"user_id"`
	BdbID   string             `json:"bdb_id"`
	Ref     string             `json:"ref"`
	Display *formulaDisplayDTO `json:"display"`
}

// createSkillInternalRequest holds the JSON body for an internal create-skill request.
type createSkillInternalRequest struct {
	UserID      string   `json:"user_id"`
	BdbID       string   `json:"bdb_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Targets     []string `json:"targets,omitempty"`
}

// snapshotRequest holds the JSON body for an internal on-demand snapshot request.
type snapshotRequest struct {
	DatabaseID string `json:"database_id"`
}

// snapshotResponse describes the snapshot object produced by an on-demand snapshot.
type snapshotResponse struct {
	Key       string    `json:"key"`
	TakenAt   time.Time `json:"taken_at"`
	SizeBytes int64     `json:"size_bytes"`
}

// adoptRequest holds the JSON body for adopting a specific snapshot during a move hand-off.
type adoptRequest struct {
	DatabaseID string `json:"database_id"`
	Key        string `json:"key"`
}

// completeMoveRequest holds the JSON body for the move hand-off: the database and its new owner (color, shard).
type completeMoveRequest struct {
	DatabaseID string `json:"database_id"`
	Color      int16  `json:"color"`
	Shard      int16  `json:"shard"`
}

// snapshotSweepResponse reports the outcome of an on-demand backup sweep.
type snapshotSweepResponse struct {
	Scanned            int `json:"scanned"`
	Uploaded           int `json:"uploaded"`
	SkippedClean       int `json:"skipped_clean"`
	SkippedMissingFile int `json:"skipped_missing_file"`
	SkippedLowSpace    int `json:"skipped_low_space"`
	SkippedForeign     int `json:"skipped_foreign"`
	Failed             int `json:"failed"`
}

// InternalHandler handles internal service-to-service endpoints.
type InternalHandler struct {
	querySvc   *service.QueryService
	skillSvc   *service.SkillService
	formulaSvc *service.FormulaService
	dbSvc      *service.DatabaseService

	// backupSvc is nil when snapshot storage is not configured; the snapshot endpoints then respond 503.
	backupSvc *service.BackupService

	logger zerolog.Logger
}

// NewInternalHandler creates a new InternalHandler.
func NewInternalHandler(
	querySvc *service.QueryService,
	skillSvc *service.SkillService,
	formulaSvc *service.FormulaService,
	dbSvc *service.DatabaseService,
	backupSvc *service.BackupService,
	logger zerolog.Logger,
) *InternalHandler {
	return &InternalHandler{
		querySvc:   querySvc,
		skillSvc:   skillSvc,
		formulaSvc: formulaSvc,
		dbSvc:      dbSvc,
		backupSvc:  backupSvc,
		logger:     logger,
	}
}

// TablesWithSchemas handles POST /internal/api/databases/v1/tables-with-schemas.
// Returns all tables with their full schema (columns and foreign keys) for the given bdb.
func (h *InternalHandler) TablesWithSchemas(w http.ResponseWriter, r *http.Request) {
	var req tablesWithSchemasRequest
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

	schemas, err := h.querySvc.ListTablesWithSchemas(r.Context(), userID, bdbID)
	if err != nil {
		h.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Msg("Internal tables-with-schemas failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.TableSchemas(schemas))
}

// ListTables handles POST /internal/api/databases/v1/tables.
// Returns the lightweight {name, type} list of user-created tables for the
// given bdb. Used by the bdbs HTTP DatabasesApp adapter for orientation
// tools that don't need column-level schemas.
func (h *InternalHandler) ListTables(w http.ResponseWriter, r *http.Request) {
	var req tablesWithSchemasRequest
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

	infos, err := h.querySvc.ListTablesInfo(r.Context(), userID, bdbID)
	if err != nil {
		h.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Msg("Internal list-tables failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.TableInfos(infos))
}

// ListSkills handles POST /internal/api/databases/v1/skills.
// Returns all skills (with their target tables) for the given bdb. Used by
// the bdbs HTTP DatabasesApp adapter to enrich list_bdbs responses.
func (h *InternalHandler) ListSkills(w http.ResponseWriter, r *http.Request) {
	var req tablesWithSchemasRequest
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

	skills, err := h.skillSvc.ListSkills(r.Context(), userID, bdbID)
	if err != nil {
		h.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Msg("Internal list-skills failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.Skills(skills))
}

// ListFormulas handles POST /internal/api/databases/v1/formulas.
// Returns the bdb's formulas as lightweight orientation summaries (no SQL or
// parameter schema).
func (h *InternalHandler) ListFormulas(w http.ResponseWriter, r *http.Request) {
	var req tablesWithSchemasRequest
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

	formulas, err := h.formulaSvc.ListFormulasIncludingHidden(r.Context(), userID, bdbID)
	if err != nil {
		h.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Msg("Internal list-formulas failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, appconv.Formulas(formulas))
}

// ExecuteSchema handles POST /internal/api/databases/v1/schema/execute.
// Runs each DDL statement through the same schema-intent path as the public execute-schema endpoint, acting as
// the given user. Statements run one at a time so later DDL can reference objects created by earlier ones; on
// the first failure the response is a 422 carrying the statement index and the real error, since the internal
// caller reports it to whoever authored the schema.
func (h *InternalHandler) ExecuteSchema(w http.ResponseWriter, r *http.Request) {
	var req executeSchemaInternalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	userID, bdbID, ok := parseInternalIDs(w, req.UserID, req.BdbID)
	if !ok {
		return
	}

	for i, stmt := range req.Statements {
		if _, err := h.querySvc.Execute(r.Context(), userID, bdbID, stmt, nil, domain.IntentSchema); err != nil {
			h.logger.Warn().Err(err).
				Str("user_id", userID.String()).
				Str("bdb_id", bdbID.String()).
				Int("statement", i).
				Msg("Internal schema execution failed")
			httputil.RespondJSON(w, http.StatusUnprocessableEntity,
				errorResponse{Error: fmt.Sprintf("statement %d: %s", i, err)})
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExecuteData handles POST /internal/api/databases/v1/data/execute.
// Runs one data statement with its bound values through the same data-intent path as the public execute-data
// endpoint, acting as the given user. A malformed argument is a 400; a failing statement is a 422 carrying the
// real error, since the internal caller reports it to whoever authored the statement.
func (h *InternalHandler) ExecuteData(w http.ResponseWriter, r *http.Request) {
	var req executeDataInternalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	userID, bdbID, ok := parseInternalIDs(w, req.UserID, req.BdbID)
	if !ok {
		return
	}
	args, err := toTypedValues(req.Args)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if _, err := h.querySvc.Execute(r.Context(), userID, bdbID, req.SQL, args, domain.IntentExecute); err != nil {
		h.logger.Warn().Err(err).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Msg("Internal data execution failed")
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateFormula handles POST /internal/api/databases/v1/formulas/create.
// Creates one formula acting as the given user, running the same validation and classification as the public
// endpoint, and returns the new formula's ID. Creation failures respond 422 with the real error text.
func (h *InternalHandler) CreateFormula(w http.ResponseWriter, r *http.Request) {
	var req createFormulaInternalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	userID, bdbID, ok := parseInternalIDs(w, req.UserID, req.BdbID)
	if !ok {
		return
	}
	var attachedToID *uuid.UUID
	if req.AttachedToFormulaID != nil {
		parsed, err := uuid.Parse(*req.AttachedToFormulaID)
		if err != nil {
			httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid attached_to_formula_id"})
			return
		}
		attachedToID = &parsed
	}

	created, err := h.formulaSvc.CreateFormula(r.Context(), userID, bdbID, service.FormulaInput{
		Name:                req.Name,
		Label:               req.Label,
		Description:         req.Description,
		SQL:                 req.SQL,
		Params:              toParamSchema(req.Params),
		AttachedToFormulaID: attachedToID,
		Hidden:              req.Hidden,
	})
	if err != nil {
		h.logger.Warn().Err(err).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Str("formula_name", req.Name).
			Msg("Internal formula creation failed")
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	httputil.RespondJSON(w, http.StatusOK, createFormulaInternalResponse{ID: created.ID.String()})
}

// UpdateFormula handles POST /internal/api/databases/v1/formulas/update.
// Stores a formula's display config acting as the given user, through the same service update (and so the same
// validation and slot resolution) as the public PUT. Update failures respond 422 with the real error text.
func (h *InternalHandler) UpdateFormula(w http.ResponseWriter, r *http.Request) {
	var req updateFormulaInternalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	userID, bdbID, ok := parseInternalIDs(w, req.UserID, req.BdbID)
	if !ok {
		return
	}
	if req.Ref == "" || req.Display == nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "ref and display are required"})
		return
	}

	_, err := h.formulaSvc.UpdateFormula(r.Context(), userID, bdbID, req.Ref, service.FormulaPatch{
		Display: toDomainDisplay(req.Display),
	})
	if err != nil {
		h.logger.Warn().Err(err).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Str("formula_ref", req.Ref).
			Msg("Internal formula update failed")
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CreateSkill handles POST /internal/api/databases/v1/skills/create.
// Creates one skill acting as the given user, running the same validation as the public endpoint. Creation
// failures respond 422 with the real error text.
func (h *InternalHandler) CreateSkill(w http.ResponseWriter, r *http.Request) {
	var req createSkillInternalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	userID, bdbID, ok := parseInternalIDs(w, req.UserID, req.BdbID)
	if !ok {
		return
	}

	if _, err := h.skillSvc.CreateSkill(r.Context(), userID, bdbID, service.SkillInput{
		Name:         req.Name,
		Description:  req.Description,
		Body:         req.Body,
		TargetTables: req.Targets,
	}); err != nil {
		h.logger.Warn().Err(err).
			Str("user_id", userID.String()).
			Str("bdb_id", bdbID.String()).
			Str("skill_name", req.Name).
			Msg("Internal skill creation failed")
		httputil.RespondJSON(w, http.StatusUnprocessableEntity, errorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseInternalIDs parses the user and bdb IDs shared by every acting-as-user internal request, responding 400
// on the first invalid one.
func parseInternalIDs(w http.ResponseWriter, rawUserID, rawBdbID string) (userID, bdbID uuid.UUID, ok bool) {
	userID, err := uuid.Parse(rawUserID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid user_id"})
		return uuid.Nil, uuid.Nil, false
	}
	bdbID, err = uuid.Parse(rawBdbID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid bdb_id"})
		return uuid.Nil, uuid.Nil, false
	}
	return userID, bdbID, true
}

// SnapshotDatabase handles POST /internal/api/databases/v1/snapshot.
// Snapshots one database to the object store immediately, bypassing the dirty-check. Used by pre-deploy tooling
// and by the rebalancer's release flow.
func (h *InternalHandler) SnapshotDatabase(w http.ResponseWriter, r *http.Request) {
	if h.backupSvc == nil {
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "snapshot storage not configured"})
		return
	}

	var req snapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	databaseID, err := uuid.Parse(req.DatabaseID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid database_id"})
		return
	}

	info, err := h.backupSvc.BackupDatabase(r.Context(), databaseID, time.Now().UTC())
	switch {
	case errors.Is(err, domain.ErrDatabaseNotFound):
		httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
		return
	case errors.Is(err, domain.ErrDatabaseFileMissing):
		httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "database has no file on this instance"})
		return
	case err != nil:
		h.logger.Error().Err(err).
			Str("database_id", databaseID.String()).
			Msg("Internal snapshot failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, snapshotResponse{
		Key:       info.Key,
		TakenAt:   info.TakenAt,
		SizeBytes: info.SizeBytes,
	})
}

// SnapshotSweep handles POST /internal/api/databases/v1/snapshot-sweep.
// Runs one backup sweep immediately — every dirty or stale database is snapshotted and uploaded. Used as the
// pre-deploy "snapshot-all dirty" gate.
func (h *InternalHandler) SnapshotSweep(w http.ResponseWriter, r *http.Request) {
	if h.backupSvc == nil {
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "snapshot storage not configured"})
		return
	}

	stats, err := h.backupSvc.SweepOnce(r.Context(), time.Now().UTC())
	if err != nil {
		h.logger.Error().Err(err).Msg("internal snapshot sweep failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, snapshotSweepResponse{
		Scanned:            stats.Scanned,
		Uploaded:           stats.Uploaded,
		SkippedClean:       stats.SkippedClean,
		SkippedMissingFile: stats.SkippedMissingFile,
		SkippedLowSpace:    stats.SkippedLowSpace,
		SkippedForeign:     stats.SkippedForeign,
		Failed:             stats.Failed,
	})
}

// ReleaseDatabase handles POST /internal/api/databases/v1/release — the source side of a move. It marks the
// database read_only, evicts this pod's cached connection, takes a fresh snapshot, uploads it, and returns the
// snapshot ref the new owner adopts. read_only is left set on failure (the orchestrator retries or aborts) so
// writes are never served from a half-moved database.
//
// Idempotent when the live file is already gone (an already-drained source, or a pod on a wiped volume): rather
// than failing, it falls back to the latest stored snapshot so the move still completes — the hand-off then flips
// ownership and clears read_only, which stops the source being re-selected for the drain on every retry. Only when
// there is no snapshot either — genuine, unrecoverable data loss — does it fail, with a distinct 410 so the
// orchestrator stops loudly instead of looping forever on the same database.
func (h *InternalHandler) ReleaseDatabase(w http.ResponseWriter, r *http.Request) {
	if h.backupSvc == nil {
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "snapshot storage not configured"})
		return
	}
	var req snapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	databaseID, err := uuid.Parse(req.DatabaseID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid database_id"})
		return
	}

	if err := h.dbSvc.SetReadOnly(r.Context(), databaseID, true); err != nil {
		if errors.Is(err, domain.ErrDatabaseNotFound) {
			httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
			return
		}
		h.logger.Error().Err(err).Str("database_id", databaseID.String()).Msg("release: setting read_only failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	if err := h.dbSvc.EvictDatabase(r.Context(), databaseID); err != nil {
		h.logger.Error().Err(err).Str("database_id", databaseID.String()).Msg("release: evict failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	info, err := h.backupSvc.BackupDatabase(r.Context(), databaseID, time.Now().UTC())
	switch {
	case errors.Is(err, domain.ErrDatabaseFileMissing):
		// No live file to snapshot — recover the move from the latest stored snapshot (see the idempotency note
		// on the handler). Only a database with no snapshot either is unrecoverable → distinct 410.
		latest, lerr := h.backupSvc.LatestSnapshot(r.Context(), databaseID)
		if errors.Is(lerr, domain.ErrNoSnapshots) {
			httputil.RespondJSON(w, http.StatusGone, errorResponse{
				Error: "database has no file on this instance and no snapshot to recover from",
			})
			return
		}
		if lerr != nil {
			h.logger.Error().Err(lerr).Str("database_id", databaseID.String()).
				Msg("Release: resolving latest snapshot failed")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
			return
		}
		h.logger.Warn().Str("database_id", databaseID.String()).Str("snapshot_key", latest.Key).
			Msg("Release: live file missing, recovering the move from the latest snapshot")
		info = latest
	case err != nil:
		h.logger.Error().Err(err).Str("database_id", databaseID.String()).Msg("release: snapshot failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	httputil.RespondJSON(w, http.StatusOK, snapshotResponse{
		Key: info.Key, TakenAt: info.TakenAt, SizeBytes: info.SizeBytes,
	})
}

// AdoptDatabase handles POST /internal/api/databases/v1/adopt — the target side. It downloads the given snapshot
// ref, installs it as this pod's live file, and verifies it opens with the bdb's DEK.
func (h *InternalHandler) AdoptDatabase(w http.ResponseWriter, r *http.Request) {
	if h.backupSvc == nil {
		httputil.RespondJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "snapshot storage not configured"})
		return
	}
	var req adoptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	databaseID, err := uuid.Parse(req.DatabaseID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid database_id"})
		return
	}
	if req.Key == "" {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "key is required"})
		return
	}

	if err := h.backupSvc.AdoptSnapshot(r.Context(), databaseID, req.Key); err != nil {
		switch {
		case errors.Is(err, domain.ErrDatabaseOwnedHere):
			httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "database is owned by this shard"})
		case errors.Is(err, domain.ErrDatabaseNotFound):
			httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
		case errors.Is(err, domain.ErrSnapshotNotFound):
			httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "snapshot not found"})
		default:
			h.logger.Error().Err(err).Str("database_id", databaseID.String()).Msg("adopt: installing snapshot failed")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		}
		return
	}
	if err := h.dbSvc.VerifyDatabaseOpens(r.Context(), databaseID); err != nil {
		h.logger.Error().Err(err).Str("database_id", databaseID.String()).Msg("adopt: verify open failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "adopted file failed to open"})
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "adopted"})
}

// CompleteMove handles POST /internal/api/databases/v1/complete-move — the move hand-off. It flips the database's
// owning pod_shard and clears read_only so the new owner serves writes. Keeping this server-side means the
// orchestrator never needs direct Postgres credentials.
func (h *InternalHandler) CompleteMove(w http.ResponseWriter, r *http.Request) {
	var req completeMoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	databaseID, err := uuid.Parse(req.DatabaseID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid database_id"})
		return
	}
	if err := h.dbSvc.CompleteMove(r.Context(), databaseID, req.Color, req.Shard); err != nil {
		if errors.Is(err, domain.ErrDatabaseNotFound) {
			httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
			return
		}
		h.logger.Error().Err(err).Str("database_id", databaseID.String()).Msg("CompleteMove failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "move completed"})
}

// DeleteLocalDatabase handles POST /internal/api/databases/v1/delete-local — cleanup after a move. It removes the
// source pod's now-orphaned database file (file only; the Postgres metadata, now pointing at the new owner, is
// untouched). Idempotent: a missing file is not an error.
func (h *InternalHandler) DeleteLocalDatabase(w http.ResponseWriter, r *http.Request) {
	var req snapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}
	databaseID, err := uuid.Parse(req.DatabaseID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid database_id"})
		return
	}
	if err := h.dbSvc.DeleteLocalFile(r.Context(), databaseID); err != nil {
		switch {
		case errors.Is(err, domain.ErrDatabaseOwnedHere):
			httputil.RespondJSON(w, http.StatusConflict, errorResponse{Error: "database is owned by this shard"})
		case errors.Is(err, domain.ErrDatabaseNotFound):
			httputil.RespondJSON(w, http.StatusNotFound, errorResponse{Error: "database not found"})
		default:
			h.logger.Error().Err(err).Str("database_id", databaseID.String()).Msg("DeleteLocal failed")
			httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		}
		return
	}
	httputil.RespondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// DeleteBdbDatabase handles POST /internal/api/databases/v1/bdb-database/delete.
// Removes the per-bdb SQLite database and the database record. Idempotent: 204 No Content even when no database
// exists for the bdb yet.
func (h *InternalHandler) DeleteBdbDatabase(w http.ResponseWriter, r *http.Request) {
	var req bdbDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid request body"})
		return
	}

	bdbID, err := uuid.Parse(req.BdbID)
	if err != nil {
		httputil.RespondJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid bdb_id"})
		return
	}

	if err := h.dbSvc.DeleteBdbDatabase(r.Context(), bdbID); err != nil {
		h.logger.Error().Err(err).
			Str("bdb_id", bdbID.String()).
			Msg("Internal delete-bdb-database failed")
		httputil.RespondJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal server error"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
