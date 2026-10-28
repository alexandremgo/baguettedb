//go:build integration

package integrationtest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/filekek"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbapi "github.com/alexandremgo/baguettedb/pkg/databases/api"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	dbmcp "github.com/alexandremgo/baguettedb/pkg/databases/mcp"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// allowAllBdbsApp is a test mock for BdbsApp that always allows access.
type allowAllBdbsApp struct {
	ownerRoles []authz.Role
}

func (a *allowAllBdbsApp) CheckMemberPermission(
	_ context.Context, _, _ uuid.UUID, _ authz.DatabasePermission,
) (bool, error) {
	return true, nil
}

func (a *allowAllBdbsApp) GetBdbOwner(_ context.Context, _ uuid.UUID) (uuid.UUID, []authz.Role, error) {
	if a.ownerRoles != nil {
		return uuid.Nil, a.ownerRoles, nil
	}
	return uuid.Nil, []authz.Role{authz.RoleUser}, nil
}

func (a *allowAllBdbsApp) GetOwnedBdbIDs(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// roleAwareBdbsApp is a test mock for BdbsApp that checks permissions based on a bdb role.
type roleAwareBdbsApp struct {
	role authz.BdbRole
}

func (a *roleAwareBdbsApp) CheckMemberPermission(
	_ context.Context, _, _ uuid.UUID, perm authz.DatabasePermission,
) (bool, error) {
	return authz.BdbRoleHasDatabasePermission(a.role, perm), nil
}

func (a *roleAwareBdbsApp) GetBdbOwner(_ context.Context, _ uuid.UUID) (uuid.UUID, []authz.Role, error) {
	return uuid.Nil, []authz.Role{authz.RoleUser}, nil
}

func (a *roleAwareBdbsApp) GetOwnedBdbIDs(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// newTestKEKProvider writes a random 32-byte KEK to a temp file and returns
// a filekek.Provider backed by it. Lets integration tests exercise the real
// wrap path without depending on a fixture file.
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

// newTestSQLiteClient builds a sqlitelocal.Client backed by a temp data dir,
// with a DEK resolver that fetches the wrapped DEK from Postgres and unwraps
// it via kekProvider — mirroring the production wiring in setup.go.
func newTestSQLiteClient(
	t *testing.T, dbRepo service.DatabaseRepository, kekProvider *filekek.Provider,
) (*sqlitelocal.Client, string) {
	t.Helper()
	dataDir := t.TempDir()
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
		DataDir:         dataDir,
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
	return client, dataDir
}

// stackTestBacking groups the shared encryption backing (KEK provider and
// sqlitelocal client + data dir) used by all setupStack* calls within a single
// test. Sharing matters: when one test wires up multiple stacks (e.g. owner +
// viewer on the same bdb's database), they must resolve DEKs through the
// same KEK and read/write the same on-disk files.
type stackTestBacking struct {
	kekProvider  *filekek.Provider
	sqliteClient *sqlitelocal.Client

	// dataDir is the directory holding the sqliteClient's database files, for tests that manipulate files
	// directly (planting stale sidecars, deleting a database file out from under the stack).
	dataDir string

	// exportStagingDir is where ExportService spools export artifacts — under dataDir, mirroring the
	// production layout, and shared across stacks like the rest of the backing.
	exportStagingDir string

	// importSvc is the import service of the stack set up last, which has no route of its own to reach it by.
	importSvc *service.ImportService
}

var stackBackings sync.Map // map[*testing.T]*stackTestBacking

// getOrCreateStackBacking returns the per-test backing, creating it on first
// use and registering cleanup to evict it when the test finishes.
func getOrCreateStackBacking(t *testing.T, dbRepo service.DatabaseRepository) *stackTestBacking {
	t.Helper()
	if v, ok := stackBackings.Load(t); ok {
		return v.(*stackTestBacking)
	}
	b := &stackTestBacking{
		kekProvider: newTestKEKProvider(t),
	}
	b.sqliteClient, b.dataDir = newTestSQLiteClient(t, dbRepo, b.kekProvider)
	b.exportStagingDir = filepath.Join(b.dataDir, ".exports-tmp")
	stackBackings.Store(t, b)
	t.Cleanup(func() { stackBackings.Delete(t) })
	return b
}

// --- test wiring helpers ---

// setupRouter wires up the full databases HTTP stack backed by the shared test containers.
// Uses allowAllBdbsApp (all permissions granted).
func setupRouter(t *testing.T) chi.Router {
	t.Helper()
	return setupRouterWithBdbsApp(t, &allowAllBdbsApp{})
}

// setupRouterWithRole wires up the full databases HTTP stack with a role-aware BdbsApp.
func setupRouterWithRole(t *testing.T, role authz.BdbRole) chi.Router {
	t.Helper()
	return setupRouterWithBdbsApp(t, &roleAwareBdbsApp{role: role})
}

// setupRouterWithBdbsApp wires up the full databases HTTP stack with the given BdbsApp.
func setupRouterWithBdbsApp(t *testing.T, bdbsApp service.BdbsApp) chi.Router {
	t.Helper()
	router, _ := setupStackWithBdbsApp(t, bdbsApp)
	return router
}

// setupStack wires up the full databases HTTP stack and returns both the
// router and the sqlitelocal client. Use this in tests that need to bypass
// the REST API to execute SQL directly against the database file.
func setupStack(t *testing.T) (chi.Router, *sqlitelocal.Client) {
	t.Helper()
	return setupStackWithBdbsApp(t, &allowAllBdbsApp{})
}

// defaultTestFormulaRunTimeout is generous so ordinary test queries (sub-millisecond) never trip it;
// the timeout path is exercised separately via setupRouterWithFormulaTimeout.
const defaultTestFormulaRunTimeout = 5 * time.Second

// defaultTestQueryRequestTimeout is generous so ordinary test requests never trip it; the timeout path is exercised
// separately via setupRouterWithQueryTimeout.
const defaultTestQueryRequestTimeout = 30 * time.Second

// stackOptions bundles the tunables of setupStackWithOptions; the zero value of each field selects
// the test default.
type stackOptions struct {
	bdbsApp             service.BdbsApp // nil = allowAllBdbsApp
	formulaRunTimeout   time.Duration   // 0 = defaultTestFormulaRunTimeout
	queryRequestTimeout time.Duration   // 0 = defaultTestQueryRequestTimeout

	// sizeRefreshInterval > 0 re-enables stored-size refresh coalescing (disabled by default so
	// timing-sensitive tests see every refresh).
	sizeRefreshInterval time.Duration

	exportRowCap int            // 0 = uncapped
	resolver     quota.Resolver // nil = the default test quota.Limits

	// onQueryTx runs right before the query service opens a transaction, once the request's statements have been
	// classified, with the mode the transaction opens in (internal = authorizer bypassed). A test uses it to play a
	// concurrent request that changes the schema in that window, or to record the modes a request opens.
	onQueryTx func(internal bool)
}

// txHookExecutor is the stack's query executor with a hook that runs before each transaction it opens.
type txHookExecutor struct {
	*sqlitelocal.Client
	onTx func(internal bool)
}

func (e *txHookExecutor) WithTx(
	ctx context.Context, databaseID string, internal bool, fn func(context.Context, service.Tx) error,
) error {
	if e.onTx != nil {
		e.onTx(internal)
	}
	return e.Client.WithTx(ctx, databaseID, internal, fn)
}

// setupRouterWithFormulaTimeout wires the stack with a custom formula-run deadline so a test can
// trip it deterministically (e.g. a runaway recursive CTE against a tiny timeout).
func setupRouterWithFormulaTimeout(t *testing.T, formulaRunTimeout time.Duration) chi.Router {
	t.Helper()
	router, _ := setupStackWithOptions(t, stackOptions{formulaRunTimeout: formulaRunTimeout})
	return router
}

// setupRouterWithQueryTimeout wires the stack with a custom per-request SQL time budget so a test can trip it
// deterministically.
func setupRouterWithQueryTimeout(t *testing.T, queryRequestTimeout time.Duration) chi.Router {
	t.Helper()
	router, _ := setupStackWithOptions(t, stackOptions{queryRequestTimeout: queryRequestTimeout})
	return router
}

// setupRouterWithExportRowCap wires the stack with a tiny formula-export row cap so a test can trip the
// too-large rejection deterministically.
func setupRouterWithExportRowCap(t *testing.T, exportRowCap int) chi.Router {
	t.Helper()
	router, _ := setupStackWithOptions(t, stackOptions{exportRowCap: exportRowCap})
	return router
}

// setupRouterWithSizeRefreshInterval wires the stack with a non-zero stored-size refresh interval so a
// test can observe coalescing.
func setupRouterWithSizeRefreshInterval(t *testing.T, sizeRefreshInterval time.Duration) chi.Router {
	t.Helper()
	router, _ := setupStackWithOptions(t, stackOptions{sizeRefreshInterval: sizeRefreshInterval})
	return router
}

// setupStackWithBdbsApp wires up the full databases HTTP stack and also
// returns the underlying sqlitelocal client. Used by tests that need to
// bypass the REST API and execute SQL directly against the database file.
func setupStackWithBdbsApp(t *testing.T, bdbsApp service.BdbsApp) (chi.Router, *sqlitelocal.Client) {
	t.Helper()
	return setupStackWithOptions(t, stackOptions{bdbsApp: bdbsApp})
}

// setupStackWithOptions wires up the full databases HTTP stack with the given tunables.
func setupStackWithOptions(t *testing.T, opts stackOptions) (chi.Router, *sqlitelocal.Client) {
	bdbsApp := opts.bdbsApp
	if bdbsApp == nil {
		bdbsApp = &allowAllBdbsApp{}
	}
	formulaRunTimeout := opts.formulaRunTimeout
	if formulaRunTimeout == 0 {
		formulaRunTimeout = defaultTestFormulaRunTimeout
	}
	queryRequestTimeout := opts.queryRequestTimeout
	if queryRequestTimeout == 0 {
		queryRequestTimeout = defaultTestQueryRequestTimeout
	}
	sizeRefreshInterval := opts.sizeRefreshInterval
	exportRowCap := opts.exportRowCap
	t.Helper()

	// Shares KEK provider and sqlitelocal client across all setupStack* calls
	// within this test so multi-stack tests (e.g. owner + viewer on the same
	// bdb's database) resolve DEKs through the same KEK and read/write the
	// same on-disk files.
	dbRepo := newDatabaseRepo()
	backing := getOrCreateStackBacking(t, dbRepo)
	kekProvider := backing.kekProvider
	sqliteClient := backing.sqliteClient
	classifier := sqlitelocal.NewClassifier()

	limits := quota.Fixed(quota.Limits{
		MaxDBSizeBytes:          10 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
		MaxDBExportsPerWindow:   1,
		DBExportWindow:          24 * time.Hour,
		MaxFormulaExportRows:    exportRowCap,
	})
	if opts.resolver != nil {
		limits = opts.resolver
	}
	dbSvc := service.NewDatabaseService(dbRepo, sqliteClient, sqliteClient, bdbsApp, limits, kekProvider, testLogger)
	dbSvc.SetSizeRefreshInterval(sizeRefreshInterval)
	// Ownership lookups read through on every check so tests that mutate their BdbsApp fake (roles,
	// owned sets) see the change immediately.
	dbSvc.SetOwnershipCacheTTL(0)
	queryExecutor := &txHookExecutor{Client: sqliteClient, onTx: opts.onQueryTx}
	querySvc := service.NewQueryService(dbSvc, queryExecutor, classifier, limits, 100, queryRequestTimeout)
	skillSvc := service.NewSkillService(dbSvc, sqliteClient)
	formulaSvc := service.NewFormulaService(dbSvc, sqliteClient, classifier, querySvc, formulaRunTimeout, testLogger)

	tv, err := authhttp.NewTokenValidator(context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	mcpSrv := dbmcp.NewServer(querySvc, skillSvc, formulaSvc, testLogger)
	sseServer := mcpserver.NewSSEServer(mcpSrv,
		mcpserver.WithStaticBasePath("/mcp"),
		mcpserver.WithSSEContextFunc(mcputil.SSEContextFunc),
	)

	exportRepo := newExportRepo()
	exportSvc, err := service.NewExportService(dbSvc, formulaSvc, sqliteClient, sqliteClient, exportRepo,
		limits, backing.exportStagingDir, 0, testLogger)
	if err != nil {
		t.Fatalf("creating export service: %v", err)
	}

	importSvc, err := service.NewImportService(dbSvc, sqliteClient, filepath.Join(backing.dataDir, ".imports-tmp"),
		testLogger)
	if err != nil {
		t.Fatalf("creating import service: %v", err)
	}
	backing.importSvc = importSvc

	handler := dbapi.NewHandler(dbSvc, querySvc, testLogger)
	skillHandler := dbapi.NewSkillHandler(skillSvc, testLogger)
	formulaHandler := dbapi.NewFormulaHandler(formulaSvc, testLogger)
	exportHandler := dbapi.NewExportHandler(exportSvc, testLogger)
	internalHandler := dbapi.NewInternalHandler(querySvc, skillSvc, formulaSvc, dbSvc, nil, testLogger)
	return dbapi.NewRouter(
		handler, skillHandler, formulaHandler, exportHandler, internalHandler, nil, tv.RequireToken(), authz.Policy{},
		sseServer, 1024*1024, testutil.InternalAPISecret,
	), sqliteClient
}

// userToken builds a JWT with user role for the given user ID.
func userToken(t *testing.T, userID string) string {
	t.Helper()
	tok, err := testJWKS.BuildToken(userID, []string{"user"})
	if err != nil {
		t.Fatalf("building user token: %v", err)
	}
	return tok
}

// adminToken builds a JWT with admin role for the given user ID.
func adminToken(t *testing.T, userID string) string {
	t.Helper()
	tok, err := testJWKS.BuildToken(userID, []string{"user", "admin"})
	if err != nil {
		t.Fatalf("building admin token: %v", err)
	}
	return tok
}

// dbPath returns the API path for database endpoints scoped to a bdb.
func dbPath(bdbID, suffix string) string {
	base := fmt.Sprintf("/api/databases/v1/bdbs/%s/database", bdbID)
	if suffix == "" {
		return base
	}
	return base + "/" + suffix
}

// --- GetDatabase tests ---

func TestGetDatabase_AutoCreates(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	dbID, ok := resp["id"].(string)
	if !ok || dbID == "" {
		t.Fatal("expected non-empty id in response")
	}
	if _, err := uuid.Parse(dbID); err != nil {
		t.Errorf("id is not a valid UUID: %v", dbID)
	}
	if resp["bdb_id"] != bdbID {
		t.Errorf("bdb_id = %v, want %s", resp["bdb_id"], bdbID)
	}
	if resp["created_at"] == nil {
		t.Error("expected created_at in response")
	}
	if resp["updated_at"] == nil {
		t.Error("expected updated_at in response")
	}
}

func TestGetDatabase_Idempotent(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	_, resp1 := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)
	_, resp2 := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	if resp1["id"] != resp2["id"] {
		t.Errorf("second call returned different id: %v vs %v", resp1["id"], resp2["id"])
	}
}

func TestGetDatabase_Unauthorized(t *testing.T) {
	router := setupRouter(t)
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// --- Table operations tests ---

func TestListTables_Empty(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Auto-creates the database first.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "tables"), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows, ok := resp["rows"].([]any)
	if !ok {
		t.Fatalf("expected rows array, got %v", resp["rows"])
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestExecuteSchema_CreateTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	// DDL statements should report 0 affected rows.
	if arc, ok := resp["affected_row_count"].(float64); !ok || arc != 0 {
		t.Errorf("affected_row_count = %v, want 0", resp["affected_row_count"])
	}
}

func TestListTables_AfterCreate(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates a table.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "tables"), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	// Verifies columns from sqlite_master query.
	columns := resp["columns"].([]any)
	colNames := extractColumnNames(t, columns)
	if colNames[0] != "name" || colNames[1] != "type" {
		t.Errorf("columns = %v, want [name, type]", colNames)
	}

	// Verifies the table name and type.
	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 table, got %d", len(rows))
	}
	row := extractRowValues(t, rows[0])
	if row[0] != "items" {
		t.Errorf("table name = %v, want items", row[0])
	}
	if row[1] != "table" {
		t.Errorf("table type = %v, want table", row[1])
	}
}

func TestListTables_FTS5ShadowsExcludedVirtualTyped(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	for _, ddl := range []string{
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT)",
		// A plain (non-external-content) fts5 table creates all five shadow tables alongside itself.
		"CREATE VIRTUAL TABLE notes_fts USING fts5(title, body)",
		// A decoy with an fts-shadow-looking name but no fts5 sibling — must survive the filter.
		"CREATE TABLE logs_fts_data (id INTEGER PRIMARY KEY)",
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
			userToken(t, userID), map[string]string{"sql": ddl})
		if status != http.StatusOK {
			t.Fatalf("schema %q: status = %d, body: %v", ddl, status, resp)
		}
	}

	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "tables"), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	listed := make(map[string][2]string, len(rows))
	for _, r := range rows {
		vals := extractRowValues(t, r)
		listed[vals[0].(string)] = [2]string{vals[1].(string), vals[2].(string)}
	}

	want := map[string][2]string{
		"notes": {"table", ""}, "notes_fts": {"virtual", "fts5"}, "logs_fts_data": {"table", ""},
	}
	for name, wantEntry := range want {
		if gotEntry, ok := listed[name]; !ok {
			t.Errorf("table %s missing from listing, got %v", name, listed)
		} else if gotEntry != wantEntry {
			t.Errorf("table %s (type, module) = %v, want %v", name, gotEntry, wantEntry)
		}
	}
	if len(listed) != len(want) {
		t.Errorf("expected exactly %d tables (fts5 shadows excluded), got %v", len(want), listed)
	}
}

func TestDescribeTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates a table.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE products (id INTEGER PRIMARY KEY, name TEXT NOT NULL, price REAL)"})

	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "tables/products"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	columns := resp["columns"].([]any)
	if len(columns) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(columns))
	}

	col0 := columns[0].(map[string]any)
	col1 := columns[1].(map[string]any)
	col2 := columns[2].(map[string]any)

	if col0["name"] != "id" {
		t.Errorf("col0 name = %v, want id", col0["name"])
	}
	if col1["name"] != "name" {
		t.Errorf("col1 name = %v, want name", col1["name"])
	}
	if col2["name"] != "price" {
		t.Errorf("col2 name = %v, want price", col2["name"])
	}

	if col0["type"] != "INTEGER" {
		t.Errorf("col0 type = %v, want INTEGER", col0["type"])
	}
	if col1["type"] != "TEXT" {
		t.Errorf("col1 type = %v, want TEXT", col1["type"])
	}
	if col2["type"] != "REAL" {
		t.Errorf("col2 type = %v, want REAL", col2["type"])
	}

	if col0["primary_key"] != true {
		t.Errorf("col0 primary_key = %v, want true", col0["primary_key"])
	}
	if col1["not_null"] != true {
		t.Errorf("col1 not_null = %v, want true", col1["not_null"])
	}

	// Verifies foreign_keys is present (empty for this table).
	fks := resp["foreign_keys"].([]any)
	if len(fks) != 0 {
		t.Errorf("expected 0 foreign keys, got %d", len(fks))
	}
}

func TestDescribeTable_InvalidName(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Ensures database exists.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	status, _ := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "tables/drop%20table"),
		userToken(t, userID), nil)
	if status != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", status, http.StatusUnprocessableEntity)
	}
}

// --- Query execution tests ---

func TestExecuteQuery_Select(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates table and inserts data.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT)"})

	_, insertResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO books VALUES (1, 'The Go Programming Language')"})

	// Verifies INSERT affected_row_count.
	if arc, ok := insertResp["affected_row_count"].(float64); !ok || arc != 1 {
		t.Errorf("INSERT affected_row_count = %v, want 1", insertResp["affected_row_count"])
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT * FROM books"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	// Verifies column names.
	columns := resp["columns"].([]any)
	colNames := extractColumnNames(t, columns)
	if len(colNames) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(colNames))
	}
	if colNames[0] != "id" || colNames[1] != "title" {
		t.Errorf("columns = %v, want [id, title]", colNames)
	}

	// Verifies row values.
	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	row := extractRowValues(t, rows[0])
	if len(row) != 2 {
		t.Fatalf("expected 2 values, got %d", len(row))
	}
	// Hrana returns integers as strings in JSON.
	if row[0] != "1" && row[0] != float64(1) {
		t.Errorf("id = %v (%T), want 1", row[0], row[0])
	}
	if row[1] != "The Go Programming Language" {
		t.Errorf("title = %v, want 'The Go Programming Language'", row[1])
	}
}

func TestExecuteQuery_MissingSQL(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": ""})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestExecuteQuery_Unauthorized(t *testing.T) {
	router := setupRouter(t)
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// --- Isolation test ---

func TestDatabaseIsolation_OtherBdbCannotSeeData(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdb1 := uuid.New().String()
	bdb2 := uuid.New().String()

	// Bdb 1 creates a table and inserts data.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdb1, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE secrets (id INTEGER PRIMARY KEY, value TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdb1, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO secrets VALUES (1, 'bdb1-secret')"})

	// Verifies bdb 1 can see its own data.
	_, c1Resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdb1, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT * FROM secrets"})
	c1Rows := c1Resp["rows"].([]any)
	if len(c1Rows) != 1 {
		t.Fatalf("bdb1 should have 1 row, got %d", len(c1Rows))
	}
	c1Row := extractRowValues(t, c1Rows[0])
	if c1Row[1] != "bdb1-secret" {
		t.Errorf("bdb1 value = %v, want bdb1-secret", c1Row[1])
	}

	// Bdb 2 should have an empty database (no secrets table).
	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdb2, "tables"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 0 {
		t.Errorf("bdb2 should have 0 tables, got %d", len(rows))
	}
}

// --- Cross-database prevention tests ---

func TestUnsupportedStatements_RejectedOnAllEndpoints(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	statements := []struct {
		name string
		sql  string
	}{
		{"attach", "ATTACH DATABASE 'other.db' AS other"},
		{"detach", "DETACH DATABASE other"},
	}

	endpoints := []string{"query", "data", "schema"}

	for _, stmt := range statements {
		for _, ep := range endpoints {
			t.Run(stmt.name+"/"+ep, func(t *testing.T) {
				status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, ep),
					userToken(t, userID),
					map[string]string{"sql": stmt.sql})
				if status != http.StatusBadRequest {
					t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
				}
			})
		}
	}
}

// --- Multi-statement rejection tests ---

func TestMultiStatement_RejectedOnAllEndpoints(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	statements := []struct {
		name string
		sql  string
	}{
		{"select then delete", "SELECT 1; DELETE FROM t"},
		{"insert then select", "INSERT INTO t VALUES (1); SELECT * FROM t"},
		{"create then insert", "CREATE TABLE t (id INT); INSERT INTO t VALUES (1)"},
		{"delete then drop", "DELETE FROM t; DROP TABLE t"},
		{"two selects", "SELECT 1; SELECT 2"},
	}

	endpoints := []string{"query", "data", "schema"}

	for _, stmt := range statements {
		for _, ep := range endpoints {
			t.Run(stmt.name+"/"+ep, func(t *testing.T) {
				status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, ep),
					userToken(t, userID),
					map[string]string{"sql": stmt.sql})
				if status != http.StatusBadRequest {
					t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
				}
			})
		}
	}
}

