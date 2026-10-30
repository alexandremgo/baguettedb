//go:build integration

package integrationtest

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	mcpserver "github.com/mark3labs/mcp-go/server"

	bdbsservice "github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/filekek"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbapi "github.com/alexandremgo/baguettedb/pkg/databases/api"
	dbmcp "github.com/alexandremgo/baguettedb/pkg/databases/mcp"
	dbservice "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessbdbs"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessdatabases"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/startershttp"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
	startersservice "github.com/alexandremgo/baguettedb/pkg/starters/service"
)

// testAuthApp implements the bdbs service's AuthApp for tests.
type testAuthApp struct{}

func (a *testAuthApp) GetUserRoles(_ context.Context, _ uuid.UUID) ([]authz.Role, error) {
	return []authz.Role{authz.RoleUser}, nil
}

func (a *testAuthApp) GetUserProfiles(
	_ context.Context, userIDs []uuid.UUID,
) (map[uuid.UUID]*apptypes.UserProfile, error) {
	out := make(map[uuid.UUID]*apptypes.UserProfile, len(userIDs))
	for _, id := range userIDs {
		out[id] = &apptypes.UserProfile{Email: id.String() + "@test.com", FirstName: "Test", LastName: "User"}
	}
	return out, nil
}

// testSource is an in-memory StarterSource serving fixture manifests.
type testSource struct {
	manifests []domain.StarterManifest
}

func (s *testSource) List(_ context.Context) ([]domain.StarterManifest, error) {
	return s.manifests, nil
}

func (s *testSource) Get(_ context.Context, key string) (domain.StarterManifest, error) {
	for _, m := range s.manifests {
		if m.Key == key {
			return m, nil
		}
	}
	return domain.StarterManifest{}, fmt.Errorf("%w: %q", domain.ErrStarterNotFound, key)
}

// newTestKEKProvider writes a random 32-byte KEK to a temp file and returns a filekek.Provider backed by it.
func newTestKEKProvider(t *testing.T) *filekek.Provider {
	t.Helper()
	kek := make([]byte, filekek.KEKSize)
	if _, err := rand.Read(kek); err != nil {
		t.Fatalf("generating test KEK: %v", err)
	}
	path := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(path, kek, 0o600); err != nil {
		t.Fatalf("writing test KEK file: %v", err)
	}
	provider, err := filekek.NewProvider(path)
	if err != nil {
		t.Fatalf("loading test KEK provider: %v", err)
	}
	return provider
}

// newTestSQLiteClient builds a sqlitelocal.Client backed by a temp data dir, with a DEK resolver that fetches
// the wrapped DEK from Postgres and unwraps it via kekProvider — mirroring the production wiring.
func newTestSQLiteClient(
	t *testing.T, dbRepo dbservice.DatabaseRepository, kekProvider *filekek.Provider,
) *sqlitelocal.Client {
	t.Helper()
	dekResolver := func(ctx context.Context, databaseID string) ([]byte, error) {
		id, err := uuid.Parse(databaseID)
		if err != nil {
			return nil, fmt.Errorf("parsing database ID %q as UUID: %w", databaseID, err)
		}
		db, err := dbRepo.FindByID(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("finding database %s: %w", id, err)
		}
		dek, err := kekProvider.Unwrap(db.EncryptionKeyWrapped)
		if err != nil {
			return nil, fmt.Errorf("unwrapping DEK for %s: %w", id, err)
		}
		return dek, nil
	}
	client, err := sqlitelocal.NewClient(sqlitelocal.Config{
		DataDir:         t.TempDir(),
		CacheTTL:        30 * time.Minute,
		CacheMaxEntries: 100,
		CacheSweepEvery: 5 * time.Minute,
		DEKResolver:     dekResolver,
		Logger:          testLogger,
	})
	if err != nil {
		t.Fatalf("creating sqlitelocal client: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Logf("closing sqlitelocal client: %v", err)
		}
	})
	return client
}

// engineDeps exposes the real services behind a wired StarterService so tests can assert against the stores.
type engineDeps struct {
	bdbRepo    bdbReader
	dbRepo     dbservice.DatabaseRepository
	querySvc   *dbservice.QueryService
	formulaSvc *dbservice.FormulaService
	skillSvc   *dbservice.SkillService
}

// testStacks holds the real bdbs + databases services wired monolith-style, ready to be composed behind a
// StarterService through either the intraprocess or the HTTP adapters.
type testStacks struct {
	bdbSvc       *bdbsservice.BdbService
	dbSvc        *dbservice.DatabaseService
	querySvc     *dbservice.QueryService
	skillSvc     *dbservice.SkillService
	formulaSvc   *dbservice.FormulaService
	sqliteClient *sqlitelocal.Client
	deps         engineDeps
}

// setupStacks wires the real bdbs service (with real membership checks feeding the databases module) and the
// real databases services on a real SQLite file.
func setupStacks(t *testing.T) *testStacks {
	t.Helper()
	return setupStacksWithBdbLimit(t, 100)
}

