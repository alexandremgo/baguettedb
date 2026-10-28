package service

import (
	"context"
	"errors"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
)

// AuthenticationService handles user credential verification.
type AuthenticationService struct {
	repo credentialFinder
}

// credentialFinder finds the account a sign-in names.
type credentialFinder interface {
	// FindByEmail returns the user with the given email. Returns domain.ErrUserNotFound if not found.
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
}

// NewAuthenticationService creates a new AuthenticationService.
func NewAuthenticationService(repo credentialFinder) *AuthenticationService {
	return &AuthenticationService{repo: repo}
}

// Authenticate verifies the email/password combination and returns the user.
// Returns ErrInvalidCredentials for both "not found" and "wrong password" to prevent user enumeration.
func (s *AuthenticationService) Authenticate(ctx context.Context, email, password string) (*domain.User, error) {
	email = normalizeEmail(email)

	user, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return nil, domain.ErrInvalidCredentials
		}
		return nil, err
	}

	if !user.IsActive {
		return nil, domain.ErrInvalidCredentials
	}

	// Users created via an external identity provider have no local password. Returning ErrInvalidCredentials
	// (rather than a distinct error) preserves the no-enumeration property: the email-plus-password form cannot be
	// used to probe whether a given address is bound to an OIDC-only account.
	if user.PasswordHash == nil {
		return nil, domain.ErrInvalidCredentials
	}

	if err := domain.CheckPassword(*user.PasswordHash, password); err != nil {
		return nil, err
	}

	return user, nil
}