// --- Endpoint separation tests ---

func TestQuery_RejectsDML(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Ensures database + table exist.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY)"})

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO t VALUES (1)"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestQuery_RejectsDDL(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY)"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestExecute_RejectsDQL(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT 1"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestExecute_RejectsDDL(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY)"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestSchema_RejectsDQL(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT 1"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestSchema_RejectsDML(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO t VALUES (1)"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// --- Permission enforcement tests ---

func TestSchema_OwnerCanDDL(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleOwner)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY)"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
}

func TestExecute_EditorCanDML(t *testing.T) {
	truncateAll(t)
	// Creates the table as owner first, then tests DML as editor.
	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, ownerRouter, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)"})

	editorRouter := setupRouterWithRole(t, authz.BdbRoleEditor)

	status, resp := testutil.DoRequest(t, editorRouter, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO t VALUES (1, 'hello')"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
}

func TestSchema_EditorCannotDDL(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleEditor)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY)"})
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestExecute_ViewerCannotDML(t *testing.T) {
	truncateAll(t)
	// Creates table as owner first.
	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, ownerRouter, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY)"})

	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)

	status, _ := testutil.DoRequest(t, viewerRouter, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO t VALUES (1)"})
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestSchema_ViewerCannotDDL(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleViewer)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY)"})
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

func TestQuery_ViewerCanDQL(t *testing.T) {
	truncateAll(t)
	// Creates table and data as owner first.
	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, ownerRouter, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, ownerRouter, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO t VALUES (1, 'hello')"})

	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)

	status, resp := testutil.DoRequest(t, viewerRouter, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT * FROM t"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(rows))
	}
}

// --- Mutation log trigger auto-creation tests ---

func TestCreateTable_AutoCreatesMutationLogTriggers(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates a table (triggers should be auto-created).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Verifies 3 triggers exist in sqlite_master.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM sqlite_master WHERE type = 'trigger' " +
			"AND name LIKE '_change_log_items%' ORDER BY name"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("expected 3 triggers, got %d", len(rows))
	}
}

func TestDML_LogsToMutationLog(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates table (auto-triggers).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Inserts a row.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Queries mutation log.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT table_name, operation, changed_by FROM _change_log WHERE kind = 'DML'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 mutation log entry, got %d", len(rows))
	}

	row := extractRowValues(t, rows[0])
	if row[0] != "items" {
		t.Errorf("table_name = %v, want items", row[0])
	}
	if row[1] != "INSERT" {
		t.Errorf("operation = %v, want INSERT", row[1])
	}
	if row[2] != userID {
		t.Errorf("changed_by = %v, want %s", row[2], userID)
	}
}

func TestDML_UpdateLogsOldAndNewValues(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET name = 'bread' WHERE id = 1"})

	// Queries mutation log for the UPDATE entry.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT operation, old_values, new_values FROM _change_log WHERE kind = 'DML' " +
			"AND operation = 'UPDATE'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 UPDATE mutation log entry, got %d", len(rows))
	}

	row := extractRowValues(t, rows[0])
	oldValues, _ := row[1].(string)
	newValues, _ := row[2].(string)

	if !strings.Contains(oldValues, "milk") {
		t.Errorf("old_values should contain 'milk', got %s", oldValues)
	}
	if !strings.Contains(newValues, "bread") {
		t.Errorf("new_values should contain 'bread', got %s", newValues)
	}
}

func TestDML_DeleteLogsToMutationLog(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "DELETE FROM items WHERE id = 1"})

	// Queries mutation log for the DELETE entry.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT operation, old_values, new_values FROM _change_log WHERE kind = 'DML' " +
			"AND operation = 'DELETE'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 DELETE mutation log entry, got %d", len(rows))
	}

	row := extractRowValues(t, rows[0])
	oldValues, _ := row[1].(string)
	if !strings.Contains(oldValues, "milk") {
		t.Errorf("old_values should contain 'milk', got %s", oldValues)
	}
	if row[2] != nil {
		t.Errorf("new_values should be nil for DELETE, got %v", row[2])
	}
}

func TestDML_MultipleInserts_CorrectChangedBy(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Inserts two rows in separate requests.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('bread')"})

	// Verifies both mutation log entries have correct changed_by.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT changed_by FROM _change_log WHERE kind = 'DML' ORDER BY id"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 mutation log entries, got %d", len(rows))
	}

	for i, r := range rows {
		row := extractRowValues(t, r)
		if row[0] != userID {
			t.Errorf("entry %d: changed_by = %v, want %s", i, row[0], userID)
		}
	}
}

// --- Batch ID and affected rows tests ---

func TestExecute_ResponseIncludesBatchID(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	batchID, ok := resp["batch_id"].(string)
	if !ok || batchID == "" {
		t.Fatalf("expected non-empty batch_id, got %v", resp["batch_id"])
	}

	// batch_id should be a valid UUID.
	if _, err := uuid.Parse(batchID); err != nil {
		t.Errorf("batch_id %q is not a valid UUID: %v", batchID, err)
	}
}

func TestExecute_ResponseIncludesAffectedRows_Insert(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	affectedRows, ok := resp["affected_rows"].([]any)
	if !ok {
		t.Fatalf("expected affected_rows array, got %T", resp["affected_rows"])
	}
	if len(affectedRows) != 1 {
		t.Fatalf("expected 1 affected row, got %d", len(affectedRows))
	}

	row := affectedRows[0].(map[string]any)
	if row["table_name"] != "items" {
		t.Errorf("table_name = %v, want items", row["table_name"])
	}
	if row["operation"] != "INSERT" {
		t.Errorf("operation = %v, want INSERT", row["operation"])
	}
	if row["old_values"] != nil {
		t.Errorf("old_values should be nil for INSERT, got %v", row["old_values"])
	}
	newVals, _ := row["new_values"].(map[string]any)
	if newVals["name"] != "milk" {
		t.Errorf("new_values.name = %v, want milk", newVals["name"])
	}
}

func TestExecute_ResponseIncludesAffectedRows_Update(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 2)"})

	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET qty = 6 WHERE name = 'milk'"})

	affectedRows := resp["affected_rows"].([]any)
	if len(affectedRows) != 1 {
		t.Fatalf("expected 1 affected row, got %d", len(affectedRows))
	}

	row := affectedRows[0].(map[string]any)
	if row["operation"] != "UPDATE" {
		t.Errorf("operation = %v, want UPDATE", row["operation"])
	}

	oldVals := row["old_values"].(map[string]any)
	if oldVals["qty"] != float64(2) {
		t.Errorf("old_values.qty = %v, want 2", oldVals["qty"])
	}

	newVals := row["new_values"].(map[string]any)
	if newVals["qty"] != float64(6) {
		t.Errorf("new_values.qty = %v, want 6", newVals["qty"])
	}
}

func TestExecute_ResponseIncludesAffectedRows_Delete(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "DELETE FROM items WHERE name = 'milk'"})

	affectedRows := resp["affected_rows"].([]any)
	if len(affectedRows) != 1 {
		t.Fatalf("expected 1 affected row, got %d", len(affectedRows))
	}

	row := affectedRows[0].(map[string]any)
	if row["operation"] != "DELETE" {
		t.Errorf("operation = %v, want DELETE", row["operation"])
	}

	oldVals := row["old_values"].(map[string]any)
	if oldVals["name"] != "milk" {
		t.Errorf("old_values.name = %v, want milk", oldVals["name"])
	}
	if row["new_values"] != nil {
		t.Errorf("new_values should be nil for DELETE, got %v", row["new_values"])
	}
}

func TestExecute_ResponseIncludesAffectedRows_MultiRow(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	for _, item := range []string{"milk", "eggs", "bread"} {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (name, qty) VALUES ('%s', 1)", item)})
	}

	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET qty = 10"})

	affectedRows := resp["affected_rows"].([]any)
	if len(affectedRows) != 3 {
		t.Fatalf("expected 3 affected rows, got %d", len(affectedRows))
	}

	batchID := resp["batch_id"].(string)
	if batchID == "" {
		t.Error("expected non-empty batch_id")
	}

	// All affected rows should have the same batch.
	for i, ar := range affectedRows {
		row := ar.(map[string]any)
		if row["operation"] != "UPDATE" {
			t.Errorf("affected_rows[%d].operation = %v, want UPDATE", i, row["operation"])
		}
	}
}

// --- Undo/redo stack management tests ---

func TestUndoStack_PushedOnChange(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	batchID := resp["batch_id"].(string)

	// The top of _undo_stack must be the DML batch we just pushed (the
	// preceding CREATE TABLE batch sits below it).
	_, stackResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT batch_id FROM _undo_stack ORDER BY position DESC LIMIT 1"})

	rows := stackResp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected at least 1 undo stack entry, got %d", len(rows))
	}
	stackBatchID := extractRowValues(t, rows[0])[0]
	if stackBatchID != batchID {
		t.Errorf("top undo stack batch_id = %v, want %s", stackBatchID, batchID)
	}
}

func TestUndoStack_MultipleChanges(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	for _, item := range []string{"milk", "eggs", "bread"} {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (name) VALUES ('%s')", item)})
	}

	// Scopes to DML batches (the preceding CREATE TABLE entry is DDL).
	_, stackResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT us.batch_id FROM _undo_stack us " +
			"JOIN _change_log cl ON cl.batch_id = us.batch_id " +
			"WHERE cl.kind = 'DML' " +
			"GROUP BY us.batch_id, us.position " +
			"ORDER BY us.position"})

	rows := stackResp["rows"].([]any)
	if len(rows) != 3 {
		t.Fatalf("expected 3 DML undo stack entries, got %d", len(rows))
	}

	// Each entry should have a unique batch_id.
	seen := map[string]bool{}
	for i, row := range rows {
		bid := extractRowValues(t, row)[0].(string)
		if seen[bid] {
			t.Errorf("duplicate batch_id at position %d: %s", i, bid)
		}
		seen[bid] = true
	}
}

// (Removed: TestStacks_ClearedOnSchemaChange / DropTable / CreateTable —
// DDL no longer clears stacks under the unified change-log invariant. The
// flipped behavior is exercised by the dedicated DDL-undo integration
// tests.)

// --- List mutations tests ---

func TestListChanges_ReturnsRecentEntries(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	for _, item := range []string{"milk", "eggs", "bread"} {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (name) VALUES ('%s')", item)})
	}

	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DML&kind=DML"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	batches := resp["batches"].([]any)
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(batches))
	}
}

func TestListChanges_GroupedByBatch(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	for _, item := range []string{"milk", "eggs", "bread"} {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (name, qty) VALUES ('%s', 1)", item)})
	}

	// Multi-row update — one batch with 3 entries.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET qty = 10"})

	_, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DML&kind=DML"),
		userToken(t, userID), nil)

	batches := resp["batches"].([]any)
	// First batch (most recent) is the UPDATE with 3 entries.
	firstBatch := batches[0].(map[string]any)
	entries := firstBatch["entries"].([]any)
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries in UPDATE batch, got %d", len(entries))
	}
}

func TestListChanges_FilterByTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE other (id INTEGER PRIMARY KEY, val TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO other (val) VALUES ('x')"})

	_, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DML&table_name=items"),
		userToken(t, userID), nil)

	batches := resp["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch for 'items' filter, got %d", len(batches))
	}

	entry := batches[0].(map[string]any)["entries"].([]any)[0].(map[string]any)
	if entry["table_name"] != "items" {
		t.Errorf("table_name = %v, want items", entry["table_name"])
	}
}

