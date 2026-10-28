//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// seedGroceries creates the groceries table and three rows with known rowids (1,2,3).
func seedGroceries(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE groceries (id INTEGER PRIMARY KEY, name TEXT, quantity INTEGER)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]any{"sql": "INSERT INTO groceries (id, name, quantity) " +
			"VALUES (1,'milk',2),(2,'eggs',1),(3,'bread',3)"})
}

func createFormula(t *testing.T, router http.Handler, bdbID, userID string, body map[string]any) {
	t.Helper()
	status, resp := testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), body)
	if status != http.StatusCreated {
		t.Fatalf("create formula %q status = %d, want 201, body: %v", body["name"], status, resp)
	}
}

func runPath(bdbID, ref string) string {
	return formulasPath(bdbID, ref+"/run")
}

// TestFormulas_Run_TypedOptionsEnforced pins the run-layer closed set on a labeled integer param: a
// declared value binds, one outside the set is rejected listing the accepted values, and a label is
// never a way to address its option — runs speak values only. Both refusals read as the caller's
// value error, naming the parameter, not as a broken formula; an unknown parameter lists the declared ones.
func TestFormulas_Run_TypedOptionsEnforced(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "monthly",
		"description": "Groceries for a month.",
		"sql":         "SELECT rowid, * FROM groceries WHERE :month IS NULL OR :month > 0",
		"params": []map[string]any{{
			"name": "month", "type": "integer", "source": "prompt", "optional": true,
			"options": []any{
				map[string]any{"value": 9, "label": "September"},
				map[string]any{"value": 10, "label": "October"},
			},
		}},
	})

	for _, tt := range []struct {
		label      string
		params     map[string]any
		wantStatus int
		wantErr    string
	}{
		{"a declared value binds", map[string]any{"month": 9}, http.StatusOK, ""},
		{"a value outside the set is rejected", map[string]any{"month": 11}, http.StatusBadRequest,
			`parameter "month" must be one of "9", "10", got 11`},
		{"a label is not a value", map[string]any{"month": "September"}, http.StatusBadRequest,
			`parameter "month" expects integer, got string`},
		{"an unknown parameter lists the declared ones", map[string]any{"mnth": 9}, http.StatusBadRequest,
			`parameter "mnth" is unknown: the formula takes month`},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "monthly"),
			userToken(t, userID), map[string]any{"params": tt.params})
		if status != tt.wantStatus {
			t.Fatalf("%s: run status = %d, want %d, body: %v", tt.label, status, tt.wantStatus, resp)
		}
		if tt.wantErr != "" {
			if msg, _ := resp["error"].(string); msg != tt.wantErr {
				t.Errorf("%s: error = %q, want %q", tt.label, msg, tt.wantErr)
			}
		}
	}
}

// TestFormulas_Run_ConditionalRequiredness pins the two faces of a when-gated required param: while
// its condition is false the run treats it as optional — omitted binds NULL, and a supplied value
// still binds as usual — while its condition is true an omission is rejected as missing.
func TestFormulas_Run_ConditionalRequiredness(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "add-line",
		"description": "Adds a grocery line; quantity only counts while the counted flag is on.",
		"sql": "INSERT INTO groceries (name, quantity) " +
			"VALUES (:name, COALESCE(:qty, CASE WHEN :counted THEN -2 ELSE -1 END))",
		"params": []map[string]any{
			{"name": "name", "type": "text", "source": "prompt"},
			{"name": "counted", "type": "boolean", "source": "prompt", "optional": true},
			{"name": "qty", "type": "integer", "source": "prompt",
				"when": map[string]any{"param": "counted", "value": "true"}},
		},
	})

	for _, tt := range []struct {
		label      string
		params     map[string]any
		wantStatus int
		wantQty    float64
	}{
		{"condition off, omitted binds NULL", map[string]any{"name": "tea"}, http.StatusOK, -1},
		{"condition off, a supplied value still binds", map[string]any{"name": "jam", "counted": false, "qty": 7.0},
			http.StatusOK, 7},
		{"condition on, supplied binds", map[string]any{"name": "rice", "counted": true, "qty": 5.0},
			http.StatusOK, 5},
		{"condition on, omitted is missing", map[string]any{"name": "salt", "counted": true},
			http.StatusBadRequest, 0},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-line"),
			userToken(t, userID), map[string]any{"params": tt.params})
		if status != tt.wantStatus {
			t.Fatalf("%s: run status = %d, want %d, body: %v", tt.label, status, tt.wantStatus, resp)
		}
		if tt.wantStatus != http.StatusOK {
			if msg, _ := resp["error"].(string); !strings.Contains(msg, "qty") {
				t.Errorf("%s: error = %q, want the missing param named", tt.label, msg)
			}
			continue
		}
		name := tt.params["name"].(string)
		_, rows := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
			userToken(t, userID),
			map[string]any{"sql": fmt.Sprintf("SELECT quantity FROM groceries WHERE name = '%s'", name)})
		got, _ := rows["rows"].([]any)
		if len(got) != 1 {
			t.Fatalf("%s: inserted rows = %d, want 1", tt.label, len(got))
		}
		row, _ := got[0].([]any)
		cell, _ := row[0].(map[string]any)
		if cell["value"] != tt.wantQty {
			t.Errorf("%s: quantity = %v, want %v", tt.label, cell["value"], tt.wantQty)
		}
	}
}

