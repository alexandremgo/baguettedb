//go:build integration

package integrationtest

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	bdbsintra "github.com/alexandremgo/baguettedb/pkg/bdbs/adapters/intraprocess"
	bdbsmcp "github.com/alexandremgo/baguettedb/pkg/bdbs/mcp"
	bdbsvc "github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/config"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/filekek"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	dbmcp "github.com/alexandremgo/baguettedb/pkg/databases/mcp"
	dbsvc "github.com/alexandremgo/baguettedb/pkg/databases/service"
	"github.com/alexandremgo/baguettedb/pkg/mcpgateway"
	"github.com/alexandremgo/baguettedb/pkg/starters"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessbdbs"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessdatabases"
)

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
	t *testing.T, dbRepo dbsvc.DatabaseRepository, kekProvider *filekek.Provider,
) *sqlitelocal.Client {
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
	return client
}

// gatewayTestBacking groups the shared encryption backing (KEK provider and
// sqlitelocal client + data dir) used by all setupGatewayMCPClient calls
// within a single test. Sharing matters: when one test wires up multiple
// clients (e.g. owner + viewer on the same bdb's database), they must
// resolve DEKs through the same KEK and read/write the same on-disk files.
type gatewayTestBacking struct {
	kekProvider  *filekek.Provider
	sqliteClient *sqlitelocal.Client
}

var gatewayBackings sync.Map // map[*testing.T]*gatewayTestBacking

// getOrCreateGatewayBacking returns the per-test backing, creating it on
// first use and registering cleanup to evict it when the test finishes.
func getOrCreateGatewayBacking(t *testing.T, dbRepo dbsvc.DatabaseRepository) *gatewayTestBacking {
	t.Helper()
	if v, ok := gatewayBackings.Load(t); ok {
		return v.(*gatewayTestBacking)
	}
	b := &gatewayTestBacking{
		kekProvider: newTestKEKProvider(t),
	}
	b.sqliteClient = newTestSQLiteClient(t, dbRepo, b.kekProvider)
	gatewayBackings.Store(t, b)
	t.Cleanup(func() { gatewayBackings.Delete(t) })
	return b
}

// allowAllBdbsApp is a test mock that always allows access.
type allowAllBdbsApp struct{}

func (a *allowAllBdbsApp) CheckMemberPermission(
	_ context.Context, _, _ uuid.UUID, _ authz.DatabasePermission,
) (bool, error) {
	return true, nil
}

func (a *allowAllBdbsApp) GetBdbOwner(_ context.Context, _ uuid.UUID) (uuid.UUID, []authz.Role, error) {
	return uuid.Nil, []authz.Role{authz.RoleUser}, nil
}

