//go:build integration

package integrationtest

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestTokenService_DataAPIKeys checks a Data API key's life through the token service: created bound to its bdb, with
// its prefix kept and no expiry, and listed by that bdb alone, newest first. Check, which takes the tokens every router
// accepts, refuses it; the personal-token list and revoke don't reach it; and it is revoked one by one, by member, or
// with all of its bdb's, leaving the user's personal token working.
func TestTokenService_DataAPIKeys(t *testing.T) {
	truncateAll(t)
	setupRouter(t)
	ctx := context.Background()
	tokens := newTokenService()
	owner := uuid.MustParse(createUser(t, "owner@example.com", "Str0ng-Passw0rd!"))
	member := uuid.MustParse(createUser(t, "member@example.com", "Str0ng-Passw0rd!"))
	bdbID, otherBdbID := uuid.New(), uuid.New()

	plain, key, err := tokens.CreateDataAPIKey(ctx, owner, bdbID, "  finance app ")
	if err != nil {
		t.Fatalf("CreateDataAPIKey = %v", err)
	}
	prefix := domain.AccessTokenDataAPIKey.Prefix()
	if !strings.HasPrefix(plain, prefix) || key.Kind != domain.AccessTokenDataAPIKey || key.Name != "finance app" ||
		key.BdbID == nil || *key.BdbID != bdbID || key.ExpiresAt != nil || !strings.HasPrefix(plain[len(prefix):],
		key.TokenPrefix) || len(key.TokenPrefix) != domain.AccessTokenPrefixLen {
		t.Fatalf("CreateDataAPIKey = %q, %+v; want a bdbdk_ key named %q, bound to the bdb, its prefix kept, "+
			"never expiring", plain, key, "finance app")
	}
	_, memberKey, err := tokens.CreateDataAPIKey(ctx, member, bdbID, "member app")
	if err != nil {
		t.Fatalf("CreateDataAPIKey for the member = %v", err)
	}
	_, otherKey, err := tokens.CreateDataAPIKey(ctx, owner, otherBdbID, "other app")
	if err != nil {
		t.Fatalf("CreateDataAPIKey for another bdb = %v", err)
	}
	pat, personal, err := tokens.Create(ctx, owner, "laptop", nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}

	listKeys := func(bdb uuid.UUID) []uuid.UUID {
		t.Helper()
		keys, err := tokens.ListDataAPIKeys(ctx, bdb)
		if err != nil {
			t.Fatalf("ListDataAPIKeys = %v", err)
		}
		ids := make([]uuid.UUID, len(keys))
		for i, k := range keys {
			ids[i] = k.ID
		}
		return ids
	}
	if got := listKeys(bdbID); !slices.Equal(got, []uuid.UUID{memberKey.ID, key.ID}) {
		t.Fatalf("ListDataAPIKeys = %v, want the bdb's two keys, newest first", got)
	}

	if _, _, err := tokens.Check(ctx, plain); !errors.Is(err, domain.ErrInvalidAccessToken) {
		t.Errorf("Check of a Data API key = %v, want ErrInvalidAccessToken", err)
	}
	if listed, err := tokens.List(ctx, owner); err != nil || len(listed) != 1 || listed[0].ID != personal.ID {
		t.Errorf("List = %v, %v; want the personal token alone", listed, err)
	}
	if err := tokens.Revoke(ctx, owner, key.ID); !errors.Is(err, domain.ErrAccessTokenNotFound) {
		t.Errorf("personal-token Revoke of a Data API key = %v, want ErrAccessTokenNotFound", err)
	}
	if err := tokens.RevokeDataAPIKey(ctx, otherBdbID, key.ID); !errors.Is(err, domain.ErrAccessTokenNotFound) {
		t.Errorf("RevokeDataAPIKey from another bdb = %v, want ErrAccessTokenNotFound", err)
	}

	if err := tokens.RevokeMemberDataAPIKeys(ctx, bdbID, member); err != nil {
		t.Fatalf("RevokeMemberDataAPIKeys = %v", err)
	}
	if got := listKeys(bdbID); !slices.Equal(got, []uuid.UUID{key.ID}) {
		t.Errorf("ListDataAPIKeys after RevokeMemberDataAPIKeys = %v, want the owner's key alone", got)
	}
	if err := tokens.RevokeDataAPIKey(ctx, bdbID, key.ID); err != nil {
		t.Fatalf("RevokeDataAPIKey = %v", err)
	}
	if err := tokens.RevokeDataAPIKey(ctx, bdbID, key.ID); !errors.Is(err, domain.ErrAccessTokenNotFound) {
		t.Errorf("second RevokeDataAPIKey = %v, want ErrAccessTokenNotFound", err)
	}
	if got := listKeys(bdbID); len(got) != 0 {
		t.Errorf("ListDataAPIKeys after revoking every key = %v, want none", got)
	}

	if err := tokens.RevokeDataAPIKeys(ctx, otherBdbID); err != nil {
		t.Fatalf("RevokeDataAPIKeys = %v", err)
	}
	if got := listKeys(otherBdbID); len(got) != 0 {
		t.Errorf("ListDataAPIKeys after RevokeDataAPIKeys = %v, want none, not %s", got, otherKey.ID)
	}
	if gotUser, _, err := tokens.Check(ctx, pat); err != nil || gotUser != owner {
		t.Errorf("Check of the personal token after revoking the Data API keys = %s, %v; want the owner", gotUser, err)
	}
}

