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

// AdoptDatabaseFile installs the database file at srcPath as databaseID's live database file. srcPath must be a
// complete database file on the same filesystem as the data dir; on success it is consumed (moved into place,
// not copied) and the database is immediately servable, with no stale state from a previous incarnation
// surviving adoption.
//
// When a live file already exists for databaseID, returns domain.ErrDatabaseFileExists unless force is set —
// with force the live database is replaced, losing any writes newer than the adopted file; queries served
// afterwards observe the adopted file's content, never the replaced one's.
func (c *Client) AdoptDatabaseFile(_ context.Context, databaseID string, srcPath string, force bool) error {
	path := c.databaseFilePath(databaseID)
	info, err := os.Stat(path)

	if info != nil && !force {
		return fmt.Errorf("%w: %q", domain.ErrDatabaseFileExists, path)
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reading file info of %q: %w", path, err)
	}

	if err := c.cache.Evict(databaseID); err != nil {
		return fmt.Errorf("evicting cached connection for %q: %w", databaseID, err)
	}

	// A leftover WAL/SHM from the database's previous incarnation must never survive adoption: SQLite would
	// replay the stale WAL against the restored main file on next open and corrupt it. Fails rather than
	// proceeding past a sidecar it could not remove.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing stale sidecar %q: %w", path+suffix, err)
		}
	}

	if err := os.Rename(srcPath, path); err != nil {
		return fmt.Errorf("renaming %q to %q: %w", srcPath, path, err)
	}

	return nil
}
