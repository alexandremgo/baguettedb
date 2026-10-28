package service

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// scriptedConflictExecutor scripts schema-state probe results by SQL
// substring match. Reuses programmableExecutor by setting matchFn.
func scriptedConflictExecutor(rules map[string]*domain.QueryResult) *programmableExecutor {
	return &programmableExecutor{
		matchFn: func(sql string) *domain.QueryResult {
			for needle, result := range rules {
				if strings.Contains(sql, needle) {
					return result
				}
			}
			return &domain.QueryResult{}
		},
	}
}

func countResult(n int64) *domain.QueryResult {
	return &domain.QueryResult{
		Columns: []domain.Column{{Name: "count"}},
		Rows:    [][]domain.TypedValue{{domain.IntegerValue(n)}},
	}
}

func presenceResult(present bool) *domain.QueryResult {
	if !present {
		return &domain.QueryResult{}
	}
	return &domain.QueryResult{
		Columns: []domain.Column{{Name: "1"}},
		Rows:    [][]domain.TypedValue{{domain.IntegerValue(1)}},
	}
}

func pragmaTableInfoResult(columns ...string) *domain.QueryResult {
	cols := []domain.Column{
		{Name: "cid"}, {Name: "name"}, {Name: "type"},
		{Name: "notnull"}, {Name: "dflt_value"}, {Name: "pk"},
	}
	rows := make([][]domain.TypedValue, len(columns))
	for i, name := range columns {
		rows[i] = []domain.TypedValue{
			domain.IntegerValue(int64(i)),
			domain.TextValue(name),
			domain.TextValue("TEXT"),
			domain.IntegerValue(0),
			domain.NullValue(),
			domain.IntegerValue(0),
		}
	}
	return &domain.QueryResult{Columns: cols, Rows: rows}
}

func newTestQueryService(exec *programmableExecutor) *QueryService {
	return &QueryService{executor: exec, dbSvc: &DatabaseService{}}
}

// --- DML branch --------------------------------------------------------

func TestDetectChangeConflicts_DML_Insert_RowMissing_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT * FROM "items" WHERE rowid`: {},
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 10, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpInsert,
		TableName: "items", RowID: "5",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil {
		t.Fatal("expected ChangeConflict, got nil")
	}
	if got.ChangeLogID != 10 || got.Kind != domain.ChangeKindDML {
		t.Errorf("conflict identity wrong: id=%d kind=%s", got.ChangeLogID, got.Kind)
	}
	if !strings.Contains(got.Reason, "items") || !strings.Contains(got.Reason, "5") {
		t.Errorf("reason should name the row and table: %q", got.Reason)
	}
	if len(got.ResolutionHints) == 0 {
		t.Error("ResolutionHints must be non-empty on conflict")
	}
}

// TestDetectChangeConflicts_DML_Insert_RowStillAsInserted_NoConflict pins that an INSERT is undoable while the live row
// still equals what was inserted — which also covers a later update that was itself undone.
func TestDetectChangeConflicts_DML_Insert_RowStillAsInserted_NoConflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT * FROM "items" WHERE rowid`: liveRow(map[string]any{"name": "milk", "qty": int64(2)}),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 10, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpInsert,
		TableName: "items", RowID: "5",
		NewValues: map[string]any{"name": "milk", "qty": json.Number("2")},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("expected no conflict, got %+v", got)
	}
}

