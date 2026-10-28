//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/adapters/postgres"
	authsqlite "github.com/alexandremgo/baguettedb/pkg/auth/adapters/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// newTokenService returns a TokenService over the instance store the suite runs on.
func newTokenService() *service.TokenService {
	r := repos()
	passwords := service.NewAuthenticationService(r.users)
	if testDB.SQLite != nil {
		return service.NewTokenService(authsqlite.NewAccessTokenRepo(testDB.SQLite), r.users, passwords)
	}
	return service.NewTokenService(postgres.NewAccessTokenRepo(testDB.Pool), r.users, passwords)
}

// TestTokenService_CreateListCheckRevoke checks a personal access token's life: created with its prefix and shown
// once, listed newest first, authenticating its user with their roles and recording its first use, refused once
// revoked and gone from the list, and a second revoke of it not found.
func TestTokenService_CreateListCheckRevoke(t *testing.T) {
	truncateAll(t)
	setupRouter(t)
	ctx := context.Background()
	tokens := newTokenService()
	userID := uuid.MustParse(seedAdmin(t, "tokens@example.com", "Str0ng-Passw0rd!"))

	plain, token, err := tokens.Create(ctx, userID, "  laptop scripts ", nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	prefix := domain.AccessTokenPersonal.Prefix()
	if !strings.HasPrefix(plain, prefix) || token.Name != "laptop scripts" ||
		token.Kind != domain.AccessTokenPersonal || !strings.HasPrefix(plain[len(prefix):],
		token.TokenPrefix) || len(token.TokenPrefix) != domain.AccessTokenPrefixLen || token.LastUsedAt != nil {
		t.Fatalf("Create = %q, %+v; want a bdbp_ token named %q, its prefix kept, never used", plain, token,
			"laptop scripts")
	}
	_, second, err := tokens.Create(ctx, userID, "ci", nil)
	if err != nil {
		t.Fatalf("second Create = %v", err)
	}

	listed, err := tokens.List(ctx, userID)
	if err != nil || len(listed) != 2 || listed[0].ID != second.ID || listed[1].ID != token.ID {
		t.Fatalf("List = %v, %v; want the two tokens, newest first", listed, err)
	}

	gotUser, roles, err := tokens.Check(ctx, plain)
	if err != nil || gotUser != userID || !slices.Contains(roles, authz.RoleAdmin) {
		t.Fatalf("Check = %s, %v, %v; want the user with the admin role", gotUser, roles, err)
	}
	if listed, _ = tokens.List(ctx, userID); listed[1].LastUsedAt == nil {
		t.Errorf("token after Check = %+v, want its last use recorded", listed[1])
	}

	if err := tokens.Revoke(ctx, userID, token.ID); err != nil {
		t.Fatalf("Revoke = %v", err)
	}
	if _, _, err := tokens.Check(ctx, plain); !errors.Is(err, domain.ErrInvalidAccessToken) {
		t.Errorf("Check of a revoked token = %v, want ErrInvalidAccessToken", err)
	}
	if listed, _ = tokens.List(ctx, userID); len(listed) != 1 || listed[0].ID != second.ID {
		t.Errorf("List after Revoke = %v, want the other token alone", listed)
	}
	if err := tokens.Revoke(ctx, userID, token.ID); !errors.Is(err, domain.ErrAccessTokenNotFound) {
		t.Errorf("second Revoke = %v, want ErrAccessTokenNotFound", err)
	}
}

// TestTokenService_LastUseThrottled checks that a token's last use is recorded on first use and not rewritten by a use
// within the minute after it.
func TestTokenService_LastUseThrottled(t *testing.T) {
	truncateAll(t)
	setupRouter(t)
	ctx := context.Background()
	tokens := newTokenService()
	userID := uuid.MustParse(createUser(t, "throttle@example.com", "Str0ng-Passw0rd!"))
	plain, _, err := tokens.Create(ctx, userID, "ci", nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}

	lastUse := func() time.Time {
		t.Helper()
		if _, _, err := tokens.Check(ctx, plain); err != nil {
			t.Fatalf("Check = %v", err)
		}
		listed, err := tokens.List(ctx, userID)
		if err != nil || len(listed) != 1 || listed[0].LastUsedAt == nil {
			t.Fatalf("List = %v, %v; want the token with its last use", listed, err)
		}
		return *listed[0].LastUsedAt
	}
	first := lastUse()
	if again := lastUse(); !again.Equal(first) {
		t.Errorf("last use after a second use = %v, want it kept at %v", again, first)
	}
}

// TestTokenService_CheckRefuses checks that Check refuses what authenticates no one, with one error for all: a string
// that isn't a personal token, an unknown token, an expired one, one whose stored kind isn't the one its prefix says,
// and a token of an inactive user.
func TestTokenService_CheckRefuses(t *testing.T) {
	truncateAll(t)
	setupRouter(t)
	ctx := context.Background()
	tokens := newTokenService()
	userID := uuid.MustParse(createUser(t, "refused@example.com", "Str0ng-Passw0rd!"))

	create := func(change string, args ...any) string {
		t.Helper()
		plain, token, err := tokens.Create(ctx, userID, "refused", nil)
		if err != nil {
			t.Fatalf("Create = %v", err)
		}
		if change != "" {
			if err := testDB.Exec(ctx, change, append(args, token.ID)...); err != nil {
				t.Fatalf("%s = %v", change, err)
			}
		}
		return plain
	}
	expired := create(`UPDATE access_tokens SET expires_at = $1 WHERE id = $2`, time.Now().UTC().Add(-time.Minute))
	otherKind := create(`UPDATE access_tokens SET kind = 'session' WHERE id = $1`)
	ofInactive := create("")

	for _, tt := range []struct{ name, plain string }{
		{"not a personal token", "eyJhbGciOiJSUzI1NiJ9.e30.sig"},
		{"unknown", domain.AccessTokenPersonal.Prefix() + "unknown"},
		{"expired", expired},
		{"stored as another kind", otherKind},
	} {
		if _, _, err := tokens.Check(ctx, tt.plain); !errors.Is(err, domain.ErrInvalidAccessToken) {
			t.Errorf("Check of a token %s = %v, want ErrInvalidAccessToken", tt.name, err)
		}
	}

	if err := testDB.Exec(ctx, `UPDATE users SET is_active = $1 WHERE id = $2`, false, userID); err != nil {
		t.Fatalf("deactivating the user = %v", err)
	}
	if _, _, err := tokens.Check(ctx, ofInactive); !errors.Is(err, domain.ErrInvalidAccessToken) {
		t.Errorf("Check of an inactive user's token = %v, want ErrInvalidAccessToken", err)
	}
}

// TestTokenService_CreateRefuses checks that Create refuses an empty or too long name, an expiry not in the future,
// and an unknown user.
func TestTokenService_CreateRefuses(t *testing.T) {
	truncateAll(t)
	setupRouter(t)
	ctx := context.Background()
	tokens := newTokenService()
	userID := uuid.MustParse(createUser(t, "names@example.com", "Str0ng-Passw0rd!"))

	for _, name := range []string{" ", strings.Repeat("x", domain.AccessTokenNameMaxLen+1)} {
		if _, _, err := tokens.Create(ctx, userID, name, nil); !errors.Is(err, domain.ErrInvalidAccessTokenName) {
			t.Errorf("Create named %q = %v, want ErrInvalidAccessTokenName", name, err)
		}
	}
	past := time.Now().Add(-time.Second)
	if _, _, err := tokens.Create(ctx, userID, "ci", &past); !errors.Is(err, domain.ErrInvalidAccessTokenExpiry) {
		t.Errorf("Create expiring in the past = %v, want ErrInvalidAccessTokenExpiry", err)
	}
	if _, _, err := tokens.Create(ctx, uuid.New(), "ci", nil); !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("Create for an unknown user = %v, want ErrUserNotFound", err)
	}
}
