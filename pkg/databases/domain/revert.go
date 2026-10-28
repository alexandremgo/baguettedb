package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// GenerateRevertSQL generates the inverse SQL statement to undo a DML
// change log entry. Returns the SQL string and the positional arguments.
func GenerateRevertSQL(entry *ChangeLogEntry) (string, []TypedValue, error) {
	switch entry.Operation {
	case ChangeOpInsert:
		return generateRevertInsert(entry)
	case ChangeOpUpdate:
		return generateRevertUpdate(entry)
	case ChangeOpDelete:
		return generateRevertDelete(entry)
	default:
		return "", nil, fmt.Errorf("unsupported operation for revert: %s", entry.Operation)
	}
}

// generateRevertInsert reverts an INSERT by deleting the row.
func generateRevertInsert(entry *ChangeLogEntry) (string, []TypedValue, error) {
	sql := fmt.Sprintf("DELETE FROM %s WHERE rowid = ?", QuoteIdentifier(entry.TableName))
	args := []TypedValue{TextValue(entry.RowID)}
	return sql, args, nil
}

// generateRevertUpdate reverts an UPDATE by restoring old values.
func generateRevertUpdate(entry *ChangeLogEntry) (string, []TypedValue, error) {
	if len(entry.OldValues) == 0 {
		return "", nil, errors.New("cannot revert UPDATE: no old values")
	}

	// Sorts keys for deterministic SQL output.
	keys := sortedKeys(entry.OldValues)

	setClauses := make([]string, len(keys))
	args := make([]TypedValue, 0, len(keys)+1)
	for i, k := range keys {
		setClauses[i] = QuoteIdentifier(k) + " = ?"
		args = append(args, toTypedValue(entry.OldValues[k]))
	}
	args = append(args, TextValue(entry.RowID))

	sql := fmt.Sprintf("UPDATE %s SET %s WHERE rowid = ?",
		QuoteIdentifier(entry.TableName),
		strings.Join(setClauses, ", "))

	return sql, args, nil
}

// generateRevertDelete reverts a DELETE by re-inserting the row with its old values under its original rowid, so
// that entries recorded against that rowid before the delete stay addressable. The rowid is left to SQLite when the
// entry carries none or when a user column claims a rowid alias.
func generateRevertDelete(entry *ChangeLogEntry) (string, []TypedValue, error) {
	if len(entry.OldValues) == 0 {
		return "", nil, errors.New("cannot revert DELETE: no old values")
	}

	keys := sortedKeys(entry.OldValues)

	columns := make([]string, 0, len(keys)+1)
	placeholders := make([]string, 0, len(keys)+1)
	args := make([]TypedValue, 0, len(keys)+1)
	// Restores the rowid only when one was recorded and no user column already carries the row's identity.
	if entry.RowID != "" && !shadowsRowID(keys) {
		columns = append(columns, "rowid")
		placeholders = append(placeholders, "?")
		args = append(args, TextValue(entry.RowID))
	}
	for _, k := range keys {
		columns = append(columns, QuoteIdentifier(k))
		placeholders = append(placeholders, "?")
		args = append(args, toTypedValue(entry.OldValues[k]))
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		QuoteIdentifier(entry.TableName),
		strings.Join(columns, ", "),
		strings.Join(placeholders, ", "))

	return sql, args, nil
}

// GenerateBatchRevertSQL generates the inverse SQL for a batch of DML
// change log entries, opportunistically collapsing homogeneous runs into
// one statement to avoid N round-trips on bulk undo.
//
// Collapsed shapes:
//   - All INSERT-revert on the same table → one DELETE FROM t WHERE rowid IN (?, ?, …).
//   - All DELETE-revert on the same table with the same column set → one
//     INSERT INTO t (cols) VALUES (?,?,?), (?,?,?), …
//
// UPDATE-revert always falls back to per-row (each row's SET clause
// uses the row's own old_values, so they can't share one statement).
// Heterogeneous batches (mixed ops or mixed tables, or DELETE-revert
// with non-uniform column sets) also fall back to per-row.
//
// Returns one SQLStatement per row in the fallback case, or one
// collapsed SQLStatement otherwise. Empty entries returns nil, nil.
func GenerateBatchRevertSQL(entries []*ChangeLogEntry) ([]SQLStatement, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if collapsed, ok, err := tryCollapseRevert(entries); err != nil {
		return nil, err
	} else if ok {
		return []SQLStatement{collapsed}, nil
	}
	out := make([]SQLStatement, len(entries))
	for i, e := range entries {
		sql, args, err := GenerateRevertSQL(e)
		if err != nil {
			return nil, err
		}
		out[i] = SQLStatement{SQL: sql, Args: args}
	}
	return out, nil
}

