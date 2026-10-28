//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
)

// callFormulaTool calls an MCP formula tool and decodes the response as a single formula object
// (create_formula, update_formula, get_formula).
func callFormulaTool(t *testing.T, c *mcpclient.Client, name string, args map[string]any) map[string]any {
	t.Helper()
	result := callTool(t, c, name, args)
	if result.IsError {
		t.Fatalf("tool %s returned error: %v", name, result.Content)
	}
	if len(result.Content) == 0 {
		t.Fatalf("tool %s: empty content", name)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool %s: expected TextContent, got %T", name, result.Content[0])
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(text.Text), &data); err != nil {
		t.Fatalf("tool %s: unmarshal: %v (text=%s)", name, err, text.Text)
	}
	return data
}

// listFormulasViaMCP calls list_formulas and decodes the JSON array response.
func listFormulasViaMCP(t *testing.T, c *mcpclient.Client, bdbID string) []any {
	t.Helper()
	result := callTool(t, c, "list_formulas", map[string]any{"bdb_id": bdbID})
	if result.IsError {
		t.Fatalf("list_formulas returned error: %v", result.Content)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("list_formulas: expected TextContent, got %T", result.Content[0])
	}
	var data []any
	if err := json.Unmarshal([]byte(text.Text), &data); err != nil {
		t.Fatalf("list_formulas: unmarshal: %v (text=%s)", err, text.Text)
	}
	return data
}

// seedGroceriesMCP creates the groceries table and three rows (rowids 1,2,3) over MCP.
func seedGroceriesMCP(t *testing.T, c *mcpclient.Client, bdbID string) {
	t.Helper()
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE groceries (id INTEGER PRIMARY KEY, name TEXT, quantity INTEGER)",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO groceries (id, name, quantity) VALUES (1,'milk',2),(2,'eggs',1),(3,'bread',3)",
	})
}

// --- Discovery ---

func TestMCP_ListTools_IncludesFormulaTools(t *testing.T) {
	truncateAll(t)
	c := setupMCPClient(t, uuid.New().String())

	result, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}

	names := make(map[string]bool)
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	for _, expected := range []string{
		"create_formula", "update_formula", "get_formula", "list_formulas", "delete_formula", "run_formula",
	} {
		if !names[expected] {
			t.Errorf("expected tool %q not registered", expected)
		}
	}
}

// --- create_formula ---

func TestMCP_CreateFormula_View(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	formula := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "all-groceries",
		"description": "Every grocery row.",
		"sql":         "SELECT id, name, quantity FROM groceries ORDER BY id",
	})

	if formula["name"] != "all-groceries" {
		t.Errorf("name = %v, want all-groceries", formula["name"])
	}
	if formula["kind"] != "view" {
		t.Errorf("kind = %v, want view", formula["kind"])
	}
	if _, err := uuid.Parse(formula["id"].(string)); err != nil {
		t.Errorf("id is not a UUID: %v", formula["id"])
	}
}

func TestMCP_CreateFormula_ActionWithParams(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	formula := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "add-grocery",
		"description": "Adds a grocery item.",
		"sql":         "INSERT INTO groceries (name, quantity) VALUES (:name, :qty)",
		"params": []any{
			map[string]any{"name": "name", "type": "text", "source": "prompt"},
			map[string]any{"name": "qty", "type": "integer", "source": "prompt"},
		},
	})

	if formula["kind"] != "action" {
		t.Errorf("kind = %v, want action", formula["kind"])
	}
	params, _ := formula["params"].([]any)
	if len(params) != 2 {
		t.Fatalf("params count = %d, want 2", len(params))
	}
}

// TestMCP_CreateFormula_LabeledOptions pins the options contract over MCP: input entries mix bare
// scalars and {value, label} objects, reads return the normalized object list, and a run passes
// values only — one outside the set is rejected naming the accepted values.
func TestMCP_CreateFormula_LabeledOptions(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	formula := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "monthly",
		"description": "Groceries for a month.",
		"sql":         "SELECT id, name FROM groceries WHERE :month IS NULL OR :month > 0",
		"params": []any{
			map[string]any{
				"name": "month", "type": "integer", "source": "prompt", "optional": true,
				"options": []any{map[string]any{"value": 9, "label": "September"}, 10},
			},
		},
	})

	params, _ := formula["params"].([]any)
	if len(params) != 1 {
		t.Fatalf("params count = %d, want 1", len(params))
	}
	month, _ := params[0].(map[string]any)
	options, _ := month["options"].([]any)
	if len(options) != 2 {
		t.Fatalf("options = %v, want 2 normalized entries", month["options"])
	}
	for i, want := range []map[string]any{
		{"value": "9", "label": "September"},
		{"value": "10", "label": "10"},
	} {
		if opt, _ := options[i].(map[string]any); opt["value"] != want["value"] || opt["label"] != want["label"] {
			t.Errorf("options[%d] = %v, want %v", i, options[i], want)
		}
	}

	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "monthly", "params": map[string]any{"month": 9},
	}); result.IsError {
		t.Errorf("run with a declared value failed: %v", result.Content)
	}
	result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "monthly", "params": map[string]any{"month": 11},
	})
	if !result.IsError {
		t.Fatal("expected an error for a value outside the option set")
	}
	want := `failed to run formula: parameter "month" must be one of "9", "10", got 11`
	if text := result.Content[0].(mcp.TextContent).Text; text != want {
		t.Errorf("error = %q, want %q", text, want)
	}
}

