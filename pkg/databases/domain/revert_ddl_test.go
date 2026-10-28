package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// assertShape checks the recorded shape of an inverse unit: its DDL type, the single object it targets, and the
// statement the change log records for it.
func assertShape(t *testing.T, unit DDLUnit, ddlType DDLType, object, sql string) {
	t.Helper()
	if unit.Shape.Type != StatementDDL || unit.Shape.DDLType != ddlType {
		t.Errorf("Shape type = %v/%v, want DDL/%v", unit.Shape.Type, unit.Shape.DDLType, ddlType)
	}
	if len(unit.Shape.Tables) != 1 || unit.Shape.Tables[0] != object {
		t.Errorf("Shape.Tables = %v, want [%s]", unit.Shape.Tables, object)
	}
	if unit.Shape.SQL != sql {
		t.Errorf("Shape.SQL = %q, want %q", unit.Shape.SQL, sql)
	}
}

// --- dispatch + error paths ---

func TestGenerateRevertDDL_RejectsDMLEntry(t *testing.T) {
	entry := &ChangeLogEntry{Kind: ChangeKindDML, Operation: ChangeOpInsert}
	_, err := GenerateRevertDDL(entry, nil)
	if !errors.Is(err, ErrInvalidRevertEntry) {
		t.Errorf("expected ErrInvalidRevertEntry, got %v", err)
	}
}

func TestGenerateRevertDDL_RejectsNilEntry(t *testing.T) {
	_, err := GenerateRevertDDL(nil, nil)
	if !errors.Is(err, ErrInvalidRevertEntry) {
		t.Errorf("expected ErrInvalidRevertEntry, got %v", err)
	}
}

func TestGenerateRevertDDL_DropRequiresSnapshot(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpDrop,
		ObjectType: ChangeObjectTable, ObjectName: "items",
	}
	_, err := GenerateRevertDDL(entry, nil)
	if !errors.Is(err, ErrMissingSnapshot) {
		t.Errorf("expected ErrMissingSnapshot, got %v", err)
	}
}

func TestGenerateRevertDDL_DropColumnRequiresSnapshot(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "items",
		DDLSubtype: DDLSubtypeDropColumn,
	}
	_, err := GenerateRevertDDL(entry, nil)
	if !errors.Is(err, ErrMissingSnapshot) {
		t.Errorf("expected ErrMissingSnapshot, got %v", err)
	}
}

// --- CREATE inverse ---

func TestGenerateRevertDDL_CreateTable(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpCreate,
		ObjectType: ChangeObjectTable, ObjectName: "items",
	}
	unit, err := GenerateRevertDDL(entry, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertShape(t, unit, DDLDropTable, "items", `DROP TABLE "items"`)
	// The _skill_targets cascade belongs to the DROP TABLE support work, not to the inverse.
	if len(unit.Exec) != 1 || unit.Exec[0].SQL != `DROP TABLE "items"` {
		t.Errorf("expected the DROP TABLE alone, got: %+v", unit.Exec)
	}
}

func TestGenerateRevertDDL_CreateIndex(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpCreate,
		ObjectType: ChangeObjectIndex, ObjectName: "idx_items_name",
	}
	unit, err := GenerateRevertDDL(entry, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertShape(t, unit, DDLDropIndex, "idx_items_name", `DROP INDEX "idx_items_name"`)
	if len(unit.Exec) != 1 || unit.Exec[0].SQL != `DROP INDEX "idx_items_name"` {
		t.Errorf("expected single DROP INDEX, got: %+v", unit.Exec)
	}
}

func TestGenerateRevertDDL_CreateView_Trigger(t *testing.T) {
	for _, tc := range []struct {
		objectType, object, want string
		ddlType                  DDLType
	}{
		{ChangeObjectView, "v_active", `DROP VIEW "v_active"`, DDLDropView},
		{ChangeObjectTrigger, "trg_audit", `DROP TRIGGER "trg_audit"`, DDLDropTrigger},
	} {
		entry := &ChangeLogEntry{
			Kind: ChangeKindDDL, Operation: ChangeOpCreate,
			ObjectType: tc.objectType, ObjectName: tc.object,
		}
		unit, err := GenerateRevertDDL(entry, nil)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.objectType, err)
			continue
		}
		assertShape(t, unit, tc.ddlType, tc.object, tc.want)
		if len(unit.Exec) != 1 || unit.Exec[0].SQL != tc.want {
			t.Errorf("%s: expected %q, got %+v", tc.objectType, tc.want, unit.Exec)
		}
	}
}

