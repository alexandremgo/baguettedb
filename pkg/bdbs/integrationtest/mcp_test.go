//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	bdbsmcp "github.com/alexandremgo/baguettedb/pkg/bdbs/mcp"
	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
	"github.com/alexandremgo/baguettedb/pkg/common/apptypes"
	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/common/quota"
)

// setupMCPClient wires the bdbs MCP server with an SSE transport and returns
// an initialized MCP client authenticated as the given user, along with the
// BdbService for seeding test data.
func setupMCPClient(t *testing.T, userID string) (*mcpclient.Client, *service.BdbService) {
	t.Helper()
	return setupMCPClientWithDBApp(t, userID, &mockDatabasesApp{})
}

func setupMCPClientWithDBApp(
	t *testing.T, userID string, dbApp service.DatabasesApp,
) (*mcpclient.Client, *service.BdbService) {
	t.Helper()

	bdbSvc := newBdbService(&testAuthApp{}, quota.Fixed(quota.Limits{MaxBdbs: 100, MaxMembersPerBdb: 100}))

	dbRef := &bdbsmcp.DatabasesAppRef{App: dbApp}
	mcpSrv := bdbsmcp.NewServer(bdbSvc, dbRef, bdbsmcp.Options{}, testLogger)

	tv, err := authhttp.NewTokenValidator(context.Background(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	sseServer := mcpserver.NewSSEServer(mcpSrv,
		mcpserver.WithStaticBasePath("/mcp"),
		mcpserver.WithSSEContextFunc(mcputil.SSEContextFunc),
	)

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(tv.RequireToken())
		r.Handle("/mcp/sse", sseServer.SSEHandler())
		r.Handle("/mcp/message", sseServer.MessageHandler())
	})

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)

	token := userToken(t, userID)
	c, err := mcpclient.NewSSEMCPClient(ts.URL+"/mcp/sse",
		mcpclient.WithHeaders(map[string]string{
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

	return c, bdbSvc
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

// parseToolJSON extracts the text content as a JSON object.
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

// --- MCP tool discovery tests ---

func TestMCP_ListTools(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	c, _ := setupMCPClient(t, userID)

	result, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}

	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = true
	}

	expected := []string{"list_bdbs", "describe_bdb", "update_bdb"}
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
	c, _ := setupMCPClient(t, userID)

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
	c, bdbSvc := setupMCPClient(t, userID.String())

	ctx := context.Background()
	bdb1, err := bdbSvc.CreateBdb(ctx, userID, []authz.Role{authz.RoleUser}, "Alpha Bdb", "first")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	bdb2, err := bdbSvc.CreateBdb(ctx, userID, []authz.Role{authz.RoleUser}, "Beta Bdb", "second")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "list_bdbs", nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	bdbs := parseToolJSONArray(t, result)
	if len(bdbs) != 2 {
		t.Fatalf("expected 2 bdbs, got %d", len(bdbs))
	}

	ids := map[string]bool{}
	for _, raw := range bdbs {
		c := raw.(map[string]any)
		ids[c["id"].(string)] = true
		if c["role"] != "owner" {
			t.Errorf("expected role owner, got %v", c["role"])
		}
	}
	if !ids[bdb1.ID.String()] {
		t.Errorf("bdb1 %s not found in result", bdb1.ID)
	}
	if !ids[bdb2.ID.String()] {
		t.Errorf("bdb2 %s not found in result", bdb2.ID)
	}
}

// TestMCP_OwnerAttribution verifies the owner display name in list_bdbs and describe_bdb: present for a
// caller who is not the owner (marking the bdb as someone else's), absent for the owner's own bdbs.
func TestMCP_OwnerAttribution(t *testing.T) {
	truncateAll(t)
	ownerID := uuid.New()
	memberID := uuid.New()
	ctx := context.Background()

	ownerClient, bdbSvc := setupMCPClient(t, ownerID.String())
	memberClient, _ := setupMCPClient(t, memberID.String())

	bdb, err := bdbSvc.CreateBdb(ctx, ownerID, []authz.Role{authz.RoleUser}, "Shared Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	if _, err := bdbSvc.AddMember(ctx, ownerID, bdb.ID, memberID, "editor", []authz.Role{authz.RoleUser}); err != nil {
		t.Fatalf("adding member: %v", err)
	}

	// The test auth app resolves every profile as Test User.
	const wantOwner = "Test User"

	assertOwnerField := func(payload map[string]any, wantPresent bool, context string) {
		t.Helper()
		got, present := payload["owner"]
		if wantPresent && (!present || got != wantOwner) {
			t.Errorf("%s: owner = %v, want %q", context, got, wantOwner)
		}
		if !wantPresent && present {
			t.Errorf("%s: owner must be absent for the owner's own bdb, got %v", context, got)
		}
	}

	for _, tc := range []struct {
		name        string
		client      *mcpclient.Client
		wantPresent bool
	}{
		{"member sees attribution", memberClient, true},
		{"owner does not", ownerClient, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listResult := callTool(t, tc.client, "list_bdbs", nil)
			bdbs := parseToolJSONArray(t, listResult)
			if len(bdbs) != 1 {
				t.Fatalf("expected 1 bdb, got %d", len(bdbs))
			}
			assertOwnerField(bdbs[0].(map[string]any), tc.wantPresent, "list_bdbs")

			describeResult := callTool(t, tc.client, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()})
			assertOwnerField(parseToolJSON(t, describeResult), tc.wantPresent, "describe_bdb")
		})
	}
}