// TestFormulas_Run_DefaultBindsWhenOmitted pins the run-layer default: an optional param with a
// declared default binds it when omitted or explicitly null, while a provided value still wins.
func TestFormulas_Run_DefaultBindsWhenOmitted(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "add-line",
		"description": "Adds a grocery line.",
		"sql":         "INSERT INTO groceries (name, quantity) VALUES (:name, :quantity)",
		"params": []map[string]any{
			{"name": "name", "type": "text", "source": "prompt"},
			{"name": "quantity", "type": "integer", "source": "prompt", "optional": true, "default": "3"},
		},
	})

	for _, tt := range []struct {
		label  string
		params map[string]any
		want   float64
	}{
		{"omitted binds the default", map[string]any{"name": "butter"}, 3},
		{"explicit null binds the default", map[string]any{"name": "jam", "quantity": nil}, 3},
		{"a provided value wins", map[string]any{"name": "tea", "quantity": 5}, 5},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-line"),
			userToken(t, userID), map[string]any{"params": tt.params})
		if status != http.StatusOK {
			t.Fatalf("%s: run status = %d, want 200, body: %v", tt.label, status, resp)
		}
		name := tt.params["name"].(string)
		_, rows := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
			userToken(t, userID),
			map[string]any{"sql": fmt.Sprintf("SELECT quantity FROM groceries WHERE name = '%s'", name)})
		got, _ := rows["rows"].([]any)
		if len(got) != 1 {
			t.Fatalf("%s: rows for %q = %v, want 1", tt.label, name, rows["rows"])
		}
		cells, _ := got[0].([]any)
		cell, _ := cells[0].(map[string]any)
		if cell["value"] != tt.want {
			t.Errorf("%s: quantity = %v, want %v", tt.label, cell["value"], tt.want)
		}
	}
}

// --- Views ---

func TestFormulas_Run_ViewWithParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "min-quantity",
		"description": "Items at or above a quantity.",
		"sql":         "SELECT rowid, name FROM groceries WHERE quantity >= :min ORDER BY rowid",
		"params":      []map[string]any{{"name": "min", "type": "integer", "source": "prompt"}},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "min-quantity"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"min": 2}})
	if status != http.StatusOK {
		t.Fatalf("run view status = %d, want 200, body: %v", status, resp)
	}
	rows, _ := resp["rows"].([]any)
	if len(rows) != 2 { // milk(2) and bread(3), not eggs(1)
		t.Errorf("rows = %d, want 2 (quantity >= 2)", len(rows))
	}
}

func TestFormulas_Run_ViewNoParams(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "all-groceries",
		"description": "Everything.",
		"sql":         "SELECT rowid, * FROM groceries ORDER BY rowid",
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "all-groceries"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("run no-param view status = %d, want 200, body: %v", status, resp)
	}
	rows, _ := resp["rows"].([]any)
	if len(rows) != 3 {
		t.Errorf("rows = %d, want 3", len(rows))
	}
}

// --- Actions ---

func TestFormulas_Run_SingleRowAction(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "set-quantity",
		"description": "Set one item's quantity.",
		"sql":         "UPDATE groceries SET quantity = :q WHERE rowid = :rowid",
		"params": []map[string]any{
			{"name": "q", "type": "integer", "source": "prompt"},
			{"name": "rowid", "type": "integer", "source": "selection-single"},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "set-quantity"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"q": 9, "rowid": 1}})
	if status != http.StatusOK {
		t.Fatalf("run action status = %d, want 200, body: %v", status, resp)
	}
	if affected, _ := resp["affected_row_count"].(float64); affected != 1 {
		t.Errorf("affected_row_count = %v, want 1", resp["affected_row_count"])
	}
	if count, _ := resp["statement_count"].(float64); count != 1 {
		t.Errorf("statement_count = %v, want 1 for a single-statement action", resp["statement_count"])
	}
}

func TestFormulas_Run_BulkDeleteViaRowIDs(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "delete-selected",
		"description": "Delete the selected rows.",
		"sql":         "DELETE FROM groceries WHERE rowid IN (:rowids)",
		"params":      []map[string]any{{"name": "rowids", "type": "integer", "source": "selection-bulk"}},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "delete-selected"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowids": []int{2, 3}}})
	if status != http.StatusOK {
		t.Fatalf("run bulk delete status = %d, want 200, body: %v", status, resp)
	}
	if affected, _ := resp["affected_row_count"].(float64); affected != 2 {
		t.Errorf("affected_row_count = %v, want 2", resp["affected_row_count"])
	}
}

// --- Param enforcement ---

func TestFormulas_Run_MissingParamReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "needs-min",
		"description": "x",
		"sql":         "SELECT rowid FROM groceries WHERE quantity >= :min",
		"params":      []map[string]any{{"name": "min", "type": "integer", "source": "prompt"}},
	})

	status, _ := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "needs-min"),
		userToken(t, userID), map[string]any{"params": map[string]any{}})
	if status != http.StatusBadRequest {
		t.Errorf("missing-param run status = %d, want 400", status)
	}
}

func TestFormulas_Run_UnknownParamReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "all-rows",
		"description": "x",
		"sql":         "SELECT rowid FROM groceries",
	})

	status, _ := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "all-rows"),
		userToken(t, userID), map[string]any{"params": map[string]any{"bogus": 1}})
	if status != http.StatusBadRequest {
		t.Errorf("unknown-param run status = %d, want 400", status)
	}
}

// --- Keyset pagination ---

// seedGroceriesN creates the groceries table with n rows (rowids 1..n).
func seedGroceriesN(t *testing.T, router http.Handler, bdbID, userID string, n int) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE groceries (id INTEGER PRIMARY KEY, name TEXT)"})
	for i := 1; i <= n; i++ {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
			userToken(t, userID),
			map[string]any{"sql": fmt.Sprintf("INSERT INTO groceries (id, name) VALUES (%d, 'item%d')", i, i)})
	}
}