// TestDetectChangeConflicts_DML_Insert_RowChangedSince_Conflict pins that an INSERT whose row no longer equals the
// inserted values is refused, naming the changed column, listing the later DML entry on the row as the blocker, and
// hinting at reverting it without naming any tool or operation.
func TestDetectChangeConflicts_DML_Insert_RowChangedSince_Conflict(t *testing.T) {
	foreignUpdate := &domain.ChangeLogEntry{
		ID: 11, BatchID: "b2", Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
		Origin: domain.ChangeOriginChange, ChangedBy: "bob", TableName: "items", RowID: "5",
	}
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT * FROM "items" WHERE rowid`:              liveRow(map[string]any{"name": "cheese"}),
		`kind = 'DML' AND table_name = ? AND row_id = ?`: changeLogRowFor(foreignUpdate),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 10, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpInsert,
		TableName: "items", RowID: "5", NewValues: map[string]any{"name": "milk"},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || !strings.Contains(got.Reason, `"name"`) {
		t.Fatalf("expected a changed-column conflict, got %+v", got)
	}
	if len(got.BlockedBy) != 1 || got.BlockedBy[0].ID != 11 {
		t.Errorf("BlockedBy = %+v, want the foreign update", got.BlockedBy)
	}
	want := []string{
		"The row changed after this insert. Revert the newer change to the row first, or leave this change in place.",
	}
	if !slices.Equal(got.ResolutionHints, want) {
		t.Errorf("ResolutionHints = %q, want %q", got.ResolutionHints, want)
	}
}

// TestDetectChangeConflicts_DML_ColumnDropped_SchemaDriftConflict pins, for INSERT and UPDATE alike, that a recorded
// column the live row no longer has raises a schema-drift conflict: the reason names the column, the blockers are the
// table's DDL entries since the change, and the hint points at reverting that schema change.
func TestDetectChangeConflicts_DML_ColumnDropped_SchemaDriftConflict(t *testing.T) {
	dropColumn := &domain.ChangeLogEntry{
		ID: 12, BatchID: "b3", Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpAlter,
		Origin: domain.ChangeOriginChange, ObjectType: domain.ChangeObjectTable, ObjectName: "items",
		DDLSubtype: domain.DDLSubtypeDropColumn, DDLStatement: "ALTER TABLE items DROP COLUMN qty",
	}
	for _, op := range []string{domain.ChangeOpInsert, domain.ChangeOpUpdate} {
		t.Run(op, func(t *testing.T) {
			exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
				`SELECT * FROM "items" WHERE rowid`: liveRow(map[string]any{"name": "milk"}),
				`kind = 'DDL' AND object_name = ?`:  changeLogRowFor(dropColumn),
			})
			s := newTestQueryService(exec)
			entry := &domain.ChangeLogEntry{
				ID: 10, Kind: domain.ChangeKindDML, Operation: op, TableName: "items", RowID: "5",
				NewValues: map[string]any{"name": "milk", "qty": json.Number("2")},
			}
			got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got == nil || !strings.Contains(got.Reason, `no longer has column "qty"`) {
				t.Fatalf("expected a schema-drift conflict, got %+v", got)
			}
			if len(got.BlockedBy) != 1 || got.BlockedBy[0].ID != 12 {
				t.Errorf("BlockedBy = %+v, want the DROP COLUMN entry", got.BlockedBy)
			}
			if len(got.ResolutionHints) != 1 || !strings.Contains(got.ResolutionHints[0], "schema change") {
				t.Errorf("ResolutionHints = %v, want a schema-change hint", got.ResolutionHints)
			}
		})
	}
}

// liveRow renders a live row as a SELECT * result with its columns in name order.
func liveRow(values map[string]any) *domain.QueryResult {
	cols := make([]domain.Column, 0, len(values))
	row := make([]domain.TypedValue, 0, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		cols = append(cols, domain.Column{Name: name})
		row = append(row, domain.TypedValue{Value: values[name]})
	}
	return &domain.QueryResult{Columns: cols, Rows: [][]domain.TypedValue{row}}
}

func TestDetectChangeConflicts_DML_Delete_RowReInserted_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`WHERE rowid = ? LIMIT 1`: presenceResult(true),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 11, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpDelete,
		TableName: "items", RowID: "7",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || !strings.Contains(got.Reason, "re-inserted") {
		t.Fatalf("expected re-insert conflict, got %+v", got)
	}
}

func TestDetectChangeConflicts_DML_Update_ValueMismatch_Conflict(t *testing.T) {
	rows := [][]domain.TypedValue{{domain.IntegerValue(3), domain.TextValue("CHANGED")}}
	cols := []domain.Column{{Name: "rowid"}, {Name: "name"}}
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT * FROM "items" WHERE rowid`: {Columns: cols, Rows: rows},
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 12, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
		TableName: "items", RowID: "3",
		NewValues: map[string]any{"name": "expected"},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || !strings.Contains(got.Reason, "name") {
		t.Fatalf("expected column-mismatch conflict, got %+v", got)
	}
}