func TestMCP_ListBdbs_ShowsCorrectRole(t *testing.T) {
	truncateAll(t)
	ownerID := uuid.New()
	editorID := uuid.New()
	viewerID := uuid.New()

	ctx := context.Background()
	_, bdbSvc := setupMCPClient(t, ownerID.String())

	bdb, err := bdbSvc.CreateBdb(ctx, ownerID, []authz.Role{authz.RoleUser}, "Role Test", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	if _, err := bdbSvc.AddMember(ctx, ownerID, bdb.ID, editorID, "editor", []authz.Role{authz.RoleUser}); err != nil {
		t.Fatalf("adding editor: %v", err)
	}
	if _, err := bdbSvc.AddMember(ctx, ownerID, bdb.ID, viewerID, "viewer", []authz.Role{authz.RoleUser}); err != nil {
		t.Fatalf("adding viewer: %v", err)
	}

	tests := []struct {
		name     string
		userID   uuid.UUID
		wantRole string
	}{
		{"owner sees owner role", ownerID, "owner"},
		{"editor sees editor role", editorID, "editor"},
		{"viewer sees viewer role", viewerID, "viewer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := setupMCPClient(t, tt.userID.String())
			result := callTool(t, c, "list_bdbs", nil)
			if result.IsError {
				t.Fatalf("unexpected error: %v", result.Content)
			}

			bdbs := parseToolJSONArray(t, result)
			if len(bdbs) != 1 {
				t.Fatalf("expected 1 bdb, got %d", len(bdbs))
			}

			c0 := bdbs[0].(map[string]any)
			if c0["role"] != tt.wantRole {
				t.Errorf("role = %v, want %v", c0["role"], tt.wantRole)
			}
		})
	}
}

func TestMCP_ListBdbs_Isolation(t *testing.T) {
	truncateAll(t)
	user1 := uuid.New()
	user2 := uuid.New()

	ctx := context.Background()
	_, bdbSvc := setupMCPClient(t, user1.String())

	// User 1 creates two bdbs.
	if _, err := bdbSvc.CreateBdb(ctx, user1, []authz.Role{authz.RoleUser}, "User1 Bdb A", ""); err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	if _, err := bdbSvc.CreateBdb(ctx, user1, []authz.Role{authz.RoleUser}, "User1 Bdb B", ""); err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	// User 2 creates one bdb.
	if _, err := bdbSvc.CreateBdb(ctx, user2, []authz.Role{authz.RoleUser}, "User2 Bdb", ""); err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	// User 1 should see 2 bdbs.
	c1, _ := setupMCPClient(t, user1.String())
	result1 := callTool(t, c1, "list_bdbs", nil)
	bdbs1 := parseToolJSONArray(t, result1)
	if len(bdbs1) != 2 {
		t.Errorf("user1 expected 2 bdbs, got %d", len(bdbs1))
	}

	// User 2 should see 1 bdb.
	c2, _ := setupMCPClient(t, user2.String())
	result2 := callTool(t, c2, "list_bdbs", nil)
	bdbs2 := parseToolJSONArray(t, result2)
	if len(bdbs2) != 1 {
		t.Errorf("user2 expected 1 bdb, got %d", len(bdbs2))
	}
}

