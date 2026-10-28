//go:build cgo

package sqlitelocal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// openSnapshotForInspection opens a snapshot file with the production Open path (authorizer installed) and registers
// cleanup. Stats the file first — Open would silently create a missing one.
func openSnapshotForInspection(t *testing.T, path string, key []byte) *Conn {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot file: %v", err)
	}
	conn, err := Open(path, key, 0)
	if err != nil {
		t.Fatalf("opening snapshot %q: %v", path, err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("closing snapshot conn: %v", err)
		}
	})
	return conn
}

// countAndMaxID returns count(*) and coalesce(max(id), 0) from table t.
func countAndMaxID(t *testing.T, conn *Conn) (count, maxID int64) {
	t.Helper()
	rows, err := conn.Query(context.Background(), "SELECT count(*), coalesce(max(id), 0) FROM t")
	if err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}()
	if !rows.Next() {
		t.Fatalf("count query returned no rows: %v", rows.Err())
	}
	if err := rows.Scan(&count, &maxID); err != nil {
		t.Fatalf("scanning count: %v", err)
	}
	return count, maxID
}

// TestClient_Snapshot_CopiesDataAndUserVersion verifies the copy opens with the same DEK and carries the rows, the
// user_version, and a WAL header — as a single file, with no -wal/-shm sidecars left behind.
func TestClient_Snapshot_CopiesDataAndUserVersion(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "db1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "db1", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := c.Execute(ctx, "db1", "INSERT INTO t (v) VALUES ('a'), ('b'), ('c')", nil); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := c.ExecuteInternal(ctx, "db1", "PRAGMA user_version = 7", nil); err != nil {
		t.Fatalf("setting user_version: %v", err)
	}

	destPath := filepath.Join(t.TempDir(), "snap.db")
	if err := c.Snapshot(ctx, "db1", destPath); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// The shipped artifact must be a single file — DETACH checkpoints the WAL away.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(destPath + suffix); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected no %s sidecar, stat err = %v", suffix, err)
		}
	}

	conn := openSnapshotForInspection(t, destPath, resolver.key)

	if count, _ := countAndMaxID(t, conn); count != 3 {
		t.Errorf("snapshot row count = %d, want 3", count)
	}

	if err := conn.WithInternal(func() error {
		userVersion, err := readInt64Pragma(context.Background(), conn, "PRAGMA user_version")
		if err != nil {
			return err
		}
		if userVersion != 7 {
			t.Errorf("snapshot user_version = %d, want 7", userVersion)
		}
		journalMode, err := conn.queryString(context.Background(), "PRAGMA journal_mode")
		if err != nil {
			return err
		}
		if !strings.EqualFold(journalMode, "wal") {
			t.Errorf("snapshot journal_mode = %q, want wal", journalMode)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspecting snapshot pragmas: %v", err)
	}
}

// TestClient_Snapshot_UnderConcurrentWrites verifies snapshots taken while a writer hammers the database are
// internally consistent (count == max(id), integrity_check ok) and never block the writer.
func TestClient_Snapshot_UnderConcurrentWrites(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "db1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, "db1", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}

	// Hammers single-row autocommit inserts on the cached connection while snapshots run on their dedicated
	// connection. Sequential rowids with no rollbacks mean any consistent snapshot must satisfy count == max(id).
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var writerErr error
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := c.Execute(ctx, "db1", "INSERT INTO t (v) VALUES (?)", []domain.TypedValue{
				domain.TextValue(fmt.Sprintf("row-%d", i)),
			}); err != nil {
				writerErr = err
				return
			}
		}
	})

	for i := range 3 {
		destPath := filepath.Join(t.TempDir(), fmt.Sprintf("snap-%d.db", i))
		if err := c.Snapshot(ctx, "db1", destPath); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Snapshot %d: %v", i, err)
		}

		conn := openSnapshotForInspection(t, destPath, resolver.key)
		count, maxID := countAndMaxID(t, conn)
		if count != maxID {
			t.Errorf("snapshot %d inconsistent: count = %d, max(id) = %d", i, count, maxID)
		}
		integrity, err := conn.queryString(context.Background(), "PRAGMA integrity_check")
		if err != nil {
			t.Fatalf("integrity_check on snapshot %d: %v", i, err)
		}
		if !strings.EqualFold(integrity, "ok") {
			t.Errorf("snapshot %d integrity_check = %q, want ok", i, integrity)
		}
	}

	close(stop)
	wg.Wait()
	if writerErr != nil {
		t.Fatalf("concurrent writer: %v", writerErr)
	}

	// Source must have kept accepting writes for the test to mean anything.
	var liveCount int64
	if res, err := c.Execute(ctx, "db1", "SELECT count(*) FROM t", nil); err != nil {
		t.Fatalf("counting source rows: %v", err)
	} else {
		liveCount = res.Rows[0][0].Value.(int64)
	}
	if liveCount == 0 {
		t.Error("writer inserted no rows — concurrency was not exercised")
	}
}

