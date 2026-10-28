package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// snapshotter produces consistent encrypted snapshot files of per-bdb databases and reports their on-disk
// state.
type snapshotter interface {
	// Snapshot writes a consistent encrypted copy of the database to destPath (which must not exist yet).
	// Returns domain.ErrDatabaseFileMissing when the database has no file on disk.
	Snapshot(ctx context.Context, databaseID string, destPath string) error

	// Export writes a consistent copy of the database to destPath (which must not exist yet), re-encrypted
	// under passphrase instead of the database's own key, so the copy is openable outside the platform.
	// Returns domain.ErrDatabaseFileMissing when the database has no file on disk.
	Export(ctx context.Context, databaseID string, destPath string, passphrase string) error

	// GetDatabaseStats returns usage statistics for the given database, including the on-disk
	// last-modification time used by the backup dirty-check.
	GetDatabaseStats(ctx context.Context, databaseID string) (*domain.DatabaseStats, error)
}

// ParseHandle is the port for parse-only access to a bdb's SQLite
// connection. The classifier needs schema-bound parsing (SQLite's
// native parser resolves against the connection's schema during
// prepare), so consumers obtain a ParseHandle from
// queryExecutor.WithConn — or use the Tx of WithTx, which parses
// against the schema the transaction sees — and pass it to the
// classifier.
//
// Implementations are responsible for finalizing the underlying
// prepared statement before returning the ParseResult — callers
// receive pure data and have no cleanup obligation.
type ParseHandle interface {
	Parse(sql string) (domain.ParseResult, error)
}

// queryExecutor defines operations for executing SQL against a per-bdb SQLite database.
//
// Tenant-facing methods (Execute / ExecuteTransaction) should run with
// the reserved-prefix authorizer active — SQL writing to `_*` tables
// should be denied. Platform code uses the *Internal variants for SQL
// that touches platform tables (migrations, mutation/schema log,
// undo/redo stacks, skill CRUD).
type queryExecutor interface {
	// Execute runs a SQL statement against the given database and returns the result.
	Execute(ctx context.Context, databaseID string, sql string, args []domain.TypedValue) (*domain.QueryResult, error)

	// ExecuteWithRowCap is Execute for a read whose result may hold at most maxRows rows: the scan stops at the first
	// row past the cap and domain.ErrQueryResultTooLarge is returned. maxRows <= 0 means no cap.
	ExecuteWithRowCap(ctx context.Context, databaseID string, sql string, args []domain.TypedValue,
		maxRows int) (*domain.QueryResult, error)

	// ExecuteInternal is Execute with reserved-prefix deny rules bypassed.
	ExecuteInternal(ctx context.Context, databaseID string, sql string,
		args []domain.TypedValue) (*domain.QueryResult, error)

	// ExecuteTransaction runs multiple statements atomically in a single transaction.
	// Returns one result per statement. If any statement fails, all are rolled back.
	//
	// Use when the full statement list is known up-front (the list is
	// the natural representation). For transactions that need
	// mid-transaction observation, conditional rollback, or
	// dynamically generated statements, use WithTx instead.
	ExecuteTransaction(ctx context.Context, databaseID string,
		stmts []domain.SQLStatement) ([]*domain.QueryResult, error)

	// ExecuteTransactionInternal is ExecuteTransaction with deny rules bypassed.
	ExecuteTransactionInternal(ctx context.Context, databaseID string,
		stmts []domain.SQLStatement) ([]*domain.QueryResult, error)

	// WithConn calls fn with a parse handle backed by the database's connection, giving
	// schema-bound, prepare-only access without executing anything. SQLite resolves names
	// and assigns bind-parameter indices against the live schema during prepare, so this is
	// the way to inspect SQL in that context first — classify a statement, confirm a
	// referenced table exists, or read a statement's bind parameters — and decide what to do
	// before running it. The handle is valid only for the duration of fn.
	WithConn(ctx context.Context, databaseID string, fn func(h ParseHandle) error) error

	// WithTx opens a transaction on database's connection and runs fn
	// inside it. The adapter issues BEGIN before fn and COMMIT after fn
	// returns nil; if fn returns a non-nil error the transaction is
	// rolled back and the error is propagated.
	//
	// Use when the transaction needs mid-transaction observation
	// (e.g. count rows after each statement, decide whether to abort),
	// conditional rollback driven by intermediate state, or
	// dynamically generated statements. For transactions where the
	// full statement list is known up-front, ExecuteTransaction is
	// the simpler fit.
	//
	// internal controls whether the authorizer's tenant deny rules apply
	// to statements run via tx.Exec inside fn — pass true for platform
	// code that writes to `_*` tables (mutation log corrections,
	// undo/redo stack writes), false for tenant SQL.
	//
	// fn receives the (possibly timeout-bounded) ctx so callers can pass
	// it through to tx.Exec without remembering to use the outer ctx.
	WithTx(ctx context.Context, databaseID string, internal bool, fn func(ctx context.Context, tx Tx) error) error
}

