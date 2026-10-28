//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// setupMCPClientWithRole starts an httptest server with a role-aware BdbsApp
// and returns an initialized MCP SSE client authenticated as the given user.
func setupMCPClientWithRole(t *testing.T, userID string, role authz.BdbRole) *mcpclient.Client {
	t.Helper()
	router := setupRouterWithRole(t, role)
	return startMCPClient(t, router, userID)
}

// setupMCPClient starts an httptest server with the full router and returns
// an initialized MCP SSE client authenticated as the given user.
func setupMCPClient(t *testing.T, userID string) *mcpclient.Client {
	t.Helper()
	router := setupRouter(t)
	return startMCPClient(t, router, userID)
}

// startMCPClient creates an httptest server from the given router and returns
// an initialized MCP SSE client authenticated as the given user.
func startMCPClient(t *testing.T, router chi.Router, userID string) *mcpclient.Client {
	t.Helper()

	ts := httptest.NewServer(router)
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

	return c
}

// callTool is a helper that calls an MCP tool and returns the text content.
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

// --- MCP tool discovery tests ---

func TestMCP_ListTools(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	c := setupMCPClient(t, userID)

	result, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}

	toolNames := make(map[string]bool)
	for _, tool := range result.Tools {
		toolNames[tool.Name] = true
	}

	expected := []string{
		"describe_table", "execute_query", "execute_data", "list_changes", "undo_last_change",
		"redo_last_change", "undo_last_change_anyone", "redo_last_change_anyone", "execute_schema",
		"revert_change", "get_skill", "create_skill", "update_skill", "get_docs",
	}
	for _, name := range expected {
		if !toolNames[name] {
			t.Errorf("expected tool %q not found in %v", name, toolNames)
		}
	}

	// Legacy names from before the rename must not be registered (would mask agent prompts).
	legacy := []string{"execute_mutation", "list_mutations", "undo_last_mutation", "redo_last_mutation",
		"revert_mutation"}
	for _, name := range legacy {
		if toolNames[name] {
			t.Errorf("legacy tool %q is still registered (should be removed by step 8 rename)", name)
		}
	}
}

// TestMCP_ListChanges_KindFilter pins that the kind argument scopes results to DML, DDL, or all.
func TestMCP_ListChanges_KindFilter(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// 1 DDL batch (CREATE TABLE).
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	// 2 DDL batches (ALTER + DROP)? Actually let's do ALTER + ALTER to make 3 DDL total.
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "ALTER TABLE items ADD COLUMN qty INTEGER",
	})
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "ALTER TABLE items ADD COLUMN tag TEXT",
	})

	// 2 DML batches.
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('bread')",
	})

	// kind=DML → 2 batches.
	rDML := callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID,
		"kind":   "DML",
	})
	if got := len(parseToolJSON(t, rDML)["batches"].([]any)); got != 2 {
		t.Errorf("kind=DML batches = %d, want 2", got)
	}

	// kind=DDL → 3 batches.
	rDDL := callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID,
		"kind":   "DDL",
	})
	if got := len(parseToolJSON(t, rDDL)["batches"].([]any)); got != 3 {
		t.Errorf("kind=DDL batches = %d, want 3", got)
	}

	// Omitted → all 5.
	rAll := callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID,
	})
	if got := len(parseToolJSON(t, rAll)["batches"].([]any)); got != 5 {
		t.Errorf("no kind filter batches = %d, want 5", got)
	}

	// Invalid kind → error.
	rBad := callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID,
		"kind":   "BOGUS",
	})
	if !rBad.IsError {
		t.Error("invalid kind value must produce an error")
	}
}

// --- MCP tool execution tests ---

func TestMCP_ExecuteSchema_CreateTable(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

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

func TestMCP_DescribeTable(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

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

	// Verifies structured columns.
	columns := data["columns"].([]any)
	if len(columns) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(columns))
	}

	col0 := columns[0].(map[string]any)
	if col0["name"] != "id" {
		t.Errorf("col0 name = %v, want id", col0["name"])
	}
	if col0["primary_key"] != true {
		t.Errorf("col0 primary_key = %v, want true", col0["primary_key"])
	}

	col1 := columns[1].(map[string]any)
	if col1["name"] != "name" {
		t.Errorf("col1 name = %v, want name", col1["name"])
	}
	if col1["not_null"] != true {
		t.Errorf("col1 not_null = %v, want true", col1["not_null"])
	}

	// Verifies foreign_keys is present (empty for this table).
	fks := data["foreign_keys"].([]any)
	if len(fks) != 0 {
		t.Errorf("expected 0 foreign keys, got %d", len(fks))
	}

	// Regular table: kind="table", module omitted.
	if kind, _ := data["kind"].(string); kind != "table" {
		t.Errorf("kind = %q, want %q", kind, "table")
	}
	if _, present := data["module"]; present {
		t.Errorf("module should be omitted on a regular table, got %v", data["module"])
	}
}

