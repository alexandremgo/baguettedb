// Package service holds the auth app's application services: authentication, user accounts and their global roles,
// personal access tokens and sessions, the login and consent requests the authorization server delegates, sign-in
// through external OIDC providers, the instance owner, and the web frontend's OAuth2 client registration. The contracts
// several parties name sit in port.go.
package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// UserService handles user profile and user creation operations.
type UserService struct {
	repo   UserRepository
	inTx   RunInTx[UserTx]
	logger zerolog.Logger
}

// UserTx holds the repositories UserService writes through within one transaction.
type UserTx struct {
	Users    userWriter
	Roles    roleAssigner
	Sessions sessionRevoker
}

// userWriter stores user accounts and their identity and password.
type userWriter interface {
	// Create persists a new user. Returns domain.ErrEmailAlreadyExists if the email is taken.
	Create(ctx context.Context, user *domain.User) error

	// UpdateIdentity sets the email and names of the given user. Returns domain.ErrUserNotFound if the user does not
	// exist, domain.ErrEmailAlreadyExists if another user has the email.
	UpdateIdentity(ctx context.Context, id uuid.UUID, email, firstName, lastName string) error

	// UpdatePassword updates the password hash for the given user. Returns domain.ErrUserNotFound if the user does not
	// exist.
	UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string) error
}

// roleAssigner grants global roles to users.
type roleAssigner interface {
	// AssignRole grants the given role to a user. The operation is idempotent.
	AssignRole(ctx context.Context, userID uuid.UUID, role authz.Role) error
}

// sessionRevoker ends the web sessions of an account.
type sessionRevoker interface {
	// RevokeSessions sets the revocation time of the user's sessions that are not revoked to at.
	RevokeSessions(ctx context.Context, userID uuid.UUID, at time.Time) error
}

// NewUserService creates a new UserService reading users from repo and writing them through inTx.
func NewUserService(repo UserRepository, inTx RunInTx[UserTx], logger zerolog.Logger) *UserService {
	return &UserService{repo: repo, inTx: inTx, logger: logger}
}

// GetProfile retrieves a user by ID. A plain read, kept on the service so that user storage stays reachable only
// through the app's services.
func (s *UserService) GetProfile(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	return s.repo.FindByID(ctx, userID)
}

// GetProfiles returns the non-credential profile of each user matching the
// given IDs. Duplicate IDs are deduplicated; unknown IDs are silently omitted.
// Returns nil without a database call when ids is empty.
func (s *UserService) GetProfiles(ctx context.Context, ids []uuid.UUID) ([]*domain.UserProfile, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return s.repo.FindByIDs(ctx, dedupUUIDs(ids))
}

// dedupUUIDs returns ids with duplicates removed, preserving first occurrence.
func dedupUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ChangePassword validates the old password, checks the new password strength, and updates the hash. With
// endSessions, every session of the user ends with it, in one transaction; their personal access tokens stay.
func (s *UserService) ChangePassword(
	ctx context.Context, userID uuid.UUID, oldPassword, newPassword string, endSessions bool,
) error {
	user, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	// Users created via an external identity provider have no local password, so there is no "old password" to
	// verify against. Surfacing ErrInvalidCredentials keeps the response indistinguishable from a wrong-password
	// attempt and avoids hinting at the account's auth method.
	if user.PasswordHash == nil {
		return domain.ErrInvalidCredentials
	}
	if err := domain.CheckPassword(*user.PasswordHash, oldPassword); err != nil {
		return err
	}

	if err := domain.ValidatePasswordStrength(newPassword); err != nil {
		return err
	}

	hash, err := domain.HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("changing password: %w", err)
	}

	return s.inTx(ctx, func(tx UserTx) error {
		return storePassword(ctx, tx, userID, hash, endSessions)
	})
}

// UpdateAccount sets the email and names of the user userID, and their password unless password is empty, in one
// transaction, so either all of it changes or nothing does. The email is stored as CreateUser stores it, trimmed and
// lowercased, the names trimmed.
//
// With endSessions, a new password ends every session of the user in the same transaction; their personal access
// tokens stay.
//
// An invalid email is a domain.ErrInvalidEmail; one another user has a domain.ErrEmailAlreadyExists; a weak password a
// domain.ErrPasswordTooWeak; an unknown user a domain.ErrUserNotFound.
func (s *UserService) UpdateAccount(
	ctx context.Context, userID uuid.UUID, email, firstName, lastName, password string, endSessions bool,
) error {
	email = normalizeEmail(email)
	if err := domain.ValidateEmail(email); err != nil {
		return err
	}
	var hash string
	if password != "" {
		if err := domain.ValidatePasswordStrength(password); err != nil {
			return err
		}
		var err error
		if hash, err = domain.HashPassword(password); err != nil {
			return fmt.Errorf("updating account: %w", err)
		}
	}

	return s.inTx(ctx, func(tx UserTx) error {
		err := tx.Users.UpdateIdentity(ctx, userID, email, strings.TrimSpace(firstName), strings.TrimSpace(lastName))
		if err != nil || hash == "" {
			return err
		}
		return storePassword(ctx, tx, userID, hash, endSessions)
	})
}

// storePassword sets the password hash of the user userID in tx's transaction and, with endSessions, revokes every
// session of theirs.
func storePassword(ctx context.Context, tx UserTx, userID uuid.UUID, hash string, endSessions bool) error {
	if err := tx.Users.UpdatePassword(ctx, userID, hash); err != nil {
		return err
	}
	if !endSessions {
		return nil
	}
	return tx.Sessions.RevokeSessions(ctx, userID, time.Now().UTC())
}

