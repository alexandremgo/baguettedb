package domain

import (
	"encoding/json"
	"testing"
)

func TestChangeKind_Helpers(t *testing.T) {
	if !ChangeKindDML.IsDML() {
		t.Error("ChangeKindDML.IsDML() should be true")
	}
	if ChangeKindDML.IsDDL() {
		t.Error("ChangeKindDML.IsDDL() should be false")
	}
	if !ChangeKindDDL.IsDDL() {
		t.Error("ChangeKindDDL.IsDDL() should be true")
	}
	if ChangeKindDDL.IsDML() {
		t.Error("ChangeKindDDL.IsDML() should be false")
	}
}

func TestChangeLogEntry_DMLShape(t *testing.T) {
	entry := &ChangeLogEntry{
		BatchID:   "batch-1",
		Kind:      ChangeKindDML,
		Operation: ChangeOpInsert,
		Origin:    ChangeOriginChange,
		ChangedBy: "user-1",
		TableName: "items",
		RowID:     "1",
		NewValues: map[string]any{"name": "bread"},
	}

	if !entry.IsDML() {
		t.Error("DML entry should report IsDML()")
	}
	if entry.IsDDL() {
		t.Error("DML entry should not report IsDDL()")
	}
	// DDL-side fields stay zero for a DML entry — by convention, not by enforcement.
	if entry.ObjectType != "" || entry.ObjectName != "" || entry.DDLSubtype != "" || entry.DDLStatement != "" {
		t.Errorf("DML entry should have zero DDL fields, got ObjectType=%q ObjectName=%q DDLSubtype=%q DDLStatement=%q",
			entry.ObjectType, entry.ObjectName, entry.DDLSubtype, entry.DDLStatement)
	}
}

func TestChangeLogEntry_DDLShape(t *testing.T) {
	entry := &ChangeLogEntry{
		BatchID:      "batch-2",
		Kind:         ChangeKindDDL,
		Operation:    ChangeOpAlter,
		Origin:       ChangeOriginChange,
		ChangedBy:    "user-1",
		ObjectType:   ChangeObjectTable,
		ObjectName:   "items",
		DDLSubtype:   "DropColumn",
		DDLStatement: "ALTER TABLE items DROP COLUMN qty",
	}

	if !entry.IsDDL() {
		t.Error("DDL entry should report IsDDL()")
	}
	if entry.IsDML() {
		t.Error("DDL entry should not report IsDML()")
	}
	// DML-side fields stay zero for a DDL entry.
	if entry.TableName != "" || entry.RowID != "" || entry.OldValues != nil || entry.NewValues != nil {
		t.Errorf("DDL entry should have zero DML fields, got TableName=%q RowID=%q OldValues=%v NewValues=%v",
			entry.TableName, entry.RowID, entry.OldValues, entry.NewValues)
	}
}

func TestColumnSnapshot_JSONRoundTrip_NilDefault(t *testing.T) {
	original := ColumnSnapshot{
		Name:       "email",
		Type:       "TEXT",
		NotNull:    true,
		DfltValue:  nil,
		PrimaryKey: false,
		ClauseSQL:  `"email" TEXT NOT NULL`,
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ColumnSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.DfltValue != nil {
		t.Errorf("DfltValue round-tripped to non-nil: %v (raw=%s)", *decoded.DfltValue, raw)
	}
	if decoded.Name != original.Name || decoded.Type != original.Type ||
		decoded.NotNull != original.NotNull || decoded.PrimaryKey != original.PrimaryKey ||
		decoded.ClauseSQL != original.ClauseSQL {
		t.Errorf("decoded mismatch: %+v vs %+v", decoded, original)
	}
}

func TestColumnSnapshot_JSONRoundTrip_WithDefault(t *testing.T) {
	defaultExpr := "'unknown'"
	original := ColumnSnapshot{
		Name:       "status",
		Type:       "TEXT",
		NotNull:    true,
		DfltValue:  &defaultExpr,
		PrimaryKey: false,
		ClauseSQL:  `"status" TEXT NOT NULL DEFAULT 'unknown'`,
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded ColumnSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.DfltValue == nil {
		t.Fatal("DfltValue round-tripped to nil; want non-nil")
	}
	if *decoded.DfltValue != defaultExpr {
		t.Errorf("DfltValue = %q, want %q", *decoded.DfltValue, defaultExpr)
	}
}

func TestChangeSnapshot_ShapeForDropColumn(t *testing.T) {
	colName := "qty"
	snap := ChangeSnapshot{
		ChangeLogID: 42,
		ColumnMetadata: &ColumnSnapshot{
			Name:    colName,
			Type:    "INTEGER",
			NotNull: false,
		},
		RowPayload: []SnapshotRow{
			{RowID: 1, Values: map[string]any{colName: int64(3)}},
			{RowID: 2, Values: map[string]any{colName: int64(7)}},
		},
		RowCountTotal: 2,
		Truncated:     false,
	}

	if snap.ColumnMetadata == nil {
		t.Fatal("DROP COLUMN snapshot must populate ColumnMetadata")
	}
	if len(snap.DependentObjects) != 0 {
		t.Errorf("DROP COLUMN snapshot should not populate DependentObjects, got %d", len(snap.DependentObjects))
	}
	if len(snap.RowPayload) != snap.RowCountTotal {
		t.Errorf("RowPayload length %d should equal RowCountTotal %d when not truncated",
			len(snap.RowPayload), snap.RowCountTotal)
	}
}

func TestChangeSnapshot_ShapeForDropTable(t *testing.T) {
	snap := ChangeSnapshot{
		ChangeLogID: 43,
		DependentObjects: []DependentObject{
			{Kind: DependentKindTable, Name: "items", SQL: "CREATE TABLE items (...)"},
			{Kind: DependentKindIndex, Name: "idx_items_name", SQL: "CREATE INDEX ..."},
			{Kind: DependentKindSkillTarget, Name: "skill-1:items", SQL: `{"skill_id":"...","table_name":"items"}`},
		},
		RowPayload: []SnapshotRow{
			{RowID: 1, Values: map[string]any{"name": "bread", "qty": int64(2)}},
		},
		RowCountTotal: 1,
		Truncated:     false,
	}

	if snap.ColumnMetadata != nil {
		t.Error("DROP TABLE snapshot should not populate ColumnMetadata")
	}
	if len(snap.DependentObjects) == 0 {
		t.Fatal("DROP TABLE snapshot must populate DependentObjects")
	}
	var foundTable bool
	for _, dep := range snap.DependentObjects {
		if dep.Kind == DependentKindTable {
			foundTable = true
			break
		}
	}
	if !foundTable {
		t.Error("DROP TABLE snapshot DependentObjects should include the TABLE entry")
	}
}

func TestChangeSnapshot_TruncatedFlag(t *testing.T) {
	snap := ChangeSnapshot{
		RowPayload:    []SnapshotRow{{RowID: 1, Values: map[string]any{"x": int64(1)}}},
		RowCountTotal: 15000,
		Truncated:     true,
	}

	if !snap.Truncated {
		t.Error("Truncated flag should be true")
	}
	if len(snap.RowPayload) >= snap.RowCountTotal {
		t.Errorf("Truncated snapshot: RowPayload length (%d) should be less than RowCountTotal (%d)",
			len(snap.RowPayload), snap.RowCountTotal)
	}
}