// TestTokenService_CheckDataAPIKey checks that a Data API key authenticates the user who created it, with their roles,
// and tells which key it is and which bdb it is bound to, recording the key's first use.
func TestTokenService_CheckDataAPIKey(t *testing.T) {
	truncateAll(t)
	setupRouter(t)
	ctx := context.Background()
	tokens := newTokenService()
	owner := uuid.MustParse(seedAdmin(t, "keys@example.com", "Str0ng-Passw0rd!"))
	bdbID := uuid.New()

	plain, key, err := tokens.CreateDataAPIKey(ctx, owner, bdbID, "app")
	if err != nil {
		t.Fatalf("CreateDataAPIKey = %v", err)
	}

	got, err := tokens.CheckDataAPIKey(ctx, plain)
	if err != nil {
		t.Fatalf("CheckDataAPIKey = %v, want the key's identity", err)
	}
	if got.UserID != owner || got.KeyID != key.ID || got.BdbID != bdbID ||
		!slices.Contains(got.Roles, authz.RoleAdmin) {
		t.Fatalf("CheckDataAPIKey = %+v; want user %s with the admin role, key %s, bdb %s", got, owner, key.ID, bdbID)
	}
	if keys, err := tokens.ListDataAPIKeys(ctx, bdbID); err != nil || len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Errorf("ListDataAPIKeys after CheckDataAPIKey = %v, %v; want the key with its last use recorded", keys, err)
	}
}

// TestTokenService_CheckDataAPIKeyRefuses checks that CheckDataAPIKey refuses what isn't a valid Data API key, with one
// error for all: a personal token and a session of the key's own user, a JWT, an unknown key, a revoked one, and a key
// of an inactive user.
func TestTokenService_CheckDataAPIKeyRefuses(t *testing.T) {
	truncateAll(t)
	setupRouter(t)
	ctx := context.Background()
	tokens := newTokenService()
	const password = "Str0ng-Passw0rd!"
	userID := uuid.MustParse(createUser(t, "refused-keys@example.com", password))
	bdbID := uuid.New()

	pat, _, err := tokens.Create(ctx, userID, "laptop", nil)
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	session, _, err := tokens.SignIn(ctx, "refused-keys@example.com", password)
	if err != nil {
		t.Fatalf("SignIn = %v", err)
	}
	revoked, revokedKey, err := tokens.CreateDataAPIKey(ctx, userID, bdbID, "revoked")
	if err != nil {
		t.Fatalf("CreateDataAPIKey = %v", err)
	}
	if err := tokens.RevokeDataAPIKey(ctx, bdbID, revokedKey.ID); err != nil {
		t.Fatalf("RevokeDataAPIKey = %v", err)
	}
	ofInactive, _, err := tokens.CreateDataAPIKey(ctx, userID, bdbID, "inactive")
	if err != nil {
		t.Fatalf("CreateDataAPIKey = %v", err)
	}

	for _, tt := range []struct{ name, plain string }{
		{"a personal token", pat},
		{"a session", session},
		{"a JWT", "eyJhbGciOiJSUzI1NiJ9.e30.sig"},
		{"an unknown key", domain.AccessTokenDataAPIKey.Prefix() + "unknown"},
		{"a revoked key", revoked},
	} {
		if _, err := tokens.CheckDataAPIKey(ctx, tt.plain); !errors.Is(err, domain.ErrInvalidAccessToken) {
			t.Errorf("CheckDataAPIKey of %s = %v, want ErrInvalidAccessToken", tt.name, err)
		}
	}

	if err := testDB.Exec(ctx, `UPDATE users SET is_active = $1 WHERE id = $2`, false, userID); err != nil {
		t.Fatalf("deactivating the user = %v", err)
	}
	if _, err := tokens.CheckDataAPIKey(ctx, ofInactive); !errors.Is(err, domain.ErrInvalidAccessToken) {
		t.Errorf("CheckDataAPIKey of an inactive user's key = %v, want ErrInvalidAccessToken", err)
	}
}

// TestTokenService_CreateDataAPIKeyRefuses checks that CreateDataAPIKey refuses an empty or too long name, and an
// unknown user.
func TestTokenService_CreateDataAPIKeyRefuses(t *testing.T) {
	truncateAll(t)
	setupRouter(t)
	ctx := context.Background()
	tokens := newTokenService()
	userID := uuid.MustParse(createUser(t, "key-names@example.com", "Str0ng-Passw0rd!"))

	for _, name := range []string{" ", strings.Repeat("x", domain.AccessTokenNameMaxLen+1)} {
		if _, _, err := tokens.CreateDataAPIKey(ctx, userID, uuid.New(), name); !errors.Is(err,
			domain.ErrInvalidAccessTokenName) {
			t.Errorf("CreateDataAPIKey named %q = %v, want ErrInvalidAccessTokenName", name, err)
		}
	}
	_, _, err := tokens.CreateDataAPIKey(ctx, uuid.New(), uuid.New(), "app")
	if !errors.Is(err, domain.ErrUserNotFound) {
		t.Errorf("CreateDataAPIKey for an unknown user = %v, want ErrUserNotFound", err)
	}
}