// ListUsers returns a paginated list of users matching the search query, along with the total count. A plain read,
// kept on the service so that user storage stays reachable only through the app's services.
func (s *UserService) ListUsers(ctx context.Context, search string, limit, offset int) ([]*domain.User, int, error) {
	return s.repo.ListUsers(ctx, search, limit, offset)
}

// normalizeEmail lowercases and trims whitespace from an email address.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// CreateUser creates a new user account with the given credentials and assigns
// the default user role. All writes run in a single database transaction.
func (s *UserService) CreateUser(
	ctx context.Context, email, password, firstName, lastName string,
) (*domain.User, error) {
	email = normalizeEmail(email)

	if err := domain.ValidateEmail(email); err != nil {
		return nil, err
	}

	if err := domain.ValidatePasswordStrength(password); err != nil {
		return nil, err
	}

	hash, err := domain.HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}

	user := newUser(email, firstName, lastName)
	user.PasswordHash = &hash
	if err := s.create(ctx, user, authz.RoleUser); err != nil {
		return nil, err
	}
	return user, nil
}

// CreateAccount creates a user account with password, or none when password is empty, so it can't sign in with one,
// and assigns it the user role, plus the admin role when admin is set, all in one transaction.
//
// An invalid email is a domain.ErrInvalidEmail; an email already in use a domain.ErrEmailAlreadyExists; a weak password
// a domain.ErrPasswordTooWeak.
func (s *UserService) CreateAccount(
	ctx context.Context, email, firstName, lastName, password string, admin bool,
) (*domain.User, error) {
	email = normalizeEmail(email)
	if err := domain.ValidateEmail(email); err != nil {
		return nil, err
	}
	user := newUser(email, firstName, lastName)
	if password != "" {
		if err := domain.ValidatePasswordStrength(password); err != nil {
			return nil, err
		}
		hash, err := domain.HashPassword(password)
		if err != nil {
			return nil, fmt.Errorf("creating user: %w", err)
		}
		user.PasswordHash = &hash
	}

	roles := []authz.Role{authz.RoleUser}
	if admin {
		roles = append(roles, authz.RoleAdmin)
	}
	if err := s.create(ctx, user, roles...); err != nil {
		return nil, err
	}
	return user, nil
}

// SetPassword sets the password of the user userID, whatever their current one, after checking its strength. With
// endSessions, every session of the user ends with it, in one transaction; their personal access tokens stay.
//
// A weak password is a domain.ErrPasswordTooWeak; an unknown user a domain.ErrUserNotFound.
func (s *UserService) SetPassword(ctx context.Context, userID uuid.UUID, password string, endSessions bool) error {
	if err := domain.ValidatePasswordStrength(password); err != nil {
		return err
	}
	hash, err := domain.HashPassword(password)
	if err != nil {
		return fmt.Errorf("setting password: %w", err)
	}
	return s.inTx(ctx, func(tx UserTx) error {
		return storePassword(ctx, tx, userID, hash, endSessions)
	})
}

// FindByEmail returns the user with email, compared as CreateUser stores it: trimmed and lowercased. Returns
// domain.ErrUserNotFound when there is none.
func (s *UserService) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	return s.repo.FindByEmail(ctx, normalizeEmail(email))
}

// newUser returns an active user with a new ID, email, and the trimmed names, created now.
func newUser(email, firstName, lastName string) *domain.User {
	now := time.Now().UTC()
	return &domain.User{
		ID:        uuid.New(),
		Email:     email,
		FirstName: strings.TrimSpace(firstName),
		LastName:  strings.TrimSpace(lastName),
		CreatedAt: now,
		UpdatedAt: now,
		IsActive:  true,
	}
}

// create stores user and assigns it roles, in one transaction.
func (s *UserService) create(ctx context.Context, user *domain.User, roles ...authz.Role) error {
	err := s.inTx(ctx, func(tx UserTx) error {
		if err := tx.Users.Create(ctx, user); err != nil {
			return err
		}
		for _, role := range roles {
			if err := tx.Roles.AssignRole(ctx, user.ID, role); err != nil {
				return fmt.Errorf("assigning role %s: %w", role, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

// BootstrapAdmin creates the first account, an admin with adminEmail and adminPassword, when both are set and no user
// exists yet; it does nothing otherwise, so every startup after the first leaves the users as they are. The password
// is stored without a strength check: it comes from the operator's configuration, not from a sign-up.
func (s *UserService) BootstrapAdmin(ctx context.Context, adminEmail, adminPassword string) error {
	adminEmail = normalizeEmail(adminEmail)
	if adminEmail == "" || adminPassword == "" {
		return nil
	}

	exists, err := s.repo.Exists(ctx)
	if err != nil {
		return fmt.Errorf("checking existing users: %w", err)
	}
	if exists {
		s.logger.Info().Msg("bootstrap admin skipped (users already exist)")
		return nil
	}

	hash, err := domain.HashPassword(adminPassword)
	if err != nil {
		return fmt.Errorf("hashing admin password: %w", err)
	}
	admin := newUser(adminEmail, "", "")
	admin.PasswordHash = &hash
	if err := s.create(ctx, admin, authz.RoleUser, authz.RoleAdmin); err != nil {
		return fmt.Errorf("creating bootstrap admin: %w", err)
	}

	s.logger.Info().Str("email", adminEmail).Msg("bootstrap admin created")
	return nil
}
