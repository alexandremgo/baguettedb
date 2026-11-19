//go:build integration

package integrationtest

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// notesSchema creates the notes table the text function tests edit.
const notesSchema = "CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"

// planNote is the body of note 1: several lines, with a word ("it") that occurs more than once.
const planNote = "# Plan\n\n## Goal\nShip it.\n\n## Risks\nNone yet, but watch it.\n"

// textNotFound is the end of the message a text function raises when a match string is absent.
const textNotFound = ": it must match the text exactly, whitespace and line breaks included"

// seedNotes creates the notes table in bdbID with planNote as note 1 and a short note 2.
func seedNotes(t *testing.T, c *mcpclient.Client, bdbID string) {
	t.Helper()
	if r := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": notesSchema}); r.IsError {
		t.Fatalf("create notes: %v", r.Content)
	}
	if r := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": "INSERT INTO notes (id, body) VALUES (1, '# Plan' || char(10, 10) || '## Goal' || char(10) || " +
			"'Ship it.' || char(10, 10) || '## Risks' || char(10) || 'None yet, but watch it.' || char(10)), " +
			"(2, 'second note')",
	}); r.IsError {
		t.Fatalf("seed notes: %v", r.Content)
	}
}

// noteBody returns the body of the note id, read through execute_query.
func noteBody(t *testing.T, c *mcpclient.Client, bdbID string, id int) string {
	t.Helper()
	result := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID, "sql": "SELECT body FROM notes WHERE id = " + strconv.Itoa(id),
	})
	if result.IsError {
		t.Fatalf("read note %d: %v", id, result.Content)
	}
	rows, _ := parseToolJSON(t, result)["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("read note %d: %d rows, want 1", id, len(rows))
	}
	body, _ := rows[0].([]any)[0].(map[string]any)["value"].(string)
	return body
}

// TestMCP_TextFunctions_EditInPlaceAndUndo checks that execute_data edits a long text in place with text_patch and
// text_replace_between, each as one logged row change, and that undoing them restores the previous text.
func TestMCP_TextFunctions_EditInPlaceAndUndo(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	seedNotes(t, c, bdbID)
	if got := noteBody(t, c, bdbID, 1); got != planNote {
		t.Fatalf("seeded note = %q, want %q", got, planNote)
	}

	result := callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "UPDATE notes SET body = text_patch(body, 'Ship it.', 'Ship it on Friday.') WHERE id = 1",
	})
	if result.IsError {
		t.Fatalf("text_patch: %v", result.Content)
	}
	affected, _ := parseToolJSON(t, result)["affected_rows"].([]any)
	if len(affected) != 1 || affected[0].(map[string]any)["operation"] != "UPDATE" {
		t.Errorf("affected_rows = %v, want one UPDATE", affected)
	}
	patched := "# Plan\n\n## Goal\nShip it on Friday.\n\n## Risks\nNone yet, but watch it.\n"
	if got := noteBody(t, c, bdbID, 1); got != patched {
		t.Errorf("note after text_patch = %q, want %q", got, patched)
	}

	result = callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": "UPDATE notes SET body = text_replace_between(body, '## Risks', 'watch it.', " +
			"'## Risks' || char(10) || 'Weather.') WHERE id = 1",
	})
	if result.IsError {
		t.Fatalf("text_replace_between: %v", result.Content)
	}
	replaced := "# Plan\n\n## Goal\nShip it on Friday.\n\n## Risks\nWeather.\n"
	if got := noteBody(t, c, bdbID, 1); got != replaced {
		t.Errorf("note after text_replace_between = %q, want %q", got, replaced)
	}

	for _, want := range []string{patched, planNote} {
		if r := callTool(t, c, "undo_last_change", map[string]any{"bdb_id": bdbID}); r.IsError {
			t.Fatalf("undo: %v", r.Content)
		}
		if got := noteBody(t, c, bdbID, 1); got != want {
			t.Errorf("note after undo = %q, want %q", got, want)
		}
	}
}

// TestMCP_TextFunctions_FailuresReadPlainlyAndRollBack checks that a text function failing in a read, a write or a
// schema change answers with its own message, numbered within a batch, and that a batch with one failing edit
// changes nothing.
func TestMCP_TextFunctions_FailuresReadPlainlyAndRollBack(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	seedNotes(t, c, bdbID)

	for _, tt := range []struct {
		tool, sql, want string
	}{
		{"execute_data", "UPDATE notes SET body = text_patch(body, 'Ship them.', 'x') WHERE id = 1",
			"mutation execution failed: text_patch: old not found" + textNotFound},
		{"execute_data", "UPDATE notes SET body = text_patch(body, 'it.', 'x') WHERE id = 1",
			"mutation execution failed: text_patch: old found 2 times: add surrounding text so that it matches " +
				"exactly once"},
		{"execute_data", "UPDATE notes SET body = text_patch(body, '', 'x') WHERE id = 1",
			"mutation execution failed: text_patch: old is empty"},
		{"execute_data", "UPDATE notes SET body = text_patch(body, 'second', 'third') WHERE id = 2; " +
			"UPDATE notes SET body = text_replace_between(body, '## Goal', '## Tasks', 'x') WHERE id = 1",
			"mutation execution failed: statement 2: text_replace_between: end not found after start" + textNotFound},
		{"execute_query", "SELECT text_patch(body, 'missing', 'x') FROM notes WHERE id = 2",
			"query execution failed: text_patch: old not found" + textNotFound},
		{"execute_schema", "CREATE INDEX notes_patched ON notes (text_patch(body, 'a', 'b'))",
			"schema execution failed: unsafe use of text_patch()"},
	} {
		result := callTool(t, c, tt.tool, map[string]any{"bdb_id": bdbID, "sql": tt.sql})
		if text := result.Content[0].(mcp.TextContent).Text; !result.IsError || text != tt.want {
			t.Errorf("%s %q = %q (error %v), want %q", tt.tool, tt.sql, text, result.IsError, tt.want)
		}
	}

	if got := noteBody(t, c, bdbID, 1); got != planNote {
		t.Errorf("note 1 after the failed edits = %q, want it unchanged", got)
	}
	if got := noteBody(t, c, bdbID, 2); got != "second note" {
		t.Errorf("note 2 after the failed batch = %q, want it unchanged: the batch must roll back", got)
	}
}

