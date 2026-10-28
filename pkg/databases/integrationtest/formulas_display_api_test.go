//go:build integration

package integrationtest

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// createGroceriesTable provisions the checklist-shaped table the display tests reference.
func createGroceriesTable(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE groceries (id INTEGER PRIMARY KEY, item TEXT, notes TEXT, " +
			"bought INTEGER NOT NULL DEFAULT 0)"})
}

// createChecklistFixture creates a grocery view plus three attached actions (insert, toggle,
// delete) and returns their ids.
func createChecklistFixture(
	t *testing.T, router http.Handler, bdbID, userID string,
) (viewID, addID, toggleID, deleteID string) {
	t.Helper()
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "grocery-list",
		"description": "Groceries to buy.",
		"sql":         "SELECT rowid AS rowid, item, bought FROM groceries",
	})
	viewID = view["id"].(string)
	add := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "add-grocery",
		"description":            "Adds a grocery item.",
		"sql":                    "INSERT INTO groceries (item) VALUES (:item)",
		"params":                 []map[string]any{{"name": "item", "type": "text", "source": "prompt"}},
		"attached_to_formula_id": viewID,
	})
	toggle := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "toggle-bought",
		"description":            "Flips an item's bought state.",
		"sql":                    "UPDATE groceries SET bought = 1 - bought WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": viewID,
	})
	del := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "delete-grocery",
		"description":            "Removes a grocery item.",
		"sql":                    "DELETE FROM groceries WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": viewID,
	})
	return viewID, add["id"].(string), toggle["id"].(string), del["id"].(string)
}

// displayOf extracts the display object from a formula response, failing if absent.
func displayOf(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	display, ok := resp["display"].(map[string]any)
	if !ok {
		t.Fatalf("display = %v, want an object", resp["display"])
	}
	return display
}

func TestFormulasDisplay_REST_ChecklistLifecycle(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)
	_, addID, toggleID, deleteID := createChecklistFixture(t, router, bdbID, userID)

	// Slots are set by action name; the response must carry them normalized to UUIDs.
	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "checklist",
			"columns": []map[string]any{
				{"column": "item", "role": "title"}, {"column": "bought", "role": "check"},
			},
			"slots": map[string]any{
				"add": "add-grocery", "toggle": "toggle-bought", "delete": "delete-grocery",
			},
		}})
	if status != http.StatusOK {
		t.Fatalf("set display status = %d, want 200, body: %v", status, resp)
	}
	display := displayOf(t, resp)
	if display["mode"] != "checklist" || displayRoleColumn(display, "title") != "item" ||
		displayRoleColumn(display, "check") != "bought" {
		t.Errorf("display = %v, want checklist/item/bought", display)
	}
	actions, _ := display["slots"].(map[string]any)
	if actions["add"] != addID || actions["toggle"] != toggleID || actions["delete"] != deleteID {
		t.Errorf("actions = %v, want slot refs normalized to UUIDs %s/%s/%s", actions, addID, toggleID, deleteID)
	}

	// The stored config comes back on a plain GET.
	status, resp = testutil.DoRequest(t, router, http.MethodGet, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200, body: %v", status, resp)
	}
	if displayOf(t, resp)["mode"] != "checklist" {
		t.Errorf("display after get = %v, want checklist", resp["display"])
	}
	if resp["hidden"] != false {
		t.Errorf("hidden = %v, want false by default", resp["hidden"])
	}

	// JSON null clears the config back to table rendering.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID), map[string]any{"display": nil})
	if status != http.StatusOK {
		t.Fatalf("clear display status = %d, want 200, body: %v", status, resp)
	}
	if resp["display"] != nil {
		t.Errorf("display after clear = %v, want null", resp["display"])
	}
}

func TestFormulasDisplay_REST_CardsAtCreate(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)

	// A slot-free display config is valid at create time (slots need attached actions, which can
	// only exist once the view does).
	resp := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "grocery-notes",
		"description": "Grocery notes as cards.",
		"sql":         "SELECT rowid AS rowid, item, notes FROM groceries",
		"display": map[string]any{
			"mode":    "cards",
			"columns": []map[string]any{{"column": "item", "role": "title"}, {"column": "notes", "role": "body"}},
		},
	})
	display := displayOf(t, resp)
	if display["mode"] != "cards" || displayRoleColumn(display, "body") != "notes" {
		t.Errorf("display = %v, want cards with notes body", display)
	}
}

func TestFormulasDisplay_REST_CreateRejections(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "display on an action",
			body: map[string]any{
				"name": "add-plain", "description": "d.",
				"sql": "INSERT INTO groceries (item) VALUES ('x')",
				"display": map[string]any{"mode": "checklist",
					"columns": []map[string]any{{"column": "item", "role": "title"}}},
			},
		},
		{
			name: "unknown mode",
			body: map[string]any{
				"name": "v1", "description": "d.",
				"sql": "SELECT rowid AS rowid, item FROM groceries",
				"display": map[string]any{"mode": "gallery",
					"columns": []map[string]any{{"column": "item", "role": "title"}}},
			},
		},
		{
			name: "title column not in the select",
			body: map[string]any{
				"name": "v2", "description": "d.",
				"sql": "SELECT rowid AS rowid, item FROM groceries",
				"display": map[string]any{"mode": "checklist",
					"columns": []map[string]any{{"column": "missing", "role": "title"}}},
			},
		},
		{
			name: "row-bound slot without rowid column",
			body: map[string]any{
				"name": "v3", "description": "d.",
				"sql": "SELECT item FROM groceries",
				"display": map[string]any{
					"mode": "checklist", "columns": []map[string]any{{"column": "item", "role": "title"}},
					"slots": map[string]any{"delete": "delete-grocery"},
				},
			},
		},
		{
			name: "slot ref at create time",
			body: map[string]any{
				"name": "v4", "description": "d.",
				"sql": "SELECT rowid AS rowid, item FROM groceries",
				"display": map[string]any{
					"mode": "checklist", "columns": []map[string]any{{"column": "item", "role": "title"}},
					"slots": map[string]any{"add": "add-grocery"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
				userToken(t, userID), tt.body)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body: %v", status, resp)
			}
		})
	}
}

func TestFormulasDisplay_REST_UpdateRejections(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)
	viewID, _, _, _ := createChecklistFixture(t, router, bdbID, userID)

	// A row-detail view attached to the grocery view; its own display support is covered by
	// TestFormulasDisplay_REST_InteractiveDetail.
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "grocery-detail",
		"description":            "One grocery row.",
		"sql":                    "SELECT rowid AS rowid, item, notes FROM groceries WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": viewID,
	})

	baseDisplay := func(actions map[string]any) map[string]any {
		d := map[string]any{"mode": "checklist", "columns": []map[string]any{{"column": "item", "role": "title"}}}
		if actions != nil {
			d["slots"] = actions
		}
		return d
	}

	tests := []struct {
		name string
		ref  string
		body map[string]any
	}{
		{
			// The shape fits the delete slot (:rowid, no required extras), so only the verb can be
			// what rejects it.
			name: "slot action with the wrong verb",
			ref:  "grocery-list",
			body: map[string]any{"display": baseDisplay(map[string]any{"delete": "toggle-bought"})},
		},
		{
			name: "slot ref not attached to this view",
			ref:  "grocery-list",
			body: map[string]any{"display": baseDisplay(map[string]any{"add": "no-such-action"})},
		},
		{
			name: "display on an action",
			ref:  "delete-grocery",
			body: map[string]any{"display": baseDisplay(nil)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, tt.ref),
				userToken(t, userID), tt.body)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body: %v", status, resp)
			}
		})
	}
}

