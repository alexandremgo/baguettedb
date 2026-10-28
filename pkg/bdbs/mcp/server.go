// Package mcp serves the bdbs MCP surface: list_bdbs to pick the bdb a task belongs to, describe_bdb to orient in it,
// and update_bdb to rename it or rewrite its description. Tables, skills and formulas come from the databases app
// through a port set after the server is built; describe_bdb falls back to summaries past its size limits.
package mcp

import (
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/rs/zerolog"

	"github.com/alexandremgo/baguettedb/pkg/bdbs/service"
)

// defaultSkillBodyBudgetBytes is the fallback describe_bdb skill-body budget when Options carries no positive value.
// Keep in sync with the config default (16 KiB) so direct constructions behave like configured ones.
const defaultSkillBodyBudgetBytes = 16 * 1024

// defaultSchemaTableLimit is the fallback describe_bdb schema table limit when Options carries no positive value.
// Keep in sync with the config default (20) so direct constructions behave like configured ones.
const defaultSchemaTableLimit = 20

// Options carries the tunable limits of the bdbs MCP tools.
type Options struct {
	// SkillBodyBudgetBytes caps the total bytes of skill bodies inlined in one describe_bdb response. At or under
	// the cap every skill body (bdb-level and table-targeted) is inlined; over it, table-targeted bodies degrade
	// to summaries and the response's `omitted` note says so. Zero or negative selects the default (16 KiB).
	SkillBodyBudgetBytes int

	// SchemaTableLimit caps how many tables a bdb may have before describe_bdb stops inlining column-level
	// schemas. At or under the limit every table entry carries its columns and foreign keys; over it, all table
	// entries degrade to name+type and the response's `omitted` note says so. Zero or negative selects the
	// default (20).
	SchemaTableLimit int
}

// NewServer creates an MCP server with all bdbs tools registered.
// The dbRef parameter holds a lazily-injected reference to the databases module.
// The App field is populated after the databases module is set up.
func NewServer(
	bdbSvc *service.BdbService, dbRef *DatabasesAppRef, opts Options, logger zerolog.Logger,
) *mcpserver.MCPServer {
	if opts.SkillBodyBudgetBytes <= 0 {
		opts.SkillBodyBudgetBytes = defaultSkillBodyBudgetBytes
	}
	if opts.SchemaTableLimit <= 0 {
		opts.SchemaTableLimit = defaultSchemaTableLimit
	}

	s := mcpserver.NewMCPServer(
		"baguettedb-bdbs",
		"1.0.0",
	)

	registerBdbTools(s, bdbSvc, dbRef, opts, logger)
	registerUpdateBdbTool(s, bdbSvc, logger)

	return s
}
