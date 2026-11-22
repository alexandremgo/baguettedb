//go:build integration

package integrationtest

import (
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// bakersNote is a text full of what breaks when written inside SQL: an apostrophe, double quotes, a real line break,
// backslashes (one of them before an "n"), a double dash, a semicolon, a percent sign, typographic characters and
// "<", ">" and "&".
const bakersNote = "Baker's note: \"pain de campagne\" — 20 % rye; rest 12h.\n" +
	`Line two: C:\ovens\deck -- not a comment; 'quoted' <b>R&D</b>, and \n stays two characters. “Très bon” …`

// noteCount returns the number of rows of the notes table, read through execute_query.
func noteCount(t *testing.T, c *mcpclient.Client, bdbID string) float64 {
	t.Helper()
	result := callTool(t, c, "execute_query", map[string]any{"bdb_id": bdbID, "sql": "SELECT count(*) FROM notes"})
	if result.IsError {
		t.Fatalf("count notes: %v", result.Content)
	}
	rows, _ := parseToolJSON(t, result)["rows"].([]any)
	count, _ := rows[0].([]any)[0].(map[string]any)["value"].(float64)
	return count
}

// TestMCP_SQLParams_TextArrivesAsGiven checks that a text given in params reaches the engine unchanged, in a write, in
// a read and as the arguments of a text function, and that the write is one change that undo reverts.
func TestMCP_SQLParams_TextArrivesAsGiven(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	if r := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": notesSchema}); r.IsError {
		t.Fatalf("create notes: %v", r.Content)
	}

	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO notes (id, body) VALUES (:id, :body)",
		"params": map[string]any{"id": 1, "body": bakersNote},
	})
	if result.IsError {
		t.Fatalf("insert with params: %v", result.Content)
	}
	if got := noteBody(t, c, bdbID, 1); got != bakersNote {
		t.Errorf("stored note = %q, want %q", got, bakersNote)
	}

	result = callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql":    "SELECT id FROM notes WHERE body = :body",
		"params": map[string]any{"body": bakersNote},
	})
	if result.IsError {
		t.Fatalf("read with params: %v", result.Content)
	}
	if rows, _ := parseToolJSON(t, result)["rows"].([]any); len(rows) != 1 {
		t.Errorf("read by the same text = %d rows, want 1", len(rows))
	}

	result = callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "UPDATE notes SET body = text_patch(body, :old, :new) WHERE id = :id",
		"params": map[string]any{
			"id": 1, "old": "rest 12h.\nLine two: C:\\ovens", "new": "rest 14h.\nLine 2: D:\\ovens",
		},
	})
	if result.IsError {
		t.Fatalf("text_patch with params: %v", result.Content)
	}
	patched := "Baker's note: \"pain de campagne\" — 20 % rye; rest 14h.\n" +
		`Line 2: D:\ovens\deck -- not a comment; 'quoted' <b>R&D</b>, and \n stays two characters. “Très bon” …`
	if got := noteBody(t, c, bdbID, 1); got != patched {
		t.Errorf("note after text_patch = %q, want %q", got, patched)
	}

	if r := callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("undo: %v", r.Content)
	}
	if got := noteBody(t, c, bdbID, 1); got != bakersNote {
		t.Errorf("note after undo = %q, want %q", got, bakersNote)
	}
}

// TestMCP_SQLParams_BindAsJSONTypes checks the storage class each JSON type binds as: text for a string, even one
// made of digits, an integer for a whole number and for true and false, a real for a decimal, NULL for null, and JSON
// text for an array and an object.
func TestMCP_SQLParams_BindAsJSONTypes(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)

	result := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql": "SELECT typeof(:text), :text, typeof(:whole), :whole, typeof(:decimal), :decimal, typeof(:yes), :yes, " +
			":no, typeof(:nothing), :list, :object",
		"params": map[string]any{
			"text": "0042", "whole": 240, "decimal": 1.8, "yes": true, "no": false, "nothing": nil,
			"list": []any{"rye", 2}, "object": map[string]any{"tag": "<b>R&D</b>"},
		},
	})
	if result.IsError {
		t.Fatalf("read with params: %v", result.Content)
	}
	rows, _ := parseToolJSON(t, result)["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	want := []any{
		"text", "0042", "integer", float64(240), "real", 1.8, "integer", float64(1), float64(0), "null",
		`["rye",2]`, `{"tag":"<b>R&D</b>"}`,
	}
	cells, _ := rows[0].([]any)
	if len(cells) != len(want) {
		t.Fatalf("cells = %d, want %d", len(cells), len(want))
	}
	for i, cell := range cells {
		if got := cell.(map[string]any)["value"]; got != want[i] {
			t.Errorf("column %d = %v (%T), want %v", i+1, got, got, want[i])
		}
	}
}

