//go:build integration

package integrationtest

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/testutil"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// reversalRow is one DDL row of the change log as the tests read it back.
type reversalRow struct {
	origin, operation, objectType, objectName, subtype, statement string
	hasSnapshot                                                   bool
}

// ddlRowsNewestFirst reads every DDL row of the bdb's change log, newest first, with whether a snapshot backs it.
func ddlRowsNewestFirst(t *testing.T, router http.Handler, bdbID, token string) []reversalRow {
	t.Helper()
	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT c.origin, c.operation, c.object_type, c.object_name, " +
			"COALESCE(c.ddl_subtype, ''), c.ddl_statement, s.change_log_id IS NOT NULL " +
			"FROM _change_log c LEFT JOIN _change_snapshots s ON s.change_log_id = c.id " +
			"WHERE c.kind = 'DDL' ORDER BY c.id DESC"})
	var rows []reversalRow
	for _, raw := range resp["rows"].([]any) {
		v := extractRowValues(t, raw)
		rows = append(rows, reversalRow{
			origin: v[0].(string), operation: v[1].(string), objectType: v[2].(string), objectName: v[3].(string),
			subtype: v[4].(string), statement: v[5].(string), hasSnapshot: v[6] == float64(1),
		})
	}
	return rows
}

// countQuery runs a single-value COUNT query against the bdb.
func countQuery(t *testing.T, router http.Handler, bdbID, token, sql string) float64 {
	t.Helper()
	_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": sql})
	return extractRowValues(t, resp["rows"].([]any)[0])[0].(float64)
}

// mustReverse calls one of the undo / redo endpoints and fails the test unless it succeeds.
func mustReverse(t *testing.T, router http.Handler, bdbID, token, action string) map[string]any {
	t.Helper()
	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/"+action), token, nil)
	if status != http.StatusOK {
		t.Fatalf("%s status = %d, want %d (body: %v)", action, status, http.StatusOK, body)
	}
	return body
}

// TestChange_Redo_DDL_DropTable_WithRows pins the undo → redo → undo chain of a DROP TABLE holding rows: undo
// brings the table and its rows back under their rowids, redo drops it again through a fresh snapshot, and the next
// undo restores it once more — every step a forward row that the next one inverts.
func TestChange_Redo_DDL_DropTable_WithRows(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE items (name TEXT, qty INTEGER)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO items (name, qty) VALUES ('milk', 3), ('bread', 1); " +
			"DELETE FROM items WHERE rowid = 1; INSERT INTO items (name, qty) VALUES ('eggs', 12)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "DROP TABLE items"})

	rowsAndIDs := func() string {
		_, resp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
			map[string]string{"sql": "SELECT rowid, name, qty FROM items ORDER BY rowid"})
		var rows []any
		for _, raw := range resp["rows"].([]any) {
			rows = append(rows, extractRowValues(t, raw))
		}
		return fmt.Sprint(rows)
	}
	tableExists := func() float64 {
		return countQuery(t, router, bdbID, token, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'items'")
	}

	mustReverse(t, router, bdbID, token, "undo")
	want := "[[2 bread 1] [3 eggs 12]]"
	if got := rowsAndIDs(); got != want {
		t.Fatalf("after undo rows = %s, want %s (rowids restored, the gap kept)", got, want)
	}
	rows := ddlRowsNewestFirst(t, router, bdbID, token)
	wantCreate := `CREATE TABLE "items" (name TEXT, qty INTEGER)`
	if r := rows[0]; r.origin != "undo" || r.operation != "CREATE" || r.statement != wantCreate || r.hasSnapshot {
		t.Errorf("undo row = %+v, want a CREATE TABLE forward row without a snapshot", r)
	}

	mustReverse(t, router, bdbID, token, "redo")
	if tableExists() != 0 {
		t.Fatal("redo should drop the table again")
	}
	rows = ddlRowsNewestFirst(t, router, bdbID, token)
	if r := rows[0]; r.origin != "redo" || r.operation != "DROP" || r.statement != `DROP TABLE "items"` ||
		!r.hasSnapshot {
		t.Errorf("redo row = %+v, want a DROP TABLE forward row backed by a fresh snapshot", r)
	}

	mustReverse(t, router, bdbID, token, "undo")
	if got := rowsAndIDs(); got != want {
		t.Errorf("after the second undo rows = %s, want %s", got, want)
	}
}

