//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexandremgo/baguettedb/pkg/auth/adapters/postgres"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// TestPostgresInTx checks that database.PostgresInTx commits what fn wrote when fn returns nil, and rolls all of it
// back, returning fn's error unchanged, when fn fails after writing. A panicking fn still ends the transaction, so its
// connection goes back to the pool, and the panic reaches the caller.
func TestPostgresInTx(t *testing.T) {
	if testDB.Pool == nil {
		t.Skip("database.PostgresInTx needs the Postgres instance store")
	}
	ctx := context.Background()
	truncateAll(t)
	inTx := database.PostgresInTx(testDB.Pool, func(tx pgx.Tx) *postgres.UserRepo { return postgres.NewUserRepo(tx) })
	users := postgres.NewUserRepo(testDB.Pool)

	committed := newTxTestUser("committed@example.com")
	if err := inTx(ctx, func(r *postgres.UserRepo) error { return r.Create(ctx, committed) }); err != nil {
		t.Fatalf("InTx with a succeeding fn: %v", err)
	}
	if _, err := users.FindByID(ctx, committed.ID); err != nil {
		t.Errorf("user written by a succeeding fn: %v, want it committed", err)
	}

	failure := errors.New("fn failed after writing")
	rolledBack := newTxTestUser("rolled-back@example.com")
	err := inTx(ctx, func(r *postgres.UserRepo) error {
		if err := r.Create(ctx, rolledBack); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Errorf("InTx with a failing fn = %v, want fn's error", err)
	}
	if _, err := users.FindByID(ctx, rolledBack.ID); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("user written by a failing fn: %v, want domain.ErrUserNotFound (rolled back)", err)
	}

	// A pool of its own: closing a pool waits for every connection to come back, so a leaked one would hang the suite.
	pool, err := pgxpool.New(ctx, testDB.DSN)
	if err != nil {
		t.Fatalf("opening a pool for the panic case: %v", err)
	}
	panicking := database.PostgresInTx(pool, func(tx pgx.Tx) *postgres.UserRepo { return postgres.NewUserRepo(tx) })
	func() {
		defer func() {
			if recover() == nil {
				t.Error("InTx with a panicking fn returned normally, want the panic to reach the caller")
			}
		}()
		_ = panicking(ctx, func(*postgres.UserRepo) error { panic("fn panicked") })
	}()
	if held := pool.Stat().AcquiredConns(); held != 0 {
		t.Errorf("connections held after a panicking fn = %d, want 0 (the transaction ended)", held)
		return
	}
	pool.Close()
}

// newTxTestUser returns an active, passwordless user with email and a new ID.
func newTxTestUser(email string) *domain.User {
	now := time.Now().UTC()
	return &domain.User{ID: uuid.New(), Email: email, IsActive: true, CreatedAt: now, UpdatedAt: now}
}