// TestMCP_RunFormula_ConditionalRequiredness pins the two faces of a when-gated required param over
// MCP: while its condition is false the run treats it as optional (omitted binds NULL), and while
// it is true an omission is rejected naming the missing param.
func TestMCP_RunFormula_ConditionalRequiredness(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "add-line",
		"description": "Adds a grocery line; quantity only counts while the counted flag is on.",
		"sql": "INSERT INTO groceries (name, quantity) " +
			"VALUES (:name, COALESCE(:qty, CASE WHEN :counted THEN -2 ELSE -1 END))",
		"params": []any{
			map[string]any{"name": "name", "type": "text", "source": "prompt"},
			map[string]any{"name": "counted", "type": "boolean", "source": "prompt", "optional": true},
			map[string]any{"name": "qty", "type": "integer", "source": "prompt",
				"when": map[string]any{"param": "counted", "value": "true"}},
		},
	})

	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "add-line", "params": map[string]any{"name": "tea"},
	}); result.IsError {
		t.Errorf("run with the condition off failed: %v", result.Content)
	}
	result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "add-line", "params": map[string]any{"name": "salt", "counted": true},
	})
	if !result.IsError {
		t.Fatal("expected an error for the omitted param while its condition holds")
	}
	if text := result.Content[0].(mcp.TextContent).Text; !strings.Contains(text, "qty") {
		t.Errorf("error = %q, want the missing param named", text)
	}
}

func TestMCP_CreateFormula_RejectsAnonymousPlaceholder(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	// A bare '?' placeholder is rejected: formulas require named params.
	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "bad-anon",
		"description": "Uses an anonymous placeholder.",
		"sql":         "SELECT * FROM groceries WHERE quantity > ?",
	})
	if !result.IsError {
		t.Fatal("expected error for anonymous placeholder")
	}
}

func TestMCP_CreateFormula_RejectsMultiSelect(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "multi",
		"description": "Two statements.",
		"sql":         "SELECT 1; SELECT 2",
	})
	if !result.IsError {
		t.Fatal("expected error for multi-select formula")
	}
}

// TestMCP_RunFormula_MultiStatementAction runs a param-less two-statement action by name: both
// inserts land atomically and the second statement chains the first's id via last_insert_rowid().
func TestMCP_RunFormula_MultiStatementAction(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT UNIQUE); " +
			"CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, author_id INTEGER)",
	})

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "add-camus",
		"description": "Adds the author and one book keyed to it.",
		"sql": "INSERT INTO authors (name) VALUES ('Camus'); " +
			"INSERT INTO books (title, author_id) VALUES ('La Peste', last_insert_rowid())",
	})

	result := callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "add-camus"})
	if result.IsError {
		t.Fatalf("run_formula returned error: %v", result.Content)
	}
	data := parseToolJSON(t, result)
	if affected, _ := data["affected_row_count"].(float64); affected != 2 {
		t.Errorf("affected_row_count = %v, want 2 (both inserts)", data["affected_row_count"])
	}
	// Two rows across TWO statements — the count that tells callers the total mixes statements.
	if count, _ := data["statement_count"].(float64); count != 2 {
		t.Errorf("statement_count = %v, want 2", data["statement_count"])
	}
}

// TestMCP_CreateFormula_MultiStatementAction pins multi-statement authoring over MCP: several DML
// statements form one action, a param may repeat across statements (:name) or appear only in a
// later one (:qty), and the stored verb reduces across the whole list.
func TestMCP_CreateFormula_MultiStatementAction(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	formula := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "add-and-bump",
		"description": "Adds an item, then bumps its quantity.",
		"sql": "INSERT INTO groceries (name, quantity) VALUES (:name, 1); " +
			"UPDATE groceries SET quantity = quantity + :qty WHERE name = :name",
		"params": []any{
			map[string]any{"name": "name", "type": "text", "source": "prompt"},
			map[string]any{"name": "qty", "type": "integer", "source": "prompt"},
		},
	})
	if formula["kind"] != "action" {
		t.Errorf("kind = %v, want action", formula["kind"])
	}
	if formula["statement_verb"] != "insert" {
		t.Errorf("statement_verb = %v, want insert (insert > update across statements)", formula["statement_verb"])
	}
}

// --- get / list / update / delete ---

func TestMCP_GetFormula_ByNameAndUUID(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	created := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "grocery-count",
		"description": "Counts groceries.",
		"sql":         "SELECT COUNT(*) AS n FROM groceries",
	})
	id := created["id"].(string)

	byName := callFormulaTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "grocery-count"})
	if byName["id"] != id {
		t.Errorf("by-name id = %v, want %v", byName["id"], id)
	}
	byUUID := callFormulaTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": id})
	if byUUID["name"] != "grocery-count" {
		t.Errorf("by-uuid name = %v, want grocery-count", byUUID["name"])
	}
}

