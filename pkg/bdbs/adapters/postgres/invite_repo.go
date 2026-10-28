package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// InviteRepo implements the invite repository using PostgreSQL. It is pool-bound.
type InviteRepo struct {
	pool *pgxpool.Pool
}

// NewInviteRepo creates an InviteRepo backed by the given connection pool.
func NewInviteRepo(pool *pgxpool.Pool) *InviteRepo {
	return &InviteRepo{pool: pool}
}

// Create persists a new invite.
func (r *InviteRepo) Create(ctx context.Context, invite *domain.Invite) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO bdb_invites (id, bdb_id, token, role, status, created_by, expires_at, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		invite.ID, invite.BdbID, invite.Token, invite.Role,
		invite.Status, invite.CreatedBy, invite.ExpiresAt, invite.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting invite: %w", err)
	}
	return nil
}

// FindByToken returns the invite with the given token.
// Returns domain.ErrInviteNotFound if not found.
func (r *InviteRepo) FindByToken(ctx context.Context, token string) (*domain.Invite, error) {
	return r.scanInvite(r.pool.QueryRow(ctx,
		`SELECT id, bdb_id, token, role, status, created_by, expires_at, accepted_by, accepted_at, created_at
		 FROM bdb_invites WHERE token = $1`, token,
	))
}

// FindByID returns the invite with the given ID.
// Returns domain.ErrInviteNotFound if not found.
func (r *InviteRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Invite, error) {
	return r.scanInvite(r.pool.QueryRow(ctx,
		`SELECT id, bdb_id, token, role, status, created_by, expires_at, accepted_by, accepted_at, created_at
		 FROM bdb_invites WHERE id = $1`, id,
	))
}

// Update updates the invite's mutable fields (status, accepted_by, accepted_at).
func (r *InviteRepo) Update(ctx context.Context, invite *domain.Invite) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE bdb_invites SET status = $1, accepted_by = $2, accepted_at = $3 WHERE id = $4`,
		invite.Status, nullableUUID(invite.AcceptedBy), nullableTime(invite.AcceptedAt), invite.ID,
	)
	if err != nil {
		return fmt.Errorf("updating invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrInviteNotFound
	}
	return nil
}

// ListByBdb returns all invites for the given bdb, ordered by creation time descending.
func (r *InviteRepo) ListByBdb(ctx context.Context, bdbID uuid.UUID) ([]*domain.Invite, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, bdb_id, token, role, status, created_by, expires_at, accepted_by, accepted_at, created_at
		 FROM bdb_invites WHERE bdb_id = $1
		 ORDER BY created_at DESC`, bdbID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing invites: %w", err)
	}
	defer rows.Close()

	var invites []*domain.Invite
	for rows.Next() {
		inv, err := r.scanInviteRow(rows)
		if err != nil {
			return nil, err
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// scanInvite scans a single invite from a query row, handling nullable fields.
func (r *InviteRepo) scanInvite(row pgx.Row) (*domain.Invite, error) {
	var inv domain.Invite
	var role string
	var status string
	var acceptedBy *uuid.UUID
	var acceptedAt *time.Time

	err := row.Scan(
		&inv.ID, &inv.BdbID, &inv.Token, &role, &status,
		&inv.CreatedBy, &inv.ExpiresAt, &acceptedBy, &acceptedAt, &inv.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrInviteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning invite: %w", err)
	}

	inv.Role = authz.BdbRole(role)
	inv.Status = domain.InviteStatus(status)
	if acceptedBy != nil {
		inv.AcceptedBy = *acceptedBy
	}
	if acceptedAt != nil {
		inv.AcceptedAt = *acceptedAt
	}

	return &inv, nil
}

// scanInviteRow scans a single invite from a rows iterator.
func (r *InviteRepo) scanInviteRow(rows pgx.Rows) (*domain.Invite, error) {
	var inv domain.Invite
	var role string
	var status string
	var acceptedBy *uuid.UUID
	var acceptedAt *time.Time

	err := rows.Scan(
		&inv.ID, &inv.BdbID, &inv.Token, &role, &status,
		&inv.CreatedBy, &inv.ExpiresAt, &acceptedBy, &acceptedAt, &inv.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scanning invite row: %w", err)
	}

	inv.Role = authz.BdbRole(role)
	inv.Status = domain.InviteStatus(status)
	if acceptedBy != nil {
		inv.AcceptedBy = *acceptedBy
	}
	if acceptedAt != nil {
		inv.AcceptedAt = *acceptedAt
	}

	return &inv, nil
}

// nullableUUID returns nil if the UUID is the zero value, otherwise returns a pointer.
func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// nullableTime returns nil if the time is zero, otherwise returns a pointer.
func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