func TestFormulasDisplay_REST_DeleteSlottedActionGuard(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)
	createChecklistFixture(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "item", "role": "title"}},
			"slots": map[string]any{"delete": "delete-grocery"},
		}})
	if status != http.StatusOK {
		t.Fatalf("set display status = %d, want 200, body: %v", status, resp)
	}

	// The slotted action cannot be deleted out from under the display config.
	status, resp = testutil.DoRequest(t, router, http.MethodDelete, formulasPath(bdbID, "delete-grocery"),
		userToken(t, userID), nil)
	if status != http.StatusBadRequest {
		t.Fatalf("delete slotted action status = %d, want 400, body: %v", status, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "slot") {
		t.Errorf("error = %q, want a message naming the slot", msg)
	}

	// Clearing the slot unblocks the delete.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{"mode": "checklist",
			"columns": []map[string]any{{"column": "item", "role": "title"}}}})
	if status != http.StatusOK {
		t.Fatalf("clear slot status = %d, want 200, body: %v", status, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodDelete, formulasPath(bdbID, "delete-grocery"),
		userToken(t, userID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete unslotted action status = %d, want 204, body: %v", status, resp)
	}
}

func TestFormulasDisplay_REST_MutateSlottedActionGuard(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)
	createChecklistFixture(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode":    "checklist",
			"columns": []map[string]any{{"column": "item", "role": "title"}, {"column": "bought", "role": "check"}},
			"slots":   map[string]any{"toggle": "toggle-bought"},
		}})
	if status != http.StatusOK {
		t.Fatalf("set display status = %d, want 200, body: %v", status, resp)
	}

	// The toggle slot pins no verb, so an action that flips the row AND records the flip elsewhere
	// keeps filling it, even though it derives a non-update verb.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "toggle-bought"),
		userToken(t, userID),
		map[string]any{"sql": "UPDATE groceries SET bought = 1 - bought WHERE rowid = :rowid; " +
			"INSERT INTO groceries (item, bought) SELECT 'restock ' || item, 0 FROM groceries " +
			"WHERE rowid = :rowid AND bought = 1"})
	if status != http.StatusOK {
		t.Fatalf("composite toggle status = %d, want 200, body: %v", status, resp)
	}
	if resp["statement_verb"] != "insert" {
		t.Errorf("statement_verb = %v, want insert (derived across all statements)", resp["statement_verb"])
	}

	// Adding a required prompt param breaks the one-tap contract of the toggle slot.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "toggle-bought"),
		userToken(t, userID),
		map[string]any{
			"sql": "UPDATE groceries SET item = :item WHERE rowid = :rowid",
			"params": []map[string]any{
				{"name": "rowid", "type": "integer", "source": "selection-single"},
				{"name": "item", "type": "text", "source": "prompt"},
			},
		})
	if status != http.StatusBadRequest {
		t.Fatalf("required param status = %d, want 400, body: %v", status, resp)
	}

	// Clearing the slot unblocks the same mutation.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{"mode": "checklist",
			"columns": []map[string]any{{"column": "item", "role": "title"}}}})
	if status != http.StatusOK {
		t.Fatalf("clear slot status = %d, want 200, body: %v", status, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "toggle-bought"),
		userToken(t, userID),
		map[string]any{
			"sql": "UPDATE groceries SET item = :item WHERE rowid = :rowid",
			"params": []map[string]any{
				{"name": "rowid", "type": "integer", "source": "selection-single"},
				{"name": "item", "type": "text", "source": "prompt"},
			},
		})
	if status != http.StatusOK {
		t.Fatalf("unslotted required-param mutation status = %d, want 200, body: %v", status, resp)
	}
}

func TestFormulasDisplay_REST_ViewSQLChangeGuards(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)
	createChecklistFixture(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode":    "checklist",
			"columns": []map[string]any{{"column": "item", "role": "title"}, {"column": "bought", "role": "check"}},
		}})
	if status != http.StatusOK {
		t.Fatalf("set display status = %d, want 200, body: %v", status, resp)
	}

	// Dropping the title column from the SELECT breaks the surviving display config.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"sql": "SELECT rowid AS rowid, notes, bought FROM groceries"})
	if status != http.StatusBadRequest {
		t.Fatalf("drop title column status = %d, want 400, body: %v", status, resp)
	}

	// A SQL change keeping the referenced columns re-validates fine.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"sql": "SELECT rowid AS rowid, item, bought FROM groceries WHERE bought = 0"})
	if status != http.StatusOK {
		t.Fatalf("compatible sql change status = %d, want 200, body: %v", status, resp)
	}

	// A kind flip under a display config is rejected even with no attachments in play.
	plain := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "note-cards",
		"description": "Notes as cards.",
		"sql":         "SELECT rowid AS rowid, item, notes FROM groceries",
		"display": map[string]any{"mode": "cards",
			"columns": []map[string]any{{"column": "item", "role": "title"}, {"column": "notes", "role": "body"}}},
	})
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, plain["id"].(string)),
		userToken(t, userID),
		map[string]any{"sql": "INSERT INTO groceries (item) VALUES ('x')"})
	if status != http.StatusBadRequest {
		t.Fatalf("kind flip under display status = %d, want 400, body: %v", status, resp)
	}
}

func TestFormulasDisplay_REST_TableDetailLifecycle(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)
	viewID, _, _, _ := createChecklistFixture(t, router, bdbID, userID)
	detail := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "grocery-detail",
		"description":            "One grocery row.",
		"sql":                    "SELECT rowid AS rowid, item, notes FROM groceries WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": viewID,
	})

	// An explicit table display exists to declare the detail slot; the ref normalizes to a UUID.
	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode":  "table",
			"slots": map[string]any{"detail": "grocery-detail"},
		}})
	if status != http.StatusOK {
		t.Fatalf("set table display status = %d, want 200, body: %v", status, resp)
	}
	display := displayOf(t, resp)
	if display["mode"] != "table" {
		t.Errorf("mode = %v, want table", display["mode"])
	}
	actions, _ := display["slots"].(map[string]any)
	if actions["detail"] != detail["id"].(string) {
		t.Errorf("detail slot = %v, want %v (normalized UUID)", actions["detail"], detail["id"])
	}

	// The detail slot only takes a view; a table display takes every slot except toggle; a table
	// display names no columns.
	rejections := []struct {
		name    string
		display map[string]any
	}{
		{"detail slot on an action", map[string]any{
			"mode": "table", "slots": map[string]any{"detail": "delete-grocery"},
		}},
		{"table with toggle slot", map[string]any{
			"mode": "table", "slots": map[string]any{"toggle": "toggle-bought"},
		}},
		{"table with title column", map[string]any{
			"mode": "table", "columns": []map[string]any{{"column": "item", "role": "title"}},
		}},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-list"),
				userToken(t, userID), map[string]any{"display": tt.display})
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body: %v", status, resp)
			}
		})
	}

	// Deleting the slotted detail view is guarded like any slotted formula.
	status, resp = testutil.DoRequest(t, router, http.MethodDelete, formulasPath(bdbID, "grocery-detail"),
		userToken(t, userID), nil)
	if status != http.StatusBadRequest {
		t.Fatalf("delete slotted detail status = %d, want 400, body: %v", status, resp)
	}
}

