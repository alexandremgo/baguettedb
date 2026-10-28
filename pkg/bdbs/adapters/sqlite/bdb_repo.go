// Package sqlite implements the bdbs app's repositories on the SQLite instance store.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// topOfListDisplayOrder computes the display_order that places a new membership at the top of that user's
// list, one below their current minimum. Every INSERT using it must bind the member's user_id as $2.
const topOfListDisplayOrder = `COALESCE((SELECT MIN(display_order) FROM bdb_members WHERE user_id = $2), 0) - 1`

// BdbRepo implements the bdb repository using SQLite. The multi-statement operations (CreateOwned, Update,
// ReorderForUser) open and own their transaction internally; every transaction takes the write lock when it
// begins, so concurrent ones run one after the other.
type BdbRepo struct {
	db *sql.DB
}

// NewBdbRepo creates a BdbRepo backed by the given handle.
func NewBdbRepo(db *sql.DB) *BdbRepo {
	return &BdbRepo{db: db}
}

// ListOwnedIDsByUser returns the IDs of every bdb the given user owns. Reads bdb_members directly:
// ownership rows survive a soft delete (only the purge's hard delete cascades them away), so
// soft-deleted bdbs are included by construction.
func (r *BdbRepo) ListOwnedIDsByUser(ctx context.Context, userID uuid.UUID) (_ []uuid.UUID, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT bdb_id FROM bdb_members WHERE user_id = $1 AND role = 'owner'`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing bdb ids owned by user: %w", err)
	}
	defer database.CloseRows(rows, &err)

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
// bdb names. When maxOwned > 0, the owner's current count is checked inside the transaction — which holds the write
// lock from its start, so no concurrent creation can slip between the count and the insert — and
// domain.BdbLimitReachedError is returned at or above the cap. maxOwned = 0 skips the guard. A name another of the
// owner's bdbs has, whatever the case, is a domain.ErrBdbNameTaken.
func (r *BdbRepo) CreateOwned(ctx context.Context, bdb *domain.Bdb, maxOwned int) error {
	owner := bdb.Owner()
	if owner == nil {
		return fmt.Errorf("bdb %s has no owner", bdb.ID)
	}
	return database.InTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := refuseTakenName(ctx, tx, owner.UserID, bdb); err != nil {
			return err
		}
		if maxOwned > 0 {
			var count int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM bdb_members WHERE user_id = $1 AND role = 'owner'`, owner.UserID,
			).Scan(&count); err != nil {
				return fmt.Errorf("counting bdbs owned by user: %w", err)
			}
			if domain.CapReached(count, maxOwned) {
				return domain.BdbLimitReachedError{Max: maxOwned}
			}
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO bdbs (id, name, description, starter_key, starter_version, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			bdb.ID, bdb.Name, bdb.Description, bdb.StarterKey, bdb.StarterVersion, bdb.CreatedAt, bdb.UpdatedAt,
		); err != nil {
			return fmt.Errorf("inserting bdb: %w", err)
		}
		for _, m := range bdb.Members {
			if _, err := tx.ExecContext(ctx,
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

// refuseRenameToTakenName returns domain.ErrBdbNameTaken when bdb takes a new name another bdb of its owner has. A
// bdb keeping its name, its case aside, is not checked, so saving the members of a bdb whose name an older one shares
// still works.
func refuseRenameToTakenName(ctx context.Context, tx *sql.Tx, bdb *domain.Bdb) error {
	owner := bdb.Owner()
	if owner == nil {
		return nil
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM bdbs WHERE id = $1`, bdb.ID).Scan(&stored); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrBdbNotFound
		}
		return fmt.Errorf("reading the bdb's name: %w", err)
	}
	if domain.SameBdbName(stored, bdb.Name) {
		return nil
	}
	return refuseTakenName(ctx, tx, owner.UserID, bdb)
}

// refuseTakenName returns domain.ErrBdbNameTaken when another bdb ownerID owns, not deleted, has bdb's name, whatever
// the case. The names are compared here rather than in SQL, whose lower() folds ASCII alone in SQLite.
func refuseTakenName(ctx context.Context, tx *sql.Tx, ownerID uuid.UUID, bdb *domain.Bdb) (err error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT b.id, b.name FROM bdbs b JOIN bdb_members m ON m.bdb_id = b.id
		 WHERE m.user_id = $1 AND m.role = 'owner' AND b.deleted_at IS NULL`, ownerID,
	)
	if err != nil {
		return fmt.Errorf("reading the owner's bdb names: %w", err)
	}
	defer database.CloseRows(rows, &err)
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

// FindByID returns the bdb with the given ID, including all its members.
// Soft-deleted bdbs are returned with DeletedAt populated; visibility is
// not filtered here.
// Returns domain.ErrBdbNotFound if not found.
func (r *BdbRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Bdb, error) {
	return findBdb(ctx, r.db, id)
}

// findBdb returns the bdb with id and its members, read through q. Returns domain.ErrBdbNotFound if not found.
func findBdb(ctx context.Context, q database.SQLiteDBTX, id uuid.UUID) (*domain.Bdb, error) {
	var b domain.Bdb
	err := q.QueryRowContext(ctx,
		`SELECT id, name, description, starter_key, starter_version, created_at, updated_at, deleted_at
		 FROM bdbs WHERE id = $1`, id,
	).Scan(&b.ID, &b.Name, &b.Description, &b.StarterKey, &b.StarterVersion, &b.CreatedAt, &b.UpdatedAt, &b.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
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
	err := r.db.QueryRowContext(ctx,
		`SELECT bdb_id, user_id, role, created_at, updated_at
		 FROM bdb_members WHERE bdb_id = $1 AND user_id = $2`,
		bdbID, userID,
	).Scan(&m.BdbID, &m.UserID, &m.Role, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrMemberNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("finding member: %w", err)
	}
	return &m, nil
}

// Update saves the bdb's name and description in one transaction it opens itself; its members are left as stored. A
// new name another of the owner's bdbs has, whatever the case, is a domain.ErrBdbNameTaken.
func (r *BdbRepo) Update(ctx context.Context, bdb *domain.Bdb) error {
	return database.InTx(ctx, r.db, func(tx *sql.Tx) error {
		if err := refuseRenameToTakenName(ctx, tx, bdb); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE bdbs SET name = $1, description = $2, updated_at = $3 WHERE id = $4`,
			bdb.Name, bdb.Description, time.Now(), bdb.ID,
		)
		if err := database.RequireRow(res, err, "updating bdb", domain.ErrBdbNotFound); err != nil {
			return err
		}
		return nil
	})
}

// SoftDelete marks an active bdb as deleted by setting deleted_at.
// Returns domain.ErrBdbNotFound if no active (non-deleted) row matches the id.
func (r *BdbRepo) SoftDelete(ctx context.Context, id uuid.UUID, at time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE bdbs SET deleted_at = $2, updated_at = $3 WHERE id = $1 AND deleted_at IS NULL`,
		id, at, time.Now(),
	)
	return database.RequireRow(res, err, "soft-deleting bdb", domain.ErrBdbNotFound)
}

// Restore clears deleted_at on a soft-deleted bdb.
// Returns domain.ErrBdbNotFound if no soft-deleted row matches the id.
func (r *BdbRepo) Restore(ctx context.Context, id uuid.UUID) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE bdbs SET deleted_at = NULL, updated_at = $2 WHERE id = $1 AND deleted_at IS NOT NULL`,
		id, time.Now(),
	)
	return database.RequireRow(res, err, "restoring bdb", domain.ErrBdbNotFound)
}

// HardDelete removes the bdb row. Members and invites are cascade-deleted by the database.
// Returns domain.ErrBdbNotFound if not found.
func (r *BdbRepo) HardDelete(ctx context.Context, id uuid.UUID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM bdbs WHERE id = $1`, id)
	return database.RequireRow(res, err, "deleting bdb", domain.ErrBdbNotFound)
}

// ListPurgeable returns the ids of soft-deleted bdbs whose deleted_at is
// strictly before cutoff.
func (r *BdbRepo) ListPurgeable(ctx context.Context, cutoff time.Time) (_ []uuid.UUID, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM bdbs WHERE deleted_at IS NOT NULL AND deleted_at < $1`,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("listing purgeable bdbs: %w", err)
	}
	defer database.CloseRows(rows, &err)

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
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bdbs WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting active bdbs: %w", err)
	}
	return n, nil
}

// ListByUser returns all bdbs the given user belongs to, including their members, in the order that user
// arranged them. Soft-deleted bdbs are returned with DeletedAt populated; visibility is not filtered here.
// The bdb's own creation time only breaks ties between memberships that somehow share a position.
func (r *BdbRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Bdb, error) {
	bdbs, err := r.listBdbsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, b := range bdbs {
		members, err := loadMembers(ctx, r.db, b.ID)
		if err != nil {
			return nil, err
		}
		b.Members = members
	}
	return bdbs, nil
}

// listBdbsByUser returns the bdbs of ListByUser without their members, closing its rows before members load.
func (r *BdbRepo) listBdbsByUser(ctx context.Context, userID uuid.UUID) (_ []*domain.Bdb, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT c.id, c.name, c.description, c.starter_key, c.starter_version, c.created_at, c.updated_at,
		        c.deleted_at
		 FROM bdbs c
		 INNER JOIN bdb_members cm ON c.id = cm.bdb_id
		 WHERE cm.user_id = $1
		 ORDER BY cm.display_order, c.created_at DESC`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing bdbs by user: %w", err)
	}
	defer database.CloseRows(rows, &err)

	var bdbs []*domain.Bdb
	for rows.Next() {
		var b domain.Bdb
		if err := rows.Scan(&b.ID, &b.Name, &b.Description, &b.StarterKey, &b.StarterVersion,
			&b.CreatedAt, &b.UpdatedAt, &b.DeletedAt); err != nil {
			return nil, fmt.Errorf("scanning bdb: %w", err)
		}
		bdbs = append(bdbs, &b)
	}
	return bdbs, rows.Err()
}

// ReorderForUser renumbers the given user's memberships so their bdbs sit in the order the ids arrive in,
// leaving every other user's arrangement of the same bdbs untouched. Ids the user is not a member of match
// no row and are ignored, so callers that care must validate the set first.
func (r *BdbRepo) ReorderForUser(ctx context.Context, userID uuid.UUID, orderedBdbIDs []uuid.UUID) error {
	now := time.Now()
	return database.InTx(ctx, r.db, func(tx *sql.Tx) error {
		for position, bdbID := range orderedBdbIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE bdb_members SET display_order = $1, updated_at = $2 WHERE user_id = $3 AND bdb_id = $4`,
				position, now, userID, bdbID,
			); err != nil {
				return fmt.Errorf("reordering bdbs for user: %w", err)
			}
		}
		return nil
	})
}

// loadMembers loads all members for the given bdb through q, which is either the handle or an open transaction.
func loadMembers(ctx context.Context, q database.SQLiteDBTX, bdbID uuid.UUID) (_ []domain.Member, err error) {
	rows, err := q.QueryContext(ctx,
		`SELECT bdb_id, user_id, role, created_at, updated_at
		 FROM bdb_members WHERE bdb_id = $1
		 ORDER BY created_at ASC`, bdbID,
	)
	if err != nil {
		return nil, fmt.Errorf("loading members: %w", err)
	}
	defer database.CloseRows(rows, &err)

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
