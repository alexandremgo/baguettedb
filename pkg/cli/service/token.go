package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/cli/domain"
)

// tokenUserFinder finds the account whose tokens are managed.
type tokenUserFinder interface {
	// FindUserByEmail returns the account with email. None wraps domain.ErrUserNotFound.
	FindUserByEmail(ctx context.Context, email string) (domain.User, error)
}

// tokenStore manages an account's personal access tokens.
type tokenStore interface {
	// CreateToken creates a token named name for the account userID, expiring at expiresAt (nil for never), and
	// returns its value, which can't be read again, with the token.
	CreateToken(
		ctx context.Context, userID uuid.UUID, name string, expiresAt *time.Time,
	) (string, domain.AccessToken, error)

	// ListTokens returns the account's tokens that are not revoked, newest first.
	ListTokens(ctx context.Context, userID uuid.UUID) ([]domain.AccessToken, error)

	// RevokeToken revokes the account's token id. A token the account doesn't have, or already revoked, wraps
	// domain.ErrTokenNotFound.
	RevokeToken(ctx context.Context, userID, id uuid.UUID) error
}

// TokenService runs the personal access token use cases, each for the account with a given email.
type TokenService struct {
	users  tokenUserFinder
	tokens tokenStore
}

// NewTokenService creates a TokenService finding accounts in users and managing their tokens in tokens.
func NewTokenService(users tokenUserFinder, tokens tokenStore) *TokenService {
	return &TokenService{users: users, tokens: tokens}
}

// Create creates a personal access token named name for the account with email, working for lifetime from now, or
// forever when lifetime is 0, and returns its value, shown this once, with the token.
func (s *TokenService) Create(
	ctx context.Context, email, name string, lifetime time.Duration,
) (string, domain.AccessToken, error) {
	user, err := s.users.FindUserByEmail(ctx, email)
	if err != nil {
		return "", domain.AccessToken{}, err
	}
	var expiresAt *time.Time
	if lifetime > 0 {
		at := time.Now().Add(lifetime)
		expiresAt = &at
	}
	plain, token, err := s.tokens.CreateToken(ctx, user.ID, name, expiresAt)
	if err != nil {
		return "", domain.AccessToken{}, fmt.Errorf("creating a token for %s: %w", user.Email, err)
	}
	return plain, token, nil
}

// List returns the personal access tokens of the account with email that are not revoked, newest first.
func (s *TokenService) List(ctx context.Context, email string) ([]domain.AccessToken, error) {
	user, err := s.users.FindUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	tokens, err := s.tokens.ListTokens(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("listing the tokens of %s: %w", user.Email, err)
	}
	return tokens, nil
}

// Revoke revokes the token of the account with email that ref names, its ID or a prefix of it, and returns it. It
// stops authenticating at once. A ref matching none of the account's tokens wraps domain.ErrTokenNotFound; several, a
// *domain.AmbiguousTokenError.
func (s *TokenService) Revoke(ctx context.Context, email, ref string) (domain.AccessToken, error) {
	user, err := s.users.FindUserByEmail(ctx, email)
	if err != nil {
		return domain.AccessToken{}, err
	}
	tokens, err := s.tokens.ListTokens(ctx, user.ID)
	if err != nil {
		return domain.AccessToken{}, fmt.Errorf("listing the tokens of %s: %w", user.Email, err)
	}
	token, err := domain.ResolveToken(ref, tokens)
	if err != nil {
		return domain.AccessToken{}, fmt.Errorf("%w for %s", err, user.Email)
	}
	if err := s.tokens.RevokeToken(ctx, user.ID, token.ID); err != nil {
		return domain.AccessToken{}, fmt.Errorf("revoking a token of %s: %w", user.Email, err)
	}
	return token, nil
}