func TestFormulasDisplay_REST_InteractiveDetail(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE checklists (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE checklist_items (id INTEGER PRIMARY KEY, " +
			"checklist_id INTEGER NOT NULL, label TEXT NOT NULL)"})

	parent := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "lists",
		"description": "All checklists.",
		"sql":         "SELECT rowid AS rowid, name FROM checklists",
	})
	detail := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "list-items",
		"description": "One checklist's items.",
		"sql": "SELECT i.rowid AS rowid, i.label FROM checklist_items i " +
			"WHERE i.checklist_id = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": parent["id"],
	})
	// The add action inserts child rows scoped to the parent row: its :parent_rowid is the
	// checklist the modal is open for, not an item.
	logItem := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "log-item",
		"description": "Adds an item to this checklist.",
		"sql":         "INSERT INTO checklist_items (checklist_id, label) VALUES (:parent_rowid, :label)",
		"params": []map[string]any{
			{"name": "parent_rowid", "type": "integer", "source": "selection-single"},
			{"name": "label", "type": "text", "source": "prompt"},
		},
		"attached_to_formula_id": detail["id"],
	})
	// A row-bound slot of a detail may also bind the parent row alongside the tapped child row.
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "drop-item",
		"description": "Removes an item of this checklist.",
		"sql":         "DELETE FROM checklist_items WHERE rowid = :rowid AND checklist_id = :parent_rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "parent_rowid", "type": "integer", "source": "selection-single"},
		},
		"attached_to_formula_id": detail["id"],
	})
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "bad-add",
		"description":            "An add without the parent binding.",
		"sql":                    "INSERT INTO checklist_items (checklist_id, label) VALUES (1, :label)",
		"params":                 []map[string]any{{"name": "label", "type": "text", "source": "prompt"}},
		"attached_to_formula_id": detail["id"],
	})
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "rowid-add",
		"description": "An add binding the tapped row instead of the parent.",
		"sql":         "INSERT INTO checklist_items (checklist_id, label) VALUES (:rowid, :label)",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "label", "type": "text", "source": "prompt"},
		},
		"attached_to_formula_id": detail["id"],
	})

	// A detail view carries its own checklist display; the parent-bound add slot passes, and its
	// row_actions list may hold a :rowid action of any verb (rowid-add is an insert).
	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "list-items"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "label", "role": "title"}},
			"slots": map[string]any{
				"add": "log-item", "delete": "drop-item", "row_actions": []string{"rowid-add"},
			},
		}})
	if status != http.StatusOK {
		t.Fatalf("set detail display status = %d, want 200, body: %v", status, resp)
	}
	slots, _ := displayOf(t, resp)["slots"].(map[string]any)
	if slots["add"] != logItem["id"].(string) {
		t.Errorf("add slot = %v, want %v (normalized UUID)", slots["add"], logItem["id"])
	}

	// A detail may fill its own detail slot with a view attached to it — details nest recursively,
	// to any depth the attachment chain reaches: checklists → a checklist's items → one item's record.
	nested := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "item-record",
		"description":            "One item's record.",
		"sql":                    "SELECT label FROM checklist_items WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": detail["id"],
	})
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "list-items"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "label", "role": "title"}},
			"slots": map[string]any{
				"add": "log-item", "delete": "drop-item", "row_actions": []string{"rowid-add"},
				"detail": "item-record",
			},
		}})
	if status != http.StatusOK {
		t.Fatalf("set nested detail display status = %d, want 200, body: %v", status, resp)
	}
	slots, _ = displayOf(t, resp)["slots"].(map[string]any)
	if slots["detail"] != nested["id"].(string) {
		t.Errorf("nested detail slot = %v, want %v (normalized UUID)", slots["detail"], nested["id"])
	}

	rejections := []struct {
		name    string
		display map[string]any
	}{
		{"detail slot naming a view not attached to this one", map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "label", "role": "title"}},
			"slots": map[string]any{"detail": "lists"},
		}},
		{"slotted table mode on a detail view", map[string]any{
			"mode": "table", "slots": map[string]any{"delete": "remove-item"},
		}},
		{"add slot without the parent binding", map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "label", "role": "title"}},
			"slots": map[string]any{"add": "bad-add"},
		}},
		{"add slot binding the tapped row instead", map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "label", "role": "title"}},
			"slots": map[string]any{"add": "rowid-add"},
		}},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "list-items"),
				userToken(t, userID), map[string]any{"display": tt.display})
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body: %v", status, resp)
			}
		})
	}

	// Only a detail view's slotted actions receive the parent binding: a top-level view's slot
	// rejects an action declaring :parent_rowid — there is no parent row to bind.
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "stray-add",
		"description": "An add binding a parent row outside a detail context.",
		"sql":         "INSERT INTO checklist_items (checklist_id, label) VALUES (:parent_rowid, :label)",
		"params": []map[string]any{
			{"name": "parent_rowid", "type": "integer", "source": "selection-single"},
			{"name": "label", "type": "text", "source": "prompt"},
		},
		"attached_to_formula_id": parent["id"],
	})
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "lists"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "name", "role": "title"}},
			"slots": map[string]any{"add": "stray-add"},
		}})
	if status != http.StatusBadRequest {
		t.Errorf("top-level parent-bound add status = %d, want 400, body: %v", status, resp)
	}
}

// TestFormulasDisplay_REST_DeclaredLists covers the ordered list slots: a table display's bulk
// list (the selection toolbar) and a checklist's row_actions list, entries resolved by name to
// UUIDs in array order, the shape/mode/duplicate rejections, and the symmetric guards holding on
// list entries.
func TestFormulasDisplay_REST_DeclaredLists(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "grocery-table",
		"description": "All groceries as a table.",
		"sql":         "SELECT rowid AS rowid, item, bought FROM groceries",
	})
	archive := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "archive-selected",
		"description":            "Marks the selected groceries bought.",
		"sql":                    "UPDATE groceries SET bought = 1 WHERE rowid IN (:rowids)",
		"params":                 []map[string]any{{"name": "rowids", "type": "integer", "source": "selection-bulk"}},
		"attached_to_formula_id": view["id"],
	})
	dropSelected := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "delete-selected",
		"description":            "Deletes the selected groceries.",
		"sql":                    "DELETE FROM groceries WHERE rowid IN (:rowids)",
		"params":                 []map[string]any{{"name": "rowids", "type": "integer", "source": "selection-bulk"}},
		"attached_to_formula_id": view["id"],
	})
	duplicate := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "duplicate-item",
		"description":            "Copies one grocery row.",
		"sql":                    "INSERT INTO groceries (item) SELECT item FROM groceries WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": view["id"],
	})

	refList := func(t *testing.T, slots map[string]any, key string) []string {
		t.Helper()
		raw, _ := slots[key].([]any)
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			s, _ := v.(string)
			out = append(out, s)
		}
		return out
	}

	// A table display's bulk list resolves names to UUIDs, keeping the declared order.
	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-table"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode":  "table",
			"slots": map[string]any{"bulk": []string{"archive-selected", "delete-selected"}},
		}})
	if status != http.StatusOK {
		t.Fatalf("set bulk list status = %d, want 200, body: %v", status, resp)
	}
	slots, _ := displayOf(t, resp)["slots"].(map[string]any)
	wantBulk := []string{archive["id"].(string), dropSelected["id"].(string)}
	if got := refList(t, slots, "bulk"); !slices.Equal(got, wantBulk) {
		t.Errorf("bulk = %v, want %v (normalized UUIDs in declared order)", got, wantBulk)
	}

	// A checklist's row_actions list resolves the same way; any verb with :rowid qualifies.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-table"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "item", "role": "title"}},
			"slots": map[string]any{"row_actions": []string{"duplicate-item"}},
		}})
	if status != http.StatusOK {
		t.Fatalf("set row_actions list status = %d, want 200, body: %v", status, resp)
	}
	slots, _ = displayOf(t, resp)["slots"].(map[string]any)
	if got := refList(t, slots, "row_actions"); !slices.Equal(got, []string{duplicate["id"].(string)}) {
		t.Errorf("row_actions = %v, want [%v]", got, duplicate["id"])
	}

	rejections := []struct {
		name    string
		display map[string]any
	}{
		{"bulk entry binding a single row", map[string]any{
			"mode":  "table",
			"slots": map[string]any{"bulk": []string{"duplicate-item"}},
		}},
		{"toggle slot on a table", map[string]any{
			"mode":  "table",
			"slots": map[string]any{"toggle": "archive-selected"},
		}},
		{"row_actions entry binding a bulk selection", map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "item", "role": "title"}},
			"slots": map[string]any{"row_actions": []string{"archive-selected"}},
		}},
		{"bulk list on a checklist", map[string]any{
			"mode": "checklist", "columns": []map[string]any{{"column": "item", "role": "title"}},
			"slots": map[string]any{"bulk": []string{"archive-selected"}},
		}},
		{"one role per formula", map[string]any{
			"mode":  "table",
			"slots": map[string]any{"bulk": []string{"archive-selected", "archive-selected"}},
		}},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-table"),
				userToken(t, userID), map[string]any{"display": tt.display})
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body: %v", status, resp)
			}
		})
	}

	// A table display takes the full mutation surface (all but toggle): add / edit / delete slots
	// plus both lists, resolved together.
	addGrocery := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "add-grocery",
		"description":            "Adds a grocery.",
		"sql":                    "INSERT INTO groceries (item) VALUES (:item)",
		"params":                 []map[string]any{{"name": "item", "type": "text", "source": "prompt"}},
		"attached_to_formula_id": view["id"],
	})
	editGrocery := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "edit-grocery",
		"description": "Renames a grocery.",
		"sql":         "UPDATE groceries SET item = :item WHERE rowid = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "item", "type": "text", "source": "prompt"},
		},
		"attached_to_formula_id": view["id"],
	})
	createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":                   "drop-grocery",
		"description":            "Deletes one grocery.",
		"sql":                    "DELETE FROM groceries WHERE rowid = :rowid",
		"params":                 []map[string]any{{"name": "rowid", "type": "integer", "source": "selection-single"}},
		"attached_to_formula_id": view["id"],
	})
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-table"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "table",
			"slots": map[string]any{
				"add": "add-grocery", "edit": "edit-grocery", "delete": "drop-grocery",
				"bulk": []string{"archive-selected"}, "row_actions": []string{"duplicate-item"},
			},
		}})
	if status != http.StatusOK {
		t.Fatalf("set table mutation slots status = %d, want 200, body: %v", status, resp)
	}
	slots, _ = displayOf(t, resp)["slots"].(map[string]any)
	if slots["add"] != addGrocery["id"].(string) {
		t.Errorf("table add slot = %v, want %v (normalized UUID)", slots["add"], addGrocery["id"])
	}
	if got := refList(t, slots, "row_actions"); !slices.Equal(got, []string{duplicate["id"].(string)}) {
		t.Errorf("table row_actions = %v, want [%v]", got, duplicate["id"])
	}

	// A row_actions entry repeating the edit slot's formula positions that built-in menu entry — the
	// one allowed role repeat — and comes back normalized in the listed order.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-table"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "table",
			"slots": map[string]any{
				"add": "add-grocery", "edit": "edit-grocery", "delete": "drop-grocery",
				"row_actions": []string{"edit-grocery", "duplicate-item"},
			},
		}})
	if status != http.StatusOK {
		t.Fatalf("place edit in row_actions status = %d, want 200, body: %v", status, resp)
	}
	slots, _ = displayOf(t, resp)["slots"].(map[string]any)
	wantMenu := []string{editGrocery["id"].(string), duplicate["id"].(string)}
	if got := refList(t, slots, "row_actions"); !slices.Equal(got, wantMenu) {
		t.Errorf("table row_actions = %v, want the edit slot placed first: %v", got, wantMenu)
	}
	for _, tt := range []struct {
		name, want string
		rowActions []string
	}{
		{"placing a slot that is not a menu entry", "one role", []string{"add-grocery"}},
		{"placing the edit entry twice", "twice", []string{"edit-grocery", "duplicate-item", "edit-grocery"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-table"),
				userToken(t, userID),
				map[string]any{"display": map[string]any{
					"mode": "table",
					"slots": map[string]any{
						"add": "add-grocery", "edit": "edit-grocery", "delete": "drop-grocery",
						"row_actions": tt.rowActions,
					},
				}})
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body: %v", status, resp)
			}
			if msg, _ := resp["error"].(string); !strings.Contains(msg, tt.want) {
				t.Errorf("error = %q, want it to mention %q", msg, tt.want)
			}
		})
	}

	// The symmetric guards hold on list entries: back on the bulk display, the listed action can
	// neither be deleted nor reshaped away from :rowids.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "grocery-table"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode":  "table",
			"slots": map[string]any{"bulk": []string{"archive-selected"}},
		}})
	if status != http.StatusOK {
		t.Fatalf("restore bulk list status = %d, want 200, body: %v", status, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodDelete, formulasPath(bdbID, "archive-selected"),
		userToken(t, userID), nil)
	if status != http.StatusBadRequest {
		t.Errorf("delete listed action status = %d, want 400, body: %v", status, resp)
	}
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "archive-selected"),
		userToken(t, userID),
		map[string]any{
			"sql":    "UPDATE groceries SET bought = 1 WHERE item = :item",
			"params": []map[string]any{{"name": "item", "type": "text", "source": "prompt"}},
		})
	if status != http.StatusBadRequest {
		t.Errorf("reshape listed action status = %d, want 400, body: %v", status, resp)
	}
}