func TestListChanges_Limit(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	for i := 0; i < 10; i++ {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (name) VALUES ('item_%d')", i)})
	}

	// Limit=3 returns at most 3 entries (which may be 3 batches since each is a single INSERT).
	_, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DML&limit=3"),
		userToken(t, userID), nil)

	batches := resp["batches"].([]any)
	if len(batches) > 3 {
		t.Errorf("expected at most 3 batches with limit=3, got %d", len(batches))
	}
}

func TestListChanges_ShowsOrigin(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	_, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DML&kind=DML"),
		userToken(t, userID), nil)

	batches := resp["batches"].([]any)
	// Most recent batch should be the undo.
	undoBatch := batches[0].(map[string]any)
	if undoBatch["origin"] != "undo" {
		t.Errorf("most recent batch origin = %v, want undo", undoBatch["origin"])
	}

	// Second batch should be the original mutation.
	mutationBatch := batches[1].(map[string]any)
	if mutationBatch["origin"] != "change" {
		t.Errorf("second batch origin = %v, want change", mutationBatch["origin"])
	}
}

func TestListChanges_EmptyDatabase(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY)"})

	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DML&kind=DML"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	batches := resp["batches"].([]any)
	if len(batches) != 0 {
		t.Errorf("expected 0 batches for empty database, got %d", len(batches))
	}
}

func TestListChanges_IncludesOldNewValues(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 2)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET qty = 6 WHERE name = 'milk'"})

	_, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DML&kind=DML"),
		userToken(t, userID), nil)

	batches := resp["batches"].([]any)
	// Most recent is the UPDATE.
	updateBatch := batches[0].(map[string]any)
	entry := updateBatch["entries"].([]any)[0].(map[string]any)

	oldVals := entry["old_values"].(map[string]any)
	if oldVals["qty"] != float64(2) {
		t.Errorf("old_values.qty = %v, want 2", oldVals["qty"])
	}

	newVals := entry["new_values"].(map[string]any)
	if newVals["qty"] != float64(6) {
		t.Errorf("new_values.qty = %v, want 6", newVals["qty"])
	}
}

func TestREST_ListMutations_UserIDQueryParam(t *testing.T) {
	// Pins the REST param-parsing surface: missing param → 200 unfiltered,
	// valid UUID → 200 filtered, malformed UUID → 400. Doesn't duplicate
	// full filter-coverage tests (those live next to the MCP variant; both
	// go through the same service function).
	truncateAll(t)
	router := setupRouter(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, aliceID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, aliceID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('a1')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, aliceID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('a2')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, bobID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('b1')"})

	// Missing param → 200 with all 3 batches (audit view default).
	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		dbPath(bdbID, "changes?kind=DML&kind=DML"), userToken(t, aliceID), nil)
	if status != http.StatusOK {
		t.Fatalf("missing user_id: status = %d, want 200", status)
	}
	if got := len(resp["batches"].([]any)); got != 3 {
		t.Errorf("missing user_id: batches = %d, want 3 (unfiltered)", got)
	}

	// Valid UUID → 200 with filtered set.
	status, resp = testutil.DoRequest(t, router, http.MethodGet,
		dbPath(bdbID, "changes?kind=DML&user_id="+aliceID), userToken(t, aliceID), nil)
	if status != http.StatusOK {
		t.Fatalf("valid user_id: status = %d, want 200", status)
	}
	if got := len(resp["batches"].([]any)); got != 2 {
		t.Errorf("user_id=alice: batches = %d, want 2", got)
	}

	// Malformed UUID → 400 (must not silently fall back to unfiltered).
	status, _ = testutil.DoRequest(t, router, http.MethodGet,
		dbPath(bdbID, "changes?kind=DML&user_id=not-a-uuid"), userToken(t, aliceID), nil)
	if status != http.StatusBadRequest {
		t.Errorf("malformed user_id: status = %d, want 400", status)
	}
}

// --- Undo tests ---

func TestUndo_SingleInsert(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d", status, http.StatusOK)
	}

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	count := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if count != float64(0) {
		t.Errorf("expected 0 rows after undo, got %v", count)
	}
}

func TestUndo_SingleUpdate(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 2)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET qty = 6 WHERE name = 'milk'"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT qty FROM items WHERE name = 'milk'"})
	qty := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if qty != float64(2) {
		t.Errorf("qty = %v, want 2 (original value)", qty)
	}
}

// TestUndo_RestoresStorageClassAndBigIntegers pins that undo writes values back with the storage class and precision
// they had: a REAL 1.0 must not come back as INTEGER 1, and an integer above 2^53 must not be rounded. The columns
// are declared without a type so SQLite keeps whatever storage class the write supplies. The update writes a REAL
// so the conflict check compares a live REAL against the change log's rendering of it before the undo may run.
func TestUndo_RestoresStorageClassAndBigIntegers(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE m (id INTEGER PRIMARY KEY, ratio, big)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO m (ratio, big) VALUES (1.0, 9007199254740993)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE m SET ratio = 2.5, big = 1 WHERE id = 1"})

	status, undoResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d: %v", status, http.StatusOK, undoResp)
	}

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT typeof(ratio), CAST(ratio AS TEXT), typeof(big), CAST(big AS TEXT) FROM m"})
	got := extractRowValues(t, selectResp["rows"].([]any)[0])
	want := []any{"real", "1.0", "integer", "9007199254740993"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("column %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestUndo_DeleteRestoresRowIDOnImplicitRowIDTable pins that undoing a DELETE puts the row back under its original
// rowid on a table without an INTEGER PRIMARY KEY alias, so an older change on that row can still be undone afterwards.
func TestUndo_DeleteRestoresRowIDOnImplicitRowIDTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE u (name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO u (name) VALUES ('a'), ('b'), ('c')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "UPDATE u SET name = 'B' WHERE rowid = 2"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "DELETE FROM u WHERE rowid = 2"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo of the delete: status = %d, want %d: %v", status, http.StatusOK, resp)
	}
	_, back := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT rowid FROM u WHERE name = 'B'"})
	if got := extractRowValues(t, back["rows"].([]any)[0])[0]; got != float64(2) {
		t.Fatalf("restored row has rowid %v, want 2", got)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo of the update on the restored row: status = %d, want %d: %v", status, http.StatusOK, resp)
	}
	_, final := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT name FROM u WHERE rowid = 2"})
	if got := extractRowValues(t, final["rows"].([]any)[0])[0]; got != "b" {
		t.Errorf("name at rowid 2 = %v, want b", got)
	}
}

func TestUndo_SingleDelete(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('eggs')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "DELETE FROM items WHERE name = 'eggs'"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM items"})
	rows := selectResp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after undo delete, got %d", len(rows))
	}
	name := extractRowValues(t, rows[0])[0]
	if name != "eggs" {
		t.Errorf("name = %v, want eggs", name)
	}
}

func TestUndo_MultiRowChange(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	for _, item := range []struct {
		name string
		qty  int
	}{{"milk", 2}, {"eggs", 12}, {"bread", 1}} {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (name, qty) VALUES ('%s', %d)", item.name,
				item.qty)})
	}

	// Deletes 2 rows (qty < 10).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "DELETE FROM items WHERE qty < 10"})

	// Undo restores the 2 deleted rows.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	count := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if count != float64(3) {
		t.Errorf("expected 3 rows after undo, got %v", count)
	}
}

func TestUndo_Sequential_TwoUndos(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET name = 'bread' WHERE name = 'milk'"})

	// Undo the UPDATE.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	// Undo the INSERT.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	count := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if count != float64(0) {
		t.Errorf("expected 0 rows after 2 undos, got %v", count)
	}
}

func TestUndo_Sequential_ThreeUndos(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET name = 'bread'"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "DELETE FROM items"})

	// Undo x3.
	for i := 0; i < 3; i++ {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
			userToken(t, userID), nil)
	}

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	count := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if count != float64(0) {
		t.Errorf("expected 0 rows after 3 undos (back to empty), got %v", count)
	}
}

func TestUndo_NothingToUndo(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Touch the database (without pushing anything to the undo stack)
	// so the API route resolves a real database.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""),
		userToken(t, userID), nil)

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestUndo_ConflictDetection pins the conflict an undo raises when another user changed the row since: Alice's UPDATE
// no longer describes the live row, so her undo is refused with a 409 naming Bob's change as the blocker and leaves
// the row untouched.
func TestUndo_ConflictDetection(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	alice := uuid.New().String()
	bob := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, alice),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, alice),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, alice),
		map[string]string{"sql": "UPDATE items SET name = 'bread' WHERE id = 1"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, bob),
		map[string]string{"sql": "UPDATE items SET name = 'cheese' WHERE id = 1"})

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, alice), nil)
	if status != http.StatusConflict {
		t.Fatalf("undo status = %d, want %d: %v", status, http.StatusConflict, body)
	}
	blockers, _ := body["blocked_by"].([]any)
	if len(blockers) != 1 {
		t.Fatalf("blocked_by = %v, want exactly Bob's update", body["blocked_by"])
	}
	blocker := blockers[0].(map[string]any)
	if blocker["changed_by"] != bob || blocker["operation"] != "UPDATE" {
		t.Errorf("blocker = %v, want Bob's UPDATE", blocker)
	}

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, alice),
		map[string]string{"sql": "SELECT name FROM items WHERE id = 1"})
	if name := extractRowValues(t, selectResp["rows"].([]any)[0])[0]; name != "cheese" {
		t.Errorf("name = %v, want cheese (a refused undo must leave the row untouched)", name)
	}
}

// TestUndo_InsertRefusedWhenAnotherUserUpdatedTheRow pins that undoing an INSERT is refused while the row no longer
// equals what was inserted: Bob's update is the blocker, his edit survives, and once his update is reverted Alice's
// undo goes through and deletes the row.
func TestUndo_InsertRefusedWhenAnotherUserUpdatedTheRow(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	alice := uuid.New().String()
	bob := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, alice),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, alice),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, bob),
		map[string]string{"sql": "UPDATE items SET name = 'cheese' WHERE id = 1"})

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, alice), nil)
	if status != http.StatusConflict {
		t.Fatalf("undo status = %d, want %d: %v", status, http.StatusConflict, body)
	}
	blockers, _ := body["blocked_by"].([]any)
	if len(blockers) != 1 {
		t.Fatalf("blocked_by = %v, want exactly Bob's update", body["blocked_by"])
	}
	blocker := blockers[0].(map[string]any)
	if blocker["changed_by"] != bob || blocker["operation"] != "UPDATE" {
		t.Fatalf("blocker = %v, want Bob's UPDATE", blocker)
	}
	_, still := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, alice),
		map[string]string{"sql": "SELECT name FROM items WHERE id = 1"})
	if name := extractRowValues(t, still["rows"].([]any)[0])[0]; name != "cheese" {
		t.Fatalf("name = %v, want cheese (a refused undo must leave Bob's edit in place)", name)
	}

	// Reverting the blocker restores the inserted state, so the undo now applies and the row is gone.
	status, resp := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%d/revert", int64(blocker["id"].(float64)))), userToken(t, bob), nil)
	if status != http.StatusOK {
		t.Fatalf("revert of the blocker: status = %d, want %d: %v", status, http.StatusOK, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, alice), nil)
	if status != http.StatusOK {
		t.Fatalf("undo after the blocker was reverted: status = %d, want %d: %v", status, http.StatusOK, resp)
	}
	_, count := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, alice),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	if c := extractRowValues(t, count["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("rows after undo = %v, want 0", c)
	}
}

