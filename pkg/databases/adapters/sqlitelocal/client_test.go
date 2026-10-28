//go:build cgo

package sqlitelocal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

func newTestClient(t *testing.T) (*Client, *fixedDEKResolver) {
	t.Helper()
	resolver := newFixedDEKResolver(t)
	c, err := NewClient(Config{
		DataDir:         t.TempDir(),
		CacheTTL:        time.Hour,
		CacheMaxEntries: 16,
		CacheSweepEvery: time.Hour,
		DEKResolver:     resolver.Resolve,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return c, resolver
}

func TestNewClient_Errors(t *testing.T) {
	resolver := newFixedDEKResolver(t)

	t.Run("empty data dir", func(t *testing.T) {
		_, err := NewClient(Config{DEKResolver: resolver.Resolve})
		if err == nil {
			t.Fatal("expected error for empty DataDir")
		}
	})

	t.Run("missing data dir", func(t *testing.T) {
		_, err := NewClient(Config{
			DataDir:     filepath.Join(t.TempDir(), "does-not-exist"),
			DEKResolver: resolver.Resolve,
		})
		if err == nil {
			t.Fatal("expected error for missing DataDir")
		}
	})

	t.Run("data dir is a file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "afile")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewClient(Config{DataDir: f, DEKResolver: resolver.Resolve})
		if err == nil {
			t.Fatal("expected error when DataDir is a file")
		}
	})
}

func TestClient_CreateDatabase_Success(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	// Main file should exist on disk.
	if _, err := os.Stat(filepath.Join(c.dataDir, "ns1.db")); err != nil {
		t.Fatalf("database file: %v", err)
	}

	// Subsequent Use must skip the resolver because Insert seeded the cache.
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil); err != nil {
		t.Fatalf("Execute after create: %v", err)
	}
	if got := resolver.Calls(); got != 0 {
		t.Errorf("resolver calls = %d, want 0 (cache was seeded)", got)
	}
}

func TestClient_CreateDatabase_BadDEK(t *testing.T) {
	c, _ := newTestClient(t)
	err := c.CreateDatabase(context.Background(), "ns1", []byte("short"))
	if err == nil {
		t.Fatal("expected error for short DEK")
	}
}

func TestClient_CreateDatabase_FileExists(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("first create: %v", err)
	}

	err := c.CreateDatabase(ctx, "ns1", resolver.key)
	if !errors.Is(err, domain.ErrDatabaseCreation) {
		t.Errorf("err = %v, want ErrDatabaseCreation", err)
	}
}

// TestClient_RejectsAttachOnSeededAndLoadedConnections asserts that the
// production cache path locks down ATTACH at the engine level — both on a
// connection seeded by CreateDatabase and on one freshly loaded via the
// cache's DEK-resolver path. This is the engine-level half of the
// cross-database-query prevention chain (see Conn.DisableAttach).
func TestClient_RejectsAttachOnSeededAndLoadedConnections(t *testing.T) {
	dataDir := t.TempDir()
	resolver := newFixedDEKResolver(t)

	mkClient := func() *Client {
		c, err := NewClient(Config{
			DataDir:         dataDir,
			CacheTTL:        time.Hour,
			CacheMaxEntries: 16,
			CacheSweepEvery: time.Hour,
			DEKResolver:     resolver.Resolve,
		})
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		return c
	}

	ctx := context.Background()

	c1 := mkClient()
	if err := c1.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c1.Execute(ctx, "ns1", "ATTACH DATABASE ':memory:' AS aux", nil); err == nil {
		t.Fatal("expected ATTACH on seeded connection to fail, got nil")
	}
	if err := c1.Close(); err != nil {
		t.Fatalf("Close c1: %v", err)
	}

	// Forces a fresh load through the cache's dekResolver path on a brand-new
	// client pointed at the same data dir (no seeded connection in cache).
	c2 := mkClient()
	t.Cleanup(func() {
		if err := c2.Close(); err != nil {
			t.Errorf("Close c2: %v", err)
		}
	})
	if _, err := c2.Execute(ctx, "ns1", "ATTACH DATABASE ':memory:' AS aux", nil); err == nil {
		t.Fatal("expected ATTACH on loaded connection to fail, got nil")
	}
}