// --- DROP inverse: simple objects ---

func TestGenerateRevertDDL_DropIndex_ReplaysCapture(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpDrop,
		ObjectType: ChangeObjectIndex, ObjectName: "idx_items_name",
	}
	snap := &ChangeSnapshot{
		DependentObjects: []DependentObject{
			{Kind: DependentKindIndex, Name: "idx_items_name", SQL: `CREATE INDEX idx_items_name ON items(name)`},
		},
	}
	unit, err := GenerateRevertDDL(entry, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertShape(t, unit, DDLCreateIndex, "idx_items_name", `CREATE INDEX idx_items_name ON items(name)`)
	if len(unit.Exec) != 1 || unit.Exec[0].SQL != `CREATE INDEX idx_items_name ON items(name)` {
		t.Errorf("expected captured CREATE INDEX SQL, got %+v", unit.Exec)
	}
}

func TestGenerateRevertDDL_DropIndex_MissingSnapshotEntry(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpDrop,
		ObjectType: ChangeObjectIndex, ObjectName: "idx_items_name",
	}
	snap := &ChangeSnapshot{}
	_, err := GenerateRevertDDL(entry, snap)
	if !errors.Is(err, ErrMissingSnapshot) {
		t.Errorf("expected ErrMissingSnapshot, got %v", err)
	}
}

// --- DROP TABLE inverse ---

func TestGenerateRevertDDL_DropTable_DependentOrder(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpDrop,
		ObjectType: ChangeObjectTable, ObjectName: "items",
	}
	snap := &ChangeSnapshot{
		DependentObjects: []DependentObject{
			{Kind: DependentKindTrigger, Name: "trg_audit", SQL: "CREATE TRIGGER trg_audit ..."},
			{Kind: DependentKindIndex, Name: "idx_items_name", SQL: "CREATE INDEX idx_items_name ON items(name)"},
			{Kind: DependentKindTable, Name: "items",
				SQL:     "(id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)",
				Columns: []string{"id", "name", "qty"}},
			{Kind: DependentKindView, Name: "v_items", SQL: "CREATE VIEW v_items AS SELECT * FROM items"},
			{Kind: DependentKindSkillTarget, Name: "skill-1:items", SQL: `{"skill_id":"skill-1","table_name":"items"}`},
		},
		RowPayload: []SnapshotRow{
			{RowID: 1, Values: map[string]any{"id": json.Number("1"), "name": "bread", "qty": json.Number("2")}},
		},
		RowCountTotal: 1,
	}
	unit, err := GenerateRevertDDL(entry, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The unit is recorded as the CREATE TABLE alone; rows, dependents, and skill links are how it is performed.
	assertShape(t, unit, DDLCreateTable, "items",
		`CREATE TABLE "items" (id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)`)
	stmts := unit.Exec
	// Expected order: CREATE TABLE → INSERT rows → CREATE INDEX → CREATE VIEW
	// → CREATE TRIGGER → INSERT _skill_targets
	if len(stmts) != 6 {
		t.Fatalf("expected 6 statements, got %d:\n%s", len(stmts), debugStmts(stmts))
	}
	if stmts[0].SQL != unit.Shape.SQL {
		t.Errorf("stmt 0 should be the recorded CREATE TABLE, got: %q", stmts[0].SQL)
	}
	if !strings.HasPrefix(stmts[1].SQL, `INSERT INTO "items" (rowid, "id", "name", "qty") VALUES (?, ?, ?, ?)`) {
		t.Errorf("stmt 1 should be a bulk INSERT leading with the captured rowid, got: %q", stmts[1].SQL)
	}
	if stmts[1].Args[0] != IntegerValue(1) {
		t.Errorf("stmt 1 first arg = %v, want the captured rowid 1", stmts[1].Args[0])
	}
	if !strings.HasPrefix(stmts[2].SQL, "CREATE INDEX") {
		t.Errorf("stmt 2 should be CREATE INDEX, got: %q", stmts[2].SQL)
	}
	if !strings.HasPrefix(stmts[3].SQL, "CREATE VIEW") {
		t.Errorf("stmt 3 should be CREATE VIEW, got: %q", stmts[3].SQL)
	}
	if !strings.HasPrefix(stmts[4].SQL, "CREATE TRIGGER") {
		t.Errorf("stmt 4 should be CREATE TRIGGER, got: %q", stmts[4].SQL)
	}
	if !strings.Contains(stmts[5].SQL, "_skill_targets") || !strings.Contains(stmts[5].SQL, "INSERT INTO") {
		t.Errorf("stmt 5 should restore _skill_targets, got: %q", stmts[5].SQL)
	}
}

