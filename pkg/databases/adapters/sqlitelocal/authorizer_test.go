package sqlitelocal

import (
	"context"
	"strings"
	"testing"
)

// authzConn opens a fresh Conn and creates an unrestricted user table,
// so deny-rule tests can exercise both the user surface and the reserved
// surface.
func authzConn(t *testing.T) *Conn {
	t.Helper()
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := c.WithInternal(func() error {
		_, err := c.Exec(context.Background(), "CREATE TABLE recipes (id INTEGER PRIMARY KEY, name TEXT)")
		return err
	}); err != nil {
		tClose(t, "conn", c)
		t.Fatalf("seed: %v", err)
	}
	return c
}

func TestAuthorizer_RejectsReservedTableInsert(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(), "INSERT INTO _mutation_log (table_name) VALUES ('x')"); err == nil {
		t.Fatal("expected denial for write to _mutation_log, got nil")
	}
}

func TestAuthorizer_AllowsReservedTableSelect(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if err := c.WithInternal(func() error {
		_, err := c.Exec(context.Background(), "CREATE TABLE _audit (id INTEGER, msg TEXT)")
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := c.Query(context.Background(), "SELECT id, msg FROM _audit")
	if err != nil {
		t.Fatalf("SELECT on _audit should be allowed, got: %v", err)
	}
	tClose(t, "rows", rows)
}

func TestAuthorizer_RejectsReservedTrigger_Drop(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if err := c.WithInternal(func() error {
		_, err := c.Exec(context.Background(),
			"CREATE TRIGGER _mutation_log_recipes_after_insert AFTER INSERT ON recipes BEGIN SELECT 1; END")
		return err
	}); err != nil {
		t.Fatalf("seeding platform trigger: %v", err)
	}
	if _, err := c.Exec(context.Background(), "DROP TRIGGER _mutation_log_recipes_after_insert"); err == nil {
		t.Fatal("expected denial for DROP TRIGGER on reserved trigger name, got nil")
	}
}

func TestAuthorizer_RejectsShadowTrigger_Create(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(),
		"CREATE TRIGGER _evil AFTER INSERT ON recipes BEGIN SELECT 1; END"); err == nil {
		t.Fatal("expected denial for CREATE TRIGGER with reserved-prefix name, got nil")
	}
}

func TestAuthorizer_RejectsTriggerOnReservedTable(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if err := c.WithInternal(func() error {
		_, err := c.Exec(context.Background(), "CREATE TABLE _audit (id INTEGER, msg TEXT)")
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := c.Exec(context.Background(),
		"CREATE TRIGGER user_t AFTER INSERT ON _audit BEGIN SELECT 1; END"); err == nil {
		t.Fatal("expected denial for CREATE TRIGGER targeting a reserved table, got nil")
	}
}

func TestAuthorizer_RejectsTenantBegin(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(), "BEGIN"); err == nil {
		t.Fatal("expected denial for tenant BEGIN, got nil")
	}
}

func TestAuthorizer_RejectsTenantCommit(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(), "COMMIT"); err == nil {
		t.Fatal("expected denial for tenant COMMIT, got nil")
	}
}

func TestAuthorizer_RejectsTenantRollback(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(), "ROLLBACK"); err == nil {
		t.Fatal("expected denial for tenant ROLLBACK, got nil")
	}
}

func TestAuthorizer_RejectsTenantSavepoint(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(), "SAVEPOINT s1"); err == nil {
		t.Fatal("expected denial for tenant SAVEPOINT, got nil")
	}
}

func TestAuthorizer_RejectsTenantRelease(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if err := c.WithInternal(func() error {
		_, e := c.Exec(context.Background(), "SAVEPOINT internal_sp")
		return e
	}); err != nil {
		t.Fatalf("seed SAVEPOINT under WithInternal: %v", err)
	}
	if _, err := c.Exec(context.Background(), "RELEASE internal_sp"); err == nil {
		t.Fatal("expected denial for tenant RELEASE, got nil")
	}
	// Release the savepoint via internal mode so the test exits cleanly.
	if err := c.WithInternal(func() error {
		_, e := c.Exec(context.Background(), "RELEASE internal_sp")
		return e
	}); err != nil {
		t.Fatalf("cleanup RELEASE: %v", err)
	}
}

