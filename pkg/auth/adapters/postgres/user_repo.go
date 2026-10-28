// Package postgres implements the auth app's repositories on the Postgres instance store: users and their global roles,
// external identity links, the state of OIDC sign-in flows in progress, access tokens, and the instance owner. Each
// repository is built over a database.DBTX, so it runs on the pool or inside a transaction alike.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

const uniqueViolationCode = "23505"

// UserRepo stores users in Postgres.
type UserRepo struct {
	db database.DBTX
}

// NewUserRepo creates a new UserRepo backed by the given DBTX (pool or transaction).
func NewUserRepo(db database.DBTX) *UserRepo {
	return &UserRepo{db: db}
}

// Create persists a new user. Returns ErrEmailAlreadyExists if the email is taken.
func (r *UserRepo) Create(ctx context.Context, user *domain.User) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO users (
			id, email, password_hash, first_name, last_name,
			created_at, updated_at, is_active, email_verified_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		user.ID, user.Email, user.PasswordHash, user.FirstName, user.LastName,
		user.CreatedAt, user.UpdatedAt, user.IsActive, user.EmailVerifiedAt,
	)
	if err != nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolationCode {
			return domain.ErrEmailAlreadyExists
		}
		return fmt.Errorf("inserting user: %w", err)
	}
	return nil
}

// FindByEmail returns the user with the given email. Returns ErrUserNotFound if not found.
func (r *UserRepo) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	return r.scanUser(r.db.QueryRow(ctx,
		`SELECT id, email, password_hash, first_name, last_name, created_at, updated_at, is_active, email_verified_at
		 FROM users WHERE email = $1`, email))
}

// FindByID returns the user with the given ID. Returns ErrUserNotFound if not found.
func (r *UserRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return r.scanUser(r.db.QueryRow(ctx,
		`SELECT id, email, password_hash, first_name, last_name, created_at, updated_at, is_active, email_verified_at
		 FROM users WHERE id = $1`, id))
}

// FindByIDs returns the non-credential profile of each user matching the given IDs.
func (r *UserRepo) FindByIDs(ctx context.Context, ids []uuid.UUID) ([]*domain.UserProfile, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(ctx,
		`SELECT id, email, first_name, last_name
		 FROM users WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("querying user profiles by IDs: %w", err)
	}
	defer rows.Close()

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
	tag, err := r.db.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = NOW() WHERE id = $2`,
		passwordHash, id,
	)
	if err != nil {
		return fmt.Errorf("updating password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// UpdateIdentity sets the email and names of the given user. Returns ErrUserNotFound if the user does not exist,
// ErrEmailAlreadyExists if another user has the email.
func (r *UserRepo) UpdateIdentity(ctx context.Context, id uuid.UUID, email, firstName, lastName string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE users SET email = $1, first_name = $2, last_name = $3, updated_at = NOW() WHERE id = $4`,
		email, firstName, lastName, id,
	)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == uniqueViolationCode {
		return domain.ErrEmailAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("updating identity: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// MarkEmailVerified sets email_verified_at to the given timestamp iff it is currently NULL. Idempotent — if the column
// is already populated the row is left untouched and no error is returned. ErrUserNotFound surfaces only when no row
// matches the id at all (distinguished from the no-op case via a separate existence check).
func (r *UserRepo) MarkEmailVerified(ctx context.Context, id uuid.UUID, when time.Time) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE users SET email_verified_at = $1, updated_at = NOW()
		 WHERE id = $2 AND email_verified_at IS NULL`,
		when, id,
	)
	if err != nil {
		return fmt.Errorf("marking email verified: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	var exists bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("checking user existence: %w", err)
	}
	if !exists {
		return domain.ErrUserNotFound
	}
	return nil
}

// ListUsers returns a paginated list of users matching the search query, along with the total count.
// The search is case-insensitive and matches against email, first name, and last name.
func (r *UserRepo) ListUsers(ctx context.Context, search string, limit, offset int) ([]*domain.User, int, error) {
	pattern := "%" + escapeILIKE(search) + "%"

	var total int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM users
		 WHERE email ILIKE $1 OR first_name ILIKE $1 OR last_name ILIKE $1`,
		pattern,
	).Scan(&total)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, fmt.Errorf("counting users: %w", err)
	}

	rows, err := r.db.Query(ctx,
		`SELECT id, email, first_name, last_name, created_at, updated_at, is_active, email_verified_at
		 FROM users
		 WHERE email ILIKE $1 OR first_name ILIKE $1 OR last_name ILIKE $1
		 ORDER BY created_at DESC
		 LIMIT $2 OFFSET $3`,
		pattern, limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("listing users: %w", err)
	}
	defer rows.Close()

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
	err := r.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users LIMIT 1)`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking if users exist: %w", err)
	}
	return exists, nil
}

// scanUser scans a single row into a domain.User.
func (r *UserRepo) scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	err := row.Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.FirstName, &u.LastName,
		&u.CreatedAt, &u.UpdatedAt, &u.IsActive, &u.EmailVerifiedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scanning user: %w", err)
	}
	return &u, nil
}

// escapeILIKE escapes special ILIKE/LIKE characters (%, _, \) in user input.
func escapeILIKE(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
