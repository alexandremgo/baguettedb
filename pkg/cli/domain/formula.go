package domain

import (
	"errors"
	"fmt"
)

// Formula kinds.
const (
	FormulaKindView   = "view"
	FormulaKindAction = "action"
)

// FormulaRun is the outcome of running a formula. A view's Result holds its rows, and when the view paginates,
// HasMore reports more rows and NextCursor is the cursor parameter that fetches them. An action's Result holds the
// rows it changed.
type FormulaRun struct {
	Kind       string
	Result     QueryResult
	HasMore    bool
	NextCursor *int64
}

// Formula parameter sources: where the value of a parameter comes from when the app runs the formula. Every one of
// them is passed the same way from the command line.
const (
	ParamSourcePrompt          = "prompt"
	ParamSourceLookup          = "lookup"
	ParamSourceSelectionSingle = "selection-single"
	ParamSourceSelectionBulk   = "selection-bulk"
	ParamSourceReorder         = "reorder"
	ParamSourcePagination      = "pagination"
	ParamSourceWindow          = "window"
)

// FormulaDetail is a formula with what running it takes: its parameters, and the SQL it runs.
type FormulaDetail struct {
	Name        string
	Label       string
	Description string
	Kind        string

	// Verb is the formula's most consequential statement verb: select, insert, update or delete.
	Verb   string
	Hidden bool
	SQL    string
	Params []FormulaParam
}

// FormulaParam is one parameter a formula declares.
type FormulaParam struct {
	Name string

	// Type is the value's type (text, integer, float, boolean, date, datetime, time), or group for a list of rows
	// each made of Fields.
	Type   string
	Source string

	// Required reports that a run must give a value, while When holds when it is set.
	Required bool

	// Default is the value an omitted parameter takes, empty for none; Defaults is a multi-select's default set.
	Default  string
	Defaults []string

	// Options is the closed set of values the parameter accepts, empty when any value of its type goes.
	Options []FormulaOption

	// Multiple reports that the value is a list, given as a JSON array.
	Multiple bool

	// Lookup is where a lookup parameter's values come from, nil for any other source.
	Lookup *FormulaLookup

	// When is the condition on another parameter under which the parameter applies, nil when it always does.
	When *FormulaParamWhen

	Fields []FormulaParam
}

// FormulaOption is one value a parameter with options accepts, and the label the app shows for it.
type FormulaOption struct {
	Value string
	Label string
}

// FormulaLookup is the table column a lookup parameter's values come from, and the column the app shows for each.
// AllowNew reports that a value the column doesn't hold yet is accepted too.
type FormulaLookup struct {
	Table       string
	ValueColumn string
	LabelColumn string
	AllowNew    bool
}

// FormulaParamWhen is a condition on the value of the parameter Param: Op (eq, ne, lt, lte, gt or gte) compares it
// with Value.
type FormulaParamWhen struct {
	Param string
	Op    string
	Value string
}

// ParamValueError reports a value given for one of a formula's parameters, or the lack of one, that the parameter
// refuses.
type ParamValueError struct {
	Param string

	// Problem says what is wrong, as a predicate of the parameter, e.g. `expects an integer, got "four"`.
	Problem string
}

// Error names the parameter, then the problem.
func (e *ParamValueError) Error() string {
	return fmt.Sprintf("parameter %q %s", e.Param, e.Problem)
}

// ErrFormulaNotFound reports a formula reference that matches no formula in the bdb.
var ErrFormulaNotFound = errors.New("no formula")

// Export is a bdb's database written out: its size, and the one-time passphrase that opens it.
type Export struct {
	SizeBytes  int64
	Passphrase string
}