func TestClient_SetDatabaseMaxSize_Enforces(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	// SQLite default page size is 4096; cap at ~12KB → 3 pages.
	if err := c.SetDatabaseMaxSize(ctx, "ns1", 12*1024); err != nil {
		t.Fatalf("SetDatabaseMaxSize: %v", err)
	}

	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE big (data BLOB)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	// Insert until we exceed the cap; SQLite returns SQLITE_FULL once max_page_count is hit.
	blob := make([]byte, 4096)
	var lastErr error
	for range 32 {
		_, err := c.Execute(ctx, "ns1", "INSERT INTO big (data) VALUES (?)",
			[]domain.TypedValue{{Type: domain.SQLBlob, Value: blob}})
		if err != nil {
			lastErr = err
			break
		}
	}
	if lastErr == nil {
		t.Fatal("expected an INSERT to fail once max_page_count is exceeded")
	}
	if !errors.Is(lastErr, domain.ErrQueryExecution) {
		t.Errorf("lastErr = %v, want wrapped ErrQueryExecution", lastErr)
	}
}

func TestClient_SetDatabaseMaxSize_NonPositiveIsNoop(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if err := c.SetDatabaseMaxSize(ctx, "ns1", 0); err != nil {
		t.Errorf("SetDatabaseMaxSize(0): %v", err)
	}
	if err := c.SetDatabaseMaxSize(ctx, "ns1", -1); err != nil {
		t.Errorf("SetDatabaseMaxSize(-1): %v", err)
	}
}

func TestClient_GetDatabaseStats(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	// Write something so the file isn't trivially empty.
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	stats, err := c.GetDatabaseStats(ctx, "ns1")
	if err != nil {
		t.Fatalf("GetDatabaseStats: %v", err)
	}
	if stats.StorageBytesUsed == 0 {
		t.Error("StorageBytesUsed = 0, want > 0")
	}
}

func TestClient_GetDatabaseStats_MissingFileIsZero(t *testing.T) {
	c, _ := newTestClient(t)
	stats, err := c.GetDatabaseStats(context.Background(), "missing")
	if err != nil {
		t.Fatalf("GetDatabaseStats: %v", err)
	}
	if stats.StorageBytesUsed != 0 {
		t.Errorf("StorageBytesUsed = %d, want 0", stats.StorageBytesUsed)
	}
}

func TestClient_DeleteDatabase(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	if err := c.DeleteDatabase(ctx, "ns1"); err != nil {
		t.Fatalf("DeleteDatabase: %v", err)
	}

	if _, err := os.Stat(filepath.Join(c.dataDir, "ns1.db")); !os.IsNotExist(err) {
		t.Errorf("main file still present: err = %v", err)
	}

	// Idempotent on repeat.
	if err := c.DeleteDatabase(ctx, "ns1"); err != nil {
		t.Errorf("repeat DeleteDatabase: %v", err)
	}

	// Idempotent on never-created database.
	if err := c.DeleteDatabase(ctx, "never"); err != nil {
		t.Errorf("DeleteDatabase(never): %v", err)
	}
}

func TestClient_Execute_DDL(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	res, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil)
	if err != nil {
		t.Fatalf("Execute DDL: %v", err)
	}
	if len(res.Columns) != 0 || len(res.Rows) != 0 {
		t.Errorf("DDL should produce empty result, got %+v", res)
	}
}

func TestClient_Execute_DML_AffectedAndLastID(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	res, err := c.Execute(ctx, "ns1", "INSERT INTO t (x) VALUES (?), (?), (?)", []domain.TypedValue{
		domain.IntegerValue(1), domain.IntegerValue(2), domain.IntegerValue(3),
	})
	if err != nil {
		t.Fatalf("Execute INSERT: %v", err)
	}
	if res.AffectedRowCount != 3 {
		t.Errorf("AffectedRowCount = %d, want 3", res.AffectedRowCount)
	}
	if res.LastInsertRowID == nil || *res.LastInsertRowID != 3 {
		t.Errorf("LastInsertRowID = %v, want 3", res.LastInsertRowID)
	}
}