// --- DDL branch --------------------------------------------------------

// TestDetectChangeConflicts_DDL_CreateTable_NonEmpty_Conflict pins the CREATE TABLE rule: rows written since the
// CREATE by a logged DML change block the undo, and those changes are the blockers.
func TestDetectChangeConflicts_DDL_CreateTable_NonEmpty_Conflict(t *testing.T) {
	bobInsert := &domain.ChangeLogEntry{
		ID: 25, BatchID: "b-bob", Kind: domain.ChangeKindDML, Operation: domain.ChangeOpInsert,
		Origin: domain.ChangeOriginChange, ChangedBy: "bob", TableName: "t", RowID: "1",
		NewValues: map[string]any{"x": "1"},
	}
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT COUNT(*) FROM "t"`:            countResult(3),
		`AND kind = 'DML' AND table_name = ?`: changeLogRowFor(bobInsert),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 20, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpCreate,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil {
		t.Fatal("expected conflict (table not empty)")
	}
	if !strings.Contains(got.Reason, "not empty") || !strings.Contains(got.Reason, "3") {
		t.Errorf("reason should mention row count: %q", got.Reason)
	}
	if len(got.BlockedBy) != 1 || got.BlockedBy[0].ID != 25 {
		t.Errorf("BlockedBy = %+v, want Bob's insert", got.BlockedBy)
	}
	if len(got.ResolutionHints) == 0 {
		t.Error("ResolutionHints must be non-empty")
	}
}

// TestDetectChangeConflicts_DDL_CreateTable_RowsWithoutLoggedWriter_NoConflict pins that a non-empty table whose
// rows have no logged writer (a recorded DML change since the CREATE) — restored by a reversal, or produced by the
// CREATE itself — does not block the undo: the DROP's own snapshot captures them.
func TestDetectChangeConflicts_DDL_CreateTable_RowsWithoutLoggedWriter_NoConflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT COUNT(*) FROM "t"`: countResult(3),
		`SELECT sql FROM sqlite_master`: {
			Rows: [][]domain.TypedValue{{domain.TextValue("CREATE TABLE t (x)")}},
		},
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 20, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpCreate,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("expected no conflict, got %+v", got)
	}
}

// TestDetectChangeConflicts_DDL_CreateVirtualTable_NonEmpty_Conflict pins that a non-empty virtual table keeps the
// strict rule: its writes bypass the change log, so nothing could vouch for the rows the DROP would discard.
func TestDetectChangeConflicts_DDL_CreateVirtualTable_NonEmpty_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT COUNT(*) FROM "t"`: countResult(3),
		`SELECT sql FROM sqlite_master`: {
			Rows: [][]domain.TypedValue{{domain.TextValue("CREATE VIRTUAL TABLE t USING fts5(body)")}},
		},
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 20, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpCreate,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || len(got.BlockedBy) != 0 {
		t.Fatalf("expected a conflict without blockers, got %+v", got)
	}
}

// TestDetectChangeConflicts_DDL_UndoOriginRow_IsProbed pins that a DDL row written by a reversal is checked like any
// other forward row: undoing an undo's DROP TABLE is refused when an object of that name has since appeared.
func TestDetectChangeConflicts_DDL_UndoOriginRow_IsProbed(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT 1 FROM sqlite_master WHERE name = ?`: presenceResult(true),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 30, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpDrop, Origin: domain.ChangeOriginUndo,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || !strings.Contains(got.Reason, "already exists") {
		t.Fatalf("expected the undo-origin DROP to conflict on the name collision, got %+v", got)
	}
}

