// Package domain holds the starters model: inert manifests describing the tables, formulas, and skills a new
// bdb is seeded with, and their structural validation. Manifests are storage-agnostic — each source adapter
// decodes its own stored form into these types, which carry no serialization tags.
package domain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// StarterManifest is the canonical, inert description of one starter: catalog metadata plus everything needed
// to seed a new bdb.
type StarterManifest struct {
	// Key is the stable identifier recorded as provenance on bdbs created from this starter.
	Key string
	// Version is the starter's integer version. A version is immutable: any content change bumps it.
	Version     int
	Name        string
	Description string
	Icon        string
	Tags        []string
	// Schema is the DDL applied first, one SQL statement per entry, in order.
	Schema []string
	// Formulas are created after the schema, views before the actions attached to them.
	Formulas []StarterFormula
	Skills   []StarterSkill
	// Seed is the reference rows inserted right after the schema, one entry per table: the catalog a formula
	// expects to exist (a list of slots, a fixed set of categories), never sample user content.
	Seed []StarterSeed
}

// StarterSeed is the rows a starter inserts into one of its own tables: Columns names the columns each row
// fills, in order, and every row holds one scalar per column (a string, a number, a bool, or nil for NULL).
type StarterSeed struct {
	Table   string
	Columns []string
	Rows    [][]any
}

// InsertStatement renders the seed as one multi-row INSERT with positional placeholders, returning the SQL and
// the flattened bound values, row by row. Identifiers are double-quoted so a name never has to be SQL-safe.
func (s StarterSeed) InsertStatement() (string, []any) {
	quote := func(ident string) string { return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"` }
	cols := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		cols[i] = quote(c)
	}
	row := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(s.Columns)), ", ") + ")"
	rows := make([]string, len(s.Rows))
	args := make([]any, 0, len(s.Rows)*len(s.Columns))
	for i, r := range s.Rows {
		rows[i] = row
		args = append(args, r...)
	}
	return "INSERT INTO " + quote(s.Table) + " (" + strings.Join(cols, ", ") + ") VALUES " +
		strings.Join(rows, ", "), args
}

// StarterFormula seeds one formula. The view-vs-action kind is not authored — the classifier derives it from
// the SQL when the formula is created.
type StarterFormula struct {
	Name string
	// Label is the optional human name the app renders instead of Name; empty falls back to Name.
	Label       string
	Description string
	SQL         string
	Params      []StarterFormulaParam
	// AttachedTo names the view (by formula name within the same manifest) this action attaches to. The engine
	// resolves it to the UUID the view received when it was created a step earlier.
	AttachedTo string
	// Display is the view's optional presentation config. It is applied after every manifest formula exists,
	// so its slot refs (sibling formula names) resolve; nil seeds the default table rendering.
	Display *StarterFormulaDisplay
	// Hidden marks the formula assistant-only: the app omits it from the members' formula listing by default.
	Hidden bool
}

// StarterFormulaDisplay mirrors the formula display payload: how the app renders the view's rows and which
// formulas attached to it (named within the same manifest) fill the rendering's interaction slots.
type StarterFormulaDisplay struct {
	Mode    string
	Columns []StarterDisplayColumn
	Slots   StarterDisplaySlots
	Window  *StarterDisplayWindow
}

// StarterDisplayWindow mirrors a calendar's window: weekday names for a weekly window, a start day and a
// length in days for a fixed range.
type StarterDisplayWindow struct {
	WeekStart string
	WeekEnd   string
	Start     string
	Days      int
}

// StarterDisplayColumn assigns one result column a rendering role (title / body / check) and/or a
// value format (markdown), or marks it hidden (dropped from the grid, kept in the row data).
type StarterDisplayColumn struct {
	Column string
	Role   string
	Format string
	Hidden bool
}

// StarterDisplaySlots names the display's interaction slots by formula name. Bulk and RowActions are ordered
// lists — array order is the render order.
type StarterDisplaySlots struct {
	Add        string
	Toggle     string
	Edit       string
	Delete     string
	Detail     string
	Reorder    string
	Move       string
	Bulk       []string
	RowActions []string
}

