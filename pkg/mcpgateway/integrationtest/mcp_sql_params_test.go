//go:build integration

package integrationtest

import (
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestMCP_SQLParams_TextArrivesAsGiven checks, through the gateway, that execute_data and execute_query take values
// apart from the SQL in params: a text with quotes, a backslash and a line break is stored and matched unchanged, and
// a placeholder left without a value is refused with what to do.
func TestMCP_SQLParams_TextArrivesAsGiven(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c, _, _ := setupGatewayMCPClient(t, userID)
	const note = "Baker's \"levain\" log:\n" + `fed at 7, and \n stays two characters`

	result := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID, "sql": "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)",
	})
	if result.IsError {
		t.Fatalf("create notes: %v", result.Content)
	}
	result = callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO notes (id, body) VALUES (:id, :body)",
		"params": map[string]any{"id": 1, "body": note},
	})
	if result.IsError {
		t.Fatalf("insert with params: %v", result.Content)
	}

	result = callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT body FROM notes WHERE id = :id AND body = :body",
		"params": map[string]any{"id": 1, "body": note},
	})
	if result.IsError {
		t.Fatalf("read with params: %v", result.Content)
	}
	rows, _ := parseToolJSON(t, result)["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("read by the same text = %d rows, want 1", len(rows))
	}
	if got := rows[0].([]any)[0].(map[string]any)["value"]; got != note {
		t.Errorf("stored note = %q, want %q", got, note)
	}

	result = callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": "UPDATE notes SET body = :body WHERE id = :id", "params": map[string]any{"id": 1},
	})
	want := "mutation execution failed: the SQL has parameters (body) but no values for them; give each one a value " +
		"in params, keyed by its name without the colon"
	if text := result.Content[0].(mcp.TextContent).Text; !result.IsError || text != want {
		t.Errorf("update without a value = %q (error %v), want %q", text, result.IsError, want)
	}
}