func TestMCP_ListBdbs_IncludesDescription(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc := setupMCPClient(t, userID.String())

	_, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Described Bdb",
		"A detailed description")
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
	if c0["name"] != "Described Bdb" {
		t.Errorf("name = %v, want Described Bdb", c0["name"])
	}
	if c0["description"] != "A detailed description" {
		t.Errorf("description = %v, want A detailed description", c0["description"])
	}
}

// --- list_bdbs with schema enrichment tests ---

// mockDatabasesApp returns predefined table schemas and skills for any bdb.
// ListTables returns just the {name, type} pairs derived from `tables`.
type mockDatabasesApp struct {
	tables   []apptypes.TableSchema
	skills   []apptypes.Skill
	formulas []apptypes.Formula
}

func (m *mockDatabasesApp) ListTables(_ context.Context, _, _ uuid.UUID) ([]apptypes.TableInfo, error) {
	infos := make([]apptypes.TableInfo, len(m.tables))
	for i, t := range m.tables {
		infos[i] = apptypes.TableInfo{Name: t.Name, Type: t.Type}
	}
	return infos, nil
}

func (m *mockDatabasesApp) ListTablesWithSchemas(_ context.Context, _, _ uuid.UUID) ([]apptypes.TableSchema, error) {
	return m.tables, nil
}

func (m *mockDatabasesApp) ListSkills(_ context.Context, _, _ uuid.UUID) ([]apptypes.Skill, error) {
	return m.skills, nil
}

func (m *mockDatabasesApp) ListFormulas(_ context.Context, _, _ uuid.UUID) ([]apptypes.Formula, error) {
	return m.formulas, nil
}

func (m *mockDatabasesApp) DeleteDatabase(_ context.Context, _ uuid.UUID) error {
	return nil
}

// countingDatabasesApp wraps a DatabasesApp and counts how many times each port method is called, so tests can
// pin the cross-module call fan-out of the bdb MCP tools.
type countingDatabasesApp struct {
	service.DatabasesApp
	mu     sync.Mutex
	counts map[string]int
}

// record notes one call of the named method.
func (c *countingDatabasesApp) record(method string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.counts == nil {
		c.counts = make(map[string]int)
	}

	c.counts[method]++
}

// count returns how many calls of the named method were recorded.
func (c *countingDatabasesApp) count(method string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[method]
}

func (c *countingDatabasesApp) ListTables(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.TableInfo, error) {
	c.record("ListTables")
	return c.DatabasesApp.ListTables(ctx, userID, bdbID)
}

func (c *countingDatabasesApp) ListTablesWithSchemas(
	ctx context.Context, userID, bdbID uuid.UUID,
) ([]apptypes.TableSchema, error) {
	c.record("ListTablesWithSchemas")
	return c.DatabasesApp.ListTablesWithSchemas(ctx, userID, bdbID)
}

func (c *countingDatabasesApp) ListSkills(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Skill, error) {
	c.record("ListSkills")
	return c.DatabasesApp.ListSkills(ctx, userID, bdbID)
}

func (c *countingDatabasesApp) ListFormulas(ctx context.Context, userID, bdbID uuid.UUID) ([]apptypes.Formula, error) {
	c.record("ListFormulas")
	return c.DatabasesApp.ListFormulas(ctx, userID, bdbID)
}