func (a *allowAllBdbsApp) GetOwnedBdbIDs(_ context.Context, _ uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

// gatewayTestAuthApp implements bdbsvc.AuthApp for tests.
type gatewayTestAuthApp struct{}

func (a *gatewayTestAuthApp) GetUserRoles(_ context.Context, _ uuid.UUID) ([]authz.Role, error) {
	return []authz.Role{authz.RoleUser}, nil
}

func (a *gatewayTestAuthApp) GetUserProfiles(
	_ context.Context, userIDs []uuid.UUID,
) (map[uuid.UUID]*apptypes.UserProfile, error) {
	out := make(map[uuid.UUID]*apptypes.UserProfile, len(userIDs))
	for _, id := range userIDs {
		out[id] = &apptypes.UserProfile{Email: "test@test.com"}
	}
	return out, nil
}

// setupGatewayMCPClient creates both module MCP servers, aggregates them via the gateway, and returns an initialized
// SSE MCP client authenticated as the given user. Returns the bdbs + databases services so tests can drive lifecycle
// ops directly (e.g. provisioning a per-bdb DB, soft-deleting, advancing deleted_at, calling PurgeExpired).
func setupGatewayMCPClient(
	t *testing.T, userID string,
) (*mcpclient.Client, *bdbsvc.BdbService, *dbsvc.DatabaseService) {
	t.Helper()

	// Bdbs MCP server.
	bdbSvc := newBdbService(&gatewayTestAuthApp{}, quota.Fixed(quota.Limits{MaxBdbs: 100, MaxMembersPerBdb: 100}))
	dbRef := &bdbsmcp.DatabasesAppRef{}
	bdbsMCPServer := bdbsmcp.NewServer(bdbSvc, dbRef, bdbsmcp.Options{}, testLogger)

	// Databases MCP server. Shares KEK provider and sqlitelocal client across
	// all setupGatewayMCPClient calls within this test so multi-client tests
	// (e.g. owner + viewer on the same bdb's database) resolve DEKs through
	// the same KEK and read/write the same on-disk files.
	dbRepo := newDatabaseRepo()
	classifier := sqlitelocal.NewClassifier()
	backing := getOrCreateGatewayBacking(t, dbRepo)
	kekProvider := backing.kekProvider
	sqliteClient := backing.sqliteClient
	limits := quota.Fixed(quota.Limits{
		MaxDBSizeBytes:          100 * 1024 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
	})
	dbService := dbsvc.NewDatabaseService(dbRepo, sqliteClient, sqliteClient, &allowAllBdbsApp{}, limits, kekProvider,
		testLogger)
	querySvc := dbsvc.NewQueryService(dbService, sqliteClient, classifier, limits, 100, 0)
	skillSvc := dbsvc.NewSkillService(dbService, sqliteClient)
	formulaSvc := dbsvc.NewFormulaService(dbService, sqliteClient, classifier, querySvc, 0, testLogger)
	dbMCPServer := dbmcp.NewServer(querySvc, skillSvc, formulaSvc, dbsvc.NewDocsService(querySvc), testLogger)

	// Wires the databases app into the bdbs MCP server for list_bdbs enrichment.
	dbRef.App = bdbsintra.NewDatabasesApp(querySvc, skillSvc, formulaSvc, dbService)
	bdbSvc.SetDatabasesApp(dbRef.App)

	// Starters tools on the bdbs MCP server, from the embedded builtin catalog — production's setup, registered
	// before the gateway below discovers the tool lists.
	if _, err := starters.Setup(config.Config{}, testLogger,
		intraprocessbdbs.New(bdbSvc),
		intraprocessdatabases.New(querySvc, formulaSvc, skillSvc, dbService),
		starters.Host{MCPServer: bdbsMCPServer},
		starters.SetupOptions{},
	); err != nil {
		t.Fatalf("setting up starters: %v", err)
	}

	// Gateway.
	gwResult, err := mcpgateway.SetupInProcess(
		[]*mcpserver.MCPServer{bdbsMCPServer, dbMCPServer},
		testLogger,
		mcpgateway.Options{},
	)
	if err != nil {
		t.Fatalf("setting up MCP gateway: %v", err)
	}
	t.Cleanup(gwResult.Cleanup)

	// Token validator.
	tv, err := authhttp.NewTokenValidator(context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	// HTTP server with authenticated MCP routes.
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(tv.RequireToken())
		r.Handle("/mcp", gwResult.StreamableServer)
	})
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)

	// MCP Streamable HTTP client.
	token := userToken(t, userID)
	c, err := mcpclient.NewStreamableHttpClient(ts.URL+"/mcp",
		transport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + token,
		}),
	)
	if err != nil {
		t.Fatalf("creating MCP client: %v", err)
	}

	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatalf("starting MCP client: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("closing MCP client: %v", err)
		}
	})

	_, err = c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "test-client",
				Version: "1.0.0",
			},
		},
	})
	if err != nil {
		t.Fatalf("initializing MCP client: %v", err)
	}

	return c, bdbSvc, dbService
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

// callTool is a helper that calls an MCP tool and returns the result.
func callTool(t *testing.T, c *mcpclient.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name:      name,
			Arguments: args,
		},
	})
	if err != nil {
		t.Fatalf("calling tool %s: %v", name, err)
	}
	return result
}

// parseToolJSON extracts the text content from a tool result and unmarshals it as JSON.
func parseToolJSON(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("expected content in tool result")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(text.Text), &data); err != nil {
		t.Fatalf("unmarshaling tool result: %v", err)
	}
	return data
}

// parseToolJSONArray extracts the text content as a JSON array.
func parseToolJSONArray(t *testing.T, result *mcp.CallToolResult) []any {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("expected content in tool result")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	var data []any
	if err := json.Unmarshal([]byte(text.Text), &data); err != nil {
		t.Fatalf("unmarshaling tool result array: %v", err)
	}
	return data
}

// --- Tool discovery tests ---

func TestMCP_ListTools(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	result, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}

	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = true
	}

	expected := []string{
		"list_bdbs", "describe_bdb", "update_bdb", "list_starters", "create_bdb",
		"describe_table", "execute_query", "execute_data",
		"list_changes", "undo_last_change", "redo_last_change", "undo_last_change_anyone",
		"redo_last_change_anyone", "execute_schema", "revert_change", "get_skill", "create_skill",
		"update_skill",
		"create_formula", "update_formula", "get_formula", "list_formulas", "delete_formula", "run_formula",
		"get_docs",
	}
	for _, name := range expected {
		if !toolNames[name] {
			t.Errorf("expected tool %q not found in %v", name, toolNames)
		}
	}

	if len(result.Tools) != len(expected) {
		t.Errorf("expected %d tools, got %d", len(expected), len(result.Tools))
	}
}

// --- list_bdbs tests ---

// TestMCP_ListBdbs_Empty verifies the zero-bdb response is a friendly guide-to-the-app hint rather than a bare
// empty array (a new account starts with no bdbs).
func TestMCP_ListBdbs_Empty(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	result := callTool(t, c, "list_bdbs", nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "no bdbs yet") {
		t.Errorf("empty list_bdbs = %q, want the no-bdbs-yet hint", text.Text)
	}
	if !strings.Contains(text.Text, "create_bdb") {
		t.Errorf("empty list_bdbs = %q, want it to offer create_bdb", text.Text)
	}
}

