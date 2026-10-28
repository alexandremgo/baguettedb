package domain

import (
	"fmt"
	"strings"
)

// StatementKindError reports SQL refused because a statement in it is not of the kind its request runs, or because a
// read request holds more than one statement. The message names no surface: each surface adds its own advice (the
// endpoint, tool or command that runs the statement).
type StatementKindError struct {
	// Intent is the kind of statement the request runs.
	Intent OperationIntent

	// Statement is the 1-based position of the refused statement in a batch, 0 for a lone statement.
	Statement int

	// Kind is the refused statement's kind. It is unset when Count is the reason.
	Kind StatementType

	// Count is the number of statements a read request held, when there was more than one; 0 otherwise.
	Count int
}

// Error describes the mismatch, e.g. "statement 2 is a schema change; the request runs row changes only".
func (e *StatementKindError) Error() string {
	if e.Count > 1 {
		return fmt.Sprintf("a read takes a single statement; got %d", e.Count)
	}
	subject := "the statement"
	if e.Statement > 0 {
		subject = fmt.Sprintf("statement %d", e.Statement)
	}
	return fmt.Sprintf("%s is %s; the request runs %s only", subject, e.Kind.Description(), e.Intent.Description())
}

// Unwrap returns ErrStatementNotAllowedOnEndpoint, so errors.Is matches the sentinel.
func (e *StatementKindError) Unwrap() error {
	return ErrStatementNotAllowedOnEndpoint
}

// InvalidSQLError reports SQL that SQLite cannot prepare, with SQLite's own message.
type InvalidSQLError struct {
	// Statement is the 1-based position of the failing statement, 0 when it is the first.
	Statement int

	// Message is SQLite's message, e.g. "no such column: nope".
	Message string
}

// Error returns SQLite's message, prefixed with the statement's position past the first.
func (e *InvalidSQLError) Error() string {
	if e.Statement > 0 {
		return fmt.Sprintf("statement %d: %s", e.Statement, e.Message)
	}
	return e.Message
}

// Unwrap returns ErrInvalidSQL, so errors.Is matches the sentinel.
func (e *InvalidSQLError) Unwrap() error {
	return ErrInvalidSQL
}

// MissingParametersError reports SQL with parameter placeholders run without values for them.
type MissingParametersError struct {
	// Names lists the placeholders without their sigil ("x" for ":x"), in order and without repeats; a bare "?" is
	// listed as "?".
	Names []string
}

// Error names the placeholders, e.g. "the SQL has parameters (x, y) but no values for them".
func (e *MissingParametersError) Error() string {
	return fmt.Sprintf("the SQL has parameters (%s) but no values for them", strings.Join(e.Names, ", "))
}

// Unwrap returns ErrMissingParameters, so errors.Is matches the sentinel.
func (e *MissingParametersError) Unwrap() error {
	return ErrMissingParameters
}

// MissingParameters returns a *MissingParametersError naming the placeholders of stmts, or nil when they have none.
func MissingParameters(stmts []ClassifiedStatement) error {
	var names []string
	seen := map[string]bool{}
	for _, s := range stmts {
		for _, name := range s.ParamNames {
			if name == "" {
				name = "?"
			}
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	return &MissingParametersError{Names: names}
}
