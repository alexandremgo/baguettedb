package service

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// TestNewToken checks that a new token is its kind's prefix followed by 32 random bytes in unpadded URL-safe base64,
// that its hash is hashToken of it, and that two tokens differ.
func TestNewToken(t *testing.T) {
	prefix := domain.AccessTokenPersonal.Prefix()
	plain, hash, err := newToken(domain.AccessTokenPersonal)
	if err != nil {
		t.Fatalf("newToken() = %v", err)
	}
	encoded, found := strings.CutPrefix(plain, prefix)
	if !found {
		t.Fatalf("token %q doesn't start with %q", plain, prefix)
	}
	if random, err := base64.RawURLEncoding.DecodeString(encoded); err != nil || len(random) != 32 {
		t.Errorf("token %q carries %d bytes (%v), want 32 in unpadded URL-safe base64", plain, len(random), err)
	}
	if hash != hashToken(plain) {
		t.Errorf("hash = %q, want hashToken(token) = %q", hash, hashToken(plain))
	}

	other, _, err := newToken(domain.AccessTokenPersonal)
	if err != nil || other == plain {
		t.Errorf("second newToken() = %q, %v; want another token", other, err)
	}
}

// TestTokenService_Check checks that Check reads a bearer token with its user's active flag and roles in one read,
// never the token or the user on their own; that it returns the user and roles of a valid personal token
// or session; that an unknown token, one without a known prefix, a revoked or expired one, one of another kind than
// its prefix says and one of an inactive user are all ErrInvalidAccessToken; that a failed read is returned, never
// taken for a valid token; and that it records a use only when the last one is older than lastUsedInterval.
func TestTokenService_Check(t *testing.T) {
	now := time.Now().UTC()
	userID := uuid.New()
	personal := domain.AccessTokenPersonal.Prefix() + "personal-token"
	session := domain.AccessTokenSession.Prefix() + "session-token"
	roles := []authz.Role{authz.RoleUser}
	errStore := errors.New("store unreachable")

	tests := []struct {
		name        string
		plain       string
		stored      *domain.AccessTokenWithUser
		findErr     error
		wantErr     error
		wantTouched bool
	}{
		{"personal token, never used", personal,
			withUser(domain.AccessTokenPersonal, true, nil, nil, nil), nil, nil, true},
		{"session, never used", session,
			withUser(domain.AccessTokenSession, true, nil, nil, nil), nil, nil, true},
		{"used a moment ago", personal,
			withUser(domain.AccessTokenPersonal, true, new(now.Add(-time.Second)), nil, nil), nil, nil, false},
		{"used long ago", personal,
			withUser(domain.AccessTokenPersonal, true, new(now.Add(-time.Hour)), nil, nil), nil, nil, true},
		{"expires later", personal,
			withUser(domain.AccessTokenPersonal, true, nil, new(now.Add(time.Hour)), nil), nil, nil, true},
		{"unknown", personal, nil, nil, domain.ErrInvalidAccessToken, false},
		{"no known prefix", "nope_token",
			withUser(domain.AccessTokenPersonal, true, nil, nil, nil), nil, domain.ErrInvalidAccessToken, false},
		{"revoked", personal,
			withUser(domain.AccessTokenPersonal, true, nil, nil, new(now.Add(-time.Minute))), nil,
			domain.ErrInvalidAccessToken, false},
		{"expired", personal,
			withUser(domain.AccessTokenPersonal, true, nil, new(now.Add(-time.Minute)), nil), nil,
			domain.ErrInvalidAccessToken, false},
		{"another kind than its prefix", personal,
			withUser(domain.AccessTokenSession, true, nil, nil, nil), nil, domain.ErrInvalidAccessToken, false},
		{"inactive user", personal,
			withUser(domain.AccessTokenPersonal, false, nil, nil, nil), nil, domain.ErrInvalidAccessToken, false},
		{"failed read", personal, nil, errStore, errStore, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &checkTokenRepo{byHash: map[string]*domain.AccessTokenWithUser{}, findErr: tt.findErr}
			if tt.stored != nil {
				tt.stored.Token.UserID = userID
				tt.stored.Roles = roles
				repo.byHash[hashToken(tt.plain)] = tt.stored
			}
			users := &separateUserReads{}
			svc := NewTokenService(repo, users, nil)

			gotUser, gotRoles, err := svc.Check(context.Background(), tt.plain)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("Check = %s, %v, %v; want %v", gotUser, gotRoles, err, tt.wantErr)
				}
			case err != nil || gotUser != userID || !slices.Equal(gotRoles, roles):
				t.Errorf("Check = %s, %v, %v; want %s with %v", gotUser, gotRoles, err, userID, roles)
			}
			if touched := len(repo.touched) > 0; touched != tt.wantTouched {
				t.Errorf("last use recorded = %v, want %v", touched, tt.wantTouched)
			}
			if repo.separateReads > 0 || users.calls > 0 {
				t.Errorf("Check read the token %d and the user %d times on their own; want one read of the token with "+
					"its user", repo.separateReads, users.calls)
			}
		})
	}
}

// withUser returns a stored token of kind with its user's active flag, last use, expiry and revocation.
func withUser(
	kind domain.AccessTokenKind, active bool, lastUsedAt, expiresAt, revokedAt *time.Time,
) *domain.AccessTokenWithUser {
	return &domain.AccessTokenWithUser{
		Token: &domain.AccessToken{
			ID: uuid.New(), Kind: kind, LastUsedAt: lastUsedAt, ExpiresAt: expiresAt, RevokedAt: revokedAt,
		},
		UserActive: active,
	}
}

// checkTokenRepo serves tokens with their user by hash, or fails every such read with findErr when set, counts the
// reads of a token on its own, and records the uses written. Check calls none of the methods left to the embedded nil
// interface.
type checkTokenRepo struct {
	accessTokenRepository
	byHash        map[string]*domain.AccessTokenWithUser
	findErr       error
	separateReads int
	touched       []uuid.UUID
}

func (r *checkTokenRepo) FindWithUserByHash(_ context.Context, hash string) (*domain.AccessTokenWithUser, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	found, ok := r.byHash[hash]
	if !ok {
		return nil, domain.ErrAccessTokenNotFound
	}
	return found, nil
}

func (r *checkTokenRepo) FindByHash(context.Context, string) (*domain.AccessToken, error) {
	r.separateReads++
	return nil, domain.ErrAccessTokenNotFound
}

func (r *checkTokenRepo) TouchLastUsed(_ context.Context, id uuid.UUID, _ time.Time) error {
	r.touched = append(r.touched, id)
	return nil
}

// separateUserReads counts the reads of a token's user on its own, finding none.
type separateUserReads struct{ calls int }

func (u *separateUserReads) FindByID(context.Context, uuid.UUID) (*domain.User, error) {
	u.calls++
	return nil, domain.ErrUserNotFound
}