// TestGenerateRevertDDL_DropVirtualTable_RecordsTheVerbatimCreate pins that a dropped virtual table comes back as
// a CREATE TABLE unit recorded with the captured CREATE VIRTUAL TABLE text, performed by that text plus the skill
// links — no row payload exists to restore.
func TestGenerateRevertDDL_DropVirtualTable_RecordsTheVerbatimCreate(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpDrop,
		ObjectType: ChangeObjectTable, ObjectName: "notes_fts",
	}
	create := `CREATE VIRTUAL TABLE notes_fts USING fts5(body, content='notes')`
	snap := &ChangeSnapshot{
		DependentObjects: []DependentObject{
			{Kind: DependentKindVirtualTable, Name: "notes_fts", SQL: create},
			{Kind: DependentKindSkillTarget, Name: "s:notes_fts", SQL: `{"skill_id":"s","table_name":"notes_fts"}`},
		},
	}
	unit, err := GenerateRevertDDL(entry, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertShape(t, unit, DDLCreateTable, "notes_fts", create)
	if len(unit.Exec) != 2 || unit.Exec[0].SQL != create || !strings.Contains(unit.Exec[1].SQL, "_skill_targets") {
		t.Errorf("Exec = %s, want the CREATE VIRTUAL TABLE then the skill link", debugStmts(unit.Exec))
	}
}

func TestGenerateRevertDDL_DropTable_MissingTableEntry(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpDrop,
		ObjectType: ChangeObjectTable, ObjectName: "items",
	}
	snap := &ChangeSnapshot{
		DependentObjects: []DependentObject{
			{Kind: DependentKindIndex, Name: "idx", SQL: "CREATE INDEX idx ON items(x)"},
		},
	}
	_, err := GenerateRevertDDL(entry, snap)
	if !errors.Is(err, ErrMissingSnapshot) {
		t.Errorf("expected ErrMissingSnapshot, got %v", err)
	}
}

// --- ALTER inverse: ADD COLUMN ---

func TestGenerateRevertDDL_AddColumn(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "items",
		DDLSubtype:  DDLSubtypeAddColumn,
		DDLMetadata: &DDLAlterMetadata{ColumnName: "created_at"},
	}
	unit, err := GenerateRevertDDL(entry, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `ALTER TABLE "items" DROP COLUMN "created_at"`
	assertShape(t, unit, DDLAlterTable, "items", want)
	wantAlter := AlterTableInfo{Subtype: AlterTableDropColumn, TableName: "items", ColumnName: "created_at"}
	if unit.Alter != wantAlter {
		t.Errorf("Alter = %+v, want %+v", unit.Alter, wantAlter)
	}
	if len(unit.Exec) != 1 || unit.Exec[0].SQL != want {
		t.Errorf("expected %s, got: %+v", want, unit.Exec)
	}
}

func TestGenerateRevertDDL_AddColumn_MissingMetadata(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "items",
		DDLSubtype: DDLSubtypeAddColumn,
	}
	_, err := GenerateRevertDDL(entry, nil)
	if !errors.Is(err, ErrMissingDDLMetadata) {
		t.Errorf("expected ErrMissingDDLMetadata, got %v", err)
	}
}

// --- ALTER inverse: RENAME COLUMN ---