func TestAuthorizer_AllowsTransactionControlInternal(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	// Regression: platform code (executeTransaction) must still be able
	// to drive BEGIN/COMMIT/ROLLBACK via WithInternal.
	if err := c.WithInternal(func() error {
		if _, e := c.Exec(context.Background(), "BEGIN DEFERRED"); e != nil {
			return e
		}
		if _, e := c.Exec(context.Background(), "COMMIT"); e != nil {
			return e
		}
		return nil
	}); err != nil {
		t.Fatalf("BEGIN/COMMIT under WithInternal should succeed, got: %v", err)
	}
}

func TestAuthorizer_RejectsAttach(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(), "ATTACH DATABASE ':memory:' AS aux"); err == nil {
		t.Fatal("expected denial for ATTACH, got nil")
	}
}

func TestAuthorizer_RejectsPragmaWrite(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(), "PRAGMA journal_mode = OFF"); err == nil {
		t.Fatal("expected denial for PRAGMA journal_mode write, got nil")
	}
}

func TestAuthorizer_AllowsAllowlistedPragmaRead(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	rows, err := c.Query(context.Background(), "PRAGMA table_info('recipes')")
	if err != nil {
		t.Fatalf("PRAGMA table_info should be allowed, got: %v", err)
	}
	tClose(t, "rows", rows)
}

func TestAuthorizer_WithInternal_AllowsReservedWrites(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	err := c.WithInternal(func() error {
		_, e := c.Exec(context.Background(), "CREATE TABLE _internal_t (id INTEGER, msg TEXT)")
		return e
	})
	if err != nil {
		t.Fatalf("WithInternal should allow CREATE _internal_t, got: %v", err)
	}
	// Same SQL outside WithInternal: denied.
	if _, err := c.Exec(context.Background(), "CREATE TABLE _other_t (id INTEGER)"); err == nil {
		t.Fatal("expected denial for CREATE TABLE _other_t outside WithInternal, got nil")
	}
}

func TestAuthorizer_WithInternal_NestedScope(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	err := c.WithInternal(func() error {
		return c.WithInternal(func() error {
			_, e := c.Exec(context.Background(), "CREATE TABLE _nested_t (id INTEGER)")
			return e
		})
	})
	if err != nil {
		t.Fatalf("nested WithInternal should allow CREATE, got: %v", err)
	}
	// After both scopes return, deny rules are restored.
	if _, err := c.Exec(context.Background(), "CREATE TABLE _post_nested (id INTEGER)"); err == nil {
		t.Fatal("expected deny rules to be restored after nested WithInternal, got nil")
	}
}

func TestAuthorizer_AllowsUserTables(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(), "INSERT INTO recipes (name) VALUES ('carbonara')"); err != nil {
		t.Fatalf("user-table INSERT should be allowed, got: %v", err)
	}
	rows, err := c.Query(context.Background(), "SELECT name FROM recipes")
	if err != nil {
		t.Fatalf("user-table SELECT should be allowed, got: %v", err)
	}
	defer tClose(t, "rows", rows)
	if !rows.Next() {
		t.Fatal("expected one row")
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if name != "carbonara" {
		t.Errorf("name = %q, want carbonara", name)
	}
}

