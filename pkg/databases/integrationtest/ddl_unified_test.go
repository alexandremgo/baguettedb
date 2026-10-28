//go:build integration

package integrationtest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/alexandremgo/baguettedb/pkg/common/quota"
	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/sqlitelocal"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
	"github.com/alexandremgo/baguettedb/pkg/databases/service"
)

// setupUnifiedDDLTest prepares a SQLite database at the current platform
// schema version (v4+ — _change_log is applied by initPlatformTables)
// and returns a QueryService wired through the DDL flow plus the raw
// client and a fresh user UUID.
func setupUnifiedDDLTest(t *testing.T) (*service.QueryService, *sqlitelocal.Client, string, uuid.UUID) {
	t.Helper()
	client, databaseID := setupAdapterSQLiteClient(t)
	initPlatformTables(t, client, databaseID)

	limits := quota.Fixed(quota.Limits{
		SnapshotMaxRows:  10000,
		UndoStackMaxSize: 50,
	})
	qs := service.NewQueryServiceForTesting(client, sqlitelocal.NewClassifier(), limits, testLogger)
	return qs, client, databaseID, uuid.New()
}

// setupUnifiedDDLTestWithLimits is like setupUnifiedDDLTest but lets the
// test override the snapshot cap. Used by the truncation test.
func setupUnifiedDDLTestWithLimits(
	t *testing.T, limits quota.Resolver,
) (*service.QueryService, *sqlitelocal.Client, string, uuid.UUID) {
	t.Helper()
	client, databaseID := setupAdapterSQLiteClient(t)
	initPlatformTables(t, client, databaseID)
	qs := service.NewQueryServiceForTesting(client, sqlitelocal.NewClassifier(), limits, testLogger)
	return qs, client, databaseID, uuid.New()
}

// createUserTableWithTriggers runs CREATE TABLE on a user table and then
// installs the per-table _change_log triggers via the post-migration
// regen helper. Bypasses the unified flow so the test can isolate the
// behavior of a subsequent DDL statement.
func createUserTableWithTriggers(t *testing.T, client *sqlitelocal.Client, databaseID, createSQL string) {
	t.Helper()
	ctx := context.Background()
	if _, err := client.ExecuteInternal(ctx, databaseID, createSQL, nil); err != nil {
		t.Fatalf("create user table: %v\nSQL: %s", err, createSQL)
	}
	if err := service.RegenChangeLogTriggersForAllUserTablesForTesting(ctx, client, databaseID); err != nil {
		t.Fatalf("regen change-log triggers: %v", err)
	}
}

func TestUnifiedDDL_AddColumn_CapturesNoSnapshot(t *testing.T) {
	qs, client, databaseID, userID := setupUnifiedDDLTest(t)
	ctx := context.Background()

	createUserTableWithTriggers(t, client, databaseID, "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)")

	stmt := domain.ClassifiedStatement{
		SQL:     "ALTER TABLE items ADD COLUMN created_at TEXT",
		Type:    domain.StatementDDL,
		DDLType: domain.DDLAlterTable,
		Tables:  []string{"items"},
	}
	_, warnings, err := qs.ExecuteDDLRequestForTesting(ctx, databaseID, userID, []domain.ClassifiedStatement{stmt})
	if err != nil {
		t.Fatalf("unified DDL: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no snapshot warnings, got %d", len(warnings))
	}

	row := readSingleChangeLogRow(t, client, databaseID)
	if row.kind != "DDL" {
		t.Errorf("kind = %q, want DDL", row.kind)
	}
	if row.operation != "ALTER" {
		t.Errorf("operation = %q, want ALTER", row.operation)
	}
	if row.objectName != "items" {
		t.Errorf("object_name = %q, want items", row.objectName)
	}
	if row.ddlSubtype != "AddColumn" {
		t.Errorf("ddl_subtype = %q, want AddColumn", row.ddlSubtype)
	}
	if row.changedBy != userID.String() {
		t.Errorf("changed_by = %q, want %q", row.changedBy, userID.String())
	}

	var meta domain.DDLAlterMetadata
	if err := json.Unmarshal([]byte(row.ddlMetadata), &meta); err != nil {
		t.Fatalf("decode ddl_metadata: %v", err)
	}
	if meta.ColumnName != "created_at" {
		t.Errorf("metadata.ColumnName = %q, want created_at", meta.ColumnName)
	}

	if count := countSnapshots(t, client, databaseID); count != 0 {
		t.Errorf("ADD COLUMN should not create a snapshot row, got %d", count)
	}
}