// TestUndo_InsertRefusedWhenAColumnWasDropped pins the schema-drift conflict: a column the insert recorded is gone,
// so the undo names the column, points at the schema change as the blocker, and hints at reverting it.
func TestUndo_InsertRefusedWhenAColumnWasDropped(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	alice := uuid.New().String()
	bob := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, alice),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, alice),
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 2)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, bob),
		map[string]string{"sql": "ALTER TABLE items DROP COLUMN qty"})

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, alice), nil)
	if status != http.StatusConflict {
		t.Fatalf("undo status = %d, want %d: %v", status, http.StatusConflict, body)
	}
	if reason, _ := body["reason"].(string); !strings.Contains(reason, `no longer has column "qty"`) {
		t.Errorf("reason = %q, want the dropped column named", reason)
	}
	blockers, _ := body["blocked_by"].([]any)
	if len(blockers) != 1 || blockers[0].(map[string]any)["kind"] != "DDL" {
		t.Errorf("blocked_by = %v, want Bob's DROP COLUMN entry", body["blocked_by"])
	}
	hints, _ := body["resolution_hints"].([]any)
	if len(hints) != 2 || !strings.Contains(hints[0].(string), "schema change") ||
		hints[1] != fmt.Sprintf("Blocked by entry %v.", blockers[0].(map[string]any)["id"]) {
		t.Errorf("resolution_hints = %v, want a schema-change hint and the blocking entry", body["resolution_hints"])
	}
}

// TestExecuteData_BlobValueIsRefusedClearly pins that a write carrying a BLOB is refused with the domain's own error
// rather than SQLite's trigger message, as a 400, whatever the column's declared type, and that nothing is written.
func TestExecuteData_BlobValueIsRefusedClearly(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE b (id INTEGER PRIMARY KEY, data BLOB, note TEXT)"})

	for _, sql := range []string{
		"INSERT INTO b (data) VALUES (x'00ff')",
		"INSERT INTO b (note) VALUES (x'00ff')",
	} {
		status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
			map[string]string{"sql": sql})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want %d: %v", sql, status, http.StatusBadRequest, body)
		}
		if msg, _ := body["error"].(string); !strings.Contains(msg, "BLOB values are not supported") ||
			strings.Contains(msg, "JSON cannot hold") {
			t.Errorf("%s: error = %q, want the domain message without SQLite's trigger wording", sql, msg)
		}
	}

	_, count := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM b"})
	if c := extractRowValues(t, count["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("rows after the refused writes = %v, want 0", c)
	}
}

// TestRevert_ConcurrentWrites_NeverApplyAStaleInverse races a writer against a reverter on one row. A revert may be
// refused as a conflict or succeed, but a successful revert must have inverted the state its check validated: the
// audit row it writes records as old_values exactly the new_values of the entry it reverted. A revert that checked,
// let a newer write land, and then applied would record that newer write's values instead.
func TestRevert_ConcurrentWrites_NeverApplyAStaleInverse(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	writer := uuid.New().String()
	reverter := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, writer),
		map[string]string{"sql": "CREATE TABLE c (id INTEGER PRIMARY KEY, v INTEGER)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, writer),
		map[string]string{"sql": "INSERT INTO c (id, v) VALUES (1, 0)"})

	type applied struct {
		revertBatch string
		targetNew   map[string]any
	}
	// revertLatest reverts the newest user UPDATE and reports the revert batch when it was applied.
	revertLatest := func() (applied, bool) {
		_, latest := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, reverter),
			map[string]string{"sql": "SELECT id, new_values FROM _change_log " +
				"WHERE kind = 'DML' AND operation = 'UPDATE' AND origin = 'change' ORDER BY id DESC LIMIT 1"})
		rows, _ := latest["rows"].([]any)
		if len(rows) == 0 {
			return applied{}, false
		}
		vals := extractRowValues(t, rows[0])
		var targetNew map[string]any
		if err := json.Unmarshal([]byte(vals[1].(string)), &targetNew); err != nil {
			return applied{}, false
		}
		status, resp := testutil.DoRequest(t, router, http.MethodPost,
			dbPath(bdbID, fmt.Sprintf("changes/%d/revert", int64(vals[0].(float64)))), userToken(t, reverter), nil)
		if status != http.StatusOK {
			return applied{}, false
		}
		batch, _ := resp["batch_id"].(string)
		return applied{revertBatch: batch, targetNew: targetNew}, batch != ""
	}

	const writes, reverts = 150, 40
	var wg sync.WaitGroup
	var results []applied
	var conflicts int
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range writes {
			testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, writer),
				map[string]string{"sql": "UPDATE c SET v = v + 1 WHERE id = 1"})
		}
	}()
	go func() {
		defer wg.Done()
		for range reverts {
			if a, ok := revertLatest(); ok {
				results = append(results, a)
			} else {
				conflicts++
			}
		}
	}()
	wg.Wait()

	// With the writer quiet, one more revert must go through, so the invariant is checked on at least one success.
	a, ok := revertLatest()
	if !ok {
		t.Fatal("revert of the latest update with no concurrent writer was refused")
	}
	results = append(results, a)
	t.Logf("reverts applied: %d, refused: %d", len(results), conflicts)

	for _, r := range results {
		_, audit := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, reverter),
			map[string]string{"sql": fmt.Sprintf(
				"SELECT old_values FROM _change_log WHERE batch_id = '%s' AND kind = 'DML'", r.revertBatch)})
		rows, _ := audit["rows"].([]any)
		if len(rows) != 1 {
			t.Fatalf("revert batch %s wrote %d DML rows, want 1", r.revertBatch, len(rows))
		}
		var old map[string]any
		if err := json.Unmarshal([]byte(extractRowValues(t, rows[0])[0].(string)), &old); err != nil {
			t.Fatalf("decoding old_values: %v", err)
		}
		if old["v"] != r.targetNew["v"] {
			t.Errorf("revert %s inverted v=%v but its check validated v=%v: a write landed between check and apply",
				r.revertBatch, old["v"], r.targetNew["v"])
		}
	}
}

// TestRedo_DDLFailureIsAClientError pins that a DDL redo whose statement can no longer run — here a CREATE TABLE
// whose name another user took in the meantime — is refused with a client error inside the transaction, not a 500.
func TestRedo_DDLFailureIsAClientError(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	alice := uuid.New().String()
	bob := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, alice),
		map[string]string{"sql": "CREATE TABLE d (id INTEGER PRIMARY KEY)"})
	if status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, alice), nil); status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d", status, http.StatusOK)
	}
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, bob),
		map[string]string{"sql": "CREATE TABLE d (id INTEGER PRIMARY KEY, note TEXT)"})

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
		userToken(t, alice), nil)
	if status < 400 || status >= 500 {
		t.Fatalf("redo status = %d, want a 4xx: %v", status, body)
	}
	if body["error"] == nil {
		t.Errorf("redo body = %v, want an error message", body)
	}

	_, cols := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, bob),
		map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('d') WHERE name = 'note'"})
	if c := extractRowValues(t, cols["rows"].([]any)[0])[0]; c != float64(1) {
		t.Errorf("Bob's table must survive Alice's failed redo, note column count = %v", c)
	}
}

// (Removed: TestUndo_Force — the force=true flag was retired in the
// unified change-log activation flip. Conflicts are now surfaced via a
// structured 409 ConflictError; callers resolve blockers explicitly.)

// (Removed: TestUndo_TransactionRollsBackOnPartialFailure — relied on
// force=true to bypass conflict detection so an in-tx UNIQUE-constraint
// rollback could be observed. Without force, the conflict detector
// catches the colliding row first and refuses the undo with a structured
// 409, so the in-tx failure path it exercised is no longer reachable
// from the public API.)

func TestUndo_OriginIsUndo(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT origin FROM _change_log WHERE kind = 'DML' AND origin = 'undo'"})
	rows := resp["rows"].([]any)
	if len(rows) == 0 {
		t.Error("expected at least one mutation log entry with origin = 'undo'")
	}
}

// --- Redo tests ---

func TestRedo_AfterUndoInsert(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("redo status = %d, want %d", status, http.StatusOK)
	}

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM items"})
	rows := selectResp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after redo, got %d", len(rows))
	}
	if extractRowValues(t, rows[0])[0] != "milk" {
		t.Errorf("name = %v, want milk", extractRowValues(t, rows[0])[0])
	}
}

func TestRedo_AfterUndoUpdate(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 2)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET qty = 6 WHERE name = 'milk'"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
		userToken(t, userID), nil)

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT qty FROM items WHERE name = 'milk'"})
	qty := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if qty != float64(6) {
		t.Errorf("qty = %v, want 6 (re-applied update)", qty)
	}
}

func TestRedo_AfterUndoDelete(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('eggs')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "DELETE FROM items WHERE name = 'eggs'"})

	// Undo the delete → eggs restored.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	// Redo → eggs deleted again.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
		userToken(t, userID), nil)

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	count := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if count != float64(0) {
		t.Errorf("expected 0 rows after redo (delete re-applied), got %v", count)
	}
}

func TestRedo_Sequential(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET name = 'bread'"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "DELETE FROM items"})

	// Undo x3 → back to empty.
	for i := 0; i < 3; i++ {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
			userToken(t, userID), nil)
	}

	// Redo x3 → back to post-delete (empty).
	for i := 0; i < 3; i++ {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
			userToken(t, userID), nil)
	}

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	count := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if count != float64(0) {
		t.Errorf("expected 0 rows after redo x3 (back to post-delete), got %v", count)
	}
}

func TestRedo_NothingToRedo(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY)"})

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
		userToken(t, userID), nil)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestRedo_ClearedByNewChange(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Undo → redo stack populated.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	// New mutation → clears redo stack.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('eggs')"})

	// Redo → nothing to redo.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
		userToken(t, userID), nil)
	if status != http.StatusBadRequest {
		t.Errorf("redo after new mutation: status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestUndoRedo_Interleaved(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 2)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET qty = 10 WHERE name = 'milk'"})

	// Undo → qty back to 2.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	_, r1 := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT qty FROM items"})
	if extractRowValues(t, r1["rows"].([]any)[0])[0] != float64(2) {
		t.Fatalf("after undo: qty = %v, want 2", extractRowValues(t, r1["rows"].([]any)[0])[0])
	}

	// Redo → qty back to 10.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
		userToken(t, userID), nil)
	_, r2 := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT qty FROM items"})
	if extractRowValues(t, r2["rows"].([]any)[0])[0] != float64(10) {
		t.Fatalf("after redo: qty = %v, want 10", extractRowValues(t, r2["rows"].([]any)[0])[0])
	}

	// Undo again → qty back to 2.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	_, r3 := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT qty FROM items"})
	if extractRowValues(t, r3["rows"].([]any)[0])[0] != float64(2) {
		t.Errorf("after second undo: qty = %v, want 2", extractRowValues(t, r3["rows"].([]any)[0])[0])
	}
}

func TestUndoRedo_UndoUndoRedoUndo(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('eggs')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('bread')"})

	// Undo bread, undo eggs.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	// Redo eggs.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"),
		userToken(t, userID), nil)

	// Undo eggs again.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	// Should have only milk (equivalent to undo x2).
	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM items"})
	rows := selectResp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if extractRowValues(t, rows[0])[0] != "milk" {
		t.Errorf("name = %v, want milk", extractRowValues(t, rows[0])[0])
	}
}

// --- Revert tests ---

func TestRevert_InsertThenRevert(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Gets the mutation log ID.
	_, mutationLogResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DML' AND operation = 'INSERT' LIMIT 1"})
	mutationLogID := strconv.FormatInt(int64(extractRowValues(t, mutationLogResp["rows"].([]any)[0])[0].(float64)), 10)

	// Reverts the INSERT.
	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%s/revert", mutationLogID)),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("revert status = %d, want %d", status, http.StatusOK)
	}

	// Verifies the row is gone.
	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	count := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if count != float64(0) {
		t.Errorf("expected 0 rows after revert, got %v", count)
	}

	// Verifies the revert itself was logged in mutation log (as a DELETE).
	_, clResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT operation FROM _change_log WHERE kind = 'DML' ORDER BY id DESC LIMIT 1"})
	lastOp := extractRowValues(t, clResp["rows"].([]any)[0])[0]
	if lastOp != "DELETE" {
		t.Errorf("last mutation log operation = %v, want DELETE (from revert)", lastOp)
	}
}

