package domain

import (
	"errors"
	"strings"
	"testing"
)

// opts builds a value-equals-label option set from bare values, the pre-label authored shape.
func opts(values ...string) []FormulaOption {
	out := make([]FormulaOption, len(values))
	for i, v := range values {
		out[i] = FormulaOption{Value: v, Label: v}
	}
	return out
}

func TestValidateFormulaName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"empty", "", ErrFormulaNameInvalid},
		{"single letter", "a", nil},
		{"valid slug", "overdue-tasks", nil},
		{"valid with digits", "report-2026", nil},
		{"max length", strings.Repeat("a", FormulaNameMaxLen), nil},
		{"over max length", strings.Repeat("a", FormulaNameMaxLen+1), ErrFormulaNameInvalid},
		{"starts with digit", "1-formula", ErrFormulaNameInvalid},
		{"uppercase", "Formula", ErrFormulaNameInvalid},
		{"contains space", "my formula", ErrFormulaNameInvalid},
		{"contains underscore", "my_formula", ErrFormulaNameInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFormulaName(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateFormulaName(%q) returned error: %v, want nil", tt.input, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateFormulaName(%q) error = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateFormulaLabel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"empty falls back to the name", "", nil},
		{"plain label", "Shopping list", nil},
		{"punctuation and accents", "Épinards & co.", nil},
		{"max length in runes", strings.Repeat("é", FormulaLabelMaxLen), nil},
		{"over max length", strings.Repeat("a", FormulaLabelMaxLen+1), ErrFormulaLabelInvalid},
		{"newline", "Shopping\nlist", ErrFormulaLabelInvalid},
		{"tab", "Shopping\tlist", ErrFormulaLabelInvalid},
		{"zero-width space", "Shopping\u200blist", ErrFormulaLabelInvalid},
		{"right-to-left override", "Shopping\u202elist", ErrFormulaLabelInvalid},
		{"non-breaking space stays legal", "Shopping\u00a0list", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFormulaLabel(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateFormulaLabel(%q) returned error: %v, want nil", tt.input, err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateFormulaLabel(%q) error = %v, want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestValidateFormulaDescription(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"empty", "", ErrFormulaDescriptionInvalid},
		{"normal", "Lists tasks that are overdue.", nil},
		{"max length", strings.Repeat("a", FormulaDescriptionMaxLen), nil},
		{"over max length", strings.Repeat("a", FormulaDescriptionMaxLen+1), ErrFormulaDescriptionInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFormulaDescription(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateFormulaDescription returned error: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateFormulaDescription error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateFormulaSQL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"empty", "", ErrFormulaSQLInvalid},
		{"normal", "SELECT rowid, * FROM tasks WHERE status = :status", nil},
		{"max bytes", strings.Repeat("a", FormulaSQLMaxBytes), nil},
		{"over max bytes", strings.Repeat("a", FormulaSQLMaxBytes+1), ErrFormulaSQLInvalid},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFormulaSQL(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateFormulaSQL returned error: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ValidateFormulaSQL error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestDeriveFormulaKind pins the view-vs-action derivation: a lone SELECT is a
// view, one or more INSERT/UPDATE/DELETE statements are an action, and anything
// else (empty, several SELECTs, a read/write mix, DDL, unsupported) is rejected.
func TestDeriveFormulaKind(t *testing.T) {
	tests := []struct {
		name     string
		stmts    []ClassifiedStatement
		wantKind FormulaKind
		wantErr  error
	}{
		{"select is a view", []ClassifiedStatement{{Type: StatementDQL}}, FormulaKindView, nil},
		{"dml is an action", []ClassifiedStatement{{Type: StatementDML}}, FormulaKindAction, nil},
		{
			"several dml statements are an action",
			[]ClassifiedStatement{{Type: StatementDML}, {Type: StatementDML}, {Type: StatementDML}},
			FormulaKindAction, nil,
		},
		{"ddl rejected", []ClassifiedStatement{{Type: StatementDDL}}, "", ErrFormulaKindUndeterminable},
		{"unsupported rejected", []ClassifiedStatement{{Type: StatementUnsupported}}, "", ErrFormulaKindUndeterminable},
		{"empty rejected", nil, "", ErrFormulaKindUndeterminable},
		{
			"several selects rejected",
			[]ClassifiedStatement{{Type: StatementDQL}, {Type: StatementDQL}},
			"", ErrFormulaKindUndeterminable,
		},
		{
			"read/write mix rejected",
			[]ClassifiedStatement{{Type: StatementDML}, {Type: StatementDQL}},
			"", ErrFormulaKindUndeterminable,
		},
		{
			"ddl among dml rejected",
			[]ClassifiedStatement{{Type: StatementDML}, {Type: StatementDDL}},
			"", ErrFormulaKindUndeterminable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeriveFormulaKind(tt.stmts)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("DeriveFormulaKind() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DeriveFormulaKind() unexpected error: %v", err)
			}
			if got != tt.wantKind {
				t.Errorf("DeriveFormulaKind() = %q, want %q", got, tt.wantKind)
			}
		})
	}
}

// TestDeriveStatementVerb pins the verb reduction: a lone SELECT reads as select, and a write
// takes the most consequential verb recorded across all statements (delete > insert > update).
func TestDeriveStatementVerb(t *testing.T) {
	tests := []struct {
		name  string
		stmts []ClassifiedStatement
		want  StatementVerb
	}{
		{"lone select", []ClassifiedStatement{{Type: StatementDQL}}, StatementVerbSelect},
		{"no verb recorded", []ClassifiedStatement{{Type: StatementDML}}, ""},
		{"empty input", nil, ""},
		{
			"several selects return no verb",
			[]ClassifiedStatement{{Type: StatementDQL}, {Type: StatementDQL}},
			"",
		},
		{
			"read/write mix never reads as select",
			[]ClassifiedStatement{
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbUpdate}},
				{Type: StatementDQL},
			},
			StatementVerbUpdate,
		},
		{
			"update then delete reads as delete",
			[]ClassifiedStatement{
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbUpdate}},
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbDelete}},
			},
			StatementVerbDelete,
		},
		{
			"insert then update reads as insert",
			[]ClassifiedStatement{
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbInsert}},
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbUpdate}},
			},
			StatementVerbInsert,
		},
		{
			"insert then delete reads as delete",
			[]ClassifiedStatement{
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbInsert}},
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbDelete}},
			},
			StatementVerbDelete,
		},
		{
			"updates only stay update",
			[]ClassifiedStatement{
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbUpdate}},
				{Type: StatementDML, DMLVerbs: []StatementVerb{StatementVerbUpdate}},
			},
			StatementVerbUpdate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveStatementVerb(tt.stmts); got != tt.want {
				t.Errorf("DeriveStatementVerb() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestValidateRowDetailParams pins the row-detail schema rule: the reserved rowid selection
// parameter is required, and every other parameter must be optional or pagination-sourced (a
// row click supplies nothing else).
func TestValidateRowDetailParams(t *testing.T) {
	tests := []struct {
		name    string
		schema  ParamSchema
		wantErr bool
	}{
		{
			"rowid only",
			ParamSchema{{Name: "rowid", Type: ParamTypeInteger, Source: ParamSourceSelectionSingle}},
			false,
		},
		{
			"rowid plus optional prompt",
			ParamSchema{
				{Name: "rowid", Type: ParamTypeInteger, Source: ParamSourceSelectionSingle},
				{Name: "note", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true},
			},
			false,
		},
		{
			"rowid plus pagination",
			ParamSchema{
				{Name: "rowid", Type: ParamTypeInteger, Source: ParamSourceSelectionSingle},
				{Name: "cursor", Type: ParamTypeInteger, Source: ParamSourcePagination},
				{Name: "limit", Type: ParamTypeInteger, Source: ParamSourcePagination},
			},
			false,
		},
		{"no params", ParamSchema{}, true},
		{
			"rowid plus required prompt",
			ParamSchema{
				{Name: "rowid", Type: ParamTypeInteger, Source: ParamSourceSelectionSingle},
				{Name: "filter", Type: ParamTypeText, Source: ParamSourcePrompt},
			},
			true,
		},
		{
			"bulk selection is not row-detail bindable",
			ParamSchema{
				{Name: "rowid", Type: ParamTypeInteger, Source: ParamSourceSelectionSingle},
				{Name: "rowids", Type: ParamTypeInteger, Source: ParamSourceSelectionBulk},
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRowDetailParams(tt.schema)
			if tt.wantErr && !errors.Is(err, ErrFormulaAttachConflict) {
				t.Errorf("ValidateRowDetailParams() error = %v, want ErrFormulaAttachConflict", err)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateRowDetailParams() unexpected error: %v", err)
			}
		})
	}
}

// TestParamSchema_Validate pins the parameter-schema rules: name shape + length
// + uniqueness, allowed type, allowed source, and the reserved selection names
// (rowid / parent_rowid / rowids) being bound to their required source.
func TestParamSchema_Validate(t *testing.T) {
	tests := []struct {
		name    string
		schema  ParamSchema
		wantErr error
	}{
		{"empty schema", ParamSchema{}, nil},
		{
			"valid prompt param",
			ParamSchema{{Name: "status", Type: ParamTypeText, Source: ParamSourcePrompt}},
			nil,
		},
		{
			"valid rowid selection",
			ParamSchema{{Name: ReservedParamRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionSingle}},
			nil,
		},
		{
			"valid rowids selection",
			ParamSchema{{Name: ReservedParamRowIDs, Type: ParamTypeInteger, Source: ParamSourceSelectionBulk}},
			nil,
		},
		{
			"valid parent_rowid selection",
			ParamSchema{{
				Name: ReservedParamParentRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionSingle,
			}},
			nil,
		},
		{
			"valid options on a prompt text param",
			ParamSchema{{
				Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Options: opts("film", "tv"),
			}},
			nil,
		},
		{
			"valid options on an integer param",
			ParamSchema{{
				Name: "n", Type: ParamTypeInteger, Source: ParamSourcePrompt, Options: opts("1", "2"),
			}},
			nil,
		},
		{
			"valid labeled options on an integer param",
			ParamSchema{{
				Name: "month", Type: ParamTypeInteger, Source: ParamSourcePrompt,
				Options: []FormulaOption{{Value: "1", Label: "January"}, {Value: "2", Label: "February"}},
			}},
			nil,
		},
		{
			"valid options on a date param",
			ParamSchema{{
				Name: "day", Type: ParamTypeDate, Source: ParamSourcePrompt, Options: opts("2026-01-01", "2026-06-01"),
			}},
			nil,
		},
		{
			"options on a boolean param",
			ParamSchema{{
				Name: "flag", Type: ParamTypeBoolean, Source: ParamSourcePrompt, Options: opts("true", "false"),
			}},
			ErrFormulaParamInvalid,
		},
		{
			"options on a datetime param",
			ParamSchema{{
				Name: "at", Type: ParamTypeDateTime, Source: ParamSourcePrompt, Options: opts("2026-01-01 09:00:00"),
			}},
			ErrFormulaParamInvalid,
		},
		{
			"integer options with a non-integer value",
			ParamSchema{{
				Name: "n", Type: ParamTypeInteger, Source: ParamSourcePrompt, Options: opts("1", "two"),
			}},
			ErrFormulaParamInvalid,
		},
		{
			"date options with a non-date value",
			ParamSchema{{
				Name: "day", Type: ParamTypeDate, Source: ParamSourcePrompt, Options: opts("2026-02-30"),
			}},
			ErrFormulaParamInvalid,
		},
		{
			"options with a blank entry",
			ParamSchema{{
				Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Options: opts("film", "  "),
			}},
			ErrFormulaParamInvalid,
		},
		{
			"options with surrounding whitespace",
			ParamSchema{{
				Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Options: opts("film", " tv "),
			}},
			ErrFormulaParamInvalid,
		},
		{
			"options differing only by case",
			ParamSchema{{
				Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Options: opts("film", "Film"),
			}},
			ErrFormulaParamInvalid,
		},
		{
			"options with a blank label",
			ParamSchema{{
				Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt,
				Options: []FormulaOption{{Value: "film", Label: " "}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"options whose labels differ only by case",
			ParamSchema{{
				Name: "n", Type: ParamTypeInteger, Source: ParamSourcePrompt,
				Options: []FormulaOption{{Value: "1", Label: "One"}, {Value: "2", Label: "one"}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"valid options on a group field",
			ParamSchema{{
				Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{
					Name: "unit", Type: ParamTypeText, Source: ParamSourcePrompt, Options: opts("g", "kg"),
				}},
			}},
			nil,
		},
		{
			"options on a boolean group field",
			ParamSchema{{
				Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{
					Name: "urgent", Type: ParamTypeBoolean, Source: ParamSourcePrompt, Options: opts("true", "false"),
				}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"valid inline on a prompt text param",
			ParamSchema{{Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Inline: true}},
			nil,
		},
		{
			"valid inline on a lookup param",
			ParamSchema{{
				Name: "member", Type: ParamTypeInteger, Source: ParamSourceLookup, Inline: true,
				Lookup: &LookupConfig{Table: "members", LabelColumn: "name", ValueColumn: "id"},
			}},
			nil,
		},
		{
			"inline on a group param",
			ParamSchema{{
				Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt, Inline: true,
				Fields: []FormulaParam{{Name: "qty", Type: ParamTypeInteger, Source: ParamSourcePrompt}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"inline on a selection param",
			ParamSchema{{
				Name: ReservedParamRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionSingle, Inline: true,
			}},
			ErrFormulaParamInvalid,
		},
		{
			"inline on a group field",
			ParamSchema{{
				Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{Name: "unit", Type: ParamTypeText, Source: ParamSourcePrompt, Inline: true}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"valid default on an optional prompt text param with options",
			ParamSchema{{
				Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true,
				Options: opts("film", "tv"), Default: "film",
			}},
			nil,
		},
		{
			"valid integer default",
			ParamSchema{{
				Name: "year", Type: ParamTypeInteger, Source: ParamSourcePrompt, Optional: true, Default: "2026",
			}},
			nil,
		},
		{
			"valid boolean default",
			ParamSchema{{
				Name: "liked", Type: ParamTypeBoolean, Source: ParamSourcePrompt, Optional: true, Default: "false",
			}},
			nil,
		},
		{
			"default on a required param",
			ParamSchema{{Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Default: "film"}},
			ErrFormulaParamInvalid,
		},
		{
			"default outside the declared options",
			ParamSchema{{
				Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true,
				Options: opts("film", "tv"), Default: "book",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"default not matching the declared option spelling",
			ParamSchema{{
				Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true,
				Options: opts("film", "tv"), Default: "Film",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"non-numeric integer default",
			ParamSchema{{
				Name: "year", Type: ParamTypeInteger, Source: ParamSourcePrompt, Optional: true, Default: "soon",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"non-boolean boolean default",
			ParamSchema{{
				Name: "liked", Type: ParamTypeBoolean, Source: ParamSourcePrompt, Optional: true, Default: "1",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"default on a lookup param",
			ParamSchema{{
				Name: "member", Type: ParamTypeInteger, Source: ParamSourceLookup, Optional: true, Default: "1",
				Lookup: &LookupConfig{Table: "members", LabelColumn: "name", ValueColumn: "id"},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"default on a group field",
			ParamSchema{{
				Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{
					Name: "unit", Type: ParamTypeText, Source: ParamSourcePrompt, Default: "g",
				}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"valid date param",
			ParamSchema{{Name: "expires_at", Type: ParamTypeDate, Source: ParamSourcePrompt, Optional: true}},
			nil,
		},
		{
			// A real leap day: the check parses the date rather than matching its shape.
			"valid date default",
			ParamSchema{{
				Name: "due", Type: ParamTypeDate, Source: ParamSourcePrompt, Optional: true, Default: "2024-02-29",
			}},
			nil,
		},
		{
			// The same day in a common year — well-shaped, and not a day that exists.
			"date default that is not a real day",
			ParamSchema{{
				Name: "due", Type: ParamTypeDate, Source: ParamSourcePrompt, Optional: true, Default: "2026-02-29",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"date default in the wrong shape",
			ParamSchema{{
				Name: "due", Type: ParamTypeDate, Source: ParamSourcePrompt, Optional: true, Default: "12/08/2026",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"valid datetime param",
			ParamSchema{{Name: "started_at", Type: ParamTypeDateTime, Source: ParamSourcePrompt, Optional: true}},
			nil,
		},
		{
			// The browser picker's "T" form is a legal authored default; the run binds it canonicalized.
			"valid datetime default in the picker's form",
			ParamSchema{{
				Name: "started_at", Type: ParamTypeDateTime, Source: ParamSourcePrompt, Optional: true,
				Default: "2024-02-29T07:30",
			}},
			nil,
		},
		{
			"datetime default on a day that does not exist",
			ParamSchema{{
				Name: "started_at", Type: ParamTypeDateTime, Source: ParamSourcePrompt, Optional: true,
				Default: "2026-02-29 07:30:00",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"datetime default without a time",
			ParamSchema{{
				Name: "started_at", Type: ParamTypeDateTime, Source: ParamSourcePrompt, Optional: true,
				Default: "2026-08-09",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"valid time param",
			ParamSchema{{Name: "reminder_at", Type: ParamTypeTime, Source: ParamSourcePrompt, Optional: true}},
			nil,
		},
		{
			"valid time default without seconds",
			ParamSchema{{
				Name: "reminder_at", Type: ParamTypeTime, Source: ParamSourcePrompt, Optional: true, Default: "07:30",
			}},
			nil,
		},
		{
			// The 24-hour clock has no 24th hour; midnight is "00:00".
			"time default past the clock",
			ParamSchema{{
				Name: "reminder_at", Type: ParamTypeTime, Source: ParamSourcePrompt, Optional: true, Default: "24:00",
			}},
			ErrFormulaParamInvalid,
		},
		{
			"valid multiline prompt text",
			ParamSchema{{Name: "body", Type: ParamTypeText, Source: ParamSourcePrompt, Multiline: true}},
			nil,
		},
		{
			"multiline on a non-text param",
			ParamSchema{{Name: "qty", Type: ParamTypeInteger, Source: ParamSourcePrompt, Multiline: true}},
			ErrFormulaParamInvalid,
		},
		{
			"multiline on a lookup param",
			ParamSchema{{
				Name: "member", Type: ParamTypeText, Source: ParamSourceLookup, Multiline: true,
				Lookup: &LookupConfig{Table: "members", LabelColumn: "name", ValueColumn: "name"},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"empty name",
			ParamSchema{{Name: "", Type: ParamTypeText, Source: ParamSourcePrompt}},
			ErrFormulaParamInvalid,
		},
		{
			"bad name pattern",
			ParamSchema{{Name: "has space", Type: ParamTypeText, Source: ParamSourcePrompt}},
			ErrFormulaParamInvalid,
		},
		{
			"bad type",
			ParamSchema{{Name: "x", Type: "timestamp", Source: ParamSourcePrompt}},
			ErrFormulaParamInvalid,
		},
		{
			"bad source",
			ParamSchema{{Name: "x", Type: ParamTypeText, Source: ParamSource("magic")}},
			ErrFormulaParamInvalid,
		},
		{
			"duplicate names",
			ParamSchema{
				{Name: "x", Type: ParamTypeText, Source: ParamSourcePrompt},
				{Name: "x", Type: ParamTypeText, Source: ParamSourcePrompt},
			},
			ErrFormulaParamInvalid,
		},
		{
			"rowids with wrong source",
			ParamSchema{{Name: ReservedParamRowIDs, Type: ParamTypeInteger, Source: ParamSourcePrompt}},
			ErrFormulaParamInvalid,
		},
		{
			"rowid with wrong source",
			ParamSchema{{Name: ReservedParamRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionBulk}},
			ErrFormulaParamInvalid,
		},
		{
			"parent_rowid with wrong source",
			ParamSchema{{Name: ReservedParamParentRowID, Type: ParamTypeInteger, Source: ParamSourcePrompt}},
			ErrFormulaParamInvalid,
		},
		{
			"valid group with prompt and lookup fields",
			ParamSchema{{
				Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{
					{Name: "member", Type: ParamTypeInteger, Source: ParamSourceLookup,
						Lookup: &LookupConfig{Table: "members", LabelColumn: "name", ValueColumn: "id"}},
					{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt},
				},
			}},
			nil,
		},
		{
			"group with non-prompt source",
			ParamSchema{{
				Name: "shares", Type: ParamTypeGroup, Source: ParamSourceSelectionSingle,
				Fields: []FormulaParam{{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"group without fields",
			ParamSchema{{Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt}},
			ErrFormulaParamInvalid,
		},
		{
			"group with a nested group field",
			ParamSchema{{
				Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{Name: "inner", Type: ParamTypeGroup, Source: ParamSourcePrompt,
					Fields: []FormulaParam{{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt}}}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"group field with a selection source",
			ParamSchema{{
				Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{Name: "row", Type: ParamTypeInteger, Source: ParamSourceSelectionSingle}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"group with duplicate field names",
			ParamSchema{{
				Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{
					{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt},
					{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt},
				},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"scalar declaring fields",
			ParamSchema{{
				Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{Name: "x", Type: ParamTypeFloat, Source: ParamSourcePrompt}},
			}},
			ErrFormulaParamInvalid,
		},
		{
			"valid cursor + limit pagination",
			ParamSchema{
				{Name: ReservedParamCursor, Type: ParamTypeInteger, Source: ParamSourcePagination},
				{Name: ReservedParamLimit, Type: ParamTypeInteger, Source: ParamSourcePagination},
			},
			nil,
		},
		{
			"cursor with wrong source",
			ParamSchema{{Name: ReservedParamCursor, Type: ParamTypeInteger, Source: ParamSourcePrompt}},
			ErrFormulaParamInvalid,
		},
		{
			"limit with non-integer type",
			ParamSchema{{Name: ReservedParamLimit, Type: ParamTypeText, Source: ParamSourcePagination}},
			ErrFormulaParamInvalid,
		},
		{
			"pagination source on a non-reserved name",
			ParamSchema{{Name: "page", Type: ParamTypeInteger, Source: ParamSourcePagination}},
			ErrFormulaParamInvalid,
		},
		{
			"valid ordered_rowids reorder param",
			ParamSchema{{Name: ReservedParamOrderedRowIDs, Type: ParamTypeInteger, Source: ParamSourceReorder}},
			nil,
		},
		{
			"ordered_rowids with wrong source",
			ParamSchema{{Name: ReservedParamOrderedRowIDs, Type: ParamTypeInteger, Source: ParamSourcePrompt}},
			ErrFormulaParamInvalid,
		},
		{
			"ordered_rowids with non-integer type",
			ParamSchema{{Name: ReservedParamOrderedRowIDs, Type: ParamTypeText, Source: ParamSourceReorder}},
			ErrFormulaParamInvalid,
		},
		{
			"reorder source on a non-reserved name",
			ParamSchema{{Name: "order", Type: ParamTypeInteger, Source: ParamSourceReorder}},
			ErrFormulaParamInvalid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.schema.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("Validate() returned error: %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestCrossCheckParams pins the schema-vs-SQL reconciliation: the declared parameter
// names must exactly match the statement's named bind parameters (order-independent),
// anonymous "?" placeholders are rejected, and either-direction drift is an error.
func TestCrossCheckParams(t *testing.T) {
	prompt := func(name string) FormulaParam {
		return FormulaParam{Name: name, Type: ParamTypeText, Source: ParamSourcePrompt}
	}

	tests := []struct {
		name          string
		schema        ParamSchema
		sqlParamNames []string
		wantErr       bool
	}{
		{"both empty", ParamSchema{}, nil, false},
		{"single match", ParamSchema{prompt("status")}, []string{"status"}, false},
		{
			"multiple match, order-independent",
			ParamSchema{prompt("a"), prompt("b")},
			[]string{"b", "a"},
			false,
		},
		{
			"rowids bulk matches its named placeholder",
			ParamSchema{{Name: ReservedParamRowIDs, Type: ParamTypeInteger, Source: ParamSourceSelectionBulk}},
			[]string{ReservedParamRowIDs},
			false,
		},
		{
			"anonymous placeholder rejected",
			ParamSchema{prompt("status")},
			[]string{"", "status"},
			true,
		},
		{
			"sql uses an undeclared parameter",
			ParamSchema{},
			[]string{"status"},
			true,
		},
		{
			"schema declares an unused parameter",
			ParamSchema{prompt("status")},
			nil,
			true,
		},
		{
			"partial overlap is drift both ways",
			ParamSchema{prompt("a"), prompt("b")},
			[]string{"a", "c"},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CrossCheckParams(tt.schema, tt.sqlParamNames)
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaParamInvalid) {
					t.Fatalf("CrossCheckParams() error = %v, want ErrFormulaParamInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("CrossCheckParams() returned error: %v, want nil", err)
			}
		})
	}
}

// TestParamSchemaValidate_LookupRules pins the lookup-specific schema rules: a lookup-source
// parameter carries a complete config with plainly-named, non-reserved table and columns, and no
// other source carries one.
func TestParamSchemaValidate_LookupRules(t *testing.T) {
	valid := &LookupConfig{Table: "exercises", LabelColumn: "name", ValueColumn: "id"}
	lookupParam := func(l *LookupConfig) FormulaParam {
		return FormulaParam{Name: "exercise", Type: ParamTypeInteger, Source: ParamSourceLookup, Lookup: l}
	}
	tests := []struct {
		name    string
		param   FormulaParam
		wantErr bool
	}{
		{"valid lookup", lookupParam(valid), false},
		{"lookup without config", lookupParam(nil), true},
		{
			"config on a prompt param",
			FormulaParam{Name: "qty", Type: ParamTypeInteger, Source: ParamSourcePrompt, Lookup: valid},
			true,
		},
		{
			"reserved-prefix table",
			lookupParam(&LookupConfig{Table: "_formulas", LabelColumn: "name", ValueColumn: "id"}),
			true,
		},
		{
			"empty label column",
			lookupParam(&LookupConfig{Table: "exercises", LabelColumn: "", ValueColumn: "id"}),
			true,
		},
		{
			"table with punctuation",
			lookupParam(&LookupConfig{Table: "bad;name", LabelColumn: "name", ValueColumn: "id"}),
			true,
		},
		{
			"column starting with digit",
			lookupParam(&LookupConfig{Table: "exercises", LabelColumn: "name", ValueColumn: "1st"}),
			true,
		},
		{
			"allow_new on a text param",
			FormulaParam{Name: "item", Type: ParamTypeText, Source: ParamSourceLookup,
				Lookup: &LookupConfig{Table: "items", LabelColumn: "name", ValueColumn: "name", AllowNew: true}},
			false,
		},
		{
			"allow_new on a non-text param",
			lookupParam(&LookupConfig{Table: "exercises", LabelColumn: "name", ValueColumn: "id", AllowNew: true}),
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ParamSchema{tt.param}.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaParamInvalid) {
					t.Errorf("Validate() error = %v, want ErrFormulaParamInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() returned error: %v, want nil", err)
			}
		})
	}
}

// TestParamSchemaValidate_FillRules pins the lookup fill rules: an entry copies a plainly-named column of
// the lookup table into a scalar prompt- or lookup-sourced sibling; the lookup itself, undeclared names,
// groups, multi-select lookups, and group-field lookups are all rejected.
func TestParamSchemaValidate_FillRules(t *testing.T) {
	category := FormulaParam{Name: "category", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true}
	habitPick := FormulaParam{Name: "habit", Type: ParamTypeText, Source: ParamSourceLookup, Optional: true,
		Lookup: &LookupConfig{Table: "habits", LabelColumn: "name", ValueColumn: "name"}}
	group := FormulaParam{Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt, Optional: true,
		Fields: []FormulaParam{{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt}}}
	filling := func(fill map[string]string) FormulaParam {
		return FormulaParam{Name: "item", Type: ParamTypeText, Source: ParamSourceLookup,
			Lookup: &LookupConfig{Table: "items", LabelColumn: "name", ValueColumn: "name",
				AllowNew: true, Fill: fill}}
	}

	tests := []struct {
		name    string
		schema  ParamSchema
		wantErr bool
	}{
		{
			"fills a prompt sibling",
			ParamSchema{filling(map[string]string{"category": "category_name"}), category},
			false,
		},
		{"fills a lookup sibling", ParamSchema{filling(map[string]string{"habit": "habit"}), habitPick}, false},
		{"undeclared sibling", ParamSchema{filling(map[string]string{"ghost": "category_name"}), category}, true},
		{"fills itself", ParamSchema{filling(map[string]string{"item": "name"}), category}, true},
		{"group sibling", ParamSchema{filling(map[string]string{"shares": "category_name"}), group}, true},
		{"bad column identifier", ParamSchema{filling(map[string]string{"category": "bad;col"}), category}, true},
		{
			"fill on a multi-select lookup",
			ParamSchema{FormulaParam{Name: "members", Type: ParamTypeInteger, Source: ParamSourceLookup,
				Multiple: true, Lookup: &LookupConfig{Table: "members", LabelColumn: "name", ValueColumn: "id",
					Fill: map[string]string{"category": "kind"}}}, category},
			true,
		},
		{
			"fill inside a group field",
			ParamSchema{FormulaParam{Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Optional: true, Fields: []FormulaParam{{Name: "member", Type: ParamTypeInteger,
					Source: ParamSourceLookup, Lookup: &LookupConfig{Table: "members", LabelColumn: "name",
						ValueColumn: "id", Fill: map[string]string{"category": "kind"}}}}}, category},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.schema.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaParamInvalid) {
					t.Errorf("Validate() error = %v, want ErrFormulaParamInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() error = %v, want nil", err)
			}
		})
	}
}

// TestParamSchemaValidate_WindowRules pins the calendar window pair: window_start and window_end use the
// window source and one shared date or datetime type, are declared together, are never optional, and the
// window source is theirs alone.
func TestParamSchemaValidate_WindowRules(t *testing.T) {
	from := FormulaParam{Name: ReservedParamWindowStart, Type: ParamTypeDate, Source: ParamSourceWindow}
	to := FormulaParam{Name: ReservedParamWindowEnd, Type: ParamTypeDate, Source: ParamSourceWindow}
	fromInstant := FormulaParam{Name: ReservedParamWindowStart, Type: ParamTypeDateTime, Source: ParamSourceWindow}
	toInstant := FormulaParam{Name: ReservedParamWindowEnd, Type: ParamTypeDateTime, Source: ParamSourceWindow}

	tests := []struct {
		name    string
		schema  ParamSchema
		wantErr bool
	}{
		{"the pair", ParamSchema{from, to}, false},
		{"the pair as instants", ParamSchema{fromInstant, toInstant}, false},
		{"a day and an instant", ParamSchema{from, toInstant}, true},
		{"from alone", ParamSchema{from}, true},
		{"to alone", ParamSchema{to}, true},
		{"from with a prompt source", ParamSchema{
			{Name: ReservedParamWindowStart, Type: ParamTypeDate, Source: ParamSourcePrompt}, to}, true},
		{"to typed as text", ParamSchema{from,
			{Name: ReservedParamWindowEnd, Type: ParamTypeText, Source: ParamSourceWindow}}, true},
		{"an optional bound", ParamSchema{from,
			{Name: ReservedParamWindowEnd, Type: ParamTypeDate, Source: ParamSourceWindow, Optional: true}}, true},
		{"the window source on another name", ParamSchema{from, to,
			{Name: "until", Type: ParamTypeDate, Source: ParamSourceWindow}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.schema.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaParamInvalid) {
					t.Errorf("Validate() error = %v, want ErrFormulaParamInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() error = %v, want nil", err)
			}
		})
	}
}

// TestParamSchemaValidate_ScopeRules pins the lookup scope rules: an entry narrows the picker by a
// scalar prompt-sourced sibling matched against a plainly-named column of the lookup table; the
// lookup itself, undeclared names, non-prompt siblings, groups, and group-field lookups are all
// rejected, while a multi-select lookup may be scoped like any other.
func TestParamSchemaValidate_ScopeRules(t *testing.T) {
	kind := FormulaParam{Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true}
	scoped := func(scope map[string]string) FormulaParam {
		return FormulaParam{Name: "name", Type: ParamTypeText, Source: ParamSourceLookup,
			Lookup: &LookupConfig{Table: "drinks", LabelColumn: "name", ValueColumn: "name",
				AllowNew: true, Scope: scope}}
	}
	otherLookup := FormulaParam{Name: "other", Type: ParamTypeInteger, Source: ParamSourceLookup,
		Lookup: &LookupConfig{Table: "makers", LabelColumn: "name", ValueColumn: "id"}}
	group := FormulaParam{Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt, Optional: true,
		Fields: []FormulaParam{{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt}}}

	tests := []struct {
		name    string
		schema  ParamSchema
		wantErr bool
	}{
		{"valid scope", ParamSchema{scoped(map[string]string{"kind": "kind"}), kind}, false},
		{"undeclared sibling", ParamSchema{scoped(map[string]string{"ghost": "kind"}), kind}, true},
		{"scopes on itself", ParamSchema{scoped(map[string]string{"name": "name"}), kind}, true},
		{"non-prompt sibling", ParamSchema{scoped(map[string]string{"other": "kind"}), otherLookup}, true},
		{"group sibling", ParamSchema{scoped(map[string]string{"shares": "kind"}), group}, true},
		{"bad column identifier", ParamSchema{scoped(map[string]string{"kind": "bad;col"}), kind}, true},
		{
			"scope on a multi-select lookup",
			ParamSchema{FormulaParam{Name: "members", Type: ParamTypeInteger, Source: ParamSourceLookup,
				Multiple: true, Lookup: &LookupConfig{Table: "members", LabelColumn: "name", ValueColumn: "id",
					Scope: map[string]string{"kind": "kind"}}}, kind},
			false,
		},
		{
			"scope inside a group field",
			ParamSchema{FormulaParam{Name: "shares", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Optional: true, Fields: []FormulaParam{{Name: "member", Type: ParamTypeInteger,
					Source: ParamSourceLookup, Lookup: &LookupConfig{Table: "members", LabelColumn: "name",
						ValueColumn: "id", Scope: map[string]string{"kind": "kind"}}}}}, kind},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.schema.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaParamInvalid) {
					t.Errorf("Validate() error = %v, want ErrFormulaParamInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() returned error: %v, want nil", err)
			}
		})
	}
}

// TestParamSchemaValidate_ExcludeSelection pins the exclude_selection rule: the flag needs a selection-single
// param in the schema to exclude against — with one it validates, without one it is rejected rather than
// becoming a silent no-op, and a group-field lookup follows the same rule as a top-level one.
func TestParamSchemaValidate_ExcludeSelection(t *testing.T) {
	excluding := &LookupConfig{Table: "exercises", LabelColumn: "name", ValueColumn: "id", ExcludeSelection: true}
	rowid := FormulaParam{Name: ReservedParamRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionSingle}
	lookup := FormulaParam{Name: "duplicate_id", Type: ParamTypeInteger, Source: ParamSourceLookup, Lookup: excluding}

	tests := []struct {
		name    string
		schema  ParamSchema
		wantErr bool
	}{
		{"with a selection-single param", ParamSchema{rowid, lookup}, false},
		{"without a selection-single param", ParamSchema{lookup}, true},
		{
			"on a group field with a selection-single param",
			ParamSchema{rowid, {
				Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{
					Name: "member", Type: ParamTypeInteger, Source: ParamSourceLookup, Lookup: excluding,
				}},
			}},
			false,
		},
		{
			"on a group field without a selection-single param",
			ParamSchema{{
				Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt,
				Fields: []FormulaParam{{
					Name: "member", Type: ParamTypeInteger, Source: ParamSourceLookup, Lookup: excluding,
				}},
			}},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.schema.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaParamInvalid) {
					t.Errorf("Validate() error = %v, want ErrFormulaParamInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() returned error: %v, want nil", err)
			}
		})
	}
}

// TestParamSchemaValidate_WhenRules pins the visibility-condition invariants: the gated param is
// prompt/lookup-sourced (required or optional — requiredness applies only while the condition
// holds), its controller is a distinct declared prompt param without a condition of its own, the
// op is known — the ordered four only on controllers whose type orders — and the value is
// non-empty, among the controller's options for eq/ne when it has any, "true"/"false" for a
// boolean controller, and type-parseable for ordered comparisons. Group fields take no condition
// at all.
func TestParamSchemaValidate_WhenRules(t *testing.T) {
	kind := FormulaParam{Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt,
		Optional: true, Options: opts("gym", "run"), Default: "gym"}
	flag := FormulaParam{Name: "flag", Type: ParamTypeBoolean, Source: ParamSourcePrompt, Optional: true}
	amount := FormulaParam{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt, Optional: true}
	gated := func(when *ParamWhen) FormulaParam {
		return FormulaParam{Name: "target", Type: ParamTypeFloat, Source: ParamSourcePrompt,
			Optional: true, When: when}
	}

	tests := []struct {
		name    string
		schema  ParamSchema
		wantErr bool
	}{
		{"gating on an options controller", ParamSchema{kind, gated(&ParamWhen{Param: "kind", Value: "run"})}, false},
		{"gating on a boolean controller", ParamSchema{flag, gated(&ParamWhen{Param: "flag", Value: "true"})}, false},
		{"explicit eq op", ParamSchema{kind, gated(&ParamWhen{Param: "kind", Op: "eq", Value: "run"})}, false},
		{"ne on an options controller", ParamSchema{kind, gated(&ParamWhen{Param: "kind", Op: "ne", Value: "gym"})},
			false},
		{"ordered op on a numeric controller",
			ParamSchema{amount, gated(&ParamWhen{Param: "amount", Op: "gt", Value: "100"})}, false},
		{"unknown op", ParamSchema{kind, gated(&ParamWhen{Param: "kind", Op: "like", Value: "run"})}, true},
		{"ordered op on a text controller",
			ParamSchema{kind, gated(&ParamWhen{Param: "kind", Op: "gt", Value: "gym"})}, true},
		{"ordered op on a boolean controller",
			ParamSchema{flag, gated(&ParamWhen{Param: "flag", Op: "lt", Value: "true"})}, true},
		{"ordered value not parsing as the controller's type",
			ParamSchema{amount, gated(&ParamWhen{Param: "amount", Op: "gte", Value: "fast"})}, true},
		{"value outside the controller's options",
			ParamSchema{kind, gated(&ParamWhen{Param: "kind", Value: "swim"})}, true},
		{"non-boolean value on a boolean controller",
			ParamSchema{flag, gated(&ParamWhen{Param: "flag", Value: "yes"})}, true},
		{"undeclared controller", ParamSchema{gated(&ParamWhen{Param: "kind", Value: "run"})}, true},
		{"empty value", ParamSchema{kind, gated(&ParamWhen{Param: "kind"})}, true},
		{"self-reference", ParamSchema{{Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt,
			Optional: true, When: &ParamWhen{Param: "kind", Value: "run"}}}, true},
		{"required gated param", ParamSchema{kind, {Name: "target", Type: ParamTypeFloat,
			Source: ParamSourcePrompt, When: &ParamWhen{Param: "kind", Value: "run"}}}, false},
		{"required gated group", ParamSchema{kind, {
			Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt,
			When:   &ParamWhen{Param: "kind", Value: "run"},
			Fields: []FormulaParam{{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt}},
		}}, false},
		{"chained condition on the controller", ParamSchema{
			flag,
			{Name: "kind", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true,
				Options: opts("gym", "run"), When: &ParamWhen{Param: "flag", Value: "true"}},
			gated(&ParamWhen{Param: "kind", Value: "run"}),
		}, true},
		{"selection-sourced controller", ParamSchema{
			{Name: ReservedParamRowID, Type: ParamTypeInteger, Source: ParamSourceSelectionSingle},
			gated(&ParamWhen{Param: ReservedParamRowID, Value: "1"}),
		}, true},
		{"condition on a group field", ParamSchema{kind, {
			Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt, Optional: true,
			Fields: []FormulaParam{{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt,
				When: &ParamWhen{Param: "kind", Value: "run"}}},
		}}, true},
		{"condition on a whole group", ParamSchema{kind, {
			Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt, Optional: true,
			When:   &ParamWhen{Param: "kind", Value: "run"},
			Fields: []FormulaParam{{Name: "amount", Type: ParamTypeFloat, Source: ParamSourcePrompt}},
		}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.schema.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaParamInvalid) {
					t.Errorf("Validate() error = %v, want ErrFormulaParamInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() returned error: %v, want nil", err)
			}
		})
	}
}

func TestMatchOption(t *testing.T) {
	options := opts("film", "tv", "book")

	tests := []struct {
		name          string
		options       []FormulaOption
		raw           string
		wantCanonical string
		wantOK        bool
	}{
		{"exact match", options, "film", "film", true},
		{"last entry", options, "book", "book", true},
		{"differing case", options, "Film", "film", true},
		{"upper case", options, "TV", "tv", true},
		{"surrounding whitespace", options, "  book  ", "book", true},
		{"case and whitespace", options, " Film ", "film", true},
		{"binds the declared spelling, not the caller's", opts("Sci-Fi"), "sci-fi", "Sci-Fi", true},
		{"matches the value, never the label", []FormulaOption{{Value: "9", Label: "September"}}, "September", "",
			false},
		{"no match", options, "movie", "", false},
		{"empty value", options, "", "", false},
		{"whitespace only", options, "   ", "", false},
		{"substring is not a match", options, "fil", "", false},
		{"no options declared", nil, "film", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonical, ok := MatchOption(tt.options, tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("MatchOption(%v, %q) ok = %v, want %v", tt.options, tt.raw, ok, tt.wantOK)
			}
			if canonical != tt.wantCanonical {
				t.Errorf("MatchOption(%v, %q) = %q, want %q", tt.options, tt.raw, canonical, tt.wantCanonical)
			}
		})
	}
}

// TestCanonicalDateTime pins the normalization contract, not just acceptance: every accepted shape
// comes out as the one space-separated, seconds-carrying form, since "2026-08-09T09:00" sorts AFTER
// "2026-08-09 14:30" as plain text and a column mixing the shapes mis-orders same-day values.
func TestCanonicalDateTime(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantCanonical string
		wantOK        bool
	}{
		{"picker form gains seconds and loses the T", "2026-08-09T14:30", "2026-08-09 14:30:00", true},
		{"canonical form passes through", "2026-08-09 14:30:00", "2026-08-09 14:30:00", true},
		{"space form without seconds", "2026-08-09 14:30", "2026-08-09 14:30:00", true},
		{"T form with seconds", "2026-08-09T14:30:05", "2026-08-09 14:30:05", true},
		{"fractional seconds truncate", "2026-08-09T14:30:05.500", "2026-08-09 14:30:05", true},
		{"unpadded hour is padded", "2026-08-09T9:05", "2026-08-09 09:05:00", true},
		{"a date alone is not a datetime", "2026-08-09", "", false},
		{"a time alone is not a datetime", "14:30", "", false},
		{"leap day in a common year", "2026-02-29T10:00", "", false},
		{"unpadded date", "2026-8-9T10:00", "", false},
		{"61st second", "2026-08-09T14:30:61", "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonical, ok := CanonicalDateTime(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("CanonicalDateTime(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if canonical != tt.wantCanonical {
				t.Errorf("CanonicalDateTime(%q) = %q, want %q", tt.raw, canonical, tt.wantCanonical)
			}
		})
	}
}

// TestCanonicalTime pins the same contract for bare clock times: "14:30" and "14:30:00" name the same
// instant but compare unequal as text, so everything accepted comes out with seconds.
func TestCanonicalTime(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		wantCanonical string
		wantOK        bool
	}{
		{"picker form gains seconds", "14:30", "14:30:00", true},
		{"canonical form passes through", "14:30:00", "14:30:00", true},
		{"fractional seconds truncate", "14:30:00.123", "14:30:00", true},
		{"unpadded hour is padded", "9:05", "09:05:00", true},
		{"midnight is 00:00", "24:00", "", false},
		{"61st minute", "14:61", "", false},
		{"12-hour clock", "9pm", "", false},
		{"a datetime is not a time", "2026-08-09 14:30", "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonical, ok := CanonicalTime(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("CanonicalTime(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if canonical != tt.wantCanonical {
				t.Errorf("CanonicalTime(%q) = %q, want %q", tt.raw, canonical, tt.wantCanonical)
			}
		})
	}
}

// TestParamSchemaValidate_MultipleRules pins the multi-select invariants: the flag sits on a lookup
// parameter or an options parameter (required or optional, when-gated or not), never combines with
// allow_new, never sits on a group field, and its default is either the literal "all" or a declared
// subset in Defaults whose values each pass the scalar checks — both stay exclusive to multi-selects.
func TestParamSchemaValidate_MultipleRules(t *testing.T) {
	members := &LookupConfig{Table: "members", LabelColumn: "name", ValueColumn: "id"}
	multi := func(mutate func(*FormulaParam)) FormulaParam {
		p := FormulaParam{Name: "share_among", Type: ParamTypeInteger, Source: ParamSourceLookup,
			Lookup: members, Multiple: true}
		if mutate != nil {
			mutate(&p)
		}
		return p
	}
	flag := FormulaParam{Name: "flag", Type: ParamTypeBoolean, Source: ParamSourcePrompt, Optional: true}
	habits := func(mutate func(*FormulaParam)) FormulaParam {
		p := FormulaParam{Name: "habit", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true,
			Multiple: true, Options: []FormulaOption{{Value: "staple", Label: "staple"},
				{Value: "occasional", Label: "occasional"}, {Value: "one-off", Label: "one-off"}}}
		if mutate != nil {
			mutate(&p)
		}
		return p
	}

	tests := []struct {
		name    string
		schema  ParamSchema
		wantErr bool
	}{
		{"required multi lookup", ParamSchema{multi(nil)}, false},
		{"required multi with default all", ParamSchema{multi(func(p *FormulaParam) { p.Default = "all" })}, false},
		{"optional multi with default all", ParamSchema{multi(func(p *FormulaParam) {
			p.Optional = true
			p.Default = "all"
		})}, false},
		{"when-gated multi", ParamSchema{flag, multi(func(p *FormulaParam) {
			p.When = &ParamWhen{Param: "flag", Value: "false"}
		})}, false},
		{"multiple on a prompt param without options", ParamSchema{{
			Name: "qty", Type: ParamTypeInteger, Source: ParamSourcePrompt, Multiple: true,
		}}, true},
		{"multiple on an options param", ParamSchema{habits(nil)}, false},
		{"options multi with default all", ParamSchema{habits(func(p *FormulaParam) { p.Default = "all" })}, false},
		{"options multi with a subset", ParamSchema{habits(func(p *FormulaParam) {
			p.Defaults = []string{"staple", "occasional"}
		})}, false},
		{"lookup multi with an integer subset", ParamSchema{multi(func(p *FormulaParam) {
			p.Defaults = []string{"1", "3"}
		})}, false},
		{"subset off the vocabulary", ParamSchema{habits(func(p *FormulaParam) {
			p.Defaults = []string{"weekly"}
		})}, true},
		{"subset mistyped", ParamSchema{multi(func(p *FormulaParam) { p.Defaults = []string{"one"} })}, true},
		{"subset with a duplicate", ParamSchema{habits(func(p *FormulaParam) {
			p.Defaults = []string{"staple", "staple"}
		})}, true},
		{"subset beside an all default", ParamSchema{habits(func(p *FormulaParam) {
			p.Default = "all"
			p.Defaults = []string{"staple"}
		})}, true},
		{"subset on a single-pick options param", ParamSchema{{
			Name: "habit", Type: ParamTypeText, Source: ParamSourcePrompt, Optional: true,
			Options: []FormulaOption{{Value: "staple", Label: "staple"}}, Defaults: []string{"staple"},
		}}, true},
		{"multi default that is neither all nor empty", ParamSchema{habits(func(p *FormulaParam) {
			p.Default = "staple"
		})}, true},
		{"multiple with allow_new", ParamSchema{{
			Name: "items", Type: ParamTypeText, Source: ParamSourceLookup, Multiple: true,
			Lookup: &LookupConfig{Table: "items", LabelColumn: "name", ValueColumn: "name", AllowNew: true},
		}}, true},
		{"multi default other than all", ParamSchema{multi(func(p *FormulaParam) { p.Default = "3" })}, true},
		{"default all on a single-pick lookup", ParamSchema{{
			Name: "member", Type: ParamTypeInteger, Source: ParamSourceLookup, Lookup: members, Default: "all",
		}}, true},
		{"multiple on a group field", ParamSchema{{
			Name: "lines", Type: ParamTypeGroup, Source: ParamSourcePrompt,
			Fields: []FormulaParam{{
				Name: "member", Type: ParamTypeInteger, Source: ParamSourceLookup, Lookup: members, Multiple: true,
			}},
		}}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.schema.Validate()
			if tt.wantErr {
				if !errors.Is(err, ErrFormulaParamInvalid) {
					t.Errorf("Validate() error = %v, want ErrFormulaParamInvalid", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() returned error: %v, want nil", err)
			}
		})
	}
}
