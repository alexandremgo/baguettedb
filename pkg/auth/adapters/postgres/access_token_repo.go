package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// accessTokenColumns lists the columns scanAccessToken reads, in its order.
const accessTokenColumns = `id, user_id, kind, name, token_prefix, created_at, expires_at, last_used_at, revoked_at`

// AccessTokenRepo persists access tokens in Postgres.
type AccessTokenRepo struct {
	db database.DBTX
}

// NewAccessTokenRepo creates an AccessTokenRepo backed by the given DBTX (pool or transaction).
func NewAccessTokenRepo(db database.DBTX) *AccessTokenRepo {
	return &AccessTokenRepo{db: db}
}

// Create persists token, whose plain value hashes to hash.
func (r *AccessTokenRepo) Create(ctx context.Context, token *domain.AccessToken, hash string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO access_tokens (id, user_id, kind, name, token_prefix, token_hash, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		token.ID, token.UserID, string(token.Kind), token.Name, token.TokenPrefix, hash, token.CreatedAt,
		token.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("inserting access token: %w", err)
	}
	return nil
}

// FindByHash returns the token whose plain value hashes to hash. Returns domain.ErrAccessTokenNotFound when none does.
func (r *AccessTokenRepo) FindByHash(ctx context.Context, hash string) (*domain.AccessToken, error) {
	token, err := scanAccessToken(r.db.QueryRow(ctx,
		`SELECT `+accessTokenColumns+` FROM access_tokens WHERE token_hash = $1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAccessTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("finding access token: %w", err)
	}
	return token, nil
}

// FindWithUserByHash returns, in one query, the token whose plain value hashes to hash with whether its user is active
// and the user's roles. Returns domain.ErrAccessTokenNotFound when none does.
func (r *AccessTokenRepo) FindWithUserByHash(ctx context.Context, hash string) (*domain.AccessTokenWithUser, error) {
	var t domain.AccessToken
	var kind string
	var found domain.AccessTokenWithUser
	var roles []string
	err := r.db.QueryRow(ctx,
		`SELECT t.id, t.user_id, t.kind, t.name, t.token_prefix, t.created_at, t.expires_at, t.last_used_at,
		        t.revoked_at, u.is_active,
		        COALESCE(array_agg(ur.role ORDER BY ur.role) FILTER (WHERE ur.role IS NOT NULL), '{}')
		 FROM access_tokens t
		 JOIN users u ON u.id = t.user_id
		 LEFT JOIN user_roles ur ON ur.user_id = t.user_id
		 WHERE t.token_hash = $1
		 GROUP BY t.id, u.id`,
		hash,
	).Scan(&t.ID, &t.UserID, &kind, &t.Name, &t.TokenPrefix, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt,
		&found.UserActive, &roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAccessTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("finding access token with its user: %w", err)
	}
	t.Kind = domain.AccessTokenKind(kind)
	found.Token = &t
	for _, role := range roles {
		found.Roles = append(found.Roles, authz.Role(role))
	}
	return &found, nil
}

// ListByUser returns the user's tokens of kind that are not revoked, newest first.
func (r *AccessTokenRepo) ListByUser(
	ctx context.Context, userID uuid.UUID, kind domain.AccessTokenKind,
) ([]*domain.AccessToken, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+accessTokenColumns+` FROM access_tokens
		 WHERE user_id = $1 AND kind = $2 AND revoked_at IS NULL
		 ORDER BY created_at DESC, id`,
		userID, string(kind),
	)
	if err != nil {
		return nil, fmt.Errorf("listing access tokens: %w", err)
	}
	defer rows.Close()

	var tokens []*domain.AccessToken
	for rows.Next() {
		token, err := scanAccessToken(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning access token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing access tokens: %w", err)
	}
	return tokens, nil
}

// Revoke sets the revocation time of the user's token id to at. Returns domain.ErrAccessTokenNotFound when the user
// has no such token, or it is already revoked.
func (r *AccessTokenRepo) Revoke(ctx context.Context, userID, id uuid.UUID, at time.Time) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE access_tokens SET revoked_at = $1 WHERE id = $2 AND user_id = $3 AND revoked_at IS NULL`,
		at, id, userID,
	)
	if err != nil {
		return fmt.Errorf("revoking access token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAccessTokenNotFound
	}
	return nil
}

// RevokeSessions sets the revocation time of the user's sessions that are not revoked to at.
func (r *AccessTokenRepo) RevokeSessions(ctx context.Context, userID uuid.UUID, at time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE access_tokens SET revoked_at = $1 WHERE user_id = $2 AND kind = $3 AND revoked_at IS NULL`,
		at, userID, string(domain.AccessTokenSession),
	)
	if err != nil {
		return fmt.Errorf("revoking sessions: %w", err)
	}
	return nil
}

// TouchLastUsed sets the last use of token id to at.
func (r *AccessTokenRepo) TouchLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE access_tokens SET last_used_at = $1 WHERE id = $2`,
		at, id,
	)
	if err != nil {
		return fmt.Errorf("recording access token use: %w", err)
	}
	return nil
}

// scanAccessToken reads one row of accessTokenColumns.
func scanAccessToken(row pgx.Row) (*domain.AccessToken, error) {
	var t domain.AccessToken
	var kind string
	if err := row.Scan(&t.ID, &t.UserID, &kind, &t.Name, &t.TokenPrefix, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt,
		&t.RevokedAt); err != nil {
		return nil, err
	}
	t.Kind = domain.AccessTokenKind(kind)
	return &t, nil
}
