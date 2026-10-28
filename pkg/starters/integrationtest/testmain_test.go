//go:build integration

package integrationtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	bdbpg "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/postgres"
	bdbsqlite "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/sqlite"
	bdbsdomain "github.com/alexandremgo/baguettedb/pkg/bdbs/domain"
	bdbmigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations"
	bdbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/bdbs/migrations/sqlite"
	bdbsservice "github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/database"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	dbpg "github.com/alexandremgo/baguettedb/pkg/databases/adapters/postgres"
	dbsqlite "github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlite"
	dbmigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations"
	dbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations/sqlite"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
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

	// Starts the test JWKS server: the databases router used by the HTTP-adapter tests requires a token
	// validator even though the internal routes authenticate with the shared secret.
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

// bdbReader is the part of the bdb repository the tests read stored bdbs through.
type bdbReader interface {
	FindByID(ctx context.Context, id uuid.UUID) (*bdbsdomain.Bdb, error)
	ListByUser(ctx context.Context, userID uuid.UUID) ([]*bdbsdomain.Bdb, error)
}

// newBdbStack returns the bdb service, the membership checker and a reader of stored bdbs, all over the bdb
// repository of the instance store the suite runs on.
func newBdbStack(
	authApp bdbsservice.AuthApp, limits quota.Resolver,
) (*bdbsservice.BdbService, *bdbsservice.BdbAuthChecker, bdbReader) {
	if testDB.SQLite != nil {
		repo := bdbsqlite.NewBdbRepo(testDB.SQLite)
		return bdbsservice.NewBdbService(repo, membershipTx(), authApp, limits, testLogger),
			bdbsservice.NewBdbAuthChecker(repo, authApp), repo
	}
	repo := bdbpg.NewBdbRepo(testDB.Pool)
	return bdbsservice.NewBdbService(repo, membershipTx(), authApp, limits, testLogger),
		bdbsservice.NewBdbAuthChecker(repo, authApp), repo
}

// newDatabaseRepo returns the database metadata repository of the instance store the suite runs on.
func newDatabaseRepo() dbservice.DatabaseRepository {
	if testDB.SQLite != nil {
		return dbsqlite.NewDatabaseRepo(testDB.SQLite)
	}
	return dbpg.NewDatabaseRepo(testDB.Pool)
}

// newExportRepo returns the export audit repository of the instance store the suite runs on.
func newExportRepo() dbservice.ExportRepository {
	if testDB.SQLite != nil {
		return dbsqlite.NewExportRepo(testDB.SQLite)
	}
	return dbpg.NewExportRepo(testDB.Pool)
}

// membershipTx returns the member-change transaction over the instance store the suite runs on, composed as the
// bdbs app's setup composes it.
func membershipTx() bdbsservice.RunInTx[bdbsservice.MembershipTx] {
	if testDB.SQLite != nil {
		return database.SQLiteInTx(testDB.SQLite, func(tx *sql.Tx) bdbsservice.MembershipTx {
			m := bdbsqlite.NewMembershipTxRepo(tx)
			return bdbsservice.MembershipTx{Bdbs: m, Members: m}
		})
	}
	return database.PostgresInTx(testDB.Pool, func(tx pgx.Tx) bdbsservice.MembershipTx {
		m := bdbpg.NewMembershipTxRepo(tx)
		return bdbsservice.MembershipTx{Bdbs: m, Members: m}
	})
}
