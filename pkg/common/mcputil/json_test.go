package mcputil

import (
	"encoding/json"
	"testing"
)

// TestMarshalToolResult verifies that MarshalToolResult writes compact JSON with no trailing line break, keeps <, >
// and & as they are, and still decodes to the value it was given.
func TestMarshalToolResult(t *testing.T) {
	in := map[string]any{"name": "Notes <draft> & ideas", "count": 2}

	got, err := MarshalToolResult(in)
	if err != nil {
		t.Fatalf("MarshalToolResult: %v", err)
	}
	if want := `{"count":2,"name":"Notes <draft> & ideas"}`; string(got) != want {
		t.Errorf("MarshalToolResult = %s, want %s", got, want)
	}

	var back map[string]any
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatalf("decoding the result: %v", err)
	}
	if back["name"] != in["name"] {
		t.Errorf("decoded name = %v, want %v", back["name"], in["name"])
	}
}

// TestMarshalToolResult_UnsupportedValue verifies that a value JSON cannot represent is reported, not written
// partially.
func TestMarshalToolResult_UnsupportedValue(t *testing.T) {
	got, err := MarshalToolResult(map[string]any{"ch": make(chan int)})
	if err == nil {
		t.Fatalf("MarshalToolResult of a channel = %s, want an error", got)
	}
	if got != nil {
		t.Errorf("MarshalToolResult of a channel returned %s with its error, want nil", got)
	}
}