func TestMCP_ListFormulas_OrdersByDisplayThenName(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "view-b", "description": "B.",
		"sql": "SELECT id FROM groceries", "display_order": float64(2),
	})
	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "view-a", "description": "A.",
		"sql": "SELECT name FROM groceries", "display_order": float64(1),
	})

	formulas := listFormulasViaMCP(t, c, bdbID)
	if len(formulas) != 2 {
		t.Fatalf("list count = %d, want 2", len(formulas))
	}
	first := formulas[0].(map[string]any)
	if first["name"] != "view-a" {
		t.Errorf("first formula = %v, want view-a (lower display_order)", first["name"])
	}
}

func TestMCP_UpdateFormula_PartialDescription(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "to-edit", "description": "Original.",
		"sql": "SELECT id FROM groceries",
	})

	updated := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID, "ref": "to-edit", "description": "Updated.",
	})
	if updated["description"] != "Updated." {
		t.Errorf("description = %v, want Updated.", updated["description"])
	}
	if updated["sql"] != "SELECT id FROM groceries" {
		t.Errorf("sql = %v, want unchanged", updated["sql"])
	}
}

// TestMCP_Formula_LabelLifecycle pins the label over MCP, where absent and empty are two different
// intents read from one map entry: creating with a label returns it, updating replaces it, and an
// explicit empty string clears it back to the name — while omitting the argument leaves it alone.
func TestMCP_Formula_LabelLifecycle(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	created := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "pending-groceries", "label": "Shopping list",
		"description": "Everything still to buy.", "sql": "SELECT id FROM groceries",
	})
	if created["label"] != "Shopping list" {
		t.Errorf("created label = %v, want Shopping list", created["label"])
	}
	if created["name"] != "pending-groceries" {
		t.Errorf("created name = %v, want pending-groceries — the label is not the reference", created["name"])
	}

	renamed := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID, "ref": "pending-groceries", "label": "To buy",
	})
	if renamed["label"] != "To buy" {
		t.Errorf("updated label = %v, want To buy", renamed["label"])
	}

	// An unrelated field must not disturb the label.
	kept := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID, "ref": "pending-groceries", "description": "Still to buy.",
	})
	if kept["label"] != "To buy" {
		t.Errorf("label after an unrelated update = %v, want To buy", kept["label"])
	}

	cleared := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID, "ref": "pending-groceries", "label": "",
	})
	if _, present := cleared["label"]; present {
		t.Errorf("cleared label = %v, want the key omitted", cleared["label"])
	}
}

// TestMCP_CreateFormula_RejectsInvisibleLabel pins that the label guards apply on the MCP path too:
// a zero-width character would make the rendered label differ from the stored one.
func TestMCP_CreateFormula_RejectsInvisibleLabel(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "pending-groceries", "label": "Shopping\u200blist",
		"description": "Everything still to buy.", "sql": "SELECT id FROM groceries",
	})
	if !result.IsError {
		t.Errorf("create_formula with a zero-width label succeeded, want an error")
	}
}

func TestMCP_DeleteFormula(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "doomed", "description": "Will be deleted.",
		"sql": "SELECT id FROM groceries",
	})

	if result := callTool(t, c, "delete_formula", map[string]any{
		"bdb_id": bdbID, "ref": "doomed",
	}); result.IsError {
		t.Fatalf("delete_formula returned error: %v", result.Content)
	}

	if result := callTool(t, c, "get_formula", map[string]any{
		"bdb_id": bdbID, "ref": "doomed",
	}); !result.IsError {
		t.Fatal("expected error fetching a deleted formula")
	}
}

// --- run_formula ---

func TestMCP_RunFormula_ViewReturnsRows(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "all-rows", "description": "All groceries.",
		"sql": "SELECT id, name FROM groceries ORDER BY id",
	})

	result := callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "all-rows"})
	if result.IsError {
		t.Fatalf("run_formula returned error: %v", result.Content)
	}
	data := parseToolJSON(t, result)
	rows, _ := data["rows"].([]any)
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3", len(rows))
	}
}

// TestMCP_RunFormula_PaginatedViewDefaults runs a paginated list view without explicit cursor/limit:
// the engine applies the default page size and returns the pagination block with next_cursor read from
// the "rowid" column.
func TestMCP_RunFormula_PaginatedViewDefaults(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "paged-groceries",
		"description": "Groceries, paged.",
		"sql":         "SELECT rowid AS rowid, name FROM groceries WHERE rowid > :cursor ORDER BY rowid LIMIT :limit",
		"params": []any{
			map[string]any{"name": "cursor", "type": "integer", "source": "pagination"},
			map[string]any{"name": "limit", "type": "integer", "source": "pagination"},
		},
	})

	result := callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "paged-groceries"})
	if result.IsError {
		t.Fatalf("run_formula returned error: %v", result.Content)
	}
	data := parseToolJSON(t, result)
	rows, _ := data["rows"].([]any)
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3 (all rows fit the default page)", len(rows))
	}
	pagination, ok := data["pagination"].(map[string]any)
	if !ok {
		t.Fatalf("expected a pagination block, got %v", data["pagination"])
	}
	if pagination["has_more"] != false {
		t.Errorf("has_more = %v, want false", pagination["has_more"])
	}
	if pagination["next_cursor"] != float64(3) {
		t.Errorf("next_cursor = %v, want 3", pagination["next_cursor"])
	}
}

