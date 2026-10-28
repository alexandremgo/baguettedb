package database

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// newCountedTable opens a fresh SQLite file with an empty table t (n INTEGER) and returns the handle.
func newCountedTable(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "instance.sqlite"))
	if _, err := db.Exec("CREATE TABLE t (n INTEGER NOT NULL)"); err != nil {
		t.Fatalf("creating table: %v", err)
	}
	return db
}

// countRows returns how many rows table t holds.
func countRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	return n
}

// TestInTx checks that a transaction whose function succeeds is committed, and that one whose function fails is
// rolled back and returns that function's error. A panicking function still ends the transaction, so its connection
// goes back to the handle, and the panic reaches the caller.
func TestInTx(t *testing.T) {
	ctx := context.Background()
	db := newCountedTable(t)

	if err := InTx(ctx, db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO t (n) VALUES (1)")
		return err
	}); err != nil {
		t.Fatalf("committing InTx: %v", err)
	}

	errBoom := errors.New("boom")
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO t (n) VALUES (2)"); err != nil {
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("failing InTx returned %v, want %v", err, errBoom)
	}

	if got := countRows(t, db); got != 1 {
		t.Errorf("rows = %d, want 1 (the committed insert only)", got)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("InTx with a panicking function returned normally, want the panic to reach the caller")
			}
		}()
		_ = InTx(ctx, db, func(*sql.Tx) error { panic("fn panicked") })
	}()
	if inUse := db.Stats().InUse; inUse != 0 {
		t.Errorf("connections in use after a panicking function = %d, want 0 (the transaction ended)", inUse)
	}
}

// TestRequireRow checks the three outcomes of a write: an execution error is wrapped with the operation, a write
// matching no row yields the caller's not-found error, and a write matching a row yields nil.
func TestRequireRow(t *testing.T) {
	db := newCountedTable(t)
	errNotFound := errors.New("not found")
	if _, err := db.Exec("INSERT INTO t (n) VALUES (1)"); err != nil {
		t.Fatalf("inserting: %v", err)
	}

	res, err := db.Exec("UPDATE t SET n = 2 WHERE n = 1")
	if err := RequireRow(res, err, "updating", errNotFound); err != nil {
		t.Errorf("matching write: %v, want nil", err)
	}

	res, err = db.Exec("UPDATE t SET n = 3 WHERE n = 99")
	if err := RequireRow(res, err, "updating", errNotFound); !errors.Is(err, errNotFound) {
		t.Errorf("unmatched write: %v, want %v", err, errNotFound)
	}

	res, err = db.Exec("UPDATE missing SET n = 1")
	err = RequireRow(res, err, "updating", errNotFound)
	if err == nil || errors.Is(err, errNotFound) || !strings.HasPrefix(err.Error(), "updating: ") {
		t.Errorf("failed write: %v, want the execution error prefixed with the operation", err)
	}
}

// TestCloseRows checks that closing rows keeps an earlier error and leaves a nil one nil when the close succeeds.
func TestCloseRows(t *testing.T) {
	db := newCountedTable(t)
	earlier := errors.New("earlier")

	for _, start := range []error{nil, earlier} {
		rows, err := db.Query("SELECT n FROM t")
		if err != nil {
			t.Fatalf("querying: %v", err)
		}
		got := start
		CloseRows(rows, &got)
		//nolint:errorlint // identity on purpose: *err must be left as it was, not replaced by a wrap of it
		if got != start {
			t.Errorf("CloseRows with *err = %v left %v, want it unchanged", start, got)
		}
	}
}