func TestMCP_ListBdbs_ReturnsUserBdbs(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc, _ := setupGatewayMCPClient(t, userID.String())

	// Creates a bdb for this user.
	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Test Bdb", "A test bdb")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "list_bdbs", nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	bdbs := parseToolJSONArray(t, result)
	if len(bdbs) != 1 {
		t.Fatalf("expected 1 bdb, got %d", len(bdbs))
	}

	c0 := bdbs[0].(map[string]any)
	if c0["id"] != bdb.ID.String() {
		t.Errorf("bdb id = %v, want %v", c0["id"], bdb.ID.String())
	}
	if c0["name"] != "Test Bdb" {
		t.Errorf("bdb name = %v, want Test Bdb", c0["name"])
	}
	if c0["role"] != "owner" {
		t.Errorf("bdb role = %v, want owner", c0["role"])
	}
}

// TestMCP_ListBdbs_FTS5ShadowsExcluded pins the cross-module chain (gateway → bdbs module → databases
// service): fts5 shadow tables never appear as ordinary tables in list_bdbs or describe_bdb, the fts5
// virtual table itself stays listed with type "virtual", and an fts-lookalike user table survives.
func TestMCP_ListBdbs_FTS5ShadowsExcluded(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc, _ := setupGatewayMCPClient(t, userID.String())

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Notes", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	bdbID := bdb.ID.String()

	for _, ddl := range []string{
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT)",
		"CREATE VIRTUAL TABLE notes_fts USING fts5(title, body)",
		"CREATE TABLE logs_fts_data (id INTEGER PRIMARY KEY)",
	} {
		r := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": ddl})
		if r.IsError {
			t.Fatalf("execute_schema %q: %v", ddl, r.Content)
		}
	}

	// Per table name: expected type and module ("" = module key absent thanks to omitempty).
	wantTables := map[string][2]string{
		"notes": {"table", ""}, "notes_fts": {"virtual", "fts5"}, "logs_fts_data": {"table", ""},
	}

	assertTables := func(toolName string, tables []any) {
		t.Helper()
		got := make(map[string][2]string, len(tables))
		for _, tbl := range tables {
			m := tbl.(map[string]any)
			module, _ := m["module"].(string)
			got[m["name"].(string)] = [2]string{m["type"].(string), module}
		}
		for name, wantEntry := range wantTables {
			if gotEntry, ok := got[name]; !ok {
				t.Errorf("%s: table %s missing, got %v", toolName, name, got)
			} else if gotEntry != wantEntry {
				t.Errorf("%s: table %s (type, module) = %v, want %v", toolName, name, gotEntry, wantEntry)
			}
		}
		if len(got) != len(wantTables) {
			t.Errorf("%s: expected exactly %d tables (fts5 shadows excluded), got %v", toolName, len(wantTables), got)
		}
	}

	listResult := callTool(t, c, "list_bdbs", nil)
	if listResult.IsError {
		t.Fatalf("list_bdbs: %v", listResult.Content)
	}
	bdbs := parseToolJSONArray(t, listResult)
	if len(bdbs) != 1 {
		t.Fatalf("expected 1 bdb, got %d", len(bdbs))
	}
	assertTables("list_bdbs", bdbs[0].(map[string]any)["tables"].([]any))

	describeResult := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdbID})
	if describeResult.IsError {
		t.Fatalf("describe_bdb: %v", describeResult.Content)
	}
	assertTables("describe_bdb", parseToolJSON(t, describeResult)["tables"].([]any))
}

// TestMCP_OwnerAttribution pins the owner display name through the gateway: present in list_bdbs and
// describe_bdb for a caller who is not the owner, absent for the owner's own bdbs. The gateway test auth
// app resolves profiles with email only, so the name falls back to the email.
func TestMCP_OwnerAttribution(t *testing.T) {
	truncateAll(t)
	ownerID := uuid.New()
	memberID := uuid.New()
	ctx := context.Background()

	ownerClient, bdbSvc, _ := setupGatewayMCPClient(t, ownerID.String())
	memberClient, _, _ := setupGatewayMCPClient(t, memberID.String())

	bdb, err := bdbSvc.CreateBdb(ctx, ownerID, []authz.Role{authz.RoleUser}, "Shared Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	if _, err := bdbSvc.AddMember(ctx, ownerID, bdb.ID, memberID, "editor", []authz.Role{authz.RoleUser}); err != nil {
		t.Fatalf("adding member: %v", err)
	}

	memberList := parseToolJSONArray(t, callTool(t, memberClient, "list_bdbs", nil))
	if len(memberList) != 1 {
		t.Fatalf("expected 1 bdb for member, got %d", len(memberList))
	}
	if got := memberList[0].(map[string]any)["owner"]; got != "test@test.com" {
		t.Errorf("member list_bdbs owner = %v, want test@test.com", got)
	}

	memberOverview := parseToolJSON(t,
		callTool(t, memberClient, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()}))
	if got := memberOverview["owner"]; got != "test@test.com" {
		t.Errorf("member describe_bdb owner = %v, want test@test.com", got)
	}

	ownerList := parseToolJSONArray(t, callTool(t, ownerClient, "list_bdbs", nil))
	if len(ownerList) != 1 {
		t.Fatalf("expected 1 bdb for owner, got %d", len(ownerList))
	}
	if got, present := ownerList[0].(map[string]any)["owner"]; present {
		t.Errorf("owner list_bdbs: owner must be absent for own bdb, got %v", got)
	}
}

