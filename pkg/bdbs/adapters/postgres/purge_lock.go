package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// purgeLockSQL / purgeUnlockSQL take and release the purge-sweep advisory lock. hashtext maps the fixed label to
// the int4 that pg_try_advisory_lock promotes to its bigint key.
const (
	purgeLockSQL   = `SELECT pg_try_advisory_lock(hashtext('bdbs:purge-sweep'))`
	purgeUnlockSQL = `SELECT pg_advisory_unlock(hashtext('bdbs:purge-sweep'))`
)

// PurgeLock takes a session-scoped Postgres advisory lock so only one replica runs the purge sweep at a time. It
// owns a pool handle because a session lock lives on a single connection that must be held for the sweep's whole
// duration, then released on that same connection.
type PurgeLock struct {
	pool   *pgxpool.Pool
	logger zerolog.Logger
}

// NewPurgeLock creates a PurgeLock backed by the given connection pool.
func NewPurgeLock(pool *pgxpool.Pool, logger zerolog.Logger) *PurgeLock {
	return &PurgeLock{pool: pool, logger: logger}
}

// TryAcquire takes the sweep lock without blocking, on a dedicated pooled connection. When acquired, the returned
// release frees the lock and returns the connection to the pool; the lock stays held on that one connection until
// then. When not acquired (another replica holds it) the connection is returned immediately and release is nil.
func (l *PurgeLock) TryAcquire(ctx context.Context) (bool, func(context.Context), error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return false, nil, fmt.Errorf("acquiring connection for purge lock: %w", err)
	}

	var acquired bool
	if err := conn.QueryRow(ctx, purgeLockSQL).Scan(&acquired); err != nil {
		conn.Release()
		return false, nil, fmt.Errorf("taking purge advisory lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return false, nil, nil
	}

	release := func(ctx context.Context) {
		// Frees the session lock on the same connection it was taken on, then returns the connection. If the
		// unlock fails the lock would otherwise linger on a pooled connection, so close that connection instead —
		// ending its session releases the lock — and let the pool discard it.
		if _, err := conn.Exec(ctx, purgeUnlockSQL); err != nil {
			l.logger.Error().Err(err).Msg("purge: releasing advisory lock failed; dropping connection")
			_ = conn.Conn().Close(ctx) // best-effort: ending the session releases the lingering lock
		}
		conn.Release()
	}
	return true, release, nil
}
