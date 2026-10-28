//go:build cgo

package sqlitelocal

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// Snapshot writes a consistent encrypted copy of databaseID's SQLCipher file to destPath, keyed with the same DEK
// as the source. destPath must not exist yet; on failure any partial destination files are removed.
//
// The copy runs on a dedicated short-lived connection (keyed, no authorizer — see openKeyedNoAuthorizer) that
// ATTACHes destPath and runs sqlcipher_export inside a single transaction. WAL MVCC (Multi-Version Concurrency
// Control) gives that transaction a stable view of the source without blocking the cached connection, so writer
// traffic continues during the copy. sqlcipher_export copies schema and rows but not PRAGMA user_version, which
// holds the platform schema version — it is copied explicitly inside the same transaction.
func (c *Client) Snapshot(ctx context.Context, databaseID string, destPath string) error {
	conn, dek, err := c.openSourceForCopy(ctx, databaseID)
	if err != nil {
		return err
	}
	// The destination is keyed in raw-key form, the same way Open reads a database file. Handing the DEK over as
	// plain bytes here would key the snapshot as a passphrase instead, producing a file the platform can no
	// longer open (see keySpec).
	return c.exportToDest(ctx, conn, databaseID, destPath, string(keySpec(dek)))
}

// Export writes a consistent copy of databaseID's SQLCipher file to destPath, re-encrypted under passphrase
// instead of the database's own DEK — the DEK never leaves the platform, and the copy opens anywhere with
// SQLCipher's standard passphrase processing (PRAGMA key = '<passphrase>'). destPath must not exist yet; on
// failure any partial destination files are removed. The copy mechanics are identical to Snapshot: dedicated
// connection, single transaction, WAL MVCC keeps writer traffic unblocked during the copy.
func (c *Client) Export(ctx context.Context, databaseID string, destPath string, passphrase string) error {
	// SQLCipher treats an empty key as "no encryption" — refuse rather than silently produce a plaintext file.
	if passphrase == "" {
		return errors.New("sqlitelocal: export passphrase must not be empty")
	}

	conn, _, err := c.openSourceForCopy(ctx, databaseID)
	if err != nil {
		return err
	}
	return c.exportToDest(ctx, conn, databaseID, destPath, passphrase)
}

// openSourceForCopy resolves databaseID's DEK and opens its file on a dedicated no-authorizer connection for a
// copy operation, returning the DEK alongside so a same-key copy can reuse it as the destination key.
func (c *Client) openSourceForCopy(ctx context.Context, databaseID string) (*Conn, []byte, error) {
	dek, err := c.dekResolver(ctx, databaseID)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving DEK for %q: %w", databaseID, err)
	}
	if len(dek) != KeySize {
		return nil, nil, fmt.Errorf("sqlitelocal: dekResolver returned %d bytes, want %d", len(dek), KeySize)
	}

	srcPath := c.databaseFilePath(databaseID)
	if _, err := os.Stat(srcPath); errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("%w: %q", domain.ErrDatabaseFileMissing, srcPath)
	}

	// The connection opens without the CREATE flag, so a source deleted between the stat above and here fails the
	// open instead of being recreated empty.
	conn, err := openKeyedNoAuthorizer(srcPath, dek, c.busyTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("opening source %q for copy: %w", srcPath, err)
	}
	return conn, dek, nil
}

// exportToDest pre-creates destPath exclusively and runs the export sequence on conn, keying the destination with
// destKey. Always closes conn; removes partial destination files on failure.
func (c *Client) exportToDest(ctx context.Context, conn *Conn, databaseID, destPath, destKey string) error {
	// Pre-creates the destination exclusively: refuses to overwrite an existing snapshot, and gives ATTACH (which
	// inherits the connection's no-CREATE open flags) an empty file it can initialize as a fresh database.
	f, err := os.OpenFile(destPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		c.closeSnapshotConn(conn, databaseID)
		return fmt.Errorf("creating snapshot destination: %w", err)
	}
	if err := f.Close(); err != nil {
		c.closeSnapshotConn(conn, databaseID)
		c.removeSnapshotFiles(destPath)
		return fmt.Errorf("closing snapshot destination after create: %w", err)
	}

	exportErr := exportSnapshot(ctx, conn, destPath, destKey)
	c.closeSnapshotConn(conn, databaseID)
	if exportErr != nil {
		c.removeSnapshotFiles(destPath)
		return exportErr
	}
	return nil
}