func TestClient_Execute_Select(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER, y TEXT)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "INSERT INTO t VALUES (1, 'a'), (2, 'b')", nil); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	res, err := c.Execute(ctx, "ns1", "SELECT x, y FROM t ORDER BY x", nil)
	if err != nil {
		t.Fatalf("Execute SELECT: %v", err)
	}
	if len(res.Columns) != 2 || res.Columns[0].Name != "x" || res.Columns[1].Name != "y" {
		t.Errorf("columns = %+v, want [x y]", res.Columns)
	}
	if res.Columns[0].DeclaredType != "INTEGER" || res.Columns[1].DeclaredType != "TEXT" {
		t.Errorf("declared types = [%q %q], want [INTEGER TEXT]",
			res.Columns[0].DeclaredType, res.Columns[1].DeclaredType)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Rows))
	}
	if res.Rows[0][0].Value.(int64) != 1 || res.Rows[0][1].Value.(string) != "a" {
		t.Errorf("row 0 = %+v", res.Rows[0])
	}
	if res.Rows[1][0].Value.(int64) != 2 || res.Rows[1][1].Value.(string) != "b" {
		t.Errorf("row 1 = %+v", res.Rows[1])
	}
}

func TestClient_Execute_QueryError(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	_, err := c.Execute(ctx, "ns1", "SELECT * FROM does_not_exist", nil)
	if err == nil {
		t.Fatal("expected error for missing table")
	}
	if !errors.Is(err, domain.ErrQueryExecution) {
		t.Errorf("err = %v, want wrapped ErrQueryExecution", err)
	}
	var qe *domain.QueryExecutionError
	if !errors.As(err, &qe) {
		t.Errorf("err = %v, want *domain.QueryExecutionError", err)
	}
}

// TestClient_Execute_UniqueViolation_SurfacesExtendedCode confirms that the
// binding's sqlite3_extended_result_codes call is wired up — a UNIQUE
// violation must reach the service layer as code 2067, not the primary 19.
// This is the real-path equivalent of TestWrapExecError's unit assertion.
func TestClient_Execute_UniqueViolation_SurfacesExtendedCode(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER UNIQUE)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "INSERT INTO t (x) VALUES (1)", nil); err != nil {
		t.Fatalf("INSERT 1: %v", err)
	}

	_, err := c.Execute(ctx, "ns1", "INSERT INTO t (x) VALUES (1)", nil)
	if err == nil {
		t.Fatal("expected UNIQUE violation, got nil")
	}
	if !errors.Is(err, domain.ErrUniqueViolation) {
		t.Errorf("errors.Is(err, ErrUniqueViolation) = false, want true (err=%v)", err)
	}
	var qe *domain.QueryExecutionError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v, want *domain.QueryExecutionError", err)
	}
	if !qe.IsUniqueConstraint() {
		t.Errorf("IsUniqueConstraint() = false, want true (Code=%d)", qe.Code)
	}
	if qe.Code != 2067 {
		t.Errorf("Code = %d, want 2067 (extended SQLITE_CONSTRAINT_UNIQUE)", qe.Code)
	}
}

// TestClient_Execute_CheckConstraint_NotUnique exercises the negative case:
// a generic constraint (CHECK) must satisfy ErrQueryExecution but NOT
// ErrUniqueViolation, so callers can distinguish constraint kinds.
func TestClient_Execute_CheckConstraint_NotUnique(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER CHECK (x > 0))", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	_, err := c.Execute(ctx, "ns1", "INSERT INTO t (x) VALUES (-1)", nil)
	if err == nil {
		t.Fatal("expected CHECK violation, got nil")
	}
	if !errors.Is(err, domain.ErrQueryExecution) {
		t.Errorf("errors.Is(err, ErrQueryExecution) = false, want true")
	}
	if errors.Is(err, domain.ErrUniqueViolation) {
		t.Errorf("errors.Is(err, ErrUniqueViolation) = true, want false (CHECK is not UNIQUE)")
	}
}

