//go:build cgo

package sqlitelocal

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, KeySize)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

func tmpDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.db")
}

func mustExec(t *testing.T, c *Conn, sql string, args ...any) Result {
	t.Helper()
	r, err := c.Exec(context.Background(), sql, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
	return r
}

// tClose closes c and reports any error to t. Use for both deferred closes
// and inline closes — keeps the lint-clean error-handling rule satisfied
// without burying signal in test bodies.
func tClose(t *testing.T, name string, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close %s: %v", name, err)
	}
}

// TestOpenAndCloseEncryptsFile verifies that opening a fresh database with a
// key produces a file that does not start with the plaintext SQLite magic.
func TestOpenAndCloseEncryptsFile(t *testing.T) {
	path := tmpDB(t)
	key := newKey(t)

	c, err := Open(path, key, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExec(t, c, "CREATE TABLE t (x TEXT)")
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	hdr, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(hdr) < 16 {
		t.Fatalf("file too short: %d bytes", len(hdr))
	}
	const sqliteMagic = "SQLite format 3"
	if string(hdr[:len(sqliteMagic)]) == sqliteMagic {
		t.Fatalf("file starts with plaintext SQLite header — encryption did not apply")
	}
}

// TestOpenWithWrongKeyReturnsNotADB verifies that reopening with a wrong key
// surfaces SQLITE_NOTADB.
func TestOpenWithWrongKeyReturnsNotADB(t *testing.T) {
	path := tmpDB(t)
	keyA := newKey(t)
	keyB := newKey(t)

	c, err := Open(path, keyA, 0)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	mustExec(t, c, "CREATE TABLE t (x TEXT)")
	if err := c.Close(); err != nil {
		t.Fatalf("close A: %v", err)
	}

	_, err = Open(path, keyB, 0)
	if err == nil {
		t.Fatal("expected error opening with wrong key, got nil")
	}
	if !IsCode(err, CodeNotADB) {
		t.Fatalf("expected SQLITE_NOTADB (%d), got %v", CodeNotADB, err)
	}
}

// TestRoundTrip verifies basic insert/select with primitive types.
func TestRoundTrip(t *testing.T) {
	path := tmpDB(t)
	key := newKey(t)

	c, err := Open(path, key, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExec(t, c, "CREATE TABLE t (i INTEGER, f REAL, s TEXT, b BLOB, n INTEGER)")
	mustExec(t, c, "INSERT INTO t VALUES (?, ?, ?, ?, ?)", int64(42), 3.14, "hello", []byte{1, 2, 3}, nil)

	rows, err := c.Query(context.Background(), "SELECT i, f, s, b, n FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !rows.Next() {
		tClose(t, "rows", rows)
		t.Fatalf("expected one row, got none (err=%v)", rows.Err())
	}
	var (
		gotI int64
		gotF float64
		gotS string
		gotB []byte
		gotN any
	)
	if err := rows.Scan(&gotI, &gotF, &gotS, &gotB, &gotN); err != nil {
		tClose(t, "rows", rows)
		t.Fatalf("scan: %v", err)
	}
	tClose(t, "rows", rows)

	if gotI != 42 {
		t.Errorf("i: got %d, want 42", gotI)
	}
	if gotF != 3.14 {
		t.Errorf("f: got %v, want 3.14", gotF)
	}
	if gotS != "hello" {
		t.Errorf("s: got %q, want %q", gotS, "hello")
	}
	if string(gotB) != string([]byte{1, 2, 3}) {
		t.Errorf("b: got %v, want [1 2 3]", gotB)
	}
	if gotN != nil {
		t.Errorf("n: got %v, want nil", gotN)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopens and re-reads to confirm persistence + decryption.
	c2, err := Open(path, key, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { tClose(t, "c2", c2) })

	rows2, err := c2.Query(context.Background(), "SELECT s FROM t")
	if err != nil {
		t.Fatalf("query2: %v", err)
	}

	if !rows2.Next() {
		tClose(t, "rows2", rows2)
		t.Fatalf("reopen: no rows (err=%v)", rows2.Err())
	}
	var s2 string
	if err := rows2.Scan(&s2); err != nil {
		tClose(t, "rows2", rows2)
		t.Fatalf("scan2: %v", err)
	}
	tClose(t, "rows2", rows2)

	if s2 != "hello" {
		t.Errorf("after reopen: got %q, want %q", s2, "hello")
	}
}

// TestRekey verifies the rekey dance: write under key A, rekey to B, reopen
// with B, read data back, and confirm A no longer opens it.
func TestRekey(t *testing.T) {
	path := tmpDB(t)
	keyA := newKey(t)
	keyB := newKey(t)

	c, err := Open(path, keyA, 0)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	mustExec(t, c, "CREATE TABLE t (x TEXT)")
	mustExec(t, c, "INSERT INTO t VALUES (?)", "rekey-payload")

	if err := c.Rekey(keyB); err != nil {
		t.Fatalf("rekey: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	c2, err := Open(path, keyB, 0)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}

	rows, err := c2.Query(context.Background(), "SELECT x FROM t")
	if err != nil {
		tClose(t, "c2", c2)
		t.Fatalf("query: %v", err)
	}
	if !rows.Next() {
		tClose(t, "rows", rows)
		tClose(t, "c2", c2)
		t.Fatalf("no row after rekey (err=%v)", rows.Err())
	}
	var got string
	if err := rows.Scan(&got); err != nil {
		tClose(t, "rows", rows)
		tClose(t, "c2", c2)
		t.Fatalf("scan: %v", err)
	}
	tClose(t, "rows", rows)
	tClose(t, "c2", c2)

	if got != "rekey-payload" {
		t.Errorf("after rekey: got %q, want rekey-payload", got)
	}

	if _, err := Open(path, keyA, 0); err == nil || !IsCode(err, CodeNotADB) {
		t.Errorf("expected old key A to fail with SQLITE_NOTADB after rekey, got %v", err)
	}
}

// TestConcurrentReads verifies that multiple goroutines can hit the same
// *Conn in parallel without deadlock or corruption (SQLITE_THREADSAFE=1
// serialises internally, but the binding must not introduce its own
// serialisation that deadlocks).
func TestConcurrentReads(t *testing.T) {
	path := tmpDB(t)
	key := newKey(t)

	c, err := Open(path, key, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { tClose(t, "c", c) })

	mustExec(t, c, "CREATE TABLE t (x INTEGER)")
	for i := range 100 {
		mustExec(t, c, "INSERT INTO t VALUES (?)", int64(i))
	}

	const goroutines = 16
	const iterations = 25

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*iterations)

	for range goroutines {
		wg.Go(func() {
			for range iterations {
				rows, err := c.Query(context.Background(), "SELECT count(*) FROM t")
				if err != nil {
					errCh <- err
					return
				}
				if !rows.Next() {
					errCh <- rows.Err()
					if cerr := rows.Close(); cerr != nil {
						errCh <- cerr
					}
					return
				}
				var n int64
				if err := rows.Scan(&n); err != nil {
					errCh <- err
					if cerr := rows.Close(); cerr != nil {
						errCh <- cerr
					}
					return
				}
				if cerr := rows.Close(); cerr != nil {
					errCh <- cerr
					return
				}
				if n != 100 {
					errCh <- &Error{Op: "concurrent-count", Msg: "wrong count", Code: int(n)}
					return
				}
			}
		})
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent: %v", err)
	}
}

// TestExecResult verifies LastInsertID and RowsAffected.
func TestExecResult(t *testing.T) {
	path := tmpDB(t)
	c, err := Open(path, newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { tClose(t, "c", c) })

	mustExec(t, c, "CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, x TEXT)")

	r := mustExec(t, c, "INSERT INTO t (x) VALUES (?)", "first")
	if r.LastInsertID != 1 {
		t.Errorf("LastInsertID: got %d, want 1", r.LastInsertID)
	}
	if r.RowsAffected != 1 {
		t.Errorf("RowsAffected: got %d, want 1", r.RowsAffected)
	}

	r = mustExec(t, c, "INSERT INTO t (x) VALUES (?)", "second")
	if r.LastInsertID != 2 {
		t.Errorf("LastInsertID after second insert: got %d, want 2", r.LastInsertID)
	}

	r = mustExec(t, c, "UPDATE t SET x = ? WHERE x = ?", "modified", "first")
	if r.RowsAffected != 1 {
		t.Errorf("UPDATE RowsAffected: got %d, want 1", r.RowsAffected)
	}
}

// TestCtxCancellation verifies that cancelling the context interrupts a
// long-running operation. The test starts a recursive CTE generating enough
// work that step blocks for well over the cancellation delay, then cancels
// from a goroutine while step is mid-flight. The watcher goroutine inside
// the binding must call sqlite3_interrupt, causing step to surface
// SQLITE_INTERRUPT through Rows.Err.
func TestCtxCancellation(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { tClose(t, "c", c) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rows, err := c.Query(ctx, `
		WITH RECURSIVE seq(n) AS (
			SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n < 100000000
		)
		SELECT count(*) FROM seq
	`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	t.Cleanup(func() { tClose(t, "rows", rows) })

	// Cancel after a short delay so step is already running. Pre-cancelling
	// would race with the watcher goroutine getting CPU cycles.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if rows.Next() {
		t.Fatal("expected cancellation to interrupt step, but step ran to completion — interrupt did not land in time")
	}
	err = rows.Err()
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !IsCode(err, CodeInterrupt) {
		t.Errorf("expected SQLITE_INTERRUPT (%d), got %v", CodeInterrupt, err)
	}
}

// TestCtxCancellation_DoesNotLeakInterruptToNextExec is a regression test
// for a cross-Exec SQLITE_INTERRUPT leak in watchCtx.
//
// Reproduces the shape: an Exec finishes with an already-cancelled ctx,
// and a later Exec on the same Conn (with a fresh ctx) must not be
// interrupted by a stale watchCtx goroutine. If watchCtx returned
// without waiting for its goroutine to exit, the goroutine could observe
// the (now-closed) ctx.Done() and call sqlite3_interrupt(db) after the
// next Exec had already incremented nVdbeActive past sqlite3_step's
// auto-clear point, aborting the next statement with SQLITE_INTERRUPT.
//
// Loops to amplify the scheduling race so a regression has more chances
// to surface on a lucky run.
func TestCtxCancellation_DoesNotLeakInterruptToNextExec(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { tClose(t, "c", c) })

	mustExec(t, c, "CREATE TABLE t (n INTEGER)")

	for i := range 200 {
		// Exec1 runs under a ctx that is already-cancelled. The step itself
		// returns OK (the statement is too fast to interrupt mid-flight), but
		// the spawned watchCtx goroutine observes ctx.Done() and calls
		// sqlite3_interrupt(db). Pre-fix, that call could land after Exec
		// returned; post-fix, stop() blocks until the goroutine exits so any
		// interrupt has already happened (and SQLite's per-step auto-clear
		// can dismiss it before the next statement starts).
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = c.Exec(ctx, "INSERT INTO t (n) VALUES (?)", i)

		// Exec2 runs under a fresh, alive ctx. It must complete normally —
		// any stale interrupt from Exec1 would surface here as
		// SQLITE_INTERRUPT.
		if _, err := c.Exec(context.Background(), "INSERT INTO t (n) VALUES (?)", -i); err != nil {
			if IsCode(err, CodeInterrupt) {
				t.Fatalf("iter %d: stale interrupt leaked from prior Exec — %v", i, err)
			}
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
	}
}

// TestDisableAttach_RejectsAttach verifies that after DisableAttach, ATTACH
// DATABASE statements are rejected by the SQLite engine itself — Defense 1 of
// the cross-database-query prevention chain.
func TestDisableAttach_RejectsAttach(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	c.DisableAttach()

	_, err = c.Exec(context.Background(), "ATTACH DATABASE ':memory:' AS aux")
	if err == nil {
		t.Fatal("expected ATTACH to fail after DisableAttach, got nil error")
	}
}

// TestDisableAttach_RegularQueriesStillWork verifies DisableAttach affects
// only ATTACH — DDL/DML/DQL keep working on the same connection.
func TestDisableAttach_RegularQueriesStillWork(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	c.DisableAttach()

	mustExec(t, c, "CREATE TABLE t (x INTEGER)")
	mustExec(t, c, "INSERT INTO t VALUES (?)", 42)

	rows, err := c.Query(context.Background(), "SELECT x FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer tClose(t, "rows", rows)
	if !rows.Next() {
		t.Fatal("expected one row")
	}
	var x int64
	if err := rows.Scan(&x); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if x != 42 {
		t.Errorf("got x=%d, want 42", x)
	}
}

// TestOpen_DeniesAttach pins the binding's default behaviour: a fresh Conn
// from Open() denies ATTACH at parse time via the authorizer hook. The
// migration tool uses OpenPlaintext (no authorizer installed) so it can
// still run sqlcipher_export against an attached database.
func TestOpen_DeniesAttach(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	if _, err := c.Exec(context.Background(), "ATTACH DATABASE ':memory:' AS aux"); err == nil {
		t.Fatal("ATTACH should be denied by the authorizer, got nil error")
	}
}

// TestOpenPlaintext_BasicReadWrite verifies the unencrypted entry point
// can open a fresh file, run schema + DML, and read it back. Pins the
// migration-style usage even though no production caller exists today.
func TestOpenPlaintext_BasicReadWrite(t *testing.T) {
	c, err := OpenPlaintext(tmpDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	mustExec(t, c, "CREATE TABLE t (x INTEGER)")
	mustExec(t, c, "INSERT INTO t VALUES (?)", int64(42))

	rows, err := c.Query(context.Background(), "SELECT x FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer tClose(t, "rows", rows)

	if !rows.Next() {
		t.Fatal("expected one row")
	}
	var x int64
	if err := rows.Scan(&x); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if x != 42 {
		t.Errorf("got x=%d, want 42", x)
	}
}

// TestOpenPlaintext_NoAuthorizerInstalled pins the deliberate difference
// from Open(): OpenPlaintext skips the reserved-prefix authorizer so
// migration-style flows can ATTACH and write directly to `_*` platform
// tables. If this ever fails it means OpenPlaintext started installing
// the authorizer — production callers using Open() will continue to be
// protected, but plaintext-inspection / migration use cases would break.
func TestOpenPlaintext_NoAuthorizerInstalled(t *testing.T) {
	c, err := OpenPlaintext(tmpDB(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	// ATTACH succeeds without authorizer.
	if _, err := c.Exec(context.Background(), "ATTACH DATABASE ':memory:' AS aux"); err != nil {
		t.Fatalf("ATTACH should succeed without authorizer: %v", err)
	}
	// Direct write to a reserved-prefix table also succeeds.
	mustExec(t, c, "CREATE TABLE _platform (id INTEGER)")
	mustExec(t, c, "INSERT INTO _platform VALUES (1)")
}

// TestBindEmptyStringIsEmptyNotNull verifies an empty string parameter binds as empty TEXT, not SQL NULL:
// inserting "" into a NOT NULL column succeeds and reads back as "" with IS NULL = 0. Regression for the
// binding handing SQLite a nil data pointer for empty strings, which SQLite treated as NULL.
func TestBindEmptyStringIsEmptyNotNull(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	mustExec(t, c, "CREATE TABLE t (c TEXT NOT NULL)")
	mustExec(t, c, "INSERT INTO t (c) VALUES (?)", "") // would fail the NOT NULL constraint before the fix

	rows, err := c.Query(context.Background(), "SELECT c, c IS NULL FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !rows.Next() {
		tClose(t, "rows", rows)
		t.Fatalf("expected one row, got none (err=%v)", rows.Err())
	}
	var (
		got    string
		isNull int64
	)
	if err := rows.Scan(&got, &isNull); err != nil {
		tClose(t, "rows", rows)
		t.Fatalf("scan: %v", err)
	}
	tClose(t, "rows", rows)

	if got != "" {
		t.Errorf("c = %q, want empty string", got)
	}
	if isNull != 0 {
		t.Errorf("c IS NULL = %d, want 0 (empty string must store as '', not NULL)", isNull)
	}
}

// TestBindEmptyBytesIsEmptyNotNull verifies an empty []byte parameter binds as a zero-length BLOB, not SQL NULL:
// inserting an empty blob into a NOT NULL column succeeds and reads back with IS NULL = 0. Regression for the
// binding handing SQLite a nil data pointer for empty []byte, which SQLite treated as NULL.
func TestBindEmptyBytesIsEmptyNotNull(t *testing.T) {
	c, err := Open(tmpDB(t), newKey(t), 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer tClose(t, "conn", c)

	mustExec(t, c, "CREATE TABLE t (c BLOB NOT NULL)")
	mustExec(t, c, "INSERT INTO t (c) VALUES (?)", []byte{}) // would fail the NOT NULL constraint before the fix

	rows, err := c.Query(context.Background(), "SELECT c, c IS NULL FROM t")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !rows.Next() {
		tClose(t, "rows", rows)
		t.Fatalf("expected one row, got none (err=%v)", rows.Err())
	}
	var (
		got    []byte
		isNull int64
	)
	if err := rows.Scan(&got, &isNull); err != nil {
		tClose(t, "rows", rows)
		t.Fatalf("scan: %v", err)
	}
	tClose(t, "rows", rows)

	if isNull != 0 {
		t.Errorf("c IS NULL = %d, want 0 (empty []byte must store as a zero-length blob, not NULL)", isNull)
	}
	if len(got) != 0 {
		t.Errorf("c = %v, want empty blob", got)
	}
}
