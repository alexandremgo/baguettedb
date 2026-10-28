// Package postgres implements the bdbs app's repositories on the Postgres instance store, and the advisory lock that
// keeps the purge sweep to one replica at a time. Bdb and invite repositories are pool-bound; member changes and invite
// acceptance go through MembershipTxRepo, bound to the caller's transaction.
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
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

const uniqueViolationCode = "23505"

// topOfListDisplayOrder computes the display_order that places a new membership at the top of that user's
// list, one below their current minimum. Every INSERT using it must bind the member's user_id as $2.
// Two concurrent inserts for the same user can read the same minimum and tie; the list stays deterministic
// because ListByUser breaks ties on the bdb's creation time.
const topOfListDisplayOrder = `COALESCE((SELECT MIN(display_order) FROM bdb_members WHERE user_id = $2), 0) - 1`

// BdbRepo implements the bdb repository using PostgreSQL. It is pool-bound; the multi-statement operations
// (CreateOwned, Update) open and own their transaction internally.
type BdbRepo struct {
	pool *pgxpool.Pool
}

// NewBdbRepo creates a BdbRepo backed by the given connection pool.
func NewBdbRepo(pool *pgxpool.Pool) *BdbRepo {
	return &BdbRepo{pool: pool}
}

// ListOwnedIDsByUser returns the IDs of every bdb the given user owns. Reads bdb_members directly:
// ownership rows survive a soft delete (only the purge's hard delete cascades them away), so
// soft-deleted bdbs are included by construction.
func (r *BdbRepo) ListOwnedIDsByUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT bdb_id FROM bdb_members WHERE user_id = $1 AND role = 'owner'`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing bdb ids owned by user: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning owned bdb id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateOwned persists a new bdb and all its members in one transaction, enforcing the owner's bdb cap and their
// bdb names. A per-owner advisory lock serializes concurrent creations and renames by the same owner; under it, when
// maxOwned > 0, the owner's current count is checked — domain.BdbLimitReachedError is returned at or above the cap,
// maxOwned = 0 skipping the guard — and a name another of the owner's bdbs has, whatever the case, is a
// domain.ErrBdbNameTaken.
func (r *BdbRepo) CreateOwned(ctx context.Context, bdb *domain.Bdb, maxOwned int) error {
	owner := bdb.Owner()
	if owner == nil {
		return fmt.Errorf("bdb %s has no owner", bdb.ID)
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := lockOwner(ctx, tx, owner.UserID); err != nil {
			return err
		}
		if err := refuseTakenName(ctx, tx, owner.UserID, bdb); err != nil {
			return err
		}
		if maxOwned > 0 {
			var count int
			if err := tx.QueryRow(ctx,
				`SELECT COUNT(*) FROM bdb_members WHERE user_id = $1 AND role = 'owner'`, owner.UserID,
			).Scan(&count); err != nil {
				return fmt.Errorf("counting bdbs owned by user: %w", err)
			}
			if domain.CapReached(count, maxOwned) {
				return domain.BdbLimitReachedError{Max: maxOwned}
			}
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO bdbs (id, name, description, starter_key, starter_version, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			bdb.ID, bdb.Name, bdb.Description, bdb.StarterKey, bdb.StarterVersion, bdb.CreatedAt, bdb.UpdatedAt,
		); err != nil {
			return fmt.Errorf("inserting bdb: %w", err)
		}
		for _, m := range bdb.Members {
			if _, err := tx.Exec(ctx,
				`INSERT INTO bdb_members (bdb_id, user_id, role, created_at, updated_at, display_order)
				 VALUES ($1, $2, $3, $4, $5, `+topOfListDisplayOrder+`)`,
				bdb.ID, m.UserID, m.Role, m.CreatedAt, m.UpdatedAt,
			); err != nil {
				return fmt.Errorf("inserting member: %w", err)
			}
		}
		return nil
	})
}

// FindByID returns the bdb with the given ID, including all its members.
// Soft-deleted bdbs are returned with DeletedAt populated; visibility is
// not filtered here.
// Returns domain.ErrBdbNotFound if not found.
func (r *BdbRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Bdb, error) {
	return findBdb(ctx, r.pool, id, "")
}

// findBdb returns the bdb with id and its members, read through q; suffix ends the bdb's SELECT (e.g. " FOR
// UPDATE"). Returns domain.ErrBdbNotFound if not found.
func findBdb(ctx context.Context, q database.DBTX, id uuid.UUID, suffix string) (*domain.Bdb, error) {
	var b domain.Bdb
	err := q.QueryRow(ctx,
		`SELECT id, name, description, starter_key, starter_version, created_at, updated_at, deleted_at
		 FROM bdbs WHERE id = $1`+suffix, id,
	).Scan(&b.ID, &b.Name, &b.Description, &b.StarterKey, &b.StarterVersion, &b.CreatedAt, &b.UpdatedAt, &b.DeletedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrBdbNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("finding bdb: %w", err)
	}

	members, err := loadMembers(ctx, q, id)
	if err != nil {
		return nil, err
	}
	b.Members = members

	return &b, nil
}

// FindMember returns the member for the given bdb and user.
// Returns domain.ErrMemberNotFound if not found.
func (r *BdbRepo) FindMember(ctx context.Context, bdbID, userID uuid.UUID) (*domain.Member, error) {
	var m domain.Member
	err := r.pool.QueryRow(ctx,
		`SELECT bdb_id, user_id, role, created_at, updated_at
		 FROM bdb_members WHERE bdb_id = $1 AND user_id = $2`,
		bdbID, userID,
	).Scan(&m.BdbID, &m.UserID, &m.Role, &m.CreatedAt, &m.UpdatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("finding member: %w", err)
	}
	return &m, nil
}

// Update saves the bdb's name and description in one transaction it opens itself; its members are left as stored. A
// name another of the owner's bdbs has, whatever the case, is a domain.ErrBdbNameTaken, checked under the owner's lock
// CreateOwned takes when the name changes.
func (r *BdbRepo) Update(ctx context.Context, bdb *domain.Bdb) error {
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := r.refuseRenameToTakenName(ctx, tx, bdb); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE bdbs SET name = $1, description = $2, updated_at = NOW() WHERE id = $3`,
			bdb.Name, bdb.Description, bdb.ID,
		)
		if err != nil {
			return fmt.Errorf("updating bdb: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrBdbNotFound
		}

		return nil
	})
}