// TestMCP_DescribeTable_VirtualFTS5 confirms describe_table surfaces the
// kind/module marker for FTS5 virtual tables so agents know to use MATCH
// rather than treating them as regular tables.
func TestMCP_DescribeTable_VirtualFTS5(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	r1 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT)",
	})
	if r1.IsError {
		t.Fatalf("CREATE TABLE notes failed: %v", r1.Content)
	}
	r2 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE VIRTUAL TABLE notes_fts USING fts5(title, body, content='notes', content_rowid='id')",
	})
	if r2.IsError {
		t.Fatalf("CREATE VIRTUAL TABLE notes_fts failed: %v", r2.Content)
	}

	result := callTool(t, c, "describe_table", map[string]any{
		"bdb_id":     bdbID,
		"table_name": "notes_fts",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data := parseToolJSON(t, result)
	if kind, _ := data["kind"].(string); kind != "virtual" {
		t.Errorf("kind = %q, want %q", kind, "virtual")
	}
	if module, _ := data["module"].(string); module != "fts5" {
		t.Errorf("module = %q, want %q", module, "fts5")
	}
}

// TestMCP_FTS5_NotesPattern_EndToEnd exercises the full FTS5 workflow through the platform: a base table, a virtual
// FTS5 index, a user-defined sync trigger, a row insert, then a MATCH query. Catches the regen blocker (creating
// triggers on the vtable) and confirms the user sync trigger survives the platform's per-table _change_log trigger
// regen on `notes`.
func TestMCP_FTS5_NotesPattern_EndToEnd(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	r1 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT)",
	})
	if r1.IsError {
		t.Fatalf("CREATE TABLE notes failed: %v", r1.Content)
	}

	r2 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE VIRTUAL TABLE notes_fts USING fts5(title, body, content='notes', content_rowid='id')",
	})
	if r2.IsError {
		t.Fatalf("CREATE VIRTUAL TABLE notes_fts failed: %v", r2.Content)
	}

	r3 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TRIGGER notes_ai AFTER INSERT ON notes BEGIN " +
			"INSERT INTO notes_fts(rowid, title, body) VALUES (new.id, new.title, new.body); END",
	})
	if r3.IsError {
		t.Fatalf("CREATE TRIGGER notes_ai failed: %v", r3.Content)
	}

	r4 := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO notes (title, body) VALUES ('Sourdough', 'flour water salt time')",
	})
	if r4.IsError {
		t.Fatalf("INSERT INTO notes failed: %v", r4.Content)
	}

	r5 := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT title FROM notes_fts WHERE notes_fts MATCH 'flour'",
	})
	if r5.IsError {
		t.Fatalf("MATCH query failed: %v", r5.Content)
	}
	data := parseToolJSON(t, r5)
	rows, _ := data["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 MATCH row, got %d", len(rows))
	}
	title, _ := rows[0].([]any)[0].(map[string]any)["value"].(string)
	if title != "Sourdough" {
		t.Errorf("matched row title = %q, want %q", title, "Sourdough")
	}
}

// TestMCP_FTS5_DropAndUndo confirms DROP TABLE on an FTS5 virtual table is
// captured as a DDL change with the verbatim CREATE statement and that undo
// restores the vtable. Row payload is intentionally not captured for vtables —
// after undo the index is empty and must be rebuilt from the source.
func TestMCP_FTS5_DropAndUndo_EndToEnd(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	r1 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT, body TEXT)",
	})
	if r1.IsError {
		t.Fatalf("CREATE TABLE notes failed: %v", r1.Content)
	}

	r2 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE VIRTUAL TABLE notes_fts USING fts5(title, body, content='notes', content_rowid='id')",
	})
	if r2.IsError {
		t.Fatalf("CREATE VIRTUAL TABLE notes_fts failed: %v", r2.Content)
	}

	r3 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TRIGGER notes_ai AFTER INSERT ON notes BEGIN " +
			"INSERT INTO notes_fts(rowid, title, body) VALUES (new.id, new.title, new.body); END",
	})
	if r3.IsError {
		t.Fatalf("CREATE TRIGGER notes_ai failed: %v", r3.Content)
	}

	r4 := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO notes (title, body) VALUES ('Brioche', 'eggs butter milk flour')",
	})
	if r4.IsError {
		t.Fatalf("INSERT INTO notes failed: %v", r4.Content)
	}

	r5 := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) AS cnt FROM notes_fts WHERE notes_fts MATCH 'butter'",
	})
	if r5.IsError {
		t.Fatalf("MATCH (pre-drop) failed: %v", r5.Content)
	}
	if cnt := matchCount(t, r5); cnt != 1 {
		t.Fatalf("pre-drop MATCH count = %d, want 1", cnt)
	}

	r6 := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "DROP TABLE notes_fts",
	})
	if r6.IsError {
		t.Fatalf("DROP TABLE notes_fts failed: %v", r6.Content)
	}

	r7 := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) AS cnt FROM sqlite_master WHERE name = 'notes_fts'",
	})
	if r7.IsError {
		t.Fatalf("post-drop sqlite_master check failed: %v", r7.Content)
	}
	if cnt := matchCount(t, r7); cnt != 0 {
		t.Fatalf("post-drop sqlite_master count = %d, want 0", cnt)
	}

	r8 := callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if r8.IsError {
		t.Fatalf("undo_last_change failed: %v", r8.Content)
	}

	r9 := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) AS cnt FROM sqlite_master WHERE name = 'notes_fts'",
	})
	if r9.IsError {
		t.Fatalf("post-undo sqlite_master check failed: %v", r9.Content)
	}
	if cnt := matchCount(t, r9); cnt != 1 {
		t.Fatalf("post-undo sqlite_master count = %d, want 1 (vtable restored)", cnt)
	}

	// Index is empty after undo — vtable shape restored but row payload was
	// intentionally not captured.
	r10 := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) AS cnt FROM notes_fts WHERE notes_fts MATCH 'butter'",
	})
	if r10.IsError {
		t.Fatalf("post-undo MATCH failed: %v", r10.Content)
	}
	if cnt := matchCount(t, r10); cnt != 0 {
		t.Errorf("post-undo MATCH count = %d, want 0 (empty index pending rebuild)", cnt)
	}
}

// matchCount extracts row 0 column 0 from an execute_query result as an int.
func matchCount(t *testing.T, result *mcp.CallToolResult) int {
	t.Helper()
	data := parseToolJSON(t, result)
	rows, _ := data["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal("expected at least 1 row")
	}
	v := rows[0].([]any)[0].(map[string]any)["value"]
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	t.Fatalf("unexpected count value type %T", v)
	return 0
}

