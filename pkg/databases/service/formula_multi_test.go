package service

import (
	"errors"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// multiParam is the multi-select lookup parameter under test; mutate tweaks one aspect per case.
func multiParam(mutate func(*domain.FormulaParam)) domain.FormulaParam {
	p := domain.FormulaParam{
		Name: "share_among", Type: domain.ParamTypeInteger, Source: domain.ParamSourceLookup, Multiple: true,
		Lookup: &domain.LookupConfig{Table: "members", LabelColumn: "name", ValueColumn: "id"},
	}
	if mutate != nil {
		mutate(&p)
	}
	return p
}

// TestCoerceMultiValue pins the multi-select bind shape: a supplied array binds as one JSON TEXT
// value in caller order with each element coerced to the param's type, an omitted or empty optional
// (or condition-false) parameter binds '[]', and a required one demands at least one element.
func TestCoerceMultiValue(t *testing.T) {
	tests := []struct {
		name         string
		param        domain.FormulaParam
		raw          any
		present      bool
		conditionMet bool
		want         string
		wantErr      bool
	}{
		{"integers bind in caller order", multiParam(nil), []any{3.0, 1.0, 2.0}, true, true, "[3,1,2]", false},
		{
			"text elements bind quoted",
			multiParam(func(p *domain.FormulaParam) { p.Type = domain.ParamTypeText }),
			[]any{"b", "a"}, true, true, `["b","a"]`, false,
		},
		{
			"omitted optional binds empty array",
			multiParam(func(p *domain.FormulaParam) { p.Optional = true }), nil, false, true, "[]", false,
		},
		{"omitted while condition false binds empty array", multiParam(nil), nil, false, false, "[]", false},
		{
			"explicit empty optional binds empty array",
			multiParam(func(p *domain.FormulaParam) { p.Optional = true }), []any{}, true, true, "[]", false,
		},
		{"omitted required errors", multiParam(nil), nil, false, true, "", true},
		{"null required errors", multiParam(nil), nil, true, true, "", true},
		{"empty required errors", multiParam(nil), []any{}, true, true, "", true},
		{"non-array errors", multiParam(nil), 3.0, true, true, "", true},
		{"mistyped element errors", multiParam(nil), []any{3.0, "x"}, true, true, "", true},
		{"fractional element errors", multiParam(nil), []any{1.5}, true, true, "", true},
		// The element coercion is what keeps uncomparable values (reachable via nested JSON) away from
		// the interface-keyed duplicate check — these pin the clean rejection, so a regression would
		// surface as a failed assertion rather than a map-key panic.
		{"nested array element errors", multiParam(nil), []any{[]any{1.0}, 2.0}, true, true, "", true},
		{
			"object element errors",
			multiParam(func(p *domain.FormulaParam) { p.Type = domain.ParamTypeText }),
			[]any{map[string]any{"name": "Alice"}}, true, true, "", true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := coerceMultiValue(tt.param, tt.raw, tt.present, tt.conditionMet)
			if tt.wantErr {
				if !errors.Is(err, domain.ErrFormulaParamValue) {
					t.Errorf("coerceMultiValue() error = %v, want ErrFormulaParamValue", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("coerceMultiValue() returned error: %v, want nil", err)
			}
			if got := val.Value; got != tt.want {
				t.Errorf("coerceMultiValue() bound %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCoerceMultiValue_DuplicateRejected pins the one-entry-per-value rule: a repeated pick would
// hand a member two shares, so it is rejected — whatever the element type — naming the value.
func TestCoerceMultiValue_DuplicateRejected(t *testing.T) {
	tests := []struct {
		name  string
		param domain.FormulaParam
		raw   []any
	}{
		{"duplicate integer", multiParam(nil), []any{3.0, 1.0, 3.0}},
		{
			"duplicate text",
			multiParam(func(p *domain.FormulaParam) { p.Type = domain.ParamTypeText }),
			[]any{"a", "b", "a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := coerceMultiValue(tt.param, tt.raw, true, true)
			if !errors.Is(err, domain.ErrFormulaParamValue) {
				t.Errorf("coerceMultiValue(%v) error = %v, want ErrFormulaParamValue", tt.raw, err)
			}
		})
	}
}
