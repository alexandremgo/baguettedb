//go:build integration

package integrationtest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
)

// TestMCP_GetDocs_Topics pins that each documentation topic serves its full reference document as
// plain text: a marker section heading and a load-bearing rule must come through verbatim.
func TestMCP_GetDocs_Topics(t *testing.T) {
	truncateAll(t)
	c := setupMCPClient(t, uuid.New().String())

	tests := []struct {
		topic   string
		heading string
		rule    string
	}{
		{topic: "formulas", heading: "# Formula authoring manual", rule: "rowid AS rowid"},
		{topic: "formulas", heading: "### Calendar mode", rule: "day IS NULL"},
		{topic: "formulas", heading: "### Calendar mode", rule: "week_start"},
		{topic: "sql", heading: "# SQL capabilities and engine rules", rule: "json_valid"},
		{topic: "sql", heading: "## Change log semantics", rule: "BLOB values cannot be stored"},
		{topic: "sql", heading: "## Change log semantics", rule: "recorded as the equivalent"},
		{topic: "sql", heading: "## Change log semantics", rule: "size limit"},
	}
	for _, tt := range tests {
		t.Run(tt.topic+"/"+tt.rule, func(t *testing.T) {
			result := callTool(t, c, "get_docs", map[string]any{"topic": tt.topic})
			if result.IsError {
				t.Fatalf("get_docs(%q) returned error: %v", tt.topic, result.Content)
			}
			text, ok := result.Content[0].(mcp.TextContent)
			if !ok {
				t.Fatalf("get_docs(%q): expected TextContent, got %T", tt.topic, result.Content[0])
			}
			if !strings.Contains(text.Text, tt.heading) {
				t.Errorf("get_docs(%q) missing heading %q", tt.topic, tt.heading)
			}
			if !strings.Contains(text.Text, tt.rule) {
				t.Errorf("get_docs(%q) missing load-bearing rule %q", tt.topic, tt.rule)
			}
		})
	}
}

// TestMCP_CreateFormula_ValidationErrorPointsToDocs pins the self-healing discovery path: an
// authoring-validation rejection ends with a get_docs pointer, while a not-found error does not —
// no documentation fixes a bad reference.
func TestMCP_CreateFormula_ValidationErrorPointsToDocs(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "broken",
		"description": "SQL mixing read and write cannot derive a kind.",
		"sql":         "SELECT 1; DELETE FROM groceries",
	})
	if !result.IsError {
		t.Fatal("create_formula with a read/write mix: expected an error result")
	}
	text := fmt.Sprintf("%v", result.Content)
	if !strings.Contains(text, "get_docs") {
		t.Errorf("validation rejection %q should point at get_docs", text)
	}

	result = callTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID, "ref": "does-not-exist", "description": "x",
	})
	if !result.IsError {
		t.Fatal("update_formula on a missing ref: expected an error result")
	}
	text = fmt.Sprintf("%v", result.Content)
	if strings.Contains(text, "get_docs") {
		t.Errorf("not-found error %q should not point at get_docs", text)
	}
}

// TestMCP_GetDocs_UnknownTopic pins that an unknown or missing topic errors with a message naming
// every valid topic, so a caller that guessed wrong can self-correct without other documentation.
func TestMCP_GetDocs_UnknownTopic(t *testing.T) {
	truncateAll(t)
	c := setupMCPClient(t, uuid.New().String())

	for _, args := range []map[string]any{
		{"topic": "recipes"},
		{},
	} {
		result := callTool(t, c, "get_docs", args)
		if !result.IsError {
			t.Fatalf("get_docs(%v): expected an error result", args)
		}
		text := fmt.Sprintf("%v", result.Content)
		if !strings.Contains(text, "formulas") || !strings.Contains(text, "sql") {
			t.Errorf("get_docs(%v) error %q should name the valid topics", args, text)
		}
	}
}

// TestMCP_GetDocs_ShowsCallerLimits pins that the SQL reference quotes the caps resolved for the calling user,
// not a fixed default.
func TestMCP_GetDocs_ShowsCallerLimits(t *testing.T) {
	truncateAll(t)
	router, _ := setupStackWithOptions(t, stackOptions{resolver: rowCappedLimits(3)})
	c := startMCPClient(t, router, uuid.New().String())

	result := callTool(t, c, "get_docs", map[string]any{"topic": "sql"})
	if result.IsError {
		t.Fatalf("get_docs(sql) returned error: %v", result.Content)
	}
	doc := toolResultText(result)
	for _, want := range []string{"3 result rows", "up to 100 DML statements", "1000 affected rows"} {
		if !strings.Contains(doc, want) {
			t.Errorf("get_docs(sql) missing %q", want)
		}
	}
}
