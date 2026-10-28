package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresInTx returns a function running fn in one transaction on pool, with the repositories build makes over that
// transaction. The transaction is committed when fn returns nil and rolled back otherwise.
//
// R is what fn receives: usually a struct of repositories bound to the transaction.
//
// pool is the connection pool each transaction begins on, and its connection returns to once the transaction ends,
// including when fn panics.
//
// build makes R over the transaction; it runs once per transaction, just before fn.
//
// The returned function's fn error comes back unchanged, so callers can match it with errors.Is; a failure to begin or
// commit the transaction comes back wrapped.
func PostgresInTx[R any](pool *pgxpool.Pool, build func(pgx.Tx) R) func(context.Context, func(R) error) error {
	return func(ctx context.Context, fn func(R) error) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning transaction: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if err := fn(build(tx)); err != nil {
			return err
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing transaction: %w", err)
		}
		return nil
	}
}