func TestMCP_ListBdbs_SoftDeletedExposesDeletedAt(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc, _ := setupGatewayMCPClient(t, userID.String())

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID,
		[]authz.Role{authz.RoleUser}, "Going Away", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	if err := bdbSvc.DeleteBdb(context.Background(), userID, bdb.ID); err != nil {
		t.Fatalf("soft-deleting bdb: %v", err)
	}

	result := callTool(t, c, "list_bdbs", nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	bdbs := parseToolJSONArray(t, result)
	if len(bdbs) != 1 {
		t.Fatalf("expected 1 bdb (owner still sees soft-deleted), got %d", len(bdbs))
	}
	c0 := bdbs[0].(map[string]any)
	if c0["deleted_at"] == nil {
		t.Errorf("deleted_at = nil, want a timestamp")
	}
	if c0["purge_at"] == nil {
		t.Errorf("purge_at = nil, want a timestamp")
	}
}

// TestMCP_DescribeBdb_InlinesTargetedSkillBodies pins the cross-module chain (gateway → bdbs module → databases
// service): a table-targeted skill created through the gateway comes back from describe_bdb with its full body
// inlined, not as a summary — within the skill-body budget, target_tables is a relevance hint, not a delivery gate.
func TestMCP_DescribeBdb_InlinesTargetedSkillBodies(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc, _ := setupGatewayMCPClient(t, userID.String())

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Notes", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	bdbID := bdb.ID.String()

	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)",
	}); r.IsError {
		t.Fatalf("execute_schema: %v", r.Content)
	}
	if r := callTool(t, c, "create_skill", map[string]any{
		"bdb_id":        bdbID,
		"name":          "notes-capture",
		"description":   "How to capture notes.",
		"body":          "Always write markdown into notes.body.",
		"target_tables": []string{"notes"},
	}); r.IsError {
		t.Fatalf("create_skill: %v", r.Content)
	}

	result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdbID})
	if result.IsError {
		t.Fatalf("describe_bdb: %v", result.Content)
	}

	overview := parseToolJSON(t, result)
	skills, _ := overview["skills"].([]any)
	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}
	s := skills[0].(map[string]any)
	if body, _ := s["body"].(string); body != "Always write markdown into notes.body." {
		t.Errorf("table-targeted skill body = %q, want it inlined in describe_bdb", body)
	}
	if _, present := overview["omitted"]; present {
		t.Errorf("omitted must be absent when nothing was dropped, got %v", overview["omitted"])
	}

	// Within the schema table limit, describe_bdb also folds column schemas into its table entries.
	tables, _ := overview["tables"].([]any)
	if len(tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(tables))
	}
	tbl := tables[0].(map[string]any)
	colNames := map[string]bool{}
	cols, _ := tbl["columns"].([]any)
	for _, c := range cols {
		name, _ := c.(map[string]any)["name"].(string)
		colNames[name] = true
	}
	if !colNames["id"] || !colNames["body"] {
		t.Errorf("notes: expected columns id and body inlined, got %v", tbl["columns"])
	}

	// list_bdbs keeps the same skill as a routing summary: name + description, nothing else.
	listResult := callTool(t, c, "list_bdbs", nil)
	if listResult.IsError {
		t.Fatalf("list_bdbs: %v", listResult.Content)
	}
	listBdbs := parseToolJSONArray(t, listResult)
	if len(listBdbs) != 1 {
		t.Fatalf("expected 1 bdb in list_bdbs, got %d", len(listBdbs))
	}
	listSkills, _ := listBdbs[0].(map[string]any)["skills"].([]any)
	if len(listSkills) != 1 {
		t.Fatalf("expected 1 skill in list_bdbs, got %d", len(listSkills))
	}
	listSkill := listSkills[0].(map[string]any)
	if listSkill["name"] != "notes-capture" || listSkill["description"] != "How to capture notes." {
		t.Errorf("list_bdbs skill summary = %v, want name+description", listSkill)
	}
	for _, field := range []string{"body", "id", "target_tables"} {
		if _, present := listSkill[field]; present {
			t.Errorf("list_bdbs skill: %s should not be present, got %v", field, listSkill[field])
		}
	}
}

func TestMCP_DescribeBdb_SoftDeletedExposesDeletedAt(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc, _ := setupGatewayMCPClient(t, userID.String())

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID,
		[]authz.Role{authz.RoleUser}, "Soon Purged", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	if err := bdbSvc.DeleteBdb(context.Background(), userID, bdb.ID); err != nil {
		t.Fatalf("soft-deleting bdb: %v", err)
	}

	result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	overview := parseToolJSON(t, result)
	if overview["deleted_at"] == nil {
		t.Errorf("deleted_at = nil, want a timestamp")
	}
	if overview["purge_at"] == nil {
		t.Errorf("purge_at = nil, want a timestamp")
	}
}

