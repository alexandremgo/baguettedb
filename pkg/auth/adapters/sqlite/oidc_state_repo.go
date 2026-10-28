package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// OIDCStateRepo stores the state of OIDC login flows in progress in SQLite.
type OIDCStateRepo struct {
	db database.SQLiteDBTX
}

// NewOIDCStateRepo creates a new OIDCStateRepo backed by the given handle or transaction.
func NewOIDCStateRepo(db database.SQLiteDBTX) *OIDCStateRepo {
	return &OIDCStateRepo{db: db}
}

// Create persists the flow state for a freshly started sign-in round-trip.
func (r *OIDCStateRepo) Create(ctx context.Context, s *domain.OIDCFlowState) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO oidc_state (state, provider, login_challenge, nonce, code_verifier, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.State, s.Provider, s.LoginChallenge, s.Nonce, s.CodeVerifier, s.CreatedAt, s.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("inserting oidc flow state: %w", err)
	}
	return nil
}

// ConsumeByState atomically deletes and returns the flow state for the given state value. The single-statement
// DELETE … RETURNING guarantees that two concurrent callbacks carrying the same state value can never both succeed.
func (r *OIDCStateRepo) ConsumeByState(ctx context.Context, state string) (*domain.OIDCFlowState, error) {
	var s domain.OIDCFlowState
	err := r.db.QueryRowContext(ctx,
		`DELETE FROM oidc_state WHERE state = $1
		 RETURNING state, provider, login_challenge, nonce, code_verifier, created_at, expires_at`,
		state,
	).Scan(&s.State, &s.Provider, &s.LoginChallenge, &s.Nonce, &s.CodeVerifier, &s.CreatedAt, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrOIDCStateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("consuming oidc flow state: %w", err)
	}
	// An expired row is still deleted (self-cleaning) but never honored.
	if time.Now().After(s.ExpiresAt) {
		return nil, domain.ErrOIDCStateNotFound
	}
	return &s, nil
}

// DeleteExpired removes rows whose expiry is in the past and returns how many were deleted.
func (r *OIDCStateRepo) DeleteExpired(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM oidc_state WHERE expires_at <= $1`, time.Now())
	if err != nil {
		return 0, fmt.Errorf("deleting expired oidc flow state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("deleting expired oidc flow state: %w", err)
	}
	return n, nil
}