// TestMCP_RunFormula_ActionWithParams runs a single-row UPDATE action, supplying its :q (prompt) and
// :rowid (selection-single) values through the `params` object — exercising buildFormulaRunInput's
// scalar extraction end-to-end.
func TestMCP_RunFormula_ActionWithParams(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "set-quantity",
		"description": "Set one item's quantity.",
		"sql":         "UPDATE groceries SET quantity = :q WHERE rowid = :rowid",
		"params": []any{
			map[string]any{"name": "q", "type": "integer", "source": "prompt"},
			map[string]any{"name": "rowid", "type": "integer", "source": "selection-single"},
		},
	})

	result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "set-quantity",
		"params": map[string]any{"q": 9, "rowid": 1},
	})
	if result.IsError {
		t.Fatalf("run_formula returned error: %v", result.Content)
	}
	data := parseToolJSON(t, result)
	if affected, _ := data["affected_row_count"].(float64); affected != 1 {
		t.Errorf("affected_row_count = %v, want 1", data["affected_row_count"])
	}
}

// TestMCP_RunFormula_AttachedBulkActionViaRowIDs builds a list view that exposes rowid and an action
// attached to it that deletes the selected rows via :rowids, then runs the action with an explicit
// `rowids` array in `params` — the list-view + attached :rowids action end-to-end path over MCP.
func TestMCP_RunFormula_AttachedBulkActionViaRowIDs(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	view := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "grocery-list",
		"description": "Groceries with selectable rows.",
		"sql":         "SELECT rowid AS rowid, name FROM groceries ORDER BY rowid",
	})
	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":                 bdbID,
		"name":                   "delete-selected",
		"description":            "Delete the selected rows.",
		"sql":                    "DELETE FROM groceries WHERE rowid IN (:rowids)",
		"attached_to_formula_id": view["id"],
		"params": []any{
			map[string]any{"name": "rowids", "type": "integer", "source": "selection-bulk"},
		},
	})

	result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "delete-selected",
		"params": map[string]any{"rowids": []int{2, 3}},
	})
	if result.IsError {
		t.Fatalf("run_formula returned error: %v", result.Content)
	}
	data := parseToolJSON(t, result)
	if affected, _ := data["affected_row_count"].(float64); affected != 2 {
		t.Errorf("affected_row_count = %v, want 2", data["affected_row_count"])
	}

	// Only rowid 1 (milk) should remain.
	remaining := callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "grocery-list"})
	rows, _ := parseToolJSON(t, remaining)["rows"].([]any)
	if len(rows) != 1 {
		t.Errorf("remaining rows = %d, want 1", len(rows))
	}
}

// TestMCP_RunFormula_PaginatedViewExplicitCursorLimit walks a paged view two pages deep by passing
// explicit :limit and :cursor in `params`, proving the pagination controls flow through.
func TestMCP_RunFormula_PaginatedViewExplicitCursorLimit(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID) // rowids 1,2,3

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "paged",
		"description": "Paged groceries.",
		"sql":         "SELECT rowid AS rowid, name FROM groceries WHERE rowid > :cursor ORDER BY rowid LIMIT :limit",
		"params": []any{
			map[string]any{"name": "cursor", "type": "integer", "source": "pagination"},
			map[string]any{"name": "limit", "type": "integer", "source": "pagination"},
		},
	})

	// Page 1: limit 2 → rowids 1,2, more to come.
	page1 := parseToolJSON(t, callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "paged", "params": map[string]any{"limit": 2},
	}))
	if rows, _ := page1["rows"].([]any); len(rows) != 2 {
		t.Errorf("page 1 rows = %d, want 2", len(rows))
	}
	pag1, _ := page1["pagination"].(map[string]any)
	if pag1["has_more"] != true {
		t.Errorf("page 1 has_more = %v, want true", pag1["has_more"])
	}
	if pag1["next_cursor"] != float64(2) {
		t.Errorf("page 1 next_cursor = %v, want 2", pag1["next_cursor"])
	}

	// Page 2: cursor 2, limit 2 → rowid 3, no more.
	page2 := parseToolJSON(t, callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "paged", "params": map[string]any{"cursor": 2, "limit": 2},
	}))
	if rows, _ := page2["rows"].([]any); len(rows) != 1 {
		t.Errorf("page 2 rows = %d, want 1", len(rows))
	}
	pag2, _ := page2["pagination"].(map[string]any)
	if pag2["has_more"] != false {
		t.Errorf("page 2 has_more = %v, want false", pag2["has_more"])
	}
}

func TestMCP_RunFormula_NotFound(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	result := callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "ghost"})
	if !result.IsError {
		t.Fatal("expected error running a missing formula")
	}
}

// --- permissions ---

func TestMCP_CreateFormula_ViewerForbidden(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClientWithRole(t, userID, authz.BdbRoleViewer)

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "viewer-attempt", "description": "Should fail.",
		"sql": "SELECT 1",
	})
	if !result.IsError {
		t.Error("viewer should not be able to create formulas")
	}
}

func TestMCP_RunFormula_ViewerCannotRunAction(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	owner := setupMCPClientWithRole(t, userID, authz.BdbRoleOwner)
	seedGroceriesMCP(t, owner, bdbID)
	callFormulaTool(t, owner, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "wipe-all", "description": "Delete everything.",
		"sql": "DELETE FROM groceries",
	})

	viewer := setupMCPClientWithRole(t, userID, authz.BdbRoleViewer)
	result := callTool(t, viewer, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "wipe-all"})
	if !result.IsError {
		t.Error("viewer should not be able to run an action over MCP")
	}
}

