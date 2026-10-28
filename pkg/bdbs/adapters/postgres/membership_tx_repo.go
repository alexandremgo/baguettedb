package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
)

// MembershipTxRepo reads a bdb and changes its members and invites within the transaction it is bound to.
type MembershipTxRepo struct {
	tx pgx.Tx
}

// NewMembershipTxRepo creates a MembershipTxRepo bound to tx.
func NewMembershipTxRepo(tx pgx.Tx) *MembershipTxRepo {
	return &MembershipTxRepo{tx: tx}
}

// FindByIDForUpdate returns the bdb with id and its members, locking the bdb's row until the transaction ends, so
// member changes to the same bdb run one after the other, each on what the previous one stored. Returns
// domain.ErrBdbNotFound if not found.
func (m *MembershipTxRepo) FindByIDForUpdate(ctx context.Context, id uuid.UUID) (*domain.Bdb, error) {
	return findBdb(ctx, m.tx, id, " FOR UPDATE")
}

// ApplyMemberChanges inserts added as members of the bdb bdbID, updates the role of each of changed, and deletes
// removed. A user already a member is a domain.ErrAlreadyMember.
func (m *MembershipTxRepo) ApplyMemberChanges(
	ctx context.Context, bdbID uuid.UUID, added, changed, removed []domain.Member,
) error {
	for _, member := range added {
		_, err := m.tx.Exec(ctx,
			`INSERT INTO bdb_members (bdb_id, user_id, role, created_at, updated_at, display_order)
			 VALUES ($1, $2, $3, $4, $5, `+topOfListDisplayOrder+`)`,
			bdbID, member.UserID, member.Role, member.CreatedAt, member.UpdatedAt,
		)
		if err != nil {
			if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolationCode {
				return domain.ErrAlreadyMember
			}
			return fmt.Errorf("inserting member: %w", err)
		}
	}
	for _, member := range changed {
		if _, err := m.tx.Exec(ctx,
			`UPDATE bdb_members SET role = $1, updated_at = NOW() WHERE bdb_id = $2 AND user_id = $3`,
			member.Role, bdbID, member.UserID,
		); err != nil {
			return fmt.Errorf("updating member role: %w", err)
		}
	}
	for _, member := range removed {
		if _, err := m.tx.Exec(ctx,
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
	tag, err := m.tx.Exec(ctx,
		`UPDATE bdb_invites SET status = $1, accepted_by = $2, accepted_at = $3
		 WHERE id = $4 AND status = $5`,
		invite.Status, nullableUUID(invite.AcceptedBy), nullableTime(invite.AcceptedAt), invite.ID,
		string(domain.InviteStatusPending),
	)
	if err != nil {
		return fmt.Errorf("updating invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrInviteAlreadyUsed
	}
	return nil
}
