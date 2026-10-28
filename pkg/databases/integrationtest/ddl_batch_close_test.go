//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// dropCascadingParent creates a parent table with two child rows cascading on delete, drops the parent, and returns
// the drop's batch id.
func dropCascadingParent(t *testing.T, router http.Handler, bdbID, token string) string {
	t.Helper()
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE parents (id INTEGER PRIMARY KEY, name TEXT); " +
			"CREATE TABLE children (id INTEGER PRIMARY KEY, " +
			"parent_id INTEGER REFERENCES parents(id) ON DELETE CASCADE, label TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO parents (id, name) VALUES (1, 'root'); " +
			"INSERT INTO children (parent_id, label) VALUES (1, 'a'), (1, 'b')"})

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "DROP TABLE parents"})
	if status != http.StatusOK {
		t.Fatalf("drop status = %d, want %d (body: %v)", status, http.StatusOK, body)
	}
	return body["batch_id"].(string)
}

// familyState renders the parent and child rows as one string for comparisons.
func familyState(t *testing.T, router http.Handler, bdbID, token string) string {
	t.Helper()
	parents := countQuery(t, router, bdbID, token,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'parents'")
	if parents == 0 {
		return fmt.Sprintf("no parents table, children=%v", countQuery(t, router, bdbID, token,
			"SELECT COUNT(*) FROM children"))
	}
	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT p.name, c.label FROM parents p JOIN children c ON c.parent_id = p.id " +
			"ORDER BY c.label"})
	var pairs []any
	for _, raw := range resp["rows"].([]any) {
		pairs = append(pairs, extractRowValues(t, raw))
	}
	return fmt.Sprint(pairs)
}

// TestChange_Undo_DropTableParent_RestoresCascadedChildren pins mixed-batch reversal: undoing a DROP TABLE whose
// foreign-key actions cascaded into a child table re-creates the parent with its rows before re-inserting the child
// rows, redo removes both again, and a further undo restores everything once more.
func TestChange_Undo_DropTableParent_RestoresCascadedChildren(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)
	dropCascadingParent(t, router, bdbID, token)
	if got := familyState(t, router, bdbID, token); got != "no parents table, children=0" {
		t.Fatalf("after the drop: %s", got)
	}

	restored := "[[root a] [root b]]"
	mustReverse(t, router, bdbID, token, "undo")
	if got := familyState(t, router, bdbID, token); got != restored {
		t.Fatalf("after undo: %s, want %s", got, restored)
	}
	// The undo batch is mixed too: the CREATE TABLE unit plus the two child rows its inverse re-inserted.
	_, changes := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes"), token, nil)
	for _, raw := range changes["batches"].([]any) {
		batch := raw.(map[string]any)
		if batch["origin"] != "undo" {
			continue
		}
		if n := len(batch["entries"].([]any)); n != 3 {
			t.Errorf("undo batch entries = %d, want 1 DDL + 2 DML", n)
		}
	}

	mustReverse(t, router, bdbID, token, "redo")
	if got := familyState(t, router, bdbID, token); got != "no parents table, children=0" {
		t.Fatalf("after redo: %s, want the parent dropped and the children cascaded again", got)
	}
	mustReverse(t, router, bdbID, token, "undo")
	if got := familyState(t, router, bdbID, token); got != restored {
		t.Errorf("after the second undo: %s, want %s", got, restored)
	}
}

// TestRevert_Batch_MixedKinds_ByBatchID pins that revert_change by batch id reverses a mixed batch the same way an undo
// does: the parent table comes back with its rows, then the cascaded child rows.
func TestRevert_Batch_MixedKinds_ByBatchID(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)
	dropBatch := dropCascadingParent(t, router, bdbID, token)

	status, body := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, "changes/batch/"+dropBatch+"/revert"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("revert status = %d, want %d (body: %v)", status, http.StatusOK, body)
	}
	if got, want := familyState(t, router, bdbID, token), "[[root a] [root b]]"; got != want {
		t.Errorf("after the revert: %s, want %s", got, want)
	}
}

// TestExecuteSchema_DropTableParent_OwnsCascadedChildRows pins that a DDL batch closes like a DML batch: the child
// rows a foreign-key action deletes while a parent table is dropped are captured by the child's change-log triggers
// and stamped with the DDL batch's id and origin, so nothing is left as __pending__ for the next writer to adopt.
func TestExecuteSchema_DropTableParent_OwnsCascadedChildRows(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	dropBatch := dropCascadingParent(t, router, bdbID, token)

	_, pending := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM _change_log WHERE batch_id = '__pending__'"})
	if c := extractRowValues(t, pending["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("__pending__ rows left after the drop = %v, want 0", c)
	}

	// The two cascaded child deletes belong to the drop's batch, recorded as forward DML rows of origin change.
	_, cascaded := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT batch_id, origin, changed_by FROM _change_log " +
			"WHERE kind = 'DML' AND table_name = 'children' AND operation = 'DELETE' ORDER BY id"})
	rows := cascaded["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("cascaded child DELETE rows = %d, want 2", len(rows))
	}
	for i, row := range rows {
		vals := extractRowValues(t, row)
		if vals[0] != dropBatch || vals[1] != "change" || vals[2] != userID {
			t.Errorf("cascaded row %d = %v, want (%s, change, %s)", i, vals, dropBatch, userID)
		}
	}
}
