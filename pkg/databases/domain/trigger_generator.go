package domain

import (
	"fmt"
	"strings"
)

// TriggerColumn represents a column used for trigger generation.
type TriggerColumn struct {
	Name string
}

// GenerateChangeLogTriggers generates the three AFTER trigger SQL statements
// (INSERT, UPDATE, DELETE) for the given table, writing into the unified
// _change_log table with kind='DML'. The app layer corrects batch_id /
// changed_by / origin via the change-log correction statement in the same
// transaction. DDL-only columns (object_type, object_name, ddl_subtype,
// ddl_statement) stay NULL on DML rows.
func GenerateChangeLogTriggers(
	tableName string, columns []TriggerColumn,
) (insertTrigger, updateTrigger, deleteTrigger string) {
	// NEW/OLD are SQLite's row references inside triggers:
	// NEW.col = value after the operation (INSERT/UPDATE), OLD.col = value before (UPDATE/DELETE).
	newJSON := buildJSONObject("NEW", columns)
	oldJSON := buildJSONObject("OLD", columns)
	changedBy := fmt.Sprintf("'%s'", ChangePendingUser)
	batchID := fmt.Sprintf("'%s'", ChangePendingBatchID)
	tableLiteral := escapeTextLiteral(tableName)
	triggerPrefix := fmt.Sprintf("_change_log_%s_after", tableName)

	insertTrigger = fmt.Sprintf(
		"CREATE TRIGGER %s AFTER INSERT ON %s\n"+
			"BEGIN\n"+
			"    INSERT INTO %s (batch_id, kind, operation, table_name, row_id, old_values, new_values, changed_by)\n"+
			"    VALUES (%s, '%s', '%s', '%s', CAST(NEW.rowid AS TEXT), NULL,\n"+
			"            %s,\n"+
			"            %s);\n"+
			"END",
		QuoteIdentifier(triggerPrefix+"_insert"), QuoteIdentifier(tableName),
		ChangeLogTable, batchID, ChangeKindDML, ChangeOpInsert, tableLiteral,
		newJSON,
		changedBy,
	)

	updateTrigger = fmt.Sprintf(
		"CREATE TRIGGER %s AFTER UPDATE ON %s\n"+
			"BEGIN\n"+
			"    INSERT INTO %s (batch_id, kind, operation, table_name, row_id, old_values, new_values, changed_by)\n"+
			"    VALUES (%s, '%s', '%s', '%s', CAST(OLD.rowid AS TEXT),\n"+
			"            %s,\n"+
			"            %s,\n"+
			"            %s);\n"+
			"END",
		QuoteIdentifier(triggerPrefix+"_update"), QuoteIdentifier(tableName),
		ChangeLogTable, batchID, ChangeKindDML, ChangeOpUpdate, tableLiteral,
		oldJSON,
		newJSON,
		changedBy,
	)

	deleteTrigger = fmt.Sprintf(
		"CREATE TRIGGER %s AFTER DELETE ON %s\n"+
			"BEGIN\n"+
			"    INSERT INTO %s (batch_id, kind, operation, table_name, row_id, old_values, new_values, changed_by)\n"+
			"    VALUES (%s, '%s', '%s', '%s', CAST(OLD.rowid AS TEXT),\n"+
			"            %s,\n"+
			"            NULL, %s);\n"+
			"END",
		QuoteIdentifier(triggerPrefix+"_delete"), QuoteIdentifier(tableName),
		ChangeLogTable, batchID, ChangeKindDML, ChangeOpDelete, tableLiteral,
		oldJSON,
		changedBy,
	)

	return insertTrigger, updateTrigger, deleteTrigger
}

// GenerateDropChangeLogTriggers generates the three DROP TRIGGER IF EXISTS
// statements for the unified _change_log trigger names. Companion to
// GenerateChangeLogTriggers.
func GenerateDropChangeLogTriggers(tableName string) (drop1, drop2, drop3 string) {
	triggerPrefix := fmt.Sprintf("_change_log_%s_after", tableName)
	drop1 = "DROP TRIGGER IF EXISTS " + QuoteIdentifier(triggerPrefix+"_insert")
	drop2 = "DROP TRIGGER IF EXISTS " + QuoteIdentifier(triggerPrefix+"_update")
	drop3 = "DROP TRIGGER IF EXISTS " + QuoteIdentifier(triggerPrefix+"_delete")
	return drop1, drop2, drop3
}

// buildJSONObject generates a json_object() call for all columns using the
// given row reference (typically "NEW" or "OLD" — SQLite's trigger-body
// pseudo-rows pointing at the post- or pre-state of the affected row). The
// column-name literal stays as a quoted string ('id'), while NEW.id / OLD.id
// is an expression that SQLite evaluates at trigger-fire time against the
// real row. Example output for columns [id, name]:
//
//	json_object('id', NEW."id", 'name', NEW."name")
func buildJSONObject(rowRef string, columns []TriggerColumn) string {
	var b strings.Builder
	b.WriteString("json_object(")
	for i, col := range columns {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "'%s', %s.%s", escapeTextLiteral(col.Name), rowRef, QuoteIdentifier(col.Name))
	}
	b.WriteString(")")
	return b.String()
}
