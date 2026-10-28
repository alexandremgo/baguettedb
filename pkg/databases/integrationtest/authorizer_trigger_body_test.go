//go:build integration

package integrationtest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestExecute_TenantTriggerBodyCannotWriteReservedTables covers the trigger-body path: SQLite compiles a trigger
// body only when the firing statement is prepared, so creating the trigger succeeds (it targets a user object) and
// the denial has to land on the statement that fires it. The reserved table it targets must be left untouched.
//
// The bodies forge rows with UPDATE rather than DELETE: a DELETE on _change_log cascades into _change_snapshots
// through a foreign key, and the cascade runs as a nameless trigger that the top-level rules already catch.
func TestExecute_TenantTriggerBodyCannotWriteReservedTables(t *testing.T) {
	tests := []struct {
		name     string
		setup    []string
		firing   string
		tampered string
	}{
		{
			name: "AFTER INSERT trigger on a table forging _change_log batches",
			setup: []string{
				"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
				"CREATE TRIGGER evil AFTER INSERT ON t BEGIN UPDATE _change_log SET batch_id = 'forged'; END",
			},
			firing:   "INSERT INTO t (v) VALUES ('x')",
			tampered: "SELECT count(*) FROM _change_log WHERE batch_id = 'forged'",
		},
		{
			name: "INSTEAD OF INSERT trigger on a view hijacking _undo_stack entries",
			setup: []string{
				"CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)",
				"CREATE VIEW tv AS SELECT id, v FROM t",
				"CREATE TRIGGER evil INSTEAD OF INSERT ON tv BEGIN UPDATE _undo_stack SET user_id = 'forged'; END",
			},
			firing:   "INSERT INTO tv (v) VALUES ('x')",
			tampered: "SELECT count(*) FROM _undo_stack WHERE user_id = 'forged'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			truncateAll(t)
			router := setupRouter(t)
			bdbID := uuid.New().String()
			token := userToken(t, uuid.New().String())

			for _, sql := range tt.setup {
				status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
					map[string]string{"sql": sql})
				if status != http.StatusOK {
					t.Fatalf("schema %q: status = %d, want %d, body: %v", sql, status, http.StatusOK, resp)
				}
			}

			status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
				map[string]string{"sql": tt.firing})
			if status != http.StatusForbidden {
				t.Errorf("firing statement: status = %d, want %d, body: %v", status, http.StatusForbidden, resp)
			}
			if n := countViaQuery(t, router, token, bdbID, tt.tampered); n != 0 {
				t.Errorf("%q = %v, want 0 (the trigger body must not have run)", tt.tampered, n)
			}
		})
	}
}

// countViaQuery runs a single-count query through the query endpoint and returns the number it yields (reads on
// reserved tables are always allowed).
func countViaQuery(t *testing.T, router http.Handler, token, bdbID, countSQL string) float64 {
	t.Helper()
	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": countSQL})
	if status != http.StatusOK {
		t.Fatalf("%q: status = %d, body: %v", countSQL, status, resp)
	}
	rows, _ := resp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("%q: rows = %v, want one row", countSQL, resp["rows"])
	}
	n, ok := extractRowValues(t, rows[0])[0].(float64)
	if !ok {
		t.Fatalf("%q: count cell is not a number: %v", countSQL, rows[0])
	}
	return n
}
