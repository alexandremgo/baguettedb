//go:build integration

package integrationtest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestFormulasGroup_REST covers the repeated-group param end to end over REST: authoring a
// multi-statement action whose group binds as one JSON value consumed with json_each, running it
// with explicit rows, the empty-optional-group semantics, and the per-row validation rejections.
func TestFormulasGroup_REST(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE orders (id INTEGER PRIMARY KEY, note TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE order_items (id INTEGER PRIMARY KEY, " +
			"order_id INTEGER NOT NULL, sku TEXT NOT NULL, qty INTEGER)"})

	// The group's rows land through json_each in the second statement; the response echoes the
	// nested field schema.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "add-order",
			"description": "Adds an order with its item rows.",
			"sql": "INSERT INTO orders (note) VALUES (:note);\n" +
				"INSERT INTO order_items (order_id, sku, qty) " +
				"SELECT last_insert_rowid(), json_extract(je.value, '$.sku'), json_extract(je.value, '$.qty') " +
				"FROM json_each(:items) je",
			"params": []map[string]any{
				{"name": "note", "type": "text", "source": "prompt", "optional": true, "hint": "defaults to none"},
				{"name": "items", "type": "group", "source": "prompt", "optional": true, "fields": []map[string]any{
					{"name": "sku", "type": "text", "source": "prompt"},
					{"name": "qty", "type": "integer", "source": "prompt", "optional": true},
				}},
			},
		})
	if status != http.StatusCreated {
		t.Fatalf("create group formula status = %d, want 201, body: %v", status, resp)
	}
	params, _ := resp["params"].([]any)
	if len(params) != 2 {
		t.Fatalf("params = %d, want 2", len(params))
	}
	if note, _ := params[0].(map[string]any); note["hint"] != "defaults to none" {
		t.Errorf("note hint = %v, want the display hint echoed", note["hint"])
	}
	group, _ := params[1].(map[string]any)
	if fields, _ := group["fields"].([]any); len(fields) != 2 {
		t.Errorf("group fields = %v, want the 2-field row schema echoed", group["fields"])
	}

	// A run with rows inserts one order_items row per group entry; the omitted optional qty lands
	// as NULL.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, "add-order")+"/run",
		userToken(t, userID),
		map[string]any{"params": map[string]any{
			"note": "restock",
			"items": []map[string]any{
				{"sku": "flour", "qty": 2},
				{"sku": "yeast"},
			},
		}})
	if status != http.StatusOK {
		t.Fatalf("group run status = %d, want 200, body: %v", status, resp)
	}
	_, rows := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, userID),
		map[string]string{"sql": "SELECT sku, qty FROM order_items ORDER BY id"})
	items, _ := rows["rows"].([]any)
	if len(items) != 2 {
		t.Fatalf("order_items rows = %d, want 2", len(items))
	}
	second := extractRowValues(t, items[1])
	if second[0] != "yeast" || second[1] != nil {
		t.Errorf("second item = %v, want (yeast, NULL qty)", second)
	}

	// An omitted optional group binds '[]' — the order lands with zero items, no NULL guard in SQL.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, "add-order")+"/run",
		userToken(t, userID),
		map[string]any{"params": map[string]any{"note": "empty restock"}})
	if status != http.StatusOK {
		t.Fatalf("empty-group run status = %d, want 200, body: %v", status, resp)
	}
	_, counts := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, userID),
		map[string]string{"sql": "SELECT (SELECT COUNT(*) FROM orders), (SELECT COUNT(*) FROM order_items)"})
	countRow := extractRowValues(t, counts["rows"].([]any)[0])
	if countRow[0] != float64(2) || countRow[1] != float64(2) {
		t.Errorf("counts = %v, want 2 orders and still 2 items", countRow)
	}

	rejections := []struct {
		name  string
		items any
	}{
		{"non-array value", "flour"},
		{"non-object row", []any{"flour"}},
		{"unknown field", []map[string]any{{"sku": "flour", "color": "white"}}},
		{"missing required field", []map[string]any{{"qty": 2}}},
		{"mistyped field", []map[string]any{{"sku": "flour", "qty": "two"}}},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPost,
				formulasPath(bdbID, "add-order")+"/run", userToken(t, userID),
				map[string]any{"params": map[string]any{"items": tt.items}})
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body: %v", status, resp)
			}
		})
	}

	// The group flows over MCP unchanged: the fields schema authors through create_formula's
	// params array, and run_formula takes the same array-of-objects value.
	t.Run("mcp path", func(t *testing.T) {
		c := setupMCPClient(t, userID)
		mcpBdb := uuid.New().String()
		callTool(t, c, "execute_schema", map[string]any{
			"bdb_id": mcpBdb,
			"sql":    "CREATE TABLE tags (id INTEGER PRIMARY KEY, label TEXT NOT NULL)",
		})
		created := callFormulaTool(t, c, "create_formula", map[string]any{
			"bdb_id":      mcpBdb,
			"name":        "add-tags",
			"description": "Adds several tags at once.",
			"sql":         "INSERT INTO tags (label) SELECT json_extract(je.value, '$.label') FROM json_each(:tags) je",
			"params": []map[string]any{
				{"name": "tags", "type": "group", "source": "prompt", "fields": []map[string]any{
					{"name": "label", "type": "text", "source": "prompt"},
				}},
			},
		})
		params, _ := created["params"].([]any)
		if len(params) != 1 {
			t.Fatalf("params = %v, want the group echoed", created["params"])
		}
		if fields, _ := params[0].(map[string]any)["fields"].([]any); len(fields) != 1 {
			t.Errorf("group fields = %v, want the label field echoed", params[0])
		}

		ran := callFormulaTool(t, c, "run_formula", map[string]any{
			"bdb_id": mcpBdb,
			"ref":    "add-tags",
			"params": map[string]any{"tags": []map[string]any{{"label": "urgent"}, {"label": "later"}}},
		})
		if affected, _ := ran["affected_row_count"].(float64); affected != 2 {
			t.Errorf("affected = %v, want 2 tag rows", ran["affected_row_count"])
		}
		// Two rows out of ONE statement — the count that tells callers the total is a plain row count.
		if count, _ := ran["statement_count"].(float64); count != 1 {
			t.Errorf("statement_count = %v, want 1", ran["statement_count"])
		}
	})

	// A required group needs at least one row: both an empty array and an omission reject.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID),
		map[string]any{
			"name":        "add-strict-order",
			"description": "Adds an order that must carry items.",
			"sql": "INSERT INTO orders (note) VALUES (:note);\n" +
				"INSERT INTO order_items (order_id, sku) " +
				"SELECT last_insert_rowid(), json_extract(je.value, '$.sku') FROM json_each(:items) je",
			"params": []map[string]any{
				{"name": "note", "type": "text", "source": "prompt", "optional": true},
				{"name": "items", "type": "group", "source": "prompt", "fields": []map[string]any{
					{"name": "sku", "type": "text", "source": "prompt"},
				}},
			},
		})
	if status != http.StatusCreated {
		t.Fatalf("create strict formula status = %d, want 201, body: %v", status, resp)
	}
	for _, params := range []map[string]any{{}, {"items": []any{}}} {
		status, resp = testutil.DoRequest(t, router, http.MethodPost,
			formulasPath(bdbID, "add-strict-order")+"/run", userToken(t, userID),
			map[string]any{"params": params})
		if status != http.StatusBadRequest {
			t.Errorf("strict run with %v status = %d, want 400, body: %v", params, status, resp)
		}
	}
}