// TestMCP_ListBdbs_BoundedFanOut pins the cross-module call budget of the two orientation tools: list_bdbs
// issues exactly one lightweight ListTables + one ListSkills per bdb and never fetches schemas or formulas;
// describe_bdb fetches schemas, skills, and formulas exactly once for its single bdb.
func TestMCP_ListBdbs_BoundedFanOut(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	counting := &countingDatabasesApp{DatabasesApp: &mockDatabasesApp{}}
	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), counting)

	const bdbCount = 3
	var lastBdbID string
	for i := range bdbCount {
		bdb, err := bdbSvc.CreateBdb(context.Background(), userID,
			[]authz.Role{authz.RoleUser}, fmt.Sprintf("Bdb %d", i), "")
		if err != nil {
			t.Fatalf("creating bdb %d: %v", i, err)
		}
		lastBdbID = bdb.ID.String()
	}

	if result := callTool(t, c, "list_bdbs", nil); result.IsError {
		t.Fatalf("list_bdbs: %v", result.Content)
	}

	wantAfterList := map[string]int{
		"ListTables": bdbCount, "ListSkills": bdbCount, "ListTablesWithSchemas": 0, "ListFormulas": 0,
	}
	for method, want := range wantAfterList {
		if got := counting.count(method); got != want {
			t.Errorf("after list_bdbs: %s called %d times, want %d", method, got, want)
		}
	}

	if result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": lastBdbID}); result.IsError {
		t.Fatalf("describe_bdb: %v", result.Content)
	}

	wantAfterDescribe := map[string]int{
		"ListTables": bdbCount, "ListSkills": bdbCount + 1, "ListTablesWithSchemas": 1, "ListFormulas": 1,
	}
	for method, want := range wantAfterDescribe {
		if got := counting.count(method); got != want {
			t.Errorf("after describe_bdb: %s called %d times, want %d", method, got, want)
		}
	}
}

// TestMCP_ListBdbs_TablesAreLightweight verifies that list_bdbs returns only table names+types (no column-level
// schemas) — it is the routing directory; schemas live in describe_bdb and describe_table.
func TestMCP_ListBdbs_TablesAreLightweight(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	mockDB := &mockDatabasesApp{
		tables: []apptypes.TableSchema{
			{
				Name: "tasks",
				Type: "table",
				Schema: apptypes.TableDescription{
					Columns: []apptypes.TableColumn{
						{Name: "id", Type: "INTEGER", PrimaryKey: true},
						{Name: "title", Type: "TEXT", NotNull: true},
					},
					ForeignKeys: []apptypes.ForeignKey{},
				},
			},
		},
	}

	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), mockDB)

	_, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Schema Bdb", "")
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

	bdb := bdbs[0].(map[string]any)
	tables := bdb["tables"].([]any)
	if len(tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(tables))
	}

	tbl := tables[0].(map[string]any)
	if tbl["name"] != "tasks" {
		t.Errorf("table name = %v, want tasks", tbl["name"])
	}
	if tbl["type"] != "table" {
		t.Errorf("table type = %v, want table", tbl["type"])
	}
	// Schema field must be absent — list_bdbs is lightweight.
	if _, present := tbl["schema"]; present {
		t.Errorf("table %v: schema must not be present in list_bdbs, got %v", tbl["name"], tbl["schema"])
	}
}

// --- list_bdbs skill enrichment tests ---

// TestMCP_ListBdbs_IncludesSkillSummariesNoBody verifies that list_bdbs
// returns name+description+target_tables for every skill but never returns
// bodies (regardless of whether the skill is bdb-level or table-targeted).
// Skill bodies live in describe_bdb (bdb-level) and describe_table (table-targeted).
func TestMCP_ListBdbs_IncludesSkillSummariesNoBody(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	mockDB := &mockDatabasesApp{
		skills: []apptypes.Skill{
			{
				ID:           "11111111-1111-1111-1111-111111111111",
				Name:         "bdb-overview",
				Description:  "How this bdb is structured.",
				Body:         "Should not appear in list_bdbs.",
				TargetTables: []string{},
			},
			{
				ID:           "22222222-2222-2222-2222-222222222222",
				Name:         "groceries-flow",
				Description:  "How to organize groceries.",
				Body:         "Should not appear in list_bdbs either.",
				TargetTables: []string{"groceries"},
			},
		},
	}

	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), mockDB)

	_, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Skill Bdb", "")
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

	bdb := bdbs[0].(map[string]any)
	skills, ok := bdb["skills"].([]any)
	if !ok {
		t.Fatalf("expected skills field to be an array, got %T", bdb["skills"])
	}
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}

	// list_bdbs skill entries are routing summaries: name + description only.
	for _, raw := range skills {
		s := raw.(map[string]any)
		if s["name"] == nil || s["name"].(string) == "" {
			t.Errorf("skill missing name: %v", s)
		}
		if s["description"] == nil || s["description"].(string) == "" {
			t.Errorf("skill missing description: %v", s)
		}
		for _, field := range []string{"body", "id", "target_tables"} {
			if _, present := s[field]; present {
				t.Errorf("skill %s: %s should not be present in list_bdbs, got %v", s["name"], field, s[field])
			}
		}
	}
}

