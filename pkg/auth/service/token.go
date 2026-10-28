package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// tokenBytes is how many random bytes a personal access token carries: 256 bits.
const tokenBytes = 32

// lastUsedInterval is how stale a token's last use may get before a use records a new one: a token in steady use
// writes its last use at most once per interval, not once per request.
const lastUsedInterval = time.Minute

// SessionTTL is how long a session lasts from the sign-in. Exported so the cookie carrying it can last as long.
const SessionTTL = 30 * 24 * time.Hour

// sessionName is the name every session gets, since nobody chooses it.
const sessionName = "session"

// bearerKinds lists the token kinds a request may authenticate with: personal tokens and sessions.
var bearerKinds = []domain.AccessTokenKind{domain.AccessTokenPersonal, domain.AccessTokenSession}

// accessTokenRepository persists access tokens, by the hash of their plain value.
type accessTokenRepository interface {
	// Create persists token, whose plain value hashes to hash. A user that doesn't exist fails the insert.
	Create(ctx context.Context, token *domain.AccessToken, hash string) error

	// FindByHash returns the token whose plain value hashes to hash, revoked or expired ones included. Returns
	// domain.ErrAccessTokenNotFound when none does.
	FindByHash(ctx context.Context, hash string) (*domain.AccessToken, error)

	// FindWithUserByHash returns, in one read, the token whose plain value hashes to hash, revoked or expired ones
	// included, with whether its user is active and the user's roles. Returns domain.ErrAccessTokenNotFound when none
	// does.
	FindWithUserByHash(ctx context.Context, hash string) (*domain.AccessTokenWithUser, error)

	// ListByUser returns the user's tokens of kind that are not revoked, newest first.
	ListByUser(ctx context.Context, userID uuid.UUID, kind domain.AccessTokenKind) ([]*domain.AccessToken, error)

	// Revoke sets the revocation time of the user's token id to at. Returns domain.ErrAccessTokenNotFound when the
	// user has no such token, or it is already revoked.
	Revoke(ctx context.Context, userID, id uuid.UUID, at time.Time) error

	// TouchLastUsed sets the last use of token id to at.
	TouchLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error
}

// tokenUserFinder reads the account a token belongs to.
type tokenUserFinder interface {
	// FindByID returns the user with the given ID. Returns domain.ErrUserNotFound if not found.
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
}

// passwordChecker checks a user's email and password.
type passwordChecker interface {
	// Authenticate returns the active user with email whose password is password. An unknown email, a wrong password,
	// an inactive user and one without a password are all domain.ErrInvalidCredentials.
	Authenticate(ctx context.Context, email, password string) (*domain.User, error)
}

// TokenService manages access tokens: creating, listing and revoking personal ones, starting and ending sessions,
// and checking a bearer token.
type TokenService struct {
	tokens    accessTokenRepository
	users     tokenUserFinder
	passwords passwordChecker
}

// NewTokenService creates a TokenService over the given access token repository, finding the account a new token is
// for in users, and checking the passwords of sign-ins with passwords.
func NewTokenService(tokens accessTokenRepository, users tokenUserFinder, passwords passwordChecker) *TokenService {
	return &TokenService{tokens: tokens, users: users, passwords: passwords}
}

// Create creates a personal access token named name for the user userID, expiring at expiresAt (nil for never), and
// returns its plain value, which is not stored and can't be shown again, with the token.
//
// An empty or too long name is a domain.ErrInvalidAccessTokenName; an expiry not in the future a
// domain.ErrInvalidAccessTokenExpiry; an unknown user a domain.ErrUserNotFound.
func (s *TokenService) Create(
	ctx context.Context, userID uuid.UUID, name string, expiresAt *time.Time,
) (string, *domain.AccessToken, error) {
	name, err := domain.ValidateAccessTokenName(name)
	if err != nil {
		return "", nil, err
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return "", nil, domain.ErrInvalidAccessTokenExpiry
	}
	return s.create(ctx, userID, domain.AccessTokenPersonal, name, expiresAt)
}

// SignIn checks the email and password of a user and starts a session for them, lasting SessionTTL. It returns the
// session's plain value with its token.
//
// An unknown email, a wrong password, an inactive account and one without a password are all a
// domain.ErrInvalidCredentials, so a sign-in never tells which accounts exist.
func (s *TokenService) SignIn(ctx context.Context, email, password string) (string, *domain.AccessToken, error) {
	user, err := s.passwords.Authenticate(ctx, email, password)
	if err != nil {
		return "", nil, err
	}
	return s.createSession(ctx, user.ID, time.Now().UTC())
}

