//go:build cgo

package sqlitelocal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// Config bundles tunables for Client and its underlying connector cache.
type Config struct {
	// DataDir is the on-disk directory holding `<database>.db` files. Must
	// exist and be writable; Client does not create it.
	DataDir string

	// CacheTTL is the idle duration after which a cached connection is
	// closed by the sweeper. Bumped on every Use.
	CacheTTL time.Duration

	// CacheMaxEntries caps the number of simultaneously open connections.
	// Above this count, the LRU entry is evicted on insert.
	CacheMaxEntries int

	// CacheSweepEvery controls how often the sweeper wakes. Typically a
	// small fraction of CacheTTL.
	CacheSweepEvery time.Duration

	// DEKResolver fetches the raw 32-byte DEK for a database on cache miss.
	DEKResolver DEKResolver

	// BusyTimeout is how long a statement waits for a lock another connection or process holds on the same file
	// before failing with SQLITE_BUSY. Zero fails at once.
	BusyTimeout time.Duration

	// Logger receives WARN lines for operational anomalies (cleanup errors,
	// rollback failures, connection poisoning). Zero value is a no-op writer.
	Logger zerolog.Logger
}

// Client is the per-process owner of database files plus the connector
// cache. It implements databaseManager, queryExecutor, and the service's
// streaming row port (structural).
type Client struct {
	dataDir string
	cache   *connectorCache
	logger  zerolog.Logger

	// dekResolver is also held by the cache; Snapshot keeps its own reference because it opens dedicated
	// connections outside the cache.
	dekResolver DEKResolver

	busyTimeout time.Duration
}

// NewClient validates cfg, checks that DataDir is an existing directory, and starts the cache.
// Callers must invoke Close to release file handles + the sweeper goroutine.
func NewClient(cfg Config) (*Client, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("sqlitelocal: DataDir is required")
	}
	info, err := os.Stat(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("sqlitelocal: stat DataDir %q: %w", cfg.DataDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sqlitelocal: DataDir %q is not a directory", cfg.DataDir)
	}

	cache, err := newConnectorCache(cacheConfig{
		dataDir:     cfg.DataDir,
		ttl:         cfg.CacheTTL,
		maxEntries:  cfg.CacheMaxEntries,
		sweepEvery:  cfg.CacheSweepEvery,
		dekResolver: cfg.DEKResolver,
		busyTimeout: cfg.BusyTimeout,
		logger:      cfg.Logger,
	})
	if err != nil {
		return nil, err
	}

	return &Client{
		dataDir:     cfg.DataDir,
		cache:       cache,
		logger:      cfg.Logger,
		dekResolver: cfg.DEKResolver,
		busyTimeout: cfg.BusyTimeout,
	}, nil
}

// Close stops the sweeper and closes all cached connections. Idempotent.
func (c *Client) Close() error {
	return c.cache.Close()
}

// beginWrite opens every transaction on a bdb file. IMMEDIATE takes the write lock up front, so a transaction that
// reads before it writes waits on the busy timeout while another process writes, instead of failing with
// SQLITE_BUSY_SNAPSHOT when it upgrades to a writer: SQLite never retries that error through the busy handler.
// Transactions here are write paths; reads run as single statements outside them.
const beginWrite = "BEGIN IMMEDIATE"

// --- databaseManager ---

