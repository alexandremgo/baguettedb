package domain

// ParseResult is what the classifier reads after a single prepare-only walk
type ParseResult struct {
	// IsReadOnly is true when the parsed statement performs no
	// mutations (SQLite's sqlite3_stmt_readonly).
	IsReadOnly bool

	// IsDML is true when prepare recorded a top-level INSERT, UPDATE,
	// or DELETE. Trigger-fired DML does not count.
	IsDML bool

	// DMLVerbs lists the distinct top-level write verbs prepare recorded (e.g. both insert and
	// update for an upsert). Empty for non-DML statements; trigger-fired DML does not count.
	DMLVerbs []StatementVerb

	// Empty is true when prepare consumed only whitespace or comments
	// and produced no statement handle. Tail still describes any
	// remainder so a multi-statement walker can advance.
	Empty bool

	// Tail is the SQL text remaining after the parsed statement. A
	// non-empty tail (beyond whitespace) indicates multi-statement input.
	Tail string

	// Tables lists the names referenced during prepare, in
	// authorizer-recording order. Includes index/trigger/view names
	// for DROP X (the dropped object surfaces here, not its parent).
	// Duplicates are not removed at this layer — consumers dedupe.
	Tables []string

	// CreatedObjects lists the names of schema objects the statement
	// creates (table, index, trigger, view, or virtual table names on
	// CREATE), in authorizer-recording order. Empty for non-CREATE
	// statements. ALTER rename/add targets are not reported by the
	// authorizer and so never surface here.
	CreatedObjects []string

	// DDLKind is the DDL kind recorded during prepare. DDLUnknown for
	// non-DDL statements.
	DDLKind DDLType

	// DenyReason is set when the authorizer rejected the prepare.
	// DenyNone when prepare succeeded or failed for non-authorization
	// reasons (syntax error, schema mismatch).
	DenyReason DenyReason

	// ParamNames lists the statement's bind parameters in positional index
	// order (index 1 first). A named parameter carries its sigil-stripped
	// name (":status" → "status"); an anonymous "?" yields an empty string.
	// SQLite assigns each distinct named parameter a single index even when
	// it repeats in the SQL, so entries are unique per index, not per textual
	// occurrence.
	ParamNames []string

	// ResultColumns lists the statement's result column names in positional
	// order. Empty for statements that return no rows. An aliased column
	// carries its alias; an unaliased expression's name is unspecified by
	// SQLite, so consumers should only match columns the author aliased.
	ResultColumns []string
}

// DenyReason explains why the authorizer rejected a prepare. Used to
// disambiguate auth-denial errors when mapping to domain errors —
// reserved-prefix violations should surface as ErrReservedTable (403),
// while ATTACH/DETACH and disallowed PRAGMAs should surface as
// ErrUnsupportedStatement (400).
type DenyReason int

const (
	// DenyNone — no denial (prepare succeeded, or failed for a
	// non-authorization reason).
	DenyNone DenyReason = iota

	// DenyReservedPrefix — tenant SQL targeted a `_`-prefixed table,
	// index, trigger, or view name reserved for platform use.
	DenyReservedPrefix

	// DenyAttachDetach — ATTACH or DETACH DATABASE statement.
	DenyAttachDetach

	// DenyTransactionControl — BEGIN, COMMIT, ROLLBACK, SAVEPOINT, or
	// RELEASE issued by tenant SQL. Transaction boundaries are managed
	// by the platform.
	DenyTransactionControl

	// DenyPragmaNotAllowed — PRAGMA write, or PRAGMA read not in the
	// introspection allowlist.
	DenyPragmaNotAllowed

	// DenyVTableModuleNotAllowed — CREATE VIRTUAL TABLE targeting a
	// module not on the allowlist (only fts5 / fts5vocab are permitted).
	DenyVTableModuleNotAllowed

	// DenyTempObject — CREATE TEMP TABLE / INDEX / TRIGGER / VIEW issued by tenant SQL. Temporary objects live on
	// the shared per-database connection rather than in the bdb file.
	DenyTempObject

	// DenyReindex — REINDEX issued by tenant SQL. Index maintenance is managed by the platform.
	DenyReindex

	// DenyOther — fail-safe denial for unhandled action codes (ANALYZE, future SQLite action codes we haven't
	// classified).
	DenyOther
)