func TestGenerateRevertDDL_RenameColumn(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "items",
		DDLSubtype: DDLSubtypeRenameColumn,
		DDLMetadata: &DDLAlterMetadata{
			ColumnName:    "qty",
			NewColumnName: "quantity",
		},
	}
	unit, err := GenerateRevertDDL(entry, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := `ALTER TABLE "items" RENAME COLUMN "quantity" TO "qty"`
	assertShape(t, unit, DDLAlterTable, "items", expected)
	// The rename back is recorded from the live name "quantity" to "qty".
	wantAlter := AlterTableInfo{
		Subtype: AlterTableRenameColumn, TableName: "items", ColumnName: "quantity", NewColumnName: "qty",
	}
	if unit.Alter != wantAlter {
		t.Errorf("Alter = %+v, want %+v", unit.Alter, wantAlter)
	}
	if len(unit.Exec) != 1 || unit.Exec[0].SQL != expected {
		t.Errorf("expected %q, got %+v", expected, unit.Exec)
	}
}

// --- ALTER inverse: RENAME TO ---

func TestGenerateRevertDDL_RenameTo(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "groceries",
		DDLSubtype:  DDLSubtypeRenameTo,
		DDLMetadata: &DDLAlterMetadata{NewTableName: "shopping"},
	}
	unit, err := GenerateRevertDDL(entry, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The unit targets "shopping", the name the table has when the rename back runs.
	assertShape(t, unit, DDLAlterTable, "shopping", `ALTER TABLE "shopping" RENAME TO "groceries"`)
	wantAlter := AlterTableInfo{Subtype: AlterTableRenameTo, TableName: "shopping", NewTableName: "groceries"}
	if unit.Alter != wantAlter {
		t.Errorf("Alter = %+v, want %+v", unit.Alter, wantAlter)
	}
	// The _skill_targets cascade belongs to the RENAME TO support work, not to the inverse.
	if len(unit.Exec) != 1 || unit.Exec[0].SQL != `ALTER TABLE "shopping" RENAME TO "groceries"` {
		t.Errorf("expected the rename alone, got %+v", unit.Exec)
	}
}

// --- ALTER inverse: DROP COLUMN (rebuild path) ---

func TestGenerateRevertDDL_DropColumn_Rebuild_PreservesConstraints(t *testing.T) {
	notNull := true
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "items",
		DDLSubtype: DDLSubtypeDropColumn,
	}
	snap := &ChangeSnapshot{
		ColumnMetadata: &ColumnSnapshot{
			Name: "email", Type: "TEXT", NotNull: notNull, Position: 2,
			ClauseSQL: `"email" TEXT NOT NULL CHECK(length(email) > 3) UNIQUE`,
		},
		DependentObjects: []DependentObject{
			{Kind: DependentKindTable, Name: "items",
				SQL:     `(id INTEGER PRIMARY KEY, name TEXT, email TEXT NOT NULL CHECK(length(email) > 3) UNIQUE)`,
				Columns: []string{"id", "name", "email"}},
		},
		RowPayload: []SnapshotRow{
			{RowID: 1, Values: map[string]any{"email": "alice@x.test"}},
			{RowID: 2, Values: map[string]any{"email": "bob@x.test"}},
		},
		RowCountTotal: 2,
		Truncated:     false,
	}
	unit, err := GenerateRevertDDL(entry, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stmts := unit.Exec

	// Sequence: temp create, captured insert, shadow create, populate,
	// drop original, rename shadow, drop temp.
	if len(stmts) < 6 {
		t.Fatalf("expected at least 6 stmts for rebuild path, got %d:\n%s", len(stmts), debugStmts(stmts))
	}

	// Shadow CREATE TABLE must preserve the constraint clauses from the
	// captured CREATE TABLE — that's the whole point of the rebuild path.
	var shadowCreate string
	for _, stmt := range stmts {
		if strings.Contains(stmt.SQL, `"_revert_rebuild_items"`) && strings.HasPrefix(stmt.SQL, "CREATE TABLE") {
			shadowCreate = stmt.SQL
			break
		}
	}
	if shadowCreate == "" {
		t.Fatalf("did not find shadow CREATE TABLE statement, got:\n%s", debugStmts(stmts))
	}
	if !strings.Contains(shadowCreate, "NOT NULL") {
		t.Errorf("shadow CREATE TABLE should preserve NOT NULL: %s", shadowCreate)
	}
	if !strings.Contains(shadowCreate, "CHECK") {
		t.Errorf("shadow CREATE TABLE should preserve CHECK: %s", shadowCreate)
	}
	if !strings.Contains(shadowCreate, "UNIQUE") {
		t.Errorf("shadow CREATE TABLE should preserve UNIQUE: %s", shadowCreate)
	}
}