// TestMCP_SQLParams_SharedAcrossStatements checks that the statements of one request share the set of values, each
// binding the names it uses, and that the request stays one change: a single undo reverts every statement.
func TestMCP_SQLParams_SharedAcrossStatements(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	if r := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": notesSchema}); r.IsError {
		t.Fatalf("create notes: %v", r.Content)
	}

	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": "INSERT INTO notes (id, body) VALUES (:id, :body); " +
			"UPDATE notes SET body = body || :suffix WHERE id = :id; " +
			"INSERT INTO notes (body) VALUES ('Levain fed at 7.')",
		"params": map[string]any{"id": 7, "body": "Don't slash too deep", "suffix": "; it's a soft dough."},
	})
	if result.IsError {
		t.Fatalf("batch with params: %v", result.Content)
	}
	if got, want := noteBody(t, c, bdbID, 7), "Don't slash too deep; it's a soft dough."; got != want {
		t.Errorf("note 7 = %q, want %q", got, want)
	}
	if got := noteCount(t, c, bdbID); got != 2 {
		t.Errorf("notes after the batch = %v, want 2", got)
	}

	if r := callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
		t.Fatalf("undo: %v", r.Content)
	}
	if got := noteCount(t, c, bdbID); got != 0 {
		t.Errorf("notes after undo = %v, want 0", got)
	}
}

// TestMCP_SQLParams_Refusals checks how the execute tools word params they refuse: a placeholder without a value,
// values no statement uses (named in sorted order), a placeholder without a name, params that are not an object, a
// name written with its colon; that execute_schema, which takes no values, says so; and that a refused write changes
// nothing.
func TestMCP_SQLParams_Refusals(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	seedNotes(t, c, bdbID)

	const giveValues = "; give each one a value in params, keyed by its name without the colon"
	for _, tt := range []struct {
		name, tool, sql string
		params          any
		want            string
	}{
		{"a value is missing", "execute_data", "UPDATE notes SET body = :body WHERE id = :id",
			map[string]any{"id": 2},
			"mutation execution failed: the SQL has parameters (body) but no values for them" + giveValues},
		{"no params at all", "execute_query", "SELECT body FROM notes WHERE id = :id AND body <> :body", nil,
			"query execution failed: the SQL has parameters (id, body) but no values for them" + giveValues},
		{"a missing value in a later statement", "execute_data",
			"UPDATE notes SET body = :body WHERE id = 2; DELETE FROM notes WHERE id = :id",
			map[string]any{"body": "gone"},
			"mutation execution failed: the SQL has parameters (id) but no values for them" + giveValues},
		{"values no statement uses", "execute_data",
			"UPDATE notes SET body = :body WHERE id = 2; DELETE FROM notes WHERE id = :id",
			map[string]any{"id": 1, "body": "gone", "oven": 240, "flour": "T65"},
			"mutation execution failed: values given for parameters the SQL does not have (flour, oven); use each " +
				"one in the SQL as :name, or leave it out of params"},
		{"a value for SQL without placeholders", "execute_query", "SELECT body FROM notes", map[string]any{"id": 2},
			"query execution failed: values given for parameters the SQL does not have (id); use each one in the " +
				"SQL as :name, or leave it out of params"},
		{"a placeholder without a name", "execute_query", "SELECT body FROM notes WHERE id = ?",
			map[string]any{"id": 2},
			"query execution failed: a ? placeholder has no name to take a value by; write a named one such as :name"},
		{"params that are not an object", "execute_query", "SELECT body FROM notes WHERE id = :id", "id=2",
			`params must be an object of values keyed by parameter name, e.g. {"title": "Rye loaf"}`},
		{"a name with its colon", "execute_query", "SELECT body FROM notes WHERE id = :id",
			map[string]any{":id": 2}, `params: parameter ":id": write the name without its ":", as "id"`},
		{"a schema change", "execute_schema", "CREATE TABLE loaves AS SELECT :name AS name", nil,
			"schema execution failed: the SQL has parameters (name) but no values for them; this tool takes none, " +
				"so write the values in the SQL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{"bdb_id": bdbID, "sql": tt.sql}
			if tt.params != nil {
				args["params"] = tt.params
			}
			result := callTool(t, c, tt.tool, args)
			if text := result.Content[0].(mcp.TextContent).Text; !result.IsError || text != tt.want {
				t.Errorf("%s = %q (error %v), want %q", tt.tool, text, result.IsError, tt.want)
			}
		})
	}

	if got := noteBody(t, c, bdbID, 2); got != "second note" {
		t.Errorf("note 2 after the refusals = %q, want it unchanged", got)
	}
	if got := noteCount(t, c, bdbID); got != 2 {
		t.Errorf("notes after the refusals = %v, want 2", got)
	}
}
