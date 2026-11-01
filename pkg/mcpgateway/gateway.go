// Package mcpgateway aggregates the MCP servers of an instance's apps behind one MCP server: it lists each backend's
// tools once, at construction, registers them as its own and proxies every call to the backend serving the tool,
// knowing nothing of what the tools do. It sends clients the instance's orientation instructions, and reaches its
// backends in-process, or over Streamable HTTP with the shared internal API secret and the caller's token.
package mcpgateway

import (
	"cmp"
	"context"
	"fmt"
	"maps"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/common/mcputil"
)

// Gateway aggregates multiple MCP backend servers into a single MCP server.
// It discovers tools from each backend via ListTools and proxies CallTool
// requests to the correct backend. It has no knowledge of what the tools do
// and imports no domain types from other modules.
type Gateway struct {
	server   *mcpserver.MCPServer
	backends []*mcpclient.Client
	logger   zerolog.Logger
}

// Instructions is the orientation text every MCP session receives from the gateway before its first tool call:
// what a bdb is, which tool to start from, how skills work, and the response style agents are expected to keep.
// Tool names are spliced in from the mcputil constants, so a renamed tool cannot leave the text pointing at a
// name that no longer exists.
const Instructions = "BaguetteDB is a collaborative database platform. " +
	"A bdb — short for baguettedb — is a personal database a user owns and shares: it holds their tables, and they can invite other people into it as members. " +
	"Users can design and run custom workflows and applications entirely within a chat, " +
	"with all data persisted across sessions thanks to BaguetteDB. " +
	"Use " + mcputil.ToolListBdbs + " to find the user's bdbs and decide which one the task belongs to; " +
	"when none fits, " + mcputil.ToolCreateBdb + " creates a new one, blank or from a curated starter " +
	"(" + mcputil.ToolListStarters + ") — only on the user's explicit ask, with the name confirmed first. " +
	"BaguetteDB can also be written 🥖db or simply 🥖; 'bdb' on its own refers to one of these databases (e.g. 'my Sport bdb').\n\n" +
	"Each bdb can also have skills — short markdown instructions that teach agents how to work with the data in this bdb. " +
	"Skills can target one or more tables or be bdb-level (no targets). " +
	"Once you have picked a bdb, call " + mcputil.ToolDescribeBdb + "(bdb_id) before working in it — one call returns the tables with their column schemas, every skill with its full body inlined, and the bdb's formulas. " +
	"When a bdb is too large, parts of that response degrade to summaries and its `omitted` field says exactly what to call instead. " +
	mcputil.ToolDescribeTable + " re-reads one table (schema plus the bodies of skills targeting it); " + mcputil.ToolGetSkill + "(bdb_id, ref) re-reads a specific skill body. " +
	"When the user wants to capture a workflow, naming convention, or rule, offer to create a skill with " + mcputil.ToolCreateSkill + "; use " + mcputil.ToolUpdateSkill + " to refine it. " +
	"Deletion is via the web UI.\n\n" +
	"Response style: " +
	"Do not mention SQL queries, statements, or database concepts (SELECT, INSERT, JOIN, schema, etc.) unless the user explicitly asks for them or uses technical vocabulary themselves. " +
	"Table names and column names are fine to use — they are the user's own data model. " +
	"Talk about the user's data naturally (e.g. 'here are the items on your grocery list', 'added butter to groceries', 'yes, you have eggs'). " +
	"When presenting multiple results, display them in a clean table or spreadsheet-like format — structured data should still look structured, just without exposing the SQL behind it. " +
	"Match the user's tone: if they ask in plain English, respond conversationally; if they interact in a more structured or spreadsheet-oriented way, lean into tables and columns. " +
	"When something fails, give a short plain-language explanation and offer technical details as a follow-up only if the user wants them."

// Default server identity the gateway reports when Options leaves it empty.
const (
	DefaultServerName    = "baguettedb"
	DefaultServerVersion = "1.0.0"
)