func TestDetectChangeConflicts_DDL_CreateTable_Empty_NoConflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT COUNT(*) FROM "t"`: countResult(0),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 20, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpCreate,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("expected no conflict, got %+v", got)
	}
}

func TestDetectChangeConflicts_DDL_DropTable_NameCollision_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`FROM sqlite_master WHERE name = ?`: presenceResult(true),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 21, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpDrop,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || !strings.Contains(got.Reason, "already exists") {
		t.Fatalf("expected name-collision conflict, got %+v", got)
	}
}

func TestDetectChangeConflicts_DDL_AddColumn_ColumnGone_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`PRAGMA table_info("t")`: pragmaTableInfoResult("id", "name"),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 22, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpAlter,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
		DDLSubtype:  domain.DDLSubtypeAddColumn,
		DDLMetadata: &domain.DDLAlterMetadata{ColumnName: "score"},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil {
		t.Fatal("expected conflict — column was removed elsewhere")
	}
	if got.Subtype != domain.DDLSubtypeAddColumn {
		t.Errorf("Subtype = %q, want AddColumn", got.Subtype)
	}
	if !strings.Contains(got.Reason, "score") {
		t.Errorf("reason should mention column name: %q", got.Reason)
	}
}

func TestDetectChangeConflicts_DDL_AddColumn_ColumnPresent_NoConflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`PRAGMA table_info("t")`: pragmaTableInfoResult("id", "name", "score"),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 22, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpAlter,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
		DDLSubtype:  domain.DDLSubtypeAddColumn,
		DDLMetadata: &domain.DDLAlterMetadata{ColumnName: "score"},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("expected no conflict, got %+v", got)
	}
}

func TestDetectChangeConflicts_DDL_DropColumn_ColumnReAdded_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`PRAGMA table_info("t")`: pragmaTableInfoResult("id", "name", "score"),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 23, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpAlter,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
		DDLSubtype:  domain.DDLSubtypeDropColumn,
		DDLMetadata: &domain.DDLAlterMetadata{ColumnName: "score"},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || got.Subtype != domain.DDLSubtypeDropColumn {
		t.Fatalf("expected DropColumn conflict, got %+v", got)
	}
	if !strings.Contains(got.Reason, "re-added") {
		t.Errorf("reason should mention re-addition: %q", got.Reason)
	}
}

func TestDetectChangeConflicts_DDL_RenameColumn_TargetMissing_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`PRAGMA table_info("t")`: pragmaTableInfoResult("id", "name"),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 24, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpAlter,
		ObjectType: domain.ChangeObjectTable, ObjectName: "t",
		DDLSubtype:  domain.DDLSubtypeRenameColumn,
		DDLMetadata: &domain.DDLAlterMetadata{ColumnName: "old", NewColumnName: "newcol"},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || got.Subtype != domain.DDLSubtypeRenameColumn {
		t.Fatalf("expected RenameColumn conflict, got %+v", got)
	}
	if !strings.Contains(got.Reason, "newcol") {
		t.Errorf("reason should mention new column name: %q", got.Reason)
	}
}

func TestDetectChangeConflicts_DDL_RenameTo_TargetMissing_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`FROM sqlite_master WHERE name = ?`: presenceResult(false),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 25, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpAlter,
		ObjectType: domain.ChangeObjectTable, ObjectName: "old_t",
		DDLSubtype:  domain.DDLSubtypeRenameTo,
		DDLMetadata: &domain.DDLAlterMetadata{NewTableName: "new_t"},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || got.Subtype != domain.DDLSubtypeRenameTo {
		t.Fatalf("expected RenameTo conflict, got %+v", got)
	}
	if !strings.Contains(got.Reason, "new_t") {
		t.Errorf("reason should mention new table name: %q", got.Reason)
	}
}

func TestDetectChangeConflicts_DDL_CreateIndex_Missing_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`FROM sqlite_master WHERE name = ?`: presenceResult(false),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 26, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpCreate,
		ObjectType: domain.ChangeObjectIndex, ObjectName: "idx_x",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || !strings.Contains(got.Reason, "idx_x") {
		t.Fatalf("expected CreateIndex undo conflict, got %+v", got)
	}
}

func TestDetectChangeConflicts_DDL_DropIndex_NameCollision_Conflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`FROM sqlite_master WHERE name = ?`: presenceResult(true),
	})
	s := newTestQueryService(exec)
	entry := &domain.ChangeLogEntry{
		ID: 27, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpDrop,
		ObjectType: domain.ChangeObjectIndex, ObjectName: "idx_x",
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", []*domain.ChangeLogEntry{entry})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || !strings.Contains(got.Reason, "already exists") {
		t.Fatalf("expected name-collision conflict, got %+v", got)
	}
}

func TestDetectChangeConflicts_AllConflictsHaveResolutionHints(t *testing.T) {
	// Sanity invariant: every code path that produces a ChangeConflict
	// must include at least one ResolutionHint.
	cases := []struct {
		name  string
		exec  *programmableExecutor
		entry *domain.ChangeLogEntry
	}{
		{
			name: "DML insert missing",
			exec: scriptedConflictExecutor(map[string]*domain.QueryResult{
				`WHERE rowid = ? LIMIT 1`: presenceResult(false),
			}),
			entry: &domain.ChangeLogEntry{ID: 1, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpInsert,
				TableName: "t", RowID: "1"},
		},
		{
			name: "DDL drop name collision",
			exec: scriptedConflictExecutor(map[string]*domain.QueryResult{
				`FROM sqlite_master WHERE name = ?`: presenceResult(true),
			}),
			entry: &domain.ChangeLogEntry{ID: 2, Kind: domain.ChangeKindDDL, Operation: domain.ChangeOpDrop,
				ObjectType: domain.ChangeObjectTable, ObjectName: "t"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestQueryService(tc.exec)
			got, err := s.detectChangeConflicts(context.Background(), tc.exec, "ns", []*domain.ChangeLogEntry{tc.entry})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got == nil {
				t.Fatal("expected conflict")
			}
			if len(got.ResolutionHints) == 0 {
				t.Error("ResolutionHints empty — every conflict must include at least one")
			}
		})
	}
}

func TestDetectChangeConflicts_Empty_NoConflict(t *testing.T) {
	exec := scriptedConflictExecutor(nil)
	s := newTestQueryService(exec)
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("expected nil conflict for empty entries, got %+v", got)
	}
}

// --- Multi-touch batches (same row written more than once) -------------

// multiTouchUpdateEntries builds a two-entry batch where both UPDATEs hit items row 3: the older one
// snapshots an intermediate NewValues state, the newer one the batch's final state.
func multiTouchUpdateEntries() []*domain.ChangeLogEntry {
	return []*domain.ChangeLogEntry{
		{
			ID: 21, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
			TableName: "items", RowID: "3",
			NewValues: map[string]any{"name": "final"},
		},
		{
			ID: 20, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
			TableName: "items", RowID: "3",
			NewValues: map[string]any{"name": "intermediate"},
		},
	}
}

// TestDetectChangeConflicts_DML_MultiTouchRow_IntermediateStateSkipped verifies that a batch touching one row twice
// does not conflict with itself: the older entry's snapshot ("intermediate") differs from the live row, but only the
// row's newest entry describes live state, so the batch stays undoable.
func TestDetectChangeConflicts_DML_MultiTouchRow_IntermediateStateSkipped(t *testing.T) {
	// The live row at undo time: name matches the newest entry's recorded post-state, i.e. nothing moved since the
	// batch ran.
	rows := [][]domain.TypedValue{{domain.IntegerValue(3), domain.TextValue("final")}}
	cols := []domain.Column{{Name: "rowid"}, {Name: "name"}}
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT * FROM "items" WHERE rowid`: {Columns: cols, Rows: rows},
	})
	s := newTestQueryService(exec)
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", multiTouchUpdateEntries())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("expected no conflict when the row's newest entry matches live state, got %+v", got)
	}
}