// Refs yields the filled slot refs in declaration order (add, toggle, edit, delete, detail, reorder, move, then each
// bulk entry, then each row_actions entry), skipping unfilled slots.
func (s StarterDisplaySlots) Refs() []string {
	var refs []string
	for _, ref := range []string{s.Add, s.Toggle, s.Edit, s.Delete, s.Detail, s.Reorder, s.Move} {
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	for _, ref := range s.Bulk {
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	for _, ref := range s.RowActions {
		if ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

// StarterFormulaParam is one entry in a seeded formula's ordered parameter schema. It mirrors the create-formula
// parameter payload: Type is a value type (text / integer / float / boolean / group), Source a binding source
// (prompt / selection-single / selection-bulk / pagination / lookup / reorder), Lookup is set only for lookup
// params, and
// Fields carries a group parameter's per-row schema.
type StarterFormulaParam struct {
	Name     string
	Type     string
	Source   string
	Optional bool
	Lookup   *StarterLookupConfig
	// Hint is display-only placeholder text for the app's input. It never affects binding.
	Hint string
	// Multiline marks a prompt text parameter for the app's textarea input. Display-only.
	Multiline bool
	// Inline places the parameter in the app's quick-add form when its action fills an add slot. Display-only.
	Inline bool
	// Multiple marks a lookup or options parameter multi-select: the run value is an array of picked values
	// binding as one JSON TEXT value, consumed via json_each; Default may then only be the literal "all", and
	// Defaults carries a declared subset instead.
	Multiple bool
	// Options is the closed set of accepted values for a typed-in prompt parameter (text, integer,
	// float, date).
	Options []StarterFormulaOption
	// Default is the value an omitted optional prompt parameter binds instead of SQL NULL, as its
	// string form; the app's forms prefill from it.
	Default string
	// Defaults is a multi-select parameter's declared subset, each value in its string form; exclusive
	// with Default.
	Defaults []string
	// When gates the parameter's form field on a sibling parameter's current value. Display-only.
	When   *StarterParamWhen
	Fields []StarterFormulaParam
}

// StarterFormulaOption is one closed-set entry: Value is what a run binds, as the param type's string form
// ("film", "9"); Label is what the app shows in its place ("September" shown, "9" bound).
type StarterFormulaOption struct {
	Value string
	Label string
}

// StarterLookupConfig names where a lookup parameter's options come from: LabelColumn is shown in the picker,
// ValueColumn supplies the bound value. ExcludeSelection drops the acting row from the picker's options — for
// lookups reading the same table the row lives in. Display-only.
type StarterLookupConfig struct {
	Table            string
	LabelColumn      string
	ValueColumn      string
	ExcludeSelection bool
	// AllowNew lets the picker also submit text typed past the option list — the formula's SQL should resolve
	// or create it; text params only.
	AllowNew bool
	// Scope narrows the picker's options by sibling parameters' current values: sibling param name → column of
	// Table compared against it. Display-only.
	Scope map[string]string
	// Fill copies columns of the picked row into sibling parameters: sibling param name → column of Table whose
	// value the pick writes there. Display-only.
	Fill map[string]string
}

// StarterParamWhen is a parameter's display-only visibility condition: the app's forms render the field only
// while the named sibling parameter's current value compares true against Value under Op (empty means "eq").
type StarterParamWhen struct {
	Param string
	Op    string
	Value string
}

// StarterSkill seeds one skill. Targets lists the table names the skill applies to; empty means bdb-level.
type StarterSkill struct {
	Name        string
	Description string
	Body        string
	Targets     []string
}

// ValidateManifestSet checks a decoded set of manifests for structural authoring errors: identity fields
// present, unique keys, unique formula and skill names, and attachment references that resolve within their
// manifest. Deeper semantic validation (SQL classification, parameter cross-checks, lookup targets) happens
// when a manifest is applied through the real services.
func ValidateManifestSet(manifests []StarterManifest) error {
	seenKeys := make(map[string]struct{}, len(manifests))
	for _, m := range manifests {
		if strings.TrimSpace(m.Key) == "" {
			return fmt.Errorf("%w: manifest key is blank", ErrManifestInvalid)
		}
		if _, dup := seenKeys[m.Key]; dup {
			return fmt.Errorf("%w: manifest %q: key is duplicated", ErrManifestInvalid, m.Key)
		}
		seenKeys[m.Key] = struct{}{}

		if err := validateManifest(m); err != nil {
			return fmt.Errorf("%w: manifest %q: %w", ErrManifestInvalid, m.Key, err)
		}
	}
	return nil
}

// validateManifest checks one manifest's local invariants. Returned errors carry neither the manifest identity
// nor the ErrManifestInvalid sentinel.
func validateManifest(m StarterManifest) error {
	if m.Version < 1 {
		return fmt.Errorf("version must be >= 1, is %d", m.Version)
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("name is blank")
	}
	if len(m.Schema) == 0 {
		return errors.New("schema is empty")
	}
	for i, stmt := range m.Schema {
		if strings.TrimSpace(stmt) == "" {
			return fmt.Errorf("schema statement %d is blank", i)
		}
	}
	if err := validateSeed(m.Seed, m.Schema); err != nil {
		return err
	}
	if err := validateFormulas(m.Formulas); err != nil {
		return err
	}
	return validateSkills(m.Skills)
}

// validateSeed checks each seed entry: it names a table the schema creates, its columns are present and
// distinct, it carries at least one row, and every row holds exactly one scalar per column.
func validateSeed(seed []StarterSeed, schema []string) error {
	for i, sd := range seed {
		if strings.TrimSpace(sd.Table) == "" {
			return fmt.Errorf("seed %d: table is blank", i)
		}
		if !schemaCreatesTable(schema, sd.Table) {
			return fmt.Errorf("seed %d: table %q is not created by the schema", i, sd.Table)
		}
		if len(sd.Columns) == 0 {
			return fmt.Errorf("seed %d (%s): columns are empty", i, sd.Table)
		}
		seen := make(map[string]struct{}, len(sd.Columns))
		for _, c := range sd.Columns {
			if strings.TrimSpace(c) == "" {
				return fmt.Errorf("seed %d (%s): a column name is blank", i, sd.Table)
			}
			if _, dup := seen[c]; dup {
				return fmt.Errorf("seed %d (%s): column %q is listed twice", i, sd.Table, c)
			}
			seen[c] = struct{}{}
		}
		if len(sd.Rows) == 0 {
			return fmt.Errorf("seed %d (%s): rows are empty", i, sd.Table)
		}
		for j, row := range sd.Rows {
			if len(row) != len(sd.Columns) {
				return fmt.Errorf("seed %d (%s): row %d has %d values, want %d",
					i, sd.Table, j, len(row), len(sd.Columns))
			}
			for k, v := range row {
				switch v.(type) {
				case nil, string, bool, int, int64, float64:
				default:
					return fmt.Errorf("seed %d (%s): row %d value %d is a %T, want a scalar", i, sd.Table, j, k, v)
				}
			}
		}
	}
	return nil
}

// schemaCreatesTable reports whether one of the schema statements is a CREATE TABLE for the given name,
// quoted or bare, with or without IF NOT EXISTS.
func schemaCreatesTable(schema []string, table string) bool {
	pattern := regexp.MustCompile(`(?is)^\s*(?:--[^\n]*\n\s*)*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?["\x60]?` +
		regexp.QuoteMeta(table) + `["\x60]?\s*\(`)
	return slices.ContainsFunc(schema, pattern.MatchString)
}

// validateFormulas checks that formula names are present and unique and that every attachment resolves to
// another, itself unattached, formula.
func validateFormulas(formulas []StarterFormula) error {
	formulasByName := make(map[string]StarterFormula, len(formulas))
	for i, f := range formulas {
		if strings.TrimSpace(f.Name) == "" {
			return fmt.Errorf("formula %d: name is blank", i)
		}
		if _, dup := formulasByName[f.Name]; dup {
			return fmt.Errorf("formula %d: name %q is duplicated", i, f.Name)
		}
		formulasByName[f.Name] = f
	}

	// Attachments are checked in a second pass so a target declared later in the manifest still resolves.
	// Chains are allowed — a row-detail view's own slotted actions attach to the detail, which is itself
	// attached — so only dangling targets, self-attachment, and cycles are authoring errors.
	for i, f := range formulas {
		if f.AttachedTo == "" {
			continue
		}
		if f.AttachedTo == f.Name {
			return fmt.Errorf("formula %d: %q cannot attach to itself", i, f.Name)
		}
		if _, ok := formulasByName[f.AttachedTo]; !ok {
			return fmt.Errorf("formula %d: %q attached to %q which does not exist", i, f.Name, f.AttachedTo)
		}
		// Walks the parent chain; needing more steps than there are formulas means a cycle.
		parent := f.AttachedTo
		for steps := 0; parent != ""; steps++ {
			if steps >= len(formulas) {
				return fmt.Errorf("formula %d: %q attachment chain contains a cycle", i, f.Name)
			}
			parent = formulasByName[parent].AttachedTo
		}
	}
	return validateFormulaDisplays(formulas, formulasByName)
}

// validateFormulaDisplays checks every display's slot refs: each ref must name a formula in the same manifest
// that is attached to the view carrying the display. Deeper shape checks (mode rules, verbs, params) happen
// when the display is applied through the real services.
func validateFormulaDisplays(formulas []StarterFormula, formulasByName map[string]StarterFormula) error {
	for i, f := range formulas {
		if f.Display == nil {
			continue
		}
		for _, r := range f.Display.Slots.Refs() {
			target, ok := formulasByName[r]
			if !ok {
				return fmt.Errorf("formula %d: %q contains a slot ref %q which does not exist in the starter manifest",
					i, f.Name, r)
			}
			if target.AttachedTo != f.Name {
				return fmt.Errorf("formula %d: %q contains a slot ref %q which is not attached to the view",
					i, f.Name, r)
			}
		}
	}
	return nil
}

// validateSkills checks that skill names are present and unique.
func validateSkills(skills []StarterSkill) error {
	seenNames := make(map[string]struct{}, len(skills))
	for i, s := range skills {
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("skill %d: name is blank", i)
		}
		if _, dup := seenNames[s.Name]; dup {
			return fmt.Errorf("skill %d: name %q is duplicated", i, s.Name)
		}
		seenNames[s.Name] = struct{}{}
	}
	return nil
}