func TestFormulasDisplay_REST_HiddenListFiltering(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	createGroceriesTable(t, ownerRouter, bdbID, userID)
	createFormulaAPI(t, ownerRouter, bdbID, userID, map[string]any{
		"name":        "visible-view",
		"description": "Groceries.",
		"sql":         "SELECT rowid AS rowid, item FROM groceries",
	})
	createFormulaAPI(t, ownerRouter, bdbID, userID, map[string]any{
		"name":        "plumbing-rollup",
		"description": "Assistant-only rollup.",
		"sql":         "SELECT COUNT(*) AS total FROM groceries",
		"hidden":      true,
	})

	listNames := func(router http.Handler) []string {
		t.Helper()
		status, list := testutil.DoRequestList(t, router, http.MethodGet, formulasPath(bdbID, ""),
			userToken(t, userID), nil)
		if status != http.StatusOK {
			t.Fatalf("list status = %d, want 200", status)
		}
		names := make([]string, 0, len(list))
		for _, f := range list {
			names = append(names, f["name"].(string))
		}
		return names
	}

	// A caller who can write formulas gets the hidden one, flagged (the app's show-hidden toggle).
	ownerNames := listNames(ownerRouter)
	if !slices.Contains(ownerNames, "plumbing-rollup") || !slices.Contains(ownerNames, "visible-view") {
		t.Errorf("owner list = %v, want both formulas", ownerNames)
	}

	// A viewer's listing omits hidden formulas entirely.
	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)
	viewerNames := listNames(viewerRouter)
	if slices.Contains(viewerNames, "plumbing-rollup") {
		t.Errorf("viewer list = %v, must omit the hidden formula", viewerNames)
	}
	if !slices.Contains(viewerNames, "visible-view") {
		t.Errorf("viewer list = %v, want the visible formula", viewerNames)
	}

	// Hidden is presentation-only, not a permission boundary: the viewer still runs the hidden
	// view by ref.
	status, resp := testutil.DoRequest(t, viewerRouter, http.MethodPost,
		formulasPath(bdbID, "plumbing-rollup/run"), userToken(t, userID), map[string]any{})
	if status != http.StatusOK {
		t.Errorf("viewer run of hidden view status = %d, want 200, body: %v", status, resp)
	}
}

func TestFormulasDisplay_REST_HiddenRoundTrip(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	createGroceriesTable(t, router, bdbID, userID)

	resp := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "plumbing-rollup",
		"description": "Assistant-only rollup.",
		"sql":         "SELECT COUNT(*) AS total FROM groceries",
		"hidden":      true,
	})
	if resp["hidden"] != true {
		t.Fatalf("hidden after create = %v, want true", resp["hidden"])
	}

	status, resp := testutil.DoRequest(t, router, http.MethodGet,
		formulasPath(bdbID, "plumbing-rollup"), userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("get status = %d, want 200, body: %v", status, resp)
	}
	if resp["hidden"] != true {
		t.Errorf("hidden after get = %v, want true", resp["hidden"])
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPut,
		formulasPath(bdbID, "plumbing-rollup"), userToken(t, userID),
		map[string]any{"hidden": false})
	if status != http.StatusOK {
		t.Fatalf("unhide status = %d, want 200, body: %v", status, resp)
	}
	if resp["hidden"] != false {
		t.Errorf("hidden after update = %v, want false", resp["hidden"])
	}
}

// displayRoleColumn returns the column name holding the given role in a display response's
// columns array ("" when unassigned).
func displayRoleColumn(display map[string]any, role string) string {
	cols, _ := display["columns"].([]any)
	for _, c := range cols {
		m, _ := c.(map[string]any)
		if m["role"] == role {
			name, _ := m["column"].(string)
			return name
		}
	}
	return ""
}

// seedOrderedTasks provisions a position-carrying table with three rows for the reorder tests.
func seedOrderedTasks(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, " +
			"position INTEGER NOT NULL DEFAULT 1)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]any{"sql": "INSERT INTO tasks (id, name, position) VALUES " +
			"(1,'first',1),(2,'second',2),(3,'third',3)"})
}