func TestAuthorizer_DenialErrorMentionsAuthorization(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	// Seeds a real platform-style table so DROP fails on authorization,
	// not on schema lookup (schema lookup runs before the authorizer).
	if err := c.WithInternal(func() error {
		_, e := c.Exec(context.Background(), "CREATE TABLE _platform_t (id INTEGER)")
		return e
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := c.Exec(context.Background(), "DROP TABLE _platform_t")
	if err == nil {
		t.Fatal("expected denial, got nil")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("denial error should mention authorization, got: %v", err)
	}
}

// --- CREATE VIRTUAL TABLE: FTS5 carve-out ---------------------------------

// TestAuthorizer_AllowsCreateVirtualTable_FTS5 confirms an external-content
// FTS5 table can be created against a tenant user table. This is the
// load-bearing positive case — the Notes template's `notes_fts` virtual
// table depends on this carve-out.
func TestAuthorizer_AllowsCreateVirtualTable_FTS5(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(),
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT)"); err != nil {
		t.Fatalf("seed notes: %v", err)
	}
	if _, err := c.Exec(context.Background(),
		"CREATE VIRTUAL TABLE notes_fts USING fts5(title, body, content='notes', content_rowid='id')"); err != nil {
		t.Fatalf("CREATE VIRTUAL TABLE USING fts5 should be allowed: %v", err)
	}
}

// TestAuthorizer_AllowsCreateVirtualTable_FTS5Vocab confirms the read-only
// fts5vocab helper module is also on the allowlist.
func TestAuthorizer_AllowsCreateVirtualTable_FTS5Vocab(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(),
		"CREATE VIRTUAL TABLE notes_fts USING fts5(body)"); err != nil {
		t.Fatalf("seed fts5: %v", err)
	}
	if _, err := c.Exec(context.Background(),
		"CREATE VIRTUAL TABLE notes_fts_v USING fts5vocab(notes_fts, 'row')"); err != nil {
		t.Fatalf("CREATE VIRTUAL TABLE USING fts5vocab should be allowed: %v", err)
	}
}

// TestAuthorizer_RejectsCreateVirtualTable_NonAllowlistedModules confirms
// every other module is rejected with SQLITE_AUTH (proving the deny path,
// not a "no such module" error). Module presence in the SQLite build
// doesn't matter — the authorizer fires before module resolution.
func TestAuthorizer_RejectsCreateVirtualTable_NonAllowlistedModules(t *testing.T) {
	modules := []string{
		"csv",            // reads filesystem
		"dbstat",         // exposes internals
		"fts3_tokenizer", // registers raw tokenizer pointers
		"fts4",           // older FTS, redundant with fts5
		"fts3",
		"made_up_module",
	}
	for _, mod := range modules {
		t.Run(mod, func(t *testing.T) {
			c := authzConn(t)
			defer tClose(t, "conn", c)
			sql := "CREATE VIRTUAL TABLE evil USING " + mod + "(x)"
			_, err := c.Exec(context.Background(), sql)
			if err == nil {
				t.Fatalf("expected denial for module %q, got nil", mod)
			}
			if !IsCode(err, CodeAuth) {
				t.Errorf("expected SQLITE_AUTH for module %q, got: %v", mod, err)
			}
		})
	}
}

// TestAuthorizer_RejectsCreateVirtualTable_ReservedPrefix confirms the
// reserved-prefix rule still applies to FTS5 vtable names — a tenant
// can't squat on a `_`-prefixed name even with an allowlisted module.
func TestAuthorizer_RejectsCreateVirtualTable_ReservedPrefix(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	_, err := c.Exec(context.Background(),
		"CREATE VIRTUAL TABLE _shadow USING fts5(body)")
	if err == nil {
		t.Fatal("expected denial for reserved-prefix vtable name, got nil")
	}
	if !IsCode(err, CodeAuth) {
		t.Errorf("expected SQLITE_AUTH, got: %v", err)
	}
}

// TestAuthorizer_AllowsCreateVirtualTable_ReservedPrefixUnderInternal
// confirms platform code can still seed `_`-prefixed virtual tables
// (parity with regular CREATE TABLE under WithInternal).
func TestAuthorizer_AllowsCreateVirtualTable_ReservedPrefixUnderInternal(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	err := c.WithInternal(func() error {
		_, e := c.Exec(context.Background(),
			"CREATE VIRTUAL TABLE _platform_fts USING fts5(body)")
		return e
	})
	if err != nil {
		t.Fatalf("WithInternal should allow reserved-prefix fts5 vtable, got: %v", err)
	}
}