// createPaginatedView creates a keyset-paginated view over groceries.
func createPaginatedView(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "browse-groceries",
		"description": "Paginated grocery list.",
		"sql":         "SELECT rowid AS rowid, name FROM groceries WHERE rowid > :cursor ORDER BY rowid LIMIT :limit",
		"params": []map[string]any{
			{"name": "cursor", "type": "integer", "source": "pagination"},
			{"name": "limit", "type": "integer", "source": "pagination"},
		},
	})
}

// runPage runs the paginated view and returns the row count, has_more, and next_cursor.
func runPage(t *testing.T, router http.Handler, bdbID, userID string, cursor, limit int) (int, bool, int64) {
	t.Helper()
	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "browse-groceries"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"cursor": cursor, "limit": limit}})
	if status != http.StatusOK {
		t.Fatalf("run page (cursor=%d limit=%d) status = %d, want 200, body: %v", cursor, limit, status, resp)
	}
	rows, _ := resp["rows"].([]any)
	pag, ok := resp["pagination"].(map[string]any)
	if !ok {
		t.Fatalf("response has no pagination block: %v", resp)
	}
	hasMore, _ := pag["has_more"].(bool)
	nextCursor, _ := pag["next_cursor"].(float64)
	return len(rows), hasMore, int64(nextCursor)
}

func TestFormulas_Run_PaginatedView(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceriesN(t, router, bdbID, userID, 5) // rowids 1..5
	createPaginatedView(t, router, bdbID, userID)

	// Page through 5 rows two at a time: (1,2) more, (3,4) more, (5) done.
	if n, more, next := runPage(t, router, bdbID, userID, 0, 2); n != 2 || !more || next != 2 {
		t.Errorf("page 1: got (rows=%d, more=%v, next=%d), want (2, true, 2)", n, more, next)
	}
	if n, more, next := runPage(t, router, bdbID, userID, 2, 2); n != 2 || !more || next != 4 {
		t.Errorf("page 2: got (rows=%d, more=%v, next=%d), want (2, true, 4)", n, more, next)
	}
	if n, more, next := runPage(t, router, bdbID, userID, 4, 2); n != 1 || more || next != 5 {
		t.Errorf("page 3 (last): got (rows=%d, more=%v, next=%d), want (1, false, 5)", n, more, next)
	}
}

func TestFormulas_Run_PaginationLimitOmittedUsesDefault(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceriesN(t, router, bdbID, userID, 5)
	createPaginatedView(t, router, bdbID, userID)

	// Omit :limit — the default page size (50) covers all 5 rows, so no next page.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "browse-groceries"),
		userToken(t, userID), map[string]any{"params": map[string]any{"cursor": 0}})
	if status != http.StatusOK {
		t.Fatalf("run status = %d, want 200, body: %v", status, resp)
	}
	rows, _ := resp["rows"].([]any)
	pag, _ := resp["pagination"].(map[string]any)
	if len(rows) != 5 {
		t.Errorf("rows = %d, want 5 (default limit covers all)", len(rows))
	}
	if hasMore, _ := pag["has_more"].(bool); hasMore {
		t.Errorf("has_more = true, want false (all rows fit in the default page)")
	}
}

// --- Timeout guardrail ---

func TestFormulas_Run_TimeoutReturns504(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithFormulaTimeout(t, 200*time.Millisecond)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// A view whose SELECT never terminates on its own — a recursive CTE with no stop condition —
	// so the run can only end by hitting the per-run deadline. (Create only prepares it, which is
	// instant; the runaway loop happens at run time.)
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "runaway",
		"description": "Spins until interrupted.",
		"sql":         "WITH RECURSIVE r(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM r) SELECT count(*) FROM r",
	})

	status, _ := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "runaway"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusGatewayTimeout {
		t.Errorf("runaway formula run status = %d, want 504 (timeout)", status)
	}
}

// --- Permission: a viewer may run a view but not an action ---

func TestFormulas_Run_ViewerCanRunView(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	seedGroceries(t, ownerRouter, bdbID, userID)
	createFormula(t, ownerRouter, bdbID, userID, map[string]any{
		"name":        "all-items",
		"description": "Every grocery item.",
		"sql":         "SELECT name, quantity FROM groceries ORDER BY id",
	})

	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)
	status, resp := testutil.DoRequest(t, viewerRouter, http.MethodPost, runPath(bdbID, "all-items"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("viewer running a view: status = %d, want 200, body: %v", status, resp)
	}
	if rows, _ := resp["rows"].([]any); len(rows) != 3 {
		t.Errorf("rows = %d, want 3", len(rows))
	}
}

func TestFormulas_Run_ViewerCannotRunAction(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	seedGroceries(t, ownerRouter, bdbID, userID)
	createFormula(t, ownerRouter, bdbID, userID, map[string]any{
		"name":        "wipe-all",
		"description": "x",
		"sql":         "DELETE FROM groceries",
	})

	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)
	status, _ := testutil.DoRequest(t, viewerRouter, http.MethodPost, runPath(bdbID, "wipe-all"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusForbidden {
		t.Errorf("viewer running an action: status = %d, want 403", status)
	}
}

// --- Param value strictness ---

// createSetQuantity creates the single-selection UPDATE action used by the value-strictness tests.
func createSetQuantity(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "set-qty",
		"description": "Set one item's quantity.",
		"sql":         "UPDATE groceries SET quantity = :q WHERE rowid = :rowid",
		"params": []map[string]any{
			{"name": "q", "type": "integer", "source": "prompt"},
			{"name": "rowid", "type": "integer", "source": "selection-single"},
		},
	})
}

func TestFormulas_Run_NonIntegralIntegerRejected(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)
	createSetQuantity(t, router, bdbID, userID)

	for name, params := range map[string]map[string]any{
		"fractional prompt value": {"q": 2.5, "rowid": 1},
		"fractional rowid":        {"q": 9, "rowid": 1.5},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "set-qty"),
			userToken(t, userID), map[string]any{"params": params})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400, body: %v", name, status, resp)
		}
	}
}