// TestMCP_DescribeBdb_FlagsHiddenFormulas pins the cross-module chain (gateway → bdbs module →
// databases service): a hidden formula still appears in describe_bdb's orientation listing,
// carrying its hidden flag.
func TestMCP_DescribeBdb_FlagsHiddenFormulas(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc, _ := setupGatewayMCPClient(t, userID.String())

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID,
		[]authz.Role{authz.RoleUser}, "Hidden Formulas", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	bdbID := bdb.ID.String()

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE notes (id INTEGER PRIMARY KEY, content TEXT)",
	})
	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "plumbing-rollup",
		"description": "Assistant-only rollup.",
		"sql":         "SELECT COUNT(*) AS total FROM notes",
		"hidden":      true,
	})
	if result.IsError {
		t.Fatalf("create_formula error: %v", result.Content)
	}

	result = callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdbID})
	if result.IsError {
		t.Fatalf("describe_bdb error: %v", result.Content)
	}
	overview := parseToolJSON(t, result)
	formulas, _ := overview["formulas"].([]any)
	found := false
	for _, raw := range formulas {
		entry, _ := raw.(map[string]any)
		if entry["name"] == "plumbing-rollup" {
			found = true
			if entry["hidden"] != true {
				t.Errorf("hidden = %v, want true", entry["hidden"])
			}
		}
	}
	if !found {
		t.Fatalf("hidden formula missing from describe_bdb formulas: %v", formulas)
	}
}

// TestMCP_Formula_HiddenDisplayColumn pins the display-config round-trip through the gateway: a
// hidden column survives create_formula → get_formula, and the exclusivity rule (a hidden column
// carries no role or format) is enforced at the MCP surface.
// TestMCP_Formula_ConditionalRequiredness pins the two faces of a when-gated required param through
// the gateway: while its condition is false the run treats it as optional (omitted binds NULL), and
// while it is true an omission is rejected.
func TestMCP_Formula_ConditionalRequiredness(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)",
	})
	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "add-item",
		"description": "Adds an item; qty only counts while the counted flag is on.",
		"sql": "INSERT INTO items (name, qty) " +
			"VALUES (:name, COALESCE(:qty, CASE WHEN :counted THEN -2 ELSE -1 END))",
		"params": []any{
			map[string]any{"name": "name", "type": "text", "source": "prompt"},
			map[string]any{"name": "counted", "type": "boolean", "source": "prompt", "optional": true},
			map[string]any{"name": "qty", "type": "integer", "source": "prompt",
				"when": map[string]any{"param": "counted", "value": "true"}},
		},
	})
	if result.IsError {
		t.Fatalf("create_formula error: %v", result.Content)
	}

	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "add-item", "params": map[string]any{"name": "tea"},
	}); result.IsError {
		t.Errorf("run with the condition off failed: %v", result.Content)
	}
	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "add-item", "params": map[string]any{"name": "salt", "counted": true},
	}); !result.IsError {
		t.Error("expected an error for the omitted param while its condition holds")
	}
}

// TestMCP_Formula_LabeledOptions pins the options contract through the gateway: input entries mix
// bare scalars and {value, label} objects, reads return the normalized object list, and a run
// passes values only — one outside the set is rejected as a value error naming the parameter.
func TestMCP_Formula_LabeledOptions(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "monthly-items",
		"description": "Items for a month.",
		"sql":         "SELECT id, name FROM items WHERE :month IS NULL OR :month > 0",
		"params": []any{
			map[string]any{
				"name": "month", "type": "integer", "source": "prompt", "optional": true,
				"options": []any{map[string]any{"value": 9, "label": "September"}, 10},
			},
		},
	})
	if result.IsError {
		t.Fatalf("create_formula error: %v", result.Content)
	}

	result = callTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "monthly-items"})
	if result.IsError {
		t.Fatalf("get_formula error: %v", result.Content)
	}
	detail := parseToolJSON(t, result)
	params, _ := detail["params"].([]any)
	if len(params) != 1 {
		t.Fatalf("params = %v, want one entry", params)
	}
	month, _ := params[0].(map[string]any)
	options, _ := month["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("options = %v, want 2 normalized entries", month["options"])
	}
	for i, want := range []map[string]any{
		{"value": "9", "label": "September"},
		{"value": "10", "label": "10"},
	} {
		if opt, _ := options[i].(map[string]any); opt["value"] != want["value"] || opt["label"] != want["label"] {
			t.Errorf("options[%d] = %v, want %v", i, options[i], want)
		}
	}

	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "monthly-items", "params": map[string]any{"month": 9},
	}); result.IsError {
		t.Errorf("run with a declared value failed: %v", result.Content)
	}
	result = callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "monthly-items", "params": map[string]any{"month": 11},
	})
	want := `failed to run formula: parameter "month" must be one of "9", "10", got 11`
	if text := result.Content[0].(mcp.TextContent).Text; !result.IsError || text != want {
		t.Errorf("run with a value outside the option set = %q (error %v), want %q", text, result.IsError, want)
	}
}

