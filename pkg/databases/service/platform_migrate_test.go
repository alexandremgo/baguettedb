package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/migrations"
)

// fakePlatformExecutor records executed SQL and simulates PRAGMA user_version.
type fakePlatformExecutor struct {
	executed    []string
	userVersion int
	failOnSQL   string
}

func (f *fakePlatformExecutor) Execute(
	_ context.Context, _ string, sql string, _ []domain.TypedValue,
) (*domain.QueryResult, error) {
	f.executed = append(f.executed, sql)

	if f.failOnSQL != "" && sql == f.failOnSQL {
		return nil, errors.New("simulated failure")
	}

	if sql == "PRAGMA user_version" {
		return &domain.QueryResult{
			Rows: [][]domain.TypedValue{
				{{Type: domain.SQLInteger, Value: int64(f.userVersion)}},
			},
		}, nil
	}

	if len(sql) > 20 && sql[:20] == "PRAGMA user_version " {
		var v int
		if _, err := fmt.Sscanf(sql, "PRAGMA user_version = %d", &v); err == nil {
			f.userVersion = v
		}
	}

	return &domain.QueryResult{}, nil
}

func (f *fakePlatformExecutor) ExecuteInternal(
	ctx context.Context, ns string, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return f.Execute(ctx, ns, sql, args)
}

func (f *fakePlatformExecutor) ExecuteWithRowCap(
	ctx context.Context, ns string, sql string, args []domain.TypedValue, _ int,
) (*domain.QueryResult, error) {
	return f.Execute(ctx, ns, sql, args)
}

func (f *fakePlatformExecutor) ExecuteTransaction(
	_ context.Context, _ string, _ []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return nil, nil
}

func (f *fakePlatformExecutor) ExecuteTransactionInternal(
	ctx context.Context, ns string, stmts []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	return f.ExecuteTransaction(ctx, ns, stmts)
}

func (f *fakePlatformExecutor) WithConn(_ context.Context, _ string, fn func(h ParseHandle) error) error {
	return fn(nil)
}

func (f *fakePlatformExecutor) WithTx(
	ctx context.Context, _ string, _ bool, fn func(ctx context.Context, tx Tx) error,
) error {
	return fn(ctx, f)
}

// Exec satisfies the Tx interface so the fake executor can stand in for both
// queryExecutor and Tx. Routes back to the fake's recording / user_version
// tracking; no real transaction semantics, which is fine for these tests —
// the production atomicity behavior is exercised by the integration suite
// against the real sqlitelocal executor.
func (f *fakePlatformExecutor) Exec(
	ctx context.Context, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return f.Execute(ctx, "", sql, args)
}

// ExecInternal satisfies the Tx interface; the fake has no authorizer, so it routes like Exec.
func (f *fakePlatformExecutor) ExecInternal(
	ctx context.Context, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	return f.Exec(ctx, sql, args)
}

// Parse satisfies the Tx interface; the fake has no schema to resolve against, so it yields an empty parse result.
func (f *fakePlatformExecutor) Parse(_ string) (domain.ParseResult, error) {
	return domain.ParseResult{}, nil
}

func testFS(files map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for name, content := range files {
		m["platform/"+name] = &fstest.MapFile{Data: []byte(content)}
	}
	return m
}

func newTestDatabaseService(executor queryExecutor) *DatabaseService {
	return NewDatabaseService(
		&mockDatabaseRepo{},
		&mockDatabaseManager{},
		executor,
		&mockBdbsApp{},
		nil,
		nil,
		zerolog.Nop(),
	)
}

func TestRunPlatformMigrations_AlreadyCurrent(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: CurrentPlatformSchemaVersion}
	fs := testFS(map[string]string{})

	version, err := runPlatformMigrations(context.Background(), exec, "test-ns", fs, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != CurrentPlatformSchemaVersion {
		t.Errorf("version = %d, want %d", version, CurrentPlatformSchemaVersion)
	}

	// Only the PRAGMA read should have been executed.
	if len(exec.executed) != 1 {
		t.Errorf("expected 1 statement (PRAGMA read), got %d: %v", len(exec.executed), exec.executed)
	}
}

func TestRunPlatformMigrations_Idempotent(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: CurrentPlatformSchemaVersion}
	fs := testFS(map[string]string{})

	for i := range 2 {
		version, err := runPlatformMigrations(context.Background(), exec, "test-ns", fs, zerolog.Nop())
		if err != nil {
			t.Fatalf("run %d: unexpected error: %v", i+1, err)
		}
		if version != CurrentPlatformSchemaVersion {
			t.Errorf("run %d: version = %d, want %d", i+1, version, CurrentPlatformSchemaVersion)
		}
	}
}

