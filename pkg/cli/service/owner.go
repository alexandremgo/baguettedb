package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// ownerAccount reads and changes the account of the instance's owner.
type ownerAccount interface {
	// FindUserByID returns the account id. None wraps domain.ErrUserNotFound.
	FindUserByID(ctx context.Context, id uuid.UUID) (domain.User, error)

	// UpdateUser sets the email and names of the account id, and its password unless password is empty, ending its web
	// sessions with a new password when endSessions is set: all of it or nothing. An email another account has wraps
	// domain.ErrUserExists, an invalid one domain.ErrInvalidEmail, a weak password domain.ErrWeakPassword.
	UpdateUser(ctx context.Context, id uuid.UUID, email, firstName, lastName, password string, endSessions bool) error

	// OpenSessions returns how many web sessions of the account id are open: neither ended nor expired.
	OpenSessions(ctx context.Context, id uuid.UUID) (int, error)
}

// OwnerService runs the use cases of the instance owner's account: reading it, and setting the owner's identity and
// password.
type OwnerService struct {
	accounts ownerAccount
	ownerID  uuid.UUID
}

// NewOwnerService creates an OwnerService over accounts, for the owner ownerID.
func NewOwnerService(accounts ownerAccount, ownerID uuid.UUID) *OwnerService {
	return &OwnerService{accounts: accounts, ownerID: ownerID}
}

// Owner returns the owner's account.
func (s *OwnerService) Owner(ctx context.Context) (domain.User, error) {
	owner, err := s.accounts.FindUserByID(ctx, s.ownerID)
	if err != nil {
		return domain.User{}, fmt.Errorf("reading the owner: %w", err)
	}
	return owner, nil
}

// OpenSessions returns how many web sessions of the owner are open: neither ended nor expired.
func (s *OwnerService) OpenSessions(ctx context.Context) (int, error) {
	open, err := s.accounts.OpenSessions(ctx, s.ownerID)
	if err != nil {
		return 0, fmt.Errorf("counting the owner's web sessions: %w", err)
	}
	return open, nil
}

// Update sets the owner's email and full name, its first word the first name and the rest the last name, and their
// password unless password is empty, all of it or nothing, then returns the account as it now stands. A new password
// ends the owner's web sessions when endSessions is set; their personal access tokens stay.
func (s *OwnerService) Update(
	ctx context.Context, email, name, password string, endSessions bool,
) (domain.User, error) {
	first, last := splitName(name)
	if err := s.accounts.UpdateUser(ctx, s.ownerID, email, first, last, password, endSessions); err != nil {
		return domain.User{}, err
	}
	return s.Owner(ctx)
}
