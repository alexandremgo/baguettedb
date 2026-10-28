package mcputil

import (
	"errors"
	"testing"
)

// TestBdbIDFromToolCallBody pins the contract of the MCP body peek. A "tools/call" with a valid bdb_id
// yields that bdb and no error (route it). A method that simply isn't a routable tool call (initialize,
// tools/list, notifications) yields ErrNotToolCall — the normal, silent skip. Anything else (a tool call with a
// missing/invalid bdb_id, or a malformed body) yields a non-sentinel error — a skip worth a breadcrumb.
func TestBdbIDFromToolCallBody(t *testing.T) {
	const bdb = "11111111-1111-1111-1111-111111111111"

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"execute_query","arguments":{"sql":"SELECT 1","bdb_id":"` + bdb + `"}}}`)
	got, err := BdbIDFromToolCallBody(body)
	if err != nil || got.String() != bdb {
		t.Fatalf("BdbIDFromToolCallBody(valid tools/call) = (%v, %v), want (%s, nil)", got, err, bdb)
	}

	// Normal, silent skips: not a routable tool call → ErrNotToolCall.
	notToolCall := map[string]string{
		"initialize":   `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		"tools/list":   `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		"notification": `{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	}
	for name, raw := range notToolCall {
		if _, err := BdbIDFromToolCallBody([]byte(raw)); !errors.Is(err, ErrNotToolCall) {
			t.Errorf("BdbIDFromToolCallBody(%s) err = %v, want ErrNotToolCall", name, err)
		}
	}

	// Suspicious skips: a tool call (or a malformed body) with no usable bdb → a non-sentinel error.
	const callPrefix = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"execute_query","arguments":`
	suspicious := map[string]string{
		"tools/call no bdb":   callPrefix + `{"sql":"SELECT 1"}}}`,
		"tools/call bad uuid": callPrefix + `{"bdb_id":"not-a-uuid"}}}`,
		"malformed":           `{not json`,
		"empty":               ``,
	}
	for name, raw := range suspicious {
		_, err := BdbIDFromToolCallBody([]byte(raw))
		if err == nil || errors.Is(err, ErrNotToolCall) {
			t.Errorf("BdbIDFromToolCallBody(%s) err = %v, want a non-ErrNotToolCall error", name, err)
		}
	}
}
