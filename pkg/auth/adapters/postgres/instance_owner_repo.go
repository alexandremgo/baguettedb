package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// InstanceOwnerRepo stores the account recorded as the instance owner in Postgres.
type InstanceOwnerRepo struct {
	db database.DBTX
}

// NewInstanceOwnerRepo creates an InstanceOwnerRepo backed by the given DBTX (pool or transaction).
func NewInstanceOwnerRepo(db database.DBTX) *InstanceOwnerRepo {
	return &InstanceOwnerRepo{db: db}
}

// Find returns the owner's user ID. Returns domain.ErrInstanceOwnerNotSet when none is recorded.
func (r *InstanceOwnerRepo) Find(ctx context.Context) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `SELECT user_id FROM instance_owner WHERE id = 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrInstanceOwnerNotSet
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("reading the instance owner: %w", err)
	}
	return id, nil
}

// Set records userID as the owner, in place of any other.
func (r *InstanceOwnerRepo) Set(ctx context.Context, userID uuid.UUID) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO instance_owner (id, user_id) VALUES (1, $1)
		 ON CONFLICT (id) DO UPDATE SET user_id = EXCLUDED.user_id`, userID)
	if err != nil {
		return fmt.Errorf("recording the instance owner: %w", err)
	}
	return nil
}
