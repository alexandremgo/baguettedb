package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// MembershipTxRepo reads a bdb and changes its members and invites within the transaction it is bound to.
type MembershipTxRepo struct {
	tx *sql.Tx
}

// NewMembershipTxRepo creates a MembershipTxRepo bound to tx.
func NewMembershipTxRepo(tx *sql.Tx) *MembershipTxRepo {
	return &MembershipTxRepo{tx: tx}
}

// FindByIDForUpdate returns the bdb with id and its members. The transaction already holds the database's write lock
// from its start, so member changes to the same bdb run one after the other, each on what the previous one stored.
// Returns domain.ErrBdbNotFound if not found.
func (m *MembershipTxRepo) FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Bdb, error) {
	return findBdb(ctx, m.tx, id)
}

// ApplyMemberChanges inserts added as members of the bdb bdbID, updates the role of each of changed, and deletes
// removed. A user already a member is a domain.ErrAlreadyMember.
func (m *MembershipTxRepo) ApplyMemberChanges(
	ctx context.Context, bdbID uuid.UUID, added, changed, removed []domain.Member,
) error {
	for _, member := range added {
		_, err := m.tx.ExecContext(ctx,
			`INSERT INTO bdb_members (bdb_id, user_id, role, created_at, updated_at, display_order)
			 VALUES ($1, $2, $3, $4, $5, `+topOfListDisplayOrder+`)`,
			bdbID, member.UserID, member.Role, member.CreatedAt, member.UpdatedAt,
		)
		if database.IsSQLiteUniqueViolation(err) {
			return domain.ErrAlreadyMember
		}
		if err != nil {
			return fmt.Errorf("inserting member: %w", err)
		}
	}
	now := time.Now()
	for _, member := range changed {
		if _, err := m.tx.ExecContext(ctx,
			`UPDATE bdb_members SET role = $1, updated_at = $2 WHERE bdb_id = $3 AND user_id = $4`,
			member.Role, now, bdbID, member.UserID,
		); err != nil {
			return fmt.Errorf("updating member role: %w", err)
		}
	}
	for _, member := range removed {
		if _, err := m.tx.ExecContext(ctx,
			`DELETE FROM bdb_members WHERE bdb_id = $1 AND user_id = $2`, bdbID, member.UserID,
		); err != nil {
			return fmt.Errorf("deleting member: %w", err)
		}
	}
	return nil
}

// MarkInviteAccepted stores invite as accepted, provided it is still pending: a single-use token admits exactly one
// user, and an invite accepted meanwhile is a domain.ErrInviteAlreadyUsed.
func (m *MembershipTxRepo) MarkInviteAccepted(ctx context.Context, invite *domain.Invite) error {
	res, err := m.tx.ExecContext(ctx,
		`UPDATE bdb_invites SET status = $1, accepted_by = $2, accepted_at = $3
		 WHERE id = $4 AND status = $5`,
		invite.Status, nullableUUID(invite.AcceptedBy), nullableTime(invite.AcceptedAt), invite.ID,
		string(domain.InviteStatusPending),
	)
	return database.RequireRow(res, err, "updating invite", domain.ErrInviteAlreadyUsed)
}
