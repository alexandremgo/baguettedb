package mcputil

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrNotToolCall reports an MCP request that is not a tool call (initialize, tools/list, notifications), so it
// carries no bdb.
var ErrNotToolCall = errors.New("not a tool call")

// reqWithBdbID is the part of an MCP JSON-RPC request that names its method and, for a tool call, its bdb.
type reqWithBdbID struct {
	Method string `json:"method"`
	Params struct {
		Arguments struct {
			BdbID string `json:"bdb_id"`
		} `json:"arguments"`
	} `json:"params"`
}

// BdbIDFromToolCallBody extracts the target bdb from a raw MCP JSON-RPC request body, before any MCP server parses
// it. The bdb rides in the tools/call arguments as bdb_id (the argument every bdb-scoped tool declares), not in the
// URL.
//
// Returns ErrNotToolCall for a request that legitimately carries no bdb (initialize, tools/list, notifications), and
// a wrapped error for a tool call whose bdb_id is missing or invalid, or for a malformed body.
func BdbIDFromToolCallBody(body []byte) (uuid.UUID, error) {
	req := reqWithBdbID{}
	err := json.Unmarshal(body, &req)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parsing tool call body: %w", err)
	}

	if req.Method != "tools/call" {
		return uuid.Nil, ErrNotToolCall
	}

	bdbID, err := uuid.Parse(req.Params.Arguments.BdbID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid bdb_id %q: %w", req.Params.Arguments.BdbID, err)
	}

	return bdbID, nil
}
