package mcp

import (
	"reflect"
	"testing"
)

// TestBuildFormulaRunInput checks that the raw run_formula arguments map is mapped onto a
// service.FormulaRunInput: the "params" object passes through as Params; other arguments are ignored.
func TestBuildFormulaRunInput(t *testing.T) {
	tests := []struct {
		name       string
		args       map[string]any
		wantParams map[string]any
	}{
		{
			name: "scalars and a rowids array pass through",
			args: map[string]any{"params": map[string]any{
				"status": "open",
				"rowids": []any{float64(3), float64(7)},
			}},
			wantParams: map[string]any{"status": "open", "rowids": []any{float64(3), float64(7)}},
		},
		{
			name:       "no params",
			args:       map[string]any{"bdb_id": "abc", "ref": "some-formula"},
			wantParams: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildFormulaRunInput(tt.args)
			if !reflect.DeepEqual(got.Params, tt.wantParams) {
				t.Errorf("Params = %#v, want %#v", got.Params, tt.wantParams)
			}
		})
	}
}
