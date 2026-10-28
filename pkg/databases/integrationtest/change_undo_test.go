//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestChange_Undo_DMLBatch_StillWorks pins the legacy DML undo behavior
// under the unified change-log flow: a multi-row INSERT batch is undone
// by reverting the row writes; the batch moves from the undo stack to
// the redo stack.
func TestChange_Undo_DMLBatch_StillWorks(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// One DML batch with three rows.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk'); " +
			"INSERT INTO items (name) VALUES ('eggs'); " +
			"INSERT INTO items (name) VALUES ('bread')"})

	// Confirms the three rows are there.
	_, before := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	if c := extractRowValues(t, before["rows"].([]any)[0])[0]; c != float64(3) {
		t.Fatalf("expected 3 rows before undo, got %v", c)
	}

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d", status, http.StatusOK)
	}

	_, after := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	if c := extractRowValues(t, after["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("expected 0 rows after undo, got %v", c)
	}

	// Redo stack has the undone batch; undo stack has the CREATE TABLE
	// entry left over (one row).
	_, redo := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM _redo_stack"})
	if c := extractRowValues(t, redo["rows"].([]any)[0])[0]; c != float64(1) {
		t.Errorf("redo stack count = %v, want 1 (undone DML)", c)
	}
}

// TestChange_Undo_DDL_AddColumn verifies that undoing an ADD COLUMN
// drops the column and shifts the batch to the redo stack, reporting no
// rows changed: a schema change is not a row change.
func TestChange_Undo_DDL_AddColumn(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN created_at TEXT"})

	// Confirms the column exists before undo.
	_, before := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'created_at'"})
	if c := extractRowValues(t, before["rows"].([]any)[0])[0]; c != float64(1) {
		t.Fatalf("expected created_at to exist before undo, count = %v", c)
	}

	status, undo := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d", status, http.StatusOK)
	}
	if n, _ := undo["affected_row_count"].(float64); n != 0 {
		t.Errorf("undo affected_row_count = %v, want 0", undo["affected_row_count"])
	}

	_, after := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'created_at'"})
	if c := extractRowValues(t, after["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("expected created_at to be gone after undo, count = %v", c)
	}
}

// TestChange_Undo_DDL_DropColumn verifies DROP COLUMN undo: snapshot
// rows are replayed and the column is re-added with all original values.
func TestChange_Undo_DDL_DropColumn(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})

	// Populates 5 rows with distinct qty values for snapshot capture.
	for i := 1; i <= 5; i++ {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
			map[string]string{"sql": fmt.Sprintf(
				"INSERT INTO items (name, qty) VALUES ('item-%d', %d)", i, i*10)})
	}

	// Drops the qty column — snapshot captures the 5 values.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items DROP COLUMN qty"})

	_, gone := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'qty'"})
	if c := extractRowValues(t, gone["rows"].([]any)[0])[0]; c != float64(0) {
		t.Fatalf("expected qty to be gone after DROP COLUMN, count = %v", c)
	}

	// Undo restores the column and its values.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d", status, http.StatusOK)
	}

	_, back := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT name, qty FROM items ORDER BY id"})
	rows := back["rows"].([]any)
	if len(rows) != 5 {
		t.Fatalf("expected 5 rows after undo, got %d", len(rows))
	}
	for i, row := range rows {
		vals := extractRowValues(t, row)
		wantQty := float64((i + 1) * 10)
		if vals[1] != wantQty {
			t.Errorf("row %d qty = %v, want %v", i, vals[1], wantQty)
		}
	}
}

// TestChange_Undo_DDL_DropColumn_KeepsRowIDs pins that the table rebuild behind a DROP COLUMN undo carries each row's
// rowid across, on a table without an INTEGER PRIMARY KEY alias and with a gap in the rowid sequence that a re-keying
// rebuild would close.
func TestChange_Undo_DDL_DropColumn_KeepsRowIDs(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE notes (title TEXT, body TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO notes (title, body) VALUES ('t1', 'b1'), ('t2', 'b2'), ('t3', 'b3')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "DELETE FROM notes WHERE rowid = 2"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE notes DROP COLUMN body"})

	status, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d: %v", status, http.StatusOK, resp)
	}

	_, back := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT rowid, title, body FROM notes ORDER BY rowid"})
	rows := back["rows"].([]any)
	want := [][]any{{float64(1), "t1", "b1"}, {float64(3), "t3", "b3"}}
	if len(rows) != len(want) {
		t.Fatalf("expected %d rows after undo, got %d", len(want), len(rows))
	}
	for i, row := range rows {
		vals := extractRowValues(t, row)
		for j := range want[i] {
			if vals[j] != want[i][j] {
				t.Errorf("row %d column %d = %v, want %v", i, j, vals[j], want[i][j])
			}
		}
	}
}