// TestMCP_ListBdbs_NoSkillsReturnsEmptyArray verifies that a bdb with
// zero skills returns an empty `skills` array (not null).
func TestMCP_ListBdbs_NoSkillsReturnsEmptyArray(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	mockDB := &mockDatabasesApp{}
	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), mockDB)

	_, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Empty Bdb", "")
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

	bdb := bdbs[0].(map[string]any)
	skills, ok := bdb["skills"].([]any)
	if !ok {
		t.Fatalf("expected skills field to be an array (even when empty), got %T", bdb["skills"])
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
}

// --- describe_bdb tests ---

func TestMCP_DescribeBdb_ReturnsTablesAndSkills(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	mockDB := &mockDatabasesApp{
		tables: []apptypes.TableSchema{
			{
				Name: "groceries",
				Type: "table",
				Schema: apptypes.TableDescription{
					Columns: []apptypes.TableColumn{
						{Name: "id", Type: "INTEGER", PrimaryKey: true},
						{Name: "item_id", Type: "INTEGER", NotNull: true},
					},
					ForeignKeys: []apptypes.ForeignKey{
						{From: "item_id", Table: "items", To: "id"},
					},
				},
			},
			{
				Name: "inventory",
				Type: "table",
				Schema: apptypes.TableDescription{
					Columns: []apptypes.TableColumn{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
				},
			},
		},
		skills: []apptypes.Skill{
			{
				ID:           "11111111-1111-1111-1111-111111111111",
				Name:         "bdb-overview",
				Description:  "Bdb-level guidance.",
				Body:         "Body of the bdb-level skill.",
				TargetTables: []string{},
			},
			{
				ID:           "22222222-2222-2222-2222-222222222222",
				Name:         "groceries-flow",
				Description:  "Groceries handling.",
				Body:         "Body of the table-targeted skill.",
				TargetTables: []string{"groceries"},
			},
		},
	}

	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), mockDB)

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Test Bdb", "desc")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()})
	if result.IsError {
		t.Fatalf("describe_bdb returned error: %v", result.Content)
	}

	overview := parseToolJSON(t, result)
	if overview["id"] != bdb.ID.String() {
		t.Errorf("id = %v, want %v", overview["id"], bdb.ID)
	}
	if overview["role"] != "owner" {
		t.Errorf("role = %v, want owner", overview["role"])
	}

	tables, _ := overview["tables"].([]any)
	if len(tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(tables))
	}
	// Within the schema table limit, every table entry carries its columns (and foreign keys when it has any),
	// flattened — no nested schema object.
	for _, raw := range tables {
		tbl := raw.(map[string]any)
		if tbl["name"] == nil || tbl["type"] == nil {
			t.Errorf("table missing name/type: %v", tbl)
		}
		if cols, _ := tbl["columns"].([]any); len(cols) == 0 {
			t.Errorf("table %v: expected columns inlined, got %v", tbl["name"], tbl["columns"])
		}
		if _, present := tbl["schema"]; present {
			t.Errorf("table %v: nested schema object must not be present, got %v", tbl["name"], tbl["schema"])
		}
	}
	groceries := tables[0].(map[string]any)
	if fks, _ := groceries["foreign_keys"].([]any); len(fks) != 1 {
		t.Errorf("groceries: expected 1 foreign key inlined, got %v", groceries["foreign_keys"])
	}

	skills, _ := overview["skills"].([]any)
	if len(skills) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(skills))
	}
	// Within the skill-body budget, every skill body is inlined — table-targeted ones included.
	for _, raw := range skills {
		s := raw.(map[string]any)
		if body, _ := s["body"].(string); body == "" {
			t.Errorf("skill %s: expected body inlined, got none", s["name"])
		}
	}
	if _, present := overview["omitted"]; present {
		t.Errorf("omitted must be absent when nothing was dropped, got %v", overview["omitted"])
	}
}

