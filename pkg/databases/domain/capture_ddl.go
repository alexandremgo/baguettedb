package domain

import (
	"errors"
	"fmt"
	"strings"
)

// ParseCreateTableNameAndBody splits a CREATE TABLE statement (typically
// read from sqlite_master.sql) into (tableName, body), where body is
// everything from the opening "(" through the end of the statement.
// Handles the four common quoting forms for the table identifier:
// plain, "double-quoted", `backtick-quoted`, [bracket-quoted]. An optional
// "IF NOT EXISTS" clause is skipped. Returns the unquoted table name.
//
// Used at capture time to record the table body separately from its name,
// so destructive-DDL undo can rebuild the table with any name (the source
// name or a shadow name) without re-parsing user SQL.
func ParseCreateTableNameAndBody(createSQL string) (name, body string, err error) {
	upper := strings.ToUpper(createSQL)
	idx := strings.Index(upper, "CREATE TABLE")
	if idx < 0 {
		return "", "", errors.New("not a CREATE TABLE statement")
	}
	cursor := idx + len("CREATE TABLE")
	for cursor < len(createSQL) && isASCIISpace(createSQL[cursor]) {
		cursor++
	}
	if cursor+13 <= len(upper) && upper[cursor:cursor+13] == "IF NOT EXISTS" {
		cursor += 13
		for cursor < len(createSQL) && isASCIISpace(createSQL[cursor]) {
			cursor++
		}
	}
	if cursor >= len(createSQL) {
		return "", "", errors.New("CREATE TABLE missing name")
	}
	nameStart := cursor
	var nameEnd int
	switch createSQL[cursor] {
	case '"':
		end := strings.IndexByte(createSQL[cursor+1:], '"')
		if end < 0 {
			return "", "", errors.New("unterminated double-quoted identifier")
		}
		nameEnd = cursor + 1 + end + 1
	case '`':
		end := strings.IndexByte(createSQL[cursor+1:], '`')
		if end < 0 {
			return "", "", errors.New("unterminated backtick-quoted identifier")
		}
		nameEnd = cursor + 1 + end + 1
	case '[':
		end := strings.IndexByte(createSQL[cursor+1:], ']')
		if end < 0 {
			return "", "", errors.New("unterminated bracketed identifier")
		}
		nameEnd = cursor + 1 + end + 1
	default:
		nameEnd = cursor
		for nameEnd < len(createSQL) && !isASCIISpace(createSQL[nameEnd]) && createSQL[nameEnd] != '(' {
			nameEnd++
		}
	}
	if nameStart == nameEnd {
		return "", "", errors.New("could not locate table identifier")
	}
	name = strings.Trim(createSQL[nameStart:nameEnd], "\"`[]")
	bodyStart := nameEnd
	for bodyStart < len(createSQL) && isASCIISpace(createSQL[bodyStart]) {
		bodyStart++
	}
	body = strings.TrimSpace(createSQL[bodyStart:])
	if body == "" {
		return "", "", errors.New("CREATE TABLE missing body")
	}
	return name, body, nil
}

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// CaptureSQL holds the read SQL the service layer issues to gather
// pre-image data before a destructive DDL runs. Domain layer only owns
// the SQL strings; execution lives in service.
//
// The %s placeholders accept already-quoted identifiers (use
// QuoteIdentifier). The ? placeholders take the table name as a string
// arg via the executor's parameter binding.
const (
	// CaptureTableCreateSQLQuery returns the original CREATE TABLE
	// statement text from sqlite_master for the given table.
	CaptureTableCreateSQLQuery = "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?"

	// CaptureDependentObjectsQuery returns all CREATE INDEX / TRIGGER /
	// VIEW statements that reference the given table, excluding the
	// internal auto-indexes SQLite creates for PRIMARY KEY / UNIQUE
	// constraints (those auto-regenerate when the table is recreated)
	// and the platform's _change_log_<table>_* triggers (the
	// post-DDL hook regenerates them).
	CaptureDependentObjectsQuery = `SELECT type, name, sql FROM sqlite_master
		WHERE tbl_name = ?
		AND name NOT LIKE 'sqlite_autoindex_%'
		AND name NOT LIKE '\_change\_log\_%' ESCAPE '\'
		AND type IN ('index', 'trigger', 'view')
		AND sql IS NOT NULL`

	// CaptureSkillTargetsQuery returns _skill_targets rows pointing at
	// the given table name.
	CaptureSkillTargetsQuery = "SELECT skill_id, table_name FROM _skill_targets WHERE table_name = ?"

	// CaptureRowCountSQL counts all rows in the given table. The %s
	// placeholder takes a pre-quoted table identifier.
	CaptureRowCountSQL = "SELECT COUNT(*) FROM %s"

	// CaptureColumnRowsSQL reads (rowid, value) pairs for a single column,
	// capped by LIMIT. Both placeholders take pre-quoted identifiers.
	CaptureColumnRowsSQL = "SELECT rowid, %s FROM %s LIMIT ?"

	// CaptureTableRowsSQL reads full rows from a table, capped by LIMIT.
	// The %s placeholder takes the pre-quoted table identifier.
	CaptureTableRowsSQL = "SELECT * FROM %s LIMIT ?"
)