// TestAuthorizer_AllowsDropVirtualTable confirms DROP TABLE on an fts5
// virtual table succeeds. SQLite cascades to the shadow tables
// (`<name>_data`, `_idx`, `_docsize`, `_config`) under the parent's
// authorization — those cascading SQLITE_DROP_TABLE actions must not
// trip the reserved-prefix or default-deny rules.
func TestAuthorizer_AllowsDropVirtualTable(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	if _, err := c.Exec(context.Background(),
		"CREATE VIRTUAL TABLE notes_fts USING fts5(body)"); err != nil {
		t.Fatalf("seed fts5: %v", err)
	}
	if _, err := c.Exec(context.Background(), "DROP TABLE notes_fts"); err != nil {
		t.Fatalf("DROP TABLE on fts5 vtable should be allowed: %v", err)
	}
}

// TestAuthorizer_FTS5SyncTriggersFire is the end-to-end smoke test that
// the Notes template pattern works under the authorizer: user table +
// external-content fts5 vtable + AFTER INSERT trigger that mirrors writes
// into the index. Exercises trigger creation, insert-fires-trigger, and
// MATCH lookup paths through the tenant authorizer.
func TestAuthorizer_FTS5SyncTriggersFire(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	ctx := context.Background()
	stmts := []string{
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT)",
		"CREATE VIRTUAL TABLE notes_fts USING fts5(title, body, content='notes', content_rowid='id')",
		"CREATE TRIGGER notes_ai AFTER INSERT ON notes " +
			"BEGIN INSERT INTO notes_fts(rowid, title, body) VALUES (new.id, new.title, new.body); END",
		"INSERT INTO notes(id, title, body) VALUES (1, 'hello', 'docker compose tutorial')",
	}
	for _, sql := range stmts {
		if _, err := c.Exec(ctx, sql); err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	rows, err := c.Query(ctx, "SELECT rowid FROM notes_fts WHERE notes_fts MATCH 'docker'")
	if err != nil {
		t.Fatalf("MATCH query: %v", err)
	}
	defer tClose(t, "rows", rows)
	if !rows.Next() {
		t.Fatal("expected one MATCH row, got none — sync trigger didn't fire or FTS index missed the row")
	}
	var rowid int64
	if err := rows.Scan(&rowid); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rowid != 1 {
		t.Errorf("rowid = %d, want 1", rowid)
	}
}

// TestIsAllowedVTableModule is a tiny unit-level guard against
// allowlist drift — adding/removing a module silently elsewhere will
// fail one of these explicit cases.
func TestIsAllowedVTableModule(t *testing.T) {
	cases := map[string]bool{
		"fts5":           true,
		"FTS5":           true, // case-insensitive
		"fts5vocab":      true,
		"FTS5VOCAB":      true,
		"csv":            false,
		"dbstat":         false,
		"fts3_tokenizer": false,
		"fts4":           false,
		"":               false,
	}
	for module, want := range cases {
		if got := isAllowedVTableModule(module); got != want {
			t.Errorf("isAllowedVTableModule(%q) = %v, want %v", module, got, want)
		}
	}
}

