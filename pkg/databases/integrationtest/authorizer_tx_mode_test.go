//go:build integration

package integrationtest

import (
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestExecute_UserStatementsRunInTenantModeTransactions pins the invariant behind the trigger-body checks: every
// transaction the query service opens for a user's data or schema request is in tenant mode, so the user's statements
// are prepared under the authorizer at execution and only the pipeline's own bookkeeping bypasses it. The schema
// requests cover the shapes whose pipelines interleave the most platform statements — capture, cascades, trigger
// regeneration — so a platform statement left on the authorized path would fail them. An undo opens an internal
// transaction, since every statement it runs is computed by the platform.
func TestExecute_UserStatementsRunInTenantModeTransactions(t *testing.T) {
	truncateAll(t)
	var modes []bool
	router, _ := setupStackWithOptions(t, stackOptions{onQueryTx: func(internal bool) {
		modes = append(modes, internal)
	}})
	bdbID := uuid.New().String()
	token := userToken(t, uuid.New().String())

	requests := []struct{ kind, sql string }{
		{"schema", "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"},
		{"schema", "ALTER TABLE t ADD COLUMN w TEXT"},
		{"schema", "CREATE INDEX t_v ON t (v)"},
		{"schema", "CREATE TRIGGER t_audit AFTER INSERT ON t BEGIN UPDATE t SET w = 'seen' WHERE id = NEW.id; END"},
		{"schema", "CREATE VIRTUAL TABLE t_fts USING fts5(v)"},
		{"data", "INSERT INTO t (v) VALUES ('x')"},
		{"schema", "ALTER TABLE t DROP COLUMN w"},
		{"schema", "DROP TABLE t"},
	}
	for _, req := range requests {
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, req.kind), token,
			map[string]string{"sql": req.sql})
		if status != http.StatusOK {
			t.Fatalf("%s %q: status = %d, body: %v", req.kind, req.sql, status, resp)
		}
	}
	if len(modes) < len(requests) {
		t.Fatalf("recorded %d transactions for %d requests", len(modes), len(requests))
	}
	if slices.Contains(modes, true) {
		t.Errorf("a user request opened an internal transaction: modes = %v", modes)
	}

	before := len(modes)
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo: status = %d, body: %v", status, resp)
	}
	if undoModes := modes[before:]; len(undoModes) == 0 || slices.Contains(undoModes, false) {
		t.Errorf("undo must run in internal transactions only: modes = %v", undoModes)
	}
}
