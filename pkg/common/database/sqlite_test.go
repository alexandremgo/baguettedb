package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rs/zerolog"
)

// openTestSQLite opens a fresh SQLite file in a temp directory and closes it when the test ends.
func openTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing sqlite: %v", err)
		}
	})
	return db
}

// TestOpenSQLite_Pragmas checks that a handle from OpenSQLite runs in WAL mode, enforces foreign keys and waits on
// a busy lock, on every connection of its pool.
func TestOpenSQLite_Pragmas(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "instance.sqlite"))
	db.SetMaxOpenConns(3)

	for range 3 {
		var journalMode string
		var foreignKeys, busyTimeout int
		if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
			t.Fatalf("reading journal_mode: %v", err)
		}
		if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("reading foreign_keys: %v", err)
		}
		if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("reading busy_timeout: %v", err)
		}
		if journalMode != "wal" || foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("journal_mode=%s foreign_keys=%d busy_timeout=%d, want wal 1 5000",
				journalMode, foreignKeys, busyTimeout)
		}
	}
}

// TestOpenSQLite_TimestampRoundTrip checks that a time.Time written to a TIMESTAMP column reads back as the same
// instant, with its sub-second precision.
func TestOpenSQLite_TimestampRoundTrip(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "instance.sqlite"))
	if _, err := db.Exec("CREATE TABLE t (at TIMESTAMP NOT NULL)"); err != nil {
		t.Fatalf("creating table: %v", err)
	}

	want := time.Date(2026, 9, 26, 17, 5, 54, 123456789, time.UTC)
	if _, err := db.Exec("INSERT INTO t (at) VALUES (?)", want); err != nil {
		t.Fatalf("inserting: %v", err)
	}
	var got time.Time
	if err := db.QueryRow("SELECT at FROM t").Scan(&got); err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if !got.Equal(want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// TestOpenSQLite_WritesTimesInUTC checks that a time in another zone is stored as its UTC instant, so stored times
// share one offset and sort as text.
func TestOpenSQLite_WritesTimesInUTC(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "instance.sqlite"))
	if _, err := db.Exec("CREATE TABLE t (at TIMESTAMP NOT NULL)"); err != nil {
		t.Fatalf("creating table: %v", err)
	}

	paris := time.FixedZone("CEST", 2*60*60)
	if _, err := db.Exec("INSERT INTO t (at) VALUES (?)", time.Date(2026, 9, 26, 19, 5, 0, 0, paris)); err != nil {
		t.Fatalf("inserting: %v", err)
	}
	var stored string
	if err := db.QueryRow("SELECT CAST(at AS TEXT) FROM t").Scan(&stored); err != nil {
		t.Fatalf("reading the stored text: %v", err)
	}
	if want := "2026-09-26 17:05:00+00:00"; stored != want {
		t.Errorf("stored %q, want %q", stored, want)
	}
}

// TestIsSQLiteUniqueViolation checks that a duplicate primary key and a duplicate UNIQUE value are both recognized,
// and that any other failure is not.
func TestIsSQLiteUniqueViolation(t *testing.T) {
	db := openTestSQLite(t, filepath.Join(t.TempDir(), "instance.sqlite"))
	if _, err := db.Exec("CREATE TABLE t (id TEXT PRIMARY KEY, email TEXT UNIQUE)"); err != nil {
		t.Fatalf("creating table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO t VALUES ('a', 'a@x')"); err != nil {
		t.Fatalf("inserting: %v", err)
	}

	_, dupKey := db.Exec("INSERT INTO t VALUES ('a', 'b@x')")
	_, dupEmail := db.Exec("INSERT INTO t VALUES ('b', 'a@x')")
	_, noTable := db.Exec("INSERT INTO missing VALUES (1)")
	if !IsSQLiteUniqueViolation(dupKey) || !IsSQLiteUniqueViolation(dupEmail) {
		t.Errorf("duplicate key %v / email %v not recognized", dupKey, dupEmail)
	}
	if IsSQLiteUniqueViolation(noTable) || IsSQLiteUniqueViolation(nil) {
		t.Errorf("a non-constraint error %v was recognized", noTable)
	}
}

// TestOpenSQLite_WriterWaitsForConcurrentWriter checks that a second handle on the same file, as another process
// would hold, waits for an open write transaction to commit instead of failing with SQLITE_BUSY.
func TestOpenSQLite_WriterWaitsForConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instance.sqlite")
	first := openTestSQLite(t, path)
	second := openTestSQLite(t, path)
	if _, err := first.Exec("CREATE TABLE t (n INTEGER NOT NULL)"); err != nil {
		t.Fatalf("creating table: %v", err)
	}

	tx, err := first.Begin()
	if err != nil {
		t.Fatalf("beginning the first transaction: %v", err)
	}
	if _, err := tx.Exec("INSERT INTO t (n) VALUES (1)"); err != nil {
		t.Fatalf("writing in the first transaction: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := second.Exec("INSERT INTO t (n) VALUES (2)")
		done <- err
	}()

	time.Sleep(200 * time.Millisecond)
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing the first transaction: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("second writer failed instead of waiting: %v", err)
	}

	var count int
	if err := first.QueryRow("SELECT COUNT(*) FROM t").Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 2 {
		t.Errorf("rows = %d, want 2", count)
	}
}