func TestRevert_UpdateThenRevert(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET name = 'bread' WHERE id = 1"})

	// Gets the UPDATE mutation log ID.
	_, mutationLogResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DML' AND operation = 'UPDATE' LIMIT 1"})
	mutationLogID := strconv.FormatInt(int64(extractRowValues(t, mutationLogResp["rows"].([]any)[0])[0].(float64)), 10)

	// Reverts the UPDATE.
	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%s/revert", mutationLogID)),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("revert status = %d, want %d", status, http.StatusOK)
	}

	// Verifies the row has original value.
	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM items WHERE id = 1"})
	name := extractRowValues(t, selectResp["rows"].([]any)[0])[0]
	if name != "milk" {
		t.Errorf("name = %v, want milk", name)
	}
}

func TestRevert_DeleteThenRevert(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "DELETE FROM items WHERE id = 1"})

	// Gets the DELETE mutation log ID.
	_, mutationLogResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DML' AND operation = 'DELETE' LIMIT 1"})
	mutationLogID := strconv.FormatInt(int64(extractRowValues(t, mutationLogResp["rows"].([]any)[0])[0].(float64)), 10)

	// Reverts the DELETE.
	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%s/revert", mutationLogID)),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("revert status = %d, want %d", status, http.StatusOK)
	}

	// Verifies the row is back.
	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM items WHERE id = 1"})
	rows := selectResp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after revert, got %d", len(rows))
	}
	name := extractRowValues(t, rows[0])[0]
	if name != "milk" {
		t.Errorf("name = %v, want milk", name)
	}
}

func TestRevert_Conflict(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// First update.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET name = 'bread' WHERE id = 1"})

	// Gets the UPDATE mutation log ID.
	_, mutationLogResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DML' AND operation = 'UPDATE' LIMIT 1"})
	mutationLogID := strconv.FormatInt(int64(extractRowValues(t, mutationLogResp["rows"].([]any)[0])[0].(float64)), 10)

	// Second update (changes the row again).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET name = 'eggs' WHERE id = 1"})

	// Tries to revert the first update — should conflict, naming the second update as the blocker.
	status, body := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%s/revert", mutationLogID)),
		userToken(t, userID), nil)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d (conflict)", status, http.StatusConflict)
	}
	blockers, _ := body["blocked_by"].([]any)
	if len(blockers) != 1 {
		t.Fatalf("blocked_by = %v, want exactly the second update", body["blocked_by"])
	}
	blocker := blockers[0].(map[string]any)
	if blocker["operation"] != "UPDATE" {
		t.Errorf("blocker operation = %v, want UPDATE", blocker["operation"])
	}
	changedAt, err := time.Parse(time.RFC3339Nano, blocker["changed_at"].(string))
	if err != nil {
		t.Fatalf("blocker changed_at = %v, want an RFC 3339 timestamp: %v", blocker["changed_at"], err)
	}
	if changedAt.IsZero() || time.Since(changedAt) > time.Minute {
		t.Errorf("blocker changed_at = %v, want the moment the second update ran", changedAt)
	}
}

// (Removed: TestRevert_ConflictForceOverride — force=true was retired
// in the unified change-log activation flip. Revert conflicts are now
// returned as a structured 409 ConflictError; there is no override.)

func TestRevert_NotFound(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Ensures database exists.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, "changes/99999/revert"),
		userToken(t, userID), nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- Batch revert tests ---

func TestRevert_ByBatchID(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	for _, item := range []string{"milk", "eggs", "bread"} {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (name, qty) VALUES ('%s', 1)", item)})
	}

	// Multi-row update — one batch.
	_, updateResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "UPDATE items SET qty = 10"})
	batchID := updateResp["batch_id"].(string)

	// Reverts the entire batch.
	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/batch/%s/revert", batchID)),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("batch revert status = %d, want %d", status, http.StatusOK)
	}

	// All rows should have qty=1 (original value).
	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT qty FROM items ORDER BY name"})
	rows := selectResp["rows"].([]any)
	for i, row := range rows {
		qty := extractRowValues(t, row)[0]
		if qty != float64(1) {
			t.Errorf("row %d qty = %v, want 1", i, qty)
		}
	}
}

func TestRevert_ByBatchID_NotFound(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY)"})

	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, "changes/batch/nonexistent-batch-id/revert"),
		userToken(t, userID), nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestRevert_PushesToUndoStack(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Gets the mutation log ID for single-entry revert.
	_, mlResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DML' AND operation = 'INSERT' LIMIT 1"})
	mlID := strconv.FormatInt(int64(extractRowValues(t, mlResp["rows"].([]any)[0])[0].(float64)), 10)

	testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%s/revert", mlID)),
		userToken(t, userID), nil)

	// The revert should be on the undo stack. CREATE TABLE + INSERT +
	// revert = 3 entries under the unified-change-log invariant.
	_, stackResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM _undo_stack"})
	count := extractRowValues(t, stackResp["rows"].([]any)[0])[0]
	if count != float64(3) {
		t.Errorf("undo stack count = %v, want 3 (create + insert + revert)", count)
	}
}

func TestRevert_UndoAfterRevert(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	_, mlResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DML' AND operation = 'INSERT' LIMIT 1"})
	mlID := strconv.FormatInt(int64(extractRowValues(t, mlResp["rows"].([]any)[0])[0].(float64)), 10)

	// Revert the insert → row deleted.
	testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%s/revert", mlID)),
		userToken(t, userID), nil)

	// Undo the revert → row restored.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)

	_, selectResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM items"})
	rows := selectResp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after undo-revert, got %d", len(rows))
	}
	if extractRowValues(t, rows[0])[0] != "milk" {
		t.Errorf("name = %v, want milk", extractRowValues(t, rows[0])[0])
	}
}

func TestRevert_OriginIsRevert(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	_, mlResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DML' AND operation = 'INSERT' LIMIT 1"})
	mlID := strconv.FormatInt(int64(extractRowValues(t, mlResp["rows"].([]any)[0])[0].(float64)), 10)

	testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%s/revert", mlID)),
		userToken(t, userID), nil)

	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT origin FROM _change_log WHERE kind = 'DML' AND origin = 'revert'"})
	rows := resp["rows"].([]any)
	if len(rows) == 0 {
		t.Error("expected at least one mutation log entry with origin = 'revert'")
	}
}

// --- Stack cleanup tests ---

func TestUndoStack_CappedAt50(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, val INTEGER)"})

	// Executes 55 mutations to exceed the cap of 50.
	for i := 0; i < 55; i++ {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (val) VALUES (%d)", i)})
	}

	// Verifies the undo stack is capped at 50.
	_, stackResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM _undo_stack"})
	count := extractRowValues(t, stackResp["rows"].([]any)[0])[0]
	if count != float64(50) {
		t.Errorf("undo stack count = %v, want 50", count)
	}

	// Verifies undo works for the 50 most recent.
	for i := 0; i < 50; i++ {
		status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
			userToken(t, userID), nil)
		if status != http.StatusOK {
			t.Fatalf("undo %d: status = %d, want %d", i+1, status, http.StatusOK)
		}
	}

	// 51st undo should fail — stack is empty.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	if status != http.StatusBadRequest {
		t.Errorf("51st undo: status = %d, want %d (nothing to undo)", status, http.StatusBadRequest)
	}
}

func TestUndoStack_PrunedOnRetention(t *testing.T) {
	truncateAll(t)
	router, sqliteClient := setupStack(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Gets the batch_id of the first DML mutation (the preceding CREATE
	// TABLE also lives on the undo stack, so we filter to DML kind).
	_, mlResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT us.batch_id FROM _undo_stack us " +
			"JOIN _change_log cl ON cl.batch_id = us.batch_id " +
			"WHERE cl.kind = 'DML' " +
			"GROUP BY us.batch_id, us.position " +
			"ORDER BY us.position LIMIT 1"})
	firstBatchID := extractRowValues(t, mlResp["rows"].([]any)[0])[0].(string)

	// Simulates retention cleanup: directly deletes the mutation log entries for this batch.
	_, dbResp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""),
		userToken(t, userID), nil)
	databaseID := dbResp["id"].(string)

	_, err := sqliteClient.ExecuteInternal(context.Background(), databaseID,
		fmt.Sprintf("DELETE FROM _change_log WHERE kind = 'DML' AND batch_id = '%s'", firstBatchID), nil)
	if err != nil {
		t.Fatalf("direct delete: %v", err)
	}

	// Triggers cleanup by doing another mutation.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('eggs')"})

	// Verifies the pruned batch is no longer on the undo stack.
	_, stackResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": fmt.Sprintf("SELECT COUNT(*) FROM _undo_stack WHERE batch_id = '%s'", firstBatchID)})
	prunedCount := extractRowValues(t, stackResp["rows"].([]any)[0])[0]
	if prunedCount != float64(0) {
		t.Errorf("pruned batch should not be on undo stack, got count = %v", prunedCount)
	}
}

// --- Mutation log cleanup tests ---

func TestChangeLogCleanup_OldEntriesDeleted(t *testing.T) {
	truncateAll(t)
	router, sqliteClient := setupStack(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates table and inserts a row (creates a recent mutation log entry).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Manually inserts an old mutation log entry (8 days ago) via direct client (bypasses reserved table protection).
	databaseID := getDatabaseID(t, bdbID)
	_, err := sqliteClient.ExecuteInternal(context.Background(), databaseID,
		"INSERT INTO _change_log (kind, table_name, row_id, operation, new_values, changed_by, changed_at) "+
			"VALUES ('DML', 'items', '99', 'INSERT', '{\"name\":\"old\"}', 'old_user', "+
			"strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-8 days'))", nil)
	if err != nil {
		t.Fatalf("inserting old mutation log entry: %v", err)
	}

	// Verifies we have 2 entries (1 recent + 1 old).
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM _change_log WHERE kind = 'DML'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	rows := resp["rows"].([]any)
	count := extractRowValues(t, rows[0])[0]
	if count != float64(2) {
		t.Fatalf("expected 2 mutation log entries before cleanup, got %v", count)
	}

	// Executes another DML to trigger cleanup.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('bread')"})

	// Verifies old entry was deleted and only recent entries remain.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT changed_by FROM _change_log WHERE kind = 'DML' ORDER BY id"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	rows = resp["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 mutation log entries after cleanup, got %d", len(rows))
	}
	// Both remaining entries should belong to the current user, not 'old_user'.
	for i, r := range rows {
		changedBy := extractRowValues(t, r)[0]
		if changedBy != userID {
			t.Errorf("entry %d: changed_by = %v, want %s (old entry should have been deleted)", i, changedBy, userID)
		}
	}
}