// TestGenerateRevertDDL_DropColumn_RecordsAddColumnShape pins that both DROP COLUMN inverses are recorded as the
// ADD COLUMN declaring the restored column: the rebuild path records it while performing a table rebuild that never
// issues an ADD COLUMN, and the truncated fallback records exactly the nullable ADD COLUMN it runs.
func TestGenerateRevertDDL_DropColumn_RecordsAddColumnShape(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "items",
		DDLSubtype: DDLSubtypeDropColumn,
	}
	wantAlter := AlterTableInfo{Subtype: AlterTableAddColumn, TableName: "items", ColumnName: "email"}

	t.Run("rebuild", func(t *testing.T) {
		snap := &ChangeSnapshot{
			ColumnMetadata: &ColumnSnapshot{Name: "email", Type: "TEXT", NotNull: true, Position: 2},
			DependentObjects: []DependentObject{
				{Kind: DependentKindTable, Name: "items",
					SQL:     `(id INTEGER PRIMARY KEY, name TEXT, email TEXT NOT NULL)`,
					Columns: []string{"id", "name", "email"}},
			},
			RowPayload:    []SnapshotRow{{RowID: 1, Values: map[string]any{"email": "alice@x.test"}}},
			RowCountTotal: 1,
		}
		unit, err := GenerateRevertDDL(entry, snap)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if unit.Shape.DDLType != DDLAlterTable || unit.Shape.Tables[0] != "items" || unit.Alter != wantAlter {
			t.Errorf("recorded as %+v / %+v, want an ALTER TABLE items AddColumn email", unit.Shape, unit.Alter)
		}
		if !strings.HasPrefix(unit.Shape.SQL, `ALTER TABLE "items" ADD COLUMN "email" TEXT`) {
			t.Errorf("Shape.SQL = %q, want the ADD COLUMN declaring email", unit.Shape.SQL)
		}
		for _, stmt := range unit.Exec {
			if strings.HasPrefix(stmt.SQL, `ALTER TABLE "items" ADD COLUMN`) {
				t.Errorf("the rebuild path must not run an ADD COLUMN, got %q", stmt.SQL)
			}
		}
	})

	t.Run("truncated fallback", func(t *testing.T) {
		snap := &ChangeSnapshot{
			ColumnMetadata: &ColumnSnapshot{Name: "email", Type: "TEXT", NotNull: true},
			RowPayload:     []SnapshotRow{{RowID: 1, Values: map[string]any{"email": "alice@x.test"}}},
			RowCountTotal:  15000,
			Truncated:      true,
		}
		unit, err := GenerateRevertDDL(entry, snap)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if unit.Alter != wantAlter {
			t.Errorf("Alter = %+v, want %+v", unit.Alter, wantAlter)
		}
		want := `ALTER TABLE "items" ADD COLUMN "email" TEXT`
		if unit.Shape.SQL != want || unit.Exec[0].SQL != want {
			t.Errorf("Shape.SQL = %q, Exec[0] = %q, want both %q", unit.Shape.SQL, unit.Exec[0].SQL, want)
		}
	})
}

