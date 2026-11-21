//go:build integration

package integrationtest

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// TestMCP_Results_KeepHTMLCharacters checks, through the gateway, that the tools of both backends answer with <, >
// and & as the stored values hold them, not as unicode escape sequences: a bdb's name and description in list_bdbs,
// and a row value in execute_query.
func TestMCP_Results_KeepHTMLCharacters(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc, _ := setupGatewayMCPClient(t, userID.String())
	const name, description = "R&D notes", "Ideas > plans, <drafts> & notes"
	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, name, description)
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	bdbID := bdb.ID.String()
	// A backslash followed by "u00" starts the escape sequence encoding/json writes for these characters by default.
	escapePrefix := string(rune(92)) + "u00"

	// text returns the text of the result of calling tool, failing the test when the tool answers with an error.
	text := func(tool string, args map[string]any) string {
		t.Helper()
		result := callTool(t, c, tool, args)
		if result.IsError || len(result.Content) == 0 {
			t.Fatalf("%s = %v, want a text answer", tool, result.Content)
		}
		content, ok := result.Content[0].(mcp.TextContent)
		if !ok {
			t.Fatalf("%s: expected TextContent, got %T", tool, result.Content[0])
		}
		return content.Text
	}

	listed := text("list_bdbs", nil)
	if !strings.Contains(listed, name) || !strings.Contains(listed, description) ||
		strings.Contains(listed, escapePrefix) {
		t.Errorf("list_bdbs result = %s, want it to carry %q and %q as they are", listed, name, description)
	}

	const body = "> quote <b>R&D</b>"
	text("execute_schema", map[string]any{
		"bdb_id": bdbID, "sql": "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)",
	})
	text("execute_data", map[string]any{"bdb_id": bdbID, "sql": "INSERT INTO notes (body) VALUES ('" + body + "')"})
	read := text("execute_query", map[string]any{"bdb_id": bdbID, "sql": "SELECT body FROM notes"})
	if !strings.Contains(read, body) || strings.Contains(read, escapePrefix) {
		t.Errorf("execute_query result = %s, want it to carry %q as it is", read, body)
	}
}
