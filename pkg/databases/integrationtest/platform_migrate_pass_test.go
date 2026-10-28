//go:build integration

package integrationtest

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// migratePassStack groups the services needed to exercise the boot-time platform migration pass.
type migratePassStack struct {
	dbSvc        *service.DatabaseService
	querySvc     *service.QueryService
	sqliteClient *sqlitelocal.Client
}

func setupMigratePassStack(t *testing.T) *migratePassStack {
	t.Helper()
	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)
	classifier := sqlitelocal.NewClassifier()
	limits := quota.Fixed(quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	})
	dbSvc := service.NewDatabaseService(
		dbRepo, backing.sqliteClient, backing.sqliteClient, &allowAllBdbsApp{}, limits,
		backing.kekProvider, testLogger,
	)
	return &migratePassStack{
		dbSvc:        dbSvc,
		querySvc:     service.NewQueryService(dbSvc, backing.sqliteClient, classifier, limits, 100, 0),
		sqliteClient: backing.sqliteClient,
	}
}

// newRawDatabaseRecord registers a database record (wrapped DEK, version 0) and creates its file empty, without
// migrating it, so the caller controls exactly which migrations run against the fresh SQLite file.
func newRawDatabaseRecord(t *testing.T, dbRepo service.DatabaseRepository, backing *stackTestBacking) string {
	t.Helper()
	ctx := context.Background()
	dek, err := domain.GenerateDEK()
	if err != nil {
		t.Fatalf("generating DEK: %v", err)
	}
	wrapped, err := backing.kekProvider.Wrap(dek)
	if err != nil {
		t.Fatalf("wrapping DEK: %v", err)
	}
	now := time.Now().UTC()
	db := &domain.Database{
		ID:                   uuid.New(),
		BdbID:                uuid.New(),
		EncryptionKeyWrapped: wrapped,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := backing.sqliteClient.CreateDatabase(ctx, db.ID.String(), dek); err != nil {
		t.Fatalf("creating database file: %v", err)
	}
	if err := dbRepo.Create(ctx, db); err != nil {
		t.Fatalf("creating database record: %v", err)
	}
	t.Cleanup(func() {
		if err := testDB.Exec(
			context.Background(), "DELETE FROM databases WHERE id = $1", db.ID,
		); err != nil {
			t.Errorf("cleaning up database record %s: %v", db.ID, err)
		}
	})
	return db.ID.String()
}

// sqliteUserVersion reads PRAGMA user_version from the database's SQLite file.
func sqliteUserVersion(t *testing.T, c *sqlitelocal.Client, databaseID string) int {
	t.Helper()
	res, err := c.ExecuteInternal(context.Background(), databaseID, "PRAGMA user_version", nil)
	if err != nil {
		t.Fatalf("reading user_version of %s: %v", databaseID, err)
	}
	if len(res.Rows) == 0 || len(res.Rows[0]) == 0 {
		t.Fatalf("reading user_version of %s: empty result", databaseID)
	}
	v, ok := res.Rows[0][0].Value.(int64)
	if !ok {
		t.Fatalf("reading user_version of %s: unexpected value %#v", databaseID, res.Rows[0][0].Value)
	}
	return int(v)
}

// TestMigrateOwnedOnBoot_LevelsBehindDatabases verifies the boot pass migrates a behind database this pod owns
// to the current platform schema version, while leaving read-only databases, databases owned by another pod
// (excluded from the listing), and databases already at head untouched. A behind database is simulated as a
// registered record whose SQLite file is empty, at user_version 0: no platform migration has run on it yet.
func TestMigrateOwnedOnBoot_LevelsBehindDatabases(t *testing.T) {
	s := setupMigratePassStack(t)
	ctx := context.Background()
	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)

	behind := newRawDatabaseRecord(t, dbRepo, backing)
	readOnly := newRawDatabaseRecord(t, dbRepo, backing)
	foreign := newRawDatabaseRecord(t, dbRepo, backing)
	if err := testDB.Exec(ctx, "UPDATE databases SET read_only = TRUE WHERE id = $1", readOnly); err != nil {
		t.Fatalf("setting read_only: %v", err)
	}
	if err := testDB.Exec(ctx, "UPDATE databases SET pod_shard = 9 WHERE id = $1", foreign); err != nil {
		t.Fatalf("re-homing database to a foreign shard: %v", err)
	}

	// A fully-provisioned database, already at head.
	atHeadBdbID := uuid.New()
	if _, err := s.querySvc.Execute(
		ctx, uuid.New(), atHeadBdbID, "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil, domain.IntentSchema,
	); err != nil {
		t.Fatalf("provisioning at-head database: %v", err)
	}
	atHead, err := s.dbSvc.ResolveBdbDatabase(ctx, atHeadBdbID)
	if err != nil {
		t.Fatalf("resolving at-head database: %v", err)
	}
	t.Cleanup(func() {
		if err := testDB.Exec(
			context.Background(), "DELETE FROM databases WHERE id = $1", atHead.ID,
		); err != nil {
			t.Errorf("cleaning up database record %s: %v", atHead.ID, err)
		}
	})

	stats := s.dbSvc.MigrateOwnedOnBoot(ctx)

	// The shared Postgres holds records from other tests too, so global counters are checked as lower bounds;
	// per-database end states are exact.
	if stats.Migrated < 1 {
		t.Errorf("stats.Migrated = %d, want >= 1", stats.Migrated)
	}
	if stats.SkippedReadOnly < 1 {
		t.Errorf("stats.SkippedReadOnly = %d, want >= 1", stats.SkippedReadOnly)
	}
	if stats.AtHead < 1 {
		t.Errorf("stats.AtHead = %d, want >= 1", stats.AtHead)
	}
	if stats.Scanned < 3 {
		t.Errorf("stats.Scanned = %d, want >= 3", stats.Scanned)
	}

	// The behind database reached head: user_version, the head schema, and the Postgres mirror.
	if v := sqliteUserVersion(t, s.sqliteClient, behind); v != service.CurrentPlatformSchemaVersion {
		t.Errorf("behind database user_version = %d, want %d", v, service.CurrentPlatformSchemaVersion)
	}
	if _, err := s.sqliteClient.ExecuteInternal(
		ctx, behind, "SELECT label FROM _formulas LIMIT 1", nil,
	); err != nil {
		t.Errorf("behind database should have the head schema after the pass: %v", err)
	}
	var mirror int
	if err := testDB.QueryRow(
		ctx, "SELECT platform_schema_version FROM databases WHERE id = $1", behind,
	).Scan(&mirror); err != nil {
		t.Fatalf("reading mirror version: %v", err)
	}
	if mirror != service.CurrentPlatformSchemaVersion {
		t.Errorf("behind database mirror = %d, want %d", mirror, service.CurrentPlatformSchemaVersion)
	}

	// The read-only database is skipped; the foreign-owned one is never listed. Both stay unmigrated.
	if v := sqliteUserVersion(t, s.sqliteClient, readOnly); v != 0 {
		t.Errorf("read-only database user_version = %d, want 0 (skipped)", v)
	}
	if v := sqliteUserVersion(t, s.sqliteClient, foreign); v != 0 {
		t.Errorf("foreign-owned database user_version = %d, want 0 (not listed)", v)
	}

	// The already-at-head database is still at head.
	if v := sqliteUserVersion(t, s.sqliteClient, atHead.ID.String()); v != service.CurrentPlatformSchemaVersion {
		t.Errorf("at-head database user_version = %d, want %d", v, service.CurrentPlatformSchemaVersion)
	}
}
