//go:build integration

package integrationtest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestMCP_GetDocs_ThroughGateway pins that the docs tool proxies through the gateway: a known topic
// returns its document text, an unknown topic errors naming the valid topics.
func TestMCP_GetDocs_ThroughGateway(t *testing.T) {
	truncateAll(t)
	c, _, _ := setupGatewayMCPClient(t, uuid.New().String())

	result := callTool(t, c, "get_docs", map[string]any{"topic": "formulas"})
	if result.IsError {
		t.Fatalf("get_docs(formulas) returned error: %v", result.Content)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("get_docs(formulas): expected TextContent, got %T", result.Content[0])
	}
	if !strings.Contains(text.Text, "# Formula authoring manual") {
		t.Errorf("get_docs(formulas) missing the manual heading")
	}

	result = callTool(t, c, "get_docs", map[string]any{"topic": "recipes"})
	if !result.IsError {
		t.Fatal("get_docs(recipes): expected an error result")
	}
	errText := fmt.Sprintf("%v", result.Content)
	if !strings.Contains(errText, "formulas") || !strings.Contains(errText, "sql") {
		t.Errorf("get_docs(recipes) error %q should name the valid topics", errText)
	}
}

// TestMCP_CreateFormula_ValidationErrorPointsToDocs_ThroughGateway pins that a formula
// authoring-validation rejection keeps its get_docs pointer across the gateway proxy.
func TestMCP_CreateFormula_ValidationErrorPointsToDocs_ThroughGateway(t *testing.T) {
	truncateAll(t)
	c, _, _ := setupGatewayMCPClient(t, uuid.New().String())
	bdbID := uuid.New().String()

	r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID, "sql": "CREATE TABLE groceries (id INTEGER PRIMARY KEY, name TEXT)",
	})
	if r.IsError {
		t.Fatalf("execute_schema: %v", r.Content)
	}

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "broken",
		"description": "SQL mixing read and write cannot derive a kind.",
		"sql":         "SELECT 1; DELETE FROM groceries",
	})
	if !result.IsError {
		t.Fatal("create_formula with a read/write mix: expected an error result")
	}
	if text := fmt.Sprintf("%v", result.Content); !strings.Contains(text, "get_docs") {
		t.Errorf("validation rejection %q should point at get_docs", text)
	}
}