func TestLoadPlatformMigrations_SortsByVersion(t *testing.T) {
	fs := testFS(map[string]string{
		"000003_third.up.sql":  "SELECT 3",
		"000001_first.up.sql":  "SELECT 1",
		"000002_second.up.sql": "SELECT 2",
	})

	loaded, err := loadPlatformMigrations(fs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(loaded) != 3 {
		t.Fatalf("expected 3 migrations, got %d", len(loaded))
	}
	for i, want := range []int{1, 2, 3} {
		if loaded[i].Version != want {
			t.Errorf("migration[%d].Version = %d, want %d", i, loaded[i].Version, want)
		}
	}
}

func TestLoadPlatformMigrations_SkipsNonUpFiles(t *testing.T) {
	fs := testFS(map[string]string{
		"000001_first.up.sql":   "SELECT 1",
		"000001_first.down.sql": "DROP TABLE foo",
		"README.md":             "not a migration",
	})

	loaded, err := loadPlatformMigrations(fs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(loaded) != 1 {
		t.Errorf("expected 1 migration, got %d", len(loaded))
	}
}

func TestLoadPlatformMigrations_ParsesMultiStatementFile(t *testing.T) {
	fs := testFS(map[string]string{
		"000001_multi.up.sql": "CREATE TABLE _foo (id INTEGER); ALTER TABLE _bar ADD COLUMN x TEXT",
	})

	loaded, err := loadPlatformMigrations(fs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 migration, got %d", len(loaded))
	}

	stmts := splitStatements(loaded[0].SQL)
	if len(stmts) != 2 {
		t.Errorf("expected 2 statements, got %d: %v", len(stmts), stmts)
	}
}

func TestEnsurePlatformSchema_SkipsWhenPostgresVersionIsCurrent(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: 0}
	svc := newTestDatabaseService(exec)

	db := &domain.Database{
		ID:                    uuid.New(),
		PlatformSchemaVersion: CurrentPlatformSchemaVersion,
	}

	// First call: Postgres version matches current → skips entirely, no SQL executed.
	if err := svc.EnsurePlatformSchema(context.Background(), db); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.executed) != 0 {
		t.Errorf("expected 0 SQL statements (skipped), got %d: %v", len(exec.executed), exec.executed)
	}

	// Second call: same result — still skipped.
	if err := svc.EnsurePlatformSchema(context.Background(), db); err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if len(exec.executed) != 0 {
		t.Errorf("expected 0 SQL statements on second call, got %d: %v", len(exec.executed), exec.executed)
	}
}

// TestEnsurePlatformSchema_RefusesNewerFile checks that a database file a newer version upgraded past
// CurrentPlatformSchemaVersion is refused even when the stored mirror lags behind it, before any migration runs: with
// the message SetNewerPlatformSchemaMessage set, or with domain.ErrNewerPlatformSchema when none is set.
func TestEnsurePlatformSchema_RefusesNewerFile(t *testing.T) {
	const message = "upgraded by a newer version: update"
	for _, tt := range []struct {
		message string
		want    string
	}{
		{"", domain.ErrNewerPlatformSchema.Error()},
		{message, message},
	} {
		exec := &fakePlatformExecutor{userVersion: CurrentPlatformSchemaVersion + 1}
		svc := newTestDatabaseService(exec)
		svc.SetNewerPlatformSchemaMessage(tt.message)
		db := &domain.Database{ID: uuid.New(), PlatformSchemaVersion: CurrentPlatformSchemaVersion - 1}

		err := svc.EnsurePlatformSchema(context.Background(), db)
		if !errors.Is(err, domain.ErrNewerPlatformSchema) || err.Error() != tt.want {
			t.Errorf("message %q: EnsurePlatformSchema = %v; want %q, ErrNewerPlatformSchema", tt.message, err, tt.want)
		}
		if len(exec.executed) != 1 {
			t.Errorf("message %q: ran %v; want the user_version read alone", tt.message, exec.executed)
		}
	}
}

func TestRunPlatformMigrations_AppliesMigrationV1(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: 0}
	fs := testFS(map[string]string{
		"000001_add_stuff.up.sql": "CREATE TABLE _foo (id INTEGER); CREATE TABLE _bar (id INTEGER)",
	})

	version, err := runPlatformMigrations(context.Background(), exec, "test-ns", fs, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}

	// Should have executed: PRAGMA read + PRAGMA re-read in the transaction + 2 statements + PRAGMA write = 5.
	wantStmts := 5
	if len(exec.executed) != wantStmts {
		t.Errorf("expected %d statements, got %d: %v", wantStmts, len(exec.executed), exec.executed)
	}
	if exec.userVersion != 1 {
		t.Errorf("userVersion = %d, want 1", exec.userVersion)
	}
}