// FormatCaptureRowCountSQL returns the COUNT(*) statement bound to a
// pre-quoted table identifier.
func FormatCaptureRowCountSQL(table string) string {
	return fmt.Sprintf(CaptureRowCountSQL, QuoteIdentifier(table))
}

// FormatCaptureColumnRowsSQL returns the column-extraction statement
// bound to pre-quoted column and table identifiers.
func FormatCaptureColumnRowsSQL(table, column string) string {
	return fmt.Sprintf(CaptureColumnRowsSQL, QuoteIdentifier(column), QuoteIdentifier(table))
}

// FormatCaptureTableRowsSQL returns the SELECT * statement bound to a
// pre-quoted table identifier.
func FormatCaptureTableRowsSQL(table string) string {
	return fmt.Sprintf(CaptureTableRowsSQL, QuoteIdentifier(table))
}

// PragmaTableInfoSQL returns the PRAGMA used to read column metadata for
// a table. Returns one row per column with (cid, name, type, notnull,
// dflt_value, pk).
func PragmaTableInfoSQL(table string) string {
	return fmt.Sprintf("PRAGMA table_info(%s)", QuoteIdentifier(table))
}

// BuildDropColumnSnapshot assembles a ChangeSnapshot for an ALTER TABLE
// DROP COLUMN. The caller (service layer) reads pragma_table_info,
// sqlite_master, and the column's (rowid, value) pairs, then hands them
// here. rowCountTotal is the true source row count (from COUNT(*));
// rows is the captured slice (≤ cap). If len(rows) < rowCountTotal the
// snapshot is marked Truncated.
//
// captureTableBody is the BODY of the original CREATE TABLE — everything
// after `CREATE TABLE <name>` (column list, constraints, options). Parsed
// off the table name at capture time so no SQL parser is needed at undo.
// preDropColumns is the pre-DROP column-name list in declaration order
// (including the dropped column at its original position). Used by the
// rebuild's populate SQL to place captured.value at the right column slot.
func BuildDropColumnSnapshot(
	tableName string,
	colMeta ColumnSnapshot,
	captureTableBody string,
	preDropColumns []string,
	rows []SnapshotRow,
	rowCountTotal int,
) ChangeSnapshot {
	snap := ChangeSnapshot{
		ColumnMetadata: &colMeta,
		RowPayload:     rows,
		RowCountTotal:  rowCountTotal,
		Truncated:      len(rows) < rowCountTotal,
	}
	if captureTableBody != "" {
		snap.DependentObjects = []DependentObject{
			{
				Kind:    DependentKindTable,
				Name:    tableName,
				SQL:     captureTableBody,
				Columns: preDropColumns,
			},
		}
	}
	return snap
}

// BuildDropTableSnapshot assembles a ChangeSnapshot for a DROP TABLE.
// dependents must include exactly one DependentKindTable entry (the
// table's CREATE statement) plus zero-or-more INDEX / VIEW / TRIGGER /
// SKILL_TARGET entries.
func BuildDropTableSnapshot(
	dependents []DependentObject,
	rows []SnapshotRow,
	rowCountTotal int,
) ChangeSnapshot {
	return ChangeSnapshot{
		DependentObjects: dependents,
		RowPayload:       rows,
		RowCountTotal:    rowCountTotal,
		Truncated:        len(rows) < rowCountTotal,
	}
}

// BuildDropSchemaObjectSnapshot assembles a ChangeSnapshot for a
// DROP INDEX / DROP VIEW / DROP TRIGGER. The CREATE statement text is the
// only thing needed; no row payload, no other dependents.
func BuildDropSchemaObjectSnapshot(kind DependentObjectKind, name, createSQL string) ChangeSnapshot {
	return ChangeSnapshot{
		DependentObjects: []DependentObject{
			{Kind: kind, Name: name, SQL: createSQL},
		},
	}
}
