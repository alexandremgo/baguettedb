package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// ExternalIdentityRepo stores the links between users and their external identity provider accounts in SQLite.
type ExternalIdentityRepo struct {
	db database.SQLiteDBTX
}

// NewExternalIdentityRepo creates a new ExternalIdentityRepo backed by the given handle or transaction.
func NewExternalIdentityRepo(db database.SQLiteDBTX) *ExternalIdentityRepo {
	return &ExternalIdentityRepo{db: db}
}

// Create persists a new external identity link.
func (r *ExternalIdentityRepo) Create(ctx context.Context, identity *domain.ExternalIdentity) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO external_identities (id, user_id, provider, provider_subject, email, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		identity.ID, identity.UserID, identity.Provider, identity.ProviderSubject, identity.Email, identity.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting external identity: %w", err)
	}
	return nil
}

// FindByProviderSubject returns the identity for the given provider + provider_subject.
func (r *ExternalIdentityRepo) FindByProviderSubject(
	ctx context.Context, provider, providerSubject string,
) (*domain.ExternalIdentity, error) {
	var id domain.ExternalIdentity
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, provider, provider_subject, email, created_at
		 FROM external_identities WHERE provider = $1 AND provider_subject = $2`,
		provider, providerSubject,
	).Scan(&id.ID, &id.UserID, &id.Provider, &id.ProviderSubject, &id.Email, &id.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrExternalIdentityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning external identity: %w", err)
	}
	return &id, nil
}
