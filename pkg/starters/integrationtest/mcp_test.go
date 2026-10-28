//go:build integration

package integrationtest

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/alexandremgo/baguettedb/pkg/common/authhttp"
	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessbdbs"
	"github.com/alexandremgo/baguettedb/pkg/starters/adapters/intraprocessdatabases"
	"github.com/alexandremgo/baguettedb/pkg/starters/domain"
	startersmcp "github.com/alexandremgo/baguettedb/pkg/starters/mcp"
	startersservice "github.com/alexandremgo/baguettedb/pkg/starters/service"
)

// setupStartersMCPClient serves the starters tools over the real SSE transport and returns a client carrying a
// signed token for userID with the given roles, so quota resolution sees the same roles a real caller would.
func setupStartersMCPClient(
	t *testing.T, userID string, roles []string, maxBdbs int, sources ...startersservice.StarterSource,
) (*mcpclient.Client, *testStacks) {
	t.Helper()
	stacks := setupStacksWithBdbLimit(t, maxBdbs)

	tv, err := authhttp.NewTokenValidator(t.Context(), testJWKS.JWKSURL(), testJWKS.Issuer(), "", testLogger)
	if err != nil {
		t.Fatalf("creating token validator: %v", err)
	}
	t.Cleanup(tv.Close)

	starterSvc := startersservice.NewStarterService(
		startersservice.NewRegistry(sources...),
		intraprocessbdbs.New(stacks.bdbSvc),
		intraprocessdatabases.New(stacks.querySvc, stacks.formulaSvc, stacks.skillSvc, stacks.dbSvc),
		testLogger,
	)

	mcpSrv := mcpserver.NewMCPServer("baguettedb-starters-test", "1.0.0")
	startersmcp.RegisterTools(mcpSrv, starterSvc, testLogger)

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

	token, err := testJWKS.BuildToken(userID, roles)
	if err != nil {
		t.Fatalf("building token: %v", err)
	}
	c, err := mcpclient.NewSSEMCPClient(ts.URL+"/mcp/sse",
		mcpclient.WithHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatalf("creating MCP client: %v", err)
	}
	if err := c.Start(t.Context()); err != nil {
		t.Fatalf("starting MCP client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	if _, err := c.Initialize(t.Context(), mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "test-client", Version: "1.0.0"},
		},
	}); err != nil {
		t.Fatalf("initializing MCP client: %v", err)
	}

	return c, stacks
}

// callStarterTool calls one tool and returns its result, failing the test on a transport error.
func callStarterTool(t *testing.T, c *mcpclient.Client, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := c.CallTool(t.Context(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: name, Arguments: args},
	})
	if err != nil {
		t.Fatalf("calling tool %s: %v", name, err)
	}
	return result
}

// toolText returns the tool result's text content.
func toolText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) == 0 {
		t.Fatal("expected content in tool result")
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	return text.Text
}

// TestMCP_ListStarters_ReturnsCatalog checks that every starter a source serves reaches the caller with the
// fields needed to choose one.
func TestMCP_ListStarters_ReturnsCatalog(t *testing.T) {
	c, _ := setupStartersMCPClient(t, uuid.NewString(), []string{"user"}, 100,
		&testSource{manifests: []domain.StarterManifest{tasksManifest()}})

	result := callStarterTool(t, c, "list_starters", nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(toolText(t, result)), &entries); err != nil {
		t.Fatalf("unmarshaling catalog: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 starter, got %d", len(entries))
	}
	if entries[0]["key"] != "tasks" {
		t.Errorf("key = %v, want tasks", entries[0]["key"])
	}
	if entries[0]["name"] != "Tasks" {
		t.Errorf("name = %v, want Tasks", entries[0]["name"])
	}
	if entries[0]["description"] != "A tiny task tracker." {
		t.Errorf("description = %v, want A tiny task tracker.", entries[0]["description"])
	}
	skills, _ := entries[0]["skills"].([]any)
	if len(skills) != 1 || skills[0] != "tasks-orientation" {
		t.Errorf("skills = %v, want [tasks-orientation]", entries[0]["skills"])
	}
	if _, ok := entries[0]["icon"]; ok {
		t.Error("icon should not reach the catalog entry")
	}
}