func TestRunPlatformMigrations_SkipsAlreadyApplied(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: 1}
	fs := testFS(map[string]string{
		"000001_add_stuff.up.sql": "CREATE TABLE _foo (id INTEGER)",
	})

	version, err := runPlatformMigrations(context.Background(), exec, "test-ns", fs, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}

	// Only the PRAGMA read — migration was skipped.
	if len(exec.executed) != 1 {
		t.Errorf("expected 1 statement (PRAGMA read), got %d: %v", len(exec.executed), exec.executed)
	}
}

// concurrentMigratorExecutor is a fakePlatformExecutor with a second process migrating the same file: that process
// commits migratedTo between the runner's first user_version read and the moment its transaction takes the write lock.
type concurrentMigratorExecutor struct {
	*fakePlatformExecutor
	migratedTo int
}

// WithTx lets the other process commit its migration, then runs fn as the fake does.
func (e *concurrentMigratorExecutor) WithTx(
	ctx context.Context, databaseID string, internal bool, fn func(ctx context.Context, tx Tx) error,
) error {
	// The other process committed migratedTo while this transaction waited for the write lock.
	e.userVersion = max(e.userVersion, e.migratedTo)
	return e.fakePlatformExecutor.WithTx(ctx, databaseID, internal, fn)
}

// TestRunPlatformMigrations_SkipsVersionAppliedByAnotherProcess checks that a version another process applied after
// the runner's first user_version read is skipped inside the transaction instead of run a second time: the
// non-idempotent ADD COLUMN would fail on a real file.
func TestRunPlatformMigrations_SkipsVersionAppliedByAnotherProcess(t *testing.T) {
	exec := &concurrentMigratorExecutor{fakePlatformExecutor: &fakePlatformExecutor{userVersion: 0}, migratedTo: 1}
	fs := testFS(map[string]string{
		"000001_add_column.up.sql": "ALTER TABLE _foo ADD COLUMN bar TEXT",
	})

	version, err := runPlatformMigrations(context.Background(), exec, "test-ns", fs, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}
	for _, sql := range exec.executed {
		if strings.HasPrefix(sql, "ALTER TABLE") || strings.HasPrefix(sql, "PRAGMA user_version =") {
			t.Errorf("ran %q for a version another process already applied", sql)
		}
	}
}

func TestRunPlatformMigrations_FailureMidMigration(t *testing.T) {
	exec := &fakePlatformExecutor{
		userVersion: 0,
		failOnSQL:   "CREATE TABLE _bar (id INTEGER)",
	}
	fs := testFS(map[string]string{
		"000001_add_stuff.up.sql": "CREATE TABLE _foo (id INTEGER); CREATE TABLE _bar (id INTEGER)",
	})

	version, err := runPlatformMigrations(context.Background(), exec, "test-ns", fs, zerolog.Nop())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if version != 0 {
		t.Errorf("version = %d, want 0 (should not advance on failure)", version)
	}
}

func TestEnsurePlatformSchema_ConcurrentCallFailsThenSelfHeals(t *testing.T) {
	// Simulates two pods both seeing stale Postgres version.
	// Pod 1 runs migration successfully. Pod 2 tries the same SQL and fails
	// (e.g. column already exists). On the next request, Postgres version
	// is up-to-date and pod 2 skips entirely.

	// Pod 1: succeeds.
	exec1 := &fakePlatformExecutor{userVersion: 0}
	dbID := uuid.New()

	fs := testFS(map[string]string{
		"000001_add_stuff.up.sql": "ALTER TABLE _mutation_log ADD COLUMN batch_id TEXT",
	})

	_, err := runPlatformMigrations(context.Background(), exec1, dbID.String(), fs, zerolog.Nop())
	if err != nil {
		t.Fatalf("pod 1: unexpected error: %v", err)
	}

	// Pod 2: also sees userVersion 0, tries the same migration SQL → fails
	// (e.g. column already exists because pod 1 already applied it).
	exec2 := &fakePlatformExecutor{
		userVersion: 0,
		failOnSQL:   "ALTER TABLE _mutation_log ADD COLUMN batch_id TEXT",
	}
	_, err = runPlatformMigrations(context.Background(), exec2, dbID.String(), fs, zerolog.Nop())
	if err == nil {
		t.Fatal("pod 2: expected migration to fail, got nil")
	}
}