// TestReversal_EveryDDLRowIsForward pins the recording rule across a timeline of reversals: every DDL row written by
// an undo, a redo, or a revert describes the statement that ran — operation, object, subtype, and text agree — and
// every destructive one carries its own snapshot.
func TestReversal_EveryDDLRowIsForward(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)
	schema := func(sql string) {
		t.Helper()
		status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
			map[string]string{"sql": sql})
		if status != http.StatusOK {
			t.Fatalf("schema %q status = %d (body: %v)", sql, status, body)
		}
	}

	schema("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)")
	schema("ALTER TABLE notes ADD COLUMN tag TEXT")
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO notes (body, tag) VALUES ('a', 'x')"})

	// Revert the ADD COLUMN by id (drops tag with a snapshot of its values), then undo that revert (restores it).
	_, idResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DDL' AND ddl_subtype = 'AddColumn'"})
	addColumnID := int64(extractRowValues(t, idResp["rows"].([]any)[0])[0].(float64))
	status, body := testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%d/revert", addColumnID)), token, nil)
	if status != http.StatusOK {
		t.Fatalf("revert status = %d (body: %v)", status, body)
	}
	mustReverse(t, router, bdbID, token, "undo")

	schema("CREATE INDEX idx_notes_tag ON notes(tag)")
	schema("ALTER TABLE notes RENAME TO memos")
	mustReverse(t, router, bdbID, token, "undo") // rename back: memos → notes
	mustReverse(t, router, bdbID, token, "undo") // drop the index, snapshotting its CREATE
	mustReverse(t, router, bdbID, token, "redo") // re-create the index from that snapshot
	mustReverse(t, router, bdbID, token, "redo") // rename again: notes → memos

	table, index := domain.ChangeObjectTable, domain.ChangeObjectIndex
	alter, create, drop := domain.ChangeOpAlter, domain.ChangeOpCreate, domain.ChangeOpDrop
	want := []reversalRow{
		{"redo", alter, table, "notes", "RenameTo", `ALTER TABLE "notes" RENAME TO "memos"`, false},
		// The rename rewrote the stored index SQL to quote the table, and the snapshot captured that text.
		{"redo", create, index, "idx_notes_tag", "", `CREATE INDEX idx_notes_tag ON "notes"(tag)`, false},
		{"undo", drop, index, "idx_notes_tag", "", `DROP INDEX "idx_notes_tag"`, true},
		{"undo", alter, table, "memos", "RenameTo", `ALTER TABLE "memos" RENAME TO "notes"`, false},
		{"undo", alter, table, "notes", "AddColumn", `ALTER TABLE "notes" ADD COLUMN "tag" TEXT`, false},
		{"revert", alter, table, "notes", "DropColumn", `ALTER TABLE "notes" DROP COLUMN "tag"`, true},
	}
	var got []reversalRow
	for _, r := range ddlRowsNewestFirst(t, router, bdbID, token) {
		if r.origin != domain.ChangeOriginChange {
			got = append(got, r)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("reversal rows = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got[i], w)
		}
	}

	// The revert snapshotted the column's values, so undoing it brought them back along with the column.
	_, tagResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT tag FROM memos WHERE id = 1"})
	if v := extractRowValues(t, tagResp["rows"].([]any)[0])[0]; v != "x" {
		t.Errorf("tag after the undo of the revert = %v, want x", v)
	}
}

// TestChange_Undo_MultiStatementDDL_Order pins that a multi-statement DDL batch is reversed newest first — the second
// table is dropped before the first — and that redo re-creates them in the original order, each step one batch.
func TestChange_Undo_MultiStatementDDL_Order(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE a (id INTEGER PRIMARY KEY); CREATE TABLE b (id INTEGER PRIMARY KEY)"})
	if status != http.StatusOK {
		t.Fatalf("schema status = %d (body: %v)", status, body)
	}
	tables := func() float64 {
		return countQuery(t, router, bdbID, token,
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('a', 'b')")
	}

	mustReverse(t, router, bdbID, token, "undo")
	if tables() != 0 {
		t.Fatal("undo should drop both tables")
	}
	rows := ddlRowsNewestFirst(t, router, bdbID, token)
	if rows[0].statement != `DROP TABLE "a"` || rows[1].statement != `DROP TABLE "b"` {
		t.Errorf("undo rows (newest first) = %q, %q; want b dropped before a",
			rows[0].statement, rows[1].statement)
	}
	if c := countQuery(t, router, bdbID, token,
		"SELECT COUNT(DISTINCT batch_id) FROM _change_log WHERE origin = 'undo'"); c != 1 {
		t.Errorf("undo batches = %v, want the two drops under one batch", c)
	}

	mustReverse(t, router, bdbID, token, "redo")
	if tables() != 2 {
		t.Fatal("redo should re-create both tables")
	}
	rows = ddlRowsNewestFirst(t, router, bdbID, token)
	if rows[1].objectName != "a" || rows[0].objectName != "b" || rows[0].operation != "CREATE" {
		t.Errorf("redo rows (newest first) = %+v, %+v; want a re-created before b", rows[1], rows[0])
	}
}

// TestChange_Undo_RenameTo_ThenRedo_NoDuplicateTriggers pins that reversing a rename keeps exactly one set of
// change-log triggers on the table: the reversal drops the triggers under the table's live name before renaming, so
// SQLite's rename rewriting cannot leave a stale set behind that would log every later write twice.
func TestChange_Undo_RenameTo_ThenRedo_NoDuplicateTriggers(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE groceries (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "ALTER TABLE groceries RENAME TO shopping"})
	mustReverse(t, router, bdbID, token, "undo")
	mustReverse(t, router, bdbID, token, "redo")

	if c := countQuery(t, router, bdbID, token,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'shopping'"); c != 3 {
		t.Fatalf("triggers on shopping = %v, want exactly the 3 change-log triggers", c)
	}
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO shopping (name) VALUES ('milk')"})
	if c := countQuery(t, router, bdbID, token,
		"SELECT COUNT(*) FROM _change_log WHERE kind = 'DML' AND table_name = 'shopping'"); c != 1 {
		t.Errorf("DML rows logged for one insert = %v, want 1", c)
	}
}

