package service

import (
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// whenSchema builds the byName index paramConditionMet reads controllers from.
func whenSchema(params ...domain.FormulaParam) map[string]domain.FormulaParam {
	byName := make(map[string]domain.FormulaParam, len(params))
	for _, p := range params {
		byName[p.Name] = p
	}
	return byName
}

// gatedOn declares a float prompt param gated by the given condition.
func gatedOn(when domain.ParamWhen) domain.FormulaParam {
	return domain.FormulaParam{
		Name: "target", Type: domain.ParamTypeFloat, Source: domain.ParamSourcePrompt, Optional: true, When: &when,
	}
}

// TestParamConditionMet_EqNe pins the equality face of the server-side condition evaluation against
// the app form's semantics: the controller's effective value is the supplied one, else its declared
// default, else empty; a boolean controller normalizes anything but "true" to "false"; eq and ne
// compare exact strings; and JSON shapes fold to their form spelling (bool to "true"/"false", a
// number to its literal digits, a datetime's "T" separator to the authored space form).
func TestParamConditionMet_EqNe(t *testing.T) {
	kind := domain.FormulaParam{
		Name: "kind", Type: domain.ParamTypeText, Source: domain.ParamSourcePrompt, Optional: true,
	}
	kindDefaulted := kind
	kindDefaulted.Default = "run"
	flag := domain.FormulaParam{
		Name: "flag", Type: domain.ParamTypeBoolean, Source: domain.ParamSourcePrompt, Optional: true,
	}
	count := domain.FormulaParam{
		Name: "count", Type: domain.ParamTypeInteger, Source: domain.ParamSourcePrompt, Optional: true,
	}
	at := domain.FormulaParam{
		Name: "at", Type: domain.ParamTypeDateTime, Source: domain.ParamSourcePrompt, Optional: true,
	}

	tests := []struct {
		name       string
		controller domain.FormulaParam
		when       domain.ParamWhen
		params     map[string]any
		want       bool
	}{
		{"eq matches the supplied value", kind,
			domain.ParamWhen{Param: "kind", Value: "run"}, map[string]any{"kind": "run"}, true},
		{"eq misses another value", kind,
			domain.ParamWhen{Param: "kind", Value: "run"}, map[string]any{"kind": "gym"}, false},
		{"eq against an omitted controller reads empty", kind,
			domain.ParamWhen{Param: "kind", Value: "run"}, map[string]any{}, false},
		{"eq against an explicitly null controller reads empty", kind,
			domain.ParamWhen{Param: "kind", Value: "run"}, map[string]any{"kind": nil}, false},
		{"an omitted controller falls back to its default", kindDefaulted,
			domain.ParamWhen{Param: "kind", Value: "run"}, map[string]any{}, true},
		{"a supplied value beats the default", kindDefaulted,
			domain.ParamWhen{Param: "kind", Value: "run"}, map[string]any{"kind": "gym"}, false},
		{"ne holds on a different value", kind,
			domain.ParamWhen{Param: "kind", Op: "ne", Value: "run"}, map[string]any{"kind": "gym"}, true},
		{"ne holds on an omitted controller", kind,
			domain.ParamWhen{Param: "kind", Op: "ne", Value: "run"}, map[string]any{}, true},
		{"boolean true matches", flag,
			domain.ParamWhen{Param: "flag", Value: "true"}, map[string]any{"flag": true}, true},
		{"boolean false matches false", flag,
			domain.ParamWhen{Param: "flag", Value: "false"}, map[string]any{"flag": false}, true},
		{"an omitted boolean normalizes to false", flag,
			domain.ParamWhen{Param: "flag", Value: "false"}, map[string]any{}, true},
		{"an omitted boolean never reads true", flag,
			domain.ParamWhen{Param: "flag", Value: "true"}, map[string]any{}, false},
		{"a numeric controller folds to its digits", count,
			domain.ParamWhen{Param: "count", Value: "3"}, map[string]any{"count": 3.0}, true},
		{"a datetime folds its T separator", at,
			domain.ParamWhen{Param: "at", Value: "2026-08-19 09:00"}, map[string]any{"at": "2026-08-19T09:00"}, true},
		{"a mistyped controller value reads empty", kind,
			domain.ParamWhen{Param: "kind", Value: "run"}, map[string]any{"kind": []any{"run"}}, false},
		{"a condition on an undeclared controller always holds", kind,
			domain.ParamWhen{Param: "gone", Value: "run"}, map[string]any{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gated := gatedOn(tt.when)
			byName := whenSchema(tt.controller, gated)
			if got := paramConditionMet(gated, byName, tt.params); got != tt.want {
				t.Errorf("paramConditionMet(%+v, params %v) = %v, want %v", tt.when, tt.params, got, tt.want)
			}
		})
	}

	unconditional := domain.FormulaParam{Name: "plain", Type: domain.ParamTypeText, Source: domain.ParamSourcePrompt}
	if !paramConditionMet(unconditional, whenSchema(unconditional), map[string]any{}) {
		t.Error("a parameter without a condition must always be met")
	}
}

// TestParamConditionMet_OrderedOps pins the ordered face: lt/lte/gt/gte compare the controller's
// effective value numerically for integer and float controllers and as canonical strings for date,
// datetime and time ones, and never hold while the controller is empty, whitespace, or unparseable.
func TestParamConditionMet_OrderedOps(t *testing.T) {
	amount := domain.FormulaParam{
		Name: "amount", Type: domain.ParamTypeFloat, Source: domain.ParamSourcePrompt, Optional: true,
	}
	day := domain.FormulaParam{
		Name: "day", Type: domain.ParamTypeDate, Source: domain.ParamSourcePrompt, Optional: true,
	}

	tests := []struct {
		name       string
		controller domain.FormulaParam
		when       domain.ParamWhen
		params     map[string]any
		want       bool
	}{
		{"gt holds above the value", amount,
			domain.ParamWhen{Param: "amount", Op: "gt", Value: "100"}, map[string]any{"amount": 150.0}, true},
		{"gt fails at the value", amount,
			domain.ParamWhen{Param: "amount", Op: "gt", Value: "100"}, map[string]any{"amount": 100.0}, false},
		{"gte holds at the value", amount,
			domain.ParamWhen{Param: "amount", Op: "gte", Value: "100"}, map[string]any{"amount": 100.0}, true},
		{"lt holds below the value", amount,
			domain.ParamWhen{Param: "amount", Op: "lt", Value: "100"}, map[string]any{"amount": 99.5}, true},
		{"lt fails above the value", amount,
			domain.ParamWhen{Param: "amount", Op: "lt", Value: "100"}, map[string]any{"amount": 150.0}, false},
		{"lte holds at the value", amount,
			domain.ParamWhen{Param: "amount", Op: "lte", Value: "100"}, map[string]any{"amount": 100.0}, true},
		{"lte fails above the value", amount,
			domain.ParamWhen{Param: "amount", Op: "lte", Value: "100"}, map[string]any{"amount": 100.01}, false},
		{"an omitted controller never orders", amount,
			domain.ParamWhen{Param: "amount", Op: "gt", Value: "100"}, map[string]any{}, false},
		{"an unparseable controller never orders", amount,
			domain.ParamWhen{Param: "amount", Op: "gt", Value: "100"}, map[string]any{"amount": "lots"}, false},
		{"dates order as canonical strings", day,
			domain.ParamWhen{Param: "day", Op: "gte", Value: "2026-06-01"}, map[string]any{"day": "2026-08-19"}, true},
		{"dates below the bound fail", day,
			domain.ParamWhen{Param: "day", Op: "gte", Value: "2026-06-01"}, map[string]any{"day": "2026-05-31"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gated := gatedOn(tt.when)
			byName := whenSchema(tt.controller, gated)
			if got := paramConditionMet(gated, byName, tt.params); got != tt.want {
				t.Errorf("paramConditionMet(%+v, params %v) = %v, want %v", tt.when, tt.params, got, tt.want)
			}
		})
	}
}
