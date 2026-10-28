//go:build integration

package integrationtest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// placesSchema creates the places table the SQL error tests run against.
const placesSchema = "CREATE TABLE places (id INTEGER PRIMARY KEY, name TEXT NOT NULL, score REAL)"

// TestREST_SQLErrors_ReadPlainly checks how the SQL endpoints word refused or failing SQL: a statement of another kind
// names the endpoint that runs it (numbered within a batch), a read with two statements asks for one per request,
// invalid SQL carries SQLite's message alone, and SQL with parameters but no args asks for them, all as 400s.
func TestREST_SQLErrors_ReadPlainly(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	token := userToken(t, userID)
	if status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": placesSchema}); status != http.StatusOK {
		t.Fatalf("create table = %d %v", status, resp)
	}

	for _, tt := range []struct {
		endpoint, sql, want string
	}{
		{"query", "DELETE FROM places", "statement not allowed on this endpoint: the statement is a row change; " +
			"the request runs reads only; send it to /data"},
		{"data", "UPDATE places SET score = 0; DROP TABLE places", "statement not allowed on this endpoint: " +
			"statement 2 is a schema change; the request runs row changes only; send it to /schema"},
		{"schema", "SELECT * FROM places", "statement not allowed on this endpoint: the statement is a read; " +
			"the request runs schema changes only; send it to /query"},
		{"query", "SELECT 1; SELECT 2", "statement not allowed on this endpoint: a read takes a single statement; " +
			"got 2; send one statement per /query request"},
		{"query", "SELECT nope FROM places", "no such column: nope"},
		{"data", "UPDATE places SET score = 0; UPDATE places SET nope = 1", "statement 2: no such column: nope"},
		{"query", "SELECT * FROM places WHERE name = :name",
			"the SQL has parameters (name) but no values for them; pass them in args"},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, tt.endpoint), token,
			map[string]string{"sql": tt.sql})
		if status != http.StatusBadRequest || resp["error"] != tt.want {
			t.Errorf("POST /%s %q = %d %v; want 400 with %q", tt.endpoint, tt.sql, status, resp["error"], tt.want)
		}
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token, map[string]any{
		"sql":  "SELECT count(*) FROM places WHERE name = :name",
		"args": []map[string]any{{"type": "text", "value": "x"}},
	})
	if status != http.StatusOK {
		t.Errorf("a parameter with its value = %d %v, want 200", status, resp)
	}
}

// TestREST_Data_Returning checks that a row change with RETURNING returns its rows and the true count of changed
// rows, alongside the change log entries.
func TestREST_Data_Returning(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	token := userToken(t, userID)
	if status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": placesSchema}); status != http.StatusOK {
		t.Fatalf("create table = %d %v", status, resp)
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token, map[string]string{
		"sql": "INSERT INTO places (name, score) VALUES ('Paris', 4.5), ('Lyon', 4) RETURNING id, name",
	})
	if status != http.StatusOK {
		t.Fatalf("insert RETURNING = %d %v", status, resp)
	}
	columns, _ := resp["columns"].([]any)
	rows, _ := resp["rows"].([]any)
	affected, _ := resp["affected_rows"].([]any)
	if len(columns) != 2 || len(rows) != 2 || resp["affected_row_count"] != float64(2) ||
		resp["last_insert_row_id"] != float64(2) || len(affected) != 2 {
		t.Fatalf("insert RETURNING = %v; want 2 columns, 2 rows, a count of 2, last id 2 and 2 change log rows", resp)
	}
	if name := rows[1].([]any)[1].(map[string]any)["value"]; name != "Lyon" {
		t.Errorf("second returned row's name = %v, want Lyon", name)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token, map[string]string{
		"sql": "UPDATE places SET score = 0 RETURNING id; UPDATE places SET score = 1 RETURNING id",
	})
	errMsg, _ := resp["error"].(string)
	if status != http.StatusBadRequest || !strings.Contains(errMsg, "RETURNING is supported on one statement") {
		t.Errorf("two RETURNING statements = %d %v, want a 400 refusal", status, resp)
	}
	_, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT sum(score) FROM places"})
	if sum := resp["rows"].([]any)[0].([]any)[0].(map[string]any)["value"]; sum != 8.5 {
		t.Errorf("scores after the refused batch sum to %v, want 8.5: the batch must roll back", sum)
	}
}

// TestMCP_SQLErrors_ReadPlainly checks how the execute tools word refused or failing SQL: a statement of another kind
// names the tool that runs it (numbered within a batch), invalid SQL and a statement the engine refuses (a NOT NULL
// column left empty) carry SQLite's message alone, numbered within a batch only, and SQL with parameters points at
// formulas; and that execute_data with RETURNING returns its rows and the true count.
func TestMCP_SQLErrors_ReadPlainly(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	result := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": placesSchema})
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
