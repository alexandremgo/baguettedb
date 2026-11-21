//go:build integration

package integrationtest

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// longDocBody is the SQL expression of a 3,006-character text: 1,500 "a", " MARK " in the middle, then 1,500 "z".
const longDocBody = "replace(printf('%1500s', ''), ' ', 'a') || ' MARK ' || replace(printf('%1500s', ''), ' ', 'z')"

// seedLongDoc creates a docs table in bdbID holding one row whose body is longDocBody, and returns the affected row
// execute_data answers the insert with.
func seedLongDoc(t *testing.T, c *mcpclient.Client, bdbID string) map[string]any {
	t.Helper()
	if r := callTool(t, c, "execute_schema", map[string]any{
		"bdb_id": bdbID, "sql": "CREATE TABLE docs (id INTEGER PRIMARY KEY, title TEXT, body TEXT)",
	}); r.IsError {
		t.Fatalf("create docs: %v", r.Content)
	}
	return oneAffectedRow(t, c, bdbID, "INSERT INTO docs (id, title, body) VALUES (1, 'Plan', "+longDocBody+")")
}

// oneAffectedRow runs sql through execute_data and returns the single affected row of its answer.
func oneAffectedRow(t *testing.T, c *mcpclient.Client, bdbID, sql string) map[string]any {
	t.Helper()
	result := callTool(t, c, "execute_data", map[string]any{"bdb_id": bdbID, "sql": sql})
	if result.IsError {
		t.Fatalf("execute_data %q: %v", sql, result.Content)
	}
	affected, _ := parseToolJSON(t, result)["affected_rows"].([]any)
	if len(affected) != 1 {
		t.Fatalf("execute_data %q: %d affected rows, want 1", sql, len(affected))
	}
	return affected[0].(map[string]any)
}

// wantCutValue fails the test unless v is a cut value of the given full length whose 200-character excerpt holds
// inside, and returns the position the excerpt starts at.
func wantCutValue(t *testing.T, name string, v any, length int, inside string) int {
	t.Helper()
	cut, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want a cut value object", name, v)
	}
	excerpt, _ := cut["excerpt"].(string)
	start, _ := cut["excerpt_start"].(float64)
	if cut["truncated"] != true || cut["length"] != float64(length) || len(excerpt) != 200 ||
		!strings.Contains(excerpt, inside) || start < 1 {
		t.Errorf("%s = %v, want truncated, length %d and a 200-character excerpt holding %q", name, cut, length, inside)
	}
	return int(start)
}

// TestMCP_AffectedRows_ChangedColumnsAndCutValues checks what a write tool answers about the rows it changed: an
// update names the changed columns alone, and a long text comes as its length and an excerpt at the change (from its
// start for an insert or a delete), while short values pass whole.
func TestMCP_AffectedRows_ChangedColumnsAndCutValues(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)

	inserted := seedLongDoc(t, c, bdbID)
	newValues, _ := inserted["new_values"].(map[string]any)
	if newValues["id"] != float64(1) || newValues["title"] != "Plan" {
		t.Errorf("insert new_values = %v, want id and title whole", newValues)
	}
	if start := wantCutValue(t, "inserted body", newValues["body"], 3006, "aaaa"); start != 1 {
		t.Errorf("inserted body excerpt starts at %d, want 1", start)
	}

	patched := oneAffectedRow(t, c, bdbID, "UPDATE docs SET body = text_patch(body, 'MARK', 'DONE') WHERE id = 1")
	oldValues, _ := patched["old_values"].(map[string]any)
	newValues, _ = patched["new_values"].(map[string]any)
	if len(oldValues) != 1 || len(newValues) != 1 {
		t.Fatalf("text edit answers old %v and new %v, want the body alone on each side", oldValues, newValues)
	}
	oldStart := wantCutValue(t, "old body", oldValues["body"], 3006, "a MARK z")
	newStart := wantCutValue(t, "new body", newValues["body"], 3006, "a DONE z")
	if oldStart != newStart {
		t.Errorf("excerpts start at %d and %d, want the same position on both sides", oldStart, newStart)
	}

	retitled := oneAffectedRow(t, c, bdbID, "UPDATE docs SET title = 'Plan B' WHERE id = 1")
	oldValues, _ = retitled["old_values"].(map[string]any)
	newValues, _ = retitled["new_values"].(map[string]any)
	if len(oldValues) != 1 || oldValues["title"] != "Plan" || len(newValues) != 1 || newValues["title"] != "Plan B" {
		t.Errorf("title change answers old %v and new %v, want the title alone on each side", oldValues, newValues)
	}

	unchanged := oneAffectedRow(t, c, bdbID, "UPDATE docs SET title = title WHERE id = 1")
	if unchanged["old_values"] != nil || unchanged["new_values"] != nil {
		t.Errorf("an update changing nothing answers %v, want no values", unchanged)
	}

	deleted := oneAffectedRow(t, c, bdbID, "DELETE FROM docs WHERE id = 1")
	oldValues, _ = deleted["old_values"].(map[string]any)
	if oldValues["title"] != "Plan B" {
		t.Errorf("delete old_values = %v, want the title whole", oldValues)
	}
	wantCutValue(t, "deleted body", oldValues["body"], 3006, "aaaa")
}

