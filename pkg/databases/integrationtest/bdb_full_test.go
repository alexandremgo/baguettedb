//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// smallBdbRouter builds a stack whose bdbs are capped at 512 KiB, small enough to fill in a few dozen writes.
func smallBdbRouter(t *testing.T) chi.Router {
	t.Helper()
	router, _ := setupStackWithOptions(t, stackOptions{resolver: quota.Fixed(quota.Limits{
		MaxDBSizeBytes:          512 * 1024,
		MaxStatementsPerRequest: 100,
		MaxRowsPerRequest:       1000,
		MaxDBExportsPerWindow:   1,
		DBExportWindow:          24 * time.Hour,
	})})
	return router
}

// fillBdb inserts 4 KiB text rows into a filler table until the bdb refuses one, and returns that refusal's body.
func fillBdb(t *testing.T, router http.Handler, bdbID, token string) map[string]any {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE filler (v TEXT)"})
	for range 400 {
		status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
			map[string]string{"sql": "INSERT INTO filler (v) VALUES (hex(randomblob(2048)))"})
		if status == http.StatusOK {
			continue
		}
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("filling refused with status %d, want %d (body: %v)", status, http.StatusUnprocessableEntity, body)
		}
		return body
	}
	t.Fatal("the bdb never refused a write; the cap did not apply")
	return nil
}

// assertFullMessage checks that a refusal explains the size limit in the platform's words rather than SQLite's.
func assertFullMessage(t *testing.T, msg string) {
	t.Helper()
	if !strings.Contains(msg, "size limit") || strings.Contains(msg, "disk is full") {
		t.Errorf("error = %q, want the size-limit wording and none of SQLite's", msg)
	}
}

// TestExecuteData_BdbFullIsRefusedClearly pins that a write refused by the bdb's page cap surfaces as a 422 whose
// message explains the size limit instead of leaking SQLite's "database or disk is full".
func TestExecuteData_BdbFullIsRefusedClearly(t *testing.T) {
	truncateAll(t)
	router := smallBdbRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	body := fillBdb(t, router, bdbID, token)
	assertFullMessage(t, body["error"].(string))
}

// TestMCP_ExecuteData_BdbFullIsRefusedClearly is the MCP twin: the tool error carries the size-limit wording.
func TestMCP_ExecuteData_BdbFullIsRefusedClearly(t *testing.T) {
	truncateAll(t)
	router := smallBdbRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	c := startMCPClient(t, router, userID)

	callTool(t, c, "execute_schema", map[string]any{"bdb_id": bdbID, "sql": "CREATE TABLE filler (v TEXT)"})
	for range 400 {
		r := callTool(t, c, "execute_data", map[string]any{
			"bdb_id": bdbID, "sql": "INSERT INTO filler (v) VALUES (hex(randomblob(2048)))",
		})
		if !r.IsError {
			continue
		}
		text, ok := r.Content[0].(mcp.TextContent)
		if !ok {
			t.Fatalf("expected TextContent, got %T", r.Content[0])
		}
		assertFullMessage(t, text.Text)
		return
	}
	t.Fatal("the bdb never refused a write; the cap did not apply")
}

// TestRevert_DropColumnRefusedWhenBdbFull pins that a reversal needing room — restoring a dropped column rebuilds
// the table, so the bdb must hold a second copy of it — is refused with the size-limit wording once the bdb is full,
// and leaves the schema untouched.
func TestRevert_DropColumnRefusedWhenBdbFull(t *testing.T) {
	truncateAll(t)
	router := smallBdbRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, note TEXT)"})
	for range 8 {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
			map[string]string{"sql": "INSERT INTO items (note) VALUES (hex(randomblob(2048)))"})
	}
	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items DROP COLUMN note"})
	if status != http.StatusOK {
		t.Fatalf("drop column status = %d (body: %v)", status, body)
	}
	_, idResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DDL' AND ddl_subtype = 'DropColumn'"})
	dropID := int64(extractRowValues(t, idResp["rows"].([]any)[0])[0].(float64))

	fillBdb(t, router, bdbID, token)

	status, body = testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%d/revert", dropID)), token, nil)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("revert status = %d, want %d (body: %v)", status, http.StatusUnprocessableEntity, body)
	}
	assertFullMessage(t, body["error"].(string))
	if c := countQuery(t, router, bdbID, token,
		"SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'note'"); c != 0 {
		t.Errorf("note column present after the refused revert = %v, want 0 (rolled back)", c)
	}
}