// tryCollapseRevert returns a single collapsed revert SQLStatement when
// all entries share (table, op) and the op is INSERT-revert (rowid-only)
// or DELETE-revert with a uniform column set across entries. Returns
// ok=false otherwise; callers fall back to per-row revert.
func tryCollapseRevert(entries []*ChangeLogEntry) (SQLStatement, bool, error) {
	first := entries[0]
	for _, e := range entries[1:] {
		if e.TableName != first.TableName || e.Operation != first.Operation {
			return SQLStatement{}, false, nil
		}
	}
	switch first.Operation {
	case ChangeOpInsert:
		// One DELETE WHERE rowid IN (?, ?, …) for the whole batch.
		placeholders := make([]string, len(entries))
		args := make([]TypedValue, len(entries))
		for i, e := range entries {
			placeholders[i] = "?"
			args[i] = TextValue(e.RowID)
		}
		sql := fmt.Sprintf("DELETE FROM %s WHERE rowid IN (%s)",
			QuoteIdentifier(first.TableName),
			strings.Join(placeholders, ", "))
		return SQLStatement{SQL: sql, Args: args}, true, nil

	case ChangeOpDelete:
		// One multi-VALUES INSERT for the whole batch — only valid if
		// every entry has the same column set.
		if len(first.OldValues) == 0 {
			return SQLStatement{}, false, nil
		}
		keys := sortedKeys(first.OldValues)
		for _, e := range entries[1:] {
			if len(e.OldValues) != len(keys) {
				return SQLStatement{}, false, nil
			}
			for _, k := range keys {
				if _, ok := e.OldValues[k]; !ok {
					return SQLStatement{}, false, nil
				}
			}
		}
		// One column list serves every row, so the rowid is restored only when every entry recorded one and no user
		// column already carries the row's identity.
		restoreRowID := !shadowsRowID(keys)
		for _, e := range entries {
			if e.RowID == "" {
				restoreRowID = false
			}
		}
		columns := make([]string, 0, len(keys)+1)
		if restoreRowID {
			columns = append(columns, "rowid")
		}
		for _, k := range keys {
			columns = append(columns, QuoteIdentifier(k))
		}
		rowPlaceholders := "(" + strings.Repeat("?, ", len(columns)-1) + "?)"
		valueGroups := make([]string, len(entries))
		args := make([]TypedValue, 0, len(entries)*len(columns))
		for i, e := range entries {
			valueGroups[i] = rowPlaceholders
			if restoreRowID {
				args = append(args, TextValue(e.RowID))
			}
			for _, k := range keys {
				args = append(args, toTypedValue(e.OldValues[k]))
			}
		}
		sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s",
			QuoteIdentifier(first.TableName),
			strings.Join(columns, ", "),
			strings.Join(valueGroups, ", "))
		return SQLStatement{SQL: sql, Args: args}, true, nil

	default:
		// UPDATE-revert can't collapse — each row has its own SET clause.
		return SQLStatement{}, false, nil
	}
}

// rowIDAliases lists the names SQLite resolves to a table's rowid unless a user column claims the name.
var rowIDAliases = map[string]bool{"rowid": true, "_rowid_": true, "oid": true}

// shadowsRowID reports whether one of the column names claims a rowid alias, in which case the row's identity already
// lives in that column and a rebuild must not add a synthetic rowid column.
func shadowsRowID(columns []string) bool {
	for _, c := range columns {
		if rowIDAliases[strings.ToLower(c)] {
			return true
		}
	}
	return false
}

// sortedKeys returns the keys of a map in sorted order.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// toTypedValue converts a value decoded from the change log's JSON to a TypedValue. A json.Number keeps its storage
// class: an integral literal becomes INTEGER (exact beyond 2^53), anything else REAL.
func toTypedValue(v any) TypedValue {
	switch val := v.(type) {
	case nil:
		return NullValue()
	case json.Number:
		if n, err := val.Int64(); err == nil {
			return IntegerValue(n)
		}
		if f, err := val.Float64(); err == nil {
			return RealValue(f)
		}
		return TextValue(val.String())
	case string:
		return TextValue(val)
	case bool:
		if val {
			return IntegerValue(1)
		}
		return IntegerValue(0)
	default:
		return TextValue(fmt.Sprintf("%v", v))
	}
}
