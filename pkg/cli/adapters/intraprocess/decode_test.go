package intraprocess

import (
	"strings"
	"testing"
)

// TestDecodeOne checks that decodeOne decodes one JSON value into a struct or a map, numbers as float64 in a map,
// JSON null as the zero value, and refuses a key the struct does not declare and anything after the value.
func TestDecodeOne(t *testing.T) {
	type spec struct {
		Name string `json:"name"`
	}

	s, err := decodeOne[spec]([]byte(`{"name": "open-tasks"}`))
	if err != nil || s.Name != "open-tasks" {
		t.Errorf("decodeOne[spec] = %+v, %v; want the name", s, err)
	}
	if _, err := decodeOne[spec]([]byte(`{"title": "open-tasks"}`)); err == nil ||
		!strings.Contains(err.Error(), `unknown field "title"`) {
		t.Errorf("an unknown key = %v, want it refused by name", err)
	}

	m, err := decodeOne[map[string]any]([]byte(`{"qty": 2, "tags": ["a"]}`))
	if err != nil || m["qty"] != float64(2) || len(m["tags"].([]any)) != 1 {
		t.Errorf("decodeOne[map] = %v, %v; want qty 2 as a float64 and one tag", m, err)
	}
	if m, err := decodeOne[map[string]any]([]byte(`null`)); err != nil || m != nil {
		t.Errorf("JSON null = %v, %v; want a nil map", m, err)
	}

	for _, data := range []string{`{"name": "a"} {"name": "b"}`, `{"name": "a"} 1`} {
		if _, err := decodeOne[spec]([]byte(data)); err == nil || !strings.Contains(err.Error(), "more than one") {
			t.Errorf("%s = %v, want it refused as more than one JSON value", data, err)
		}
	}
	if _, err := decodeOne[map[string]any]([]byte(`[1]`)); err == nil {
		t.Error("a JSON array into a map = nil error, want it refused")
	}
}