func TestMCP_DescribeTable_WithForeignKeys(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, author_id INTEGER REFERENCES authors(id))",
	})

	result := callTool(t, c, "describe_table", map[string]any{
		"bdb_id":     bdbID,
		"table_name": "books",
	})
	if result.IsError {
		t.Fatalf("unexpected error: %v", result.Content)
	}

	data := parseToolJSON(t, result)

	columns := data["columns"].([]any)
	if len(columns) != 3 {
		t.Fatalf("expected 3 columns, got %d", len(columns))
	}

	fks := data["foreign_keys"].([]any)
	if len(fks) != 1 {
		t.Fatalf("expected 1 foreign key, got %d", len(fks))
	}

	fk := fks[0].(map[string]any)
	if fk["table"] != "authors" {
		t.Errorf("FK table = %v, want authors", fk["table"])
	}
	if fk["from"] != "author_id" {
		t.Errorf("FK from = %v, want author_id", fk["from"])
	}
	if fk["to"] != "id" {
		t.Errorf("FK to = %v, want id", fk["to"])
	}
}

func TestMCP_ExecuteQuery_Select(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

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

	// Verifies columns.
	columns := data["columns"].([]any)
	if len(columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(columns))
	}
	col0 := columns[0].(map[string]any)
	col1 := columns[1].(map[string]any)
	if col0["name"] != "id" || col1["name"] != "name" {
		t.Errorf("columns = [%v, %v], want [id, name]", col0["name"], col1["name"])
	}

	// Verifies rows.
	rows := data["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	row0 := rows[0].([]any)
	row1 := rows[1].([]any)
	// Values are domain.Value structs serialized as {Type, Value}.
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
	c := setupMCPClient(t, userID)

	result := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": uuid.New().String(),
		"sql":    "",
	})
	if !result.IsError {
		t.Error("expected error for empty sql")
	}
}

func TestMCP_DescribeTable_InvalidName(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	c := setupMCPClient(t, userID)

	result := callTool(t, c, "describe_table", map[string]any{
		"bdb_id":     uuid.New().String(),
		"table_name": "drop table",
	})
	if !result.IsError {
		t.Error("expected error for invalid table name")
	}
}

// --- MCP multi-statement rejection tests ---

func TestMCP_MultiStatement_RejectedOnAllTools(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

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

	tools := []string{"execute_query", "execute_data", "execute_schema"}

	for _, stmt := range statements {
		for _, tool := range tools {
			t.Run(stmt.name+"/"+tool, func(t *testing.T) {
				result := callTool(t, c, tool, map[string]any{
					"bdb_id": bdbID,
					"sql":    stmt.sql,
				})
				if !result.IsError {
					t.Errorf("expected error for multi-statement query %q on tool %s", stmt.sql, tool)
				}
			})
		}
	}
}

// --- MCP permission enforcement tests ---

func TestMCP_ExecuteSchema_OwnerCanDDL(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClientWithRole(t, userID, authz.BdbRoleOwner)

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE t (id INTEGER PRIMARY KEY)",
	})
	if result.IsError {
		t.Fatalf("owner should be able to DDL, got error: %v", result.Content)
	}
}

func TestMCP_ExecuteMutation_EditorCanDML(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates table as owner first.
	ownerClient := setupMCPClientWithRole(t, userID, authz.BdbRoleOwner)
	callTool(t, ownerClient, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)",
	})

	editorClient := setupMCPClientWithRole(t, userID, authz.BdbRoleEditor)
	result := callTool(t, editorClient, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO t VALUES (1, 'hello')",
	})
	if result.IsError {
		t.Fatalf("editor should be able to DML, got error: %v", result.Content)
	}
}

func TestMCP_ExecuteSchema_EditorCannotDDL(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClientWithRole(t, userID, authz.BdbRoleEditor)

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE t (id INTEGER PRIMARY KEY)",
	})
	if !result.IsError {
		t.Error("editor should not be able to DDL")
	}
}

func TestMCP_ExecuteMutation_ViewerCannotDML(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates table as owner first.
	ownerClient := setupMCPClientWithRole(t, userID, authz.BdbRoleOwner)
	callTool(t, ownerClient, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE t (id INTEGER PRIMARY KEY)",
	})

	viewerClient := setupMCPClientWithRole(t, userID, authz.BdbRoleViewer)
	result := callTool(t, viewerClient, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO t VALUES (1)",
	})
	if !result.IsError {
		t.Error("viewer should not be able to DML")
	}
}

func TestMCP_ExecuteSchema_ViewerCannotDDL(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClientWithRole(t, userID, authz.BdbRoleViewer)

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE t (id INTEGER PRIMARY KEY)",
	})
	if !result.IsError {
		t.Error("viewer should not be able to DDL")
	}
}

func TestMCP_ExecuteQuery_ViewerCanDQL(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Creates table and data as owner.
	ownerClient := setupMCPClientWithRole(t, userID, authz.BdbRoleOwner)
	callTool(t, ownerClient, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, ownerClient, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO t VALUES (1, 'hello')",
	})

	viewerClient := setupMCPClientWithRole(t, userID, authz.BdbRoleViewer)
	result := callTool(t, viewerClient, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT * FROM t",
	})
	if result.IsError {
		t.Fatalf("viewer should be able to DQL, got error: %v", result.Content)
	}

	data := parseToolJSON(t, result)
	rows := data["rows"].([]any)
	if len(rows) != 1 {
		t.Errorf("expected 1 row, got %d", len(rows))
	}
}

// --- MCP batch ID and affected rows tests ---

func TestMCP_ExecuteMutation_ResponseIncludesBatchID(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})
	data := parseToolJSON(t, result)

	batchID, ok := data["batch_id"].(string)
	if !ok || batchID == "" {
		t.Fatalf("expected non-empty BatchID, got %v", data["batch_id"])
	}

	affectedRows, ok := data["affected_rows"].([]any)
	if !ok || len(affectedRows) == 0 {
		t.Fatalf("expected affected_rows array with entries, got %v", data["affected_rows"])
	}

	row := affectedRows[0].(map[string]any)
	if row["table_name"] != "items" {
		t.Errorf("affected_row.table_name = %v, want items", row["table_name"])
	}
	if row["operation"] != "INSERT" {
		t.Errorf("affected_row.operation = %v, want INSERT", row["operation"])
	}
}

// --- MCP list mutations tests ---