func TestSchemaLogCleanup_OldEntriesDeleted(t *testing.T) {
	truncateAll(t)
	router, sqliteClient := setupStack(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates a table (creates a recent schema log entry).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Manually inserts an old schema log entry (8 days ago) via direct client.
	databaseID := getDatabaseID(t, bdbID)
	_, err := sqliteClient.ExecuteInternal(context.Background(), databaseID,
		"INSERT INTO _change_log (kind, operation, object_type, object_name, ddl_statement, changed_by, changed_at) "+
			"VALUES ('DDL', 'CREATE', 'TABLE', 'old_table', 'CREATE TABLE old_table (id INT)', 'old_user', "+
			"strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-8 days'))", nil)
	if err != nil {
		t.Fatalf("inserting old schema log entry: %v", err)
	}

	// Verifies 2 entries.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM _change_log WHERE kind = 'DDL'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	rows := resp["rows"].([]any)
	count := extractRowValues(t, rows[0])[0]
	if count != float64(2) {
		t.Fatalf("expected 2 schema log entries before cleanup, got %v", count)
	}

	// Executes another DDL to trigger cleanup.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN category TEXT"})

	// Verifies old entry was deleted and only recent entries remain.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT changed_by FROM _change_log WHERE kind = 'DDL' ORDER BY id"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}
	rows = resp["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 schema log entries after cleanup, got %d", len(rows))
	}
	// Both remaining entries should belong to the current user, not 'old_user'.
	for i, r := range rows {
		changedBy := extractRowValues(t, r)[0]
		if changedBy != userID {
			t.Errorf("entry %d: changed_by = %v, want %s (old entry should have been deleted)", i, changedBy, userID)
		}
	}
}

// TestRetention_PrunesChangeLog_DDL_CascadesToSnapshot pins that pruning an old _change_log DDL row cascades through
// the FK to delete its _change_snapshots sidecar row atomically. Without the cascade, snapshots would orphan and
// silently bloat the database.
func TestRetention_PrunesChangeLog_DDL_CascadesToSnapshot(t *testing.T) {
	truncateAll(t)
	router, sqliteClient := setupStack(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	ctx := context.Background()

	// Bootstraps the database via a recent DDL — keeps cleanup eligible to run.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	databaseID := getDatabaseID(t, bdbID)

	// Inserts an old DDL _change_log row dated 8 days ago (past the 7-day retention).
	if _, err := sqliteClient.ExecuteInternal(ctx, databaseID,
		"INSERT INTO _change_log (kind, operation, object_type, object_name, ddl_statement, changed_by, changed_at) "+
			"VALUES ('DDL', 'DROP', 'TABLE', 'old_dropped_table', 'DROP TABLE old_dropped_table', 'old_user', "+
			"strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-8 days'))", nil); err != nil {
		t.Fatalf("inserting old DDL change_log row: %v", err)
	}

	// Reads back the inserted id so the matching snapshot row can reference it.
	idResult, err := sqliteClient.ExecuteInternal(ctx, databaseID,
		"SELECT id FROM _change_log WHERE changed_by = 'old_user'", nil)
	if err != nil {
		t.Fatalf("fetching old row id: %v", err)
	}
	if len(idResult.Rows) != 1 {
		t.Fatalf("expected 1 old row, got %d", len(idResult.Rows))
	}
	oldID, _ := idResult.Rows[0][0].Value.(int64)

	// Inserts a matching _change_snapshots sidecar row.
	if _, err := sqliteClient.ExecuteInternal(ctx, databaseID,
		"INSERT INTO _change_snapshots (change_log_id, dependent_objects, row_payload, row_count_total, truncated) "+
			"VALUES (?, '[]', '[]', 0, 0)", []domain.TypedValue{domain.IntegerValue(oldID)}); err != nil {
		t.Fatalf("inserting old snapshot row: %v", err)
	}

	// Confirms both rows exist before cleanup.
	beforeSnap, err := sqliteClient.ExecuteInternal(ctx, databaseID,
		"SELECT COUNT(*) FROM _change_snapshots WHERE change_log_id = ?",
		[]domain.TypedValue{domain.IntegerValue(oldID)})
	if err != nil {
		t.Fatalf("counting snapshots before: %v", err)
	}
	if got, _ := beforeSnap.Rows[0][0].Value.(int64); got != 1 {
		t.Fatalf("expected 1 snapshot row before cleanup, got %d", got)
	}

	// Triggers cleanup by running another DDL (which calls cleanupChangeLog post-commit).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN qty INTEGER"})

	// Old change_log row gone.
	afterLog, err := sqliteClient.ExecuteInternal(ctx, databaseID,
		"SELECT COUNT(*) FROM _change_log WHERE id = ?",
		[]domain.TypedValue{domain.IntegerValue(oldID)})
	if err != nil {
		t.Fatalf("counting change_log after: %v", err)
	}
	if got, _ := afterLog.Rows[0][0].Value.(int64); got != 0 {
		t.Errorf("old _change_log row not pruned: count = %d, want 0", got)
	}

	// FK cascade: matching snapshot row also gone.
	afterSnap, err := sqliteClient.ExecuteInternal(ctx, databaseID,
		"SELECT COUNT(*) FROM _change_snapshots WHERE change_log_id = ?",
		[]domain.TypedValue{domain.IntegerValue(oldID)})
	if err != nil {
		t.Fatalf("counting snapshots after: %v", err)
	}
	if got, _ := afterSnap.Rows[0][0].Value.(int64); got != 0 {
		t.Errorf("snapshot row not cascade-deleted: count = %d, want 0", got)
	}
}

// --- Schema log tests ---

func TestSchemaLog_CreateTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT operation, object_type, object_name, changed_by FROM _change_log " +
			"WHERE kind = 'DDL'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 schema log entry, got %d", len(rows))
	}

	row := extractRowValues(t, rows[0])
	if row[0] != "CREATE" {
		t.Errorf("operation = %v, want CREATE", row[0])
	}
	if row[1] != "TABLE" {
		t.Errorf("object_type = %v, want TABLE", row[1])
	}
	if row[2] != "items" {
		t.Errorf("object_name = %v, want items", row[2])
	}
	if row[3] != userID {
		t.Errorf("changed_by = %v, want %s", row[3], userID)
	}
}

func TestSchemaLog_AlterTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN category TEXT"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT operation, object_type, object_name FROM _change_log WHERE kind = 'DDL' " +
			"AND operation = 'ALTER'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 ALTER entry, got %d", len(rows))
	}

	row := extractRowValues(t, rows[0])
	if row[0] != "ALTER" || row[1] != "TABLE" || row[2] != "items" {
		t.Errorf("got %v/%v/%v, want ALTER/TABLE/items", row[0], row[1], row[2])
	}
}

func TestSchemaLog_DropTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "DROP TABLE items"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT operation, object_type, object_name FROM _change_log WHERE kind = 'DDL' " +
			"AND operation = 'DROP'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 DROP entry, got %d", len(rows))
	}

	row := extractRowValues(t, rows[0])
	if row[0] != "DROP" || row[1] != "TABLE" || row[2] != "items" {
		t.Errorf("got %v/%v/%v, want DROP/TABLE/items", row[0], row[1], row[2])
	}
}

func TestSchemaLog_CreateIndex(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE INDEX idx_items_name ON items (name)"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT operation, object_type, object_name FROM _change_log WHERE kind = 'DDL' " +
			"AND object_type = 'INDEX'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 INDEX entry, got %d", len(rows))
	}

	row := extractRowValues(t, rows[0])
	if row[0] != "CREATE" || row[1] != "INDEX" {
		t.Errorf("got %v/%v, want CREATE/INDEX", row[0], row[1])
	}
}

// --- Trigger regeneration tests ---

func TestAlterTable_RegeneratesTriggersWithNewColumn(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates table and inserts a row (triggers log 2 columns: id, name).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// Adds a column — triggers should be regenerated.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN category TEXT"})

	// Inserts a row with the new column.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name, category) VALUES ('bread', 'bakery')"})

	// Queries mutation log for the second INSERT — it should include the new column.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT new_values FROM _change_log WHERE kind = 'DML' AND operation = 'INSERT' " +
			"ORDER BY id DESC LIMIT 1"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	newValues, _ := extractRowValues(t, rows[0])[0].(string)
	if !strings.Contains(newValues, "category") {
		t.Errorf("new_values should contain 'category' after ALTER TABLE, got %s", newValues)
	}
	if !strings.Contains(newValues, "bakery") {
		t.Errorf("new_values should contain 'bakery', got %s", newValues)
	}
}

func TestAlterTable_DropColumn_RegeneratesTriggers(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates a table with three columns; triggers reference all three.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE recipes (id INTEGER PRIMARY KEY, name TEXT, tags TEXT)"})

	// Drops the 'tags' column. SQLite refuses today because the existing triggers reference NEW.tags.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "ALTER TABLE recipes DROP COLUMN tags"})
	if status != http.StatusOK {
		t.Fatalf("ALTER TABLE DROP COLUMN status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	// Inserts a row using the new schema.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO recipes (name) VALUES ('Baguette')"})

	// The mutation log entry must reflect the post-ALTER schema (no 'tags' key).
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT new_values FROM _change_log WHERE kind = 'DML' AND table_name = 'recipes' " +
			"AND operation = 'INSERT'"})
	if status != http.StatusOK {
		t.Fatalf("query status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 INSERT mutation log entry, got %d", len(rows))
	}

	newValues, _ := extractRowValues(t, rows[0])[0].(string)
	if !strings.Contains(newValues, "name") {
		t.Errorf("new_values should contain 'name', got %s", newValues)
	}
	if strings.Contains(newValues, "tags") {
		t.Errorf("new_values must NOT contain 'tags' after DROP COLUMN, got %s", newValues)
	}
}

func TestAlterTable_RenameColumn_RegeneratesTriggers(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE recipes (id INTEGER PRIMARY KEY, old_name TEXT)"})

	// SQLite refuses today because the existing triggers reference NEW.old_name after rename.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "ALTER TABLE recipes RENAME COLUMN old_name TO new_name"})
	if status != http.StatusOK {
		t.Fatalf("ALTER TABLE RENAME COLUMN status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO recipes (new_name) VALUES ('Cake')"})

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT new_values FROM _change_log WHERE kind = 'DML' AND table_name = 'recipes' " +
			"AND operation = 'INSERT'"})
	if status != http.StatusOK {
		t.Fatalf("query status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 INSERT mutation log entry, got %d", len(rows))
	}

	newValues, _ := extractRowValues(t, rows[0])[0].(string)
	if !strings.Contains(newValues, "new_name") {
		t.Errorf("new_values should contain 'new_name', got %s", newValues)
	}
	if strings.Contains(newValues, "old_name") {
		t.Errorf("new_values must NOT contain 'old_name' after RENAME COLUMN, got %s", newValues)
	}
}

func TestAlterTable_FailureRollsBackPreDroppedTriggers(t *testing.T) {
	// Single-statement ALTER pre-drops mutation_log triggers in the
	// same transaction. If the user's ALTER fails, the rollback must
	// restore the triggers so subsequent INSERTs still log mutations.
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Force the ALTER to fail: dropping a non-existent column.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "ALTER TABLE items DROP COLUMN nonexistent"})
	if status == http.StatusOK {
		t.Fatal("expected ALTER DROP COLUMN of missing column to fail")
	}

	// Insert after the failed ALTER — the mutation_log triggers must still fire.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM _change_log WHERE kind = 'DML' AND table_name = 'items'"})
	if status != http.StatusOK {
		t.Fatalf("query: status=%d body=%v", status, resp)
	}
	rows := resp["rows"].([]any)
	count := extractRowValues(t, rows[0])[0].(float64)
	if count != 1 {
		t.Errorf("_change_log row count after rollback+INSERT = %v, want 1 (triggers must be intact)", count)
	}
}

func TestCreateTable_TriggersCreatedAtomically(t *testing.T) {
	// CREATE TABLE and its mutation_log triggers run in one
	// transaction; an INSERT in the very next request must log a row.
	// Pre-unification, trigger creation was a non-atomic post-step,
	// leaving a window where an INSERT could fire before triggers
	// existed. This test guards against regressing to that.
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO notes (body) VALUES ('first')"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM _change_log WHERE kind = 'DML' AND table_name = 'notes'"})
	if status != http.StatusOK {
		t.Fatalf("query: status=%d body=%v", status, resp)
	}
	rows := resp["rows"].([]any)
	count := extractRowValues(t, rows[0])[0].(float64)
	if count != 1 {
		t.Errorf("_change_log row count after CREATE TABLE + INSERT = %v, want 1", count)
	}
}

