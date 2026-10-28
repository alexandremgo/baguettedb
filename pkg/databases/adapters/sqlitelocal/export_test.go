//go:build cgo

package sqlitelocal

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// sqlitePlaintextMagic is the 16-byte header every plaintext SQLite file starts with; an SQLCipher-encrypted file
// never carries it (its first page is ciphertext).
var sqlitePlaintextMagic = []byte("SQLite format 3\x00")

// seedExportSource provisions databaseID with 3 rows in table t and user_version 7.
func seedExportSource(t *testing.T, c *Client, databaseID string, dek []byte) {
	t.Helper()
	ctx := context.Background()
	if err := c.CreateDatabase(ctx, databaseID, dek); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := c.Execute(ctx, databaseID, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	if _, err := c.Execute(ctx, databaseID, "INSERT INTO t (v) VALUES ('a'), ('b'), ('c')", nil); err != nil {
		t.Fatalf("INSERT: %v", err)
	}
	if _, err := c.ExecuteInternal(ctx, databaseID, "PRAGMA user_version = 7", nil); err != nil {
		t.Fatalf("setting user_version: %v", err)
	}
}

// TestClient_Export_ReEncryptsWithPassphrase verifies the exported copy is encrypted (not plaintext), is NOT
// openable with the source DEK (the key really changed), IS openable with the export passphrase through
// SQLCipher's standard passphrase processing, and carries the rows and user_version — while the source stays
// untouched and DEK-openable.
func TestClient_Export_ReEncryptsWithPassphrase(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	seedExportSource(t, c, "db1", resolver.key)

	const passphrase = "correct-horse-battery-staple"
	destPath := filepath.Join(t.TempDir(), "export.sqlite")
	if err := c.Export(ctx, "db1", destPath, passphrase); err != nil {
		t.Fatalf("Export: %v", err)
	}

	// The shipped artifact must be a single file — DETACH checkpoints the WAL away.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(destPath + suffix); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("expected no %s sidecar, stat err = %v", suffix, err)
		}
	}

	// Encrypted at rest: no plaintext magic bytes, and no plaintext open.
	header, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("reading export: %v", err)
	}
	if bytes.HasPrefix(header, sqlitePlaintextMagic) {
		t.Error("export starts with the plaintext SQLite magic — it is NOT encrypted")
	}
	if plain, err := OpenPlaintext(destPath); err == nil {
		_ = plain.Close()
		t.Error("export opened without a key — it is NOT encrypted")
	}

	// Re-keyed: the source DEK must not open the export.
	if conn, err := Open(destPath, resolver.key, 0); err == nil {
		_ = conn.Close()
		t.Error("export opened with the source DEK — it was not re-keyed")
	}

	// Openable with the passphrase, through the same key processing external SQLCipher tools apply. The source
	// connection opening with its DEK also proves the source file is untouched.
	srcConn, err := openKeyedNoAuthorizer(c.databaseFilePath("db1"), resolver.key, 0)
	if err != nil {
		t.Fatalf("opening source for verification: %v", err)
	}
	defer func() {
		if err := srcConn.Close(); err != nil {
			t.Errorf("closing verification conn: %v", err)
		}
	}()
	if _, err := srcConn.Exec(ctx, "ATTACH DATABASE ? AS verify KEY ?", destPath, passphrase); err != nil {
		t.Fatalf("attaching export with passphrase: %v", err)
	}

	rows, err := srcConn.Query(ctx, "SELECT count(*) FROM verify.t")
	if err != nil {
		t.Fatalf("counting export rows: %v", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("closing rows: %v", err)
		}
	}()
	if !rows.Next() {
		t.Fatalf("count query returned no rows: %v", rows.Err())
	}
	var count int64
	if err := rows.Scan(&count); err != nil {
		t.Fatalf("scanning count: %v", err)
	}
	if count != 3 {
		t.Errorf("export row count = %d, want 3", count)
	}

	userVersion, err := readInt64Pragma(ctx, srcConn, "PRAGMA verify.user_version")
	if err != nil {
		t.Fatalf("reading export user_version: %v", err)
	}
	if userVersion != 7 {
		t.Errorf("export user_version = %d, want 7", userVersion)
	}
}