// TestFormulasDisplay_REST_ReorderSlot pins the reorder slot end to end: the slot resolves and
// normalizes like the others, a run with :ordered_rowids renumbers the position column from the
// array order, and a duplicate id in the order is rejected.
func TestFormulasDisplay_REST_ReorderSlot(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	seedOrderedTasks(t, router, bdbID, userID)
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "task-list",
		"description": "Tasks in their manual order.",
		"sql":         "SELECT rowid AS rowid, name FROM tasks ORDER BY position, id",
	})
	reorder := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "reorder-tasks",
		"description": "Renumbers positions from the dropped order.",
		"sql": "UPDATE tasks SET position = (SELECT je.key + 1 FROM json_each(:ordered_rowids) je " +
			"WHERE je.value = tasks.id) WHERE id IN (SELECT value FROM json_each(:ordered_rowids))",
		"params": []map[string]any{
			{"name": "ordered_rowids", "type": "integer", "source": "reorder"},
		},
		"attached_to_formula_id": view["id"].(string),
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "task-list"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode":    "checklist",
			"columns": []map[string]any{{"column": "name", "role": "title"}},
			"slots":   map[string]any{"reorder": "reorder-tasks"},
		}})
	if status != http.StatusOK {
		t.Fatalf("set display status = %d, want 200, body: %v", status, resp)
	}
	slots, _ := displayOf(t, resp)["slots"].(map[string]any)
	if slots["reorder"] != reorder["id"].(string) {
		t.Errorf("reorder slot = %v, want %v (normalized UUID)", slots["reorder"], reorder["id"])
	}

	// The run renumbers from the array order: third first, first last.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "reorder-tasks"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"ordered_rowids": []int{3, 2, 1}}})
	if status != http.StatusOK {
		t.Fatalf("reorder run status = %d, want 200, body: %v", status, resp)
	}
	status, listed := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "task-list"),
		userToken(t, userID), map[string]any{"params": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("list run status = %d, want 200, body: %v", status, listed)
	}
	rows, _ := listed["rows"].([]any)
	var names []string
	for _, r := range rows {
		cells, _ := r.([]any)
		cell, _ := cells[1].(map[string]any)
		name, _ := cell["value"].(string)
		names = append(names, name)
	}
	if !slices.Equal(names, []string{"third", "second", "first"}) {
		t.Errorf("names after reorder = %v, want the dropped order [third second first]", names)
	}

	// A duplicate id would make the renumbering indeterminate, so the run rejects it.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "reorder-tasks"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"ordered_rowids": []int{3, 3, 1}}})
	if status != http.StatusBadRequest {
		t.Errorf("duplicate-id run status = %d, want 400, body: %v", status, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "twice") {
		t.Errorf("duplicate-id error = %q, want it to name the duplicate", resp["error"])
	}
}

// TestFormulasDisplay_REST_ReorderSlotRejections pins the shapes the reorder slot refuses: a
// non-update action, an action missing :ordered_rowids, the slot outside checklist mode, and the
// slot on a paginated view.
func TestFormulasDisplay_REST_ReorderSlotRejections(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	seedOrderedTasks(t, router, bdbID, userID)
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "task-list",
		"description": "Tasks in their manual order.",
		"sql":         "SELECT rowid AS rowid, name FROM tasks ORDER BY position, id",
	})
	viewID := view["id"].(string)
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "reorder-tasks",
		"description": "Renumbers positions from the dropped order.",
		"sql": "UPDATE tasks SET position = (SELECT je.key + 1 FROM json_each(:ordered_rowids) je " +
			"WHERE je.value = tasks.id) WHERE id IN (SELECT value FROM json_each(:ordered_rowids))",
		"params": []map[string]any{
			{"name": "ordered_rowids", "type": "integer", "source": "reorder"},
		},
		"attached_to_formula_id": viewID,
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":                   "purge-tasks",
		"description":            "Deletes the listed tasks.",
		"sql":                    "DELETE FROM tasks WHERE id IN (SELECT value FROM json_each(:ordered_rowids))",
		"params":                 []map[string]any{{"name": "ordered_rowids", "type": "integer", "source": "reorder"}},
		"attached_to_formula_id": viewID,
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "rename-task",
		"description": "Renames one task.",
		"sql":         "UPDATE tasks SET name = :name WHERE id = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "name", "type": "text", "source": "prompt"},
		},
		"attached_to_formula_id": viewID,
	})

	checklist := func(slots map[string]any) map[string]any {
		return map[string]any{
			"mode":    "checklist",
			"columns": []map[string]any{{"column": "name", "role": "title"}},
			"slots":   slots,
		}
	}
	rejected := []struct {
		name    string
		display map[string]any
	}{
		{"delete-verb action in the reorder slot", checklist(map[string]any{"reorder": "purge-tasks"})},
		{"action without ordered_rowids in the reorder slot", checklist(map[string]any{"reorder": "rename-task"})},
		{"reorder slot outside checklist mode", map[string]any{
			"mode":  "table",
			"slots": map[string]any{"reorder": "reorder-tasks"},
		}},
	}
	for _, tt := range rejected {
		status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "task-list"),
			userToken(t, userID), map[string]any{"display": tt.display})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body: %v", tt.name, status, resp)
		}
	}

	// A paginated view refuses the slot: a drag can only order the rows it can see.
	pagedView := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "task-pages",
		"description": "Tasks a page at a time.",
		"sql": "SELECT rowid AS rowid, name FROM tasks WHERE rowid > :cursor " +
			"ORDER BY rowid LIMIT :limit",
		"params": []map[string]any{
			{"name": "cursor", "type": "integer", "source": "pagination"},
			{"name": "limit", "type": "integer", "source": "pagination"},
		},
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "reorder-pages",
		"description": "Renumbers positions from the dropped order.",
		"sql": "UPDATE tasks SET position = (SELECT je.key + 1 FROM json_each(:ordered_rowids) je " +
			"WHERE je.value = tasks.id) WHERE id IN (SELECT value FROM json_each(:ordered_rowids))",
		"params": []map[string]any{
			{"name": "ordered_rowids", "type": "integer", "source": "reorder"},
		},
		"attached_to_formula_id": pagedView["id"].(string),
	})
	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "task-pages"),
		userToken(t, userID),
		map[string]any{"display": checklist(map[string]any{"reorder": "reorder-pages"})})
	if status != http.StatusBadRequest {
		t.Errorf("paginated view reorder slot: status = %d, want 400, body: %v", status, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "paginated") {
		t.Errorf("paginated view reorder slot error = %q, want it to name the pagination conflict", resp["error"])
	}
}

// seedStatusTasks provisions a status-carrying table with three rows for the board tests.
func seedStatusTasks(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, " +
			"status TEXT NOT NULL DEFAULT 'todo', position INTEGER NOT NULL DEFAULT 1)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]any{"sql": "INSERT INTO tasks (id, name, status, position) VALUES " +
			"(1,'write it','todo',1),(2,'review it','doing',1),(3,'ship it','done',1),(4,'test it','todo',2)"})
}

// TestFormulasDisplay_REST_BoardMoveSlot pins the board's move slot end to end: the slot resolves
// and normalizes like the others, a run with :rowid + the target value re-homes the row, and a
// value outside the target's declared options is rejected at the run layer.
func TestFormulasDisplay_REST_BoardMoveSlot(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	seedStatusTasks(t, router, bdbID, userID)
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "task-board",
		"description": "Tasks by status.",
		"sql":         "SELECT rowid AS rowid, name, status FROM tasks ORDER BY id",
	})
	move := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "move-task",
		"description": "Re-homes a task in another column.",
		"sql":         "UPDATE tasks SET status = :status WHERE id = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "status", "type": "text", "source": "prompt",
				"options": []any{"todo", map[string]any{"value": "doing", "label": "In progress"}, "done"}},
		},
		"attached_to_formula_id": view["id"].(string),
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "task-board"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "board",
			"columns": []map[string]any{
				{"column": "name", "role": "title"},
				{"column": "status", "role": "group"},
			},
			"slots": map[string]any{"move": "move-task"},
		}})
	if status != http.StatusOK {
		t.Fatalf("set display status = %d, want 200, body: %v", status, resp)
	}
	display := displayOf(t, resp)
	slots, _ := display["slots"].(map[string]any)
	if slots["move"] != move["id"].(string) {
		t.Errorf("move slot = %v, want %v (normalized UUID)", slots["move"], move["id"])
	}
	if got := displayRoleColumn(display, "group"); got != "status" {
		t.Errorf("group-role column = %q, want status", got)
	}

	// The drop's run: task 1 lands in the doing column.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "move-task"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 1, "status": "doing"}})
	if status != http.StatusOK {
		t.Fatalf("move run status = %d, want 200, body: %v", status, resp)
	}
	status, listed := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "task-board"),
		userToken(t, userID), map[string]any{"params": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("list run status = %d, want 200, body: %v", status, listed)
	}
	rows, _ := listed["rows"].([]any)
	first, _ := rows[0].([]any)
	statusCell, _ := first[2].(map[string]any)
	if statusCell["value"] != "doing" {
		t.Errorf("task 1 status after move = %v, want doing", statusCell["value"])
	}

	// A destination outside the declared options never reaches the SQL — undeclared columns are
	// not drop targets, and a forged run is rejected the same way.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "move-task"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 1, "status": "archived"}})
	if status != http.StatusBadRequest {
		t.Errorf("off-vocabulary move status = %d, want 400, body: %v", status, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "one of") {
		t.Errorf("off-vocabulary move error = %q, want it to list the options", resp["error"])
	}
}

