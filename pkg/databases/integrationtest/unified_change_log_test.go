//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/authz"
	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
)

// TestUnified_MixedHistoryAuditView pins that list_changes returns DML and DDL entries interleaved by changed_at, and
// that the kind filter narrows the response to one side or the other.
func TestUnified_MixedHistoryAuditView(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	// Interleaves DML and DDL so the audit view has rows of both kinds in chronological order.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN qty INTEGER"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('bread', 2)"})

	// Unfiltered: all 4 batches present.
	_, all := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes"), token, nil)
	batches := all["batches"].([]any)
	if len(batches) != 4 {
		t.Fatalf("expected 4 mixed batches, got %d", len(batches))
	}
	// The list is ordered DESC by id, so kinds should alternate DDL, DML, DDL, DML.
	wantKinds := []string{"DML", "DDL", "DML", "DDL"}
	for i, b := range batches {
		got := b.(map[string]any)["kind"]
		if got != wantKinds[i] {
			t.Errorf("batch[%d].kind = %v, want %v", i, got, wantKinds[i])
		}
	}

	// kind=DML narrows to the 2 inserts.
	_, dmlOnly := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DML"), token, nil)
	if got := len(dmlOnly["batches"].([]any)); got != 2 {
		t.Errorf("kind=DML batches = %d, want 2", got)
	}

	// kind=DDL narrows to the 2 schema operations.
	_, ddlOnly := testutil.DoRequest(t, router, http.MethodGet, dbPath(bdbID, "changes?kind=DDL"), token, nil)
	if got := len(ddlOnly["batches"].([]any)); got != 2 {
		t.Errorf("kind=DDL batches = %d, want 2", got)
	}
}

// TestUnified_RevertChange_DDLById pins that revert_change can target a specific DDL _change_log.id surgically and
// that the revert lands on the caller's undo stack, where it can be undone and redone like any other change.
func TestUnified_RevertChange_DDLById(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN qty INTEGER"})

	// Finds the ADD COLUMN change_log row's id.
	_, idResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DDL' AND ddl_subtype = 'AddColumn'"})
	rows := idResp["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 AddColumn row, got %d", len(rows))
	}
	addColumnID := int64(extractRowValues(t, rows[0])[0].(float64))

	// Reverts that specific entry by id.
	status, _ := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%d/revert", addColumnID)), token, nil)
	if status != http.StatusOK {
		t.Fatalf("revert status = %d, want %d", status, http.StatusOK)
	}

	// Column is gone.
	_, colCheck := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'qty'"})
	if c := extractRowValues(t, colCheck["rows"].([]any)[0])[0]; c != float64(0) {
		t.Errorf("expected qty to be gone after surgical revert, count = %v", c)
	}

	qtyPresent := func() any {
		_, r := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
			map[string]string{"sql": "SELECT COUNT(*) FROM pragma_table_info('items') WHERE name = 'qty'"})
		return extractRowValues(t, r["rows"].([]any)[0])[0]
	}
	reverse := func(action string, wantStatus int) {
		t.Helper()
		status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/"+action), token, nil)
		if status != wantStatus {
			t.Fatalf("%s status = %d, want %d, body: %v", action, status, wantStatus, body)
		}
	}

	// The revert is recorded as the DROP COLUMN that ran, with a snapshot of the column, on the user's undo stack.
	// Undoing it inverts that row: the column comes back, and the undo lands on the redo stack like any other.
	reverse("undo", http.StatusOK)
	if c := qtyPresent(); c != float64(1) {
		t.Errorf("expected qty restored by undo-of-revert, count = %v", c)
	}
	reverse("redo", http.StatusOK)
	if c := qtyPresent(); c != float64(0) {
		t.Errorf("expected qty gone again after redo, count = %v", c)
	}
	reverse("undo", http.StatusOK)
	if c := qtyPresent(); c != float64(1) {
		t.Errorf("expected qty back after the second undo, count = %v", c)
	}

	// The next undo walks past the revert to the original ADD COLUMN, which drops the column once more.
	reverse("undo", http.StatusOK)
	if c := qtyPresent(); c != float64(0) {
		t.Errorf("expected qty gone after undoing the original ADD COLUMN, count = %v", c)
	}
	reverse("redo", http.StatusOK)
	if c := qtyPresent(); c != float64(1) {
		t.Errorf("expected the redo of the original ADD COLUMN to restore qty, count = %v", c)
	}
}

// TestUnified_PerUserIsolation_Preserved pins the symmetric direction of the invariant: Alice's DDL doesn't pollute
// Bob's undo stack. (The DML→DDL direction is covered by TestChange_Undo_DDL_NoLongerClearsStacks; this one runs Alice
// as the DDL author and Bob as the prior-DML author to make sure isolation holds both ways.)
func TestUnified_PerUserIsolation_Preserved(t *testing.T) {
	truncateAll(t)
	router := setupRouterWithRole(t, authz.BdbRoleOwner)
	aliceID := uuid.New().String()
	bobID := uuid.New().String()
	bdbID := uuid.New().String()

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, aliceID),
		map[string]string{"sql": "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)"})

	// Bob pushes two DML batches first.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, bobID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('milk')"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), userToken(t, bobID),
		map[string]string{"sql": "INSERT INTO items (name) VALUES ('eggs')"})

	// Alice runs a non-destructive DDL.
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), userToken(t, aliceID),
		map[string]string{"sql": "ALTER TABLE items ADD COLUMN qty INTEGER"})

	// Bob's two DML batches are still on his undo stack.
	_, bobStack := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, bobID),
		map[string]string{"sql": "SELECT COUNT(*) FROM _undo_stack us " +
			"JOIN _change_log cl ON cl.batch_id = us.batch_id " +
			"WHERE us.user_id = '" + bobID + "' AND cl.kind = 'DML' " +
			"GROUP BY us.batch_id HAVING COUNT(*) >= 1"})
	if rows := bobStack["rows"].([]any); len(rows) != 2 {
		t.Errorf("expected 2 of bob's DML undo entries to survive Alice's DDL, got %d", len(rows))
	}

	// And bob can still undo his latest insert.
	status, _ := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), userToken(t, bobID), nil)
	if status != http.StatusOK {
		t.Errorf("bob's undo status = %d, want %d", status, http.StatusOK)
	}
	_, after := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), userToken(t, bobID),
		map[string]string{"sql": "SELECT COUNT(*) FROM items"})
	if c := extractRowValues(t, after["rows"].([]any)[0])[0]; c != float64(1) {
		t.Errorf("expected 1 row remaining after bob's undo, got %v", c)
	}
}