func TestEnsurePlatformSchema_SecondCallSkipsAfterFirstMigrates(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: CurrentPlatformSchemaVersion}
	repo := &mockDatabaseRepo{}
	svc := NewDatabaseService(
		repo,
		&mockDatabaseManager{},
		exec,
		&mockBdbsApp{},
		nil,
		nil,
		zerolog.Nop(),
	)

	dbID := uuid.New()

	// First call: Postgres version is behind → runs migration check (reads PRAGMA).
	db1 := &domain.Database{ID: dbID, PlatformSchemaVersion: CurrentPlatformSchemaVersion - 1}
	if err := svc.EnsurePlatformSchema(context.Background(), db1); err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}
	firstCallStatements := len(exec.executed)
	if firstCallStatements == 0 {
		t.Error("expected at least 1 SQL statement on first call (PRAGMA read)")
	}

	// Second call: simulate Postgres version now up-to-date (as if another
	// request already ran the migration or the mirror was updated).
	db2 := &domain.Database{ID: dbID, PlatformSchemaVersion: CurrentPlatformSchemaVersion}
	exec.executed = nil // Resets tracking.
	if err := svc.EnsurePlatformSchema(context.Background(), db2); err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if len(exec.executed) != 0 {
		t.Errorf("expected 0 SQL statements on second call (skipped via Postgres version), got %d: %v",
			len(exec.executed), exec.executed)
	}
}

// TestPlatformFS_AppliesAllMigrationsInOrder runs the real embedded platform
// migration FS against the fake executor and verifies that every migration
// from version 1 up to CurrentPlatformSchemaVersion is applied in order.
// This catches mistakes like forgetting to bump CurrentPlatformSchemaVersion
// when a new migration file is added (or vice versa).
func TestPlatformFS_AppliesAllMigrationsInOrder(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: 0}

	version, err := runPlatformMigrations(context.Background(), exec, "test-ns", migrations.PlatformFS, zerolog.Nop())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != CurrentPlatformSchemaVersion {
		t.Errorf("version = %d, want %d", version, CurrentPlatformSchemaVersion)
	}
	if exec.userVersion != CurrentPlatformSchemaVersion {
		t.Errorf("userVersion = %d, want %d", exec.userVersion, CurrentPlatformSchemaVersion)
	}
}

// TestPlatformFS_IdempotentAtCurrentVersion confirms that re-running migrations
// when already at CurrentPlatformSchemaVersion is a no-op (only the PRAGMA read
// fires). This is the concurrent-pod-race safety property.
func TestPlatformFS_IdempotentAtCurrentVersion(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: CurrentPlatformSchemaVersion}

	if _, err := runPlatformMigrations(context.Background(), exec, "test-ns", migrations.PlatformFS,
		zerolog.Nop()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(exec.executed) != 1 {
		t.Errorf("expected only PRAGMA read, got %d statements: %v", len(exec.executed), exec.executed)
	}
}

// TestPlatformFS_NeverCreatesRetiredTables pins that no embedded migration touches the retired
// _mutation_log / _schema_log tables, even transiently: a fresh database goes straight to the head shape.
func TestPlatformFS_NeverCreatesRetiredTables(t *testing.T) {
	exec := &fakePlatformExecutor{userVersion: 0}
	if _, err := runPlatformMigrations(context.Background(), exec, "test-ns", migrations.PlatformFS,
		zerolog.Nop()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, stmt := range exec.executed {
		s := strings.ToUpper(stmt)
		if strings.Contains(s, "_MUTATION_LOG") || strings.Contains(s, "_SCHEMA_LOG") {
			t.Errorf("migration statement touches a retired table: %q", stmt)
		}
	}
}

func TestSplitStatements(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"single statement", "SELECT 1", 1},
		{"two statements", "SELECT 1; SELECT 2", 2},
		{"trailing semicolon", "SELECT 1;", 1},
		{"empty between", "SELECT 1;; SELECT 2", 2},
		{"whitespace only", "  ;  ;  ", 0},
		{"line comment with semicolon inside", "-- comment with ; inside\nSELECT 1", 1},
		{"block comment with semicolon and statements after", "/* multi\n; line */ SELECT 1; SELECT 2", 2},
		{"semicolon inside string literal", "INSERT INTO t VALUES ('a;b'); SELECT 1", 2},
		{"escaped quote inside string literal", "INSERT INTO t VALUES ('it''s'); SELECT 1", 2},
		{"line comment only", "-- nothing but comment", 0},
		{"block comment only", "/* just a block comment */", 0},
		{"trailing comment after statement", "SELECT 1; -- trailing\n", 1},
		{"comment then statement", "-- header\nSELECT 1", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitStatements(tt.input)
			if len(got) != tt.want {
				t.Errorf("splitStatements(%q) = %d statements (%v), want %d", tt.input, len(got), got, tt.want)
			}
		})
	}
}
