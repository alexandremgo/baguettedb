package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGenerateRevertSQL_Insert(t *testing.T) {
	entry := &ChangeLogEntry{
		TableName: "items",
		RowID:     "42",
		Operation: ChangeOpInsert,
	}

	sql, args, err := GenerateRevertSQL(entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sql != `DELETE FROM "items" WHERE rowid = ?` {
		t.Errorf("sql = %q, want DELETE FROM \"items\" WHERE rowid = ?", sql)
	}
	if len(args) != 1 || args[0].Value != "42" {
		t.Errorf("args = %v, want [{SQLText 42}]", args)
	}
}

func TestGenerateRevertSQL_Update(t *testing.T) {
	entry := &ChangeLogEntry{
		TableName: "items",
		RowID:     "42",
		Operation: ChangeOpUpdate,
		OldValues: map[string]any{"name": "milk", "quantity": json.Number("2")},
	}

	sql, args, err := GenerateRevertSQL(entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Keys are sorted: name, quantity.
	expectedSQL := `UPDATE "items" SET "name" = ?, "quantity" = ? WHERE rowid = ?`
	if sql != expectedSQL {
		t.Errorf("sql = %q, want %q", sql, expectedSQL)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(args))
	}
	if args[0].Value != "milk" {
		t.Errorf("args[0] = %v, want milk", args[0])
	}
	if args[1].Type != SQLInteger || args[1].Value != int64(2) {
		t.Errorf("args[1] = %v, want {SQLInteger 2}", args[1])
	}
	if args[2].Value != "42" {
		t.Errorf("args[2] = %v, want 42 (rowid)", args[2])
	}
}

func TestGenerateRevertSQL_Delete(t *testing.T) {
	entry := &ChangeLogEntry{
		TableName: "items",
		RowID:     "42",
		Operation: ChangeOpDelete,
		OldValues: map[string]any{"id": json.Number("42"), "name": "milk"},
	}

	sql, args, err := GenerateRevertSQL(entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The rowid leads, then the keys sorted: id, name.
	expectedSQL := `INSERT INTO "items" (rowid, "id", "name") VALUES (?, ?, ?)`
	if sql != expectedSQL {
		t.Errorf("sql = %q, want %q", sql, expectedSQL)
	}
	if len(args) != 3 {
		t.Fatalf("expected 3 args, got %d", len(args))
	}
	if args[0] != TextValue("42") {
		t.Errorf("args[0] = %v, want the recorded rowid", args[0])
	}
	if args[1].Type != SQLInteger || args[1].Value != int64(42) {
		t.Errorf("args[1] = %v, want {SQLInteger 42}", args[1])
	}
	if args[2].Value != "milk" {
		t.Errorf("args[2] = %v, want milk", args[2])
	}
}

// TestGenerateRevertSQL_Delete_LeavesRowIDToSQLiteWhenUnrecordedOrShadowed pins the two cases where a DELETE revert
// must not add a synthetic rowid column: the entry recorded no rowid, or a user column already claims a rowid alias.
func TestGenerateRevertSQL_Delete_LeavesRowIDToSQLiteWhenUnrecordedOrShadowed(t *testing.T) {
	tests := []struct {
		name  string
		entry *ChangeLogEntry
		want  string
	}{
		{
			name: "no rowid recorded",
			entry: &ChangeLogEntry{
				TableName: "items", Operation: ChangeOpDelete,
				OldValues: map[string]any{"name": "milk"},
			},
			want: `INSERT INTO "items" ("name") VALUES (?)`,
		},
		{
			name: "a user column claims the rowid alias",
			entry: &ChangeLogEntry{
				TableName: "items", RowID: "7", Operation: ChangeOpDelete,
				OldValues: map[string]any{"ROWID": json.Number("7"), "name": "milk"},
			},
			want: `INSERT INTO "items" ("ROWID", "name") VALUES (?, ?)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, _, err := GenerateRevertSQL(tt.entry)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sql != tt.want {
				t.Errorf("sql = %q, want %q", sql, tt.want)
			}
		})
	}
}

func TestGenerateRevertSQL_UpdateNoOldValues(t *testing.T) {
	entry := &ChangeLogEntry{
		TableName: "items",
		RowID:     "42",
		Operation: ChangeOpUpdate,
		OldValues: nil,
	}

	_, _, err := GenerateRevertSQL(entry)
	if err == nil {
		t.Error("expected error for UPDATE with no old values")
	}
}

func TestGenerateBatchRevertSQL_HomogeneousInsertBatch_CollapsesToOneDelete(t *testing.T) {
	entries := []*ChangeLogEntry{
		{Operation: ChangeOpInsert, TableName: "items", RowID: "1"},
		{Operation: ChangeOpInsert, TableName: "items", RowID: "2"},
		{Operation: ChangeOpInsert, TableName: "items", RowID: "3"},
	}
	stmts, err := GenerateBatchRevertSQL(entries)
	if err != nil {
		t.Fatalf("GenerateBatchRevertSQL: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d stmts, want 1 (collapsed)", len(stmts))
	}
	if !strings.Contains(stmts[0].SQL, "rowid IN (?, ?, ?)") {
		t.Errorf("unexpected SQL: %s", stmts[0].SQL)
	}
	if len(stmts[0].Args) != 3 {
		t.Errorf("got %d args, want 3", len(stmts[0].Args))
	}
}

func TestGenerateBatchRevertSQL_HomogeneousDeleteBatch_CollapsesToMultiVALUES(t *testing.T) {
	entries := []*ChangeLogEntry{
		{
			Operation: ChangeOpDelete, TableName: "items", RowID: "1",
			OldValues: map[string]any{"id": json.Number("1"), "name": "milk"},
		},
		{
			Operation: ChangeOpDelete, TableName: "items", RowID: "2",
			OldValues: map[string]any{"id": json.Number("2"), "name": "bread"},
		},
	}
	stmts, err := GenerateBatchRevertSQL(entries)
	if err != nil {
		t.Fatalf("GenerateBatchRevertSQL: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d stmts, want 1 (collapsed)", len(stmts))
	}
	want := `INSERT INTO "items" (rowid, "id", "name") VALUES (?, ?, ?), (?, ?, ?)`
	if stmts[0].SQL != want {
		t.Errorf("sql = %q, want %q", stmts[0].SQL, want)
	}
	if len(stmts[0].Args) != 6 {
		t.Fatalf("got %d args, want 6", len(stmts[0].Args))
	}
	if stmts[0].Args[0] != TextValue("1") || stmts[0].Args[3] != TextValue("2") {
		t.Errorf("each row must lead with its recorded rowid, got %v", stmts[0].Args)
	}
}

// TestGenerateBatchRevertSQL_HomogeneousDeleteBatch_UnrecordedRowIDDropsTheColumnForAll pins that the collapsed
// multi-row INSERT omits the rowid column for every row as soon as one entry recorded none, since one column list
// serves the whole statement.
func TestGenerateBatchRevertSQL_HomogeneousDeleteBatch_UnrecordedRowIDDropsTheColumnForAll(t *testing.T) {
	entries := []*ChangeLogEntry{
		{Operation: ChangeOpDelete, TableName: "items", RowID: "1", OldValues: map[string]any{"name": "milk"}},
		{Operation: ChangeOpDelete, TableName: "items", OldValues: map[string]any{"name": "bread"}},
	}
	stmts, err := GenerateBatchRevertSQL(entries)
	if err != nil {
		t.Fatalf("GenerateBatchRevertSQL: %v", err)
	}
	want := `INSERT INTO "items" ("name") VALUES (?), (?)`
	if len(stmts) != 1 || stmts[0].SQL != want {
		t.Errorf("stmts = %v, want one %q", stmts, want)
	}
}

func TestGenerateBatchRevertSQL_HeterogeneousBatch_FallsBackToPerRow(t *testing.T) {
	entries := []*ChangeLogEntry{
		{Operation: ChangeOpInsert, TableName: "items", RowID: "1"},
		{Operation: ChangeOpInsert, TableName: "other", RowID: "2"},
	}
	stmts, err := GenerateBatchRevertSQL(entries)
	if err != nil {
		t.Fatalf("GenerateBatchRevertSQL: %v", err)
	}
	if len(stmts) != 2 {
		t.Errorf("got %d stmts, want 2 (per-row fallback)", len(stmts))
	}
}

func TestGenerateBatchRevertSQL_UpdateBatch_AlwaysPerRow(t *testing.T) {
	entries := []*ChangeLogEntry{
		{Operation: ChangeOpUpdate, TableName: "items", RowID: "1", OldValues: map[string]any{"name": "old1"}},
		{Operation: ChangeOpUpdate, TableName: "items", RowID: "2", OldValues: map[string]any{"name": "old2"}},
	}
	stmts, err := GenerateBatchRevertSQL(entries)
	if err != nil {
		t.Fatalf("GenerateBatchRevertSQL: %v", err)
	}
	if len(stmts) != 2 {
		t.Errorf("UPDATE-revert never collapses; got %d stmts, want 2", len(stmts))
	}
}

func TestGenerateBatchRevertSQL_DeleteBatchWithMismatchedColumns_FallsBackToPerRow(t *testing.T) {
	entries := []*ChangeLogEntry{
		{
			Operation: ChangeOpDelete, TableName: "items", RowID: "1",
			OldValues: map[string]any{"id": json.Number("1"), "name": "milk"},
		},
		{
			Operation: ChangeOpDelete, TableName: "items", RowID: "2",
			OldValues: map[string]any{"id": json.Number("2")},
		},
	}
	stmts, err := GenerateBatchRevertSQL(entries)
	if err != nil {
		t.Fatalf("GenerateBatchRevertSQL: %v", err)
	}
	if len(stmts) != 2 {
		t.Errorf("non-uniform DELETE batch should fall back per-row; got %d stmts, want 2", len(stmts))
	}
}

func TestGenerateBatchRevertSQL_Empty_ReturnsNil(t *testing.T) {
	stmts, err := GenerateBatchRevertSQL(nil)
	if err != nil {
		t.Fatalf("GenerateBatchRevertSQL(nil): %v", err)
	}
	if stmts != nil {
		t.Errorf("got %v, want nil", stmts)
	}
}

func TestGenerateRevertSQL_NullValue(t *testing.T) {
	entry := &ChangeLogEntry{
		TableName: "items",
		RowID:     "42",
		Operation: ChangeOpUpdate,
		OldValues: map[string]any{"name": nil},
	}

	_, args, err := GenerateRevertSQL(entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// First arg should be the null value, second is the rowid.
	if args[0].Type != SQLNull {
		t.Errorf("args[0].Type = %v, want SQLNull", args[0].Type)
	}
}

// TestToTypedValue_JSONNumber pins that a json.Number keeps its storage class on the way back into SQLite: an
// integral literal becomes INTEGER even beyond 2^53, a real-typed literal stays REAL even when integral.
func TestToTypedValue_JSONNumber(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want TypedValue
	}{
		{"integral literal stays integer beyond 2^53", json.Number("9007199254740993"), IntegerValue(9007199254740993)},
		{"real literal stays real even when integral", json.Number("1.0"), RealValue(1.0)},
		{"fractional literal", json.Number("2.5"), RealValue(2.5)},
		{"bool becomes integer", true, IntegerValue(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toTypedValue(tt.in); got != tt.want {
				t.Errorf("toTypedValue(%#v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
