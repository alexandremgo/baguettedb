package cli

import (
	"maps"
	"testing"
)

// TestParamFlag_Set checks that repeated --param flags fill the map, the value keeping everything after the first
// "=", and that a parameter without "=" or without a name is refused.
func TestParamFlag_Set(t *testing.T) {
	params := paramFlag{}
	for _, s := range []string{"a=1", "b=x=y"} {
		if err := params.Set(s); err != nil {
			t.Fatalf("Set(%q) = %v", s, err)
		}
	}
	if want := (paramFlag{"a": "1", "b": "x=y"}); !maps.Equal(params, want) {
		t.Errorf("params = %v, want %v", params, want)
	}

	for _, s := range []string{"novalue", "=x"} {
		if err := (paramFlag{}).Set(s); err == nil {
			t.Errorf("Set(%q) = nil, want an error", s)
		}
	}
}

// TestParamFlag_SetRepeated checks that a parameter given twice is refused, naming it, and keeps its first value.
func TestParamFlag_SetRepeated(t *testing.T) {
	params := paramFlag{}
	if err := params.Set("a=1"); err != nil {
		t.Fatalf("Set(%q) = %v", "a=1", err)
	}

	err := params.Set("a=2")
	if want := `"a" given twice`; err == nil || err.Error() != want {
		t.Errorf("Set(%q) again = %v, want %q", "a=2", err, want)
	}
	if params["a"] != "1" {
		t.Errorf("params[%q] = %q, want the first value %q", "a", params["a"], "1")
	}
}