// setupStacksWithBdbLimit is setupStacks with the per-owner bdb quota pinned, for tests that need to reach it.
func setupStacksWithBdbLimit(t *testing.T, maxBdbs int) *testStacks {
	t.Helper()

	bdbLimits := quota.Fixed(quota.Limits{MaxBdbs: maxBdbs, MaxMembersPerBdb: 10})
	bdbSvc, authChecker, bdbRepo := newBdbStack(&testAuthApp{}, bdbLimits)

	dbRepo := newDatabaseRepo()
	kekProvider := newTestKEKProvider(t)
	sqliteClient := newTestSQLiteClient(t, dbRepo, kekProvider)
	classifier := sqlitelocal.NewClassifier()
	dbLimits := quota.Fixed(quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	})
	dbSvc := dbservice.NewDatabaseService(
		dbRepo, sqliteClient, sqliteClient, authChecker, dbLimits, kekProvider, testLogger)
	querySvc := dbservice.NewQueryService(dbSvc, sqliteClient, classifier, dbLimits, 100, 0)
	skillSvc := dbservice.NewSkillService(dbSvc, sqliteClient)
	formulaSvc := dbservice.NewFormulaService(dbSvc, sqliteClient, classifier, querySvc, 5*time.Second, testLogger)

	return &testStacks{
		bdbSvc:       bdbSvc,
		dbSvc:        dbSvc,
		querySvc:     querySvc,
		skillSvc:     skillSvc,
		formulaSvc:   formulaSvc,
		sqliteClient: sqliteClient,
		deps: engineDeps{
			bdbRepo:    bdbRepo,
			dbRepo:     dbRepo,
			querySvc:   querySvc,
			formulaSvc: formulaSvc,
			skillSvc:   skillSvc,
		},
	}
}

// setupEngine composes the stacks behind a StarterService through the intraprocess adapters (monolith
// topology), reading manifests from the given sources.
func setupEngine(t *testing.T, sources ...startersservice.StarterSource) (*startersservice.StarterService, engineDeps) {
	t.Helper()
	stacks := setupStacks(t)

	starterSvc := startersservice.NewStarterService(
		startersservice.NewRegistry(sources...),
		intraprocessbdbs.New(stacks.bdbSvc),
		intraprocessdatabases.New(stacks.querySvc, stacks.formulaSvc, stacks.skillSvc, stacks.dbSvc),
		testLogger,
	)
	return starterSvc, stacks.deps
}

// setupHTTPEngine composes the stacks behind a StarterService whose databases leg goes through the real HTTP
// path (standalone topology): the full databases router served by httptest, called by the starters HTTP adapter
// with the internal secret. The bdbs leg stays in-process, as it does in both topologies.
func setupHTTPEngine(
	t *testing.T, sources ...startersservice.StarterSource,
) (*startersservice.StarterService, engineDeps) {
	t.Helper()
	stacks := setupStacks(t)

	tv, err := authhttp.NewTokenValidator(
		context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	mcpSrv := dbmcp.NewServer(stacks.querySvc, stacks.skillSvc, stacks.formulaSvc,
		dbservice.NewDocsService(stacks.querySvc), testLogger)
	sseServer := mcpserver.NewSSEServer(mcpSrv,
		mcpserver.WithStaticBasePath("/mcp"),
		mcpserver.WithSSEContextFunc(mcputil.SSEContextFunc),
	)

	exportSvc, err := dbservice.NewExportService(stacks.dbSvc, stacks.formulaSvc, stacks.sqliteClient,
		stacks.sqliteClient, newExportRepo(), quota.Fixed(quota.Limits{}),
		filepath.Join(t.TempDir(), ".exports-tmp"), 0, testLogger)
	if err != nil {
		t.Fatalf("creating export service: %v", err)
	}

	handler := dbapi.NewHandler(stacks.dbSvc, stacks.querySvc, testLogger)
	skillHandler := dbapi.NewSkillHandler(stacks.skillSvc, testLogger)
	formulaHandler := dbapi.NewFormulaHandler(stacks.formulaSvc, testLogger)
	exportHandler := dbapi.NewExportHandler(exportSvc, testLogger)
	internalHandler := dbapi.NewInternalHandler(
		stacks.querySvc, stacks.skillSvc, stacks.formulaSvc, stacks.dbSvc, nil, testLogger)
	router := dbapi.NewRouter(
		handler, skillHandler, formulaHandler, exportHandler, internalHandler, nil, tv.RequireToken(), authz.Policy{},
		sseServer, 1024*1024, testutil.InternalAPISecret,
	)

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	starterSvc := startersservice.NewStarterService(
		startersservice.NewRegistry(sources...),
		intraprocessbdbs.New(stacks.bdbSvc),
		startershttp.NewDatabasesApp(server.URL, testutil.InternalAPISecret),
		testLogger,
	)
	return starterSvc, stacks.deps
}
