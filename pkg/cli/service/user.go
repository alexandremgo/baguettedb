package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// userDirectory creates, finds and lists the instance's accounts, and sets their passwords.
type userDirectory interface {
	// CreateUser creates an account with password, none when it is empty, an admin one when admin is set, and returns
	// it. An email already in use wraps domain.ErrUserExists, an invalid one domain.ErrInvalidEmail, a weak password
	// domain.ErrWeakPassword.
	CreateUser(ctx context.Context, email, firstName, lastName, password string, admin bool) (domain.User, error)

	// FindUserByEmail returns the account with email. None wraps domain.ErrUserNotFound.
	FindUserByEmail(ctx context.Context, email string) (domain.User, error)

	// ListUsers returns every account.
	ListUsers(ctx context.Context) ([]domain.User, error)

	// SetPassword sets the password of the account id, whatever its current one, and ends its web sessions with it
	// when endSessions is set. A weak password wraps domain.ErrWeakPassword.
	SetPassword(ctx context.Context, id uuid.UUID, password string, endSessions bool) error

	// OpenSessions returns how many web sessions of the account id are open: neither ended nor expired.
	OpenSessions(ctx context.Context, id uuid.UUID) (int, error)

	// CheckPasswordStrength returns nil for a password strong enough to set, an error wrapping domain.ErrWeakPassword
	// otherwise.
	CheckPasswordStrength(password string) error
}

// UserService runs the account use cases.
type UserService struct {
	users userDirectory
}

// NewUserService creates a UserService over the instance's accounts.
func NewUserService(users userDirectory) *UserService {
	return &UserService{users: users}
}

// Create creates an account for email, named name, with password, an admin one when admin is set. The first word of
// name is the first name and the rest the last name. With an empty password, the account signs in with the tokens
// created for it alone, until one is set.
func (s *UserService) Create(ctx context.Context, email, name, password string, admin bool) (domain.User, error) {
	first, last := splitName(name)
	return s.users.CreateUser(ctx, email, first, last, password, admin)
}

// SetPassword sets the password of the account with email, whatever its current one, and ends its web sessions with
// it when endSessions is set. Its personal access tokens stay.
func (s *UserService) SetPassword(ctx context.Context, email, password string, endSessions bool) error {
	user, err := s.users.FindUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if err := s.users.SetPassword(ctx, user.ID, password, endSessions); err != nil {
		return fmt.Errorf("setting the password of %s: %w", user.Email, err)
	}
	return nil
}

// OpenSessions returns how many web sessions of the account with email are open: neither ended nor expired.
func (s *UserService) OpenSessions(ctx context.Context, email string) (int, error) {
	user, err := s.users.FindUserByEmail(ctx, email)
	if err != nil {
		return 0, err
	}
	open, err := s.users.OpenSessions(ctx, user.ID)
	if err != nil {
		return 0, fmt.Errorf("counting the web sessions of %s: %w", user.Email, err)
	}
	return open, nil
}

// CheckPassword returns nil for a password strong enough to set, an error wrapping domain.ErrWeakPassword otherwise,
// so a command can refuse it before asking anything else.
func (s *UserService) CheckPassword(password string) error {
	return s.users.CheckPasswordStrength(password)
}

// List returns every account.
func (s *UserService) List(ctx context.Context) ([]domain.User, error) {
	users, err := s.users.ListUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}
	return users, nil
}

// splitName returns the first and last names of the full name name: its first word, and the rest.
func splitName(name string) (first, last string) {
	first, last, _ = strings.Cut(strings.TrimSpace(name), " ")
	return first, strings.TrimSpace(last)
}
