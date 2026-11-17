//go:build integration

package integrationtest

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestREST_GetManual checks the manual route: a signed-in user gets a manual in Markdown under its topic, the Data
// API's included; an unknown topic is a 404 naming the topics; and a request with no token is refused.
func TestREST_GetManual(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	token := userToken(t, uuid.NewString())

	status, resp := testutil.DoRequest(t, router, http.MethodGet, "/api/databases/v1/docs/data-api", token, nil)
	content, _ := resp["content"].(string)
	if status != http.StatusOK || resp["topic"] != "data-api" || !strings.HasPrefix(content, "# Data API manual") {
		t.Errorf("GET the data-api manual = %d, topic %v, content starting %.40q; want 200 and the manual", status,
			resp["topic"], content)
	}

	status, resp = testutil.DoRequest(t, router, http.MethodGet, "/api/databases/v1/docs/recipes", token, nil)
	if message, _ := resp["error"].(string); status != http.StatusNotFound ||
		!strings.Contains(message, "data-api, formulas, sql") {
		t.Errorf("GET an unknown manual = %d %v, want 404 naming the topics", status, resp)
	}

	if status, _ := testutil.DoRequest(t, router, http.MethodGet, "/api/databases/v1/docs/data-api", "",
		nil); status != http.StatusUnauthorized {
		t.Errorf("GET a manual with no token = %d, want 401", status)
	}
}

// TestREST_ListFormulas_Columns checks that the formula listing gives each view its result columns when asked with
// ?columns=true, an action none, and that a listing not asking gives none at all.
func TestREST_ListFormulas_Columns(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID, bdbID := uuid.NewString(), uuid.NewString()
	token := userToken(t, userID)
	seedGroceries(t, router, bdbID, userID)
	createFormula(t, router, bdbID, userID, map[string]any{
		"name": "stock", "description": "What is in stock", "sql": "SELECT id AS rowid, name, quantity FROM groceries",
	})
	createFormula(t, router, bdbID, userID, map[string]any{
		"name": "restock", "description": "Adds one of each", "sql": "UPDATE groceries SET quantity = quantity + 1",
	})

	columnsOf := func(path string) map[string][]string {
		t.Helper()
		status, formulas := testutil.DoRequestList(t, router, http.MethodGet, path, token, nil)
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, status)
		}
		columns := make(map[string][]string, len(formulas))
		for _, f := range formulas {
			name, _ := f["name"].(string)
			raw, _ := f["result_columns"].([]any)
			for _, c := range raw {
				column, _ := c.(string)
				columns[name] = append(columns[name], column)
			}
		}
		return columns
	}

	asked := columnsOf(formulasPath(bdbID, "") + "?columns=true")
	if !slices.Equal(asked["stock"], []string{"rowid", "name", "quantity"}) || asked["restock"] != nil {
		t.Errorf("listing with columns = %v, want the view's three columns and none for the action", asked)
	}
	if plain := columnsOf(formulasPath(bdbID, "")); plain["stock"] != nil {
		t.Errorf("listing without asking = %v, want no columns", plain)
	}
}
