package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// InviteRepo implements the invite repository using SQLite.
type InviteRepo struct {
	db *sql.DB
}

// NewInviteRepo creates an InviteRepo backed by the given handle.
func NewInviteRepo(db *sql.DB) *InviteRepo {
	return &InviteRepo{db: db}
}

// inviteColumns lists the columns scanInvite reads, in its order.
const inviteColumns = `id, bdb_id, token, role, status, created_by, expires_at, accepted_by, accepted_at, created_at`

// Create persists a new invite.
func (r *InviteRepo) Create(ctx context.Context, invite *domain.Invite) error {
	_, err := r.db.ExecContext(ctx,
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
	return findInvite(r.db.QueryRowContext(ctx,
		`SELECT `+inviteColumns+` FROM bdb_invites WHERE token = $1`, token))
}

// FindByID returns the invite with the given ID.
// Returns domain.ErrInviteNotFound if not found.
func (r *InviteRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Invite, error) {
	return findInvite(r.db.QueryRowContext(ctx,
		`SELECT `+inviteColumns+` FROM bdb_invites WHERE id = $1`, id))
}

// Update updates the invite's mutable fields (status, accepted_by, accepted_at).
func (r *InviteRepo) Update(ctx context.Context, invite *domain.Invite) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE bdb_invites SET status = $1, accepted_by = $2, accepted_at = $3 WHERE id = $4`,
		invite.Status, nullableUUID(invite.AcceptedBy), nullableTime(invite.AcceptedAt), invite.ID,
	)
	return database.RequireRow(res, err, "updating invite", domain.ErrInviteNotFound)
}

// ListByBdb returns all invites for the given bdb, ordered by creation time descending.
func (r *InviteRepo) ListByBdb(ctx context.Context, bdbID uuid.UUID) (_ []*domain.Invite, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+inviteColumns+` FROM bdb_invites WHERE bdb_id = $1 ORDER BY created_at DESC`, bdbID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing invites: %w", err)
	}
	defer database.CloseRows(rows, &err)

	var invites []*domain.Invite
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning invite row: %w", err)
		}
		invites = append(invites, inv)
	}
	return invites, rows.Err()
}

// findInvite scans the single invite row, returning domain.ErrInviteNotFound when there is none.
func findInvite(row *sql.Row) (*domain.Invite, error) {
	inv, err := scanInvite(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrInviteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning invite: %w", err)
	}
	return inv, nil
}

// scanInvite scans one invite, in inviteColumns order, from a row or a rows iterator, mapping NULL acceptance
// fields to their zero values.
func scanInvite(row interface{ Scan(dest ...any) error }) (*domain.Invite, error) {
	var inv domain.Invite
	var role, status string
	var acceptedBy *uuid.UUID
	var acceptedAt *time.Time

	if err := row.Scan(
		&inv.ID, &inv.BdbID, &inv.Token, &role, &status,
		&inv.CreatedBy, &inv.ExpiresAt, &acceptedBy, &acceptedAt, &inv.CreatedAt,
	); err != nil {
		return nil, err
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
