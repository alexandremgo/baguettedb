package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// InstanceOwnerRepo stores the account recorded as the instance owner in SQLite.
type InstanceOwnerRepo struct {
	db database.SQLiteDBTX
}

// NewInstanceOwnerRepo creates an InstanceOwnerRepo backed by the given handle or transaction.
func NewInstanceOwnerRepo(db database.SQLiteDBTX) *InstanceOwnerRepo {
	return &InstanceOwnerRepo{db: db}
}

// Find returns the owner's user ID. Returns domain.ErrInstanceOwnerNotSet when none is recorded.
func (r *InstanceOwnerRepo) Find(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRowContext(ctx, `SELECT user_id FROM instance_owner WHERE id = 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, domain.ErrInstanceOwnerNotSet
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("reading the instance owner: %w", err)
	}
	return id, nil
}

// Set records userID as the owner, in place of any other.
func (r *InstanceOwnerRepo) Set(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO instance_owner (id, user_id) VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET user_id = excluded.user_id`, userID)
	if err != nil {
		return fmt.Errorf("recording the instance owner: %w", err)
	}
	return nil
}