// TestMCP_DescribeBdb_SkillBodyBudgetDegradesTargeted verifies the size-budget fallback: when a bdb's summed skill
// bodies exceed the budget, table-targeted bodies degrade to summaries, bdb-level bodies stay inlined, and the
// omitted note tells the agent what to call instead.
func TestMCP_DescribeBdb_SkillBodyBudgetDegradesTargeted(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	bigBody := strings.Repeat("x", 10*1024)
	mockDB := &mockDatabasesApp{
		skills: []apptypes.Skill{
			{
				ID:           "11111111-1111-1111-1111-111111111111",
				Name:         "bdb-overview",
				Description:  "Bdb-level guidance.",
				Body:         bigBody,
				TargetTables: []string{},
			},
			{
				ID:           "22222222-2222-2222-2222-222222222222",
				Name:         "groceries-flow",
				Description:  "Groceries handling.",
				Body:         bigBody,
				TargetTables: []string{"groceries"},
			},
		},
	}

	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), mockDB)

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Big Skills", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()})
	if result.IsError {
		t.Fatalf("describe_bdb returned error: %v", result.Content)
	}

	overview := parseToolJSON(t, result)
	for _, raw := range overview["skills"].([]any) {
		s := raw.(map[string]any)
		body, _ := s["body"].(string)
		targets, _ := s["target_tables"].([]any)
		if len(targets) == 0 && body == "" {
			t.Errorf("bdb-level skill %s: body must stay inlined over budget", s["name"])
		}
		if len(targets) > 0 && body != "" {
			t.Errorf("table-targeted skill %s: body must degrade to a summary over budget", s["name"])
		}
	}

	omitted, _ := overview["omitted"].([]any)
	if len(omitted) != 1 {
		t.Fatalf("expected 1 omitted note, got %v", overview["omitted"])
	}
	note, _ := omitted[0].(string)
	if !strings.Contains(note, "describe_table") || !strings.Contains(note, "get_skill") {
		t.Errorf("omitted note must point at describe_table/get_skill, got %q", note)
	}
}

// TestMCP_DescribeBdb_SchemaTableLimitDegradesToNames verifies the table-limit fallback: when a bdb has more
// tables than the schema limit, every table entry degrades to name+type and the omitted note points at
// describe_table.
func TestMCP_DescribeBdb_SchemaTableLimitDegradesToNames(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	tables := make([]apptypes.TableSchema, 21)
	for i := range tables {
		tables[i] = apptypes.TableSchema{
			Name: fmt.Sprintf("t%02d", i),
			Type: "table",
			Schema: apptypes.TableDescription{
				Columns: []apptypes.TableColumn{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
			},
		}
	}
	mockDB := &mockDatabasesApp{tables: tables}

	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), mockDB)

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Wide Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()})
	if result.IsError {
		t.Fatalf("describe_bdb returned error: %v", result.Content)
	}

	overview := parseToolJSON(t, result)
	gotTables, _ := overview["tables"].([]any)
	if len(gotTables) != 21 {
		t.Fatalf("expected 21 tables, got %d", len(gotTables))
	}
	for _, raw := range gotTables {
		tbl := raw.(map[string]any)
		if _, present := tbl["columns"]; present {
			t.Errorf("table %v: columns must degrade to name+type over the table limit", tbl["name"])
		}
	}

	omitted, _ := overview["omitted"].([]any)
	if len(omitted) != 1 {
		t.Fatalf("expected 1 omitted note, got %v", overview["omitted"])
	}
	if note, _ := omitted[0].(string); !strings.Contains(note, "describe_table") {
		t.Errorf("omitted note must point at describe_table, got %q", note)
	}
}