// TestFormulasDisplay_REST_BoardRejections pins the shapes the board refuses: a config without a
// group-role column, the move slot outside board mode, the toggle slot on a board, a delete-verb
// action in the move slot, and a move action lacking the required options-declaring target.
func TestFormulasDisplay_REST_BoardRejections(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	seedStatusTasks(t, router, bdbID, userID)
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "task-board",
		"description": "Tasks by status.",
		"sql":         "SELECT rowid AS rowid, name, status FROM tasks ORDER BY id",
	})
	viewID := view["id"].(string)
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "move-task",
		"description": "Re-homes a task in another column.",
		"sql":         "UPDATE tasks SET status = :status WHERE id = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "status", "type": "text", "source": "prompt", "options": []any{"todo", "doing", "done"}},
		},
		"attached_to_formula_id": viewID,
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "drop-task",
		"description": "Deletes a task by status choice.",
		"sql":         "DELETE FROM tasks WHERE id = :rowid AND status = :status",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "status", "type": "text", "source": "prompt", "options": []any{"todo", "doing", "done"}},
		},
		"attached_to_formula_id": viewID,
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "rename-task",
		"description": "Renames one task.",
		"sql":         "UPDATE tasks SET name = :name WHERE id = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "name", "type": "text", "source": "prompt"},
		},
		"attached_to_formula_id": viewID,
	})

	board := func(slots map[string]any) map[string]any {
		return map[string]any{
			"mode": "board",
			"columns": []map[string]any{
				{"column": "name", "role": "title"},
				{"column": "status", "role": "group"},
			},
			"slots": slots,
		}
	}
	rejected := []struct {
		name    string
		display map[string]any
	}{
		{"board without a group column", map[string]any{
			"mode":    "board",
			"columns": []map[string]any{{"column": "name", "role": "title"}},
		}},
		{"move slot outside board mode", map[string]any{
			"mode":    "cards",
			"columns": []map[string]any{{"column": "name", "role": "title"}},
			"slots":   map[string]any{"move": "move-task"},
		}},
		{"toggle slot on a board", board(map[string]any{"toggle": "move-task"})},
		{"delete-verb action in the move slot", board(map[string]any{"move": "drop-task"})},
		{"move action without an options-declaring target", board(map[string]any{"move": "rename-task"})},
	}
	for _, tt := range rejected {
		status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "task-board"),
			userToken(t, userID), map[string]any{"display": tt.display})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body: %v", tt.name, status, resp)
		}
	}
}

// TestFormulasDisplay_REST_BoardOrdering pins per-column ordering on a board: the reorder slot
// renumbers one column's subset, a move carrying :ordered_rowids re-homes and places the card in
// one atomic run, and a move omitting it (the filtered-subset case) re-homes without touching
// positions — the renumbering statement no-ops on the NULL.
func TestFormulasDisplay_REST_BoardOrdering(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	seedStatusTasks(t, router, bdbID, userID)
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "task-board",
		"description": "Tasks by status, in their manual order.",
		"sql":         "SELECT rowid AS rowid, name, status FROM tasks ORDER BY position, id",
	})
	viewID := view["id"].(string)
	renumber := "UPDATE tasks SET position = (SELECT je.key + 1 FROM json_each(:ordered_rowids) je " +
		"WHERE je.value = tasks.id) WHERE id IN (SELECT value FROM json_each(:ordered_rowids))"
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "reorder-tasks",
		"description": "Renumbers one column's positions from the dropped order.",
		"sql":         renumber,
		"params": []map[string]any{
			{"name": "ordered_rowids", "type": "integer", "source": "reorder"},
		},
		"attached_to_formula_id": viewID,
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "move-task",
		"description": "Re-homes a task and places it in its new column.",
		"sql":         "UPDATE tasks SET status = :status WHERE id = :rowid; " + renumber,
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "status", "type": "text", "source": "prompt", "options": []any{"todo", "doing", "done"}},
			{"name": "ordered_rowids", "type": "integer", "source": "reorder", "optional": true},
		},
		"attached_to_formula_id": viewID,
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "task-board"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "board",
			"columns": []map[string]any{
				{"column": "name", "role": "title"},
				{"column": "status", "role": "group"},
			},
			"slots": map[string]any{"move": "move-task", "reorder": "reorder-tasks"},
		}})
	if status != http.StatusOK {
		t.Fatalf("set display status = %d, want 200, body: %v", status, resp)
	}

	taskState := func() []string {
		status, listed := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
			userToken(t, userID),
			map[string]any{"sql": "SELECT name || ':' || status || ':' || position FROM tasks ORDER BY id"})
		if status != http.StatusOK {
			t.Fatalf("state query status = %d, body: %v", status, listed)
		}
		rows, _ := listed["rows"].([]any)
		var out []string
		for _, r := range rows {
			cells, _ := r.([]any)
			cell, _ := cells[0].(map[string]any)
			out = append(out, cell["value"].(string))
		}
		return out
	}

	// A drag within the todo column: [test it, write it] — only that column's positions renumber.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "reorder-tasks"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"ordered_rowids": []int{4, 1}}})
	if status != http.StatusOK {
		t.Fatalf("reorder run status = %d, want 200, body: %v", status, resp)
	}
	want := []string{"write it:todo:2", "review it:doing:1", "ship it:done:1", "test it:todo:1"}
	if got := taskState(); !slices.Equal(got, want) {
		t.Errorf("after column reorder = %v, want %v", got, want)
	}

	// A cross-column drop carrying the destination order: one run re-homes 'review it' into todo
	// and renumbers the whole destination column.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "move-task"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 2, "status": "todo", "ordered_rowids": []int{2, 4, 1}}})
	if status != http.StatusOK {
		t.Fatalf("placing move run status = %d, want 200, body: %v", status, resp)
	}
	want = []string{"write it:todo:3", "review it:todo:1", "ship it:done:1", "test it:todo:2"}
	if got := taskState(); !slices.Equal(got, want) {
		t.Errorf("after placing move = %v, want %v", got, want)
	}

	// A drop while the rows are a subset omits the order: the re-home lands, positions stay put.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "move-task"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 3, "status": "doing"}})
	if status != http.StatusOK {
		t.Fatalf("orderless move run status = %d, want 200, body: %v", status, resp)
	}
	want = []string{"write it:todo:3", "review it:todo:1", "ship it:doing:1", "test it:todo:2"}
	if got := taskState(); !slices.Equal(got, want) {
		t.Errorf("after orderless move = %v, want %v", got, want)
	}
}

// seedMeals provisions a day-carrying table for the calendar tests: three meals inside one week, one
// the week after, and one unscheduled.
func seedMeals(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE meals (id INTEGER PRIMARY KEY, dish TEXT, day TEXT, " +
			"position INTEGER NOT NULL DEFAULT 1)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]any{"sql": "INSERT INTO meals (id, dish, day, position) VALUES " +
			"(1,'soup','2026-09-07',1),(2,'pasta','2026-09-07',2),(3,'tacos','2026-09-09',1)," +
			"(4,'cake','2026-09-20',1),(5,'leftovers',NULL,1)"})
}

