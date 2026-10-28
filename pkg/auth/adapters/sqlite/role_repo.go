package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// RoleRepo stores the global roles assigned to users in SQLite.
type RoleRepo struct {
	db database.SQLiteDBTX
}

// NewRoleRepo creates a new RoleRepo backed by the given handle or transaction.
func NewRoleRepo(db database.SQLiteDBTX) *RoleRepo {
	return &RoleRepo{db: db}
}

// AssignRole grants the given role to a user. The operation is idempotent:
// assigning an already-assigned role is a no-op.
func (r *RoleRepo) AssignRole(ctx context.Context, userID uuid.UUID, role authz.Role) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO user_roles (user_id, role, created_at)
		 VALUES ($1, $2, $3)
		 ON CONFLICT DO NOTHING`,
		userID, string(role), time.Now(),
	)
	if err != nil {
		return fmt.Errorf("assigning role: %w", err)
	}
	return nil
}

// RemoveRole revokes the given role from a user.
func (r *RoleRepo) RemoveRole(ctx context.Context, userID uuid.UUID, role authz.Role) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM user_roles WHERE user_id = $1 AND role = $2`,
		userID, string(role),
	)
	if err != nil {
		return fmt.Errorf("removing role: %w", err)
	}
	return nil
}

// GetRoles returns all roles assigned to the given user.
func (r *RoleRepo) GetRoles(ctx context.Context, userID uuid.UUID) (_ []authz.Role, err error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT role FROM user_roles WHERE user_id = $1`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("getting roles: %w", err)
	}
	defer database.CloseRows(rows, &err)

	var roles []authz.Role
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, fmt.Errorf("scanning role: %w", err)
		}
		roles = append(roles, authz.Role(role))
	}
	return roles, rows.Err()
}
