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

// TestMCP_Results_KeepHTMLCharacters checks that update_bdb, list_bdbs and describe_bdb answer with <, > and & as
// the bdb's name and description hold them, not as unicode escape sequences.
func TestMCP_Results_KeepHTMLCharacters(t *testing.T) {
	truncateAll(t)
	userID := uuid.New()
	c, bdbSvc := setupMCPClientWithDBApp(t, userID.String(), &mockDatabasesApp{})
	bdb, err := bdbSvc.CreateBdb(context.Background(), userID, []authz.Role{authz.RoleUser}, "Old name", "")
	if err != nil {
		t.Fatalf("creating bdb: %v", err)
	}
	const name, description = "R&D notes", "Ideas > plans, <drafts> & notes"
	// A backslash followed by "u00" starts the escape sequence encoding/json writes for these characters by default.
	escapePrefix := string(rune(92)) + "u00"

	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"update_bdb", map[string]any{"bdb_id": bdb.ID.String(), "name": name, "description": description}},
		{"list_bdbs", nil},
		{"describe_bdb", map[string]any{"bdb_id": bdb.ID.String()}},
	} {
		result := callTool(t, c, call.tool, call.args)
		if result.IsError || len(result.Content) == 0 {
			t.Fatalf("%s = %v, want a text answer", call.tool, result.Content)
		}
		text, ok := result.Content[0].(mcp.TextContent)
		if !ok {
			t.Fatalf("%s: expected TextContent, got %T", call.tool, result.Content[0])
		}
		if !strings.Contains(text.Text, name) || !strings.Contains(text.Text, description) ||
			strings.Contains(text.Text, escapePrefix) {
			t.Errorf("%s result = %s, want it to carry %q and %q as they are", call.tool, text.Text, name, description)
		}
	}
}