// CreateDatabase opens a fresh SQLCipher file at `<DataDir>/<database>.db`,
// applies the provided DEK as the raw key, switches the file to WAL mode, and
// seeds the cache so the first query skips the DEK-resolver round trip.
//
// Fails if the file already exists — databases are unique per bdb and a
// pre-existing file likely indicates a bug or a stale data dir.
func (c *Client) CreateDatabase(ctx context.Context, databaseID string, dek []byte) error {
	if len(dek) != KeySize {
		return fmt.Errorf("sqlitelocal: DEK must be %d bytes, got %d", KeySize, len(dek))
	}

	path := c.databaseFilePath(databaseID)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: file already exists at %q", domain.ErrDatabaseCreation, path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: stat %q: %w", domain.ErrDatabaseCreation, path, err)
	}

	conn, err := Open(path, dek, c.busyTimeout)
	if err != nil {
		return fmt.Errorf("%w: open %q: %w", domain.ErrDatabaseCreation, path, err)
	}
	conn.DisableAttach()

	if err := conn.WithInternal(func() error {
		// auto_vacuum must be set while the database is still empty (before any table exists) — it
		// requires a full VACUUM to enable later. FULL truncates freed pages at every commit, so
		// deletes and drops genuinely shrink the file (and the tracked size); a NONE-mode file only
		// ever grows.
		if _, e := conn.Exec(ctx, "PRAGMA auto_vacuum = FULL"); e != nil {
			return e
		}
		_, e := conn.Exec(ctx, "PRAGMA journal_mode = WAL")
		return e
	}); err != nil {
		if cerr := conn.Close(); cerr != nil {
			c.logger.Warn().Err(cerr).Str("database_id", databaseID).
				Msg("sqlitelocal: closing conn after creation-pragma failure")
		}
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			c.logger.Warn().Err(rerr).Str("path", path).
				Msg("sqlitelocal: removing file after creation-pragma failure")
		}
		return fmt.Errorf("%w: creation pragmas on %q: %w", domain.ErrDatabaseCreation, path, err)
	}

	if err := c.cache.Insert(databaseID, conn); err != nil {
		// Insert failures (cache closed, dup) close conn for us; remove the
		// file so the database state stays consistent.
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			c.logger.Warn().Err(rerr).Str("path", path).
				Msg("sqlitelocal: removing file after cache Insert failure")
		}
		return fmt.Errorf("%w: caching connection: %w", domain.ErrDatabaseCreation, err)
	}

	return nil
}

// SetDatabaseMaxSize caps the database file at maxSizeBytes via SQLite's
// max_page_count pragma. A non-positive limit is treated as "no cap" and is
// a no-op (mirrors the service contract — admins skip the call).
func (c *Client) SetDatabaseMaxSize(ctx context.Context, databaseID string, maxSizeBytes int64) error {
	if maxSizeBytes <= 0 {
		return nil
	}

	return c.cache.Use(ctx, databaseID, func(conn *Conn) error {
		return conn.WithInternal(func() error {
			pageSize, err := readInt64Pragma(ctx, conn, "PRAGMA page_size")
			if err != nil {
				return fmt.Errorf("reading page_size: %w", err)
			}
			if pageSize <= 0 {
				return fmt.Errorf("invalid page_size %d", pageSize)
			}
			// Rounds up so we never silently undershoot the requested cap.
			maxPages := (maxSizeBytes + pageSize - 1) / pageSize
			if _, err := conn.Exec(ctx, fmt.Sprintf("PRAGMA max_page_count = %d", maxPages)); err != nil {
				return fmt.Errorf("setting max_page_count=%d: %w", maxPages, err)
			}
			return nil
		})
	})
}

// GetDatabaseStats returns disk usage = main file + WAL sidecar (no SHM —
// SHM is a fixed ~32KB coordination file, not user data). A missing file
// contributes 0; this lets the call survive racing deletes without bubbling
// noisy errors.
func (c *Client) GetDatabaseStats(_ context.Context, databaseID string) (*domain.DatabaseStats, error) {
	base := c.databaseFilePath(databaseID)
	var total uint64
	var lastModified time.Time
	for _, suffix := range []string{"", "-wal"} {
		info, err := os.Stat(base + suffix)
		switch {
		case err == nil:
			total += uint64(info.Size())
			if info.ModTime().After(lastModified) {
				lastModified = info.ModTime()
			}
		case errors.Is(err, fs.ErrNotExist):
			// Skip; main may have been deleted, or WAL not yet flushed.
		default:
			return nil, fmt.Errorf("stat %q: %w", base+suffix, err)
		}
	}
	return &domain.DatabaseStats{StorageBytesUsed: total, LastModifiedAt: lastModified}, nil
}

// EvictDatabase drops any cached connection to the database without touching its file, leaving the file with no
// open handles. Used to drain a database before it's snapshotted and handed off to another pod.
func (c *Client) EvictDatabase(_ context.Context, databaseID string) error {
	if err := c.cache.Evict(databaseID); err != nil {
		return fmt.Errorf("evicting cached connection for %q: %w", databaseID, err)
	}
	return nil
}