// Options sets how the aggregated server presents itself to MCP clients.
type Options struct {
	// ServerName and ServerVersion are reported in the initialize response. Empty values fall back to
	// DefaultServerName and DefaultServerVersion.
	ServerName    string
	ServerVersion string

	// InstructionsPreamble is sent before Instructions, separated by a blank line. Empty sends Instructions alone.
	InstructionsPreamble string
}

// instructions returns the orientation text the aggregated server sends: Instructions, after the preamble if any.
func (o Options) instructions() string {
	if o.InstructionsPreamble == "" {
		return Instructions
	}
	return o.InstructionsPreamble + "\n\n" + Instructions
}

// NewGateway creates a gateway that aggregates tools from the given MCP clients. Each client must already be
// started (Start + Initialize called).
//
// opts are applied to the aggregated server after its instructions, so a caller can register middleware or hooks.
func NewGateway(
	backends []*mcpclient.Client, logger zerolog.Logger, gwOpts Options, opts ...mcpserver.ServerOption,
) (*Gateway, error) {
	serverOpts := make([]mcpserver.ServerOption, 0, 1+len(opts))
	serverOpts = append(serverOpts, mcpserver.WithInstructions(gwOpts.instructions()))
	serverOpts = append(serverOpts, opts...)
	s := mcpserver.NewMCPServer(
		cmp.Or(gwOpts.ServerName, DefaultServerName), cmp.Or(gwOpts.ServerVersion, DefaultServerVersion), serverOpts...,
	)

	gw := &Gateway{
		server:   s,
		backends: backends,
		logger:   logger,
	}

	if err := gw.discoverAndRegister(); err != nil {
		return nil, fmt.Errorf("discovering tools: %w", err)
	}

	return gw, nil
}

// MCPServer returns the aggregated MCP server.
func (gw *Gateway) MCPServer() *mcpserver.MCPServer {
	return gw.server
}

// discoverAndRegister calls ListTools on each backend and registers proxy handlers.
func (gw *Gateway) discoverAndRegister() error {
	ctx := context.Background()

	for i, backend := range gw.backends {
		result, err := backend.ListTools(ctx, mcp.ListToolsRequest{})
		if err != nil {
			return fmt.Errorf("listing tools from backend %d: %w", i, err)
		}

		for _, tool := range result.Tools {
			gw.logger.Info().Str("tool", tool.Name).Int("backend", i).Msg("registering proxied tool")
			gw.server.AddTool(tool, gw.proxyHandler(backend))
		}
	}

	return nil
}

// proxyHandler returns a tool handler that forwards the call to the given backend.
func (gw *Gateway) proxyHandler(backend *mcpclient.Client) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// The caller's connection metadata describes its connection to the gateway, not the gateway's to the backend,
		// which can run on another protocol version: a 2026-07-28 version over a connection on an earlier one fails.
		result, err := backend.CallTool(ctx, withoutConnectionMeta(request))
		if err != nil {
			gw.logger.Error().Err(err).Str("tool", request.Params.Name).Msg("proxied tool call failed")
			return mcp.NewToolResultError(fmt.Sprintf("tool call failed: %v", err)), nil
		}
		return result, nil
	}
}

// connectionMetaKeys are the _meta keys that describe the connection a request travels on rather than the request
// itself: the protocol version, and the client's identity, capabilities, and log level. Protocol version 2026-07-28
// defines them, carrying on each request what earlier versions settle once per connection through the initialize
// handshake and logging/setLevel.
var connectionMetaKeys = []string{
	mcp.MetaKeyProtocolVersion, mcp.MetaKeyClientInfo, mcp.MetaKeyClientCapabilities, mcp.MetaKeyLogLevel,
}

// withoutConnectionMeta returns request with connectionMetaKeys removed from its _meta, keeping every other field,
// such as a progress token. The _meta of the request passed in is left unchanged.
func withoutConnectionMeta(request mcp.CallToolRequest) mcp.CallToolRequest {
	if request.Params.Meta == nil {
		return request
	}
	meta := *request.Params.Meta
	meta.AdditionalFields = maps.Clone(meta.AdditionalFields)
	for _, key := range connectionMetaKeys {
		delete(meta.AdditionalFields, key)
	}
	request.Params.Meta = &meta
	return request
}