// TestMCP_AffectedRows_SmallAnswerFullLog checks that a text edit is answered in a size close to the edit, and that
// list_changes still returns the whole old and new values.
func TestMCP_AffectedRows_SmallAnswerFullLog(t *testing.T) {
	truncateAll(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	c := setupMCPClient(t, userID)
	seedLongDoc(t, c, bdbID)

	answer := toolText(t, c, "execute_data", map[string]any{
		"bdb_id": bdbID, "sql": "UPDATE docs SET body = text_patch(body, 'MARK', 'DONE') WHERE id = 1",
	})
	if len(answer) > 1000 {
		t.Errorf("a four-character edit of a 3,006-character text is answered in %d bytes, want under 1000",
			len(answer))
	}

	batches, _ := parseToolJSON(t, callTool(t, c, "list_changes", map[string]any{
		"bdb_id": bdbID, "kind": "DML", "limit": "1",
	}))["batches"].([]any)
	if len(batches) == 0 {
		t.Fatal("list_changes returned no batch")
	}
	entry := batches[0].(map[string]any)["entries"].([]any)[0].(map[string]any)
	oldBody, _ := entry["old_values"].(map[string]any)["body"].(string)
	newBody, _ := entry["new_values"].(map[string]any)["body"].(string)
	if len(oldBody) != 3006 || len(newBody) != 3006 || !strings.Contains(newBody, " DONE ") {
		t.Errorf("list_changes bodies are %d and %d characters, want both whole (3006)", len(oldBody), len(newBody))
	}
	if entry["old_values"].(map[string]any)["title"] != "Plan" {
		t.Errorf("list_changes old_values = %v, want every column of the row", entry["old_values"])
	}
}

// TestREST_AffectedRows_StayWhole checks that the data endpoint keeps answering with every column and whole values:
// the trimming is the MCP tools' alone.
func TestREST_AffectedRows_StayWhole(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID, bdbID := uuid.New().String(), uuid.New().String()
	token := userToken(t, userID)
	for _, setup := range []struct{ endpoint, sql string }{
		{"schema", "CREATE TABLE docs (id INTEGER PRIMARY KEY, title TEXT, body TEXT)"},
		{"data", "INSERT INTO docs (id, title, body) VALUES (1, 'Plan', " + longDocBody + ")"},
	} {
		if status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, setup.endpoint), token,
			map[string]string{"sql": setup.sql}); status != http.StatusOK {
			t.Fatalf("POST /%s = %d %v", setup.endpoint, status, resp)
		}
	}

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token, map[string]string{
		"sql": "UPDATE docs SET body = text_patch(body, 'MARK', 'DONE') WHERE id = 1",
	})
	if status != http.StatusOK {
		t.Fatalf("text edit = %d %v", status, resp)
	}
	row := resp["affected_rows"].([]any)[0].(map[string]any)
	oldValues, _ := row["old_values"].(map[string]any)
	newValues, _ := row["new_values"].(map[string]any)
	oldBody, _ := oldValues["body"].(string)
	newBody, _ := newValues["body"].(string)
	if len(oldBody) != 3006 || len(newBody) != 3006 || oldValues["title"] != "Plan" || newValues["title"] != "Plan" {
		t.Errorf("REST affected row = old %d characters, new %d characters, titles %v / %v; want whole values and "+
			"every column", len(oldBody), len(newBody), oldValues["title"], newValues["title"])
	}
}