func TestFormulas_Run_WrongTypeParamRejected(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)
	createSetQuantity(t, router, bdbID, userID)

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "set-qty"),
		userToken(t, userID), map[string]any{"params": map[string]any{"q": "lots", "rowid": 1}})
	if status != http.StatusBadRequest {
		t.Errorf("string for integer param: status = %d, want 400, body: %v", status, resp)
	}
}

func TestFormulas_Run_EmptyOrMisdirectedRowIDsRejected(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)
	createSetQuantity(t, router, bdbID, userID)
	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "delete-selected",
		"description": "Delete the selected rows.",
		"sql":         "DELETE FROM groceries WHERE rowid IN (:rowids)",
		"params":      []map[string]any{{"name": "rowids", "type": "integer", "source": "selection-bulk"}},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "delete-selected"),
		userToken(t, userID), map[string]any{"params": map[string]any{"rowids": []int{}}})
	if status != http.StatusBadRequest {
		t.Errorf("empty rowids: status = %d, want 400, body: %v", status, resp)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "set-qty"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"q": 9, "rowid": 1, "rowids": []int{1}}})
	if status != http.StatusBadRequest {
		t.Errorf("rowids on a non-bulk formula: status = %d, want 400, body: %v", status, resp)
	}
}

// --- Pagination enforcement ---

func TestFormulas_Run_PaginatedViewWithoutRowIDColumnRejected(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "names-only",
		"description": "Paginated but forgets to select rowid.",
		"sql":         "SELECT name FROM groceries WHERE rowid > :cursor ORDER BY rowid LIMIT :limit",
		"params": []map[string]any{
			{"name": "cursor", "type": "integer", "source": "pagination"},
			{"name": "limit", "type": "integer", "source": "pagination"},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "names-only"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusBadRequest {
		t.Errorf("paginated view without rowid column: status = %d, want 400, body: %v", status, resp)
	}
}

func TestFormulas_Run_LimitClampedToMax(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	// Seeds 205 rows in one INSERT so the 200-row ceiling is actually exceeded.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE groceries (id INTEGER PRIMARY KEY, name TEXT)"})
	var values strings.Builder
	for i := 1; i <= 205; i++ {
		if i > 1 {
			values.WriteString(",")
		}
		fmt.Fprintf(&values, "(%d, 'item%d')", i, i)
	}
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]any{"sql": "INSERT INTO groceries (id, name) VALUES " + values.String()})

	createPaginatedView(t, router, bdbID, userID)

	count, hasMore, nextCursor := runPage(t, router, bdbID, userID, 0, 500)
	if count != 200 {
		t.Errorf("rows = %d, want 200 (the clamped max page size)", count)
	}
	if !hasMore {
		t.Error("has_more = false, want true (5 rows beyond the clamped page)")
	}
	if nextCursor != 200 {
		t.Errorf("next_cursor = %d, want 200", nextCursor)
	}
}

// --- Boolean params ---

func TestFormulas_Run_BooleanParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "set-flag",
		"description": "Write a boolean into an item's quantity.",
		"sql":         "UPDATE groceries SET quantity = :flag WHERE rowid = :rowid",
		"params": []map[string]any{
			{"name": "flag", "type": "boolean", "source": "prompt", "optional": true},
			{"name": "rowid", "type": "integer", "source": "selection-single"},
		},
	})

	run := func(flag any) (int, map[string]any) {
		return testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "set-flag"),
			userToken(t, userID), map[string]any{"params": map[string]any{"flag": flag, "rowid": 1}})
	}
	readQuantity := func() any {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
			userToken(t, userID), map[string]any{"sql": "SELECT quantity FROM groceries WHERE rowid = 1"})
		if status != http.StatusOK {
			t.Fatalf("read-back query status = %d, want 200, body: %v", status, resp)
		}
		rows, _ := resp["rows"].([]any)
		row, _ := rows[0].([]any)
		cell, _ := row[0].(map[string]any)
		return cell["value"]
	}

	if status, resp := run(true); status != http.StatusOK {
		t.Fatalf("run with true: status = %d, want 200, body: %v", status, resp)
	}
	if v := readQuantity(); v != float64(1) {
		t.Errorf("quantity after true = %v, want 1", v)
	}
	if status, resp := run(false); status != http.StatusOK {
		t.Fatalf("run with false: status = %d, want 200, body: %v", status, resp)
	}
	if v := readQuantity(); v != float64(0) {
		t.Errorf("quantity after false = %v, want 0", v)
	}
	// The flag is optional, so an explicit null binds SQL NULL.
	if status, resp := run(nil); status != http.StatusOK {
		t.Fatalf("run with null: status = %d, want 200, body: %v", status, resp)
	}
	if v := readQuantity(); v != nil {
		t.Errorf("quantity after null = %v, want NULL", v)
	}

	for name, bad := range map[string]any{"numeric 1": 1, "string true": "true"} {
		if status, _ := run(bad); status != http.StatusBadRequest {
			t.Errorf("run with %s: status = %d, want 400 (booleans are strict)", name, status)
		}
	}

	// The required selection param rejects an explicit null.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "set-flag"),
		userToken(t, userID), map[string]any{"params": map[string]any{"flag": true, "rowid": nil}})
	if status != http.StatusBadRequest {
		t.Errorf("null for a required param: status = %d, want 400", status)
	}
}