// TestDetectChangeConflicts_DML_MultiTouchRow_NewestEntryStillChecked verifies that skipping intermediate entries
// does not weaken real conflict detection: when the live row matches no recorded snapshot, a later change moved the
// row and the conflict is raised on the row's newest entry.
func TestDetectChangeConflicts_DML_MultiTouchRow_NewestEntryStillChecked(t *testing.T) {
	// The live row at undo time: name matches neither recorded post-state, i.e. the row changed after the batch.
	rows := [][]domain.TypedValue{{domain.IntegerValue(3), domain.TextValue("moved-by-someone-else")}}
	cols := []domain.Column{{Name: "rowid"}, {Name: "name"}}
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT * FROM "items" WHERE rowid`: {Columns: cols, Rows: rows},
	})
	s := newTestQueryService(exec)
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", multiTouchUpdateEntries())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || got.ChangeLogID != 21 {
		t.Fatalf("expected a conflict on the row's newest entry (id 21), got %+v", got)
	}
}

// TestDetectChangeConflicts_DML_MultiTouchRow_UpdateThenDelete_NoConflict verifies the multi-touch rule across mixed
// operations: the batch updated row 7 then deleted it, so only the DELETE's precondition applies (the row must be
// gone); the UPDATE entry is skipped — its check would find the row missing and falsely conflict.
func TestDetectChangeConflicts_DML_MultiTouchRow_UpdateThenDelete_NoConflict(t *testing.T) {
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`WHERE rowid = ? LIMIT 1`: presenceResult(false),
	})
	s := newTestQueryService(exec)
	entries := []*domain.ChangeLogEntry{
		{
			ID: 31, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpDelete,
			TableName: "items", RowID: "7",
			OldValues: map[string]any{"name": "gone"},
		},
		{
			ID: 30, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
			TableName: "items", RowID: "7",
			NewValues: map[string]any{"name": "gone"},
		},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", entries)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Errorf("expected no conflict for update-then-delete on one row, got %+v", got)
	}
}

// TestDetectChangeConflicts_DML_MultiTouchRow_SameRowIDDifferentTables_BothChecked verifies that the multi-touch rule
// keys on (table, row) and not row_id alone: the older entry belongs to another table and is that table's newest
// entry, so its mismatch still conflicts.
func TestDetectChangeConflicts_DML_MultiTouchRow_SameRowIDDifferentTables_BothChecked(t *testing.T) {
	itemsCols := []domain.Column{{Name: "rowid"}, {Name: "name"}}
	itemsRows := [][]domain.TypedValue{{domain.IntegerValue(3), domain.TextValue("final")}}
	otherCols := []domain.Column{{Name: "rowid"}, {Name: "label"}}
	otherRows := [][]domain.TypedValue{{domain.IntegerValue(3), domain.TextValue("moved")}}
	exec := scriptedConflictExecutor(map[string]*domain.QueryResult{
		`SELECT * FROM "items" WHERE rowid`: {Columns: itemsCols, Rows: itemsRows},
		`SELECT * FROM "other" WHERE rowid`: {Columns: otherCols, Rows: otherRows},
	})
	s := newTestQueryService(exec)
	entries := []*domain.ChangeLogEntry{
		{
			ID: 41, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
			TableName: "items", RowID: "3",
			NewValues: map[string]any{"name": "final"},
		},
		{
			ID: 40, Kind: domain.ChangeKindDML, Operation: domain.ChangeOpUpdate,
			TableName: "other", RowID: "3",
			NewValues: map[string]any{"label": "expected"},
		},
	}
	got, err := s.detectChangeConflicts(context.Background(), exec, "ns", entries)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || got.ChangeLogID != 40 {
		t.Fatalf("expected a conflict on the other-table entry (id 40), got %+v", got)
	}
}

// TestConflictValue checks how a conflict's reason spells a value: NULL for a missing one, text quoted, a number in
// its shortest form whatever its recorded spelling, and a blob by its size.
func TestConflictValue(t *testing.T) {
	for _, tt := range []struct {
		v    any
		want string
	}{
		{nil, "NULL"},
		{"x", `"x"`},
		{json.Number("7.0"), "7"},
		{json.Number("7.5"), "7.5"},
		{json.Number("9007199254740993"), "9007199254740993"},
		{int64(8), "8"},
		{8.0, "8"},
		{[]byte{1, 2}, "<blob, 2 bytes>"},
	} {
		if got := conflictValue(tt.v); got != tt.want {
			t.Errorf("conflictValue(%#v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}