// TestRunSQLiteMigrations checks that two apps migrate the same file side by side, each tracked in its own
// table, that a second run is a no-op, and that a handle the caller holds on the file stays usable throughout.
func TestRunSQLiteMigrations(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "instance.sqlite")
	held := openTestSQLite(t, path)

	apps := map[string]fstest.MapFS{}
	for _, name := range []string{"alpha", "beta"} {
		up := "CREATE TABLE IF NOT EXISTS " + name + " (id TEXT PRIMARY KEY);"
		apps[name] = fstest.MapFS{"000001_init_" + name + ".up.sql": {Data: []byte(up)}}
	}
	for range 2 {
		for name, fsys := range apps {
			if err := RunSQLiteMigrations(ctx, path, fsys, name, zerolog.Nop()); err != nil {
				t.Fatalf("migrating %s: %v", name, err)
			}
		}
	}

	for _, table := range []string{"alpha", "beta", "schema_migrations_alpha", "schema_migrations_beta"} {
		var name string
		err := held.QueryRow("SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	var version int
	if err := held.QueryRow("SELECT version FROM schema_migrations_alpha").Scan(&version); err != nil || version != 1 {
		t.Errorf("schema_migrations_alpha version = %d (%v), want 1", version, err)
	}
}

// TestInPlaceholders checks the placeholder list for an empty, a single and a multi-value IN clause.
func TestInPlaceholders(t *testing.T) {
	for n, want := range map[int]string{0: "", 1: "?", 3: "?, ?, ?"} {
		if got := InPlaceholders(n); got != want {
			t.Errorf("InPlaceholders(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestEscapeLike checks that the LIKE wildcards and the escape character come out escaped, and that a term with
// none of them is unchanged.
func TestEscapeLike(t *testing.T) {
	for in, want := range map[string]string{
		"alice":    "alice",
		"100%":     `100\%`,
		"a_b":      `a\_b`,
		`c:\dir`:   `c:\\dir`,
		`%_\`:      `\%\_\\`,
		"":         "",
		"é_accent": `é\_accent`,
	} {
		if got := EscapeLike(in); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLatestMigrationVersion checks that the latest version is the highest numbered migration, whatever the order of
// the files and with gaps, and that a directory without migrations is an error.
func TestLatestMigrationVersion(t *testing.T) {
	fsys := fstest.MapFS{
		"000002_second.up.sql": {Data: []byte("SELECT 1;")},
		"000010_tenth.up.sql":  {Data: []byte("SELECT 1;")},
		"000001_first.up.sql":  {Data: []byte("SELECT 1;")},
	}
	if v, err := LatestMigrationVersion(fsys); err != nil || v != 10 {
		t.Errorf("LatestMigrationVersion = %d, %v; want 10", v, err)
	}
	if _, err := LatestMigrationVersion(fstest.MapFS{}); err == nil {
		t.Error("LatestMigrationVersion of no migration = nil error, want one")
	}
}
