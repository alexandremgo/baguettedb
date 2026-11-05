//go:build integration

package integrationtest

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"

	authsqlite "github.com/alexandremgo/baguettedb/pkg/auth/adapters/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	authsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/auth/migrations/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
)

// TestSQLiteMigration_DataAPIKeysKeepsTokens checks that the SQLite migration adding Data API keys, which rebuilds the
// access_tokens table, keeps the tokens stored before it: a personal token reads back with every column and bound to no
// bdb. The rebuilt table keeps both indexes, takes a Data API key bound to a bdb, and refuses a Data API key bound to
// none and a personal token bound to one.
//
// The test migrates a file of its own from the first migration, whatever instance store the suite runs on.
func TestSQLiteMigration_DataAPIKeysKeepsTokens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "auth.db")

	first, err := fs.ReadFile(authsqlitemigrations.FS, "000001_init_auth.up.sql")
	if err != nil {
		t.Fatalf("reading the first migration: %v", err)
	}
	firstOnly := fstest.MapFS{"000001_init_auth.up.sql": {Data: first}}
	if err := database.RunSQLiteMigrations(ctx, path, firstOnly, "auth", testLogger); err != nil {
		t.Fatalf("running the first migration: %v", err)
	}

	userID, tokenID := uuid.New(), uuid.New()
	createdAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	lastUsedAt := createdAt.Add(time.Minute)
	db, err := database.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO users (id, email, created_at, updated_at) VALUES ($1, $2, $3, $3)`,
		userID, "before@example.com", createdAt,
	); err != nil {
		t.Fatalf("inserting a user: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO access_tokens (id, user_id, kind, name, token_prefix, token_hash, created_at, last_used_at)
		 VALUES ($1, $2, 'personal', 'laptop', 'abcdefgh', 'hash-before', $3, $4)`,
		tokenID, userID, createdAt, lastUsedAt,
	); err != nil {
		t.Fatalf("inserting a personal token: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("closing the database: %v", err)
	}

	if err := database.RunSQLiteMigrations(ctx, path, authsqlitemigrations.FS, "auth", testLogger); err != nil {
		t.Fatalf("running every migration: %v", err)
	}
	db, err = database.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatalf("reopening the database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing the database: %v", err)
		}
	})

	got, err := authsqlite.NewAccessTokenRepo(db).FindByHash(ctx, "hash-before")
	if err != nil {
		t.Fatalf("FindByHash of the token from before = %v", err)
	}
	if got.ID != tokenID || got.UserID != userID || got.Kind != domain.AccessTokenPersonal || got.Name != "laptop" ||
		got.TokenPrefix != "abcdefgh" || !got.CreatedAt.Equal(createdAt) || got.LastUsedAt == nil ||
		!got.LastUsedAt.Equal(lastUsedAt) || got.ExpiresAt != nil || got.RevokedAt != nil || got.BdbID != nil {
		t.Errorf("token from before = %+v; want it unchanged and bound to no bdb", got)
	}

	var indexes int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master
		 WHERE type = 'index' AND name IN ('idx_access_tokens_user_id', 'idx_access_tokens_bdb_id')`,
	).Scan(&indexes); err != nil || indexes != 2 {
		t.Errorf("access_tokens indexes = %d, %v; want both", indexes, err)
	}

	insert := func(kind string, bdbID any) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO access_tokens (id, user_id, kind, name, token_prefix, token_hash, created_at, bdb_id)
			 VALUES ($1, $2, $3, 'app', 'ijklmnop', $4, $5, $6)`,
			uuid.New(), userID, kind, uuid.NewString(), createdAt, bdbID,
		)
		return err
	}
	if err := insert("data_api_key", uuid.New()); err != nil {
		t.Errorf("inserting a Data API key bound to a bdb = %v, want it stored", err)
	}
	if err := insert("data_api_key", nil); err == nil {
		t.Error("inserting a Data API key bound to no bdb succeeded, want the CHECK to refuse it")
	}
	if err := insert("personal", uuid.New()); err == nil {
		t.Error("inserting a personal token bound to a bdb succeeded, want the CHECK to refuse it")
	}
}