func TestMCP_Formula_HiddenDisplayColumn(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, meta TEXT)",
	})

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "items-list",
		"description": "Items with a machine column hidden from the grid.",
		"sql":         "SELECT name, meta FROM items",
		"display": map[string]any{
			"mode": "table",
			"columns": []any{
				map[string]any{"column": "meta", "hidden": true},
			},
		},
	})
	if result.IsError {
		t.Fatalf("create_formula error: %v", result.Content)
	}

	result = callTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "items-list"})
	if result.IsError {
		t.Fatalf("get_formula error: %v", result.Content)
	}
	detail := parseToolJSON(t, result)
	display, _ := detail["display"].(map[string]any)
	if display == nil {
		t.Fatalf("display missing from get_formula detail: %v", detail)
	}
	columns, _ := display["columns"].([]any)
	if len(columns) != 1 {
		t.Fatalf("display columns = %v, want one entry", columns)
	}
	entry, _ := columns[0].(map[string]any)
	if entry["column"] != "meta" || entry["hidden"] != true {
		t.Errorf("display column = %v, want meta with hidden = true", entry)
	}

	result = callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "items-cards",
		"description": "Invalid: a rendered role combined with hidden.",
		"sql":         "SELECT name, meta FROM items",
		"display": map[string]any{
			"mode": "cards",
			"columns": []any{
				map[string]any{"column": "name", "role": "title", "hidden": true},
			},
		},
	})
	if !result.IsError {
		t.Fatal("create_formula accepted a hidden display column that also carries a role")
	}
	if text, ok := result.Content[0].(mcp.TextContent); !ok || !strings.Contains(text.Text, "hidden") {
		t.Errorf("error = %v, want a message naming the hidden/role conflict", result.Content)
	}
}

// --- Database tool tests (via gateway) ---

func TestMCP_ExecuteSchema_CreateTable(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE notes (id INTEGER PRIMARY KEY, content TEXT)",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	// Verifies the table is queryable.
	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT name FROM sqlite_master WHERE type='table' AND name='notes'",
	})
	if queryResult.IsError {
		t.Fatalf("unexpected error verifying table: %v", queryResult.Content)
	}
	data := parseToolJSON(t, queryResult)
	rows := data["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
}

// TestMCP_ExecuteSchema_InvalidIdentifierRejected mirrors the databases-module admission behavior through
// the gateway: a non-conforming created name is rejected with an error naming the identifier rule, and no
// schema object is left behind.
func TestMCP_ExecuteSchema_InvalidIdentifierRejected(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    `CREATE TABLE "my café" (id INTEGER PRIMARY KEY)`,
	})
	if !result.IsError {
		t.Fatal("expected error for non-conforming table name, got success")
	}
	if len(result.Content) == 0 {
		t.Fatal("expected error content")
	}
	text, _ := result.Content[0].(mcp.TextContent)
	if !strings.Contains(text.Text, "identifier") {
		t.Errorf("error text %q does not name the identifier rule", text.Text)
	}

	queryResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM sqlite_master WHERE name = 'my café'",
	})
	if queryResult.IsError {
		t.Fatalf("unexpected error verifying rejection: %v", queryResult.Content)
	}
	count := parseToolJSON(t, queryResult)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("table exists after rejected CREATE: count = %v, want 0", count)
	}
}

func TestMCP_DescribeTable(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE events (id INTEGER PRIMARY KEY, name TEXT NOT NULL, date TEXT)",
	})

	result := callTool(t, c, "describe_table", map[string]any{
		"bdb_id":     bdbID,
		"table_name": "events",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data := parseToolJSON(t, result)
	columns := data["columns"].([]any)
	if len(columns) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(columns))
	}
}

func TestMCP_ExecuteQuery_Select(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE cities (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO cities VALUES (1, 'Paris')",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO cities VALUES (2, 'Lyon')",
	})

	result := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT * FROM cities ORDER BY id",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data := parseToolJSON(t, result)

	columns := data["columns"].([]any)
	if len(columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(columns))
	}

	rows := data["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	row0 := rows[0].([]any)
	row1 := rows[1].([]any)
	val0Name := row0[1].(map[string]any)["value"]
	val1Name := row1[1].(map[string]any)["value"]
	if val0Name != "Paris" {
		t.Errorf("row0 name = %v, want Paris", val0Name)
	}
	if val1Name != "Lyon" {
		t.Errorf("row1 name = %v, want Lyon", val1Name)
	}
}

func TestMCP_ExecuteQuery_MissingSQL(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	result := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "",
	})
	if !result.IsError {
		t.Error("expected error for empty sql")
	}
}

func TestMCP_DescribeTable_InvalidName(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	result := callTool(t, c, "describe_table", map[string]any{
		"bdb_id":     bdbID,
		"table_name": "drop table",
	})
	if !result.IsError {
		t.Error("expected error for invalid table name")
	}
}

// --- Database isolation test ---

