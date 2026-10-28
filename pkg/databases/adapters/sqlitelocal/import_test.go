//go:build cgo

package sqlitelocal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// importPassphrase is the passphrase the exports these tests import are encrypted under.
const importPassphrase = "correct-horse-battery-staple"

// TestClient_InstallImport_Exports checks that an export, encrypted under its passphrase or decrypted, inspects as
// intact with its user_version, and installs as another database's live file, keyed with that database's own key,
// with the rows and user_version it was exported with, leaving the file imported from in place.
func TestClient_InstallImport_Exports(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	seedExportSource(t, c, "source", resolver.key)
	encrypted := filepath.Join(t.TempDir(), "export.sqlite")
	if err := c.Export(ctx, "source", encrypted, importPassphrase); err != nil {
		t.Fatalf("Export: %v", err)
	}
	plain := filepath.Join(t.TempDir(), "plain.sqlite")
	decryptExport(t, encrypted, importPassphrase, plain)

	for _, tc := range []struct{ name, path, passphrase string }{
		{"encrypted", encrypted, importPassphrase},
		{"decrypted", plain, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := c.InspectImport(ctx, tc.path, tc.passphrase)
			if err != nil || !file.Intact || file.PlatformSchemaVersion != 7 || file.HasPlatformTables {
				t.Fatalf("InspectImport = %+v, %v; want intact, version 7, no platform tables", file, err)
			}
			databaseID := "imported-" + tc.name
			if err := c.CreateDatabase(ctx, databaseID, resolver.key); err != nil {
				t.Fatalf("CreateDatabase: %v", err)
			}
			if err := c.InstallImport(ctx, databaseID, tc.path, tc.passphrase); err != nil {
				t.Fatalf("InstallImport: %v", err)
			}

			result, err := c.Execute(ctx, databaseID, "SELECT count(*) FROM t", nil)
			if err != nil || len(result.Rows) != 1 || result.Rows[0][0].Value != int64(3) {
				t.Errorf("rows in the installed database = %+v, %v; want 3", result, err)
			}
			conn, err := openKeyedNoAuthorizer(c.databaseFilePath(databaseID), resolver.key, 0)
			if err != nil {
				t.Fatalf("opening the installed file with the database's key: %v", err)
			}
			defer func() {
				if err := conn.Close(); err != nil {
					t.Errorf("closing: %v", err)
				}
			}()
			if v, err := readInt64Pragma(ctx, conn, "PRAGMA user_version"); err != nil || v != 7 {
				t.Errorf("installed user_version = %d, %v; want 7", v, err)
			}
			if _, err := os.Stat(tc.path); err != nil {
				t.Errorf("the imported file is gone: %v", err)
			}
		})
	}
}

// TestClient_InspectImport_Refusals checks the files InspectImport refuses or finds wanting: an encrypted export
// without its passphrase or under a wrong one, an empty file, a text file, and a decrypted export whose pages were
// overwritten.
func TestClient_InspectImport_Refusals(t *testing.T) {
	c, resolver := newTestClient(t)
	ctx := context.Background()
	seedExportSource(t, c, "source", resolver.key)
	dir := t.TempDir()
	encrypted := filepath.Join(dir, "export.sqlite")
	if err := c.Export(ctx, "source", encrypted, importPassphrase); err != nil {
		t.Fatalf("Export: %v", err)
	}

	for _, tc := range []struct {
		name, path, passphrase string
		want                   error
	}{
		{"no passphrase", encrypted, "", domain.ErrImportPassphraseNeeded},
		{"a wrong passphrase", encrypted, "wrong", domain.ErrImportUnreadable},
		{"an empty file", writeFile(t, dir, "empty.sqlite", nil), "", domain.ErrImportNotExport},
		{"a text file", writeFile(t, dir, "notes.txt", []byte("not a database\n")), "", domain.ErrImportNotExport},
	} {
		if _, err := c.InspectImport(ctx, tc.path, tc.passphrase); !errors.Is(err, tc.want) {
			t.Errorf("InspectImport of %s = %v, want %v", tc.name, err, tc.want)
		}
	}

	damaged := filepath.Join(dir, "damaged.sqlite")
	decryptExport(t, encrypted, importPassphrase, damaged)
	content, err := os.ReadFile(damaged)
	if err != nil {
		t.Fatal(err)
	}
	// Overwrites every page past the first, where the table's rows and the free list live.
	for i := 4096; i < len(content); i++ {
		content[i] = 0xff
	}
	writeFile(t, dir, "damaged.sqlite", content)
	if file, err := c.InspectImport(ctx, damaged, ""); err == nil && file.Intact {
		t.Errorf("InspectImport of a damaged file = %+v, want it not intact, or refused", file)
	}
}

// TestIsCorrupt checks that isCorrupt recognizes SQLite's corruption errors by their primary code, extended codes
// and wrapped errors included, and nothing else.
func TestIsCorrupt(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&Error{Code: 11}, true},
		{&Error{Code: 267}, true},
		{fmt.Errorf("opening: %w", &Error{Code: 523}), true},
		{&Error{Code: CodeNotADB}, false},
		{&Error{Code: 2067}, false},
		{errors.New("corrupt"), false},
		{nil, false},
	} {
		if got := isCorrupt(tc.err); got != tc.want {
			t.Errorf("isCorrupt(%v) = %t, want %t", tc.err, got, tc.want)
		}
	}
}

// decryptExport writes to dst an unencrypted copy of the export at src, under passphrase, user_version included, as
// SQLCipher's own tools decrypt one.
func decryptExport(t *testing.T, src, passphrase, dst string) {
	t.Helper()
	ctx := context.Background()
	conn, err := OpenPlaintext(dst)
	if err != nil {
		t.Fatalf("creating %s: %v", dst, err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("closing %s: %v", dst, err)
		}
	}()
	if _, err := conn.Exec(ctx, "ATTACH DATABASE ? AS export KEY ?", src, passphrase); err != nil {
		t.Fatalf("attaching the export: %v", err)
	}
	if _, err := conn.Exec(ctx, "SELECT sqlcipher_export('main', 'export')"); err != nil {
		t.Fatalf("decrypting: %v", err)
	}
	version, err := readInt64Pragma(ctx, conn, "PRAGMA export.user_version")
	if err != nil {
		t.Fatalf("reading the export's user_version: %v", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("PRAGMA main.user_version = %d", version)); err != nil {
		t.Fatalf("setting user_version: %v", err)
	}
	if _, err := conn.Exec(ctx, "DETACH DATABASE export"); err != nil {
		t.Fatalf("detaching the export: %v", err)
	}
}

// writeFile writes content to name in dir, owner-only, and returns its path.
func writeFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