// TestMCP_Formula_ArrayDefaultRoundTrip pins that a multi-select's defaults subset is authored as a real
// array over MCP and reads back as one, and that a mistyped default is a tool error rather than a
// silently dropped default.
func TestMCP_Formula_ArrayDefaultRoundTrip(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "groceries-by-kind",
		"description": "Groceries narrowed to the picked kinds.",
		"sql": "SELECT name FROM groceries WHERE json_array_length(:kinds) = 0 " +
			"OR name IN (SELECT je.value FROM json_each(:kinds) je)",
		"params": []any{
			map[string]any{
				"name": "kinds", "type": "text", "source": "prompt", "optional": true, "multiple": true,
				"options": []any{"milk", "bread"}, "defaults": []any{"milk"},
			},
		},
	})
	got := callFormulaTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "groceries-by-kind"})
	params, _ := got["params"].([]any)
	first, _ := params[0].(map[string]any)
	if def, _ := first["defaults"].([]any); len(def) != 1 || def[0] != "milk" {
		t.Errorf("defaults = %v, want the array [milk] read back", first["defaults"])
	}

	result := callTool(t, c, "create_formula", map[string]any{
		"bdb_id": bdbID, "name": "bad-default", "description": "x", "sql": "SELECT :n AS n",
		"params": []any{map[string]any{"name": "n", "type": "integer", "source": "prompt", "optional": true,
			"default": map[string]any{"value": 3}}},
	})
	if !result.IsError {
		t.Error("create with an object default succeeded, want a tool error")
	}
}

// TestMCP_Formula_LookupRoundTrip pins that the nested lookup config — allow_new included —
// survives the MCP create/get round-trip and that a lookup param binds its value like any scalar.
func TestMCP_Formula_LookupRoundTrip(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "grocery-by-id",
		"description": "One grocery item, picked from a dropdown.",
		"sql":         "SELECT name FROM groceries WHERE id = :item AND (:name_pick IS NULL OR name = :name_pick)",
		"params": []any{
			map[string]any{
				"name": "item", "type": "integer", "source": "lookup", "optional": true,
				"lookup": map[string]any{"table": "groceries", "label_column": "name", "value_column": "id"},
			},
			map[string]any{
				"name": "name_pick", "type": "text", "source": "lookup", "optional": true,
				"lookup": map[string]any{
					"table": "groceries", "label_column": "name", "value_column": "name", "allow_new": true,
					"fill": map[string]any{"item": "id"},
				},
			},
		},
	})

	got := callFormulaTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "grocery-by-id"})
	params, _ := got["params"].([]any)
	first, _ := params[0].(map[string]any)
	lookup, _ := first["lookup"].(map[string]any)
	if lookup["table"] != "groceries" || lookup["value_column"] != "id" {
		t.Errorf("lookup config not round-tripped, got %v", first["lookup"])
	}
	if first["optional"] != true {
		t.Errorf("optional flag not round-tripped, got %v", first["optional"])
	}
	second, _ := params[1].(map[string]any)
	if lookup, _ := second["lookup"].(map[string]any); lookup["allow_new"] != true {
		t.Errorf("allow_new not round-tripped, got %v", second["lookup"])
	}
	if lookup, _ := second["lookup"].(map[string]any); lookup["fill"] == nil {
		t.Errorf("fill not round-tripped, got %v", second["lookup"])
	} else if fill, _ := lookup["fill"].(map[string]any); fill["item"] != "id" {
		t.Errorf("fill = %v, want the item fill kept", fill)
	}

	result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "grocery-by-id",
		"params": map[string]any{"item": 2},
	})
	if result.IsError {
		t.Fatalf("run_formula returned error: %v", result.Content)
	}
	if rows, _ := parseToolJSON(t, result)["rows"].([]any); len(rows) != 1 {
		t.Errorf("rows = %d, want 1 (id = 2)", len(rows))
	}
}

// TestMCP_Formula_DetailViewRoundTrip authors a row-detail view over MCP (a view attached to a
// view, binding :rowid) and runs it by name with a row id.
func TestMCP_Formula_DetailViewRoundTrip(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	seedGroceriesMCP(t, c, bdbID)

	view := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "groceries-list",
		"description": "All groceries.",
		"sql":         "SELECT rowid AS rowid, name FROM groceries ORDER BY rowid",
	})

	detail := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "grocery-detail",
		"description": "One grocery, all columns.",
		"sql":         "SELECT id, name, quantity FROM groceries WHERE rowid = :rowid",
		"params": []any{map[string]any{"name": "rowid", "type": "integer",
			"source": "selection-single"}},
		"attached_to_formula_id": view["id"],
	})
	if detail["kind"] != "view" {
		t.Errorf("detail kind = %v, want view", detail["kind"])
	}

	result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "grocery-detail",
		"params": map[string]any{"rowid": 2},
	})
	if result.IsError {
		t.Fatalf("run_formula returned error: %v", result.Content)
	}
	data := parseToolJSON(t, result)
	rows, _ := data["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("detail rows = %d, want 1", len(rows))
	}
}