// EndSession revokes the session plain, whoever's it is. A value that is no session, or one already revoked, leaves
// nothing to revoke and is not an error.
func (s *TokenService) EndSession(ctx context.Context, plain string) error {
	if !strings.HasPrefix(plain, domain.AccessTokenSession.Prefix()) {
		return nil
	}
	token, err := s.tokens.FindByHash(ctx, hashToken(plain))
	if errors.Is(err, domain.ErrAccessTokenNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if token.Kind != domain.AccessTokenSession || token.RevokedAt != nil {
		return nil
	}
	err = s.tokens.Revoke(ctx, token.UserID, token.ID, time.Now().UTC())
	if errors.Is(err, domain.ErrAccessTokenNotFound) {
		return nil
	}
	return err
}

// createSession creates a session for the user userID signing in at now, lasting SessionTTL.
func (s *TokenService) createSession(
	ctx context.Context, userID uuid.UUID, now time.Time,
) (string, *domain.AccessToken, error) {
	expiresAt := now.Add(SessionTTL)
	return s.create(ctx, userID, domain.AccessTokenSession, sessionName, &expiresAt)
}

// create creates a token of kind named name for the user userID, expiring at expiresAt (nil for never), and returns
// its plain value with the token. An unknown user is a domain.ErrUserNotFound.
func (s *TokenService) create(
	ctx context.Context, userID uuid.UUID, kind domain.AccessTokenKind, name string, expiresAt *time.Time,
) (string, *domain.AccessToken, error) {
	if _, err := s.users.FindByID(ctx, userID); err != nil {
		return "", nil, err
	}

	plain, hash, err := newToken(kind)
	if err != nil {
		return "", nil, fmt.Errorf("generating an access token: %w", err)
	}
	token := &domain.AccessToken{
		ID:          uuid.New(),
		UserID:      userID,
		Kind:        kind,
		Name:        name,
		TokenPrefix: plain[len(kind.Prefix()):][:domain.AccessTokenPrefixLen],
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   expiresAt,
	}
	if err := s.tokens.Create(ctx, token, hash); err != nil {
		return "", nil, fmt.Errorf("storing the access token: %w", err)
	}
	return plain, token, nil
}

// List returns the user's personal access tokens that are not revoked, newest first.
func (s *TokenService) List(ctx context.Context, userID uuid.UUID) ([]*domain.AccessToken, error) {
	return s.tokens.ListByUser(ctx, userID, domain.AccessTokenPersonal)
}

// OpenSessions returns how many sessions of the user userID are neither revoked nor expired.
func (s *TokenService) OpenSessions(ctx context.Context, userID uuid.UUID) (int, error) {
	sessions, err := s.tokens.ListByUser(ctx, userID, domain.AccessTokenSession)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	open := 0
	for _, session := range sessions {
		if session.ExpiresAt == nil || now.Before(*session.ExpiresAt) {
			open++
		}
	}
	return open, nil
}

// Revoke revokes the user's token id, which stops authenticating at once. Returns domain.ErrAccessTokenNotFound when
// the user has no such token, or it is already revoked.
func (s *TokenService) Revoke(ctx context.Context, userID, id uuid.UUID) error {
	return s.tokens.Revoke(ctx, userID, id, time.Now().UTC())
}

// Check returns the user a personal access token or a session authenticates, with their roles, and records its use at
// most once per lastUsedInterval.
//
// A token that is unknown, revoked, expired, of another kind than its prefix says, or of an inactive or deleted user
// is a domain.ErrInvalidAccessToken.
//
// The token, its user's active flag and the user's roles come from one read of the store, made on every call: nothing
// is cached, so a token revoked by any process stops authenticating on its next request.
func (s *TokenService) Check(ctx context.Context, plain string) (uuid.UUID, []authz.Role, error) {
	i := slices.IndexFunc(bearerKinds, func(k domain.AccessTokenKind) bool {
		return strings.HasPrefix(plain, k.Prefix())
	})
	if i < 0 {
		return uuid.Nil, nil, domain.ErrInvalidAccessToken
	}

	found, err := s.tokens.FindWithUserByHash(ctx, hashToken(plain))
	if errors.Is(err, domain.ErrAccessTokenNotFound) {
		return uuid.Nil, nil, domain.ErrInvalidAccessToken
	}
	if err != nil {
		return uuid.Nil, nil, err
	}

	now := time.Now().UTC()
	if found.Token.Kind != bearerKinds[i] || found.Token.RevokedAt != nil ||
		(found.Token.ExpiresAt != nil && !now.Before(*found.Token.ExpiresAt)) {
		return uuid.Nil, nil, domain.ErrInvalidAccessToken
	}

	if !found.UserActive {
		return uuid.Nil, nil, domain.ErrInvalidAccessToken
	}

	if found.Token.LastUsedAt == nil || found.Token.LastUsedAt.Before(now.Add(-lastUsedInterval)) {
		if err := s.tokens.TouchLastUsed(ctx, found.Token.ID, now); err != nil {
			return uuid.Nil, nil, fmt.Errorf("recording the token's use: %w", err)
		}
	}
	return found.Token.UserID, found.Roles, nil
}

// newToken returns a new access token of kind and its hash: the kind's prefix followed by tokenBytes random bytes in
// unpadded URL-safe base64, and hashToken of that.
func newToken(kind domain.AccessTokenKind) (plain, hash string, err error) {
	secret := make([]byte, tokenBytes)
	if _, err = rand.Read(secret); err != nil {
		return "", "", fmt.Errorf("reading random bytes: %w", err)
	}

	plain = kind.Prefix() + base64.RawURLEncoding.EncodeToString(secret)
	return plain, hashToken(plain), nil
}

// hashToken returns the hex SHA-256 of a plain token, the form tokens are stored and looked up in. A token carries 256
// random bits, so a fast unsalted hash is enough: there is nothing to brute-force.
func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
