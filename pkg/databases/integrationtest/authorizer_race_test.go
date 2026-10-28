//go:build integration

package integrationtest

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestExecute_TriggerCreatedAfterClassificationIsStillDenied covers the window between a request's classification
// pass and the transaction that runs its statements. SQLite compiles trigger bodies when the firing statement is
// prepared, so a trigger created in that window is first seen at execution time, and its body must be authorized
// there as well. The stack's transaction hook plays the concurrent request: it creates a trigger that forges
// _change_log rows once the INSERT has already been classified as harmless.
func TestExecute_TriggerCreatedAfterClassificationIsStillDenied(t *testing.T) {
	truncateAll(t)
	bdbID := uuid.New().String()
	token := userToken(t, uuid.New().String())

	var router http.Handler
	armed := false
	router, _ = setupStackWithOptions(t, stackOptions{onQueryTx: func(bool) {
		if !armed {
			return
		}
		// Disarms before acting: the trigger's own DDL request opens a transaction through this same hook.
		armed = false
		status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
			map[string]string{
				"sql": "CREATE TRIGGER evil AFTER INSERT ON t BEGIN UPDATE _change_log SET batch_id = 'forged'; END",
			})
		if status != http.StatusOK {
			t.Fatalf("creating the trigger inside the window: status = %d, body: %v", status, resp)
		}
	}})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)"})
	if status != http.StatusOK {
		t.Fatalf("schema: status = %d, body: %v", status, resp)
	}
	// A committed change-log row gives the forged UPDATE something to hit whatever order the triggers fire in.
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO t (v) VALUES ('before')"})
	if status != http.StatusOK {
		t.Fatalf("first insert: status = %d, body: %v", status, resp)
	}

	armed = true
	status, resp = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO t (v) VALUES ('raced')"})
	if status != http.StatusForbidden {
		t.Errorf("raced insert: status = %d, want %d, body: %v", status, http.StatusForbidden, resp)
	}
	forged := "SELECT count(*) FROM _change_log WHERE batch_id = 'forged'"
	if n := countViaQuery(t, router, token, bdbID, forged); n != 0 {
		t.Errorf("%q = %v, want 0 (the trigger body must not have run)", forged, n)
	}
	if n := countViaQuery(t, router, token, bdbID, "SELECT count(*) FROM t"); n != 1 {
		t.Errorf("rows in t = %v, want 1 (the raced INSERT must roll back)", n)
	}
}
