package domain

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// FormulasTable is the platform-managed table backing the formulas system.
const FormulasTable = "_formulas"

// FormulaNameMaxLen caps the human-readable formula name. Names are slug-shaped
// and used to address formulas by reference in MCP and REST.
const FormulaNameMaxLen = 64

// FormulaDescriptionMaxLen caps the formula description shown alongside the name
// in listings and discovery payloads.
const FormulaDescriptionMaxLen = 2000

// FormulaLabelMaxLen caps the human label, counted in runes rather than bytes so an accented label is
// not penalised. It is short on purpose: the label renders as a heading, a tab entry and a menu item.
const FormulaLabelMaxLen = 60

// FormulaSQLMaxBytes caps the stored SQL body of a formula.
const FormulaSQLMaxBytes = 100 * 1024

// ParamNameMaxLen caps a parameter name. Parameter names map to SQL bind
// placeholders, so they follow identifier rules.
const ParamNameMaxLen = 64

// formulaNamePattern matches lowercase slug names: must start with a letter,
// followed by lowercase letters, digits, or hyphens (mirrors skill naming).
var formulaNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// FormulaKind classifies a formula by the effect of its SQL statements.
type FormulaKind string

const (
	// FormulaKindView is a read formula (a single SELECT): it auto-runs and renders its rows.
	FormulaKindView FormulaKind = "view"

	// FormulaKindAction is a write formula (one or more INSERT/UPDATE/DELETE statements, run atomically):
	// it runs on explicit confirm.
	FormulaKindAction FormulaKind = "action"
)

var paramNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// lookupIdentifierPattern gates the table/column names a lookup config references: the schema-identifier
// admission charset and length cap, plus a leading `_` since reads on platform-reserved tables are allowed.
var lookupIdentifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,127}$`)

// ParamSource describes how the UI binds a parameter's value at run time. It is
// a presentation hint only — MCP callers always supply parameter values directly.
type ParamSource string

const (
	// ParamSourcePrompt is filled from a user-entered form field.
	ParamSourcePrompt ParamSource = "prompt"

	// ParamSourceSelectionSingle is bound from a single selected row's rowid.
	ParamSourceSelectionSingle ParamSource = "selection-single"

	// ParamSourceSelectionBulk is bound from the rowids of several selected rows.
	ParamSourceSelectionBulk ParamSource = "selection-bulk"

	// ParamSourcePagination is a keyset-pagination control (the reserved :cursor / :limit
	// names). Unlike the other sources it is optional at run time — the run engine supplies a
	// default when the caller omits it and clamps :limit to a ceiling.
	ParamSourcePagination ParamSource = "pagination"

	// ParamSourceLookup is picked from a dropdown of options read from another table: the
	// configured label column is shown, the value column supplies the bound value.
	ParamSourceLookup ParamSource = "lookup"

	// ParamSourceReorder is bound from the view's rows in their new display order after a drag —
	// the reserved :ordered_rowids name, carrying every listed row's rowid as one JSON array. It
	// binds as a single JSON TEXT value, consumed in SQL via json_each, whose key column is each
	// row's zero-based position in the new order.
	ParamSourceReorder ParamSource = "reorder"

	// ParamSourceWindow is bound by the app to the range a calendar display shows — the reserved
	// :window_start / :window_end pair, each a "YYYY-MM-DD" TEXT value (or "YYYY-MM-DD HH:MM:SS" when
	// the pair is typed datetime) — so the view's SQL filters to the visible days. Both bind SQL NULL
	// when a caller omits them (an assistant run gets the view unfiltered), so the SQL must treat NULL
	// as no bound: `(:window_start IS NULL OR day >= :window_start)`.
	ParamSourceWindow ParamSource = "window"
)

// Parameter value types, aligned with the storable scalar Value types a prompt can produce.
const (
	ParamTypeText    = "text"
	ParamTypeInteger = "integer"
	ParamTypeFloat   = "float"

	// ParamTypeBoolean binds as a SQLite 0/1 integer; callers supply JSON true/false.
	ParamTypeBoolean = "boolean"

	// ParamTypeDate binds a calendar day as a "YYYY-MM-DD" TEXT value — the form SQLite's date
	// functions compare and order correctly as plain strings. It is a distinct type rather than
	// text with a hint so the app can offer a picker, and so a value that is not a real date is
	// refused at the boundary instead of reaching a column that will happily store it.
	ParamTypeDate = "date"

	// ParamTypeDateTime binds a calendar day with a clock time as a "YYYY-MM-DD HH:MM:SS" TEXT value —
	// the form SQLite's datetime() writes. Input arrives in more than one shape (the browser's picker
	// emits "2026-08-09T14:30", SQLite writes "2026-08-09 14:30:00"), and a column mixing them compares
	// as plain text where "T" sorts after " ", so same-day values mis-order; canonicalizing at the
	// boundary is the point of the type.
	ParamTypeDateTime = "datetime"

	// ParamTypeTime binds a clock time as a "HH:MM:SS" TEXT value — the form SQLite's time() writes.
	// "HH:MM" input (the browser picker's default) gains ":00" seconds, since "14:30" and "14:30:00"
	// name the same instant but compare unequal as text.
	ParamTypeTime = "time"

	// ParamTypeGroup is a repeated group: its run-time value is an array of objects, one per row,
	// each validated against the parameter's Fields. The rows bind as one JSON TEXT value under the
	// parameter's name, consumed in SQL via json_each / json_extract.
	ParamTypeGroup = "group"
)

// Reserved parameter names that carry a row selection, each pinned to a required source.
const (
	// ReservedParamRowID binds one selected row's rowid; it must use ParamSourceSelectionSingle.
	ReservedParamRowID = "rowid"

	// ReservedParamParentRowID binds the row a row-detail modal is open for, supplied by the app to
	// the detail view's slotted actions; it must use ParamSourceSelectionSingle.
	ReservedParamParentRowID = "parent_rowid"

	// ReservedParamRowIDs binds several selected rows' rowids; it must use ParamSourceSelectionBulk.
	ReservedParamRowIDs = "rowids"

	// ReservedParamCursor is the keyset cursor (the last rowid seen); it must use ParamSourcePagination.
	ReservedParamCursor = "cursor"

	// ReservedParamLimit is the page size; it must use ParamSourcePagination.
	ReservedParamLimit = "limit"

	// ReservedParamWindowStart binds the first day (or instant) of a calendar display's visible range;
	// it must use ParamSourceWindow, type date or datetime, and be declared together with
	// ReservedParamWindowEnd of the same type.
	ReservedParamWindowStart = "window_start"

	// ReservedParamWindowEnd binds the last day (inclusive) or instant of a calendar display's
	// visible range; it must use ParamSourceWindow, type date or datetime, and be declared together
	// with ReservedParamWindowStart of the same type.
	ReservedParamWindowEnd = "window_end"

	// ReservedParamOrderedRowIDs binds the view's rowids in their new display order, as one JSON
	// array value; it must use ParamSourceReorder.
	ReservedParamOrderedRowIDs = "ordered_rowids"
)

// Keyset-pagination bounds for a formula view run.
const (
	// DefaultPageSize is the LIMIT applied when a paginated view's caller omits :limit.
	DefaultPageSize = 50

	// MaxPageSize caps :limit so a single app run can't pull an unbounded result set.
	MaxPageSize = 200
)

// LookupConfig names where a lookup parameter's options come from: LabelColumn is shown in the
// picker, ValueColumn supplies the bound value.
type LookupConfig struct {
	Table       string
	LabelColumn string
	ValueColumn string
	// ExcludeSelection drops the selected row from the picker's options — for lookups reading the
	// same table the acting row lives in (e.g. picking which duplicate a merge absorbs), where
	// offering the row itself would be a self-referencing choice. Display-only; it never affects
	// binding, and the formula must still guard against receiving its own selection.
	ExcludeSelection bool
	// AllowNew lets the picker also submit text typed past the option list — pick to reuse, keep
	// typing to name something new. The run binds whatever was typed (options are suggestions, not
	// a closed set), so the SQL should resolve the value and create the missing row itself, and the
	// parameter must be text: a typed value can be nothing else. Point ValueColumn at the text the
	// user would type (usually the label itself), not at an id.
	AllowNew bool
	// Scope narrows the picker's options by sibling parameters' current values: each key names a
	// sibling parameter, each value the column of Table compared against it — the options list only
	// rows where the column equals the sibling's current value, and a sibling left empty adds no
	// constraint. Display-only: a run still binds exactly what is sent, so the SQL must not rely on
	// the options having been narrowed.
	Scope map[string]string
	// Fill copies columns of the picked row into sibling parameters: each key names a sibling
	// parameter, each value the column of Table whose value the pick writes into it — picking
	// "yogurt" fills the category field with the catalog's filing. A pick overwrites what the
	// sibling holds; text typed past the options (AllowNew) fills nothing, and clearing the picker
	// leaves the siblings alone. Display-only like Scope: the run binds what the form sends, so the
	// SQL must cope with the siblings arriving empty (an MCP caller never fills anything).
	Fill map[string]string
}

// Comparison operators a ParamWhen condition may use. The ordered four apply to controllers whose
// type orders meaningfully (integer, float, date, datetime, time); eq and ne apply to any.
const (
	// ParamWhenOpEq shows the field while the controller equals the value.
	ParamWhenOpEq = "eq"
	// ParamWhenOpNe shows the field while the controller differs from the value.
	ParamWhenOpNe = "ne"
	// ParamWhenOpLt shows the field while the controller is strictly less than the value.
	ParamWhenOpLt = "lt"
	// ParamWhenOpLte shows the field while the controller is less than or equal to the value.
	ParamWhenOpLte = "lte"
	// ParamWhenOpGt shows the field while the controller is strictly greater than the value.
	ParamWhenOpGt = "gt"
	// ParamWhenOpGte shows the field while the controller is greater than or equal to the value.
	ParamWhenOpGte = "gte"
)

// ParamWhen is a visibility condition on a parameter: the app's forms render its field only while
// the named sibling parameter's current value compares true against the given value, and a hidden
// field submits nothing. The run mirrors the form's two faces: while the condition is false the
// parameter is not required and, when omitted, binds its default else SQL NULL ('[]' for a group).
// A supplied value still binds as usual — visibility relaxes requiredness, it never discards data.
type ParamWhen struct {
	// Param names the controlling sibling parameter, declared in the same schema.
	Param string
	// Op is the comparison ("eq", "ne", "lt", "lte", "gt", "gte"); empty means "eq".
	Op string
	// Value is the compared-against value, as its string form ("run", "true", "100").
	Value string
}

// EffectiveOp returns the condition's comparison, folding the empty default to "eq".
func (w ParamWhen) EffectiveOp() string {
	if w.Op == "" {
		return ParamWhenOpEq
	}
	return w.Op
}

// FormulaOption is one entry in a parameter's closed value set. Value is what a run binds, stored as
// the param type's string form (the Default convention: "film", "9", "2026-01-01"); Label is what
// the app shows in its place ("September" shown, "9" bound). A bare-string authored option folds to
// Value = Label before it reaches the domain, so Label is never empty here.
type FormulaOption struct {
	Value string
	Label string
}

// OptionValues returns the options' bound values in declared order — the spellings a run may pass.
func OptionValues(options []FormulaOption) []string {
	values := make([]string, len(options))
	for i, o := range options {
		values[i] = o.Value
	}
	return values
}

// FormulaParam is one entry in a formula's ordered parameter schema. Lookup is set only for
// ParamSourceLookup parameters. An Optional parameter may be omitted by the caller — the run
// binds its Default when one is declared, else SQL NULL (an optional group binds '[]', so
// json_each yields zero rows); a required one must be supplied with a non-null value.
type FormulaParam struct {
	Name     string
	Type     string
	Source   ParamSource
	Optional bool
	Lookup   *LookupConfig
	// Hint is display-only placeholder text for the app's input (e.g. "matches title or tags"). It
	// never affects binding.
	Hint string
	// Multiline marks a prompt text parameter for the app's textarea input (note bodies,
	// descriptions). Display-only: the bound value is ordinary text either way.
	Multiline bool
	// Inline marks a parameter for the app's quick-add form: an add-slot action whose params carry
	// the flag renders them in an always-visible form above the view's rows instead of behind the
	// + Add dialog. Display-only — binding is unchanged, unmarked optional params are omitted from
	// the form (they bind NULL), and an unmarked required param sends the whole add back to the
	// dialog, since the form could never submit without it.
	Inline bool
	// Multiple marks a parameter as multi-select — a lookup, or a prompt parameter with options — so
	// the app renders a checkbox panel over its options, and the run value is an array of picked
	// values (each typed like a single pick) binding as ONE JSON TEXT value under the parameter's
	// name, consumed in SQL via json_each. Duplicates are rejected; array order is preserved.
	// Omitted, it binds like a scalar: its default when declared — for a multi-select either the
	// literal "all", resolved at bind time to every value the lookup's table currently holds (or
	// every option value), or a JSON array of values — else '[]' for an optional (or
	// condition-false) parameter, while a required one must carry at least one value.
	Multiple bool
	// Options is a closed set of accepted values for a prompt parameter of a typed-in scalar type
	// (text, integer, float, date) — a fixed vocabulary (e.g. film / tv / book, or months 1–12)
	// rather than a catalog a lookup would read from a table. Unlike Hint it is enforced: a value
	// outside the set is rejected before it reaches the SQL, and a text value that matches
	// case-insensitively binds the declared spelling. Empty means the value is unconstrained.
	Options []FormulaOption
	// Default is the value an omitted (or explicitly null) optional prompt parameter binds instead
	// of SQL NULL, stored as its string form ("film", "2026", "true") and coerced to the param's
	// type at run time. The app's forms prefill from it, so what the user sees is what an omitting
	// caller gets. Declaring one forfeits NULL for the parameter — a param whose NULL is meaningful
	// (a filter's "no constraint") should not carry a default. Empty means none.
	//
	// On a Multiple parameter, Default is either empty or the literal "all": the one dynamic default,
	// resolved on each run to every row the lookup table holds at that moment (or every option value),
	// so a member added later is included without editing the formula — a static list could not say
	// that. It is allowed even on a required parameter — an expense shared among "all" members reads
	// as omit-means-everyone — while an explicit empty array stays rejected there, so the bound set
	// can never be empty. A static subset goes in Defaults instead.
	Default string
	// Defaults is a Multiple parameter's declared subset — the values an omitted parameter binds, each
	// in its string form like an option value (`staple`, `3`) — exclusive with Default and empty for
	// every other parameter. Like "all" it is allowed on a required parameter: omission then means
	// exactly this set.
	Defaults []string
	// When gates the parameter on a sibling parameter's current value: the app's forms render its
	// field only while the condition holds, and the run demands a required parameter only then — a
	// gated parameter omitted while its condition is false binds like an omitted optional one. So a
	// required parameter may carry a condition: it must be filled exactly while its face is active.
	// Nil means the field always renders and the declared requiredness always applies.
	When *ParamWhen
	// Fields is the per-row schema of a ParamTypeGroup parameter: scalar prompt/lookup entries
	// only, one level deep. Empty for scalar parameters.
	Fields []FormulaParam
}

// ParamSchema is the ordered list of parameters a formula declares.
type ParamSchema []FormulaParam

// Formula is a named, parameterized SQL artifact stored per-bdb. A formula is either a view (a
// single SELECT, rendered) or an action (one or more DML statements, run atomically on confirm).
// An action may attach to a view via AttachedToFormulaID to render as a row/bulk
// action that binds the user's selection.
type Formula struct {
	ID   uuid.UUID
	Name string
	// Label is the human name the app renders in place of Name. Presentation only: it is never a
	// reference, so slots, attachments and by-name lookups keep using Name. Empty means the app falls
	// back to Name.
	Label       string
	Description string
	SQL         string
	Kind        FormulaKind
	// StatementVerb is the formula's effective verb (select / insert / update / delete), derived at
	// create/update time so consumers (e.g. a confirm-before-run flow) never re-parse the SQL. An
	// action's verb is the most consequential one recorded across its statements.
	StatementVerb StatementVerb
	Params        ParamSchema
	// Display is a view's optional presentation config; nil renders the default table.
	Display *FormulaDisplay
	// Hidden marks a formula as assistant-only: the app omits it from its formula listing by
	// default. Presentation only — running and authoring permissions are unchanged.
	Hidden              bool
	AttachedToFormulaID *uuid.UUID
	DisplayOrder        int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ValidateFormulaName checks that name is a non-empty slug matching formulaNamePattern
// and within FormulaNameMaxLen.
func ValidateFormulaName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrFormulaNameInvalid)
	}
	if len(name) > FormulaNameMaxLen {
		return fmt.Errorf("%w: name must be at most %d characters", ErrFormulaNameInvalid, FormulaNameMaxLen)
	}
	if !formulaNamePattern.MatchString(name) {
		return fmt.Errorf("%w: name must match %s", ErrFormulaNameInvalid, formulaNamePattern.String())
	}
	// Refs resolve UUID-shaped strings by id, so a UUID-shaped name could never be fetched by name.
	if _, err := uuid.Parse(name); err == nil {
		return fmt.Errorf("%w: name must not be UUID-shaped", ErrFormulaNameInvalid)
	}
	return nil
}

// ValidateFormulaDescription checks that the description is non-empty and within
// FormulaDescriptionMaxLen.
func ValidateFormulaDescription(description string) error {
	if description == "" {
		return fmt.Errorf("%w: description must not be empty", ErrFormulaDescriptionInvalid)
	}
	if len(description) > FormulaDescriptionMaxLen {
		return fmt.Errorf("%w: description must be at most %d characters",
			ErrFormulaDescriptionInvalid, FormulaDescriptionMaxLen)
	}
	return nil
}

// ValidateFormulaLabel checks the optional human label: an empty label is valid and means the app falls
// back to the name, a non-empty one is at most FormulaLabelMaxLen runes and carries no invisible
// characters. It renders inline in headings and menus, where a newline or a tab breaks the layout and a
// zero-width or bidi-override rune makes the label read as something other than what is stored.
func ValidateFormulaLabel(label string) error {
	if utf8.RuneCountInString(label) > FormulaLabelMaxLen {
		return fmt.Errorf("%w: label must be at most %d characters", ErrFormulaLabelInvalid, FormulaLabelMaxLen)
	}

	if i := strings.IndexFunc(label, isInvisibleRune); i >= 0 {
		r, _ := utf8.DecodeRuneInString(label[i:])
		return fmt.Errorf("%w: label must not contain invisible characters, found %U", ErrFormulaLabelInvalid, r)
	}

	return nil
}

// isInvisibleRune reports whether r renders as nothing: control characters (Cc, e.g. newline and tab)
// and format characters (Cf, e.g. the zero-width space and the bidirectional overrides, which reorder
// the text after them so a label reads as something other than what is stored). Space separators (Zs,
// e.g. the non-breaking space) are allowed — they carry width and ordinary typographic meaning.
func isInvisibleRune(r rune) bool {
	return unicode.In(r, unicode.Cc, unicode.Cf)
}

// ValidateFormulaSQL checks that the SQL body is non-empty and within FormulaSQLMaxBytes.
// The statement shape (view vs action) is enforced separately by DeriveFormulaKind.
func ValidateFormulaSQL(sql string) error {
	if sql == "" {
		return fmt.Errorf("%w: SQL must not be empty", ErrFormulaSQLInvalid)
	}
	if len(sql) > FormulaSQLMaxBytes {
		return fmt.Errorf("%w: SQL must be at most %d bytes (got %d)",
			ErrFormulaSQLInvalid, FormulaSQLMaxBytes, len(sql))
	}
	return nil
}

// DeriveStatementVerb reduces a formula's classified statements to a single stored verb: a lone
// read is a select; a write takes the most consequential verb recorded across all its statements —
// delete over insert over update — so an action able to delete rows anywhere reads as delete and
// an upsert reads as insert. Returns "" when no verb was recorded (not a plain DQL/DML request).
func DeriveStatementVerb(stmts []ClassifiedStatement) StatementVerb {
	if len(stmts) == 0 {
		return ""
	}
	if len(stmts) == 1 && stmts[0].Type == StatementDQL {
		return StatementVerbSelect
	}

	recorded := make(map[StatementVerb]bool)
	for _, stmt := range stmts {
		for _, v := range stmt.DMLVerbs {
			recorded[v] = true
		}
	}

	for _, v := range []StatementVerb{StatementVerbDelete, StatementVerbInsert, StatementVerbUpdate} {
		if recorded[v] {
			return v
		}
	}
	return ""
}

// DeriveFormulaKind reduces the classified statements of a formula's SQL to a single FormulaKind:
// a lone SELECT is a view; one or more INSERT/UPDATE/DELETE statements are an action. Anything
// else — empty input, more than one SELECT, a read/write mix, DDL, or an unsupported statement —
// yields ErrFormulaKindUndeterminable.
func DeriveFormulaKind(stmts []ClassifiedStatement) (FormulaKind, error) {
	if len(stmts) == 0 {
		return "", ErrFormulaKindUndeterminable
	}
	if len(stmts) == 1 && stmts[0].Type == StatementDQL {
		return FormulaKindView, nil
	}
	for _, stmt := range stmts {
		if stmt.Type != StatementDML {
			return "", ErrFormulaKindUndeterminable
		}
	}
	return FormulaKindAction, nil
}

// Validate checks the parameter schema: each parameter's name (identifier shape,
// length, uniqueness), type, and source must be valid, and the reserved selection
// names (rowid / parent_rowid / rowids) must use their required source.
func (p ParamSchema) Validate() error {
	seen := make(map[string]struct{}, len(p))
	byName := make(map[string]FormulaParam, len(p))
	var hasCursor, hasLimit, hasWindowStart, hasWindowEnd, hasSelectionSingle bool
	var windowTypes []string
	var excludeSelectionParam string

	for _, fp := range p {
		if len(fp.Name) > ParamNameMaxLen {
			return fmt.Errorf("%w: parameter name %q has invalid length", ErrFormulaParamInvalid, fp.Name)
		}

		if !paramNamePattern.MatchString(fp.Name) {
			return fmt.Errorf("%w: name must match %s", ErrFormulaParamInvalid, paramNamePattern.String())
		}

		if _, dup := seen[fp.Name]; dup {
			return fmt.Errorf("%w: duplicate param name %q", ErrFormulaParamInvalid, fp.Name)
		}

		switch fp.Type {
		case ParamTypeText, ParamTypeInteger, ParamTypeFloat, ParamTypeBoolean, ParamTypeDate, ParamTypeDateTime,
			ParamTypeTime, ParamTypeGroup:
		// Ok
		default:
			return fmt.Errorf("%w: param has invalid type %q", ErrFormulaParamInvalid, fp.Type)
		}

		if fp.Type == ParamTypeGroup {
			if err := fp.validateGroup(); err != nil {
				return err
			}
		} else if len(fp.Fields) > 0 {
			return fmt.Errorf("%w: parameter %q is not a group and cannot declare fields",
				ErrFormulaParamInvalid, fp.Name)
		}

		switch fp.Source {
		case ParamSourcePrompt, ParamSourceSelectionSingle, ParamSourceSelectionBulk,
			ParamSourcePagination, ParamSourceLookup, ParamSourceReorder, ParamSourceWindow:
		// Ok
		default:
			return fmt.Errorf("%w: param has invalid source %q", ErrFormulaParamInvalid, fp.Source)
		}

		if err := fp.validateLookup(); err != nil {
			return err
		}
		if err := fp.validateMultiline(); err != nil {
			return err
		}
		if err := fp.validateOptions(); err != nil {
			return err
		}
		if err := fp.validateInline(); err != nil {
			return err
		}
		if err := fp.validateMultiple(); err != nil {
			return err
		}
		if err := fp.validateDefault(); err != nil {
			return err
		}
		if err := fp.validateDefaults(); err != nil {
			return err
		}

		// Optionality only makes sense for values the caller may withhold: a selection is bound by
		// the app and pagination controls carry their own defaulting rules. A reorder binding may be
		// optional — a board's move action takes the destination order only when every row of the
		// column is visible, and binds SQL NULL otherwise (the renumbering SQL must no-op on it).
		if fp.Optional && fp.Source != ParamSourcePrompt && fp.Source != ParamSourceLookup &&
			fp.Source != ParamSourceReorder {
			return fmt.Errorf("%w: only a prompt, lookup, or reorder parameter can be optional (got source %q)",
				ErrFormulaParamInvalid, fp.Source)
		}

		switch fp.Name {
		case ReservedParamRowID, ReservedParamParentRowID:
			if fp.Source != ParamSourceSelectionSingle {
				return fmt.Errorf("%w: %q must use source %q",
					ErrFormulaParamInvalid, fp.Name, ParamSourceSelectionSingle)
			}
		case ReservedParamRowIDs:
			if fp.Source != ParamSourceSelectionBulk {
				return fmt.Errorf("%w: %q must use source %q",
					ErrFormulaParamInvalid, ReservedParamRowIDs, ParamSourceSelectionBulk)
			}
		case ReservedParamCursor, ReservedParamLimit:
			if fp.Source != ParamSourcePagination {
				return fmt.Errorf("%w: %q must use source %q",
					ErrFormulaParamInvalid, fp.Name, ParamSourcePagination)
			}
			if fp.Type != ParamTypeInteger {
				return fmt.Errorf("%w: %q must be of type %q", ErrFormulaParamInvalid, fp.Name, ParamTypeInteger)
			}
			hasCursor = hasCursor || fp.Name == ReservedParamCursor
			hasLimit = hasLimit || fp.Name == ReservedParamLimit
		case ReservedParamOrderedRowIDs:
			if fp.Source != ParamSourceReorder {
				return fmt.Errorf("%w: %q must use source %q",
					ErrFormulaParamInvalid, ReservedParamOrderedRowIDs, ParamSourceReorder)
			}
			// Integer names the element type — the bound value is the JSON array of the rowids, the
			// same convention the bulk selection uses.
			if fp.Type != ParamTypeInteger {
				return fmt.Errorf("%w: %q must be of type %q", ErrFormulaParamInvalid, fp.Name, ParamTypeInteger)
			}
		}

		// The pagination source is reserved for the cursor / limit names — it carries run-engine
		// meaning (optional + clamped) that only makes sense for those two controls.
		if fp.Source == ParamSourcePagination && fp.Name != ReservedParamCursor && fp.Name != ReservedParamLimit {
			return fmt.Errorf("%w: source %q is only valid for %q or %q",
				ErrFormulaParamInvalid, ParamSourcePagination, ReservedParamCursor, ReservedParamLimit)
		}

		// The reorder source is likewise reserved for its one name: the run engine binds the row
		// order under that name, and a second reorder-sourced param could never be filled.
		if fp.Source == ParamSourceReorder && fp.Name != ReservedParamOrderedRowIDs {
			return fmt.Errorf("%w: source %q is only valid for %q",
				ErrFormulaParamInvalid, ParamSourceReorder, ReservedParamOrderedRowIDs)
		}

		// The window source is reserved for the window_start / window_end pair, and those names for it:
		// the app binds a range under exactly those two names, and nothing else could fill a third.
		if fp.Source == ParamSourceWindow && fp.Name != ReservedParamWindowStart && fp.Name != ReservedParamWindowEnd {
			return fmt.Errorf("%w: source %q is only valid for %q or %q",
				ErrFormulaParamInvalid, ParamSourceWindow, ReservedParamWindowStart, ReservedParamWindowEnd)
		}
		if fp.Name == ReservedParamWindowStart || fp.Name == ReservedParamWindowEnd {
			if fp.Source != ParamSourceWindow {
				return fmt.Errorf("%w: %q must use source %q", ErrFormulaParamInvalid, fp.Name, ParamSourceWindow)
			}
			if fp.Type != ParamTypeDate && fp.Type != ParamTypeDateTime {
				return fmt.Errorf("%w: %q must be of type %q or %q — the app binds a calendar day or instant to it",
					ErrFormulaParamInvalid, fp.Name, ParamTypeDate, ParamTypeDateTime)
			}
			hasWindowStart = hasWindowStart || fp.Name == ReservedParamWindowStart
			hasWindowEnd = hasWindowEnd || fp.Name == ReservedParamWindowEnd
			windowTypes = append(windowTypes, fp.Type)
		}

		seen[fp.Name] = struct{}{}
		byName[fp.Name] = fp

		hasSelectionSingle = hasSelectionSingle || fp.Source == ParamSourceSelectionSingle
		if fp.Lookup != nil && fp.Lookup.ExcludeSelection {
			excludeSelectionParam = fp.Name
		}
		for _, field := range fp.Fields {
			if field.Lookup != nil && field.Lookup.ExcludeSelection {
				excludeSelectionParam = field.Name
			}
		}
	}

	// Keyset pagination needs both controls: a cursor with no page size (or the reverse) leaves the
	// run engine unable to over-fetch and detect a next page.
	if hasCursor != hasLimit {
		return fmt.Errorf("%w: %q and %q must be declared together",
			ErrFormulaParamInvalid, ReservedParamCursor, ReservedParamLimit)
	}
	// A window is a range: one bound alone would let the app show a week the SQL cannot narrow to, and
	// bounds of two types (a day and an instant) would not compare against the same column.
	if hasWindowStart != hasWindowEnd {
		return fmt.Errorf("%w: %q and %q must be declared together",
			ErrFormulaParamInvalid, ReservedParamWindowStart, ReservedParamWindowEnd)
	}
	if len(windowTypes) == 2 && windowTypes[0] != windowTypes[1] {
		return fmt.Errorf("%w: %q and %q must share one type — both %q or both %q",
			ErrFormulaParamInvalid, ReservedParamWindowStart, ReservedParamWindowEnd, ParamTypeDate, ParamTypeDateTime)
	}

	// exclude_selection filters the picker against the acting row — without a selection-single
	// param there is no such row, and the flag could only ever be a silent no-op.
	if excludeSelectionParam != "" && !hasSelectionSingle {
		return fmt.Errorf("%w: parameter %q sets exclude_selection but the formula has no selection-single param",
			ErrFormulaParamInvalid, excludeSelectionParam)
	}

	// Second pass: when-conditions and lookup scopes reference sibling parameters, so every
	// declaration must be collected before either can be checked.
	for _, fp := range p {
		if err := fp.validateWhen(byName); err != nil {
			return err
		}
		if err := fp.validateScope(byName); err != nil {
			return err
		}
		if err := fp.validateFill(byName); err != nil {
			return err
		}
	}
	return nil
}

// validateWhen checks a visibility condition: it sits on a scalar prompt/lookup or group parameter
// (required or optional — requiredness applies only while the condition holds), references a
// distinct prompt-sourced sibling that carries no condition of its own (chained gating would make
// visibility ambiguous to reason about), uses a known comparison — the ordered four only against a
// controller whose type orders meaningfully — and compares against a non-empty value that fits the
// controller: parseable as its type for ordered comparisons, one of its options when it declares
// any, "true"/"false" for a boolean.
func (fp FormulaParam) validateWhen(byName map[string]FormulaParam) error {
	if fp.When == nil {
		return nil
	}
	if fp.Source != ParamSourcePrompt && fp.Source != ParamSourceLookup {
		return fmt.Errorf("%w: only a prompt or lookup parameter can declare when (param %q is %s %s)",
			ErrFormulaParamInvalid, fp.Name, fp.Source, fp.Type)
	}
	if fp.When.Param == fp.Name {
		return fmt.Errorf("%w: parameter %q cannot gate on itself", ErrFormulaParamInvalid, fp.Name)
	}
	controller, ok := byName[fp.When.Param]
	if !ok {
		return fmt.Errorf("%w: parameter %q gates on undeclared parameter %q",
			ErrFormulaParamInvalid, fp.Name, fp.When.Param)
	}
	if controller.Source != ParamSourcePrompt {
		return fmt.Errorf("%w: parameter %q gates on %q, which is not prompt-sourced — "+
			"only a typed or picked field has a value to gate on", ErrFormulaParamInvalid, fp.Name, fp.When.Param)
	}
	// A gated field's controller is always visible: a chain would gate a field on a value the user
	// may no longer see (a hidden controller keeps its stale form value), and would make cycles
	// authorable. Lifted if a real cascading-form case shows up — visibility then evaluates
	// transitively (a hidden controller counts as empty) and this becomes a cycle check instead.
	if controller.When != nil {
		return fmt.Errorf("%w: parameter %q gates on %q, which carries a condition of its own",
			ErrFormulaParamInvalid, fp.Name, fp.When.Param)
	}
	if fp.When.Value == "" {
		return fmt.Errorf("%w: parameter %q declares when without a value", ErrFormulaParamInvalid, fp.Name)
	}

	switch fp.When.EffectiveOp() {
	case ParamWhenOpEq, ParamWhenOpNe:
		if len(controller.Options) > 0 && !slices.Contains(OptionValues(controller.Options), fp.When.Value) {
			return fmt.Errorf("%w: parameter %q gates on %q = %q, which is not one of its options",
				ErrFormulaParamInvalid, fp.Name, fp.When.Param, fp.When.Value)
		}
		if controller.Type == ParamTypeBoolean && fp.When.Value != "true" && fp.When.Value != "false" {
			return fmt.Errorf("%w: parameter %q gates on boolean %q with %q, want \"true\" or \"false\"",
				ErrFormulaParamInvalid, fp.Name, fp.When.Param, fp.When.Value)
		}
	case ParamWhenOpLt, ParamWhenOpLte, ParamWhenOpGt, ParamWhenOpGte:
		if err := validateWhenOrdered(controller, fp.Name, fp.When); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: parameter %q declares unknown when op %q",
			ErrFormulaParamInvalid, fp.Name, fp.When.Op)
	}
	return nil
}

// validateWhenOrdered checks an ordered comparison's controller and value: the controller's type
// must order meaningfully, and the value must parse as that type so the form's comparison is
// well-defined.
func validateWhenOrdered(controller FormulaParam, gated string, w *ParamWhen) error {
	var ok bool
	switch controller.Type {
	case ParamTypeInteger:
		_, err := strconv.ParseInt(w.Value, 10, 64)
		ok = err == nil
	case ParamTypeFloat:
		_, err := strconv.ParseFloat(w.Value, 64)
		ok = err == nil
	case ParamTypeDate:
		_, ok = CanonicalDate(w.Value)
	case ParamTypeDateTime:
		_, ok = CanonicalDateTime(w.Value)
	case ParamTypeTime:
		_, ok = CanonicalTime(w.Value)
	default:
		return fmt.Errorf("%w: parameter %q gates on %q with %q, but type %q does not order — "+
			"ordered comparisons need a numeric, date, or time controller",
			ErrFormulaParamInvalid, gated, w.Param, w.Op, controller.Type)
	}
	if !ok {
		return fmt.Errorf("%w: parameter %q gates on %q %s %q, which does not parse as its %q type",
			ErrFormulaParamInvalid, gated, w.Param, w.Op, w.Value, controller.Type)
	}
	return nil
}

// validateGroup checks a repeated-group parameter: dialog-supplied (source prompt), at least one
// field, and every field a uniquely-named scalar prompt/lookup entry — no nested groups, and no
// selection or pagination bindings inside a row.
func (fp FormulaParam) validateGroup() error {
	if fp.Source != ParamSourcePrompt {
		return fmt.Errorf("%w: group parameter %q must use source %q",
			ErrFormulaParamInvalid, fp.Name, ParamSourcePrompt)
	}
	if len(fp.Fields) == 0 {
		return fmt.Errorf("%w: group parameter %q needs at least one field", ErrFormulaParamInvalid, fp.Name)
	}

	seen := make(map[string]struct{}, len(fp.Fields))
	for _, field := range fp.Fields {
		if len(field.Name) > ParamNameMaxLen || !paramNamePattern.MatchString(field.Name) {
			return fmt.Errorf("%w: group parameter %q has a field with invalid name %q",
				ErrFormulaParamInvalid, fp.Name, field.Name)
		}
		if _, dup := seen[field.Name]; dup {
			return fmt.Errorf("%w: group parameter %q has duplicate field %q",
				ErrFormulaParamInvalid, fp.Name, field.Name)
		}
		seen[field.Name] = struct{}{}

		switch field.Type {
		case ParamTypeText, ParamTypeInteger, ParamTypeFloat, ParamTypeBoolean, ParamTypeDate, ParamTypeDateTime,
			ParamTypeTime:
		// Ok — one level deep only.
		default:
			return fmt.Errorf("%w: group parameter %q field %q has invalid type %q",
				ErrFormulaParamInvalid, fp.Name, field.Name, field.Type)
		}
		if field.Source != ParamSourcePrompt && field.Source != ParamSourceLookup {
			return fmt.Errorf("%w: group parameter %q field %q must be prompt- or lookup-sourced",
				ErrFormulaParamInvalid, fp.Name, field.Name)
		}
		if err := field.validateLookup(); err != nil {
			return err
		}
		if err := field.validateMultiline(); err != nil {
			return err
		}
		if err := field.validateOptions(); err != nil {
			return err
		}
		// A group's fields render inside its own row editor, which only ever lives in the dialog —
		// there is no quick-add form for the flag to place them in.
		if field.Inline {
			return fmt.Errorf("%w: group parameter %q field %q cannot be inline",
				ErrFormulaParamInvalid, fp.Name, field.Name)
		}
		// A group row's cell holds one scalar; a value array nested inside a row object has no bind
		// shape — several members per row means several rows.
		if field.Multiple {
			return fmt.Errorf("%w: group parameter %q field %q cannot be multiple",
				ErrFormulaParamInvalid, fp.Name, field.Name)
		}
		// A group binds as a JSON array of caller-built rows; no per-field omission exists for a
		// default to fill.
		if field.Default != "" || len(field.Defaults) > 0 {
			return fmt.Errorf("%w: group parameter %q field %q cannot declare a default",
				ErrFormulaParamInvalid, fp.Name, field.Name)
		}
		// A group's row editor renders every field of every row; per-row conditional visibility has
		// no controller to read — conditions live on top-level parameters only.
		if field.When != nil {
			return fmt.Errorf("%w: group parameter %q field %q cannot declare when",
				ErrFormulaParamInvalid, fp.Name, field.Name)
		}
	}
	return nil
}

// optionParamTypes are the param types a closed value set may sit on: typed-in scalars whose values
// have an authorable string form. Booleans are excluded (two fixed values need no vocabulary), as
// are datetime/time (no closed-set use has shown up) and groups/lookups (their values come from rows).
var optionParamTypes = map[string]struct{}{
	ParamTypeText: {}, ParamTypeInteger: {}, ParamTypeFloat: {}, ParamTypeDate: {},
}

// validateOptions checks that a closed value set sits on a prompt parameter of an option-capable
// type, and that the set itself is usable: every value non-blank, un-padded (a value binds verbatim)
// and readable as the param's type; every label non-blank and un-padded; and no two values or two
// labels that differ only by case — those would make the case-insensitive match at bind time (and
// the app's dropdown) ambiguous.
func (fp FormulaParam) validateOptions() error {
	if len(fp.Options) == 0 {
		return nil
	}
	if _, ok := optionParamTypes[fp.Type]; !ok || fp.Source != ParamSourcePrompt {
		return fmt.Errorf("%w: only a prompt text, integer, float or date parameter can declare options "+
			"(param %q is %s %s)", ErrFormulaParamInvalid, fp.Name, fp.Source, fp.Type)
	}
	seenValues := make(map[string]struct{}, len(fp.Options))
	seenLabels := make(map[string]struct{}, len(fp.Options))
	for _, opt := range fp.Options {
		for entry, s := range map[string]string{"value": opt.Value, "label": opt.Label} {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("%w: parameter %q has an option with a blank %s",
					ErrFormulaParamInvalid, fp.Name, entry)
			}
			if strings.TrimSpace(s) != s {
				return fmt.Errorf("%w: parameter %q has an option whose %s carries surrounding whitespace (%q)",
					ErrFormulaParamInvalid, fp.Name, entry, s)
			}
		}
		if issue := typedTextIssue(fp.Type, opt.Value); issue != "" {
			return fmt.Errorf("%w: parameter %q option value %q %s",
				ErrFormulaParamInvalid, fp.Name, opt.Value, issue)
		}
		foldedValue, foldedLabel := strings.ToLower(opt.Value), strings.ToLower(opt.Label)
		if _, dup := seenValues[foldedValue]; dup {
			return fmt.Errorf("%w: parameter %q has options whose values differ only by case (%q)",
				ErrFormulaParamInvalid, fp.Name, opt.Value)
		}
		if _, dup := seenLabels[foldedLabel]; dup {
			return fmt.Errorf("%w: parameter %q has options whose labels differ only by case (%q)",
				ErrFormulaParamInvalid, fp.Name, opt.Label)
		}
		seenValues[foldedValue] = struct{}{}
		seenLabels[foldedLabel] = struct{}{}
	}
	return nil
}

// validateInline checks that the display-only inline flag sits on a scalar prompt or lookup
// parameter — the quick-add form renders typed-in and picked values, and a repeated group, an
// app-bound selection, or a pagination control has no input to show there.
func (fp FormulaParam) validateInline() error {
	if !fp.Inline {
		return nil
	}
	if fp.Type == ParamTypeGroup || (fp.Source != ParamSourcePrompt && fp.Source != ParamSourceLookup) {
		return fmt.Errorf("%w: only a scalar prompt or lookup parameter can be inline (param %q is %s %s)",
			ErrFormulaParamInvalid, fp.Name, fp.Source, fp.Type)
	}
	return nil
}

// validateMultiple checks the multi-select flag: it sits on a lookup-sourced parameter or on a
// prompt parameter with options (a closed checkbox panel needs a fixed set to enumerate — a table
// to read, or the declared vocabulary) without allow_new — the panel offers picking only, and a
// typed value has no place in a value array.
func (fp FormulaParam) validateMultiple() error {
	if !fp.Multiple {
		return nil
	}
	optionsPrompt := fp.Source == ParamSourcePrompt && len(fp.Options) > 0
	if fp.Source != ParamSourceLookup && !optionsPrompt {
		return fmt.Errorf("%w: only a lookup or an options parameter can be multiple (param %q is %s %s)",
			ErrFormulaParamInvalid, fp.Name, fp.Source, fp.Type)
	}
	if fp.Lookup != nil && fp.Lookup.AllowNew {
		return fmt.Errorf("%w: parameter %q cannot combine multiple with allow_new — "+
			"a checkbox panel picks existing values only", ErrFormulaParamInvalid, fp.Name)
	}
	return nil
}

// validateDefault checks a declared default: it sits on an optional scalar prompt parameter (a
// required param forbids the omission a default answers; a lookup's ids and an app-bound selection
// have no authorable value), parses as the param's type, and — when options constrain the param —
// is one of them, spelled exactly as declared. A multi-select parameter is the exception: its Default
// may only be the literal "all" (every option value, resolved at bind time), required or not.
func (fp FormulaParam) validateDefault() error {
	if fp.Default == "" {
		return nil
	}
	if fp.Multiple {
		if fp.Default != "all" {
			return fmt.Errorf("%w: parameter %q is multiple and its default may only be %q — a subset goes in "+
				"defaults (got %q)", ErrFormulaParamInvalid, fp.Name, "all", fp.Default)
		}
		return nil
	}
	if fp.Type == ParamTypeGroup || fp.Source != ParamSourcePrompt {
		return fmt.Errorf("%w: only a scalar prompt parameter can declare a default (param %q is %s %s)",
			ErrFormulaParamInvalid, fp.Name, fp.Source, fp.Type)
	}
	if !fp.Optional {
		return fmt.Errorf("%w: parameter %q is required and cannot declare a default — "+
			"a default gives an omitted value a meaning, required forbids omitting it", ErrFormulaParamInvalid, fp.Name)
	}
	if len(fp.Options) > 0 && !slices.Contains(OptionValues(fp.Options), fp.Default) {
		return fmt.Errorf("%w: parameter %q default %q is not one of its options",
			ErrFormulaParamInvalid, fp.Name, fp.Default)
	}
	if issue := typedTextIssue(fp.Type, fp.Default); issue != "" {
		return fmt.Errorf("%w: parameter %q default %q %s", ErrFormulaParamInvalid, fp.Name, fp.Default, issue)
	}
	return nil
}

// validateDefaults checks a declared subset default: it sits on a multi-select parameter only, never
// beside a Default, holds no duplicate, and each value passes the scalar checks — one of the options
// when options exist, readable as the parameter's type. An empty slice means none and always passes.
func (fp FormulaParam) validateDefaults() error {
	if len(fp.Defaults) == 0 {
		return nil
	}
	if !fp.Multiple {
		return fmt.Errorf("%w: parameter %q is not multiple and cannot declare defaults — a single value goes "+
			"in default", ErrFormulaParamInvalid, fp.Name)
	}
	if fp.Default != "" {
		return fmt.Errorf("%w: parameter %q declares both default %q and defaults — pick one",
			ErrFormulaParamInvalid, fp.Name, fp.Default)
	}
	for i, v := range fp.Defaults {
		if slices.Contains(fp.Defaults[:i], v) {
			return fmt.Errorf("%w: parameter %q defaults repeat %q", ErrFormulaParamInvalid, fp.Name, v)
		}
		if len(fp.Options) > 0 && !slices.Contains(OptionValues(fp.Options), v) {
			return fmt.Errorf("%w: parameter %q defaults element %q is not one of its options",
				ErrFormulaParamInvalid, fp.Name, v)
		}
		if issue := typedTextIssue(fp.Type, v); issue != "" {
			return fmt.Errorf("%w: parameter %q defaults element %q %s", ErrFormulaParamInvalid, fp.Name, v, issue)
		}
	}
	return nil
}

// DefaultsResponse is a multi-select parameter's declared subset in the shape a caller authors it:
// integer and float values as numbers, everything else as text; nil when none.
func DefaultsResponse(p FormulaParam) []any {
	if len(p.Defaults) == 0 {
		return nil
	}
	out := make([]any, 0, len(p.Defaults))
	for _, v := range p.Defaults {
		switch p.Type {
		case ParamTypeInteger, ParamTypeFloat:
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				out = append(out, f)
				continue
			}
		}
		out = append(out, v)
	}
	return out
}

// typedTextIssue says why s does not read as a value of the given scalar param type, or "" when it
// does. Text always reads; the phrasing slots after a "<subject> %q " prefix in the caller's error.
func typedTextIssue(typ, s string) string {
	switch typ {
	case ParamTypeInteger:
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return "is not an integer"
		}
	case ParamTypeFloat:
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return "is not a number"
		}
	case ParamTypeBoolean:
		if s != "true" && s != "false" {
			return "is not \"true\" or \"false\""
		}
	case ParamTypeDate:
		if _, ok := CanonicalDate(s); !ok {
			return "is not a YYYY-MM-DD date"
		}
	case ParamTypeDateTime:
		if _, ok := CanonicalDateTime(s); !ok {
			return "is not a YYYY-MM-DD HH:MM[:SS] date and time"
		}
	case ParamTypeTime:
		if _, ok := CanonicalTime(s); !ok {
			return "is not a HH:MM[:SS] time"
		}
	}
	return ""
}

// CanonicalDate parses s as a real calendar day written as YYYY-MM-DD and returns it in that same
// canonical form. Parsing rejects what a pattern match would let through — the 31st of a 30-day month,
// the 29th of February in a common year — so a stored date is always one SQLite's date functions can
// compute with. Unlike its datetime and time siblings the only accepted shape already is the canonical
// one, so today the value passes through unchanged; the returning shape keeps the three types uniform
// at their call sites. ok is false when s is not a calendar day.
func CanonicalDate(s string) (canonical string, ok bool) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return "", false
	}
	return t.Format(time.DateOnly), true
}

// CanonicalDateTime parses s as a real calendar day with a clock time and returns the canonical
// "YYYY-MM-DD HH:MM:SS" form. The "T" and space separators are both accepted, seconds are optional and
// fractional seconds truncate, but the result always carries the one shape — canonical values compare
// and order correctly as plain TEXT, which mixed shapes do not. ok is false when s parses as neither.
func CanonicalDateTime(s string) (canonical string, ok bool) {
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", time.DateTime, "2006-01-02 15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(time.DateTime), true
		}
	}
	return "", false
}

// CanonicalTime parses s as a clock time and returns the canonical "HH:MM:SS" form. Seconds are
// optional on input and fractional seconds truncate; the 24-hour clock is required ("24:00" and "9pm"
// are refused). ok is false when s is not a clock time.
func CanonicalTime(s string) (canonical string, ok bool) {
	for _, layout := range []string{time.TimeOnly, "15:04"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(time.TimeOnly), true
		}
	}
	return "", false
}

// MatchOption resolves raw against a parameter's declared option values, ignoring case and
// surrounding whitespace, and returns the value as declared so the bound value is always the
// canonical spelling. ok is false when no option matches.
func MatchOption(options []FormulaOption, raw string) (canonical string, ok bool) {
	raw = strings.TrimSpace(raw)
	for _, o := range options {
		if strings.EqualFold(strings.TrimSpace(o.Value), raw) {
			return o.Value, true
		}
	}
	return "", false
}

// MatchOptionInt reports whether one of the options' values reads as n. An integer param's declared
// values are validated as integer text, so an entry that does not parse never occurs.
func MatchOptionInt(options []FormulaOption, n int64) bool {
	for _, o := range options {
		if v, err := strconv.ParseInt(o.Value, 10, 64); err == nil && v == n {
			return true
		}
	}
	return false
}

// MatchOptionFloat reports whether one of the options' values reads as f. A float param's declared
// values are validated as numeric text, so an entry that does not parse never occurs.
func MatchOptionFloat(options []FormulaOption, f float64) bool {
	for _, o := range options {
		if v, err := strconv.ParseFloat(o.Value, 64); err == nil && v == f {
			return true
		}
	}
	return false
}

// validateMultiline checks that the display-only multiline flag sits on a typed-in text parameter —
// it selects the app's textarea input, which no other type or source renders.
func (fp FormulaParam) validateMultiline() error {
	if fp.Multiline && (fp.Type != ParamTypeText || fp.Source != ParamSourcePrompt) {
		return fmt.Errorf("%w: only a prompt text parameter can be multiline (param %q is %s %s)",
			ErrFormulaParamInvalid, fp.Name, fp.Source, fp.Type)
	}
	return nil
}

// validateLookup checks the parameter's lookup configuration: a lookup-source parameter carries a
// complete config with plainly-named, non-reserved table and columns, and no other source carries one.
func (fp FormulaParam) validateLookup() error {
	if fp.Source != ParamSourceLookup {
		if fp.Lookup != nil {
			return fmt.Errorf("%w: only a lookup-source param may carry a lookup config", ErrFormulaParamInvalid)
		}
		return nil
	}
	if fp.Lookup == nil {
		return fmt.Errorf("%w: a lookup param must carry a lookup config", ErrFormulaParamInvalid)
	}

	if strings.HasPrefix(fp.Lookup.Table, "_") {
		return fmt.Errorf("%w: lookup table must not use the reserved _ prefix", ErrFormulaParamInvalid)
	}

	fields := []struct {
		label string
		value string
	}{
		{"table", fp.Lookup.Table},
		{"label column", fp.Lookup.LabelColumn},
		{"value column", fp.Lookup.ValueColumn},
	}
	for _, f := range fields {
		if !lookupIdentifierPattern.MatchString(f.value) {
			return fmt.Errorf("%w: lookup %s must match %s",
				ErrFormulaParamInvalid, f.label, lookupIdentifierPattern.String())
		}
	}

	// A value typed past the options can only be text; any other type would make the free-text
	// path unparseable while the pick path still worked.
	if fp.Lookup.AllowNew && fp.Type != ParamTypeText {
		return fmt.Errorf("%w: an allow_new lookup must be a text parameter (got type %q)",
			ErrFormulaParamInvalid, fp.Type)
	}

	return nil
}

// validateScope checks a lookup's scope declaration, where each entry narrows the picker's options
// by a sibling parameter (the entry's key) matched against a column of the lookup table (the
// entry's value):
//   - every sibling must be a declared scalar prompt-sourced parameter — never the lookup itself, a
//     group, or an undeclared name;
//   - every column must be plainly named;
//   - a group field's lookup must not declare a scope — a field's siblings are repeated form rows,
//     not schema parameters.
//
// byName indexes every declared parameter of the schema by name, so sibling references resolve
// without rescanning the schema.
func (fp FormulaParam) validateScope(byName map[string]FormulaParam) error {
	for _, field := range fp.Fields {
		if field.Lookup != nil && len(field.Lookup.Scope) > 0 {
			return fmt.Errorf("%w: group field %q of parameter %q must not declare a scope",
				ErrFormulaParamInvalid, field.Name, fp.Name)
		}
	}

	if fp.Lookup == nil || len(fp.Lookup.Scope) == 0 {
		return nil
	}

	for _, scopeName := range slices.Sorted(maps.Keys(fp.Lookup.Scope)) {
		scopeParam, ok := byName[scopeName]
		if !ok {
			return fmt.Errorf("%w: parameter %q scopes on undeclared parameter %q",
				ErrFormulaParamInvalid, fp.Name, scopeName)
		}
		if scopeParam.Source != ParamSourcePrompt {
			return fmt.Errorf("%w: parameter %q scopes on %q, which is not prompt-sourced — "+
				"only a typed or picked field has a value to scope by", ErrFormulaParamInvalid, fp.Name, scopeName)
		}
		if scopeParam.Type == ParamTypeGroup {
			return fmt.Errorf("%w: parameter %q scopes on %q, which is a group",
				ErrFormulaParamInvalid, fp.Name, scopeName)
		}

		if column := fp.Lookup.Scope[scopeName]; !lookupIdentifierPattern.MatchString(column) {
			return fmt.Errorf("%w: scope column %q of parameter %q must match %s",
				ErrFormulaParamInvalid, column, fp.Name, lookupIdentifierPattern.String())
		}
	}

	return nil
}

// validateFill checks a lookup's fill declaration, where each entry copies a column of the lookup
// table (the entry's value) into a sibling parameter (the entry's key) when a row is picked:
//   - every sibling must be a declared scalar prompt- or lookup-sourced parameter — never the lookup
//     itself, a group, or an undeclared name; a lookup sibling is fine, since its field takes a value
//     like any other (the fill lands as the bound value, which the picker then displays);
//   - every column must be plainly named;
//   - a multi-select lookup must not declare a fill — several picked rows have no single value to
//     copy;
//   - a group field's lookup must not declare a fill — a field's siblings are repeated form rows,
//     not schema parameters.
//
// byName indexes every declared parameter of the schema by name, so sibling references resolve
// without rescanning the schema.
func (fp FormulaParam) validateFill(byName map[string]FormulaParam) error {
	for _, field := range fp.Fields {
		if field.Lookup != nil && len(field.Lookup.Fill) > 0 {
			return fmt.Errorf("%w: group field %q of parameter %q must not declare a fill",
				ErrFormulaParamInvalid, field.Name, fp.Name)
		}
	}

	if fp.Lookup == nil || len(fp.Lookup.Fill) == 0 {
		return nil
	}
	if fp.Multiple {
		return fmt.Errorf("%w: parameter %q is multiple and cannot declare a fill — "+
			"several picked rows have no single value to copy", ErrFormulaParamInvalid, fp.Name)
	}

	for _, siblingName := range slices.Sorted(maps.Keys(fp.Lookup.Fill)) {
		sibling, ok := byName[siblingName]
		if !ok {
			return fmt.Errorf("%w: parameter %q fills undeclared parameter %q",
				ErrFormulaParamInvalid, fp.Name, siblingName)
		}
		if siblingName == fp.Name {
			return fmt.Errorf("%w: parameter %q cannot fill itself", ErrFormulaParamInvalid, fp.Name)
		}
		if sibling.Source != ParamSourcePrompt && sibling.Source != ParamSourceLookup {
			return fmt.Errorf("%w: parameter %q fills %q, which is neither prompt- nor lookup-sourced — "+
				"only a form field can take a value", ErrFormulaParamInvalid, fp.Name, siblingName)
		}
		if sibling.Type == ParamTypeGroup {
			return fmt.Errorf("%w: parameter %q fills %q, which is a group",
				ErrFormulaParamInvalid, fp.Name, siblingName)
		}

		if column := fp.Lookup.Fill[siblingName]; !lookupIdentifierPattern.MatchString(column) {
			return fmt.Errorf("%w: fill column %q of parameter %q must match %s",
				ErrFormulaParamInvalid, column, fp.Name, lookupIdentifierPattern.String())
		}
	}

	return nil
}

// CrossCheckParams verifies that a formula's declared parameter schema lines up with the bind
// parameters its SQL actually uses.
// sqlParamNames is the statement's bind parameters in index order, as read from a prepare -
// a named placeholder carries its name (":status" → "status") and an anonymous "?" appears as an empty string.
// It errors when any placeholder is anonymous (formulas require named parameters so each maps to a schema entry),
// when the SQL uses a parameter the schema does not declare, or when the schema declares one the SQL never uses.
func CrossCheckParams(schema ParamSchema, sqlParamNames []string) error {
	declared := make(map[string]struct{}, len(schema))
	for _, ps := range schema {
		declared[ps.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(schema))

	for _, pname := range sqlParamNames {
		if pname == "" {
			return fmt.Errorf("%w: sql formula cannot contain anonymous placeholder", ErrFormulaParamInvalid)
		}

		if _, ok := declared[pname]; !ok {
			return fmt.Errorf("%w: sql param %q is not in the declared parameters", ErrFormulaParamInvalid, pname)
		}

		seen[pname] = struct{}{}
	}

	if len(seen) != len(declared) {
		unused := make([]string, 0, len(declared)-len(seen))
		// Iterates the schema, not the declared set, so the reported order matches the declaration order.
		for _, ps := range schema {
			if _, ok := seen[ps.Name]; !ok {
				unused = append(unused, ps.Name)
			}
		}
		return fmt.Errorf("%w: declared params not used in the sql formula: %s",
			ErrFormulaParamInvalid, strings.Join(unused, ", "))
	}

	return nil
}

// ValidateRowDetailParams checks the parameter schema of a view attached to another view — a row
// detail, run from a single clicked row: it must declare the reserved rowid selection parameter,
// and every other parameter must be optional or pagination-sourced, since a row click supplies
// nothing else. The rowid source and requiredness are already pinned by ParamSchema.Validate
// (rowid must be selection-single, and a selection parameter cannot be optional).
func ValidateRowDetailParams(schema ParamSchema) error {
	hasRowID := false
	for _, p := range schema {
		if p.Name == ReservedParamRowID {
			hasRowID = true
			continue
		}
		if p.Source == ParamSourcePagination || p.Source == ParamSourceWindow || p.Optional {
			continue
		}
		return fmt.Errorf("%w: a row-detail view's parameter %q must be optional, pagination-, or window-sourced",
			ErrFormulaAttachConflict, p.Name)
	}
	if !hasRowID {
		return fmt.Errorf("%w: a row-detail view must declare the %q selection parameter",
			ErrFormulaAttachConflict, ReservedParamRowID)
	}
	return nil
}