// TestChange_Revert_CreateTable_DropsSkillTargets pins the _skill_targets cascade across a reversal: reverting a
// CREATE TABLE unlinks the skills that targeted it, and undoing that revert re-creates the table with its links.
func TestChange_Revert_CreateTable_DropsSkillTargets(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE inventory (id INTEGER PRIMARY KEY)"})
	status, body := testutil.DoRequest(t, router, http.MethodPost, skillsPath(bdbID, ""), token,
		map[string]any{"name": "inventory-rules", "description": "Rules.", "body": "Uppercase SKUs.",
			"target_tables": []string{"inventory"}})
	if status != http.StatusCreated {
		t.Fatalf("create skill status = %d (body: %v)", status, body)
	}
	links := func() float64 {
		return countQuery(t, router, bdbID, token,
			"SELECT COUNT(*) FROM _skill_targets WHERE table_name = 'inventory'")
	}
	if links() != 1 {
		t.Fatal("expected the skill to target inventory")
	}

	_, idResp := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "query"), token,
		map[string]string{"sql": "SELECT id FROM _change_log WHERE kind = 'DDL' AND object_name = 'inventory'"})
	createID := int64(extractRowValues(t, idResp["rows"].([]any)[0])[0].(float64))
	status, body = testutil.DoRequest(t, router, http.MethodPost,
		dbPath(bdbID, fmt.Sprintf("changes/%d/revert", createID)), token, nil)
	if status != http.StatusOK {
		t.Fatalf("revert status = %d (body: %v)", status, body)
	}
	if links() != 0 {
		t.Error("reverting the CREATE should unlink the skill from the dropped table")
	}

	mustReverse(t, router, bdbID, token, "undo")
	if links() != 1 {
		t.Error("undoing the revert should restore the skill link with the table")
	}
}

// TestUndo_CreateTableAsSelect pins the CREATE TABLE rule: rows the CREATE itself produced have no logged writer (a
// recorded DML change since the CREATE), so the undo drops the table and captures them in its snapshot instead of
// refusing, and redo brings them back.
func TestUndo_CreateTableAsSelect(t *testing.T) {
	truncateAll(t)
	router := setupRouter(t)
	userID := uuid.New().String()
	bdbID := uuid.New().String()
	token := userToken(t, userID)

	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE src (id INTEGER PRIMARY KEY, name TEXT)"})
	testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "data"), token,
		map[string]string{"sql": "INSERT INTO src (name) VALUES ('a'), ('b')"})
	status, body := testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "schema"), token,
		map[string]string{"sql": "CREATE TABLE copy AS SELECT * FROM src"})
	if status != http.StatusOK {
		t.Skipf("CREATE TABLE AS SELECT is not admitted: %d %v", status, body)
	}

	status, body = testutil.DoRequest(t, router, http.MethodPost, dbPath(bdbID, "changes/undo"), token, nil)
	if status != http.StatusOK {
		t.Fatalf("undo status = %d, want %d (body: %v)", status, http.StatusOK, body)
	}
	if c := countQuery(t, router, bdbID, token, "SELECT COUNT(*) FROM sqlite_master WHERE name = 'copy'"); c != 0 {
		t.Fatal("undo should drop the copied table")
	}
	mustReverse(t, router, bdbID, token, "redo")
	if c := countQuery(t, router, bdbID, token, "SELECT COUNT(*) FROM copy"); c != 2 {
		t.Errorf("rows in the re-created copy = %v, want 2 (from the undo's snapshot)", c)
	}
}
