package mcputil

import (
	"bytes"
	"encoding/json"
)

// MarshalToolResult returns v as the compact JSON text of a tool result. The characters <, > and & stay as they are,
// where encoding/json writes them as unicode escape sequences by default: a tool result is read as text, never
// embedded in HTML, and a reader copying a passage out of it must find the characters the value holds.
func MarshalToolResult(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	// Encode ends its output with a line break, which json.Marshal does not.
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
