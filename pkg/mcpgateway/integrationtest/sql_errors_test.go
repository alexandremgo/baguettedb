//go:build integration

package integrationtest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
)

// gatewayPlacesSchema creates the places table the SQL error tests run against.
const gatewayPlacesSchema = "CREATE TABLE places (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score REAL)"

// TestMCP_SQLErrors_ReadPlainly checks how the execute tools word refused or failing SQL: a statement of another kind
// names the tool that runs it (numbered within a batch), invalid SQL and a statement the engine refuses (a NOT NULL
// column left empty) carry SQLite's message alone, numbered within a batch only, and SQL with parameters points at
// formulas; and that execute_data with RETURNING returns its rows and the true count.
func TestMCP_SQLErrors_ReadPlainly(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)
	result := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": gatewayPlacesSchema})
	if result.IsError {
		t.Fatalf("create table: %v", result.Content)
	}

	for _, tt := range []struct {
		tool, sql, want string
	}{
		{"execute_query", "DELETE FROM places", "query execution failed: the statement is a row change; " +
			"the request runs reads only; use execute_data for it"},
		{"execute_data", "UPDATE places SET score = 0; DROP TABLE places", "mutation execution failed: " +
			"statement 2 is a schema change; the request runs row changes only; use execute_schema for it"},
		{"execute_query", "SELECT 1; SELECT 2", "query execution failed: a read takes a single statement; got 2; " +
			"send one statement per execute_query call"},
		{"execute_query", "SELECT nope FROM places", "query execution failed: no such column: nope"},
		{"execute_data", "INSERT INTO places (name) VALUES (NULL)",
			"mutation execution failed: NOT NULL constraint failed: places.name"},
		{"execute_data", "INSERT INTO places (name) VALUES ('Lyon'); INSERT INTO places (name) VALUES (NULL)",
			"mutation execution failed: statement 2: NOT NULL constraint failed: places.name"},
		{"execute_query", "SELECT * FROM places WHERE name = :name", "query execution failed: the SQL has parameters " +
			"(name) but no values for them; this tool takes none, so write the values in the SQL, or save it as a " +
			"formula (create_formula) and run it with its parameters (run_formula)"},
	} {
		result = callTool(t, c, tt.tool, map[string]any{"bdb_id": bdbID, "sql": tt.sql})
		if text := result.Content[0].(mcp.TextContent).Text; !result.IsError || text != tt.want {
			t.Errorf("%s %q = %q (error %v), want %q", tt.tool, tt.sql, text, result.IsError, tt.want)
		}
	}

	result = callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": "INSERT INTO places (name) VALUES ('Nice') RETURNING id, name",
	})
	if result.IsError {
		t.Fatalf("execute_data RETURNING: %v", result.Content)
	}
	data := parseToolJSON(t, result)
	if rows, _ := data["rows"].([]any); len(rows) != 1 || data["affected_row_count"] != float64(1) {
		t.Errorf("execute_data RETURNING = %v, want the new row and a count of 1", data)
	}
}