// TestAuthorizer_RejectsTenantTriggerBodyWriteToReservedTable covers the trigger-body path: SQLite compiles a
// trigger body only when the firing statement is prepared, so a tenant trigger whose body writes a reserved table
// must be denied at that point — creating the trigger itself is legitimately allowed (it targets a user table).
func TestAuthorizer_RejectsTenantTriggerBodyWriteToReservedTable(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	ctx := context.Background()
	if err := c.WithInternal(func() error {
		_, err := c.Exec(ctx, "CREATE TABLE _audit (id INTEGER PRIMARY KEY, msg TEXT)")
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := c.Exec(ctx,
		"CREATE TRIGGER leak AFTER INSERT ON recipes BEGIN INSERT INTO _audit(msg) VALUES ('x'); END"); err != nil {
		t.Fatalf("creating a tenant trigger on a user table must be allowed, got: %v", err)
	}
	if _, err := c.Exec(ctx, "INSERT INTO recipes (name) VALUES ('bread')"); err == nil {
		t.Fatal("expected denial: the tenant trigger body writes to a reserved table, got nil")
	}
	rows, err := c.Query(ctx, "SELECT count(*) FROM _audit")
	if err != nil {
		t.Fatalf("counting _audit: %v", err)
	}
	defer tClose(t, "rows", rows)
	var n int64
	if !rows.Next() {
		t.Fatal("no count row")
	}
	if err := rows.Scan(&n); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 0 {
		t.Errorf("_audit rows = %d, want 0 (the trigger body must not have run)", n)
	}
}

// TestAuthorizer_AllowsPlatformTriggerBodyWriteToReservedTable is the control case: a platform-installed trigger
// (reserved name, created in internal mode) writing a reserved table must keep firing on tenant DML — that is how
// the change-log triggers record every write.
func TestAuthorizer_AllowsPlatformTriggerBodyWriteToReservedTable(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	ctx := context.Background()
	if err := c.WithInternal(func() error {
		if _, err := c.Exec(ctx, "CREATE TABLE _audit (id INTEGER PRIMARY KEY, msg TEXT)"); err != nil {
			return err
		}
		_, err := c.Exec(ctx, "CREATE TRIGGER _change_log_recipes_after_insert AFTER INSERT ON recipes "+
			"BEGIN INSERT INTO _audit(msg) VALUES (new.name); END")
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := c.Exec(ctx, "INSERT INTO recipes (name) VALUES ('bread')"); err != nil {
		t.Fatalf("tenant INSERT must succeed with the platform trigger attached, got: %v", err)
	}
	rows, err := c.Query(ctx, "SELECT msg FROM _audit")
	if err != nil {
		t.Fatalf("reading _audit: %v", err)
	}
	defer tClose(t, "rows", rows)
	if !rows.Next() {
		t.Fatal("expected the platform trigger to have written one _audit row, got none")
	}
}

// TestAuthorizer_RejectsTenantTempObjects covers temp-schema objects: they live on the connection rather than in the
// database file, so tenant SQL must not create them while platform code (internal mode) still can.
func TestAuthorizer_RejectsTenantTempObjects(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	ctx := context.Background()
	for _, sql := range []string{
		"CREATE TEMP TABLE scratch (x INTEGER)",
		"CREATE TEMP VIEW recipes_v AS SELECT id FROM recipes",
		"CREATE TEMP TRIGGER recipes_t AFTER INSERT ON recipes BEGIN SELECT 1; END",
	} {
		if _, err := c.Exec(ctx, sql); err == nil {
			t.Errorf("%q: expected denial, got nil", sql)
		}
	}
	if err := c.WithInternal(func() error {
		_, err := c.Exec(ctx, "CREATE TEMP TABLE _captured (rowid INTEGER PRIMARY KEY, value TEXT)")
		return err
	}); err != nil {
		t.Fatalf("internal-mode CREATE TEMP TABLE must stay allowed, got: %v", err)
	}
}

// TestAuthorizer_RejectsTenantReindex covers REINDEX in every form for tenant SQL; internal mode keeps it available.
func TestAuthorizer_RejectsTenantReindex(t *testing.T) {
	c := authzConn(t)
	defer tClose(t, "conn", c)
	ctx := context.Background()
	if _, err := c.Exec(ctx, "CREATE INDEX recipes_name ON recipes (name)"); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	for _, sql := range []string{"REINDEX", "REINDEX recipes", "REINDEX recipes_name"} {
		if _, err := c.Exec(ctx, sql); err == nil {
			t.Errorf("%q: expected denial, got nil", sql)
		}
	}
	if err := c.WithInternal(func() error {
		_, err := c.Exec(ctx, "REINDEX")
		return err
	}); err != nil {
		t.Fatalf("internal-mode REINDEX must stay allowed, got: %v", err)
	}
}