func TestAlterTable_RenameTo_RegeneratesTriggers(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE old_recipes (id INTEGER PRIMARY KEY, name TEXT)"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "ALTER TABLE old_recipes RENAME TO new_recipes"})
	if status != http.StatusOK {
		t.Fatalf("ALTER TABLE RENAME TO status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	// Inserting into the renamed table currently fails silently in mutation log because
	// triggers were dropped (under old name) but never recreated (PRAGMA on old name returns empty).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]string{"sql": "INSERT INTO new_recipes (name) VALUES ('Cake')"})

	// Asserts the mutation log entry exists under the new table name.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT table_name FROM _change_log WHERE kind = 'DML' AND operation = 'INSERT'"})
	if status != http.StatusOK {
		t.Fatalf("query status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 INSERT mutation log entry, got %d", len(rows))
	}
	tableName := extractRowValues(t, rows[0])[0]
	if tableName != "new_recipes" {
		t.Errorf("mutation log table_name = %v, want new_recipes", tableName)
	}

	// Asserts triggers are named after the new table.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM sqlite_master WHERE type = 'trigger' " +
			"AND name LIKE '_change_log_%' ORDER BY name"})
	if status != http.StatusOK {
		t.Fatalf("trigger query status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	triggerRows := resp["rows"].([]any)
	if len(triggerRows) != 3 {
		t.Fatalf("expected 3 triggers under new table name, got %d", len(triggerRows))
	}
	for _, r := range triggerRows {
		name, _ := extractRowValues(t, r)[0].(string)
		if !strings.HasPrefix(name, "_change_log_new_recipes_after_") {
			t.Errorf("trigger %q should be named _change_log_new_recipes_after_*", name)
		}
	}
}

func TestDropTable_NoErrors(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Drops the table — SQLite auto-drops triggers, no errors expected.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "DROP TABLE items"})
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}
}

func TestCreateIndex_TriggersStillWork(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Creates an index — triggers should still work afterwards.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE INDEX idx_items_name ON items (name)"})
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d", status, http.StatusOK)
	}

	// Verifies triggers still exist after index creation.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' " +
			"AND name LIKE '_change_log_items%'"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	count := extractRowValues(t, rows[0])[0]
	if count != float64(3) {
		t.Errorf("expected 3 triggers, got %v", count)
	}
}

// --- Platform table initialization tests ---

func TestGetDatabase_CreatesPlatformTables(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Auto-creates the database (which should also create platform tables).
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	// Queries sqlite_master for _change_log and _change_snapshots.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE '\\_%' " +
			"ESCAPE '\\' ORDER BY name"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	expectedTables := []string{
		"_change_log", "_change_snapshots", "_formulas", "_redo_stack",
		"_skill_targets", "_skills", "_undo_stack",
	}
	if len(rows) != len(expectedTables) {
		t.Fatalf("expected %d platform tables, got %d", len(expectedTables), len(rows))
	}

	for i, expected := range expectedTables {
		actual := extractRowValues(t, rows[i])[0]
		if actual != expected {
			t.Errorf("table[%d] = %v, want %s", i, actual, expected)
		}
	}
}

func TestGetDatabase_PlatformTablesHaveIndexes(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Auto-creates the database.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	// Queries for indexes on the unified _change_log table.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT name FROM sqlite_master WHERE type = 'index' " +
			"AND name LIKE 'idx_change_log_%' ORDER BY name"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	rows := resp["rows"].([]any)
	// Migration v4 creates 4 _change_log indexes: kind+changed_at,
	// table+changed_at, changed_at, batch_id.
	if len(rows) != 4 {
		t.Fatalf("expected 4 _change_log indexes, got %d", len(rows))
	}
}

func TestGetDatabase_PlatformTablesIdempotent(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates database twice (second call hits existing DB path).
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)
	status, _ := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Errorf("second call status = %d, want %d", status, http.StatusOK)
	}
}

// --- Reserved table protection tests ---

func TestExecute_RejectsReservedTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Ensures database exists.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	tests := []struct {
		name     string
		endpoint string
		sql      string
	}{
		{"insert into _change_log via data", "data", "INSERT INTO _change_log (kind, table_name) VALUES ('DML', 'x')"},
		{"delete from _change_log via data", "data", "DELETE FROM _change_log WHERE kind = 'DML' AND id = 1"},
		{"update _change_log via data", "data", "UPDATE _change_log SET table_name = 'x' WHERE id = 1"},
		{"drop _change_log via schema", "schema", "DROP TABLE _change_log"},
		{"alter _change_log via schema", "schema", "ALTER TABLE _change_log ADD COLUMN foo TEXT"},
		{"create _reserved via schema", "schema", "CREATE TABLE _reserved (id INTEGER PRIMARY KEY)"},
		{"drop _change_snapshots via schema", "schema", "DROP TABLE _change_snapshots"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, tt.endpoint),
				userToken(t, userID),
				map[string]string{"sql": tt.sql})
			if status != http.StatusForbidden {
				t.Errorf("status = %d, want %d", status, http.StatusForbidden)
			}
		})
	}
}

func TestQuery_AllowsSelectOnReservedTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Ensures database exists.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	// SELECT on a reserved table should be allowed (even if it doesn't exist yet, the point
	// is the permission check passes — the query may fail at the DB level).
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]string{"sql": "SELECT * FROM _change_log WHERE kind = 'DML'"})
	// Expect 422 (table doesn't exist) — not 403 (reserved table).
	if status == http.StatusForbidden {
		t.Error("SELECT on _change_log should not be blocked as reserved")
	}
}

func TestSchema_RejectsWithoutRowID(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Ensures database exists.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), userToken(t, userID), nil)

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT) WITHOUT ROWID"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// getDatabaseID retrieves the database ID for a bdb by querying Postgres.
func getDatabaseID(t *testing.T, bdbID string) string {
	t.Helper()
	var databaseID string
	err := testDB.QueryRow(context.Background(),
		"SELECT id FROM databases WHERE bdb_id = $1", bdbID).Scan(&databaseID)
	if err != nil {
		t.Fatalf("getting database ID for bdb %s: %v", bdbID, err)
	}
	return databaseID
}

// --- test helpers ---

// extractColumnNames extracts the "name" field from each column in the response.
func extractColumnNames(t *testing.T, columns []any) []string {
	t.Helper()
	names := make([]string, len(columns))
	for i, c := range columns {
		col, ok := c.(map[string]any)
		if !ok {
			t.Fatalf("column %d is not an object: %v", i, c)
		}
		name, ok := col["name"].(string)
		if !ok {
			t.Fatalf("column %d has no name: %v", i, col)
		}
		names[i] = name
	}
	return names
}

// extractRowValues extracts the "value" field from each cell in a row.
// Each cell is a {type, value} object in the Hrana wire format.
func extractRowValues(t *testing.T, row any) []any {
	t.Helper()
	cells, ok := row.([]any)
	if !ok {
		t.Fatalf("row is not an array: %v", row)
	}
	values := make([]any, len(cells))
	for i, cell := range cells {
		c, ok := cell.(map[string]any)
		if !ok {
			t.Fatalf("cell %d is not an object: %v", i, cell)
		}
		values[i] = c["value"]
	}
	return values
}

// --- Database stats tests ---

func TestGetDatabaseStats_ReturnsUsageAndLimit(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	// Triggers lazy provisioning.
	status, _ := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), token, nil)
	if status != http.StatusOK {
		t.Fatalf("get database: status = %d, want %d", status, http.StatusOK)
	}

	// Inserts a table to generate some storage usage.
	status, _ = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE test_stats (id INTEGER PRIMARY KEY, data TEXT)"})
	if status != http.StatusOK {
		t.Fatalf("create table: status = %d, want %d", status, http.StatusOK)
	}

	// Gets stats.
	status, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "stats"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("stats: status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	if _, ok := resp["storage_bytes_used"].(float64); !ok {
		t.Fatal("expected storage_bytes_used in response")
	}

	bytesLimit, ok := resp["storage_bytes_limit"].(float64)
	if !ok {
		t.Fatal("expected storage_bytes_limit in response")
	}
	if int64(bytesLimit) != 10*1024*1024 {
		t.Errorf("storage_bytes_limit = %v, want %d", bytesLimit, 10*1024*1024)
	}

	if _, ok := resp["storage_usage_percent"].(float64); !ok {
		t.Fatal("expected storage_usage_percent in response")
	}
}

func TestGetDatabaseStatsAdmin(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	// Triggers lazy provisioning as regular user.
	status, _ := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), token, nil)
	if status != http.StatusOK {
		t.Fatalf("get database: status = %d, want %d", status, http.StatusOK)
	}

	// Admin gets stats.
	adminTok := adminToken(t, uuid.New().String())
	adminPath := fmt.Sprintf("/api/databases/v1/admin/bdbs/%s/database/stats", bdbID)
	status, resp := testutil.DoRequest(t, router, http.MethodGet, adminPath, adminTok, nil)
	if status != http.StatusOK {
		t.Fatalf("admin stats: status = %d, want %d, body: %v", status, http.StatusOK, resp)
	}

	if _, ok := resp["storage_bytes_used"].(float64); !ok {
		t.Error("expected storage_bytes_used in admin response")
	}
	if _, ok := resp["storage_bytes_limit"].(float64); !ok {
		t.Error("expected storage_bytes_limit in admin response")
	}
}

func TestGetDatabaseStatsAdmin_NonAdminDenied(t *testing.T) {
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	adminPath := fmt.Sprintf("/api/databases/v1/admin/bdbs/%s/database/stats", bdbID)
	status, _ := testutil.DoRequest(t, router, http.MethodGet, adminPath, userToken(t, userID), nil)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestListChanges_KindFilter pins that the ?kind query parameter scopes results to DML, DDL, or all on REST.
func TestListChanges_KindFilter(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN qty INTEGER"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN tag TEXT"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('bread')"})

	cases := []struct {
		kindQuery string
		want      int
	}{
		{"?kind=DML", 2},
		{"?kind=DDL", 3},
		{"", 5},
	}
	for _, tc := range cases {
		_, resp := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes"+tc.kindQuery), token, nil)
		if got := len(resp["batches"].([]any)); got != tc.want {
			t.Errorf("kind=%q batches = %d, want %d", tc.kindQuery, got, tc.want)
		}
	}

	status, _ := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=BOGUS"), token, nil)
	if status != http.StatusBadRequest {
		t.Errorf("invalid kind status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestOldEndpoints_NotMounted asserts that the pre-rename URLs return 404 (catches stale router registrations).
func TestOldEndpoints_NotMounted(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	// Touch the database so the database exists.
	testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, ""), token, nil)

	cases := []struct {
		method  string
		suffix  string
		wantSts int
	}{
		{http.MethodPost, "execute", http.StatusNotFound},
		{http.MethodPost, "undo", http.StatusNotFound},
		{http.MethodPost, "redo", http.StatusNotFound},
		{http.MethodGet, "mutations", http.StatusNotFound},
		{http.MethodPost, "mutations/123/revert", http.StatusNotFound},
		{http.MethodPost, "mutations/batch/abc/revert", http.StatusNotFound},
		{http.MethodPost, "undo/anyone", http.StatusNotFound},
		{http.MethodPost, "redo/anyone", http.StatusNotFound},
	}
	for _, tc := range cases {
		status, _ := testutil.DoRequest(t, router, tc.method, dbPath(bdbID, tc.suffix), token,
			map[string]string{"sql": "SELECT 1"})
		if status != tc.wantSts {
			t.Errorf("%s %s status = %d, want %d", tc.method, tc.suffix, status, tc.wantSts)
		}
	}
}