func TestMCP_ListChanges(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})

	result := callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID,
		"kind":   "DML",
	})
	if result.IsError {
		t.Fatalf("list_mutations failed: %v", result.Content)
	}

	data := parseToolJSON(t, result)
	batches := data["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}

	batch := batches[0].(map[string]any)
	if batch["origin"] != "change" {
		t.Errorf("origin = %v, want change", batch["origin"])
	}
}

func TestMCP_ListChanges_WithTableFilter(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE other (id INTEGER PRIMARY KEY, val TEXT)",
	})

	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO other (val) VALUES ('x')",
	})

	result := callTool(t, c, "list_changes", map[string]any{
		"bdb_id":     bdbID,
		"table_name": "items",
		"kind":       "DML",
	})
	data := parseToolJSON(t, result)
	batches := data["batches"].([]any)
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch for 'items' filter, got %d", len(batches))
	}
}

// --- MCP list_mutations user_id filter tests (plan #3, step 6) ---

func TestMCP_ListChanges_DefaultReturnsAllUsers(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('a1')",
	})
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('a2')",
	})
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('b1')",
	})
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('b2')",
	})

	// Default (no user_id arg, called by alice) should return all 4 DML batches.
	result := callTool(t, alice, "list_changes", map[string]any{
		"bdb_id": bdbID,
		"kind":   "DML",
	})
	data := parseToolJSON(t, result)
	batches := data["batches"].([]any)
	if len(batches) != 4 {
		t.Errorf("default list_mutations should return all 4 batches (audit view), got %d", len(batches))
	}
}

func TestMCP_ListChanges_FilterByUser(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('a1')",
	})
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('a2')",
	})
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('b1')",
	})
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('b2')",
	})

	// Scoped to alice → 2 DML batches.
	rA := callTool(t, alice, "list_changes", map[string]any{
		"bdb_id":  bdbID,
		"user_id": aliceID,
		"kind":    "DML",
	})
	if got := len(parseToolJSON(t, rA)["batches"].([]any)); got != 2 {
		t.Errorf("user_id=alice batches = %d, want 2", got)
	}

	// Scoped to bob → 2 DML batches.
	rB := callTool(t, alice, "list_changes", map[string]any{
		"bdb_id":  bdbID,
		"user_id": bobID,
		"kind":    "DML",
	})
	if got := len(parseToolJSON(t, rB)["batches"].([]any)); got != 2 {
		t.Errorf("user_id=bob batches = %d, want 2", got)
	}

	// Empty user_id string → unfiltered (4 DML batches). Pins that "" is
	// treated as "not set", consistent with REST's missing-param case.
	rEmpty := callTool(t, alice, "list_changes", map[string]any{
		"bdb_id":  bdbID,
		"user_id": "",
		"kind":    "DML",
	})
	if got := len(parseToolJSON(t, rEmpty)["batches"].([]any)); got != 4 {
		t.Errorf("user_id='' batches = %d, want 4 (empty = unfiltered)", got)
	}
}

func TestMCP_ListChanges_FilterByUser_InvalidUUID(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY)",
	})

	r := callTool(t, c, "list_changes", map[string]any{
		"bdb_id":  bdbID,
		"user_id": "not-a-uuid",
	})
	if !r.IsError {
		t.Error("expected malformed user_id to error (must not silently fall back to unfiltered)")
	}
}

// --- MCP undo tests ---

// TestMCP_ExecuteData_BlobValueIsRefusedClearly pins that over MCP a BLOB write is refused with the domain's own
// message, not SQLite's trigger wording.
func TestMCP_ExecuteData_BlobValueIsRefusedClearly(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID, "sql": "CREATE TABLE b (id INTEGER PRIMARY KEY, data BLOB)",
	})
	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": "INSERT INTO b (data) VALUES (x'00ff')",
	})
	if !result.IsError {
		t.Fatalf("execute_data with a BLOB must be refused, got %v", result.Content)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "BLOB values are not supported") || strings.Contains(text, "JSON cannot hold") {
		t.Errorf("error text = %q, want the domain message without SQLite's trigger wording", text)
	}
}

func TestMCP_UndoLastChange(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})

	result := callTool(t, c, "undo_last_change", map[string]any{
		"bdb_id": bdbID,
	})
	if result.IsError {
		t.Fatalf("undo_last_mutation failed: %v", result.Content)
	}

	// Verifies the row is gone.
	countResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM items",
	})
	countData := parseToolJSON(t, countResult)
	countRows := countData["rows"].([]any)
	countVal := countRows[0].([]any)[0].(map[string]any)["value"].(float64)
	if countVal != 0 {
		t.Errorf("expected 0 rows after undo, got %v", countVal)
	}
}

// TestMCP_UndoLastChange_InsertRefusedAfterForeignUpdate pins over MCP that undoing an INSERT is refused once another
// user changed the row, with a hint naming the blocking entry and the revert_change call that reverts it, and that
// the refusal leaves their edit in place.
func TestMCP_UndoLastChange_InsertRefusedAfterForeignUpdate(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()
	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID, "sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, alice, "execute_data",
		map[string]any{"bdb_id": bdbID, "sql": "INSERT INTO items (name) VALUES ('milk')"})
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": "UPDATE items SET name = 'cheese' WHERE id = 1",
	})

	result := callTool(t, alice, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if !result.IsError {
		t.Fatalf("undo_last_change must be refused while Bob's edit is on the row, got %v", result.Content)
	}
	conflict := parseToolJSON(t, result)
	if reason, _ := conflict["reason"].(string); !strings.Contains(reason, "has changed") {
		t.Errorf("reason = %q, want the changed column named", reason)
	}
	blockers, _ := conflict["blocked_by"].([]any)
	if len(blockers) != 1 {
		t.Fatalf("blocked_by = %v, want exactly Bob's update", conflict["blocked_by"])
	}
	blocker := blockers[0].(map[string]any)
	if blocker["changed_by"] != bobID || blocker["operation"] != "UPDATE" {
		t.Errorf("blocker = %v, want Bob's UPDATE", blocker)
	}
	changedAt, err := time.Parse(time.RFC3339Nano, blocker["changed_at"].(string))
	if err != nil || changedAt.IsZero() || time.Since(changedAt) > time.Minute {
		t.Errorf("blocker changed_at = %v, want the moment Bob's update ran (%v)", blocker["changed_at"], err)
	}
	hints, _ := conflict["resolution_hints"].([]any)
	wantHints := []any{
		"The row changed after this insert. Revert the newer change to the row first, or leave this change in place.",
		fmt.Sprintf("Blocked by entry %[1]v: `revert_change(change_log_id=\"%[1]v\")` reverts it.", blocker["id"]),
	}
	if !slices.Equal(hints, wantHints) {
		t.Errorf("resolution_hints = %q, want %q", hints, wantHints)
	}

	countResult := callTool(t, alice, "execute_query", map[string]any{
		"bdb_id": bdbID, "sql": "SELECT name FROM items WHERE id = 1",
	})
	rows := parseToolJSON(t, countResult)["rows"].([]any)
	if name := rows[0].([]any)[0].(map[string]any)["value"]; name != "cheese" {
		t.Errorf("name = %v, want cheese (a refused undo must leave Bob's edit in place)", name)
	}
}

