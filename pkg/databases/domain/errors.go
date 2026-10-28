package domain

import (
	"errors"
	"fmt"
	"strings"
)

// SQLite extended result codes used by the predicates below. The numeric
// values are stable across SQLite versions and are documented at
// https://sqlite.org/rescode.html.
const (
	sqliteCodeConstraintUnique     = 2067 // SQLITE_CONSTRAINT_UNIQUE
	sqliteCodeConstraintForeignKey = 787  // SQLITE_CONSTRAINT_FOREIGNKEY
	sqliteCodeFull                 = 13   // SQLITE_FULL
	sqliteCodePrimaryBusy          = 5    // SQLITE_BUSY (primary)
	sqliteCodePrimaryMask          = 0xFF // primary code lives in the low byte
)

// QueryExecutionError is the typed error returned when the storage engine
// rejects a statement. Code carries SQLite's extended result code (e.g.
// 2067 for SQLITE_CONSTRAINT_UNIQUE); the primary code can be obtained via
// Code & 0xFF.
//
// Satisfies errors.Is(err, ErrQueryExecution) so existing matchers keep
// working, and additionally the typed sentinels: errors.Is(err, ErrUniqueViolation)
// when the underlying SQLite code is SQLITE_CONSTRAINT_UNIQUE, and
// errors.Is(err, ErrBlobUnsupported) when SQLite refused to render a BLOB into
// the change log's JSON.
type QueryExecutionError struct {
	Code    int
	Message string
}

// Error returns SQLite's message alone, as InvalidSQLError does: each surface says on its own that a statement failed,
// and Code stays readable for callers that branch on it.
func (e *QueryExecutionError) Error() string {
	return e.Message
}

// Is reports that *QueryExecutionError satisfies ErrQueryExecution and the
// typed sentinels (ErrUniqueViolation, ErrBlobUnsupported, ErrDatabaseFull)
// when their predicate fires.
func (e *QueryExecutionError) Is(target error) bool {
	switch target {
	case ErrQueryExecution:
		return true
	case ErrUniqueViolation:
		return e.IsUniqueConstraint()
	case ErrBlobUnsupported:
		return e.IsBlobUnsupported()
	case ErrDatabaseFull:
		return e.IsDatabaseFull()
	}
	return false
}

// IsDatabaseFull reports whether the error is SQLite refusing to grow the database file past its page cap, which is
// how any write — a reversal re-adding data included — fails once the bdb reaches its size limit.
func (e *QueryExecutionError) IsDatabaseFull() bool {
	return e.Code&sqliteCodePrimaryMask == sqliteCodeFull
}

// IsBlobUnsupported reports whether the error is SQLite refusing to render a BLOB value into JSON, which is how a
// write of a non-NULL BLOB fails inside the change-log triggers: the row's values are recorded with json_object(),
// and JSON has no binary type. SQLite reports it under the generic error code, so the message is the only handle.
func (e *QueryExecutionError) IsBlobUnsupported() bool {
	return strings.Contains(e.Message, "JSON cannot hold BLOB values")
}

// IsUniqueConstraint reports whether the error is a UNIQUE constraint violation.
func (e *QueryExecutionError) IsUniqueConstraint() bool {
	return e.Code == sqliteCodeConstraintUnique
}

// IsFKConstraint reports whether the error is a FOREIGN KEY constraint violation.
func (e *QueryExecutionError) IsFKConstraint() bool {
	return e.Code == sqliteCodeConstraintForeignKey
}

// IsBusy reports whether the error is SQLITE_BUSY (primary code, regardless of
// extended sub-kind such as SQLITE_BUSY_SNAPSHOT).
func (e *QueryExecutionError) IsBusy() bool {
	return e.Code&sqliteCodePrimaryMask == sqliteCodePrimaryBusy
}

