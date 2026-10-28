//go:build integration

package integrationtest

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/s3store"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbapi "github.com/alexandremgo/baguettedb/pkg/databases/api"
	dbmcp "github.com/alexandremgo/baguettedb/pkg/databases/mcp"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// backupStack groups everything a backup test needs: the HTTP router with internal snapshot routes wired, the
// services to provision databases and write data, and the store to assert on uploaded objects.
type backupStack struct {
	router       chi.Router
	dbSvc        *service.DatabaseService
	skillSvc     *service.SkillService
	backupSvc    *service.BackupService
	store        *s3store.Client
	sqliteClient *sqlitelocal.Client
	stagingDir   string
}

// setupBackupStack wires the full databases HTTP stack plus the backup pipeline against the shared test
// containers. storePrefix isolates this test's objects inside the shared bucket; maxSnapshotAge is the freshness
// floor. Truncates the databases table so sweep stats are exact.
func setupBackupStack(t *testing.T, storePrefix string, maxSnapshotAge time.Duration) *backupStack {
	t.Helper()
	truncateAll(t)

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
	querySvc := service.NewQueryService(dbSvc, backing.sqliteClient, classifier, limits, 100, 0)
	skillSvc := service.NewSkillService(dbSvc, backing.sqliteClient)
	formulaSvc := service.NewFormulaService(dbSvc, backing.sqliteClient, classifier, querySvc,
		defaultTestFormulaRunTimeout, testLogger)

	store := newSnapshotStoreClient(t, storePrefix)
	stagingDir := filepath.Join(t.TempDir(), ".snapshots-tmp")
	backupSvc, err := service.NewBackupService(
		dbRepo, backing.sqliteClient, backing.sqliteClient, store, stagingDir, maxSnapshotAge, testLogger,
	)
	if err != nil {
		t.Fatalf("creating backup service: %v", err)
	}

	tv, err := authhttp.NewTokenValidator(
		context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger,
	)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	mcpSrv := dbmcp.NewServer(querySvc, skillSvc, formulaSvc, testLogger)
	sseServer := mcpserver.NewSSEServer(mcpSrv,
		mcpserver.WithStaticBasePath("/mcp"),
		mcpserver.WithSSEContextFunc(mcputil.SSEContextFunc),
	)
	exportSvc, err := service.NewExportService(dbSvc, formulaSvc, backing.sqliteClient, backing.sqliteClient,
		newExportRepo(), limits, backing.exportStagingDir, 0, testLogger)
	if err != nil {
		t.Fatalf("creating export service: %v", err)
	}

	handler := dbapi.NewHandler(dbSvc, querySvc, testLogger)
	skillHandler := dbapi.NewSkillHandler(skillSvc, testLogger)
	formulaHandler := dbapi.NewFormulaHandler(formulaSvc, testLogger)
	exportHandler := dbapi.NewExportHandler(exportSvc, testLogger)
	internalHandler := dbapi.NewInternalHandler(querySvc, skillSvc, formulaSvc, dbSvc, backupSvc, testLogger)
	router := dbapi.NewRouter(
		handler, skillHandler, formulaHandler, exportHandler, internalHandler, nil, tv.RequireToken(), authz.Policy{},
		sseServer, 1024*1024, testutil.InternalAPISecret,
	)

	return &backupStack{
		router:       router,
		dbSvc:        dbSvc,
		skillSvc:     skillSvc,
		backupSvc:    backupSvc,
		store:        store,
		sqliteClient: backing.sqliteClient,
		stagingDir:   stagingDir,
	}
}

// provisionDatabase creates a database for a fresh bdb and writes one table into it. Returns the database ID.
func provisionDatabase(t *testing.T, stack *backupStack) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	databaseID, err := stack.dbSvc.CreateDatabase(ctx, uuid.New())
	if err != nil {
		t.Fatalf("creating database: %v", err)
	}
	if _, err := stack.sqliteClient.Execute(
		ctx, databaseID.String(), "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)", nil,
	); err != nil {
		t.Fatalf("creating table: %v", err)
	}
	return databaseID
}

// snapshotCount returns how many snapshot objects the store holds for the database.
func snapshotCount(t *testing.T, stack *backupStack, databaseID uuid.UUID) int {
	t.Helper()
	snapshots, err := stack.store.ListSnapshots(context.Background(), databaseID.String())
	if err != nil {
		t.Fatalf("listing snapshots: %v", err)
	}
	return len(snapshots)
}

