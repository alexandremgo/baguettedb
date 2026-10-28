//go:build integration

package integrationtest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestREST_QueryBoundArgs pins the request-side bound-args contract: positional values travel in
// the same {type, value} wire shape responses use, are bound (never spliced) into the SQL, and
// malformed args are rejected before execution.
func TestREST_QueryBoundArgs(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, userID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, userID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk'), ('bread'), ('oat milk')"})

	// A bound text arg filters without any SQL splicing — quotes in the term are data, not syntax.
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]any{
			"sql":  "SELECT name FROM items WHERE name LIKE '%' || ? || '%' ORDER BY name",
			"args": []map[string]any{{"type": "text", "value": "il"}},
		})
	if status != http.StatusOK {
		t.Fatalf("text-arg query status = %d, want 200, body: %v", status, resp)
	}
	rows, _ := resp["rows"].([]any)
	if len(rows) != 2 {
		t.Errorf("text-arg query rows = %d, want 2 (milk, oat milk)", len(rows))
	}

	// An integer arg binds as the integer storage class.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
		userToken(t, userID),
		map[string]any{
			"sql":  "SELECT name FROM items WHERE id = ?",
			"args": []map[string]any{{"type": "integer", "value": 2}},
		})
	if status != http.StatusOK {
		t.Fatalf("integer-arg query status = %d, want 200, body: %v", status, resp)
	}
	if rows, _ := resp["rows"].([]any); len(rows) != 1 {
		t.Errorf("integer-arg query rows = %d, want 1", len(rows))
	}

	rejections := []struct {
		name string
		arg  map[string]any
	}{
		{"unsupported type", map[string]any{"type": "blob", "value": "AAA="}},
		{"non-integral integer", map[string]any{"type": "integer", "value": 1.5}},
		{"non-string text", map[string]any{"type": "text", "value": 7}},
	}
	for _, tt := range rejections {
		t.Run(tt.name, func(t *testing.T) {
			status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"),
				userToken(t, userID),
				map[string]any{
					"sql":  "SELECT name FROM items WHERE name = ?",
					"args": []map[string]any{tt.arg},
				})
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400, body: %v", status, resp)
			}
		})
	}
}
