//go:build integration

package integrationtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/auth/adapters/postgres"
	authsqlite "github.com/alexandremgo/baguettedb/pkg/auth/adapters/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/auth/domain"
	authmigrations "github.com/alexandremgo/baguettedb/pkg/auth/migrations"
	authsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/auth/migrations/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/auth/service"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

var (
	testDB     *testutil.InstanceDB
	testJWKS   *testutil.TestJWKS
	testLogger zerolog.Logger
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	testLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	db, closeDB, err := testutil.OpenInstanceDB(ctx, testLogger, testutil.Migrations{
		App: "auth", Postgres: authmigrations.FS, SQLite: authsqlitemigrations.FS,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening the instance store: %v\n", err)
		os.Exit(1)
	}
	testDB = db

	jwks, err := testutil.NewTestJWKS()
	if err != nil {
		closeDB()
		fmt.Fprintf(os.Stderr, "creating test JWKS: %v\n", err)
		os.Exit(1)
	}
	testJWKS = jwks

	code := m.Run()
	jwks.Close()
	closeDB()
	os.Exit(code)
}

// testRepos holds the auth repositories over the instance store the suite runs on, and the transactions of the
// services that write through several of them, composed as the app's setup composes them.
type testRepos struct {
	users      service.UserRepository
	roles      service.RoleRepository
	oidcStates service.OIDCStateRepository
	identities identityCreator
	userTx     service.RunInTx[service.UserTx]
	identityTx service.RunInTx[service.IdentityTx]
	ownerTx    service.RunInTx[service.OwnerTx]
}

// identityCreator links a user to an external identity provider subject.
type identityCreator interface {
	Create(ctx context.Context, identity *domain.ExternalIdentity) error
}

// repos returns the auth repositories over the instance store the suite runs on.
func repos() testRepos {
	if db := testDB.SQLite; db != nil {
		return testRepos{
			users:      authsqlite.NewUserRepo(db),
			roles:      authsqlite.NewRoleRepo(db),
			oidcStates: authsqlite.NewOIDCStateRepo(db),
			identities: authsqlite.NewExternalIdentityRepo(db),
			userTx: database.SQLiteInTx(db, func(tx *sql.Tx) service.UserTx {
				return service.UserTx{
					Users:    authsqlite.NewUserRepo(tx),
					Roles:    authsqlite.NewRoleRepo(tx),
					Sessions: authsqlite.NewAccessTokenRepo(tx),
				}
			}),
			identityTx: database.SQLiteInTx(db, func(tx *sql.Tx) service.IdentityTx {
				return service.IdentityTx{
					Identities: authsqlite.NewExternalIdentityRepo(tx),
					Users:      authsqlite.NewUserRepo(tx),
				}
			}),
			ownerTx: database.SQLiteInTx(db, func(tx *sql.Tx) service.OwnerTx {
				return service.OwnerTx{
					Owner: authsqlite.NewInstanceOwnerRepo(tx),
					Users: authsqlite.NewUserRepo(tx),
					Roles: authsqlite.NewRoleRepo(tx),
				}
			}),
		}
	}
	pool := testDB.Pool
	return testRepos{
		users:      postgres.NewUserRepo(pool),
		roles:      postgres.NewRoleRepo(pool),
		oidcStates: postgres.NewOIDCStateRepo(pool),
		identities: postgres.NewExternalIdentityRepo(pool),
		userTx: database.PostgresInTx(pool, func(tx pgx.Tx) service.UserTx {
			return service.UserTx{
				Users:    postgres.NewUserRepo(tx),
				Roles:    postgres.NewRoleRepo(tx),
				Sessions: postgres.NewAccessTokenRepo(tx),
			}
		}),
		identityTx: database.PostgresInTx(pool, func(tx pgx.Tx) service.IdentityTx {
			return service.IdentityTx{
				Identities: postgres.NewExternalIdentityRepo(tx),
				Users:      postgres.NewUserRepo(tx),
			}
		}),
		ownerTx: database.PostgresInTx(pool, func(tx pgx.Tx) service.OwnerTx {
			return service.OwnerTx{
				Owner: postgres.NewInstanceOwnerRepo(tx),
				Users: postgres.NewUserRepo(tx),
				Roles: postgres.NewRoleRepo(tx),
			}
		}),
	}
}

// truncateAll removes all rows from auth tables that integration tests write to. Preserves schema.
func truncateAll(t *testing.T) {
	t.Helper()
	err := testDB.DeleteAll(context.Background(),
		"access_tokens", "instance_owner", "user_roles", "external_identities", "oidc_state", "users")
	if err != nil {
		t.Fatalf("truncating tables: %v", err)
	}
}
