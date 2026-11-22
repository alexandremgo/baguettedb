package domain

import (
	"errors"
	"reflect"
	"testing"
)

// TestBindNamedParameters checks that named values bind in each statement's placeholder order, that a name binds in
// every statement using it, that SQL without placeholders binds nothing, and that a placeholder without a value or
// without a name is refused.
func TestBindNamedParameters(t *testing.T) {
	crumb, loaf := TextValue("open crumb"), IntegerValue(3)

	t.Run("binds each statement in its own order", func(t *testing.T) {
		stmts := []ClassifiedStatement{
			{ParamNames: []string{"note", "loaf"}},
			{},
			{ParamNames: []string{"loaf"}},
		}
		got, err := BindNamedParameters(stmts, map[string]TypedValue{"loaf": loaf, "note": crumb})
		if err != nil {
			t.Fatalf("BindNamedParameters: %v", err)
		}
		want := [][]TypedValue{{crumb, loaf}, {}, {loaf}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("args = %v, want %v", got, want)
		}
	})

	t.Run("binds nothing without placeholders", func(t *testing.T) {
		got, err := BindNamedParameters([]ClassifiedStatement{{}, {}}, map[string]TypedValue{})
		if err != nil || got != nil {
			t.Errorf("BindNamedParameters = %v, %v, want nil, nil", got, err)
		}
	})

	t.Run("names the placeholders without a value once each", func(t *testing.T) {
		stmts := []ClassifiedStatement{
			{ParamNames: []string{"note", "loaf"}},
			{ParamNames: []string{"oven", "note"}},
		}
		_, err := BindNamedParameters(stmts, map[string]TypedValue{"loaf": loaf})
		missing, ok := errors.AsType[*MissingParametersError](err)
		if !ok {
			t.Fatalf("error = %v, want a *MissingParametersError", err)
		}
		if want := []string{"note", "oven"}; !reflect.DeepEqual(missing.Names, want) {
			t.Errorf("missing names = %v, want %v", missing.Names, want)
		}
	})

	t.Run("refuses a placeholder without a name", func(t *testing.T) {
		stmts := []ClassifiedStatement{{ParamNames: []string{"loaf", ""}}}
		_, err := BindNamedParameters(stmts, map[string]TypedValue{"loaf": loaf})
		if !errors.Is(err, ErrUnnamedParameter) {
			t.Errorf("error = %v, want ErrUnnamedParameter", err)
		}
	})
}

// TestBindNamedParameters_RefusesUnusedValues checks that values no statement uses are refused, named in sorted order
// whatever order the map yields them in.
func TestBindNamedParameters_RefusesUnusedValues(t *testing.T) {
	stmts := []ClassifiedStatement{{ParamNames: []string{"loaf"}}, {ParamNames: []string{"note"}}}
	values := map[string]TypedValue{
		"loaf": IntegerValue(3), "note": TextValue("open crumb"),
		"oven": IntegerValue(240), "flour": TextValue("T65"), "salt": RealValue(1.8),
	}
	_, err := BindNamedParameters(stmts, values)
	unused, ok := errors.AsType[*UnusedParametersError](err)
	if !ok {
		t.Fatalf("error = %v, want an *UnusedParametersError", err)
	}
	if want := []string{"flour", "oven", "salt"}; !reflect.DeepEqual(unused.Names, want) {
		t.Errorf("unused names = %v, want %v", unused.Names, want)
	}
	if want := "values given for parameters the SQL does not have (flour, oven, salt)"; err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}
