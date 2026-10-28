//go:build integration

package integrationtest

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
)

// queryCell runs a single-row, single-column execute_query and returns the cell value.
func queryCell(t *testing.T, c *mcpclient.Client, bdbID, sql string) any {
	t.Helper()
	result := callTool(t, c, "execute_query", map[string]any{"bdb_id": bdbID, "sql": sql})
	if result.IsError {
		t.Fatalf("execute_query %q: %v", sql, result.Content)
	}
	data := parseToolJSON(t, result)
	rows, _ := data["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("execute_query %q: expected 1 row, got %d", sql, len(rows))
	}
	cells, _ := rows[0].([]any)
	if len(cells) != 1 {
		t.Fatalf("execute_query %q: expected 1 column, got %d", sql, len(cells))
	}
	cell, _ := cells[0].(map[string]any)
	return cell["value"]
}

// TestMCP_JSON_Capabilities locks in the JSON1/JSONB surface the sql docs topic promises, so an
// engine bump or build-flag change that silently drops it fails here first.
func TestMCP_JSON_Capabilities(t *testing.T) {
	truncateAll(t)
	bdbID := uuid.New().String()
	c := setupMCPClient(t, uuid.New().String())

	t.Run("version floor", func(t *testing.T) {
		version, _ := queryCell(t, c, bdbID, "SELECT sqlite_version()").(string)
		parts := strings.Split(version, ".")
		if len(parts) < 2 {
			t.Fatalf("unexpected version format %q", version)
		}
		major, _ := strconv.Atoi(parts[0])
		minor, _ := strconv.Atoi(parts[1])
		if major < 3 || (major == 3 && minor < 45) {
			t.Errorf("sqlite_version() = %q, want >= 3.45 (JSON1 + JSONB + FTS5 floor)", version)
		}
	})

	tests := []struct {
		name string
		sql  string
		want any
	}{
		{name: "json round-trip", sql: `SELECT json('{"a": 1}')`, want: `{"a":1}`},
		{name: "json_extract", sql: `SELECT json_extract('{"a": 42}', '$.a')`, want: float64(42)},
		{name: "arrow operator", sql: `SELECT '{"a": 42}' ->> '$.a'`, want: float64(42)},
		{name: "json_each", sql: `SELECT COUNT(*) FROM json_each('[1, 2, 3]')`, want: float64(3)},
		{name: "json_tree", sql: `SELECT COUNT(*) FROM json_tree('{"a": {"b": 1}}') WHERE atom IS NOT NULL`,
			want: float64(1)},
		{name: "jsonb round-trip", sql: `SELECT json(jsonb_object('a', 1))`, want: `{"a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := queryCell(t, c, bdbID, tt.sql); got != tt.want {
				t.Errorf("%s = %v (%T), want %v", tt.sql, got, got, tt.want)
			}
		})
	}
}

// TestMCP_JSON_CheckConstraint pins the idiomatic JSON-column validation: a CHECK (json_valid(col))
// accepts well-formed JSON and rejects garbage at insert time.
func TestMCP_JSON_CheckConstraint(t *testing.T) {
	truncateAll(t)
	bdbID := uuid.New().String()
	c := setupMCPClient(t, uuid.New().String())

	r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE configs (id INTEGER PRIMARY KEY, data TEXT CHECK (json_valid(data)))",
	})
	if r.IsError {
		t.Fatalf("execute_schema: %v", r.Content)
	}

	r = callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": `INSERT INTO configs (data) VALUES ('{"theme": "dark"}')`,
	})
	if r.IsError {
		t.Fatalf("valid JSON insert rejected: %v", r.Content)
	}

	r = callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": `INSERT INTO configs (data) VALUES ('not json')`,
	})
	if !r.IsError {
		t.Fatal("invalid JSON insert: expected a CHECK constraint rejection")
	}
}

// TestMCP_DescribeTable_ExposesChecks pins that describe_table surfaces CHECK constraint
// expressions — PRAGMA table_info doesn't carry them, so without this a json_valid guard would be
// invisible to agents.
func TestMCP_DescribeTable_ExposesChecks(t *testing.T) {
	truncateAll(t)
	bdbID := uuid.New().String()
	c := setupMCPClient(t, uuid.New().String())

	r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TABLE configs (data TEXT CHECK (json_valid(data)), qty INTEGER, " +
			"CONSTRAINT positive_qty CHECK (qty > 0))",
	})
	if r.IsError {
		t.Fatalf("execute_schema: %v", r.Content)
	}

	result := callTool(t, c, "describe_table", map[string]any{"bdb_id": bdbID, "table_name": "configs"})
	if result.IsError {
		t.Fatalf("describe_table: %v", result.Content)
	}
	data := parseToolJSON(t, result)
	checks, _ := data["checks"].([]any)
	if len(checks) != 2 {
		t.Fatalf("checks = %v, want 2 entries", data["checks"])
	}
	if checks[0] != "json_valid(data)" || checks[1] != "qty > 0" {
		t.Errorf("checks = %v, want [json_valid(data), qty > 0]", checks)
	}
}

// TestMCP_JSON_ExpressionIndex pins that an expression index over json_extract can be created and is
// picked up by the query planner for a matching WHERE.
func TestMCP_JSON_ExpressionIndex(t *testing.T) {
	truncateAll(t)
	bdbID := uuid.New().String()
	c := setupMCPClient(t, uuid.New().String())

	for _, ddl := range []string{
		"CREATE TABLE items (id INTEGER PRIMARY KEY, data TEXT)",
		"CREATE INDEX idx_items_field ON items (json_extract(data, '$.field'))",
	} {
		r := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": ddl})
		if r.IsError {
			t.Fatalf("execute_schema %q: %v", ddl, r.Content)
		}
	}

	result := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "EXPLAIN QUERY PLAN SELECT id FROM items WHERE json_extract(data, '$.field') = 'x'",
	})
	if result.IsError {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", result.Content)
	}
	plan := fmt.Sprintf("%v", parseToolJSON(t, result))
	if !strings.Contains(plan, "idx_items_field") {
		t.Errorf("query plan does not use the expression index: %s", plan)
	}
}
