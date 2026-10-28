//go:build cgo

package sqlitelocal

/*
#include "sqlite3.h"
#include <stdint.h>

// goAuthorizer is the Go-side authorizer dispatcher (defined via //export in
// this file). The C thunk below is what's installed into SQLite via
// sqlite3_set_authorizer; it forwards each call to goAuthorizer.
//
// Param types match cgo's auto-generated declaration in _cgo_export.h
// (char*, not const char*) — declaring const here would conflict.
//
// userData is reinterpreted between void* (SQLite's API) and uintptr_t
// (cgo.Handle's underlying type) on the C side, so Go never has to do a
// uintptr↔unsafe.Pointer conversion (which trips go vet's unsafeptr
// check). The Handle value is opaque to C — never dereferenced, just
// stored and passed back.
extern int goAuthorizer(uintptr_t handle, int action, char *a1, char *a2, char *a3, char *a4);

static int sqlitelocal_authorizer_thunk(void *userData, int action, const char *a1, const char *a2, const char *a3,
	const char *a4) {
    return goAuthorizer((uintptr_t)userData, action, (char *)a1, (char *)a2, (char *)a3, (char *)a4);
}

static int sqlitelocal_set_authorizer(sqlite3 *db, uintptr_t handle) {
    if (handle == 0) {
        // Detach the hook entirely (used by releaseAuthorizer in Close).
        return sqlite3_set_authorizer(db, NULL, NULL);
    }
    return sqlite3_set_authorizer(db, sqlitelocal_authorizer_thunk, (void *)handle);
}
*/
import "C"