func TestGenerateRevertDDL_DropColumn_RebuildSequenceOrder(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "items",
		DDLSubtype: DDLSubtypeDropColumn,
	}
	snap := &ChangeSnapshot{
		ColumnMetadata: &ColumnSnapshot{Name: "qty", Type: "INTEGER", Position: 2},
		DependentObjects: []DependentObject{
			{Kind: DependentKindTable, Name: "items",
				SQL:     "(id INTEGER PRIMARY KEY, name TEXT, qty INTEGER)",
				Columns: []string{"id", "name", "qty"}},
			{Kind: DependentKindIndex, Name: "idx_items_qty",
				SQL: "CREATE INDEX idx_items_qty ON items(qty)"},
		},
		RowPayload: []SnapshotRow{
			{RowID: 1, Values: map[string]any{"qty": json.Number("5")}},
		},
		RowCountTotal: 1,
	}
	unit, err := GenerateRevertDDL(entry, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stmts := unit.Exec

	// Expected ordering of leading statements.
	expectedPatterns := []string{
		`CREATE TEMP TABLE "_captured_qty"`,
		`INSERT INTO "_captured_qty"`,
		`CREATE TABLE "_revert_rebuild_items"`,
		`INSERT INTO "_revert_rebuild_items" (rowid, "id", "name", "qty")`,
		`DROP TABLE "items"`,
		`ALTER TABLE "_revert_rebuild_items" RENAME TO "items"`,
		`CREATE INDEX idx_items_qty ON items(qty)`,
		`DROP TABLE "_captured_qty"`,
	}
	if len(stmts) != len(expectedPatterns) {
		t.Fatalf("statement count = %d, want %d:\n%s",
			len(stmts), len(expectedPatterns), debugStmts(stmts))
	}
	for i, want := range expectedPatterns {
		if !strings.Contains(stmts[i].SQL, want) {
			t.Errorf("stmt %d: expected to contain %q, got %q", i, want, stmts[i].SQL)
		}
	}
}

func TestGenerateRevertDDL_DropColumn_TruncatedSnapshot_FallsBackToNullable(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "events",
		DDLSubtype: DDLSubtypeDropColumn,
	}
	notNull := true
	snap := &ChangeSnapshot{
		ColumnMetadata: &ColumnSnapshot{
			Name: "payload", Type: "TEXT", NotNull: notNull,
		},
		RowPayload: []SnapshotRow{
			{RowID: 1, Values: map[string]any{"payload": "a"}},
			{RowID: 2, Values: map[string]any{"payload": "b"}},
		},
		RowCountTotal: 15000,
		Truncated:     true,
	}
	unit, err := GenerateRevertDDL(entry, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	stmts := unit.Exec

	// Fallback emits: 1 × ADD COLUMN + N × UPDATE.
	if len(stmts) != 1+len(snap.RowPayload) {
		t.Fatalf("expected %d stmts (ADD COLUMN + %d UPDATEs), got %d:\n%s",
			1+len(snap.RowPayload), len(snap.RowPayload), len(stmts), debugStmts(stmts))
	}

	// The ADD COLUMN must NOT include NOT NULL (we'd violate it for the rows beyond the cap).
	if !strings.HasPrefix(stmts[0].SQL, `ALTER TABLE "events" ADD COLUMN "payload" TEXT`) {
		t.Errorf("ADD COLUMN wrong: %q", stmts[0].SQL)
	}
	if strings.Contains(stmts[0].SQL, "NOT NULL") {
		t.Errorf("truncated fallback should drop NOT NULL, got: %q", stmts[0].SQL)
	}

	// Each UPDATE binds (value, rowid).
	for i := 1; i < len(stmts); i++ {
		if !strings.Contains(stmts[i].SQL, `UPDATE "events" SET "payload" = ? WHERE rowid = ?`) {
			t.Errorf("update %d wrong: %q", i, stmts[i].SQL)
		}
		if len(stmts[i].Args) != 2 {
			t.Errorf("update %d: expected 2 args, got %d", i, len(stmts[i].Args))
		}
	}
}