// TestMCP_DescribeBdb_IncludesFormulas verifies that describe_bdb surfaces the bdb's
// formulas as lightweight summaries (id, name, description, kind) for orientation — the SQL and
// parameter schema are not included (they come from get_formula).
func TestMCP_DescribeBdb_IncludesFormulas(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	mockDB := &mockDatabasesApp{
		formulas: []apptypes.Formula{
			{
				ID:          "33333333-3333-3333-3333-333333333333",
				Name:        "low-stock",
				Description: "Items running low.",
				Kind:        "view",
			},
			{
				ID:          "44444444-4444-4444-4444-444444444444",
				Name:        "restock",
				Description: "Bump a row's quantity.",
				Kind:        "action",
			},
		},
	}

	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), mockDB)

	bdb, err := bdbSvc.CreateBdb(
		context.Background(), userID, []authz.Role{authz.RoleUser}, "Formula Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()})
	if result.IsError {
		t.Fatalf("describe_bdb returned error: %v", result.Content)
	}

	overview := parseToolJSON(t, result)
	formulas, ok := overview["formulas"].([]any)
	if !ok {
		t.Fatalf("expected formulas field to be an array, got %T", overview["formulas"])
	}
	if len(formulas) != 2 {
		t.Fatalf("expected 2 formulas, got %d", len(formulas))
	}

	byName := make(map[string]map[string]any, len(formulas))
	for _, raw := range formulas {
		f := raw.(map[string]any)
		byName[f["name"].(string)] = f
		if f["description"] == nil || f["description"].(string) == "" {
			t.Errorf("formula %v missing description", f["name"])
		}
		// The SQL must never be surfaced in describe_bdb.
		if _, present := f["sql"]; present {
			t.Errorf("formula %v: sql must not be present in describe_bdb", f["name"])
		}
	}
	if byName["low-stock"]["kind"] != "view" {
		t.Errorf("low-stock kind = %v, want view", byName["low-stock"]["kind"])
	}
	if byName["restock"]["kind"] != "action" {
		t.Errorf("restock kind = %v, want action", byName["restock"]["kind"])
	}
}

func TestMCP_DescribeBdb_EmptyBdbReturnsEmptyArrays(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()

	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), &mockDatabasesApp{})

	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Empty Bdb", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	overview := parseToolJSON(t, result)
	tables, ok := overview["tables"].([]any)
	if !ok {
		t.Fatalf("tables must be an array (even when empty), got %T", overview["tables"])
	}
	if len(tables) != 0 {
		t.Errorf("expected 0 tables, got %d", len(tables))
	}
	skills, ok := overview["skills"].([]any)
	if !ok {
		t.Fatalf("skills must be an array (even when empty), got %T", overview["skills"])
	}
	if len(skills) != 0 {
		t.Errorf("expected 0 skills, got %d", len(skills))
	}
	formulas, ok := overview["formulas"].([]any)
	if !ok {
		t.Fatalf("formulas must be an array (even when empty), got %T", overview["formulas"])
	}
	if len(formulas) != 0 {
		t.Errorf("expected 0 formulas, got %d", len(formulas))
	}
}

func TestMCP_DescribeBdb_NotMemberReturnsError(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	otherUserID := uuid.New()

	c, bdbSvc := setupMCPClientWithDBApp(t, otherUserID.String(), &mockDatabasesApp{})

	// Owned by userID, not otherUserID.
	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Private", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "describe_bdb", map[string]any{"bdb_id": bdb.ID.String()})
	if !result.IsError {
		t.Error("expected error when caller is not a member of the bdb")
	}
}

func TestMCP_DescribeBdb_MissingBdbIDReturnsError(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, _ := setupMCPClientWithDBApp(t, userID.String(), &mockDatabasesApp{})

	result := callTool(t, c, "describe_bdb", map[string]any{})
	if !result.IsError {
		t.Error("expected error when bdb_id is missing")
	}
}

// --- update_bdb tests ---