// TestFormulas_Run_OptionalParam pins the optional-param contract: an omitted optional param binds
// SQL NULL (here through the "omitted means no filter" authoring pattern), a supplied one binds its
// value, and optionality is only declarable on prompt / lookup sources.
func TestFormulas_Run_OptionalParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "stock-filter",
		"description": "Items, optionally filtered by a minimum quantity.",
		"sql":         "SELECT rowid, name FROM groceries WHERE (:min IS NULL OR quantity >= :min) ORDER BY rowid",
		"params":      []map[string]any{{"name": "min", "type": "integer", "source": "prompt", "optional": true}},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "stock-filter"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("run without the optional param: status = %d, want 200, body: %v", status, resp)
	}
	if rows, _ := resp["rows"].([]any); len(rows) != 3 {
		t.Errorf("rows without filter = %d, want 3 (NULL disables the filter)", len(rows))
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "stock-filter"),
		userToken(t, userID), map[string]any{"params": map[string]any{"min": 2}})
	if status != http.StatusOK {
		t.Fatalf("run with the optional param: status = %d, want 200, body: %v", status, resp)
	}
	if rows, _ := resp["rows"].([]any); len(rows) != 2 {
		t.Errorf("rows with min=2 = %d, want 2", len(rows))
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, formulasPath(bdbID, ""),
		userToken(t, userID), map[string]any{
			"name": "bad-optional", "description": "x",
			"sql": "UPDATE groceries SET quantity = 0 WHERE rowid = :rowid",
			"params": []map[string]any{
				{"name": "rowid", "type": "integer", "source": "selection-single", "optional": true},
			},
		})
	if status != http.StatusBadRequest {
		t.Errorf("optional on a selection param: status = %d, want 400, body: %v", status, resp)
	}
}

// --- Multi-statement actions ---

// seedLibrary creates the authors + books pair the multi-statement action tests chain with
// last_insert_rowid(). The schema request itself pushes one undo entry (DDL is undoable), so
// stack assertions compare against a baseline read after seeding rather than zero.
func seedLibrary(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE authors (id INTEGER PRIMARY KEY, name TEXT UNIQUE); " +
			"CREATE TABLE books (id INTEGER PRIMARY KEY, title TEXT, author_id INTEGER)"})
}

// countRows returns COUNT(*) for the given table through the query endpoint.
func countRows(t *testing.T, router http.Handler, bdbID, userID, table string) float64 {
	t.Helper()
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID), map[string]any{"sql": "SELECT COUNT(*) FROM " + table})
	if status != http.StatusOK {
		t.Fatalf("count %s status = %d, want 200, body: %v", table, status, resp)
	}
	c, _ := extractRowValues(t, resp["rows"].([]any)[0])[0].(float64)
	return c
}

func TestFormulas_Run_MultiStatementFKChain(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedLibrary(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "add-camus",
		"description": "Adds the author and one book keyed to it.",
		"sql": "INSERT INTO authors (name) VALUES ('Camus'); " +
			"INSERT INTO books (title, author_id) VALUES ('La Peste', last_insert_rowid())",
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-camus"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("run multi-statement action status = %d, want 200, body: %v", status, resp)
	}
	if affected, _ := resp["affected_row_count"].(float64); affected != 2 {
		t.Errorf("affected_row_count = %v, want 2 (both inserts)", resp["affected_row_count"])
	}

	// The chain held: the book's author_id points at the freshly-inserted author.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]any{"sql": "SELECT COUNT(*) FROM books b JOIN authors a ON a.id = b.author_id " +
			"WHERE a.name = 'Camus' AND b.title = 'La Peste'"})
	if status != http.StatusOK {
		t.Fatalf("join query status = %d, want 200, body: %v", status, resp)
	}
	if c, _ := extractRowValues(t, resp["rows"].([]any)[0])[0].(float64); c != 1 {
		t.Errorf("joined rows = %v, want 1 (author_id chained via last_insert_rowid)", c)
	}
}

func TestFormulas_Run_MultiStatementRollsBackAtomically(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedLibrary(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "twin-authors",
		"description": "Statement 2 violates the UNIQUE(name) constraint.",
		"sql": "INSERT INTO authors (name) VALUES ('Twin'); " +
			"INSERT INTO authors (name) VALUES ('Twin')",
	})
	undoBaseline := countRows(t, router, bdbID, userID, "_undo_stack")

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "twin-authors"),
		userToken(t, userID), map[string]any{})
	if status == http.StatusOK {
		t.Fatalf("run with failing statement 2 status = 200, want an error, body: %v", resp)
	}
	if c := countRows(t, router, bdbID, userID, "authors"); c != 0 {
		t.Errorf("authors after rollback = %v, want 0 (statement 1 must not persist)", c)
	}
	if c := countRows(t, router, bdbID, userID, "_undo_stack"); c != undoBaseline {
		t.Errorf("_undo_stack after failed run = %v, want %v (no push for a rolled-back batch)",
			c, undoBaseline)
	}
}

func TestFormulas_Run_MultiStatementUndoRevertsWholeRun(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedLibrary(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "add-camus",
		"description": "x",
		"sql": "INSERT INTO authors (name) VALUES ('Camus'); " +
			"INSERT INTO books (title, author_id) VALUES ('La Peste', last_insert_rowid())",
	})
	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-camus"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("run status = %d, want 200, body: %v", status, resp)
	}

	// One undo entry covers the whole run: both tables come back empty.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"),
		userToken(t, userID), nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want 200, body: %v", status, resp)
	}
	if c := countRows(t, router, bdbID, userID, "authors"); c != 0 {
		t.Errorf("authors after undo = %v, want 0", c)
	}
	if c := countRows(t, router, bdbID, userID, "books"); c != 0 {
		t.Errorf("books after undo = %v, want 0", c)
	}
}

