package domain

import "fmt"

// Column represents a column in a query result set.
type Column struct {
	Name         string
	DeclaredType string
}

// SQLType represents a SQLite storage class.
type SQLType int

// The SQLite storage classes.
const (
	SQLNull    SQLType = iota // NULL
	SQLInteger                // INTEGER (int64)
	SQLReal                   // REAL (float64)
	SQLText                   // TEXT (string)
	SQLBlob                   // BLOB ([]byte)
)

// TypedValue represents a SQL value with its SQLite storage class.
type TypedValue struct {
	Type  SQLType
	Value any // nil, int64, float64, string, []byte
}

// String returns the string representation of the value.
func (v TypedValue) String() string {
	if v.Value == nil {
		return ""
	}
	return fmt.Sprintf("%v", v.Value)
}

// TextValue creates a TypedValue with SQLText type.
func TextValue(s string) TypedValue {
	return TypedValue{Type: SQLText, Value: s}
}

// IntegerValue creates a TypedValue with SQLInteger type.
func IntegerValue(n int64) TypedValue {
	return TypedValue{Type: SQLInteger, Value: n}
}

// RealValue creates a TypedValue with SQLReal type.
func RealValue(f float64) TypedValue {
	return TypedValue{Type: SQLReal, Value: f}
}

// NullValue creates a TypedValue with SQLNull type.
func NullValue() TypedValue {
	return TypedValue{Type: SQLNull, Value: nil}
}

// QueryResult represents the result of executing a SQL statement.
type QueryResult struct {
	Columns               []Column
	Rows                  [][]TypedValue
	AffectedRowCount      int64
	LastInsertRowID       *int64
	BatchID               string        // Set for DML mutations; groups mutation log entries.
	AffectedRows          []AffectedRow // Previous/new values of rows affected by the mutation.
	AffectedRowsTruncated bool          // True if more affected rows exist than AffectedRowsMaxInResponse.
}

// AffectedRow describes one row changed by a DML mutation, with its before/after values.
type AffectedRow struct {
	TableName string
	RowID     string
	Operation string
	OldValues map[string]any
	NewValues map[string]any
}

// TableColumn describes a single column in a table, parsed from PRAGMA table_info.
type TableColumn struct {
	Name       string
	Type       string
	NotNull    bool
	Default    string
	PrimaryKey bool
}

// ForeignKey describes a foreign key constraint, parsed from PRAGMA foreign_key_list.
type ForeignKey struct {
	From     string
	Table    string
	To       string
	OnUpdate string
	OnDelete string
}

// TableDescription holds the full schema of a single table: columns and foreign keys.
//
// Kind is "table", "view", "virtual", or "shadow" (a virtual table's internal
// storage table, as classified by SQLite's `pragma_table_list`). Module is the
// SQLite virtual-table module name (e.g. "fts5") when Kind == "virtual", and
// empty otherwise. Together they let agents know that, for instance, a
// `notes_fts` row in describe_table is an FTS5 index — not a regular table —
// so the right query patterns are `MATCH` rather than `LIKE`.
type TableDescription struct {
	Columns     []TableColumn
	ForeignKeys []ForeignKey
	Kind        string
	Module      string
	// Checks holds the table's CHECK constraint expressions in declaration order (e.g.
	// "json_valid(data)"), extracted from the stored CREATE statement since PRAGMA table_info
	// doesn't carry them. Empty for views and virtual tables.
	Checks []string
}

// TableInfo is the lightweight table listing entry used when callers need to
// list tables without paying for column-level schema reads.
// Module is the SQLite virtual-table module name (e.g. "fts5") when Type == "virtual", and empty otherwise.
type TableInfo struct {
	Name   string
	Type   string
	Module string
}

// TableSchema pairs a table's name and type with its full description. Module is the SQLite virtual-table
// module name (e.g. "fts5") when Type == "virtual", and empty otherwise.
//
// DescribeError is set when the table's schema introspection failed; Schema is then empty. It marks the
// entry as degraded rather than schemaless, so consumers don't mistake an unreadable table for one with no
// columns.
type TableSchema struct {
	Name          string
	Type          string
	Module        string
	Schema        TableDescription
	DescribeError string
}