// TestGenerateRevertDDL_DropColumn_Rebuild_MiddleColumn places the captured
// column in the middle of the original column order and asserts that the
// shadow-populate INSERT enumerates columns explicitly with captured.value
// at the right position. Regression-pins the fix for the SELECT t.*,
// captured.value pattern that would misalign every value after the dropped
// column on a middle-column undo.
func TestGenerateRevertDDL_DropColumn_Rebuild_MiddleColumn(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "items",
		DDLSubtype: DDLSubtypeDropColumn,
	}
	// Original table: (id, name, qty, created_at). qty is at column index
	// 2 of 4. After the DROP, the live table is (id, name, created_at).
	snap := &ChangeSnapshot{
		ColumnMetadata: &ColumnSnapshot{Name: "qty", Type: "INTEGER", Position: 2},
		DependentObjects: []DependentObject{
			{Kind: DependentKindTable, Name: "items",
				SQL:     "(id INTEGER PRIMARY KEY, name TEXT, qty INTEGER, created_at TEXT)",
				Columns: []string{"id", "name", "qty", "created_at"}},
		},
		RowPayload: []SnapshotRow{
			{RowID: 1, Values: map[string]any{"qty": json.Number("5")}},
			{RowID: 2, Values: map[string]any{"qty": json.Number("8")}},
		},
		RowCountTotal: 2,
	}
	unit, err := GenerateRevertDDL(entry, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var populate string
	for _, stmt := range unit.Exec {
		if strings.HasPrefix(stmt.SQL, `INSERT INTO "_revert_rebuild_items"`) {
			populate = stmt.SQL
			break
		}
	}
	if populate == "" {
		t.Fatalf("did not find shadow-populate INSERT, got:\n%s", debugStmts(unit.Exec))
	}

	// Column list leads with the rowid, then enumerates ("id", "name", "qty", "created_at") — qty in position 3 of 4.
	wantCols := `INSERT INTO "_revert_rebuild_items" (rowid, "id", "name", "qty", "created_at")`
	if !strings.HasPrefix(populate, wantCols) {
		t.Errorf("shadow populate must enumerate columns with qty at its original position;\nwant prefix: %s\ngot: %s",
			wantCols, populate)
	}

	// SELECT expressions: t.rowid, t."id", t."name", "_captured_qty".value, t."created_at"
	// — captured.value lands at the qty slot, NOT at the end.
	wantSelect := `SELECT t.rowid, t."id", t."name", "_captured_qty".value, t."created_at"`
	if !strings.Contains(populate, wantSelect) {
		t.Errorf(
			"shadow populate must place captured.value at qty's original column position;\n"+
				"want substring: %s\ngot: %s",
			wantSelect, populate)
	}

	// Anti-regression: the SELECT t.*, captured.value pattern (which
	// misaligned middle-column undo) must not return.
	if strings.Contains(populate, "SELECT t.*") {
		t.Errorf(
			"shadow populate must not use SELECT t.*, captured.value pattern "+
				"(it corrupts middle-column undo);\ngot: %s",
			populate)
	}
}

// --- helpers ---