// TestInternalDataAPIKeys checks the internal Data API key endpoints. Creating a key for a user and a bdb returns it
// once with its value; a bad name is a 422, an unknown user a 404, a malformed ID a 400. Listing returns that bdb's
// keys alone, never a value. Revoking one is a 204, then a 404; revoking a member's keys or all of a bdb's leaves only
// what each doesn't cover.
func TestInternalDataAPIKeys(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	owner := createUser(t, "internal-owner@example.com", "Str0ng-Passw0rd!")
	member := createUser(t, "internal-member@example.com", "Str0ng-Passw0rd!")
	bdbID, otherBdbID := uuid.NewString(), uuid.NewString()

	post := func(path string, body map[string]any) (int, map[string]any) {
		t.Helper()
		return testutil.DoRequest(t, router, http.MethodPost, "/internal/api/auth/v1/data-api-keys"+path, "", body)
	}
	create := func(userID, bdb, name string) map[string]any {
		t.Helper()
		status, resp := post("/create", map[string]any{"user_id": userID, "bdb_id": bdb, "name": name})
		if status != http.StatusCreated {
			t.Fatalf("create = %d %v, want 201", status, resp)
		}
		return resp
	}
	listIDs := func(bdb string) []string {
		t.Helper()
		status, resp := post("", map[string]any{"bdb_id": bdb})
		if status != http.StatusOK {
			t.Fatalf("list = %d %v, want 200", status, resp)
		}
		keys, _ := resp["keys"].([]any)
		ids := make([]string, len(keys))
		for i, k := range keys {
			entry, _ := k.(map[string]any)
			if _, hasValue := entry["value"]; hasValue {
				t.Errorf("listed key %v carries its value", entry)
			}
			ids[i], _ = entry["id"].(string)
		}
		return ids
	}

	created := create(owner, bdbID, "finance app")
	value, _ := created["value"].(string)
	key, _ := created["key"].(map[string]any)
	if !strings.HasPrefix(value, domain.AccessTokenDataAPIKey.Prefix()) || key["bdb_id"] != bdbID ||
		key["user_id"] != owner || key["name"] != "finance app" || key["token_prefix"] == "" {
		t.Fatalf("create = %v; want a bdbdk_ value and the key bound to the bdb, made by the owner", created)
	}
	ownerKeyID, _ := key["id"].(string)
	memberKey, _ := create(member, bdbID, "member app")["key"].(map[string]any)
	memberKeyID, _ := memberKey["id"].(string)
	otherKey, _ := create(owner, otherBdbID, "other app")["key"].(map[string]any)
	otherKeyID, _ := otherKey["id"].(string)

	for _, tt := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"an empty name", map[string]any{"user_id": owner, "bdb_id": bdbID, "name": " "},
			http.StatusUnprocessableEntity},
		{"an unknown user", map[string]any{"user_id": uuid.NewString(), "bdb_id": bdbID, "name": "app"},
			http.StatusNotFound},
		{"a malformed bdb ID", map[string]any{"user_id": owner, "bdb_id": "nope", "name": "app"},
			http.StatusBadRequest},
	} {
		if status, resp := post("/create", tt.body); status != tt.want {
			t.Errorf("create with %s = %d %v, want %d", tt.name, status, resp, tt.want)
		}
	}

	if got := listIDs(bdbID); !slices.Equal(got, []string{memberKeyID, ownerKeyID}) {
		t.Fatalf("list = %v, want the bdb's two keys, newest first", got)
	}

	if status, _ := post("/revoke-member", map[string]any{"bdb_id": bdbID, "user_id": member}); status !=
		http.StatusNoContent {
		t.Fatalf("revoke-member = %d, want 204", status)
	}
	if got := listIDs(bdbID); !slices.Equal(got, []string{ownerKeyID}) {
		t.Errorf("list after revoke-member = %v, want the owner's key alone", got)
	}
	if status, _ := post("/revoke", map[string]any{"bdb_id": otherBdbID, "key_id": ownerKeyID}); status !=
		http.StatusNotFound {
		t.Errorf("revoke from another bdb = %d, want 404", status)
	}
	if status, _ := post("/revoke", map[string]any{"bdb_id": bdbID, "key_id": ownerKeyID}); status !=
		http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", status)
	}
	if status, _ := post("/revoke", map[string]any{"bdb_id": bdbID, "key_id": ownerKeyID}); status !=
		http.StatusNotFound {
		t.Errorf("second revoke = %d, want 404", status)
	}

	if status, _ := post("/revoke-all", map[string]any{"bdb_id": otherBdbID}); status != http.StatusNoContent {
		t.Fatalf("revoke-all = %d, want 204", status)
	}
	if got := listIDs(otherBdbID); len(got) != 0 {
		t.Errorf("list after revoke-all = %v, want none, not %s", got, otherKeyID)
	}
}