var (
	// ErrDatabaseNotFound indicates that no database was found for the given criteria.
	ErrDatabaseNotFound = errors.New("database not found")

	// ErrDatabaseFileMissing indicates that the database has no file on local disk although it is known: deleted
	// concurrently with a backup pass, which is "nothing to snapshot", or lost, which an open reports rather than
	// answer from an empty file.
	ErrDatabaseFileMissing = errors.New("database file missing")

	// ErrDatabaseFileExists indicates that the database already has a live file on local disk and the caller
	// did not ask to overwrite it.
	ErrDatabaseFileExists = errors.New("database file already exists")

	// ErrNoSnapshots indicates that the snapshot store holds no snapshot for the database.
	ErrNoSnapshots = errors.New("no snapshots for database")

	// ErrSnapshotNotFound indicates that the requested snapshot object does not exist — e.g. pruned by the
	// store's lifecycle rule between a listing and the download.
	ErrSnapshotNotFound = errors.New("snapshot object not found")

	// ErrAccessDenied indicates that the requesting user does not have permission.
	ErrAccessDenied = errors.New("access denied")

	// ErrNotMember indicates a user who is not a member of the bdb at all, or a bdb that doesn't exist, which reads
	// the same. Also an ErrAccessDenied.
	ErrNotMember = fmt.Errorf("%w: you are not a member of this bdb", ErrAccessDenied)

	// ErrDatabaseReadOnly indicates that a write was attempted against a database currently in read-only mode
	// (e.g. during a rebalance move). Reads are unaffected; the write is retryable once the move completes. The
	// message is phrased so it reads as retryable wherever it surfaces verbatim — notably MCP tool-result errors,
	// where an agent only sees the text, not the HTTP status.
	ErrDatabaseReadOnly = errors.New("database is temporarily read-only; retry shortly")

	// ErrDatabaseOwnedHere guards the destructive move primitives: a pod refuses to delete its local file or to
	// overwrite it by adopting a snapshot while this shard still owns the database (pod_shard == this ordinal).
	// Those operations are only safe once ownership has moved to another shard, so a same-shard call signals an
	// incomplete or misdirected hand-off and is rejected rather than risking data loss.
	ErrDatabaseOwnedHere = errors.New("database is owned by this shard")

	// ErrStorageQuotaExceeded indicates the bdb owner's total storage across all their bdbs has reached
	// its cap, so size-growing writes and new database provisioning are rejected. Reads, deletes, and
	// drops stay allowed; the message says so because MCP agents only see the text, not the status code.
	// Deleted data keeps counting until its undo history expires (change-log retention), so the message
	// promises recovery without promising it is instant.
	ErrStorageQuotaExceeded = errors.New(
		"total storage quota reached for the bdb owner; reads and deletes remain available — " +
			"deleting data frees quota once its undo history expires")

	// ErrQueryExecution indicates that the SQL statement failed to execute.
	ErrQueryExecution = errors.New("query execution failed")

	// ErrUniqueViolation indicates that a statement failed a UNIQUE constraint.
	// Returned by errors.Is on a *QueryExecutionError whose Code is
	// SQLITE_CONSTRAINT_UNIQUE (2067).
	ErrUniqueViolation = errors.New("unique constraint violation")

	// ErrBlobUnsupported indicates that a write carried a non-NULL BLOB value. The change log records row values as
	// JSON, which has no binary type, so BLOBs cannot be stored in bdb tables; binary belongs in a TEXT column as
	// base64 or hex. Also returned by errors.Is on a *QueryExecutionError carrying SQLite's refusal.
	ErrBlobUnsupported = errors.New("BLOB values are not supported in bdb tables; store binary as base64 or hex text")

	// ErrDatabaseFull indicates that the bdb has reached its size limit, so the write — or the reversal re-adding
	// data — cannot allocate the pages it needs. Also returned by errors.Is on a *QueryExecutionError carrying
	// SQLite's refusal.
	ErrDatabaseFull = errors.New(
		"the bdb has reached its size limit; delete rows or drop tables to free space before writing again")

	// ErrNewerPlatformSchema indicates a bdb file upgraded by a newer version than the running one, which refuses it
	// rather than read and write it in the shape it knows. Also returned by errors.Is on a *NewerPlatformSchemaError.
	ErrNewerPlatformSchema = errors.New("this bdb was upgraded by a newer version than the one running")

	// ErrDatabaseCreation indicates that the per-bdb SQLite database could not be created.
	ErrDatabaseCreation = errors.New("database creation failed")

	// ErrUnsupportedStatement indicates that the SQL statement is not supported (e.g., ATTACH, DETACH).
	ErrUnsupportedStatement = errors.New("unsupported statement")

	// ErrStatementNotAllowedOnEndpoint indicates a statement type mismatch with the endpoint intent. Also returned by
	// errors.Is on a *StatementKindError.
	ErrStatementNotAllowedOnEndpoint = errors.New("statement not allowed on this endpoint")

	// ErrInvalidSQL indicates SQL that SQLite cannot prepare: a syntax error, an unknown table or column. Also returned
	// by errors.Is on an *InvalidSQLError.
	ErrInvalidSQL = errors.New("invalid SQL")

	// ErrMissingParameters indicates SQL with parameter placeholders run without values for them. Also returned by
	// errors.Is on a *MissingParametersError.
	ErrMissingParameters = errors.New("SQL parameters without values")

	// ErrReservedTable indicates that the statement targets a platform-reserved table (name starts with '_').
	ErrReservedTable = errors.New("table is reserved by the platform")

	// ErrWithoutRowIDNotSupported indicates that WITHOUT ROWID tables are not supported.
	ErrWithoutRowIDNotSupported = errors.New("WITHOUT ROWID tables are not supported")

	// ErrSkillNotFound indicates that no skill was found for the given criteria.
	ErrSkillNotFound = errors.New("skill not found")

	// ErrSkillNameTaken indicates that a skill with the given name already exists in the bdb.
	ErrSkillNameTaken = errors.New("skill name already taken")

	// ErrSkillNameInvalid indicates that the skill name does not match the expected slug format.
	ErrSkillNameInvalid = errors.New("skill name is invalid")

	// ErrSkillDescriptionInvalid indicates that the skill description is empty or too long.
	ErrSkillDescriptionInvalid = errors.New("skill description is invalid")

	// ErrSkillBodyInvalid indicates that the skill body is empty.
	ErrSkillBodyInvalid = errors.New("skill body is invalid")

	// ErrSkillBodyTooLarge indicates that the skill body exceeds SkillBodyMaxBytes.
	ErrSkillBodyTooLarge = errors.New("skill body too large")

	// ErrSkillTargetNotFound indicates that one or more target table names
	// do not exist in the bdb's database at create/update time.
	ErrSkillTargetNotFound = errors.New("skill target table does not exist")

	// ErrFormulaNotFound indicates that no formula was found for the given criteria.
	ErrFormulaNotFound = errors.New("formula not found")

	// ErrFormulaNameTaken indicates that a formula with the given name already exists in the bdb.
	ErrFormulaNameTaken = errors.New("formula name already taken")

	// ErrFormulaNameInvalid indicates that the formula name does not match the expected slug format.
	ErrFormulaNameInvalid = errors.New("formula name is invalid")

	// ErrFormulaDescriptionInvalid indicates that the formula description is empty or too long.
	ErrFormulaDescriptionInvalid = errors.New("formula description is invalid")

	// ErrFormulaLabelInvalid indicates that the formula label is too long or carries invisible characters.
	ErrFormulaLabelInvalid = errors.New("formula label is invalid")

	// ErrFormulaSQLInvalid indicates that the formula SQL is empty or too long.
	ErrFormulaSQLInvalid = errors.New("formula SQL is invalid")

	// ErrFormulaKindUndeterminable indicates that a formula's SQL could not be reduced to a
	// single view (SELECT) or action (INSERT/UPDATE/DELETE) statement — it was empty, held more
	// than one statement, was DDL, or was otherwise unsupported.
	ErrFormulaKindUndeterminable = errors.New("formula SQL must be a single SELECT or INSERT/UPDATE/DELETE statement")

	// ErrFormulaParamInvalid indicates that the formula's parameter schema violates a naming,
	// type, source, or reserved-name rule.
	ErrFormulaParamInvalid = errors.New("formula parameter schema is invalid")

	// ErrFormulaParamValue indicates a run given a value one of the formula's parameters refuses, or no value for a
	// required one; a *ParamValueError says which and why.
	ErrFormulaParamValue = errors.New("invalid formula parameter value")

	// ErrFormulaAttachTargetNotFound indicates that a formula's attached_to reference does not
	// resolve to an existing formula in the bdb.
	ErrFormulaAttachTargetNotFound = errors.New("formula attach target does not exist")

	// ErrFormulaAttachConflict indicates that an attachment and the formulas' kinds are
	// incompatible: only an action can attach, only to a view, and an attached pair keeps those
	// kinds until detached.
	ErrFormulaAttachConflict = errors.New("formula attachment conflicts with its kind")

	// ErrFormulaDisplayInvalid indicates that a view's display config violates a mode, column, or
	// action-slot rule.
	ErrFormulaDisplayInvalid = errors.New("formula display config is invalid")

	// ErrFormulaViewNotPageable indicates that a view declaring the reserved pagination parameters
	// returned no usable rowid column, so its pages could not be resumed by cursor.
	ErrFormulaViewNotPageable = errors.New("a paginated view must select `rowid AS rowid` so its pages can be resumed")

	// ErrFormulaRunTimeout indicates that running a formula from the app exceeded the per-run
	// deadline. The work is too heavy for an interactive run; the user should ask their AI
	// assistant to run it instead.
	ErrFormulaRunTimeout = errors.New("formula is too big to run in the app")

	// ErrQueryTimeout indicates that a SQL request exceeded the execution time budget, counting the time it
	// spent waiting for the bdb's connection.
	ErrQueryTimeout = errors.New("query exceeded the execution time limit (including time spent waiting for the " +
		"bdb's connection); narrow it or add an index")

	// ErrChangeLogEntryNotFound indicates that the requested _change_log entry (or batch) does not exist.
	ErrChangeLogEntryNotFound = errors.New("change log entry not found")

	// ErrNothingToUndo indicates that the undo stack is empty.
	ErrNothingToUndo = errors.New("nothing to undo")

	// ErrNothingToRedo indicates that the redo stack is empty.
	ErrNothingToRedo = errors.New("nothing to redo")

	// ErrTooManyStatements indicates that a request submitted more statements than the per-request cap allows.
	ErrTooManyStatements = errors.New("request exceeds maximum statements")

	// ErrTooManyRows indicates that the statements in a request collectively produced more _change_log rows than
	// the per-request cap allows.
	ErrTooManyRows = errors.New("request affects too many rows")

	// ErrTooManyConcurrentQueries indicates that the caller already has as many SQL requests in flight as their
	// concurrency cap allows. Returned wrapped in a QueryRateLimitedError carrying the suggested wait.
	ErrTooManyConcurrentQueries = errors.New("too many queries running for this user")

	// ErrQueryResultTooLarge indicates that a read returned more rows than the per-request result cap allows; the
	// request fails loud rather than truncating silently.
	ErrQueryResultTooLarge = errors.New("query returned more rows than allowed; add a LIMIT, aggregate, paginate " +
		"the view with :cursor/:limit, or export it as CSV")

	// ErrExportInFlight indicates an export for the same database is already running.
	ErrExportInFlight = errors.New("an export for this database is already in progress")

	// ErrExportLowSpace indicates the staging volume lacks the free space to produce an export.
	ErrExportLowSpace = errors.New("not enough free space to produce the export; retry later")

	// ErrExportRateLimited indicates the rolling-window export limit is reached. Returned wrapped in an
	// ExportRateLimitedError carrying the wait until the window frees a slot.
	ErrExportRateLimited = errors.New("export limit reached")

	// ErrFormulaExportNotView indicates a CSV export was requested for a formula that is not a view.
	ErrFormulaExportNotView = errors.New("only a view formula can be exported as CSV")

	// ErrFormulaExportTooLarge indicates a view's result crossed the export row cap; the export fails loud
	// rather than truncating silently.
	ErrFormulaExportTooLarge = errors.New("formula result exceeds the export row limit")

	// ErrFormulaExportTimeout indicates a formula CSV export exceeded the query duration ceiling.
	ErrFormulaExportTimeout = errors.New("formula took too long to export")

	// ErrSchemaConflict indicates that the schema changed between the time the
	// service read the current schema and the time the transaction ran. The caller
	// (or the surrounding retry loop) may attempt the operation again.
	ErrSchemaConflict = errors.New("schema conflict: another writer changed the schema concurrently")
)