func TestMCP_DatabaseIsolation(t *testing.T) {
	truncateAll(t)
	user1 := uuid.New().String()
	bdb1 := uuid.New().String()
	user2 := uuid.New().String()
	bdb2 := uuid.New().String()
	c1, _, _ := setupGatewayMCPClient(t, user1)
	c2, _, _ := setupGatewayMCPClient(t, user2)

	// User 1 creates a table and inserts data in bdb1.
	callTool(t, c1, "execute_schema", map[string]any{
		"bdb_id": bdb1,
		"sql":    "CREATE TABLE private_data (id INTEGER PRIMARY KEY, secret TEXT)",
	})
	callTool(t, c1, "execute_data", map[string]any{
		"bdb_id": bdb1,
		"sql":    "INSERT INTO private_data VALUES (1, 'user1-only')",
	})

	// User 1 can see their data.
	u1Result := callTool(t, c1, "execute_query", map[string]any{
		"bdb_id": bdb1,
		"sql":    "SELECT * FROM private_data",
	})
	u1Data := parseToolJSON(t, u1Result)
	u1Rows := u1Data["rows"].([]any)
	if len(u1Rows) != 1 {
		t.Fatalf("user1 should have 1 row, got %d", len(u1Rows))
	}

	// User 2 in bdb2 should have no tables — querying sqlite_master via
	// execute_query (list_tables tool was removed in step 8f). Filter mirrors
	// QueryService.ListTables: excludes platform tables (`_*`) and SQLite
	// internals (`sqlite_*` like sqlite_sequence).
	u2Result := callTool(t, c2, "execute_query", map[string]any{
		"bdb_id": bdb2,
		"sql": "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' " +
			"AND name NOT LIKE '\\_%' ESCAPE '\\'",
	})
	u2Data := parseToolJSON(t, u2Result)
	u2Rows := u2Data["rows"].([]any)
	if len(u2Rows) != 0 {
		t.Errorf("user2 should have 0 tables, got %d", len(u2Rows))
	}
}

// --- Soft-delete purge sweep ---

func TestPurgeExpired_PurgesRowAndDatabase(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	_, bdbSvc, dbService := setupGatewayMCPClient(t, userID.String())
	ctx := context.Background()

	bdb, err := bdbSvc.CreateBdb(ctx, userID,
		[]authz.Role{authz.RoleUser}, "Purge Target", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	// Provisions the per-bdb SQLite database so the purge actually has
	// something to delete on the databases side.
	if _, err := dbService.CreateDatabase(ctx, bdb.ID); err != nil {
		t.Fatalf("provisioning bdb database: %v", err)
	}

	if err := bdbSvc.DeleteBdb(ctx, userID, bdb.ID); err != nil {
		t.Fatalf("soft-deleting bdb: %v", err)
	}

	// Advances deleted_at past the grace period via direct SQL so the next sweep sees it as purgeable
	// without waiting 7 days.
	past := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if err := testDB.Exec(ctx,
		"UPDATE bdbs SET deleted_at = $1 WHERE id = $2", past, bdb.ID); err != nil {
		t.Fatalf("advancing deleted_at: %v", err)
	}

	stats, err := bdbSvc.PurgeExpired(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("PurgeExpired: %v", err)
	}
	if stats.Scanned != 1 || stats.Purged != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want {Scanned:1 Purged:1 Failed:0}", stats)
	}

	// Bdb row should be hard-deleted now.
	var bdbRowCount int
	if err := testDB.QueryRow(ctx,
		"SELECT COUNT(*) FROM bdbs WHERE id = $1", bdb.ID).Scan(&bdbRowCount); err != nil {
		t.Fatalf("counting bdb rows: %v", err)
	}
	if bdbRowCount != 0 {
		t.Errorf("bdb row count = %d, want 0 after purge", bdbRowCount)
	}

	// Database record should also be gone (FindByBdb returns ErrDatabaseNotFound).
	var dbRowCount int
	if err := testDB.QueryRow(ctx,
		"SELECT COUNT(*) FROM databases WHERE bdb_id = $1", bdb.ID).Scan(&dbRowCount); err != nil {
		t.Fatalf("counting database rows: %v", err)
	}
	if dbRowCount != 0 {
		t.Errorf("database row count = %d, want 0 after purge", dbRowCount)
	}
}