func TestFormulas_Run_ViewerCannotRunMultiStatementAction(t *testing.T) {
	truncateAll(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	ownerRouter := setupRouterWithRole(t, authz.BdbRoleOwner)
	seedLibrary(t, ownerRouter, bdbID, userID)
	createFormula(t, ownerRouter, bdbID, userID, map[string]any{
		"name":        "add-camus",
		"description": "x",
		"sql": "INSERT INTO authors (name) VALUES ('Camus'); " +
			"INSERT INTO books (title, author_id) VALUES ('La Peste', last_insert_rowid())",
	})

	viewerRouter := setupRouterWithRole(t, authz.BdbRoleViewer)
	status, _ := testutil.DoRequest(t, viewerRouter, http.MethodPost, runPath(bdbID, "add-camus"),
		userToken(t, userID), map[string]any{})
	if status != http.StatusForbidden {
		t.Errorf("viewer running a multi-statement action: status = %d, want 403", status)
	}
}

func TestFormulas_Run_MultiStatementActionWithParams(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedLibrary(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "add-author-and-book",
		"description": "Adds an author, then a book keyed to it.",
		"sql": "INSERT INTO authors (name) VALUES (:author); " +
			"INSERT INTO books (title, author_id) VALUES (:title, last_insert_rowid())",
		"params": []map[string]any{
			{"name": "author", "type": "text", "source": "prompt"},
			{"name": "title", "type": "text", "source": "prompt"},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-author-and-book"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"author": "Camus", "title": "La Peste"}})
	if status != http.StatusOK {
		t.Fatalf("run parameterized multi-statement action status = %d, want 200, body: %v", status, resp)
	}
	if affected, _ := resp["affected_row_count"].(float64); affected != 2 {
		t.Errorf("affected_row_count = %v, want 2", resp["affected_row_count"])
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]any{"sql": "SELECT COUNT(*) FROM books b JOIN authors a ON a.id = b.author_id " +
			"WHERE a.name = 'Camus' AND b.title = 'La Peste'"})
	if status != http.StatusOK {
		t.Fatalf("join query status = %d, want 200, body: %v", status, resp)
	}
	if c, _ := extractRowValues(t, resp["rows"].([]any)[0])[0].(float64); c != 1 {
		t.Errorf("joined rows = %v, want 1 (params bound in their own statements)", c)
	}
}

// TestFormulas_Run_MultiStatementSharedParam runs an action whose :name binds in both statements
// (insert, then update keyed on the same value) — one supplied value fans out per statement.
func TestFormulas_Run_MultiStatementSharedParam(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "add-and-bump",
		"description": "Adds an item, then bumps its quantity.",
		"sql": "INSERT INTO groceries (name, quantity) VALUES (:name, 1); " +
			"UPDATE groceries SET quantity = quantity + :qty WHERE name = :name",
		"params": []map[string]any{
			{"name": "name", "type": "text", "source": "prompt"},
			{"name": "qty", "type": "integer", "source": "prompt"},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "add-and-bump"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"name": "butter", "qty": 4}})
	if status != http.StatusOK {
		t.Fatalf("run shared-param action status = %d, want 200, body: %v", status, resp)
	}
	if affected, _ := resp["affected_row_count"].(float64); affected != 2 {
		t.Errorf("affected_row_count = %v, want 2 (insert + update)", resp["affected_row_count"])
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]any{"sql": "SELECT quantity FROM groceries WHERE name = 'butter'"})
	if status != http.StatusOK {
		t.Fatalf("query status = %d, want 200, body: %v", status, resp)
	}
	if q, _ := extractRowValues(t, resp["rows"].([]any)[0])[0].(float64); q != 5 {
		t.Errorf("butter quantity = %v, want 5 (1 inserted + 4 bumped, same :name in both)", q)
	}
}

// TestFormulas_Run_AttachedMultiStatementRowAction binds a view's single-row selection into a
// two-statement attached action: :rowid feeds both the update and the insert-from-select.
func TestFormulas_Run_AttachedMultiStatementRowAction(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	view := createFormulaAPI(t, router, bdbID, userID, map[string]any{
		"name":        "groceries-list",
		"description": "All groceries.",
		"sql":         "SELECT rowid AS rowid, name, quantity FROM groceries ORDER BY rowid",
	})

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":                   "restock-and-archive",
		"description":            "Restocks the selected row, then snapshots it.",
		"attached_to_formula_id": view["id"],
		"sql": "UPDATE groceries SET quantity = quantity + 10 WHERE rowid = :rowid; " +
			"INSERT INTO groceries (name, quantity) SELECT name || ' (archived)', 99 FROM groceries " +
			"WHERE rowid = :rowid",
		"params": []map[string]any{
			{"name": "rowid", "type": "integer", "source": "selection-single"},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "restock-and-archive"),
		userToken(t, userID),
		map[string]any{"params": map[string]any{"rowid": 1}})
	if status != http.StatusOK {
		t.Fatalf("run attached action status = %d, want 200, body: %v", status, resp)
	}
	if affected, _ := resp["affected_row_count"].(float64); affected != 2 {
		t.Errorf("affected_row_count = %v, want 2 (update + insert)", resp["affected_row_count"])
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]any{"sql": "SELECT quantity FROM groceries WHERE name = 'milk' " +
			"UNION ALL SELECT quantity FROM groceries WHERE name = 'milk (archived)'"})
	if status != http.StatusOK {
		t.Fatalf("query status = %d, want 200, body: %v", status, resp)
	}
	rows, _ := resp["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (restocked original + archived copy)", len(rows))
	}
	if q, _ := extractRowValues(t, rows[0])[0].(float64); q != 12 {
		t.Errorf("milk quantity = %v, want 12 (2 seeded + 10 restocked)", q)
	}
	if q, _ := extractRowValues(t, rows[1])[0].(float64); q != 99 {
		t.Errorf("archived quantity = %v, want 99", q)
	}
}

