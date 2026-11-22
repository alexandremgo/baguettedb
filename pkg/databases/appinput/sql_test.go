package appinput

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// TestSQLParams_BindsAsJSONTypes checks the value each JSON type binds as: text for a string, an integer for a whole
// number and for a boolean, a real for another number, NULL for null, and JSON text for an array or an object.
func TestSQLParams_BindsAsJSONTypes(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  any
		want domain.TypedValue
	}{
		{"string", "It's a \"pain de campagne\"", domain.TextValue("It's a \"pain de campagne\"")},
		{"empty string", "", domain.TextValue("")},
		{"numeric string stays text", "0042", domain.TextValue("0042")},
		{"whole number", float64(3), domain.IntegerValue(3)},
		{"negative whole number", float64(-12), domain.IntegerValue(-12)},
		{"decimal", 1.8, domain.RealValue(1.8)},
		{"whole number beyond 2^53", 1e300, domain.RealValue(1e300)},
		{"true", true, domain.IntegerValue(1)},
		{"false", false, domain.IntegerValue(0)},
		{"null", nil, domain.NullValue()},
		{"exact integer", json.Number("9007199254740993"), domain.IntegerValue(9007199254740993)},
		{"exact decimal", json.Number("2.5"), domain.RealValue(2.5)},
		{"array", []any{"rye", float64(2)}, domain.TextValue(`["rye",2]`)},
		{"object", map[string]any{"tag": "<b>R&D</b>"}, domain.TextValue(`{"tag":"<b>R&D</b>"}`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SQLParams(map[string]any{"value": tt.raw})
			if err != nil {
				t.Fatalf("SQLParams: %v", err)
			}
			if !reflect.DeepEqual(got["value"], tt.want) {
				t.Errorf("value = %#v, want %#v", got["value"], tt.want)
			}
		})
	}
}

// TestSQLParams_RefusesBadNames checks that an empty name and a name written with its sigil are refused, the second
// with the name to write instead, and that no params give an empty set.
func TestSQLParams_RefusesBadNames(t *testing.T) {
	if _, err := SQLParams(map[string]any{"": 1.0}); err == nil {
		t.Error("an empty name was accepted")
	}
	_, err := SQLParams(map[string]any{":loaf": 1.0})
	if err == nil || !strings.Contains(err.Error(), `as "loaf"`) {
		t.Errorf("error for a name with its sigil = %v, want it to give the name to write", err)
	}
	if got, err := SQLParams(nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("SQLParams(nil) = %v, %v, want an empty set", got, err)
	}
}