// TestMCP_Formula_MultiSelectLookup pins the multi-select contract through the gateway: a picked
// array binds one row per value, an omitting run resolves the "all" default to every lookup value,
// and an explicit empty selection on a required param errors.
func TestMCP_Formula_MultiSelectLookup(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT); " +
			"CREATE TABLE tally (member_id INTEGER, tag TEXT)",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO members (id, name) VALUES (1,'Alice'),(2,'Bob'),(3,'Cara')",
	})

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "tag-members",
		"description": "Tags the picked members.",
		"sql":         "INSERT INTO tally (member_id, tag) SELECT je.value, :tag FROM json_each(:picked) je",
		"params": []any{
			map[string]any{"name": "tag", "type": "text", "source": "prompt"},
			map[string]any{"name": "picked", "type": "integer", "source": "lookup", "multiple": true,
				"default": "all",
				"lookup":  map[string]any{"table": "members", "label_column": "name", "value_column": "id"}},
		},
	})
	if result.IsError {
		t.Fatalf("create_formula error: %v", result.Content)
	}

	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "tag-members",
		"params": map[string]any{"tag": "subset", "picked": []any{3, 1}},
	}); result.IsError {
		t.Errorf("run with a picked subset failed: %v", result.Content)
	}
	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "tag-members", "params": map[string]any{"tag": "everyone"},
	}); result.IsError {
		t.Errorf("omitting run failed to resolve the all default: %v", result.Content)
	}
	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "tag-members", "params": map[string]any{"tag": "none", "picked": []any{}},
	}); !result.IsError {
		t.Error("expected an error for an explicit empty selection on a required param")
	}

	q := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql": "SELECT tag, group_concat(member_id) FROM " +
			"(SELECT tag, member_id FROM tally ORDER BY rowid) GROUP BY tag ORDER BY tag",
	})
	text := q.Content[0].(mcp.TextContent).Text
	for _, want := range []string{"subset", "3,1", "everyone", "1,2,3"} {
		if !strings.Contains(text, want) {
			t.Errorf("tally query = %q, want it to contain %q", text, want)
		}
	}
}

// --- creation tools through the gateway (twins of the bdbs and starters module suites) ---

// TestMCP_UpdateBdb_ThroughGateway checks that a rename with only a name keeps the description, proxied
// through the gateway to the bdbs backend.
func TestMCP_UpdateBdb_ThroughGateway(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc, _ := setupGatewayMCPClient(t, userID.String())

	bdb, err := bdbSvc.CreateBdb(
		context.Background(), userID, []authz.Role{authz.RoleUser}, "Old Name", "keep me")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "update_bdb", map[string]any{"bdb_id": bdb.ID.String(), "name": "New Name"})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	updated := parseToolJSON(t, result)
	if updated["name"] != "New Name" {
		t.Errorf("name = %v, want New Name", updated["name"])
	}
	if updated["description"] != "keep me" {
		t.Errorf("description = %v, want keep me", updated["description"])
	}
}

// TestMCP_ListStarters_ThroughGateway checks that the embedded builtin catalog reaches a gateway client, with
// each entry carrying what choosing a starter takes.
func TestMCP_ListStarters_ThroughGateway(t *testing.T) {
	truncateAll(t)
	c, _, _ := setupGatewayMCPClient(t, uuid.New().String())

	result := callTool(t, c, "list_starters", nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	entries := parseToolJSONArray(t, result)
	if len(entries) == 0 {
		t.Fatal("expected the builtin catalog, got no entries")
	}
	keys := make(map[string]map[string]any, len(entries))
	for _, e := range entries {
		entry := e.(map[string]any)
		keys[entry["key"].(string)] = entry
	}
	places, ok := keys["places"]
	if !ok {
		t.Fatalf("catalog keys = %v, want places among them", keys)
	}
	if _, ok := places["formulas"].([]any); !ok {
		t.Errorf("places entry = %v, want a formulas array", places)
	}
	if _, ok := places["icon"]; ok {
		t.Error("icon should not reach a catalog entry")
	}
}

// TestMCP_CreateBdb_FromStarterThroughGateway creates a bdb from a builtin starter and orients in it, all
// through the gateway: the seeded tables and formulas must be visible to the same client via describe_bdb.
func TestMCP_CreateBdb_FromStarterThroughGateway(t *testing.T) {
	truncateAll(t)
	c, _, _ := setupGatewayMCPClient(t, uuid.New().String())

	result := callTool(t, c, "create_bdb", map[string]any{"starter_key": "places"})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}
	created := parseToolJSON(t, result)
	if created["name"] != "Places" {
		t.Errorf("name = %v, want the manifest's Places", created["name"])
	}
	if created["starter_key"] != "places" {
		t.Errorf("starter_key = %v, want places", created["starter_key"])
	}

	described := parseToolJSON(t, callTool(t, c, "describe_bdb", map[string]any{"bdb_id": created["id"]}))
	tables := described["tables"].([]any)
	names := make(map[string]bool, len(tables))
	for _, tb := range tables {
		names[tb.(map[string]any)["name"].(string)] = true
	}
	if !names["places"] || !names["categories"] {
		t.Errorf("seeded tables = %v, want places and categories", described["tables"])
	}
	seeded := make(map[string]bool)
	for _, f := range described["formulas"].([]any) {
		seeded[f.(map[string]any)["name"].(string)] = true
	}
	if len(seeded) == 0 {
		t.Fatal("expected the starter's formulas in describe_bdb")
	}

	// What the catalog advertised joins by name against what actually landed.
	catalog := parseToolJSONArray(t, callTool(t, c, "list_starters", nil))
	for _, e := range catalog {
		entry := e.(map[string]any)
		if entry["key"] != "places" {
			continue
		}
		for _, f := range entry["formulas"].([]any) {
			name := f.(map[string]any)["name"].(string)
			if !seeded[name] {
				t.Errorf("catalog formula %q not found by name in describe_bdb: %v", name, seeded)
			}
		}
	}
}