// TestClient_Snapshot_SourceMissing verifies a database with no file on disk yields ErrDatabaseFileMissing.
func TestClient_Snapshot_SourceMissing(t *testing.T) {
	c, _ := newTestClient(t)

	err := c.Snapshot(context.Background(), "never-created", filepath.Join(t.TempDir(), "snap.db"))
	if !errors.Is(err, domain.ErrDatabaseFileMissing) {
		t.Errorf("err = %v, want ErrDatabaseFileMissing", err)
	}
}

// TestClient_Snapshot_DestExists verifies an existing destination file is refused and left untouched.
func TestClient_Snapshot_DestExists(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "db1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}

	destPath := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(destPath, []byte("pre-existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := c.Snapshot(ctx, "db1", destPath)
	if !errors.Is(err, fs.ErrExist) {
		t.Errorf("err = %v, want fs.ErrExist", err)
	}

	// The pre-existing file must be untouched — failure cleanup only removes files the snapshot itself created.
	content, rerr := os.ReadFile(destPath)
	if rerr != nil || string(content) != "pre-existing" {
		t.Errorf("destination modified: content = %q, err = %v", content, rerr)
	}
}

// TestClient_Snapshot_AttachStaysDeniedForCachedConnections pins the security boundary the snapshot path must not
// loosen: ATTACH keeps failing on cached connections for both tenant and internal SQL, before and after a snapshot
// has run.
func TestClient_Snapshot_AttachStaysDeniedForCachedConnections(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()

	if err := c.CreateDatabase(ctx, "db1", resolver.key); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if err := c.Snapshot(ctx, "db1", filepath.Join(t.TempDir(), "snap.db")); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if _, err := c.Execute(ctx, "db1", "ATTACH DATABASE ':memory:' AS aux", nil); err == nil {
		t.Error("expected tenant ATTACH to fail after snapshot, got nil")
	}
	if _, err := c.ExecuteInternal(ctx, "db1", "ATTACH DATABASE ':memory:' AS aux", nil); err == nil {
		t.Error("expected internal ATTACH to fail after snapshot, got nil")
	}
}

// TestVacuumInto_InheritsEncryptionKey is a spike documenting VACUUM INTO behavior under SQLCipher. Two findings
// worth pinning:
//   - On a connection with the authorizer installed, VACUUM INTO fails with SQLITE_AUTH even in internal mode —
//     vacuum's internal ATTACH of the target fires the authorizer, which denies ATTACH unconditionally. It is only
//     usable on a no-authorizer connection.
//   - On such a connection, the vacuumed copy IS encrypted with the source's key.
//
// sqlcipher_export stays the primary snapshot mechanism — unlike VACUUM INTO it runs inside a caller-controlled
// transaction alongside the user_version copy.
func TestVacuumInto_InheritsEncryptionKey(t *testing.T) {
	dataDir := t.TempDir()
	key := newKey(t)
	srcPath := filepath.Join(dataDir, "src.db")

	src, err := Open(srcPath, key, 0)
	if err != nil {
		t.Fatalf("Open source: %v", err)
	}
	if _, err := src.Exec(context.Background(), "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := src.Exec(context.Background(), "INSERT INTO t (v) VALUES ('a'), ('b')"); err != nil {
		t.Fatalf("INSERT: %v", err)
	}

	destPath := filepath.Join(dataDir, "vacuumed.db")

	// Authorizer-installed connection: denied even in internal mode (the internal ATTACH of the vacuum target hits
	// the unconditional ATTACH denial).
	if err := src.WithInternal(func() error {
		_, e := src.Exec(context.Background(), "VACUUM INTO ?", destPath)
		return e
	}); !IsCode(err, CodeAuth) {
		t.Fatalf("VACUUM INTO on authorizer-installed conn: err = %v, want SQLITE_AUTH", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("closing source: %v", err)
	}

	tooling, err := openKeyedNoAuthorizer(srcPath, key, 0)
	if err != nil {
		t.Fatalf("openKeyedNoAuthorizer: %v", err)
	}
	defer func() {
		if err := tooling.Close(); err != nil {
			t.Errorf("closing tooling conn: %v", err)
		}
	}()
	if _, err := tooling.Exec(context.Background(), "VACUUM INTO ?", destPath); err != nil {
		t.Fatalf("VACUUM INTO on no-authorizer conn: %v", err)
	}

	if plain, err := OpenPlaintext(destPath); err == nil {
		_ = plain.Close()
		t.Fatal("vacuumed copy opened without a key — it is NOT encrypted")
	}

	copyConn, err := Open(destPath, key, 0)
	if err != nil {
		t.Fatalf("vacuumed copy did not open with the source key: %v", err)
	}
	defer func() {
		if err := copyConn.Close(); err != nil {
			t.Errorf("closing vacuumed copy: %v", err)
		}
	}()
	if count, _ := countAndMaxID(t, copyConn); count != 2 {
		t.Errorf("vacuumed copy row count = %d, want 2", count)
	}
}