// TestMCP_RevertChange_ConflictPayload pins that a refused revert_change carries the structured conflict: the reason,
// the later update as blocker, and the hints, rather than a flattened sentence.
func TestMCP_RevertChange_ConflictPayload(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID, "sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, c, "execute_data", map[string]any{"bdb_id": bdbID, "sql": "INSERT INTO items (name) VALUES ('milk')"})
	first := parseToolJSON(t, callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": "UPDATE items SET name = 'bread' WHERE id = 1",
	}))
	callTool(t, c, "execute_data",
		map[string]any{"bdb_id": bdbID, "sql": "UPDATE items SET name = 'eggs' WHERE id = 1"})

	result := callTool(t, c, "revert_change", map[string]any{"bdb_id": bdbID, "batch_id": first["batch_id"]})
	if !result.IsError {
		t.Fatalf("revert_change of the first update must be refused, got %v", result.Content)
	}
	conflict := parseToolJSON(t, result)
	if conflict["error"] == nil || conflict["change_log_id"] == nil || conflict["kind"] != "DML" {
		t.Errorf("conflict = %v, want the identity of the refused entry", conflict)
	}
	blockers, _ := conflict["blocked_by"].([]any)
	if len(blockers) != 1 || blockers[0].(map[string]any)["operation"] != "UPDATE" {
		t.Errorf("blocked_by = %v, want the second update", conflict["blocked_by"])
	}
	if hints, _ := conflict["resolution_hints"].([]any); len(hints) == 0 {
		t.Error("resolution_hints empty, want a way out")
	}
}

func TestMCP_UndoLastChange_NothingToUndo(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// Touch the database without pushing anything to the undo stack —
	// list_mutations resolves the database without writing.
	callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID,
	})

	result := callTool(t, c, "undo_last_change", map[string]any{
		"bdb_id": bdbID,
	})
	if !result.IsError {
		t.Error("expected error for nothing to undo")
	}
}

// --- MCP redo tests ---

func TestMCP_RedoLastChange(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})

	callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID})

	result := callTool(t, c, "redo_last_change", map[string]any{"bdb_id": bdbID})
	if result.IsError {
		t.Fatalf("redo_last_mutation failed: %v", result.Content)
	}

	countResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM items",
	})
	countData := parseToolJSON(t, countResult)
	countVal := countData["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if countVal != 1 {
		t.Errorf("expected 1 row after redo, got %v", countVal)
	}
}

func TestMCP_RedoLastChange_NothingToRedo(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY)",
	})

	result := callTool(t, c, "redo_last_change", map[string]any{"bdb_id": bdbID})
	if !result.IsError {
		t.Error("expected error for nothing to redo")
	}
}

// --- MCP revert tests ---

func TestMCP_RevertChange_Insert(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})

	// Gets the mutation log ID.
	clResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT id FROM _change_log WHERE kind = 'DML' AND operation = 'INSERT' LIMIT 1",
	})
	clData := parseToolJSON(t, clResult)
	clRows := clData["rows"].([]any)
	// JSON round-trip converts int64 to float64.
	mutationLogIDFloat := clRows[0].([]any)[0].(map[string]any)["value"].(float64)
	mutationLogID := strconv.FormatInt(int64(mutationLogIDFloat), 10)

	// Reverts via MCP tool.
	revertResult := callTool(t, c, "revert_change", map[string]any{
		"bdb_id":        bdbID,
		"change_log_id": mutationLogID,
	})
	if revertResult.IsError {
		t.Fatalf("revert_mutation failed: %v", revertResult.Content)
	}

	// Verifies the row is gone.
	countResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM items",
	})
	countData := parseToolJSON(t, countResult)
	countRows := countData["rows"].([]any)
	count := countRows[0].([]any)[0].(map[string]any)["value"].(float64)
	if count != 0 {
		t.Errorf("expected 0 rows after revert, got %v", count)
	}
}

func TestMCP_RevertChange_NotFound(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// Ensures database exists.
	callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT 1",
	})

	result := callTool(t, c, "revert_change", map[string]any{
		"bdb_id":        bdbID,
		"change_log_id": "99999",
	})
	if !result.IsError {
		t.Error("expected error for non-existent mutation log ID")
	}
}

// --- MCP revert batch tests ---

func TestMCP_RevertChange_ByBatchID(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	mutResult := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('milk')",
	})
	mutData := parseToolJSON(t, mutResult)
	batchID := mutData["batch_id"].(string)

	result := callTool(t, c, "revert_change", map[string]any{
		"bdb_id":   bdbID,
		"batch_id": batchID,
	})
	if result.IsError {
		t.Fatalf("revert_mutation by batch_id failed: %v", result.Content)
	}

	countResult := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM items",
	})
	countData := parseToolJSON(t, countResult)
	countVal := countData["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64)
	if countVal != 0 {
		t.Errorf("expected 0 rows after batch revert, got %v", countVal)
	}
}