// TestChange_Undo_DDL_DropTable verifies that undoing a DROP TABLE
// recreates the table, restores all rows from the snapshot, and brings
// back the dependent index.
func TestChange_Undo_DDL_DropTable(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE INDEX idx_items_name ON items (name)"})

	for _, item := range []string{"milk", "eggs", "bread"} {
		testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
			map[string]string{"sql": fmt.Sprintf("INSERT INTO items (name, qty) VALUES ('%s', 1)", item)})
	}

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "DROP TABLE items"})

	// Undo restores the table, its rows, and the dependent index.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d", status, http.StatusOK)
	}

	_, rowsResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT name FROM items ORDER BY name"})
	rows := rowsResp["rows"].([]any)
	if len(rows) != 3 {
		t.Errorf("expected 3 restored rows, got %d", len(rows))
	}

	_, idxResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_items_name'"})
	if len(idxResp["rows"].([]any)) != 1 {
		t.Errorf("expected idx_items_name to be restored, got %v", idxResp["rows"])
	}
}

// TestChange_Undo_DDL_RenameTo verifies that undoing an ALTER TABLE
// RENAME TO restores the original table name.
func TestChange_Undo_DDL_RenameTo(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items RENAME TO products"})

	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d", status, http.StatusOK)
	}

	// Original name back, data intact.
	_, originalResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT name FROM items"})
	rows := originalResp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row in restored items, got %d", len(rows))
	}
	if v := extractRowValues(t, rows[0])[0]; v != "milk" {
		t.Errorf("row name = %v, want milk", v)
	}

	// The post-rename name shouldn't exist.
	_, gone := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='products'"})
	if c := extractRowValues(t, gone["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("expected 'products' to be gone after undo, count = %v", c)
	}
}

// TestChange_Undo_TimelineWalk_CreateThenInsertThenUndo walks the
// edge-case dissolution: CREATE t → INSERT row → undo (DML revert
// removes the row) → undo again (DDL revert drops t — succeeds because
// the table is now empty).
func TestChange_Undo_TimelineWalk_CreateThenInsertThenUndo(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})

	// First undo removes the INSERT.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("first undo status = %d, want %d", status, http.StatusOK)
	}

	_, mid := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	if c := extractRowValues(t, mid["rows"].([]any)[0])[0]; c != float64(0) {
		t.Fatalf("expected 0 rows after first undo, got %v", c)
	}

	// Second undo drops the now-empty table; conflict detector allows it
	// (CreateTable undo only blocks when the table is non-empty).
	status2, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status2 != http.StatusOK {
		t.Fatalf("second undo status = %d, want %d", status2, http.StatusOK)
	}

	_, gone := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='items'"})
	if c := extractRowValues(t, gone["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("expected items table to be dropped, count = %v", c)
	}
}

// TestChange_Redo_DDL_DropColumn verifies that redo of a DROP COLUMN re-applies the drop by inverting the undo's own
// row — recorded as the ADD COLUMN that restored the column — with a fresh snapshot of the column; the resulting
// batch lands on the undo stack so a follow-up undo restores the column again.
func TestChange_Redo_DDL_DropColumn(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 3)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items DROP COLUMN qty"})

	// Undo the DROP COLUMN — column re-added with snapshot data.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("first undo status = %d, want %d", status, http.StatusOK)
	}
	_, restored := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'qty'"})
	if c := extractRowValues(t, restored["rows"].([]any)[0])[0]; c != float64(1) {
		t.Fatalf("expected qty to be re-added by undo, count = %v", c)
	}

	_, undoRow := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT ddl_statement, ddl_subtype FROM _change_log " +
			"WHERE kind = 'DDL' AND origin = 'undo'"})
	if v := extractRowValues(t, undoRow["rows"].([]any)[0]); v[0] != `ALTER TABLE "items" ADD COLUMN "qty" INTEGER` ||
		v[1] != "AddColumn" {
		t.Errorf("undo row = %v, want the ADD COLUMN that restored qty", v)
	}

	// Redo re-applies the DROP COLUMN.
	status, _ = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/redo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("redo status = %d, want %d", status, http.StatusOK)
	}
	_, droppedAgain := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'qty'"})
	if c := extractRowValues(t, droppedAgain["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("expected qty to be dropped after redo, count = %v", c)
	}
	_, redoSnapshot := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM _change_snapshots s JOIN _change_log c " +
			"ON c.id = s.change_log_id " +
			"WHERE c.origin = 'redo' AND c.ddl_subtype = 'DropColumn'"})
	if c := extractRowValues(t, redoSnapshot["rows"].([]any)[0])[0]; c != float64(1) {
		t.Errorf("redo DROP COLUMN rows backed by a snapshot = %v, want 1", c)
	}

	// Following undo should put the column back again (full cycle).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	_, restoredAgain := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'qty'"})
	if c := extractRowValues(t, restoredAgain["rows"].([]any)[0])[0]; c != float64(1) {
		t.Errorf("expected qty to be re-added by second undo, count = %v", c)
	}
}