// TestBackupSweep_ShardAware verifies the sweep backs up only databases this pod owns: with the pod on shard 0, a
// shard-0 database is uploaded while a shard-1 (foreign) database is skipped and counted, never reaching the store.
func TestBackupSweep_ShardAware(t *testing.T) {
	stack := setupBackupStack(t, "sweep-shard-aware", time.Hour)
	stack.backupSvc.SetShardOrdinal(0) // this pod owns shard 0
	ctx := context.Background()

	owned := provisionDatabase(t, stack)   // shard 0 (the stack's dbSvc ordinal defaults to 0)
	foreign := provisionDatabase(t, stack) // make it shard 1 below
	if err := testDB.Exec(ctx, "UPDATE databases SET pod_shard = 1 WHERE id = $1", foreign); err != nil {
		t.Fatalf("marking foreign database shard 1: %v", err)
	}

	stats, err := stack.backupSvc.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stats.Scanned != 2 || stats.Uploaded != 1 || stats.SkippedForeign != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want Scanned=2 Uploaded=1 SkippedForeign=1 Failed=0", stats)
	}

	if got := snapshotCount(t, stack, owned); got != 1 {
		t.Errorf("owned database snapshot count = %d, want 1", got)
	}
	if got := snapshotCount(t, stack, foreign); got != 0 {
		t.Errorf("foreign database snapshot count = %d, want 0 (must not be backed up here)", got)
	}
}

// TestBackupSweep_BacksUpDirtyAndSkipsClean verifies the sweep's dirty-check: fresh databases are uploaded, an
// untouched database is skipped on the next sweep, a mutated one is re-uploaded — and the staging dir ends empty.
func TestBackupSweep_BacksUpDirtyAndSkipsClean(t *testing.T) {
	stack := setupBackupStack(t, "sweep-dirty", time.Hour)
	ctx := context.Background()

	db1 := provisionDatabase(t, stack)
	db2 := provisionDatabase(t, stack)

	stats, err := stack.backupSvc.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if stats.Scanned != 2 || stats.Uploaded != 2 || stats.Failed != 0 {
		t.Fatalf("first sweep stats = %+v, want Scanned=2 Uploaded=2 Failed=0", stats)
	}

	stats, err = stack.backupSvc.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if stats.Uploaded != 0 || stats.SkippedClean != 2 {
		t.Fatalf("second sweep stats = %+v, want Uploaded=0 SkippedClean=2", stats)
	}

	if _, err := stack.sqliteClient.Execute(
		ctx, db1.String(), "INSERT INTO t (v) VALUES ('mutated')", nil,
	); err != nil {
		t.Fatalf("mutating db1: %v", err)
	}

	stats, err = stack.backupSvc.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("third sweep: %v", err)
	}
	if stats.Uploaded != 1 || stats.SkippedClean != 1 {
		t.Fatalf("third sweep stats = %+v, want Uploaded=1 SkippedClean=1", stats)
	}

	if got := snapshotCount(t, stack, db1); got != 2 {
		t.Errorf("db1 snapshot count = %d, want 2", got)
	}
	if got := snapshotCount(t, stack, db2); got != 1 {
		t.Errorf("db2 snapshot count = %d, want 1", got)
	}

	entries, err := os.ReadDir(stack.stagingDir)
	if err != nil {
		t.Fatalf("reading staging dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("staging dir holds %d leftover files, want 0", len(entries))
	}
}