// TestMCP_Formula_DisplayLifecycle authors a checklist display over MCP: view + attached toggle,
// slots set by name via update_formula, refs returned normalized to UUIDs, and null clears.
func TestMCP_Formula_DisplayLifecycle(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE todos (id INTEGER PRIMARY KEY, title TEXT, done INTEGER NOT NULL DEFAULT 0)",
	})
	view := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "todo-list",
		"description": "Open todos.",
		"sql":         "SELECT rowid AS rowid, title, done FROM todos",
	})
	toggle := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":                 bdbID,
		"name":                   "toggle-done",
		"description":            "Flips a todo's done state.",
		"sql":                    "UPDATE todos SET done = 1 - done WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": view["id"],
	})

	updated := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "todo-list",
		"display": map[string]any{
			"mode": "checklist",
			"columns": []map[string]any{
				{"column": "title", "role": "title"}, {"column": "done", "role": "check"},
			},
			"slots": map[string]any{"toggle": "toggle-done"},
		},
	})
	display, ok := updated["display"].(map[string]any)
	if !ok {
		t.Fatalf("display = %v, want an object", updated["display"])
	}
	if display["mode"] != "checklist" || displayRoleColumn(display, "check") != "done" {
		t.Errorf("display = %v, want checklist with done check column", display)
	}
	slots, _ := display["slots"].(map[string]any)
	if slots["toggle"] != toggle["id"] {
		t.Errorf("toggle slot = %v, want %v (normalized UUID)", slots["toggle"], toggle["id"])
	}

	fetched := callFormulaTool(t, c, "get_formula", map[string]any{"bdb_id": bdbID, "ref": "todo-list"})
	if _, ok := fetched["display"].(map[string]any); !ok {
		t.Errorf("get_formula display = %v, want the stored config", fetched["display"])
	}

	// The list slots pass through the MCP display object as JSON arrays, normalized to UUIDs.
	clearDone := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":                 bdbID,
		"name":                   "clear-selected",
		"description":            "Deletes the selected todos.",
		"sql":                    "DELETE FROM todos WHERE rowid IN (:rowids)",
		"params":                 []map[string]any{{"name": "rowids", "type": "integer", "source": "selection-bulk"}},
		"attached_to_formula_id": view["id"],
	})
	tabled := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "todo-list",
		"display": map[string]any{
			"mode":  "table",
			"slots": map[string]any{"bulk": []string{"clear-selected"}},
		},
	})
	tabledDisplay, _ := tabled["display"].(map[string]any)
	tabledSlots, _ := tabledDisplay["slots"].(map[string]any)
	if bulk, _ := tabledSlots["bulk"].([]any); len(bulk) != 1 || bulk[0] != clearDone["id"] {
		t.Errorf("bulk = %v, want [%v] (normalized UUID)", tabledSlots["bulk"], clearDone["id"])
	}

	// Explicit null clears the config.
	cleared := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id":  bdbID,
		"ref":     "todo-list",
		"display": nil,
	})
	if cleared["display"] != nil {
		t.Errorf("display after clear = %v, want absent", cleared["display"])
	}
}

// TestMCP_Formula_ReorderSlot authors the drag-to-reorder wiring over MCP: an update action binding
// :ordered_rowids fills a checklist's reorder slot (normalized to its UUID), and running it with the
// rowids in a new order renumbers the position column the view sorts on.
func TestMCP_Formula_ReorderSlot(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TABLE steps (id INTEGER PRIMARY KEY, title TEXT, " +
			"position INTEGER NOT NULL DEFAULT 1)",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO steps (id, title, position) VALUES (1,'first',1),(2,'second',2)",
	})
	view := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "step-list",
		"description": "Steps in their manual order.",
		"sql":         "SELECT rowid AS rowid, title FROM steps ORDER BY position, id",
	})
	reorder := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "reorder-steps",
		"description": "Renumbers positions from the dropped order.",
		"sql": "UPDATE steps SET position = (SELECT je.key + 1 FROM json_each(:ordered_rowids) je " +
			"WHERE je.value = steps.id) WHERE id IN (SELECT value FROM json_each(:ordered_rowids))",
		"params": []map[string]any{
			{"name": "ordered_rowids", "type": "integer", "source": "reorder"},
		},
		"attached_to_formula_id": view["id"],
	})

	updated := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "step-list",
		"display": map[string]any{
			"mode":    "checklist",
			"columns": []map[string]any{{"column": "title", "role": "title"}},
			"slots":   map[string]any{"reorder": "reorder-steps"},
		},
	})
	display, _ := updated["display"].(map[string]any)
	slots, _ := display["slots"].(map[string]any)
	if slots["reorder"] != reorder["id"] {
		t.Errorf("reorder slot = %v, want %v (normalized UUID)", slots["reorder"], reorder["id"])
	}

	callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "reorder-steps",
		"params": map[string]any{"ordered_rowids": []int{2, 1}},
	})
	result := callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "step-list"})
	listed := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(listed, "second") || strings.Index(listed, "second") > strings.Index(listed, "first") {
		t.Errorf("step-list after reorder = %q, want second listed before first", listed)
	}
}