// TestMCP_UpdateBdb_RenamesAndRedescribes checks that an owner can change both fields in one call, and that the
// result echoes the bdb as it now stands.
func TestMCP_UpdateBdb_RenamesAndRedescribes(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc := setupMCPClient(t, userID.String())

	bdb, err := bdbSvc.CreateBdb(
		context.Background(), userID, []authz.Role{authz.RoleUser}, "Old Name", "old desc")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "update_bdb", map[string]any{
		"bdb_id":      bdb.ID.String(),
		"name":        "New Name",
		"description": "new desc",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	updated := parseToolJSON(t, result)
	if updated["name"] != "New Name" {
		t.Errorf("name = %v, want New Name", updated["name"])
	}
	if updated["description"] != "new desc" {
		t.Errorf("description = %v, want new desc", updated["description"])
	}
}

// TestMCP_UpdateBdb_OmittedFieldUnchanged checks that each field can be changed on its own, leaving the other
// as it was.
func TestMCP_UpdateBdb_OmittedFieldUnchanged(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc := setupMCPClient(t, userID.String())

	bdb, err := bdbSvc.CreateBdb(
		context.Background(), userID, []authz.Role{authz.RoleUser}, "Old Name", "keep me")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}

	result := callTool(t, c, "update_bdb", map[string]any{"bdb_id": bdb.ID.String(), "name": "New Name"})
	if result.IsError {
		t.Fatalf("renaming: unexpected error: %v", result.Content)
	}
	updated := parseToolJSON(t, result)
	if updated["name"] != "New Name" {
		t.Errorf("name = %v, want New Name", updated["name"])
	}
	if updated["description"] != "keep me" {
		t.Errorf("description = %v, want keep me", updated["description"])
	}

	result = callTool(t, c, "update_bdb", map[string]any{"bdb_id": bdb.ID.String(), "description": "new desc"})
	if result.IsError {
		t.Fatalf("re-describing: unexpected error: %v", result.Content)
	}
	updated = parseToolJSON(t, result)
	if updated["name"] != "New Name" {
		t.Errorf("name = %v, want New Name", updated["name"])
	}
	if updated["description"] != "new desc" {
		t.Errorf("description = %v, want new desc", updated["description"])
	}
}

// TestMCP_UpdateBdb_NonOwnerRefused checks that a member who is not the owner cannot rename a shared bdb.
func TestMCP_UpdateBdb_NonOwnerRefused(t *testing.T) {
	truncateAll(t)
	ownerID := uuid.New()
	editorID := uuid.New()

	ctx := context.Background()
	_, bdbSvc := setupMCPClient(t, ownerID.String())

	bdb, err := bdbSvc.CreateBdb(ctx, ownerID, []authz.Role{authz.RoleUser}, "Shared Bdb", "desc")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	if _, err := bdbSvc.AddMember(
		ctx, ownerID, bdb.ID, editorID, "editor", []authz.Role{authz.RoleUser}); err != nil {
		t.Fatalf("adding editor: %v", err)
	}

	editorClient, _ := setupMCPClient(t, editorID.String())
	result := callTool(t, editorClient, "update_bdb", map[string]any{
		"bdb_id": bdb.ID.String(),
		"name":   "Renamed by editor",
	})
	if !result.IsError {
		t.Fatalf("expected an error for a non-owner, got %v", result.Content)
	}

	reloaded, err := bdbSvc.GetBdb(ctx, ownerID, bdb.ID)
	if err != nil {
		t.Fatalf("reloading bdb: %v", err)
	}
	if reloaded.Name != "Shared Bdb" {
		t.Errorf("name = %q, want Shared Bdb", reloaded.Name)
	}
}

// TestMCP_UpdateBdb_SoftDeletedRefused checks that a bdb waiting to be purged cannot be edited.
func TestMCP_UpdateBdb_SoftDeletedRefused(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc := setupMCPClient(t, userID.String())

	ctx := context.Background()
	bdb, err := bdbSvc.CreateBdb(ctx, userID, []authz.Role{authz.RoleUser}, "Doomed", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	if err := bdbSvc.DeleteBdb(ctx, userID, bdb.ID); err != nil {
		t.Fatalf("deleting bdb: %v", err)
	}

	result := callTool(t, c, "update_bdb", map[string]any{"bdb_id": bdb.ID.String(), "name": "Revived"})
	if !result.IsError {
		t.Fatalf("expected an error for a soft-deleted bdb, got %v", result.Content)
	}
}