// seedMembersTally creates a members catalog plus a tally target for multi-select runs.
func seedMembersTally(t *testing.T, router http.Handler, bdbID, userID string) {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"),
		userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT); " +
			"CREATE TABLE tally (member_id INTEGER, tag TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"),
		userToken(t, userID),
		map[string]any{"sql": "INSERT INTO members (id, name) VALUES (1,'Alice'),(2,'Bob'),(3,'Cara')"})
}

// TestFormulas_Run_MultiSelectLookup pins the multi-select bind and its "all" default: a supplied
// array writes one row per picked value, an omitting run resolves "all" to every member the lookup
// table holds, and a required empty selection is rejected before the SQL runs.
func TestFormulas_Run_MultiSelectLookup(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedMembersTally(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "tag-members",
		"description": "Tags the picked members.",
		"sql":         "INSERT INTO tally (member_id, tag) SELECT je.value, :tag FROM json_each(:picked) je",
		"params": []map[string]any{
			{"name": "tag", "type": "text", "source": "prompt"},
			{"name": "picked", "type": "integer", "source": "lookup", "multiple": true, "default": "all",
				"lookup": map[string]any{"table": "members", "label_column": "name", "value_column": "id"}},
		},
	})

	for _, tt := range []struct {
		label       string
		params      map[string]any
		wantStatus  int
		wantErr     string
		wantMembers string
	}{
		{"a picked subset binds in order", map[string]any{"tag": "subset", "picked": []any{3, 1}},
			http.StatusOK, "", "3,1"},
		{"omitted resolves the all default", map[string]any{"tag": "everyone"},
			http.StatusOK, "", "1,2,3"},
		{"an explicit empty selection is rejected", map[string]any{"tag": "none", "picked": []any{}},
			http.StatusBadRequest, "at least one value", ""},
		{"a non-array is rejected", map[string]any{"tag": "scalar", "picked": 3},
			http.StatusBadRequest, "expects an array of values", ""},
		{"a mistyped element is rejected", map[string]any{"tag": "mistyped", "picked": []any{"Alice"}},
			http.StatusBadRequest, "expects integer", ""},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "tag-members"),
			userToken(t, userID), map[string]any{"params": tt.params})
		if status != tt.wantStatus {
			t.Fatalf("%s: run status = %d, want %d, body: %v", tt.label, status, tt.wantStatus, resp)
		}
		if tt.wantErr != "" {
			if msg, _ := resp["error"].(string); !strings.Contains(msg, tt.wantErr) {
				t.Errorf("%s: error = %q, want it to mention %q", tt.label, msg, tt.wantErr)
			}
			continue
		}
		tag := tt.params["tag"].(string)
		_, rows := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
			userToken(t, userID),
			map[string]any{"sql": fmt.Sprintf(
				"SELECT group_concat(member_id) FROM (SELECT member_id FROM tally WHERE tag = '%s' ORDER BY rowid)",
				tag)})
		got, _ := rows["rows"].([]any)
		row, _ := got[0].([]any)
		cell, _ := row[0].(map[string]any)
		if cell["value"] != tt.wantMembers {
			t.Errorf("%s: tagged members = %v, want %q", tt.label, cell["value"], tt.wantMembers)
		}
	}
}

// TestFormulas_Run_MultiSelectOptionsArrayDefault pins the options multi-select and the array default:
// an omitting run binds the declared array, a supplied array wins over it (case-folded to the declared
// spelling), an explicit empty array on an optional filter binds '[]' (read as "no constraint"), and an
// element off the vocabulary is rejected before the SQL runs.
func TestFormulas_Run_MultiSelectOptionsArrayDefault(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, habit TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, userID),
		map[string]any{"sql": "INSERT INTO items (name, habit) VALUES ('milk','staple'),('cake','one-off')," +
			"('candles','occasional')"})

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "items-by-habit",
		"description": "Items narrowed to the picked habits; none picked means every habit.",
		"sql": "SELECT name FROM items WHERE json_array_length(:habits) = 0 " +
			"OR habit IN (SELECT je.value FROM json_each(:habits) je) ORDER BY name",
		"params": []map[string]any{
			{"name": "habits", "type": "text", "source": "prompt", "optional": true, "multiple": true,
				"options": []string{"staple", "occasional", "one-off"}, "defaults": []any{"staple"}},
		},
	})

	for _, tt := range []struct {
		label      string
		params     map[string]any
		wantStatus int
		wantErr    string
		wantNames  string
	}{
		{"omitted binds the declared array", map[string]any{}, http.StatusOK, "", "milk"},
		{"a supplied array wins, case-folded", map[string]any{"habits": []any{"One-Off", "occasional"}},
			http.StatusOK, "", "cake,candles"},
		{"an explicit empty array means no constraint", map[string]any{"habits": []any{}},
			http.StatusOK, "", "cake,candles,milk"},
		{"an element off the vocabulary is rejected", map[string]any{"habits": []any{"weekly"}},
			http.StatusBadRequest, "must be one of", ""},
	} {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "items-by-habit"),
			userToken(t, userID), map[string]any{"params": tt.params})
		if status != tt.wantStatus {
			t.Fatalf("%s: run status = %d, want %d, body: %v", tt.label, status, tt.wantStatus, resp)
		}
		if tt.wantErr != "" {
			if msg, _ := resp["error"].(string); !strings.Contains(msg, tt.wantErr) {
				t.Errorf("%s: error = %q, want it to mention %q", tt.label, msg, tt.wantErr)
			}
			continue
		}
		rows, _ := resp["rows"].([]any)
		names := make([]string, 0, len(rows))
		for _, r := range rows {
			row, _ := r.([]any)
			cell, _ := row[0].(map[string]any)
			name, _ := cell["value"].(string)
			names = append(names, name)
		}
		if got := strings.Join(names, ","); got != tt.wantNames {
			t.Errorf("%s: names = %q, want %q", tt.label, got, tt.wantNames)
		}
	}
}