// TestBackupSweep_FreshnessFloorReuploadsIdleDatabase verifies an unchanged database is re-snapshotted once its
// newest stored snapshot is older than the freshness floor — the guard against lifecycle expiry deleting an idle
// database's only backup.
func TestBackupSweep_FreshnessFloorReuploadsIdleDatabase(t *testing.T) {
	stack := setupBackupStack(t, "freshness-floor", time.Millisecond)
	ctx := context.Background()

	db := provisionDatabase(t, stack)

	if _, err := stack.backupSvc.SweepOnce(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	time.Sleep(5 * time.Millisecond)

	stats, err := stack.backupSvc.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if stats.Uploaded != 1 {
		t.Fatalf("second sweep stats = %+v, want Uploaded=1 (stale despite clean)", stats)
	}
	if got := snapshotCount(t, stack, db); got != 2 {
		t.Errorf("snapshot count = %d, want 2", got)
	}
}

// TestBackupSweep_MissingFileSkippedNotFailed verifies a database record whose file is gone from disk counts as a
// skip, not a sweep failure.
func TestBackupSweep_MissingFileSkippedNotFailed(t *testing.T) {
	stack := setupBackupStack(t, "missing-file", time.Hour)
	ctx := context.Background()

	db := provisionDatabase(t, stack)
	// Removes the file but keeps the Postgres record — the shape of a database deleted mid-sweep.
	if err := stack.sqliteClient.DeleteDatabase(ctx, db.String()); err != nil {
		t.Fatalf("deleting database file: %v", err)
	}

	stats, err := stack.backupSvc.SweepOnce(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if stats.SkippedMissingFile != 1 || stats.Failed != 0 || stats.Uploaded != 0 {
		t.Fatalf("sweep stats = %+v, want SkippedMissingFile=1 Failed=0 Uploaded=0", stats)
	}
}

// TestBackupService_SweepsStagingDirOnBoot verifies leftover staged files from an interrupted run are removed at
// service construction.
func TestBackupService_SweepsStagingDirOnBoot(t *testing.T) {
	stagingDir := filepath.Join(t.TempDir(), ".snapshots-tmp")
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(stagingDir, "deadbeef-123.db")
	if err := os.WriteFile(leftover, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)
	store := newSnapshotStoreClient(t, "boot-sweep")
	if _, err := service.NewBackupService(
		dbRepo, backing.sqliteClient, backing.sqliteClient, store, stagingDir, time.Hour, testLogger,
	); err != nil {
		t.Fatalf("creating backup service: %v", err)
	}

	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Errorf("leftover staged file still present, stat err = %v", err)
	}
}

// TestInternalSnapshotEndpoint_RoundTrip verifies the on-demand snapshot endpoint: 200 with object metadata for an
// existing database, 404 for an unknown one, 400 for a bad ID.
func TestInternalSnapshotEndpoint_RoundTrip(t *testing.T) {
	stack := setupBackupStack(t, "endpoint-snapshot", time.Hour)

	db := provisionDatabase(t, stack)

	status, resp := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/snapshot", "", map[string]string{"database_id": db.String()})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (resp: %v)", status, resp)
	}
	if key, _ := resp["key"].(string); key == "" {
		t.Errorf("response key is empty: %v", resp)
	}
	if size, _ := resp["size_bytes"].(float64); size <= 0 {
		t.Errorf("response size_bytes = %v, want > 0", resp["size_bytes"])
	}
	if got := snapshotCount(t, stack, db); got != 1 {
		t.Errorf("snapshot count = %d, want 1", got)
	}

	status, _ = testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/snapshot", "", map[string]string{"database_id": uuid.New().String()})
	if status != http.StatusNotFound {
		t.Errorf("unknown database status = %d, want 404", status)
	}

	status, _ = testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/snapshot", "", map[string]string{"database_id": "not-a-uuid"})
	if status != http.StatusBadRequest {
		t.Errorf("bad ID status = %d, want 400", status)
	}
}

// TestInternalSnapshotSweepEndpoint_BacksUpDirty verifies the snapshot-all-dirty endpoint returns sweep stats and
// uploads pending changes — the pre-deploy gate primitive.
func TestInternalSnapshotSweepEndpoint_BacksUpDirty(t *testing.T) {
	stack := setupBackupStack(t, "endpoint-sweep", time.Hour)

	db := provisionDatabase(t, stack)

	status, resp := testutil.DoRequest(t, stack.router, http.MethodPost,
		"/internal/api/databases/v1/snapshot-sweep", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (resp: %v)", status, resp)
	}
	if uploaded, _ := resp["uploaded"].(float64); uploaded != 1 {
		t.Errorf("uploaded = %v, want 1 (resp: %v)", resp["uploaded"], resp)
	}
	if got := snapshotCount(t, stack, db); got != 1 {
		t.Errorf("snapshot count = %d, want 1", got)
	}
}

// TestInternalSnapshotEndpoints_NotConfigured verifies both snapshot endpoints respond 503 when snapshot storage
// is not configured (nil backup service — the default test stack).
func TestInternalSnapshotEndpoints_NotConfigured(t *testing.T) {
	router := setupRouter(t)

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		"/internal/api/databases/v1/snapshot", "", map[string]string{"database_id": uuid.New().String()})
	if status != http.StatusServiceUnavailable {
		t.Errorf("snapshot status = %d, want 503", status)
	}

	status, _ = testutil.DoRequest(t, router, http.MethodPost,
		"/internal/api/databases/v1/snapshot-sweep", "", nil)
	if status != http.StatusServiceUnavailable {
		t.Errorf("snapshot-sweep status = %d, want 503", status)
	}
}