// Tx is the handle yielded inside a WithTx callback. Each Exec call
// runs against the same connection that opened the transaction, so
// statements share the transaction's lock and visibility scope. A Tx
// also parses statements against that connection, so it satisfies
// ParseHandle and classification can run inside the transaction.
type Tx interface {
	// Exec runs sql with the given args and returns the result, inside the transaction opened by WithTx and under
	// the authorizer mode WithTx was opened with: reserved-object rules apply unless the transaction is internal.
	Exec(ctx context.Context, sql string, args []domain.TypedValue) (*domain.QueryResult, error)

	// ExecInternal runs sql with the given args inside the same transaction with the reserved-object rules bypassed
	// whatever mode WithTx was opened with, so platform bookkeeping can share a transaction with statements that
	// stay authorized.
	ExecInternal(ctx context.Context, sql string, args []domain.TypedValue) (*domain.QueryResult, error)

	// Parse prepares sql against the transaction's connection without
	// running it, returning the parse result for classification.
	Parse(sql string) (domain.ParseResult, error)
}

// internalExecutor runs single platform-table SQL statements with the reserved-prefix deny rules bypassed. It is the
// narrowest slice the change-log readers and conflict probes need, so the same code runs against the executor
// directly or against a transaction handle adapted to it.
type internalExecutor interface {
	// ExecuteInternal runs a SQL statement with reserved-prefix deny rules bypassed.
	ExecuteInternal(ctx context.Context, databaseID string, sql string,
		args []domain.TypedValue) (*domain.QueryResult, error)
}

// statementClassifier walks the input SQL and returns one classified
// entry per statement. Methods take a ParseHandle (yielded by
// queryExecutor.WithConn) because SQLite's native parser does schema
// resolution as part of prepare — classification needs a connection
// bound to the right per-bdb schema.
type statementClassifier interface {
	// Classify walks all statements in sql and returns one
	// ClassifiedStatement per parsed statement (with kind, DDL type,
	// and recorded tables). Errors on empty input; mid-walk errors
	// are prefixed with the 1-indexed statement number.
	Classify(h ParseHandle, sql string) ([]domain.ClassifiedStatement, error)

	// ClassifyAlterTable parses an ALTER TABLE statement and returns
	// its subtype + relevant names (subtype info isn't carried by
	// ClassifiedStatement).
	ClassifyAlterTable(h ParseHandle, sql string) (domain.AlterTableInfo, error)
}

// KEKProvider wraps and unwraps per-bdb DEKs using a key-encryption key.
// Implementations perform crypto only; callers (the service) emit audit logs
// around each call.
//
// Exported so that setup.go can accept it as a SetupOptions field, mirroring
// BdbsApp below.
type KEKProvider interface {
	// Wrap encrypts plaintext under the provider's KEK. The returned blob is
	// opaque to callers; format and authentication are implementation details.
	Wrap(plaintext []byte) ([]byte, error)

	// Unwrap decrypts a previously wrapped blob and returns the original
	// plaintext. Returns an error on any tampering, wrong-KEK, or malformed
	// input.
	Unwrap(ciphertext []byte) ([]byte, error)
}