// TestClient_Export_WrongPassphraseFailsToOpen verifies an export cannot be read with a different passphrase.
func TestClient_Export_WrongPassphraseFailsToOpen(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	seedExportSource(t, c, "db1", resolver.key)

	destPath := filepath.Join(t.TempDir(), "export.sqlite")
	if err := c.Export(ctx, "db1", destPath, "right-passphrase"); err != nil {
		t.Fatalf("Export: %v", err)
	}

	srcConn, err := openKeyedNoAuthorizer(c.databaseFilePath("db1"), resolver.key, 0)
	if err != nil {
		t.Fatalf("opening source for verification: %v", err)
	}
	defer func() {
		if err := srcConn.Close(); err != nil {
			t.Errorf("closing verification conn: %v", err)
		}
	}()

	// SQLCipher may reject the key at ATTACH time or on the first read — either way the data must stay sealed.
	if _, err := srcConn.Exec(ctx, "ATTACH DATABASE ? AS bad KEY ?", destPath, "wrong-passphrase"); err == nil {
		if _, err := srcConn.Exec(ctx, "SELECT count(*) FROM bad.t"); err == nil {
			t.Error("export readable with a wrong passphrase")
		}
	}
}

// TestClient_Export_EmptyPassphraseRejected verifies the plaintext-export guard: an empty passphrase errors before
// any file is created.
func TestClient_Export_EmptyPassphraseRejected(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	seedExportSource(t, c, "db1", resolver.key)

	destPath := filepath.Join(t.TempDir(), "export.sqlite")
	if err := c.Export(ctx, "db1", destPath, ""); err == nil {
		t.Fatal("expected error for empty passphrase, got nil")
	}
	if _, err := os.Stat(destPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("destination should not exist after rejected export, stat err = %v", err)
	}
}

// TestClient_Export_SourceMissing verifies a database with no file on disk yields ErrDatabaseFileMissing.
func TestClient_Export_SourceMissing(t *testing.T) {
	c, _ := newTestClient(t)

	err := c.Export(context.Background(), "never-created", filepath.Join(t.TempDir(), "export.sqlite"), "pass")
	if !errors.Is(err, domain.ErrDatabaseFileMissing) {
		t.Errorf("err = %v, want ErrDatabaseFileMissing", err)
	}
}

// TestOpenWithPassphrase_KeySizedPassphraseIsNotKeyMaterial pins the distinction between a passphrase and raw
// key material, which is invisible at the call site because both are byte strings.
//
// The generated export passphrase is exactly KeySize characters, so it slips past Open's length check. While
// Open still keyed passphrase-style, handing it that passphrase happened to work — the two code paths were
// the same one. They are not anymore, and the coincidence would otherwise resurface as an export that opens
// in tests but nowhere else.
func TestOpenWithPassphrase_KeySizedPassphraseIsNotKeyMaterial(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	seedExportSource(t, c, "db1", resolver.key)

	const passphrase = "abcdefghijklmnopqrstuvwxyz012345"
	if len(passphrase) != KeySize {
		t.Fatalf("test passphrase is %d chars, want exactly KeySize (%d) to exercise the trap",
			len(passphrase), KeySize)
	}

	destPath := filepath.Join(t.TempDir(), "export.sqlite")
	if err := c.Export(ctx, "db1", destPath, passphrase); err != nil {
		t.Fatalf("Export: %v", err)
	}

	if conn, err := Open(destPath, []byte(passphrase), 0); err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing unexpectedly-opened conn: %v", cerr)
		}
		t.Error("export opened via Open — a passphrase is being accepted as raw key material")
	}

	conn, err := OpenWithPassphrase(destPath, passphrase)
	if err != nil {
		t.Fatalf("OpenWithPassphrase: %v", err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing export: %v", cerr)
		}
	}()
	if count, _ := countAndMaxID(t, conn); count != 3 {
		t.Errorf("export row count = %d, want 3", count)
	}
}

// TestOpenWithPassphrase_EmptyRejected guards the plaintext trap: SQLCipher reads an empty key as "no
// encryption", so an empty passphrase would open a plaintext file and report success.
func TestOpenWithPassphrase_EmptyRejected(t *testing.T) {
	if conn, err := OpenWithPassphrase(filepath.Join(t.TempDir(), "x.sqlite"), ""); err == nil {
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing conn: %v", cerr)
		}
		t.Error("expected an error for an empty passphrase")
	}
}