func debugStmts(stmts []SQLStatement) string {
	var b strings.Builder
	for i, s := range stmts {
		b.WriteString(strings.TrimSpace(s.SQL))
		if i < len(stmts)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestSanitizeColumnType verifies the declared-type allowlist: common type shapes pass through verbatim,
// anything outside degrades to an untyped column ("").
func TestSanitizeColumnType(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "TEXT", "TEXT"},
		{"parenthesized", "VARCHAR(30)", "VARCHAR(30)"},
		{"two args", "DECIMAL(10,5)", "DECIMAL(10,5)"},
		{"multi word", "UNSIGNED BIG INT", "UNSIGNED BIG INT"},
		{"empty stays empty", "", ""},
		{"quote injection", "eur'); DROP TABLE x; --", ""},
		{"double quote", `a"b`, ""},
		{"semicolon", "TEXT;", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeColumnType(tt.in); got != tt.want {
				t.Errorf("sanitizeColumnType(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestAddColumnStatement pins the ADD COLUMN a restored column is recorded as: quoted identifiers, the sanitized
// declared type, then PRIMARY KEY, NOT NULL, and DEFAULT in that order, each clause omitted when the snapshot lacks
// it and never leaving a stray space behind.
func TestAddColumnStatement(t *testing.T) {
	zero := "0"
	label := "'n/a'"
	tests := []struct {
		name string
		col  ColumnSnapshot
		want string
	}{
		{"typed", ColumnSnapshot{Name: "qty", Type: "INTEGER"},
			`ALTER TABLE "items" ADD COLUMN "qty" INTEGER`},
		{"untyped once sanitized", ColumnSnapshot{Name: "qty", Type: "eur'); DROP TABLE x; --"},
			`ALTER TABLE "items" ADD COLUMN "qty"`},
		{"not null", ColumnSnapshot{Name: "name", Type: "TEXT", NotNull: true},
			`ALTER TABLE "items" ADD COLUMN "name" TEXT NOT NULL`},
		{"numeric default", ColumnSnapshot{Name: "qty", Type: "INTEGER", DfltValue: &zero},
			`ALTER TABLE "items" ADD COLUMN "qty" INTEGER DEFAULT 0`},
		{"text default keeps its quotes", ColumnSnapshot{Name: "label", Type: "TEXT", DfltValue: &label},
			`ALTER TABLE "items" ADD COLUMN "label" TEXT DEFAULT 'n/a'`},
		{"primary key", ColumnSnapshot{Name: "id", Type: "INTEGER", PrimaryKey: true},
			`ALTER TABLE "items" ADD COLUMN "id" INTEGER PRIMARY KEY`},
		{"every clause in order",
			ColumnSnapshot{Name: "id", Type: "INTEGER", NotNull: true, DfltValue: &zero, PrimaryKey: true},
			`ALTER TABLE "items" ADD COLUMN "id" INTEGER PRIMARY KEY NOT NULL DEFAULT 0`},
		{"untyped with a constraint", ColumnSnapshot{Name: "note", NotNull: true},
			`ALTER TABLE "items" ADD COLUMN "note" NOT NULL`},
		{"quoted identifiers", ColumnSnapshot{Name: `we"ird`, Type: "TEXT"},
			`ALTER TABLE "items" ADD COLUMN "we""ird" TEXT`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addColumnStatement("items", tt.col); got != tt.want {
				t.Errorf("addColumnStatement = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGenerateRevertDDL_DropColumn_HostileDeclaredType verifies that a hostile declared type read back from
// the schema is dropped from the generated ADD COLUMN instead of being spliced into privileged DDL.
func TestGenerateRevertDDL_DropColumn_HostileDeclaredType(t *testing.T) {
	entry := &ChangeLogEntry{
		Kind: ChangeKindDDL, Operation: ChangeOpAlter,
		ObjectType: ChangeObjectTable, ObjectName: "events",
		DDLSubtype: DDLSubtypeDropColumn,
	}
	snap := &ChangeSnapshot{
		// The hostile text sits in the declared TYPE (spliced into the generated ADD COLUMN); the
		// RowPayload cell value "a" is inert — restored values always travel as bound arguments.
		ColumnMetadata: &ColumnSnapshot{Name: "payload", Type: "eur'); DROP TABLE users; --"},
		RowPayload:     []SnapshotRow{{RowID: 1, Values: map[string]any{"payload": "a"}}},
		RowCountTotal:  100,
		Truncated:      true,
	}
	unit, err := GenerateRevertDDL(entry, snap)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Sanitization drops the hostile declared type entirely: the column comes back untyped, both in the statement
	// that runs and in the one the change log records.
	want := `ALTER TABLE "events" ADD COLUMN "payload"`
	if unit.Exec[0].SQL != want || unit.Shape.SQL != want {
		t.Errorf("ADD COLUMN ran as %q, recorded as %q, want %q (type dropped)", unit.Exec[0].SQL, unit.Shape.SQL, want)
	}
}

// TestBuildBulkInsert_DropsTheRowIDColumnWhenARowCarriesNone pins that the DROP TABLE undo bulk INSERT omits the rowid
// column for the whole payload when a captured row has no rowid, rather than mixing rows with and without one.
func TestBuildBulkInsert_DropsTheRowIDColumnWhenARowCarriesNone(t *testing.T) {
	rows := []SnapshotRow{
		{RowID: 1, Values: map[string]any{"name": "a"}},
		{RowID: 0, Values: map[string]any{"name": "b"}},
	}
	stmt, err := buildBulkInsert("items", rows)
	if err != nil {
		t.Fatalf("buildBulkInsert: %v", err)
	}
	want := `INSERT INTO "items" ("name") VALUES (?), (?)`
	if stmt.SQL != want {
		t.Errorf("sql = %q, want %q", stmt.SQL, want)
	}
}
