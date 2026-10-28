package service

import (
	"errors"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// TestCoerceParamValueFromString checks the conversion of a string parameter value to its declared type: refused
// values are a parameter value error, an unknown type a parameter schema error.
func TestCoerceParamValueFromString(t *testing.T) {
	tests := []struct {
		name    string
		param   domain.FormulaParam
		raw     string
		want    any
		wantErr error
	}{
		{"text passes through", domain.FormulaParam{Name: "q", Type: domain.ParamTypeText},
			`a,b "c"`, `a,b "c"`, nil},
		{"integer", domain.FormulaParam{Name: "n", Type: domain.ParamTypeInteger}, "42", float64(42), nil},
		{"negative integer", domain.FormulaParam{Name: "n", Type: domain.ParamTypeInteger},
			"-7", float64(-7), nil},
		{"integer rejects decimal", domain.FormulaParam{Name: "n", Type: domain.ParamTypeInteger},
			"4.2", nil, domain.ErrFormulaParamValue},
		{"integer rejects text", domain.FormulaParam{Name: "n", Type: domain.ParamTypeInteger},
			"abc", nil, domain.ErrFormulaParamValue},
		{"integer rejects a value float64 cannot hold exactly",
			domain.FormulaParam{Name: "n", Type: domain.ParamTypeInteger}, "9007199254740993", nil,
			domain.ErrFormulaParamValue},
		{"float", domain.FormulaParam{Name: "f", Type: domain.ParamTypeFloat}, "3.5", 3.5, nil},
		{"float exponent", domain.FormulaParam{Name: "f", Type: domain.ParamTypeFloat}, "1e3", 1000.0, nil},
		{"float rejects text", domain.FormulaParam{Name: "f", Type: domain.ParamTypeFloat},
			"abc", nil, domain.ErrFormulaParamValue},
		{"boolean true", domain.FormulaParam{Name: "b", Type: domain.ParamTypeBoolean}, "true", true, nil},
		{"boolean false", domain.FormulaParam{Name: "b", Type: domain.ParamTypeBoolean}, "false", false, nil},
		{"boolean rejects 1", domain.FormulaParam{Name: "b", Type: domain.ParamTypeBoolean},
			"1", nil, domain.ErrFormulaParamValue},
		{"boolean rejects TRUE", domain.FormulaParam{Name: "b", Type: domain.ParamTypeBoolean},
			"TRUE", nil, domain.ErrFormulaParamValue},
		{"group rejects a value that is not a JSON array", domain.FormulaParam{Name: "g", Type: domain.ParamTypeGroup},
			"bread", nil, domain.ErrFormulaParamValue},
		{"unknown type", domain.FormulaParam{Name: "x", Type: "blob"}, "x", nil, domain.ErrFormulaParamInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := coerceParamValueFromString(tt.param, tt.raw)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("coerceParamValueFromString: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolveFormulaParams_ExportMode(t *testing.T) {
	schema := domain.ParamSchema{
		{Name: domain.ReservedParamCursor, Type: domain.ParamTypeInteger, Source: domain.ParamSourcePagination},
		{Name: domain.ReservedParamLimit, Type: domain.ParamTypeInteger, Source: domain.ParamSourcePagination},
		{Name: "status", Type: domain.ParamTypeText, Source: domain.ParamSourcePrompt},
	}
	input := FormulaRunInput{Params: map[string]any{"status": "open"}}

	t.Run("export mode binds the full sweep", func(t *testing.T) {
		scalars, rowIDs, plan, err := resolveFormulaParams(schema, input, false, 1000)
		if err != nil {
			t.Fatalf("resolveFormulaParams: %v", err)
		}
		if plan.enabled {
			t.Error("export mode must not produce a pagination plan")
		}
		if len(rowIDs) != 0 {
			t.Errorf("rowIDs = %v, want none", rowIDs)
		}
		if got := scalars[domain.ReservedParamCursor].Value; got != int64(0) {
			t.Errorf("cursor bind = %v, want 0", got)
		}
		if got := scalars[domain.ReservedParamLimit].Value; got != int64(1001) {
			t.Errorf("limit bind = %v, want 1001 (ceiling + 1)", got)
		}
		if got := scalars["status"].Value; got != "open" {
			t.Errorf("status bind = %v, want \"open\"", got)
		}
	})

	t.Run("zero ceiling keeps the pagination behavior unchanged", func(t *testing.T) {
		scalars, _, plan, err := resolveFormulaParams(schema, input, true, 0)
		if err != nil {
			t.Fatalf("resolveFormulaParams: %v", err)
		}
		if !plan.enabled || plan.limit != domain.DefaultPageSize {
			t.Errorf("plan = %+v, want enabled with limit %d", plan, domain.DefaultPageSize)
		}
		if got := scalars[domain.ReservedParamCursor].Value; got != int64(0) {
			t.Errorf("cursor bind = %v, want 0", got)
		}
		if got := scalars[domain.ReservedParamLimit].Value; got != int64(domain.DefaultPageSize+1) {
			t.Errorf("limit bind = %v, want %d (page size + over-fetch)", got, domain.DefaultPageSize+1)
		}
	})
}
