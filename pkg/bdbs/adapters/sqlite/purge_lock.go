package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/rs/zerolog"
)

// PurgeLock takes an exclusive, non-blocking file lock (flock) on a sidecar file next to the instance store's file,
// so only one purge sweep runs at a time across every process on the machine — and across lock instances within
// one process, since the lock belongs to each open file. The operating system drops it when the file is closed or
// the process dies, so a crashed sweep cannot leave it held.
//
// The lock is taken on its own file rather than on the database file, which SQLite locks with fcntl.
type PurgeLock struct {
	path   string
	logger zerolog.Logger
}

// NewPurgeLock creates a PurgeLock for the instance store's SQLite file at storePath; the lock file is
// storePath + ".purge-lock", created on first use.
func NewPurgeLock(storePath string, logger zerolog.Logger) *PurgeLock {
	return &PurgeLock{path: storePath + ".purge-lock", logger: logger}
}

// TryAcquire takes the sweep lock without blocking. When acquired, the returned release unlocks and closes the lock
// file. When another holder has it, acquired is false and release is nil.
func (l *PurgeLock) TryAcquire(_ context.Context) (bool, func(context.Context), error) {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, nil, fmt.Errorf("opening purge lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if cerr := f.Close(); cerr != nil {
			l.logger.Warn().Err(cerr).Msg("purge: closing the lock file after a failed lock")
		}
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("taking purge file lock: %w", err)
	}

	release := func(context.Context) {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
			l.logger.Error().Err(err).Msg("purge: releasing the file lock failed; closing the file releases it")
		}
		if err := f.Close(); err != nil {
			l.logger.Warn().Err(err).Msg("purge: closing the lock file")
		}
	}
	return true, release, nil
}