// TestMCP_Formula_BoardMoveSlot authors the board wiring over MCP: an update action binding :rowid
// plus a required options-declaring target fills a board's move slot (normalized to its UUID), and
// running it with a destination value re-homes the row.
func TestMCP_Formula_BoardMoveSlot(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, " +
			"status TEXT NOT NULL DEFAULT 'todo')",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO tasks (id, name, status) VALUES (1,'write it','todo'),(2,'ship it','done')",
	})
	view := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "task-board",
		"description": "Tasks by status.",
		"sql":         "SELECT rowid AS rowid, name, status FROM tasks ORDER BY id",
	})
	move := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "move-task",
		"description": "Re-homes a task in another column.",
		"sql":         "UPDATE tasks SET status = :status WHERE id = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "status", "type": "text", "source": "prompt", "options": []any{"todo", "doing", "done"}},
		},
		"attached_to_formula_id": view["id"],
	})

	updated := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "task-board",
		"display": map[string]any{
			"mode": "board",
			"columns": []map[string]any{
				{"column": "name", "role": "title"},
				{"column": "status", "role": "group"},
			},
			"slots": map[string]any{"move": "move-task"},
		},
	})
	display, _ := updated["display"].(map[string]any)
	slots, _ := display["slots"].(map[string]any)
	if slots["move"] != move["id"] {
		t.Errorf("move slot = %v, want %v (normalized UUID)", slots["move"], move["id"])
	}

	callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "move-task",
		"params": map[string]any{"rowid": 1, "status": "doing"},
	})
	result := callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "task-board"})
	listed := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(listed, "doing") {
		t.Errorf("task-board after move = %q, want task 1 in doing", listed)
	}
}

// TestMCP_ListFormulas_IncludesHiddenForViewer pins that hiding never trims the assistant surface:
// even with viewer-role access, list_formulas returns hidden formulas, flagged.
func TestMCP_ListFormulas_IncludesHiddenForViewer(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	owner := setupMCPClient(t, userID)
	seedGroceriesMCP(t, owner, bdbID)
	callFormulaTool(t, owner, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "plumbing-rollup",
		"description": "Assistant-only rollup.",
		"sql":         "SELECT COUNT(*) AS total FROM groceries",
		"hidden":      true,
	})

	viewer := setupMCPClientWithRole(t, userID, authz.BdbRoleViewer)
	found := false
	for _, raw := range listFormulasViaMCP(t, viewer, bdbID) {
		entry, _ := raw.(map[string]any)
		if entry["name"] == "plumbing-rollup" {
			found = true
			if entry["hidden"] != true {
				t.Errorf("hidden = %v, want true", entry["hidden"])
			}
		}
	}
	if !found {
		t.Fatal("hidden formula missing from the viewer's list_formulas")
	}
}

// TestMCP_RunFormula_MultiSelectLookup pins the multi-select contract over MCP: a picked array
// writes one row per value, an omitting run resolves the "all" default to every member the lookup
// table holds, and an explicit empty selection on a required param errors naming the rule.
func TestMCP_RunFormula_MultiSelectLookup(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)
	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql": "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT); " +
			"CREATE TABLE tally (member_id INTEGER, tag TEXT)",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql":    "INSERT INTO members (id, name) VALUES (1,'Alice'),(2,'Bob'),(3,'Cara')",
	})

	callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "tag-members",
		"description": "Tags the picked members.",
		"sql":         "INSERT INTO tally (member_id, tag) SELECT je.value, :tag FROM json_each(:picked) je",
		"params": []any{
			map[string]any{"name": "tag", "type": "text", "source": "prompt"},
			map[string]any{"name": "picked", "type": "integer", "source": "lookup", "multiple": true,
				"default": "all",
				"lookup":  map[string]any{"table": "members", "label_column": "name", "value_column": "id"}},
		},
	})

	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "tag-members",
		"params": map[string]any{"tag": "subset", "picked": []any{3, 1}},
	}); result.IsError {
		t.Errorf("run with a picked subset failed: %v", result.Content)
	}
	if result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "tag-members", "params": map[string]any{"tag": "everyone"},
	}); result.IsError {
		t.Errorf("omitting run failed to resolve the all default: %v", result.Content)
	}
	result := callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID, "ref": "tag-members", "params": map[string]any{"tag": "none", "picked": []any{}},
	})
	if !result.IsError {
		t.Fatal("expected an error for an explicit empty selection on a required param")
	}
	if text := result.Content[0].(mcp.TextContent).Text; !strings.Contains(text, "at least one value") {
		t.Errorf("error = %q, want the at-least-one rule named", text)
	}

	q := callTool(t, c, "execute_query", map[string]any{
		"bdb_id": bdbID,
		"sql": "SELECT tag, group_concat(member_id) FROM " +
			"(SELECT tag, member_id FROM tally ORDER BY rowid) GROUP BY tag ORDER BY tag",
	})
	text := q.Content[0].(mcp.TextContent).Text
	for _, want := range []string{"subset", "3,1", "everyone", "1,2,3"} {
		if !strings.Contains(text, want) {
			t.Errorf("tally query = %q, want it to contain %q", text, want)
		}
	}
}