// DeleteDatabase evicts the cached connection (waiting for any in-flight
// query to release its read-lock) and removes the main file plus the WAL/SHM
// sidecars. Missing files are tolerated so the call is idempotent.
func (c *Client) DeleteDatabase(_ context.Context, databaseID string) error {
	if err := c.cache.Evict(databaseID); err != nil {
		return fmt.Errorf("evicting cached connection for %q: %w", databaseID, err)
	}

	base := c.databaseFilePath(databaseID)
	var firstErr error
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(base + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			c.logger.Warn().Err(err).Str("path", base+suffix).
				Msg("sqlitelocal: removing database file")
			if firstErr == nil {
				firstErr = fmt.Errorf("removing %q: %w", base+suffix, err)
			}
		}
	}
	return firstErr
}

// --- queryExecutor ---

// Execute runs sql with the given typed args and returns the result. For
// SELECTs the rows + column metadata are populated; for DML the affected
// count + last insert rowid are populated.
func (c *Client) Execute(
	ctx context.Context, databaseID string, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return c.execute(ctx, databaseID, sql, args, false, 0)
}

// ExecuteWithRowCap is Execute for a read that may materialize at most maxRows rows; the scan stops at the first row
// past the cap and domain.ErrQueryResultTooLarge is returned. maxRows <= 0 means no cap.
func (c *Client) ExecuteWithRowCap(
	ctx context.Context, databaseID string, sql string, args []domain.TypedValue, maxRows int,
) (*domain.QueryResult, error) {
	return c.execute(ctx, databaseID, sql, args, false, maxRows)
}

// ExecuteInternal is Execute with the reserved-prefix authorizer bypassed.
// Used by platform code (migrations, mutation/schema log writes, undo/redo
// stack writes, skill CRUD).
func (c *Client) ExecuteInternal(
	ctx context.Context, databaseID string, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return c.execute(ctx, databaseID, sql, args, true, 0)
}

