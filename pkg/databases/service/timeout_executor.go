package service

import (
	"context"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// BoundedExecutor wraps a queryExecutor and enforces a process-wide ceiling on
// every Execute / ExecuteTransaction call. Defense in depth: the adapter
// binding already honours upstream ctx cancellation, but background callers
// that pass context.Background() would otherwise have no upper bound.
// Wrapping at the service layer makes the guarantee adapter-agnostic.
type BoundedExecutor struct {
	inner queryExecutor
	max   time.Duration
}

// NewBoundedExecutor wraps inner so each call is capped at timeout. A non-positive timeout leaves every call uncapped,
// for callers opting out (tests, the migration tool).
func NewBoundedExecutor(inner queryExecutor, timeout time.Duration) *BoundedExecutor {
	return &BoundedExecutor{inner: inner, max: timeout}
}

// bound returns ctx capped at e.max, or ctx itself when e.max is not positive, with the cancel func to defer.
func (e *BoundedExecutor) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.max <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, e.max)
}

// Execute caps the call at e.max and forwards to the inner executor.
func (e *BoundedExecutor) Execute(
	ctx context.Context, databaseID, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	ctx, cancel := e.bound(ctx)
	defer cancel()
	return e.inner.Execute(ctx, databaseID, sql, args)
}

// ExecuteWithRowCap caps the call at e.max and forwards to the inner executor.
func (e *BoundedExecutor) ExecuteWithRowCap(
	ctx context.Context, databaseID, sql string, args []domain.TypedValue, maxRows int,
) (*domain.QueryResult, error) {
	ctx, cancel := e.bound(ctx)
	defer cancel()
	return e.inner.ExecuteWithRowCap(ctx, databaseID, sql, args, maxRows)
}

// ExecuteInternal caps the call at e.max and forwards to the inner executor.
func (e *BoundedExecutor) ExecuteInternal(
	ctx context.Context, databaseID, sql string, args []domain.TypedValue,
) (*domain.QueryResult, error) {
	ctx, cancel := e.bound(ctx)
	defer cancel()
	return e.inner.ExecuteInternal(ctx, databaseID, sql, args)
}

// ExecuteTransaction caps the call at e.max and forwards to the inner executor.
func (e *BoundedExecutor) ExecuteTransaction(
	ctx context.Context, databaseID string, stmts []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	ctx, cancel := e.bound(ctx)
	defer cancel()
	return e.inner.ExecuteTransaction(ctx, databaseID, stmts)
}

// ExecuteTransactionInternal caps the call at e.max and forwards to the inner executor.
func (e *BoundedExecutor) ExecuteTransactionInternal(
	ctx context.Context, databaseID string, stmts []domain.SQLStatement,
) ([]*domain.QueryResult, error) {
	ctx, cancel := e.bound(ctx)
	defer cancel()
	return e.inner.ExecuteTransactionInternal(ctx, databaseID, stmts)
}

// WithConn caps the call at e.max and forwards to the inner executor.
func (e *BoundedExecutor) WithConn(ctx context.Context, databaseID string, fn func(h ParseHandle) error) error {
	ctx, cancel := e.bound(ctx)
	defer cancel()
	return e.inner.WithConn(ctx, databaseID, fn)
}

// WithTx caps the entire transaction at e.max and forwards to the
// inner executor. The bounded ctx is the one passed into fn so
// tx.Exec calls inherit the same deadline.
func (e *BoundedExecutor) WithTx(
	ctx context.Context, databaseID string, internal bool, fn func(ctx context.Context, tx Tx) error,
) error {
	ctx, cancel := e.bound(ctx)
	defer cancel()
	return e.inner.WithTx(ctx, databaseID, internal, fn)
}
