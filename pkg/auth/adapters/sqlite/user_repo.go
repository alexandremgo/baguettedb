// Package sqlite implements the auth app's repositories on the SQLite instance store.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// UserRepo stores users in SQLite.
type UserRepo struct {
	db database.SQLiteDBTX
}

// NewUserRepo creates a new UserRepo backed by the given handle or transaction.
func NewUserRepo(db database.SQLiteDBTX) *UserRepo {
	return &UserRepo{db: db}
}

// Create persists a new user. Returns ErrEmailAlreadyExists if the email is taken.
func (r *UserRepo) Create(ctx context.Context, user *domain.User) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO users (
			id, email, password_hash, first_name, last_name,
			created_at, updated_at, is_active, email_verified_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		user.ID, user.Email, user.PasswordHash, user.FirstName, user.LastName,
		user.CreatedAt, user.UpdatedAt, user.IsActive, user.EmailVerifiedAt,
	)
	if database.IsSQLiteUniqueViolation(err) {
		return domain.ErrEmailAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("inserting user: %w", err)
	}
	return nil
}

// FindByEmail returns the user with the given email. Returns ErrUserNotFound if not found.
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	return scanUser(r.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, first_name, last_name, created_at, updated_at, is_active, email_verified_at
		 FROM users WHERE email = $1`, email))
}

// FindByID returns the user with the given ID. Returns ErrUserNotFound if not found.
func (r *UserRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return scanUser(r.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, first_name, last_name, created_at, updated_at, is_active, email_verified_at
		 FROM users WHERE id = $1`, id))
}

// FindByIDs returns the non-credential profile of each user matching the given IDs.
func (r *UserRepo) FindByIDs(ctx context.Context, ids []uuid.UUID) (_ []*domain.UserProfile, err error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, email, first_name, last_name
		 FROM users WHERE id IN (`+database.InPlaceholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying user profiles by IDs: %w", err)
	}
	defer database.CloseRows(rows, &err)

	profiles := make([]*domain.UserProfile, 0, len(ids))
	for rows.Next() {
		var p domain.UserProfile
		if err := rows.Scan(&p.ID, &p.Email, &p.FirstName, &p.LastName); err != nil {
			return nil, fmt.Errorf("scanning user profile: %w", err)
		}
		profiles = append(profiles, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating user profiles: %w", err)
	}
	return profiles, nil
}

// UpdatePassword updates the password hash for the given user. Returns ErrUserNotFound if not found.
func (r *UserRepo) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET password_hash = $1, updated_at = $2 WHERE id = $3`,
		passwordHash, time.Now(), id,
	)
	return database.RequireRow(res, err, "updating password", domain.ErrUserNotFound)
}

// UpdateIdentity sets the email and names of the given user. Returns ErrUserNotFound if the user does not exist,
// ErrEmailAlreadyExists if another user has the email.
func (r *UserRepo) UpdateIdentity(ctx context.Context, id uuid.UUID, email, firstName, lastName string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET email = $1, first_name = $2, last_name = $3, updated_at = $4 WHERE id = $5`,
		email, firstName, lastName, time.Now(), id,
	)
	if database.IsSQLiteUniqueViolation(err) {
		return domain.ErrEmailAlreadyExists
	}
	return database.RequireRow(res, err, "updating identity", domain.ErrUserNotFound)
}

// MarkEmailVerified sets email_verified_at to the given timestamp iff it is currently NULL. Idempotent — if the
// column is already populated the row is left untouched and no error is returned. ErrUserNotFound surfaces only when
// no row matches the id at all (distinguished from the no-op case via a separate existence check).
func (r *UserRepo) MarkEmailVerified(ctx context.Context, id uuid.UUID, when time.Time) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE users SET email_verified_at = $1, updated_at = $2
		 WHERE id = $3 AND email_verified_at IS NULL`,
		when, time.Now(), id,
	)
	if err != nil {
		return fmt.Errorf("marking email verified: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("marking email verified: %w", err)
	}
	if n > 0 {
		return nil
	}

	var exists bool
	err = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		return fmt.Errorf("checking user existence: %w", err)
	}
	if !exists {
		return domain.ErrUserNotFound
	}
	return nil
}

// ListUsers returns a paginated list of users matching the search query, along with the total count.
// The search is case-insensitive for ASCII letters (SQLite's LIKE) and matches against email, first name, and last
// name.
func (r *UserRepo) ListUsers(
	ctx context.Context, search string, limit, offset int,
) (_ []*domain.User, _ int, err error) {
	pattern := "%" + database.EscapeLike(search) + "%"

	var total int
	err = r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users
		 WHERE email LIKE $1 ESCAPE '\' OR first_name LIKE $1 ESCAPE '\' OR last_name LIKE $1 ESCAPE '\'`,
		pattern,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("counting users: %w", err)
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, email, first_name, last_name, created_at, updated_at, is_active, email_verified_at
		 FROM users
		 WHERE email LIKE $1 ESCAPE '\' OR first_name LIKE $1 ESCAPE '\' OR last_name LIKE $1 ESCAPE '\'
		 ORDER BY created_at DESC
		 LIMIT $2 OFFSET $3`,
		pattern, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("listing users: %w", err)
	}
	defer database.CloseRows(rows, &err)

	var users []*domain.User
	for rows.Next() {
		var u domain.User
		if err := rows.Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName,
			&u.CreatedAt, &u.UpdatedAt, &u.IsActive, &u.EmailVerifiedAt); err != nil {
			return nil, 0, fmt.Errorf("scanning user: %w", err)
		}
		users = append(users, &u)
	}
	return users, total, rows.Err()
}

// Exists returns true if at least one user exists.
func (r *UserRepo) Exists(ctx context.Context) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users LIMIT 1)`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking if users exist: %w", err)
	}
	return exists, nil
}

// scanUser scans a single row into a domain.User. Returns ErrUserNotFound when the row is missing.
func scanUser(row *sql.Row) (*domain.User, error) {
	var u domain.User
	err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.FirstName, &u.LastName,
		&u.CreatedAt, &u.UpdatedAt, &u.IsActive, &u.EmailVerifiedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning user: %w", err)
	}
	return &u, nil
}