// TestFormulas_Run_MultiSelectLookupArrayDefault pins the array default on a lookup multi-select: an
// omitting run binds exactly the declared ids, in order, through the same element coercion a caller's
// array gets.
func TestFormulas_Run_MultiSelectLookupArrayDefault(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedMembersTally(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "tag-usual",
		"description": "Tags the usual members unless told otherwise.",
		"sql":         "INSERT INTO tally (member_id, tag) SELECT je.value, :tag FROM json_each(:picked) je",
		"params": []map[string]any{
			{"name": "tag", "type": "text", "source": "prompt"},
			{"name": "picked", "type": "integer", "source": "lookup", "multiple": true, "defaults": []any{3, 1},
				"lookup": map[string]any{"table": "members", "label_column": "name", "value_column": "id"}},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "tag-usual"),
		userToken(t, userID), map[string]any{"params": map[string]any{"tag": "usual"}})
	if status != http.StatusOK {
		t.Fatalf("run status = %d, want 200, body: %v", status, resp)
	}
	_, rows := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, userID),
		map[string]any{"sql": "SELECT group_concat(member_id) FROM (SELECT member_id FROM tally ORDER BY rowid)"})
	got, _ := rows["rows"].([]any)
	row, _ := got[0].([]any)
	cell, _ := row[0].(map[string]any)
	if cell["value"] != "3,1" {
		t.Errorf("tagged members = %v, want the declared array in its order", cell["value"])
	}
}

// TestFormulas_Run_CalendarWindowBinds pins the calendar window pair on a run: bound from and to narrow the
// rows to the range, an omitting run binds NULL on both so the view comes back unfiltered, and a bound that
// is not a calendar day is rejected before the SQL runs.
func TestFormulas_Run_CalendarWindowBinds(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "CREATE TABLE meals (id INTEGER PRIMARY KEY, dish TEXT, day TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, userID),
		map[string]any{"sql": "INSERT INTO meals (dish, day) VALUES ('soup','2026-09-07')," +
			"('pasta','2026-09-10'),('cake','2026-09-20'),('leftovers',NULL)"})

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "meal-plan",
		"description": "Meals on their days.",
		// A NULL day must survive the window (it is the unscheduled tray), so the range test is
		// guarded by IS NULL rather than folded into comparisons NULL would fail.
		"sql": "SELECT id AS rowid, dish, day FROM meals WHERE day IS NULL OR " +
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

	dishes := func(params map[string]any) (int, []string, map[string]any) {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "meal-plan"),
			userToken(t, userID), map[string]any{"params": params})
		rows, _ := resp["rows"].([]any)
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			row, _ := r.([]any)
			cell, _ := row[1].(map[string]any)
			dish, _ := cell["value"].(string)
			out = append(out, dish)
		}
		return status, out, resp
	}

	status, got, resp := dishes(map[string]any{"window_start": "2026-09-07", "window_end": "2026-09-13"})
	if status != http.StatusOK {
		t.Fatalf("windowed run status = %d, body: %v", status, resp)
	}
	if want := []string{"leftovers", "soup", "pasta"}; !slices.Equal(got, want) {
		t.Errorf("windowed dishes = %v, want %v — the week's meals plus the unscheduled one", got, want)
	}

	status, got, resp = dishes(map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("unwindowed run status = %d, body: %v", status, resp)
	}
	if len(got) != 4 {
		t.Errorf("unwindowed dishes = %v, want all four — omitted bounds bind NULL", got)
	}

	status, _, resp = dishes(map[string]any{"window_start": "next monday", "window_end": "2026-09-13"})
	if status != http.StatusBadRequest {
		t.Errorf("run with a non-date bound: status = %d, want 400, body: %v", status, resp)
	}
}

// TestFormulas_Run_MultiSelectRequiredWithoutDefault pins the no-default face: omitting a required
// multi-select that declares no "all" default is a missing value, named in the error.
func TestFormulas_Run_MultiSelectRequiredWithoutDefault(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedMembersTally(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "tag-picked",
		"description": "Tags the picked members.",
		"sql":         "INSERT INTO tally (member_id, tag) SELECT je.value, :tag FROM json_each(:picked) je",
		"params": []map[string]any{
			{"name": "tag", "type": "text", "source": "prompt"},
			{"name": "picked", "type": "integer", "source": "lookup", "multiple": true,
				"lookup": map[string]any{"table": "members", "label_column": "name", "value_column": "id"}},
		},
	})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "tag-picked"),
		userToken(t, userID), map[string]any{"params": map[string]any{"tag": "nobody"}})
	if status != http.StatusBadRequest {
		t.Fatalf("run status = %d, want 400, body: %v", status, resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "picked") {
		t.Errorf("error = %q, want the missing param named", msg)
	}
}

// TestFormulas_Run_SQLNoLongerValidReturns400 checks that running a formula whose SQL no longer prepares against the
// schema (a column it reads was renamed since) fails as a 400 carrying SQLite's message, without internal labels.
func TestFormulas_Run_SQLNoLongerValidReturns400(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	seedGroceries(t, router, bdbID, userID)

	createFormula(t, router, bdbID, userID, map[string]any{
		"name":        "names",
		"description": "Every grocery name.",
		"sql":         "SELECT rowid, name FROM groceries ORDER BY rowid",
	})
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]any{"sql": "ALTER TABLE groceries RENAME COLUMN name TO label"})
	if status != http.StatusOK {
		t.Fatalf("rename column = %d %v", status, resp)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodPost, runPath(bdbID, "names"), userToken(t, userID),
		map[string]any{})
	if status != http.StatusBadRequest || resp["error"] != "formula SQL is invalid: no such column: name" {
		t.Errorf("run after the rename = %d %v, want 400 with SQLite's message", status, resp)
	}
}