// refuseRenameToTakenName returns domain.ErrBdbNameTaken when bdb takes a new name another bdb of its owner has, under
// the owner's lock. A bdb keeping its name, its case aside, is not checked, so saving the members of a bdb whose name
// an older one shares still works.
func (r *BdbRepo) refuseRenameToTakenName(ctx context.Context, tx pgx.Tx, bdb *domain.Bdb) error {
	owner := bdb.Owner()
	if owner == nil {
		return nil
	}
	var stored string
	if err := tx.QueryRow(ctx, `SELECT name FROM bdbs WHERE id = $1`, bdb.ID).Scan(&stored); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrBdbNotFound
		}
		return fmt.Errorf("reading the bdb's name: %w", err)
	}
	if domain.SameBdbName(stored, bdb.Name) {
		return nil
	}
	if err := lockOwner(ctx, tx, owner.UserID); err != nil {
		return err
	}
	return refuseTakenName(ctx, tx, owner.UserID, bdb)
}

// lockOwner takes the per-owner advisory lock that serializes the creations and renames of ownerID's bdbs. It is
// transaction-scoped (_xact_): Postgres releases it on commit or rollback, so it cannot leak.
func lockOwner(ctx context.Context, tx pgx.Tx, ownerID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('bdb-create:' || $1::text))`, ownerID)
	if err != nil {
		return fmt.Errorf("locking owner for bdb create: %w", err)
	}
	return nil
}

// refuseTakenName returns domain.ErrBdbNameTaken when another bdb ownerID owns, not deleted, has bdb's name, whatever
// the case, compared as domain.SameBdbName does, like the SQLite store.
func refuseTakenName(ctx context.Context, tx pgx.Tx, ownerID uuid.UUID, bdb *domain.Bdb) error {
	rows, err := tx.Query(ctx,
		`SELECT b.id, b.name FROM bdbs b JOIN bdb_members m ON m.bdb_id = b.id
		 WHERE m.user_id = $1 AND m.role = 'owner' AND b.deleted_at IS NULL`, ownerID,
	)
	if err != nil {
		return fmt.Errorf("reading the owner's bdb names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return fmt.Errorf("reading the owner's bdb names: %w", err)
		}
		if id != bdb.ID && domain.SameBdbName(name, bdb.Name) {
			return domain.ErrBdbNameTaken
		}
	}
	return rows.Err()
}

// SoftDelete marks an active bdb as deleted by setting deleted_at.
// Returns domain.ErrBdbNotFound if no active (non-deleted) row matches the id.
func (r *BdbRepo) SoftDelete(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE bdbs SET deleted_at = $2, updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`,
		id, at,
	)
	if err != nil {
		return fmt.Errorf("soft-deleting bdb: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrBdbNotFound
	}
	return nil
}