func TestUnifiedDDL_DropColumn_CapturesRowPayload(t *testing.T) {
	qs, client, databaseID, userID := setupUnifiedDDLTest(t)
	ctx := context.Background()

	createUserTableWithTriggers(t, client, databaseID,
		"CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT NOT NULL, qty INTEGER NOT NULL DEFAULT 0)")

	for i, name := range []string{"bread", "milk", "eggs", "butter", "cheese"} {
		insertSQL := fmt.Sprintf("INSERT INTO items (name, qty) VALUES ('%s', %d)", name, (i+1)*2)
		if _, err := client.Execute(ctx, databaseID, insertSQL, nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	stmt := domain.ClassifiedStatement{
		SQL:     "ALTER TABLE items DROP COLUMN qty",
		Type:    domain.StatementDDL,
		DDLType: domain.DDLAlterTable,
		Tables:  []string{"items"},
	}
	_, warnings, err := qs.ExecuteDDLRequestForTesting(ctx, databaseID, userID, []domain.ClassifiedStatement{stmt})
	if err != nil {
		t.Fatalf("unified DDL: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no snapshot warnings (cap=10000 > 5 rows), got %d", len(warnings))
	}

	row := readSingleChangeLogRow(t, client, databaseID)
	if row.ddlSubtype != "DropColumn" {
		t.Errorf("ddl_subtype = %q, want DropColumn", row.ddlSubtype)
	}

	snap := readSingleSnapshot(t, client, databaseID, row.id)
	if snap.rowCountTotal != 5 {
		t.Errorf("row_count_total = %d, want 5", snap.rowCountTotal)
	}
	if snap.truncated != 0 {
		t.Errorf("truncated = %d, want 0", snap.truncated)
	}

	var colMeta domain.ColumnSnapshot
	if err := json.Unmarshal([]byte(snap.columnMetadata), &colMeta); err != nil {
		t.Fatalf("decode column_metadata: %v", err)
	}
	if colMeta.Name != "qty" {
		t.Errorf("ColumnMetadata.Name = %q, want qty", colMeta.Name)
	}
	if colMeta.Type != "INTEGER" {
		t.Errorf("ColumnMetadata.Type = %q, want INTEGER", colMeta.Type)
	}
	if !colMeta.NotNull {
		t.Errorf("ColumnMetadata.NotNull = false, want true")
	}
	if colMeta.DfltValue == nil || *colMeta.DfltValue != "0" {
		got := "<nil>"
		if colMeta.DfltValue != nil {
			got = *colMeta.DfltValue
		}
		t.Errorf("ColumnMetadata.DfltValue = %q, want \"0\"", got)
	}
	if colMeta.Position != 2 {
		t.Errorf("ColumnMetadata.Position = %d, want 2", colMeta.Position)
	}

	var rows []domain.SnapshotRow
	if err := json.Unmarshal([]byte(snap.rowPayload), &rows); err != nil {
		t.Fatalf("decode row_payload: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("row_payload length = %d, want 5", len(rows))
	}
	for i, r := range rows {
		if r.RowID == 0 {
			t.Errorf("row[%d].RowID = 0, expected non-zero", i)
		}
		if _, ok := r.Values["qty"]; !ok {
			t.Errorf("row[%d] missing qty value", i)
		}
	}
}

func TestUnifiedDDL_DropColumn_TruncatesAtCap(t *testing.T) {
	maxRows := 3
	qs, client, databaseID, userID := setupUnifiedDDLTestWithLimits(t, quota.Fixed(quota.Limits{
		SnapshotMaxRows:  maxRows,
		UndoStackMaxSize: 50,
	}))
	ctx := context.Background()

	createUserTableWithTriggers(t, client, databaseID,
		"CREATE TABLE events (id INTEGER PRIMARY KEY, payload TEXT)")
	for i := 0; i < 8; i++ {
		insertSQL := fmt.Sprintf("INSERT INTO events (payload) VALUES ('row-%d')", i)
		if _, err := client.Execute(ctx, databaseID, insertSQL, nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	stmt := domain.ClassifiedStatement{
		SQL:     "ALTER TABLE events DROP COLUMN payload",
		Type:    domain.StatementDDL,
		DDLType: domain.DDLAlterTable,
		Tables:  []string{"events"},
	}
	_, warnings, err := qs.ExecuteDDLRequestForTesting(ctx, databaseID, userID, []domain.ClassifiedStatement{stmt})
	if err != nil {
		t.Fatalf("unified DDL: %v", err)
	}

	if len(warnings) != 1 {
		t.Fatalf("expected 1 snapshot warning, got %d", len(warnings))
	}
	w := warnings[0]
	if !w.Truncated || w.Captured != maxRows || w.Total != 8 || w.Lost != 5 || w.Statement != 1 {
		t.Errorf("warning shape wrong: %+v (want Truncated=true Captured=%d Total=8 Lost=5 Statement=1)", w, maxRows)
	}

	row := readSingleChangeLogRow(t, client, databaseID)
	snap := readSingleSnapshot(t, client, databaseID, row.id)
	if snap.truncated != 1 {
		t.Errorf("snapshot.truncated = %d, want 1", snap.truncated)
	}
	if snap.rowCountTotal != 8 {
		t.Errorf("snapshot.row_count_total = %d, want 8", snap.rowCountTotal)
	}
	var rows []domain.SnapshotRow
	if err := json.Unmarshal([]byte(snap.rowPayload), &rows); err != nil {
		t.Fatalf("decode row_payload: %v", err)
	}
	if len(rows) != maxRows {
		t.Errorf("row_payload length = %d, want %d", len(rows), maxRows)
	}
}

func TestUnifiedDDL_DropTable_CapturesEverything(t *testing.T) {
	qs, client, databaseID, userID := setupUnifiedDDLTest(t)
	ctx := context.Background()

	createUserTableWithTriggers(t, client, databaseID,
		"CREATE TABLE groceries (id INTEGER PRIMARY KEY, item TEXT, qty INTEGER)")
	if _, err := client.ExecuteInternal(ctx, databaseID,
		"CREATE INDEX idx_groceries_item ON groceries(item)", nil); err != nil {
		t.Fatalf("create index: %v", err)
	}
	// Seeds a parent _skills row so the _skill_targets FK is satisfied.
	if _, err := client.ExecuteInternal(ctx, databaseID,
		`INSERT INTO _skills (id, name, description, body) `+
			`VALUES ('skill-1', 'test-skill', 'd', 'b')`, nil); err != nil {
		t.Fatalf("insert skill: %v", err)
	}
	if _, err := client.ExecuteInternal(ctx, databaseID,
		`INSERT INTO _skill_targets (skill_id, table_name) VALUES ('skill-1', 'groceries')`, nil); err != nil {
		t.Fatalf("insert skill_target: %v", err)
	}
	if _, err := client.Execute(ctx, databaseID, "INSERT INTO groceries (item, qty) VALUES ('bread', 2)",
		nil); err != nil {
		t.Fatalf("insert row: %v", err)
	}
	// Truncate the _change_log rows produced by the INSERT so the test
	// can assert on just the DROP TABLE entry.
	if _, err := client.ExecuteInternal(ctx, databaseID, "DELETE FROM _change_log", nil); err != nil {
		t.Fatalf("clear change_log: %v", err)
	}

	stmt := domain.ClassifiedStatement{
		SQL:     "DROP TABLE groceries",
		Type:    domain.StatementDDL,
		DDLType: domain.DDLDropTable,
		Tables:  []string{"groceries"},
	}
	_, warnings, err := qs.ExecuteDDLRequestForTesting(ctx, databaseID, userID, []domain.ClassifiedStatement{stmt})
	if err != nil {
		t.Fatalf("unified DDL: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %d", len(warnings))
	}

	row := readSingleChangeLogRow(t, client, databaseID)
	if row.operation != "DROP" || row.objectName != "groceries" {
		t.Errorf("change_log row wrong: operation=%q object_name=%q", row.operation, row.objectName)
	}

	snap := readSingleSnapshot(t, client, databaseID, row.id)
	if snap.rowCountTotal != 1 {
		t.Errorf("row_count_total = %d, want 1", snap.rowCountTotal)
	}

	var deps []domain.DependentObject
	if err := json.Unmarshal([]byte(snap.dependentObjects), &deps); err != nil {
		t.Fatalf("decode dependent_objects: %v", err)
	}
	// Expect TABLE entry + INDEX entry + SKILL_TARGET entry.
	var sawTable, sawIndex, sawSkillTarget bool
	for _, dep := range deps {
		switch dep.Kind {
		case domain.DependentKindTable:
			sawTable = true
			if dep.Name != "groceries" {
				t.Errorf("table dep name = %q, want groceries", dep.Name)
			}
			if len(dep.Columns) != 3 {
				t.Errorf("table dep columns = %v, want 3 entries", dep.Columns)
			}
		case domain.DependentKindIndex:
			sawIndex = true
		case domain.DependentKindSkillTarget:
			sawSkillTarget = true
		}
	}
	if !sawTable {
		t.Error("missing TABLE dependent")
	}
	if !sawIndex {
		t.Error("missing INDEX dependent")
	}
	if !sawSkillTarget {
		t.Error("missing SKILL_TARGET dependent")
	}
}

func TestUnifiedDDL_MultiStatementRequest_OneBatchID(t *testing.T) {
	qs, client, databaseID, userID := setupUnifiedDDLTest(t)
	ctx := context.Background()

	createUserTableWithTriggers(t, client, databaseID,
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)")

	stmts := []domain.ClassifiedStatement{
		{SQL: "ALTER TABLE notes ADD COLUMN created_at TEXT", Type: domain.StatementDDL, DDLType: domain.DDLAlterTable,
			Tables: []string{"notes"}},
		{SQL: "ALTER TABLE notes ADD COLUMN updated_at TEXT", Type: domain.StatementDDL, DDLType: domain.DDLAlterTable,
			Tables: []string{"notes"}},
		{SQL: "CREATE INDEX idx_notes_created_at ON notes(created_at)", Type: domain.StatementDDL,
			DDLType: domain.DDLCreateIndex, Tables: []string{"idx_notes_created_at"}},
	}
	_, _, err := qs.ExecuteDDLRequestForTesting(ctx, databaseID, userID, stmts)
	if err != nil {
		t.Fatalf("unified DDL: %v", err)
	}

	r, err := client.ExecuteInternal(ctx, databaseID,
		"SELECT DISTINCT batch_id FROM _change_log WHERE kind='DDL'", nil)
	if err != nil {
		t.Fatalf("query batch_ids: %v", err)
	}
	if len(r.Rows) != 1 {
		t.Fatalf("expected 1 distinct batch_id, got %d", len(r.Rows))
	}
	batchID, _ := r.Rows[0][0].Value.(string)

	r, err = client.ExecuteInternal(ctx, databaseID, "SELECT batch_id FROM _undo_stack", nil)
	if err != nil {
		t.Fatalf("query _undo_stack: %v", err)
	}
	if len(r.Rows) != 1 {
		t.Fatalf("_undo_stack should have 1 entry, got %d", len(r.Rows))
	}
	stackBatchID, _ := r.Rows[0][0].Value.(string)
	if stackBatchID != batchID {
		t.Errorf("_undo_stack batch_id (%q) != _change_log batch_id (%q)", stackBatchID, batchID)
	}
}

func TestUnifiedDDL_PushesUndoEntry(t *testing.T) {
	qs, client, databaseID, userID := setupUnifiedDDLTest(t)
	ctx := context.Background()

	createUserTableWithTriggers(t, client, databaseID, "CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT)")

	// Pre-populate the redo stack for the same user — the unified flow
	// should clear it on push.
	if _, err := client.ExecuteInternal(ctx, databaseID,
		"INSERT INTO _redo_stack (batch_id, user_id) VALUES (?, ?)",
		[]domain.TypedValue{domain.TextValue("stale-batch"), domain.TextValue(userID.String())}); err != nil {
		t.Fatalf("seed redo stack: %v", err)
	}

	stmt := domain.ClassifiedStatement{
		SQL: "ALTER TABLE items ADD COLUMN created_at TEXT", Type: domain.StatementDDL,
		DDLType: domain.DDLAlterTable, Tables: []string{"items"},
	}
	_, _, err := qs.ExecuteDDLRequestForTesting(ctx, databaseID, userID, []domain.ClassifiedStatement{stmt})
	if err != nil {
		t.Fatalf("unified DDL: %v", err)
	}

	r, err := client.ExecuteInternal(ctx, databaseID,
		"SELECT batch_id, user_id FROM _undo_stack", nil)
	if err != nil {
		t.Fatalf("query _undo_stack: %v", err)
	}
	if len(r.Rows) != 1 {
		t.Fatalf("_undo_stack should have 1 entry, got %d", len(r.Rows))
	}
	stackUserID, _ := r.Rows[0][1].Value.(string)
	if stackUserID != userID.String() {
		t.Errorf("_undo_stack user_id = %q, want %q", stackUserID, userID.String())
	}

	// Caller's redo stack must be cleared.
	r, err = client.ExecuteInternal(ctx, databaseID,
		"SELECT COUNT(*) FROM _redo_stack WHERE user_id = ?",
		[]domain.TypedValue{domain.TextValue(userID.String())})
	if err != nil {
		t.Fatalf("query _redo_stack: %v", err)
	}
	count, _ := r.Rows[0][0].Value.(int64)
	if count != 0 {
		t.Errorf("caller's _redo_stack should be cleared, got %d entries", count)
	}
}

// --- read helpers ---

type changeLogRow struct {
	id          int64
	kind        string
	operation   string
	objectType  string
	objectName  string
	ddlSubtype  string
	ddlMetadata string
	changedBy   string
}

func readSingleChangeLogRow(t *testing.T, client *sqlitelocal.Client, databaseID string) changeLogRow {
	t.Helper()
	const sql = `SELECT id, kind, operation, COALESCE(object_type, ''), COALESCE(object_name, ''),
		COALESCE(ddl_subtype, ''), COALESCE(ddl_metadata, ''), changed_by
		FROM _change_log WHERE kind='DDL' ORDER BY id DESC LIMIT 1`
	r, err := client.ExecuteInternal(context.Background(), databaseID, sql, nil)
	if err != nil {
		t.Fatalf("read _change_log: %v", err)
	}
	if len(r.Rows) == 0 {
		t.Fatalf("no DDL rows in _change_log")
	}
	row := r.Rows[0]
	id, _ := row[0].Value.(int64)
	kind, _ := row[1].Value.(string)
	operation, _ := row[2].Value.(string)
	objectType, _ := row[3].Value.(string)
	objectName, _ := row[4].Value.(string)
	ddlSubtype, _ := row[5].Value.(string)
	ddlMetadata, _ := row[6].Value.(string)
	changedBy, _ := row[7].Value.(string)
	return changeLogRow{id, kind, operation, objectType, objectName, ddlSubtype, ddlMetadata, changedBy}
}

type snapshotRow struct {
	columnMetadata   string
	dependentObjects string
	rowPayload       string
	rowCountTotal    int64
	truncated        int64
}

func readSingleSnapshot(t *testing.T, client *sqlitelocal.Client, databaseID string, changeLogID int64) snapshotRow {
	t.Helper()
	const sql = `SELECT COALESCE(column_metadata, ''), COALESCE(dependent_objects, ''),
		COALESCE(row_payload, ''), row_count_total, truncated
		FROM _change_snapshots WHERE change_log_id = ?`
	r, err := client.ExecuteInternal(context.Background(), databaseID, sql,
		[]domain.TypedValue{domain.IntegerValue(changeLogID)})
	if err != nil {
		t.Fatalf("read _change_snapshots: %v", err)
	}
	if len(r.Rows) == 0 {
		t.Fatalf("no snapshot row for change_log_id=%d", changeLogID)
	}
	row := r.Rows[0]
	colMeta, _ := row[0].Value.(string)
	deps, _ := row[1].Value.(string)
	payload, _ := row[2].Value.(string)
	rowCountTotal, _ := row[3].Value.(int64)
	truncated, _ := row[4].Value.(int64)
	return snapshotRow{colMeta, deps, payload, rowCountTotal, truncated}
}

func countSnapshots(t *testing.T, client *sqlitelocal.Client, databaseID string) int {
	t.Helper()
	r, err := client.ExecuteInternal(context.Background(), databaseID, "SELECT COUNT(*) FROM _change_snapshots", nil)
	if err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	c, _ := r.Rows[0][0].Value.(int64)
	return int(c)
}
