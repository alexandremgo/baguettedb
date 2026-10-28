//go:build cgo

package sqlitelocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// sqliteHeader is how every unencrypted SQLite database file starts. A SQLCipher file starts with random bytes
// instead, so the header tells the two apart before any key is tried.
var sqliteHeader = []byte("SQLite format 3\x00")

// minPageSize is SQLite's smallest page size. Every SQLite or SQLCipher database file is a whole number of pages,
// each a power of two at least this large, so its size is a multiple of it.
const minPageSize = 512

// codeCorrupt is SQLITE_CORRUPT, the primary code of every corruption error.
const codeCorrupt = 11

// platformTablesQuery counts the platform tables every bdb export carries, by their names alone.
const platformTablesQuery = `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name IN
	('_change_log', '_formulas', '_skills')`

// platformTableCount is how many tables platformTablesQuery looks for.
const platformTableCount = 3

// InspectImport opens the exported database file at path, unencrypted or under passphrase, and describes it for an
// import: its platform schema version, whether it holds the platform tables, and whether it passes SQLite's quick
// check. Opening may let SQLite recover the file's journal, so path should be a copy the import owns.
//
// An encrypted file without a passphrase is a domain.ErrImportPassphraseNeeded; one whose size no database file has,
// empty or not a whole number of pages, a domain.ErrImportNotExport; one that doesn't open, under a wrong passphrase
// or not being a SQLite database, a domain.ErrImportUnreadable; one too damaged to open a domain.ErrImportDamaged.
func (c *Client) InspectImport(ctx context.Context, path, passphrase string) (domain.ImportFile, error) {
	conn, err := openImportSource(ctx, path, passphrase)
	if err != nil {
		return domain.ImportFile{}, err
	}
	defer c.closeSnapshotConn(conn, path)

	var file domain.ImportFile
	version, err := readInt64Pragma(ctx, conn, "PRAGMA user_version")
	if err != nil {
		return domain.ImportFile{}, fmt.Errorf("reading the file's user_version: %w", err)
	}
	file.PlatformSchemaVersion = int(version)
	tables, err := readInt64Pragma(ctx, conn, platformTablesQuery)
	if err != nil {
		return domain.ImportFile{}, fmt.Errorf("listing the file's tables: %w", err)
	}
	file.HasPlatformTables = tables == platformTableCount
	if file.Intact, err = quickCheck(ctx, conn); err != nil {
		return domain.ImportFile{}, err
	}
	return file, nil
}

// InstallImport copies the exported database file at path, unencrypted or under passphrase, re-encrypted under
// databaseID's own key, and installs the copy as databaseID's live file in place of the current one, as
// AdoptDatabaseFile does with force. path itself is left where it is. Errors opening the file are InspectImport's.
func (c *Client) InstallImport(ctx context.Context, databaseID, path, passphrase string) error {
	dek, err := c.dekResolver(ctx, databaseID)
	if err != nil {
		return fmt.Errorf("resolving DEK for %q: %w", databaseID, err)
	}
	if len(dek) != KeySize {
		return fmt.Errorf("sqlitelocal: dekResolver returned %d bytes, want %d", len(dek), KeySize)
	}
	conn, err := openImportSource(ctx, path, passphrase)
	if err != nil {
		return err
	}

	// Beside the live file, so the rename into place stays on one filesystem; a copy a crash left behind goes first.
	dest := c.databaseFilePath(databaseID) + ".import"
	c.removeSnapshotFiles(dest)
	if err := c.exportToDest(ctx, conn, databaseID, dest, string(keySpec(dek))); err != nil {
		return fmt.Errorf("copying the file under the database's key: %w", err)
	}
	if err := c.AdoptDatabaseFile(ctx, databaseID, dest, true); err != nil {
		c.removeSnapshotFiles(dest)
		return err
	}
	return nil
}

// openImportSource opens the database file at path for an import, with no key when it starts with the SQLite header
// and under passphrase otherwise, and stops the functions its schema calls from running unless they are safe anywhere:
// the file comes from outside. Errors are InspectImport's.
func openImportSource(ctx context.Context, path, passphrase string) (*Conn, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading the file: %w", err)
	}
	header, err := readHeader(path)
	if err != nil {
		return nil, err
	}
	var key []byte
	switch {
	case info.Size() == 0 || info.Size()%minPageSize != 0:
		return nil, domain.ErrImportNotExport
	case !bytes.Equal(header, sqliteHeader) && passphrase == "":
		return nil, domain.ErrImportPassphraseNeeded
	case !bytes.Equal(header, sqliteHeader):
		key = []byte(passphrase)
	}

	conn, err := openNoAuthorizer(path, key, 0)
	if isCorrupt(err) {
		return nil, domain.ErrImportDamaged
	}
	if IsCode(err, CodeNotADB) {
		return nil, domain.ErrImportUnreadable
	}
	if err != nil {
		return nil, fmt.Errorf("opening the file: %w", err)
	}
	if _, err := conn.Exec(ctx, "PRAGMA trusted_schema = OFF"); err != nil {
		return nil, errors.Join(fmt.Errorf("distrusting the file's schema: %w", err), conn.Close())
	}
	return conn, nil
}

// readHeader returns the first bytes of the file at path, as many as sqliteHeader holds, or fewer for a shorter file.
func readHeader(path string) (_ []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening the file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("closing the file: %w", cerr))
		}
	}()
	header := make([]byte, len(sqliteHeader))
	n, err := io.ReadFull(f, header)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("reading the file: %w", err)
	}
	return header[:n], nil
}

// quickCheck reports whether conn's database passes PRAGMA quick_check, which answers a single "ok" row when it does
// and describes the first problem otherwise; a check that stops on corruption is a failed check, not an error.
func quickCheck(ctx context.Context, conn *Conn) (_ bool, err error) {
	rows, err := conn.Query(ctx, "PRAGMA quick_check(1)")
	if isCorrupt(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking the file's integrity: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && !isCorrupt(cerr) {
			err = errors.Join(err, fmt.Errorf("closing the integrity check: %w", cerr))
		}
	}()
	var result string
	if rows.Next() {
		if err := rows.Scan(&result); err != nil {
			return false, fmt.Errorf("reading the integrity check: %w", err)
		}
	}
	if err := rows.Err(); isCorrupt(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("reading the integrity check: %w", err)
	}
	return result == "ok", nil
}

// isCorrupt reports whether err is a SQLite corruption error, of any extended code.
func isCorrupt(err error) bool {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Code&0xff == codeCorrupt
	}
	return false
}