func TestMCP_RevertChange_RequiresOneOf(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY)",
	})

	result := callTool(t, c, "revert_change", map[string]any{
		"bdb_id": bdbID,
	})
	if !result.IsError {
		t.Error("expected error when neither mutation_log_id nor batch_id is provided")
	}
}

// --- MCP isolation test ---

func TestMCP_DatabaseIsolation(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdb1 := uuid.New().String()
	bdb2 := uuid.New().String()
	c := setupMCPClient(t, userID)

	// Bdb 1 creates a table and inserts data.
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdb1,
		"sql":    "CREATE TABLE private_data (id INTEGER PRIMARY KEY, secret TEXT)",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdb1,
		"sql":    "INSERT INTO private_data VALUES (1, 'bdb1-only')",
	})

	// Bdb 1 can see its data.
	u1Result := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdb1,
		"sql":    "SELECT * FROM private_data",
	})
	u1Data := parseToolJSON(t, u1Result)
	u1Rows := u1Data["rows"].([]any)
	if len(u1Rows) != 1 {
		t.Fatalf("bdb1 should have 1 row, got %d", len(u1Rows))
	}

	// Bdb 2 should have no tables — querying sqlite_master via execute_query
	// (list_tables tool was removed in step 8f; per-bdb table listing now
	// happens via list_bdbs / describe_bdb in the bdbs MCP module).
	// Filter mirrors QueryService.ListTables: excludes platform tables (`_*`)
	// and SQLite internals (`sqlite_*` like sqlite_sequence).
	u2Result := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdb2,
		"sql": "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' " +
			"AND name NOT LIKE '\\_%' ESCAPE '\\'",
	})
	u2Data := parseToolJSON(t, u2Result)
	u2Rows := u2Data["rows"].([]any)
	if len(u2Rows) != 0 {
		t.Errorf("bdb2 should have 0 tables, got %d", len(u2Rows))
	}
}

// --- per-user undo isolation tests (plan #3, step 3) ---

// rowCount returns the number of rows in the given table on the given bdb,
// via execute_query through MCP.
func rowCount(t *testing.T, c *mcpclient.Client, bdbID, table string) int {
	t.Helper()
	r := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT COUNT(*) FROM " + table,
	})
	if r.IsError {
		t.Fatalf("counting rows on %s: %v", table, r.Content)
	}
	data := parseToolJSON(t, r)
	rows := data["rows"].([]any)
	v := rows[0].([]any)[0].(map[string]any)["value"].(float64)
	return int(v)
}