// TestChange_Undo_DDL_NoLongerClearsStacks is the invariant flip: a
// non-destructive DDL by Bob no longer wipes Alice's undo stack. Alice
// keeps her DML history; Bob keeps his own DDL entry.
func TestChange_Undo_DDL_NoLongerClearsStacks(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()
	aliceToken := userToken(t, aliceID)
	bobToken := userToken(t, bobID)

	// Alice creates the table and pushes two DML batches onto her undo
	// stack.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), aliceToken,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), aliceToken,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), aliceToken,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('eggs')"})

	// Bob runs a non-destructive DDL (ADD COLUMN).
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), bobToken,
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN qty INTEGER"})

	// Alice's DML entries are still on her undo stack.
	_, aliceStack := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), aliceToken,
		map[string]string{"sql": fmt.Sprintf(
			"SELECT COUNT(*) FROM _undo_stack us "+
				"JOIN _change_log cl ON cl.batch_id = us.batch_id "+
				"WHERE us.user_id = '%s' AND cl.kind = 'DML' "+
				"GROUP BY us.batch_id HAVING COUNT(*) >= 1", aliceID)})
	if rows := aliceStack["rows"].([]any); len(rows) != 2 {
		t.Errorf("expected 2 of alice's DML undo entries to survive Bob's DDL, got %d", len(rows))
	}

	// And she can still undo her latest insert.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), aliceToken, nil)
	if status != http.StatusOK {
		t.Errorf("alice's undo status = %d, want %d", status, http.StatusOK)
	}
	_, after := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), aliceToken,
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	if c := extractRowValues(t, after["rows"].([]any)[0])[0]; c != float64(1) {
		t.Errorf("expected 1 row remaining after alice's undo, got %v", c)
	}
}

// TestChange_Undo_MultiTouchRow_SameBatch verifies that a batch whose statements update the same row
// more than once can be undone: each entry's post-state snapshot reflects an intermediate state that a
// later statement in the same batch legitimately moved, and only the row's final state must match the
// live row for the undo to proceed.
func TestChange_Undo_MultiTouchRow_SameBatch(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 1)"})

	// One batch, two UPDATEs to the same row.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "UPDATE items SET qty = 2 WHERE id = 1; " +
			"UPDATE items SET name = 'oat milk' WHERE id = 1"})

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d (body: %v)", status, http.StatusOK, body)
	}

	_, after := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT name, qty FROM items WHERE id = 1"})
	vals := extractRowValues(t, after["rows"].([]any)[0])
	if vals[0] != "milk" || vals[1] != float64(1) {
		t.Errorf("expected row restored to ('milk', 1), got (%v, %v)", vals[0], vals[1])
	}
}

// TestChange_Undo_MultiTouchRow_FKSetNull verifies undo when one of a batch's touches on a row is
// indirect: it updates a parent row, then deletes a child row whose FK carries ON DELETE SET NULL back
// onto the parent — an FK action that writes the parent again and records its own change-log entry.
// The undo must restore the parent's original column values and re-insert the child.
func TestChange_Undo_MultiTouchRow_FKSetNull(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE titles (id INTEGER PRIMARY KEY, name TEXT, checked_at TEXT, " +
			"checked_entry_id INTEGER REFERENCES entries(id) ON DELETE SET NULL); " +
			"CREATE TABLE entries (id INTEGER PRIMARY KEY, title_id INTEGER)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO titles (name, checked_at) VALUES ('Alien', '2026-08-07'); " +
			"INSERT INTO entries (title_id) VALUES (1); " +
			"UPDATE titles SET checked_entry_id = 1 WHERE id = 1"})

	// One batch, two touches on titles row 1: the explicit UPDATE, then the FK action nulling
	// checked_entry_id when the linked entry is deleted.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "UPDATE titles SET checked_at = NULL WHERE id = 1; " +
			"DELETE FROM entries WHERE id = 1"})

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d (body: %v)", status, http.StatusOK, body)
	}

	_, title := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT checked_at, checked_entry_id FROM titles WHERE id = 1"})
	vals := extractRowValues(t, title["rows"].([]any)[0])
	if vals[0] != "2026-08-07" || vals[1] != float64(1) {
		t.Errorf("expected title restored to ('2026-08-07', 1), got (%v, %v)", vals[0], vals[1])
	}
	_, entry := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM entries WHERE id = 1"})
	if c := extractRowValues(t, entry["rows"].([]any)[0])[0]; c != float64(1) {
		t.Errorf("expected the deleted entry re-inserted, count = %v", c)
	}
}