// mealPlanSQL is the canonical calendar SELECT: the NULL-day guard keeps the unscheduled tray through
// the window test, and an omitted bound (NULL) means no bound.
const mealPlanSQL = "SELECT rowid AS rowid, dish, day FROM meals WHERE day IS NULL OR " +
	"((:window_start IS NULL OR day >= :window_start) AND (:window_end IS NULL OR day <= :window_end)) " +
	"ORDER BY day, position, id"

// windowParams declares the reserved window pair a calendar view must carry.
func windowParams() []map[string]any {
	return []map[string]any{
		{"name": "window_start", "type": "date", "source": "window"},
		{"name": "window_end", "type": "date", "source": "window"},
	}
}

// TestFormulasDisplay_REST_CalendarMoveSlot pins the calendar's drag surfaces end to end: the move slot
// resolves against a date target and normalizes like the others, a drop re-homes the row on the bound day
// and places it from the day's dropped order, a drag within a day renumbers that day alone, a row action
// clears the day back into the unscheduled tray, and a day that is not a calendar day never reaches the SQL.
func TestFormulasDisplay_REST_CalendarMoveSlot(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	seedMeals(t, router, bdbID, userID)
	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "meal-plan",
		"description": "Meals on their days.",
		"sql":         mealPlanSQL,
		"params":      windowParams(),
	})
	viewID := view["id"].(string)
	renumber := "UPDATE meals SET position = (SELECT je.key + 1 FROM json_each(:ordered_rowids) je " +
		"WHERE je.value = meals.id) WHERE id IN (SELECT value FROM json_each(:ordered_rowids))"
	move := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "move-meal",
		"description": "Moves a meal to the dropped day and places it there.",
		"sql":         "UPDATE meals SET day = :day WHERE id = :rowid; " + renumber,
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
			{"name": "day", "type": "date", "source": "prompt"},
			{"name": "ordered_rowids", "type": "integer", "source": "reorder", "optional": true},
		},
		"attached_to_formula_id": viewID,
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "reorder-meals",
		"description": "Renumbers one day's meals from the dropped order.",
		"sql":         renumber,
		"params": []map[string]any{
			{"name": "ordered_rowids", "type": "integer", "source": "reorder"},
		},
		"attached_to_formula_id": viewID,
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "unplan-meal",
		"description": "Sends a meal back to the unscheduled tray.",
		"sql":         "UPDATE meals SET day = NULL WHERE id = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
		},
		"attached_to_formula_id": viewID,
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "meal-plan"),
		userToken(t, userID),
		map[string]any{"display": map[string]any{
			"mode": "calendar",
			"columns": []map[string]any{
				{"column": "dish", "role": "title"},
				{"column": "day", "role": "day"},
			},
			"slots": map[string]any{
				"move": "move-meal", "reorder": "reorder-meals", "row_actions": []string{"unplan-meal"},
			},
		}})
	if status != http.StatusOK {
		t.Fatalf("set display status = %d, want 200, body: %v", status, resp)
	}
	display := displayOf(t, resp)
	slots, _ := display["slots"].(map[string]any)
	if slots["move"] != move["id"].(string) {
		t.Errorf("move slot = %v, want %v (normalized UUID)", slots["move"], move["id"])
	}
	if got := displayRoleColumn(display, "day"); got != "day" {
		t.Errorf("day-role column = %q, want day", got)
	}

	mealState := func() []string {
		status, listed := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
			userToken(t, userID),
			map[string]any{"sql": "SELECT dish || ':' || COALESCE(day, '-') || ':' || position FROM meals ORDER BY id"})
		if status != http.StatusOK {
			t.Fatalf("state query status = %d, body: %v", status, listed)
		}
		rows, _ := listed["rows"].([]any)
		var out []string
		for _, r := range rows {
			cells, _ := r.([]any)
			cell, _ := cells[0].(map[string]any)
			out = append(out, cell["value"].(string))
		}
		return out
	}
	weekDishes := func() []string {
		status, listed := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "meal-plan"),
			userToken(t, userID),
			map[string]any{"params": map[string]any{"window_start": "2026-09-07", "window_end": "2026-09-13"}})
		if status != http.StatusOK {
			t.Fatalf("windowed run status = %d, body: %v", status, listed)
		}
		rows, _ := listed["rows"].([]any)
		var out []string
		for _, r := range rows {
			cells, _ := r.([]any)
			cell, _ := cells[1].(map[string]any)
			out = append(out, cell["value"].(string))
		}
		return out
	}

	// The week shows its three meals plus the tray; the meal a week later stays out.
	if got, want := weekDishes(), []string{"leftovers", "soup", "pasta", "tacos"}; !slices.Equal(got, want) {
		t.Errorf("week dishes = %v, want %v", got, want)
	}

	// A drop from Wednesday onto Monday carrying the day's dropped order: one run re-homes tacos and
	// renumbers the whole destination day.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "move-meal"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 3, "day": "2026-09-07", "ordered_rowids": []int{3, 1, 2}}})
	if status != http.StatusOK {
		t.Fatalf("placing move run status = %d, want 200, body: %v", status, resp)
	}
	want := []string{"soup:2026-09-07:2", "pasta:2026-09-07:3", "tacos:2026-09-07:1", "cake:2026-09-20:1",
		"leftovers:-:1"}
	if got := mealState(); !slices.Equal(got, want) {
		t.Errorf("after placing move = %v, want %v", got, want)
	}

	// A drag within Monday renumbers that day alone.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "reorder-meals"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"ordered_rowids": []int{1, 2, 3}}})
	if status != http.StatusOK {
		t.Fatalf("reorder run status = %d, want 200, body: %v", status, resp)
	}
	want = []string{"soup:2026-09-07:1", "pasta:2026-09-07:2", "tacos:2026-09-07:3", "cake:2026-09-20:1",
		"leftovers:-:1"}
	if got := mealState(); !slices.Equal(got, want) {
		t.Errorf("after day reorder = %v, want %v", got, want)
	}

	// A drop while the rows are a subset omits the order: the re-home lands, positions stay put.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "move-meal"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 4, "day": "2026-09-10"}})
	if status != http.StatusOK {
		t.Fatalf("orderless move run status = %d, want 200, body: %v", status, resp)
	}
	want = []string{"soup:2026-09-07:1", "pasta:2026-09-07:2", "tacos:2026-09-07:3", "cake:2026-09-10:1",
		"leftovers:-:1"}
	if got := mealState(); !slices.Equal(got, want) {
		t.Errorf("after orderless move = %v, want %v", got, want)
	}

	// The tray round trip: the row action clears the day, and the windowed view keeps the row.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "unplan-meal"),
		userToken(t, userID), map[string]any{"params": map[string]any{"rowid": 1}})
	if status != http.StatusOK {
		t.Fatalf("unplan run status = %d, want 200, body: %v", status, resp)
	}
	if got, want := weekDishes(), []string{"soup", "leftovers", "pasta", "tacos", "cake"}; !slices.Equal(got, want) {
		t.Errorf("week dishes after unplan = %v, want %v (the tray leads the ORDER BY)", got, want)
	}

	// A destination that is not a calendar day never reaches the SQL.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "move-meal"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 2, "day": "next monday"}})
	if status != http.StatusBadRequest {
		t.Errorf("non-day move status = %d, want 400, body: %v", status, resp)
	}
}