func (c *Client) execute(
	ctx context.Context, databaseID string, sql string, args []domain.TypedValue, internal bool, maxRows int,
) (*domain.QueryResult, error) {
	var result *domain.QueryResult
	err := c.cache.Use(ctx, databaseID, func(conn *Conn) error {
		body := func() error {
			r, err := executeStatement(ctx, conn, sql, args, maxRows)
			if err != nil {
				return err
			}
			result = r
			return nil
		}
		if internal {
			return conn.WithInternal(body)
		}
		return body()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ExecuteTransaction runs stmts atomically inside a BEGIN IMMEDIATE / COMMIT
// pair. On any statement error, the transaction is rolled back and the error
// is returned. Returns one *QueryResult per input statement (in order) on
// success; nil on rollback.
func (c *Client) ExecuteTransaction(
	ctx context.Context, databaseID string, stmts []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return c.executeTransaction(ctx, databaseID, stmts, false)
}

// ExecuteTransactionInternal is ExecuteTransaction with the reserved-prefix
// authorizer bypassed.
func (c *Client) ExecuteTransactionInternal(
	ctx context.Context, databaseID string, stmts []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return c.executeTransaction(ctx, databaseID, stmts, true)
}

// WithConn calls fn with the database's *Conn (as a service.ParseHandle)
// for the duration of fn. Used by the service-layer classifier to
// run parse-only walks against the connection's schema.
//
// The handle is valid only inside fn — it must not be retained or used
// after fn returns.
func (c *Client) WithConn(ctx context.Context, databaseID string, fn func(h service.ParseHandle) error) error {
	return c.cache.Use(ctx, databaseID, func(conn *Conn) error {
		return fn(conn)
	})
}

// WithTx opens a write transaction (beginWrite) on database's connection and runs fn inside it.
// BEGIN/COMMIT/ROLLBACK are always issued in internal mode (platform-managed); the internal flag governs only fn's own
// tx.Exec calls, while tx.ExecInternal always bypasses the authorizer's reserved-object rules. On any non-nil return
// from fn (or COMMIT failure), ROLLBACK runs on a fresh ctx so a cancelled caller still releases the txn; the
// connection is evicted if ROLLBACK itself fails so the next caller reopens cleanly.
func (c *Client) WithTx(
	ctx context.Context, databaseID string, internal bool, fn func(ctx context.Context, tx service.Tx) error,
) error {
	var rollbackErr error
	err := c.cache.Use(ctx, databaseID, func(conn *Conn) error {
		txnExec := func(c2 context.Context, sql string) error {
			return conn.WithInternal(func() error {
				_, e := conn.Exec(c2, sql)
				return e
			})
		}

		if err := txnExec(ctx, beginWrite); err != nil {
			return fmt.Errorf("begin: %w", err)
		}

		runFn := func() error {
			return fn(ctx, &connTx{conn: conn})
		}
		var fnErr error
		if internal {
			fnErr = conn.WithInternal(runFn)
		} else {
			fnErr = runFn()
		}

		if fnErr != nil {
			if rbErr := txnExec(context.Background(), "ROLLBACK"); rbErr != nil {
				rollbackErr = rbErr
				c.logger.Warn().Err(rbErr).
					Str("database_id", databaseID).
					Msg("sqlitelocal: ROLLBACK failed in WithTx")
			}
			return fnErr
		}

		if err := txnExec(ctx, "COMMIT"); err != nil {
			if rbErr := txnExec(context.Background(), "ROLLBACK"); rbErr != nil {
				rollbackErr = rbErr
				c.logger.Warn().Err(rbErr).
					Str("database_id", databaseID).
					Msg("sqlitelocal: ROLLBACK failed after COMMIT error")
			}
			return fmt.Errorf("commit: %w", err)
		}

		return nil
	})
	if rollbackErr != nil {
		if evictErr := c.cache.Evict(databaseID); evictErr != nil {
			c.logger.Warn().Err(evictErr).
				Str("database_id", databaseID).
				Msg("sqlitelocal: evicting connection after ROLLBACK failure")
		}
	}
	return err
}

// connTx satisfies service.Tx by delegating to executeStatement on the
// connection that opened the transaction.
type connTx struct {
	conn *Conn
}

// Exec runs sql with args inside the open transaction, under the authorizer mode the transaction was opened with.
func (t *connTx) Exec(ctx context.Context, sql string, args []domain.TypedValue) (*domain.QueryResult, error) {
	return executeStatement(ctx, t.conn, sql, args, 0)
}

// ExecInternal runs sql with args inside the open transaction with the reserved-object rules bypassed.
func (t *connTx) ExecInternal(
	ctx context.Context, sql string, args []domain.TypedValue,
) (result *domain.QueryResult, err error) {
	err = t.conn.WithInternal(func() error {
		result, err = executeStatement(ctx, t.conn, sql, args, 0)
		return err
	})
	return result, err
}

// Parse prepares sql against the connection that holds the open transaction, so the parse resolves the schema the
// transaction sees, including objects it created or dropped itself.
func (t *connTx) Parse(sql string) (domain.ParseResult, error) {
	return t.conn.Parse(sql)
}

func (c *Client) executeTransaction(
	ctx context.Context, databaseID string, stmts []domain.SQLStatement, internal bool,
) ([]*domain.QueryResult, error) {
	if len(stmts) == 0 {
		return nil, nil
	}

	var (
		results     []*domain.QueryResult
		rollbackErr error
	)
	err := c.cache.Use(ctx, databaseID, func(conn *Conn) error {
		// BEGIN, COMMIT, and ROLLBACK are platform-managed transaction
		// boundaries; the authorizer denies them for tenant SQL, so the
		// adapter must issue them in internal mode regardless of the
		// caller's flag (which only governs the user statements).
		txnExec := func(c2 context.Context, sql string) error {
			return conn.WithInternal(func() error {
				_, e := conn.Exec(c2, sql)
				return e
			})
		}

		body := func() error {
			if err := txnExec(ctx, beginWrite); err != nil {
				return fmt.Errorf("begin: %w", err)
			}

			out := make([]*domain.QueryResult, len(stmts))
			for i, stmt := range stmts {
				r, err := executeStatement(ctx, conn, stmt.SQL, stmt.Args, 0)
				if err != nil {
					// Best-effort rollback; ignore its error and surface the
					// original. Use a fresh ctx so a cancelled caller still
					// releases the transaction.
					if rbErr := txnExec(context.Background(), "ROLLBACK"); rbErr != nil {
						rollbackErr = rbErr
						c.logger.Warn().Err(rbErr).
							Str("database_id", databaseID).
							Int("stmt", i).
							Msg("sqlitelocal: ROLLBACK failed after statement error")
					}
					return fmt.Errorf("statement %d: %w", i, err)
				}
				out[i] = r
			}

			if err := txnExec(ctx, "COMMIT"); err != nil {
				if rbErr := txnExec(context.Background(), "ROLLBACK"); rbErr != nil {
					rollbackErr = rbErr
					c.logger.Warn().Err(rbErr).
						Str("database_id", databaseID).
						Msg("sqlitelocal: ROLLBACK failed after COMMIT error")
				}
				return fmt.Errorf("commit: %w", err)
			}

			results = out
			return nil
		}
		if internal {
			return conn.WithInternal(body)
		}
		return body()
	})
	if rollbackErr != nil {
		// Rollback failure leaves the connection wedged — evict so the next
		// caller reopens cleanly. Best-effort: Evict's own errors are logged
		// inside the cache.
		if evictErr := c.cache.Evict(databaseID); evictErr != nil {
			c.logger.Warn().Err(evictErr).
				Str("database_id", databaseID).
				Msg("sqlitelocal: evicting connection after ROLLBACK failure")
		}
	}
	if err != nil {
		return nil, err
	}
	return results, nil
}

// --- rowStreamer ---

// ExecuteStream runs sql with the given typed args and yields each result row to fn as it is scanned,
// without materializing the result set. fn receives the column metadata on every invocation; a non-nil
// error from fn aborts the scan and is returned unwrapped, so callers can match their own sentinel errors
// with errors.Is. Runs with the reserved-prefix authorizer active, and holds the database's connection
// only for the duration of the scan.
func (c *Client) ExecuteStream(
	ctx context.Context, databaseID string, sql string, args []domain.TypedValue,
	fn func(cols []domain.Column, row []domain.TypedValue) error,
) error {
	return c.cache.Use(ctx, databaseID, func(conn *Conn) error {
		_, err := scanStatement(ctx, conn, sql, args, fn)
		return err
	})
}

// --- helpers ---

// databaseFilePath returns the on-disk path for database's main DB file. Kept
// internal so the directory layout convention isn't leaked.
func (c *Client) databaseFilePath(databaseID string) string {
	return filepath.Join(c.dataDir, databaseID+".db")
}

// executeStatement runs a single SQL statement and shapes the materialized result: rows + column metadata for
// SELECTs, affected count + last insert rowid for DML. A positive maxRows caps the materialized rows: the scan stops
// at the first row past it and domain.ErrQueryResultTooLarge is returned, columns intact.
func executeStatement(
	ctx context.Context, conn *Conn, sql string, args []domain.TypedValue, maxRows int,
) (*domain.QueryResult, error) {
	var out [][]domain.TypedValue
	result, err := scanStatement(ctx, conn, sql, args, func(_ []domain.Column, row []domain.TypedValue) error {
		if maxRows > 0 && len(out) >= maxRows {
			return domain.ErrQueryResultTooLarge
		}
		out = append(out, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	result.Rows = out
	return result, nil
}

// scanStatement runs a single SQL statement, yielding each result row to onRow instead of materializing
// the result set.
//
// SQLite's prepared-statement API is uniform for SELECT and DML: prepare,
// step until DONE, finalize. We always go through Query (which prepares but
// does not step) so we have the column metadata in hand before deciding how
// to drain the statement.
//
//   - 0 columns => DML/DDL: a single Next() runs the statement (onRow is never invoked); we then
//     read changes() + last_insert_rowid() via a small follow-up query.
//   - >0 columns => DQL, or DML with a RETURNING clause: iterate rows, invoking onRow with the column metadata and
//     each scanned row. For DML, changes() + last_insert_rowid() are then read as above, once the statement has run
//     to completion. The returned QueryResult carries the columns only, never the rows.
//
// A non-nil error from onRow aborts the scan and is returned unwrapped, so callers can match their own
// sentinel errors with errors.Is; SQLite errors are still surfaced as domain.QueryExecutionError.
func scanStatement(
	ctx context.Context, conn *Conn, sql string, args []domain.TypedValue,
	onRow func(cols []domain.Column, row []domain.TypedValue) error,
) (*domain.QueryResult, error) {
	rawArgs := make([]any, len(args))
	for i, a := range args {
		rawArgs[i] = a.Value
	}

	rows, err := conn.Query(ctx, sql, rawArgs...)
	if err != nil {
		if IsCode(err, CodeAuth) {
			// The authorizer refused the prepare. The recorded reason maps it to the same domain error the
			// classification pass yields, so a statement whose meaning changed since it was classified — a trigger
			// created in between, say — fails the way it would have failed up front.
			return nil, translatePrepareError(err, conn.parseCtx.denyReason)
		}
		return nil, wrapExecError(err)
	}

	colNames := rows.Columns()
	declTypes := rows.DeclaredTypes()

	if len(colNames) == 0 {
		// DML / DDL path. Step once to execute, then probe metadata.
		_ = rows.Next()
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, wrapExecError(err)
		}
		if err := rows.Close(); err != nil {
			return nil, wrapExecError(err)
		}
		affected, lastID, err := readChangesAndLastID(ctx, conn)
		if err != nil {
			return nil, wrapExecError(err)
		}
		return &domain.QueryResult{
			AffectedRowCount: affected,
			LastInsertRowID:  lastID,
		}, nil
	}

	defer func() { _ = rows.Close() }()

	columns := make([]domain.Column, len(colNames))
	for i, name := range colNames {
		columns[i] = domain.Column{Name: name, DeclaredType: declTypes[i]}
	}
	readOnly := rows.ReadOnly()

	for rows.Next() {
		dest := make([]any, len(colNames))
		ptrs := make([]any, len(colNames))
		for i := range colNames {
			ptrs[i] = &dest[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, wrapExecError(err)
		}
		row := make([]domain.TypedValue, len(colNames))
		for i, v := range dest {
			row[i] = anyToTypedValue(v)
		}
		if err := onRow(columns, row); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, wrapExecError(err)
	}
	if readOnly {
		return &domain.QueryResult{Columns: columns}, nil
	}

	if err := rows.Close(); err != nil {
		return nil, wrapExecError(err)
	}
	affected, lastID, err := readChangesAndLastID(ctx, conn)
	if err != nil {
		return nil, wrapExecError(err)
	}
	return &domain.QueryResult{Columns: columns, AffectedRowCount: affected, LastInsertRowID: lastID}, nil
}

// readChangesAndLastID returns AffectedRowCount and LastInsertRowID for the
// most recent statement on conn. Uses scalar SQL functions rather than cgo
// helpers to keep the binding minimal. last_insert_rowid is returned as nil
// when zero (no INSERT has happened on this connection yet).
func readChangesAndLastID(ctx context.Context, conn *Conn) (int64, *int64, error) {
	rows, err := conn.Query(ctx, "SELECT changes(), last_insert_rowid()")
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if rerr := rows.Err(); rerr != nil {
			return 0, nil, rerr
		}
		return 0, nil, nil
	}

	var changes, lastID int64
	if err := rows.Scan(&changes, &lastID); err != nil {
		return 0, nil, err
	}

	var lastIDPtr *int64
	if lastID != 0 {
		lastIDPtr = &lastID
	}
	return changes, lastIDPtr, nil
}

// readInt64Pragma runs a single-cell PRAGMA query and returns its int64 value.
func readInt64Pragma(ctx context.Context, conn *Conn, pragma string) (int64, error) {
	rows, err := conn.Query(ctx, pragma)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if rerr := rows.Err(); rerr != nil {
			return 0, rerr
		}
		return 0, fmt.Errorf("%s returned no rows", pragma)
	}
	var v int64
	if err := rows.Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// anyToTypedValue maps the binding's `*any` scan output (int64/float64/
// string/[]byte/nil) into a domain TypedValue.
func anyToTypedValue(v any) domain.TypedValue {
	switch val := v.(type) {
	case nil:
		return domain.NullValue()
	case int64:
		return domain.IntegerValue(val)
	case float64:
		return domain.RealValue(val)
	case string:
		return domain.TextValue(val)
	case []byte:
		return domain.TypedValue{Type: domain.SQLBlob, Value: val}
	default:
		return domain.NullValue()
	}
}

// wrapExecError surfaces SQLite errors as domain.QueryExecutionError so the
// service layer can match them via errors.Is(err, ErrQueryExecution).
// Non-binding errors (ctx, cache load failures) pass through unchanged.
func wrapExecError(err error) error {
	if err == nil {
		return nil
	}
	if sqlErr, ok := errors.AsType[*Error](err); ok {
		return &domain.QueryExecutionError{
			Code:    sqlErr.Code,
			Message: sqlErr.Msg,
		}
	}
	return err
}