// BdbsApp defines the subset of the bdbs app needed by databases.
// Exported so that setup.go can accept it as a parameter.
type BdbsApp interface {
	// CheckMemberPermission checks whether the user has the given database permission
	// on the bdb's database, based on their bdb role.
	CheckMemberPermission(
		ctx context.Context, userID, bdbID uuid.UUID, perm authz.DatabasePermission,
	) (bool, error)

	// GetBdbOwner returns the bdb owner's user ID and global roles.
	GetBdbOwner(ctx context.Context, bdbID uuid.UUID) (uuid.UUID, []authz.Role, error)

	// GetOwnedBdbIDs returns the IDs of every bdb the given user owns, including soft-deleted bdbs
	// still awaiting purge (their storage remains allocated until the purge removes it).
	GetOwnedBdbIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}

// DatabaseRepository defines persistence operations for database metadata.
// Exported because the app's setup and its integration suites name it to hold either instance store's adapter.
type DatabaseRepository interface {
	// Create persists a new database record.
	Create(ctx context.Context, db *domain.Database) error

	// FindByID returns the database with the given ID.
	// Returns domain.ErrDatabaseNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Database, error)

	// FindByBdb returns the database owned by the given bdb.
	// Returns domain.ErrDatabaseNotFound if not found.
	FindByBdb(ctx context.Context, bdbID uuid.UUID) (*domain.Database, error)

	// Delete removes the database record.
	// Returns domain.ErrDatabaseNotFound if not found.
	Delete(ctx context.Context, id uuid.UUID) error

	// UpdatePlatformSchemaVersion sets the platform schema version for a database.
	// Returns domain.ErrDatabaseNotFound if not found.
	UpdatePlatformSchemaVersion(ctx context.Context, id uuid.UUID, version int) error

	// SetReadOnly sets the read_only flag for a database.
	// Returns domain.ErrDatabaseNotFound if not found.
	SetReadOnly(ctx context.Context, id uuid.UUID, readOnly bool) error

	// UpdateOwnerAndReadOnly sets the read_only flag and the owner (pod_color + pod_shard) for a database.
	// Returns domain.ErrDatabaseNotFound if not found.
	UpdateOwnerAndReadOnly(ctx context.Context, id uuid.UUID, color, shard int16, readOnly bool) error

	// UpdateSize sets the tracked on-disk size for a database, stamping size_updated_at.
	// Returns domain.ErrDatabaseNotFound if not found.
	UpdateSize(ctx context.Context, id uuid.UUID, sizeBytes int64) error

	// SumSizeBytesByBdbIDs returns the total tracked size of the databases belonging to the given bdbs.
	// Unknown IDs contribute nothing; an empty list sums to 0.
	SumSizeBytesByBdbIDs(ctx context.Context, bdbIDs []uuid.UUID) (int64, error)

	// ListAll returns every database record.
	ListAll(ctx context.Context) ([]*domain.Database, error)

	// ListOwned returns every database record assigned to the given fleet color and shard.
	ListOwned(ctx context.Context, color, shard int16) ([]*domain.Database, error)

	// GetActiveColor returns the currently active blue/green fleet color (0 = blue, 1 = green).
	GetActiveColor(ctx context.Context) (int16, error)
}

// ExportRepository persists the audit trail of user-facing exports; its successful rows double as the
// rolling-window export rate-limit source.
// Exported because the app's setup and its integration suites name it to hold either instance store's adapter.
type ExportRepository interface {
	// RecordStart inserts the audit row for a starting export attempt.
	RecordStart(ctx context.Context, rec domain.ExportRecord) error

	// RecordOutcome sets the terminal outcome and streamed byte count of an export attempt, stamping its
	// finish time.
	RecordOutcome(ctx context.Context, id uuid.UUID, outcome domain.ExportOutcome, bytes int64) error

	// CountSuccessesSince returns how many exports of the given kind succeeded for (databaseID, userID)
	// since the given time, plus the started_at of the oldest counted attempt (nil when none).
	CountSuccessesSince(ctx context.Context, databaseID, userID uuid.UUID, kind domain.ExportKind,
		since time.Time) (int, *time.Time, error)
}