// Restore clears deleted_at on a soft-deleted bdb.
// Returns domain.ErrBdbNotFound if no soft-deleted row matches the id.
func (r *BdbRepo) Restore(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE bdbs SET deleted_at = NULL, updated_at = NOW() WHERE id = $1 AND deleted_at IS NOT NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("restoring bdb: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrBdbNotFound
	}
	return nil
}

// HardDelete removes the bdb row. Members are cascade-deleted by the database.
// Returns domain.ErrBdbNotFound if not found.
func (r *BdbRepo) HardDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM bdbs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deleting bdb: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrBdbNotFound
	}
	return nil
}

// ListPurgeable returns the ids of soft-deleted bdbs whose deleted_at is
// strictly before cutoff.
func (r *BdbRepo) ListPurgeable(ctx context.Context, cutoff time.Time) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id FROM bdbs WHERE deleted_at IS NOT NULL AND deleted_at < $1`,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("listing purgeable bdbs: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning purgeable id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CountActive returns the number of bdbs whose deleted_at is unset.
func (r *BdbRepo) CountActive(ctx context.Context) (int64, error) {
	var n int64
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM bdbs WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting active bdbs: %w", err)
	}
	return n, nil
}

// ListByUser returns all bdbs the given user belongs to, including their members, in the order that user
// arranged them. Soft-deleted bdbs are returned with DeletedAt populated; visibility is not filtered here.
// The bdb's own creation time only breaks ties between memberships that somehow share a position.
func (r *BdbRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Bdb, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT c.id, c.name, c.description, c.starter_key, c.starter_version, c.created_at, c.updated_at, c.deleted_at
		 FROM bdbs c
		 INNER JOIN bdb_members cm ON c.id = cm.bdb_id
		 WHERE cm.user_id = $1
		 ORDER BY cm.display_order, c.created_at DESC`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing bdbs by user: %w", err)
	}
	defer rows.Close()

	var bdbs []*domain.Bdb
	for rows.Next() {
		var b domain.Bdb
		if err := rows.Scan(&b.ID, &b.Name, &b.Description, &b.StarterKey, &b.StarterVersion,
			&b.CreatedAt, &b.UpdatedAt, &b.DeletedAt); err != nil {
			return nil, fmt.Errorf("scanning bdb: %w", err)
		}
		bdbs = append(bdbs, &b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, b := range bdbs {
		members, err := loadMembers(ctx, r.pool, b.ID)
		if err != nil {
			return nil, err
		}
		b.Members = members
	}

	return bdbs, nil
}

// ReorderForUser renumbers the given user's memberships so their bdbs sit in the order the ids arrive in,
// leaving every other user's arrangement of the same bdbs untouched. Ids the user is not a member of match
// no row and are ignored, so callers that care must validate the set first.
func (r *BdbRepo) ReorderForUser(ctx context.Context, userID uuid.UUID, orderedBdbIDs []uuid.UUID) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE bdb_members m
		 SET display_order = ordered.position - 1, updated_at = NOW()
		 FROM unnest($2::uuid[]) WITH ORDINALITY AS ordered(bdb_id, position)
		 WHERE m.user_id = $1 AND m.bdb_id = ordered.bdb_id`,
		userID, orderedBdbIDs,
	); err != nil {
		return fmt.Errorf("reordering bdbs for user: %w", err)
	}
	return nil
}

// loadMembers loads all members for the given bdb through q, which is either the pool or an open transaction.
func loadMembers(ctx context.Context, q database.DBTX, bdbID uuid.UUID) ([]domain.Member, error) {
	rows, err := q.Query(ctx,
		`SELECT bdb_id, user_id, role, created_at, updated_at
		 FROM bdb_members WHERE bdb_id = $1
		 ORDER BY created_at ASC`, bdbID,
	)
	if err != nil {
		return nil, fmt.Errorf("loading members: %w", err)
	}
	defer rows.Close()

	var members []domain.Member
	for rows.Next() {
		var m domain.Member
		if err := rows.Scan(&m.BdbID, &m.UserID, &m.Role, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}