// TestFormulasDisplay_REST_CalendarRejections pins the shapes a calendar refuses: a view without the
// window pair, a lone bound, a paginated view, a config without a day-role column, the toggle slot, a
// move target that declares options or is not a date, and an add action carrying two date parameters.
func TestFormulasDisplay_REST_CalendarRejections(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	seedMeals(t, router, bdbID, userID)
	calendar := func(columns []map[string]any, slots map[string]any) map[string]any {
		if columns == nil {
			columns = []map[string]any{{"column": "dish", "role": "title"}, {"column": "day", "role": "day"}}
		}
		return map[string]any{"mode": "calendar", "columns": columns, "slots": slots}
	}

	// Views that can never carry the mode: rejected at create, display and all.
	createCases := []struct {
		name   string
		body   map[string]any
		reason string
	}{
		{"no window pair", map[string]any{
			"name": "bare-plan", "description": "No window.",
			"sql":     "SELECT rowid AS rowid, dish, day FROM meals",
			"display": calendar(nil, nil),
		}, "window_start"},
		{"one bound alone", map[string]any{
			"name": "half-plan", "description": "One bound.",
			"sql": "SELECT rowid AS rowid, dish, day FROM meals WHERE :window_start IS NULL OR day >= :window_start",
			"params": []map[string]any{
				{"name": "window_start", "type": "date", "source": "window"},
			},
		}, "window_end"},
		{"paginated view", map[string]any{
			"name": "paged-plan", "description": "Paged.",
			"sql": "SELECT rowid AS rowid, dish, day FROM meals WHERE rowid > :cursor " +
				"AND (:window_start IS NULL OR day >= :window_start) AND (:window_end IS NULL OR day <= :window_end) " +
				"LIMIT :limit",
			"params": append(windowParams(),
				map[string]any{"name": "cursor", "type": "integer", "source": "pagination"},
				map[string]any{"name": "limit", "type": "integer", "source": "pagination"}),
			"display": calendar(nil, nil),
		}, "paginated"},
	}
	for _, tc := range createCases {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
			userToken(t, userID), tc.body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: create status = %d, want 400, body: %v", tc.name, status, resp)
			continue
		}
		if msg, _ := resp["error"].(string); !strings.Contains(msg, tc.reason) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, msg, tc.reason)
		}
	}

	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "meal-plan",
		"description": "Meals on their days.",
		"sql":         mealPlanSQL,
		"params":      windowParams(),
	})
	viewID := view["id"].(string)
	attach := func(name, sql string, params []map[string]any) {
		createFormula(t, router, bdbID, userID, map[string]any{
			"name": name, "description": name + ".", "sql": sql, "params": params,
			"attached_to_formula_id": viewID,
		})
	}
	rowid := map[string]any{"name": "rowid", "type": "integer", "source": "selection-single"}
	attach("move-meal", "UPDATE meals SET day = :day WHERE id = :rowid", []map[string]any{
		rowid, {"name": "day", "type": "date", "source": "prompt"},
	})
	attach("pick-day", "UPDATE meals SET day = :day WHERE id = :rowid", []map[string]any{
		rowid, {"name": "day", "type": "date", "source": "prompt", "options": []any{"2026-09-07", "2026-09-08"}},
	})
	attach("rename-meal", "UPDATE meals SET dish = :dish WHERE id = :rowid", []map[string]any{
		rowid, {"name": "dish", "type": "text", "source": "prompt"},
	})
	attach("plan-twice", "INSERT INTO meals (dish, day) VALUES (:dish, :day), (:dish, :other_day)",
		[]map[string]any{
			{"name": "dish", "type": "text", "source": "prompt"},
			{"name": "day", "type": "date", "source": "prompt", "optional": true},
			{"name": "other_day", "type": "date", "source": "prompt", "optional": true},
		})

	displayCases := []struct {
		name    string
		display map[string]any
		reason  string
	}{
		{"no day column", calendar([]map[string]any{{"column": "dish", "role": "title"}}, nil), "day-role"},
		{"toggle slot", calendar(nil, map[string]any{"toggle": "move-meal"}), "toggle"},
		{"move target with options", calendar(nil, map[string]any{"move": "pick-day"}), "options"},
		{"move target that is not a date", calendar(nil, map[string]any{"move": "rename-meal"}), "date"},
		{"add with two date params", calendar(nil, map[string]any{"add": "plan-twice"}), "date parameters"},
	}
	for _, tc := range displayCases {
		status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "meal-plan"),
			userToken(t, userID), map[string]any{"display": tc.display})
		if status != http.StatusBadRequest {
			t.Errorf("%s: set display status = %d, want 400, body: %v", tc.name, status, resp)
			continue
		}
		if msg, _ := resp["error"].(string); !strings.Contains(msg, tc.reason) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, msg, tc.reason)
		}
	}
}

// TestFormulasDisplay_REST_CalendarWindow pins the calendar window round trip and its rejections: a weekly
// window and a fixed range are stored as written and read back through GET (the stored JSON, not the
// request echo), an update that omits the window drops it, and the invalid shapes are refused: mixing the
// two kinds, an unknown weekday, a length without a start, a length past the ceiling, a start that is
// not a calendar day, and a window on a board.
func TestFormulasDisplay_REST_CalendarWindow(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	seedMeals(t, router, bdbID, userID)
	calendar := func(window map[string]any) map[string]any {
		display := map[string]any{
			"mode":    "calendar",
			"columns": []map[string]any{{"column": "dish", "role": "title"}, {"column": "day", "role": "day"}},
		}
		if window != nil {
			display["window"] = window
		}
		return display
	}
	readWindow := func() map[string]any {
		t.Helper()
		status, resp := testutil.DoRequest(t, router, http.MethodGet, formulasPath(bdbID, "meal-plan"),
			userToken(t, userID), nil)
		if status != http.StatusOK {
			t.Fatalf("get meal-plan status = %d, want 200, body: %v", status, resp)
		}
		display, _ := resp["display"].(map[string]any)
		window, _ := display["window"].(map[string]any)
		return window
	}

	created := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "meal-plan",
		"description": "Meals on their days.",
		"sql":         mealPlanSQL,
		"params":      windowParams(),
		"display":     calendar(map[string]any{"week_start": "sunday", "week_end": "saturday"}),
	})
	display, _ := created["display"].(map[string]any)
	if window, _ := display["window"].(map[string]any); window["week_start"] != "sunday" ||
		window["week_end"] != "saturday" || window["start"] != nil || window["days"] != nil {
		t.Errorf("created window = %v, want the weekly one as written", window)
	}
	if window := readWindow(); window["week_start"] != "sunday" || window["week_end"] != "saturday" {
		t.Errorf("stored window = %v, want the weekly one as written", window)
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "meal-plan"),
		userToken(t, userID), map[string]any{"display": calendar(map[string]any{"start": "2026-09-18", "days": 4})})
	if status != http.StatusOK {
		t.Fatalf("set fixed window status = %d, want 200, body: %v", status, resp)
	}
	if window := readWindow(); window["start"] != "2026-09-18" || window["days"] != float64(4) ||
		window["week_start"] != nil {
		t.Errorf("stored window = %v, want the fixed range as written", window)
	}

	// A start alone is a 7-day range the reader defaults; the stored config keeps only what was written.
	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "meal-plan"),
		userToken(t, userID), map[string]any{"display": calendar(map[string]any{"start": "2026-09-18"})})
	if status != http.StatusOK {
		t.Fatalf("set start-only window status = %d, want 200, body: %v", status, resp)
	}
	if window := readWindow(); window["start"] != "2026-09-18" || window["days"] != nil {
		t.Errorf("stored window = %v, want the start alone", window)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "meal-plan"),
		userToken(t, userID), map[string]any{"display": calendar(nil)})
	if status != http.StatusOK {
		t.Fatalf("clear window status = %d, want 200, body: %v", status, resp)
	}
	if window := readWindow(); window != nil {
		t.Errorf("stored window = %v, want none after an update without one", window)
	}

	rejections := []struct {
		name    string
		display map[string]any
		reason  string
	}{
		{"both kinds", calendar(map[string]any{"week_start": "monday", "start": "2026-09-18"}), "not both"},
		{"unknown weekday", calendar(map[string]any{"week_start": "lundi"}), "weekday"},
		{"days without start", calendar(map[string]any{"days": 4}), "start day"},
		{"days past the ceiling", calendar(map[string]any{"start": "2026-09-18", "days": 32}), "between 1 and 31"},
		{"start that is not a day", calendar(map[string]any{"start": "2026-02-30"}), "YYYY-MM-DD"},
		{"window on a board", map[string]any{
			"mode":    "board",
			"columns": []map[string]any{{"column": "dish", "role": "title"}, {"column": "day", "role": "group"}},
			"window":  map[string]any{"week_start": "sunday"},
		}, "only valid on a calendar"},
	}
	for _, tc := range rejections {
		status, resp := testutil.DoRequest(t, router, http.MethodPut, formulasPath(bdbID, "meal-plan"),
			userToken(t, userID), map[string]any{"display": tc.display})
		if status != http.StatusBadRequest {
			t.Errorf("%s: set display status = %d, want 400, body: %v", tc.name, status, resp)
			continue
		}
		if msg, _ := resp["error"].(string); !strings.Contains(msg, tc.reason) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, msg, tc.reason)
		}
	}
}