func TestMCP_UndoLastChange_PerUser_Isolation(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	// Both clients share the same test backing (via t key) so they hit the
	// same per-bdb SQLite file.
	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('alice-row')",
	})
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('bob-row')",
	})
	// Checks count before undoing
	if got := rowCount(t, bob, bdbID, "items"); got != 2 {
		t.Errorf("before bob undo, items count = %d, want 2 (alice and bob rows)", got)
	}

	// Bob undoes — only his own row should disappear.
	r := callTool(t, bob, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if r.IsError {
		t.Fatalf("bob undo_last_mutation failed: %v", r.Content)
	}

	if got := rowCount(t, alice, bdbID, "items"); got != 1 {
		t.Errorf("after bob undo, items count = %d, want 1 (alice-row only)", got)
	}

	queryRes := callTool(t, alice, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT name FROM items",
	})
	data := parseToolJSON(t, queryRes)
	rows := data["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	name := rows[0].([]any)[0].(map[string]any)["value"].(string)
	if name != "alice-row" {
		t.Errorf("remaining row = %q, want alice-row", name)
	}

	// Bob's stack should now be empty; a second undo errors out for him.
	r2 := callTool(t, bob, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if !r2.IsError {
		t.Error("expected bob's second undo to fail (his stack is empty)")
	}

	// Alice's stack still has her one entry — she can undo her own row.
	r3 := callTool(t, alice, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if r3.IsError {
		t.Fatalf("alice undo_last_mutation failed: %v", r3.Content)
	}
	if got := rowCount(t, alice, bdbID, "items"); got != 0 {
		t.Errorf("after alice undo, items count = %d, want 0", got)
	}
}

func TestMCP_UndoLastChange_NothingToUndoForUser(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('alice-row')",
	})

	// Bob has no mutations on his stack — undo errors out.
	r := callTool(t, bob, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if !r.IsError {
		t.Error("expected error: bob has nothing to undo")
	}

	// Alice's row is untouched.
	if got := rowCount(t, alice, bdbID, "items"); got != 1 {
		t.Errorf("after bob's failed undo, items count = %d, want 1", got)
	}
}

func TestMCP_UndoLastChange_MultiStatementRequest(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	// FK chain: authors -> books -> order_items.
	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": `CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT);
		        CREATE TABLE books (id INTEGER PRIMARY KEY, author_id INTEGER REFERENCES authors(id), title TEXT);
		        CREATE TABLE order_items (id INTEGER PRIMARY KEY, book_id INTEGER REFERENCES books(id), qty INTEGER)`,
	})

	// Alice runs a 3-statement DML request.
	// One shared batch_id → one push to the undo stack.
	mutRes := callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": `INSERT INTO authors (name) VALUES ('Author');
		        INSERT INTO books (author_id, title) VALUES (1, 'Title');
		        INSERT INTO order_items (book_id, qty) VALUES (1, 2)`,
	})
	if mutRes.IsError {
		t.Fatalf("alice multi-stmt mutation failed: %v", mutRes.Content)
	}

	// Bob has nothing — undo errors out.
	rb := callTool(t, bob, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if !rb.IsError {
		t.Error("expected bob's undo to fail (no mutations on his stack)")
	}

	// Alice's undo reverts all three INSERTs atomically (one batch).
	ra := callTool(t, alice, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if ra.IsError {
		t.Fatalf("alice undo_last_mutation failed: %v", ra.Content)
	}

	for _, table := range []string{"authors", "books", "order_items"} {
		if got := rowCount(t, alice, bdbID, table); got != 0 {
			t.Errorf("after alice undo, %s count = %d, want 0", table, got)
		}
	}
}

// --- per-user redo invalidation tests (plan #3, step 4) ---

func TestMCP_RedoLastChange_SurvivesOtherUserChange(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	// Alice inserts at id=10 and undoes — her redo stack now has 1 entry.
	// Explicit non-overlapping ids ensure Alice's redo re-inserts at a free
	// rowid (the test is about redo-survival, not conflict).
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (10, 'alice-row')",
	})
	if r := callTool(t, alice, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("alice undo failed: %v", r.Content)
	}

	// Bob inserts a row at a different id. Under the old global redo-clear
	// this would wipe Alice's redo stack; under per-user invalidation, it
	// survives.
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (20, 'bob-row')",
	})

	// Alice's redo should succeed and re-insert her row at id=10.
	r := callTool(t, alice, "redo_last_change", map[string]any{"bdb_id": bdbID})
	if r.IsError {
		t.Fatalf("alice redo failed (her redo should survive bob's mutation): %v", r.Content)
	}

	if got := rowCount(t, alice, bdbID, "items"); got != 2 {
		t.Errorf("after alice redo, items count = %d, want 2 (alice-row + bob-row)", got)
	}
}

func TestMCP_RedoLastChange_ConflictAtRedoTime(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	// Alice inserts a row at id=1, then undoes (row gone, redo has the
	// undo-batch — replaying it re-inserts at id=1).
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (1, 'alice-row')",
	})
	if r := callTool(t, alice, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("alice undo failed: %v", r.Content)
	}

	// Bob re-inserts the same primary key. Alice's redo would now collide.
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (1, 'bob-row')",
	})

	// Alice's redo should be rejected by the conflict detector
	// (rowid 1 was the deleted row; a row at rowid 1 now exists again,
	// so the DELETE-revert path sees a "row already exists" conflict).
	r := callTool(t, alice, "redo_last_change", map[string]any{"bdb_id": bdbID})
	if !r.IsError {
		t.Fatal("expected conflict on alice redo (bob re-inserted the same primary key)")
	}
	conflict := parseToolJSON(t, r)
	if reason, _ := conflict["reason"].(string); !strings.Contains(reason, "already exists") {
		t.Errorf("reason = %q, want the re-inserted row named", reason)
	}
	blockers, _ := conflict["blocked_by"].([]any)
	if len(blockers) != 1 || blockers[0].(map[string]any)["changed_by"] != bobID {
		t.Errorf("blocked_by = %v, want Bob's insert", conflict["blocked_by"])
	}
	if hints, _ := conflict["resolution_hints"].([]any); len(hints) == 0 {
		t.Error("resolution_hints empty, want a way out")
	}
}

func TestMCP_RedoLastChange_ClearedByOwnChange(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bdbID := uuid.New().String()
	alice := setupMCPClient(t, aliceID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('A')",
	})
	if r := callTool(t, alice, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("alice undo failed: %v", r.Content)
	}

	// Alice's own next mutation invalidates her redo entry.
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (name) VALUES ('B')",
	})

	// Alice's redo should now error (her redo stack is empty).
	r := callTool(t, alice, "redo_last_change", map[string]any{"bdb_id": bdbID})
	if !r.IsError {
		t.Error("expected alice redo to fail (her own mutation cleared the redo entry)")
	}
}

func TestMCP_RedoLastChange_NotClearedByOtherUserChange(t *testing.T) {
	// Mirror of ClearedByOwnMutation but with Bob doing the intervening
	// mutation: Alice's redo entry must remain available.
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	// Explicit non-overlapping ids so the redo can re-insert at rowid 10
	// without colliding with bob's row at 20.
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (10, 'A')",
	})
	if r := callTool(t, alice, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("alice undo failed: %v", r.Content)
	}

	// Bob's mutation must NOT clear Alice's redo.
	callTool(t, bob, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (20, 'B')",
	})

	// Alice's redo is still available.
	r := callTool(t, alice, "redo_last_change", map[string]any{"bdb_id": bdbID})
	if r.IsError {
		t.Fatalf("alice redo should still work after bob's mutation: %v", r.Content)
	}
}

// --- cross-cutting tests (plan #3, step 9) ---

// backingSQLiteClient returns the test's shared sqliteClient (created by the
// first setupRouter / setupMCPClient call on this t). Used by tests that need
// to bypass MCP/REST and write to platform tables (_*) directly.
func backingSQLiteClient(t *testing.T) *sqlitelocal.Client {
	t.Helper()
	v, ok := stackBackings.Load(t)
	if !ok {
		t.Fatal("no test backing; call setupMCPClient / setupRouter first")
	}
	return v.(*stackTestBacking).sqliteClient
}

// databaseIDForBdb resolves the database ID (database UUID) for the
// given bdb via the postgres database repo.
func databaseIDForBdb(t *testing.T, bdbID string) string {
	t.Helper()
	dbRepo := newDatabaseRepo()
	cid, err := uuid.Parse(bdbID)
	if err != nil {
		t.Fatalf("parsing bdb ID: %v", err)
	}
	db, err := dbRepo.FindByBdb(context.Background(), cid)
	if err != nil {
		t.Fatalf("resolving database for bdb %s: %v", bdbID, err)
	}
	return db.ID.String()
}

// firstMutationLogID returns the mutation_log_id of the first entry visible
// to the calling user via list_mutations (i.e. the most recent batch's first
// entry).
func firstMutationLogID(t *testing.T, c *mcpclient.Client, bdbID string) string {
	t.Helper()
	r := callTool(t, c, "list_changes", map[string]any{"bdb_id": bdbID})
	if r.IsError {
		t.Fatalf("list_mutations failed: %v", r.Content)
	}
	data := parseToolJSON(t, r)
	batches := data["batches"].([]any)
	if len(batches) == 0 {
		t.Fatal("list_mutations returned no batches")
	}
	entries := batches[0].(map[string]any)["entries"].([]any)
	idFloat := entries[0].(map[string]any)["id"].(float64)
	return strconv.FormatInt(int64(idFloat), 10)
}

func TestMCP_RevertChange_StillGlobal_ByID(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (1, 'alice-row')",
	})

	// Bob reverts alice's mutation by mutation_log_id. revert is global.
	aliceMutationID := firstMutationLogID(t, alice, bdbID)
	rRevert := callTool(t, bob, "revert_change", map[string]any{
		"bdb_id":        bdbID,
		"change_log_id": aliceMutationID,
	})
	if rRevert.IsError {
		t.Fatalf("bob revert_mutation by id failed: %v", rRevert.Content)
	}

	// Alice's row is gone (bob successfully reverted it).
	if got := rowCount(t, alice, bdbID, "items"); got != 0 {
		t.Errorf("after bob revert, items count = %d, want 0", got)
	}

	// Bob's undo stack now has 1 entry (his revert). He can undo it,
	// restoring alice's row. Alice's stack is still untouched.
	rBobUndo := callTool(t, bob, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if rBobUndo.IsError {
		t.Fatalf("bob undo of his revert failed: %v", rBobUndo.Content)
	}
	if got := rowCount(t, alice, bdbID, "items"); got != 1 {
		t.Errorf("after bob undoes his revert, items count = %d, want 1 (alice-row restored)", got)
	}

	// Alice's undo stack still has her original INSERT — she can undo it.
	rAliceUndo := callTool(t, alice, "undo_last_change", map[string]any{"bdb_id": bdbID})
	if rAliceUndo.IsError {
		t.Fatalf("alice undo of her own insert failed: %v", rAliceUndo.Content)
	}
}

func TestMCP_RevertChange_StillGlobal_ByBatchID(t *testing.T) {
	truncateAll(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	alice := setupMCPClient(t, aliceID)
	bob := setupMCPClient(t, bobID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})
	rMut := callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (1, 'alice-row')",
	})
	aliceBatchID := parseToolJSON(t, rMut)["batch_id"].(string)

	// Bob reverts alice's whole batch by batch_id.
	rRevert := callTool(t, bob, "revert_change", map[string]any{
		"bdb_id":   bdbID,
		"batch_id": aliceBatchID,
	})
	if rRevert.IsError {
		t.Fatalf("bob revert_mutation by batch_id failed: %v", rRevert.Content)
	}
	if got := rowCount(t, alice, bdbID, "items"); got != 0 {
		t.Errorf("after bob batch-revert, items count = %d, want 0", got)
	}
}

// (Removed: TestMCP_DDLClearsAllUserStacks — DDL no longer clears
// stacks under the unified change-log invariant. The flipped behavior is
// exercised by the dedicated DDL-undo integration tests.)

func TestMCP_StackOrphanPrune_AfterRetention(t *testing.T) {
	// Pins that the global orphan prune still wipes stack entries whose
	// mutation_log batches were removed by retention, under the per-user
	// stack model. Simulates retention by deleting _change_log rows
	// directly via the backing sqliteClient (no API surface for that).
	truncateAll(t)
	aliceID := uuid.New().String()
	bdbID := uuid.New().String()
	alice := setupMCPClient(t, aliceID)

	callTool(t, alice, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)",
	})

	// First mutation — this batch will get orphaned.
	rMut1 := callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (1, 'orphan-target')",
	})
	orphanBatchID := parseToolJSON(t, rMut1)["batch_id"].(string)

	// Simulates retention pruning the orphan's change_log rows.
	client := backingSQLiteClient(t)
	dbID := databaseIDForBdb(t, bdbID)
	if _, err := client.ExecuteInternal(context.Background(), dbID,
		"DELETE FROM _change_log WHERE batch_id = ?",
		[]domain.TypedValue{domain.TextValue(orphanBatchID)}); err != nil {
		t.Fatalf("manual change_log cleanup: %v", err)
	}

	// A second mutation triggers cleanupStack, which runs the orphan prune.
	callTool(t, alice, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO items (id, name) VALUES (2, 'survivor')",
	})

	// The orphan prune should have wiped the first batch from _undo_stack.
	// Verify by counting entries pointing at the orphan batch_id.
	rCount := callTool(t, alice, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    fmt.Sprintf("SELECT COUNT(*) FROM _undo_stack WHERE batch_id = '%s'", orphanBatchID),
	})
	if rCount.IsError {
		t.Fatalf("count undo stack entries: %v", rCount.Content)
	}
	got := int(parseToolJSON(t, rCount)["rows"].([]any)[0].([]any)[0].(map[string]any)["value"].(float64))
	if got != 0 {
		t.Errorf("orphan _undo_stack entries for batch %s: got %d, want 0 (should be pruned)", orphanBatchID, got)
	}
}

// TestMCP_ReadOnly_RejectsWriteRetryable verifies the agent-facing surface: under read_only, an MCP write tool
// returns an error result whose text reads as retryable (so an LLM knows to back off, not give up), while reads
// still succeed.
func TestMCP_ReadOnly_RejectsWriteRetryable(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	// Provision a table while writable.
	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
	}); r.IsError {
		t.Fatalf("create table: %v", r.Content)
	}

	cid, err := uuid.Parse(bdbID)
	if err != nil {
		t.Fatalf("parsing bdb id: %v", err)
	}
	setReadOnly(t, cid, true)

	// A write tool now errors with retryable text.
	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO t (v) VALUES ('x')",
	})
	if !result.IsError {
		t.Fatalf("expected an error result under read_only, got success: %v", result.Content)
	}
	text := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "read-only") || !strings.Contains(strings.ToLower(text), "retry") {
		t.Errorf("tool error text = %q, want it to mention read-only and retry (so an agent backs off)", text)
	}

	// Reads still work over MCP.
	if r := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT count(*) FROM t",
	}); r.IsError {
		t.Errorf("read under read_only should succeed over MCP, got: %v", r.Content)
	}
}