// TestMCP_Formula_CalendarMoveSlot authors the calendar wiring over MCP: a view declaring the window pair
// takes the mode at create, an update action binding :rowid plus a required date target fills its move
// slot (normalized to its UUID), a windowed run narrows to the week while keeping the unscheduled tray,
// and running the move with a day re-homes the row into the week.
func TestMCP_Formula_CalendarMoveSlot(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE meals (id INTEGER PRIMARY KEY, dish TEXT, day TEXT)",
	})
	callTool(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID,
		"sql": "INSERT INTO meals (id, dish, day) VALUES (1,'soup','2026-09-07'),(2,'cake','2026-09-20')," +
			"(3,'leftovers',NULL)",
	})
	view := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "meal-plan",
		"description": "Meals on their days.",
		"sql": "SELECT rowid AS rowid, dish, day FROM meals WHERE day IS NULL OR " +
			"((:window_start IS NULL OR day >= :window_start) AND (:window_end IS NULL OR day <= :window_end)) " +
			"ORDER BY day, id",
		"params": []map[string]any{
			{"name": "window_start", "type": "date", "source": "window"},
			{"name": "window_end", "type": "date", "source": "window"},
		},
		"display": map[string]any{
			"mode": "calendar",
			"columns": []map[string]any{
				{"column": "dish", "role": "title"},
				{"column": "day", "role": "day"},
			},
		},
	})
	move := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "move-meal",
		"description": "Moves a meal to the dropped day.",
		"sql":         "UPDATE meals SET day = :day WHERE id = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "day", "type": "date", "source": "prompt"},
		},
		"attached_to_formula_id": view["id"],
	})

	updated := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "meal-plan",
		"display": map[string]any{
			"mode": "calendar",
			"columns": []map[string]any{
				{"column": "dish", "role": "title"},
				{"column": "day", "role": "day"},
			},
			"slots": map[string]any{"move": "move-meal"},
		},
	})
	display, _ := updated["display"].(map[string]any)
	slots, _ := display["slots"].(map[string]any)
	if slots["move"] != move["id"] {
		t.Errorf("move slot = %v, want %v (normalized UUID)", slots["move"], move["id"])
	}

	week := map[string]any{"window_start": "2026-09-07", "window_end": "2026-09-13"}
	result := callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "meal-plan", "params": week})
	listed := result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(listed, "soup") || !strings.Contains(listed, "leftovers") {
		t.Errorf("windowed meal-plan = %q, want the week's meal and the unscheduled one", listed)
	}
	if strings.Contains(listed, "cake") {
		t.Errorf("windowed meal-plan = %q, want the meal a week later left out", listed)
	}

	callTool(t, c, "run_formula", map[string]any{
		"bdb_id": bdbID,
		"ref":    "move-meal",
		"params": map[string]any{"rowid": 2, "day": "2026-09-08"},
	})
	result = callTool(t, c, "run_formula", map[string]any{"bdb_id": bdbID, "ref": "meal-plan", "params": week})
	listed = result.Content[0].(mcp.TextContent).Text
	if !strings.Contains(listed, "cake") {
		t.Errorf("meal-plan after move = %q, want cake on the week's Tuesday", listed)
	}
}

// TestMCP_Formula_CalendarWindow authors a calendar window over MCP: a fixed range lands on create with
// its integer length intact, a weekly window replaces it on update, and mixing the two kinds is refused
// with the reason.
func TestMCP_Formula_CalendarWindow(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := setupMCPClient(t, userID)

	callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID,
		"sql":    "CREATE TABLE meals (id INTEGER PRIMARY KEY, dish TEXT, day TEXT)",
	})
	calendar := func(window map[string]any) map[string]any {
		return map[string]any{
			"mode":    "calendar",
			"columns": []map[string]any{{"column": "dish", "role": "title"}, {"column": "day", "role": "day"}},
			"window":  window,
		}
	}
	created := callFormulaTool(t, c, "create_formula", map[string]any{
		"bdb_id":      bdbID,
		"name":        "trip-plan",
		"description": "The trip's days.",
		"sql": "SELECT rowid AS rowid, dish, day FROM meals WHERE day IS NULL OR " +
			"((:window_start IS NULL OR day >= :window_start) AND (:window_end IS NULL OR day <= :window_end))",
		"params": []map[string]any{
			{"name": "window_start", "type": "date", "source": "window"},
			{"name": "window_end", "type": "date", "source": "window"},
		},
		"display": calendar(map[string]any{"start": "2026-09-18", "days": 4}),
	})
	display, _ := created["display"].(map[string]any)
	if window, _ := display["window"].(map[string]any); window["start"] != "2026-09-18" ||
		window["days"] != float64(4) {
		t.Errorf("created window = %v, want start 2026-09-18 over 4 days", window)
	}

	updated := callFormulaTool(t, c, "update_formula", map[string]any{
		"bdb_id":  bdbID,
		"ref":     "trip-plan",
		"display": calendar(map[string]any{"week_start": "saturday", "week_end": "sunday"}),
	})
	display, _ = updated["display"].(map[string]any)
	if window, _ := display["window"].(map[string]any); window["week_start"] != "saturday" ||
		window["week_end"] != "sunday" || window["start"] != nil {
		t.Errorf("updated window = %v, want the weekend window and no fixed range left", window)
	}

	result := callTool(t, c, "update_formula", map[string]any{
		"bdb_id":  bdbID,
		"ref":     "trip-plan",
		"display": calendar(map[string]any{"week_start": "monday", "days": 3}),
	})
	if !result.IsError {
		t.Fatal("expected an error for a window mixing weekday names with a length")
	}
	if text := result.Content[0].(mcp.TextContent).Text; !strings.Contains(text, "not both") {
		t.Errorf("error = %q, want it to say the kinds never mix", text)
	}
}
