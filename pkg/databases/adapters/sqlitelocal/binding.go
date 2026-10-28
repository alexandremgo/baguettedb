//go:build cgo

// Package sqlitelocal is the per-bdb SQLite engine, keeping each bdb's data in its own SQLCipher-encrypted file. Its
// base is a CGo binding, not a database/sql driver, over the vendored SQLCipher amalgamation in csrc/ (open, rekey,
// exec, query, prepare-only parse), with an authorizer keeping tenant SQL off what the platform reserves. Client builds
// on it to create, query, snapshot, export, import and delete those files through a cache of connections opened with
// each file's data-encryption key, and Classifier classifies statements against a bdb's live schema.
package sqlitelocal

/*
#cgo CFLAGS: -DSQLITE_HAS_CODEC
#cgo CFLAGS: -DSQLITE_EXTRA_INIT=sqlcipher_extra_init
#cgo CFLAGS: -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown
#cgo CFLAGS: -DSQLITE_TEMP_STORE=2 -DSQLITE_THREADSAFE=1
#cgo CFLAGS: -DSQLITE_ENABLE_FTS5
#cgo CFLAGS: -I${SRCDIR}/csrc
// SQLCipher's crypto comes from the system on macOS (CommonCrypto, in the Security framework every Mac has) and from
// OpenSSL elsewhere, so a macOS binary needs no OpenSSL to start. Both providers implement the same algorithms: a file
// written with one opens with the other.
#cgo darwin CFLAGS: -DSQLCIPHER_CRYPTO_CC
#cgo darwin LDFLAGS: -framework Security -framework CoreFoundation
#cgo !darwin CFLAGS: -DSQLCIPHER_CRYPTO_OPENSSL
#cgo !darwin pkg-config: libcrypto
// FTS5's BM25 ranking calls log() from libm; Linux requires explicit -lm at link time.
// Harmless on macOS where libm is folded into libc.
#cgo LDFLAGS: -lm

#include "sqlite3.h"
#include <stdlib.h>
#include <string.h>

// SQLITE_TRANSIENT is a macro defined as ((sqlite3_destructor_type)-1) and is
// not directly addressable from CGo. These two helpers wrap the bind calls so
// SQLite copies the input bytes immediately rather than retaining the Go
// pointer (which the runtime is free to move).
//
// SAFETY: SQLITE_TRANSIENT here is load-bearing. Do not add a variant that
// passes SQLITE_STATIC or NULL — SQLite would then retain the Go-owned pointer
// past the call, violating CGo's "C must not keep Go pointers" rule and
// risking a use-after-free if Go moves the backing array.
static int sqlitelocal_bind_blob(sqlite3_stmt *stmt, int idx, const void *data, int n) {
    // An empty []byte arrives here as data == NULL (bindBytes passes nil for a zero-length slice).
    // Forwarding that NULL would bind SQL NULL, so bind a zero-length BLOB instead — a non-NULL
    // pointer with length 0 — keeping an empty []byte distinct from a genuine NULL.
    if (data == NULL) return sqlite3_bind_blob(stmt, idx, "", 0, SQLITE_TRANSIENT);
    return sqlite3_bind_blob(stmt, idx, data, n, SQLITE_TRANSIENT);
}
static int sqlitelocal_bind_text(sqlite3_stmt *stmt, int idx, const char *data, int n) {
    // An empty string arrives here as data == NULL (bindString passes nil for a zero-length string).
    // Forwarding that NULL would bind SQL NULL, so bind an empty TEXT instead — a non-NULL pointer
    // with length 0 — keeping "" distinct from a genuine NULL.
    if (data == NULL) return sqlite3_bind_text(stmt, idx, "", 0, SQLITE_TRANSIENT);
    return sqlite3_bind_text(stmt, idx, data, n, SQLITE_TRANSIENT);
}

// sqlite3_db_config is variadic, which cgo cannot call directly; this wrapper applies the
// double-quoted-string-literal setting to both the DDL and DML parsers.
static int sqlitelocal_set_dqs(sqlite3 *db, int enable) {
    int rc = sqlite3_db_config(db, SQLITE_DBCONFIG_DQS_DDL, enable, (int*)0);
    if (rc != SQLITE_OK) return rc;
    return sqlite3_db_config(db, SQLITE_DBCONFIG_DQS_DML, enable, (int*)0);
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime/cgo"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// KeySize is the required length, in bytes, of a key passed to Open or Rekey:
// 32 bytes of high-entropy random material, applied through SQLCipher's
// raw-key path with no PBKDF2 derivation (see keySpec).
const KeySize = 32

// Conn wraps a single SQLCipher database handle. SQLite is built with
// SQLITE_THREADSAFE=1 (full mutex) so all engine access is serialised
// inside the C library.
//
// Authorizer state (parseCtx) is per-Conn and read by the C callback
// during prepare. All current callers serialize per-Conn via cache.Use,
// so the field is single-writer in practice. If a future feature shares
// a Conn across goroutines (e.g. concurrent reads on one database),
// move parseCtx to per-call state on context.Context.
type Conn struct {
	db *C.sqlite3

	closeOnce sync.Once
	closeErr  error

	// Authorizer state.
	authzHandle cgo.Handle
	parseCtx    parseCtx
}

// parseCtx is set up before each prepare and read by the authorizer
// callback. Op recording (tables, ddl, dml) supports table extraction and
// statement-kind classification.
type parseCtx struct {
	internal   bool                   // bypass reserved-prefix deny rules
	tables     []string               // recorded by authorizer on read/write
	created    []string               // names of objects the statement creates (table/index/trigger/view)
	ddl        domain.DDLType         // set on CREATE/DROP/ALTER
	dml        bool                   // set on top-level INSERT/UPDATE/DELETE (not trigger-fired)
	dmlVerbs   []domain.StatementVerb // distinct top-level write verbs, in recording order
	denyReason domain.DenyReason      // populated when the authorizer returns SQLITE_DENY
}

// recordDMLVerb appends a top-level write verb once, preserving recording order.
func (p *parseCtx) recordDMLVerb(verb domain.StatementVerb) {
	if !slices.Contains(p.dmlVerbs, verb) {
		p.dmlVerbs = append(p.dmlVerbs, verb)
	}
}

// Result holds the outcome of an Exec.
type Result struct {
	LastInsertID int64
	RowsAffected int64
}

// Error wraps a non-OK return code from SQLite.
type Error struct {
	Code int
	Msg  string
	Op   string
}

func (e *Error) Error() string {
	return fmt.Sprintf("sqlitelocal: %s: %s (code %d)", e.Op, e.Msg, e.Code)
}

func newError(op string, db *C.sqlite3, code C.int) *Error {
	msg := ""
	// When a connection is available, prefer sqlite3_extended_errcode so
	// callers see SQLITE_CONSTRAINT_UNIQUE (2067) rather than the primary
	// SQLITE_CONSTRAINT (19). Falls back to the passed-in code when no db
	// handle is available (e.g. open-time errors).
	resolvedCode := int(code)
	if db != nil {
		msg = C.GoString(C.sqlite3_errmsg(db))
		if ext := int(C.sqlite3_extended_errcode(db)); ext != 0 {
			resolvedCode = ext
		}
	} else {
		msg = C.GoString(C.sqlite3_errstr(code))
	}
	return &Error{Code: resolvedCode, Msg: msg, Op: op}
}

// IsCode reports whether err is a *Error with the given SQLite result code.
func IsCode(err error, code int) bool {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code == code
	}
	return false
}

// CodeNotADB is SQLITE_NOTADB — returned when the file is not SQLite (e.g.
// reading an encrypted file with the wrong key).
const CodeNotADB = int(C.SQLITE_NOTADB)

// CodeInterrupt is SQLITE_INTERRUPT — returned by step when a running
// statement is cancelled via sqlite3_interrupt (e.g. ctx cancellation).
const CodeInterrupt = int(C.SQLITE_INTERRUPT)

// CodeAuth is SQLITE_AUTH — returned by prepare when the authorizer
// denies an operation (e.g. tenant SQL writing to a reserved-prefix
// table).
const CodeAuth = int(C.SQLITE_AUTH)

// Open creates or opens a SQLCipher database at path, applies the 32-byte
// key in raw-key form, and validates the key by issuing a small probe query.
// Returns SQLITE_NOTADB (via *Error) if the key does not match an existing file.
//
// The key is key material, not a passphrase: it is used as the encryption key
// directly. A passphrase-keyed file — an export — will not open here even if
// the passphrase happens to be KeySize bytes long; use OpenWithPassphrase.
//
// busyTimeout is how long a statement waits for a lock another connection or process holds, from the probe on: a
// fresh connection's first read can find the file locked by a writer or by WAL recovery. Zero or less fails at once.
func Open(path string, key []byte, busyTimeout time.Duration) (*Conn, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("sqlitelocal: key must be %d bytes, got %d", KeySize, len(key))
	}
	return openWithKeyMaterial(path, keySpec(key), busyTimeout, true)
}

// openExisting opens the database at path as Open does, except that it never creates the file: a missing file fails
// to open, where Open would make an empty database in its place and hide the loss.
func openExisting(path string, key []byte, busyTimeout time.Duration) (*Conn, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("sqlitelocal: key must be %d bytes, got %d", KeySize, len(key))
	}
	return openWithKeyMaterial(path, keySpec(key), busyTimeout, false)
}

// OpenWithPassphrase opens a SQLCipher database keyed with a passphrase rather than raw key material, applying
// SQLCipher's standard passphrase processing — the PBKDF2 stretching that Open deliberately avoids.
//
// This is how an exported copy opens, here and in any external SQLCipher tool. Live database files are never
// keyed this way.
func OpenWithPassphrase(path, passphrase string) (*Conn, error) {
	// SQLCipher treats an empty key as "no encryption", which would open a plaintext file and report success.
	if passphrase == "" {
		return nil, errors.New("sqlitelocal: passphrase must not be empty")
	}
	// An exported copy has no other connection to wait for.
	return openWithKeyMaterial(path, []byte(passphrase), 0, true)
}

// openWithKeyMaterial opens path with the tenant safety rails installed and applies keyMaterial verbatim,
// leaving the caller to decide whether that material is a raw-key spec or a passphrase. Statements wait up to
// busyTimeout for another connection's lock, as Open describes.
//
// create lets the open create a missing file; without it, a missing file is an open error.
func openWithKeyMaterial(path string, keyMaterial []byte, busyTimeout time.Duration, create bool) (*Conn, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	flags := C.int(C.SQLITE_OPEN_READWRITE | C.SQLITE_OPEN_FULLMUTEX)
	if create {
		flags |= C.SQLITE_OPEN_CREATE
	}
	var db *C.sqlite3
	rc := C.sqlite3_open_v2(cpath, &db, flags, nil)
	if rc != C.SQLITE_OK {
		err := newError("open", db, rc)
		if db != nil {
			C.sqlite3_close(db)
		}
		return nil, err
	}

	// Surfaces extended result codes (e.g. SQLITE_CONSTRAINT_UNIQUE = 2067)
	// in addition to primary codes (SQLITE_CONSTRAINT = 19), so callers can
	// discriminate constraint kinds without parsing error messages.
	C.sqlite3_extended_result_codes(db, 1)
	setBusyTimeout(db, busyTimeout)

	// Going through sqlite3_key rather than a PRAGMA skips a SQL parse of secret material.
	rc = C.sqlite3_key(db, unsafe.Pointer(&keyMaterial[0]), C.int(len(keyMaterial)))
	if rc != C.SQLITE_OK {
		err := newError("key", db, rc)
		C.sqlite3_close(db)
		return nil, err
	}

	c := &Conn{db: db}

	// sqlite3_key alone does not detect a wrong key — SQLCipher only fails
	// once the engine actually tries to decrypt page 1. A trivial probe
	// query forces that read.
	if _, err := c.Exec(context.Background(), "SELECT count(*) FROM sqlite_master"); err != nil {
		if cerr := c.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}

	// Enables foreign-key constraint enforcement. SQLite ships with FKs off by default and the setting is
	// per-connection — without this, FK ON DELETE CASCADE wouldn't fire (e.g. retention pruning a _change_log row
	// would leave its _change_snapshots sidecar orphaned).
	if _, err := c.Exec(context.Background(), "PRAGMA foreign_keys = ON"); err != nil {
		if cerr := c.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}

	// Keeps on-disk size honest in WAL mode (both settings are per-connection). Eager checkpoints land
	// auto-vacuum truncations in the main file promptly instead of after ~4 MB of WAL, and the size limit
	// truncates the WAL back after a checkpoint instead of leaving it at its high-water mark. Sized for
	// small per-bdb databases where prompt reclamation matters more than checkpoint batching.
	if _, err := c.Exec(context.Background(), "PRAGMA wal_autocheckpoint = 128"); err != nil {
		if cerr := c.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}
	if _, err := c.Exec(context.Background(), "PRAGMA journal_size_limit = 524288"); err != nil {
		if cerr := c.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}

	// Disables SQLite's legacy fallback where a double-quoted token that isn't an identifier silently
	// becomes a string literal — a typo'd "column" must fail loudly instead of corrupting data.
	// Stored-schema parsing is exempt, so databases whose existing schema relies on the fallback still load.
	if err := c.setDQS(false); err != nil {
		if cerr := c.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}

	// Installs the reserved-prefix authorizer AFTER init prepares so the
	// probe above isn't subject to deny rules. Subsequent SQL on this Conn
	// fires the hook; platform code wraps with WithInternal.
	if err := c.installAuthorizer(); err != nil {
		if cerr := c.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}

	return c, nil
}

// OpenPlaintext opens an unencrypted SQLite database at path without applying
// any key.
//
// Originally the entry point for the libsql-to-SQLCipher migration tool
// (removed in commit 97d96ef once all live databases were migrated): the
// standard SQLCipher conversion pattern opens a plaintext SQLite copy as
// the main connection, ATTACHes the encrypted destination with a key, and
// runs sqlcipher_export to copy data across. No production code path uses
// it today; kept for tests and any future plaintext-inspection tooling.
//
// Deliberately does NOT install the reserved-prefix authorizer hook —
// migration-style flows need ATTACH and direct platform-table writes,
// which the authorizer would deny. Production code paths must use Open
// with a 32-byte key (which does install the authorizer).
func OpenPlaintext(path string) (*Conn, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	var db *C.sqlite3
	rc := C.sqlite3_open_v2(
		cpath,
		&db,
		C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_CREATE|C.SQLITE_OPEN_FULLMUTEX,
		nil,
	)
	if rc != C.SQLITE_OK {
		err := newError("open", db, rc)
		if db != nil {
			C.sqlite3_close(db)
		}
		return nil, err
	}

	C.sqlite3_extended_result_codes(db, 1)

	c := &Conn{db: db}

	// Probes that the file is a readable SQLite database (or a fresh empty
	// file SQLite will initialize on first write). Mirrors the validation
	// shape of Open so callers fail early if the path is unusable.
	if _, err := c.Exec(context.Background(), "SELECT count(*) FROM sqlite_master"); err != nil {
		if cerr := c.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}

	return c, nil
}

// openKeyedNoAuthorizer opens an existing SQLCipher database at path with the 32-byte key, skipping all the
// per-connection hardening Open applies: no reserved-prefix authorizer (so ATTACH/DETACH and platform-table writes
// parse), no attach limit, and no foreign_keys pragma (enforcement during a table-by-table copy would trip on rows
// whose parents haven't been copied yet).
//
// Reserved for in-package platform tooling — building an encrypted copy via sqlcipher_export needs ATTACH, which
// every user-facing connection denies at both the authorizer and engine level. Connections from here must never
// serve tenant SQL. Deliberately unexported and must stay so — exporting it would hand importers a connection
// without the tenant safety rails.
//
// Unlike Open, the file must already exist: the CREATE flag is omitted so a concurrently-deleted database surfaces
// as an open error instead of being silently recreated empty. ATTACH on this connection inherits the same no-CREATE
// flags, so attach targets must also exist on disk (an empty file is a valid fresh database).
//
// Statements wait up to busyTimeout for another connection's lock, as Open describes.
func openKeyedNoAuthorizer(path string, key []byte, busyTimeout time.Duration) (*Conn, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("sqlitelocal: key must be %d bytes, got %d", KeySize, len(key))
	}
	return openNoAuthorizer(path, keySpec(key), busyTimeout)
}

// openNoAuthorizer opens the existing database at path as openKeyedNoAuthorizer does, applying keyMaterial verbatim: a
// raw-key spec, a passphrase SQLCipher stretches, or, when empty, no key, for an unencrypted file.
func openNoAuthorizer(path string, keyMaterial []byte, busyTimeout time.Duration) (*Conn, error) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	var db *C.sqlite3
	rc := C.sqlite3_open_v2(
		cpath,
		&db,
		C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_FULLMUTEX,
		nil,
	)
	if rc != C.SQLITE_OK {
		err := newError("open", db, rc)
		if db != nil {
			C.sqlite3_close(db)
		}
		return nil, err
	}

	C.sqlite3_extended_result_codes(db, 1)
	setBusyTimeout(db, busyTimeout)

	if len(keyMaterial) > 0 {
		rc = C.sqlite3_key(db, unsafe.Pointer(&keyMaterial[0]), C.int(len(keyMaterial)))
		if rc != C.SQLITE_OK {
			err := newError("key", db, rc)
			C.sqlite3_close(db)
			return nil, err
		}
	}

	c := &Conn{db: db}

	// Forces a page-1 decrypt so a wrong key fails here (SQLITE_NOTADB) rather than on the first real statement.
	// Mirrors Open's probe.
	if _, err := c.Exec(context.Background(), "SELECT count(*) FROM sqlite_master"); err != nil {
		if cerr := c.Close(); cerr != nil {
			return nil, errors.Join(err, cerr)
		}
		return nil, err
	}

	return c, nil
}

// Close releases the underlying database handle. Safe to call multiple times.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.releaseAuthorizer()
		rc := C.sqlite3_close(c.db)
		if rc != C.SQLITE_OK {
			c.closeErr = newError("close", c.db, rc)
		}
		c.db = nil
	})
	return c.closeErr
}

// setBusyTimeout makes a statement on db that finds the database locked by another connection or process retry for up
// to d before failing with SQLITE_BUSY. Zero or less fails at once, SQLite's default.
//
// The wait ignores ctx: sqlite3_interrupt does not cut a busy wait short, so a cancelled statement returns once the
// lock frees or d runs out.
func setBusyTimeout(db *C.sqlite3, d time.Duration) {
	C.sqlite3_busy_timeout(db, C.int(min(d.Milliseconds(), math.MaxInt32)))
}

// DisableAttach drops SQLITE_LIMIT_ATTACHED to 0 on this connection, causing
// any subsequent ATTACH DATABASE statement to fail at the engine level.
//
// SECURITY INVARIANT: every connection used to serve tenant SQL must call
// this. It is the engine-level half of a defense-in-depth pair against
// cross-database queries; the parse-time half lives in the SQLite
// authorizer hook (see authorizer.go), which denies ATTACH/DETACH
// during prepare. Both layers must remain in place — the authorizer
// gives users a clean error and protects against engine config drift,
// and the engine limit protects against authorizer bypass.
//
// sqlite3_limit is documented as infallible (returns the prior limit, never
// an error code), so this method returns no error.
func (c *Conn) DisableAttach() {
	C.sqlite3_limit(c.db, C.SQLITE_LIMIT_ATTACHED, 0)
}

// Rekey re-encrypts the database with newKey. SQLCipher's rekey only
// re-encrypts the main DB file, so running it under WAL would leave the
// -wal sidecar holding pages still encrypted with the old key. To avoid
// that, this method switches the journal mode to DELETE for the duration
// of the rekey and restores the prior mode afterwards (no-op if it was
// already DELETE).
//
// Runs in internal mode so the journal_mode PRAGMA reads/writes bypass
// the tenant authorizer.
func (c *Conn) Rekey(newKey []byte) error {
	if len(newKey) != KeySize {
		return fmt.Errorf("sqlitelocal: new key must be %d bytes, got %d", KeySize, len(newKey))
	}

	return c.WithInternal(func() error {
		originalMode, err := c.queryString(context.Background(), "PRAGMA journal_mode")
		if err != nil {
			return fmt.Errorf("rekey: read journal_mode: %w", err)
		}

		if !strings.EqualFold(originalMode, "delete") {
			if _, err := c.Exec(context.Background(), "PRAGMA journal_mode = DELETE"); err != nil {
				return fmt.Errorf("rekey: switch to DELETE journal: %w", err)
			}
		}

		// Raw-key form, so the rewritten file is keyed the same way Open reads it (see keySpec).
		spec := keySpec(newKey)
		rc := C.sqlite3_rekey(c.db, unsafe.Pointer(&spec[0]), C.int(len(spec)))
		if rc != C.SQLITE_OK {
			// Best-effort: try to restore the original journal mode before
			// returning the rekey error, so the caller's connection is left in
			// a sane state.
			if !strings.EqualFold(originalMode, "delete") {
				_, _ = c.Exec(context.Background(), "PRAGMA journal_mode = "+originalMode)
			}
			return newError("rekey", c.db, rc)
		}

		if !strings.EqualFold(originalMode, "delete") {
			if _, err := c.Exec(context.Background(), "PRAGMA journal_mode = "+originalMode); err != nil {
				return fmt.Errorf("rekey: restore journal_mode=%s: %w", originalMode, err)
			}
		}

		return nil
	})
}

// ExecScript executes a multi-statement SQL script via sqlite3_exec. Unlike
// Exec, which uses sqlite3_prepare_v2 and is therefore single-statement, this
// walks through every `;`-terminated statement in sql in order. It does not
// return per-statement results and accepts no bound parameters — it is meant
// for replaying scripts whose contents are already trusted SQL text (e.g. a
// `.dump` produced by SQLite). Returns the first error encountered;
// subsequent statements are not executed.
func (c *Conn) ExecScript(ctx context.Context, sql string) error {
	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))

	stop := watchCtx(ctx, c.db)
	defer stop()

	rc := C.sqlite3_exec(c.db, csql, nil, nil, nil)
	if rc != C.SQLITE_OK {
		return newError("exec_script", c.db, rc)
	}
	return nil
}

// Exec runs a statement that does not return rows.
func (c *Conn) Exec(ctx context.Context, sql string, args ...any) (Result, error) {
	stmt, err := c.prepare(sql)
	if err != nil {
		return Result{}, err
	}
	defer C.sqlite3_finalize(stmt)

	if err := bindArgs(stmt, args); err != nil {
		return Result{}, err
	}

	stop := watchCtx(ctx, c.db)
	defer stop()

	rc := C.sqlite3_step(stmt)
	if rc != C.SQLITE_DONE && rc != C.SQLITE_ROW {
		return Result{}, newError("exec", c.db, rc)
	}

	return Result{
		LastInsertID: int64(C.sqlite3_last_insert_rowid(c.db)),
		RowsAffected: int64(C.sqlite3_changes64(c.db)),
	}, nil
}

// Query runs a statement that returns rows. The caller must Close the
// returned Rows even if Next returns false.
func (c *Conn) Query(ctx context.Context, sql string, args ...any) (*Rows, error) {
	stmt, err := c.prepare(sql)
	if err != nil {
		return nil, err
	}

	if err := bindArgs(stmt, args); err != nil {
		C.sqlite3_finalize(stmt)
		return nil, err
	}

	stopCtx := watchCtx(ctx, c.db)

	ncol := int(C.sqlite3_column_count(stmt))
	cols := make([]string, ncol)
	for i := range ncol {
		cols[i] = C.GoString(C.sqlite3_column_name(stmt, C.int(i)))
	}

	return &Rows{
		conn:    c,
		stmt:    stmt,
		cols:    cols,
		stopCtx: stopCtx,
	}, nil
}

// Rows is the iterator returned by Query.
type Rows struct {
	conn    *Conn
	stmt    *C.sqlite3_stmt
	cols    []string
	stopCtx func()

	stepRC  C.int
	hasNext bool
	stepped bool
	closed  bool
}

// Columns returns the column names in declaration order.
func (r *Rows) Columns() []string {
	out := make([]string, len(r.cols))
	copy(out, r.cols)
	return out
}

// DeclaredTypes returns the schema-declared type for each column, in column
// order. Empty string for expression columns or columns without a declared
// type. Safe to call before Next; not safe after Close.
func (r *Rows) DeclaredTypes() []string {
	out := make([]string, len(r.cols))
	for i := range r.cols {
		p := C.sqlite3_column_decltype(r.stmt, C.int(i))
		if p != nil {
			out[i] = C.GoString(p)
		}
	}
	return out
}

// ReadOnly reports whether the statement makes no change to the database: false for a write with a RETURNING clause,
// which yields rows like a read. Not safe after Close.
func (r *Rows) ReadOnly() bool {
	return C.sqlite3_stmt_readonly(r.stmt) != 0
}

// Next advances to the next row. Returns false when the result set is
// exhausted or an error occurred (check Err).
func (r *Rows) Next() bool {
	if r.closed {
		return false
	}
	r.stepRC = C.sqlite3_step(r.stmt)
	r.stepped = true
	r.hasNext = r.stepRC == C.SQLITE_ROW
	return r.hasNext
}

// Err returns the error encountered during iteration, if any.
func (r *Rows) Err() error {
	if !r.stepped {
		return nil
	}
	if r.stepRC == C.SQLITE_ROW || r.stepRC == C.SQLITE_DONE {
		return nil
	}
	return newError("step", r.conn.db, r.stepRC)
}

// Scan copies the current row's column values into the destination pointers.
// Supported destination types: *bool, *int, *int64, *float64, *string,
// *[]byte, *any.
func (r *Rows) Scan(dest ...any) error {
	if !r.hasNext {
		return errors.New("sqlitelocal: Scan called before Next or after Next returned false")
	}
	if len(dest) != len(r.cols) {
		return fmt.Errorf("sqlitelocal: Scan expected %d destinations, got %d", len(r.cols), len(dest))
	}
	for i, d := range dest {
		if err := scanColumn(r.stmt, i, d); err != nil {
			return fmt.Errorf("sqlitelocal: scan column %d (%s): %w", i, r.cols[i], err)
		}
	}
	return nil
}

// Close finalises the underlying statement and releases the ctx watcher.
func (r *Rows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.stopCtx != nil {
		r.stopCtx()
	}
	rc := C.sqlite3_finalize(r.stmt)
	// sqlite3_finalize replays the most recent step error code. If Err already
	// surfaced it, suppress the duplicate from Close so a cancellation produces
	// one error, not two.
	if rc != C.SQLITE_OK && rc != r.stepRC {
		return newError("finalize", r.conn.db, rc)
	}
	return nil
}

// prepare wraps sqlite3_prepare_v2. Resets the per-prepare authorizer
// recording fields so each prepare sees a clean parseCtx (callers like
// the classifier read the post-prepare snapshot).
func (c *Conn) prepare(sql string) (*C.sqlite3_stmt, error) {
	c.resetParseRecording()

	csql := C.CString(sql)
	defer C.free(unsafe.Pointer(csql))

	var stmt *C.sqlite3_stmt
	rc := C.sqlite3_prepare_v2(c.db, csql, C.int(-1), &stmt, nil)
	if rc != C.SQLITE_OK {
		return nil, newError("prepare", c.db, rc)
	}
	return stmt, nil
}

// parsedStatement is a Go-typed wrapper around a parsed SQLite statement,
// exposing inspection primitives without requiring callers to import "C".
// Callers must call finalize when done.
type parsedStatement struct {
	stmt *C.sqlite3_stmt
}

// isReadOnly reports whether the parsed statement performs no mutations.
func (p *parsedStatement) isReadOnly() bool {
	return C.sqlite3_stmt_readonly(p.stmt) != 0
}

// paramNames returns the statement's bind parameters in positional index order. A named parameter yields its
// sigil-stripped name (":status" → "status"); an anonymous "?" yields an empty string. Parameters are numbered from 1.
func (p *parsedStatement) paramNames() []string {
	n := int(C.sqlite3_bind_parameter_count(p.stmt))
	if n == 0 {
		return nil
	}
	names := make([]string, n)
	for i := 1; i <= n; i++ {
		cname := C.sqlite3_bind_parameter_name(p.stmt, C.int(i))
		if cname == nil {
			continue // anonymous "?" — leave the empty string
		}
		name := C.GoString(cname)
		// Strips the leading sigil (':' / '@' / '$') SQLite reports with the name.
		if len(name) > 0 {
			name = name[1:]
		}
		names[i-1] = name
	}
	return names
}

// resultColumns returns the statement's result column names in positional order. Empty for statements that
// return no rows. An aliased column carries its alias; an unaliased expression's name is unspecified by SQLite.
func (p *parsedStatement) resultColumns() []string {
	n := int(C.sqlite3_column_count(p.stmt))
	if n == 0 {
		return nil
	}
	cols := make([]string, n)
	for i := range n {
		if cname := C.sqlite3_column_name(p.stmt, C.int(i)); cname != nil {
			cols[i] = C.GoString(cname)
		}
	}
	return cols
}

// finalize releases the underlying SQLite statement.
func (p *parsedStatement) finalize() {
	C.sqlite3_finalize(p.stmt)
}

// setDQS toggles SQLite's legacy double-quoted-string-literal fallback for both the DDL and DML parsers on
// this connection. Off, a double-quoted token that doesn't resolve to an identifier is a parse error instead
// of silently becoming a string literal. Stored-schema parsing is exempt from the setting, so legacy schemas
// load regardless.
func (c *Conn) setDQS(enable bool) error {
	v := C.int(0)
	if enable {
		v = 1
	}
	if rc := C.sqlitelocal_set_dqs(c.db, v); rc != C.SQLITE_OK {
		return newError("db_config_dqs", c.db, rc)
	}
	return nil
}

// resetParseRecording clears the per-prepare recording fields on
// parseCtx (tables, ddl, dml, dmlVerbs, denyReason). The internal flag is preserved
// since it's owned by WithInternal scopes that span multiple prepares.
func (c *Conn) resetParseRecording() {
	c.parseCtx.tables = nil
	c.parseCtx.created = nil
	c.parseCtx.ddl = domain.DDLUnknown
	c.parseCtx.dml = false
	c.parseCtx.dmlVerbs = nil
	c.parseCtx.denyReason = domain.DenyNone
}

// Parse satisfies service.ParseHandle. Runs sqlite3_prepare_v2,
// reads the statement's IsReadOnly flag, and finalizes the statement
// before returning a pure-data domain.ParseResult — callers never see
// the underlying prepared statement and have no cleanup obligation.
//
// On error, the returned ParseResult still carries whatever the
// authorizer recorded before the denial (in particular DenyReason)
// so callers can map the failure to the right domain error.
//
// Empty input (whitespace or comments only) is reported via Empty=true
// rather than an error: prepare succeeds with a NULL statement, and
// callers walking multi-statement input can advance through trailing
// whitespace/comments without special-casing them as errors.
func (c *Conn) Parse(sql string) (domain.ParseResult, error) {
	stmt, tail, err := c.prepareWithTail(sql)
	res := domain.ParseResult{
		Tail:           tail,
		Tables:         append([]string(nil), c.parseCtx.tables...),
		CreatedObjects: append([]string(nil), c.parseCtx.created...),
		DDLKind:        c.parseCtx.ddl,
		IsDML:          c.parseCtx.dml,
		DMLVerbs:       append([]domain.StatementVerb(nil), c.parseCtx.dmlVerbs...),
		DenyReason:     c.parseCtx.denyReason,
	}
	if stmt != nil {
		if stmt.stmt == nil {
			res.Empty = true
		} else {
			res.IsReadOnly = stmt.isReadOnly()
			res.ParamNames = stmt.paramNames()
			res.ResultColumns = stmt.resultColumns()
		}
		stmt.finalize()
	}
	return res, err
}

// prepareWithTail parses sql via sqlite3_prepare_v2 and returns the parsed
// statement plus any unparsed remainder. A non-empty tail (beyond whitespace)
// signals multi-statement input.
//
// Resets the per-prepare authorizer recording fields so the snapshot
// reflects only this call.
//
// pzTail points into csql's memory, so the tail must be copied to a Go
// string before csql is freed.
func (c *Conn) prepareWithTail(sql string) (*parsedStatement, string, error) {
	c.resetParseRecording()

	csql := C.CString(sql)

	var stmt *C.sqlite3_stmt
	var pzTail *C.char
	rc := C.sqlite3_prepare_v2(c.db, csql, C.int(-1), &stmt, &pzTail)

	var tail string
	if pzTail != nil {
		tail = C.GoString(pzTail)
	}

	C.free(unsafe.Pointer(csql))

	if rc != C.SQLITE_OK {
		return nil, "", newError("prepare", c.db, rc)
	}
	return &parsedStatement{stmt: stmt}, tail, nil
}

// queryString runs a query expected to return a single TEXT cell.
func (c *Conn) queryString(ctx context.Context, sql string) (out string, err error) {
	rows, err := c.Query(ctx, sql)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	if !rows.Next() {
		if rerr := rows.Err(); rerr != nil {
			return "", rerr
		}
		return "", errors.New("sqlitelocal: no row")
	}
	if serr := rows.Scan(&out); serr != nil {
		return "", serr
	}
	return out, nil
}

// bindArgs binds Go values to ?-positional parameters in stmt.
func bindArgs(stmt *C.sqlite3_stmt, args []any) error {
	expected := int(C.sqlite3_bind_parameter_count(stmt))
	if len(args) != expected {
		return fmt.Errorf("sqlitelocal: statement expects %d parameters, got %d", expected, len(args))
	}
	for i, a := range args {
		idx := C.int(i + 1)
		var rc C.int
		switch v := a.(type) {
		case nil:
			rc = C.sqlite3_bind_null(stmt, idx)
		case bool:
			n := C.sqlite3_int64(0)
			if v {
				n = 1
			}
			rc = C.sqlite3_bind_int64(stmt, idx, n)
		case int:
			rc = C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case int32:
			rc = C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case int64:
			rc = C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case uint32:
			rc = C.sqlite3_bind_int64(stmt, idx, C.sqlite3_int64(v))
		case float32:
			rc = C.sqlite3_bind_double(stmt, idx, C.double(v))
		case float64:
			rc = C.sqlite3_bind_double(stmt, idx, C.double(v))
		case string:
			rc = bindString(stmt, idx, v)
		case []byte:
			rc = bindBytes(stmt, idx, v)
		default:
			return fmt.Errorf("sqlitelocal: unsupported bind type %T at parameter %d", a, i+1)
		}
		if rc != C.SQLITE_OK {
			return fmt.Errorf("sqlitelocal: bind parameter %d: code %d", i+1, int(rc))
		}
	}
	return nil
}

func bindString(stmt *C.sqlite3_stmt, idx C.int, s string) C.int {
	if len(s) == 0 {
		return C.sqlitelocal_bind_text(stmt, idx, nil, 0)
	}
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return C.sqlitelocal_bind_text(stmt, idx, cs, C.int(len(s)))
}

func bindBytes(stmt *C.sqlite3_stmt, idx C.int, b []byte) C.int {
	if len(b) == 0 {
		return C.sqlitelocal_bind_blob(stmt, idx, nil, 0)
	}
	return C.sqlitelocal_bind_blob(stmt, idx, unsafe.Pointer(&b[0]), C.int(len(b)))
}

// scanColumn decodes column i of the current row into dest.
func scanColumn(stmt *C.sqlite3_stmt, i int, dest any) error {
	col := C.int(i)
	colType := C.sqlite3_column_type(stmt, col)

	switch d := dest.(type) {
	case *bool:
		if colType == C.SQLITE_NULL {
			*d = false
			return nil
		}
		*d = C.sqlite3_column_int64(stmt, col) != 0
		return nil
	case *int:
		if colType == C.SQLITE_NULL {
			*d = 0
			return nil
		}
		*d = int(C.sqlite3_column_int64(stmt, col))
		return nil
	case *int64:
		if colType == C.SQLITE_NULL {
			*d = 0
			return nil
		}
		*d = int64(C.sqlite3_column_int64(stmt, col))
		return nil
	case *float64:
		if colType == C.SQLITE_NULL {
			*d = 0
			return nil
		}
		*d = float64(C.sqlite3_column_double(stmt, col))
		return nil
	case *string:
		if colType == C.SQLITE_NULL {
			*d = ""
			return nil
		}
		*d = goStringFromColumn(stmt, col)
		return nil
	case *[]byte:
		if colType == C.SQLITE_NULL {
			*d = nil
			return nil
		}
		*d = goBytesFromColumn(stmt, col)
		return nil
	case *any:
		*d = anyFromColumn(stmt, col, colType)
		return nil
	default:
		return fmt.Errorf("unsupported scan target %T", dest)
	}
}

func goStringFromColumn(stmt *C.sqlite3_stmt, col C.int) string {
	p := C.sqlite3_column_text(stmt, col)
	n := C.sqlite3_column_bytes(stmt, col)
	if p == nil || n == 0 {
		return ""
	}
	return C.GoStringN((*C.char)(unsafe.Pointer(p)), n)
}

func goBytesFromColumn(stmt *C.sqlite3_stmt, col C.int) []byte {
	p := C.sqlite3_column_blob(stmt, col)
	n := C.sqlite3_column_bytes(stmt, col)
	if p == nil || n == 0 {
		return []byte{}
	}
	return C.GoBytes(p, n)
}

func anyFromColumn(stmt *C.sqlite3_stmt, col C.int, t C.int) any {
	switch t {
	case C.SQLITE_INTEGER:
		return int64(C.sqlite3_column_int64(stmt, col))
	case C.SQLITE_FLOAT:
		return float64(C.sqlite3_column_double(stmt, col))
	case C.SQLITE_TEXT:
		return goStringFromColumn(stmt, col)
	case C.SQLITE_BLOB:
		return goBytesFromColumn(stmt, col)
	case C.SQLITE_NULL:
		return nil
	}
	return nil
}

// watchCtx spawns a goroutine that calls sqlite3_interrupt if ctx is
// cancelled. The returned func must be invoked when the operation
// completes to release the goroutine; it blocks until the goroutine has
// fully exited.
//
// Either-side-claims pattern: stop() and the goroutine race for the
// `fired` flag via CompareAndSwap. Whoever wins owns the interrupt
// decision — stop() winning means "no interrupt"; the goroutine winning
// means "fire interrupt now." Without this guard, a goroutine that
// outlived its Exec call could observe the (now-cancelled) ctx and call
// sqlite3_interrupt(db) on a connection already serving the next caller.
// sqlite3_interrupt sets a global flag without holding the connection
// mutex, so a stale interrupt can race past sqlite3_step's auto-clear
// (only effective when nVdbeActive==0) and abort the next caller's step
// with SQLITE_INTERRUPT (code 9). The atomic claim alone is not enough:
// if the goroutine has already passed the CAS check when stop() runs,
// the interrupt is in flight and we still need to wait for it to land
// before letting the caller start the next statement. wg.Wait() is the
// barrier that guarantees this.
//
// once is retained because Rows.Close + a deferred stop() can invoke
// the closer twice on the same Query.
func watchCtx(ctx context.Context, db *C.sqlite3) func() {
	if ctx == nil || ctx.Done() == nil {
		return func() {}
	}
	done := make(chan struct{})
	var fired atomic.Bool
	var wg sync.WaitGroup
	wg.Go(func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		if fired.CompareAndSwap(false, true) {
			C.sqlite3_interrupt(db)
		}
	})
	var once sync.Once
	return func() {
		once.Do(func() {
			// Claim before signaling: any goroutine still at the select
			// will lose the CAS and skip the interrupt call.
			fired.Store(true)
			close(done)
		})
		// If the goroutine had already won the CAS before we claimed,
		// its sqlite3_interrupt call may still be in flight — wait for
		// it to land before returning so it cannot interfere with the
		// next caller on this connection.
		wg.Wait()
	}
}