import (
	"runtime/cgo"
	"strings"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// installAuthorizer installs the SQLite authorizer hook on c.db. The hook
// fires during every sqlite3_prepare_v2 call and applies reserved-prefix
// deny rules unless c.parseCtx.internal is true.
//
// Must be called exactly once per Conn, AFTER any init that prepares SQL
// directly on the connection (e.g. PRAGMA key probe). After install,
// callers wrap internal SQL via Conn.WithInternal.
//
// The cgo.Handle is passed across as uintptr_t (its underlying type) so
// SQLite stores an opaque integer token rather than a Go pointer — the
// actual *Conn lives in cgo's internal handle map for the handle's
// lifetime, satisfying the cgo "C may not keep Go pointers" rule.
func (c *Conn) installAuthorizer() error {
	c.authzHandle = cgo.NewHandle(c)
	rc := C.sqlitelocal_set_authorizer(c.db, C.uintptr_t(c.authzHandle))
	if rc != C.SQLITE_OK {
		c.authzHandle.Delete()
		return newError("set_authorizer", c.db, rc)
	}
	return nil
}

// releaseAuthorizer detaches the authorizer hook and frees the cgo
// handle. Called from Close.
func (c *Conn) releaseAuthorizer() {
	C.sqlitelocal_set_authorizer(c.db, C.uintptr_t(0))
	if c.authzHandle != 0 {
		c.authzHandle.Delete()
		c.authzHandle = 0
	}
}

// WithInternal runs fn with reserved-prefix deny rules bypassed for any
// SQL prepared during fn. Used by platform code (migrations, trigger
// regen, schema log writes, mutation log corrections, undo/redo stack
// writes, skill CRUD).
//
// Nesting is supported: an outer scope's bypass is preserved across
// inner scopes.
//
// Not safe for concurrent calls on the same Conn — relies on cache.Use
// serializing goroutines per database.
func (c *Conn) WithInternal(fn func() error) error {
	prev := c.parseCtx.internal
	c.parseCtx.internal = true
	defer func() { c.parseCtx.internal = prev }()
	return fn()
}

// authorize implements the deny rules. Called synchronously by SQLite
// during prepare via the cgo thunk → goAuthorizer dispatcher.
//
// Returns SQLITE_OK to allow, SQLITE_DENY to reject.
//
// Arguments are SQLite's authorizer params; their meaning varies by
// action code. General shape:
//   - a1 — primary target name (table for READ/INSERT/UPDATE/DELETE/CREATE TABLE/DROP TABLE,
//     index name for CREATE/DROP INDEX, trigger name for CREATE/DROP TRIGGER,
//     pragma name for PRAGMA, attached filename for ATTACH).
//   - a2 — secondary target (column name for READ/UPDATE, parent table for
//     CREATE INDEX/TRIGGER, table name for ALTER TABLE, pragma value for PRAGMA).
//   - a3 — database name (usually "main"); ignored here.
//   - a4 — inner-most trigger or view name responsible for the access
//     (empty string at top level; set when SQLite is compiling a trigger
//     body fired by the parent statement).
//
// See https://www.sqlite.org/c3ref/c_alter_table.html for the per-action
// argument table.
//
// Trigger-body actions on reserved tables are allowed only for platform-installed triggers, recognised by their
// reserved name. A tenant trigger's body is never authorized at CREATE TRIGGER time — SQLite compiles it when the
// firing statement is prepared — so it is checked here, where a4 names the trigger. Nameless triggers (foreign-key
// actions) count as top-level.
//
// Reads on reserved-prefix tables are always allowed (audit visibility).
// Top-level writes, schema changes, and indexed/triggered access on
// reserved objects are denied unless c.parseCtx.internal is true.
func (c *Conn) authorize(action int, a1, a2, _, a4 string) int {
	fromTrigger := a4 != ""
	// Tenant triggers cannot take a reserved name (see SQLITE_CREATE_TRIGGER below), so the name proves platform
	// ownership of the firing trigger.
	platformTrigger := fromTrigger && isReservedName(a4)

	if !c.parseCtx.internal && isTempObjectAction(action) {
		// Temporary objects live on the shared per-database connection rather than in the bdb file: every member
		// would see them, they shadow same-named main tables, and they escape the change log.
		return c.deny(domain.DenyTempObject)
	}

	switch action {
	case C.SQLITE_READ:
		if a1 != "" {
			c.recordTable(a1)
		}
		return C.SQLITE_OK

	case C.SQLITE_SELECT, C.SQLITE_FUNCTION, C.SQLITE_RECURSIVE:
		return C.SQLITE_OK

	case C.SQLITE_TRANSACTION, C.SQLITE_SAVEPOINT:
		// BEGIN, COMMIT, ROLLBACK, SAVEPOINT, RELEASE. Transaction
		// boundaries are managed by the platform — the adapter wraps
		// its own BEGIN/COMMIT/ROLLBACK in WithInternal so they pass
		// through; tenant SQL is denied so it can't corrupt an outer
		// transaction.
		if c.parseCtx.internal {
			return C.SQLITE_OK
		}
		return c.deny(domain.DenyTransactionControl)

	case C.SQLITE_INSERT, C.SQLITE_UPDATE, C.SQLITE_DELETE:
		if isReservedName(a1) && !c.parseCtx.internal && !platformTrigger {
			return c.deny(domain.DenyReservedPrefix)
		}
		if !fromTrigger {
			c.parseCtx.dml = true
			switch action {
			case C.SQLITE_INSERT:
				c.parseCtx.recordDMLVerb(domain.StatementVerbInsert)
			case C.SQLITE_UPDATE:
				c.parseCtx.recordDMLVerb(domain.StatementVerbUpdate)
			case C.SQLITE_DELETE:
				c.parseCtx.recordDMLVerb(domain.StatementVerbDelete)
			}
			if a1 != "" {
				c.recordTable(a1)
			}
		}
		return C.SQLITE_OK

	case C.SQLITE_CREATE_TABLE, C.SQLITE_CREATE_TEMP_TABLE:
		if isReservedName(a1) && !c.parseCtx.internal {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLCreateTable, a1)
		c.recordCreated(a1)
		return C.SQLITE_OK

	case C.SQLITE_DROP_TABLE, C.SQLITE_DROP_TEMP_TABLE:
		if isReservedName(a1) && !c.parseCtx.internal {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLDropTable, a1)
		return C.SQLITE_OK

	case C.SQLITE_ALTER_TABLE:
		// arg1 = database name (e.g. "main"), arg2 = table name.
		if isReservedName(a2) && !c.parseCtx.internal {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLAlterTable, a2)
		return C.SQLITE_OK

	case C.SQLITE_CREATE_INDEX, C.SQLITE_CREATE_TEMP_INDEX:
		// arg1 = index name, arg2 = table name.
		if (isReservedName(a1) || isReservedName(a2)) && !c.parseCtx.internal {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLCreateIndex, a2)
		c.recordCreated(a1)
		return C.SQLITE_OK

	case C.SQLITE_DROP_INDEX, C.SQLITE_DROP_TEMP_INDEX:
		// Cascading DROP from a previously-authorized parent DDL (e.g.
		// DROP TABLE cascading to its indexes) is allowed even on
		// reserved-prefix names — the parent's authorization implies
		// the cascade is intended.
		isCascade := c.parseCtx.ddl != domain.DDLUnknown
		if (isReservedName(a1) || isReservedName(a2)) && !c.parseCtx.internal && !isCascade {
			return c.deny(domain.DenyReservedPrefix)
		}
		// Records the index name (DROP INDEX surfaces the index, not its parent table).
		c.recordDDL(domain.DDLDropIndex, a1)
		return C.SQLITE_OK

	case C.SQLITE_CREATE_TRIGGER, C.SQLITE_CREATE_TEMP_TRIGGER:
		// arg1 = trigger name, arg2 = table name.
		if (isReservedName(a1) || isReservedName(a2)) && !c.parseCtx.internal {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLCreateTrigger, a2)
		c.recordCreated(a1)
		return C.SQLITE_OK

	case C.SQLITE_DROP_TRIGGER, C.SQLITE_DROP_TEMP_TRIGGER:
		// DROP TABLE cascades to its dependent _change_log_* triggers;
		// allow that cascade since the parent DROP was already authorized.
		isCascade := c.parseCtx.ddl != domain.DDLUnknown
		if (isReservedName(a1) || isReservedName(a2)) && !c.parseCtx.internal && !isCascade {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLDropTrigger, a1)
		return C.SQLITE_OK

	case C.SQLITE_CREATE_VIEW, C.SQLITE_CREATE_TEMP_VIEW:
		if isReservedName(a1) && !c.parseCtx.internal {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLCreateView, a1)
		c.recordCreated(a1)
		return C.SQLITE_OK

	case C.SQLITE_DROP_VIEW, C.SQLITE_DROP_TEMP_VIEW:
		if isReservedName(a1) && !c.parseCtx.internal {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLDropView, a1)
		return C.SQLITE_OK

	case C.SQLITE_PRAGMA:
		// arg1 = pragma name, arg2 = value (NULL on a read).
		if c.parseCtx.internal {
			return C.SQLITE_OK
		}
		if isAllowedPragma(a1, a2) {
			return C.SQLITE_OK
		}
		return c.deny(domain.DenyPragmaNotAllowed)

	case C.SQLITE_ATTACH, C.SQLITE_DETACH:
		return c.deny(domain.DenyAttachDetach)

	case C.SQLITE_REINDEX:
		// CREATE INDEX (and table rewrites) populate the new index through this same action, so it surfaces
		// inside legitimate DDL — recognisable because a DDL action has already fired in this prepare. An explicit
		// REINDEX has no such prefix: it rebuilds every index it names (all of them in the bare form) with a full
		// scan per index and prepares as read-only, so a read-only member could otherwise trigger it. Index
		// maintenance is the platform's job.
		if c.parseCtx.internal || c.parseCtx.ddl != domain.DDLUnknown {
			return C.SQLITE_OK
		}
		return c.deny(domain.DenyReindex)

	case C.SQLITE_CREATE_VTABLE:
		// arg1 = virtual table name, arg2 = module name.
		// Only the FTS5 module family is allowed; everything else stays
		// denied (csv reads filesystem, dbstat leaks internals,
		// fts3_tokenizer registers raw pointers, etc.).
		if !isAllowedVTableModule(a2) {
			return c.deny(domain.DenyVTableModuleNotAllowed)
		}
		if isReservedName(a1) && !c.parseCtx.internal {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLCreateTable, a1)
		c.recordCreated(a1)
		return C.SQLITE_OK

	case C.SQLITE_DROP_VTABLE:
		// arg1 = virtual table name, arg2 = module name. The module
		// allowlist isn't re-checked here — anything that exists was
		// authorized at creation time, and tenants should always be
		// able to clean up a table they own. Reserved-prefix still
		// applies (and cascades through, mirroring SQLITE_DROP_TABLE).
		isCascade := c.parseCtx.ddl != domain.DDLUnknown
		if isReservedName(a1) && !c.parseCtx.internal && !isCascade {
			return c.deny(domain.DenyReservedPrefix)
		}
		c.recordDDL(domain.DDLDropTable, a1)
		return C.SQLITE_OK

	default:
		// Fail-safe: any action code we don't explicitly allow is denied,
		// including future SQLite action codes added in later versions.
		// Currently this rejects: SQLITE_ANALYZE — no tenant or platform
		// code path exercises it today. If a new use case needs one of
		// these, add an explicit case.
		return C.SQLITE_DENY
	}
}

// isReservedName reports whether name uses the platform-reserved prefix (leading underscore).
func isReservedName(name string) bool {
	return strings.HasPrefix(name, "_")
}

// isTempObjectAction reports whether action creates an object in the temp schema.
func isTempObjectAction(action int) bool {
	switch action {
	case C.SQLITE_CREATE_TEMP_TABLE, C.SQLITE_CREATE_TEMP_INDEX, C.SQLITE_CREATE_TEMP_TRIGGER,
		C.SQLITE_CREATE_TEMP_VIEW:
		return true
	}
	return false
}

// isSQLiteInternal reports whether name is a SQLite system table
// (sqlite_master, sqlite_sequence, sqlite_temp_master, etc.). The
// authorizer fires SQLITE_READ on these during DDL compilation; they
// shouldn't show up in target-table extraction since they're not
// user-visible objects.
func isSQLiteInternal(name string) bool {
	return strings.HasPrefix(name, "sqlite_")
}

// recordTable appends name to parseCtx.tables, filtering out empty
// names and SQLite internal tables.
func (c *Conn) recordTable(name string) {
	if name == "" || isSQLiteInternal(name) {
		return
	}
	c.parseCtx.tables = append(c.parseCtx.tables, name)
}

// recordCreated appends the name of a schema object being created (table, index, trigger, view, or virtual
// table) to parseCtx.created, filtering out empty names.
func (c *Conn) recordCreated(name string) {
	if name == "" {
		return
	}
	c.parseCtx.created = append(c.parseCtx.created, name)
}

// recordDDL records the DDL kind and target name for the user's
// top-level DDL statement. SQLite fires additional DDL action codes
// for cascades (e.g. dropping a table cascades to dropping its
// triggers/indexes); those are ignored — only the first DDL action
// per parse is captured.
func (c *Conn) recordDDL(kind domain.DDLType, target string) {
	if c.parseCtx.ddl != domain.DDLUnknown {
		return // cascade — keep the first-recorded DDL kind + target
	}
	c.parseCtx.ddl = kind
	c.recordTable(target)
}

// deny records why the authorizer is rejecting and returns SQLITE_DENY.
// Returns int (not C.int) because the call site is the int-returning
// authorize() method.
func (c *Conn) deny(reason domain.DenyReason) int {
	c.parseCtx.denyReason = reason
	return C.SQLITE_DENY
}

// introspectionPragmas are read-only schema/structure inspection pragmas
// allowed for tenant SQL. arg2 may be a table/index name (not a setter
// value), so we don't gate on arg2's presence.
//
// State-mutating or sensitive pragmas (journal_mode, user_version,
// max_page_count, cipher_*, key, etc.) are NOT in this set. Platform
// code that needs them wraps the call in Conn.WithInternal.
var introspectionPragmas = map[string]bool{
	"table_info":        true,
	"foreign_key_list":  true,
	"index_list":        true,
	"index_info":        true,
	"table_xinfo":       true,
	"foreign_key_check": true,
	"integrity_check":   true,
	"schema_version":    true,
	// table_list classifies every object as table/view/virtual/shadow (the
	// module itself answers via xShadowName), which is how table listings
	// exclude virtual-table internals without name heuristics.
	"table_list": true,
	// data_version is read-only and returns an integer that bumps on any
	// data change. FTS5 fires it internally during INSERT/UPDATE prepare
	// to detect cache staleness — denying it would block any DML against
	// a table that has an FTS5 index attached via triggers.
	"data_version": true,
}

// isAllowedPragma reports whether a tenant-mode PRAGMA call is allowed.
// arg2 (the pragma's value/argument) is intentionally ignored —
// arg2-bearing introspection pragmas like PRAGMA table_info('foo') are
// allowed; setter forms of state-mutating pragmas aren't on the
// allowlist regardless of arg2.
func isAllowedPragma(name, _ string) bool {
	return introspectionPragmas[strings.ToLower(name)]
}

// allowedVTableModules is the set of SQLite virtual-table modules a
// tenant may instantiate via CREATE VIRTUAL TABLE. Limited to FTS5 (and
// its read-only `fts5vocab` helper) — every other built-in module
// either has security implications (csv reads filesystem, dbstat leaks
// internals, fts3_tokenizer registers raw pointers) or is unused.
var allowedVTableModules = map[string]bool{
	"fts5":      true,
	"fts5vocab": true,
}

// isAllowedVTableModule reports whether a CREATE VIRTUAL TABLE may
// instantiate the given module. Match is case-insensitive — SQLite
// itself is case-insensitive on module names.
func isAllowedVTableModule(module string) bool {
	return allowedVTableModules[strings.ToLower(module)]
}

//export goAuthorizer
func goAuthorizer(handle C.uintptr_t, action C.int, a1, a2, a3, a4 *C.char) C.int {
	h := cgo.Handle(handle)
	if h == 0 {
		return C.SQLITE_OK
	}
	conn, ok := h.Value().(*Conn)
	if !ok || conn == nil {
		return C.SQLITE_OK
	}
	return C.int(conn.authorize(int(action), goStr(a1), goStr(a2), goStr(a3), goStr(a4)))
}

func goStr(p *C.char) string {
	if p == nil {
		return ""
	}
	return C.GoString(p)
}