// TestMCP_ListStarters_VisibleFormulasOnly checks that a catalog entry lists the formulas a member will see in
// the app — name and label both, so the entries join against what the seeded bdb reports — leaving out the
// actions attached to a view and the assistant-only ones.
func TestMCP_ListStarters_VisibleFormulasOnly(t *testing.T) {
	// A second visible view, deliberately without a label, so the name fallback is exercised: Label is optional
	// in a manifest, and an unlabelled formula must still reach the catalog under a name.
	m := tasksManifest()
	m.Formulas = append(m.Formulas, domain.StarterFormula{
		Name:        "task-count",
		Description: "How many tasks are still open.",
		SQL:         "SELECT count(*) AS open FROM tasks WHERE done_at IS NULL",
	})

	c, _ := setupStartersMCPClient(t, uuid.NewString(), []string{"user"}, 100,
		&testSource{manifests: []domain.StarterManifest{m}})

	result := callStarterTool(t, c, "list_starters", nil)
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var entries []struct {
		Formulas []struct {
			Name        string `json:"name"`
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"formulas"`
	}
	if err := json.Unmarshal([]byte(toolText(t, result)), &entries); err != nil {
		t.Fatalf("unmarshaling catalog: %v", err)
	}

	// The manifest carries five formulas: two top-level views, two actions attached to one of them, and a
	// hidden view.
	if len(entries[0].Formulas) != 2 {
		t.Fatalf("expected 2 visible formulas, got %d: %+v", len(entries[0].Formulas), entries[0].Formulas)
	}
	if entries[0].Formulas[0].Name != "open-tasks" {
		t.Errorf("name = %q, want the ref open-tasks", entries[0].Formulas[0].Name)
	}
	if entries[0].Formulas[0].Label != "Open tasks" {
		t.Errorf("label = %q, want Open tasks", entries[0].Formulas[0].Label)
	}
	if entries[0].Formulas[0].Description != "Tasks not yet done, as a checklist." {
		t.Errorf("description = %q, want the manifest's", entries[0].Formulas[0].Description)
	}
	if entries[0].Formulas[1].Name != "task-count" || entries[0].Formulas[1].Label != "" {
		t.Errorf("unlabelled formula = %+v, want name task-count and no label", entries[0].Formulas[1])
	}
}

// TestMCP_CreateBdb_FromStarter checks that a starter key seeds the new bdb and that the manifest supplies the
// name and description when the caller gives none.
func TestMCP_CreateBdb_FromStarter(t *testing.T) {
	userID := uuid.NewString()
	c, stacks := setupStartersMCPClient(t, userID, []string{"user"}, 100,
		&testSource{manifests: []domain.StarterManifest{tasksManifest()}})

	result := callStarterTool(t, c, "create_bdb", map[string]any{"starter_key": "tasks"})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var created struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		StarterKey  string `json:"starter_key"`
		NextStep    string `json:"next_step"`
	}
	if err := json.Unmarshal([]byte(toolText(t, result)), &created); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if created.Name != "Tasks" {
		t.Errorf("name = %q, want the manifest's Tasks", created.Name)
	}
	if created.Description != "A tiny task tracker." {
		t.Errorf("description = %q, want the manifest's", created.Description)
	}
	if created.StarterKey != "tasks" {
		t.Errorf("starter_key = %q, want tasks", created.StarterKey)
	}
	if !strings.Contains(created.NextStep, "describe_bdb") {
		t.Errorf("next_step = %q, want it to point at describe_bdb", created.NextStep)
	}

	bdbID, err := uuid.Parse(created.ID)
	if err != nil {
		t.Fatalf("parsing returned id: %v", err)
	}
	stored, err := stacks.deps.bdbRepo.FindByID(t.Context(), bdbID)
	if err != nil {
		t.Fatalf("loading the created bdb: %v", err)
	}
	if stored.StarterKey == nil || *stored.StarterKey != "tasks" {
		t.Errorf("stored provenance = %v, want tasks", stored.StarterKey)
	}

	formulas, err := stacks.deps.formulaSvc.ListFormulasIncludingHidden(t.Context(), uuid.MustParse(userID), bdbID)
	if err != nil {
		t.Fatalf("listing formulas of the seeded bdb: %v", err)
	}
	if len(formulas) != 4 {
		t.Errorf("seeded %d formulas, want the manifest's 4", len(formulas))
	}
}

// TestMCP_CreateBdb_Blank checks that a bdb can be created with no starter at all.
func TestMCP_CreateBdb_Blank(t *testing.T) {
	c, stacks := setupStartersMCPClient(t, uuid.NewString(), []string{"user"}, 100,
		&testSource{manifests: []domain.StarterManifest{tasksManifest()}})

	result := callStarterTool(t, c, "create_bdb",
		map[string]any{"name": "Reading list", "description": "Books to read"})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	var created struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		StarterKey string `json:"starter_key"`
	}
	if err := json.Unmarshal([]byte(toolText(t, result)), &created); err != nil {
		t.Fatalf("unmarshaling result: %v", err)
	}
	if created.Name != "Reading list" {
		t.Errorf("name = %q, want Reading list", created.Name)
	}
	if created.StarterKey != "" {
		t.Errorf("starter_key = %q, want it absent for a blank bdb", created.StarterKey)
	}

	stored, err := stacks.deps.bdbRepo.FindByID(t.Context(), uuid.MustParse(created.ID))
	if err != nil {
		t.Fatalf("loading the created bdb: %v", err)
	}
	if stored.StarterKey != nil {
		t.Errorf("stored provenance = %v, want none", *stored.StarterKey)
	}
}

// TestMCP_CreateBdb_Rejections checks the refusals that are the caller's to fix, each leaving no bdb behind.
func TestMCP_CreateBdb_Rejections(t *testing.T) {
	userID := uuid.NewString()
	c, stacks := setupStartersMCPClient(t, userID, []string{"user"}, 100,
		&testSource{manifests: []domain.StarterManifest{tasksManifest()}})

	tests := []struct {
		name        string
		args        map[string]any
		wantMessage string
	}{
		{
			name:        "blank bdb without a name",
			args:        map[string]any{"description": "no name given"},
			wantMessage: "name is required",
		},
		{
			name:        "unknown starter key",
			args:        map[string]any{"starter_key": "does-not-exist"},
			wantMessage: "list_starters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := callStarterTool(t, c, "create_bdb", tt.args)
			if !result.IsError {
				t.Fatalf("expected an error, got %v", result.Content)
			}
			if msg := toolText(t, result); !strings.Contains(msg, tt.wantMessage) {
				t.Errorf("message = %q, want it to mention %q", msg, tt.wantMessage)
			}
		})
	}

	bdbs, err := stacks.bdbSvc.ListUserBdbs(t.Context(), uuid.MustParse(userID))
	if err != nil {
		t.Fatalf("listing bdbs: %v", err)
	}
	if len(bdbs) != 0 {
		t.Errorf("a rejected creation left %d bdbs behind", len(bdbs))
	}
}

// TestMCP_CreateBdb_QuotaFollowsCallerRoles checks that the bdb quota is resolved from the roles of the token
// the call carried: a plain user stops at the limit, while an admin, whose roles lift it, does not.
func TestMCP_CreateBdb_QuotaFollowsCallerRoles(t *testing.T) {
	source := &testSource{manifests: []domain.StarterManifest{tasksManifest()}}

	plain, _ := setupStartersMCPClient(t, uuid.NewString(), []string{"user"}, 1, source)
	if result := callStarterTool(t, plain, "create_bdb", map[string]any{"name": "First"}); result.IsError {
		t.Fatalf("first creation: unexpected error: %v", result.Content)
	}
	result := callStarterTool(t, plain, "create_bdb", map[string]any{"name": "Second"})
	if !result.IsError {
		t.Fatal("expected the second creation to hit the limit")
	}
	if msg := toolText(t, result); !strings.Contains(msg, "limit of 1 bdbs") {
		t.Errorf("message = %q, want it to name the limit", msg)
	}

	admin, _ := setupStartersMCPClient(t, uuid.NewString(), []string{"user", "admin"}, 1, source)
	if result := callStarterTool(t, admin, "create_bdb", map[string]any{"name": "First"}); result.IsError {
		t.Fatalf("admin first creation: unexpected error: %v", result.Content)
	}
	if result := callStarterTool(t, admin, "create_bdb", map[string]any{"name": "Second"}); result.IsError {
		t.Fatalf("admin creation past the plain limit: unexpected error: %v", result.Content)
	}
}