func TestClient_ExecuteTransaction_CommitsAll(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	results, err := c.ExecuteTransaction(ctx, "ns1", []domain.SQLStatement{
		{SQL: "INSERT INTO t (x) VALUES (?)", Args: []domain.TypedValue{domain.IntegerValue(10)}},
		{SQL: "INSERT INTO t (x) VALUES (?)", Args: []domain.TypedValue{domain.IntegerValue(20)}},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for i, r := range results {
		if r.AffectedRowCount != 1 {
			t.Errorf("results[%d].AffectedRowCount = %d, want 1", i, r.AffectedRowCount)
		}
	}

	// Verify both rows are visible.
	res, err := c.Execute(ctx, "ns1", "SELECT COUNT(*) FROM t", nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got := res.Rows[0][0].Value.(int64); got != 2 {
		t.Errorf("row count = %d, want 2", got)
	}
}

func TestClient_ExecuteTransaction_RollsBackOnError(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER NOT NULL)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	_, err := c.ExecuteTransaction(ctx, "ns1", []domain.SQLStatement{
		{SQL: "INSERT INTO t (x) VALUES (?)", Args: []domain.TypedValue{domain.IntegerValue(1)}},
		{SQL: "INSERT INTO t (x) VALUES (?)", Args: []domain.TypedValue{domain.NullValue()}}, // violates NOT NULL
	})
	if err == nil {
		t.Fatal("expected error from NOT NULL violation")
	}

	// Both inserts should be rolled back.
	res, qerr := c.Execute(ctx, "ns1", "SELECT COUNT(*) FROM t", nil)
	if qerr != nil {
		t.Fatalf("count: %v", qerr)
	}
	if got := res.Rows[0][0].Value.(int64); got != 0 {
		t.Errorf("rows after rollback = %d, want 0", got)
	}
}

func TestClient_WithTx_CommitsOnNilReturn(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	err := c.WithTx(ctx, "ns1", false, func(ctx context.Context, tx service.Tx) error {
		if _, e := tx.Exec(ctx, "INSERT INTO t (x) VALUES (?)", []domain.TypedValue{domain.IntegerValue(7)}); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, "INSERT INTO t (x) VALUES (?)",
			[]domain.TypedValue{domain.IntegerValue(11)}); e != nil {
			return e
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	res, err := c.Execute(ctx, "ns1", "SELECT COUNT(*) FROM t", nil)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got := res.Rows[0][0].Value.(int64); got != 2 {
		t.Errorf("row count = %d, want 2 (commit should have applied both inserts)", got)
	}
}

func TestClient_WithTx_RollsBackOnError(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	sentinel := errors.New("caller-decided abort")
	err := c.WithTx(ctx, "ns1", false, func(ctx context.Context, tx service.Tx) error {
		if _, e := tx.Exec(ctx, "INSERT INTO t (x) VALUES (?)",
			[]domain.TypedValue{domain.IntegerValue(99)}); e != nil {
			return e
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}

	res, qerr := c.Execute(ctx, "ns1", "SELECT COUNT(*) FROM t", nil)
	if qerr != nil {
		t.Fatalf("count: %v", qerr)
	}
	if got := res.Rows[0][0].Value.(int64); got != 0 {
		t.Errorf("rows after rollback = %d, want 0", got)
	}
}

func TestClient_WithTx_StatementErrorRollsBack(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER NOT NULL)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	err := c.WithTx(ctx, "ns1", false, func(ctx context.Context, tx service.Tx) error {
		if _, e := tx.Exec(ctx, "INSERT INTO t (x) VALUES (?)", []domain.TypedValue{domain.IntegerValue(1)}); e != nil {
			return e
		}
		// NOT NULL violation — error propagates out and triggers rollback.
		_, e := tx.Exec(ctx, "INSERT INTO t (x) VALUES (?)", []domain.TypedValue{domain.NullValue()})
		return e
	})
	if err == nil {
		t.Fatal("expected error from NOT NULL violation")
	}

	res, qerr := c.Execute(ctx, "ns1", "SELECT COUNT(*) FROM t", nil)
	if qerr != nil {
		t.Fatalf("count: %v", qerr)
	}
	if got := res.Rows[0][0].Value.(int64); got != 0 {
		t.Errorf("rows after rollback = %d, want 0", got)
	}
}

func TestClient_WithTx_InternalAllowsReservedWrites(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	// Seeds a reserved-prefix table via internal so the test can write to it.
	if _, err := c.ExecuteInternal(ctx, "ns1", "CREATE TABLE _platform (id INTEGER, val TEXT)", nil); err != nil {
		t.Fatalf("CREATE _platform: %v", err)
	}

	// internal=true: tx.Exec calls bypass the reserved-prefix authorizer.
	err := c.WithTx(ctx, "ns1", true, func(ctx context.Context, tx service.Tx) error {
		_, e := tx.Exec(ctx, "INSERT INTO _platform (id, val) VALUES (?, ?)",
			[]domain.TypedValue{domain.IntegerValue(1), domain.TextValue("ok")})
		return e
	})
	if err != nil {
		t.Fatalf("WithTx(internal=true) on _platform: %v", err)
	}

	// internal=false: same write fires the authorizer and is denied.
	err = c.WithTx(ctx, "ns1", false, func(ctx context.Context, tx service.Tx) error {
		_, e := tx.Exec(ctx, "INSERT INTO _platform (id, val) VALUES (?, ?)",
			[]domain.TypedValue{domain.IntegerValue(2), domain.TextValue("bad")})
		return e
	})
	if err == nil {
		t.Fatal("expected denial for tenant-mode write to _platform, got nil")
	}
}

// TestClient_WithTx_ExecInternalBypassesAuthorizerInTenantTx pins the two execution modes a tenant-mode transaction
// offers: Exec keeps the reserved-object rules and surfaces a denial as the same domain error classification yields,
// ExecInternal bypasses them for the one statement without leaking the bypass into the next Exec.
func TestClient_WithTx_ExecInternalBypassesAuthorizerInTenantTx(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.ExecuteInternal(ctx, "ns1", "CREATE TABLE _platform (id INTEGER, val TEXT)", nil); err != nil {
		t.Fatalf("CREATE _platform: %v", err)
	}

	insert := "INSERT INTO _platform (id, val) VALUES (?, ?)"
	err := c.WithTx(ctx, "ns1", false, func(ctx context.Context, tx service.Tx) error {
		_, e := tx.Exec(ctx, insert, []domain.TypedValue{domain.IntegerValue(1), domain.TextValue("denied")})
		if !errors.Is(e, domain.ErrReservedTable) {
			t.Errorf("Exec on _platform: err = %v, want ErrReservedTable", e)
		}
		if _, e := tx.ExecInternal(ctx, insert,
			[]domain.TypedValue{domain.IntegerValue(2), domain.TextValue("bookkeeping")}); e != nil {
			t.Errorf("ExecInternal on _platform: %v", e)
		}
		_, e = tx.Exec(ctx, insert, []domain.TypedValue{domain.IntegerValue(3), domain.TextValue("denied")})
		if !errors.Is(e, domain.ErrReservedTable) {
			t.Errorf("Exec after ExecInternal: err = %v, want ErrReservedTable (the bypass must not leak)", e)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTx(internal=false): %v", err)
	}

	r, err := c.ExecuteInternal(ctx, "ns1", "SELECT id FROM _platform ORDER BY id", nil)
	if err != nil {
		t.Fatalf("SELECT _platform: %v", err)
	}
	if len(r.Rows) != 1 || r.Rows[0][0].Value != int64(2) {
		t.Errorf("rows in _platform = %v, want only the ExecInternal row (id 2)", r.Rows)
	}
}

func TestClient_ExecuteTransaction_EmptyStmts(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	results, err := c.ExecuteTransaction(ctx, "ns1", nil)
	if err != nil {
		t.Errorf("ExecuteTransaction(nil): %v", err)
	}
	if results != nil {
		t.Errorf("results = %v, want nil", results)
	}
}

func TestClient_Execute_RoundtripAllTypes(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1",
		"CREATE TABLE t (i INTEGER, r REAL, s TEXT, b BLOB, n INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	blob := []byte{0x00, 0x01, 0x02}
	if _, err := c.Execute(ctx, "ns1", "INSERT INTO t VALUES (?, ?, ?, ?, ?)",
		[]domain.TypedValue{
			domain.IntegerValue(42),
			domain.RealValue(3.14),
			domain.TextValue("hello"),
			{Type: domain.SQLBlob, Value: blob},
			domain.NullValue(),
		}); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	res, err := c.Execute(ctx, "ns1", "SELECT i, r, s, b, n FROM t", nil)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(res.Rows))
	}
	row := res.Rows[0]
	if row[0].Type != domain.SQLInteger || row[0].Value.(int64) != 42 {
		t.Errorf("row[0] = %+v", row[0])
	}
	if row[1].Type != domain.SQLReal || row[1].Value.(float64) != 3.14 {
		t.Errorf("row[1] = %+v", row[1])
	}
	if row[2].Type != domain.SQLText || row[2].Value.(string) != "hello" {
		t.Errorf("row[2] = %+v", row[2])
	}
	if row[3].Type != domain.SQLBlob || string(row[3].Value.([]byte)) != string(blob) {
		t.Errorf("row[3] = %+v", row[3])
	}
	if row[4].Type != domain.SQLNull || row[4].Value != nil {
		t.Errorf("row[4] = %+v", row[4])
	}
}

// TestClient_ExecuteStream_MatchesExecute verifies the streaming path yields exactly what the
// materializing path returns for the same query: identical column metadata on every callback
// invocation and identical rows (all value types, including NULL and BLOB), in the same order.
func TestClient_ExecuteStream_MatchesExecute(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1",
		"CREATE TABLE t (i INTEGER, r REAL, s TEXT, b BLOB, n INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}

	blob := []byte{0x00, 0x01, 0x02}
	if _, err := c.Execute(ctx, "ns1", "INSERT INTO t VALUES (?, ?, ?, ?, ?)",
		[]domain.TypedValue{
			domain.IntegerValue(1),
			domain.RealValue(3.14),
			domain.TextValue("hello"),
			{Type: domain.SQLBlob, Value: blob},
			domain.NullValue(),
		}); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "INSERT INTO t VALUES (?, ?, ?, ?, ?)",
		[]domain.TypedValue{
			domain.IntegerValue(2),
			domain.NullValue(),
			domain.NullValue(),
			domain.NullValue(),
			domain.IntegerValue(7),
		}); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	const query = "SELECT i, r, s, b, n FROM t ORDER BY i"
	want, err := c.Execute(ctx, "ns1", query, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(want.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(want.Rows))
	}

	var gotRows [][]domain.TypedValue
	err = c.ExecuteStream(ctx, "ns1", query, nil, func(cols []domain.Column, row []domain.TypedValue) error {
		if !reflect.DeepEqual(cols, want.Columns) {
			t.Errorf("streamed cols = %+v, want %+v", cols, want.Columns)
		}
		gotRows = append(gotRows, row)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if !reflect.DeepEqual(gotRows, want.Rows) {
		t.Errorf("streamed rows = %+v, want %+v", gotRows, want.Rows)
	}
}

// TestClient_ExecuteStream_CallbackErrorAborts verifies a callback error stops the scan immediately and
// reaches the caller unwrapped, so sentinel errors survive an errors.Is match.
func TestClient_ExecuteStream_CallbackErrorAborts(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "ns1", "CREATE TABLE t (x INTEGER)", nil); err != nil {
		t.Fatalf("CREATE: %v", err)
	}
	for i := range 3 {
		if _, err := c.Execute(ctx, "ns1", "INSERT INTO t VALUES (?)",
			[]domain.TypedValue{domain.IntegerValue(int64(i))}); err != nil {
			t.Fatalf("INSERT: %v", err)
		}
	}

	errStop := errors.New("stop")
	calls := 0
	err := c.ExecuteStream(ctx, "ns1", "SELECT x FROM t", nil,
		func(_ []domain.Column, _ []domain.TypedValue) error {
			calls++
			return errStop
		})
	if !errors.Is(err, errStop) {
		t.Fatalf("err = %v, want the callback's error via errors.Is", err)
	}
	if calls != 1 {
		t.Errorf("callback invocations = %d, want 1", calls)
	}
}

// TestClient_ExecuteStream_TenantMode verifies the streaming path keeps the reserved-prefix authorizer
// active: reads on a reserved table stay allowed (audit visibility), while writes are denied before the
// callback ever runs.
func TestClient_ExecuteStream_TenantMode(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.ExecuteInternal(ctx, "ns1", "CREATE TABLE _priv (id INTEGER, secret TEXT)", nil); err != nil {
		t.Fatalf("CREATE _priv: %v", err)
	}
	if _, err := c.ExecuteInternal(ctx, "ns1", "INSERT INTO _priv VALUES (1, 'k')", nil); err != nil {
		t.Fatalf("INSERT _priv: %v", err)
	}

	t.Run("write denied", func(t *testing.T) {
		calls := 0
		err := c.ExecuteStream(ctx, "ns1", "INSERT INTO _priv VALUES (2, 'evil')", nil,
			func(_ []domain.Column, _ []domain.TypedValue) error {
				calls++
				return nil
			})
		if err == nil {
			t.Fatal("expected denial for write to _priv via ExecuteStream, got nil")
		}
		if !errors.Is(err, domain.ErrReservedTable) {
			t.Errorf("err = %v, want ErrReservedTable (an execute-time denial maps like one at classification)", err)
		}
		if calls != 0 {
			t.Errorf("callback invocations = %d, want 0", calls)
		}
	})

	t.Run("read allowed", func(t *testing.T) {
		rows := 0
		err := c.ExecuteStream(ctx, "ns1", "SELECT id, secret FROM _priv", nil,
			func(_ []domain.Column, _ []domain.TypedValue) error {
				rows++
				return nil
			})
		if err != nil {
			t.Fatalf("SELECT on _priv should be allowed, got: %v", err)
		}
		if rows != 1 {
			t.Errorf("rows = %d, want 1", rows)
		}
	})
}

func TestWrapExecError(t *testing.T) {
	t.Run("nil passes through", func(t *testing.T) {
		if got := wrapExecError(nil); got != nil {
			t.Errorf("wrapExecError(nil) = %v, want nil", got)
		}
	})

	t.Run("non-sqlite error passes through", func(t *testing.T) {
		orig := errors.New("ctx done")
		got := wrapExecError(orig)
		if !errors.Is(got, orig) {
			t.Errorf("wrapExecError(%v) = %v, want passthrough", orig, got)
		}
	})

	t.Run("sqlite error wrapped", func(t *testing.T) {
		orig := &Error{Code: 2067, Msg: "UNIQUE constraint failed: t.x"}
		got := wrapExecError(orig)
		var qe *domain.QueryExecutionError
		if !errors.As(got, &qe) {
			t.Fatalf("wrapExecError(%v) = %v, want *QueryExecutionError", orig, got)
		}
		if qe.Code != 2067 {
			t.Errorf("Code = %d, want 2067", qe.Code)
		}
		if qe.Message != "UNIQUE constraint failed: t.x" {
			t.Errorf("Message = %q, want UNIQUE constraint failed: t.x", qe.Message)
		}
		if !errors.Is(got, domain.ErrQueryExecution) {
			t.Errorf("errors.Is(%v, ErrQueryExecution) = false", got)
		}
		if !errors.Is(got, domain.ErrUniqueViolation) {
			t.Errorf("errors.Is(%v, ErrUniqueViolation) = false", got)
		}
	})
}

func TestAnyToTypedValue(t *testing.T) {
	cases := []struct {
		in       any
		wantType domain.SQLType
	}{
		{nil, domain.SQLNull},
		{int64(7), domain.SQLInteger},
		{3.5, domain.SQLReal},
		{"hi", domain.SQLText},
		{[]byte{1, 2}, domain.SQLBlob},
		{struct{}{}, domain.SQLNull}, // unsupported types fall through to NULL
	}
	for _, tc := range cases {
		got := anyToTypedValue(tc.in)
		if got.Type != tc.wantType {
			t.Errorf("anyToTypedValue(%T) type = %v, want %v", tc.in, got.Type, tc.wantType)
		}
	}
}

// TestClient_ExecuteWithRowCap covers the result-row cap at the adapter: a result exactly at the cap passes, one row
// past it fails with the sentinel (matchable with errors.Is), an empty result keeps its columns, and 0 means no cap.
func TestClient_ExecuteWithRowCap(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, "ns1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	for _, sql := range []string{
		"CREATE TABLE t (x INTEGER, y TEXT)",
		"INSERT INTO t VALUES (1, 'a'), (2, 'b'), (3, 'c')",
	} {
		if _, err := c.Execute(ctx, "ns1", sql, nil); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	res, err := c.ExecuteWithRowCap(ctx, "ns1", "SELECT x, y FROM t ORDER BY x", nil, 3)
	if err != nil || len(res.Rows) != 3 {
		t.Fatalf("exactly at the cap: rows = %d, err = %v, want 3 rows and no error", len(res.Rows), err)
	}

	_, err = c.ExecuteWithRowCap(ctx, "ns1", "SELECT x, y FROM t ORDER BY x", nil, 2)
	if !errors.Is(err, domain.ErrQueryResultTooLarge) {
		t.Fatalf("one past the cap: err = %v, want ErrQueryResultTooLarge", err)
	}

	res, err = c.ExecuteWithRowCap(ctx, "ns1", "SELECT x, y FROM t WHERE 0", nil, 1)
	if err != nil || len(res.Columns) != 2 || len(res.Rows) != 0 {
		t.Fatalf("empty result: columns = %d, rows = %d, err = %v, want 2 columns, 0 rows", len(res.Columns),
			len(res.Rows), err)
	}

	res, err = c.ExecuteWithRowCap(ctx, "ns1", "SELECT x FROM t", nil, 0)
	if err != nil || len(res.Rows) != 3 {
		t.Fatalf("no cap: rows = %d, err = %v, want 3 rows", len(res.Rows), err)
	}
}
