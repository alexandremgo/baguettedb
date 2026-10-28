package domain

import (
	"fmt"
	"strings"
)

// Intent is the kind of statement a SQL command runs. The instance refuses a statement of another kind, so a read
// can never change data and a schema change is always asked for explicitly.
type Intent int

// Statement kinds, one per sql subcommand.
const (
	IntentQuery  Intent = iota // reads
	IntentData                 // row changes
	IntentSchema               // schema changes
)

// StatementKindError reports SQL refused because a statement in it is not of the kind the command runs, or because a
// read was sent more than one statement.
type StatementKindError struct {
	// Statement is the 1-based position of the refused statement in a batch, 0 for a lone statement.
	Statement int

	// Kind is the refused statement's kind. It is unset when Count is the reason.
	Kind Intent

	// Count is the number of statements a read was sent, when there was more than one; 0 otherwise.
	Count int
}

// Error describes the refused statement or the statement count.
func (e *StatementKindError) Error() string {
	if e.Count > 1 {
		return fmt.Sprintf("a read takes a single statement; got %d", e.Count)
	}
	if e.Statement > 0 {
		return fmt.Sprintf("statement %d is of another kind", e.Statement)
	}
	return "the statement is of another kind"
}

// ParametersError reports SQL with parameter placeholders, which the SQL commands take no values for.
type ParametersError struct {
	// Names lists the placeholders without their sigil ("x" for ":x"); "?" for a bare one.
	Names []string
}

// Error names the placeholders.
func (e *ParametersError) Error() string {
	return fmt.Sprintf("the SQL has parameters (%s)", strings.Join(e.Names, ", "))
}

// Column is a result column: its name and its declared type, empty for an expression.
type Column struct {
	Name         string
	DeclaredType string
}

// QueryResult is what running a statement returns. Rows hold the result of a read, or of a write with RETURNING:
// each value is nil, an int64, a float64, a string or a []byte. A write also reports the rows it changed:
// AffectedRows lists them with their values before and after, up to a cap (AffectedRowsTruncated marks a longer
// list), and BatchID groups them in the change log.
type QueryResult struct {
	Columns               []Column
	Rows                  [][]any
	AffectedRowCount      int64
	LastInsertRowID       *int64
	BatchID               string
	AffectedRows          []AffectedRow
	AffectedRowsTruncated bool
}

// AffectedRow is a row a write changed, with its values before and after (OldValues empty for an insert, NewValues
// for a delete).
type AffectedRow struct {
	TableName string
	RowID     string
	Operation string
	OldValues map[string]any
	NewValues map[string]any
}
