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

	authmigrations "github.com/alexandremgo/baguettedb/pkg/auth/migrations"
	authsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/auth/migrations/sqlite"
	bdbpg "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/postgres"
	bdbsqlite "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	bdbmigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations"
	bdbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
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

	// The auth migrations are needed for SeedUser, which inserts into users/user_roles.
	db, closeDB, err := testutil.OpenInstanceDB(ctx, testLogger,
		testutil.Migrations{App: "auth", Postgres: authmigrations.FS, SQLite: authsqlitemigrations.FS},
		testutil.Migrations{App: "bdbs", Postgres: bdbmigrations.FS, SQLite: bdbsqlitemigrations.FS},
	)
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

// truncateAll removes all data from bdbs and auth tables, preserving the schema.
func truncateAll(t *testing.T) {
	t.Helper()
	err := testDB.DeleteAll(context.Background(), "bdb_invites", "bdb_members", "bdbs", "user_roles", "users")
	if err != nil {
		t.Fatalf("truncating tables: %v", err)
	}
}

// newBdbService returns a BdbService over the instance store the suite runs on, without a purge lock.
func newBdbService(authApp service.AuthApp, limits quota.Resolver) *service.BdbService {
	if testDB.SQLite != nil {
		return service.NewBdbService(bdbsqlite.NewBdbRepo(testDB.SQLite), membershipTx(), authApp, limits, testLogger)
	}
	return service.NewBdbService(bdbpg.NewBdbRepo(testDB.Pool), membershipTx(), authApp, limits, testLogger)
}

// newInviteService returns an InviteService over the instance store the suite runs on.
func newInviteService(authApp service.AuthApp, limits quota.Resolver) *service.InviteService {
	if testDB.SQLite != nil {
		return service.NewInviteService(bdbsqlite.NewInviteRepo(testDB.SQLite), bdbsqlite.NewBdbRepo(testDB.SQLite),
			inviteAcceptTx(), limits, authApp, testLogger)
	}
	return service.NewInviteService(bdbpg.NewInviteRepo(testDB.Pool), bdbpg.NewBdbRepo(testDB.Pool),
		inviteAcceptTx(), limits, authApp, testLogger)
}

// newAuthChecker returns a BdbAuthChecker over the instance store the suite runs on.
func newAuthChecker(authApp service.AuthApp) *service.BdbAuthChecker {
	if testDB.SQLite != nil {
		return service.NewBdbAuthChecker(bdbsqlite.NewBdbRepo(testDB.SQLite), authApp)
	}
	return service.NewBdbAuthChecker(bdbpg.NewBdbRepo(testDB.Pool), authApp)
}

// purgeLocker is the purge lock as the suite drives it, on either store.
type purgeLocker interface {
	TryAcquire(ctx context.Context) (acquired bool, release func(context.Context), err error)
}

// newPurgeLock returns a purge-sweep lock of the instance store the suite runs on. Two locks from separate calls
// exclude each other, as two replicas' or two processes' locks do.
func newPurgeLock() purgeLocker {
	if testDB.SQLite != nil {
		return bdbsqlite.NewPurgeLock(testDB.SQLitePath, testLogger)
	}
	return bdbpg.NewPurgeLock(testDB.Pool, testLogger)
}

// testRepos holds the repository methods the suite calls directly, on either store.
type testRepos struct {
	bdbs interface {
		CreateOwned(ctx context.Context, bdb *domain.Bdb, maxOwned int) error
	}
	invites interface {
		Create(ctx context.Context, invite *domain.Invite) error
	}
}

// repos returns the repositories of the instance store the suite runs on.
func repos() testRepos {
	if testDB.SQLite != nil {
		return testRepos{bdbs: bdbsqlite.NewBdbRepo(testDB.SQLite), invites: bdbsqlite.NewInviteRepo(testDB.SQLite)}
	}
	return testRepos{bdbs: bdbpg.NewBdbRepo(testDB.Pool), invites: bdbpg.NewInviteRepo(testDB.Pool)}
}

// membershipTx returns the member-change transaction over the instance store the suite runs on, composed as the
// bdbs app's setup composes it.
func membershipTx() service.RunInTx[service.MembershipTx] {
	if testDB.SQLite != nil {
		return database.SQLiteInTx(testDB.SQLite, func(tx *sql.Tx) service.MembershipTx {
			m := bdbsqlite.NewMembershipTxRepo(tx)
			return service.MembershipTx{Bdbs: m, Members: m}
		})
	}
	return database.PostgresInTx(testDB.Pool, func(tx pgx.Tx) service.MembershipTx {
		m := bdbpg.NewMembershipTxRepo(tx)
		return service.MembershipTx{Bdbs: m, Members: m}
	})
}

// inviteAcceptTx returns the invite-acceptance transaction over the instance store the suite runs on, composed as the
// bdbs app's setup composes it.
func inviteAcceptTx() service.RunInTx[service.InviteAcceptTx] {
	if testDB.SQLite != nil {
		return database.SQLiteInTx(testDB.SQLite, func(tx *sql.Tx) service.InviteAcceptTx {
			m := bdbsqlite.NewMembershipTxRepo(tx)
			return service.InviteAcceptTx{Bdbs: m, Members: m, Invites: m}
		})
	}
	return database.PostgresInTx(testDB.Pool, func(tx pgx.Tx) service.InviteAcceptTx {
		m := bdbpg.NewMembershipTxRepo(tx)
		return service.InviteAcceptTx{Bdbs: m, Members: m, Invites: m}
	})
}
