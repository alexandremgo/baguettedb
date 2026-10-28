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

	bdbpg "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/postgres"
	bdbsqlite "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/sqlite"
	bdbmigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations"
	bdbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations/sqlite"
	bdbsvc "github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	dbpostgres "github.com/alexandremgo/baguettedb/pkg/databases/adapters/postgres"
	dbsqlite "github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlite"
	dbmigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations"
	dbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations/sqlite"
	dbsvc "github.com/alexandremgo/baguettedb/pkg/databases/service"
)

var (
	testDB     *testutil.InstanceDB
	testJWKS   *testutil.TestJWKS
	testLogger zerolog.Logger
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	testLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	// Both apps' migrations run against the same test store; each app tracks its own migration table, and their
	// table sets are disjoint.
	db, closeDB, err := testutil.OpenInstanceDB(ctx, testLogger,
		testutil.Migrations{App: "bdbs", Postgres: bdbmigrations.FS, SQLite: bdbsqlitemigrations.FS},
		testutil.Migrations{App: "databases", Postgres: dbmigrations.FS, SQLite: dbsqlitemigrations.FS},
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening the instance store: %v\n", err)
		os.Exit(1)
	}
	testDB = db

	// Starts test JWKS server.
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

// truncateAll removes all data from both bdbs and databases tables.
func truncateAll(t *testing.T) {
	t.Helper()
	if err := testDB.DeleteAll(context.Background(), "bdb_members", "bdbs", "databases"); err != nil {
		t.Fatalf("truncating tables: %v", err)
	}
}

// newBdbService returns a BdbService over the bdb repository of the instance store the suite runs on.
func newBdbService(authApp bdbsvc.AuthApp, limits quota.Resolver) *bdbsvc.BdbService {
	if testDB.SQLite != nil {
		return bdbsvc.NewBdbService(bdbsqlite.NewBdbRepo(testDB.SQLite), membershipTx(), authApp, limits, testLogger)
	}
	return bdbsvc.NewBdbService(bdbpg.NewBdbRepo(testDB.Pool), membershipTx(), authApp, limits, testLogger)
}

// newDatabaseRepo returns the database metadata repository of the instance store the suite runs on.
func newDatabaseRepo() dbsvc.DatabaseRepository {
	if testDB.SQLite != nil {
		return dbsqlite.NewDatabaseRepo(testDB.SQLite)
	}
	return dbpostgres.NewDatabaseRepo(testDB.Pool)
}

// membershipTx returns the member-change transaction over the instance store the suite runs on, composed as the
// bdbs app's setup composes it.
func membershipTx() bdbsvc.RunInTx[bdbsvc.MembershipTx] {
	if testDB.SQLite != nil {
		return database.SQLiteInTx(testDB.SQLite, func(tx *sql.Tx) bdbsvc.MembershipTx {
			m := bdbsqlite.NewMembershipTxRepo(tx)
			return bdbsvc.MembershipTx{Bdbs: m, Members: m}
		})
	}
	return database.PostgresInTx(testDB.Pool, func(tx pgx.Tx) bdbsvc.MembershipTx {
		m := bdbpg.NewMembershipTxRepo(tx)
		return bdbsvc.MembershipTx{Bdbs: m, Members: m}
	})
}
