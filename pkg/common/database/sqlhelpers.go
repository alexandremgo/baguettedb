package database

import (
	"context"
	"database/sql"
	"fmt"
)

// InTx runs fn in a transaction on db, committing when fn returns nil and rolling back otherwise, including when fn
// panics, so the transaction never outlives the call. fn's error comes back unchanged, so callers can match it with
// errors.Is; a failure to begin or commit comes back wrapped.
//
// On a handle from OpenSQLite the transaction takes the write lock when it begins, so concurrent ones run one after
// the other.
func InTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}
	return nil
}

// SQLiteInTx returns a function running fn in one transaction on db, as InTx does, with the repositories build makes
// over that transaction.
//
// R is what fn receives: usually a struct of repositories bound to the transaction.
//
// db is the handle each transaction begins on, and its connection returns to once the transaction ends, including
// when fn panics.
//
// build makes R over the transaction; it runs once per transaction, just before fn.
//
// The returned function's fn error comes back unchanged, so callers can match it with errors.Is; a failure to begin or
// commit the transaction comes back wrapped.
func SQLiteInTx[R any](db *sql.DB, build func(*sql.Tx) R) func(context.Context, func(R) error) error {
	return func(ctx context.Context, fn func(R) error) error {
		return InTx(ctx, db, func(tx *sql.Tx) error {
			return fn(build(tx))
		})
	}
}

// RequireRow turns the outcome of a write expected to match at least one row into an error: execErr wrapped with
// op, notFound when the statement matched no row, or nil.
func RequireRow(res sql.Result, execErr error, op string, notFound error) error {
	if execErr != nil {
		return fmt.Errorf("%s: %w", op, execErr)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if n == 0 {
		return notFound
	}
	return nil
}

// CloseRows closes rows and, when *err is still nil, reports a close failure through it. Meant to be deferred by a
// function with a named error result: defer database.CloseRows(rows, &err).
func CloseRows(rows *sql.Rows, err *error) {
	if cerr := rows.Close(); cerr != nil && *err == nil {
		*err = fmt.Errorf("closing rows: %w", cerr)
	}
}
