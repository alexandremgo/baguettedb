//go:build integration

package integrationtest

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/postgres"
	dbsqlite "github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbmigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations"
	dbsqlitemigrations "github.com/alexandremgo/baguettedb/pkg/databases/migrations/sqlite"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

var (
	testDB     *testutil.InstanceDB
	testJWKS   *testutil.TestJWKS
	testLogger zerolog.Logger

	// testS3Endpoint is the S3 endpoint of the SeaweedFS container, shared by all snapshot-storage tests.
	testS3Endpoint string
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	testLogger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).With().Timestamp().Logger()

	db, closeDB, err := testutil.OpenInstanceDB(ctx, testLogger, testutil.Migrations{
		App: "databases", Postgres: dbmigrations.FS, SQLite: dbsqlitemigrations.FS,
	})
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

	// Starts SeaweedFS testcontainer (snapshot object storage) and prepares the shared bucket.
	s3Endpoint, s3Cleanup, err := testutil.NewSeaweedFSContainer(ctx)
	if err != nil {
		jwks.Close()
		closeDB()
		fmt.Fprintf(os.Stderr, "starting seaweedfs container: %v\n", err)
		os.Exit(1)
	}
	testS3Endpoint = s3Endpoint
	if err := setupS3Bucket(ctx); err != nil {
		s3Cleanup()
		jwks.Close()
		closeDB()
		fmt.Fprintf(os.Stderr, "setting up s3 test bucket: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	s3Cleanup()
	jwks.Close()
	closeDB()
	os.Exit(code)
}

// setupAdapterSQLiteClient creates a fresh sqlitelocal.Client backed by a
// temp data dir and a freshly-created database, with a constant DEK and no
// Postgres/KEK dependency. Use this for tests that exercise the adapter
// directly, bypassing the service layer. For full service-stack tests that
// go through Postgres + the KEK provider, use newTestSQLiteClient
// (rest_test.go) instead.
func setupAdapterSQLiteClient(t *testing.T) (*sqlitelocal.Client, string) {
	t.Helper()
	dataDir := t.TempDir()
	dek := make([]byte, sqlitelocal.KeySize)
	if _, err := rand.Read(dek); err != nil {
		t.Fatalf("generating DEK: %v", err)
	}
	client, err := sqlitelocal.NewClient(sqlitelocal.Config{
		DataDir:         dataDir,
		CacheTTL:        30 * time.Minute,
		CacheMaxEntries: 10,
		CacheSweepEvery: 5 * time.Minute,
		DEKResolver: func(_ context.Context, _ string) ([]byte, error) {
			return dek, nil
		},
		Logger: testLogger,
	})
	if err != nil {
		t.Fatalf("creating sqlitelocal client: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Logf("closing sqlitelocal client: %v", err)
		}
	})

	databaseID := uuid.New().String()
	if err := client.CreateDatabase(context.Background(), databaseID, dek); err != nil {
		t.Fatalf("creating database: %v", err)
	}

	return client, databaseID
}

// truncateAll removes all data from databases tables, preserving the schema.
func truncateAll(t *testing.T) {
	t.Helper()
	if err := testDB.DeleteAll(context.Background(), "databases", "database_exports"); err != nil {
		t.Fatalf("truncating tables: %v", err)
	}
}

// newDatabaseRepo returns the database metadata repository of the instance store the suite runs on.
func newDatabaseRepo() service.DatabaseRepository {
	if testDB.SQLite != nil {
		return dbsqlite.NewDatabaseRepo(testDB.SQLite)
	}
	return postgres.NewDatabaseRepo(testDB.Pool)
}

// newExportRepo returns the export audit repository of the instance store the suite runs on.
func newExportRepo() service.ExportRepository {
	if testDB.SQLite != nil {
		return dbsqlite.NewExportRepo(testDB.SQLite)
	}
	return postgres.NewExportRepo(testDB.Pool)
}

// activeColorRepo is the fleet-color part of a database repository: the deploy tooling flips it, no service does.
type activeColorRepo interface {
	GetActiveColor(ctx context.Context) (int16, error)
	SetActiveColor(ctx context.Context, color int16) error
}

// newActiveColorRepo returns the fleet-color repository of the instance store the suite runs on.
func newActiveColorRepo() activeColorRepo {
	if testDB.SQLite != nil {
		return dbsqlite.NewDatabaseRepo(testDB.SQLite)
	}
	return postgres.NewDatabaseRepo(testDB.Pool)
}