// exportSnapshot runs the attach → export → copy-user_version → detach sequence on a dedicated keyed connection.
// The destination file must already exist (empty is fine).
//
// destKey is the destination's encryption key, and its *form* decides how SQLCipher treats it.
// - A same-key snapshot passes the DEK in raw-key form (`x'<hex>'`, see keySpec) so the copy is keyed exactly like
// a live database file.
// - An export passes its passphrase verbatim, which SQLCipher deliberately stretches with PBKDF2 — that is what
// lets the copy be opened outside the platform with a plain `PRAGMA key`.
// Both arrive as strings, so the difference is invisible at the call site: do not "unify" them.
func exportSnapshot(ctx context.Context, conn *Conn, destPath, destKey string) error {
	// The key binds as a parameter, which SQLCipher feeds through the same key-processing path as the sqlite3_key
	// call that keyed the source — the copy ends up openable with the exact same key material. Binding also keeps
	// key material out of the SQL text.
	if _, err := conn.Exec(ctx, "ATTACH DATABASE ? AS snapshot KEY ?", destPath, destKey); err != nil {
		return fmt.Errorf("attaching snapshot destination: %w", err)
	}

	// Marks the copy as WAL in its header so a restored file comes up in WAL mode on first open, matching how
	// CreateDatabase configures a fresh database. Must run outside the export transaction (journal-mode changes
	// are refused inside one).
	if _, err := conn.Exec(ctx, "PRAGMA snapshot.journal_mode = WAL"); err != nil {
		return fmt.Errorf("setting WAL on snapshot destination: %w", err)
	}

	if _, err := conn.Exec(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("beginning snapshot transaction: %w", err)
	}

	if err := func() error {
		if _, err := conn.Exec(ctx, "SELECT sqlcipher_export('snapshot')"); err != nil {
			return fmt.Errorf("sqlcipher_export: %w", err)
		}
		userVersion, err := readInt64Pragma(ctx, conn, "PRAGMA main.user_version")
		if err != nil {
			return fmt.Errorf("reading source user_version: %w", err)
		}
		if _, err := conn.Exec(ctx, fmt.Sprintf("PRAGMA snapshot.user_version = %d", userVersion)); err != nil {
			return fmt.Errorf("setting snapshot user_version: %w", err)
		}
		return nil
	}(); err != nil {
		// Best-effort rollback on a fresh ctx so a cancelled caller still releases the transaction; the
		// connection is discarded right after, so a failed rollback only matters for the error already being
		// returned.
		_, _ = conn.Exec(context.Background(), "ROLLBACK")
		return err
	}

	if _, err := conn.Exec(ctx, "COMMIT"); err != nil {
		_, _ = conn.Exec(context.Background(), "ROLLBACK")
		return fmt.Errorf("committing snapshot transaction: %w", err)
	}

	// The exported pages so far live in the destination's -wal sidecar, not destPath itself. Detaching runs the
	// final checkpoint: the WAL is folded into destPath and the -wal/-shm sidecars deleted — one file to ship.
	if _, err := conn.Exec(ctx, "DETACH DATABASE snapshot"); err != nil {
		return fmt.Errorf("detaching snapshot destination: %w", err)
	}
	return nil
}

// closeSnapshotConn closes a dedicated snapshot connection, logging instead of failing — by this point the snapshot
// outcome is already decided.
func (c *Client) closeSnapshotConn(conn *Conn, databaseID string) {
	if err := conn.Close(); err != nil {
		c.logger.Warn().Err(err).Str("database_id", databaseID).
			Msg("sqlitelocal: closing snapshot connection")
	}
}

// removeSnapshotFiles deletes a failed snapshot's main file plus WAL/SHM sidecars. Missing files are expected (the
// failure may predate their creation).
func (c *Client) removeSnapshotFiles(destPath string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(destPath + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			c.logger.Warn().Err(err).Str("path", destPath+suffix).
				Msg("sqlitelocal: removing partial snapshot file")
		}
	}
}
