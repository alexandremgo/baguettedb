package domain

import (
	"strings"
	"testing"
)

// GenerateChangeLogTriggers — unified _change_log trigger generator =================

func TestGenerateChangeLogTriggers_TriggerNaming(t *testing.T) {
	columns := []TriggerColumn{{Name: "id"}}
	insertTrigger, updateTrigger, deleteTrigger := GenerateChangeLogTriggers("items", columns)

	if !strings.Contains(insertTrigger, "CREATE TRIGGER \"_change_log_items_after_insert\"") {
		t.Errorf("insert trigger: wrong name, got:\n%s", insertTrigger)
	}
	if !strings.Contains(updateTrigger, "CREATE TRIGGER \"_change_log_items_after_update\"") {
		t.Errorf("update trigger: wrong name, got:\n%s", updateTrigger)
	}
	if !strings.Contains(deleteTrigger, "CREATE TRIGGER \"_change_log_items_after_delete\"") {
		t.Errorf("delete trigger: wrong name, got:\n%s", deleteTrigger)
	}
}

func TestGenerateChangeLogTriggers_WritesToChangeLogWithKindDML(t *testing.T) {
	columns := []TriggerColumn{{Name: "id"}}
	insertTrigger, updateTrigger, deleteTrigger := GenerateChangeLogTriggers("items", columns)

	triggers := map[string]string{"insert": insertTrigger, "update": updateTrigger, "delete": deleteTrigger}
	for name, sql := range triggers {
		if !strings.Contains(sql, "INSERT INTO "+ChangeLogTable) {
			t.Errorf("%s trigger: should write to %s, got:\n%s", name, ChangeLogTable, sql)
		}
		if !strings.Contains(sql, "'"+string(ChangeKindDML)+"'") {
			t.Errorf("%s trigger: should set kind='%s', got:\n%s", name, ChangeKindDML, sql)
		}
		wantCols := "(batch_id, kind, operation, table_name, row_id, " +
			"old_values, new_values, changed_by)"
		if !strings.Contains(sql, wantCols) {
			t.Errorf("%s trigger: should target the unified column list, got:\n%s", name, sql)
		}
	}
}

func TestGenerateChangeLogTriggers_RowIDAndPlaceholders(t *testing.T) {
	columns := []TriggerColumn{{Name: "id"}}
	insertTrigger, updateTrigger, deleteTrigger := GenerateChangeLogTriggers("items", columns)

	if !strings.Contains(insertTrigger, "CAST(NEW.rowid AS TEXT)") {
		t.Error("insert trigger: should use NEW.rowid")
	}
	if !strings.Contains(updateTrigger, "CAST(OLD.rowid AS TEXT)") {
		t.Error("update trigger: should use OLD.rowid")
	}
	if !strings.Contains(deleteTrigger, "CAST(OLD.rowid AS TEXT)") {
		t.Error("delete trigger: should use OLD.rowid")
	}

	expectedChangedBy := "'" + ChangePendingUser + "'"
	expectedBatchID := "'" + ChangePendingBatchID + "'"
	triggers := map[string]string{"insert": insertTrigger, "update": updateTrigger, "delete": deleteTrigger}
	for name, sql := range triggers {
		if !strings.Contains(sql, expectedChangedBy) {
			t.Errorf("%s trigger: missing changed_by placeholder", name)
		}
		if !strings.Contains(sql, expectedBatchID) {
			t.Errorf("%s trigger: missing batch_id placeholder", name)
		}
	}
}

func TestGenerateChangeLogTriggers_OperationTypes(t *testing.T) {
	columns := []TriggerColumn{{Name: "id"}}
	insertTrigger, updateTrigger, deleteTrigger := GenerateChangeLogTriggers("items", columns)

	if !strings.Contains(insertTrigger, "'INSERT'") {
		t.Error("insert trigger: operation should be INSERT")
	}
	if !strings.Contains(updateTrigger, "'UPDATE'") {
		t.Error("update trigger: operation should be UPDATE")
	}
	if !strings.Contains(deleteTrigger, "'DELETE'") {
		t.Error("delete trigger: operation should be DELETE")
	}
}

func TestGenerateChangeLogTriggers_MultipleColumns(t *testing.T) {
	columns := []TriggerColumn{
		{Name: "id"},
		{Name: "name"},
		{Name: "quantity"},
	}
	insertTrigger, updateTrigger, deleteTrigger := GenerateChangeLogTriggers("products", columns)

	for _, col := range columns {
		newRef := "NEW.\"" + col.Name + "\""
		oldRef := "OLD.\"" + col.Name + "\""

		if !strings.Contains(insertTrigger, newRef) {
			t.Errorf("insert trigger: missing NEW reference for %q", col.Name)
		}
		if !strings.Contains(updateTrigger, newRef) || !strings.Contains(updateTrigger, oldRef) {
			t.Errorf("update trigger: missing OLD/NEW reference for %q", col.Name)
		}
		if !strings.Contains(deleteTrigger, oldRef) {
			t.Errorf("delete trigger: missing OLD reference for %q", col.Name)
		}
	}
}

func TestGenerateDropChangeLogTriggers(t *testing.T) {
	drop1, drop2, drop3 := GenerateDropChangeLogTriggers("items")

	if drop1 != "DROP TRIGGER IF EXISTS \"_change_log_items_after_insert\"" {
		t.Errorf("drop1 = %q", drop1)
	}
	if drop2 != "DROP TRIGGER IF EXISTS \"_change_log_items_after_update\"" {
		t.Errorf("drop2 = %q", drop2)
	}
	if drop3 != "DROP TRIGGER IF EXISTS \"_change_log_items_after_delete\"" {
		t.Errorf("drop3 = %q", drop3)
	}
}

// TestGenerateChangeLogTriggers_QuoteBearingNames verifies that a single quote in a table or column name is
// escaped inside the generated string literals and JSON keys instead of terminating them, while the
// identifier positions keep the raw name in double quotes.
func TestGenerateChangeLogTriggers_QuoteBearingNames(t *testing.T) {
	insert, _, _ := GenerateChangeLogTriggers("it's", []TriggerColumn{{Name: "o'clock"}})

	if !strings.Contains(insert, "'it''s'") {
		t.Errorf("table_name literal not escaped, got:\n%s", insert)
	}
	if !strings.Contains(insert, "'o''clock'") {
		t.Errorf("json_object key literal not escaped, got:\n%s", insert)
	}
	if !strings.Contains(insert, `NEW."o'clock"`) {
		t.Errorf("column identifier should keep the raw name double-quoted, got:\n%s", insert)
	}
}