// TestMCP_TextFunctions_StoredInSchema checks that a view, a column default and a trigger calling a text function are
// accepted by execute_schema and then fail, naming the unsafe use, each time they are used.
func TestMCP_TextFunctions_StoredInSchema(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	seedNotes(t, c, bdbID)

	for _, tt := range []struct {
		name, ddl, tool, use, want string
	}{
		{"view", "CREATE VIEW patched AS SELECT text_patch(body, 'a', 'b') AS body FROM notes",
			"execute_query", "SELECT body FROM patched", "query execution failed: unsafe use of text_patch()"},
		{"column default",
			"CREATE TABLE drafts (id INTEGER PRIMARY KEY, body TEXT DEFAULT (text_patch('a', 'a', 'b')))",
			"execute_data", "INSERT INTO drafts DEFAULT VALUES",
			"mutation execution failed: unsafe use of text_patch()"},
		{"trigger", "CREATE TRIGGER notes_patch AFTER INSERT ON notes BEGIN " +
			"UPDATE notes SET body = text_patch(body, 'a', 'b') WHERE id = NEW.id; END",
			"execute_data", "INSERT INTO notes (body) VALUES ('a')",
			"mutation execution failed: unsafe use of text_patch()"},
	} {
		if r := callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": tt.ddl}); r.IsError {
			t.Errorf("creating the %s: %v, want it accepted", tt.name, r.Content)
			continue
		}
		result := callTool(t, c, tt.tool, map[string]any{"bdb_id": bdbID, "sql": tt.use})
		if text := result.Content[0].(mcp.TextContent).Text; !result.IsError || text != tt.want {
			t.Errorf("using the %s = %q (error %v), want %q", tt.name, text, result.IsError, tt.want)
		}
	}
}

// TestMCP_TextFunctions_FormulaAction checks that an action formula edits a text through text_patch with its match
// and replacement passed as parameters, quotes and line breaks included, and that a failing run carries the
// function's message.
func TestMCP_TextFunctions_FormulaAction(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	seedNotes(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "patch-note",
		"description": "Replaces one passage of a note.",
		"sql":         "UPDATE notes SET body = text_patch(body, :old, :new) WHERE id = :id",
		"params": []any{
			map[string]any{"name": "id", "type": "integer", "source": "prompt"},
			map[string]any{"name": "old", "type": "text", "source": "prompt"},
			map[string]any{"name": "new", "type": "text", "source": "prompt"},
		},
	})

	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "patch-note",
		"params": map[string]any{"id": 1, "old": "## Goal\nShip it.", "new": "## Goal\nIt's \"shipped\":\n- done"},
	}); result.IsError {
		t.Fatalf("run patch-note: %v", result.Content)
	}
	want := "# Plan\n\n## Goal\nIt's \"shipped\":\n- done\n\n## Risks\nNone yet, but watch it.\n"
	if got := noteBody(t, c, bdbID, 1); got != want {
		t.Errorf("note after patch-note = %q, want %q", got, want)
	}

	result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "patch-note", "params": map[string]any{"id": 1, "old": "Ship it.", "new": "x"},
	})
	wantErr := "failed to run formula: text_patch: old not found" + textNotFound
	if text := result.Content[0].(mcp.TextContent).Text; !result.IsError || text != wantErr {
		t.Errorf("run with an absent old = %q (error %v), want %q", text, result.IsError, wantErr)
	}
}

// TestREST_TextFunctions_Data checks that the data endpoint edits a text in place with text_patch, and answers a
// failing edit with a 422 carrying the function's message.
func TestREST_TextFunctions_Data(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	token := userToken(t, userID)
	for endpoint, sql := range map[string]string{
		"schema": notesSchema,
		"data":   "INSERT INTO notes (id, body) VALUES (1, 'one two three')",
	} {
		if status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, endpoint), token,
			map[string]string{"sql": sql}); status != http.StatusOK {
			t.Fatalf("POST /%s = %d %v", endpoint, status, resp)
		}
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token, map[string]string{
		"sql": "UPDATE notes SET body = text_patch(body, 'two', '2') WHERE id = 1 RETURNING body",
	})
	if status != http.StatusOK || resp["affected_row_count"] != float64(1) {
		t.Fatalf("text_patch = %d %v, want 200 and one changed row", status, resp)
	}
	if body := resp["rows"].([]any)[0].([]any)[0].(map[string]any)["value"]; body != "one 2 three" {
		t.Errorf("returned body = %v, want %q", body, "one 2 three")
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token, map[string]string{
		"sql": "UPDATE notes SET body = text_patch(body, 'four', '4') WHERE id = 1",
	})
	want := "text_patch: old not found" + textNotFound
	if status != http.StatusUnprocessableEntity || resp["error"] != want {
		t.Errorf("failing text_patch = %d %v, want 422 with %q", status, resp["error"], want)
	}
}
